// Package gateway 实现 swjtu.zip 的聚合 MCP 网关:作为客户端连接 news 与
// teach 两个上游 MCP 服务(streamable HTTP,无状态),把它们的工具以
// news_ / teach_ 前缀重新暴露在一个统一的 /mcp 端点上。
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	newsPrefix  = "news_"
	teachPrefix = "teach_"

	retryInterval   = 30 * time.Second
	connectTimeout  = 15 * time.Second
	serverName      = "swjtu-mcp"
	serverVersion   = "1.0.0"
	instructions    = "聚合西南交通大学两个数据源:新闻(news_ 前缀,来自 news.swjtu.zip)与教学信息(teach_ 前缀,来自 teach.swjtu.zip)。新闻类工具可查询学校各站点的新闻与通知;教学类工具可查询教师目录与开课信息(含活水课程评分)。"
	newsDisplayName = "news"
	teachName       = "teach"
)

// upstream 是单个上游 MCP 服务的客户端状态。上游是无状态的,整个生命周期
// 只维护一个客户端会话;调用失败时重连一次再重试。
type upstream struct {
	name           string
	url            string
	prefix         string
	proxyResources bool
	logger         *slog.Logger

	mu         sync.Mutex
	session    *mcp.ClientSession
	registered atomic.Bool
	connected  atomic.Bool
}

// Gateway 聚合多个上游 MCP 服务,对外提供一个统一的 MCP server。
type Gateway struct {
	server    *mcp.Server
	upstreams []*upstream
	logger    *slog.Logger
	ctx       context.Context
	cancel    context.CancelFunc
}

// New 创建网关并立即尝试连接两个上游;连接失败的上游会在后台每 30s 重试,
// 成功后自动合并其工具。ctx 取消时后台重试随之停止。
func New(ctx context.Context, logger *slog.Logger, newsURL, teachURL string) *Gateway {
	if logger == nil {
		logger = slog.Default()
	}
	server := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: serverVersion}, &mcp.ServerOptions{
		Instructions: instructions,
	})
	gctx, cancel := context.WithCancel(ctx)
	g := &Gateway{
		server: server,
		logger: logger,
		ctx:    gctx,
		cancel: cancel,
		upstreams: []*upstream{
			{name: newsDisplayName, url: newsURL, prefix: newsPrefix, proxyResources: true, logger: logger},
			{name: teachName, url: teachURL, prefix: teachPrefix, logger: logger},
		},
	}
	for _, u := range g.upstreams {
		attemptCtx, done := context.WithTimeout(g.ctx, connectTimeout)
		err := g.connectUpstream(attemptCtx, u)
		done()
		if err != nil {
			logger.Warn("upstream connect failed, will retry in background", "upstream", u.name, "url", u.url, "error", err)
			go g.retryLoop(u)
		}
	}
	return g
}

// Server 返回聚合后的 MCP server。
func (g *Gateway) Server() *mcp.Server { return g.server }

// Close 关闭所有上游会话并停止后台重试。
func (g *Gateway) Close() {
	g.cancel()
	for _, u := range g.upstreams {
		u.mu.Lock()
		if u.session != nil {
			u.session.Close()
			u.session = nil
		}
		u.mu.Unlock()
	}
}

// Handler 返回 HTTP 路由:GET /healthz 健康检查,POST /mcp MCP 端点。
func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", g.handleHealthz)
	mux.Handle("POST /mcp", mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return g.server
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	return mux
}

func (g *Gateway) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintln(w, "ok")
	for _, u := range g.upstreams {
		status := "down"
		if u.connected.Load() {
			status = "up"
		}
		fmt.Fprintf(w, "%s: %s\n", u.name, status)
	}
}

// retryLoop 每 30s 重试连接一次,成功后合并工具并退出。
func (g *Gateway) retryLoop(u *upstream) {
	ticker := time.NewTicker(retryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-g.ctx.Done():
			return
		case <-ticker.C:
			attemptCtx, done := context.WithTimeout(g.ctx, connectTimeout)
			err := g.connectUpstream(attemptCtx, u)
			done()
			if err == nil {
				g.logger.Info("upstream connected after retry", "upstream", u.name, "url", u.url)
				return
			}
			g.logger.Warn("upstream retry failed", "upstream", u.name, "error", err)
		}
	}
}

// connectUpstream 建立客户端会话,拉取工具列表并以前缀注册到聚合 server。
func (g *Gateway) connectUpstream(ctx context.Context, u *upstream) error {
	client := mcp.NewClient(&mcp.Implementation{Name: serverName, Version: serverVersion}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             u.url,
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}, nil)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	var tools []*mcp.Tool
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			session.Close()
			return fmt.Errorf("list tools: %w", err)
		}
		tools = append(tools, tool)
	}

	u.mu.Lock()
	if u.session != nil {
		u.session.Close()
	}
	u.session = session
	u.mu.Unlock()

	if u.registered.CompareAndSwap(false, true) {
		g.registerTools(u, tools)
		if u.proxyResources {
			g.registerResourceTemplates(ctx, u)
		}
	}
	u.connected.Store(true)
	g.logger.Info("upstream connected", "upstream", u.name, "tools", len(tools))
	return nil
}

func (g *Gateway) registerTools(u *upstream, tools []*mcp.Tool) {
	for _, tool := range tools {
		proxied := *tool
		originalName := tool.Name
		proxied.Name = u.prefix + originalName
		proxied.InputSchema = objectSchema(proxied.InputSchema)
		g.server.AddTool(&proxied, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return u.callTool(ctx, &mcp.CallToolParams{
				Name:      originalName,
				Arguments: req.Params.Arguments,
			})
		})
	}
}

// registerResourceTemplates 透传上游的资源模板(URI 不变),resources/read
// 转发给上游会话。
func (g *Gateway) registerResourceTemplates(ctx context.Context, u *upstream) {
	for template, err := range u.currentSession().ResourceTemplates(ctx, nil) {
		if err != nil {
			g.logger.Warn("list resource templates failed", "upstream", u.name, "error", err)
			return
		}
		proxied := *template
		g.server.AddResourceTemplate(&proxied, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return u.readResource(ctx, req.Params.URI)
		})
	}
}

// objectSchema 保证注册到 SDK 的 inputSchema 是 type=object 的 JSON Schema;
// 上游是手写实现,可能缺省或给出不规范的 schema,此时回退为宽松的对象 schema。
func objectSchema(schema any) any {
	if m, ok := schema.(map[string]any); ok {
		if m["type"] == "object" {
			return schema
		}
	}
	if schema != nil {
		if m, ok := remarshalToMap(schema); ok && m["type"] == "object" {
			return m
		}
	}
	return map[string]any{"type": "object"}
}

func remarshalToMap(v any) (map[string]any, bool) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, false
	}
	return m, true
}

func (u *upstream) currentSession() *mcp.ClientSession {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.session
}

// callTool 转发 tools/call 到上游;传输层错误时重连一次并重试。
func (u *upstream) callTool(ctx context.Context, params *mcp.CallToolParams) (*mcp.CallToolResult, error) {
	result, err := u.currentSession().CallTool(ctx, params)
	if err == nil {
		return result, nil
	}
	if rerr := u.reconnect(ctx); rerr != nil {
		u.logger.Warn("upstream reconnect failed", "upstream", u.name, "error", rerr)
		return nil, err
	}
	return u.currentSession().CallTool(ctx, params)
}

// readResource 转发 resources/read 到上游;传输层错误时重连一次并重试。
func (u *upstream) readResource(ctx context.Context, uri string) (*mcp.ReadResourceResult, error) {
	result, err := u.currentSession().ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
	if err == nil {
		return result, nil
	}
	if rerr := u.reconnect(ctx); rerr != nil {
		u.logger.Warn("upstream reconnect failed", "upstream", u.name, "error", rerr)
		return nil, err
	}
	return u.currentSession().ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
}

// reconnect 关闭旧会话并重新建立。上游无状态,重连不影响已注册的工具。
func (u *upstream) reconnect(ctx context.Context) error {
	u.mu.Lock()
	old := u.session
	u.mu.Unlock()
	if old != nil {
		old.Close()
	}
	client := mcp.NewClient(&mcp.Implementation{Name: serverName, Version: serverVersion}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             u.url,
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}, nil)
	if err != nil {
		u.connected.Store(false)
		return err
	}
	u.mu.Lock()
	u.session = session
	u.mu.Unlock()
	u.connected.Store(true)
	return nil
}
