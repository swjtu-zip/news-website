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

// notifyCard delivers an interactive card to the Feishu webhook configured by
// FEISHU_WEBHOOK_URL. Silent no-op when the variable is unset.
func notifyCard(logger *log.Logger, card map[string]any) {
	if err := notify.SendCard(card); err != nil && logger != nil {
		logger.Printf("feishu card notify: %v", err)
	}
}

func mdText(content string) map[string]any {
	return map[string]any{"tag": "lark_md", "content": content}
}

func plainText(content string) map[string]any {
	return map[string]any{"tag": "plain_text", "content": content}
}

// buildSyncSummaryCard renders the end-of-run summary as a polished
// interactive card: stats overview on top, top error kinds below, and the
// full per-error samples inside a collapsed panel.
func buildSyncSummaryCard(result SyncResult, started, finished time.Time, entries []*errorEntry) map[string]any {
	duration := finished.Sub(started).Round(time.Second).String()

	var topLines strings.Builder
	for i, entry := range entries {
		if i >= summaryTopErrors {
			fmt.Fprintf(&topLines, "…另有 %d 类错误\n", len(entries)-i)
			break
		}
		fmt.Fprintf(&topLines, "• ×%d %s\n", entry.count, entry.key)
	}
	topText := strings.TrimSpace(topLines.String())
	if topText == "" {
		topText = "无"
	}

	var detailLines strings.Builder
	for i, entry := range entries {
		fmt.Fprintf(&detailLines, "**%d. ×%d %s**\n", i+1, entry.count, entry.key)
		for _, sample := range entry.samples {
			fmt.Fprintf(&detailLines, "› %s\n", sample)
		}
		detailLines.WriteString("\n")
	}
	detailText := strings.TrimSpace(detailLines.String())
	if detailText == "" {
		detailText = "无"
	}

	return map[string]any{
		"header": map[string]any{
			"title":    plainText("📰 新闻同步告警"),
			"subtitle": plainText(fmt.Sprintf("run=%d · %s", result.RunID, formatBeijing(finished))),
			"template": "red",
		},
		"elements": []any{
			map[string]any{
				"tag": "div",
				"fields": []any{
					map[string]any{"is_short": true, "text": mdText(fmt.Sprintf("**Feeds**\n%d", result.Feeds))},
					map[string]any{"is_short": true, "text": mdText(fmt.Sprintf("**新增文章**\n%d", result.Articles))},
					map[string]any{"is_short": true, "text": mdText(fmt.Sprintf("**资源**\n%d", result.Resources))},
					map[string]any{"is_short": true, "text": mdText(fmt.Sprintf("**失败**\n%d", result.Failures))},
					map[string]any{"is_short": true, "text": mdText(fmt.Sprintf("**耗时**\n%s", duration))},
				},
			},
			map[string]any{"tag": "hr"},
			map[string]any{
				"tag":  "div",
				"text": mdText("**Top 错误类型**\n" + topText),
			},
			map[string]any{
				"tag":      "collapsible_panel",
				"expanded": false,
				"header":   map[string]any{"title": plainText("📋 展开查看完整错误详情")},
				"elements": []any{
					map[string]any{
						"tag":  "div",
						"text": mdText(detailText),
					},
				},
			},
		},
	}
}

// buildSevereAlertCard renders the run-aborting alert as an urgent card.
func buildSevereAlertCard(runID int64, cause string, result SyncResult, started, now time.Time) map[string]any {
	return map[string]any{
		"header": map[string]any{
			"title":    plainText("🚨 同步严重告警：任务已中断"),
			"subtitle": plainText(fmt.Sprintf("run=%d · %s", runID, formatBeijing(now))),
			"template": "red",
		},
		"elements": []any{
			map[string]any{
				"tag":  "div",
				"text": mdText(fmt.Sprintf("**原因**\n%s", truncateBytes(cause, 500))),
			},
			map[string]any{"tag": "hr"},
			map[string]any{
				"tag": "div",
				"fields": []any{
					map[string]any{"is_short": true, "text": mdText(fmt.Sprintf("**Feeds**\n%d", result.Feeds))},
					map[string]any{"is_short": true, "text": mdText(fmt.Sprintf("**文章**\n%d", result.Articles))},
					map[string]any{"is_short": true, "text": mdText(fmt.Sprintf("**失败**\n%d", result.Failures))},
					map[string]any{"is_short": true, "text": mdText(fmt.Sprintf("**已耗时**\n%s", now.Sub(started).Round(time.Second).String()))},
				},
			},
		},
	}
}

func formatBeijing(t time.Time) string {
	return t.In(time.FixedZone("Asia/Shanghai", 8*60*60)).Format("2006-01-02 15:04:05")
}

func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max]) + "…"
}

func truncateBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
