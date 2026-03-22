package watcher

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/fsnotify/fsnotify"
)

type EventType int

const (
	EventCreate EventType = iota
	EventModify
	EventDelete
)

type Event struct {
	Type EventType
	Path string
}

type Watcher struct {
	watcher *fsnotify.Watcher
	root    string
	Events  chan Event
}

func New(root string) (*Watcher, error) {
	absRoot, err := filepath.Abs(root)
	if err == nil {
		root = absRoot
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	return &Watcher{
		watcher: w,
		root:    root,
		Events:  make(chan Event, 100),
	}, nil
}

func (w *Watcher) Start(ctx context.Context) error {
	err := filepath.WalkDir(w.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if d.IsDir() {
			if err := w.watcher.Add(path); err != nil {
				return fmt.Errorf("failed to watch %s: %w", path, err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	go w.watchLoop()
	return nil
}

func (w *Watcher) watchLoop() {
	for {
		select {
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}

			if event.Has(fsnotify.Create) {
				// If it's a directory, we must watch it too
				info, err := os.Stat(event.Name)
				if err == nil && info.IsDir() {
					_ = w.watcher.Add(event.Name)
				}
				w.Events <- Event{Type: EventCreate, Path: event.Name}
			} else if event.Has(fsnotify.Write) {
				w.Events <- Event{Type: EventModify, Path: event.Name}
			} else if event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename) {
				w.Events <- Event{Type: EventDelete, Path: event.Name}
			}

		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			fmt.Printf("watcher error: %v\n", err)
		}
	}
}

func (w *Watcher) Close() error {
	return w.watcher.Close()
}
