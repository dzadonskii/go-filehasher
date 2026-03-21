package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	RootPath     string        `yaml:"root_path"`
	DBPath       string        `yaml:"db_path"`
	ScanInterval time.Duration `yaml:"scan_interval"`
	BatchSize    int           `yaml:"batch_size"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	if cfg.RootPath == "" {
		return nil, fmt.Errorf("root_path is required")
	}
	if cfg.DBPath == "" {
		cfg.DBPath = "fim.db"
	}
	if cfg.ScanInterval == 0 {
		cfg.ScanInterval = 1 * time.Hour
	}

	return &cfg, nil
}
