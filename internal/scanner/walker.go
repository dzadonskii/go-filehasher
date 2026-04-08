package scanner

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"strings"

	"github.com/dzadonskii/go-filehasher/internal/db"
	"github.com/dzadonskii/go-filehasher/internal/hasher"
	"github.com/dzadonskii/go-filehasher/internal/merkle"
	"github.com/dzadonskii/go-filehasher/internal/models"
)

type ScanStats struct {
	Added     int
	Updated   int
	Deleted   int
	Unchanged int
}

type Scanner struct {
	db              *db.DB
	root            string
	BatchSize       int
	CommitThreshold int
	hashedCount     int
	stats           ScanStats
	tx              *sql.Tx
	txCount         int
	Out             io.Writer
	LimitPath       string
}

func New(database *db.DB, root string, batchSize int, commitThreshold int) *Scanner {
	absRoot, err := filepath.Abs(root)
	if err == nil {
		root = absRoot
	}
	if !strings.HasSuffix(root, string(os.PathSeparator)) {
		root += string(os.PathSeparator)
	}
	if commitThreshold <= 0 {
		commitThreshold = 1000
	}
	return &Scanner{
		db:              database,
		root:            root,
		BatchSize:       batchSize,
		CommitThreshold: commitThreshold,
		Out:             os.Stdout,
	}
}

func (s *Scanner) log(format string, a ...any) {
	if s.Out == nil {
		s.Out = os.Stdout
	}
	ts := time.Now().Format("2006-01-02 15:04:05")
	fmt.Fprintf(s.Out, "[%s] "+format, append([]any{ts}, a...)...)
}

func (s *Scanner) rel(path string) string {
	rel, err := filepath.Rel(s.root, path)
	if err != nil {
		return path
	}
	return rel
}

func (s *Scanner) beginTx() error {
	if s.tx != nil {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	s.tx = tx
	s.txCount = 0
	return nil
}

func (s *Scanner) commitTx() error {
	if s.tx == nil {
		return nil
	}
	err := s.tx.Commit()
	s.tx = nil
	s.txCount = 0
	if err == nil {
		_ = s.db.Checkpoint()
	}
	return err
}

func (s *Scanner) rollbackTx() {
	if s.tx != nil {
		_ = s.tx.Rollback()
		s.tx = nil
		s.txCount = 0
	}
}

func (s *Scanner) commitIfNeeded() error {
	s.txCount++
	if s.txCount >= s.CommitThreshold {
		if err := s.commitTx(); err != nil {
			return err
		}
		return s.beginTx()
	}
	return nil
}

func (s *Scanner) Cleanup(ctx context.Context) (int, error) {
	if err := s.beginTx(); err != nil {
		return 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer s.rollbackTx()

	deletedCount := 0
	err := s.db.IterateEntries(func(f models.FileInfo) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		relPath := f.Path
		// 1. If path starts with / (old absolute format), delete it
		if strings.HasPrefix(relPath, "/") {
			if err := s.db.DeleteFileTx(s.tx, relPath); err != nil {
				return fmt.Errorf("failed to delete old entry %s: %w", relPath, err)
			}
			s.log("Deleted (old format): %s\n", relPath)
			deletedCount++
			return s.commitIfNeeded()
		}

		// 2. Check if file exists on disk relative to root
		fullPath := filepath.Join(s.root, relPath)

		if s.LimitPath != "" {
			absLimit, _ := filepath.Abs(s.LimitPath)
			if !filepath.IsAbs(s.LimitPath) {
				absLimit = filepath.Join(s.root, s.LimitPath)
			}
			if !strings.HasPrefix(fullPath, absLimit) {
				return nil // Skip entries outside limited path
			}
		}

		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			if err := s.db.DeleteFileTx(s.tx, relPath); err != nil {
				return fmt.Errorf("failed to delete missing entry %s: %w", relPath, err)
			}
			s.log("Deleted (missing): %s\n", relPath)
			deletedCount++
			return s.commitIfNeeded()
		}
		return nil
	})

	if err != nil {
		_ = s.commitTx() // Commit what we deleted so far
		return deletedCount, err
	}

	err = s.commitTx()
	return deletedCount, err
}

func (s *Scanner) Scan(ctx context.Context) (ScanStats, error) {
	s.hashedCount = 0 // Reset for each scan
	s.stats = ScanStats{}

	if err := s.beginTx(); err != nil {
		return s.stats, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer s.rollbackTx()

	// 1. Start recursive scan from start point
	startPath := s.root
	if s.LimitPath != "" {
		startPath = s.LimitPath
		if !filepath.IsAbs(startPath) {
			startPath = filepath.Join(s.root, startPath)
		}
	}

	_, err := s.scanDir(ctx, startPath)
	if err != nil {
		_ = s.commitTx() // Commit progress even if interrupted
		return s.stats, err
	}

	err = s.commitTx()
	return s.stats, err
}

func (s *Scanner) scanDir(ctx context.Context, dirPath string) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return "", fmt.Errorf("failed to read dir %s: %w", dirPath, err)
	}

	var merkleEntries []merkle.Entry
	fullyScanned := true

	relDirPath := s.rel(dirPath)

	for _, d := range entries {
		select {
		case <-ctx.Done():
			fullyScanned = false
			return "", ctx.Err()
		default:
		}
		if s.BatchSize > 0 && s.hashedCount >= s.BatchSize {
			fullyScanned = false
			break // Stop walking once batch size reached
		}

		fullPath := filepath.Join(dirPath, d.Name())
		relPath := s.rel(fullPath)
		info, err := d.Info()
		if err != nil {
			continue // Skip files we can't access
		}

		var currentHash string
		if d.IsDir() {
			hash, err := s.scanDir(ctx, fullPath)
			if err != nil {
				return "", err
			}
			if hash == "" {
				fullyScanned = false
			}
			currentHash = hash
		} else {
			// Check if we need to re-hash
			known, err := s.db.GetFileInfo(relPath)
			if err != nil {
				s.log("Error checking %s in DB: %v\n", relPath, err)
			}

			if known == nil || !known.Mtime.Equal(info.ModTime()) || known.Size != info.Size() {
				// Check batch size
				if s.BatchSize > 0 && s.hashedCount >= s.BatchSize {
					currentHash = "" // Partial, don't update dir hash
					fullyScanned = false
				} else {
					hash, err := hasher.HashFile(ctx, fullPath)
					if err != nil {
						s.log("Error hashing %s: %v\n", relPath, err)
						continue
					}
					s.hashedCount++
					currentHash = hash

					if err := s.beginTx(); err != nil {
						return "", err
					}

					if err := s.db.UpsertFileTx(s.tx, models.FileInfo{
						Path:  relPath,
						Hash:  hash,
						Size:  info.Size(),
						Mtime: info.ModTime(),
						IsDir: false,
					}); err != nil {
						return "", err
					}
					if known == nil {
						s.log("Added: %s (%s)\n", relPath, hash)
						s.stats.Added++
					} else {
						s.log("Updated: %s (%s)\n", relPath, hash)
						s.stats.Updated++
					}
					if err := s.commitIfNeeded(); err != nil {
						return "", err
					}
				}
			} else {
				s.stats.Unchanged++
				currentHash = known.Hash
			}
		}

		if currentHash != "" {
			merkleEntries = append(merkleEntries, merkle.Entry{Name: d.Name(), Hash: currentHash})
		} else {
			fullyScanned = false
		}
	}

	dirHash := ""
	if fullyScanned {
		dirHash = merkle.CalculateDirHash(merkleEntries)
		// Update directory entry in DB
		info, err := os.Stat(dirPath)
		if err == nil {
			known, _ := s.db.GetFileInfo(relDirPath)
			if known == nil || known.Hash != dirHash {
				if err := s.beginTx(); err != nil {
					return "", err
				}
				if err := s.db.UpsertFileTx(s.tx, models.FileInfo{
					Path:  relDirPath,
					Hash:  dirHash,
					Mtime: info.ModTime(),
					IsDir: true,
				}); err == nil {
					if known == nil {
						s.log("Added: %s (dir) (%s)\n", relDirPath, dirHash)
						s.stats.Added++
					} else {
						s.log("Updated: %s (dir) (%s)\n", relDirPath, dirHash)
						s.stats.Updated++
					}
					_ = s.commitIfNeeded()
				}
			} else {
				s.stats.Unchanged++
			}
		}
	}

	return dirHash, nil
}
