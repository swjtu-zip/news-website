package mcp

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"swjtu-archive/internal/archive"
	"swjtu-cli/pkg/sdk"
)

type testArchive struct {
	store      *archive.Store
	articleID  int64
	resourceID int64
}

func newTestArchive(t *testing.T) testArchive {
	t.Helper()
	store, err := archive.Open(t.TempDir() + "/archive.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	feed := sdk.Feed{
		ID: "news:jdyw", SiteID: "news", SiteName: "新闻网", Category: "university",
		Slug: "jdyw", Name: "交大要闻", BaseURL: "https://news.swjtu.edu.cn",
	}
	if err := store.UpsertFeed(feed, time.Now()); err != nil {
		t.Fatal(err)
	}
	articleID, err := store.UpsertArticle(archive.ArticleInput{
		Feed: feed, Item: sdk.NewsItem{Title: "人工智能与交通发展", URL: "https://news.swjtu.edu.cn/info/1.htm", Date: "2026-08-31"},
		FetchedAt: time.Now(), Article: &sdk.Article{
			Title: "人工智能与交通发展", Source: "新闻网", Date: "2026-08-31",
			Content: "学校开展人工智能交通研究。", ContentHTML: "<p>学校开展人工智能交通研究。</p>", Author: "交大记者",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	path, hash, size, err := store.SaveResourceBody(strings.NewReader("image-data"), 1024, "cover.png", "image/png")
	if err != nil {
		t.Fatal(err)
	}
	resourceID, err := store.UpsertResource(archive.ResourceInput{
		ArticleID: articleID, Kind: "image", OriginalURL: "https://news.swjtu.edu.cn/cover.png",
		LocalPath: path, Filename: "cover.png", ContentType: "image/png", ByteSize: size,
		SHA256: hash, Status: "success", UpdatedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return testArchive{store: store, articleID: articleID, resourceID: resourceID}
}

func rpcRequest(t *testing.T, handler http.Handler, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "http://archive.example/mcp", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	var response map[string]any
	if recorder.Body.Len() > 0 && strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("invalid JSON response (%d): %v\n%s", recorder.Code, err, recorder.Body.String())
		}
	}
	return recorder, response
}

func TestInitializeAndListTools(t *testing.T) {
	data := newTestArchive(t)
	handler := NewHandler(data.store)

	recorder, response := rpcRequest(t, handler, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("initialize status = %d: %s", recorder.Code, recorder.Body.String())
	}
	result, _ := response["result"].(map[string]any)
	if result["protocolVersion"] != "2025-11-25" {
		t.Fatalf("unexpected initialize result: %#v", result)
	}
	capabilities, _ := result["capabilities"].(map[string]any)
	if capabilities["tools"] == nil || capabilities["resources"] == nil {
		t.Fatalf("missing capabilities: %#v", capabilities)
	}

	recorder, response = rpcRequest(t, handler, `{"jsonrpc":"2.0","id":"tools","method":"tools/list","params":{}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("tools/list status = %d", recorder.Code)
	}
	result, _ = response["result"].(map[string]any)
	items, _ := result["tools"].([]any)
	want := map[string]bool{
		"list_feeds": false, "query_articles": false, "search_articles": false,
		"get_article": false, "download_resource": false,
	}
	for _, raw := range items {
		tool, _ := raw.(map[string]any)
		if _, exists := want[tool["name"].(string)]; exists {
			want[tool["name"].(string)] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("tool %q was not advertised", name)
		}
	}

	request := httptest.NewRequest(http.MethodPost, "http://archive.example/mcp", strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted || recorder.Body.Len() != 0 {
		t.Fatalf("notification response = %d %q", recorder.Code, recorder.Body.String())
	}
}

func TestSearchReadArticleAndDownloadResource(t *testing.T) {
	data := newTestArchive(t)
	handler := NewHandler(data.store)

	_, response := rpcRequest(t, handler, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search_articles","arguments":{"query":"人工智能","from":"2026-01-01","page_size":10}}}`)
	result, _ := response["result"].(map[string]any)
	structured, _ := result["structuredContent"].(map[string]any)
	if structured["total"] != float64(1) {
		t.Fatalf("unexpected search result: %#v", result)
	}

	getBody := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_article","arguments":{"id":` + stringID(data.articleID) + `}}}`
	_, response = rpcRequest(t, handler, getBody)
	result, _ = response["result"].(map[string]any)
	structured, _ = result["structuredContent"].(map[string]any)
	if structured["content"] != "学校开展人工智能交通研究。" {
		t.Fatalf("article content missing: %#v", structured)
	}
	resources, _ := structured["resources"].([]any)
	resource, _ := resources[0].(map[string]any)
	wantURI := "swjtu://resources/" + stringID(data.resourceID)
	if resource["resource_uri"] != wantURI || resource["download_url"] != "http://archive.example/assets/"+stringID(data.resourceID) {
		t.Fatalf("unexpected resource metadata: %#v", resource)
	}

	downloadBody := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"download_resource","arguments":{"id":` + stringID(data.resourceID) + `}}}`
	_, response = rpcRequest(t, handler, downloadBody)
	result, _ = response["result"].(map[string]any)
	content, _ := result["content"].([]any)
	link, _ := content[1].(map[string]any)
	if link["type"] != "resource_link" || link["uri"] != wantURI {
		t.Fatalf("download tool did not return a resource link: %#v", result)
	}

	readBody := `{"jsonrpc":"2.0","id":4,"method":"resources/read","params":{"uri":"` + wantURI + `"}}`
	_, response = rpcRequest(t, handler, readBody)
	result, _ = response["result"].(map[string]any)
	contents, _ := result["contents"].([]any)
	blob, _ := contents[0].(map[string]any)["blob"].(string)
	decoded, err := base64.StdEncoding.DecodeString(blob)
	if err != nil || string(decoded) != "image-data" {
		t.Fatalf("unexpected resource bytes: %q (%v)", decoded, err)
	}

	articleBody := `{"jsonrpc":"2.0","id":5,"method":"resources/read","params":{"uri":"swjtu://articles/` + stringID(data.articleID) + `"}}`
	_, response = rpcRequest(t, handler, articleBody)
	result, _ = response["result"].(map[string]any)
	contents, _ = result["contents"].([]any)
	text, _ := contents[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "# 人工智能与交通发展") || !strings.Contains(text, "学校开展人工智能交通研究。") {
		t.Fatalf("unexpected article resource: %q", text)
	}
}

func TestModernProtocolDiscoveryAndCalls(t *testing.T) {
	data := newTestArchive(t)
	handler := NewHandler(data.store)
	meta := `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}`

	discoverBody := `{"jsonrpc":"2.0","id":"discover","method":"server/discover","params":{` + meta + `}}`
	recorder, response := modernRPCRequest(t, handler, discoverBody, "server/discover", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("server/discover status = %d: %s", recorder.Code, recorder.Body.String())
	}
	result, _ := response["result"].(map[string]any)
	versions, _ := result["supportedVersions"].([]any)
	if result["resultType"] != "complete" || len(versions) != 1 || versions[0] != "2026-07-28" || result["ttlMs"] != float64(3600000) {
		t.Fatalf("unexpected discovery result: %#v", result)
	}
	metadata, _ := result["_meta"].(map[string]any)
	if metadata["io.modelcontextprotocol/serverInfo"] == nil {
		t.Fatalf("discovery is missing server identity: %#v", result)
	}

	listBody := `{"jsonrpc":"2.0","id":"list","method":"tools/list","params":{` + meta + `}}`
	_, response = modernRPCRequest(t, handler, listBody, "tools/list", "")
	result, _ = response["result"].(map[string]any)
	if result["resultType"] != "complete" || result["cacheScope"] != "public" || result["tools"] == nil {
		t.Fatalf("unexpected modern tools/list result: %#v", result)
	}

	searchBody := `{"jsonrpc":"2.0","id":"search","method":"tools/call","params":{` + meta + `,"name":"search_articles","arguments":{"query":"人工智能"}}}`
	_, response = modernRPCRequest(t, handler, searchBody, "tools/call", "search_articles")
	result, _ = response["result"].(map[string]any)
	if result["resultType"] != "complete" || result["structuredContent"] == nil {
		t.Fatalf("unexpected modern tools/call result: %#v", result)
	}

	uri := "swjtu://resources/" + stringID(data.resourceID)
	readBody := `{"jsonrpc":"2.0","id":"read","method":"resources/read","params":{` + meta + `,"uri":"` + uri + `"}}`
	_, response = modernRPCRequest(t, handler, readBody, "resources/read", uri)
	result, _ = response["result"].(map[string]any)
	if result["resultType"] != "complete" || result["ttlMs"] != float64(300000) || result["contents"] == nil {
		t.Fatalf("unexpected modern resources/read result: %#v", result)
	}

	request := httptest.NewRequest(http.MethodPost, "http://archive.example/mcp", strings.NewReader(searchBody))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("MCP-Protocol-Version", "2026-07-28")
	request.Header.Set("Mcp-Method", "tools/list")
	request.Header.Set("Mcp-Name", "search_articles")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("header mismatch status = %d", recorder.Code)
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	errorValue, _ := response["error"].(map[string]any)
	if errorValue["code"] != float64(-32020) {
		t.Fatalf("unexpected header mismatch error: %#v", response)
	}
}

func modernRPCRequest(t *testing.T, handler http.Handler, body, method, name string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "http://archive.example/mcp", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2026-07-28")
	request.Header.Set("Mcp-Method", method)
	if name != "" {
		request.Header.Set("Mcp-Name", name)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("invalid modern JSON response (%d): %v\n%s", recorder.Code, err, recorder.Body.String())
	}
	return recorder, response
}

func TestTransportValidation(t *testing.T) {
	data := newTestArchive(t)
	handler := NewHandler(data.store)

	request := httptest.NewRequest(http.MethodGet, "http://archive.example/mcp", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d", recorder.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "http://archive.example/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://attacker.example")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d", recorder.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "http://archive.example/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("MCP-Protocol-Version", "unsupported")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unsupported protocol status = %d", recorder.Code)
	}
}

func stringID(id int64) string { return strconv.FormatInt(id, 10) }
