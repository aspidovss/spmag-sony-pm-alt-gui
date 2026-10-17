// Package store remembers which camera files have been received, so that
// the next connection can tell received files from missing ones.
package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Record is one received file.
type Record struct {
	Key        string    `json:"key"`
	Name       string    `json:"name"`
	Size       uint64    `json:"size"`
	Captured   string    `json:"captured"` // PTP capture date string as reported by the camera
	Camera     string    `json:"camera"`   // model + serial
	LocalPath  string    `json:"local_path"`
	ReceivedAt time.Time `json:"received_at"`
}

// Key identifies a camera file. File numbers (DSC00001...) repeat after
// 9999 or when the counter is reset, so the capture time and size are part
// of the key.
func Key(name string, size uint64, captured string) string {
	return strings.ToUpper(name) + "|" + strconv.FormatUint(size, 10) + "|" + captured
}

// CameraKey is Key scoped to one camera (its model and serial number), so that
// two cameras whose file names, sizes and capture times happen to coincide do
// not shadow each other. Without a camera identity it is the plain Key.
func CameraKey(camera, name string, size uint64, captured string) string {
	camera = strings.TrimSpace(camera)
	if camera == "" {
		return Key(name, size, captured)
	}
	return camera + "#" + Key(name, size, captured)
}

// DB is a JSON-backed set of records.
type DB struct {
	mu    sync.Mutex
	path  string
	byKey map[string]*Record
	dirty bool
}

// Open loads (or creates) the database file.
func Open(path string) (*DB, error) {
	db := &DB{path: path, byKey: map[string]*Record{}}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return db, nil
		}
		return nil, err
	}
	var recs []*Record
	if err := json.Unmarshal(b, &recs); err != nil {
		// keep a copy of the damaged file and start fresh
		_ = os.Rename(path, path+".corrupt-"+time.Now().Format("20060102-150405"))
		return db, nil
	}
	for _, r := range recs {
		db.byKey[r.Key] = r
	}
	return db, nil
}

// Get returns the record for key.
func (db *DB) Get(key string) (Record, bool) {
	db.mu.Lock()
	defer db.mu.Unlock()
	r, ok := db.byKey[key]
	if !ok {
		return Record{}, false
	}
	return *r, true
}

// Put adds or replaces a record.
func (db *DB) Put(r Record) {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.byKey[r.Key] = &r
	db.dirty = true
}

// Delete removes a record ("mark as not received").
func (db *DB) Delete(key string) {
	db.mu.Lock()
	defer db.mu.Unlock()
	if _, ok := db.byKey[key]; ok {
		delete(db.byKey, key)
		db.dirty = true
	}
}

// Len returns the number of records.
func (db *DB) Len() int {
	db.mu.Lock()
	defer db.mu.Unlock()
	return len(db.byKey)
}

// Save writes the database if it changed.
func (db *DB) Save() error {
	db.mu.Lock()
	if !db.dirty {
		db.mu.Unlock()
		return nil
	}
	recs := make([]*Record, 0, len(db.byKey))
	for _, r := range db.byKey {
		recs = append(recs, r)
	}
	db.dirty = false
	db.mu.Unlock()
	sort.Slice(recs, func(i, j int) bool { return recs[i].Key < recs[j].Key })
	b, err := json.MarshalIndent(recs, "", " ")
	if err != nil {
		return err
	}
	_ = os.MkdirAll(filepath.Dir(db.path), 0o755)
	tmp := db.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, db.path)
}
