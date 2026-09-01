// Command audit samples real articles per feed and prints the extracted
// fields so collection problems (bad dates, swapped author/date, empty
// fields) can be spotted site by site. Raw HTML of problem articles is
// saved under /tmp/audit-raw/ for markup inspection.
//
// With -verify it prints, for every site, the extracted date next to the
// raw page context where that date (or any date) appears, so each site can
// be eyeballed for correctness.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"swjtu-cli/pkg/sdk"
)

type result struct {
	feed     sdk.Feed
	art      *sdk.Article
	url      string
	listDate string
	raw      []byte
	err      error
	problems []string
}

var (
	datePattern    = regexp.MustCompile(`^20\d{2}[-/.年]\d{1,2}[-/.月]\d{1,2}`)
	articleURLHint = regexp.MustCompile(`(info/\d+/\d+|/\d{4,})\.s?html?|newsDetail`)
	navTitle       = regexp.MustCompile(`^(新闻动态|学院动态|中心要闻|党建要闻|通知公告|诚聘英才|查看更多|更多)$`)
	badMetaValue   = regexp.MustCompile(`^(来源|信息来源|作者|编辑|责编|摄影|日期|时间|审核|点击量?|浏览|发布|发布日期|发布时间|供稿|撰稿)$`)
)

func main() {
	verify := flag.Bool("verify", false, "print date extraction context per site")
	site := flag.String("site", "", "only check feeds of this site id")
	flag.Parse()
	_ = os.MkdirAll("/tmp/audit-raw", 0o755)
	client := sdk.New(sdk.Options{})
	feeds := sdk.Feeds()
	if *site != "" {
		var filtered []sdk.Feed
		for _, feed := range feeds {
			if feed.SiteID == *site {
				filtered = append(filtered, feed)
			}
		}
		if len(filtered) == 0 {
			fmt.Fprintf(os.Stderr, "no feeds for site %q\n", *site)
			os.Exit(2)
		}
		feeds = filtered
	}
	results := make([][]result, len(feeds))
	var wg sync.WaitGroup
	gate := make(chan struct{}, 4)
	for i, feed := range feeds {
		wg.Add(1)
		go func(i int, feed sdk.Feed) {
			defer wg.Done()
			gate <- struct{}{}
			defer func() { <-gate }()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			items, err := client.ListFeed(ctx, feed, 1)
			if err != nil {
				results[i] = []result{{feed: feed, err: fmt.Errorf("list: %v", err)}}
				return
			}
			picked := 0
			for _, item := range items {
				if picked >= 2 {
					break
				}
				if !articleURLHint.MatchString(item.URL) || navTitle.MatchString(strings.TrimSpace(item.Title)) {
					continue
				}
				picked++
				res := result{feed: feed, url: item.URL, listDate: item.Date}
				art, raw, err := client.FetchArticleSnapshot(ctx, item.URL)
				if err != nil {
					res.err = fmt.Errorf("article: %v", err)
				} else {
					res.art = art
					res.raw = raw
					res.problems = suspicious(art)
					if len(res.problems) > 0 {
						name := strings.NewReplacer("/", "_", ":", "_").Replace(feed.ID) + ".html"
						_ = os.WriteFile(filepath.Join("/tmp/audit-raw", name), raw, 0o644)
					}
				}
				results[i] = append(results[i], res)
			}
			if picked == 0 && len(items) > 0 {
				results[i] = []result{{feed: feed, err: fmt.Errorf("no article-like URL among %d items (first: %s)", len(items), items[0].URL)}}
			} else if len(items) == 0 {
				results[i] = []result{{feed: feed, err: fmt.Errorf("list: 0 items")}}
			}
		}(i, feed)
	}
	wg.Wait()

	var flat []result
	for _, group := range results {
		flat = append(flat, group...)
	}
	sort.Slice(flat, func(i, j int) bool {
		if flat[i].feed.ID != flat[j].feed.ID {
			return flat[i].feed.ID < flat[j].feed.ID
		}
		return flat[i].url < flat[j].url
	})
	if *verify {
		printVerify(flat)
		return
	}
	bad := 0
	for _, res := range flat {
		if res.err != nil {
			bad++
			fmt.Printf("[ERR ] %-22s %v\n", res.feed.ID, res.err)
			continue
		}
		tag := "OK  "
		if len(res.problems) > 0 {
			bad++
			tag = "BAD " + strings.Join(res.problems, ",")
		}
		fmt.Printf("[%-14s] %-22s date=%q author=%q source=%q title=%q\n",
			tag, res.feed.ID, res.art.Date, res.art.Author, res.art.Source, truncate(res.art.Title, 30))
	}
	fmt.Printf("\n%d/%d samples look wrong\n", bad, len(flat))
}

// printVerify shows, per sample, the extracted date alongside the snippets of
// page text where that date (in any common format) actually appears, plus the
// list-page date for cross-checking.
func printVerify(flat []result) {
	tagRe := regexp.MustCompile(`<[^>]+>`)
	spaceRe := regexp.MustCompile(`\s+`)
	labeledRe := regexp.MustCompile(`(日期|时间|发布)[^:：]{0,6}[:：][^<>]{0,40}`)
	for _, res := range flat {
		if res.err != nil {
			fmt.Printf("### %s  ERROR %v\n\n", res.feed.ID, res.err)
			continue
		}
		fmt.Printf("### %s (%s)\n", res.feed.ID, res.feed.SiteName)
		fmt.Printf("title: %s\nlist : %q\nextr : %q\n", truncate(res.art.Title, 50), res.listDate, res.art.Date)
		text := spaceRe.ReplaceAllString(tagRe.ReplaceAllString(string(res.raw), " "), " ")
		if res.art.Date != "" {
			found := false
			for _, variant := range dateVariants(res.art.Date) {
				if idx := strings.Index(text, variant); idx >= 0 {
					start := max(0, idx-60)
					end := min(len(text), idx+len(variant)+60)
					fmt.Printf("ctx  : …%s…\n", text[start:end])
					found = true
					break
				}
			}
			if !found {
				fmt.Printf("ctx  : extracted date NOT found in page!\n")
			}
		}
		seen := map[string]bool{}
		for _, m := range labeledRe.FindAllString(text, 3) {
			if !seen[m] {
				seen[m] = true
				fmt.Printf("label: %s\n", m)
			}
		}
		fmt.Println()
	}
}

// dateVariants renders an ISO date in the formats sites typically print.
func dateVariants(iso string) []string {
	t, err := time.Parse("2006-01-02", iso)
	if err != nil {
		return []string{iso}
	}
	return []string{
		t.Format("2006-01-02"),
		t.Format("2006/01/02"),
		t.Format("2006.01.02"),
		fmt.Sprintf("%d年%d月%d日", t.Year(), int(t.Month()), t.Day()),
		fmt.Sprintf("%d年%02d月%02d日", t.Year(), int(t.Month()), t.Day()),
	}
}

func suspicious(art *sdk.Article) []string {
	var problems []string
	if !datePattern.MatchString(strings.TrimSpace(art.Date)) {
		problems = append(problems, "date")
	}
	if art.Title == "" {
		problems = append(problems, "title")
	}
	if len(strings.TrimSpace(art.Content)) < 20 && len(strings.TrimSpace(art.ContentHTML)) < 20 {
		problems = append(problems, "content")
	}
	if badMetaValue.MatchString(art.Author) || badMetaValue.MatchString(art.Source) ||
		strings.ContainsRune(art.Author, ' ') || strings.ContainsRune(art.Source, ' ') {
		problems = append(problems, "meta")
	}
	return problems
}

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) > n {
		return string(runes[:n]) + "…"
	}
	return s
}
