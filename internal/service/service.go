package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dzadonskii/go-filehasher/internal/db"
	"github.com/dzadonskii/go-filehasher/internal/hasher"
	"github.com/dzadonskii/go-filehasher/internal/models"
	"github.com/dzadonskii/go-filehasher/internal/scanner"
	"github.com/dzadonskii/go-filehasher/internal/watcher"
)

type Config struct {
	RootPath          string
	LimitPath         string
	DBPath            string
	ScanInterval      time.Duration
	BatchSize         int
	DBCommitThreshold int
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

	s := scanner.New(database, cfg.RootPath, cfg.BatchSize, cfg.DBCommitThreshold)
	s.LimitPath = cfg.LimitPath

	return &Service{
		cfg:     cfg,
		db:      database,
		scanner: s,
		watcher: w,
	}, nil
}

func (s *Service) log(format string, a ...any) {
	ts := time.Now().Format("2006-01-02 15:04:05")
	fmt.Printf("[%s] "+format, append([]any{ts}, a...)...)
}

func (s *Service) Run(ctx context.Context) error {
	// 1. Initial scan
	s.log("Starting initial scan of %s...\n", s.cfg.RootPath)
	stats, err := s.scanner.Scan(ctx)
	if err != nil && ctx.Err() == nil {
		return fmt.Errorf("initial scan failed: %w", err)
	}
	if err == nil {
		s.log("Initial scan complete. Summary: Added: %d, Updated: %d, Deleted: %d, Unchanged: %d\n",
			stats.Added, stats.Updated, stats.Deleted, stats.Unchanged)

		if deleted, err := s.scanner.Cleanup(ctx); err == nil && deleted > 0 {
			s.log("Initial cleanup removed %d stale entries.\n", deleted)
		}
		_ = s.db.Checkpoint(ctx)
	}

	// 2. Start watcher
	if err := s.watcher.Start(ctx); err != nil {
		if ctx.Err() != nil {
			return nil
		}
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
		default:
		}

		select {
		case <-ctx.Done():
			return nil

		case event := <-s.watcher.Events:
			s.log("Watcher event: %v on %s\n", event.Type, s.rel(event.Path))
			if err := s.handleWatcherEvent(ctx, event); err != nil {
				if ctx.Err() == nil {
					s.log("Error handling watcher event for %s: %v\n", s.rel(event.Path), err)
				}
			}

		case <-ticker.C:
			s.log("Starting scheduled scan...\n")
			stats, err := s.scanner.Scan(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				s.log("Scheduled scan failed: %v\n", err)
			} else {
				s.log("Scheduled scan complete. Summary: Added: %d, Updated: %d, Deleted: %d, Unchanged: %d\n",
					stats.Added, stats.Updated, stats.Deleted, stats.Unchanged)
				_ = s.db.Checkpoint(ctx)
			}

			if deleted, err := s.scanner.Cleanup(ctx); err == nil && deleted > 0 {
				s.log("Scheduled cleanup removed %d stale entries.\n", deleted)
			}
		}
	}
}

func (s *Service) Close() error {
	return s.db.Close()
}

func (s *Service) rel(path string) string {
	rel, err := filepath.Rel(s.cfg.RootPath, path)
	if err != nil {
		return path
	}
	return rel
}

func (s *Service) handleWatcherEvent(ctx context.Context, event watcher.Event) error {
	if s.cfg.LimitPath != "" {
		absLimit, _ := filepath.Abs(s.cfg.LimitPath)
		if !filepath.IsAbs(s.cfg.LimitPath) {
			absLimit = filepath.Join(s.cfg.RootPath, s.cfg.LimitPath)
		}
		if !strings.HasPrefix(event.Path, absLimit) {
			return nil
		}
	}
	relPath := s.rel(event.Path)
	switch event.Type {
	case watcher.EventCreate, watcher.EventModify:
		info, err := os.Stat(event.Path)
		if err != nil {
			if os.IsNotExist(err) {
				s.log("Deleted: %s (via watcher)\n", relPath)
				return s.db.DeleteFile(ctx, relPath)
			}
			return err
		}

		existing, err := s.db.GetFileInfo(ctx, relPath)
		if err != nil {
			return err
		}

		if info.IsDir() {
			// Directory hashing is handled by scanner, but for watcher we just ensure it's in DB
			err = s.db.UpsertFile(ctx, models.FileInfo{
				Path:  relPath,
				Mtime: info.ModTime(),
				IsDir: true,
			})
			if err == nil {
				if existing == nil {
					s.log("Added: %s (dir)\n", relPath)
				} else {
					s.log("Updated: %s (dir)\n", relPath)
				}
			}
			return err
		}

		hash, err := hasher.HashFile(ctx, event.Path)
		if err != nil {
			return err
		}

		err = s.db.UpsertFile(ctx, models.FileInfo{
			Path:  relPath,
			Hash:  hash,
			Size:  info.Size(),
			Mtime: info.ModTime(),
			IsDir: false,
		})
		if err == nil {
			if existing == nil {
				s.log("Added: %s (%s)\n", relPath, hash)
			} else {
				s.log("Updated: %s (%s)\n", relPath, hash)
			}
		}
		return err

	case watcher.EventDelete:
		s.log("Deleted: %s\n", relPath)
		return s.db.DeleteFile(ctx, relPath)
	}
	return nil
}
