package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"time"

	"teach-crawler/internal/crawler"
)

func main() {
	baseURL := flag.String("base-url", crawler.DefaultBaseURL, "faculty site origin")
	lettersFlag := flag.String("letters", "a-z", "letters to crawl, for example a or a,c,d")
	output := flag.String("output", "/data/teachers.json", "JSON snapshot path")
	rawDir := flag.String("raw-dir", "", "optional directory for raw HTML snapshots")
	workers := flag.Int("workers", 4, "concurrent profile requests")
	delay := flag.Duration("request-gap", 350*time.Millisecond, "minimum gap between HTTP requests")
	timeout := flag.Duration("timeout", 30*time.Second, "per-request timeout")
	retries := flag.Int("retries", 2, "retries for network errors, HTTP 429 and 5xx")
	maxTeachers := flag.Int("max-teachers", 0, "limit profiles after de-duplication; 0 means no limit")
	listOnly := flag.Bool("list-only", false, "crawl directory pages without fetching profiles")
	failOnProfileError := flag.Bool("fail-on-profile-error", false, "return non-zero when any profile fails")
	flag.Parse()

	letters, err := parseLetters(*lettersFlag)
	if err != nil {
		log.Fatal(err)
	}

	base, err := normalizeBase(*baseURL)
	if err != nil {
		log.Fatal(err)
	}
	fetcher := crawler.NewHTTPFetcher(base.Hostname(), *timeout, *delay, *retries)
	instance, err := crawler.New(crawler.Options{
		BaseURL: *baseURL, Letters: letters, Workers: *workers,
		Fetcher: fetcher, RawDir: *rawDir, ListOnly: *listOnly,
		MaxTeachers: *maxTeachers, Logger: log.New(os.Stderr, "teach-crawler ", log.LstdFlags),
	})
	if err != nil {
		log.Fatal(err)
	}
	result, err := instance.Crawl(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	if err := crawler.WriteJSON(*output, result); err != nil {
		log.Fatal(err)
	}
	fmt.Fprintf(os.Stdout, "wrote %s: list_pages=%d failed_lists=%d directory_entries=%d unique_teachers=%d profiles_ok=%d profiles_failed=%d name_mismatches=%d\n",
		*output, result.Stats.ListPages, result.Stats.ListPagesFailed,
		result.Stats.DirectoryEntries, result.Stats.UniqueTeachers,
		result.Stats.ProfilesSucceeded, result.Stats.ProfilesFailed,
		result.Stats.NameMismatches)
	if result.Stats.ListPagesFailed > 0 || (*failOnProfileError && result.Stats.ProfilesFailed > 0) {
		os.Exit(1)
	}
}

func parseLetters(value string) ([]string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "a-z" || value == "全字母" {
		return crawler.DefaultLetters(), nil
	}
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' || r == ';' })
	if len(parts) == 0 {
		return nil, fmt.Errorf("letters cannot be empty")
	}
	letters := make([]string, 0, len(parts))
	for _, part := range parts {
		if len([]rune(part)) != 1 || part[0] < 'a' || part[0] > 'z' {
			return nil, fmt.Errorf("invalid letter %q; use a-z or comma-separated letters", part)
		}
		letters = append(letters, part)
	}
	return letters, nil
}

func normalizeBase(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("invalid base URL %q", raw)
	}
	return u, nil
}
