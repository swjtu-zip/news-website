package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"slices"
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
	NewServer(store, "https://oss.swjtu.zip/news-assets").Handler().ServeHTTP(recorder, request)
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
	if !strings.Contains(content, "https://oss.swjtu.zip/news-assets/") {
		t.Fatalf("resource URL did not use public object storage: %q", content)
	}
	resources, _ := response["resources"].([]any)
	if len(resources) != 1 {
		t.Fatalf("resources = %#v", response["resources"])
	}
	resource, _ := resources[0].(map[string]any)
	if !strings.HasPrefix(resource["url"].(string), "https://oss.swjtu.zip/news-assets/") {
		t.Fatalf("resource API URL = %#v", resource["url"])
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

func TestPaginationNumbers(t *testing.T) {
	tests := []struct {
		name           string
		current, total int
		want           []int
	}{
		{name: "first page", current: 1, total: 10, want: []int{1, 2, 3, 4, 5, 6, 7, 0, 10}},
		{name: "middle page", current: 5, total: 10, want: []int{1, 2, 3, 4, 5, 6, 7, 8, 0, 10}},
		{name: "last page", current: 10, total: 10, want: []int{1, 0, 4, 5, 6, 7, 8, 9, 10}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := paginationNumbers(test.current, test.total, 7); !slices.Equal(got, test.want) {
				t.Fatalf("paginationNumbers(%d, %d, 7) = %v, want %v", test.current, test.total, got, test.want)
			}
		})
	}
}

func TestIndexIncludesMCPGuideAndPageJump(t *testing.T) {
	store, err := archive.Open(t.TempDir() + "/archive.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	feed := sdk.Feed{ID: "news:jdyw", SiteID: "news", SiteName: "新闻网", Category: "university", Slug: "jdyw", Name: "交大要闻", BaseURL: "https://news.swjtu.edu.cn"}
	for i := 1; i <= 8; i++ {
		_, err := store.UpsertArticle(archive.ArticleInput{
			Feed: feed, Item: sdk.NewsItem{Title: fmt.Sprintf("测试文章 %d", i), URL: fmt.Sprintf("https://news.swjtu.edu.cn/%d.htm", i)},
			FetchedAt: time.Now(), Article: &sdk.Article{Title: fmt.Sprintf("测试文章 %d", i), Date: fmt.Sprintf("2026-08-%02d", i), Content: "正文"},
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	handler := NewServer(store).Handler()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/?page_size=1&site=news", nil)
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected response: %d %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, fragment := range []string{
		`href="/help/mcp"`, `MCP 配置指南`, `aria-label="第 5 页"`, `name="page"`, `max="8"`, `page_size=1`,
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("index did not contain %q", fragment)
		}
	}

	// A site filter and a large but syntactically valid page number should
	// return an empty result page, not an archive query failure.
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/?site=dqxy&page=1839", nil)
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("filtered high page returned %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestMonthRangeFilter(t *testing.T) {
	store, err := archive.Open(t.TempDir() + "/archive.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	feed := sdk.Feed{ID: "news:jdyw", SiteID: "news", SiteName: "新闻网", Category: "university", Slug: "jdyw", Name: "交大要闻", BaseURL: "https://news.swjtu.edu.cn"}
	dates := []string{"2026-01-15", "2026-03-10", "2026-03-20", "2026-07-01"}
	for i, date := range dates {
		_, err := store.UpsertArticle(archive.ArticleInput{
			Feed: feed, Item: sdk.NewsItem{Title: fmt.Sprintf("测试文章 %d", i), URL: fmt.Sprintf("https://news.swjtu.edu.cn/%d.htm", i)},
			FetchedAt: time.Now(), Article: &sdk.Article{Title: fmt.Sprintf("测试文章 %d", i), Date: date, Content: "正文"},
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	handler := NewServer(store).Handler()
	count := func(path string) string {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s returned %d: %s", path, recorder.Code, recorder.Body.String())
		}
		matches := regexp.MustCompile(`共 <b>(\d+)</b> 篇文章`).FindStringSubmatch(recorder.Body.String())
		if matches == nil {
			t.Fatalf("%s did not render the result count", path)
		}
		return matches[1]
	}

	tests := []struct{ path, want string }{
		{"/?from_month=2026-02&to_month=2026-06", "2"},
		{"/?from_month=2026-06&to_month=2026-02", "2"}, // swapped bounds are normalized
		{"/?to_month=2026-03", "3"},
		{"/?from_month=2026-04", "1"},
		{"/?month=2026-03", "2"}, // legacy single-month parameter
		{"/?from_year=2026&from_mon=3&to_year=2026&to_mon=3", "2"}, // split year/month parameters
		{"/?from_year=2026&from_mon=7", "1"},
		{"/?to_year=2026&to_mon=1", "1"},
		{"/?from_year=2026", "4"}, // year without month covers the whole year
		{"/?from_month=2026-01&to_month=2026-12&site=news&q=%E6%B5%8B%E8%AF%95", "4"}, // combined filters
		{"/?from_month=2026-01&to_month=2026-12&site=dqxy", "0"},
	}
	for _, test := range tests {
		if got := count(test.path); got != test.want {
			t.Fatalf("%s matched %s articles, want %s", test.path, got, test.want)
		}
	}

	// The rendered filter form keeps every active condition for combined search.
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/?from_month=2026-02&to_month=2026-06&site=news&q=x", nil)
	handler.ServeHTTP(recorder, request)
	body := recorder.Body.String()
	for _, fragment := range []string{
		`name="from_year"`, `name="from_mon"`, `name="to_year"`, `name="to_mon"`,
		`<option value="2026" selected>2026年</option>`, `<option value="2" selected>2月</option>`, `<option value="6" selected>6月</option>`,
		`name="from_month" value="2026-02"`, `name="to_month" value="2026-06"`,
		`name="site" value="news"`, `id="site-search"`, `2026-02 ~ 2026-06`,
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("index did not contain %q", fragment)
		}
	}
}

func TestIndexShowsDiskUsageFooter(t *testing.T) {
	store, err := archive.Open(t.TempDir() + "/archive.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, _, err := store.SaveRaw([]byte("raw-page-body")); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	NewServer(store).Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected response: %d %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, fragment := range []string{`class="footer"`, `归档占用`, `磁盘可用`} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("index did not contain %q", fragment)
		}
	}
}

func TestMCPGuideUsesRequestEndpoint(t *testing.T) {
	store, err := archive.Open(t.TempDir() + "/archive.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "https://archive.example/help/mcp", nil)
	NewServer(store).Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected response: %d %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, fragment := range []string{`https://archive.example/mcp`, `Streamable HTTP`, `search_articles`} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("guide did not contain %q", fragment)
		}
	}
}

func TestCacheHeaders(t *testing.T) {
	store, err := archive.Open(t.TempDir() + "/archive.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler := NewServer(store).Handler()

	tests := []struct {
		name, method, path, wantCache, wantCDN string
		wantStatus                             int
	}{
		{name: "html", method: http.MethodGet, path: "/", wantStatus: http.StatusOK, wantCache: indexCacheControl, wantCDN: indexCDNCacheControl},
		{name: "guide", method: http.MethodGet, path: "/help/mcp", wantStatus: http.StatusOK, wantCache: staticCacheControl, wantCDN: staticCDNCacheControl},
		{name: "resource api path", method: http.MethodGet, path: "/api/v1/resources/1", wantStatus: http.StatusNotFound, wantCache: errorCacheControl, wantCDN: errorCDNCacheControl},
		{name: "api", method: http.MethodGet, path: "/api/v1/articles?page=1", wantStatus: http.StatusOK, wantCache: apiCacheControl, wantCDN: apiCDNCacheControl},
		{name: "api error", method: http.MethodGet, path: "/api/v1/articles/1", wantStatus: http.StatusNotFound, wantCache: errorCacheControl, wantCDN: errorCDNCacheControl},
		{name: "html error", method: http.MethodGet, path: "/missing", wantStatus: http.StatusNotFound, wantCache: errorCacheControl, wantCDN: errorCDNCacheControl},
		{name: "mcp post", method: http.MethodPost, path: "/mcp", wantStatus: http.StatusUnsupportedMediaType, wantCache: noStoreCacheControl, wantCDN: noStoreCacheControl},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(test.method, test.path, nil)
			handler.ServeHTTP(recorder, request)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
			if got := recorder.Header().Get("Cache-Control"); got != test.wantCache {
				t.Errorf("Cache-Control = %q, want %q", got, test.wantCache)
			}
			if got := recorder.Header().Get("CDN-Cache-Control"); got != test.wantCDN {
				t.Errorf("CDN-Cache-Control = %q, want %q", got, test.wantCDN)
			}
		})
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/resources/1", nil)
	cacheControl, cdnCacheControl := responseCachePolicy(request, http.StatusOK)
	if cacheControl != staticCacheControl || cdnCacheControl != staticCDNCacheControl {
		t.Fatalf("successful resource API path policy = %q / %q, want %q / %q", cacheControl, cdnCacheControl, staticCacheControl, staticCDNCacheControl)
	}
}

func TestArticlePageHasResponsiveInfoAndAttachments(t *testing.T) {
	store, err := archive.Open(t.TempDir() + "/archive.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	feed := sdk.Feed{ID: "news:jdyw", SiteID: "news", SiteName: "新闻网", Category: "university", Slug: "jdyw", Name: "交大要闻", BaseURL: "https://news.swjtu.edu.cn"}
	id, err := store.UpsertArticle(archive.ArticleInput{
		Feed: feed, Item: sdk.NewsItem{Title: "一篇很长的测试文章标题", URL: "https://news.swjtu.edu.cn/article.htm"}, FetchedAt: time.Now(),
		Article: &sdk.Article{Title: "一篇很长的测试文章标题", Source: "党委宣传部", Date: "2026-08-30", Content: "正文", ContentHTML: "<p>正文</p>"},
	})
	if err != nil {
		t.Fatal(err)
	}
	local, hash, size, err := store.SaveResourceBody(strings.NewReader("attachment"), 1024, "通知.pdf", "application/pdf")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertResource(archive.ResourceInput{ArticleID: id, Kind: "attachment", OriginalURL: "https://news.swjtu.edu.cn/notice.pdf", LocalPath: local, Filename: "通知.pdf", ContentType: "application/pdf", ByteSize: size, SHA256: hash, Status: "success", UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/article/%d", id), nil)
	NewServer(store).Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected response: %d %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, fragment := range []string{
		`class="reading-bar"`, `来源：党委宣传部`, `时间：2026-08-30`, `阅读原文 ↗`,
		`收录：` + time.Now().Local().Format("2006-01-02"), `收录时间`,
		`class="article-side"`, `class="side-resources"`, `通知.pdf`,
		`@media (orientation:portrait)`, `@media(min-width:1100px) and (orientation:landscape)`,
		`class="footer"`, `服务器于 `, ` 提供，耗时 `, ` ms`,
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("article page did not contain %q", fragment)
		}
	}
}

func TestSyncTriggerEndpoint(t *testing.T) {
	store, err := archive.Open(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var runs int
	server := NewServer(store)
	server.SetSyncTrigger(func() bool { runs++; return runs == 1 }, "secret")
	handler := server.Handler()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sync/trigger", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing token: got %d, want 401", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/sync/trigger", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("first trigger: got %d, want 202", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/sync/trigger", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("second trigger while running: got %d, want 409", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/sync/trigger", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET trigger: got %d, want 405", rec.Code)
	}
}
