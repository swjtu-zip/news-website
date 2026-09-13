package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"teach-api/internal/store"
)

func newMCPTestHandler(t *testing.T) http.Handler {
	t.Helper()
	database, err := store.Open(t.TempDir() + "/teach.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.ImportBytes(context.Background(), []byte(`{
"generated_at":"2026-09-07T14:32:03Z",
"source":{"site":"faculty.swjtu.edu.cn","base_url":"https://faculty.swjtu.edu.cn"},
"stats":{"list_pages":1},
"teachers":[
  {"name":"张三","profile_url":"https://faculty.swjtu.edu.cn/a/zh_cn/zhangsan.html","initial":"z","college":"计算机学院","status":"ok"},
  {"name":"李四","profile_url":"https://faculty.swjtu.edu.cn/a/zh_cn/lisi.html","initial":"l","college":"数学学院","status":"ok"}
]}`)); err != nil {
		t.Fatal(err)
	}
	return New(database, Options{Logger: log.New(io.Discard, "", 0)}).Handler()
}

func postMCP(t *testing.T, handler http.Handler, payload string) (int, map[string]any) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	body := response.Body.Bytes()
	var message map[string]any
	if err := json.Unmarshal(body, &message); err != nil {
		t.Fatalf("status %d, invalid JSON response: %s", response.Code, body)
	}
	return response.Code, message
}

func TestMCPEndpoint(t *testing.T) {
	handler := newMCPTestHandler(t)

	status, initialize := postMCP(t, handler, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}`)
	if status != http.StatusOK {
		t.Fatalf("initialize status = %d", status)
	}
	result, ok := initialize["result"].(map[string]any)
	if !ok {
		t.Fatalf("initialize missing result: %v", initialize)
	}
	serverInfo, ok := result["serverInfo"].(map[string]any)
	if !ok || serverInfo["name"] != "swjtu-teach" {
		t.Fatalf("unexpected serverInfo: %v", result)
	}

	status, list := postMCP(t, handler, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	if status != http.StatusOK {
		t.Fatalf("tools/list status = %d", status)
	}
	tools, ok := list["result"].(map[string]any)["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Fatalf("tools/list returned no tools: %v", list)
	}
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.(map[string]any)["name"].(string)] = true
	}
	for _, want := range []string{"search_teachers", "get_teacher", "list_teacher_courses", "search_courses", "get_course", "get_course_reviews", "dataset_meta"} {
		if !names[want] {
			t.Fatalf("missing tool %q in %v", want, names)
		}
	}

	status, call := postMCP(t, handler, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"search_teachers","arguments":{"search":"张"}}}`)
	if status != http.StatusOK {
		t.Fatalf("tools/call status = %d", status)
	}
	callResult, ok := call["result"].(map[string]any)
	if !ok {
		t.Fatalf("tools/call missing result: %v", call)
	}
	if callResult["isError"] == true {
		t.Fatalf("tools/call reported error: %v", callResult)
	}
	structured, ok := callResult["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("tools/call missing structuredContent: %v", callResult)
	}
	data, ok := structured["data"].([]any)
	if !ok || len(data) != 1 {
		t.Fatalf("expected one teacher, got %v", structured)
	}
	if data[0].(map[string]any)["name"] != "张三" {
		t.Fatalf("unexpected teacher: %v", data[0])
	}

	_, notFound := postMCP(t, handler, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"get_teacher","arguments":{"id":"missing"}}}`)
	notFoundResult := notFound["result"].(map[string]any)
	if notFoundResult["isError"] != true {
		t.Fatalf("expected isError for unknown teacher: %v", notFoundResult)
	}
}

func TestMCPRejectsGET(t *testing.T) {
	handler := newMCPTestHandler(t)
	request := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	request.Header.Set("Accept", "application/json, text/event-stream")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /mcp status = %d, want 405", response.Code)
	}
}
