package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/dzadonskii/go-filehasher/internal/config"
	"github.com/dzadonskii/go-filehasher/internal/db"
	"github.com/dzadonskii/go-filehasher/internal/hasher"
	"github.com/dzadonskii/go-filehasher/internal/scanner"
)

var globalOut io.Writer = os.Stdout

func main() {
	configPath := flag.String("config", "", "Path to config file (optional)")
	dbPath := flag.String("db", "fim.db", "SQLite DB file path (overridden by config if provided)")
	rootPath := flag.String("root", ".", "Root path for manual scan (overridden by config if provided)")
	batchSize := flag.Int("batch", 0, "Batch size for scanning (overridden by config if provided)")
	commitThreshold := flag.Int("commit-threshold", 0, "DB commit threshold (overridden by config if provided)")
	logPath := flag.String("log", "", "Path to output log file (optional)")
	flag.Parse()

	if *logPath != "" {
		f, err := os.OpenFile(*logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			log.Fatalf("Failed to open log file: %v", err)
		}
		defer f.Close()
		globalOut = io.MultiWriter(os.Stdout, f)
		log.SetOutput(globalOut)
	}

	args := flag.Args()
	if len(args) < 1 {
		fmt.Fprintln(globalOut, "Usage: fh-cli [flags] <command>")
		fmt.Fprintln(globalOut, "Flags:")
		flag.CommandLine.SetOutput(globalOut)
		flag.PrintDefaults()
		fmt.Fprintln(globalOut, "Commands:")
		fmt.Fprintln(globalOut, "  list    - list all entries in DB")
		fmt.Fprintln(globalOut, "  scan    - initiate manual scan of root path")
		fmt.Fprintln(globalOut, "  check   - verify DB entries against disk")
		fmt.Fprintln(globalOut, "  cleanup - remove non-existent entries from DB")
		os.Exit(1)
	}

	finalDBPath := *dbPath
	finalRootPath := *rootPath
	finalBatchSize := *batchSize
	finalCommitThreshold := *commitThreshold
	if finalCommitThreshold <= 0 {
		finalCommitThreshold = 1000
	}

	if *configPath != "" {
		cfg, err := config.Load(*configPath)
		if err != nil {
			log.Fatalf("Failed to load config: %v", err)
		}
		finalDBPath = cfg.DBPath
		finalRootPath = cfg.RootPath
		if *commitThreshold == 0 {
			finalCommitThreshold = cfg.DBCommitThreshold
		}
		if *batchSize == 0 {
			finalBatchSize = cfg.BatchSize
		}
	}

	absRoot, err := filepath.Abs(finalRootPath)
	if err == nil {
		finalRootPath = absRoot
	}
	if !strings.HasSuffix(finalRootPath, string(os.PathSeparator)) {
		finalRootPath += string(os.PathSeparator)
	}

	cmd := args[0]
	database, err := db.New(finalDBPath)
	if err != nil {
		log.Fatalf("Failed to open DB: %v", err)
	}
	defer database.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch cmd {
	case "list":
		listEntries(ctx, database, finalRootPath)
	case "scan":
		runScan(ctx, database, finalRootPath, finalBatchSize, finalCommitThreshold)
	case "check":
		checkEntries(ctx, database, finalRootPath)
	case "cleanup":
		runCleanup(ctx, database, finalRootPath, finalCommitThreshold)
	default:
		log.Fatalf("Unknown command: %s", cmd)
	}
}

func listEntries(ctx context.Context, database *db.DB, root string) {
	entries, err := database.GetAllPaths()
	if err != nil {
		log.Fatalf("Failed to get entries: %v", err)
	}

	if len(entries) == 0 {
		fmt.Fprintln(globalOut, "DB is empty.")
		return
	}

	paths := make([]string, 0, len(entries))
	for p := range entries {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	w := tabwriter.NewWriter(globalOut, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PATH\tTYPE\tSIZE\tMODTIME\tUPDATED_AT\tHASH")
	dirs := 0
	files := 0
	for _, p := range paths {
		select {
		case <-ctx.Done():
			fmt.Fprintln(globalOut, "\nListing interrupted.")
			return
		default:
		}
		f := entries[p]
		fileType := "FILE"
		if f.IsDir {
			fileType = "DIR"
			dirs++
		} else {
			files++
		}
		sizeStr := humanize.Bytes(uint64(f.Size))
		if f.IsDir {
			sizeStr = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", f.Path, fileType, sizeStr, f.Mtime.Format(time.RFC3339), f.UpdatedAt.Format(time.RFC3339), f.Hash)
	}
	w.Flush()

	fmt.Fprintf(globalOut, "\nSummary: %d directories, %d files\n", dirs, files)
}

func runScan(ctx context.Context, database *db.DB, root string, batchSize int, commitThreshold int) {
	fmt.Fprintf(globalOut, "Starting manual scan of %s (batch size: %d)...\n", root, batchSize)
	s := scanner.New(database, root, batchSize, commitThreshold)
	s.Out = globalOut
	stats, err := s.Scan(ctx)
	if err != nil {
		if ctx.Err() != nil {
			fmt.Fprintln(globalOut, "\nScan interrupted.")
		} else {
			log.Fatalf("Scan failed: %v", err)
		}
	}
	fmt.Fprintf(globalOut, "\nSummary: Added: %d, Updated: %d, Deleted: %d, Unchanged: %d\n",
		stats.Added, stats.Updated, stats.Deleted, stats.Unchanged)
	if ctx.Err() == nil {
		fmt.Fprintln(globalOut, "Manual scan complete.")
	}
}

func runCleanup(ctx context.Context, database *db.DB, root string, commitThreshold int) {
	fmt.Fprintf(globalOut, "Starting cleanup of database for %s...\n", root)
	s := scanner.New(database, root, 0, commitThreshold)
	s.Out = globalOut
	count, err := s.Cleanup(ctx)
	if err != nil {
		if ctx.Err() != nil {
			fmt.Fprintln(globalOut, "\nCleanup interrupted.")
		} else {
			log.Fatalf("Cleanup failed: %v", err)
		}
	}
	fmt.Fprintf(globalOut, "\nCleanup complete. Removed %d entries.\n", count)
}

func checkEntries(ctx context.Context, database *db.DB, root string) {
	entries, err := database.GetAllPaths()
	if err != nil {
		log.Fatalf("Failed to get entries: %v", err)
	}

	if len(entries) == 0 {
		fmt.Fprintln(globalOut, "DB is empty.")
		return
	}

	paths := make([]string, 0, len(entries))
	for p := range entries {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	mismatches := 0
	good := 0
	for _, p := range paths {
		select {
		case <-ctx.Done():
			fmt.Fprintln(globalOut, "\nCheck interrupted.")
			fmt.Fprintf(globalOut, "\nPartial Summary: %d good, %d bad\n", good, mismatches)
			return
		default:
		}

		f := entries[p]
		fullPath := filepath.Join(root, f.Path)
		relPath := f.Path
		if f.IsDir {
			if _, err := os.Stat(fullPath); os.IsNotExist(err) {
				fmt.Fprintf(globalOut, "[MISSING DIR] %s\n", relPath)
				mismatches++
			} else {
				good++
			}
			continue
		}

		// File check
		info, err := os.Stat(fullPath)
		if os.IsNotExist(err) {
			fmt.Fprintf(globalOut, "[MISSING FILE] %s\n", relPath)
			mismatches++
			continue
		} else if err != nil {
			fmt.Fprintf(globalOut, "[ERROR] Could not access %s: %v\n", relPath, err)
			mismatches++
			continue
		}
		_ = info // Keep it for now if we want more checks later

		// Check hash
		currentHash, err := hasher.HashFile(ctx, fullPath)
		if err != nil {
			if ctx.Err() == nil {
				fmt.Fprintf(globalOut, "[ERROR] Could not hash %s: %v\n", relPath, err)
				mismatches++
			}
			continue
		}

		if currentHash != f.Hash {
			fmt.Fprintf(globalOut, "[MISMATCH] %s (Expected: %s, Found: %s)\n", relPath, f.Hash, currentHash)
			mismatches++
		} else {
			good++
		}
	}

	fmt.Fprintf(globalOut, "\nSummary: %d good, %d bad\n", good, mismatches)
	if mismatches == 0 {
		fmt.Fprintln(globalOut, "Check complete. All files matched.")
	} else {
		fmt.Fprintf(globalOut, "Check complete. Found %d mismatches.\n", mismatches)
		os.Exit(2)
	}
}
