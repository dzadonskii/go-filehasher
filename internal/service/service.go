package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ideasmus/go-filehasher/internal/db"
	"github.com/ideasmus/go-filehasher/internal/hasher"
	"github.com/ideasmus/go-filehasher/internal/models"
	"github.com/ideasmus/go-filehasher/internal/scanner"
	"github.com/ideasmus/go-filehasher/internal/watcher"
)

type Config struct {
	RootPath     string
	DBPath       string
	ScanInterval time.Duration
	BatchSize    int
}

type Service struct {
	cfg     Config
	db      *db.DB
	scanner *scanner.Scanner
	watcher *watcher.Watcher
}

func New(cfg Config) (*Service, error) {
	absRoot, err := filepath.Abs(cfg.RootPath)
	if err == nil {
		cfg.RootPath = absRoot
	}
	if !strings.HasSuffix(cfg.RootPath, string(os.PathSeparator)) {
		cfg.RootPath += string(os.PathSeparator)
	}

	database, err := db.New(cfg.DBPath)
	if err != nil {
		return nil, err
	}

	w, err := watcher.New(cfg.RootPath)
	if err != nil {
		return nil, err
	}

	s := scanner.New(database, cfg.RootPath, cfg.BatchSize)

	return &Service{
		cfg:     cfg,
		db:      database,
		scanner: s,
		watcher: w,
	}, nil
}

func (s *Service) Run(ctx context.Context) error {
	// 1. Initial scan
	fmt.Printf("Starting initial scan of %s...\n", s.cfg.RootPath)
	stats, err := s.scanner.Scan()
	if err != nil {
		return fmt.Errorf("initial scan failed: %w", err)
	}
	fmt.Printf("Initial scan complete. Summary: Added: %d, Updated: %d, Deleted: %d, Unchanged: %d\n",
		stats.Added, stats.Updated, stats.Deleted, stats.Unchanged)

	// 2. Start watcher
	if err := s.watcher.Start(); err != nil {
		return fmt.Errorf("failed to start watcher: %w", err)
	}
	defer s.watcher.Close()

	// 3. Service loop
	ticker := time.NewTicker(s.cfg.ScanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil

		case event := <-s.watcher.Events:
			fmt.Printf("Watcher event: %v on %s\n", event.Type, s.rel(event.Path))
			if err := s.handleWatcherEvent(event); err != nil {
				fmt.Printf("Error handling watcher event for %s: %v\n", s.rel(event.Path), err)
			}

		case <-ticker.C:
			fmt.Println("Starting scheduled scan...")
			stats, err := s.scanner.Scan()
			if err != nil {
				fmt.Printf("Scheduled scan failed: %v\n", err)
			}
			fmt.Printf("Scheduled scan complete. Summary: Added: %d, Updated: %d, Deleted: %d, Unchanged: %d\n",
				stats.Added, stats.Updated, stats.Deleted, stats.Unchanged)
		}
	}
}

func (s *Service) rel(path string) string {
	rel, err := filepath.Rel(s.cfg.RootPath, path)
	if err != nil {
		return path
	}
	return rel
}

func (s *Service) handleWatcherEvent(event watcher.Event) error {
	relPath := s.rel(event.Path)
	switch event.Type {
	case watcher.EventCreate, watcher.EventModify:
		info, err := os.Stat(event.Path)
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Printf("Deleted: %s (via watcher)\n", relPath)
				return s.db.DeleteFile(relPath)
			}
			return err
		}

		existing, err := s.db.GetFileInfo(relPath)
		if err != nil {
			return err
		}

		if info.IsDir() {
			// Directory hashing is handled by scanner, but for watcher we just ensure it's in DB
			err = s.db.UpsertFile(models.FileInfo{
				Path:  relPath,
				Mtime: info.ModTime(),
				IsDir: true,
			})
			if err == nil {
				if existing == nil {
					fmt.Printf("Added: %s (dir)\n", relPath)
				} else {
					fmt.Printf("Updated: %s (dir)\n", relPath)
				}
			}
			return err
		}

		hash, err := hasher.HashFile(event.Path)
		if err != nil {
			return err
		}

		err = s.db.UpsertFile(models.FileInfo{
			Path:  relPath,
			Hash:  hash,
			Size:  info.Size(),
			Mtime: info.ModTime(),
			IsDir: false,
		})
		if err == nil {
			if existing == nil {
				fmt.Printf("Added: %s\n", relPath)
			} else {
				fmt.Printf("Updated: %s\n", relPath)
			}
		}
		return err

	case watcher.EventDelete:
		fmt.Printf("Deleted: %s\n", relPath)
		return s.db.DeleteFile(relPath)
	}
	return nil
}
