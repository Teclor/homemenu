package main

import (
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/skip2/go-qrcode"
)

//go:embed web/*
var assets embed.FS

const itemColumns = `(id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, section INTEGER NOT NULL REFERENCES sections, quantity INTEGER NOT NULL CHECK(quantity >= 0), position INTEGER NOT NULL, place INTEGER REFERENCES places ON DELETE SET NULL, description TEXT NOT NULL DEFAULT '', description_align TEXT NOT NULL DEFAULT 'center' CHECK(description_align IN ('left','center')), photo TEXT NOT NULL DEFAULT '')`

const schema = `
CREATE TABLE IF NOT EXISTS sections(id INTEGER PRIMARY KEY, name TEXT NOT NULL, position INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS tags(id INTEGER PRIMARY KEY, name TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS places(id INTEGER PRIMARY KEY, name TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS items` + itemColumns + `;
CREATE TABLE IF NOT EXISTS item_tags(item INTEGER REFERENCES items ON DELETE CASCADE, tag INTEGER REFERENCES tags ON DELETE CASCADE, PRIMARY KEY(item,tag));
CREATE TABLE IF NOT EXISTS settings(key TEXT PRIMARY KEY);
CREATE TABLE IF NOT EXISTS checkout_receipts(token TEXT PRIMARY KEY);
CREATE TABLE IF NOT EXISTS homepage(id INTEGER PRIMARY KEY CHECK(id=1), caption TEXT NOT NULL, photo TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS appearance(id INTEGER PRIMARY KEY CHECK(id=1), config TEXT NOT NULL);
`

const demo = `
INSERT INTO sections VALUES (1,'Еда',10),(2,'Напитки',20),(3,'Алкоголь',30);
INSERT INTO tags VALUES (1,'Домашнее'),(2,'Сладкое'),(3,'Без сахара'),(4,'Охлаждённое');
INSERT INTO places VALUES (1,'Холодильник'),(2,'Кухонный шкаф');
INSERT INTO items(id,name,section,quantity,position,place,description) VALUES
(1,'Сырники',1,6,10,1,'Творожные сырники к завтраку.'),
(2,'Овсяное печенье',1,12,20,2,'Подходит к чаю и кофе.'),
(3,'Хлеб',1,4,30,2,''),
(4,'Чай с мятой',2,8,10,2,''),
(5,'Минеральная вода',2,5,20,1,''),
(6,'Сухой сидр',3,3,10,1,''),
(7,'Лимонад',2,0,30,1,'');
INSERT INTO item_tags VALUES (1,1),(1,2),(2,2),(4,3),(5,3),(5,4),(6,4),(7,4);
INSERT INTO settings VALUES ('demo');
`

type server struct {
	db        *sql.DB
	photos    string
	fonts     string
	publicURL string
}

func send(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}

func problem(w http.ResponseWriter, status int, message string) {
	send(w, status, map[string]string{"error": message})
}

func internal(w http.ResponseWriter, err error) {
	log.Print(err)
	problem(w, 500, "Не удалось сохранить или прочитать данные.")
}

func (s server) catalog(w http.ResponseWriter) {
	tx, err := s.db.Begin()
	if err != nil {
		internal(w, err)
		return
	}
	defer tx.Rollback()
	queries := map[string]string{
		"appearance": "SELECT config FROM appearance WHERE id=1",
		"homepage":   "SELECT caption,photo FROM homepage WHERE id=1",
		"sections":   "SELECT id,name,position FROM sections ORDER BY position,id",
		"tags":       "SELECT id,name FROM tags ORDER BY name,id",
		"places":     "SELECT id,name FROM places ORDER BY name,id",
		"items": `SELECT id,name,section,quantity,position,place,description,description_align,photo,
 coalesce((SELECT group_concat(tag) FROM item_tags WHERE item=items.id),'') AS tags
 FROM items ORDER BY position,id`,
	}
	result := make(map[string][]map[string]interface{})
	for key, query := range queries {
		rows, err := tx.Query(query)
		if err != nil {
			internal(w, err)
			return
		}
		columns, _ := rows.Columns()
		result[key] = []map[string]interface{}{}
		for rows.Next() {
			values := make([]interface{}, len(columns))
			dest := make([]interface{}, len(columns))
			for i := range values {
				dest[i] = &values[i]
			}
			if err = rows.Scan(dest...); err != nil {
				break
			}
			entry := make(map[string]interface{})
			for i, column := range columns {
				entry[column] = values[i]
			}
			result[key] = append(result[key], entry)
		}
		rows.Close()
		if err == nil {
			err = rows.Err()
		}
		if err != nil {
			internal(w, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		internal(w, err)
		return
	}
	send(w, 200, result)
}

const maxQuantity = 1<<31 - 1

func (s server) adjustQuantity(w http.ResponseWriter, id int64, delta int) {
	result, err := s.db.Exec("UPDATE items SET quantity=quantity+? WHERE id=? AND quantity+? BETWEEN 0 AND ?", delta, id, delta, maxQuantity)
	if err != nil {
		internal(w, err)
		return
	}
	count, err := result.RowsAffected()
	if err != nil {
		internal(w, err)
		return
	}
	if count == 0 {
		message := "Позиция закончилась."
		if delta > 0 {
			message = "Не удалось увеличить остаток. Возможно, позиция удалена или достигнут предел."
		}
		problem(w, 409, message)
		return
	}
	send(w, 200, map[string]bool{"ok": true})
}

type checkoutLine struct {
	ID       int64 `json:"id"`
	Quantity int32 `json:"quantity"`
}

func (s server) checkout(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Token string         `json:"token"`
		Items []checkoutLine `json:"items"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		problem(w, 400, "Не удалось прочитать корзину.")
		return
	}
	if len(input.Token) != 32 || strings.Trim(input.Token, "0123456789abcdef") != "" ||
		len(input.Items) == 0 || decoder.Decode(new(json.RawMessage)) != io.EOF {
		problem(w, 400, "Некорректная корзина.")
		return
	}
	seen := make(map[int64]bool, len(input.Items))
	for _, item := range input.Items {
		if item.ID <= 0 || item.Quantity <= 0 || seen[item.ID] {
			problem(w, 400, "Некорректное количество в корзине.")
			return
		}
		seen[item.ID] = true
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		internal(w, err)
		return
	}
	defer tx.Rollback()
	receipt, err := tx.Exec("INSERT INTO checkout_receipts(token) VALUES(?) ON CONFLICT DO NOTHING", input.Token)
	if err != nil {
		internal(w, err)
		return
	}
	created, err := receipt.RowsAffected()
	if err != nil {
		internal(w, err)
		return
	}
	if created == 0 {
		send(w, 200, map[string]bool{"ok": true})
		return
	}
	for _, item := range input.Items {
		result, err := tx.Exec("UPDATE items SET quantity=quantity-? WHERE id=? AND quantity>=?", item.Quantity, item.ID, item.Quantity)
		if err != nil {
			internal(w, err)
			return
		}
		count, err := result.RowsAffected()
		if err != nil {
			internal(w, err)
			return
		}
		if count == 0 {
			problem(w, 409, "Остатки изменились. Проверьте количество в корзине.")
			return
		}
	}
	if err := tx.Commit(); err != nil {
		internal(w, err)
		return
	}
	send(w, 200, map[string]bool{"ok": true})
}

func migrateDescriptionAlignment(db *sql.DB) error {
	var exists bool
	if err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM pragma_table_info('items') WHERE name='description_align')").Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err := db.Exec("ALTER TABLE items ADD COLUMN description_align TEXT NOT NULL DEFAULT 'center' CHECK(description_align IN ('left','center'))")
	return err
}

func migrateItemIDs(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var definition string
	if err := tx.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='items'").Scan(&definition); err != nil {
		return err
	}
	if strings.Contains(strings.ToUpper(definition), "AUTOINCREMENT") {
		return nil
	}
	// Dropping the old table cascades to item_tags when foreign keys are enabled.
	_, err = tx.Exec(`
		CREATE TEMP TABLE saved_item_tags AS SELECT item,tag FROM item_tags;
		DELETE FROM item_tags;
		CREATE TABLE new_items` + itemColumns + `;
		INSERT INTO new_items(id,name,section,quantity,position,place,description,description_align,photo)
			SELECT id,name,section,quantity,position,place,description,description_align,photo FROM items;
		DROP TABLE items;
		ALTER TABLE new_items RENAME TO items;
		INSERT INTO item_tags(item,tag) SELECT item,tag FROM saved_item_tags;
		DROP TABLE saved_item_tags;`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s server) connection(w http.ResponseWriter, r *http.Request) {
	address := s.publicURL
	if address == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		address = (&url.URL{Scheme: scheme, Host: r.Host, Path: "/"}).String()
	}
	if section := r.URL.Query().Get("section"); section != "" {
		id, err := strconv.ParseInt(section, 10, 64)
		if err != nil || id < 1 {
			problem(w, 400, "Некорректный раздел.")
			return
		}
		var exists bool
		if err := s.db.QueryRow("SELECT EXISTS(SELECT 1 FROM sections WHERE id=?)", id).Scan(&exists); err != nil {
			internal(w, err)
			return
		}
		if !exists {
			problem(w, 404, "Раздел не найден.")
			return
		}
		address += "?section=" + strconv.FormatInt(id, 10)
	}
	png, err := qrcode.Encode(address, qrcode.Medium, 256)
	if err != nil {
		internal(w, err)
		return
	}
	send(w, http.StatusOK, struct {
		URL string `json:"url"`
		PNG []byte `json:"png"`
	}{address, png})
}

func (s server) saveHomepage(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 25<<20)
	if err := r.ParseMultipartForm(128 << 10); err != nil {
		problem(w, 400, "Проверьте форму. Максимальный размер загрузки — 25 МБ.")
		return
	}
	defer r.MultipartForm.RemoveAll()
	caption := strings.TrimSpace(r.FormValue("caption"))
	if len([]rune(caption)) > 1000 {
		problem(w, 400, "Подпись должна содержать не более 1000 символов.")
		return
	}
	uploaded, err := s.savePhoto(r)
	if err != nil {
		problem(w, 400, err.Error())
		return
	}
	committed := false
	defer func() {
		if !committed {
			s.deletePhoto(uploaded)
		}
	}()
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		internal(w, err)
		return
	}
	defer tx.Rollback()
	var previous string
	err = tx.QueryRow("SELECT coalesce((SELECT photo FROM homepage WHERE id=1),'')").Scan(&previous)
	if err != nil {
		internal(w, err)
		return
	}
	photo := uploaded
	if photo == "" && r.FormValue("remove_photo") == "" {
		photo = previous
	}
	_, err = tx.Exec("INSERT INTO homepage(id,caption,photo) VALUES(1,?,?) ON CONFLICT(id) DO UPDATE SET caption=excluded.caption, photo=excluded.photo", caption, photo)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		internal(w, err)
		return
	}
	committed = true
	if previous != photo {
		s.deletePhoto(previous)
	}
	send(w, 200, map[string]bool{"ok": true})
}

func (s server) api(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/api/appearance" && r.Method == http.MethodPost {
		s.saveAppearance(w, r)
		return
	}
	if r.URL.Path == "/api/homepage" && r.Method == http.MethodPost {
		s.saveHomepage(w, r)
		return
	}
	if r.URL.Path == "/api/cart/take" && r.Method == http.MethodPost {
		s.checkout(w, r)
		return
	}
	if r.URL.Path == "/api/connection" && r.Method == http.MethodGet {
		s.connection(w, r)
		return
	}
	if r.URL.Path == "/api/import" && r.Method == http.MethodPost {
		s.importCatalog(w, r)
		return
	}
	path := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/"), "/")
	table := path[0]
	if table == "catalog" && len(path) == 1 && r.Method == "GET" {
		s.catalog(w)
		return
	}
	if table != "items" && table != "sections" && table != "places" && table != "tags" {
		http.NotFound(w, r)
		return
	}
	var id int64
	if len(path) > 1 {
		var err error
		id, err = strconv.ParseInt(path[1], 10, 64)
		if err != nil || id < 1 {
			http.NotFound(w, r)
			return
		}
	}
	switch {
	case len(path) == 3 && table == "items" && path[2] == "take" && r.Method == "POST":
		s.adjustQuantity(w, id, -1)
	case len(path) == 3 && table == "items" && path[2] == "restock" && r.Method == "POST":
		s.adjustQuantity(w, id, 1)
	case len(path) == 2 && r.Method == "DELETE":
		s.remove(w, table, id)
	case len(path) <= 2 && r.Method == "POST":
		s.save(w, r, table, id)
	default:
		http.NotFound(w, r)
	}
}

func (s server) remove(w http.ResponseWriter, table string, id int64) {
	tx, err := s.db.Begin()
	if err != nil {
		internal(w, err)
		return
	}
	defer tx.Rollback()
	var photo string
	if table == "items" {
		err = tx.QueryRow("SELECT photo FROM items WHERE id=?", id).Scan(&photo)
		if errors.Is(err, sql.ErrNoRows) {
			problem(w, 404, "Позиция не найдена.")
			return
		}
		if err != nil {
			internal(w, err)
			return
		}
	}
	if table == "sections" {
		var count int
		err = tx.QueryRow("SELECT count(*) FROM items WHERE section=?", id).Scan(&count)
		if err != nil {
			internal(w, err)
			return
		}
		if count > 0 {
			problem(w, 409, "Сначала переместите или удалите позиции раздела.")
			return
		}
	}
	_, err = tx.Exec("DELETE FROM "+table+" WHERE id=?", id)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		internal(w, err)
		return
	}
	s.deletePhoto(photo)
	send(w, 200, map[string]bool{"ok": true})
}

func (s server) deletePhoto(name string) {
	if name != "" {
		os.Remove(filepath.Join(s.photos, name))
	}
}

func (s server) savePhoto(r *http.Request) (string, error) {
	files := r.MultipartForm.File["photo"]
	if len(files) == 0 || files[0].Filename == "" {
		return "", nil
	}
	source, err := files[0].Open()
	if err != nil {
		return "", err
	}
	defer source.Close()
	return s.storePhoto(source)
}

func (s server) storePhoto(source io.ReadSeeker) (string, error) {
	var signature [512]byte
	size, err := io.ReadFull(source, signature[:])
	if err != nil && err != io.ErrUnexpectedEOF {
		return "", err
	}
	extensions := map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp", "image/gif": ".gif"}
	ext := extensions[http.DetectContentType(signature[:size])]
	if ext == "" {
		return "", errors.New("Поддерживаемые форматы фотографии: JPEG, PNG, WebP и GIF.")
	}
	if _, err = source.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	output, err := os.CreateTemp(s.photos, "photo-*"+ext)
	if err != nil {
		return "", err
	}
	_, err = io.Copy(output, source)
	closeErr := output.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(output.Name())
		return "", err
	}
	return filepath.Base(output.Name()), nil
}

func (s server) save(w http.ResponseWriter, r *http.Request, table string, id int64) {
	r.Body = http.MaxBytesReader(w, r.Body, 25<<20)
	if err := r.ParseMultipartForm(128 << 10); err != nil {
		problem(w, 400, "Проверьте форму. Максимальный размер загрузки — 25 МБ.")
		return
	}
	defer r.MultipartForm.RemoveAll()
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" || len([]rune(name)) > 160 {
		problem(w, 400, "Введите название длиной до 160 символов.")
		return
	}
	var position int64
	if table == "sections" || table == "items" {
		var err error
		position, err = strconv.ParseInt(r.FormValue("position"), 10, 32)
		if err != nil {
			problem(w, 400, "Порядок должен быть целым числом.")
			return
		}
	}
	var photo string
	var quantity, section int64
	var place interface{}
	var tags []int64
	alignment := r.FormValue("description_align")
	if table == "items" {
		if alignment != "" && alignment != "center" && alignment != "left" {
			problem(w, 400, "Выберите выравнивание описания.")
			return
		}
		var err error
		quantity, err = strconv.ParseInt(r.FormValue("quantity"), 10, 32)
		if err != nil || quantity < 0 {
			problem(w, 400, "Введите целое количество от нуля.")
			return
		}
		section, err = strconv.ParseInt(r.FormValue("section"), 10, 64)
		if err != nil || section < 1 {
			problem(w, 400, "Выберите раздел.")
			return
		}
		if value := r.FormValue("place"); value != "" {
			place, err = strconv.ParseInt(value, 10, 64)
			if err != nil {
				problem(w, 400, "Выберите место из справочника.")
				return
			}
		}
		for _, value := range r.MultipartForm.Value["tag"] {
			tag, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				problem(w, 400, "Выберите существующие теги.")
				return
			}
			tags = append(tags, tag)
		}
		photo, err = s.savePhoto(r)
		if err != nil {
			problem(w, 400, err.Error())
			return
		}
	}
	committed := false
	defer func() {
		if !committed {
			s.deletePhoto(photo)
		}
	}()
	tx, err := s.db.Begin()
	if err != nil {
		internal(w, err)
		return
	}
	defer tx.Rollback()
	var oldPhoto string
	if table == "items" {
		nextPhoto := photo
		if id != 0 {
			var currentQuantity int64
			err = tx.QueryRow("SELECT photo, quantity FROM items WHERE id=?", id).Scan(&oldPhoto, &currentQuantity)
			if err == nil {
				if baseline := r.FormValue("original_quantity"); baseline != "" {
					original, parseErr := strconv.ParseInt(baseline, 10, 32)
					if parseErr != nil {
						problem(w, 400, "Некорректное исходное количество.")
						return
					}
					if quantity == original {
						quantity = currentQuantity
					} else if original != currentQuantity {
						problem(w, 409, "Остаток изменился. Откройте позицию заново и проверьте количество.")
						return
					}
				}
				if nextPhoto == "" && r.FormValue("remove_photo") == "" {
					nextPhoto = oldPhoto
				}
				_, err = tx.Exec("UPDATE items SET name=?,section=?,quantity=?,position=?,place=?,description=?,description_align=coalesce(nullif(?,''),description_align),photo=? WHERE id=?", name, section, quantity, position, place, r.FormValue("description"), alignment, nextPhoto, id)
			}
		} else {
			var result sql.Result
			result, err = tx.Exec("INSERT INTO items(name,section,quantity,position,place,description,description_align,photo) VALUES(?,?,?,?,?,?,coalesce(nullif(?,''),'center'),?)", name, section, quantity, position, place, r.FormValue("description"), alignment, nextPhoto)
			if err == nil {
				id, err = result.LastInsertId()
			}
		}
		if err == nil {
			_, err = tx.Exec("DELETE FROM item_tags WHERE item=?", id)
		}
		for _, tag := range tags {
			if err != nil {
				break
			}
			_, err = tx.Exec("INSERT INTO item_tags(item,tag) VALUES(?,?) ON CONFLICT(item,tag) DO NOTHING", id, tag)
		}
		if nextPhoto == oldPhoto {
			oldPhoto = ""
		}
	} else if table == "sections" {
		if id == 0 {
			_, err = tx.Exec("INSERT INTO sections(name,position) VALUES(?,?)", name, position)
		} else {
			_, err = tx.Exec("UPDATE sections SET name=?,position=? WHERE id=?", name, position, id)
		}
	} else if id == 0 {
		_, err = tx.Exec("INSERT INTO "+table+"(name) VALUES(?)", name)
	} else {
		_, err = tx.Exec("UPDATE "+table+" SET name=? WHERE id=?", name, id)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		if strings.Contains(err.Error(), "FOREIGN KEY") {
			problem(w, 400, "Раздел, место или тег больше недоступны.")
			return
		}
		internal(w, err)
		return
	}
	committed = true
	s.deletePhoto(oldPhoto)
	send(w, 200, map[string]bool{"ok": true})
}

func main() {
	dir := os.Getenv("DATA_DIR")
	if dir == "" {
		dir = "data"
	}
	photos := filepath.Join(dir, "photos")
	fonts := filepath.Join(dir, "fonts")
	for _, path := range []string{photos, fonts} {
		if err := os.MkdirAll(path, 0755); err != nil {
			log.Fatal(err)
		}
	}
	db, err := sql.Open("sqlite3", filepath.Join(dir, "menu.sqlite")+"?_foreign_keys=on&_busy_timeout=5000")
	if err != nil {
		log.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(schema); err != nil {
		log.Fatal(err)
	}
	if err := migrateDescriptionAlignment(db); err != nil {
		log.Fatal(err)
	}
	if err := migrateItemIDs(db); err != nil {
		log.Fatal(err)
	}
	if os.Getenv("SEED_DEMO") == "1" {
		tx, err := db.Begin()
		if err != nil {
			log.Fatal(err)
		}
		var count int
		err = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM settings WHERE key='demo') OR EXISTS(SELECT 1 FROM sections) OR EXISTS(SELECT 1 FROM tags) OR EXISTS(SELECT 1 FROM places)").Scan(&count)
		if err == nil && count == 0 {
			_, err = tx.Exec(demo)
		}
		if err == nil {
			err = tx.Commit()
		} else {
			tx.Rollback()
		}
		if err != nil {
			log.Fatal(err)
		}
	}
	publicURL := strings.TrimSpace(os.Getenv("HOMEMENU_PUBLIC_URL"))
	if publicURL != "" {
		parsed, err := url.Parse(publicURL)
		if err != nil || len(publicURL) > 1024 || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
			parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			log.Fatal("HOMEMENU_PUBLIC_URL must be an absolute HTTP(S) URL without credentials, query or fragment")
		}
		if parsed.Path == "" {
			parsed.Path = "/"
		}
		publicURL = parsed.String()
	}
	s := server{db: db, photos: photos, fonts: fonts, publicURL: publicURL}
	web, _ := fs.Sub(assets, "web")
	mux := http.NewServeMux()
	mux.HandleFunc("/api/", s.api)
	mux.HandleFunc("/fonts/", s.serveFont)
	mux.HandleFunc("/photos/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/photos/")
		if filepath.Base(name) != name || !strings.HasPrefix(name, "photo-") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", mime.TypeByExtension(filepath.Ext(name)))
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		http.ServeFile(w, r, filepath.Join(photos, name))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/", "/admin", "/admin/":
			body, _ := fs.ReadFile(web, "index.html")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(body)
		case "/style.css", "/app.js":
			http.FileServer(http.FS(web)).ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	})
	log.Print("HomeMenu :5000")
	log.Fatal((&http.Server{Addr: ":5000", Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 90 * time.Second, WriteTimeout: 100 * time.Second, IdleTimeout: 30 * time.Second}).ListenAndServe())
}
