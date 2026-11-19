package main

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCheckoutIsAtomicAndRetryDoesNotTakeTwice(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema + demo); err != nil {
		t.Fatal(err)
	}
	s := server{db: db}
	post := func(token string, items string, want int) {
		t.Helper()
		body := fmt.Sprintf(`{"token":%q,"items":%s}`, token, items)
		result := httptest.NewRecorder()
		s.api(result, httptest.NewRequest(http.MethodPost, "/api/cart/take", strings.NewReader(body)))
		if result.Code != want {
			t.Fatalf("got status %d, want %d: %s", result.Code, want, result.Body.String())
		}
	}
	quantity := func(id, want int) {
		t.Helper()
		var got int
		if err := db.QueryRow("SELECT quantity FROM items WHERE id=?", id).Scan(&got); err != nil || got != want {
			t.Fatalf("item %d quantity %d, want %d: %v", id, got, want, err)
		}
	}
	const token = "0123456789abcdef0123456789abcdef"
	post(token, `[{"id":1,"quantity":2},{"id":2,"quantity":13}]`, 409)
	quantity(1, 6)
	quantity(2, 12)
	const items = `[{"id":1,"quantity":2},{"id":2,"quantity":3}]`
	post(token, items, 200)
	post(token, items, 200)
	quantity(1, 4)
	quantity(2, 9)
}
