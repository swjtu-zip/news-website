package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"teach-api/internal/store"
)

func TestPublicTeacherAPIUsesCacheValidators(t *testing.T) {
	database, err := store.Open(t.TempDir() + "/teach.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.ImportBytes(context.Background(), []byte(`{
"generated_at":"2026-09-07T14:32:03Z",
"source":{"site":"faculty.swjtu.edu.cn","base_url":"https://faculty.swjtu.edu.cn"},
"stats":{"list_pages":3},
"teachers":[
  {"name":"张三","profile_url":"https://faculty.swjtu.edu.cn/a/zh_cn/zhangsan.html","initial":"z","college":"计算机学院","introduction":"简介 email zhang@example.edu.cn","status":"ok"},
  {"name":"李四","profile_url":"https://faculty.swjtu.edu.cn/a/zh_cn/lisi.html","initial":"l","college":"数学学院","status":"ok"}
]}`)); err != nil {
		t.Fatal(err)
	}
	handler := New(database, Options{
		AllowedOrigins: []string{"https://web.example"},
		Logger:         log.New(&bytes.Buffer{}, "", 0),
	}).Handler()

	request := httptest.NewRequest(http.MethodGet, "/api/v1/teachers?page_size=1&search=%E5%BC%A0", nil)
	request.Header.Set("Origin", "https://web.example")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("list status = %d, body=%s", response.Code, response.Body.String())
	}
	if response.Header().Get("Access-Control-Allow-Origin") != "https://web.example" {
		t.Fatalf("missing CORS header: %v", response.Header())
	}
	if response.Header().Get("Cache-Control") != publicCacheControl {
		t.Fatalf("unexpected cache policy: %q", response.Header().Get("Cache-Control"))
	}
	etag := response.Header().Get("ETag")
	if etag == "" || response.Header().Get("Last-Modified") == "" {
		t.Fatalf("missing validators: %v", response.Header())
	}
	var page store.TeacherPage
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Pagination.Total != 1 || len(page.Data) != 1 || page.Data[0].Name != "张三" {
		t.Fatalf("unexpected list: %+v", page)
	}
	if bytes.Contains(response.Body.Bytes(), []byte("@")) {
		t.Fatalf("contact data was exposed: %s", response.Body.String())
	}

	conditional := httptest.NewRequest(http.MethodGet, "/api/v1/teachers?page_size=1&search=%E5%BC%A0", nil)
	conditional.Header.Set("If-None-Match", etag)
	conditionalResponse := httptest.NewRecorder()
	handler.ServeHTTP(conditionalResponse, conditional)
	if conditionalResponse.Code != http.StatusNotModified || conditionalResponse.Body.Len() != 0 {
		t.Fatalf("expected empty 304, got %d %q", conditionalResponse.Code, conditionalResponse.Body.String())
	}

	detailRequest := httptest.NewRequest(http.MethodGet, "/api/v1/teachers/"+page.Data[0].ID, nil)
	detailResponse := httptest.NewRecorder()
	handler.ServeHTTP(detailResponse, detailRequest)
	if detailResponse.Code != http.StatusOK || !strings.Contains(detailResponse.Body.String(), "张三") {
		t.Fatalf("unexpected detail: %d %s", detailResponse.Code, detailResponse.Body.String())
	}

	options := httptest.NewRequest(http.MethodOptions, "/api/v1/teachers", nil)
	options.Header.Set("Origin", "https://web.example")
	optionsResponse := httptest.NewRecorder()
	handler.ServeHTTP(optionsResponse, options)
	if optionsResponse.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS status = %d", optionsResponse.Code)
	}

	method := httptest.NewRequest(http.MethodPost, "/api/v1/teachers", nil)
	methodResponse := httptest.NewRecorder()
	handler.ServeHTTP(methodResponse, method)
	if methodResponse.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d", methodResponse.Code)
	}

	notFound := httptest.NewRequest(http.MethodGet, "/api/v1/teachers/missing", nil)
	notFoundResponse := httptest.NewRecorder()
	handler.ServeHTTP(notFoundResponse, notFound)
	if notFoundResponse.Code != http.StatusNotFound {
		t.Fatalf("missing detail status = %d", notFoundResponse.Code)
	}
}

func TestDisallowedOriginIsRejected(t *testing.T) {
	database, err := store.Open(t.TempDir() + "/teach.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	handler := New(database, Options{AllowedOrigins: []string{"https://web.example"}}).Handler()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil)
	request.Header.Set("Origin", "https://bad.example")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
}

func TestPublicCoursesAPI(t *testing.T) {
	database, err := store.Open(t.TempDir() + "/teach.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.ImportBytes(context.Background(), []byte(`{
"generated_at":"2026-09-07T14:32:03Z",
"source":{"site":"faculty.swjtu.edu.cn"},
"teachers":[{"name":"杨爱华","profile_url":"https://faculty.swjtu.edu.cn/a/zh_cn/yangaihua.html","status":"ok"}]
}`)); err != nil {
		t.Fatal(err)
	}
	if err := database.ImportCoursesBytes(context.Background(), []byte(`{
"schema_version":1,
"term":"2026-2027第1学期",
"courses":[
  {"id":"B3919","term":"2026-2027第1学期","college":"本科生院","course_code":"CFGE000114","course_name":"女性成长","teacher_name":"杨爱华","credit":2,"weekday":"星期三","periods":"11-12","campus":"犀浦","nature":"选修"},
  {"id":"B0001","term":"2026-2027第1学期","college":"数学学院","course_code":"MATH000001","course_name":"数学漫谈","teacher_name":"目录外老师","credit":1,"weekday":"星期一","periods":"1-2","campus":"九里","nature":"选修"}
]}`)); err != nil {
		t.Fatal(err)
	}
	handler := New(database, Options{Logger: log.New(&bytes.Buffer{}, "", 0)}).Handler()

	listRequest := httptest.NewRequest(http.MethodGet, "/api/v1/courses?campus=%E7%8A%80%E6%B5%A6", nil)
	listResponse := httptest.NewRecorder()
	handler.ServeHTTP(listResponse, listRequest)
	if listResponse.Code != http.StatusOK {
		t.Fatalf("list status = %d, body=%s", listResponse.Code, listResponse.Body.String())
	}
	var page store.CoursePage
	if err := json.Unmarshal(listResponse.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Pagination.Total != 1 || page.Data[0].ID != "B3919" || page.Data[0].TeacherID == "" {
		t.Fatalf("unexpected course list: %+v", page)
	}

	detailRequest := httptest.NewRequest(http.MethodGet, "/api/v1/courses/B0001", nil)
	detailResponse := httptest.NewRecorder()
	handler.ServeHTTP(detailResponse, detailRequest)
	if detailResponse.Code != http.StatusOK {
		t.Fatalf("detail status = %d", detailResponse.Code)
	}
	var detail struct {
		Data store.Course `json:"data"`
	}
	if err := json.Unmarshal(detailResponse.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Data.CourseName != "数学漫谈" || detail.Data.TeacherID != "" {
		t.Fatalf("unexpected course detail: %+v", detail.Data)
	}

	teacherID := page.Data[0].TeacherID
	byTeacher := httptest.NewRequest(http.MethodGet, "/api/v1/teachers/"+teacherID+"/courses", nil)
	byTeacherResponse := httptest.NewRecorder()
	handler.ServeHTTP(byTeacherResponse, byTeacher)
	if byTeacherResponse.Code != http.StatusOK {
		t.Fatalf("teacher courses status = %d", byTeacherResponse.Code)
	}
	if !strings.Contains(byTeacherResponse.Body.String(), "B3919") {
		t.Fatalf("teacher courses missing class: %s", byTeacherResponse.Body.String())
	}

	notFound := httptest.NewRequest(http.MethodGet, "/api/v1/courses/XXXX", nil)
	notFoundResponse := httptest.NewRecorder()
	handler.ServeHTTP(notFoundResponse, notFound)
	if notFoundResponse.Code != http.StatusNotFound {
		t.Fatalf("missing course status = %d", notFoundResponse.Code)
	}

	badFilter := httptest.NewRequest(http.MethodGet, "/api/v1/courses?page=abc", nil)
	badFilterResponse := httptest.NewRecorder()
	handler.ServeHTTP(badFilterResponse, badFilter)
	if badFilterResponse.Code != http.StatusBadRequest {
		t.Fatalf("invalid filter status = %d", badFilterResponse.Code)
	}

	metaRequest := httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil)
	metaResponse := httptest.NewRecorder()
	handler.ServeHTTP(metaResponse, metaRequest)
	if !strings.Contains(metaResponse.Body.String(), `"course_count":2`) {
		t.Fatalf("meta missing course count: %s", metaResponse.Body.String())
	}
}
