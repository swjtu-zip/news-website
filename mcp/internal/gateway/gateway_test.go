package gateway

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type echoInput struct {
	Text string `json:"text" jsonschema:"要回显的文本"`
}

type echoOutput struct {
	Text string `json:"text"`
	From string `json:"from"`
}

// newFakeUpstream 启动一个进程内的假上游 MCP 服务:一个回显工具,
// 以及(news 侧)一个资源模板。
func newFakeUpstream(t *testing.T, name string, withResource bool) string {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: name, Version: "0.0.1"}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "echo",
		Description: fmt.Sprintf("来自 %s 的回显工具", name),
	}, func(_ context.Context, _ *mcp.CallToolRequest, in echoInput) (*mcp.CallToolResult, echoOutput, error) {
		return nil, echoOutput{Text: in.Text, From: name}, nil
	})
	if withResource {
		server.AddResourceTemplate(&mcp.ResourceTemplate{
			URITemplate: "swjtu://articles/{id}",
			Name:        "article",
			Description: "新闻文章",
			MIMEType:    "text/plain",
		}, func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
				URI:      req.Params.URI,
				MIMEType: "text/plain",
				Text:     "article-from-" + name,
			}}}, nil
		})
	}
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	t.Cleanup(ts.Close)
	return ts.URL + "/mcp"
}

func newTestGateway(t *testing.T, newsURL, teachURL string) (*Gateway, *mcp.ClientSession) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	gw := New(context.Background(), logger, newsURL, teachURL)
	t.Cleanup(gw.Close)
	ts := httptest.NewServer(gw.Handler())
	t.Cleanup(ts.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:             ts.URL + "/mcp",
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return gw, session
}

func TestAggregatesPrefixedTools(t *testing.T) {
	newsURL := newFakeUpstream(t, "fake-news", true)
	teachURL := newFakeUpstream(t, "fake-teach", false)
	_, session := newTestGateway(t, newsURL, teachURL)

	tools := map[string]*mcp.Tool{}
	for tool, err := range session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		tools[tool.Name] = tool
	}
	if len(tools) != 2 {
		t.Fatalf("expected 2 aggregated tools, got %v", tools)
	}
	newsEcho, ok := tools["news_echo"]
	if !ok {
		t.Fatalf("missing news_echo in %v", tools)
	}
	if newsEcho.Description != "来自 fake-news 的回显工具" {
		t.Fatalf("description not preserved: %q", newsEcho.Description)
	}
	if _, ok := tools["teach_echo"]; !ok {
		t.Fatalf("missing teach_echo in %v", tools)
	}
}

func TestToolCallForwarding(t *testing.T) {
	newsURL := newFakeUpstream(t, "fake-news", false)
	teachURL := newFakeUpstream(t, "fake-teach", false)
	_, session := newTestGateway(t, newsURL, teachURL)

	for _, tc := range []struct {
		tool string
		from string
	}{
		{"news_echo", "fake-news"},
		{"teach_echo", "fake-teach"},
	} {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      tc.tool,
			Arguments: map[string]any{"text": "你好"},
		})
		if err != nil {
			t.Fatalf("call %s: %v", tc.tool, err)
		}
		if result.IsError {
			t.Fatalf("call %s reported error: %+v", tc.tool, result)
		}
		structured, ok := result.StructuredContent.(map[string]any)
		if !ok {
			t.Fatalf("call %s missing structured content: %+v", tc.tool, result)
		}
		if structured["text"] != "你好" || structured["from"] != tc.from {
			t.Fatalf("call %s not forwarded correctly: %v", tc.tool, structured)
		}
	}
}

func TestResourceTemplateProxying(t *testing.T) {
	newsURL := newFakeUpstream(t, "fake-news", true)
	teachURL := newFakeUpstream(t, "fake-teach", false)
	_, session := newTestGateway(t, newsURL, teachURL)

	var templates []*mcp.ResourceTemplate
	for template, err := range session.ResourceTemplates(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		templates = append(templates, template)
	}
	if len(templates) != 1 || templates[0].URITemplate != "swjtu://articles/{id}" {
		t.Fatalf("unexpected resource templates: %+v", templates)
	}

	result, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "swjtu://articles/42"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Contents) != 1 || result.Contents[0].Text != "article-from-fake-news" {
		t.Fatalf("unexpected resource contents: %+v", result.Contents)
	}
}

func TestServesWhenUpstreamDown(t *testing.T) {
	teachURL := newFakeUpstream(t, "fake-teach", false)
	// news 上游指向一个不可达地址,网关应继续服务 teach 的工具。
	gw, session := newTestGateway(t, "http://127.0.0.1:1/mcp", teachURL)

	tools := map[string]bool{}
	for tool, err := range session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		tools[tool.Name] = true
	}
	if !tools["teach_echo"] {
		t.Fatalf("teach tools unavailable when news is down: %v", tools)
	}
	if tools["news_echo"] {
		t.Fatalf("news tools should not be registered while unreachable: %v", tools)
	}

	recorder := httptest.NewRecorder()
	gw.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/healthz", nil))
	if recorder.Code != 200 {
		t.Fatalf("healthz status = %d", recorder.Code)
	}
	body := recorder.Body.String()
	if body == "" || body[:3] != "ok\n" {
		t.Fatalf("unexpected healthz body: %q", body)
	}
}
