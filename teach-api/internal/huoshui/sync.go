// Package huoshui mirrors the pre-scraped course-rating JSON files from the
// private swjtu-zip/huoshui-scraper repository (branch master, docs/data/)
// into a local directory served by teach-web. It does not scrape
// app.huoshui.org itself.
package huoshui

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const (
	// DefaultBaseURL serves the raw data files of the upstream repository.
	DefaultBaseURL = "https://raw.githubusercontent.com/swjtu-zip/huoshui-scraper/master/docs/data"
	// DefaultCommitsURL reports the newest upstream commit touching docs/data.
	DefaultCommitsURL = "https://api.github.com/repos/swjtu-zip/huoshui-scraper/commits?path=docs/data&per_page=1"

	maxFileBytes = 128 << 20 // reviews.json is ~12MB today; leave generous headroom
)

// FileNames are the upstream files mirrored into the target directory.
var FileNames = []string{"courses.json", "reviews.json", "stats.json"}

// Options configures a Syncer.
type Options struct {
	Dir        string        // target directory, created if missing
	Interval   time.Duration // delay between scheduled syncs
	Token      string        // GitHub token for the private upstream repo; empty disables syncing
	Logger     *log.Logger
	Client     *http.Client // defaults to a client with a 10m timeout (upstream files can be ~12MB over slow links)
	BaseURL    string       // defaults to DefaultBaseURL
	CommitsURL string       // defaults to DefaultCommitsURL
	// OnUpdate runs once at the end of a SyncOnce that replaced at least one
	// file. main.go wires it to re-import the JSON files into the database.
	OnUpdate func(ctx context.Context)
}

// Syncer periodically mirrors the upstream data files.
type Syncer struct {
	dir        string
	interval   time.Duration
	token      string
	logger     *log.Logger
	client     *http.Client
	baseURL    string
	commitsURL string
	onUpdate   func(ctx context.Context)
}

// New builds a Syncer from opts, applying defaults.
func New(opts Options) *Syncer {
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	logger := opts.Logger
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	interval := opts.Interval
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	baseURL := opts.BaseURL
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	commitsURL := opts.CommitsURL
	if commitsURL == "" {
		commitsURL = DefaultCommitsURL
	}
	return &Syncer{
		dir:        opts.Dir,
		interval:   interval,
		token:      opts.Token,
		logger:     logger,
		client:     client,
		baseURL:    baseURL,
		commitsURL: commitsURL,
		onUpdate:   opts.OnUpdate,
	}
}

// Run syncs once immediately, then every interval, until ctx is cancelled.
// Sync failures are logged and retried on the next tick; they never panic or
// stop the loop. Without a token it logs one warning and returns.
func (s *Syncer) Run(ctx context.Context) {
	if s.token == "" {
		s.logger.Print("huoshui sync disabled: no token (set HUOSHUI_GITHUB_TOKEN); serving existing files")
		return
	}
	if err := s.SyncOnce(ctx); err != nil {
		s.logger.Printf("huoshui sync: %v", err)
	}
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.logger.Print("huoshui sync stopped")
			return
		case <-ticker.C:
			if err := s.SyncOnce(ctx); err != nil {
				s.logger.Printf("huoshui sync: %v", err)
			}
		}
	}
}

// fileMeta describes one mirrored file in meta.json.
type fileMeta struct {
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// metaFile is the on-disk shape of meta.json (also produced by
// teach-web/scripts/update-huoshui.sh; keep both in sync).
type metaFile struct {
	FetchedAt           string              `json:"fetchedAt"`
	UpstreamCommit      *string             `json:"upstreamCommit"`
	UpstreamCommittedAt *string             `json:"upstreamCommittedAt"`
	Files               map[string]fileMeta `json:"files"`
}

// SyncOnce downloads each data file, replaces the local copy only when its
// sha256 changed, and always rewrites meta.json. It returns an error when any
// step failed; files that did succeed stay in place.
func (s *Syncer) SyncOnce(ctx context.Context) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", s.dir, err)
	}
	var failed error
	changed := false
	for _, name := range FileNames {
		replaced, err := s.syncFile(ctx, name)
		if err != nil {
			s.logger.Printf("huoshui %s: %v", name, err)
			failed = err
			continue
		}
		changed = changed || replaced
	}
	sha, date, err := s.latestUpstreamCommit(ctx)
	if err != nil {
		// The commits API may rate-limit; meta.json still gets written with
		// null commit fields, matching the bash sync script's behavior.
		s.logger.Printf("huoshui upstream commit lookup failed: %v", err)
	}
	if err := s.writeMeta(sha, date); err != nil {
		s.logger.Printf("huoshui meta.json: %v", err)
		failed = err
	}
	if changed && s.onUpdate != nil {
		s.onUpdate(ctx)
	}
	return failed
}

// syncFile fetches one upstream file and atomically replaces the local copy
// when the content changed. Invalid JSON is never installed. It reports
// whether the local copy was replaced.
func (s *Syncer) syncFile(ctx context.Context, name string) (bool, error) {
	body, err := s.get(ctx, s.baseURL+"/"+name)
	if err != nil {
		return false, err
	}
	if len(body) > maxFileBytes {
		return false, fmt.Errorf("download exceeds %d bytes", maxFileBytes)
	}
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return false, fmt.Errorf("downloaded JSON invalid, keeping local copy: %w", err)
	}

	target := filepath.Join(s.dir, name)
	sum := sha256.Sum256(body)
	if existing, err := os.ReadFile(target); err == nil {
		existingSum := sha256.Sum256(existing)
		if existingSum == sum {
			s.logger.Printf("huoshui %s: unchanged (%d bytes)", name, len(body))
			return false, nil
		}
	}
	if err := writeFileAtomic(target, body, 0o644); err != nil {
		return false, err
	}
	s.logger.Printf("huoshui %s: updated (%d bytes, sha256 %s)", name, len(body), hex.EncodeToString(sum[:]))
	return true, nil
}

// latestUpstreamCommit returns the sha and committer date of the newest
// upstream commit touching docs/data, or nil values when the lookup fails.
func (s *Syncer) latestUpstreamCommit(ctx context.Context) (sha *string, date *string, err error) {
	body, err := s.get(ctx, s.commitsURL)
	if err != nil {
		return nil, nil, err
	}
	var commits []struct {
		SHA    string `json:"sha"`
		Commit struct {
			Committer struct {
				Date string `json:"date"`
			} `json:"committer"`
		} `json:"commit"`
	}
	if err := json.Unmarshal(body, &commits); err != nil {
		return nil, nil, fmt.Errorf("parse commits response: %w", err)
	}
	if len(commits) == 0 || commits[0].SHA == "" {
		return nil, nil, fmt.Errorf("commits response empty")
	}
	return &commits[0].SHA, &commits[0].Commit.Committer.Date, nil
}

// writeMeta rewrites meta.json from the files currently on disk.
func (s *Syncer) writeMeta(sha, date *string) error {
	meta := metaFile{
		FetchedAt:           time.Now().UTC().Format(time.RFC3339),
		UpstreamCommit:      sha,
		UpstreamCommittedAt: date,
		Files:               make(map[string]fileMeta, len(FileNames)),
	}
	for _, name := range FileNames {
		content, err := os.ReadFile(filepath.Join(s.dir, name))
		if err != nil {
			continue // file not synced yet; leave it out of meta.json
		}
		sum := sha256.Sum256(content)
		meta.Files[name] = fileMeta{Bytes: int64(len(content)), SHA256: hex.EncodeToString(sum[:])}
	}
	encoded, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	return writeFileAtomic(filepath.Join(s.dir, "meta.json"), encoded, 0o644)
}

// get fetches url with the upstream auth token attached.
func (s *Syncer) get(ctx context.Context, url string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "teach-api-huoshui-sync")
	if s.token != "" {
		request.Header.Set("Authorization", "Bearer "+s.token)
	}
	response, err := s.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", url, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", url, err)
	}
	return body, nil
}

// writeFileAtomic writes content to a temp file in the same directory and
// renames it over path, so readers never see a partial file.
func writeFileAtomic(path string, content []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(bytes.Clone(content)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
