package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCatalogImportUpdatesByNameAndRollsBackOnError(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite3", filepath.Join(dir, "test.sqlite")+"?_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	photos := filepath.Join(dir, "photos")
	if err := os.Mkdir(photos, 0755); err != nil {
		t.Fatal(err)
	}
	s := server{db: db, photos: photos}
	post := func(body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		result := httptest.NewRecorder()
		s.api(result, httptest.NewRequest(http.MethodPost, "/api/import", strings.NewReader(body)))
		if result.Code != status {
			t.Fatalf("expected %d, got %d: %s", status, result.Code, result.Body.String())
		}
		return result
	}
	checkCounts := func(result *httptest.ResponseRecorder, want importCounts) {
		t.Helper()
		var got importCounts
		if err := json.Unmarshal(result.Body.Bytes(), &got); err != nil || got != want {
			t.Fatalf("expected %+v, got %+v: %v", want, got, err)
		}
	}

	const input = `{
		"sections":[{"name":"Еда","position":10}],
		"tags":[{"name":"Домашнее"}],
		"places":[{"name":"Холодильник"}],
		"items":[{"name":"Сырники","section":"Еда","quantity":6,"place":"Холодильник",
			"description":"Завтрак","tags":["Домашнее"],
			"photo_base64":"R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7"}]
	}`
	checkCounts(post(input, 200), importCounts{Created: 4})
	checkCounts(post(input, 200), importCounts{Updated: 4})
	checkCounts(post(`{"sections":[{"name":" ЕДА ","position":5}],"items":[{"name":" СЫРНИКИ ","quantity":2}]}`, 200), importCounts{Updated: 2})
	post(`{"tags":[{"name":"Новый тег"}],"items":[{"name":"сырники","quantity":0},{"name":"Другая позиция","section":"Неизвестный раздел","quantity":1}]}`, 400)
	post(`{"tags":[{"name":"Один"},{"name":" один "}]}`, 400)

	var name, photo, description string
	var quantity, position, tagCount, placeCount int
	err = db.QueryRow(`SELECT items.name, items.photo, items.description, items.quantity, sections.position,
		(SELECT count(*) FROM tags JOIN item_tags ON tags.id=item_tags.tag WHERE item_tags.item=items.id),
		(SELECT count(*) FROM places WHERE places.id=items.place)
		FROM items JOIN sections ON items.section=sections.id`).
		Scan(&name, &photo, &description, &quantity, &position, &tagCount, &placeCount)
	if err != nil || name != "СЫРНИКИ" || quantity != 2 || position != 5 || description != "Завтрак" || tagCount != 1 || placeCount != 1 {
		t.Fatalf("unexpected item after import: %q, %d, %d, %q, %d, %d, %v", name, quantity, position, description, tagCount, placeCount, err)
	}
	for _, table := range []string{"sections", "tags", "places", "items"} {
		var count int
		if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("unexpected %s count: %d, %v", table, count, err)
		}
	}
	entries, err := os.ReadDir(photos)
	if err != nil || photo == "" || len(entries) != 1 || entries[0].Name() != photo {
		t.Fatalf("unexpected photos: %q, %v, %v", photo, entries, err)
	}
	checkCounts(post(`{"items":[{"name":"сырники","place":"","description":"","tags":[],"photo_base64":""}]}`, 200), importCounts{Updated: 1})
	if _, err := os.Stat(filepath.Join(photos, photo)); !os.IsNotExist(err) {
		t.Fatalf("expected removed photo, got %v", err)
	}
}
