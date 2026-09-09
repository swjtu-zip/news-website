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
		s.mux.ServeHTTP(w, r.WithContext(requestContext))
	})
}

func (s *Server) setCommonHeaders(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
		if allowed := s.allowedOrigin(origin); allowed != "" {
			w.Header().Set("Access-Control-Allow-Origin", allowed)
			w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Accept, Content-Type, If-None-Match")
			w.Header().Set("Access-Control-Max-Age", "600")
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
	id, err := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/api/v1/courses/"))
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
	return store.CourseFilter{
		Page: page, PageSize: pageSize, Search: search, Campus: campus, Weekday: weekday, College: college,
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
	digest := sha256.Sum256([]byte(metaVersion + "\x00" + r.URL.RequestURI()))
	etag := `"` + hex.EncodeToString(digest[:12]) + `"`
	w.Header().Set("Cache-Control", cacheControl)
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
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"error":%q}`, code)
}
