package archive

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"swjtu-cli/pkg/sdk"
)

// SyncOptions controls one synchronization pass.
type SyncOptions struct {
	Backfill         time.Duration
	MaxPages         int
	MaxConcurrent    int
	RequestGap       time.Duration
	RefreshAfter     time.Duration
	MaxResourceBytes int64
	Logger           *log.Logger
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

	rateMu  sync.Mutex
	lastReq time.Time
	lockMu  sync.Mutex
	locks   map[string]*sync.Mutex
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
	if opts.RefreshAfter <= 0 {
		opts.RefreshAfter = 24 * time.Hour
	}
	if opts.MaxResourceBytes <= 0 {
		opts.MaxResourceBytes = 256 << 20
	}
	return &Syncer{store: store, client: client, opts: opts, locks: make(map[string]*sync.Mutex)}
}

// Store returns the backing archive store for the read-only HTTP layer.
func (s *Syncer) Store() *Store { return s.store }

// Sync performs one complete, resumable synchronization pass. A failed feed
// is recorded in the result while other feeds continue.
func (s *Syncer) Sync(ctx context.Context) (SyncResult, error) {
	started := time.Now()
	runID, err := s.store.StartRun(started)
	if err != nil {
		return SyncResult{}, err
	}
	result := SyncResult{RunID: runID}
	feeds := sdk.Feeds()
	for _, feed := range feeds {
		if err := s.store.UpsertFeed(feed, started); err != nil {
			result.Failures++
			result.Errors = append(result.Errors, fmt.Sprintf("保存 feed %s: %v", feed.ID, err))
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
				feedResult, feedErr := s.syncFeed(ctx, feed, started)
				resultMu.Lock()
				result.Feeds++
				result.Articles += feedResult.Articles
				result.Resources += feedResult.Resources
				result.Failures += feedResult.Failures
				if feedErr != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", feed.ID, feedErr))
				}
				result.Errors = append(result.Errors, feedResult.Errors...)
				resultMu.Unlock()
			}
		}()
	}
	for _, feed := range feeds {
		select {
		case jobs <- feed:
		case <-ctx.Done():
			break
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(jobs)
	wg.Wait()

	status := "success"
	var runErr error
	if ctx.Err() != nil {
		status = "canceled"
		runErr = ctx.Err()
	} else if result.Failures > 0 {
		status = "partial"
		if len(result.Errors) > 0 {
			runErr = fmt.Errorf("%d failures; first: %s", result.Failures, result.Errors[0])
		}
	}
	if err := s.store.FinishRun(runID, status, result.Feeds, result.Articles, result.Resources, result.Failures, runErr, time.Now()); err != nil {
		return result, err
	}
	if s.opts.Logger != nil {
		s.opts.Logger.Printf("sync run=%d status=%s feeds=%d articles=%d resources=%d failures=%d", runID, status, result.Feeds, result.Articles, result.Resources, result.Failures)
	}
	return result, ctx.Err()
}

type feedResult struct {
	Articles  int
	Resources int
	Failures  int
	Errors    []string
}

func (s *Syncer) syncFeed(ctx context.Context, feed sdk.Feed, now time.Time) (feedResult, error) {
	feedLock := s.siteLock(feed.SiteID)
	feedLock.Lock()
	defer feedLock.Unlock()

	if err := s.waitRequest(ctx); err != nil {
		return feedResult{}, err
	}
	items, err := s.client.ListFeedRange(ctx, feed, now.Add(-s.opts.Backfill), now.Add(24*time.Hour), s.opts.MaxPages)
	if err != nil {
		return feedResult{}, err
	}
	var result feedResult
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
		fresh, err := s.store.ArticleFresh(item.URL, now, s.opts.RefreshAfter)
		if err != nil {
			result.Failures++
			result.Errors = append(result.Errors, fmt.Sprintf("检查 %s: %v", item.URL, err))
			continue
		}
		if fresh {
			if err := s.store.TouchArticleMetadata(item.URL, feed, item, now); err != nil {
				result.Failures++
				result.Errors = append(result.Errors, fmt.Sprintf("更新 %s: %v", item.URL, err))
			}
			continue
		}

		if err := s.waitRequest(ctx); err != nil {
			return result, err
		}
		article, raw, err := s.client.FetchArticleSnapshot(ctx, item.URL)
		if err != nil {
			result.Failures++
			result.Errors = append(result.Errors, fmt.Sprintf("抓取 %s: %v", item.URL, err))
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
		rawPath, rawHash, err := s.store.SaveRaw(raw)
		if err != nil {
			result.Failures++
			result.Errors = append(result.Errors, fmt.Sprintf("保存原文 %s: %v", item.URL, err))
			continue
		}
		articleID, err := s.store.UpsertArticle(ArticleInput{
			Feed: feed, Item: item, Article: article, RawPath: rawPath, RawSHA256: rawHash, FetchedAt: time.Now(),
		})
		if err != nil {
			result.Failures++
			result.Errors = append(result.Errors, fmt.Sprintf("保存文章 %s: %v", item.URL, err))
			continue
		}
		result.Articles++

		seen := make(map[string]bool)
		for _, resourceURL := range article.Images {
			if seen[resourceURL] || resourceURL == "" {
				continue
			}
			seen[resourceURL] = true
			downloaded, err := s.archiveResource(ctx, articleID, "image", resourceURL, "", item.URL)
			if downloaded {
				result.Resources++
			}
			if err != nil {
				result.Failures++
				result.Errors = append(result.Errors, fmt.Sprintf("图片 %s: %v", resourceURL, err))
			}
		}
		for _, attachment := range article.Attachments {
			if seen[attachment.URL] || attachment.URL == "" {
				continue
			}
			seen[attachment.URL] = true
			downloaded, err := s.archiveResource(ctx, articleID, "attachment", attachment.URL, attachment.Name, item.URL)
			if downloaded {
				result.Resources++
			}
			if err != nil {
				result.Failures++
				result.Errors = append(result.Errors, fmt.Sprintf("附件 %s: %v", attachment.Name, err))
			}
		}
	}
	return result, nil
}

func (s *Syncer) archiveResource(ctx context.Context, articleID int64, kind, resourceURL, preferredName, referer string) (bool, error) {
	if existing, err := s.store.ExistingResource(articleID, kind, resourceURL); err == nil && existing.Status == "success" && existing.LocalPath != "" {
		if file, openErr := s.store.OpenResource(existing); openErr == nil {
			file.Close()
			return false, nil
		}
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if err := s.waitRequest(ctx); err != nil {
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
			filename = preferredName
		}
		localPath, hash, size, saveErr := s.store.SaveResourceBody(resource.Body, s.opts.MaxResourceBytes, filename, resource.ContentType)
		resource.Body.Close()
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

func resourceRetryable(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
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

func (s *Syncer) waitRequest(ctx context.Context) error {
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	wait := s.opts.RequestGap - time.Since(s.lastReq)
	if wait <= 0 {
		s.lastReq = time.Now()
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		s.lastReq = time.Now()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
