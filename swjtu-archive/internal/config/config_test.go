package config

import (
	"testing"
	"time"
)

func TestLoadSupportsYearBackfill(t *testing.T) {
	t.Setenv("SWJTU_ARCHIVE_DATA", "archive-data")
	t.Setenv("SWJTU_ARCHIVE_BACKFILL", "1y")
	t.Setenv("SWJTU_ARCHIVE_INTERVAL", "15m")
	t.Setenv("SWJTU_ARCHIVE_SYNC_ON_START", "false")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataDir != "archive-data" || cfg.Interval != 15*time.Minute || cfg.Backfill != 365*24*time.Hour || cfg.SyncOnStart {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestLoadRejectsInvalidDuration(t *testing.T) {
	t.Setenv("SWJTU_ARCHIVE_INTERVAL", "not-a-duration")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid duration error")
	}
}

func TestLoadSyncTimes(t *testing.T) {
	t.Setenv("SWJTU_ARCHIVE_SYNC_TIMES", "12:00, 18:00")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.SyncTimes) != 2 || cfg.SyncTimes[0] != "12:00" || cfg.SyncTimes[1] != "18:00" {
		t.Fatalf("unexpected sync times: %v", cfg.SyncTimes)
	}
}

func TestLoadRejectsInvalidSyncTime(t *testing.T) {
	t.Setenv("SWJTU_ARCHIVE_SYNC_TIMES", "25:00")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid sync time error")
	}
}
