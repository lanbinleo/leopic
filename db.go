package main

import (
	"database/sql"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS images (
  id          INTEGER PRIMARY KEY,
  key         TEXT    NOT NULL UNIQUE,
  path        TEXT    NOT NULL,
  name        TEXT    NOT NULL,
  origin_name TEXT    NOT NULL DEFAULT '',
  size        INTEGER NOT NULL,
  mimetype    TEXT    NOT NULL,
  extension   TEXT    NOT NULL,
  md5         TEXT    NOT NULL,
  sha1        TEXT    NOT NULL,
  sha256      TEXT    NOT NULL UNIQUE,
  width       INTEGER NOT NULL DEFAULT 0,
  height      INTEGER NOT NULL DEFAULT 0,
  webp_size   INTEGER NOT NULL DEFAULT 0,
  created_at  INTEGER NOT NULL,
  UNIQUE (path, name)
);
CREATE INDEX IF NOT EXISTS images_created ON images(created_at);

CREATE TABLE IF NOT EXISTS tokens (
  id           INTEGER PRIMARY KEY,
  name         TEXT    NOT NULL DEFAULT '',
  token_hash   TEXT    NOT NULL UNIQUE,
  created_at   INTEGER NOT NULL,
  last_used_at INTEGER
);

CREATE TABLE IF NOT EXISTS sessions (
  id_hash    TEXT PRIMARY KEY,
  expires_at INTEGER NOT NULL
);
`

func openDB(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

type Image struct {
	ID         int64
	Key        string
	Path       string
	Name       string
	OriginName string
	Size       int64
	Mimetype   string
	Extension  string
	MD5        string
	SHA1       string
	SHA256     string
	Width      int
	Height     int
	WebpSize   int64
	CreatedAt  int64
}

const imageCols = "id,key,path,name,origin_name,size,mimetype,extension,md5,sha1,sha256,width,height,webp_size,created_at"

type scanner interface{ Scan(...any) error }

func scanImage(sc scanner) (*Image, error) {
	var m Image
	err := sc.Scan(&m.ID, &m.Key, &m.Path, &m.Name, &m.OriginName, &m.Size, &m.Mimetype, &m.Extension,
		&m.MD5, &m.SHA1, &m.SHA256, &m.Width, &m.Height, &m.WebpSize, &m.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// Pathname 是相对上传根目录的路径，与 lsky 的 pathname 一致。
func (m *Image) Pathname() string {
	if m.Path == "" {
		return m.Name
	}
	return m.Path + "/" + m.Name
}
