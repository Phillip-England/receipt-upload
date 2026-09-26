package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestReceiptFilename(t *testing.T) {
	u := UploadRow{CreatedAt: "2026-09-26T15:00:00Z", PurchaseLocation: "Sam's", Total: "19.08", Description: "Milk"}
	for _, tc := range []struct {
		locations      []string
		category, want string
	}{
		{[]string{"Southroads", "Utica"}, "", "092626-sams-19.08-milk-blank-split.pdf"},
		{[]string{"Southroads"}, "", "092626-sams-19.08-milk-blank-southroads.pdf"},
		{[]string{"Utica"}, "", "092626-sams-19.08-milk-blank-utica.pdf"},
		{[]string{"Southroads", "Utica", "Third"}, "Food supplies", "092626-sams-19.08-milk-food-supplies-split.pdf"},
		{nil, "", "092626-sams-19.08-milk-blank-blank.pdf"},
	} {
		got, err := receiptFilename(u, tc.locations, tc.category)
		if err != nil || got != tc.want {
			t.Fatalf("got %q, %v; want %q", got, err, tc.want)
		}
	}
	u.CreatedAt = "2026-09-27T02:00:00Z"
	got, err := receiptFilename(u, nil, "")
	if err != nil || !strings.HasPrefix(got, "092626-") {
		t.Fatalf("Chicago date: %q %v", got, err)
	}
	if got := filenamePart("../Milk\r\n\" / Supplies"); got != "milk-supplies" {
		t.Fatal(got)
	}
}

func TestReceiptLifecycle(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := initDB(defaultDBPath); err != nil {
		t.Fatal(err)
	}
	db, err := openDB(defaultDBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := os.WriteFile("receipt.pdf", []byte("%PDF-test"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("INSERT INTO uploads (cardholder_name,total,purchase_location,description,original_filenames,pdf_path,pdf_size_bytes,created_at,archived_at) VALUES ('Person','19.08','sams','milk','a.jpg','receipt.pdf',9,'2026-09-26T15:00:00Z','2026-09-26')")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("INSERT INTO receipt_stores(upload_id,store_name) VALUES (1,'Southroads'),(1,'Utica')")
	if err != nil {
		t.Fatal(err)
	}
	app := &App{settings: Settings{SecretKey: "test"}}
	request := func(method, path, body string, handler http.HandlerFunc) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: signSession("test")})
		w := httptest.NewRecorder()
		handler(w, r)
		return w
	}
	w := request("POST", "/admin/categories", "name=Food", app.categoryAction)
	if w.Code != 303 {
		t.Fatalf("category: %d %s", w.Code, w.Body.String())
	}
	for _, tc := range []struct{ query, want string }{{"", "blank"}, {"?category_id=1", "food"}} {
		w = request("GET", "/admin/uploads/1/download"+tc.query, "", app.uploadAction)
		want := "092626-sams-19.08-milk-" + tc.want + "-split.pdf"
		if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Disposition"), want) || w.Body.String() != "%PDF-test" {
			t.Fatalf("download: %d %v %s", w.Code, w.Header(), w.Body.String())
		}
	}
	w = request("GET", "/admin/uploads/1/download?category_id=999", "", app.uploadAction)
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
	w = request("POST", "/admin/uploads/1/purge", "", app.uploadAction)
	if w.Code != 303 {
		t.Fatal(w.Code)
	}
	if _, err := os.Stat("receipt.pdf"); err != nil {
		t.Fatal("active receipt purged", err)
	}
	w = request("POST", "/admin/uploads/1/delete", "", app.uploadAction)
	if w.Code != 303 {
		t.Fatal(w.Code)
	}
	var deleted sql.NullString
	if err := db.QueryRow("SELECT deleted_at FROM uploads WHERE id=1").Scan(&deleted); err != nil || !deleted.Valid {
		t.Fatal(deleted, err)
	}
	if _, err := os.Stat("receipt.pdf"); err != nil {
		t.Fatal("soft delete removed PDF", err)
	}
	view, err := loadAdminView(app.settings)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(renderReceiptRows(view, false), "/1/download") || !strings.Contains(renderReceiptRows(view, true), "/1/restore") {
		t.Fatal("incorrect graveyard visibility")
	}
	page := renderAdmin(app.settings, "token", "", view, "")
	if strings.Contains(page, "%!") || strings.Contains(page, ">Archive<") {
		t.Fatal("invalid admin rendering")
	}
	w = request("POST", "/admin/uploads/1/restore", "", app.uploadAction)
	if w.Code != 303 {
		t.Fatal(w.Code)
	}
	if err := db.QueryRow("SELECT deleted_at FROM uploads WHERE id=1").Scan(&deleted); err != nil || deleted.Valid {
		t.Fatal(deleted, err)
	}
	request("POST", "/admin/uploads/1/delete", "", app.uploadAction)
	w = request("POST", "/admin/uploads/1/purge", "", app.uploadAction)
	if w.Code != 303 {
		t.Fatal(w.Code)
	}
	if _, err := os.Stat("receipt.pdf"); !os.IsNotExist(err) {
		t.Fatal("PDF still exists", err)
	}
	var count int
	db.QueryRow("SELECT COUNT(*) FROM uploads").Scan(&count)
	if count != 0 {
		t.Fatal("receipt still exists")
	}
	db.QueryRow("SELECT COUNT(*) FROM receipt_stores").Scan(&count)
	if count != 0 {
		t.Fatal("location links still exist")
	}
	if err := initDB(defaultDBPath); err != nil {
		t.Fatal("repeat migration", err)
	}
}

func TestUpgradeExistingDatabase(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := initDB(defaultDBPath); err != nil {
		t.Fatal(err)
	}
	db, err := openDB(defaultDBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, query := range []string{
		"ALTER TABLE uploads DROP COLUMN deleted_at",
		"DROP TABLE expense_categories",
		"INSERT INTO uploads (cardholder_name,total,purchase_location,original_filenames,pdf_path,pdf_size_bytes,archived_at) VALUES ('Person','19.08','sams','old.jpg','old.pdf',9,'2026-09-26')",
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if err := initDB(defaultDBPath); err != nil {
		t.Fatal(err)
	}
	view, err := loadAdminView(Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Uploads) != 1 || view.Uploads[0].DeletedAt.Valid {
		t.Fatal("legacy receipt not preserved as active")
	}
	if _, err := db.Exec("INSERT INTO expense_categories(name) VALUES ('Food')"); err != nil {
		t.Fatal(err)
	}
}
