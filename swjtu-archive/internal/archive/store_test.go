package archive

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"swjtu-cli/pkg/sdk"
)

func TestRetrySQLiteBusyRetriesTransientLock(t *testing.T) {
	attempts := 0
	err := retrySQLiteBusy(func() error {
		attempts++
		if attempts < 3 {
			return errors.New("database table is locked")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
}

func TestRetrySQLiteBusyDoesNotRetryOtherErrors(t *testing.T) {
	attempts := 0
	want := errors.New("syntax error")
	err := retrySQLiteBusy(func() error {
		attempts++
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestRetrySQLiteBusyRetriesTransientIOError(t *testing.T) {
	attempts := 0
	err := retrySQLiteBusy(func() error {
		attempts++
		if attempts < 3 {
			return errors.New("disk I/O error (6410)")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
}

func TestStoreArchivesAndSearchesArticle(t *testing.T) {
	store, err := Open(t.TempDir() + "/archive.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	feed := sdk.Feed{ID: "news:jdyw", SiteID: "news", SiteName: "新闻网", Category: "university", Slug: "jdyw", Name: "交大要闻", BaseURL: "https://news.swjtu.edu.cn"}
	if err := store.UpsertFeed(feed, time.Now()); err != nil {
		t.Fatal(err)
	}
	rawPath, rawHash, err := store.SaveRaw([]byte("<html>source</html>"))
	if err != nil {
		t.Fatal(err)
	}
	secondPath, secondHash, err := store.SaveRaw([]byte("<html>source</html>"))
	if err != nil || rawPath != secondPath || rawHash != secondHash {
		t.Fatalf("raw content was not content-addressed: %q/%q %q/%q", rawPath, rawHash, secondPath, secondHash)
	}

	item := sdk.NewsItem{Title: "人工智能与交通发展", URL: "https://news.swjtu.edu.cn/info/1/2.htm", Date: "2026/08/31", Type: "交大要闻"}
	articleID, err := store.UpsertArticle(ArticleInput{
		Feed: feed, Item: item, RawPath: rawPath, RawSHA256: rawHash, FetchedAt: time.Now(),
		Article: &sdk.Article{Title: item.Title, Source: "新闻网", Date: item.Date, PublishedAt: "2026-08-31 14:34", Type: item.Type, Content: "学校开展人工智能交通研究", ContentHTML: "<p>学校开展人工智能交通研究</p>"},
	})
	if err != nil {
		t.Fatal(err)
	}

	resourcePath, resourceHash, resourceSize, err := store.SaveResourceBody(strings.NewReader("image-data"), 1024, "cover.png", "image/png")
	if err != nil {
		t.Fatal(err)
	}
	resourceID, err := store.UpsertResource(ResourceInput{ArticleID: articleID, Kind: "image", OriginalURL: "https://news.swjtu.edu.cn/image.png", LocalPath: resourcePath, Filename: "cover.png", ContentType: "image/png", ByteSize: resourceSize, SHA256: resourceHash, Status: "success", UpdatedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if resourceID == 0 {
		t.Fatal("expected resource ID")
	}

	items, total, err := store.FindArticles(ArticleFilter{Query: "人工智能", Page: 1, PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(items) != 1 || items[0].ID != articleID || items[0].PublishedAt != "2026-08-31 14:34:00" || items[0].Type != "交大要闻" {
		t.Fatalf("unexpected search result: total=%d items=%+v", total, items)
	}
	items, total, err = store.FindArticles(ArticleFilter{Query: "https://news.swjtu.edu.cn/info/1/2.htm", Page: 1, PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(items) != 1 || items[0].ID != articleID {
		t.Fatalf("canonical URL search did not find the article: total=%d items=%+v", total, items)
	}
	full, err := store.GetArticle(articleID)
	if err != nil || len(full.Resources) != 1 || full.Resources[0].ID != resourceID {
		t.Fatalf("unexpected article: %+v (%v)", full, err)
	}
	if full.Type != "交大要闻" || full.PublishedAt != "2026-08-31 14:34:00" {
		t.Fatalf("metadata precision/type was not retained: type=%q published_at=%q", full.Type, full.PublishedAt)
	}
	file, err := store.OpenResource(&full.Resources[0])
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	body, err := io.ReadAll(file)
	if err != nil || string(body) != "image-data" {
		t.Fatalf("unexpected stored resource: %q (%v)", body, err)
	}
}

func TestPublicationDateUsesListingDayWhenDetailConflicts(t *testing.T) {
	store, err := Open(t.TempDir() + "/archive.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	feed := sdk.Feed{ID: "xinli:tzgg/gsgg", SiteID: "xinli", SiteName: "心理研究与咨询中心", Category: "college", Slug: "tzgg/gsgg", Name: "公示公告", BaseURL: "http://xinli.swjtu.edu.cn"}
	item := sdk.NewsItem{Title: "转发公告", URL: "http://xinli.swjtu.edu.cn/info/1572/45166.htm", Date: "2025-10-08"}
	id, err := store.UpsertArticle(ArticleInput{
		Feed: feed, Item: item, FetchedAt: time.Now(),
		Article: &sdk.Article{Title: item.Title, Date: "2025-10-10", PublishedAt: "2025-10-10 17:30:00", Content: "正文"},
	})
	if err != nil {
		t.Fatal(err)
	}
	article, err := store.GetArticle(id)
	if err != nil {
		t.Fatal(err)
	}
	if article.PublishedAt != "2025-10-08" {
		t.Fatalf("conflicting detail date was retained: %q", article.PublishedAt)
	}

	// The same guard must repair an existing row when a fresh listing observes
	// it again without requiring another detail fetch.
	if _, err := store.db.Exec("UPDATE articles SET published_at='2025-10-10 17:30:00' WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	if err := store.TouchArticleMetadata(item.URL, feed, item, time.Now()); err != nil {
		t.Fatal(err)
	}
	article, err = store.GetArticle(id)
	if err != nil {
		t.Fatal(err)
	}
	if article.PublishedAt != "2025-10-08" {
		t.Fatalf("conflicting existing date was not repaired: %q", article.PublishedAt)
	}
}

func TestOpenCleansLegacyDatesAndImpossibleResources(t *testing.T) {
	dbPath := t.TempDir() + "/archive.db"
	store, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	feed := sdk.Feed{ID: "rwxy:xyxw", SiteID: "rwxy", SiteName: "人文学院", Category: "college", Slug: "xyxw", Name: "学院新闻", BaseURL: "http://rwxy.swjtu.edu.cn"}
	articleID, err := store.UpsertArticle(ArticleInput{
		Feed:      feed,
		Item:      sdk.NewsItem{Title: "迁移清理测试", URL: "http://rwxy.swjtu.edu.cn/info/1/1.htm"},
		Article:   &sdk.Article{Title: "迁移清理测试", PublishedAt: "2013-01-01 00:00:00", Content: "正文"},
		FetchedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec("UPDATE articles SET published_at='2013-01-01 00:00:00' WHERE id=?", articleID); err != nil {
		t.Fatal(err)
	}
	for _, rawURL := range []string{
		"data:image/png;base64,placeholder",
		"file:///tmp/old.doc",
		"mailto:test@example.com",
		"https://rwxy.swjtu.edu.cn/doc.gif",
		"https://dqxy.swjtu.edu.cn/_mediafile/%7Bownername%7D/broken.png",
	} {
		if _, err := store.UpsertResource(ResourceInput{ArticleID: articleID, Kind: "image", OriginalURL: rawURL, Status: "failed", Error: "legacy", UpdatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.UpsertResource(ResourceInput{ArticleID: articleID, Kind: "image", OriginalURL: "https://rwxy.swjtu.edu.cn/real.png", Status: "failed", Error: "404", UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertResource(ResourceInput{ArticleID: articleID, Kind: "image", OriginalURL: "https://rwxy.swjtu.edu.cn/doc.gif", LocalPath: "assets/keep", Status: "success", UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var published string
	if err := store.db.QueryRow("SELECT published_at FROM articles WHERE id=?", articleID).Scan(&published); err != nil {
		t.Fatal(err)
	}
	if published != "" {
		t.Fatalf("legacy publication date = %q, want empty", published)
	}
	var failed, successful int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM resources WHERE article_id=? AND status='failed'", articleID).Scan(&failed); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow("SELECT COUNT(*) FROM resources WHERE article_id=? AND status='success'", articleID).Scan(&successful); err != nil {
		t.Fatal(err)
	}
	if failed != 1 || successful != 1 {
		t.Fatalf("legacy resources after cleanup = failed %d, successful %d; want 1/1", failed, successful)
	}
}

func TestTouchArticleMetadataUpgradesLegacyListing(t *testing.T) {
	store, err := Open(t.TempDir() + "/archive.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	home := sdk.Feed{ID: "sic:index", SiteID: "sic", SiteName: "集成电路科学与工程学院", Category: "college", Slug: "index", Name: "首页新闻", BaseURL: "https://sic.swjtu.edu.cn"}
	if err := store.UpsertFeed(home, time.Now()); err != nil {
		t.Fatal(err)
	}
	item := sdk.NewsItem{Title: "旧标题", URL: "https://sic.swjtu.edu.cn/info/1/2.htm", Date: "2026-08-28"}
	if _, err := store.UpsertArticle(ArticleInput{
		Feed: home, Item: item, FetchedAt: time.Now(),
		Article: &sdk.Article{Title: item.Title, Date: item.Date, Content: "正文"},
	}); err != nil {
		t.Fatal(err)
	}

	specific := sdk.Feed{ID: "sic:xydt", SiteID: "sic", SiteName: home.SiteName, Category: "college", Slug: "xydt", Name: "xydt", BaseURL: home.BaseURL}
	if err := store.TouchArticleMetadata(item.URL, specific, sdk.NewsItem{
		Title: "列表标题", URL: item.URL, Date: "2026/08/28", PublishedAt: "2026/08/28 17:03:55", Type: "学院动态",
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	article, err := store.GetArticle(1)
	if err != nil {
		t.Fatal(err)
	}
	if article.Type != "学院动态" || article.FeedID != "sic:xydt" || article.PublishedAt != "2026-08-28 17:03:55" || article.Title != "旧标题" {
		t.Fatalf("metadata upgrade = type=%q feed=%q published=%q title=%q", article.Type, article.FeedID, article.PublishedAt, article.Title)
	}

	if err := store.TouchArticleMetadata(item.URL, home, sdk.NewsItem{URL: item.URL, Type: "首页新闻", Date: "2026-08-28"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	article, err = store.GetArticle(1)
	if err != nil {
		t.Fatal(err)
	}
	if article.Type != "学院动态" || article.FeedID != "sic:xydt" || article.PublishedAt != "2026-08-28 17:03:55" {
		t.Fatalf("generic listing downgraded metadata = type=%q feed=%q published=%q", article.Type, article.FeedID, article.PublishedAt)
	}
}

func TestRemoveStaleFailedResourcesKeepsCurrentAndSuccessfulRows(t *testing.T) {
	store, err := Open(t.TempDir() + "/archive.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	feed := sdk.Feed{ID: "sic:xydt", SiteID: "sic", SiteName: "集成电路科学与工程学院", Category: "college", Slug: "xydt", Name: "学院动态", BaseURL: "https://sic.swjtu.edu.cn"}
	item := sdk.NewsItem{Title: "附件修复测试", URL: "https://sic.swjtu.edu.cn/info/1/2.htm", Date: "2026-08-28"}
	articleID, err := store.UpsertArticle(ArticleInput{Feed: feed, Item: item, FetchedAt: time.Now(), Article: &sdk.Article{Title: item.Title, Date: item.Date, Content: "正文"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, rawURL := range []string{"https://ic.swjtu.edu.cn/__local/old.png", "https://sic.swjtu.edu.cn/__local/current.png"} {
		if _, err := store.UpsertResource(ResourceInput{ArticleID: articleID, Kind: "image", OriginalURL: rawURL, Status: "failed", Error: "403", UpdatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.UpsertResource(ResourceInput{ArticleID: articleID, Kind: "image", OriginalURL: "https://sic.swjtu.edu.cn/__local/kept.png", LocalPath: "assets/kept", Status: "success", UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	if err := store.RemoveStaleFailedResources(articleID, map[string]bool{"https://sic.swjtu.edu.cn/__local/current.png": true}); err != nil {
		t.Fatal(err)
	}
	full, err := store.GetArticle(articleID)
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Resources) != 2 {
		t.Fatalf("resources after reconciliation = %#v, want current failed plus successful row", full.Resources)
	}
	for _, resource := range full.Resources {
		if resource.OriginalURL == "https://ic.swjtu.edu.cn/__local/old.png" {
			t.Fatalf("stale failed resource was not removed: %#v", resource)
		}
	}
}

func TestNormalizeRunSeenAtUsesOneFinishedTimestamp(t *testing.T) {
	store, err := Open(t.TempDir() + "/archive.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	feed := sdk.Feed{ID: "news:test", SiteID: "news", SiteName: "新闻网", Category: "university", Slug: "test", Name: "测试", BaseURL: "https://news.swjtu.edu.cn"}
	started := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	finished := started.Add(2 * time.Hour)
	item := sdk.NewsItem{Title: "结束时间测试", URL: "https://news.swjtu.edu.cn/info/1/99.htm", Date: "2026-09-03"}
	if _, err := store.UpsertArticle(ArticleInput{Feed: feed, Item: item, FetchedAt: started.Add(time.Minute), Article: &sdk.Article{Title: item.Title, Date: item.Date}}); err != nil {
		t.Fatal(err)
	}
	if err := store.NormalizeRunSeenAt(started, finished); err != nil {
		t.Fatal(err)
	}
	full, err := store.GetArticle(1)
	if err != nil {
		t.Fatal(err)
	}
	if full.LastSeenAt != finished.Format(time.RFC3339Nano) {
		t.Fatalf("last_seen_at = %q, want %q", full.LastSeenAt, finished.Format(time.RFC3339Nano))
	}
}

func TestPreferIncomingArticleTypeUpgradesRouteLabels(t *testing.T) {
	feed := sdk.Feed{Slug: "xwtz/xyxw", Name: "xwtz/xyxw"}
	cases := []struct {
		candidate string
		current   string
		want      bool
	}{
		{candidate: "学院新闻", current: "xwtz/xyxw", want: true},
		{candidate: "研究生", current: "学院新闻", want: true},
		{candidate: "学院新闻", current: "材料要闻", want: true},
		{candidate: "首页新闻", current: "研究生", want: false},
		{candidate: "本科生", current: "研究生", want: false},
	}
	for _, tc := range cases {
		if got := preferIncomingArticleType(tc.candidate, tc.current, feed); got != tc.want {
			t.Errorf("preferIncomingArticleType(%q, %q) = %v, want %v", tc.candidate, tc.current, got, tc.want)
		}
	}
}

func TestStartRunUsesProcessLock(t *testing.T) {
	dbPath := t.TempDir() + "/archive.db"
	first, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	runID, err := first.StartRun(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.StartRun(time.Now()); err == nil {
		t.Fatal("expected concurrent sync to be rejected")
	}
	if err := first.FinishRun(runID, "success", 1, 0, 0, 0, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := second.StartRun(time.Now()); err != nil {
		t.Fatalf("sync lock was not released: %v", err)
	}
}

func TestStartRunClosesOrphanedRuns(t *testing.T) {
	store, err := Open(t.TempDir() + "/archive.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.db.Exec(`INSERT INTO sync_runs (started_at, status) VALUES (?, 'running')`, "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	runID, err := store.StartRun(time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	defer store.FinishRun(runID, "canceled", 0, 0, 0, 0, context.Canceled, time.Now())
	var status, finishedAt, runError string
	if err := store.db.QueryRow("SELECT status, finished_at, error FROM sync_runs WHERE id=1").Scan(&status, &finishedAt, &runError); err != nil {
		t.Fatal(err)
	}
	if status != "canceled" || finishedAt == "" || runError == "" {
		t.Fatalf("orphaned run was not finalized: status=%q finished_at=%q error=%q", status, finishedAt, runError)
	}
}

func TestSaveResourceBodyRejectsOversize(t *testing.T) {
	store, err := Open(t.TempDir() + "/archive.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, _, _, err := store.SaveResourceBody(strings.NewReader("12345"), 4, "file.txt", "text/plain"); err == nil {
		t.Fatal("expected size limit error")
	}
}

func TestSniffExtension(t *testing.T) {
	docx := append([]byte("PK\x03\x04"), make([]byte, 64)...)
	docx = append(docx, []byte("[Content_Types].xml")...)
	docx = append(docx, []byte("word/document.xml")...)
	cfbDoc := append([]byte("\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1"), make([]byte, 512)...)
	copy(cfbDoc[256:], []byte("W\x00o\x00r\x00d\x00D\x00o\x00c\x00u\x00m\x00e\x00n\x00t\x00"))
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"pdf", []byte("%PDF-1.7 rest"), ".pdf"},
		{"png", []byte("\x89PNG\r\n\x1a\nrest"), ".png"},
		{"jpeg", []byte("\xff\xd8\xff\xe0rest"), ".jpg"},
		{"gif", []byte("GIF89a rest"), ".gif"},
		{"rar", []byte("Rar!\x1a\x07\x00rest"), ".rar"},
		{"7z", []byte("7z\xbc\xaf\x27\x1crest"), ".7z"},
		{"docx", docx, ".docx"},
		{"zip", []byte("PK\x03\x04plain-zip-entry"), ".zip"},
		{"ole-doc", cfbDoc, ".doc"},
		{"mp4", []byte("\x00\x00\x00\x18ftypmp42rest"), ".mp4"},
		{"unknown", []byte("plain text body"), ""},
		{"empty", nil, ""},
	}
	for _, tc := range cases {
		if got := SniffExtension(tc.data); got != tc.want {
			t.Errorf("%s: SniffExtension() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestSaveResourceBodySniffsExtensionlessContent(t *testing.T) {
	store, err := Open(t.TempDir() + "/archive.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	relative, _, _, err := store.SaveResourceBody(strings.NewReader("%PDF-1.4 fake"), 1024, "附件1", "application/octet-stream")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(relative, ".pdf") {
		t.Fatalf("relative path %q does not carry the sniffed .pdf extension", relative)
	}
}

func TestSiteFacetsAggregatesWithoutSQLiteTempSort(t *testing.T) {
	store, err := Open(t.TempDir() + "/archive.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	sites := []struct {
		id, name string
		count    int
	}{
		{id: "school-a", name: "甲学院", count: 3},
		{id: "school-b", name: "乙学院", count: 1},
	}
	for _, site := range sites {
		feed := sdk.Feed{ID: "news:" + site.id, SiteID: site.id, SiteName: site.name, Category: "school", Name: site.name, BaseURL: "https://example.com"}
		for i := 0; i < site.count; i++ {
			if _, err := store.UpsertArticle(ArticleInput{
				Feed:      feed,
				Item:      sdk.NewsItem{Title: site.name, URL: "https://example.com/" + site.id + "/" + string(rune('a'+i))},
				FetchedAt: time.Now(),
				Article:   &sdk.Article{Title: site.name, Date: "2026-08-01", Content: "content"},
			}); err != nil {
				t.Fatal(err)
			}
		}
	}

	facets, err := store.SiteFacets()
	if err != nil {
		t.Fatal(err)
	}
	if len(facets) != 2 || facets[0].SiteID != "school-a" || facets[0].Count != 3 || facets[1].SiteID != "school-b" || facets[1].Count != 1 {
		t.Fatalf("facets = %#v", facets)
	}
}

func TestNormalizeDate(t *testing.T) {
	cases := map[string]string{
		"2026-08-22":           "2026-08-22",
		"2026/8/2":             "2026-08-02",
		"2026年8月2日":            "2026-08-02",
		"日期：2026/08/22":        "2026-08-22",
		"2026-08-11 14:34":     "2026-08-11",
		"2026-08-11T14:34:00Z": "2026-08-11",
		"黄曙光":                  "",
		"":                     "",
		"not a date at all":    "",
	}
	for input, want := range cases {
		if got := normalizeDate(input); got != want {
			t.Errorf("normalizeDate(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNormalizePublishedAt(t *testing.T) {
	cases := map[string]string{
		"2026-08-11 14:34":     "2026-08-11 14:34:00",
		"2026/08/11 14:34:05":  "2026-08-11 14:34:05",
		"2026-08-11T14:34:00Z": "2026-08-11 14:34:00",
		"2026年8月11日 14时34分":    "2026-08-11 14:34:00",
		"2026-08-11":           "2026-08-11",
		"无效日期":                 "",
	}
	for input, want := range cases {
		if got := normalizePublishedAt(input); got != want {
			t.Errorf("normalizePublishedAt(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSitemapMonthsAndArticles(t *testing.T) {
	store, err := Open(t.TempDir() + "/archive.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	feed := sdk.Feed{ID: "news:jdyw", SiteID: "news", SiteName: "新闻网", Category: "university", Slug: "jdyw", Name: "交大要闻", BaseURL: "https://news.swjtu.edu.cn"}
	rawPath, rawHash, err := store.SaveRaw([]byte("raw"))
	if err != nil {
		t.Fatal(err)
	}
	inputs := []struct{ url, published string }{
		{"https://news.swjtu.edu.cn/2026-01a.htm", "2026-01-05"},
		{"https://news.swjtu.edu.cn/2026-01b.htm", "2026-01-20 09:30:00"},
		{"https://news.swjtu.edu.cn/2026-02a.htm", "2026-02-01"},
		{"https://news.swjtu.edu.cn/undated.htm", ""},
	}
	ids := make(map[string]int64, len(inputs))
	for _, input := range inputs {
		id, err := store.UpsertArticle(ArticleInput{Feed: feed, Item: sdk.NewsItem{Title: "t", URL: input.url, PublishedAt: input.published}, RawPath: rawPath, RawSHA256: rawHash, FetchedAt: time.Now(), Article: &sdk.Article{Title: "t", Content: "正文"}})
		if err != nil {
			t.Fatal(err)
		}
		ids[input.url] = id
	}

	months, err := store.SitemapMonths()
	if err != nil {
		t.Fatal(err)
	}
	want := []SitemapMonth{
		{Month: "2026-01", Articles: 2, LastMod: "2026-01-20"},
		{Month: "2026-02", Articles: 1, LastMod: "2026-02-01"},
	}
	if len(months) != len(want) {
		t.Fatalf("months = %#v, want %#v", months, want)
	}
	for i := range want {
		if months[i] != want[i] {
			t.Fatalf("months[%d] = %#v, want %#v", i, months[i], want[i])
		}
	}

	articles, err := store.SitemapArticles("2026-01", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(articles) != 2 || articles[0].ID != ids["https://news.swjtu.edu.cn/2026-01a.htm"] || articles[0].LastMod != "2026-01-05" ||
		articles[1].ID != ids["https://news.swjtu.edu.cn/2026-01b.htm"] || articles[1].LastMod != "2026-01-20" {
		t.Fatalf("articles = %#v", articles)
	}
	page, err := store.SitemapArticles("2026-01", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0].ID != ids["https://news.swjtu.edu.cn/2026-01b.htm"] {
		t.Fatalf("paged articles = %#v", page)
	}
	if invalid, err := store.SitemapArticles("2026-13", 0, 10); err != nil || len(invalid) != 0 {
		t.Fatalf("invalid month = %#v, %v", invalid, err)
	}
	if got, want := datePart("2026-01-20 09:30:00"), "2026-01-20"; got != want {
		t.Fatalf("datePart = %q, want %q", got, want)
	}
}

func TestSaveRefetchKeepsChangedVersions(t *testing.T) {
	store, err := Open(t.TempDir() + "/archive.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	feed := sdk.Feed{ID: "news:jdyw", SiteID: "news", SiteName: "新闻网", Category: "university", Slug: "jdyw", Name: "交大要闻", BaseURL: "https://news.swjtu.edu.cn"}
	item := sdk.NewsItem{Title: "通知", URL: "https://news.swjtu.edu.cn/info/1/9.htm", Date: "2026-09-01"}
	first := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	id, err := store.UpsertArticle(ArticleInput{Feed: feed, Item: item, FetchedAt: first, Article: &sdk.Article{Title: "通知", Content: "初稿", ContentHTML: "<p>初稿</p>"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, resourceURL := range []string{"https://news.swjtu.edu.cn/a.pdf", "https://news.swjtu.edu.cn/b.pdf"} {
		if _, err := store.UpsertResource(ResourceInput{ArticleID: id, Kind: "attachment", OriginalURL: resourceURL, Status: "success", UpdatedAt: first}); err != nil {
			t.Fatal(err)
		}
	}
	state, err := store.FetchState(item.URL)
	if err != nil || !state.Exists || state.Refetched || !state.FirstFetchedAt.Equal(first) {
		t.Fatalf("fresh article state = %+v, %v", state, err)
	}

	second := first.Add(15 * 24 * time.Hour)
	_, changed, err := store.SaveRefetch(ArticleInput{Feed: feed, Item: item, FetchedAt: second, Article: &sdk.Article{Title: "通知（更正）", Content: "修订稿", ContentHTML: "<p>修订稿</p>"}},
		[]string{"https://news.swjtu.edu.cn/b.pdf"})
	if err != nil || !changed {
		t.Fatalf("changed re-fetch: changed=%v err=%v", changed, err)
	}
	if state, _ := store.FetchState(item.URL); !state.Refetched {
		t.Fatal("re-fetch was not recorded")
	}
	latest, err := store.GetArticle(id)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Title != "通知（更正）" || latest.Content != "修订稿" || latest.Version != 2 || len(latest.Versions) != 2 {
		t.Fatalf("latest = %q %q v%d %+v", latest.Title, latest.Content, latest.Version, latest.Versions)
	}
	if len(latest.Resources) != 1 || latest.Resources[0].OriginalURL != "https://news.swjtu.edu.cn/b.pdf" {
		t.Fatalf("latest resources = %+v", latest.Resources)
	}
	old, err := store.GetArticleVersion(id, 1)
	if err != nil {
		t.Fatal(err)
	}
	if old.Title != "通知" || old.Content != "初稿" || old.ContentHTML != "<p>初稿</p>" || old.Version != 1 || len(old.Resources) != 2 {
		t.Fatalf("version 1 = %q %q %q v%d resources=%d", old.Title, old.Content, old.ContentHTML, old.Version, len(old.Resources))
	}
	if _, err := store.GetArticleVersion(id, 3); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing version error = %v", err)
	}
}

func TestSaveRefetchUnchangedStoresNoVersion(t *testing.T) {
	store, err := Open(t.TempDir() + "/archive.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	feed := sdk.Feed{ID: "news:jdyw", SiteID: "news", SiteName: "新闻网", Category: "university", Slug: "jdyw", Name: "交大要闻", BaseURL: "https://news.swjtu.edu.cn"}
	item := sdk.NewsItem{Title: "通知", URL: "https://news.swjtu.edu.cn/info/1/10.htm", Date: "2026-09-01"}
	id, err := store.UpsertArticle(ArticleInput{Feed: feed, Item: item, FetchedAt: time.Now(), Article: &sdk.Article{Title: "通知", Content: "正文 内容"}})
	if err != nil {
		t.Fatal(err)
	}
	_, changed, err := store.SaveRefetch(ArticleInput{Feed: feed, Item: item, FetchedAt: time.Now(), Article: &sdk.Article{Title: "通知", Content: "正文\n  内容"}}, nil)
	if err != nil || changed {
		t.Fatalf("whitespace-only re-fetch: changed=%v err=%v", changed, err)
	}
	article, err := store.GetArticle(id)
	if err != nil {
		t.Fatal(err)
	}
	if article.Version != 0 || len(article.Versions) != 0 {
		t.Fatalf("unchanged article got versions: %+v", article.Versions)
	}
	if state, _ := store.FetchState(item.URL); !state.Refetched {
		t.Fatal("unchanged re-fetch was not recorded")
	}
}

func TestRefetchMigrationMarksLegacyRefreshedArticles(t *testing.T) {
	path := t.TempDir() + "/archive.db"
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	feed := sdk.Feed{ID: "news:jdyw", SiteID: "news", SiteName: "新闻网", Category: "university", Slug: "jdyw", Name: "交大要闻", BaseURL: "https://news.swjtu.edu.cn"}
	first := time.Date(2026, 8, 1, 8, 0, 0, 123456789, time.UTC)
	for _, input := range []struct {
		url  string
		last time.Time
	}{
		{"https://news.swjtu.edu.cn/old.htm", first.Add(30 * 24 * time.Hour)},
		{"https://news.swjtu.edu.cn/young.htm", first.Add(3 * 24 * time.Hour)},
	} {
		item := sdk.NewsItem{Title: "t", URL: input.url}
		if _, err := store.UpsertArticle(ArticleInput{Feed: feed, Item: item, FetchedAt: first, Article: &sdk.Article{Title: "t", Content: "正文"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec("UPDATE articles SET last_fetched_at=? WHERE canonical_url=?", input.last.Format(time.RFC3339Nano), input.url); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.Exec("ALTER TABLE articles DROP COLUMN refetched_at"); err != nil {
		t.Fatal(err)
	}
	store.Close()

	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if state, _ := store.FetchState("https://news.swjtu.edu.cn/old.htm"); !state.Refetched {
		t.Fatal("article refreshed 30 days after its first fetch should count as re-fetched")
	}
	if state, _ := store.FetchState("https://news.swjtu.edu.cn/young.htm"); state.Refetched {
		t.Fatal("article only 3 days into its life must still get its re-fetch")
	}
}
