package archive

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"swjtu-cli/pkg/sdk"
)

// SyncOptions controls one synchronization pass.
type SyncOptions struct {
	Backfill      time.Duration
	MaxPages      int
	MaxConcurrent int
	RequestGap    time.Duration
	// RefetchAfter is how long after its first fetch an archived article is
	// fetched once more. A changed re-fetch is stored as a new version.
	RefetchAfter time.Duration
	// ListWindow bounds the listing crawl between full backfill listings:
	// most syncs only need the newest pages of each feed.
	ListWindow time.Duration
	// FullListEvery is how often each feed is listed across the whole
	// Backfill window, which picks up older articles a short listing missed
	// (for example after a failed run).
	FullListEvery    time.Duration
	MaxResourceBytes int64
	ResourceUploader ResourceUploader
	RawUploader      RawPageUploader
	Logger           *log.Logger
}

// ResourceUploader receives a successfully downloaded resource before it is
// exposed as a successful resource record. Fresh downloads are handed over
// in memory and never touch the host disk; Upload remains for legacy local
// copies that predate the direct path. The content-addressed local_path
// stays in SQLite so the public object-store URL can be derived
// deterministically.
type ResourceUploader interface {
	Upload(ctx context.Context, fullPath, localPath, contentType, filename, kind, storageClass string) error
	// UploadBytes uploads an in-memory resource so freshly downloaded assets
	// go straight to the object store without touching the host disk.
	UploadBytes(ctx context.Context, data []byte, localPath, contentType, filename, kind, storageClass string) error
}

// RawPageUploader stores fetched source HTML outside the service host.
type RawPageUploader interface {
	UploadRaw(ctx context.Context, data []byte, localPath string) error
}

// SyncResult reports work performed by one synchronization pass.
type SyncResult struct {
	RunID     int64    `json:"run_id"`
	Feeds     int      `json:"feeds"`
	Articles  int      `json:"articles"`
	Resources int      `json:"resources"`
	Failures  int      `json:"failures"`
	Errors    []string `json:"errors,omitempty"`
}

// Syncer discovers feeds through the SDK and persists their articles/assets.
type Syncer struct {
	store  *Store
	client *sdk.Client
	opts   SyncOptions

	lockMu     sync.Mutex
	locks      map[string]*sync.Mutex
	gates      map[string]*hostGate
	fullListed map[string]time.Time
}

// hostGate spaces requests to one host by RequestGap.
type hostGate struct {
	mu   sync.Mutex
	last time.Time
}

func NewSyncer(store *Store, client *sdk.Client, opts SyncOptions) *Syncer {
	if opts.Backfill <= 0 {
		opts.Backfill = 365 * 24 * time.Hour
	}
	if opts.MaxPages <= 0 {
		opts.MaxPages = 100
	}
	if opts.MaxConcurrent <= 0 {
		opts.MaxConcurrent = 4
	}
	if opts.RefetchAfter <= 0 {
		opts.RefetchAfter = 14 * 24 * time.Hour
	}
	if opts.ListWindow <= 0 {
		opts.ListWindow = 14 * 24 * time.Hour
	}
	if opts.FullListEvery <= 0 {
		opts.FullListEvery = 24 * time.Hour
	}
	if opts.MaxResourceBytes <= 0 {
		opts.MaxResourceBytes = 256 << 20
	}
	syncer := &Syncer{
		store: store, client: client, opts: opts,
		locks: make(map[string]*sync.Mutex), gates: make(map[string]*hostGate), fullListed: make(map[string]time.Time),
	}
	// Seeding the full-list schedule from the database keeps a container
	// restart from turning the next sync into a whole-archive backfill.
	if store != nil {
		if states, err := store.LoadFeedListStates(); err != nil {
			if opts.Logger != nil {
				opts.Logger.Printf("load feed list state: %v (starting with an empty schedule)", err)
			}
		} else {
			syncer.fullListed = states
		}
	}
	return syncer
}

// Store returns the backing archive store for the read-only HTTP layer.
func (s *Syncer) Store() *Store { return s.store }

// Sync performs one complete, resumable synchronization pass. A failed feed
// is recorded in the result while other feeds continue. When
// FEISHU_WEBHOOK_URL is set, a run with failures reports a summary to the
// Feishu webhook, and a severe error (revoked object-store credentials, auth
// failure) aborts the run immediately with an alert.
func (s *Syncer) Sync(ctx context.Context) (SyncResult, error) {
	started := time.Now()
	runID, err := s.store.StartRun(started)
	if err != nil {
		return SyncResult{}, err
	}
	// Bounded retention for the per-run error log; a failed prune must not
	// stop the sync itself.
	if err := s.store.PruneRunErrors(30*24*time.Hour, started); err != nil && s.opts.Logger != nil {
		s.opts.Logger.Printf("prune old sync run errors: %v", err)
	}
	collector := newSyncErrorCollector()
	// runCtx carries the abort triggered by a severe error; without one it
	// behaves exactly like the parent ctx.
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	var severeMu sync.Mutex
	var severeErr error
	claimSevere := func(err error) bool {
		severeMu.Lock()
		defer severeMu.Unlock()
		if severeErr != nil {
			return false
		}
		severeErr = err
		return true
	}

	result := SyncResult{RunID: runID}
	var runErrs []RunError
	var runErrsMu sync.Mutex
	feeds := sdk.Feeds()
	for _, feed := range feeds {
		if err := s.store.UpsertFeed(feed, started); err != nil {
			result.Failures++
			result.Errors = append(result.Errors, fmt.Sprintf("保存 feed %s: %v", feed.ID, err))
			runErrsMu.Lock()
			runErrs = append(runErrs, RunError{FeedID: feed.ID, Message: fmt.Sprintf("保存 feed: %v", err)})
			runErrsMu.Unlock()
			if severeSyncError(err.Error()) && claimSevere(err) {
				notifyCard(s.opts.Logger, buildSevereAlertCard(runID, err.Error(), result, started, time.Now()))
				cancelRun()
				break
			}
		}
	}

	jobs := make(chan sdk.Feed)
	var wg sync.WaitGroup
	var resultMu sync.Mutex
	for i := 0; i < s.opts.MaxConcurrent; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for feed := range jobs {
				if runCtx.Err() != nil {
					return
				}
				feedResult, feedErr := s.syncFeedSafe(runCtx, feed, started)
				resultMu.Lock()
				result.Feeds++
				result.Articles += feedResult.Articles
				result.Resources += feedResult.Resources
				result.Failures += feedResult.Failures
				if feedErr != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", feed.ID, feedErr))
				}
				result.Errors = append(result.Errors, feedResult.Errors...)
				severe := ""
				if feedErr != nil && severeSyncError(feedErr.Error()) {
					severe = fmt.Sprintf("%s: %v", feed.ID, feedErr)
				}
				for _, message := range feedResult.Errors {
					if severe == "" && severeSyncError(message) {
						severe = message
					}
				}
				resultMu.Unlock()
				if len(feedResult.Errors) > 0 || feedErr != nil {
					runErrsMu.Lock()
					if feedErr != nil {
						runErrs = append(runErrs, RunError{FeedID: feed.ID, Message: feedErr.Error()})
					}
					for _, message := range feedResult.Errors {
						runErrs = append(runErrs, RunError{FeedID: feed.ID, Message: message})
					}
					runErrsMu.Unlock()
				}
				if severe != "" && claimSevere(errors.New(severe)) {
					// Bad credentials fail every remaining feed too; stop
					// the run and alert now instead of letting the whole
					// pass limp to its end.
					cancelRun()
					notifyCard(s.opts.Logger, buildSevereAlertCard(runID, severe, result, started, time.Now()))
				}
			}
		}()
	}
	for _, feed := range feeds {
		select {
		case jobs <- feed:
		case <-runCtx.Done():
			break
		}
		if runCtx.Err() != nil {
			break
		}
	}
	close(jobs)
	wg.Wait()

	// Persist the per-feed failure messages before the run row is finalized
	// so a burst can be analyzed from the database long after its card faded.
	// A persistence failure must not turn a finished sync into a failed one.
	if err := s.store.AddRunErrors(runID, runErrs, time.Now()); err != nil && s.opts.Logger != nil {
		s.opts.Logger.Printf("persist sync run errors for run=%d: %v", runID, err)
	}

	status := "success"
	var runErr error
	if runCtx.Err() != nil {
		status = "canceled"
		runErr = runCtx.Err()
		severeMu.Lock()
		severe := severeErr
		severeMu.Unlock()
		if severe != nil {
			status = "failed"
			runErr = fmt.Errorf("severe error, sync aborted: %w", severe)
		}
	} else if result.Failures > 0 {
		status = "partial"
		if len(result.Errors) > 0 {
			runErr = fmt.Errorf("%d failures; first: %s", result.Failures, result.Errors[0])
		}
	}
	finished := time.Now()
	if err := s.store.NormalizeRunSeenAt(started, finished); err != nil {
		result.Failures++
		result.Errors = append(result.Errors, fmt.Sprintf("统一采集结束时间: %v", err))
		if runErr == nil {
			runErr = err
		}
		status = "partial"
	}
	if err := s.store.FinishRun(runID, status, result.Feeds, result.Articles, result.Resources, result.Failures, runErr, finished); err != nil {
		return result, err
	}
	if s.opts.Logger != nil {
		s.opts.Logger.Printf("sync run=%d status=%s feeds=%d articles=%d resources=%d failures=%d", runID, status, result.Feeds, result.Articles, result.Resources, result.Failures)
	}
	if result.Failures > 0 {
		for _, message := range result.Errors {
			collector.add(message)
		}
		notifyCard(s.opts.Logger, buildSyncSummaryCard(result, started, time.Now(), collector.snapshot()))
	} else {
		notifyCard(s.opts.Logger, buildSyncSuccessCard(result, started, time.Now()))
	}
	severeMu.Lock()
	severe := severeErr
	severeMu.Unlock()
	if severe != nil {
		return result, fmt.Errorf("severe error, sync aborted: %w", severe)
	}
	return result, ctx.Err()
}

type feedResult struct {
	Articles  int
	Resources int
	Failures  int
	Errors    []string
}

// syncFeedSafe runs syncFeed and converts a panic in a site adapter into a
// feed-level failure. One malformed source page must not kill a multi-hour
// run that still has dozens of feeds to archive.
func (s *Syncer) syncFeedSafe(ctx context.Context, feed sdk.Feed, now time.Time) (result feedResult, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("adapter panic: %v", recovered)
		}
	}()
	return s.syncFeed(ctx, feed, now)
}

func (s *Syncer) syncFeed(ctx context.Context, feed sdk.Feed, now time.Time) (feedResult, error) {
	feedLock := s.siteLock(feed.SiteID)
	feedLock.Lock()
	defer feedLock.Unlock()

	if err := s.waitRequest(ctx, feed.BaseURL); err != nil {
		return feedResult{}, err
	}
	var result feedResult
	// Walking a year of list pages on every run dominated sync time. Only
	// list the full Backfill window once per FullListEvery per feed; other
	// runs stop at ListWindow, which still catches every new article.
	from := now.Add(-s.opts.Backfill)
	full := s.fullListDue(feed.ID, now)
	if !full && now.Add(-s.opts.ListWindow).After(from) {
		from = now.Add(-s.opts.ListWindow)
	}
	items, listErr := s.client.ListFeedRange(ctx, feed, from, now.Add(24*time.Hour), s.opts.MaxPages)
	if listErr == nil && full {
		s.markFullListed(feed.ID, now)
	}
	if listErr != nil {
		if ctx.Err() != nil {
			return result, listErr
		}
		// The scraper returns pages already obtained together with a later-page
		// error. Persist those partial items, but make the feed failure visible
		// in the run instead of silently dropping the whole feed.
		result.Failures++
		result.Errors = append(result.Errors, fmt.Sprintf("列表 %s: %v", feed.ID, listErr))
		if len(items) == 0 {
			return result, nil
		}
	}
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		// The feed/tab is the authoritative type when a site does not expose a
		// separate article-type field. Keep it on the item so the archive and
		// downstream APIs do not lose which tab produced the article.
		if item.Type == "" {
			item.Type = feed.Name
		}
		// An archived article is fetched once more RefetchAfter after its
		// first fetch, to catch early corrections; after that only its
		// listing metadata is touched.
		state, err := s.store.FetchState(item.URL)
		if err != nil {
			result.Failures++
			result.Errors = append(result.Errors, fmt.Sprintf("检查 %s: %v", item.URL, err))
			continue
		}
		if state.Exists && (state.Refetched || now.Sub(state.FirstFetchedAt) < s.opts.RefetchAfter) {
			if err := s.store.TouchArticleMetadata(item.URL, feed, item, now); err != nil {
				result.Failures++
				result.Errors = append(result.Errors, fmt.Sprintf("更新 %s: %v", item.URL, err))
			}
			continue
		}

		if err := s.waitRequest(ctx, item.URL); err != nil {
			return result, err
		}
		article, raw, err := s.client.FetchArticleSnapshot(ctx, item.URL)
		if err != nil {
			result.Failures++
			result.Errors = append(result.Errors, fmt.Sprintf("抓取 %s: %v", item.URL, err))
			continue
		}
		if !articleHasAnyContent(article) {
			// Deleted source pages and external-link placeholders parse as an
			// empty shell. Storing them produced blank archive pages that the
			// next sync would happily fetch again, so skip them entirely.
			result.Failures++
			result.Errors = append(result.Errors, fmt.Sprintf("跳过空页面 %s: 无正文、图片或附件", item.URL))
			continue
		}
		if article.Type == "" {
			article.Type = item.Type
		}
		if article.PublishedAt == "" {
			article.PublishedAt = item.PublishedAt
		}
		if article.Date == "" {
			article.Date = item.Date
		}
		var rawPath, rawHash string
		if s.opts.RawUploader != nil {
			rawPath, rawHash = s.store.RawMetadata(raw)
			err = s.opts.RawUploader.UploadRaw(ctx, raw, rawPath)
		} else {
			rawPath, rawHash, err = s.store.SaveRaw(raw)
		}
		if err != nil {
			result.Failures++
			result.Errors = append(result.Errors, fmt.Sprintf("保存原文 %s: %v", item.URL, err))
			continue
		}
		input := ArticleInput{Feed: feed, Item: item, Article: article, RawPath: rawPath, RawSHA256: rawHash, FetchedAt: time.Now()}
		var articleID int64
		if state.Exists {
			articleID, _, err = s.store.SaveRefetch(input, articleResourceURLs(article))
		} else {
			articleID, err = s.store.UpsertArticle(input)
		}
		if err != nil {
			result.Failures++
			result.Errors = append(result.Errors, fmt.Sprintf("保存文章 %s: %v", item.URL, err))
			continue
		}
		result.Articles++

		seen := make(map[string]bool)
		resourceErr := false
		for _, resourceURL := range article.Images {
			if seen[resourceURL] || resourceURL == "" {
				continue
			}
			seen[resourceURL] = true
			downloaded, err := s.archiveResource(ctx, articleID, "image", resourceURL, "", item.URL, articlePublishedAt(article, item))
			if downloaded {
				result.Resources++
			}
			if err != nil {
				resourceErr = true
				result.Failures++
				result.Errors = append(result.Errors, fmt.Sprintf("图片 %s: %v", resourceURL, err))
			}
		}
		for _, attachment := range article.Attachments {
			if seen[attachment.URL] || attachment.URL == "" {
				continue
			}
			seen[attachment.URL] = true
			downloaded, err := s.archiveResource(ctx, articleID, "attachment", attachment.URL, attachment.Name, item.URL, articlePublishedAt(article, item))
			if downloaded {
				result.Resources++
			}
			if err != nil {
				resourceErr = true
				result.Failures++
				result.Errors = append(result.Errors, fmt.Sprintf("附件 %s: %v", attachment.Name, err))
			}
		}
		if !resourceErr {
			if err := s.store.RemoveStaleFailedResources(articleID, seen); err != nil {
				result.Failures++
				result.Errors = append(result.Errors, fmt.Sprintf("清理文章 %s 的旧资源记录: %v", item.URL, err))
			}
		}
	}
	return result, nil
}

func (s *Syncer) archiveResource(ctx context.Context, articleID int64, kind, resourceURL, preferredName, referer, publishedAt string) (bool, error) {
	if !isDownloadableResourceURL(resourceURL) {
		return false, nil
	}
	if existing, err := s.store.ExistingResource(articleID, kind, resourceURL); err == nil && existing.Status == "success" && existing.LocalPath != "" {
		if file, openErr := s.store.OpenResource(existing); openErr == nil {
			file.Close()
			if s.opts.ResourceUploader != nil {
				fullPath := filepath.Join(s.store.DataDir(), filepath.FromSlash(existing.LocalPath))
				storageClass := resourceStorageClass(kind, publishedAt, time.Now())
				if uploadErr := s.opts.ResourceUploader.Upload(ctx, fullPath, existing.LocalPath, existing.ContentType, safeFilename(existing.Filename), kind, storageClass); uploadErr != nil {
					return false, fmt.Errorf("上传已有本地资源到对象存储: %w", uploadErr)
				}
				if removeErr := os.Remove(fullPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
					return false, fmt.Errorf("删除已有本地资源副本: %w", removeErr)
				}
			}
			return false, nil
		}
		// With an object-store uploader enabled, a successful resource may
		// intentionally have no local file: successful uploads are cleaned up
		// immediately to keep the host disk bounded. local_path is retained as
		// the deterministic object key, so do not redownload it on every refresh.
		if s.opts.ResourceUploader != nil {
			return false, nil
		}
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if err := s.waitRequest(ctx, resourceURL); err != nil {
			return false, err
		}
		resource, err := s.client.DownloadResourceWithReferer(ctx, resourceURL, referer)
		if err != nil {
			lastErr = err
			// A response-header timeout is deterministic for a dead legacy
			// asset host. Retrying it three times only stalls the whole feed;
			// keep the failure recorded and move on to the next resource.
			if !resourceRetryable(err) {
				break
			}
			continue
		}
		filename := resource.Filename
		if preferredName != "" {
			filename = preferredNameWithExtension(preferredName, resource.Filename)
		}
		var localPath, hash string
		var size int64
		var saveErr error
		if s.opts.ResourceUploader != nil {
			// Straight to the object store: the asset never lands on the host
			// disk, so the data volume only holds the database and raw pages.
			var data []byte
			data, localPath, hash, size, saveErr = BufferResourceBody(resource.Body, s.opts.MaxResourceBytes, filename, resource.ContentType)
			resource.Body.Close()
			if saveErr == nil {
				storageClass := resourceStorageClass(kind, publishedAt, time.Now())
				saveErr = s.opts.ResourceUploader.UploadBytes(ctx, data, localPath, resource.ContentType, safeFilename(filename), kind, storageClass)
			}
		} else {
			localPath, hash, size, saveErr = s.store.SaveResourceBody(resource.Body, s.opts.MaxResourceBytes, filename, resource.ContentType)
			resource.Body.Close()
		}
		if saveErr == nil {
			_, err = s.store.UpsertResource(ResourceInput{
				ArticleID: articleID, Kind: kind, OriginalURL: resourceURL, LocalPath: localPath,
				Filename: safeFilename(filename), ContentType: resource.ContentType, ByteSize: size,
				SHA256: hash, Status: "success", UpdatedAt: time.Now(),
			})
			if err == nil {
				return true, nil
			}
		}
		lastErr = saveErr
		if lastErr == nil {
			lastErr = err
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("resource download failed")
	}
	_, _ = s.store.UpsertResource(ResourceInput{
		ArticleID: articleID, Kind: kind, OriginalURL: resourceURL,
		Filename: safeFilename(preferredName), Status: "failed", Error: lastErr.Error(), UpdatedAt: time.Now(),
	})
	return false, lastErr
}

// preferredNameWithExtension keeps the source page's attachment label as the
// stored filename, but borrows the extension from the download response
// (Content-Disposition header or URL) when the label itself has none. Source
// pages frequently label attachments with bare text such as "附件1" while
// the link target carries the real extension.
func preferredNameWithExtension(name, responseFilename string) string {
	if ext := filepath.Ext(name); ext != "" && len(ext) <= 10 {
		return name
	}
	ext := strings.ToLower(filepath.Ext(responseFilename))
	if len(ext) < 2 || len(ext) > 10 || dynamicPageExtensions[ext] {
		return name
	}
	return name + ext
}

// dynamicPageExtensions are URL suffixes of CMS download endpoints rather
// than of the files they serve; they must never be borrowed as a filename
// extension.
var dynamicPageExtensions = map[string]bool{
	".jsp": true, ".jspx": true, ".php": true, ".asp": true, ".aspx": true,
	".do": true, ".action": true, ".htm": true, ".html": true, ".shtml": true,
}

func articlePublishedAt(article *sdk.Article, item sdk.NewsItem) string {
	if article != nil {
		if value := strings.TrimSpace(article.PublishedAt); value != "" {
			return value
		}
		if value := strings.TrimSpace(article.Date); value != "" {
			return value
		}
	}
	if value := strings.TrimSpace(item.PublishedAt); value != "" {
		return value
	}
	return strings.TrimSpace(item.Date)
}

// articleResourceURLs lists the downloadable image and attachment URLs of an
// article, matching the rows archiveResource records.
func articleResourceURLs(article *sdk.Article) []string {
	seen := make(map[string]bool)
	var urls []string
	add := func(value string) {
		if value != "" && !seen[value] && isDownloadableResourceURL(value) {
			seen[value] = true
			urls = append(urls, value)
		}
	}
	for _, image := range article.Images {
		add(image)
	}
	for _, attachment := range article.Attachments {
		add(attachment.URL)
	}
	return urls
}

// articleHasAnyContent reports whether a fetched article carries anything
// worth archiving: text, HTML, images, or attachments. A detail page whose
// parse yields none of these is a shell (deleted source page, external-link
// placeholder) and is skipped instead of stored as a blank article.
func articleHasAnyContent(article *sdk.Article) bool {
	if article == nil {
		return false
	}
	return strings.TrimSpace(article.Content) != "" ||
		strings.TrimSpace(article.ContentHTML) != "" ||
		len(article.Images) > 0 ||
		len(article.Attachments) > 0
}

func (s *Syncer) fullListDue(feedID string, now time.Time) bool {
	s.lockMu.Lock()
	defer s.lockMu.Unlock()
	last, ok := s.fullListed[feedID]
	return !ok || now.Sub(last) >= s.opts.FullListEvery
}

func (s *Syncer) markFullListed(feedID string, now time.Time) {
	s.lockMu.Lock()
	s.fullListed[feedID] = now
	s.lockMu.Unlock()
	// The write is best-effort: the in-memory mark above already prevents an
	// immediate re-list inside this process, and a failed upsert must not
	// fail the feed that just completed its full walk.
	if s.store != nil {
		if err := s.store.MarkFeedFullListed(feedID, now); err != nil && s.opts.Logger != nil {
			s.opts.Logger.Printf("persist full-list state for %s: %v", feedID, err)
		}
	}
}

func resourceStorageClass(kind, publishedAt string, now time.Time) string {
	// Every newly mirrored resource uses frequent-access storage. The storage
	// class of an object already in R2 is deliberately not changed by syncs.
	return "STANDARD"
}

func isDownloadableResourceURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

func resourceRetryable(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	message := err.Error()
	if strings.Contains(message, "received HTML instead of a downloadable resource") {
		return false
	}
	if index := strings.LastIndex(message, "status "); index >= 0 {
		var status int
		if _, scanErr := fmt.Sscanf(message[index:], "status %d", &status); scanErr == nil && status >= 400 && status < 500 && status != 429 {
			return false
		}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return false
	}
	return true
}

func (s *Syncer) siteLock(siteID string) *sync.Mutex {
	s.lockMu.Lock()
	defer s.lockMu.Unlock()
	lock := s.locks[siteID]
	if lock == nil {
		lock = &sync.Mutex{}
		s.locks[siteID] = lock
	}
	return lock
}

// waitRequest spaces requests per host. A single global gap serialized every
// worker behind one clock, so MaxConcurrent bought almost no throughput;
// politeness only needs to hold per source server.
func (s *Syncer) waitRequest(ctx context.Context, rawURL string) error {
	gate := s.hostGate(rawURL)
	gate.mu.Lock()
	defer gate.mu.Unlock()
	wait := s.opts.RequestGap - time.Since(gate.last)
	if wait <= 0 {
		gate.last = time.Now()
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		gate.last = time.Now()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Syncer) hostGate(rawURL string) *hostGate {
	host := ""
	if u, err := url.Parse(strings.TrimSpace(rawURL)); err == nil {
		host = strings.ToLower(u.Hostname())
	}
	s.lockMu.Lock()
	defer s.lockMu.Unlock()
	gate := s.gates[host]
	if gate == nil {
		gate = &hostGate{}
		s.gates[host] = gate
	}
	return gate
}
