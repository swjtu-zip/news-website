package archive

import (
	"io"
	"strings"
	"testing"
	"time"

	"swjtu-cli/pkg/sdk"
)

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
