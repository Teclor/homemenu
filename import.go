package main

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type importName struct {
	Name string `json:"name"`
}

type importSection struct {
	importName
	Position *int32 `json:"position"`
}

type importItem struct {
	importName
	Section     *string   `json:"section"`
	Quantity    *int32    `json:"quantity"`
	Position    *int32    `json:"position"`
	Place       *string   `json:"place"`
	Alignment   *string   `json:"description_align"`
	Description *string   `json:"description"`
	Tags        *[]string `json:"tags"`
	Photo       *string   `json:"photo_base64"`
}

type importFile struct {
	Sections []importSection `json:"sections"`
	Tags     []importName    `json:"tags"`
	Places   []importName    `json:"places"`
	Items    []importItem    `json:"items"`
}

type importCounts struct {
	Created int `json:"created"`
	Updated int `json:"updated"`
}

type importIndex struct {
	ids  map[string]int64
	seen map[string]bool
}

func nameKey(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

func loadImportIndex(tx *sql.Tx, table string) (importIndex, error) {
	index := importIndex{make(map[string]int64), make(map[string]bool)}
	rows, err := tx.Query("SELECT id, name FROM " + table)
	if err != nil {
		return index, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return index, err
		}
		key := nameKey(name)
		if _, exists := index.ids[key]; exists {
			id = -1
		}
		index.ids[key] = id
	}
	return index, rows.Err()
}

func (index importIndex) record(name string) (int64, error) {
	key := nameKey(name)
	if key == "" || len([]rune(strings.TrimSpace(name))) > 160 {
		return 0, fmt.Errorf("название должно содержать от 1 до 160 символов")
	}
	if index.seen[key] {
		return 0, fmt.Errorf("название %q повторяется в файле", name)
	}
	index.seen[key] = true
	id := index.ids[key]
	if id < 0 {
		return 0, fmt.Errorf("в базе несколько записей с названием %q. Уточните названия в админпанели", name)
	}
	return id, nil
}

func (index importIndex) reference(name string) (int64, error) {
	id := index.ids[nameKey(name)]
	if id < 0 {
		return 0, fmt.Errorf("в базе несколько записей с названием %q", name)
	}
	if id == 0 {
		return 0, fmt.Errorf("запись %q отсутствует в справочнике", name)
	}
	return id, nil
}

func nextImportPosition(tx *sql.Tx, query string, args ...interface{}) (int64, error) {
	var position int64
	err := tx.QueryRow(query, args...).Scan(&position)
	return position, err
}

func importDirectory(tx *sql.Tx, table string, index importIndex, records []importSection, counts *importCounts) error {
	for i, record := range records {
		if err := importDirectoryRecord(tx, table, index, record, counts); err != nil {
			return fmt.Errorf("%s[%d]: %w", table, i, err)
		}
	}
	return nil
}

func importDirectoryRecord(tx *sql.Tx, table string, index importIndex, record importSection, counts *importCounts) error {
	id, err := index.record(record.Name)
	if err != nil {
		return err
	}
	name := strings.TrimSpace(record.Name)
	if id > 0 {
		query, args := "UPDATE "+table+" SET name=? WHERE id=?", []interface{}{name, id}
		if record.Position != nil {
			query, args = "UPDATE sections SET name=?, position=? WHERE id=?", []interface{}{name, *record.Position, id}
		}
		if _, err := tx.Exec(query, args...); err != nil {
			return err
		}
		counts.Updated++
		return nil
	}
	query, args := "INSERT INTO "+table+"(name) VALUES(?)", []interface{}{name}
	if table == "sections" {
		var position int64
		if record.Position != nil {
			position = int64(*record.Position)
		} else {
			position, err = nextImportPosition(tx, "SELECT coalesce(max(position),0)+10 FROM sections")
			if err != nil {
				return err
			}
		}
		query, args = "INSERT INTO sections(name,position) VALUES(?,?)", []interface{}{name, position}
	}
	result, err := tx.Exec(query, args...)
	if err != nil {
		return err
	}
	id, err = result.LastInsertId()
	if err != nil {
		return err
	}
	index.ids[nameKey(name)] = id
	counts.Created++
	return nil
}

func directories(records []importName) []importSection {
	result := make([]importSection, 0, len(records))
	for _, record := range records {
		result = append(result, importSection{importName: record})
	}
	return result
}

type catalogImport struct {
	tx        *sql.Tx
	indexes   map[string]importIndex
	counts    importCounts
	newPhotos []string
	oldPhotos []string
}

func (s server) importCatalog(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 25<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var input importFile
	if err := decoder.Decode(&input); err != nil {
		problem(w, 400, "Ошибка чтения JSON (лимит 25 МиБ): "+err.Error())
		return
	}
	if err := decoder.Decode(new(json.RawMessage)); err != io.EOF {
		problem(w, 400, "Файл должен содержать один объект JSON.")
		return
	}
	if len(input.Items)+len(input.Tags)+len(input.Sections)+len(input.Places) == 0 {
		problem(w, 400, "В файле нет записей для импорта.")
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		internal(w, err)
		return
	}
	defer tx.Rollback()
	imp := catalogImport{tx: tx, indexes: make(map[string]importIndex)}
	committed := false
	defer func() {
		cleanup := imp.newPhotos
		if committed {
			cleanup = imp.oldPhotos
		}
		for _, name := range cleanup {
			s.deletePhoto(name)
		}
	}()
	for _, table := range []string{"sections", "tags", "places", "items"} {
		index, err := loadImportIndex(tx, table)
		if err != nil {
			internal(w, err)
			return
		}
		imp.indexes[table] = index
	}
	for _, directory := range []struct {
		table   string
		records []importSection
	}{
		{"sections", input.Sections}, {"tags", directories(input.Tags)}, {"places", directories(input.Places)},
	} {
		if err := importDirectory(tx, directory.table, imp.indexes[directory.table], directory.records, &imp.counts); err != nil {
			problem(w, 400, err.Error())
			return
		}
	}
	for i, item := range input.Items {
		if err := s.importItem(&imp, item); err != nil {
			problem(w, 400, fmt.Sprintf("items[%d] (%q): %v", i, item.Name, err))
			return
		}
	}
	if err := tx.Commit(); err != nil {
		internal(w, err)
		return
	}
	committed = true
	send(w, 200, imp.counts)
}

func (s server) importItem(imp *catalogImport, item importItem) error {
	tx := imp.tx
	id, err := imp.indexes["items"].record(item.Name)
	if err != nil {
		return err
	}
	var section, quantity, position int64
	var place sql.NullInt64
	var description, photo string
	alignment := "center"
	if id != 0 {
		err = tx.QueryRow("SELECT section,quantity,position,place,description,description_align,photo FROM items WHERE id=?", id).
			Scan(&section, &quantity, &position, &place, &description, &alignment, &photo)
		if err != nil {
			return err
		}
	} else if item.Section == nil || item.Quantity == nil {
		return fmt.Errorf("укажите section и quantity для новой позиции")
	}
	previousSection := section
	if item.Section != nil {
		section, err = imp.indexes["sections"].reference(*item.Section)
		if err != nil {
			return err
		}
	}
	if item.Quantity != nil {
		if *item.Quantity < 0 {
			return fmt.Errorf("quantity должно быть целым числом от нуля")
		}
		quantity = int64(*item.Quantity)
	}
	if item.Position != nil {
		position = int64(*item.Position)
	} else if id == 0 || section != previousSection {
		position, err = nextImportPosition(tx, "SELECT coalesce(max(position),0)+10 FROM items WHERE section=?", section)
		if err != nil {
			return err
		}
	}
	if item.Place != nil {
		place = sql.NullInt64{}
		if strings.TrimSpace(*item.Place) != "" {
			place.Int64, err = imp.indexes["places"].reference(*item.Place)
			if err != nil {
				return err
			}
			place.Valid = true
		}
	}
	if item.Description != nil {
		description = *item.Description
	}
	if item.Alignment != nil {
		alignment = *item.Alignment
		if alignment != "center" && alignment != "left" {
			return fmt.Errorf("description_align должно быть center или left")
		}
	}
	if item.Photo != nil {
		imp.oldPhotos = append(imp.oldPhotos, photo)
		photo = ""
		if *item.Photo != "" {
			content, err := base64.StdEncoding.DecodeString(*item.Photo)
			if err != nil {
				return fmt.Errorf("photo_base64 должно содержать Base64 фотографии")
			}
			photo, err = s.storePhoto(bytes.NewReader(content))
			if err != nil {
				return err
			}
			imp.newPhotos = append(imp.newPhotos, photo)
		}
	}
	values := []interface{}{strings.TrimSpace(item.Name), section, quantity, position, place, description, alignment, photo}
	if id == 0 {
		result, err := tx.Exec("INSERT INTO items(name,section,quantity,position,place,description,description_align,photo) VALUES(?,?,?,?,?,?,?,?)", values...)
		if err != nil {
			return err
		}
		id, err = result.LastInsertId()
		if err != nil {
			return err
		}
		imp.counts.Created++
	} else {
		_, err = tx.Exec("UPDATE items SET name=?,section=?,quantity=?,position=?,place=?,description=?,description_align=?,photo=? WHERE id=?", append(values, id)...)
		if err != nil {
			return err
		}
		imp.counts.Updated++
	}
	if item.Tags == nil {
		return nil
	}
	if _, err := tx.Exec("DELETE FROM item_tags WHERE item=?", id); err != nil {
		return err
	}
	for _, name := range *item.Tags {
		tag, err := imp.indexes["tags"].reference(name)
		if err != nil {
			return err
		}
		if _, err := tx.Exec("INSERT INTO item_tags(item,tag) VALUES(?,?) ON CONFLICT(item,tag) DO NOTHING", id, tag); err != nil {
			return err
		}
	}
	return nil
}
