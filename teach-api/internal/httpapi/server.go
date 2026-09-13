// Package httpapi exposes the public, read-only teaching directory API.
package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"teach-api/internal/store"
)

const publicCacheControl = "public, max-age=60, s-maxage=300, stale-while-revalidate=86400"

// CDN-Cache-Control 单独给边缘缓存(Cloudflare)的 TTL,与浏览器缓存时长解耦,
// 和 swjtu-archive 的缓存分层方式保持一致。
const publicCDNCacheControl = "public, max-age=300, stale-while-revalidate=86400"

type Options struct {
	AllowedOrigins []string
	Logger         *log.Logger
}

type Server struct {
	store          *store.Store
	allowedOrigins []string
	logger         *log.Logger
	mux            *http.ServeMux
}

func New(s *store.Store, options Options) *Server {
	logger := options.Logger
	if logger == nil {
		logger = log.Default()
	}
	server := &Server{
		store:          s,
		allowedOrigins: normalizeOrigins(options.AllowedOrigins),
		logger:         logger,
		mux:            http.NewServeMux(),
	}
	server.mux.HandleFunc("/api/v1/healthz", server.health)
	server.mux.HandleFunc("/api/v1/meta", server.meta)
	server.mux.HandleFunc("/api/v1/teachers", server.listTeachers)
	server.mux.HandleFunc("/api/v1/teachers/", server.getTeacher)
	server.mux.HandleFunc("/api/v1/courses", server.listCourses)
	server.mux.HandleFunc("/api/v1/courses/", server.getCourse)
	server.mux.HandleFunc("/api/v1/huoshui/meta", server.huoshuiMeta)
	server.mux.HandleFunc("/api/v1/huoshui/courses", server.listHuoshuiCourses)
	server.mux.HandleFunc("/api/v1/huoshui/courses/", server.getHuoshuiCourse)
	server.mux.Handle("/mcp", newMCPHandler(s))
	return server
}

func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.setCommonHeaders(w, r)
		if r.Method == http.MethodOptions {
			if !s.originAllowed(r.Header.Get("Origin")) {
				writeError(w, http.StatusForbidden, "origin_not_allowed")
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" && !s.originAllowed(origin) {
			writeError(w, http.StatusForbidden, "origin_not_allowed")
			return
		}
		meta := s.store.Meta()
		dataVersion := meta.Version
		if meta.CoursesVersion != "" {
			dataVersion += "+" + meta.CoursesVersion
		}
		if meta.HuoshuiVersion != "" {
			dataVersion += "+" + meta.HuoshuiVersion
		}
		lastModified := ""
		if importedAt, err := time.Parse(time.RFC3339, meta.ImportedAt); err == nil {
			lastModified = importedAt.UTC().Format(http.TimeFormat)
		}
		requestContext := context.WithValue(r.Context(), dataVersionContextKey{}, dataVersion)
		requestContext = context.WithValue(requestContext, lastModifiedContextKey{}, lastModified)
		s.mux.ServeHTTP(newServerTimingWriter(w, time.Now()), r.WithContext(requestContext))
	})
}

// serverTimingWriter 在首个 WriteHeader 之前注入 Server-Timing,把
// "从收到请求到开始写响应"的服务端耗时暴露给前端(性能面板读取)。
type serverTimingWriter struct {
	http.ResponseWriter
	started time.Time
	wrote   bool
}

func newServerTimingWriter(w http.ResponseWriter, started time.Time) *serverTimingWriter {
	return &serverTimingWriter{ResponseWriter: w, started: started}
}

func (w *serverTimingWriter) WriteHeader(status int) {
	if !w.wrote {
		w.wrote = true
		elapsed := float64(time.Since(w.started)) / float64(time.Millisecond)
		w.Header().Set("Server-Timing", fmt.Sprintf("app;dur=%.2f", elapsed))
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *serverTimingWriter) Write(contents []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(contents)
}

func (s *Server) setCommonHeaders(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
		if allowed := s.allowedOrigin(origin); allowed != "" {
			w.Header().Set("Access-Control-Allow-Origin", allowed)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Accept, Content-Type, If-None-Match, Mcp-Protocol-Version")
			w.Header().Set("Access-Control-Max-Age", "600")
			// 跨源部署时也要让浏览器 JS 读得到缓存与计时相关响应头(性能面板)。
			w.Header().Set("Access-Control-Expose-Headers", "Age, Cache-Control, CDN-Cache-Control, CF-Cache-Status, CF-Ray, Date, ETag, Last-Modified, Server-Timing")
			w.Header().Add("Vary", "Origin")
		}
	}
}

func normalizeOrigins(origins []string) []string {
	if len(origins) == 0 {
		return []string{"*"}
	}
	result := make([]string, 0, len(origins))
	for _, origin := range origins {
		origin = strings.TrimSpace(origin)
		if origin == "" {
			continue
		}
		result = append(result, origin)
	}
	if len(result) == 0 {
		return []string{"*"}
	}
	return result
}

func (s *Server) allowedOrigin(origin string) string {
	origin = strings.TrimSpace(origin)
	if origin == "" {
		return ""
	}
	for _, allowed := range s.allowedOrigins {
		if allowed == "*" {
			return "*"
		}
		if allowed == origin {
			return origin
		}
	}
	return ""
}

func (s *Server) originAllowed(origin string) bool {
	return strings.TrimSpace(origin) == "" || s.allowedOrigin(origin) != ""
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	meta := s.store.Meta()
	writeJSON(w, r, map[string]any{
		"status":         "ok",
		"dataset_loaded": s.store.HasDataset(),
		"data_version":   meta.Version,
	}, "no-store")
}

func (s *Server) meta(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	writeJSON(w, r, map[string]any{"data": s.store.Meta()}, publicCacheControl)
}

func (s *Server) listTeachers(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	filter, err := parseTeacherFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	page, err := s.store.ListTeachers(r.Context(), filter)
	if err != nil {
		s.logger.Printf("list teachers: %v", err)
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	writeJSON(w, r, page, publicCacheControl)
}

func (s *Server) getTeacher(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	remainder, err := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/api/v1/teachers/"))
	if err != nil || remainder == "" {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if strings.HasSuffix(remainder, "/courses") {
		s.listTeacherCourses(w, r, strings.TrimSuffix(remainder, "/courses"))
		return
	}
	id := remainder
	if id == "" || strings.Contains(id, "/") || len(id) > 128 {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	teacher, err := s.store.GetTeacher(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		s.logger.Printf("get teacher %q: %v", id, err)
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	writeJSON(w, r, map[string]any{"data": teacher}, publicCacheControl)
}

func (s *Server) listTeacherCourses(w http.ResponseWriter, r *http.Request, id string) {
	if id == "" || strings.Contains(id, "/") || len(id) > 128 {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	courses, err := s.store.ListTeacherCourses(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		s.logger.Printf("list teacher courses %q: %v", id, err)
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	writeJSON(w, r, map[string]any{"data": courses}, publicCacheControl)
}

func (s *Server) listCourses(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	filter, err := parseCourseFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	page, err := s.store.ListCourses(r.Context(), filter)
	if err != nil {
		s.logger.Printf("list courses: %v", err)
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	writeJSON(w, r, page, publicCacheControl)
}

func (s *Server) getCourse(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	remainder := strings.TrimPrefix(r.URL.Path, "/api/v1/courses/")
	if remainder == "categories" {
		s.listCourseCategories(w, r)
		return
	}
	if remainder == "terms" {
		s.listCourseTerms(w, r)
		return
	}
	if strings.HasPrefix(remainder, "code/") {
		s.getCourseByCode(w, r, strings.TrimPrefix(remainder, "code/"))
		return
	}
	id, err := url.PathUnescape(remainder)
	if err != nil || id == "" || strings.Contains(id, "/") || len(id) > 128 {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	course, err := s.store.GetCourse(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		s.logger.Printf("get course %q: %v", id, err)
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	writeJSON(w, r, map[string]any{"data": course}, publicCacheControl)
}

// getCourseByCode 以课程代码为键返回同一门课的全部教学班(含历史学期),
// 课程代码全局稳定,课程页与对外链接都以它为准。
func (s *Server) getCourseByCode(w http.ResponseWriter, r *http.Request, rawCode string) {
	code, err := url.PathUnescape(rawCode)
	if err != nil || code == "" || strings.Contains(code, "/") || len(code) > 64 {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	group, err := s.store.ListCourseClassesByCode(r.Context(), code)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		s.logger.Printf("get course by code %q: %v", code, err)
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	writeJSON(w, r, map[string]any{"data": group}, publicCacheControl)
}

func (s *Server) listCourseCategories(w http.ResponseWriter, r *http.Request) {
	categories, err := s.store.ListCourseCategories(r.Context())
	if err != nil {
		s.logger.Printf("list course categories: %v", err)
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	writeJSON(w, r, map[string]any{"data": categories}, publicCacheControl)
}

func (s *Server) listCourseTerms(w http.ResponseWriter, r *http.Request) {
	terms, err := s.store.ListCourseTerms(r.Context())
	if err != nil {
		s.logger.Printf("list course terms: %v", err)
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	writeJSON(w, r, map[string]any{"data": terms}, publicCacheControl)
}

func (s *Server) huoshuiMeta(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	meta, err := s.store.ListHuoshuiMeta(r.Context())
	if err != nil {
		s.logger.Printf("huoshui meta: %v", err)
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	writeJSON(w, r, map[string]any{"data": meta}, publicCacheControl)
}

func (s *Server) listHuoshuiCourses(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	filter, err := parseHuoshuiFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	page, err := s.store.ListHuoshuiCourses(r.Context(), filter)
	if err != nil {
		s.logger.Printf("list huoshui courses: %v", err)
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	writeJSON(w, r, page, publicCacheControl)
}

func (s *Server) getHuoshuiCourse(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	id, err := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/api/v1/huoshui/courses/"))
	if err != nil || id == "" || strings.Contains(id, "/") || len(id) > 128 {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	course, err := s.store.GetHuoshuiCourse(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		s.logger.Printf("get huoshui course %q: %v", id, err)
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	writeJSON(w, r, map[string]any{"data": course}, publicCacheControl)
}

func getOnly(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet {
		return true
	}
	w.Header().Set("Allow", "GET, OPTIONS")
	writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
	return false
}

func parseTeacherFilter(r *http.Request) (store.TeacherFilter, error) {
	query := r.URL.Query()
	page, err := parseQueryInt(query.Get("page"), 1, 100000, 1)
	if err != nil {
		return store.TeacherFilter{}, fmt.Errorf("invalid_page")
	}
	pageSize, err := parseQueryInt(query.Get("page_size"), 1, 100, 24)
	if err != nil {
		return store.TeacherFilter{}, fmt.Errorf("invalid_page_size")
	}
	search := strings.TrimSpace(query.Get("search"))
	if len([]rune(search)) > 100 {
		return store.TeacherFilter{}, fmt.Errorf("search_too_long")
	}
	initial := strings.TrimSpace(query.Get("initial"))
	if len([]rune(initial)) > 2 {
		return store.TeacherFilter{}, fmt.Errorf("invalid_initial")
	}
	college := strings.TrimSpace(query.Get("college"))
	if len([]rune(college)) > 100 {
		return store.TeacherFilter{}, fmt.Errorf("college_too_long")
	}
	return store.TeacherFilter{
		Page: page, PageSize: pageSize, Search: search, Initial: initial, College: college,
	}, nil
}

func parseCourseFilter(r *http.Request) (store.CourseFilter, error) {
	query := r.URL.Query()
	page, err := parseQueryInt(query.Get("page"), 1, 100000, 1)
	if err != nil {
		return store.CourseFilter{}, fmt.Errorf("invalid_page")
	}
	pageSize, err := parseQueryInt(query.Get("page_size"), 1, 100, 24)
	if err != nil {
		return store.CourseFilter{}, fmt.Errorf("invalid_page_size")
	}
	search := strings.TrimSpace(query.Get("search"))
	if len([]rune(search)) > 100 {
		return store.CourseFilter{}, fmt.Errorf("search_too_long")
	}
	campus := strings.TrimSpace(query.Get("campus"))
	if len([]rune(campus)) > 20 {
		return store.CourseFilter{}, fmt.Errorf("invalid_campus")
	}
	weekday := strings.TrimSpace(query.Get("weekday"))
	if len([]rune(weekday)) > 10 {
		return store.CourseFilter{}, fmt.Errorf("invalid_weekday")
	}
	college := strings.TrimSpace(query.Get("college"))
	if len([]rune(college)) > 100 {
		return store.CourseFilter{}, fmt.Errorf("college_too_long")
	}
	category := strings.TrimSpace(query.Get("category"))
	if len([]rune(category)) > 50 {
		return store.CourseFilter{}, fmt.Errorf("category_too_long")
	}
	term := strings.TrimSpace(query.Get("term"))
	if len([]rune(term)) > 30 {
		return store.CourseFilter{}, fmt.Errorf("term_too_long")
	}
	return store.CourseFilter{
		Page: page, PageSize: pageSize, Search: search, Campus: campus, Weekday: weekday, College: college, Category: category, Term: term,
	}, nil
}

func parseHuoshuiFilter(r *http.Request) (store.HuoshuiFilter, error) {
	query := r.URL.Query()
	page, err := parseQueryInt(query.Get("page"), 1, 100000, 1)
	if err != nil {
		return store.HuoshuiFilter{}, fmt.Errorf("invalid_page")
	}
	pageSize, err := parseQueryInt(query.Get("page_size"), 1, 100, 50)
	if err != nil {
		return store.HuoshuiFilter{}, fmt.Errorf("invalid_page_size")
	}
	search := strings.TrimSpace(query.Get("search"))
	if len([]rune(search)) > 100 {
		return store.HuoshuiFilter{}, fmt.Errorf("search_too_long")
	}
	dept := strings.TrimSpace(query.Get("dept"))
	if len([]rune(dept)) > 100 {
		return store.HuoshuiFilter{}, fmt.Errorf("dept_too_long")
	}
	sort := strings.TrimSpace(query.Get("sort"))
	switch sort {
	case "", "reviews", "rating", "rate3":
	default:
		return store.HuoshuiFilter{}, fmt.Errorf("invalid_sort")
	}
	return store.HuoshuiFilter{
		Page: page, PageSize: pageSize, Search: search, Dept: dept, Sort: sort,
	}, nil
}

func parseQueryInt(value string, minimum, maximum, fallback int) (int, error) {
	if strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, errors.New("invalid integer")
	}
	return parsed, nil
}

// instanceTag 把 ETag 绑定到当前进程:重新部署后序列化可能变化,
// 若只按数据集版本算 ETag,边缘缓存用 If-None-Match 回源会拿到 304,
// 旧结构的响应体会被一直续期。
var instanceTag = time.Now().UTC().Format("20060102150405")

func writeJSON(w http.ResponseWriter, r *http.Request, value any, cacheControl string) {
	contents, err := json.Marshal(value)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	metaVersion := "empty"
	if requestStore := requestDataVersion(r); requestStore != "" {
		metaVersion = requestStore
	}
	digest := sha256.Sum256([]byte(metaVersion + "\x00" + instanceTag + "\x00" + r.URL.RequestURI()))
	etag := `"` + hex.EncodeToString(digest[:12]) + `"`
	w.Header().Set("Cache-Control", cacheControl)
	if cacheControl == publicCacheControl {
		w.Header().Set("CDN-Cache-Control", publicCDNCacheControl)
	}
	w.Header().Set("ETag", etag)
	if lastModified := requestLastModified(r); lastModified != "" {
		w.Header().Set("Last-Modified", lastModified)
	}
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(contents)
}

// The request context carries the current dataset metadata so every response
// gets a stable validator until the next import.
func requestDataVersion(r *http.Request) string {
	if value := r.Context().Value(dataVersionContextKey{}); value != nil {
		if version, ok := value.(string); ok {
			return version
		}
	}
	return ""
}

func requestLastModified(r *http.Request) string {
	if value := r.Context().Value(lastModifiedContextKey{}); value != nil {
		if lastModified, ok := value.(string); ok {
			return lastModified
		}
	}
	return ""
}

type dataVersionContextKey struct{}
type lastModifiedContextKey struct{}

func etagMatches(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || candidate == etag || strings.TrimPrefix(candidate, "W/") == strings.TrimPrefix(etag, "W/") {
			return true
		}
	}
	return false
}

func writeError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("CDN-Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"error":%q}`, code)
}
