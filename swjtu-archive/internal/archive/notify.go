package archive

import (
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"swjtu-archive/internal/notify"
)

// notifyFeishu delivers text to the Feishu webhook configured by
// FEISHU_WEBHOOK_URL. It is a silent no-op when the variable is unset and
// only logs on failure, so alerting can never break the sync itself.
func notifyFeishu(logger *log.Logger, text string) {
	if err := notify.Send(text); err != nil && logger != nil {
		logger.Printf("feishu notify: %v", err)
	}
}

const (
	// errorSampleLimit is how many raw messages of each error type the
	// collector keeps for the summary.
	errorSampleLimit = 2
	// errorSampleMaxChars bounds one retained raw message.
	errorSampleMaxChars = 200
	// maxErrorTypes bounds distinct collected error types; anything beyond
	// is counted under an overflow bucket.
	maxErrorTypes = 50
	// summaryTopErrors is how many error types the summary message lists.
	summaryTopErrors = 5
	// summaryMaxBytes keeps one message comfortably inside the Feishu text
	// limit.
	summaryMaxBytes = 3500
)

const overflowErrorKey = "其他错误"

var errorURLPattern = regexp.MustCompile(`https?://\S+`)

type errorEntry struct {
	key     string
	count   int
	samples []string
}

// syncErrorCollector groups sync errors by their URL-stripped message so the
// summary can report "3 × 抓取失败: timeout" instead of one line per article.
type syncErrorCollector struct {
	mu      sync.Mutex
	byKey   map[string]*errorEntry
	entries []*errorEntry
}

func newSyncErrorCollector() *syncErrorCollector {
	return &syncErrorCollector{byKey: make(map[string]*errorEntry)}
}

func (c *syncErrorCollector) add(message string) {
	message = strings.TrimSpace(message)
	if message == "" {
		return
	}
	key := errorURLPattern.ReplaceAllString(message, "<URL>")
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.byKey[key]
	if entry == nil {
		if len(c.entries) >= maxErrorTypes {
			entry = c.byKey[overflowErrorKey]
			if entry == nil {
				entry = &errorEntry{key: overflowErrorKey}
				c.byKey[overflowErrorKey] = entry
				c.entries = append(c.entries, entry)
			}
		} else {
			entry = &errorEntry{key: key}
			c.byKey[key] = entry
			c.entries = append(c.entries, entry)
		}
	}
	entry.count++
	if entry.key != overflowErrorKey && len(entry.samples) < errorSampleLimit {
		entry.samples = append(entry.samples, truncateRunes(message, errorSampleMaxChars))
	}
}

// snapshot returns the collected entries sorted by count descending.
func (c *syncErrorCollector) snapshot() []*errorEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*errorEntry, 0, len(c.entries))
	for _, entry := range c.entries {
		cp := *entry
		cp.samples = append([]string(nil), entry.samples...)
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].count != out[j].count {
			return out[i].count > out[j].count
		}
		return out[i].key < out[j].key
	})
	return out
}

// severeSyncError reports whether an error means the sync cannot make useful
// progress — revoked object-store credentials, login/auth failures — so the
// whole run should abort immediately with an alert instead of limping through
// every remaining feed. A bare 403 on one article asset is routine and is not
// treated as severe; only an R2-level 401/403 is.
func severeSyncError(message string) bool {
	lower := strings.ToLower(message)
	for _, marker := range []string{
		"accessdenied",
		"invalidaccesskeyid",
		"signaturedoesnotmatch",
		"invalidtoken",
		"tokenrefresherror",
		"unauthorized",
		"认证失败",
		"鉴权失败",
		"未登录",
		"登录失败",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	if strings.HasPrefix(message, "R2 ") {
		if strings.Contains(message, " 401 ") || strings.Contains(message, " 403 ") ||
			strings.Contains(message, "Forbidden") || strings.Contains(message, "Unauthorized") {
			return true
		}
	}
	return false
}

func buildSevereAlertMessage(runID int64, cause string, result SyncResult, started, now time.Time) string {
	var b strings.Builder
	b.WriteString("【严重】同步任务已中断\n")
	fmt.Fprintf(&b, "run=%d\n", runID)
	fmt.Fprintf(&b, "原因: %s\n", truncateBytes(cause, 500))
	fmt.Fprintf(&b, "已处理 feeds=%d articles=%d resources=%d failures=%d 耗时=%s\n",
		result.Feeds, result.Articles, result.Resources, result.Failures, now.Sub(started).Round(time.Second))
	return truncateBytes(b.String(), summaryMaxBytes)
}

func buildSyncSummaryMessage(result SyncResult, started, finished time.Time, entries []*errorEntry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "【同步告警】run=%d 采集存在失败\n", result.RunID)
	fmt.Fprintf(&b, "feeds=%d articles=%d resources=%d failures=%d 耗时=%s\n",
		result.Feeds, result.Articles, result.Resources, result.Failures, finished.Sub(started).Round(time.Second))
	for i, entry := range entries {
		if i >= summaryTopErrors {
			fmt.Fprintf(&b, "…另有 %d 类错误\n", len(entries)-i)
			break
		}
		fmt.Fprintf(&b, "×%d %s\n", entry.count, entry.key)
		for _, sample := range entry.samples {
			fmt.Fprintf(&b, "  示例: %s\n", sample)
		}
	}
	return truncateBytes(b.String(), summaryMaxBytes)
}

func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max]) + "…"
}

// truncateBytes cuts s to at most max bytes without splitting a multi-byte
// rune.
func truncateBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + "…"
}
