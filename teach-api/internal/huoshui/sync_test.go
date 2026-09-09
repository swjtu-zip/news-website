package huoshui

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestServer(t *testing.T, files map[string][]byte, commits string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/commits") {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, commits)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/data/")
		body, ok := files[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
}

func newTestSyncer(server *httptest.Server, dir string) *Syncer {
	return New(Options{
		Dir:        dir,
		Interval:   time.Hour,
		Token:      "test-token",
		Logger:     log.New(os.Stderr, "test ", 0),
		BaseURL:    server.URL + "/data",
		CommitsURL: server.URL + "/commits",
	})
}

func TestSyncOnceInstallsFilesAndMeta(t *testing.T) {
	files := map[string][]byte{
		"courses.json": []byte(`[{"课程名称":"概率论"}]`),
		"reviews.json": []byte(`[{"评价内容":"不错"}]`),
		"stats.json":   []byte(`{"院系统计":[]}`),
	}
	commits := `[{"sha":"abc123","commit":{"committer":{"date":"2026-09-08T19:47:23Z"}}}]`
	server := newTestServer(t, files, commits)
	defer server.Close()
	dir := filepath.Join(t.TempDir(), "huoshui")

	syncer := newTestSyncer(server, dir)
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if string(got) != string(want) {
			t.Fatalf("%s: got %s", name, got)
		}
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o644 {
			t.Fatalf("%s: perm %o, want 644", name, perm)
		}
	}

	metaRaw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta metaFile
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.FetchedAt == "" {
		t.Fatal("meta.json missing fetchedAt")
	}
	if meta.UpstreamCommit == nil || *meta.UpstreamCommit != "abc123" {
		t.Fatalf("meta.json upstreamCommit = %v, want abc123", meta.UpstreamCommit)
	}
	if meta.UpstreamCommittedAt == nil || *meta.UpstreamCommittedAt != "2026-09-08T19:47:23Z" {
		t.Fatalf("meta.json upstreamCommittedAt = %v", meta.UpstreamCommittedAt)
	}
	if len(meta.Files) != len(files) {
		t.Fatalf("meta.json files = %d, want %d", len(meta.Files), len(files))
	}
	for name, want := range files {
		entry, ok := meta.Files[name]
		if !ok {
			t.Fatalf("meta.json missing file entry %s", name)
		}
		if entry.Bytes != int64(len(want)) || len(entry.SHA256) != 64 {
			t.Fatalf("meta.json %s entry = %+v", name, entry)
		}
	}

	// A second sync with identical content must succeed and keep bytes equal.
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "courses.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(files["courses.json"]) {
		t.Fatalf("courses.json changed after unchanged sync: %s", got)
	}
}

func TestSyncOnceUpdatesChangedFile(t *testing.T) {
	files := map[string][]byte{
		"courses.json": []byte(`[{"v":1}]`),
		"reviews.json": []byte(`[]`),
		"stats.json":   []byte(`{}`),
	}
	server := newTestServer(t, files, `[]`)
	defer server.Close()
	dir := t.TempDir()

	syncer := newTestSyncer(server, dir)
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	files["courses.json"] = []byte(`[{"v":2}]`)
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "courses.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `[{"v":2}]` {
		t.Fatalf("courses.json = %s, want updated content", got)
	}
}

func TestSyncOnceRefusesInvalidJSON(t *testing.T) {
	files := map[string][]byte{
		"courses.json": []byte(`[{"v":1}]`),
		"reviews.json": []byte(`[]`),
		"stats.json":   []byte(`{}`),
	}
	server := newTestServer(t, files, `[{"sha":"abc123","commit":{"committer":{"date":"2026-09-08T19:47:23Z"}}}]`)
	defer server.Close()
	dir := t.TempDir()

	syncer := newTestSyncer(server, dir)
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	files["courses.json"] = []byte(`not json`)
	if err := syncer.SyncOnce(context.Background()); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	got, err := os.ReadFile(filepath.Join(dir, "courses.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `[{"v":1}]` {
		t.Fatalf("courses.json was replaced with invalid content: %s", got)
	}
}

func TestSyncOnceToleratesCommitsAPIFailure(t *testing.T) {
	files := map[string][]byte{
		"courses.json": []byte(`[]`),
		"reviews.json": []byte(`[]`),
		"stats.json":   []byte(`{}`),
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/commits") {
			http.Error(w, "rate limited", http.StatusForbidden)
			return
		}
		w.Write(files[strings.TrimPrefix(r.URL.Path, "/data/")])
	}))
	defer server.Close()
	dir := t.TempDir()

	syncer := newTestSyncer(server, dir)
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("commits API failure must not fail the sync: %v", err)
	}
	metaRaw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta metaFile
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.UpstreamCommit != nil || meta.UpstreamCommittedAt != nil {
		t.Fatalf("commit fields must be null on lookup failure: %+v", meta)
	}
	if len(meta.Files) != len(files) {
		t.Fatalf("meta.json files = %d, want %d", len(meta.Files), len(files))
	}
}

func TestRunWithoutTokenDoesNothing(t *testing.T) {
	dir := t.TempDir()
	syncer := New(Options{Dir: dir, Token: ""})
	syncer.Run(context.Background())
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no files without token, found %d", len(entries))
	}
}

func TestOnUpdateFiresOnlyWhenFilesChange(t *testing.T) {
	files := map[string][]byte{
		"courses.json": []byte(`[]`),
		"reviews.json": []byte(`[]`),
		"stats.json":   []byte(`{}`),
	}
	server := newTestServer(t, files, `[]`)
	defer server.Close()

	fired := 0
	syncer := New(Options{
		Dir:        t.TempDir(),
		Token:      "test-token",
		Logger:     log.New(os.Stderr, "test ", 0),
		BaseURL:    server.URL + "/data",
		CommitsURL: server.URL + "/commits",
		OnUpdate:   func(ctx context.Context) { fired++ },
	})
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fired != 1 {
		t.Fatalf("OnUpdate fired %d times after first sync, want 1", fired)
	}
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fired != 1 {
		t.Fatalf("OnUpdate fired %d times after unchanged sync, want 1", fired)
	}
	files["stats.json"] = []byte(`{"v":2}`)
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fired != 2 {
		t.Fatalf("OnUpdate fired %d times after stats.json changed, want 2", fired)
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	files := map[string][]byte{
		"courses.json": []byte(`[]`),
		"reviews.json": []byte(`[]`),
		"stats.json":   []byte(`{}`),
	}
	server := newTestServer(t, files, `[]`)
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	syncer := newTestSyncer(server, t.TempDir())
	done := make(chan struct{})
	go func() {
		defer close(done)
		syncer.Run(ctx)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after context cancellation")
	}
}
