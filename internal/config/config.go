package config

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Duration time.Duration

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err != nil {
		return err
	}

	dur, err := ParseDuration(s)
	if err != nil {
		return err
	}

	*d = Duration(dur)
	return nil
}

func ParseDuration(s string) (time.Duration, error) {
	re := regexp.MustCompile(`^(\d+)([dwhms])$`)
	match := re.FindStringSubmatch(strings.TrimSpace(s))
	if match != nil {
		val, err := strconv.Atoi(match[1])
		if err != nil {
			return 0, err
		}
		unit := match[2]
		switch unit {
		case "d":
			return time.Duration(val) * 24 * time.Hour, nil
		case "w":
			return time.Duration(val) * 7 * 24 * time.Hour, nil
		}
	}
	return time.ParseDuration(s)
}

type Config struct {
	RootPath          string   `yaml:"root_path"`
	LimitPath         string   `yaml:"limit_path"`
	DBPath            string   `yaml:"db_path"`
	ScanInterval      Duration `yaml:"scan_interval"`
	BatchSize         int      `yaml:"batch_size"`
	DBCommitThreshold int      `yaml:"db_commit_threshold"`
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
		cfg.ScanInterval = Duration(1 * time.Hour)
	}
	if cfg.DBCommitThreshold <= 0 {
		cfg.DBCommitThreshold = 1000
	}

	return &cfg, nil
}
