package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/image/bmp"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/webp"
	"golang.org/x/term"
)

//go:embed src/styles.css static/logo.png
var embedded embed.FS

const (
	sessionCookie     = "receipt-upload-session"
	banHours          = 24
	maxFailedLogins   = 3
	maxImageDimension = 1600
	jpegQuality       = 76
	pdfDPI            = 150.0
)

var configKeys = []string{
	"ADMIN_USERNAME",
	"ADMIN_PASSWORD",
	"SECRET_KEY",
	"UPLOAD_TOKEN",
	"APP_BASE_URL",
	"DATA_DIR",
	"MAX_UPLOAD_MB",
}

type Settings struct {
	AdminUsername  string
	AdminPassword  string
	SecretKey      string
	UploadToken    string
	AppBaseURL     string
	DataDir        string
	UploadDir      string
	MaxUploadBytes int64
}

type App struct {
	settings   Settings
	uploadTok  string
	appBaseURL string
}

type NamedRow struct {
	ID   int64
	Name string
}

type UploadRow struct {
	ID               int64
	CardholderName   string
	Total            string
	PurchaseLocation string
	Description      string
	Notes            string
	StoreNames       string
	PDFSize          string
	ArchivedAt       sql.NullString
	CreatedAt        string
}

type AdminView struct {
	Cardholders []NamedRow
	Stores      []NamedRow
	Uploads     []UploadRow
	DiskUsage   string
}

type UploadOptions struct {
	Cardholders []NamedRow
	Stores      []NamedRow
}

type PdfImage struct {
	Width  int
	Height int
	JPEG   []byte
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	config := "./config/.env"
	args = normalizeArgs(args)
	args, config = consumeConfig(args, config)
	if len(args) == 0 {
		args = []string{"serve"}
	}
	switch args[0] {
	case "init":
		dataDir := "./data"
		force := false
		rest := args[1:]
		rest, config = consumeConfig(rest, config)
		for i := 0; i < len(rest); i++ {
			switch rest[i] {
			case "--data-dir":
				i++
				if i >= len(rest) {
					return errors.New("--data-dir requires a path")
				}
				dataDir = rest[i]
			case "--force":
				force = true
			default:
				return fmt.Errorf("unknown argument: %s", rest[i])
			}
		}
		return initApp(config, dataDir, force)
	case "config":
		if len(args) < 2 {
			return errors.New("config requires init or check")
		}
		rest, cfg := consumeConfig(args[2:], config)
		config = cfg
		switch args[1] {
		case "init":
			path := "./config/.env"
			force := false
			for i := 0; i < len(rest); i++ {
				switch rest[i] {
				case "--path":
					i++
					if i >= len(rest) {
						return errors.New("--path requires a path")
					}
					path = rest[i]
				case "--force":
					force = true
				default:
					return fmt.Errorf("unknown argument: %s", rest[i])
				}
			}
			return initConfigFile(path, force)
		case "check":
			return checkConfigFile(config)
		}
	case "database":
		if len(args) < 2 || args[1] != "init" {
			return errors.New("database requires init")
		}
		dataDir := "./data"
		rest, _ := consumeConfig(args[2:], config)
		for i := 0; i < len(rest); i++ {
			if rest[i] != "--data-dir" || i+1 >= len(rest) {
				return errors.New("usage: receipt-upload database init [--data-dir PATH]")
			}
			i++
			dataDir = rest[i]
		}
		return initDataDir(dataDir)
	case "serve":
		host := "0.0.0.0"
		port := "8725"
		rest, cfg := consumeConfig(args[1:], config)
		config = cfg
		for i := 0; i < len(rest); i++ {
			switch rest[i] {
			case "--host":
				i++
				if i >= len(rest) {
					return errors.New("--host requires a value")
				}
				host = rest[i]
			case "--port":
				i++
				if i >= len(rest) {
					return errors.New("--port requires a value")
				}
				port = rest[i]
			default:
				return fmt.Errorf("unknown argument: %s", rest[i])
			}
		}
		return serve(config, host, port)
	case "generate-secret-key":
		length, raw, err := parseGenerateArgs(args[1:], 48)
		if err != nil {
			return err
		}
		return printGenerated("SECRET_KEY", length, raw)
	case "generate-upload-token":
		length, raw, err := parseGenerateArgs(args[1:], 32)
		if err != nil {
			return err
		}
		return printGenerated("UPLOAD_TOKEN", length, raw)
	case "set-username":
		rest, cfg := consumeConfig(args[1:], config)
		config = cfg
		if len(rest) != 1 {
			return errors.New("set-username requires a username")
		}
		return saveConfigAndPrint(config, "ADMIN_USERNAME", rest[0])
	case "set-password":
		rest, cfg := consumeConfig(args[1:], config)
		config = cfg
		var password string
		if len(rest) == 0 {
			var err error
			password, err = promptPassword()
			if err != nil {
				return err
			}
		} else if len(rest) == 1 {
			password = rest[0]
		} else {
			return errors.New("set-password accepts zero or one password argument")
		}
		return saveConfigAndPrint(config, "ADMIN_PASSWORD", password)
	case "set-config":
		rest, cfg := consumeConfig(args[1:], config)
		config = cfg
		if len(rest) != 2 {
			return errors.New("set-config requires a name and value")
		}
		return saveConfigAndPrint(config, rest[0], rest[1])
	case "list-banned-ips":
		rest, cfg := consumeConfig(args[1:], config)
		config = cfg
		showAll := false
		for _, arg := range rest {
			if arg != "--all" {
				return fmt.Errorf("unknown argument: %s", arg)
			}
			showAll = true
		}
		return listBannedIPs(config, showAll)
	case "unban-ip":
		rest, cfg := consumeConfig(args[1:], config)
		config = cfg
		if len(rest) != 1 {
			return errors.New("unban-ip requires an ID")
		}
		id, err := strconv.ParseInt(rest[0], 10, 64)
		if err != nil {
			return errors.New("unban-ip requires a numeric ID")
		}
		return unbanIP(config, id)
	}
	return fmt.Errorf("unknown command: %s", args[0])
}

func normalizeArgs(args []string) []string {
	commands := map[string]bool{"init": true, "config": true, "database": true, "serve": true, "generate-secret-key": true, "generate-upload-token": true, "set-username": true, "set-password": true, "set-config": true, "list-banned-ips": true, "unban-ip": true}
	hasCommand := false
	for _, arg := range args {
		if commands[arg] {
			hasCommand = true
			break
		}
	}
	if len(args) > 0 && strings.HasPrefix(args[0], "--") && !hasCommand && args[0] != "--help" {
		out := append([]string{"serve"}, args...)
		return out
	}
	return args
}

func consumeConfig(args []string, current string) ([]string, string) {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "--config" && i+1 < len(args) {
			current = args[i+1]
			i++
			continue
		}
		out = append(out, args[i])
	}
	return out, current
}

func parseGenerateArgs(args []string, defaultLength int) (int, bool, error) {
	length := defaultLength
	raw := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--length":
			i++
			if i >= len(args) {
				return 0, false, errors.New("--length requires a number")
			}
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 {
				return 0, false, errors.New("Length must be greater than zero.")
			}
			length = n
		case "--raw":
			raw = true
		default:
			return 0, false, fmt.Errorf("unknown argument: %s", args[i])
		}
	}
	return length, raw, nil
}

func serve(configPath, host, port string) error {
	settings, err := loadSettings(configPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(settings.UploadDir, 0755); err != nil {
		return err
	}
	if err := initDB(settings.dbPath()); err != nil {
		return err
	}
	uploadToken, err := loadAppSetting(settings, "UPLOAD_TOKEN", settings.UploadToken)
	if err != nil {
		return err
	}
	appBaseURL, err := loadAppSetting(settings, "APP_BASE_URL", settings.AppBaseURL)
	if err != nil {
		return err
	}
	app := &App{settings: settings, uploadTok: uploadToken, appBaseURL: appBaseURL}
	mux := http.NewServeMux()
	mux.HandleFunc("/", app.root)
	mux.HandleFunc("/static/styles.css", styles)
	mux.HandleFunc("/static/logo.png", logo)
	mux.HandleFunc("/admin/login", app.login)
	mux.HandleFunc("/admin/logout", app.logout)
	mux.HandleFunc("/admin", app.adminDashboard)
	mux.HandleFunc("/admin/upload-link", app.updateUploadLink)
	mux.HandleFunc("/admin/cardholders", app.addCardholder)
	mux.HandleFunc("/admin/cardholders/", app.cardholderAction)
	mux.HandleFunc("/admin/stores", app.addStore)
	mux.HandleFunc("/admin/stores/", app.storeAction)
	mux.HandleFunc("/admin/uploads/", app.uploadAction)
	mux.HandleFunc("/upload/", app.upload)
	addr := net.JoinHostPort(host, port)
	server := &http.Server{
		Addr:              addr,
		Handler:           http.MaxBytesHandler(mux, settings.MaxUploadBytes+1024*1024),
		ReadHeaderTimeout: 10 * time.Second,
	}
	fmt.Printf("loaded configuration from %s\n", configPath)
	fmt.Printf("receipt-upload listening on http://%s\n", addr)
	return server.ListenAndServe()
}

func (s Settings) dbPath() string {
	return filepath.Join(s.DataDir, "app.sqlite3")
}

func (s Settings) usesDefaultAdminCredentials() bool {
	return s.AdminUsername == "admin" || s.AdminPassword == "password"
}

func uploadURL(base, token string) string {
	return strings.TrimRight(base, "/") + "/upload/" + token
}

func (a *App) root(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeHTML(w, renderDocs())
}

func styles(w http.ResponseWriter, r *http.Request) {
	css, _ := embedded.ReadFile("src/styles.css")
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Write(css)
}

func logo(w http.ResponseWriter, r *http.Request) {
	data, _ := embedded.ReadFile("static/logo.png")
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(data)
}

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		writeHTML(w, renderLogin(a.settings, ""))
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ip := clientIP(r)
	db, err := openDB(a.settings.dbPath())
	if err != nil {
		serverError(w, err)
		return
	}
	defer db.Close()
	if err := purgeOldLoginAttempts(db); err != nil {
		serverError(w, err)
		return
	}
	banned, err := isIPBanned(db, ip)
	if err != nil {
		serverError(w, err)
		return
	}
	if banned {
		w.WriteHeader(http.StatusTooManyRequests)
		writeHTML(w, renderLogin(a.settings, "Too many failed attempts. Try again later."))
		return
	}
	valid := ctEq(r.FormValue("username"), a.settings.AdminUsername) && ctEq(r.FormValue("password"), a.settings.AdminPassword)
	if !valid {
		_ = recordFailedLogin(db, ip)
		w.WriteHeader(http.StatusUnauthorized)
		writeHTML(w, renderLogin(a.settings, "Invalid username or password."))
		return
	}
	_ = clearLoginAttempts(db, ip)
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: signSession(a.settings.SecretKey), Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

func (a *App) adminDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/admin" {
		http.NotFound(w, r)
		return
	}
	if !a.isAdmin(r) {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}
	view, err := loadAdminView(a.settings)
	if err != nil {
		serverError(w, err)
		return
	}
	writeHTML(w, renderAdmin(a.settings, a.uploadTok, a.appBaseURL, view, ""))
}

func (a *App) updateUploadLink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.isAdmin(r) {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	secretCode := strings.TrimSpace(r.FormValue("secret_code"))
	appBaseURL := strings.TrimRight(strings.TrimSpace(r.FormValue("app_base_url")), "/")
	if err := validateUploadToken(secretCode); err != nil {
		a.renderAdminError(w, err.Error())
		return
	}
	if err := validateAppBaseURL(appBaseURL); err != nil {
		a.renderAdminError(w, err.Error())
		return
	}
	if err := saveUploadLink(a.settings, secretCode, appBaseURL); err != nil {
		serverError(w, err)
		return
	}
	a.uploadTok = secretCode
	a.appBaseURL = appBaseURL
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (a *App) renderAdminError(w http.ResponseWriter, message string) {
	view, err := loadAdminView(a.settings)
	if err != nil {
		serverError(w, err)
		return
	}
	w.WriteHeader(http.StatusBadRequest)
	writeHTML(w, renderAdmin(a.settings, a.uploadTok, a.appBaseURL, view, message))
}

func (a *App) addCardholder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.isAdmin(r) {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}
	_ = r.ParseForm()
	name := strings.TrimSpace(r.FormValue("name"))
	if name != "" {
		_ = execSQL(a.settings.dbPath(), "INSERT OR IGNORE INTO cardholders (name) VALUES (?)", name)
	}
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (a *App) cardholderAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.isAdmin(r) {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}
	id, ok := idFromPath(r.URL.Path, "/admin/cardholders/", "/delete")
	if ok {
		_ = execSQL(a.settings.dbPath(), "DELETE FROM cardholders WHERE id = ?", id)
	}
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (a *App) addStore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.isAdmin(r) {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}
	_ = r.ParseForm()
	name := strings.TrimSpace(r.FormValue("name"))
	if name != "" {
		_ = execSQL(a.settings.dbPath(), "INSERT OR IGNORE INTO stores (name) VALUES (?)", name)
	}
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (a *App) storeAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.isAdmin(r) {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}
	id, ok := idFromPath(r.URL.Path, "/admin/stores/", "/delete")
	if ok {
		_ = execSQL(a.settings.dbPath(), "DELETE FROM stores WHERE id = ?", id)
	}
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (a *App) uploadAction(w http.ResponseWriter, r *http.Request) {
	if !a.isAdmin(r) {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}
	path := r.URL.Path
	if strings.HasSuffix(path, "/download") && r.Method == http.MethodGet {
		a.downloadUpload(w, r)
		return
	}
	if strings.HasSuffix(path, "/archive") && r.Method == http.MethodPost {
		id, ok := idFromPath(path, "/admin/uploads/", "/archive")
		if ok {
			_ = execSQL(a.settings.dbPath(), "UPDATE uploads SET archived_at = ? WHERE id = ?", nowISO(), id)
		}
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if strings.HasSuffix(path, "/delete") && r.Method == http.MethodPost {
		id, ok := idFromPath(path, "/admin/uploads/", "/delete")
		if ok {
			a.deleteUpload(id)
		}
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	http.NotFound(w, r)
}

func (a *App) downloadUpload(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromPath(r.URL.Path, "/admin/uploads/", "/download")
	if !ok {
		http.NotFound(w, r)
		return
	}
	db, err := openDB(a.settings.dbPath())
	if err != nil {
		serverError(w, err)
		return
	}
	defer db.Close()
	var cardholder, pdfPath string
	if err := db.QueryRow("SELECT cardholder_name, pdf_path FROM uploads WHERE id = ?", id).Scan(&cardholder, &pdfPath); err != nil {
		http.NotFound(w, r)
		return
	}
	file, err := os.Open(pdfPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	filename := fmt.Sprintf("receipt-%d-%s.pdf", id, strings.NewReplacer("/", "-", "\\", "-", "\"", "").Replace(cardholder))
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	io.Copy(w, file)
}

func (a *App) deleteUpload(id int64) {
	db, err := openDB(a.settings.dbPath())
	if err != nil {
		return
	}
	defer db.Close()
	var pdfPath string
	if err := db.QueryRow("SELECT pdf_path FROM uploads WHERE id = ?", id).Scan(&pdfPath); err == nil {
		_ = os.Remove(pdfPath)
		_, _ = db.Exec("DELETE FROM uploads WHERE id = ?", id)
	}
}

func (a *App) upload(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.URL.Path, "/upload/")
	if token == "" || !ctEq(token, a.uploadTok) {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		opts, err := loadUploadOptions(a.settings)
		if err != nil {
			serverError(w, err)
			return
		}
		writeHTML(w, renderUpload(token, opts, "", ""))
	case http.MethodPost:
		err := a.handleUpload(r)
		opts, loadErr := loadUploadOptions(a.settings)
		if loadErr != nil {
			serverError(w, loadErr)
			return
		}
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			writeHTML(w, renderUpload(token, opts, err.Error(), ""))
			return
		}
		writeHTML(w, renderUpload(token, opts, "", "Receipt uploaded."))
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleUpload(r *http.Request) error {
	reader, err := r.MultipartReader()
	if err != nil {
		return err
	}
	text := map[string][]string{}
	images := []PdfImage{}
	filenames := []string{}
	var totalBytes int64
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name := part.FormName()
		if name == "files" {
			filename := part.FileName()
			if filename == "" {
				filename = "receipt"
			}
			data, err := readLimitedPart(part, a.settings.MaxUploadBytes-totalBytes+1)
			if err != nil {
				return err
			}
			if len(data) == 0 {
				continue
			}
			totalBytes += int64(len(data))
			if totalBytes > a.settings.MaxUploadBytes {
				return errors.New("Upload is too large.")
			}
			img, err := preparePDFImage(data)
			if err != nil {
				return fmt.Errorf("Could not read %s as a receipt image", filename)
			}
			images = append(images, img)
			filenames = append(filenames, filename)
		} else {
			data, _ := io.ReadAll(part)
			text[name] = append(text[name], string(data))
		}
	}
	if len(images) == 0 {
		return errors.New("Please choose at least one receipt image.")
	}
	cardholderID, err := strconv.ParseInt(requiredText(text, "cardholder_id"), 10, 64)
	if err != nil {
		return errors.New("Please select a valid cardholder.")
	}
	total := strings.TrimSpace(requiredText(text, "total"))
	purchaseLocation := strings.TrimSpace(requiredText(text, "purchase_location"))
	if total == "" || purchaseLocation == "" {
		return errors.New("Total and place of purchase are required.")
	}
	description := firstText(text, "description")
	notes := firstText(text, "notes")
	storeIDs := []int64{}
	for _, raw := range text["store_ids"] {
		if id, err := strconv.ParseInt(raw, 10, 64); err == nil {
			storeIDs = append(storeIDs, id)
		}
	}
	if err := os.MkdirAll(a.settings.UploadDir, 0755); err != nil {
		return err
	}
	outputPath := filepath.Join(a.settings.UploadDir, uuid.NewString()+".pdf")
	if err := writePDF(images, outputPath); err != nil {
		return err
	}
	stat, err := os.Stat(outputPath)
	if err != nil {
		return err
	}
	return saveReceipt(a.settings, cardholderID, total, purchaseLocation, description, notes, storeIDs, filenames, outputPath, stat.Size())
}

func readLimitedPart(part *multipart.Part, limit int64) ([]byte, error) {
	if limit < 1 {
		return nil, errors.New("Upload is too large.")
	}
	return io.ReadAll(io.LimitReader(part, limit))
}

func requiredText(text map[string][]string, key string) string {
	if values := text[key]; len(values) > 0 {
		return strings.TrimSpace(values[0])
	}
	return ""
}

func firstText(text map[string][]string, key string) string {
	if values := text[key]; len(values) > 0 {
		return strings.TrimSpace(values[0])
	}
	return ""
}

func preparePDFImage(data []byte) (PdfImage, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return PdfImage{}, err
	}
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	scale := math.Min(1, float64(maxImageDimension)/float64(max(width, height)))
	newW := max(1, int(math.Round(float64(width)*scale)))
	newH := max(1, int(math.Round(float64(height)*scale)))
	dst := image.NewRGBA(image.Rect(0, 0, newW, newH))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, bounds, draw.Over, nil)
	rgb := image.NewRGBA(dst.Bounds())
	draw.Draw(rgb, rgb.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)
	draw.Draw(rgb, rgb.Bounds(), dst, image.Point{}, draw.Over)
	var out bytes.Buffer
	if err := jpeg.Encode(&out, rgb, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return PdfImage{}, err
	}
	return PdfImage{Width: newW, Height: newH, JPEG: out.Bytes()}, nil
}

func init() {
	image.RegisterFormat("jpeg", "\xff\xd8", jpeg.Decode, jpeg.DecodeConfig)
	image.RegisterFormat("png", "\x89PNG\r\n\x1a\n", png.Decode, png.DecodeConfig)
	image.RegisterFormat("gif", "GIF8?a", gif.Decode, gif.DecodeConfig)
	image.RegisterFormat("bmp", "BM", bmp.Decode, bmp.DecodeConfig)
	image.RegisterFormat("webp", "RIFF????WEBPVP8", webp.Decode, webp.DecodeConfig)
}

func writePDF(images []PdfImage, outputPath string) error {
	objects := [][]byte{nil, nil}
	pageIDs := []int{}
	for i, img := range images {
		pageID := 3 + i*3
		contentID := pageID + 1
		imageID := pageID + 2
		pageIDs = append(pageIDs, pageID)
		widthPt := float64(img.Width) * 72 / pdfDPI
		heightPt := float64(img.Height) * 72 / pdfDPI
		objects = append(objects, []byte(fmt.Sprintf("<< /Type /Page /Parent 1 0 R /MediaBox [0 0 %.2f %.2f] /Resources << /XObject << /Im%d %d 0 R >> >> /Contents %d 0 R >>", widthPt, heightPt, i, imageID, contentID)))
		content := fmt.Sprintf("q\n%.2f 0 0 %.2f 0 0 cm\n/Im%d Do\nQ\n", widthPt, heightPt, i)
		objects = append(objects, []byte(fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content)))
		obj := []byte(fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /DCTDecode /Length %d >>\nstream\n", img.Width, img.Height, len(img.JPEG)))
		obj = append(obj, img.JPEG...)
		obj = append(obj, []byte("\nendstream")...)
		objects = append(objects, obj)
	}
	kids := []string{}
	for _, id := range pageIDs {
		kids = append(kids, fmt.Sprintf("%d 0 R", id))
	}
	objects[0] = []byte(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(kids)))
	objects[1] = []byte("<< /Type /Catalog /Pages 1 0 R >>")
	pdf := []byte("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	offsets := []int{}
	for i, obj := range objects {
		offsets = append(offsets, len(pdf))
		pdf = append(pdf, []byte(fmt.Sprintf("%d 0 obj\n", i+1))...)
		pdf = append(pdf, obj...)
		pdf = append(pdf, []byte("\nendobj\n")...)
	}
	xrefAt := len(pdf)
	pdf = append(pdf, []byte(fmt.Sprintf("xref\n0 %d\n0000000000 65535 f \n", len(objects)+1))...)
	for _, offset := range offsets {
		pdf = append(pdf, []byte(fmt.Sprintf("%010d 00000 n \n", offset))...)
	}
	pdf = append(pdf, []byte(fmt.Sprintf("trailer\n<< /Size %d /Root 2 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xrefAt))...)
	return os.WriteFile(outputPath, pdf, 0644)
}

func saveReceipt(settings Settings, cardholderID int64, total, purchaseLocation, description, notes string, storeIDs []int64, filenames []string, pdfPath string, pdfSize int64) error {
	db, err := openDB(settings.dbPath())
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var cardholderName string
	if err := tx.QueryRow("SELECT name FROM cardholders WHERE id = ?", cardholderID).Scan(&cardholderName); err != nil {
		return errors.New("Please select a valid cardholder.")
	}
	storePairs := []NamedRow{}
	for _, storeID := range storeIDs {
		var name string
		if err := tx.QueryRow("SELECT name FROM stores WHERE id = ?", storeID).Scan(&name); err == nil {
			storePairs = append(storePairs, NamedRow{ID: storeID, Name: name})
		}
	}
	storeNames := []string{}
	for _, store := range storePairs {
		storeNames = append(storeNames, store.Name)
	}
	res, err := tx.Exec("INSERT INTO uploads (cardholder_id, cardholder_name, total, purchase_location, description, notes, store_names, original_filenames, pdf_path, pdf_size_bytes, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", cardholderID, cardholderName, total, purchaseLocation, description, notes, strings.Join(storeNames, ", "), strings.Join(filenames, ", "), pdfPath, pdfSize, nowISO())
	if err != nil {
		return err
	}
	uploadID, _ := res.LastInsertId()
	for _, store := range storePairs {
		if _, err := tx.Exec("INSERT INTO receipt_stores (upload_id, store_id, store_name) VALUES (?, ?, ?)", uploadID, store.ID, store.Name); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func openDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", path+"?_busy_timeout=5000&_foreign_keys=on")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec("PRAGMA foreign_keys = ON; PRAGMA busy_timeout = 5000;"); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func initDB(dbPath string) error {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return err
	}
	db, err := openDB(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	schema := `
PRAGMA journal_mode = WAL;
CREATE TABLE IF NOT EXISTS cardholders (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE IF NOT EXISTS stores (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE IF NOT EXISTS uploads (id INTEGER PRIMARY KEY AUTOINCREMENT, cardholder_id INTEGER, cardholder_name TEXT NOT NULL, total TEXT NOT NULL, purchase_location TEXT NOT NULL, description TEXT, notes TEXT, store_names TEXT, original_filenames TEXT NOT NULL, pdf_path TEXT NOT NULL, pdf_size_bytes INTEGER NOT NULL, archived_at TEXT, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, FOREIGN KEY(cardholder_id) REFERENCES cardholders(id) ON DELETE SET NULL);
CREATE TABLE IF NOT EXISTS receipt_stores (upload_id INTEGER NOT NULL, store_id INTEGER, store_name TEXT NOT NULL, PRIMARY KEY(upload_id, store_name), FOREIGN KEY(upload_id) REFERENCES uploads(id) ON DELETE CASCADE, FOREIGN KEY(store_id) REFERENCES stores(id) ON DELETE SET NULL);
CREATE TABLE IF NOT EXISTS login_attempts (id INTEGER PRIMARY KEY AUTOINCREMENT, ip_address TEXT NOT NULL UNIQUE, failed_count INTEGER NOT NULL, last_attempt_at TEXT NOT NULL, banned_until TEXT);
CREATE TABLE IF NOT EXISTS app_settings (name TEXT PRIMARY KEY, value TEXT NOT NULL);`
	if _, err := db.Exec(schema); err != nil {
		return err
	}
	for _, col := range []struct{ name, typ string }{{"description", "TEXT"}, {"notes", "TEXT"}, {"store_names", "TEXT"}} {
		if err := addColumnIfMissing(db, "uploads", col.name, col.typ); err != nil {
			return err
		}
	}
	return migrateLoginAttemptIDs(db)
}

func addColumnIfMissing(db *sql.DB, table, column, typ string) error {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	_, err = db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, typ))
	return err
}

func migrateLoginAttemptIDs(db *sql.DB) error {
	rows, err := db.Query("PRAGMA table_info(login_attempts)")
	if err != nil {
		return err
	}
	defer rows.Close()
	hasID := false
	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return err
		}
		if name == "id" {
			hasID = true
		}
	}
	if hasID {
		return nil
	}
	_, err = db.Exec(`ALTER TABLE login_attempts RENAME TO login_attempts_old;
CREATE TABLE login_attempts (id INTEGER PRIMARY KEY AUTOINCREMENT, ip_address TEXT NOT NULL UNIQUE, failed_count INTEGER NOT NULL, last_attempt_at TEXT NOT NULL, banned_until TEXT);
INSERT INTO login_attempts (ip_address, failed_count, last_attempt_at, banned_until) SELECT ip_address, failed_count, last_attempt_at, banned_until FROM login_attempts_old;
DROP TABLE login_attempts_old;`)
	return err
}

func loadAdminView(settings Settings) (AdminView, error) {
	db, err := openDB(settings.dbPath())
	if err != nil {
		return AdminView{}, err
	}
	defer db.Close()
	cardholders, err := namedRows(db, "SELECT id, name FROM cardholders ORDER BY name COLLATE NOCASE")
	if err != nil {
		return AdminView{}, err
	}
	stores, err := namedRows(db, "SELECT id, name FROM stores ORDER BY name COLLATE NOCASE")
	if err != nil {
		return AdminView{}, err
	}
	uploads, err := uploadRows(db)
	if err != nil {
		return AdminView{}, err
	}
	return AdminView{Cardholders: cardholders, Stores: stores, Uploads: uploads, DiskUsage: humanBytes(directorySize(settings.DataDir))}, nil
}

func loadUploadOptions(settings Settings) (UploadOptions, error) {
	db, err := openDB(settings.dbPath())
	if err != nil {
		return UploadOptions{}, err
	}
	defer db.Close()
	cardholders, err := namedRows(db, "SELECT id, name FROM cardholders ORDER BY name COLLATE NOCASE")
	if err != nil {
		return UploadOptions{}, err
	}
	stores, err := namedRows(db, "SELECT id, name FROM stores ORDER BY name COLLATE NOCASE")
	if err != nil {
		return UploadOptions{}, err
	}
	return UploadOptions{Cardholders: cardholders, Stores: stores}, nil
}

func namedRows(db *sql.DB, query string) ([]NamedRow, error) {
	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NamedRow
	for rows.Next() {
		var row NamedRow
		if err := rows.Scan(&row.ID, &row.Name); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func uploadRows(db *sql.DB) ([]UploadRow, error) {
	rows, err := db.Query("SELECT id, cardholder_name, total, purchase_location, COALESCE(description, ''), COALESCE(notes, ''), COALESCE(store_names, ''), pdf_size_bytes, archived_at, created_at FROM uploads ORDER BY archived_at IS NOT NULL, created_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UploadRow
	for rows.Next() {
		var row UploadRow
		var size int64
		if err := rows.Scan(&row.ID, &row.CardholderName, &row.Total, &row.PurchaseLocation, &row.Description, &row.Notes, &row.StoreNames, &size, &row.ArchivedAt, &row.CreatedAt); err != nil {
			return nil, err
		}
		row.PDFSize = humanBytes(uint64(size))
		out = append(out, row)
	}
	return out, rows.Err()
}

func renderLogin(settings Settings, errMsg string) string {
	return layout("Admin login", fmt.Sprintf(`<main class="auth-shell"><section class="auth-panel"><a class="brand auth-brand" href="/"><img src="/static/logo.png" alt=""><span>receipt-upload</span></a><div class="auth-heading"><p class="eyebrow">Administrator portal</p><h1>Sign in to manage receipts</h1><p>Cardholders do not need an account. This portal is only for the application administrator.</p></div>%s%s<form method="post" action="/admin/login" class="stack"><label>Username <input name="username" autocomplete="username" required autofocus></label><label>Password <input name="password" type="password" autocomplete="current-password" required></label><button type="submit">Admin sign in</button></form><a class="back-link" href="/">← Back to documentation</a></section></main>`, defaultWarning(settings), message("alert", errMsg)))
}

func renderDocs() string {
	body := `<header class="docs-nav"><a class="brand" href="/"><img src="/static/logo.png" alt=""><span>receipt-upload</span></a><nav aria-label="Primary"><a href="#quick-start">Quick start</a><a href="#workflow">Workflow</a><a class="repo-link" href="https://github.com/phillip-england/receipt-upload">GitHub</a></nav></header>
<main class="docs-shell"><section class="docs-hero"><div><p class="eyebrow">Self-hosted receipt collection</p><h1>Receipt uploads without user accounts or heavyweight infrastructure.</h1><p class="hero-copy">A small Go application for collecting phone-camera receipts through a private link, combining images into compact PDFs, and giving one administrator a clean review queue.</p><div class="hero-actions"><a class="button" href="#quick-start">Get started</a></div></div><div class="hero-mark"><img src="/static/logo.png" alt="Receipt with an upload arrow"><span>Single binary · SQLite · Local storage</span></div></section>
<section class="docs-section" id="quick-start"><div class="section-intro"><p class="eyebrow">Quick start</p><h2>Run it locally in a few commands</h2><p>Build the binary, initialize its config and data directory, replace the default password, then start the server.</p></div><div class="code-panel"><div class="code-title"><span>Terminal</span><span>bash</span></div><pre><code>make install
receipt-upload init
# Edit ADMIN_PASSWORD in ./config/.env
receipt-upload config check
receipt-upload serve</code></pre></div><div class="callout"><strong>Default address</strong><span>Documentation is available at <code>http://localhost:8725/</code>. Administration lives at <code>/admin/login</code>.</span></div></section>
<section class="docs-section" id="workflow"><div class="section-intro"><p class="eyebrow">How it works</p><h2>One administrator, simple links for everyone else</h2></div><div class="step-grid"><article><span class="step-number">01</span><h3>Configure</h3><p>The admin creates cardholders and stores, then chooses the public hostname and secret upload code.</p></article><article><span class="step-number">02</span><h3>Share</h3><p>Send the generated private upload URL to cardholders. They do not create accounts or sign in.</p></article><article><span class="step-number">03</span><h3>Collect</h3><p>Cardholders select their name, add expense details, and attach one or more receipt photos.</p></article><article><span class="step-number">04</span><h3>Review</h3><p>The app resizes images, builds one PDF per expense, and makes it available in the admin portal.</p></article></div></section>
<section class="docs-section"><div class="section-intro"><p class="eyebrow">Configuration</p><h2>Environment-style config file</h2><p>The application reads the file passed with <code>--config</code> (default: <code>./config/.env</code>). Runtime environment variables are not merged in.</p></div><div class="config-list"><div><code>ADMIN_USERNAME</code><span>Administrator login name</span></div><div><code>ADMIN_PASSWORD</code><span>Administrator password</span></div><div><code>SECRET_KEY</code><span>Signs admin session cookies</span></div><div><code>UPLOAD_TOKEN</code><span>Initial private upload-link token</span></div><div><code>APP_BASE_URL</code><span>Public origin used when building links</span></div><div><code>DATA_DIR</code><span>SQLite database and receipt PDFs</span></div><div><code>MAX_UPLOAD_MB</code><span>Maximum size of one submission</span></div></div></section>
<section class="docs-section"><div class="section-intro"><p class="eyebrow">Deployment</p><h2>Docker or a standalone binary</h2></div><div class="deploy-grid"><div><h3>Standalone</h3><pre><code>receipt-upload serve \
  --config ./runtime/app.env \
  --host 0.0.0.0 \
  --port 8725</code></pre></div><div><h3>Docker</h3><pre><code>docker build -t receipt-upload .
docker run --rm -p 8725:8725 \
  -v "$PWD/config/.env:/app/config/.env:ro" \
  -v receipt-upload-data:/app/data \
  receipt-upload</code></pre></div></div><p class="docs-note">Persist <code>DATA_DIR</code>. It contains both <code>app.sqlite3</code> and generated receipt PDFs.</p></section>
<section class="docs-section"><div class="section-intro"><p class="eyebrow">Operational notes</p><h2>Designed to stay small</h2></div><div class="feature-grid"><article><h3>Image processing included</h3><p>Phone photos are resized to a maximum 1600px dimension and JPEG-compressed before PDF generation. No external converter is required.</p></article><article><h3>Local, portable data</h3><p>Metadata lives in one SQLite database and receipt PDFs live beside it under the configured data directory.</p></article><article><h3>Protected administration</h3><p>Three failed login attempts ban an IP for 24 hours. The CLI can list and remove ban records.</p></article></div></section></main>`
	return layout("receipt-upload — Developer documentation", body)
}

func renderAdmin(settings Settings, uploadToken, appBaseURL string, view AdminView, uploadLinkError string) string {
	cardholders := `<li class="empty">No cardholders yet.</li>`
	if len(view.Cardholders) > 0 {
		var b strings.Builder
		for _, r := range view.Cardholders {
			fmt.Fprintf(&b, `<li><span>%s</span><form method="post" action="/admin/cardholders/%d/delete"><button class="danger secondary" type="submit">Remove</button></form></li>`, esc(r.Name), r.ID)
		}
		cardholders = b.String()
	}
	stores := `<li class="empty">No stores yet.</li>`
	if len(view.Stores) > 0 {
		var b strings.Builder
		for _, r := range view.Stores {
			fmt.Fprintf(&b, `<li><span>%s</span><form method="post" action="/admin/stores/%d/delete"><button class="danger secondary" type="submit">Remove</button></form></li>`, esc(r.Name), r.ID)
		}
		stores = b.String()
	}
	uploads := `<tr><td colspan="9" class="empty">No receipts uploaded yet.</td></tr>`
	if len(view.Uploads) > 0 {
		var b strings.Builder
		for _, u := range view.Uploads {
			muted := ""
			status := "Active"
			archive := fmt.Sprintf(`<form method="post" action="/admin/uploads/%d/archive"><button class="secondary" type="submit">Archive</button></form>`, u.ID)
			if u.ArchivedAt.Valid {
				muted = "muted"
				status = "Archived"
				archive = ""
			}
			stores := "Unassigned"
			if u.StoreNames != "" {
				stores = esc(u.StoreNames)
			}
			desc := u.Description
			if desc == "" {
				desc = u.Notes
			}
			fmt.Fprintf(&b, `<tr class="%s"><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td class="actions"><a class="button secondary" href="/admin/uploads/%d/download" data-download-link data-loading-text="Preparing PDF...">Download</a>%s<form method="post" action="/admin/uploads/%d/delete"><button class="danger secondary" type="submit">Delete</button></form></td></tr>`, muted, esc(u.CreatedAt), esc(u.CardholderName), esc(u.Total), esc(u.PurchaseLocation), stores, esc(desc), esc(u.PDFSize), status, u.ID, archive, u.ID)
		}
		uploads = b.String()
	}
	return layout("Admin", fmt.Sprintf(`<header class="topbar"><div><h1>receipt-upload</h1><p>%s used by this app</p></div><form method="post" action="/admin/logout"><button class="secondary" type="submit">Log out</button></form></header><main class="admin-grid"><section class="wide">%s</section><section class="panel wide"><h2>Secret Upload Link</h2>%s<div class="copy-row"><input readonly value="%s" aria-label="Current secret upload link"><a class="button" href="%s" target="_blank">Open</a></div><form class="secret-code-form" method="post" action="/admin/upload-link"><label>Public hostname / base URL <input name="app_base_url" type="url" value="%s" placeholder="https://receipts.example.com" autocomplete="url" required></label><label>Secret code <input name="secret_code" value="%s" minlength="8" maxlength="128" pattern="[A-Za-z0-9_-]+" autocomplete="off" required></label><button type="submit">Change link</button></form><p class="help-text">The public URL is saved for future links. Changing the secret code immediately disables the old upload link.</p></section><section class="panel"><h2>Cardholders</h2><form class="inline-form" method="post" action="/admin/cardholders"><input name="name" placeholder="Name" required><button type="submit">Add</button></form><ul class="manage-list">%s</ul></section><section class="panel"><h2>Stores</h2><form class="inline-form" method="post" action="/admin/stores"><input name="name" placeholder="Store" required><button type="submit">Add</button></form><ul class="manage-list">%s</ul></section><section class="panel wide"><h2>Uploads</h2><div class="table-wrap"><table><thead><tr><th>Date</th><th>Cardholder</th><th>Total</th><th>Purchased At</th><th>Stores</th><th>Description</th><th>PDF</th><th>Status</th><th>Actions</th></tr></thead><tbody>%s</tbody></table></div></section></main>`, view.DiskUsage, defaultWarning(settings), message("alert", uploadLinkError), esc(uploadURL(appBaseURL, uploadToken)), esc(uploadURL(appBaseURL, uploadToken)), esc(appBaseURL), esc(uploadToken), cardholders, stores, uploads))
}

func renderUpload(token string, view UploadOptions, errMsg, success string) string {
	var cardholders strings.Builder
	for _, r := range view.Cardholders {
		fmt.Fprintf(&cardholders, `<option value="%d">%s</option>`, r.ID, esc(r.Name))
	}
	stores := `<p class="empty left">No stores have been configured.</p>`
	if len(view.Stores) > 0 {
		var b strings.Builder
		for _, s := range view.Stores {
			fmt.Fprintf(&b, `<label class="check-row"><input type="checkbox" name="store_ids" value="%d"><span>%s</span></label>`, s.ID, esc(s.Name))
		}
		stores = b.String()
	}
	return layout("Upload Receipt", fmt.Sprintf(`<main class="upload-shell"><section class="upload-panel"><h1>Upload Receipt</h1>%s%s<form method="post" action="/upload/%s" enctype="multipart/form-data" class="stack" data-loading-form data-resize-upload><label>Cardholder<select name="cardholder_id" required><option value="">Select a name</option>%s</select></label><label>Total <input name="total" inputmode="decimal" placeholder="42.50" required></label><label>Place of Purchase <input name="purchase_location" placeholder="Vendor or location" required></label><label>Description <input name="description" placeholder="Business purpose or expense label"></label><fieldset><legend>Stores</legend><div class="check-list">%s</div></fieldset><label>Notes <textarea name="notes" rows="4"></textarea></label><label>Receipt Images<input name="files" type="file" multiple accept="image/*" required data-append-files data-file-list="receipt-file-list"></label><div class="file-selection" aria-live="polite"><div class="file-selection-header"><span id="receipt-file-count">No files selected</span><button class="secondary" type="button" data-clear-files>Clear</button></div><ul id="receipt-file-list" class="selected-files"></ul></div><button type="submit" data-loading-text="Uploading...">Upload</button><div class="loading-status" role="status" aria-live="polite"><span class="spinner" aria-hidden="true"></span><span>Uploading receipt.</span></div></form></section></main>`, message("success", success), message("alert", errMsg), esc(token), cardholders.String(), stores))
}

func layout(title, body string) string {
	return fmt.Sprintf(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>%s</title><link rel="icon" type="image/png" href="/static/logo.png"><link rel="stylesheet" href="/static/styles.css"></head><body>%s<footer class="site-footer"><a href="https://phillip-england.com">Built with ❤️ by Phillip England</a></footer><script>%s</script></body></html>`, esc(title), body, clientJS)
}

const clientJS = `
document.querySelectorAll("[data-loading-form]").forEach((form)=>{form.addEventListener("submit",()=>{form.classList.add("is-loading");form.setAttribute("aria-busy","true");form.querySelectorAll("button[type='submit']").forEach((button)=>{button.dataset.originalText=button.textContent;button.textContent=button.dataset.loadingText||"Working...";button.disabled=true;});});});
document.querySelectorAll("[data-append-files]").forEach((input)=>{const form=input.closest("form");const fileList=document.getElementById(input.dataset.fileList);const fileCount=document.getElementById("receipt-file-count");const clearButton=form?form.querySelector("[data-clear-files]"):null;let selectedFiles=window.DataTransfer?new DataTransfer():null;const renderFiles=()=>{const files=selectedFiles?selectedFiles.files:input.files;if(fileCount){const count=files.length;fileCount.textContent=count===0?"No files selected":count===1?"1 file selected":count+" files selected";}if(!fileList)return;fileList.innerHTML="";Array.from(files).forEach((file)=>{const item=document.createElement("li");item.textContent=file.name+" ("+Math.max(1,Math.round(file.size/1024))+" KB)";fileList.appendChild(item);});};input.addEventListener("change",()=>{if(selectedFiles){Array.from(input.files).forEach((file)=>selectedFiles.items.add(file));input.files=selectedFiles.files;}renderFiles();});if(clearButton){clearButton.addEventListener("click",()=>{if(window.DataTransfer){selectedFiles=new DataTransfer();input.files=selectedFiles.files;}else{input.value="";}renderFiles();});}renderFiles();});
document.querySelectorAll("[data-resize-upload]").forEach((form)=>{form.addEventListener("submit",async(event)=>{const input=form.querySelector("input[type='file'][name='files']");if(!input||!window.DataTransfer||input.dataset.resized==="true")return;event.preventDefault();const dt=new DataTransfer();for(const file of Array.from(input.files)){if(!file.type.startsWith("image/")){dt.items.add(file);continue;}const resized=await resizeImage(file,1600,0.76);dt.items.add(resized);}input.files=dt.files;input.dataset.resized="true";form.requestSubmit();});});
async function resizeImage(file,maxDim,quality){const bitmap=await createImageBitmap(file);const scale=Math.min(1,maxDim/Math.max(bitmap.width,bitmap.height));const width=Math.max(1,Math.round(bitmap.width*scale));const height=Math.max(1,Math.round(bitmap.height*scale));const canvas=document.createElement("canvas");canvas.width=width;canvas.height=height;canvas.getContext("2d").drawImage(bitmap,0,0,width,height);const blob=await new Promise((resolve)=>canvas.toBlob(resolve,"image/jpeg",quality));return new File([blob],file.name.replace(/\.[^.]+$/,"")+".jpg",{type:"image/jpeg",lastModified:file.lastModified});}
document.querySelectorAll("[data-download-link]").forEach((link)=>{link.addEventListener("click",()=>{if(link.classList.contains("is-loading"))return;link.dataset.originalText=link.textContent;link.textContent=link.dataset.loadingText||"Preparing...";link.classList.add("is-loading");link.setAttribute("aria-busy","true");window.setTimeout(()=>{link.textContent=link.dataset.originalText||"Download";link.classList.remove("is-loading");link.removeAttribute("aria-busy");},8000);});});
`

func defaultWarning(settings Settings) string {
	if settings.usesDefaultAdminCredentials() {
		return `<div class="security-warning">Default admin credentials are active. Change <code>ADMIN_USERNAME</code> and <code>ADMIN_PASSWORD</code> before real use.</div>`
	}
	return ""
}

func message(className, value string) string {
	if value == "" {
		return ""
	}
	return fmt.Sprintf(`<p class="%s">%s</p>`, className, esc(value))
}

func esc(value string) string {
	return html.EscapeString(value)
}

func writeHTML(w http.ResponseWriter, value string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	io.WriteString(w, value)
}

func serverError(w http.ResponseWriter, err error) {
	fmt.Fprintf(os.Stderr, "%+v\n", err)
	http.Error(w, "Internal server error", http.StatusInternalServerError)
}

func idFromPath(path, prefix, suffix string) (int64, bool) {
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return 0, false
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	raw = strings.Trim(raw, "/")
	id, err := strconv.ParseInt(raw, 10, 64)
	return id, err == nil
}

func signSession(secret string) string {
	body := base64.RawURLEncoding.EncodeToString([]byte(`{"admin":true}`))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return body + "." + hex.EncodeToString(mac.Sum(nil))
}

func (a *App) isAdmin(r *http.Request) bool {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 2 {
		return false
	}
	mac := hmac.New(sha256.New, []byte(a.settings.SecretKey))
	mac.Write([]byte(parts[0]))
	expected := hex.EncodeToString(mac.Sum(nil))
	if !ctEq(parts[1], expected) {
		return false
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	return err == nil && string(body) == `{"admin":true}`
}

func ctEq(a, b string) bool {
	return hmac.Equal([]byte(a), []byte(b))
}

func clientIP(r *http.Request) string {
	if forwarded := r.Header.Get("x-forwarded-for"); forwarded != "" {
		if ip := strings.TrimSpace(strings.Split(forwarded, ",")[0]); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	return "unknown"
}

func nowISO() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func parseISO(value sql.NullString) (time.Time, bool) {
	if !value.Valid {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, value.String)
	return t, err == nil
}

func purgeOldLoginAttempts(db *sql.DB) error {
	cutoff := time.Now().UTC().Add(-banHours * time.Hour).Format(time.RFC3339)
	now := nowISO()
	_, err := db.Exec("DELETE FROM login_attempts WHERE last_attempt_at < ? AND (banned_until IS NULL OR banned_until < ?)", cutoff, now)
	return err
}

func isIPBanned(db *sql.DB, ip string) (bool, error) {
	var bannedUntil sql.NullString
	err := db.QueryRow("SELECT banned_until FROM login_attempts WHERE ip_address = ?", ip).Scan(&bannedUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	t, ok := parseISO(bannedUntil)
	return ok && t.After(time.Now().UTC()), nil
}

func recordFailedLogin(db *sql.DB, ip string) error {
	var failed int64
	err := db.QueryRow("SELECT failed_count FROM login_attempts WHERE ip_address = ?", ip).Scan(&failed)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	failed++
	var banned any
	if failed >= maxFailedLogins {
		banned = time.Now().UTC().Add(banHours * time.Hour).Format(time.RFC3339)
	}
	_, err = db.Exec("INSERT INTO login_attempts (ip_address, failed_count, last_attempt_at, banned_until) VALUES (?, ?, ?, ?) ON CONFLICT(ip_address) DO UPDATE SET failed_count = excluded.failed_count, last_attempt_at = excluded.last_attempt_at, banned_until = excluded.banned_until", ip, failed, nowISO(), banned)
	return err
}

func clearLoginAttempts(db *sql.DB, ip string) error {
	_, err := db.Exec("DELETE FROM login_attempts WHERE ip_address = ?", ip)
	return err
}

func directorySize(path string) uint64 {
	var total uint64
	filepath.WalkDir(path, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += uint64(info.Size())
		}
		return nil
	})
	return total
}

func humanBytes(size uint64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	value := float64(size)
	for _, unit := range units {
		if value < 1024 || unit == "TB" {
			if unit == "B" {
				return fmt.Sprintf("%d B", size)
			}
			return fmt.Sprintf("%.1f %s", value, unit)
		}
		value /= 1024
	}
	return fmt.Sprintf("%d B", size)
}

func loadSettings(path string) (Settings, error) {
	values, err := loadConfigFile(path)
	if err != nil {
		return Settings{}, err
	}
	if err := validateConfigValues(values); err != nil {
		return Settings{}, err
	}
	maxMB, _ := strconv.ParseInt(values["MAX_UPLOAD_MB"], 10, 64)
	dataDir := filepath.Clean(values["DATA_DIR"])
	return Settings{
		AdminUsername:  values["ADMIN_USERNAME"],
		AdminPassword:  values["ADMIN_PASSWORD"],
		SecretKey:      values["SECRET_KEY"],
		UploadToken:    values["UPLOAD_TOKEN"],
		AppBaseURL:     values["APP_BASE_URL"],
		DataDir:        dataDir,
		UploadDir:      filepath.Join(dataDir, "receipts"),
		MaxUploadBytes: maxMB * 1024 * 1024,
	}, nil
}

func loadConfigFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("configuration error: could not read %s", path)
	}
	return parseEnvFile(string(data))
}

func parseEnvFile(content string) (map[string]string, error) {
	values := map[string]string{}
	for i, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		name, rawValue, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("configuration error: line %d must be KEY=VALUE", i+1)
		}
		name = strings.TrimSpace(name)
		if !contains(configKeys, name) {
			return nil, fmt.Errorf("configuration error: %s is not a supported configuration key", name)
		}
		if _, exists := values[name]; exists {
			return nil, fmt.Errorf("configuration error: %s is defined more than once", name)
		}
		value, err := parseEnvValue(strings.TrimSpace(rawValue), i+1)
		if err != nil {
			return nil, err
		}
		values[name] = value
	}
	return values, nil
}

func parseEnvValue(value string, line int) (string, error) {
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		return strconv.Unquote(value)
	}
	if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
		return value[1 : len(value)-1], nil
	}
	if strings.HasPrefix(value, "\"") || strings.HasPrefix(value, "'") || strings.HasSuffix(value, "\"") || strings.HasSuffix(value, "'") {
		return "", fmt.Errorf("configuration error: line %d has an unterminated quoted value", line)
	}
	return value, nil
}

func validateConfigValues(values map[string]string) error {
	for _, key := range configKeys {
		value := strings.TrimSpace(values[key])
		if value == "" {
			return fmt.Errorf("configuration error: %s is required", key)
		}
		if isPlaceholderConfigValue(value) {
			return fmt.Errorf("configuration error: %s must be set to a real value", key)
		}
	}
	if _, err := parsePositiveInt(values, "MAX_UPLOAD_MB"); err != nil {
		return err
	}
	if err := validateAppBaseURL(values["APP_BASE_URL"]); err != nil {
		return fmt.Errorf("configuration error: APP_BASE_URL %s", err)
	}
	if err := validateUploadToken(values["UPLOAD_TOKEN"]); err != nil {
		return fmt.Errorf("configuration error: UPLOAD_TOKEN %s", err)
	}
	return nil
}

func isPlaceholderConfigValue(value string) bool {
	return value == "REPLACE_ME" || value == "REPLACE_WITH_LONG_RANDOM_SECRET" || value == "REPLACE_WITH_SECRET_UPLOAD_TOKEN"
}

func parsePositiveInt(values map[string]string, key string) (int64, error) {
	parsed, err := strconv.ParseInt(strings.TrimSpace(values[key]), 10, 64)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("configuration error: %s must be a positive integer", key)
	}
	return parsed, nil
}

func validateUploadToken(token string) error {
	if len(token) < 8 || len(token) > 128 {
		return errors.New("The secret code must be between 8 and 128 characters.")
	}
	for _, ch := range token {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_') {
			return errors.New("Use only letters, numbers, hyphens, and underscores in the secret code.")
		}
	}
	return nil
}

func validateAppBaseURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.RawQuery != "" {
		return errors.New("Enter a valid public URL, including http:// or https://.")
	}
	return nil
}

func initApp(configPath, dataDir string, force bool) error {
	if err := initConfigFileWithDataDir(configPath, dataDir, force); err != nil {
		return err
	}
	if err := initDataDir(dataDir); err != nil {
		return err
	}
	fmt.Println("initialization complete")
	return nil
}

func initDataDir(dataDir string) error {
	uploadDir := filepath.Join(dataDir, "receipts")
	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		return fmt.Errorf("could not create data directory %s", dataDir)
	}
	dbPath := filepath.Join(dataDir, "app.sqlite3")
	if err := initDB(dbPath); err != nil {
		return err
	}
	fmt.Printf("initialized database at %s\n", dbPath)
	fmt.Printf("initialized receipt storage at %s\n", uploadDir)
	return nil
}

func initConfigFile(path string, force bool) error {
	return initConfigFileWithDataDir(path, "./data", force)
}

func initConfigFileWithDataDir(path, dataDir string, force bool) error {
	if _, err := os.Stat(path); err == nil && !force {
		return fmt.Errorf("configuration error: %s already exists; use --force to overwrite it", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	secret, _ := randomAlphanumeric(48)
	token, _ := randomAlphanumeric(32)
	content := fmt.Sprintf("ADMIN_USERNAME=admin\nADMIN_PASSWORD=REPLACE_ME\nSECRET_KEY=%s\nUPLOAD_TOKEN=%s\nAPP_BASE_URL=http://localhost:8725\nDATA_DIR=%s\nMAX_UPLOAD_MB=50\n", secret, token, quoteEnvValue(dataDir))
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		return err
	}
	_ = os.Chmod(path, 0600)
	fmt.Printf("initialized configuration at %s\n", path)
	fmt.Println("edit ADMIN_PASSWORD before real use")
	return nil
}

func checkConfigFile(path string) error {
	settings, err := loadSettings(path)
	if err != nil {
		return err
	}
	fmt.Printf("configuration OK: %s\n", path)
	fmt.Printf("APP_BASE_URL=%s\n", settings.AppBaseURL)
	fmt.Printf("DATA_DIR=%s\n", settings.DataDir)
	fmt.Printf("MAX_UPLOAD_MB=%d\n", settings.MaxUploadBytes/1024/1024)
	return nil
}

func saveConfigAndPrint(path, name, value string) error {
	saved, err := saveConfigValue(path, name, value)
	if err != nil {
		return err
	}
	fmt.Printf("Saved %s to %s.\n", name, saved)
	return nil
}

func saveConfigValue(path, name, value string) (string, error) {
	if !contains(configKeys, name) {
		return "", fmt.Errorf("configuration error: %s is not a supported configuration key", name)
	}
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("configuration error: %s is required", name)
	}
	values, err := loadConfigFile(path)
	if err != nil {
		return "", err
	}
	values[name] = value
	var b strings.Builder
	for _, key := range configKeys {
		v := values[key]
		if strings.TrimSpace(v) == "" {
			return "", fmt.Errorf("configuration error: %s is required", key)
		}
		fmt.Fprintf(&b, "%s=%s\n", key, quoteEnvValue(v))
	}
	if err := os.WriteFile(path, []byte(b.String()), 0600); err != nil {
		return "", err
	}
	_ = os.Chmod(path, 0600)
	return path, nil
}

func quoteEnvValue(value string) string {
	for _, ch := range value {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || strings.ContainsRune("./:-_", ch)) {
			return strconv.Quote(value)
		}
	}
	return value
}

func printGenerated(key string, length int, raw bool) error {
	value, err := randomAlphanumeric(length)
	if err != nil {
		return err
	}
	if raw {
		fmt.Println(value)
	} else {
		fmt.Printf("%s=%s\n", key, value)
	}
	return nil
}

func randomAlphanumeric(length int) (string, error) {
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	out := make([]byte, length)
	random := make([]byte, length)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	for i, b := range random {
		out[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(out), nil
}

func promptPassword() (string, error) {
	fmt.Print("Admin password: ")
	pass, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Println()
	if err != nil {
		return "", err
	}
	if len(pass) == 0 {
		return "", errors.New("Admin password cannot be empty.")
	}
	fmt.Print("Confirm admin password: ")
	confirm, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Println()
	if err != nil {
		return "", err
	}
	if string(pass) != string(confirm) {
		return "", errors.New("Passwords did not match.")
	}
	return string(pass), nil
}

func loadAppSetting(settings Settings, name, fallback string) (string, error) {
	db, err := openDB(settings.dbPath())
	if err != nil {
		return "", err
	}
	defer db.Close()
	var value string
	err = db.QueryRow("SELECT value FROM app_settings WHERE name = ?", name).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return fallback, nil
	}
	return value, err
}

func saveUploadLink(settings Settings, token, appBaseURL string) error {
	db, err := openDB(settings.dbPath())
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("INSERT INTO app_settings (name, value) VALUES ('APP_BASE_URL', ?) ON CONFLICT(name) DO UPDATE SET value = excluded.value", appBaseURL); err != nil {
		return err
	}
	if _, err := tx.Exec("INSERT INTO app_settings (name, value) VALUES ('UPLOAD_TOKEN', ?) ON CONFLICT(name) DO UPDATE SET value = excluded.value", token); err != nil {
		return err
	}
	return tx.Commit()
}

func execSQL(dbPath, query string, args ...any) error {
	db, err := openDB(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(query, args...)
	return err
}

func listBannedIPs(configPath string, showAll bool) error {
	settings, err := loadSettings(configPath)
	if err != nil {
		return err
	}
	if err := initDB(settings.dbPath()); err != nil {
		return err
	}
	db, err := openDB(settings.dbPath())
	if err != nil {
		return err
	}
	defer db.Close()
	if err := purgeOldLoginAttempts(db); err != nil {
		return err
	}
	sqlText := "SELECT id, ip_address, failed_count, last_attempt_at, banned_until FROM login_attempts WHERE banned_until IS NOT NULL AND banned_until > ? ORDER BY banned_until DESC, last_attempt_at DESC"
	args := []any{nowISO()}
	if showAll {
		sqlText = "SELECT id, ip_address, failed_count, last_attempt_at, banned_until FROM login_attempts ORDER BY banned_until DESC, last_attempt_at DESC"
		args = nil
	}
	rows, err := db.Query(sqlText, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	type rec struct {
		id, failures int64
		ip, last     string
		banned       sql.NullString
	}
	var records []rec
	for rows.Next() {
		var r rec
		if err := rows.Scan(&r.id, &r.ip, &r.failures, &r.last, &r.banned); err != nil {
			return err
		}
		records = append(records, r)
	}
	if len(records) == 0 {
		if showAll {
			fmt.Println("No login attempt records found.")
		} else {
			fmt.Println("No banned IP addresses found.")
		}
		return nil
	}
	fmt.Printf("%-6s %-45s %-8s %-32s Banned Until\n", "ID", "IP Address", "Failures", "Last Attempt")
	for _, r := range records {
		banned := "-"
		if r.banned.Valid {
			banned = r.banned.String
		}
		fmt.Printf("%-6d %-45s %-8d %-32s %s\n", r.id, r.ip, r.failures, r.last, banned)
	}
	return nil
}

func unbanIP(configPath string, id int64) error {
	settings, err := loadSettings(configPath)
	if err != nil {
		return err
	}
	if err := initDB(settings.dbPath()); err != nil {
		return err
	}
	db, err := openDB(settings.dbPath())
	if err != nil {
		return err
	}
	defer db.Close()
	var ip string
	if err := db.QueryRow("SELECT ip_address FROM login_attempts WHERE id = ?", id).Scan(&ip); err != nil {
		return fmt.Errorf("No login attempt record found with ID %d.", id)
	}
	if _, err := db.Exec("DELETE FROM login_attempts WHERE id = ?", id); err != nil {
		return err
	}
	fmt.Printf("Removed login attempt record %d for %s.\n", id, ip)
	return nil
}

func contains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
