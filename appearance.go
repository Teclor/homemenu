package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const maxFontBytes = 8 << 20

var colorRoles = []string{
	"bg", "surface", "header", "ink", "muted", "line", "accent", "on", "soft",
	"link", "focus", "button", "button-ink", "danger", "shadow", "backdrop", "qr",
}

var fontTypes = map[string]string{
	".woff": "font/woff", ".woff2": "font/woff2", ".ttf": "font/ttf", ".otf": "font/otf",
}

type appearanceConfig struct {
	Light     map[string]string `json:"light"`
	Dark      map[string]string `json:"dark"`
	Font      string            `json:"font"`
	FontFile  string            `json:"font_file,omitempty"`
	FontName  string            `json:"font_name,omitempty"`
	Title     *string           `json:"title,omitempty"`
	PhotoNote *string           `json:"photo_note,omitempty"`
}

func validPalette(palette map[string]string) bool {
	if len(palette) != len(colorRoles) {
		return false
	}
	for _, role := range colorRoles {
		color := palette[role]
		if len(color) != 7 || color[0] != '#' || strings.Trim(color[1:], "0123456789abcdefABCDEF") != "" {
			return false
		}
	}
	return true
}

func (s server) uploadFont(r *http.Request) (string, string, error) {
	files := r.MultipartForm.File["font_file"]
	if len(files) == 0 || files[0].Filename == "" {
		return "", "", nil
	}
	file := files[0]
	if file.Size > maxFontBytes {
		return "", "", errors.New("Максимальный размер шрифта — 8 МиБ.")
	}
	source, err := file.Open()
	if err != nil {
		return "", "", err
	}
	defer source.Close()
	var signature [4]byte
	if _, err = io.ReadFull(source, signature[:]); err != nil {
		return "", "", errors.New("Файл шрифта не удалось прочитать.")
	}
	extensions := map[string]string{"wOFF": ".woff", "wOF2": ".woff2", "\x00\x01\x00\x00": ".ttf", "true": ".ttf", "OTTO": ".otf"}
	ext := extensions[string(signature[:])]
	if ext == "" {
		return "", "", errors.New("Загрузите файл WOFF2, WOFF, TTF или OTF.")
	}
	if _, err = source.Seek(0, io.SeekStart); err != nil {
		return "", "", err
	}
	output, err := os.CreateTemp(s.fonts, "font-*"+ext)
	if err != nil {
		return "", "", err
	}
	_, err = io.Copy(output, source)
	closeErr := output.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(output.Name())
		return "", "", err
	}
	return filepath.Base(output.Name()), file.Filename, nil
}

func (s server) deleteFont(name string) {
	if name != "" {
		os.Remove(filepath.Join(s.fonts, name))
	}
}

func (s server) saveAppearance(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFontBytes+(256<<10))
	if err := r.ParseMultipartForm(128 << 10); err != nil {
		problem(w, 400, "Проверьте форму. Максимальный размер шрифта — 8 МиБ.")
		return
	}
	defer r.MultipartForm.RemoveAll()

	var config appearanceConfig
	if err := json.Unmarshal([]byte(r.FormValue("appearance")), &config); err != nil ||
		!validPalette(config.Light) || !validPalette(config.Dark) {
		problem(w, 400, "Проверьте цвета светлой и тёмной темы.")
		return
	}
	switch config.Font {
	case "system", "serif", "mono", "custom":
	default:
		problem(w, 400, "Выберите шрифт.")
		return
	}
	config.FontFile, config.FontName = "", ""
	if config.Title != nil {
		*config.Title = strings.TrimSpace(*config.Title)
		if *config.Title == "" || len([]rune(*config.Title)) > 100 {
			problem(w, 400, "Название должно содержать от 1 до 100 символов.")
			return
		}
	}
	if config.PhotoNote != nil {
		*config.PhotoNote = strings.TrimSpace(*config.PhotoNote)
		if len([]rune(*config.PhotoNote)) > 300 {
			problem(w, 400, "Подпись о фотографиях должна содержать не более 300 символов.")
			return
		}
	}

	uploaded, originalName, err := s.uploadFont(r)
	if err != nil {
		problem(w, 400, err.Error())
		return
	}
	committed := false
	defer func() {
		if !committed {
			s.deleteFont(uploaded)
		}
	}()

	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		internal(w, err)
		return
	}
	defer tx.Rollback()
	var previousConfig string
	err = tx.QueryRow("SELECT coalesce((SELECT config FROM appearance WHERE id=1),'{}')").Scan(&previousConfig)
	if err != nil {
		internal(w, err)
		return
	}
	var previous appearanceConfig
	if err = json.Unmarshal([]byte(previousConfig), &previous); err != nil {
		internal(w, err)
		return
	}
	if config.Title == nil {
		config.Title = previous.Title
	}
	if config.PhotoNote == nil {
		config.PhotoNote = previous.PhotoNote
	}
	switch {
	case uploaded != "":
		config.FontFile, config.FontName = uploaded, originalName
	case r.FormValue("remove_font") == "":
		config.FontFile, config.FontName = previous.FontFile, previous.FontName
	}
	if config.Font == "custom" && config.FontFile == "" {
		problem(w, 400, "Загрузите свой шрифт или выберите встроенный.")
		return
	}
	raw, err := json.Marshal(config)
	if err != nil {
		internal(w, err)
		return
	}
	_, err = tx.Exec("INSERT INTO appearance(id,config) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET config=excluded.config", string(raw))
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		internal(w, err)
		return
	}
	committed = true
	if previous.FontFile != config.FontFile {
		s.deleteFont(previous.FontFile)
	}
	send(w, 200, config)
}

func (s server) serveFont(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/fonts/")
	contentType := fontTypes[filepath.Ext(name)]
	if contentType == "" || filepath.Base(name) != name || !strings.HasPrefix(name, "font-") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeFile(w, r, filepath.Join(s.fonts, name))
}
