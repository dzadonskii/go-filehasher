package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/ideasmus/go-filehasher/internal/config"
	"github.com/ideasmus/go-filehasher/internal/service"
)

func main() {
	configPath := flag.String("config", "config.yaml", "Path to config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	svc, err := service.New(service.Config{
		RootPath:          cfg.RootPath,
		DBPath:            cfg.DBPath,
		ScanInterval:      cfg.ScanInterval,
		BatchSize:         cfg.BatchSize,
		DBCommitThreshold: cfg.DBCommitThreshold,
	})
	if err != nil {
		log.Fatalf("Failed to initialize service: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Printf("Starting fh-svc on %s...\n", cfg.RootPath)
	err = svc.Run(ctx)
	svc.Close()

	if err != nil {
		log.Fatalf("Service error: %v", err)
	}
	fmt.Println("fh-svc stopped.")
}
