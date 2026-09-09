package archive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"swjtu-cli/pkg/sdk"
)

type timeoutResourceError struct{}

func (timeoutResourceError) Error() string   { return "resource timeout" }
func (timeoutResourceError) Timeout() bool   { return true }
func (timeoutResourceError) Temporary() bool { return true }

var _ net.Error = timeoutResourceError{}

func TestResourceRetryable(t *testing.T) {
	if resourceRetryable(errors.New("connection reset")) != true {
		t.Fatal("connection reset should be retried")
	}
	if resourceRetryable(fmt.Errorf("wrapped: %w", timeoutResourceError{})) {
		t.Fatal("timeout should not be retried")
	}
	if resourceRetryable(fmt.Errorf("wrapped: %w", context.DeadlineExceeded)) {
		t.Fatal("deadline should not be retried")
	}
	if resourceRetryable(fmt.Errorf("wrapped: %w", context.Canceled)) {
		t.Fatal("cancellation should not be retried")
	}
	for _, err := range []error{
		errors.New("fetch resource: status 400"),
		errors.New("fetch resource: status 404"),
		errors.New("fetch resource: received HTML instead of a downloadable resource"),
	} {
		if resourceRetryable(err) {
			t.Fatalf("deterministic resource error should not be retried: %v", err)
		}
	}
	if !resourceRetryable(errors.New("fetch resource: status 429")) {
		t.Fatal("rate limiting should be retried")
	}
}

func TestIsDownloadableResourceURL(t *testing.T) {
	for _, raw := range []string{"https://example.com/image.png", "http://example.com/file.pdf"} {
		if !isDownloadableResourceURL(raw) {
			t.Fatalf("expected downloadable URL: %q", raw)
		}
	}
	for _, raw := range []string{"data:image/png;base64,abc", "javascript:void(0)", "/relative/file.pdf", ""} {
		if isDownloadableResourceURL(raw) {
			t.Fatalf("expected non-downloadable URL: %q", raw)
		}
	}
}

func TestResourceStorageClass(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	tests := []struct {
		name, kind, publishedAt, want string
	}{
		{name: "old attachment", kind: "attachment", publishedAt: "2026-08-05 11:59:00", want: "STANDARD"},
		{name: "recent attachment", kind: "attachment", publishedAt: "2026-08-05 12:01:00", want: "STANDARD"},
		{name: "old image", kind: "image", publishedAt: "2026-01-01", want: "STANDARD"},
		{name: "unknown date", kind: "attachment", publishedAt: "", want: "STANDARD"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := resourceStorageClass(test.kind, test.publishedAt, now); got != test.want {
				t.Fatalf("resourceStorageClass() = %q, want %q", got, test.want)
			}
		})
	}
}

type recordingResourceUploader struct {
	calls    int
	fullPath string
	contents []byte
}

func (u *recordingResourceUploader) Upload(_ context.Context, fullPath, _ string, _ string, _ string, _ string, _ string) error {
	u.calls++
	u.fullPath = fullPath
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return err
	}
	u.contents = data
	return nil
}

func (u *recordingResourceUploader) UploadBytes(_ context.Context, data []byte, localPath, _ string, _ string, _ string, _ string) error {
	u.calls++
	u.fullPath = localPath
	u.contents = data
	return nil
}

func TestArchiveResourceUploadsStraightToObjectStore(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	feed := sdk.Feed{ID: "news:test", SiteID: "news", SiteName: "新闻网", Category: "university", Slug: "test", Name: "测试", BaseURL: "https://news.example"}
	item := sdk.NewsItem{Title: "资源清理测试文章", URL: "https://news.example/article.htm", Date: "2026-09-04"}
	articleID, err := store.UpsertArticle(ArticleInput{
		Feed: feed, Item: item, FetchedAt: time.Now(),
		Article: &sdk.Article{Title: item.Title, Date: item.Date, Content: "正文"},
	})
	if err != nil {
		t.Fatal(err)
	}

	const payload = "resource-payload"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/image.png" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = io.WriteString(w, payload)
	}))
	defer server.Close()
	uploader := &recordingResourceUploader{}
	syncer := NewSyncer(store, sdk.New(sdk.Options{HTTPClient: server.Client()}), SyncOptions{
		RequestGap:       0,
		ResourceUploader: uploader,
	})

	downloaded, err := syncer.archiveResource(context.Background(), articleID, "image", server.URL+"/image.png", "", item.URL, item.Date)
	if err != nil {
		t.Fatal(err)
	}
	if !downloaded {
		t.Fatal("resource was not archived")
	}
	if uploader.calls != 1 || string(uploader.contents) != payload {
		t.Fatalf("uploader calls/content = %d/%q, want 1/%q", uploader.calls, uploader.contents, payload)
	}
	if _, err := os.Stat(filepath.Join(store.DataDir(), filepath.FromSlash(uploader.fullPath))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("resource touched the local disk: %v", err)
	}

	resource, err := store.ExistingResource(articleID, "image", server.URL+"/image.png")
	if err != nil {
		t.Fatal(err)
	}
	if resource.Status != "success" || resource.LocalPath == "" {
		t.Fatalf("resource record = %#v, want successful content-addressed record", resource)
	}
	if _, err := store.OpenResource(resource); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("OpenResource error = %v, want local copy to be absent", err)
	}
	if downloaded, err := syncer.archiveResource(context.Background(), articleID, "image", server.URL+"/image.png", "", item.URL, item.Date); err != nil || downloaded {
		t.Fatalf("second archiveResource() = downloaded=%v err=%v, want remote success to be reused", downloaded, err)
	}
	if uploader.calls != 1 {
		t.Fatalf("uploader calls after remote reuse = %d, want 1", uploader.calls)
	}
}

func TestArchiveExistingLocalResourceUploadsThenRemovesCopy(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	feed := sdk.Feed{ID: "news:existing", SiteID: "news", SiteName: "新闻网", Category: "university", Slug: "existing", Name: "测试", BaseURL: "https://news.example"}
	item := sdk.NewsItem{Title: "已有本地资源测试文章", URL: "https://news.example/existing.htm", Date: "2026-09-04"}
	articleID, err := store.UpsertArticle(ArticleInput{
		Feed: feed, Item: item, FetchedAt: time.Now(),
		Article: &sdk.Article{Title: item.Title, Date: item.Date, Content: "正文"},
	})
	if err != nil {
		t.Fatal(err)
	}

	const payload = "existing-resource-payload"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/attachment.pdf" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = io.WriteString(w, payload)
	}))
	defer server.Close()

	withoutUploader := NewSyncer(store, sdk.New(sdk.Options{HTTPClient: server.Client()}), SyncOptions{RequestGap: 0})
	if downloaded, err := withoutUploader.archiveResource(context.Background(), articleID, "attachment", server.URL+"/attachment.pdf", "notice.pdf", item.URL, item.Date); err != nil || !downloaded {
		t.Fatalf("initial archiveResource() = downloaded=%v err=%v, want a local download", downloaded, err)
	}
	resource, err := store.ExistingResource(articleID, "attachment", server.URL+"/attachment.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.OpenResource(resource); err != nil {
		t.Fatalf("initial local resource missing: %v", err)
	}

	uploader := &recordingResourceUploader{}
	withUploader := NewSyncer(store, sdk.New(sdk.Options{HTTPClient: server.Client()}), SyncOptions{RequestGap: 0, ResourceUploader: uploader})
	if downloaded, err := withUploader.archiveResource(context.Background(), articleID, "attachment", server.URL+"/attachment.pdf", "notice.pdf", item.URL, item.Date); err != nil || downloaded {
		t.Fatalf("existing archiveResource() = downloaded=%v err=%v, want upload without redownload", downloaded, err)
	}
	if uploader.calls != 1 || string(uploader.contents) != payload {
		t.Fatalf("uploader calls/content = %d/%q, want 1/%q", uploader.calls, uploader.contents, payload)
	}
	if _, err := store.OpenResource(resource); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("existing local resource still exists: %v", err)
	}
}

func TestArticleHasAnyContent(t *testing.T) {
	if articleHasAnyContent(nil) {
		t.Fatal("nil article has no content")
	}
	if articleHasAnyContent(&sdk.Article{Title: "只有标题"}) {
		t.Fatal("title-only shell should be skipped")
	}
	for name, article := range map[string]*sdk.Article{
		"text":       {Content: "正文"},
		"html":       {ContentHTML: "<p>正文</p>"},
		"image-only": {Images: []string{"https://example.com/a.png"}},
		"attachment": {Attachments: []sdk.Attachment{{Name: "附件.pdf", URL: "https://example.com/a.pdf"}}},
	} {
		if !articleHasAnyContent(article) {
			t.Fatalf("%s article should be archived", name)
		}
	}
}

