package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"time"

	"github.com/dzadonskii/go-filehasher/internal/config"
	"github.com/dzadonskii/go-filehasher/internal/service"
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
		LimitPath:         cfg.LimitPath,
		DBPath:            cfg.DBPath,
		ScanInterval:      time.Duration(cfg.ScanInterval),
		BatchSize:         cfg.BatchSize,
		DBCommitThreshold: cfg.DBCommitThreshold,
	})
	if err != nil {
		log.Fatalf("Failed to initialize service: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Printf("[%s] Starting fh-svc on %s...\n", time.Now().Format("2006-01-02 15:04:05"), cfg.RootPath)
	err = svc.Run(ctx)
	svc.Close()

	if err != nil {
		log.Fatalf("Service error: %v", err)
	}
	fmt.Printf("[%s] fh-svc stopped.\n", time.Now().Format("2006-01-02 15:04:05"))
}
