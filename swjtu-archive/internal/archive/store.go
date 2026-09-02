package archive

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"swjtu-cli/pkg/sdk"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS feeds (
    id TEXT PRIMARY KEY,
    site_id TEXT NOT NULL,
    site_name TEXT NOT NULL,
    category TEXT NOT NULL,
    slug TEXT NOT NULL,
    name TEXT NOT NULL,
    base_url TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS articles (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    canonical_url TEXT NOT NULL UNIQUE,
    title TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL DEFAULT '',
    site_id TEXT NOT NULL DEFAULT '',
    site_name TEXT NOT NULL DEFAULT '',
    feed_id TEXT NOT NULL DEFAULT '',
    category TEXT NOT NULL DEFAULT '',
    article_type TEXT NOT NULL DEFAULT '',
    published_at TEXT NOT NULL DEFAULT '',
    content TEXT NOT NULL DEFAULT '',
    content_html TEXT NOT NULL DEFAULT '',
    author TEXT NOT NULL DEFAULT '',
    photographer TEXT NOT NULL DEFAULT '',
    editor TEXT NOT NULL DEFAULT '',
    raw_path TEXT NOT NULL DEFAULT '',
    raw_sha256 TEXT NOT NULL DEFAULT '',
    first_seen_at TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    last_fetched_at TEXT NOT NULL DEFAULT '',
    fetch_status TEXT NOT NULL DEFAULT 'listed',
    last_error TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS articles_published_idx ON articles(published_at DESC);
CREATE INDEX IF NOT EXISTS articles_site_idx ON articles(site_id, category);
CREATE INDEX IF NOT EXISTS articles_feed_idx ON articles(feed_id);

CREATE VIRTUAL TABLE IF NOT EXISTS article_fts USING fts5(
    title, content, source, site_name,
    tokenize = 'unicode61'
);

CREATE TABLE IF NOT EXISTS resources (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    article_id INTEGER NOT NULL REFERENCES articles(id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    original_url TEXT NOT NULL,
    local_path TEXT NOT NULL DEFAULT '',
    filename TEXT NOT NULL DEFAULT '',
    content_type TEXT NOT NULL DEFAULT '',
    byte_size INTEGER NOT NULL DEFAULT 0,
    sha256 TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending',
    error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(article_id, kind, original_url)
);

CREATE INDEX IF NOT EXISTS resources_article_idx ON resources(article_id);

CREATE TABLE IF NOT EXISTS sync_runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    started_at TEXT NOT NULL,
    finished_at TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL,
    feeds INTEGER NOT NULL DEFAULT 0,
    articles INTEGER NOT NULL DEFAULT 0,
    resources INTEGER NOT NULL DEFAULT 0,
    failures INTEGER NOT NULL DEFAULT 0,
    error TEXT NOT NULL DEFAULT ''
);
`

// Store combines SQLite metadata with the content-addressed archive tree.
type Store struct {
	db       *sql.DB
	dataDir  string
	rawDir   string
	assetDir string
}

// Open opens or creates an archive rooted at the directory containing dbPath.
func Open(dbPath string) (*Store, error) {
	dataDir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &Store{
		db: db, dataDir: dataDir,
		rawDir:   filepath.Join(dataDir, "raw"),
		assetDir: filepath.Join(dataDir, "assets"),
	}
	for _, statement := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA journal_mode = WAL",
	} {
		if _, err := db.Exec(statement); err != nil {
			store.Close()
			return nil, fmt.Errorf("configure sqlite: %w", err)
		}
	}
	if _, err := db.Exec(schema); err != nil {
		store.Close()
		return nil, fmt.Errorf("migrate sqlite: %w", err)
	}
	if err := ensureArticleTypeColumn(db); err != nil {
		store.Close()
		return nil, fmt.Errorf("migrate article type: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "tmp"), 0o755); err != nil {
		store.Close()
		return nil, fmt.Errorf("create temporary directory: %w", err)
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

// DataDir returns the root directory used for the archive.
func (s *Store) DataDir() string { return s.dataDir }

// ensureArticleTypeColumn upgrades archives created before article_type was
// introduced. SQLite's CREATE TABLE IF NOT EXISTS does not alter an existing
// table, so the small idempotent migration is needed for the deployed DB.
func ensureArticleTypeColumn(db *sql.DB) error {
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('articles') WHERE name='article_type'`).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return nil
	}
	_, err := db.Exec(`ALTER TABLE articles ADD COLUMN article_type TEXT NOT NULL DEFAULT ''`)
	return err
}

// SaveRaw stores a complete source page and returns its relative path and hash.
func (s *Store) SaveRaw(data []byte) (string, string, error) {
	hash := sha256.Sum256(data)
	hexHash := hex.EncodeToString(hash[:])
	relative := filepath.ToSlash(filepath.Join("raw", hexHash[:2], hexHash+".html"))
	if err := s.writeBlob(relative, strings.NewReader(string(data)), int64(len(data)), "", "text/html"); err != nil {
		return "", "", err
	}
	return relative, hexHash, nil
}

// SaveResourceBody stores a downloaded image or attachment with a size guard.
func (s *Store) SaveResourceBody(body io.Reader, maxBytes int64, filename, contentType string) (relative, hash string, size int64, err error) {
	tmp, err := os.CreateTemp(filepath.Join(s.dataDir, "tmp"), "resource-*")
	if err != nil {
		return "", "", 0, fmt.Errorf("create resource temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	hasher := sha256.New()
	limited := io.LimitReader(io.MultiReader(io.TeeReader(body, hasher)), maxBytes+1)
	size, err = io.Copy(tmp, limited)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", "", 0, fmt.Errorf("write resource temporary file: %w", err)
	}
	if size > maxBytes {
		return "", "", size, fmt.Errorf("resource exceeds %d bytes", maxBytes)
	}
	hash = hex.EncodeToString(hasher.Sum(nil))
	ext := resourceExtension(filename, contentType)
	relative = filepath.ToSlash(filepath.Join("assets", hash[:2], hash+ext))
	if err := s.moveTemp(tmpName, relative); err != nil {
		return "", "", size, err
	}
	return relative, hash, size, nil
}

func (s *Store) moveTemp(tmpName, relative string) error {
	destination := filepath.Join(s.dataDir, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return fmt.Errorf("create archive directory: %w", err)
	}
	if _, err := os.Stat(destination); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("check archive file: %w", err)
	}
	if err := os.Rename(tmpName, destination); err != nil {
		if _, statErr := os.Stat(destination); statErr == nil {
			return nil
		}
		return fmt.Errorf("move archive file: %w", err)
	}
	return nil
}

func (s *Store) writeBlob(relative string, body io.Reader, size int64, filename, contentType string) error {
	tmp, err := os.CreateTemp(filepath.Join(s.dataDir, "tmp"), "blob-*")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := io.Copy(tmp, body); err != nil {
		tmp.Close()
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	return s.moveTemp(tmpName, relative)
}

// FeedRecord is the persisted feed metadata.
type FeedRecord = sdk.Feed

func (s *Store) UpsertFeed(feed sdk.Feed, now time.Time) error {
	_, err := s.db.Exec(`INSERT INTO feeds (id, site_id, site_name, category, slug, name, base_url, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET site_id=excluded.site_id, site_name=excluded.site_name,
category=excluded.category, slug=excluded.slug, name=excluded.name, base_url=excluded.base_url,
updated_at=excluded.updated_at`, feed.ID, feed.SiteID, feed.SiteName, feed.Category, feed.Slug,
		feed.Name, feed.BaseURL, now.UTC().Format(time.RFC3339Nano))
	return err
}

// ArticleInput contains all normalized and source data needed for an upsert.
type ArticleInput struct {
	Feed      sdk.Feed
	Item      sdk.NewsItem
	Article   *sdk.Article
	RawPath   string
	RawSHA256 string
	FetchedAt time.Time
}

// UpsertArticle inserts or updates an article and returns its stable ID.
func (s *Store) UpsertArticle(input ArticleInput) (int64, error) {
	if input.Article == nil {
		return 0, errors.New("article is nil")
	}
	now := input.FetchedAt.UTC().Format(time.RFC3339Nano)
	title := input.Article.Title
	if title == "" {
		title = input.Item.Title
	}
	date := input.Article.Date
	if date == "" {
		date = input.Item.Date
	}
	publishedAt := input.Article.PublishedAt
	if publishedAt == "" {
		publishedAt = input.Item.PublishedAt
	}
	if publishedAt == "" {
		publishedAt = date
	}
	publishedAt = normalizePublishedAt(publishedAt)
	if date == "" {
		date = publishedAt
	}
	date = normalizeDate(date)
	articleType := strings.TrimSpace(input.Article.Type)
	if articleType == "" {
		articleType = strings.TrimSpace(input.Item.Type)
	}
	if articleType == "" {
		articleType = strings.TrimSpace(input.Feed.Name)
	}
	_, err := s.db.Exec(`INSERT INTO articles
(canonical_url, title, source, site_id, site_name, feed_id, category, article_type, published_at, content, content_html,
 author, photographer, editor, raw_path, raw_sha256, first_seen_at, last_seen_at, last_fetched_at,
 fetch_status, last_error)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'success', '')
ON CONFLICT(canonical_url) DO UPDATE SET title=excluded.title, source=excluded.source,
site_id=excluded.site_id, site_name=excluded.site_name, feed_id=excluded.feed_id,
category=excluded.category, article_type=excluded.article_type, published_at=excluded.published_at, content=excluded.content,
content_html=excluded.content_html, author=excluded.author, photographer=excluded.photographer,
editor=excluded.editor, raw_path=CASE WHEN excluded.raw_path='' THEN articles.raw_path ELSE excluded.raw_path END,
raw_sha256=CASE WHEN excluded.raw_sha256='' THEN articles.raw_sha256 ELSE excluded.raw_sha256 END,
last_seen_at=excluded.last_seen_at, last_fetched_at=excluded.last_fetched_at,
fetch_status='success', last_error=''`, input.Item.URL, title, input.Article.Source, input.Feed.SiteID,
		input.Feed.SiteName, input.Feed.ID, input.Feed.Category, articleType, publishedAt, input.Article.Content,
		input.Article.ContentHTML, input.Article.Author, input.Article.Photographer, input.Article.Editor,
		input.RawPath, input.RawSHA256, now, now, now)
	if err != nil {
		return 0, fmt.Errorf("upsert article: %w", err)
	}
	var id int64
	if err := s.db.QueryRow("SELECT id FROM articles WHERE canonical_url = ?", input.Item.URL).Scan(&id); err != nil {
		return 0, fmt.Errorf("read article id: %w", err)
	}
	if _, err := s.db.Exec(`INSERT OR REPLACE INTO article_fts(rowid, title, content, source, site_name)
VALUES (?, ?, ?, ?, ?)`, id, title, input.Article.Content, input.Article.Source, input.Feed.SiteName); err != nil {
		return 0, fmt.Errorf("update article search index: %w", err)
	}
	return id, nil
}

// ArticleFresh reports whether an article was fetched recently enough to skip
// another detail request while still refreshing old entries periodically.
func (s *Store) ArticleFresh(canonicalURL string, now time.Time, refreshAfter time.Duration) (bool, error) {
	var fetched string
	err := s.db.QueryRow("SELECT last_fetched_at FROM articles WHERE canonical_url = ?", canonicalURL).Scan(&fetched)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	when, err := time.Parse(time.RFC3339Nano, fetched)
	if err != nil {
		return false, nil
	}
	return now.Sub(when) < refreshAfter, nil
}

func (s *Store) TouchArticle(canonicalURL string, now time.Time) error {
	_, err := s.db.Exec("UPDATE articles SET last_seen_at = ? WHERE canonical_url = ?", now.UTC().Format(time.RFC3339Nano), canonicalURL)
	return err
}

// TouchArticleMetadata refreshes fields that are available from a listing
// without downloading the detail page again. This matters when a new adapter
// exposes a more specific tab/feed for an article that was already fetched by
// an older adapter version.
func (s *Store) TouchArticleMetadata(canonicalURL string, feed sdk.Feed, item sdk.NewsItem, now time.Time) error {
	var existing struct {
		title, feedID, articleType, publishedAt string
	}
	if err := s.db.QueryRow(`SELECT title, feed_id, article_type, published_at
FROM articles WHERE canonical_url = ?`, canonicalURL).Scan(
		&existing.title, &existing.feedID, &existing.articleType, &existing.publishedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}

	title := existing.title
	if strings.TrimSpace(title) == "" {
		title = item.Title
	}

	articleType := strings.TrimSpace(item.Type)
	if articleType == "" {
		articleType = strings.TrimSpace(feed.Name)
	}
	if existing.articleType != "" && !(existing.articleType == "首页新闻" && articleType != "" && articleType != "首页新闻") {
		articleType = existing.articleType
	}

	feedID := existing.feedID
	// Keep a specific tab when the same URL is also listed on the generic
	// homepage, but upgrade legacy homepage rows when a specific tab sees it.
	if feedID == "" || (feed.Slug != "index" && strings.HasSuffix(feedID, ":index")) {
		feedID = feed.ID
	}

	publishedAt := existing.publishedAt
	incomingPublishedAt := normalizePublishedAt(item.PublishedAt)
	if incomingPublishedAt == "" {
		incomingPublishedAt = normalizePublishedAt(item.Date)
	}
	if publishedAt == "" || (hasPublishedTime(incomingPublishedAt) && !hasPublishedTime(publishedAt)) {
		publishedAt = incomingPublishedAt
	}

	_, err := s.db.Exec(`UPDATE articles SET title=?, site_id=?, site_name=?, feed_id=?, category=?,
article_type=?, published_at=?, last_seen_at=? WHERE canonical_url=?`,
		title, feed.SiteID, feed.SiteName, feedID, feed.Category, articleType,
		publishedAt, now.UTC().Format(time.RFC3339Nano), canonicalURL)
	return err
}

func hasPublishedTime(value string) bool {
	return len(strings.TrimSpace(value)) > len("2006-01-02")
}

// ResourceInput describes one image or attachment linked by an article.
type ResourceInput struct {
	ArticleID   int64
	Kind        string
	OriginalURL string
	LocalPath   string
	Filename    string
	ContentType string
	ByteSize    int64
	SHA256      string
	Status      string
	Error       string
	UpdatedAt   time.Time
}

// ResourceRecord is the public representation used by the HTTP layer.
type ResourceRecord struct {
	ID          int64  `json:"id"`
	ArticleID   int64  `json:"article_id"`
	Kind        string `json:"kind"`
	OriginalURL string `json:"original_url"`
	LocalPath   string `json:"local_path,omitempty"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type,omitempty"`
	ByteSize    int64  `json:"byte_size,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
}

// UpsertResource persists resource state and returns its stable ID.
func (s *Store) UpsertResource(input ResourceInput) (int64, error) {
	when := input.UpdatedAt.UTC().Format(time.RFC3339Nano)
	_, err := s.db.Exec(`INSERT INTO resources
(article_id, kind, original_url, local_path, filename, content_type, byte_size, sha256, status, error, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(article_id, kind, original_url) DO UPDATE SET local_path=excluded.local_path,
filename=excluded.filename, content_type=excluded.content_type, byte_size=excluded.byte_size,
sha256=excluded.sha256, status=excluded.status, error=excluded.error, updated_at=excluded.updated_at`,
		input.ArticleID, input.Kind, input.OriginalURL, input.LocalPath, input.Filename, input.ContentType,
		input.ByteSize, input.SHA256, input.Status, input.Error, when, when)
	if err != nil {
		return 0, fmt.Errorf("upsert resource: %w", err)
	}
	var id int64
	err = s.db.QueryRow(`SELECT id FROM resources WHERE article_id = ? AND kind = ? AND original_url = ?`,
		input.ArticleID, input.Kind, input.OriginalURL).Scan(&id)
	return id, err
}

func (s *Store) ExistingResource(articleID int64, kind, originalURL string) (*ResourceRecord, error) {
	row := s.db.QueryRow(`SELECT id, article_id, kind, original_url, local_path, filename, content_type,
byte_size, sha256, status, error FROM resources WHERE article_id=? AND kind=? AND original_url=?`, articleID, kind, originalURL)
	return scanResource(row)
}

// ArticleSummary is a compact result for list/search pages.
type ArticleSummary struct {
	ID           int64  `json:"id"`
	Title        string `json:"title"`
	Source       string `json:"source"`
	SiteID       string `json:"site_id"`
	SiteName     string `json:"site_name"`
	FeedID       string `json:"feed_id"`
	Category     string `json:"category"`
	Type         string `json:"type"`
	PublishedAt  string `json:"published_at"`
	CanonicalURL string `json:"canonical_url"`
	Author       string `json:"author,omitempty"`
	Status       string `json:"status"`
}

type ArticleFilter struct {
	Query    string
	SiteID   string
	Category string
	FeedID   string
	From     string
	To       string
	Page     int
	PageSize int
}

func (s *Store) FindArticles(filter ArticleFilter) ([]ArticleSummary, int, error) {
	if filter.Page < 1 {
		filter.Page = 1
	}
	if filter.PageSize < 1 || filter.PageSize > 100 {
		filter.PageSize = 20
	}
	where, args, join := articleWhere(filter)
	var total int
	countQuery := "SELECT COUNT(*) FROM articles a " + join + " WHERE " + where
	if err := s.db.QueryRow(countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count articles: %w", err)
	}
	query := `SELECT a.id, a.title, a.source, a.site_id, a.site_name, a.feed_id, a.category, a.article_type,
a.published_at, a.canonical_url, a.author, a.fetch_status FROM articles a ` + join + ` WHERE ` + where +
		` ORDER BY CASE WHEN a.published_at = '' THEN 1 ELSE 0 END, a.published_at DESC, a.id DESC LIMIT ? OFFSET ?`
	args = append(args, filter.PageSize, (filter.Page-1)*filter.PageSize)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("query articles: %w", err)
	}
	defer rows.Close()
	var result []ArticleSummary
	for rows.Next() {
		var item ArticleSummary
		if err := rows.Scan(&item.ID, &item.Title, &item.Source, &item.SiteID, &item.SiteName, &item.FeedID,
			&item.Category, &item.Type, &item.PublishedAt, &item.CanonicalURL, &item.Author, &item.Status); err != nil {
			return nil, 0, err
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return result, total, nil
}

func articleWhere(filter ArticleFilter) (string, []any, string) {
	conditions := []string{"1=1"}
	var args []any
	join := ""
	if strings.TrimSpace(filter.Query) != "" {
		search := strings.TrimSpace(filter.Query)
		like := "%" + search + "%"
		conditions = append(conditions, `(a.id IN (SELECT rowid FROM article_fts WHERE article_fts MATCH ?) OR
a.title LIKE ? OR a.content LIKE ? OR a.source LIKE ? OR a.site_name LIKE ?)`)
		args = append(args, makeFTSQuery(search), like, like, like, like)
	}
	if filter.SiteID != "" {
		conditions = append(conditions, "a.site_id = ?")
		args = append(args, filter.SiteID)
	}
	if filter.Category != "" {
		conditions = append(conditions, "a.category = ?")
		args = append(args, filter.Category)
	}
	if filter.FeedID != "" {
		conditions = append(conditions, "a.feed_id = ?")
		args = append(args, filter.FeedID)
	}
	if filter.From != "" {
		conditions = append(conditions, "a.published_at >= ?")
		args = append(args, filter.From)
	}
	if filter.To != "" {
		conditions = append(conditions, "a.published_at < ?")
		args = append(args, nextDateBoundary(filter.To))
	}
	return strings.Join(conditions, " AND "), args, join
}

func nextDateBoundary(value string) string {
	parsed, err := time.Parse("2006-01-02", strings.TrimSpace(value))
	if err != nil {
		return value + "\x7f"
	}
	return parsed.AddDate(0, 0, 1).Format("2006-01-02")
}

func makeFTSQuery(value string) string {
	parts := strings.Fields(strings.TrimSpace(value))
	if len(parts) == 0 {
		return `""`
	}
	for i := range parts {
		parts[i] = `"` + strings.ReplaceAll(parts[i], `"`, `""`) + `"`
	}
	return strings.Join(parts, " AND ")
}

// GetArticle loads the full article and all linked resources.
type ArticleRecord struct {
	ArticleSummary
	Content       string           `json:"content"`
	ContentHTML   string           `json:"content_html,omitempty"`
	RawPath       string           `json:"raw_path,omitempty"`
	RawSHA256     string           `json:"raw_sha256,omitempty"`
	Photographer  string           `json:"photographer,omitempty"`
	Editor        string           `json:"editor,omitempty"`
	FirstSeenAt   string           `json:"first_seen_at"`
	LastSeenAt    string           `json:"last_seen_at"`
	LastFetchedAt string           `json:"last_fetched_at,omitempty"`
	LastError     string           `json:"last_error,omitempty"`
	Resources     []ResourceRecord `json:"resources,omitempty"`
}

func (s *Store) GetArticle(id int64) (*ArticleRecord, error) {
	row := s.db.QueryRow(`SELECT id, title, source, site_id, site_name, feed_id, category, article_type, published_at,
canonical_url, author, fetch_status, content, content_html, raw_path, raw_sha256, photographer, editor,
first_seen_at, last_seen_at, last_fetched_at, last_error FROM articles WHERE id=?`, id)
	item := &ArticleRecord{}
	err := row.Scan(&item.ID, &item.Title, &item.Source, &item.SiteID, &item.SiteName, &item.FeedID,
		&item.Category, &item.Type, &item.PublishedAt, &item.CanonicalURL, &item.Author, &item.Status, &item.Content,
		&item.ContentHTML, &item.RawPath, &item.RawSHA256, &item.Photographer, &item.Editor,
		&item.FirstSeenAt, &item.LastSeenAt, &item.LastFetchedAt, &item.LastError)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, os.ErrNotExist
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT id, article_id, kind, original_url, local_path, filename, content_type,
byte_size, sha256, status, error FROM resources WHERE article_id=? ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		resource, err := scanResource(rows)
		if err != nil {
			return nil, err
		}
		item.Resources = append(item.Resources, *resource)
	}
	return item, rows.Err()
}

func scanResource(scanner interface{ Scan(...any) error }) (*ResourceRecord, error) {
	item := &ResourceRecord{}
	err := scanner.Scan(&item.ID, &item.ArticleID, &item.Kind, &item.OriginalURL, &item.LocalPath,
		&item.Filename, &item.ContentType, &item.ByteSize, &item.SHA256, &item.Status, &item.Error)
	return item, err
}

func (s *Store) Resource(id int64) (*ResourceRecord, error) {
	row := s.db.QueryRow(`SELECT id, article_id, kind, original_url, local_path, filename, content_type,
byte_size, sha256, status, error FROM resources WHERE id=?`, id)
	return scanResource(row)
}

// OpenResource opens a successfully archived resource after validating its
// relative path against the archive root.
func (s *Store) OpenResource(item *ResourceRecord) (*os.File, error) {
	if item == nil || item.LocalPath == "" {
		return nil, os.ErrNotExist
	}
	root := filepath.Clean(s.dataDir)
	full := filepath.Clean(filepath.Join(root, filepath.FromSlash(item.LocalPath)))
	if full != root && !strings.HasPrefix(full, root+string(os.PathSeparator)) {
		return nil, errors.New("resource path escapes archive root")
	}
	return os.Open(full)
}

type RunRecord struct {
	ID         int64  `json:"id"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at,omitempty"`
	Status     string `json:"status"`
	Feeds      int    `json:"feeds"`
	Articles   int    `json:"articles"`
	Resources  int    `json:"resources"`
	Failures   int    `json:"failures"`
	Error      string `json:"error,omitempty"`
}

func (s *Store) StartRun(now time.Time) (int64, error) {
	result, err := s.db.Exec("INSERT INTO sync_runs (started_at, status) VALUES (?, 'running')", now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (s *Store) FinishRun(id int64, status string, feeds, articles, resources, failures int, runErr error, now time.Time) error {
	errorText := ""
	if runErr != nil {
		errorText = runErr.Error()
	}
	_, err := s.db.Exec(`UPDATE sync_runs SET finished_at=?, status=?, feeds=?, articles=?, resources=?, failures=?, error=? WHERE id=?`,
		now.UTC().Format(time.RFC3339Nano), status, feeds, articles, resources, failures, errorText, id)
	return err
}

func (s *Store) LatestRun() (*RunRecord, error) {
	row := s.db.QueryRow(`SELECT id, started_at, finished_at, status, feeds, articles, resources, failures, error
FROM sync_runs ORDER BY id DESC LIMIT 1`)
	item := &RunRecord{}
	err := row.Scan(&item.ID, &item.StartedAt, &item.FinishedAt, &item.Status, &item.Feeds, &item.Articles,
		&item.Resources, &item.Failures, &item.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return item, err
}

type Stats struct {
	Articles  int `json:"articles"`
	Resources int `json:"resources"`
	Feeds     int `json:"feeds"`
}

func (s *Store) Stats() (Stats, error) {
	var stats Stats
	if err := s.db.QueryRow("SELECT COUNT(*) FROM articles").Scan(&stats.Articles); err != nil {
		return Stats{}, err
	}
	if err := s.db.QueryRow("SELECT COUNT(*) FROM resources WHERE status='success'").Scan(&stats.Resources); err != nil {
		return Stats{}, err
	}
	if err := s.db.QueryRow("SELECT COUNT(*) FROM feeds").Scan(&stats.Feeds); err != nil {
		return Stats{}, err
	}
	return stats, nil
}

// SiteFacet is a per-source article count used to render filter sidebars.
type SiteFacet struct {
	SiteID   string `json:"site_id"`
	SiteName string `json:"site_name"`
	Count    int    `json:"count"`
}

// SiteFacets returns article counts grouped by source site, most articles first.
func (s *Store) SiteFacets() ([]SiteFacet, error) {
	rows, err := s.db.Query(`SELECT site_id, site_name, COUNT(*) FROM articles
GROUP BY site_id, site_name ORDER BY COUNT(*) DESC, site_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var facets []SiteFacet
	for rows.Next() {
		var facet SiteFacet
		if err := rows.Scan(&facet.SiteID, &facet.SiteName, &facet.Count); err != nil {
			return nil, err
		}
		facets = append(facets, facet)
	}
	return facets, rows.Err()
}

var dateFormats = []string{"2006-01-02", "2006/01/02", "2006.01.02", "2006年01月02日", "2006-1-2", "2006/1/2", "2006年1月2日", "2006.1.2"}

var dateTimeFormats = []string{
	time.RFC3339Nano,
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"2006/01/02 15:04:05",
	"2006/01/02 15:04",
	"2006.01.02 15:04:05",
	"2006.01.02 15:04",
	"2006年1月2日 15时04分05秒",
	"2006年1月2日 15时04分",
	"2006年1月2日 15:04:05",
	"2006年1月2日 15:04",
	"Mon Jan 02 15:04:05 MST 2006",
}

// normalizePublishedAt keeps second/minute precision when the source
// exposes it. Date-only values remain YYYY-MM-DD for backwards compatibility.
func normalizePublishedAt(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	for _, format := range dateTimeFormats {
		if parsed, err := time.Parse(format, value); err == nil {
			return parsed.Format("2006-01-02 15:04:05")
		}
	}
	return normalizeDate(value)
}

var embeddedDate = regexp.MustCompile(`(20\d{2})\s*[-/.年]\s*(\d{1,2})\s*[-/.月]\s*(\d{1,2})`)

// normalizeDate returns an ISO "2006-01-02" date, or "" when the input does
// not contain a plausible calendar date (scrapers occasionally pick up
// author names or other non-date text).
func normalizeDate(value string) string {
	value = strings.TrimSpace(value)
	for _, format := range dateFormats {
		if parsed, err := time.Parse(format, value); err == nil {
			return parsed.Format("2006-01-02")
		}
	}
	if len(value) >= 10 {
		for _, separator := range []string{"/", ".", "年"} {
			candidate := strings.ReplaceAll(value[:10], separator, "-")
			if parsed, err := time.Parse("2006-01-02", candidate); err == nil {
				return parsed.Format("2006-01-02")
			}
		}
	}
	if match := embeddedDate.FindStringSubmatch(value); match != nil {
		candidate := match[1] + "-" + match[2] + "-" + match[3]
		if parsed, err := time.Parse("2006-1-2", candidate); err == nil {
			return parsed.Format("2006-01-02")
		}
	}
	return ""
}

var unsafeFilename = regexp.MustCompile(`[^\p{L}\p{N}._-]+`)

func resourceExtension(filename, contentType string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	if ext != "" && len(ext) <= 10 && !strings.ContainsAny(ext, `/\\`) {
		return ext
	}
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if exts, _ := mime.ExtensionsByType(mediaType); len(exts) > 0 {
		sort.Strings(exts)
		return exts[0]
	}
	if parsed, err := url.Parse(filename); err == nil {
		if ext := filepath.Ext(parsed.Path); ext != "" {
			return strings.ToLower(ext)
		}
	}
	return ".bin"
}

func safeFilename(value string) string {
	value = strings.TrimSpace(filepath.Base(value))
	value = unsafeFilename.ReplaceAllString(value, "_")
	if value == "" || value == "." || value == ".." {
		return "resource"
	}
	return value
}
