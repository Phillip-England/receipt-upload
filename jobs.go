package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
)

func (s Settings) queueDir() string { return filepath.Join(s.DataDir, "queue") }

// A directory is published only after all parts have been received. The database
// transaction publishes the job and its receipt metadata together.
func (a *App) handleUpload(r *http.Request) error {
	reader, err := r.MultipartReader()
	if err != nil {
		return err
	}
	if err = os.MkdirAll(a.settings.queueDir(), 0700); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(a.settings.queueDir(), ".incoming-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	text := map[string][]string{}
	filenames := []string{}
	var received int64
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if part.FormName() == "files" && part.FileName() != "" {
			f, err := os.OpenFile(filepath.Join(staging, fmt.Sprintf("%06d", len(filenames))), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
			if err != nil {
				return err
			}
			n, copyErr := io.Copy(f, io.LimitReader(part, a.settings.MaxUploadBytes-received+1))
			syncErr := f.Sync()
			closeErr := f.Close()
			if copyErr != nil {
				return copyErr
			}
			if syncErr != nil {
				return syncErr
			}
			if closeErr != nil {
				return closeErr
			}
			received += n
			if received > a.settings.MaxUploadBytes {
				return errors.New("Upload is too large.")
			}
			if n > 0 {
				filenames = append(filenames, part.FileName())
			} else {
				os.Remove(f.Name())
			}
		} else {
			data, err := readLimitedPart(part, 65537)
			if err != nil {
				return err
			}
			if len(data) > 65536 {
				return errors.New("Form field is too large.")
			}
			received += int64(len(data))
			if received > a.settings.MaxUploadBytes {
				return errors.New("Upload is too large.")
			}
			text[part.FormName()] = append(text[part.FormName()], string(data))
		}
		part.Close()
	}
	if len(filenames) == 0 {
		return errors.New("Please choose at least one receipt image.")
	}
	cardholder, err := strconv.ParseInt(requiredText(text, "cardholder_id"), 10, 64)
	if err != nil {
		return errors.New("Please select a valid cardholder.")
	}
	total, vendor := strings.TrimSpace(requiredText(text, "total")), strings.TrimSpace(requiredText(text, "purchase_location"))
	if total == "" || vendor == "" {
		return errors.New("Total and place of purchase are required.")
	}
	var stores []int64
	seenStores := map[int64]bool{}
	for _, raw := range text["store_ids"] {
		if id, err := strconv.ParseInt(raw, 10, 64); err == nil && !seenStores[id] {
			stores = append(stores, id)
			seenStores[id] = true
		}
	}
	id := uuid.NewString()
	dir := filepath.Join(a.settings.queueDir(), id)
	if err = os.Rename(staging, dir); err != nil {
		return err
	}
	published := false
	defer func() {
		if !published {
			os.RemoveAll(dir)
		}
	}()
	db, err := openDB(a.settings.dbPath())
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	uploadID, err := insertReceipt(tx, cardholder, total, vendor, firstText(text, "description"), firstText(text, "notes"), stores, filenames, filepath.Join(a.settings.UploadDir, id+".pdf"), 0)
	if err != nil {
		return err
	}
	_, err = tx.Exec("INSERT INTO receipt_jobs(id,upload_id,status,created_at,updated_at) VALUES (?,?,'queued',?,?)", id, uploadID, nowISO(), nowISO())
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	published = true
	return nil
}

func runWorker(config string) error {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return fmt.Errorf("receipt worker requires FFmpeg on PATH: %w", err)
	}
	settings, err := loadSettings(config)
	if err != nil {
		return err
	}
	if err = initDB(settings.dbPath()); err != nil {
		return err
	}
	if err = os.MkdirAll(settings.queueDir(), 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(settings.queueDir(), "worker.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("another receipt worker is already running")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	// The exclusive lock proves a previous processing job no longer has a worker.
	if err = execSQL(settings.dbPath(), "UPDATE receipt_jobs SET status='queued', updated_at=? WHERE status='processing'", nowISO()); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Printf("Receipt worker watching %s\n", settings.queueDir())
	for {
		if ctx.Err() != nil {
			return nil
		}
		worked, err := processNextJob(settings)
		if err != nil {
			fmt.Fprintln(os.Stderr, "worker:", err)
		}
		if worked && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
		}
	}
}

func processNextJob(settings Settings) (bool, error) {
	db, err := openDB(settings.dbPath())
	if err != nil {
		return false, err
	}
	defer db.Close()
	var id, output string
	var uploadID int64
	err = db.QueryRow("UPDATE receipt_jobs SET status='processing',updated_at=? WHERE id=(SELECT id FROM receipt_jobs WHERE status='queued' ORDER BY created_at LIMIT 1) AND status='queued' RETURNING id,upload_id", nowISO()).Scan(&id, &uploadID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	conversionErr := func() error {
		if err := db.QueryRow("SELECT pdf_path FROM uploads WHERE id=?", uploadID).Scan(&output); err != nil {
			return err
		}
		dir := filepath.Join(settings.queueDir(), id)
		files, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		images := []PdfImage{}
		for _, file := range files {
			data, err := os.ReadFile(filepath.Join(dir, file.Name()))
			if err != nil {
				return err
			}
			img, err := preparePDFImage(data)
			if err != nil {
				return fmt.Errorf("Could not convert image %s: %w", file.Name(), err)
			}
			images = append(images, img)
		}
		if len(images) == 0 {
			return errors.New("No receipt images found")
		}
		if err := os.MkdirAll(filepath.Dir(output), 0755); err != nil {
			return err
		}
		temp := output + ".tmp"
		defer os.Remove(temp)
		if err := writePDF(images, temp); err != nil {
			return err
		}
		if err := os.Rename(temp, output); err != nil {
			return err
		}
		stat, err := os.Stat(output)
		if err != nil {
			return err
		}
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err = tx.Exec("UPDATE uploads SET pdf_size_bytes=? WHERE id=?", stat.Size(), uploadID); err != nil {
			return err
		}
		if _, err = tx.Exec("UPDATE receipt_jobs SET status='completed',error='',updated_at=? WHERE id=?", nowISO(), id); err != nil {
			return err
		}
		return tx.Commit()
	}()
	if conversionErr != nil {
		_, err = db.Exec("UPDATE receipt_jobs SET status='failed',error=?,updated_at=? WHERE id=?", conversionErr.Error(), nowISO(), id)
		return true, err
	}
	if err := os.RemoveAll(filepath.Join(settings.queueDir(), id)); err != nil {
		fmt.Fprintln(os.Stderr, "queue cleanup:", err)
	}
	return true, nil
}

func (a *App) jobsStatus(w http.ResponseWriter, r *http.Request) {
	if !a.isAdmin(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	db, err := openDB(a.settings.dbPath())
	if err != nil {
		serverError(w, err)
		return
	}
	defer db.Close()
	rows, err := db.Query("SELECT j.id,u.cardholder_name,u.purchase_location,j.status,j.error,j.created_at,j.updated_at FROM receipt_jobs j JOIN uploads u ON u.id=j.upload_id WHERE j.status!='completed' ORDER BY j.created_at")
	if err != nil {
		serverError(w, err)
		return
	}
	defer rows.Close()
	var b strings.Builder
	b.WriteString(`<div class="table-wrap"><table><thead><tr><th>Received</th><th>Cardholder</th><th>Purchased at</th><th>Status</th><th>Updated</th><th>Error / action</th></tr></thead><tbody>`)
	count := 0
	for rows.Next() {
		var id, name, vendor, status, problem, created, updated string
		if err = rows.Scan(&id, &name, &vendor, &status, &problem, &created, &updated); err != nil {
			serverError(w, err)
			return
		}
		count++
		action := ""
		if status == "failed" {
			action = fmt.Sprintf(`<form method="post" action="/admin/jobs/%s/retry"><button type="submit">Retry</button></form><form method="post" action="/admin/jobs/%s/clear"><button class="danger" type="submit">Clear</button></form>`, esc(id), esc(id))
		}
		fmt.Fprintf(&b, `<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s%s</td></tr>`, esc(created), esc(name), esc(vendor), esc(status), esc(updated), esc(problem), action)
	}
	if err = rows.Err(); err != nil {
		serverError(w, err)
		return
	}
	if count == 0 {
		b.WriteString(`<tr><td colspan="6">No active or failed jobs. Completed PDFs appear in Receipts; refresh the dashboard to see new PDFs.</td></tr>`)
	}
	b.WriteString(`</tbody></table></div>`)
	w.Header().Set("Cache-Control", "no-store")
	writeHTML(w, b.String())
}

func (a *App) retryJob(w http.ResponseWriter, r *http.Request) {
	if !a.isAdmin(r) {
		http.Error(w, "Unauthorized", 401)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	action := filepath.Base(r.URL.Path)
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/admin/jobs/"), "/"+action)
	if _, err := uuid.Parse(id); err != nil || (action != "retry" && action != "clear") {
		http.NotFound(w, r)
		return
	}
	if action == "clear" {
		if err := a.clearFailedJob(id); err != nil {
			serverError(w, err)
			return
		}
		http.Redirect(w, r, "/admin#jobs", http.StatusSeeOther)
		return
	}
	if err := execSQL(a.settings.dbPath(), "UPDATE receipt_jobs SET status='queued',error='',updated_at=? WHERE id=? AND status='failed'", nowISO(), id); err != nil {
		serverError(w, err)
		return
	}
	http.Redirect(w, r, "/admin#jobs", http.StatusSeeOther)
}

// clearFailedJob claims the failed row before cleanup so a concurrent retry
// cannot start processing files that are being removed.
func (a *App) clearFailedJob(id string) error {
	db, err := openDB(a.settings.dbPath())
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var uploadID int64
	err = tx.QueryRow("DELETE FROM receipt_jobs WHERE id=? AND status='failed' RETURNING upload_id", id).Scan(&uploadID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var output string
	if err = tx.QueryRow("SELECT pdf_path FROM uploads WHERE id=?", uploadID).Scan(&output); err != nil {
		return err
	}
	if err = os.RemoveAll(filepath.Join(a.settings.queueDir(), id)); err != nil {
		return err
	}
	for _, path := range []string{output, output + ".tmp"} {
		if err = os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if _, err = tx.Exec("DELETE FROM uploads WHERE id=?", uploadID); err != nil {
		return err
	}
	return tx.Commit()
}
