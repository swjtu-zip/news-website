package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config contains service, crawler and storage settings.
type Config struct {
	DataDir          string
	Addr             string
	Interval         time.Duration
	Backfill         time.Duration
	MaxPages         int
	SyncOnStart      bool
	MaxConcurrent    int
	RequestGap       time.Duration
	RefreshAfter     time.Duration
	MaxResourceBytes int64
}

// Load reads configuration from environment variables.
func Load() (Config, error) {
	dataDir := env("SWJTU_ARCHIVE_DATA", "data")
	interval, err := durationEnv("SWJTU_ARCHIVE_INTERVAL", 30*time.Minute)
	if err != nil {
		return Config{}, err
	}
	backfill, err := durationEnv("SWJTU_ARCHIVE_BACKFILL", 365*24*time.Hour)
	if err != nil {
		return Config{}, err
	}
	maxPages, err := intEnv("SWJTU_ARCHIVE_MAX_PAGES", 100)
	if err != nil {
		return Config{}, err
	}
	syncOnStart, err := boolEnv("SWJTU_ARCHIVE_SYNC_ON_START", true)
	if err != nil {
		return Config{}, err
	}
	maxConcurrent, err := intEnv("SWJTU_ARCHIVE_MAX_CONCURRENT", 4)
	if err != nil {
		return Config{}, err
	}
	gap, err := durationEnv("SWJTU_ARCHIVE_REQUEST_GAP", 350*time.Millisecond)
	if err != nil {
		return Config{}, err
	}
	refreshAfter, err := durationEnv("SWJTU_ARCHIVE_REFRESH_AFTER", 24*time.Hour)
	if err != nil {
		return Config{}, err
	}
	maxBytes, err := int64Env("SWJTU_ARCHIVE_MAX_RESOURCE_BYTES", 256<<20)
	if err != nil {
		return Config{}, err
	}
	if interval <= 0 || backfill <= 0 || maxPages <= 0 || maxConcurrent <= 0 || gap < 0 || maxBytes <= 0 {
		return Config{}, fmt.Errorf("invalid non-positive crawler configuration")
	}
	return Config{
		DataDir:          dataDir,
		Addr:             env("SWJTU_ARCHIVE_ADDR", ":8080"),
		Interval:         interval,
		Backfill:         backfill,
		MaxPages:         maxPages,
		SyncOnStart:      syncOnStart,
		MaxConcurrent:    maxConcurrent,
		RequestGap:       gap,
		RefreshAfter:     refreshAfter,
		MaxResourceBytes: maxBytes,
	}, nil
}

func (c Config) DBPath() string { return filepath.Join(c.DataDir, "archive.db") }

func (c Config) RawDir() string { return filepath.Join(c.DataDir, "raw") }

func (c Config) AssetDir() string { return filepath.Join(c.DataDir, "assets") }

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func durationEnv(key string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	if strings.HasSuffix(value, "y") {
		n, err := strconv.ParseFloat(strings.TrimSuffix(value, "y"), 64)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", key, err)
		}
		return time.Duration(n * 365 * 24 * float64(time.Hour)), nil
	}
	if strings.HasSuffix(value, "d") {
		n, err := strconv.ParseFloat(strings.TrimSuffix(value, "d"), 64)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", key, err)
		}
		return time.Duration(n * 24 * float64(time.Hour)), nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return parsed, nil
}

func intEnv(key string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return parsed, nil
}

func int64Env(key string, fallback int64) (int64, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return parsed, nil
}

func boolEnv(key string, fallback bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s: %w", key, err)
	}
	return parsed, nil
}
