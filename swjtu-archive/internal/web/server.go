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

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
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
	facets, err := s.store.SiteFacets()
	if err != nil {
		http.Error(w, "archive query failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Add("Vary", "Accept")
	month := r.URL.Query().Get("month")
	if !validMonth(month) {
		month = ""
	}
	if prefersMarkdown(r) {
		writeMarkdown(w, s.indexMarkdown(r, items, total, filter, month))
		return
	}
	type siteItem struct {
		archive.SiteFacet
		Active bool
	}
	sites := make([]siteItem, 0, len(facets))
	for _, facet := range facets {
		sites = append(sites, siteItem{facet, facet.SiteID == filter.SiteID})
	}
	data := struct {
		Items    []archive.ArticleSummary
		Sites    []siteItem
		Total    int
		Page     int
		PageSize int
		Query    string
		Site     string
		Month    string
		Category string
		From     string
		To       string
		HasPrev  bool
		HasNext  bool
		PrevPage int
		NextPage int
	}{
		Items: items, Sites: sites, Total: total, Page: filter.Page, PageSize: filter.PageSize,
		Query: filter.Query, Site: filter.SiteID, Month: month, Category: filter.Category,
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
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Add("Vary", "Accept")
	if prefersMarkdown(r) {
		writeMarkdown(w, s.articleMarkdown(item, requestBase(r)))
		return
	}
	if err := articleTemplate.Execute(w, data); err != nil {
		return
	}
}

func writeMarkdown(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

// prefersMarkdown reports whether the client asked for text/markdown with a
// quality value at least as high as text/html, per RFC 9110 content negotiation.
func prefersMarkdown(r *http.Request) bool {
	mdQ, htmlQ := -1.0, -1.0
	for _, part := range strings.Split(r.Header.Get("Accept"), ",") {
		mediaType, params, err := mime.ParseMediaType(strings.TrimSpace(part))
		if err != nil {
			continue
		}
		q := 1.0
		if value, ok := params["q"]; ok {
			if parsed, err := strconv.ParseFloat(value, 64); err == nil {
				q = parsed
			}
		}
		switch mediaType {
		case "text/markdown":
			mdQ = max(mdQ, q)
		case "text/html":
			htmlQ = max(htmlQ, q)
		}
	}
	return mdQ > 0 && mdQ >= htmlQ
}

func requestBase(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwarded := r.Header.Get("X-Forwarded-Proto"); forwarded != "" {
		scheme = forwarded
	}
	return scheme + "://" + r.Host
}

func (s *Server) indexMarkdown(r *http.Request, items []archive.ArticleSummary, total int, filter archive.ArticleFilter, month string) string {
	base := requestBase(r)
	var b strings.Builder
	b.WriteString("# SWJTU 新闻归档\n\n")
	fmt.Fprintf(&b, "共 %d 篇文章 · 第 %d 页\n\n", total, filter.Page)
	for _, item := range items {
		fmt.Fprintf(&b, "- [%s](%s/article/%d)", item.Title, base, item.ID)
		meta := make([]string, 0, 3)
		if item.SiteName != "" {
			meta = append(meta, item.SiteName)
		}
		if item.PublishedAt != "" {
			meta = append(meta, item.PublishedAt)
		}
		if item.Author != "" {
			meta = append(meta, item.Author)
		}
		if len(meta) > 0 {
			b.WriteString(" — " + strings.Join(meta, " · "))
		}
		b.WriteString("\n")
	}
	pager := func(page int) string {
		values := url.Values{}
		values.Set("page", strconv.Itoa(page))
		if filter.Query != "" {
			values.Set("q", filter.Query)
		}
		if filter.SiteID != "" {
			values.Set("site", filter.SiteID)
		}
		if month != "" {
			values.Set("month", month)
		}
		return base + "/?" + values.Encode()
	}
	if filter.Page > 1 {
		fmt.Fprintf(&b, "\n[上一页](%s)", pager(filter.Page-1))
	}
	if filter.Page*filter.PageSize < total {
		fmt.Fprintf(&b, "\n[下一页](%s)", pager(filter.Page+1))
	}
	b.WriteString("\n")
	return b.String()
}

func (s *Server) articleMarkdown(item *archive.ArticleRecord, base string) string {
	var b strings.Builder
	b.WriteString("# " + item.Title + "\n\n")
	meta := make([]string, 0, 6)
	if item.SiteName != "" {
		meta = append(meta, item.SiteName)
	}
	if item.PublishedAt != "" {
		meta = append(meta, item.PublishedAt)
	}
	if item.Source != "" {
		meta = append(meta, "来源："+item.Source)
	}
	if item.Author != "" {
		meta = append(meta, "作者："+item.Author)
	}
	if item.Photographer != "" {
		meta = append(meta, "摄影："+item.Photographer)
	}
	if item.Editor != "" {
		meta = append(meta, "编辑："+item.Editor)
	}
	if len(meta) > 0 {
		b.WriteString("> " + strings.Join(meta, " · ") + "\n\n")
	}
	body := ""
	if html := strings.TrimSpace(string(s.safeContent(item))); html != "" {
		if converted, err := htmltomarkdown.ConvertString(html); err == nil {
			body = strings.TrimSpace(converted)
		}
	}
	if body == "" {
		body = strings.TrimSpace(item.Content)
	}
	// Point relative archived-resource links at this server so the markdown
	// is usable outside the original page context.
	body = strings.ReplaceAll(body, "](/", "]("+base+"/")
	b.WriteString(body + "\n")
	attachments := make([]archive.ResourceRecord, 0)
	for _, resource := range item.Resources {
		if resource.Kind == "attachment" {
			attachments = append(attachments, resource)
		}
	}
	if len(attachments) > 0 {
		b.WriteString("\n## 附件\n\n")
		for _, attachment := range attachments {
			fmt.Fprintf(&b, "- [%s](%s/assets/%d)（%d bytes）\n", attachment.Filename, base, attachment.ID, attachment.ByteSize)
		}
	}
	if item.CanonicalURL != "" {
		b.WriteString("\n---\n\n原文链接：" + item.CanonicalURL + "\n")
	}
	return b.String()
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
	filter := archive.ArticleFilter{
		Query: query.Get("q"), SiteID: query.Get("site"), Category: query.Get("category"),
		FeedID: query.Get("feed"), From: query.Get("from"), To: query.Get("to"),
		Page: page, PageSize: pageSize,
	}
	if month := query.Get("month"); validMonth(month) {
		filter.From = month + "-01"
		filter.To = month + "-31"
	}
	return filter
}

func validMonth(value string) bool {
	if len(value) != 7 || value[4] != '-' {
		return false
	}
	for i, c := range value {
		if i == 4 {
			continue
		}
		if c < '0' || c > '9' {
			return false
		}
	}
	month, _ := strconv.Atoi(value[5:7])
	return month >= 1 && month <= 12
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
:root{--primary:#1a5fb4;--primary-dark:#1c4a8c;--text:#1f2329;--muted:#6b7280;--line:#e5e7eb;--bg:#f4f5f7;--card:#fff;--hover:#f0f4fa}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--text);font-family:system-ui,-apple-system,"PingFang SC","Microsoft YaHei","Noto Sans SC",sans-serif;font-size:14px;line-height:1.6}
a{color:var(--primary);text-decoration:none}
a:hover{text-decoration:underline}
.topbar{background:var(--card);border-bottom:1px solid var(--line);position:sticky;top:0;z-index:10}
.topbar-inner{max-width:1500px;margin:0 auto;padding:0 20px;height:56px;display:flex;align-items:center;gap:14px}
.logo{width:30px;height:30px;border-radius:7px;background:var(--primary);color:#fff;display:flex;align-items:center;justify-content:center;font-size:15px;font-weight:700;flex-shrink:0}
.topbar h1{margin:0;font-size:16px;font-weight:600;letter-spacing:.5px;white-space:nowrap}
.topbar .sub{color:var(--muted);font-size:12px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.nav-toggle{display:none}
.nav-toggle-btn{display:none;margin-left:auto;padding:6px 14px;border:1px solid var(--line);border-radius:7px;background:var(--card);font-size:13px;cursor:pointer;user-select:none}
.layout{max-width:1500px;margin:0 auto;padding:20px;display:grid;grid-template-columns:250px minmax(0,1fr);gap:20px;align-items:start}
.sidebar{background:var(--card);border:1px solid var(--line);border-radius:10px;padding:16px;position:sticky;top:76px}
.side-section{margin-bottom:20px}
.side-section:last-child{margin-bottom:0}
.side-title{font-size:12px;font-weight:600;color:var(--muted);text-transform:uppercase;letter-spacing:.8px;margin-bottom:10px}
.site-list{list-style:none;margin:0;padding:0;max-height:52vh;overflow-y:auto}
.site-list a{display:flex;align-items:center;gap:8px;padding:7px 10px;border-radius:7px;color:var(--text);font-size:13.5px}
.site-list a:hover{background:var(--hover);text-decoration:none}
.site-list a.active{background:#e8f0fb;color:var(--primary);font-weight:600}
.site-list .count{margin-left:auto;color:var(--muted);font-size:12px;flex-shrink:0}
.site-list a.active .count{color:var(--primary)}
.month-form{display:flex;flex-direction:column;gap:8px}
.month-form input[type=month]{padding:8px 10px;border:1px solid var(--line);border-radius:7px;font-size:13.5px;font-family:inherit;background:#fff;color:var(--text)}
.month-form input[type=month]:focus{outline:none;border-color:var(--primary);box-shadow:0 0 0 3px #1a5fb41a}
.btn{padding:8px 14px;border:0;border-radius:7px;background:var(--primary);color:#fff;font-size:13.5px;font-weight:600;cursor:pointer}
.btn:hover{background:var(--primary-dark)}
.clear-link{display:inline-block;margin-top:10px;font-size:12.5px}
.search{display:flex;gap:10px;margin-bottom:16px}
.search input{flex:1;padding:10px 14px;border:1px solid var(--line);border-radius:8px;font-size:14px;font-family:inherit;background:var(--card)}
.search input:focus{outline:none;border-color:var(--primary);box-shadow:0 0 0 3px #1a5fb41a}
.result-meta{color:var(--muted);font-size:13px;margin:0 2px 12px;display:flex;align-items:center;gap:8px;flex-wrap:wrap}
.result-meta b{color:var(--text)}
.filter-chip{display:inline-flex;align-items:center;gap:6px;padding:3px 6px 3px 10px;border-radius:99px;background:#e8f0fb;color:var(--primary);font-size:12.5px;font-weight:500}
.filter-chip a{color:var(--primary);display:flex;padding:0 4px;font-weight:700}
.filter-chip a:hover{text-decoration:none;opacity:.7}
.list{background:var(--card);border:1px solid var(--line);border-radius:10px;overflow:hidden}
article.item{padding:16px 20px;border-bottom:1px solid var(--line)}
article.item:last-child{border-bottom:0}
article.item:hover{background:var(--hover)}
article.item h2{margin:0 0 6px;font-size:16px;line-height:1.5;font-weight:600}
article.item h2 a{color:var(--text)}
article.item h2 a:hover{color:var(--primary);text-decoration:none}
.meta{color:var(--muted);font-size:12.5px;display:flex;flex-wrap:wrap;align-items:center;gap:6px}
.badge{display:inline-block;padding:1px 8px;border-radius:5px;font-size:12px;background:#eef2f7;color:#4b5563;font-weight:500}
.dot::before{content:"·";margin:0 4px;color:#cbd5e1}
.origin{font-size:12.5px;margin-left:auto;white-space:nowrap}
.empty{background:var(--card);border:1px solid var(--line);border-radius:10px;padding:60px 20px;text-align:center;color:var(--muted)}
.pager{display:flex;justify-content:center;align-items:center;gap:14px;margin-top:18px}
.pager a{padding:8px 18px;border-radius:7px;background:var(--card);border:1px solid var(--line);font-size:13.5px;font-weight:500}
.pager a:hover{border-color:var(--primary);color:var(--primary);text-decoration:none}
.pager .page-info{color:var(--muted);font-size:12.5px}
@media(max-width:900px){
.layout{grid-template-columns:1fr;padding:12px}
.nav-toggle-btn{display:block}
.sidebar{display:none;position:static}
.nav-toggle:checked~.layout .sidebar{display:block}
.topbar .sub{display:none}
.site-list{max-height:none}
}
</style></head><body>
<header class="topbar"><div class="topbar-inner"><div class="logo">交</div><h1>SWJTU 新闻归档</h1><span class="sub">西南交通大学校园新闻离线存档与检索</span><label class="nav-toggle-btn" for="nav-toggle">筛选 ☰</label></div></header>
<input type="checkbox" id="nav-toggle" class="nav-toggle">
<div class="layout">
<aside class="sidebar">
<div class="side-section"><div class="side-title">来源筛选</div>
<ul class="site-list">
<li><a href="?{{if .Query}}q={{.Query}}&{{end}}{{if .Month}}month={{.Month}}{{end}}"{{if not .Site}} class="active"{{end}}><span>全部来源</span></a></li>
{{range .Sites}}<li><a href="?site={{.SiteID}}{{if $.Query}}&q={{$.Query}}{{end}}{{if $.Month}}&month={{$.Month}}{{end}}"{{if .Active}} class="active"{{end}}><span>{{if .SiteName}}{{.SiteName}}{{else}}{{.SiteID}}{{end}}</span><span class="count">{{.Count}}</span></a></li>{{end}}
</ul></div>
<div class="side-section"><div class="side-title">时间筛选</div>
<form class="month-form" method="get">
{{if .Query}}<input type="hidden" name="q" value="{{.Query}}">{{end}}{{if .Site}}<input type="hidden" name="site" value="{{.Site}}">{{end}}
<input type="month" name="month" value="{{.Month}}">
<button class="btn" type="submit">按月查看</button>
</form>{{if or .Site .Month .Query}}<a class="clear-link" href="/">✕ 清除全部筛选</a>{{end}}</div>
</aside>
<main>
<form class="search" method="get">
{{if .Site}}<input type="hidden" name="site" value="{{.Site}}">{{end}}{{if .Month}}<input type="hidden" name="month" value="{{.Month}}">{{end}}
<input name="q" value="{{.Query}}" placeholder="搜索标题、正文、作者…">
<button class="btn" type="submit">搜索</button>
</form>
<div class="result-meta">共 <b>{{.Total}}</b> 篇文章
{{if .Site}}{{range .Sites}}{{if .Active}}<span class="filter-chip">{{.SiteName}}<a href="?{{if $.Query}}q={{$.Query}}&{{end}}{{if $.Month}}month={{$.Month}}{{end}}" title="移除来源筛选">✕</a></span>{{end}}{{end}}{{end}}
{{if .Month}}<span class="filter-chip">{{.Month}}<a href="?{{if .Site}}site={{.Site}}&{{end}}{{if .Query}}q={{.Query}}{{end}}" title="移除时间筛选">✕</a></span>{{end}}
</div>
{{if .Items}}<div class="list">{{range .Items}}<article class="item">
<h2><a href="/article/{{.ID}}">{{.Title}}</a></h2>
<div class="meta"><span class="badge">{{.SiteName}}</span><span>{{.PublishedAt}}</span>{{if .Author}}<span class="dot"></span><span>{{.Author}}</span>{{end}}<a class="origin" href="{{.CanonicalURL}}" rel="noopener" target="_blank">原文 ↗</a></div>
</article>{{end}}</div>{{else}}<div class="empty"><p>暂无符合条件的归档文章</p><a href="/">清除筛选条件</a></div>{{end}}
<div class="pager">
{{if .HasPrev}}<a href="?page={{.PrevPage}}&q={{.Query}}&site={{.Site}}&month={{.Month}}">← 上一页</a>{{end}}
<span class="page-info">第 {{.Page}} 页</span>
{{if .HasNext}}<a href="?page={{.NextPage}}&q={{.Query}}&site={{.Site}}&month={{.Month}}">下一页 →</a>{{end}}
</div>
</main>
</div>
</body></html>`))

var articleTemplate = template.Must(template.New("article").Parse(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Item.Title}} - SWJTU 新闻归档</title><style>
:root{--primary:#1a5fb4;--primary-dark:#1c4a8c;--text:#1f2329;--muted:#6b7280;--line:#e5e7eb;--bg:#f4f5f7;--card:#fff}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--text);font-family:system-ui,-apple-system,"PingFang SC","Microsoft YaHei","Noto Sans SC",sans-serif;font-size:16px;line-height:1.85}
a{color:var(--primary);text-decoration:none}
a:hover{text-decoration:underline}
.topbar{background:var(--card);border-bottom:1px solid var(--line)}
.topbar-inner{max-width:1100px;margin:0 auto;padding:0 20px;height:56px;display:flex;align-items:center;gap:14px}
.logo{width:30px;height:30px;border-radius:7px;background:var(--primary);color:#fff;display:flex;align-items:center;justify-content:center;font-size:15px;font-weight:700;flex-shrink:0}
.back{padding:6px 14px;border:1px solid var(--line);border-radius:7px;font-size:13px;color:var(--text)}
.back:hover{border-color:var(--primary);color:var(--primary);text-decoration:none}
.container{max-width:1100px;margin:0 auto;padding:0 20px 48px}
.paper{background:var(--card);border:1px solid var(--line);border-radius:10px;padding:40px 48px;margin-top:20px}
h1{font-size:24px;line-height:1.45;text-align:center;margin:0 0 14px}
.meta{text-align:center;color:var(--muted);font-size:13px;display:flex;flex-wrap:wrap;justify-content:center;align-items:center;gap:6px;padding-bottom:20px;border-bottom:1px solid var(--line)}
.badge{display:inline-block;padding:1px 8px;border-radius:5px;font-size:12px;background:#eef2f7;color:#4b5563;font-weight:500}
.dot::before{content:"·";margin:0 4px;color:#cbd5e1}
.content{margin-top:24px;word-wrap:break-word}
.content p{margin:0 0 1em}
.content img{max-width:100%;height:auto;display:block;margin:14px auto;border:1px solid var(--line)}
.content a{word-break:break-all}
.content table{border-collapse:collapse;width:100%;margin:16px 0;font-size:14px}
.content td,.content th{border:1px solid var(--line);padding:8px 12px}
.content th{background:#f8fafc}
.content blockquote{margin:16px 0;padding:4px 18px;border-left:4px solid var(--primary);background:#f8fafc;color:#4b5563}
.resources{margin-top:32px;padding-top:18px;border-top:1px solid var(--line)}
.resources h2{font-size:16px;margin:0 0 12px}
.resources ul{list-style:none;margin:0;padding:0;display:flex;flex-direction:column;gap:8px}
.resources li a{display:flex;align-items:center;gap:10px;padding:10px 14px;border:1px solid var(--line);border-radius:7px;font-size:14px}
.resources li a:hover{border-color:var(--primary);color:var(--primary);text-decoration:none}
.resources .size{margin-left:auto;color:var(--muted);font-size:12px;flex-shrink:0}
.footer-note{margin-top:26px;padding-top:16px;border-top:1px solid var(--line);color:var(--muted);font-size:13px;word-break:break-all}
@media(max-width:640px){.paper{padding:24px 20px}h1{font-size:20px}}
</style></head><body>
<header class="topbar"><div class="topbar-inner"><a class="back" href="/">← 返回列表</a></div></header>
<div class="container"><div class="paper">
<h1>{{.Item.Title}}</h1>
<div class="meta"><span class="badge">{{.Item.SiteName}}</span><span>{{.Item.PublishedAt}}</span>{{if .Item.Source}}<span class="dot"></span><span>来源：{{.Item.Source}}</span>{{end}}{{if .Item.Author}}<span class="dot"></span><span>作者：{{.Item.Author}}</span>{{end}}{{if .Item.Photographer}}<span class="dot"></span><span>摄影：{{.Item.Photographer}}</span>{{end}}{{if .Item.Editor}}<span class="dot"></span><span>编辑：{{.Item.Editor}}</span>{{end}}</div>
<div class="content">{{.ContentHTML}}</div>
{{if .Attachments}}<section class="resources"><h2>附件</h2><ul>{{range .Attachments}}<li><a href="/assets/{{.ID}}" download="{{.Filename}}">{{.Filename}}<span class="size">{{.ByteSize}} bytes</span></a></li>{{end}}</ul></section>{{end}}
<div class="footer-note">原文链接：<a href="{{.Item.CanonicalURL}}" target="_blank" rel="noopener">{{.Item.CanonicalURL}}</a></div>
</div></div>
</body></html>`))
