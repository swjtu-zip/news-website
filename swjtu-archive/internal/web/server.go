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
	"swjtu-archive/internal/mcp"
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
	mux.HandleFunc("/help/mcp", s.handleMCPGuide)
	mux.Handle("/mcp", mcp.NewHandler(s.store))
	mux.HandleFunc("/", s.handleIndex)
	return withCache(withCORS(mux))
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
	type pageItem struct {
		Number   int
		URL      string
		Current  bool
		Ellipsis bool
	}
	sites := make([]siteItem, 0, len(facets))
	for _, facet := range facets {
		sites = append(sites, siteItem{facet, facet.SiteID == filter.SiteID})
	}
	totalPages := 0
	if total > 0 {
		totalPages = (total + filter.PageSize - 1) / filter.PageSize
	}
	pageNumbers := make([]pageItem, 0, 9)
	for _, number := range paginationNumbers(filter.Page, totalPages, 7) {
		if number == 0 {
			pageNumbers = append(pageNumbers, pageItem{Ellipsis: true})
			continue
		}
		pageNumbers = append(pageNumbers, pageItem{
			Number: number, URL: pageURL(filter, month, number), Current: number == filter.Page,
		})
	}
	data := struct {
		Items      []archive.ArticleSummary
		Sites      []siteItem
		Pages      []pageItem
		Total      int
		TotalPages int
		Page       int
		PageSize   int
		Query      string
		Site       string
		Month      string
		Category   string
		Feed       string
		From       string
		To         string
		HasPrev    bool
		HasNext    bool
		PrevURL    string
		NextURL    string
	}{
		Items: items, Sites: sites, Pages: pageNumbers, Total: total, TotalPages: totalPages,
		Page: filter.Page, PageSize: filter.PageSize,
		Query: filter.Query, Site: filter.SiteID, Month: month, Category: filter.Category,
		Feed: filter.FeedID, From: filter.From, To: filter.To, HasPrev: filter.Page > 1,
		HasNext: filter.Page*filter.PageSize < total,
		PrevURL: pageURL(filter, month, filter.Page-1), NextURL: pageURL(filter, month, filter.Page+1),
	}
	if err := indexTemplate.Execute(w, data); err != nil {
		return
	}
}

func (s *Server) handleMCPGuide(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/help/mcp" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	data := struct {
		Endpoint string
	}{Endpoint: requestBase(r) + "/mcp"}
	w.Header().Set("Cache-Control", "no-cache")
	if err := mcpGuideTemplate.Execute(w, data); err != nil {
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
	if item.Type != "" {
		meta = append(meta, "类型："+item.Type)
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
		"site_name": item.SiteName, "feed_id": item.FeedID, "category": item.Category, "type": item.Type,
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

// paginationNumbers returns a compact page range. Zero marks a visual gap.
func paginationNumbers(current, total, window int) []int {
	if total < 1 || window < 1 {
		return nil
	}
	if current < 1 {
		current = 1
	} else if current > total {
		current = total
	}
	if window > total {
		window = total
	}
	start := current - window/2
	if start < 1 {
		start = 1
	}
	end := start + window - 1
	if end > total {
		end = total
		start = end - window + 1
	}
	pages := make([]int, 0, window+4)
	if start > 1 {
		pages = append(pages, 1)
		if start > 2 {
			pages = append(pages, 0)
		}
	}
	for page := start; page <= end; page++ {
		pages = append(pages, page)
	}
	if end < total {
		if end < total-1 {
			pages = append(pages, 0)
		}
		pages = append(pages, total)
	}
	return pages
}

func pageURL(filter archive.ArticleFilter, month string, page int) string {
	values := url.Values{}
	values.Set("page", strconv.Itoa(max(page, 1)))
	if filter.Query != "" {
		values.Set("q", filter.Query)
	}
	if filter.SiteID != "" {
		values.Set("site", filter.SiteID)
	}
	if filter.Category != "" {
		values.Set("category", filter.Category)
	}
	if filter.FeedID != "" {
		values.Set("feed", filter.FeedID)
	}
	if month != "" {
		values.Set("month", month)
	} else {
		if filter.From != "" {
			values.Set("from", filter.From)
		}
		if filter.To != "" {
			values.Set("to", filter.To)
		}
	}
	if filter.PageSize != 20 {
		values.Set("page_size", strconv.Itoa(filter.PageSize))
	}
	return "/?" + values.Encode()
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

const (
	staticCacheControl    = "public, max-age=86400, s-maxage=31536000"
	staticCDNCacheControl = "public, max-age=31536000"
	apiCacheControl       = "public, max-age=0, s-maxage=120"
	apiCDNCacheControl    = "public, max-age=120"
	errorCacheControl     = "public, max-age=0, s-maxage=60"
	errorCDNCacheControl  = "public, max-age=60"
	noStoreCacheControl   = "no-store"
)

// cacheResponseWriter applies the cache policy when the final status is known.
// Several handlers set their own headers before writing, so applying the policy
// at WriteHeader keeps the policy consistent for both normal and error paths.
type cacheResponseWriter struct {
	http.ResponseWriter
	request     *http.Request
	wroteHeader bool
}

func (w *cacheResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *cacheResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	setResponseCacheHeaders(w.Header(), w.request, status)
	w.ResponseWriter.WriteHeader(status)
}

func (w *cacheResponseWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}

func withCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cw := &cacheResponseWriter{ResponseWriter: w, request: r}
		next.ServeHTTP(cw, r)
	})
}

func setResponseCacheHeaders(header http.Header, r *http.Request, status int) {
	cacheControl, cdnCacheControl := responseCachePolicy(r, status)
	header.Set("Cache-Control", cacheControl)
	header.Set("CDN-Cache-Control", cdnCacheControl)
}

func responseCachePolicy(r *http.Request, status int) (string, string) {
	// POST (including MCP) and all other write-like requests must not be
	// cached, even when they return an error response.
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return noStoreCacheControl, noStoreCacheControl
	}
	if status >= http.StatusBadRequest {
		return errorCacheControl, errorCDNCacheControl
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		return apiCacheControl, apiCDNCacheControl
	}
	if r.URL.Path == "/" ||
		r.URL.Path == "/help/mcp" ||
		strings.HasPrefix(r.URL.Path, "/article/") ||
		strings.HasPrefix(r.URL.Path, "/assets/") {
		return staticCacheControl, staticCDNCacheControl
	}
	return noStoreCacheControl, noStoreCacheControl
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, MCP-Protocol-Version, MCP-Session-Id, Mcp-Method, Mcp-Name")
		if r.Method == http.MethodOptions && r.URL.Path != "/mcp" {
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
.top-help{margin-left:auto;display:inline-flex;align-items:center;gap:6px;padding:6px 11px;border:1px solid #c9d9ef;border-radius:7px;background:#f5f9ff;font-size:12.5px;font-weight:600;white-space:nowrap}
.top-help:hover{border-color:var(--primary);text-decoration:none}
.nav-toggle-btn{display:none;padding:6px 14px;border:1px solid var(--line);border-radius:7px;background:var(--card);font-size:13px;cursor:pointer;user-select:none}
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
.pager{display:flex;justify-content:center;align-items:center;gap:6px;margin-top:18px;flex-wrap:wrap}
.pager a,.pager .current{min-width:36px;height:36px;padding:0 10px;border-radius:7px;background:var(--card);border:1px solid var(--line);font-size:13.5px;font-weight:500;display:inline-flex;align-items:center;justify-content:center}
.pager a:hover{border-color:var(--primary);color:var(--primary);text-decoration:none}
.pager .current{border-color:var(--primary);background:var(--primary);color:#fff}
.pager .ellipsis{min-width:24px;text-align:center;color:var(--muted)}
.pager .page-step{padding:0 14px}
.jump-form{display:flex;align-items:center;gap:6px;margin-left:10px;color:var(--muted);font-size:12.5px}
.jump-form input{width:62px;height:36px;padding:5px 7px;border:1px solid var(--line);border-radius:7px;background:var(--card);font:inherit;color:var(--text);text-align:center}
.jump-form input:focus{outline:none;border-color:var(--primary);box-shadow:0 0 0 3px #1a5fb41a}
.jump-form button{height:36px;padding:0 11px;border:1px solid var(--line);border-radius:7px;background:var(--card);color:var(--text);font:inherit;cursor:pointer}
.jump-form button:hover{border-color:var(--primary);color:var(--primary)}
@media(max-width:900px){
.layout{grid-template-columns:1fr;padding:12px}
.nav-toggle-btn{display:block}
.sidebar{display:none;position:static}
.nav-toggle:checked~.layout .sidebar{display:block}
.topbar .sub{display:none}
.site-list{max-height:none}
}
@media(max-width:560px){.topbar-inner{padding:0 12px;gap:8px}.top-help{padding:6px 8px}.top-help .help-label{display:none}.pager{gap:5px}.pager .page-step{padding:0 10px}.jump-form{width:100%;justify-content:center;margin:6px 0 0}}
</style></head><body>
<header class="topbar"><div class="topbar-inner"><div class="logo">交</div><h1>SWJTU 新闻归档</h1><span class="sub">西南交通大学校园新闻离线存档与检索</span><a class="top-help" href="/help/mcp" aria-label="MCP 配置指南" title="MCP 配置指南"><span aria-hidden="true">⌘</span><span class="help-label">MCP 配置指南</span></a><label class="nav-toggle-btn" for="nav-toggle">筛选 ☰</label></div></header>
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
{{if .TotalPages}}<nav class="pager" aria-label="文章分页">
{{if .HasPrev}}<a class="page-step" href="{{.PrevURL}}" rel="prev">← 上一页</a>{{end}}
{{range .Pages}}{{if .Ellipsis}}<span class="ellipsis" aria-hidden="true">…</span>{{else if .Current}}<span class="current" aria-current="page">{{.Number}}</span>{{else}}<a href="{{.URL}}" aria-label="第 {{.Number}} 页">{{.Number}}</a>{{end}}{{end}}
{{if .HasNext}}<a class="page-step" href="{{.NextURL}}" rel="next">下一页 →</a>{{end}}
<form class="jump-form" method="get"><span>跳至</span>
{{if .Query}}<input type="hidden" name="q" value="{{.Query}}">{{end}}{{if .Site}}<input type="hidden" name="site" value="{{.Site}}">{{end}}{{if .Month}}<input type="hidden" name="month" value="{{.Month}}">{{end}}{{if .Category}}<input type="hidden" name="category" value="{{.Category}}">{{end}}{{if .Feed}}<input type="hidden" name="feed" value="{{.Feed}}">{{end}}{{if not .Month}}{{if .From}}<input type="hidden" name="from" value="{{.From}}">{{end}}{{if .To}}<input type="hidden" name="to" value="{{.To}}">{{end}}{{end}}{{if ne .PageSize 20}}<input type="hidden" name="page_size" value="{{.PageSize}}">{{end}}
<input type="number" name="page" min="1" max="{{.TotalPages}}" value="{{.Page}}" aria-label="目标页码"><span>/ {{.TotalPages}} 页</span><button type="submit">跳转</button></form>
</nav>{{end}}
</main>
</div>
</body></html>`))

var mcpGuideTemplate = template.Must(template.New("mcp-guide").Parse(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>MCP 配置指南 - SWJTU 新闻归档</title><style>
:root{--primary:#1a5fb4;--primary-dark:#1c4a8c;--text:#1f2329;--muted:#6b7280;--line:#e5e7eb;--bg:#f4f5f7;--card:#fff;--code:#111827}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--text);font-family:system-ui,-apple-system,"PingFang SC","Microsoft YaHei","Noto Sans SC",sans-serif;font-size:15px;line-height:1.75}
a{color:var(--primary);text-decoration:none}a:hover{text-decoration:underline}
.topbar{background:var(--card);border-bottom:1px solid var(--line);position:sticky;top:0;z-index:10}
.topbar-inner{max-width:920px;height:56px;margin:0 auto;padding:0 20px;display:flex;align-items:center;gap:14px}
.back{padding:6px 12px;border:1px solid var(--line);border-radius:7px;color:var(--text);font-size:13px}.back:hover{border-color:var(--primary);color:var(--primary);text-decoration:none}
.topbar-title{font-size:15px;font-weight:600}
main{max-width:920px;margin:0 auto;padding:24px 20px 64px}
.hero,.section{background:var(--card);border:1px solid var(--line);border-radius:11px;padding:28px 32px;margin-bottom:18px}
.hero h1{font-size:26px;line-height:1.35;margin:0 0 8px}.hero p{margin:0;color:var(--muted)}
h2{font-size:18px;margin:0 0 14px}h3{font-size:15px;margin:22px 0 8px}
.endpoint{display:flex;align-items:center;gap:12px;margin-top:20px;padding:14px 16px;border:1px solid #bdd2ed;border-radius:9px;background:#f3f8ff}
.endpoint strong{font-size:12px;color:var(--primary);text-transform:uppercase;letter-spacing:.7px;flex-shrink:0}.endpoint code{overflow-wrap:anywhere}
ol{padding-left:22px;margin:0}li+li{margin-top:7px}
pre{margin:12px 0 0;padding:18px;border-radius:9px;background:var(--code);color:#e5e7eb;overflow:auto;font:13px/1.65 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
code{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
.note{margin:14px 0 0;padding:11px 14px;border-left:3px solid var(--primary);background:#f8fafc;color:var(--muted);font-size:13px}
.tools{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:10px;list-style:none;margin:0;padding:0}
.tools li{padding:12px 14px;border:1px solid var(--line);border-radius:8px}.tools code{display:block;color:var(--primary);font-weight:700;font-size:13px}.tools span{display:block;color:var(--muted);font-size:13px;margin-top:3px}
.status{display:inline-flex;align-items:center;gap:7px;padding:4px 9px;border-radius:99px;background:#ecfdf3;color:#18794e;font-size:12px;font-weight:600}.status::before{content:"";width:7px;height:7px;border-radius:50%;background:#22a06b}
@media(max-width:640px){main{padding:14px 12px 42px}.topbar-inner{padding:0 12px}.hero,.section{padding:22px 18px}.hero h1{font-size:22px}.endpoint{align-items:flex-start;flex-direction:column;gap:4px}.tools{grid-template-columns:1fr}}
</style></head><body>
<header class="topbar"><div class="topbar-inner"><a class="back" href="/">← 返回列表</a><span class="topbar-title">MCP 配置指南</span></div></header>
<main>
<section class="hero"><span class="status">只读服务</span><h1>连接 SWJTU 新闻归档</h1><p>通过 MCP 搜索校园新闻、读取正文，并获取归档图片和附件。</p><div class="endpoint"><strong>服务地址</strong><code>{{.Endpoint}}</code></div></section>
<section class="section"><h2>配置步骤</h2><ol><li>在 MCP 客户端中新建一个远程服务器。</li><li>传输方式选择 <strong>Streamable HTTP</strong>，名称可填写 <code>swjtu-archive</code>。</li><li>将上面的服务地址粘贴到 URL；本服务无需令牌或其他认证。</li><li>保存并连接，看到下方工具列表即表示配置成功。</li></ol>
<h3>通用配置参考</h3><pre><code>{
  "mcpServers": {
    "swjtu-archive": {
      "type": "streamable-http",
      "url": "{{.Endpoint}}"
    }
  }
}</code></pre><p class="note">不同客户端的字段名可能略有不同；如果客户端提供图形配置界面，直接填写名称、传输方式和 URL 即可。</p></section>
<section class="section"><h2>可用工具</h2><ul class="tools"><li><code>list_feeds</code><span>查看新闻来源和筛选 ID</span></li><li><code>query_articles</code><span>按来源、类别和日期浏览文章</span></li><li><code>search_articles</code><span>全文搜索标题、正文和来源</span></li><li><code>get_article</code><span>读取正文、元数据和资源列表</span></li><li><code>download_resource</code><span>获取图片或附件下载地址</span></li></ul></section>
<section class="section"><h2>使用建议</h2><p>可以先让客户端调用 <code>search_articles</code> 搜索关键词，再用 <code>get_article</code> 打开结果。需要保存附件时，调用 <code>download_resource</code> 获取归档下载地址。</p><p class="note">若连接失败，请确认 URL 以 <code>/mcp</code> 结尾，并检查客户端是否支持 Streamable HTTP。直接在浏览器中打开 MCP 地址会返回方法不允许，这是正常现象。</p></section>
</main></body></html>`))

var articleTemplate = template.Must(template.New("article").Parse(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Item.Title}} - SWJTU 新闻归档</title><style>
:root{--primary:#1a5fb4;--primary-dark:#1c4a8c;--text:#1f2329;--muted:#6b7280;--line:#e5e7eb;--bg:#f4f5f7;--card:#fff}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--text);font-family:system-ui,-apple-system,"PingFang SC","Microsoft YaHei","Noto Sans SC",sans-serif;font-size:16px;line-height:1.85}
a{color:var(--primary);text-decoration:none}
a:hover{text-decoration:underline}
.reading-bar{background:var(--card);border-bottom:1px solid var(--line);position:sticky;top:0;z-index:20;box-shadow:0 2px 8px rgba(31,35,41,.04)}
.bar-inner{max-width:1180px;margin:0 auto;padding:0 20px}
.bar-main{height:56px;display:flex;align-items:center;gap:12px;min-width:0}
.back{padding:6px 12px;border:1px solid var(--line);border-radius:7px;font-size:13px;color:var(--text);white-space:nowrap;flex-shrink:0}
.back:hover{border-color:var(--primary);color:var(--primary);text-decoration:none}
.bar-source,.bar-time{color:var(--muted);font-size:12.5px;white-space:nowrap;flex-shrink:0}
.bar-source{max-width:190px;overflow:hidden;text-overflow:ellipsis}
.bar-title{min-width:0;flex:1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:14px;font-weight:600;color:var(--text)}
.bar-divider{width:1px;height:18px;background:var(--line);flex-shrink:0}
.bar-second{display:none;height:38px;border-top:1px solid #f0f1f3;align-items:center;gap:8px;min-width:0;font-size:13px}
.bar-second-label{color:var(--muted);flex-shrink:0}
.bar-second-title{min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-weight:600}
.original-button{margin-left:auto;display:inline-flex;align-items:center;justify-content:center;padding:7px 13px;border-radius:7px;background:var(--primary);color:#fff;font-size:13px;font-weight:600;white-space:nowrap;flex-shrink:0}
.original-button:hover{background:var(--primary-dark);color:#fff;text-decoration:none}
.article-layout{max-width:1100px;margin:0 auto;padding:0 20px 48px}
.article-side{display:none}
.paper{background:var(--card);border:1px solid var(--line);border-radius:10px;padding:40px 48px;margin-top:20px;min-width:0}
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
.side-back{display:flex;margin-bottom:12px}
.side-card{background:var(--card);border:1px solid var(--line);border-radius:10px;padding:20px}
.side-info{display:flex;flex-direction:column;gap:16px}
.side-field{min-width:0}
.side-label{display:block;color:var(--muted);font-size:11px;font-weight:700;letter-spacing:.7px;margin-bottom:3px}
.side-value{display:block;font-size:13px;line-height:1.6;overflow-wrap:anywhere}
.side-title{font-size:15px;font-weight:650;line-height:1.55;display:-webkit-box;-webkit-box-orient:vertical;-webkit-line-clamp:4;overflow:hidden}
.side-card .original-button{width:100%;margin-top:18px}
.side-resources{margin-top:14px;padding-top:16px;border-top:1px solid var(--line)}
.side-resources h2{font-size:13px;margin:0 0 9px}
.side-resources ul{list-style:none;margin:0;padding:0;display:flex;flex-direction:column;gap:7px}
.side-resources a{display:flex;min-width:0;align-items:center;gap:7px;padding:8px 9px;border-radius:7px;background:#f7f8fa;color:var(--text);font-size:12.5px}
.side-resources a:hover{background:#edf3fb;color:var(--primary);text-decoration:none}
.attachment-name{min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.attachment-size{color:var(--muted);font-size:11px;flex-shrink:0;margin-left:auto}
@media (orientation:portrait), (max-width:760px){
.bar-inner{padding:0 14px}.bar-main{height:48px;gap:9px}.bar-main .bar-title,.bar-divider{display:none}.bar-source{max-width:150px;flex-shrink:1}.bar-second{display:flex}.original-button{padding:6px 11px}.article-layout{padding:0 12px 38px}.paper{padding:26px 20px;margin-top:12px}h1{font-size:20px}
}
@media(max-width:480px){.back-label{display:none}.back{width:36px;padding:6px 0;text-align:center}.bar-source{max-width:82px}.bar-time{overflow:hidden;max-width:88px;text-overflow:ellipsis}.bar-main{gap:7px}}
@media(min-width:1100px) and (orientation:landscape){
.reading-bar{display:none}.article-layout{max-width:1340px;padding:24px;display:grid;grid-template-columns:270px minmax(0,1fr);gap:24px;align-items:start}.article-side{display:block;position:sticky;top:24px;min-width:0}.paper{margin-top:0;padding:42px 52px}.resources-inline{display:none}
}
</style></head><body>
<header class="reading-bar"><div class="bar-inner"><div class="bar-main"><a class="back" href="/"><span aria-hidden="true">←</span> <span class="back-label">返回列表</span></a><span class="bar-source" title="{{if .Item.Source}}{{.Item.Source}}{{else}}{{.Item.SiteName}}{{end}}">来源：{{if .Item.Source}}{{.Item.Source}}{{else}}{{.Item.SiteName}}{{end}}</span><span class="bar-time">时间：{{if .Item.PublishedAt}}{{.Item.PublishedAt}}{{else}}未标注{{end}}</span><span class="bar-divider" aria-hidden="true"></span><span class="bar-title" title="{{.Item.Title}}">{{.Item.Title}}</span>{{if .Item.CanonicalURL}}<a class="original-button" href="{{.Item.CanonicalURL}}" target="_blank" rel="noopener">阅读原文 ↗</a>{{end}}</div><div class="bar-second"><span class="bar-second-label">标题</span><span class="bar-second-title" title="{{.Item.Title}}">{{.Item.Title}}</span></div></div></header>
<div class="article-layout">
<aside class="article-side" aria-label="文章信息"><a class="back side-back" href="/">← 返回列表</a><div class="side-card"><div class="side-info"><div class="side-field"><span class="side-label">来源</span><span class="side-value">{{if .Item.Source}}{{.Item.Source}}{{else}}{{.Item.SiteName}}{{end}}</span></div><div class="side-field"><span class="side-label">时间</span><span class="side-value">{{if .Item.PublishedAt}}{{.Item.PublishedAt}}{{else}}未标注{{end}}</span></div><div class="side-field"><span class="side-label">标题</span><span class="side-value side-title" title="{{.Item.Title}}">{{.Item.Title}}</span></div></div>{{if .Item.CanonicalURL}}<a class="original-button" href="{{.Item.CanonicalURL}}" target="_blank" rel="noopener">阅读原文 ↗</a>{{end}}{{if .Attachments}}<section class="side-resources"><h2>附件（{{len .Attachments}}）</h2><ul>{{range .Attachments}}<li><a href="/assets/{{.ID}}" download="{{.Filename}}" title="{{.Filename}}"><span aria-hidden="true">↓</span><span class="attachment-name">{{.Filename}}</span><span class="attachment-size">{{.ByteSize}} B</span></a></li>{{end}}</ul></section>{{end}}</div></aside>
<main class="paper">
<h1>{{.Item.Title}}</h1>
<div class="meta"><span class="badge">{{.Item.SiteName}}</span><span>{{.Item.PublishedAt}}</span>{{if .Item.Type}}<span class="dot"></span><span>类型：{{.Item.Type}}</span>{{end}}{{if .Item.Source}}<span class="dot"></span><span>来源：{{.Item.Source}}</span>{{end}}{{if .Item.Author}}<span class="dot"></span><span>作者：{{.Item.Author}}</span>{{end}}{{if .Item.Photographer}}<span class="dot"></span><span>摄影：{{.Item.Photographer}}</span>{{end}}{{if .Item.Editor}}<span class="dot"></span><span>编辑：{{.Item.Editor}}</span>{{end}}</div>
<div class="content">{{.ContentHTML}}</div>
{{if .Attachments}}<section class="resources resources-inline"><h2>附件</h2><ul>{{range .Attachments}}<li><a href="/assets/{{.ID}}" download="{{.Filename}}">{{.Filename}}<span class="size">{{.ByteSize}} bytes</span></a></li>{{end}}</ul></section>{{end}}
</main></div>
</body></html>`))
