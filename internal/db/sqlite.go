package db

import (
	"database/sql"
	"fmt"

	"github.com/ideasmus/go-filehasher/internal/models"
	_ "modernc.org/sqlite"
)

type DB struct {
	db *sql.DB
}

func New(dbPath string) (*DB, error) {
	// Add dsn parameters for busy timeout and other settings
	dsn := fmt.Sprintf("%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite db: %w", err)
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS entries (
			path TEXT PRIMARY KEY,
			hash TEXT,
			size INTEGER,
			mtime DATETIME,
			is_dir BOOLEAN
		);
		CREATE INDEX IF NOT EXISTS idx_mtime ON entries(mtime);
	`); err != nil {
		return nil, fmt.Errorf("failed to create tables: %w", err)
	}

	return &DB{db: db}, nil
}

func (d *DB) Close() error {
	return d.db.Close()
}

func (d *DB) UpsertFile(f models.FileInfo) error {
	return d.UpsertFileTx(nil, f)
}

func (d *DB) UpsertFileTx(tx *sql.Tx, f models.FileInfo) error {
	query := `
		INSERT INTO entries (path, hash, size, mtime, is_dir)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET
			hash = EXCLUDED.hash,
			size = EXCLUDED.size,
			mtime = EXCLUDED.mtime,
			is_dir = EXCLUDED.is_dir
	`
	var err error
	if tx != nil {
		_, err = tx.Exec(query, f.Path, f.Hash, f.Size, f.Mtime, f.IsDir)
	} else {
		_, err = d.db.Exec(query, f.Path, f.Hash, f.Size, f.Mtime, f.IsDir)
	}
	return err
}

func (d *DB) Begin() (*sql.Tx, error) {
	return d.db.Begin()
}

func (d *DB) GetFileInfo(path string) (*models.FileInfo, error) {
	var f models.FileInfo
	err := d.db.QueryRow("SELECT path, hash, size, mtime, is_dir FROM entries WHERE path = ?", path).
		Scan(&f.Path, &f.Hash, &f.Size, &f.Mtime, &f.IsDir)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

func (d *DB) DeleteFile(path string) error {
	return d.DeleteFileTx(nil, path)
}

func (d *DB) DeleteFileTx(tx *sql.Tx, path string) error {
	var err error
	if tx != nil {
		_, err = tx.Exec("DELETE FROM entries WHERE path = ?", path)
	} else {
		_, err = d.db.Exec("DELETE FROM entries WHERE path = ?", path)
	}
	return err
}

func (d *DB) GetAllPaths() (map[string]models.FileInfo, error) {
	rows, err := d.db.Query("SELECT path, hash, mtime, size, is_dir FROM entries")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	entries := make(map[string]models.FileInfo)
	for rows.Next() {
		var f models.FileInfo
		if err := rows.Scan(&f.Path, &f.Hash, &f.Mtime, &f.Size, &f.IsDir); err != nil {
			return nil, err
		}
		entries[f.Path] = f
	}
	return entries, nil
}
