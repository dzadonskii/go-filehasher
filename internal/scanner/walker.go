package scanner

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"strings"

	"github.com/ideasmus/go-filehasher/internal/db"
	"github.com/ideasmus/go-filehasher/internal/hasher"
	"github.com/ideasmus/go-filehasher/internal/merkle"
	"github.com/ideasmus/go-filehasher/internal/models"
)

type ScanStats struct {
	Added     int
	Updated   int
	Deleted   int
	Unchanged int
}

type Scanner struct {
	db          *db.DB
	root        string
	BatchSize   int
	hashedCount int
	stats       ScanStats
}

func New(database *db.DB, root string, batchSize int) *Scanner {
	absRoot, err := filepath.Abs(root)
	if err == nil {
		root = absRoot
	}
	if !strings.HasSuffix(root, string(os.PathSeparator)) {
		root += string(os.PathSeparator)
	}
	return &Scanner{
		db:        database,
		root:      root,
		BatchSize: batchSize,
	}
}

func (s *Scanner) rel(path string) string {
	rel, err := filepath.Rel(s.root, path)
	if err != nil {
		return path
	}
	return rel
}

func (s *Scanner) Scan() (ScanStats, error) {
	s.hashedCount = 0 // Reset for each scan
	s.stats = ScanStats{}
	// 1. Get all known entries from DB to detect deletions later
	relKnownEntries, err := s.db.GetAllPaths()
	if err != nil {
		return s.stats, fmt.Errorf("failed to get known paths: %w", err)
	}

	// Convert relative known entries back to absolute for easier lookup during scan
	knownEntries := make(map[string]models.FileInfo)
	for relPath, info := range relKnownEntries {
		absPath := filepath.Join(s.root, relPath)
		info.Path = absPath
		knownEntries[absPath] = info
	}

	foundPaths := make(map[string]struct{})

	tx, err := s.db.Begin()
	if err != nil {
		return s.stats, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// 2. Start recursive scan from root
	_, err = s.scanDirTx(tx, s.root, knownEntries, foundPaths)
	if err != nil {
		return s.stats, err
	}

	// 3. Detect deletions
	for absPath := range knownEntries {
		if _, found := foundPaths[absPath]; !found {
			relPath := s.rel(absPath)
			if err := s.db.DeleteFileTx(tx, relPath); err != nil {
				return s.stats, fmt.Errorf("failed to delete missing entry %s: %w", absPath, err)
			}
			fmt.Printf("Deleted: %s\n", relPath)
			s.stats.Deleted++
		}
	}

	err = tx.Commit()
	return s.stats, err
}

func (s *Scanner) scanDirTx(tx *sql.Tx, dirPath string, knownEntries map[string]models.FileInfo, foundPaths map[string]struct{}) (string, error) {
	foundPaths[dirPath] = struct{}{}

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return "", fmt.Errorf("failed to read dir %s: %w", dirPath, err)
	}

	var merkleEntries []merkle.Entry
	fullyScanned := true

	for _, d := range entries {
		fullPath := filepath.Join(dirPath, d.Name())
		info, err := d.Info()
		if err != nil {
			continue // Skip files we can't access
		}

		var currentHash string
		if d.IsDir() {
			hash, err := s.scanDirTx(tx, fullPath, knownEntries, foundPaths)
			if err != nil {
				return "", err
			}
			if hash == "" {
				fullyScanned = false
			}
			currentHash = hash
		} else {
			foundPaths[fullPath] = struct{}{}

			// Check if we need to re-hash
			known, exists := knownEntries[fullPath]
			if !exists || !known.Mtime.Equal(info.ModTime()) || known.Size != info.Size() {
				// Check batch size
				if s.BatchSize > 0 && s.hashedCount >= s.BatchSize {
					currentHash = "" // Partial, don't update dir hash
					fullyScanned = false
				} else {
					hash, err := hasher.HashFile(fullPath)
					if err != nil {
						fmt.Printf("Error hashing %s: %v\n", s.rel(fullPath), err)
						continue
					}
					s.hashedCount++
					currentHash = hash

					err = s.db.UpsertFileTx(tx, models.FileInfo{
						Path:  s.rel(fullPath),
						Hash:  currentHash,
						Size:  info.Size(),
						Mtime: info.ModTime(),
						IsDir: false,
					})
					if err != nil {
						return "", fmt.Errorf("failed to upsert %s: %w", fullPath, err)
					}

					if exists {
						fmt.Printf("Updated: %s\n", s.rel(fullPath))
						s.stats.Updated++
					} else {
						fmt.Printf("Added: %s\n", s.rel(fullPath))
						s.stats.Added++
					}
				}
			} else {
				currentHash = known.Hash
				s.stats.Unchanged++
			}
		}

		if currentHash != "" {
			merkleEntries = append(merkleEntries, merkle.Entry{
				Name: d.Name(),
				Hash: currentHash,
			})
		}
	}

	// Calculate own Merkle hash if everything below was scanned
	var dirHash string
	if fullyScanned {
		dirHash = merkle.CalculateDirHash(merkleEntries)

		// Update dir in DB if hash changed
		known, exists := knownEntries[dirPath]
		if !exists || known.Hash != dirHash {
			info, err := os.Stat(dirPath)
			var mtime time.Time
			if err == nil {
				mtime = info.ModTime()
			}
			err = s.db.UpsertFileTx(tx, models.FileInfo{
				Path:  s.rel(dirPath),
				Hash:  dirHash,
				Mtime: mtime,
				IsDir: true,
			})
			if err != nil {
				return "", fmt.Errorf("failed to upsert dir %s: %w", dirPath, err)
			}
			if exists {
				fmt.Printf("Updated: %s (dir)\n", s.rel(dirPath))
				s.stats.Updated++
			} else {
				fmt.Printf("Added: %s (dir)\n", s.rel(dirPath))
				s.stats.Added++
			}
		} else {
			s.stats.Unchanged++
		}
	}

	return dirHash, nil
}
