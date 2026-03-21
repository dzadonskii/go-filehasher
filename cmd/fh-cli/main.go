package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/ideasmus/go-filehasher/internal/config"
	"github.com/ideasmus/go-filehasher/internal/db"
	"github.com/ideasmus/go-filehasher/internal/hasher"
	"github.com/ideasmus/go-filehasher/internal/scanner"
)

func main() {
	configPath := flag.String("config", "", "Path to config file (optional)")
	dbPath := flag.String("db", "fim.db", "SQLite DB file path (overridden by config if provided)")
	rootPath := flag.String("root", ".", "Root path for manual scan (overridden by config if provided)")
	batchSize := flag.Int("batch", 0, "Batch size for scanning (overridden by config if provided)")
	flag.Parse()

	args := flag.Args()
	if len(args) < 1 {
		fmt.Println("Usage: fh-cli [flags] <command>")
		fmt.Println("Commands:")
		fmt.Println("  list   - list all entries in DB")
		fmt.Println("  scan   - initiate manual scan of root path")
		fmt.Println("  check  - verify DB entries against disk")
		os.Exit(1)
	}

	finalDBPath := *dbPath
	finalRootPath := *rootPath
	finalBatchSize := *batchSize

	if *configPath != "" {
		cfg, err := config.Load(*configPath)
		if err != nil {
			log.Fatalf("Failed to load config: %v", err)
		}
		finalDBPath = cfg.DBPath
		finalRootPath = cfg.RootPath
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

	switch cmd {
	case "list":
		listEntries(database, finalRootPath)
	case "scan":
		runScan(database, finalRootPath, finalBatchSize)
	case "check":
		checkEntries(database, finalRootPath)
	default:
		log.Fatalf("Unknown command: %s", cmd)
	}
}

func listEntries(database *db.DB, root string) {
	entries, err := database.GetAllPaths()
	if err != nil {
		log.Fatalf("Failed to get entries: %v", err)
	}

	if len(entries) == 0 {
		fmt.Println("DB is empty.")
		return
	}

	paths := make([]string, 0, len(entries))
	for p := range entries {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PATH\tTYPE\tSIZE\tMODTIME\tHASH")
	dirs := 0
	files := 0
	for _, p := range paths {
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
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", f.Path, fileType, sizeStr, f.Mtime.Format(time.RFC3339), f.Hash)
	}
	w.Flush()

	fmt.Printf("\nSummary: %d directories, %d files\n", dirs, files)
}

func runScan(database *db.DB, root string, batchSize int) {
	fmt.Printf("Starting manual scan of %s (batch size: %d)...\n", root, batchSize)
	s := scanner.New(database, root, batchSize)
	stats, err := s.Scan()
	if err != nil {
		log.Fatalf("Scan failed: %v", err)
	}
	fmt.Printf("\nSummary: Added: %d, Updated: %d, Deleted: %d, Unchanged: %d\n",
		stats.Added, stats.Updated, stats.Deleted, stats.Unchanged)
	fmt.Println("Manual scan complete.")
}

func checkEntries(database *db.DB, root string) {
	entries, err := database.GetAllPaths()
	if err != nil {
		log.Fatalf("Failed to get entries: %v", err)
	}

	if len(entries) == 0 {
		fmt.Println("DB is empty.")
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
		f := entries[p]
		fullPath := filepath.Join(root, f.Path)
		relPath := f.Path
		if f.IsDir {
			if _, err := os.Stat(fullPath); os.IsNotExist(err) {
				fmt.Printf("[MISSING DIR] %s\n", relPath)
				mismatches++
			} else {
				good++
			}
			continue
		}

		// File check
		info, err := os.Stat(fullPath)
		if os.IsNotExist(err) {
			fmt.Printf("[MISSING FILE] %s\n", relPath)
			mismatches++
			continue
		} else if err != nil {
			fmt.Printf("[ERROR] Could not access %s: %v\n", relPath, err)
			mismatches++
			continue
		}
		_ = info // Keep it for now if we want more checks later

		// Check hash
		currentHash, err := hasher.HashFile(fullPath)
		if err != nil {
			fmt.Printf("[ERROR] Could not hash %s: %v\n", relPath, err)
			mismatches++
			continue
		}

		if currentHash != f.Hash {
			fmt.Printf("[MISMATCH] %s (Expected: %s, Found: %s)\n", relPath, f.Hash, currentHash)
			mismatches++
		} else {
			good++
		}
	}

	fmt.Printf("\nSummary: %d good, %d bad\n", good, mismatches)
	if mismatches == 0 {
		fmt.Println("Check complete. All files matched.")
	} else {
		fmt.Printf("Check complete. Found %d mismatches.\n", mismatches)
		os.Exit(2)
	}
}
