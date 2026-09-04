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
	DataDir           string
	Addr              string
	AssetBaseURL      string
	R2Endpoint        string
	R2Bucket          string
	R2Prefix          string
	R2AccessKeyID     string
	R2SecretAccessKey string
	Interval          time.Duration
	SyncTimes         []string // daily trigger times "HH:MM" in Asia/Shanghai; overrides Interval when set
	Backfill          time.Duration
	MaxPages          int
	SyncOnStart       bool
	MaxConcurrent     int
	RequestGap        time.Duration
	RefreshAfter      time.Duration
	MaxResourceBytes  int64
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
	maxPages, err := intEnv("SWJTU_ARCHIVE_MAX_PAGES", 1000)
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
	syncTimes, err := timesEnv("SWJTU_ARCHIVE_SYNC_TIMES")
	if err != nil {
		return Config{}, err
	}
	if interval <= 0 || backfill <= 0 || maxPages <= 0 || maxConcurrent <= 0 || gap < 0 || maxBytes <= 0 {
		return Config{}, fmt.Errorf("invalid non-positive crawler configuration")
	}
	r2Endpoint := env("SWJTU_ARCHIVE_R2_ENDPOINT", "")
	r2Bucket := env("SWJTU_ARCHIVE_R2_BUCKET", "")
	r2Prefix := env("SWJTU_ARCHIVE_R2_PREFIX", "news-assets")
	r2AccessKeyID := env("SWJTU_ARCHIVE_R2_ACCESS_KEY_ID", "")
	r2SecretAccessKey := env("SWJTU_ARCHIVE_R2_SECRET_ACCESS_KEY", "")
	r2Configured := r2Endpoint != "" || r2Bucket != "" || r2AccessKeyID != "" || r2SecretAccessKey != ""
	r2Complete := r2Endpoint != "" && r2Bucket != "" && r2AccessKeyID != "" && r2SecretAccessKey != ""
	if r2Configured && !r2Complete {
		return Config{}, fmt.Errorf("R2 configuration is incomplete")
	}
	return Config{
		DataDir:           dataDir,
		Addr:              env("SWJTU_ARCHIVE_ADDR", ":8080"),
		AssetBaseURL:      strings.TrimRight(env("SWJTU_ARCHIVE_ASSET_BASE_URL", ""), "/"),
		R2Endpoint:        r2Endpoint,
		R2Bucket:          r2Bucket,
		R2Prefix:          r2Prefix,
		R2AccessKeyID:     r2AccessKeyID,
		R2SecretAccessKey: r2SecretAccessKey,
		Interval:          interval,
		SyncTimes:         syncTimes,
		Backfill:          backfill,
		MaxPages:          maxPages,
		SyncOnStart:       syncOnStart,
		MaxConcurrent:     maxConcurrent,
		RequestGap:        gap,
		RefreshAfter:      refreshAfter,
		MaxResourceBytes:  maxBytes,
	}, nil
}

// R2Enabled reports whether the service should mirror newly downloaded
// resources to the configured object store.
func (c Config) R2Enabled() bool {
	return c.R2Endpoint != "" && c.R2Bucket != "" && c.R2AccessKeyID != "" && c.R2SecretAccessKey != ""
}

// timesEnv parses a comma-separated list of daily trigger times ("12:00,18:00").
func timesEnv(key string) ([]string, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return nil, nil
	}
	var times []string
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		hour, minute, ok := strings.Cut(part, ":")
		if !ok {
			return nil, fmt.Errorf("%s: invalid time %q, want HH:MM", key, part)
		}
		h, errH := strconv.Atoi(hour)
		m, errM := strconv.Atoi(minute)
		if errH != nil || errM != nil || h < 0 || h > 23 || m < 0 || m > 59 {
			return nil, fmt.Errorf("%s: invalid time %q, want HH:MM", key, part)
		}
		times = append(times, fmt.Sprintf("%02d:%02d", h, m))
	}
	return times, nil
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
