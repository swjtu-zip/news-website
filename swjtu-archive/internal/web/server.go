package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"swjtu-archive/internal/archive"
	"swjtu-cli/pkg/sdk"
)

// Server exposes the read-only archive UI and JSON API.
type Server struct {
	store *archive.Store
}

func NewServer(store *archive.Store) *Server { return &Server{store: store} }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/feeds", s.handleFeeds)
	mux.HandleFunc("/api/v1/articles", s.handleArticles)
	mux.HandleFunc("/api/v1/articles/", s.handleArticle)
	mux.HandleFunc("/api/v1/resources/", s.handleResource)
	mux.HandleFunc("/api/v1/sync/status", s.handleSyncStatus)
	mux.HandleFunc("/assets/", s.handleResource)
	mux.HandleFunc("/article/", s.handleArticlePage)
	mux.HandleFunc("/", s.handleIndex)
	return withCORS(mux)
}

func (s *Server) handleFeeds(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodOptions {
		methodNotAllowed(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": sdk.Feeds()})
}

func (s *Server) handleArticles(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/v1/articles" {
		notFoundJSON(w)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodOptions {
		methodNotAllowed(w)
		return
	}
	filter := articleFilter(r)
	items, total, err := s.store.FindArticles(filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": items, "page": filter.Page, "page_size": filter.PageSize, "total": total,
	})
}

func (s *Server) handleArticle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodOptions {
		methodNotAllowed(w)
		return
	}
	value := strings.TrimPrefix(r.URL.Path, "/api/v1/articles/")
	if strings.Contains(value, "/") {
		value = strings.SplitN(value, "/", 2)[0]
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		notFoundJSON(w)
		return
	}
	item, err := s.store.GetArticle(id)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			notFoundJSON(w)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, s.articleResponse(item))
}

func (s *Server) handleResource(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w)
		return
	}
	value := strings.TrimPrefix(r.URL.Path, "/assets/")
	if strings.HasPrefix(r.URL.Path, "/api/v1/resources/") {
		value = strings.TrimPrefix(r.URL.Path, "/api/v1/resources/")
	}
	value = strings.Trim(value, "/")
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	item, err := s.store.Resource(id)
	if err != nil || item.Status != "success" {
		http.NotFound(w, r)
		return
	}
	file, err := s.store.OpenResource(item)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if item.ContentType != "" {
		mediaType, _, _ := mime.ParseMediaType(item.ContentType)
		w.Header().Set("Content-Type", mediaType)
	}
	if item.Kind == "attachment" {
		w.Header().Set("Content-Disposition", contentDisposition(item.Filename))
	}
	http.ServeContent(w, r, item.Filename, info.ModTime(), file)
}

func (s *Server) handleSyncStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodOptions {
		methodNotAllowed(w)
		return
	}
	stats, err := s.store.Stats()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	run, err := s.store.LatestRun()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stats": stats, "latest_run": run})
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	filter := articleFilter(r)
	items, total, err := s.store.FindArticles(filter)
	if err != nil {
		http.Error(w, "archive query failed", http.StatusInternalServerError)
		return
	}
	data := struct {
		Items    []archive.ArticleSummary
		Total    int
		Page     int
		PageSize int
		Query    string
		Site     string
		Category string
		From     string
		To       string
		HasPrev  bool
		HasNext  bool
		PrevPage int
		NextPage int
	}{
		Items: items, Total: total, Page: filter.Page, PageSize: filter.PageSize,
		Query: filter.Query, Site: filter.SiteID, Category: filter.Category,
		From: filter.From, To: filter.To, HasPrev: filter.Page > 1,
		HasNext: filter.Page*filter.PageSize < total, PrevPage: filter.Page - 1, NextPage: filter.Page + 1,
	}
	if err := indexTemplate.Execute(w, data); err != nil {
		return
	}
}

func (s *Server) handleArticlePage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	idText := strings.Trim(strings.TrimPrefix(r.URL.Path, "/article/"), "/")
	id, err := strconv.ParseInt(idText, 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	item, err := s.store.GetArticle(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	data := struct {
		Item        *archive.ArticleRecord
		ContentHTML template.HTML
		Images      []archive.ResourceRecord
		Attachments []archive.ResourceRecord
	}{Item: item, ContentHTML: s.safeContent(item)}
	for _, resource := range item.Resources {
		if resource.Kind == "image" {
			data.Images = append(data.Images, resource)
		} else if resource.Kind == "attachment" {
			data.Attachments = append(data.Attachments, resource)
		}
	}
	if err := articleTemplate.Execute(w, data); err != nil {
		return
	}
}

func (s *Server) articleResponse(item *archive.ArticleRecord) map[string]any {
	resources := make([]map[string]any, 0, len(item.Resources))
	for _, resource := range item.Resources {
		resources = append(resources, map[string]any{
			"id": resource.ID, "kind": resource.Kind, "original_url": resource.OriginalURL,
			"filename": resource.Filename, "content_type": resource.ContentType,
			"byte_size": resource.ByteSize, "sha256": resource.SHA256, "status": resource.Status,
			"error": resource.Error, "url": "/assets/" + strconv.FormatInt(resource.ID, 10),
		})
	}
	return map[string]any{
		"id": item.ID, "title": item.Title, "source": item.Source, "site_id": item.SiteID,
		"site_name": item.SiteName, "feed_id": item.FeedID, "category": item.Category,
		"published_at": item.PublishedAt, "canonical_url": item.CanonicalURL, "author": item.Author,
		"photographer": item.Photographer, "editor": item.Editor, "content": item.Content,
		"content_html": string(s.safeContent(item)), "raw_sha256": item.RawSHA256,
		"first_seen_at": item.FirstSeenAt, "last_seen_at": item.LastSeenAt,
		"last_fetched_at": item.LastFetchedAt, "status": item.Status, "resources": resources,
	}
}

func (s *Server) safeContent(item *archive.ArticleRecord) template.HTML {
	content := item.ContentHTML
	if strings.TrimSpace(content) == "" {
		var builder strings.Builder
		for _, line := range strings.Split(item.Content, "\n") {
			if strings.TrimSpace(line) != "" {
				builder.WriteString("<p>")
				builder.WriteString(template.HTMLEscapeString(strings.TrimSpace(line)))
				builder.WriteString("</p>")
			}
		}
		content = builder.String()
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader("<div>" + content + "</div>"))
	if err != nil {
		return template.HTML(template.HTMLEscapeString(item.Content))
	}
	resourceURLs := make(map[string]string)
	for _, resource := range item.Resources {
		if resource.Status == "success" {
			resourceURLs[resource.OriginalURL] = "/assets/" + strconv.FormatInt(resource.ID, 10)
		}
	}
	doc.Find("script, style, iframe, object, embed, form, input, button, textarea, select, meta, link").Remove()
	allowedAttrs := map[string]bool{"href": true, "src": true, "alt": true, "title": true, "colspan": true, "rowspan": true}
	doc.Find("*").Each(func(_ int, sel *goquery.Selection) {
		for _, attr := range sel.Get(0).Attr {
			if !allowedAttrs[attr.Key] {
				sel.RemoveAttr(attr.Key)
			}
		}
		if href, ok := sel.Attr("href"); ok && !safeLink(href) {
			sel.RemoveAttr("href")
		}
		if href, ok := sel.Attr("href"); ok {
			if local, exists := resourceURLs[href]; exists {
				sel.SetAttr("href", local)
			} else if !safeLink(href) {
				sel.RemoveAttr("href")
			}
		}
		if src, ok := sel.Attr("src"); ok {
			if local, exists := resourceURLs[src]; exists {
				sel.SetAttr("src", local)
			} else if !safeLink(src) {
				sel.RemoveAttr("src")
			}
		}
	})
	value, err := doc.Find("body > div").Html()
	if err != nil {
		return template.HTML(template.HTMLEscapeString(item.Content))
	}
	return template.HTML(value)
}

func safeLink(value string) bool {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "/") {
		return true
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return false
	}
	return parsed.Scheme == "http" || parsed.Scheme == "https"
}

func articleFilter(r *http.Request) archive.ArticleFilter {
	query := r.URL.Query()
	page, _ := strconv.Atoi(query.Get("page"))
	pageSize, _ := strconv.Atoi(query.Get("page_size"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	return archive.ArticleFilter{
		Query: query.Get("q"), SiteID: query.Get("site"), Category: query.Get("category"),
		FeedID: query.Get("feed"), From: query.Get("from"), To: query.Get("to"),
		Page: page, PageSize: pageSize,
	}
}

func contentDisposition(filename string) string {
	filename = strings.TrimSpace(filename)
	if filename == "" {
		filename = "download"
	}
	return `attachment; filename="download"; filename*=UTF-8''` + url.PathEscape(filename)
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func notFoundJSON(w http.ResponseWriter) { writeError(w, http.StatusNotFound, fmt.Errorf("not found")) }

func methodNotAllowed(w http.ResponseWriter) {
	w.Header().Set("Allow", "GET, HEAD, OPTIONS")
	writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("method not allowed"))
}

var indexTemplate = template.Must(template.New("index").Parse(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>SWJTU 新闻归档</title><style>
body{max-width:1100px;margin:0 auto;padding:24px;font-family:system-ui,-apple-system,"Noto Sans SC",sans-serif;color:#202124;background:#f7f8fa}
main{background:#fff;padding:24px;border-radius:12px;box-shadow:0 2px 12px #0000000d}h1{margin-top:0}form{display:flex;gap:8px;flex-wrap:wrap;margin-bottom:20px}input{padding:9px;border:1px solid #ccd1d8;border-radius:6px}button{padding:9px 16px;border:0;border-radius:6px;background:#1769aa;color:#fff}article{padding:16px 0;border-bottom:1px solid #e8eaed}h2{font-size:18px;margin:0 0 8px}a{color:#145da0;text-decoration:none}a:hover{text-decoration:underline}.meta{color:#6b7280;font-size:13px}.pager{display:flex;justify-content:space-between;margin-top:20px}
</style></head><body><main><h1>SWJTU 新闻归档</h1>
<form method="get"><input name="q" value="{{.Query}}" placeholder="搜索标题和正文" size="32"><input name="site" value="{{.Site}}" placeholder="站点 ID"><input name="from" value="{{.From}}" placeholder="起始日期"><input name="to" value="{{.To}}" placeholder="结束日期"><button>搜索</button></form>
<p class="meta">共 {{.Total}} 篇，第 {{.Page}} 页</p>
{{range .Items}}<article><h2><a href="/article/{{.ID}}">{{.Title}}</a></h2><div class="meta">{{.PublishedAt}} · {{.SiteName}} · {{.Category}}{{if .Author}} · {{.Author}}{{end}}</div><p><a href="{{.CanonicalURL}}" rel="noopener" target="_blank">查看原文</a></p></article>{{else}}<p>暂无归档文章。</p>{{end}}
<div class="pager">{{if .HasPrev}}<a href="?page={{.PrevPage}}&q={{.Query}}&site={{.Site}}&from={{.From}}&to={{.To}}">上一页</a>{{else}}<span></span>{{end}}{{if .HasNext}}<a href="?page={{.NextPage}}&q={{.Query}}&site={{.Site}}&from={{.From}}&to={{.To}}">下一页</a>{{end}}</div>
</main></body></html>`))

var articleTemplate = template.Must(template.New("article").Parse(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Item.Title}} - SWJTU 新闻归档</title><style>
body{max-width:960px;margin:0 auto;padding:24px;font-family:system-ui,-apple-system,"Noto Sans SC",sans-serif;color:#202124;line-height:1.8}h1{text-align:center;line-height:1.4}.meta{text-align:center;color:#6b7280;font-size:14px}.content{margin-top:28px}.content img{max-width:100%;height:auto}.content table{border-collapse:collapse}.content td,.content th{border:1px solid #ccd1d8;padding:4px 8px}.resources{border-top:1px solid #e5e7eb;margin-top:32px;padding-top:16px}a{color:#145da0;text-decoration:none}a:hover{text-decoration:underline}.back{margin-bottom:20px}
</style></head><body><p class="back"><a href="/">← 返回列表</a></p><h1>{{.Item.Title}}</h1><div class="meta">{{.Item.PublishedAt}} · {{.Item.SiteName}}{{if .Item.Source}} · 来源：{{.Item.Source}}{{end}}{{if .Item.Author}} · 作者：{{.Item.Author}}{{end}}</div><div class="content">{{.ContentHTML}}</div>
{{if .Attachments}}<section class="resources"><h2>附件</h2><ul>{{range .Attachments}}<li><a href="/assets/{{.ID}}" download="{{.Filename}}">{{.Filename}}</a>（{{.ByteSize}} bytes）</li>{{end}}</ul></section>{{end}}
<section class="resources"><small>原文：<a href="{{.Item.CanonicalURL}}" target="_blank" rel="noopener">{{.Item.CanonicalURL}}</a></small></section>
</body></html>`))
