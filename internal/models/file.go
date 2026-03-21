package models

import "time"

type FileInfo struct {
	Path      string
	Hash      string
	Size      int64
	Mtime     time.Time
	UpdatedAt time.Time
	IsDir     bool
}
