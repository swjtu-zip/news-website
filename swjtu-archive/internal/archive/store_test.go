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

	item := sdk.NewsItem{Title: "人工智能与交通发展", URL: "https://news.swjtu.edu.cn/info/1/2.htm", Date: "2026/08/31"}
	articleID, err := store.UpsertArticle(ArticleInput{
		Feed: feed, Item: item, RawPath: rawPath, RawSHA256: rawHash, FetchedAt: time.Now(),
		Article: &sdk.Article{Title: item.Title, Source: "新闻网", Date: item.Date, Content: "学校开展人工智能交通研究", ContentHTML: "<p>学校开展人工智能交通研究</p>"},
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
	if total != 1 || len(items) != 1 || items[0].ID != articleID || items[0].PublishedAt != "2026-08-31" {
		t.Fatalf("unexpected search result: total=%d items=%+v", total, items)
	}
	full, err := store.GetArticle(articleID)
	if err != nil || len(full.Resources) != 1 || full.Resources[0].ID != resourceID {
		t.Fatalf("unexpected article: %+v (%v)", full, err)
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
