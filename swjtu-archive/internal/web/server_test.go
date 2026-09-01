package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"swjtu-archive/internal/archive"
	"swjtu-cli/pkg/sdk"
)

func TestArticleAPIRewritesArchivedResources(t *testing.T) {
	store, err := archive.Open(t.TempDir() + "/archive.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	feed := sdk.Feed{ID: "news:jdyw", SiteID: "news", SiteName: "新闻网", Category: "university", Slug: "jdyw", Name: "交大要闻", BaseURL: "https://news.swjtu.edu.cn"}
	rawPath, rawHash, err := store.SaveRaw([]byte("raw"))
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.UpsertArticle(archive.ArticleInput{Feed: feed, Item: sdk.NewsItem{Title: "测试文章", URL: "https://news.swjtu.edu.cn/a.htm"}, RawPath: rawPath, RawSHA256: rawHash, FetchedAt: time.Now(), Article: &sdk.Article{Title: "测试文章", Content: "正文", ContentHTML: `<p>正文</p><img src="https://news.swjtu.edu.cn/image.png"><script>alert(1)</script><a href="javascript:alert(1)">危险链接</a>`}})
	if err != nil {
		t.Fatal(err)
	}
	local, hash, size, err := store.SaveResourceBody(strings.NewReader("img"), 1024, "image.png", "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertResource(archive.ResourceInput{ArticleID: id, Kind: "image", OriginalURL: "https://news.swjtu.edu.cn/image.png", LocalPath: local, Filename: "image.png", ContentType: "image/png", ByteSize: size, SHA256: hash, Status: "success", UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/articles/1", nil)
	NewServer(store).Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected response: %d %s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	content, _ := response["content_html"].(string)
	if content == "" || content == `<p>正文</p><img src="https://news.swjtu.edu.cn/image.png">` || strings.Contains(content, "script") || strings.Contains(content, "javascript:") {
		t.Fatalf("resource URL was not rewritten: %q", content)
	}
}

func TestMarkdownContentNegotiation(t *testing.T) {
	store, err := archive.Open(t.TempDir() + "/archive.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	feed := sdk.Feed{ID: "news:jdyw", SiteID: "news", SiteName: "新闻网", Category: "university", Slug: "jdyw", Name: "交大要闻", BaseURL: "https://news.swjtu.edu.cn"}
	rawPath, rawHash, err := store.SaveRaw([]byte("raw"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertArticle(archive.ArticleInput{Feed: feed, Item: sdk.NewsItem{Title: "测试文章", URL: "https://news.swjtu.edu.cn/a.htm"}, RawPath: rawPath, RawSHA256: rawHash, FetchedAt: time.Now(), Article: &sdk.Article{Title: "测试文章", Content: "正文", ContentHTML: "<p>正文</p>"}}); err != nil {
		t.Fatal(err)
	}
	handler := NewServer(store).Handler()

	request := httptest.NewRequest(http.MethodGet, "/article/1", nil)
	request.Header.Set("Accept", "text/markdown")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.HasPrefix(recorder.Header().Get("Content-Type"), "text/markdown") {
		t.Fatalf("expected markdown response: %d %q", recorder.Code, recorder.Header().Get("Content-Type"))
	}
	if body := recorder.Body.String(); !strings.Contains(body, "# 测试文章") || !strings.Contains(body, "正文") {
		t.Fatalf("unexpected markdown body: %q", body)
	}

	// HTML must remain the default for browsers.
	request = httptest.NewRequest(http.MethodGet, "/article/1", nil)
	request.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if contentType := recorder.Header().Get("Content-Type"); strings.HasPrefix(contentType, "text/markdown") {
		t.Fatalf("browser Accept header got markdown: %q", contentType)
	}

	// The article list negotiates too.
	request = httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Accept", "text/markdown")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if body := recorder.Body.String(); !strings.Contains(body, "# SWJTU 新闻归档") || !strings.Contains(body, "[测试文章]") {
		t.Fatalf("unexpected index markdown: %q", body)
	}
}
