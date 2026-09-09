// Package crawler collects the public teacher directory and public profile
// fields from faculty.swjtu.edu.cn.
package crawler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	nethtml "golang.org/x/net/html"
)

const (
	DefaultBaseURL = "https://faculty.swjtu.edu.cn"
	ListPath       = "/pyjslb.jsp"
	ListTreeID     = "1001"
	ListURLType    = "tsites.PinYinTeacherList"
	SchemaVersion  = 2
)

var (
	profilePathPattern      = regexp.MustCompile(`(?i)^/[^/]+/zh_cn/[^/]+\.html?$`)
	trailingCountPattern    = regexp.MustCompile(`\s+((?:\d{1,3}(?:,\d{3})+)|\d+)\s*$`)
	bracketIndexPattern     = regexp.MustCompile(`\s*\[\d+\]\s*`)
	imageScalePattern       = regexp.MustCompile(`(?is)\baddimg\(\s*["']([^"']+)["']`)
	viewModePattern         = regexp.MustCompile(`(?is)_tsites_com_view_mode_type_\s*=\s*['"]?(\d+)`)
	encryptedContactPattern = regexp.MustCompile(`(?i)^[0-9a-f]{64,}$`)
	emailPattern            = regexp.MustCompile(`(?i)[A-Z0-9._%+\-]+@[A-Z0-9.\-]+\.[A-Z]{2,}`)
	hashEmailPattern        = regexp.MustCompile(`(?i)[A-Z0-9._%+\-]+#[A-Z0-9.\-]+\.[A-Z]{2,}`)
	phonePattern            = regexp.MustCompile(`(?:\+?86[-\s]?)?1[3-9][0-9]{9}|0[0-9]{2,3}[-\s][0-9]{7,8}`)
)

// Fetcher is the small boundary between crawling logic and HTTP. It makes the
// parser and crawl orchestration testable against deterministic fixtures.
type Fetcher interface {
	Fetch(context.Context, string) ([]byte, error)
}

// HTTPFetcher is a polite, bounded HTTP client for the public source site.
type HTTPFetcher struct {
	Client      *http.Client
	AllowedHost string
	UserAgent   string
	Delay       time.Duration
	Retries     int
	MaxBodySize int64

	rateMu  sync.Mutex
	lastReq time.Time
}

// NewHTTPFetcher returns a fetcher with conservative defaults. Delay is
// applied globally, including while profile pages are fetched concurrently.
func NewHTTPFetcher(host string, timeout, delay time.Duration, retries int) *HTTPFetcher {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if delay < 0 {
		delay = 0
	}
	if retries < 0 {
		retries = 0
	}
	return &HTTPFetcher{
		Client:      &http.Client{Timeout: timeout},
		AllowedHost: strings.ToLower(host),
		UserAgent:   "teach-crawler/0.1 (+https://swjtu.zip)",
		Delay:       delay,
		Retries:     retries,
		MaxBodySize: 16 << 20,
	}
}

func (f *HTTPFetcher) Fetch(ctx context.Context, rawURL string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("unsupported URL scheme %q", u.Scheme)
	}
	if f.AllowedHost != "" && strings.ToLower(u.Hostname()) != f.AllowedHost {
		return nil, fmt.Errorf("refusing host %q", u.Hostname())
	}
	if f.Client == nil {
		f.Client = &http.Client{Timeout: 30 * time.Second}
	}
	maxBody := f.MaxBodySize
	if maxBody <= 0 {
		maxBody = 16 << 20
	}

	var lastErr error
	for attempt := 0; attempt <= f.Retries; attempt++ {
		if err := f.wait(ctx); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", f.UserAgent)
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/json")
		resp, err := f.Client.Do(req)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			lastErr = err
			if attempt < f.Retries {
				if err := retryWait(ctx, attempt); err != nil {
					return nil, err
				}
				continue
			}
			break
		}

		if f.AllowedHost != "" && strings.ToLower(resp.Request.URL.Hostname()) != f.AllowedHost {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("refusing redirect host %q", resp.Request.URL.Hostname())
		}
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("HTTP status %d", resp.StatusCode)
			if (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500) && attempt < f.Retries {
				if err := retryWait(ctx, attempt); err != nil {
					return nil, err
				}
				continue
			}
			break
		}

		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			if attempt < f.Retries {
				if err := retryWait(ctx, attempt); err != nil {
					return nil, err
				}
				continue
			}
			break
		}
		if int64(len(body)) > maxBody {
			return nil, fmt.Errorf("response exceeds %d bytes", maxBody)
		}
		return body, nil
	}
	if lastErr == nil {
		lastErr = errors.New("request failed")
	}
	return nil, lastErr
}

func (f *HTTPFetcher) wait(ctx context.Context) error {
	f.rateMu.Lock()
	wait := f.Delay - time.Since(f.lastReq)
	if wait < 0 {
		wait = 0
	}
	f.lastReq = time.Now().Add(wait)
	f.rateMu.Unlock()
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func retryWait(ctx context.Context, attempt int) error {
	delay := time.Duration(1<<min(attempt, 4)) * 250 * time.Millisecond
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// DirectoryEntry is one row discovered from a pinyin directory page.
type DirectoryEntry struct {
	Name         string `json:"name"`
	ProfileURL   string `json:"profile_url"`
	Initial      string `json:"initial"`
	DirectoryNum int    `json:"directory_num,omitempty"`
	AvatarURL    string `json:"avatar_url,omitempty"`
}

// ProfileData contains the source page's allowlisted public profile fields.
// Contact details are collected only when they are explicitly labelled on the
// public profile and are kept in structured fields.
type ProfileData struct {
	Name             string           `json:"name,omitempty"`
	Position         string           `json:"position,omitempty"`
	SupervisorRoles  string           `json:"supervisor_roles,omitempty"`
	College          string           `json:"college,omitempty"`
	PrimaryRole      string           `json:"primary_role,omitempty"`
	EntryDate        string           `json:"entry_date,omitempty"`
	EmploymentStatus string           `json:"employment_status,omitempty"`
	Education        string           `json:"education,omitempty"`
	Degree           string           `json:"degree,omitempty"`
	University       string           `json:"university,omitempty"`
	Gender           string           `json:"gender,omitempty"`
	Email            string           `json:"email,omitempty"`
	Phone            string           `json:"phone,omitempty"`
	OfficeLocation   string           `json:"office_location,omitempty"`
	PostalCode       string           `json:"postal_code,omitempty"`
	Introduction     string           `json:"introduction,omitempty"`
	Research         []string         `json:"research_directions,omitempty"`
	AvatarURL        string           `json:"avatar_url,omitempty"`
	Sections         []ProfileSection `json:"sections,omitempty"`
}

// ProfileSection is an additional public section from a teacher profile, such
// as education history, work experience, academic appointments, projects or
// representative achievements. Contact details are represented by the
// structured fields above rather than copied into arbitrary section text.
type ProfileSection struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

// TeacherRecord is the stable crawler-to-API handoff shape for the public
// teacher directory. Private course/grade data should use separate
// authenticated storage and must not be added to this public JSON.
type TeacherRecord struct {
	Name             string           `json:"name"`
	DirectoryName    string           `json:"directory_name,omitempty"`
	ProfileName      string           `json:"profile_name,omitempty"`
	ProfileURL       string           `json:"profile_url"`
	Initial          string           `json:"initial"`
	DirectoryNum     int              `json:"directory_num,omitempty"`
	NameMatch        bool             `json:"name_match"`
	Position         string           `json:"position,omitempty"`
	SupervisorRoles  string           `json:"supervisor_roles,omitempty"`
	College          string           `json:"college,omitempty"`
	PrimaryRole      string           `json:"primary_role,omitempty"`
	EntryDate        string           `json:"entry_date,omitempty"`
	EmploymentStatus string           `json:"employment_status,omitempty"`
	Education        string           `json:"education,omitempty"`
	Degree           string           `json:"degree,omitempty"`
	University       string           `json:"university,omitempty"`
	Gender           string           `json:"gender,omitempty"`
	Email            string           `json:"email,omitempty"`
	Phone            string           `json:"phone,omitempty"`
	OfficeLocation   string           `json:"office_location,omitempty"`
	PostalCode       string           `json:"postal_code,omitempty"`
	Introduction     string           `json:"introduction,omitempty"`
	Research         []string         `json:"research_directions,omitempty"`
	AvatarURL        string           `json:"avatar_url,omitempty"`
	Sections         []ProfileSection `json:"sections,omitempty"`
	Status           string           `json:"status"`
	Error            string           `json:"error,omitempty"`
}

type ListPageResult struct {
	Initial    string `json:"initial"`
	URL        string `json:"url"`
	EntryCount int    `json:"entry_count"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
}

type CrawlStats struct {
	ListPages                 int `json:"list_pages"`
	ListPagesFailed           int `json:"list_pages_failed"`
	DirectoryEntries          int `json:"directory_entries"`
	DuplicateDirectoryEntries int `json:"duplicate_directory_entries"`
	UniqueTeachers            int `json:"unique_teachers"`
	ProfilesFetched           int `json:"profiles_fetched"`
	ProfilesSucceeded         int `json:"profiles_succeeded"`
	ProfilesFailed            int `json:"profiles_failed"`
	NameMismatches            int `json:"name_mismatches"`
}

type CrawlResult struct {
	SchemaVersion int               `json:"schema_version"`
	GeneratedAt   string            `json:"generated_at"`
	Source        map[string]string `json:"source"`
	ListPages     []ListPageResult  `json:"list_pages"`
	Stats         CrawlStats        `json:"stats"`
	Teachers      []TeacherRecord   `json:"teachers"`
}

// Options controls one crawl. The default command uses all letters and fetches
// profiles concurrently with a global request gap.
type Options struct {
	BaseURL     string
	Letters     []string
	Workers     int
	Fetcher     Fetcher
	RawDir      string
	ListOnly    bool
	MaxTeachers int
	Now         func() time.Time
	Logger      *log.Logger
}

type Crawler struct {
	baseURL     *url.URL
	host        string
	letters     []string
	workers     int
	fetcher     Fetcher
	rawDir      string
	listOnly    bool
	maxTeachers int
	now         func() time.Time
	logger      *log.Logger
}

func New(options Options) (*Crawler, error) {
	rawBase := strings.TrimSpace(options.BaseURL)
	if rawBase == "" {
		rawBase = DefaultBaseURL
	}
	base, err := url.Parse(rawBase)
	if err != nil || base.Hostname() == "" || (base.Scheme != "http" && base.Scheme != "https") {
		return nil, fmt.Errorf("invalid base URL %q", rawBase)
	}
	base.Path = strings.TrimRight(base.Path, "/")
	if base.Path == "" {
		base.Path = ""
	}
	base.RawQuery = ""
	base.Fragment = ""
	letters := options.Letters
	if len(letters) == 0 {
		letters = DefaultLetters()
	}
	letters = normalizeLetters(letters)
	if len(letters) == 0 {
		return nil, errors.New("letters cannot be empty")
	}
	if options.Workers <= 0 {
		options.Workers = 4
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Logger == nil {
		options.Logger = log.New(io.Discard, "", 0)
	}
	if options.Fetcher == nil {
		options.Fetcher = NewHTTPFetcher(strings.ToLower(base.Hostname()), 30*time.Second, 350*time.Millisecond, 2)
	}
	return &Crawler{
		baseURL: base, host: strings.ToLower(base.Hostname()), letters: letters,
		workers: options.Workers, fetcher: options.Fetcher, rawDir: options.RawDir,
		listOnly: options.ListOnly, maxTeachers: options.MaxTeachers, now: options.Now,
		logger: options.Logger,
	}, nil
}

func DefaultLetters() []string {
	letters := make([]string, 26)
	for i := range letters {
		letters[i] = string(rune('a' + i))
	}
	return letters
}

func normalizeLetters(input []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(input))
	for _, raw := range input {
		letter := strings.ToLower(strings.TrimSpace(raw))
		if len([]rune(letter)) != 1 || letter[0] < 'a' || letter[0] > 'z' || seen[letter] {
			continue
		}
		seen[letter] = true
		result = append(result, letter)
	}
	sort.Strings(result)
	return result
}

func (c *Crawler) listURL(letter string) string {
	u := *c.baseURL
	u.Path = strings.TrimRight(c.baseURL.Path, "/") + ListPath
	query := u.Query()
	query.Set("urltype", ListURLType)
	query.Set("wbtreeid", ListTreeID)
	query.Set("py", letter)
	query.Set("lang", "zh_CN")
	u.RawQuery = query.Encode()
	return u.String()
}

// Crawl fetches each selected directory page, de-duplicates profile URLs, and
// optionally fetches each public profile. List-page failures remain in the
// result so callers can persist an auditable partial run.
func (c *Crawler) Crawl(ctx context.Context) (CrawlResult, error) {
	result := CrawlResult{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   c.now().UTC().Format(time.RFC3339),
		Source: map[string]string{
			"site":      c.host,
			"base_url":  c.baseURL.String(),
			"directory": "pinyin-teacher-list",
		},
		ListPages: make([]ListPageResult, 0, len(c.letters)),
	}
	allEntries := make([]DirectoryEntry, 0)
	for _, letter := range c.letters {
		pending := []string{c.listURL(letter)}
		seenPages := make(map[string]bool)
		for len(pending) > 0 {
			pageURL := pending[0]
			pending = pending[1:]
			pageKey := canonicalURL(pageURL)
			if seenPages[pageKey] {
				continue
			}
			seenPages[pageKey] = true

			page := ListPageResult{Initial: letter, URL: pageURL}
			body, err := c.fetcher.Fetch(ctx, pageURL)
			if err != nil {
				page.Status = "error"
				page.Error = err.Error()
				result.ListPages = append(result.ListPages, page)
				c.logger.Printf("list %s page %s failed: %v", letter, pageURL, err)
				continue
			}
			if err := c.saveRaw("list-"+letter+"-"+shortHash(pageURL)+".html", body); err != nil {
				c.logger.Printf("save list %s raw page: %v", letter, err)
			}
			entries, nextPages, err := parseDirectoryPage(body, pageURL, letter)
			if err != nil {
				page.Status = "error"
				page.Error = err.Error()
				result.ListPages = append(result.ListPages, page)
				c.logger.Printf("parse list %s page %s failed: %v", letter, pageURL, err)
				continue
			}
			for index := range entries {
				entries[index].Initial = letter
			}
			page.Status = "ok"
			page.EntryCount = len(entries)
			result.ListPages = append(result.ListPages, page)
			allEntries = append(allEntries, entries...)
			for _, nextPage := range nextPages {
				if !seenPages[canonicalURL(nextPage)] {
					pending = append(pending, nextPage)
				}
			}
		}
	}

	result.Stats.ListPages = len(result.ListPages)
	for _, page := range result.ListPages {
		if page.Status != "ok" {
			result.Stats.ListPagesFailed++
		}
	}
	result.Stats.DirectoryEntries = len(allEntries)
	entries, duplicates := deduplicateEntries(allEntries)
	result.Stats.DuplicateDirectoryEntries = duplicates
	if c.maxTeachers > 0 && len(entries) > c.maxTeachers {
		entries = entries[:c.maxTeachers]
	}
	result.Stats.UniqueTeachers = len(entries)

	if c.listOnly {
		result.Teachers = make([]TeacherRecord, 0, len(entries))
		for _, entry := range entries {
			result.Teachers = append(result.Teachers, TeacherRecord{
				Name: entry.Name, DirectoryName: entry.Name, ProfileURL: entry.ProfileURL,
				Initial: entry.Initial, DirectoryNum: entry.DirectoryNum, AvatarURL: entry.AvatarURL, Status: "not_fetched",
			})
		}
		return result, nil
	}

	result.Stats.ProfilesFetched = len(entries)
	result.Teachers = c.fetchProfiles(ctx, entries, &result.Stats)
	return result, nil
}

func deduplicateEntries(entries []DirectoryEntry) ([]DirectoryEntry, int) {
	seen := make(map[string]int, len(entries))
	unique := make([]DirectoryEntry, 0, len(entries))
	duplicates := 0
	for _, entry := range entries {
		key := canonicalURL(entry.ProfileURL)
		if index, ok := seen[key]; ok {
			duplicates++
			if unique[index].Name == "" && entry.Name != "" {
				unique[index].Name = entry.Name
			}
			if unique[index].DirectoryNum == 0 {
				unique[index].DirectoryNum = entry.DirectoryNum
			}
			if unique[index].AvatarURL == "" {
				unique[index].AvatarURL = entry.AvatarURL
			}
			continue
		}
		seen[key] = len(unique)
		unique = append(unique, entry)
	}
	return unique, duplicates
}

func (c *Crawler) fetchProfiles(ctx context.Context, entries []DirectoryEntry, stats *CrawlStats) []TeacherRecord {
	records := make([]TeacherRecord, len(entries))
	jobs := make(chan int)
	var wg sync.WaitGroup
	workers := c.workers
	if workers > len(entries) {
		workers = len(entries)
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				records[index] = c.fetchProfile(ctx, entries[index])
			}
		}()
	}
	for index := range entries {
		select {
		case jobs <- index:
		case <-ctx.Done():
			for remaining := index; remaining < len(entries); remaining++ {
				records[remaining] = TeacherRecord{
					Name: entries[remaining].Name, DirectoryName: entries[remaining].Name,
					ProfileURL: entries[remaining].ProfileURL, Initial: entries[remaining].Initial,
					DirectoryNum: entries[remaining].DirectoryNum, AvatarURL: entries[remaining].AvatarURL,
					Status: "error", Error: ctx.Err().Error(),
				}
			}
			break
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(jobs)
	wg.Wait()

	for _, record := range records {
		switch record.Status {
		case "ok":
			stats.ProfilesSucceeded++
			if !record.NameMatch && record.DirectoryName != "" && record.ProfileName != "" {
				stats.NameMismatches++
			}
		case "error":
			stats.ProfilesFailed++
		}
	}
	return records
}

func (c *Crawler) fetchProfile(ctx context.Context, entry DirectoryEntry) TeacherRecord {
	record := TeacherRecord{
		Name: entry.Name, DirectoryName: entry.Name, ProfileURL: entry.ProfileURL,
		Initial: entry.Initial, DirectoryNum: entry.DirectoryNum, AvatarURL: entry.AvatarURL, Status: "error",
	}
	body, err := c.fetcher.Fetch(ctx, entry.ProfileURL)
	if err != nil {
		record.Error = err.Error()
		return record
	}
	if err := c.saveRaw("profile-"+shortHash(entry.ProfileURL)+".html", body); err != nil {
		c.logger.Printf("save profile raw page %s: %v", entry.ProfileURL, err)
	}
	profile, err := ParseProfileAt(body, entry.ProfileURL)
	if err != nil {
		record.Error = err.Error()
		return record
	}
	c.decodeEncryptedContacts(ctx, entry.ProfileURL, body, &profile)
	record.ProfileName = profile.Name
	record.Name = firstNonEmpty(profile.Name, entry.Name)
	record.NameMatch = equalNames(entry.Name, profile.Name)
	record.Position = profile.Position
	record.SupervisorRoles = profile.SupervisorRoles
	record.College = profile.College
	record.PrimaryRole = profile.PrimaryRole
	record.EntryDate = profile.EntryDate
	record.EmploymentStatus = profile.EmploymentStatus
	record.Education = profile.Education
	record.Degree = profile.Degree
	record.University = profile.University
	record.Gender = profile.Gender
	record.Email = profile.Email
	record.Phone = profile.Phone
	record.OfficeLocation = profile.OfficeLocation
	record.PostalCode = profile.PostalCode
	record.Introduction = profile.Introduction
	record.Research = profile.Research
	record.AvatarURL = firstNonEmpty(profile.AvatarURL, entry.AvatarURL)
	record.Sections = profile.Sections
	record.Status = "ok"
	return record
}

const encryptedContactFieldID = "_tsites_encryp_tsothercontact_tsccontent"

// decodeEncryptedContacts follows the same endpoint used by the source
// page's tsitesencrypt.js. The page deliberately stores contact values as
// long hexadecimal strings in the HTML; those strings are not useful contact
// data and must never be passed through to the public API.
func (c *Crawler) decodeEncryptedContacts(ctx context.Context, pageURL string, body []byte, profile *ProfileData) {
	if profile == nil {
		return
	}
	mode := profileViewMode(body)
	for label, value := range map[string]*string{
		"email":           &profile.Email,
		"phone":           &profile.Phone,
		"office_location": &profile.OfficeLocation,
		"postal_code":     &profile.PostalCode,
	} {
		if !looksEncryptedContact(*value) {
			continue
		}
		decoded, err := c.decodeContactValue(ctx, pageURL, *value, mode)
		if err != nil {
			c.logger.Printf("decode %s for %s: %v", label, pageURL, err)
			*value = ""
			continue
		}
		*value = decoded
	}
}

func (c *Crawler) decodeContactValue(ctx context.Context, pageURL, encrypted, mode string) (string, error) {
	if !looksEncryptedContact(encrypted) {
		return sanitizeContactValue(encrypted), nil
	}
	if mode == "1" || mode == "2" || mode == "5" {
		return "", nil
	}
	endpoint, err := encryptedContactURL(pageURL, encrypted, mode)
	if err != nil {
		return "", err
	}
	body, err := c.fetcher.Fetch(ctx, endpoint)
	if err != nil {
		return "", err
	}
	return parseContactResponse(body)
}

func encryptedContactURL(pageURL, encrypted, mode string) (string, error) {
	base, err := url.Parse(pageURL)
	if err != nil || base.Hostname() == "" {
		return "", fmt.Errorf("parse profile URL: %w", err)
	}
	endpoint := *base
	endpoint.Path = "/system/resource/tsites/tsitesencrypt.jsp"
	endpoint.RawQuery = url.Values{
		"id":      []string{encryptedContactFieldID},
		"content": []string{strings.TrimSpace(encrypted)},
		"mode":    []string{firstNonEmpty(mode, "8")},
	}.Encode()
	return endpoint.String(), nil
}

func profileViewMode(body []byte) string {
	match := viewModePattern.FindSubmatch(body)
	if len(match) > 1 {
		return strings.TrimSpace(string(match[1]))
	}
	return "8"
}

func looksEncryptedContact(value string) bool {
	return encryptedContactPattern.MatchString(strings.TrimSpace(value))
}

func parseContactResponse(body []byte) (string, error) {
	var payload struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("decode contact response: %w", err)
	}
	value := strings.TrimSpace(payload.Content)
	if value == "" {
		return "", nil
	}
	if strings.ContainsAny(value, "<>") {
		doc, err := parseHTML([]byte(value))
		if err != nil {
			return "", fmt.Errorf("parse contact response HTML: %w", err)
		}
		value = nodeText(doc)
	}
	return sanitizeContactValue(value), nil
}

func (c *Crawler) saveRaw(name string, body []byte) error {
	if strings.TrimSpace(c.rawDir) == "" {
		return nil
	}
	if err := os.MkdirAll(c.rawDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(c.rawDir, filepath.Base(name))
	tmp, err := os.CreateTemp(c.rawDir, ".raw-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	// The snapshot contains only the public allowlist, so make it readable by
	// the non-root API container when it is mounted as a read-only file.
	return os.Chmod(path, 0o644)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func shortHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])[:16]
}

func canonicalURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.Fragment = ""
	u.Host = strings.ToLower(u.Host)
	return u.String()
}

// ParseDirectory parses only profile links belonging to the source host. It
// handles both text links and image links whose alt text carries the name.
func ParseDirectory(body []byte, pageURL string) ([]DirectoryEntry, error) {
	entries, _, err := parseDirectoryPage(body, pageURL, "")
	return entries, err
}

func parseDirectoryPage(body []byte, pageURL, initial string) ([]DirectoryEntry, []string, error) {
	doc, err := parseHTML(body)
	if err != nil {
		return nil, nil, err
	}
	base, err := url.Parse(pageURL)
	if err != nil {
		return nil, nil, fmt.Errorf("parse directory URL: %w", err)
	}
	var anchors []*nethtml.Node
	walk(doc, func(node *nethtml.Node) {
		if node.Type == nethtml.ElementNode && node.Data == "a" && attr(node, "href") != "" {
			anchors = append(anchors, node)
		}
	})

	byURL := make(map[string]int)
	entries := make([]DirectoryEntry, 0)
	nextPages := make([]string, 0)
	seenNextPages := make(map[string]bool)
	for _, anchor := range anchors {
		if nextPage, ok := paginationURLFor(attr(anchor, "href"), base, initial); ok {
			key := canonicalURL(nextPage)
			if key != canonicalURL(pageURL) && !seenNextPages[key] {
				seenNextPages[key] = true
				nextPages = append(nextPages, nextPage)
			}
		}
		profileURL, ok := profileURLFor(attr(anchor, "href"), base)
		if !ok {
			continue
		}
		name, count := listingName(nodeText(anchor))
		if name == "" {
			name, count = ancestorListingName(anchor)
		} else if count == 0 {
			count = ancestorDirectoryCount(anchor)
		}
		avatar := resolveProfileAsset(profileImageURL(anchor, name), pageURL, strings.ToLower(base.Hostname()))
		entry := DirectoryEntry{Name: name, ProfileURL: profileURL, DirectoryNum: count, AvatarURL: avatar}
		if index, exists := byURL[canonicalURL(profileURL)]; exists {
			if entries[index].Name == "" && name != "" {
				entries[index].Name = name
			}
			if entries[index].DirectoryNum == 0 {
				entries[index].DirectoryNum = count
			}
			if entries[index].AvatarURL == "" {
				entries[index].AvatarURL = avatar
			}
			continue
		}
		byURL[canonicalURL(profileURL)] = len(entries)
		entries = append(entries, entry)
	}
	return entries, nextPages, nil
}

func paginationURLFor(raw string, current *url.URL, initial string) (string, bool) {
	if strings.TrimSpace(raw) == "" || initial == "" {
		return "", false
	}
	ref, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", false
	}
	u := current.ResolveReference(ref)
	if (u.Scheme != "http" && u.Scheme != "https") || strings.ToLower(u.Hostname()) != strings.ToLower(current.Hostname()) || u.Path != current.Path {
		return "", false
	}
	query := u.Query()
	currentQuery := current.Query()
	if query.Get("urltype") == "" {
		query.Set("urltype", firstNonEmpty(currentQuery.Get("urltype"), ListURLType))
	}
	if query.Get("wbtreeid") == "" {
		query.Set("wbtreeid", firstNonEmpty(currentQuery.Get("wbtreeid"), ListTreeID))
	}
	if query.Get("lang") == "" && currentQuery.Get("lang") != "" {
		query.Set("lang", currentQuery.Get("lang"))
	}
	if query.Get("py") == "" {
		query.Set("py", initial)
	}
	if query.Get("py") != initial || query.Get("urltype") != ListURLType || query.Get("wbtreeid") != ListTreeID {
		return "", false
	}
	u.RawQuery = query.Encode()
	u.Fragment = ""
	return u.String(), true
}

func profileURLFor(raw string, base *url.URL) (string, bool) {
	ref, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", false
	}
	u := base.ResolveReference(ref)
	if (u.Scheme != "http" && u.Scheme != "https") || strings.ToLower(u.Hostname()) != strings.ToLower(base.Hostname()) {
		return "", false
	}
	if !profilePathPattern.MatchString(u.EscapedPath()) {
		return "", false
	}
	u.Fragment = ""
	return u.String(), true
}

func ancestorListingName(anchor *nethtml.Node) (string, int) {
	for node := anchor; node != nil; node = node.Parent {
		text := nodeText(node)
		name, count := listingName(text)
		if name == "" {
			continue
		}
		if len([]rune(normalizeText(text))) <= 120 {
			return name, count
		}
	}
	return "", 0
}

func ancestorDirectoryCount(anchor *nethtml.Node) int {
	for node := anchor.Parent; node != nil; node = node.Parent {
		_, count := listingName(nodeText(node))
		if count > 0 {
			return count
		}
	}
	return 0
}

func listingName(raw string) (string, int) {
	text := normalizeText(raw)
	if text == "" {
		return "", 0
	}
	count := 0
	if match := trailingCountPattern.FindStringSubmatch(text); len(match) == 2 {
		count, _ = strconv.Atoi(strings.ReplaceAll(match[1], ",", ""))
		text = strings.TrimSpace(strings.TrimSuffix(text, match[0]))
	}
	text = strings.Trim(text, " -–—|·:：()（）[]【】")
	if !plausibleName(text) {
		return "", 0
	}
	return text, count
}

func plausibleName(value string) bool {
	value = normalizeText(value)
	if value == "" || len([]rune(value)) > 80 {
		return false
	}
	for _, excluded := range []string{"首页", "学院列表", "检索导师", "使用帮助", "English", "个人信息", "个人简介", "教育经历", "研究方向", "Copyright", "版权所有"} {
		if value == excluded || strings.Contains(value, excluded+" ") {
			return false
		}
	}
	for _, r := range value {
		if unicode.IsDigit(r) {
			return false
		}
	}
	return !strings.ContainsAny(value, "<>：:")
}

// ParseProfile extracts the allowlisted public fields from one profile page.
func ParseProfile(body []byte) (ProfileData, error) {
	doc, err := parseHTML(body)
	if err != nil {
		return ProfileData{}, err
	}
	text := normalizeText(nodeText(doc))
	name, position := profileIdentity(doc, text)
	profile := ProfileData{Name: name}
	profile.Position = position
	profile.SupervisorRoles = profileSupervisorRoles(doc)
	fields := profilePublicFields(doc)
	profile.College = fields["所在单位"]
	profile.PrimaryRole = fields["主要任职"]
	profile.EntryDate = fields["入职时间"]
	profile.EmploymentStatus = fields["在职信息"]
	profile.Education = fields["学历"]
	profile.Degree = fields["学位"]
	profile.University = fields["毕业院校"]
	profile.Gender = fields["性别"]
	contact := profileContactFields(doc)
	profile.Email = contact.Email
	profile.Phone = contact.Phone
	profile.OfficeLocation = contact.OfficeLocation
	profile.PostalCode = contact.PostalCode
	profile.Introduction = sectionText(findSectionNode(doc, "个人简介"), "个人简介")
	profile.Research = researchDirectionsFromNode(findSectionNode(doc, "研究方向"))
	profile.AvatarURL = profileImageURL(doc, name)
	profile.Sections = profileAdditionalSections(doc)
	return profile, nil
}

// ParseProfileAt is ParseProfile with a resolved same-site avatar URL. The
// plain parser remains useful for fixtures and callers that do not know the
// source page URL.
func ParseProfileAt(body []byte, pageURL string) (ProfileData, error) {
	profile, err := ParseProfile(body)
	if err != nil {
		return ProfileData{}, err
	}
	base, err := url.Parse(pageURL)
	if err != nil {
		return ProfileData{}, fmt.Errorf("parse profile URL: %w", err)
	}
	profile.AvatarURL = resolveProfileAsset(profile.AvatarURL, pageURL, strings.ToLower(base.Hostname()))
	return profile, nil
}

func profileIdentity(doc *nethtml.Node, fullText string) (string, string) {
	var name, position string
	walk(doc, func(node *nethtml.Node) {
		if name != "" || node.Type != nethtml.ElementNode || (!hasClass(node, "name") && !hasClass(node, "nm")) {
			return
		}
		if hasClass(node, "name") {
			var spans []*nethtml.Node
			walk(node, func(child *nethtml.Node) {
				if child.Type == nethtml.ElementNode && child.Data == "span" {
					spans = append(spans, child)
				}
			})
			for _, span := range spans {
				value := normalizeText(nodeText(span))
				if value == "" {
					continue
				}
				if name == "" {
					name = value
					continue
				}
				if hasClass(span, "zc") && position == "" {
					position = value
				}
			}
			return
		}
		var headings []*nethtml.Node
		var spans []*nethtml.Node
		walk(node, func(child *nethtml.Node) {
			if child == node {
				return
			}
			if child.Type == nethtml.ElementNode && child.Data == "h2" {
				headings = append(headings, child)
			}
			if child.Type == nethtml.ElementNode && child.Data == "span" {
				spans = append(spans, child)
			}
		})
		if len(headings) > 0 {
			name = normalizeText(nodeText(headings[0]))
		}
		for _, span := range spans {
			value := normalizeText(nodeText(span))
			if value != "" && position == "" {
				position = value
			}
		}
	})
	if name == "" {
		name = profileName(doc, fullText)
	}
	return name, position
}

func profilePublicFields(doc *nethtml.Node) map[string]string {
	fields := make(map[string]string)
	labels := []string{"入职时间", "学历", "学位", "性别", "在职信息", "毕业院校", "所在单位", "主要任职"}
	var containers []*nethtml.Node
	walk(doc, func(node *nethtml.Node) {
		if node.Type != nethtml.ElementNode {
			return
		}
		if hasClass(node, "t_jbxx_nr") || hasClass(node, "bs-1") || attr(node, "id") == "contentscroll2" {
			containers = append(containers, node)
		}
	})
	for _, container := range containers {
		for _, line := range blockLines(container) {
			for _, label := range labels {
				if !strings.HasPrefix(line, label) || fields[label] != "" {
					continue
				}
				value := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line[len(label):]), " :："))
				if value != "" {
					fields[label] = sanitizePublicText(value)
				}
			}
		}
	}
	return fields
}

type profileContact struct {
	Email          string
	Phone          string
	OfficeLocation string
	PostalCode     string
}

// profileContactFields extracts the explicitly labelled contact fields. They
// are kept structured so the rest of a profile cannot accidentally inherit an
// email address or phone number while collecting long-form sections.
func profileContactFields(doc *nethtml.Node) profileContact {
	var contact profileContact
	// Contact layouts vary between the site's old and new templates. Walking
	// block boundaries from the whole document handles both <p>-based fields
	// and the newer <div>/<li> cards without copying any unlabeled text.
	lines := blockLines(doc)
	for _, line := range uniqueStrings(lines) {
		label, value := labelledValue(line, []string{"邮箱", "电子邮箱", "电子邮件", "email", "e-mail", "emial"})
		if label != "" && contact.Email == "" {
			contact.Email = sanitizeContactValue(value)
			continue
		}
		label, value = labelledValue(line, []string{"办公电话", "联系电话", "手机", "电话", "telephone", "tel"})
		if label != "" && contact.Phone == "" {
			contact.Phone = sanitizeContactValue(value)
			continue
		}
		label, value = labelledValue(line, []string{"通讯/办公地址", "办公地点", "办公地址", "联系地址", "地址", "office", "location"})
		if label != "" && contact.OfficeLocation == "" {
			contact.OfficeLocation = sanitizeContactValue(value)
			continue
		}
		label, value = labelledValue(line, []string{"邮编", "邮政编码", "postal code", "postcode"})
		if label != "" && contact.PostalCode == "" {
			contact.PostalCode = sanitizeContactValue(value)
		}
	}
	return contact
}

func labelledValue(line string, labels []string) (string, string) {
	line = normalizeText(line)
	lower := strings.ToLower(line)
	for _, rawLabel := range labels {
		label := normalizeText(rawLabel)
		if label == "" || !strings.HasPrefix(lower, strings.ToLower(label)) {
			continue
		}
		value := strings.TrimSpace(line[len(label):])
		value = strings.TrimLeft(value, " :：\t")
		return label, value
	}
	return "", ""
}

func sanitizeContactValue(value string) string {
	value = normalizeText(value)
	if value == "" || looksEncryptedContact(value) {
		return ""
	}
	if len([]rune(value)) > 500 {
		value = string([]rune(value)[:500])
	}
	return value
}

func profileSupervisorRoles(doc *nethtml.Node) string {
	var roles []string
	walk(doc, func(node *nethtml.Node) {
		if node.Type != nethtml.ElementNode || (!hasClass(node, "bsd") && !hasClass(node, "bs-1")) {
			return
		}
		for _, line := range blockLines(node) {
			if strings.Contains(line, "导师") {
				roles = append(roles, line)
			}
		}
	})
	return strings.Join(uniqueStrings(roles), " ")
}

func findSectionNode(root *nethtml.Node, heading string) *nethtml.Node {
	var found *nethtml.Node
	walk(root, func(node *nethtml.Node) {
		if found != nil || node.Type != nethtml.ElementNode || (node.Data != "h1" && node.Data != "h2" && node.Data != "h3") {
			return
		}
		if !strings.HasPrefix(normalizeText(nodeText(node)), heading) {
			return
		}
		for parent := node; parent != nil; parent = parent.Parent {
			if hasClass(parent, "home-bx") || hasClass(parent, "p_r_nr") || hasClass(parent, "pre-pro") {
				found = parent
				return
			}
		}
		found = node.Parent
	})
	if found != nil {
		return found
	}
	return findTabbedSection(root, heading)
}

func findTabbedSection(root *nethtml.Node, heading string) *nethtml.Node {
	for _, group := range profileTabGroups(root) {
		for index, tab := range group.tabs {
			if strings.HasPrefix(normalizeText(nodeText(tab)), heading) && index < len(group.contents) {
				return group.contents[index]
			}
		}
	}
	return nil
}

func sectionText(node *nethtml.Node, heading string) string {
	if node == nil {
		return ""
	}
	text := normalizeText(nodeText(node))
	if strings.HasPrefix(text, heading) {
		text = strings.TrimSpace(text[len(heading):])
	}
	for _, prefix := range []string{"Personal Profile", "Research Focus", "MORE +", "More"} {
		text = strings.TrimSpace(strings.TrimPrefix(text, prefix))
	}
	return sanitizePublicText(text)
}

var profileSectionAliases = []struct {
	Title   string
	Aliases []string
}{
	{Title: "个人信息", Aliases: []string{"个人信息", "personal information"}},
	{Title: "教育经历", Aliases: []string{"教育经历", "教育背景", "education experience", "educational background"}},
	{Title: "工作经历", Aliases: []string{"工作经历", "工作经验", "任职经历", "work experience", "employment history"}},
	{Title: "学术兼职", Aliases: []string{"学术兼职", "学术任职", "社会兼职", "社会任职", "academic appointments", "academic service"}},
	{Title: "科研项目", Aliases: []string{"科研项目", "研究项目", "承担项目", "research projects", "projects"}},
	{Title: "论文", Aliases: []string{"论文", "发表论文", "publications", "papers"}},
	{Title: "著作", Aliases: []string{"著作", "出版著作", "books", "book"}},
	{Title: "专利", Aliases: []string{"专利", "patents", "patent"}},
	{Title: "获奖与荣誉", Aliases: []string{"获奖", "获奖情况", "荣誉", "个人荣誉", "awards", "honors", "honours"}},
	{Title: "代表性成果", Aliases: []string{"代表性成果", "科研成果", "研究成果", "achievements", "selected achievements"}},
	{Title: "教学工作", Aliases: []string{"教学工作", "教学经历", "teaching", "teaching experience"}},
	{Title: "教学成果", Aliases: []string{"教学成果", "teaching achievements"}},
	{Title: "授课信息", Aliases: []string{"授课信息", "授课情况", "courses taught", "teaching resources"}},
	{Title: "团队成员", Aliases: []string{"团队成员", "研究团队", "research group", "team members"}},
	{Title: "团队介绍", Aliases: []string{"团队介绍", "团队简介", "研究团队介绍", "team introduction"}},
	{Title: "代表性论文", Aliases: []string{"代表性学术论文", "代表性论文", "selected publications"}},
	{Title: "招生信息", Aliases: []string{"招生信息", "招生", "admissions", "students"}},
}

func profileSectionTitle(raw string) string {
	value := normalizeText(raw)
	if value == "" {
		return ""
	}
	for _, section := range profileSectionAliases {
		for _, alias := range section.Aliases {
			if strings.HasPrefix(strings.ToLower(value), strings.ToLower(alias)) {
				return section.Title
			}
		}
	}
	return ""
}

func profileAdditionalSections(root *nethtml.Node) []ProfileSection {
	sections := make([]ProfileSection, 0)
	seen := make(map[string]bool)
	walk(root, func(node *nethtml.Node) {
		if node.Type != nethtml.ElementNode || (node.Data != "h1" && node.Data != "h2" && node.Data != "h3" && node.Data != "h4") {
			return
		}
		rawTitle := normalizeText(nodeText(node))
		title := profileSectionTitle(rawTitle)
		if title == "" || seen[title] {
			return
		}
		container := sectionContainerForHeading(node)
		content := profileSectionContent(container, rawTitle, title)
		if content == "" {
			return
		}
		seen[title] = true
		sections = append(sections, ProfileSection{Title: title, Content: content})
	})

	// Older faculty pages put the same sections in JavaScript tab groups and
	// do not use heading elements inside the tab content. The site has used
	// both Spry TabbedPanels and the older slideTxtBox/slideTxtBox2 markup.
	for _, group := range profileTabGroups(root) {
		for index, tab := range group.tabs {
			if index >= len(group.contents) {
				break
			}
			rawTitle := normalizeText(nodeText(tab))
			title := profileSectionTitle(rawTitle)
			if title == "" || seen[title] {
				continue
			}
			content := profileSectionContent(group.contents[index], "", title)
			if content == "" {
				continue
			}
			seen[title] = true
			sections = append(sections, ProfileSection{Title: title, Content: content})
		}
	}
	return sections
}

type profileTabGroup struct {
	tabs     []*nethtml.Node
	contents []*nethtml.Node
}

// profileTabGroups normalizes the two tab layouts found in the faculty
// templates. Keeping the pairing by DOM order is important: the same page can
// contain more than one independent tab strip (education/work and research/
// social affiliations).
func profileTabGroups(root *nethtml.Node) []profileTabGroup {
	var groups []profileTabGroup
	var tabbedTabs, tabbedContents []*nethtml.Node
	walk(root, func(node *nethtml.Node) {
		if node.Type != nethtml.ElementNode {
			return
		}
		switch {
		case hasClass(node, "TabbedPanelsTabGroup"):
			tabs := directElementChildrenByTag(node, "li")
			if len(tabs) > 0 {
				tabbedTabs = append(tabbedTabs, node)
			}
		case hasClass(node, "TabbedPanelsContentGroup"):
			contents := directElementChildrenWithClass(node, "TabbedPanelsContent")
			if len(contents) > 0 {
				tabbedContents = append(tabbedContents, node)
			}
		}
	})
	for index := 0; index < len(tabbedTabs) && index < len(tabbedContents); index++ {
		groups = append(groups, profileTabGroup{
			tabs:     directElementChildrenByTag(tabbedTabs[index], "li"),
			contents: directElementChildrenWithClass(tabbedContents[index], "TabbedPanelsContent"),
		})
	}

	walk(root, func(node *nethtml.Node) {
		if node.Type != nethtml.ElementNode || (!hasClass(node, "slideTxtBox") && !hasClass(node, "slideTxtBox2")) {
			return
		}
		head := firstDescendantWithClass(node, "hd")
		body := firstDescendantWithClass(node, "bd")
		if head == nil || body == nil {
			return
		}
		tabList := firstDescendantByTag(head, "ul")
		if tabList == nil {
			return
		}
		tabs := directElementChildrenByTag(tabList, "li")
		contents := directContentChildren(body)
		if len(tabs) == 0 || len(contents) == 0 {
			return
		}
		groups = append(groups, profileTabGroup{tabs: tabs, contents: contents})
	})
	return groups
}

func directElementChildrenByTag(root *nethtml.Node, tag string) []*nethtml.Node {
	var result []*nethtml.Node
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == nethtml.ElementNode && child.Data == tag {
			result = append(result, child)
		}
	}
	return result
}

func directElementChildrenWithClass(root *nethtml.Node, className string) []*nethtml.Node {
	var result []*nethtml.Node
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == nethtml.ElementNode && hasClass(child, className) {
			result = append(result, child)
		}
	}
	return result
}

func directContentChildren(root *nethtml.Node) []*nethtml.Node {
	var result []*nethtml.Node
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		if child.Type != nethtml.ElementNode || child.Data == "script" || child.Data == "style" || child.Data == "noscript" {
			continue
		}
		result = append(result, child)
	}
	return result
}

func firstDescendantWithClass(root *nethtml.Node, className string) *nethtml.Node {
	var found *nethtml.Node
	walk(root, func(node *nethtml.Node) {
		if found == nil && node != root && node.Type == nethtml.ElementNode && hasClass(node, className) {
			found = node
		}
	})
	return found
}

func firstDescendantByTag(root *nethtml.Node, tag string) *nethtml.Node {
	var found *nethtml.Node
	walk(root, func(node *nethtml.Node) {
		if found == nil && node != root && node.Type == nethtml.ElementNode && node.Data == tag {
			found = node
		}
	})
	return found
}

func sectionContainerForHeading(node *nethtml.Node) *nethtml.Node {
	for parent := node; parent != nil; parent = parent.Parent {
		if hasClass(parent, "home-bx") || hasClass(parent, "p_r_nr") || hasClass(parent, "pre-pro") {
			return parent
		}
	}
	return node.Parent
}

func profileSectionContent(node *nethtml.Node, rawTitle, canonicalTitle string) string {
	if node == nil {
		return ""
	}
	lines := blockLines(node)
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		line = normalizeText(line)
		if line == "" {
			continue
		}
		if rawTitle != "" && strings.HasPrefix(line, rawTitle) {
			line = strings.TrimSpace(strings.TrimPrefix(line, rawTitle))
		}
		if profileSectionTitle(line) == canonicalTitle {
			continue
		}
		line = sanitizePublicText(line)
		if line != "" {
			result = append(result, line)
		}
	}
	if len(result) == 0 {
		return sectionText(node, rawTitle)
	}
	return strings.Join(uniqueStrings(result), "\n")
}

func profileImageURL(root *nethtml.Node, name string) string {
	type candidate struct {
		url   string
		score int
	}
	best := candidate{}
	walk(root, func(node *nethtml.Node) {
		if node.Type != nethtml.ElementNode || node.Data != "img" {
			return
		}
		raw := firstNonEmpty(attr(node, "src"), attr(node, "data-src"), attr(node, "data-original"), attr(node, "data-lazy-src"), attr(node, "lazy-src"))
		raw = strings.TrimSpace(raw)
		if raw == "" || strings.HasPrefix(strings.ToLower(raw), "data:") || strings.HasPrefix(raw, "#") || strings.HasPrefix(strings.ToLower(raw), "javascript:") {
			return
		}
		metadata := strings.ToLower(strings.Join([]string{attr(node, "alt"), attr(node, "class"), attr(node, "id"), raw}, " "))
		score := 1
		if name != "" && strings.Contains(normalizeText(attr(node, "alt")), name) {
			score += 12
		}
		for _, keyword := range []string{"avatar", "portrait", "photo", "head", "teacher", "教师", "个人照片", "个人头像", "证件照"} {
			if strings.Contains(metadata, keyword) {
				score += 5
			}
		}
		for _, keyword := range []string{"logo", "banner", "icon", "favicon", "qrcode", "qr-code", "erweima", "background", "bg-"} {
			if strings.Contains(metadata, keyword) {
				score -= 10
			}
		}
		for parent := node.Parent; parent != nil && parent != root; parent = parent.Parent {
			parentMeta := strings.ToLower(attr(parent, "class") + " " + attr(parent, "id"))
			for _, keyword := range []string{"avatar", "portrait", "photo", "head", "teacher", "教师"} {
				if strings.Contains(parentMeta, keyword) {
					score += 4
				}
			}
			for _, keyword := range []string{"logo", "banner", "icon", "qrcode", "background"} {
				if strings.Contains(parentMeta, keyword) {
					score -= 8
				}
			}
		}
		if score > best.score || (score == best.score && len(raw) < len(best.url)) {
			best = candidate{url: raw, score: score}
		}
	})
	// The current source template leaves the avatar <img> without a src and
	// inserts it from ImageScale.addimg(...) in an inline script.
	walk(root, func(node *nethtml.Node) {
		if node.Type != nethtml.ElementNode || node.Data != "script" {
			return
		}
		var source strings.Builder
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if child.Type == nethtml.TextNode {
				source.WriteString(child.Data)
			}
		}
		for _, match := range imageScalePattern.FindAllStringSubmatch(source.String(), -1) {
			if best.score >= 20 || len(match) <= 1 || strings.TrimSpace(match[1]) == "" {
				continue
			}
			if len(match) > 1 && strings.TrimSpace(match[1]) != "" {
				best = candidate{url: strings.TrimSpace(match[1]), score: 20}
			}
		}
	})
	if best.score <= 0 {
		return ""
	}
	return best.url
}

func resolveProfileAsset(raw, pageURL, allowedHost string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	base, err := url.Parse(pageURL)
	if err != nil {
		return ""
	}
	ref, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	resolved := base.ResolveReference(ref)
	if (resolved.Scheme != "http" && resolved.Scheme != "https") || resolved.Hostname() == "" {
		return ""
	}
	if allowedHost != "" && !strings.EqualFold(resolved.Hostname(), allowedHost) {
		return ""
	}
	resolved.Fragment = ""
	return resolved.String()
}

func researchDirectionsFromNode(node *nethtml.Node) []string {
	if node == nil {
		return nil
	}
	var links []string
	walk(node, func(child *nethtml.Node) {
		if child.Type != nethtml.ElementNode || child.Data != "a" {
			return
		}
		value := sanitizePublicText(normalizeText(nodeText(child)))
		if value == "" || value == "更多" || strings.EqualFold(value, "more") {
			return
		}
		links = append(links, value)
	})
	if len(links) > 0 {
		return uniqueStrings(links)
	}
	return researchDirections(sectionText(node, "研究方向"))
}

func sanitizePublicText(value string) string {
	value = normalizeText(value)
	lower := strings.ToLower(value)
	end := len(value)
	for _, marker := range []string{"邮箱", "电子邮件", "email", "e-mail", "emial", "联系方式", "other contact information", "通讯/办公地址", "办公地点", "邮编", "联系电话", "电话", "telephone"} {
		if index := strings.Index(lower, strings.ToLower(marker)); index >= 0 && index < end {
			end = index
		}
	}
	value = strings.TrimSpace(value[:end])
	value = emailPattern.ReplaceAllString(value, "")
	value = hashEmailPattern.ReplaceAllString(value, "")
	value = phonePattern.ReplaceAllString(value, "")
	return normalizeText(value)
}

func hasClass(node *nethtml.Node, className string) bool {
	for _, class := range strings.Fields(attr(node, "class")) {
		if class == className {
			return true
		}
	}
	return false
}

func profileName(doc *nethtml.Node, text string) string {
	var title string
	walk(doc, func(node *nethtml.Node) {
		if title != "" || node.Type != nethtml.ElementNode || node.Data != "title" {
			return
		}
		title = normalizeText(nodeText(node))
	})
	if index := strings.Index(title, "教师主页"); index >= 0 {
		candidate := strings.TrimSpace(title[index+len("教师主页"):])
		if end := strings.Index(candidate, "--"); end >= 0 {
			candidate = strings.TrimSpace(candidate[:end])
		}
		if plausibleName(candidate) {
			return candidate
		}
	}
	var headings []string
	walk(doc, func(node *nethtml.Node) {
		if node.Type != nethtml.ElementNode || (node.Data != "h1" && node.Data != "h2" && node.Data != "h3") {
			return
		}
		candidate := normalizeText(nodeText(node))
		if plausibleName(candidate) {
			headings = append(headings, candidate)
		}
	})
	if len(headings) > 0 {
		return headings[0]
	}
	for _, line := range strings.Fields(text) {
		if plausibleName(line) && !strings.Contains(line, "西南交通大学") {
			return line
		}
	}
	return ""
}

func researchDirections(section string) []string {
	section = normalizeText(section)
	if section == "" {
		return nil
	}
	matches := bracketIndexPattern.Split(section, -1)
	if len(matches) > 1 {
		result := make([]string, 0, len(matches)-1)
		for _, item := range matches[1:] {
			item = strings.TrimSpace(item)
			if item != "" && item != "暂无内容" && !strings.EqualFold(item, "no content") {
				result = append(result, item)
			}
		}
		return result
	}
	if section == "暂无内容" || strings.EqualFold(section, "no content") {
		return nil
	}
	return []string{section}
}

func equalNames(a, b string) bool {
	return strings.EqualFold(strings.Join(strings.Fields(normalizeText(a)), ""), strings.Join(strings.Fields(normalizeText(b)), ""))
}

func parseHTML(body []byte) (*nethtml.Node, error) {
	decoded := decodeHTML(body)
	return nethtml.Parse(strings.NewReader(decoded))
}

func decodeHTML(body []byte) string {
	// The current faculty site declares UTF-8. Keeping decoding in the
	// standard-library path avoids pulling a broad charset table into the
	// crawler image; malformed bytes are handled by the HTML parser itself.
	return string(body)
}

func walk(node *nethtml.Node, visit func(*nethtml.Node)) {
	visit(node)
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		walk(child, visit)
	}
}

func attr(node *nethtml.Node, key string) string {
	for _, attribute := range node.Attr {
		if strings.EqualFold(attribute.Key, key) {
			return attribute.Val
		}
	}
	return ""
}

func nodeText(node *nethtml.Node) string {
	if node == nil {
		return ""
	}
	if node.Type == nethtml.TextNode {
		return node.Data + " "
	}
	if node.Type == nethtml.ElementNode {
		switch node.Data {
		case "script", "style", "noscript":
			return ""
		case "img":
			return attr(node, "alt") + " "
		}
	}
	var builder strings.Builder
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		builder.WriteString(nodeText(child))
	}
	return builder.String()
}

func blockLines(root *nethtml.Node) []string {
	var lines []string
	var buffer strings.Builder
	flush := func() {
		if value := normalizeText(buffer.String()); value != "" {
			lines = append(lines, value)
		}
		buffer.Reset()
	}
	var visit func(*nethtml.Node)
	visit = func(node *nethtml.Node) {
		if node.Type == nethtml.TextNode {
			buffer.WriteString(node.Data)
			return
		}
		if node.Type != nethtml.ElementNode {
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				visit(child)
			}
			return
		}
		switch node.Data {
		case "script", "style", "noscript":
			return
		case "img":
			buffer.WriteString(attr(node, "alt"))
			return
		case "br":
			flush()
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
		switch node.Data {
		case "p", "div", "li", "tr", "h1", "h2", "h3", "h4", "dt", "dd", "section":
			flush()
		}
	}
	visit(root)
	flush()
	return uniqueStrings(lines)
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func normalizeText(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

// SanitizeCrawlResult applies the public-data boundary to an already-built
// snapshot. It is also used by WriteJSON as a defense-in-depth measure for
// snapshots assembled by future importers.
func SanitizeCrawlResult(result CrawlResult) CrawlResult {
	for index := range result.Teachers {
		teacher := &result.Teachers[index]
		teacher.Position = sanitizePublicText(teacher.Position)
		teacher.SupervisorRoles = sanitizePublicText(teacher.SupervisorRoles)
		teacher.College = sanitizePublicText(teacher.College)
		teacher.PrimaryRole = sanitizePublicText(teacher.PrimaryRole)
		teacher.EntryDate = sanitizePublicText(teacher.EntryDate)
		teacher.EmploymentStatus = sanitizePublicText(teacher.EmploymentStatus)
		teacher.Education = sanitizePublicText(teacher.Education)
		teacher.Degree = sanitizePublicText(teacher.Degree)
		teacher.University = sanitizePublicText(teacher.University)
		teacher.Gender = sanitizePublicText(teacher.Gender)
		teacher.Email = sanitizeContactValue(teacher.Email)
		teacher.Phone = sanitizeContactValue(teacher.Phone)
		teacher.OfficeLocation = sanitizeContactValue(teacher.OfficeLocation)
		teacher.PostalCode = sanitizeContactValue(teacher.PostalCode)
		teacher.AvatarURL = resolveProfileAsset(teacher.AvatarURL, teacher.ProfileURL, sourceHost(result))
		teacher.Introduction = sanitizePublicText(teacher.Introduction)
		research := teacher.Research[:0]
		for _, direction := range teacher.Research {
			if direction = sanitizePublicText(direction); direction != "" {
				research = append(research, direction)
			}
		}
		teacher.Research = research
		sections := teacher.Sections[:0]
		for _, section := range teacher.Sections {
			title := normalizeText(section.Title)
			if title == "" || isContactSectionTitle(title) {
				continue
			}
			content := sanitizePublicText(section.Content)
			if content != "" {
				sections = append(sections, ProfileSection{Title: title, Content: content})
			}
		}
		teacher.Sections = sections
	}
	return result
}

func sourceHost(result CrawlResult) string {
	if host := strings.TrimSpace(result.Source["site"]); host != "" {
		return host
	}
	return ""
}

func isContactSectionTitle(value string) bool {
	lower := strings.ToLower(normalizeText(value))
	for _, marker := range []string{"联系方式", "联系信息", "通讯/办公地址", "办公地点", "办公地址", "contact", "email", "telephone", "phone"} {
		if strings.Contains(lower, strings.ToLower(marker)) {
			return true
		}
	}
	return false
}

// WriteJSON writes a crawl atomically so an API never consumes a half-written
// snapshot when a scheduled crawl overlaps a read.
func WriteJSON(path string, result CrawlResult) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("output path cannot be empty")
	}
	result = SanitizeCrawlResult(result)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".teachers-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()
	encoder := json.NewEncoder(tmp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
