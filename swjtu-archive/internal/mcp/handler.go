// Package mcp exposes the archive through the Model Context Protocol.
package mcp

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"swjtu-archive/internal/archive"
	"swjtu-cli/pkg/sdk"
)

const (
	modernProtocolVersion = "2026-07-28"
	legacyProtocolVersion = "2025-11-25"
	serverName            = "swjtu-archive"
	serverVersion         = "1.0.0"
	maxRequestBytes       = 1 << 20
)

var supportedProtocolVersions = map[string]bool{
	"2026-07-28": true,
	"2025-11-25": true,
	"2025-06-18": true,
	"2025-03-26": true,
}

var legacyProtocolVersions = map[string]bool{
	"2025-11-25": true,
	"2025-06-18": true,
	"2025-03-26": true,
}

// Handler is a stateless Streamable HTTP MCP server backed by the archive.
type Handler struct {
	store *archive.Store
}

// NewHandler creates a read-only MCP handler.
func NewHandler(store *archive.Store) *Handler { return &Handler{store: store} }

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !validOrigin(r) {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}
	if r.Method == http.MethodOptions {
		w.Header().Set("Allow", "POST, OPTIONS")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST, OPTIONS")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	decoder.UseNumber()
	var message request
	if err := decoder.Decode(&message); err != nil {
		writeRPCError(w, http.StatusBadRequest, nil, -32700, "Parse error", err.Error())
		return
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeRPCError(w, http.StatusBadRequest, nil, -32600, "Invalid Request", "request body must contain one JSON-RPC message")
		return
	}
	if message.JSONRPC != "2.0" || strings.TrimSpace(message.Method) == "" || bytes.Equal(message.ID, []byte("null")) {
		writeRPCError(w, http.StatusBadRequest, nil, -32600, "Invalid Request", nil)
		return
	}
	modern, protocolErr := validateProtocolRequest(r, message)
	if protocolErr != nil {
		writeJSON(w, http.StatusBadRequest, response{JSONRPC: "2.0", ID: message.ID, Error: protocolErr})
		return
	}

	// Notifications have no response body in Streamable HTTP.
	if len(message.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	if message.Method == "resources/read" {
		h.handleReadResource(w, r, message, modern)
		return
	}

	result, rpcErr := h.dispatch(r, message, modern)
	if rpcErr != nil {
		status := http.StatusOK
		if modern && rpcErr.Code == -32601 {
			status = http.StatusNotFound
		}
		writeJSON(w, status, response{JSONRPC: "2.0", ID: message.ID, Error: rpcErr})
		return
	}
	if modern {
		result = modernResult(message.Method, result)
	}
	writeJSON(w, http.StatusOK, response{JSONRPC: "2.0", ID: message.ID, Result: result})
}

func (h *Handler) dispatch(r *http.Request, message request, modern bool) (any, *rpcError) {
	switch message.Method {
	case "initialize":
		if modern {
			return nil, &rpcError{Code: -32601, Message: "Method not found"}
		}
		return initializeResult(message.Params)
	case "server/discover":
		if !modern {
			return nil, &rpcError{Code: -32601, Message: "Method not found"}
		}
		return discoverResult(), nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": tools()}, nil
	case "tools/call":
		return h.callTool(r, message.Params), nil
	case "resources/list":
		return h.listResources(message.Params)
	case "resources/templates/list":
		return map[string]any{"resourceTemplates": resourceTemplates()}, nil
	default:
		return nil, &rpcError{Code: -32601, Message: "Method not found"}
	}
}

func initializeResult(params json.RawMessage) (any, *rpcError) {
	var input struct {
		ProtocolVersion string         `json:"protocolVersion"`
		Capabilities    map[string]any `json:"capabilities"`
		ClientInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"clientInfo"`
	}
	if err := json.Unmarshal(params, &input); err != nil || input.ProtocolVersion == "" || input.Capabilities == nil || input.ClientInfo.Name == "" || input.ClientInfo.Version == "" {
		return nil, &rpcError{Code: -32602, Message: "Invalid params", Data: "protocolVersion, capabilities and clientInfo are required"}
	}
	version := legacyProtocolVersion
	if legacyProtocolVersions[input.ProtocolVersion] {
		version = input.ProtocolVersion
	}
	return map[string]any{
		"protocolVersion": version,
		"capabilities": map[string]any{
			"tools":     map[string]any{},
			"resources": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name": serverName, "title": "SWJTU 新闻归档", "version": serverVersion,
		},
		"instructions": "先用 search_articles 检索关键词，或用 query_articles 按来源和日期查询；用 get_article 查看正文和资源列表；用 download_resource 获取下载链接，或读取返回的 swjtu://resources/{id} MCP 资源。",
	}, nil
}

func discoverResult() map[string]any {
	return map[string]any{
		"supportedVersions": []string{modernProtocolVersion},
		"capabilities": map[string]any{
			"tools":     map[string]any{},
			"resources": map[string]any{},
		},
		"instructions": "先用 search_articles 检索关键词，或用 query_articles 按来源和日期查询；用 get_article 查看正文和资源列表；用 download_resource 获取下载链接，或读取返回的 swjtu://resources/{id} MCP 资源。",
	}
}

func validateProtocolRequest(r *http.Request, message request) (bool, *rpcError) {
	headerVersion := strings.TrimSpace(r.Header.Get("MCP-Protocol-Version"))
	metaVersion, hasCapabilities, metaErr := requestMetadata(message.Params)
	modern := headerVersion == modernProtocolVersion || metaVersion == modernProtocolVersion
	if headerVersion != "" && !supportedProtocolVersions[headerVersion] {
		return modern, &rpcError{
			Code: -32022, Message: "Unsupported protocol version",
			Data: map[string]any{"supported": []string{modernProtocolVersion, legacyProtocolVersion, "2025-06-18", "2025-03-26"}, "requested": headerVersion},
		}
	}
	if !modern {
		return false, nil
	}
	if headerVersion != modernProtocolVersion || metaVersion != modernProtocolVersion {
		return true, &rpcError{Code: -32020, Message: "Header mismatch: MCP-Protocol-Version must match params._meta protocolVersion"}
	}
	if metaErr != nil || !hasCapabilities {
		return true, &rpcError{Code: -32602, Message: "Invalid params", Data: "modern requests require params._meta with clientCapabilities"}
	}
	if method := r.Header.Get("Mcp-Method"); method == "" || method != message.Method {
		return true, &rpcError{Code: -32020, Message: "Header mismatch: Mcp-Method must match request method"}
	}
	name, needsName, err := routedRequestName(message)
	if err != nil {
		return true, &rpcError{Code: -32602, Message: "Invalid params", Data: err.Error()}
	}
	if needsName {
		headerName, err := decodeMCPHeaderValue(r.Header.Get("Mcp-Name"))
		if err != nil || headerName == "" || headerName != name {
			return true, &rpcError{Code: -32020, Message: "Header mismatch: Mcp-Name must match request params"}
		}
	}
	return true, nil
}

func decodeMCPHeaderValue(value string) (string, error) {
	const prefix = "=?base64?"
	const suffix = "?="
	if strings.HasPrefix(value, prefix) && strings.HasSuffix(value, suffix) {
		encoded := strings.TrimSuffix(strings.TrimPrefix(value, prefix), suffix)
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return "", err
		}
		return string(decoded), nil
	}
	if strings.TrimSpace(value) != value {
		return "", errors.New("header value has surrounding whitespace")
	}
	for _, char := range []byte(value) {
		if char < 0x20 || char > 0x7e {
			return "", errors.New("header value is not plain ASCII")
		}
	}
	return value, nil
}

func requestMetadata(params json.RawMessage) (version string, hasCapabilities bool, err error) {
	var envelope struct {
		Meta json.RawMessage `json:"_meta"`
	}
	if len(params) == 0 {
		return "", false, nil
	}
	if err := json.Unmarshal(params, &envelope); err != nil || len(envelope.Meta) == 0 {
		return "", false, err
	}
	var metadata struct {
		ProtocolVersion    string          `json:"io.modelcontextprotocol/protocolVersion"`
		ClientCapabilities json.RawMessage `json:"io.modelcontextprotocol/clientCapabilities"`
		ClientInfo         json.RawMessage `json:"io.modelcontextprotocol/clientInfo"`
	}
	if err := json.Unmarshal(envelope.Meta, &metadata); err != nil {
		return "", false, err
	}
	if len(metadata.ClientCapabilities) > 0 && !bytes.Equal(metadata.ClientCapabilities, []byte("null")) {
		var capabilities map[string]any
		if err := json.Unmarshal(metadata.ClientCapabilities, &capabilities); err != nil || capabilities == nil {
			return metadata.ProtocolVersion, false, errors.New("clientCapabilities must be an object")
		}
		hasCapabilities = true
	}
	if len(metadata.ClientInfo) > 0 && !bytes.Equal(metadata.ClientInfo, []byte("null")) {
		var info struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}
		if err := json.Unmarshal(metadata.ClientInfo, &info); err != nil || info.Name == "" || info.Version == "" {
			return metadata.ProtocolVersion, hasCapabilities, errors.New("clientInfo must contain name and version")
		}
	}
	return metadata.ProtocolVersion, hasCapabilities, nil
}

func routedRequestName(message request) (string, bool, error) {
	switch message.Method {
	case "tools/call", "prompts/get":
		var params struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(message.Params, &params); err != nil || params.Name == "" {
			return "", true, errors.New("name is required")
		}
		return params.Name, true, nil
	case "resources/read":
		var params struct {
			URI string `json:"uri"`
		}
		if err := json.Unmarshal(message.Params, &params); err != nil || params.URI == "" {
			return "", true, errors.New("uri is required")
		}
		return params.URI, true, nil
	default:
		return "", false, nil
	}
}

func modernResult(method string, value any) any {
	result, ok := value.(map[string]any)
	if !ok {
		return value
	}
	result["resultType"] = "complete"
	result["_meta"] = serverMetadata()
	switch method {
	case "server/discover", "tools/list", "resources/templates/list":
		result["ttlMs"] = 60 * 60 * 1000
		result["cacheScope"] = "public"
	case "resources/list":
		result["ttlMs"] = 30 * 1000
		result["cacheScope"] = "public"
	case "resources/read":
		result["ttlMs"] = 5 * 60 * 1000
		result["cacheScope"] = "public"
	}
	return result
}

func serverMetadata() map[string]any {
	return map[string]any{
		"io.modelcontextprotocol/serverInfo": map[string]any{
			"name": serverName, "title": "SWJTU 新闻归档", "version": serverVersion,
		},
	}
}

func tools() []map[string]any {
	readOnly := map[string]any{
		"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false,
	}
	filterProperties := map[string]any{
		"site":      map[string]any{"type": "string", "description": "站点 ID；可用 list_feeds 查询"},
		"category":  map[string]any{"type": "string", "description": "来源类别，例如 university、school 或 department"},
		"feed":      map[string]any{"type": "string", "description": "Feed ID"},
		"from":      map[string]any{"type": "string", "format": "date", "description": "起始发布日期，YYYY-MM-DD"},
		"to":        map[string]any{"type": "string", "format": "date", "description": "结束发布日期，YYYY-MM-DD"},
		"page":      map[string]any{"type": "integer", "minimum": 1, "default": 1},
		"page_size": map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 20},
	}
	searchProperties := cloneMap(filterProperties)
	searchProperties["query"] = map[string]any{"type": "string", "minLength": 1, "description": "在标题、正文、来源和站点名称中检索的关键词"}
	return []map[string]any{
		{
			"name": "list_feeds", "title": "查询新闻来源",
			"description": "列出可用于文章查询和检索的新闻来源、站点 ID、类别与 Feed ID。",
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false},
			"annotations": readOnly,
		},
		{
			"name": "query_articles", "title": "查询文章",
			"description": "按站点、类别、Feed 和发布日期范围查询归档文章；未给过滤条件时返回最新文章。",
			"inputSchema": map[string]any{"type": "object", "properties": filterProperties, "additionalProperties": false},
			"annotations": readOnly,
		},
		{
			"name": "search_articles", "title": "全文检索文章",
			"description": "全文检索文章标题与正文，并可按来源和发布日期进一步过滤。",
			"inputSchema": map[string]any{"type": "object", "properties": searchProperties, "required": []string{"query"}, "additionalProperties": false},
			"annotations": readOnly,
		},
		{
			"name": "get_article", "title": "查看文章正文",
			"description": "按文章 ID 读取元数据、完整正文，以及可下载的图片和附件资源。",
			"inputSchema": map[string]any{
				"type": "object", "properties": map[string]any{"id": map[string]any{"type": "integer", "minimum": 1}},
				"required": []string{"id"}, "additionalProperties": false,
			},
			"annotations": readOnly,
		},
		{
			"name": "download_resource", "title": "下载附件或图片",
			"description": "按资源 ID 返回附件或图片的 MCP 资源 URI 和 HTTP 下载地址；MCP 客户端可对资源 URI 调用 resources/read 获取原始字节。",
			"inputSchema": map[string]any{
				"type": "object", "properties": map[string]any{"id": map[string]any{"type": "integer", "minimum": 1}},
				"required": []string{"id"}, "additionalProperties": false,
			},
			"annotations": readOnly,
		},
	}
}

func cloneMap(source map[string]any) map[string]any {
	result := make(map[string]any, len(source)+1)
	for key, value := range source {
		result[key] = value
	}
	return result
}

type queryArguments struct {
	Query    string `json:"query"`
	Site     string `json:"site"`
	Category string `json:"category"`
	Feed     string `json:"feed"`
	From     string `json:"from"`
	To       string `json:"to"`
	Page     int    `json:"page"`
	PageSize int    `json:"page_size"`
}

func (h *Handler) callTool(r *http.Request, params json.RawMessage) map[string]any {
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &call); err != nil || call.Name == "" {
		return toolError("工具调用参数无效")
	}
	switch call.Name {
	case "list_feeds":
		var empty struct{}
		if err := decodeArguments(call.Arguments, &empty); err != nil {
			return toolError(err.Error())
		}
		return structuredToolResult(map[string]any{"feeds": sdk.Feeds()})
	case "query_articles", "search_articles":
		var args queryArguments
		if err := decodeArguments(call.Arguments, &args); err != nil {
			return toolError(err.Error())
		}
		if call.Name == "query_articles" && strings.TrimSpace(args.Query) != "" {
			return toolError("query_articles 不接受 query；请使用 search_articles 进行全文检索")
		}
		if call.Name == "search_articles" && strings.TrimSpace(args.Query) == "" {
			return toolError("query 不能为空")
		}
		if err := validateQueryArguments(&args); err != nil {
			return toolError(err.Error())
		}
		items, total, err := h.store.FindArticles(archive.ArticleFilter{
			Query: args.Query, SiteID: args.Site, Category: args.Category, FeedID: args.Feed,
			From: args.From, To: args.To, Page: args.Page, PageSize: args.PageSize,
		})
		if err != nil {
			return toolError("查询文章失败：" + err.Error())
		}
		return structuredToolResult(map[string]any{
			"articles": items, "total": total, "page": args.Page, "page_size": args.PageSize,
		})
	case "get_article":
		var args struct {
			ID int64 `json:"id"`
		}
		if err := decodeArguments(call.Arguments, &args); err != nil || args.ID <= 0 {
			return toolError("id 必须是正整数")
		}
		item, err := h.store.GetArticle(args.ID)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return toolError("文章不存在")
			}
			return toolError("读取文章失败：" + err.Error())
		}
		return articleToolResult(item, requestBase(r))
	case "download_resource":
		var args struct {
			ID int64 `json:"id"`
		}
		if err := decodeArguments(call.Arguments, &args); err != nil || args.ID <= 0 {
			return toolError("id 必须是正整数")
		}
		item, err := h.store.Resource(args.ID)
		if err != nil {
			return toolError("资源不存在")
		}
		if item.Status != "success" {
			return toolError("资源尚不可下载：" + item.Status)
		}
		file, err := h.store.OpenResource(item)
		if err != nil {
			return toolError("资源文件不存在")
		}
		_ = file.Close()
		return resourceToolResult(item, requestBase(r))
	default:
		return toolError("未知工具：" + call.Name)
	}
}

func decodeArguments(raw json.RawMessage, target any) error {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		raw = []byte("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("工具参数无效：%w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("工具参数必须是单个 JSON 对象")
	}
	return nil
}

func validateQueryArguments(args *queryArguments) error {
	args.Query = strings.TrimSpace(args.Query)
	args.Site = strings.TrimSpace(args.Site)
	args.Category = strings.TrimSpace(args.Category)
	args.Feed = strings.TrimSpace(args.Feed)
	if args.Page == 0 {
		args.Page = 1
	}
	if args.PageSize == 0 {
		args.PageSize = 20
	}
	if args.Page < 1 {
		return errors.New("page 必须大于等于 1")
	}
	if args.PageSize < 1 || args.PageSize > 100 {
		return errors.New("page_size 必须在 1 到 100 之间")
	}
	for name, value := range map[string]string{"from": args.From, "to": args.To} {
		if value == "" {
			continue
		}
		if parsed, err := time.Parse("2006-01-02", value); err != nil || parsed.Format("2006-01-02") != value {
			return fmt.Errorf("%s 必须是有效的 YYYY-MM-DD 日期", name)
		}
	}
	if args.From != "" && args.To != "" && args.From > args.To {
		return errors.New("from 不能晚于 to")
	}
	return nil
}

func structuredToolResult(value map[string]any) map[string]any {
	body, _ := json.Marshal(value)
	return map[string]any{
		"content":           []any{map[string]any{"type": "text", "text": string(body)}},
		"structuredContent": value,
	}
}

func toolError(message string) map[string]any {
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": message}},
		"isError": true,
	}
}

func articleToolResult(item *archive.ArticleRecord, base string) map[string]any {
	resources := make([]map[string]any, 0, len(item.Resources))
	content := []any{map[string]any{"type": "text", "text": articleMarkdown(item, base)}}
	for i := range item.Resources {
		resource := &item.Resources[i]
		entry := resourceMetadata(resource, base)
		resources = append(resources, entry)
		if resource.Status == "success" {
			content = append(content, resourceLink(resource))
		}
	}
	structured := map[string]any{
		"id": item.ID, "title": item.Title, "source": item.Source, "site_id": item.SiteID,
		"site_name": item.SiteName, "feed_id": item.FeedID, "category": item.Category, "type": item.Type,
		"published_at": item.PublishedAt, "canonical_url": item.CanonicalURL, "author": item.Author,
		"photographer": item.Photographer, "editor": item.Editor, "content": item.Content,
		"status": item.Status, "resources": resources,
	}
	return map[string]any{"content": content, "structuredContent": structured}
}

func resourceToolResult(item *archive.ResourceRecord, base string) map[string]any {
	metadata := resourceMetadata(item, base)
	body, _ := json.Marshal(metadata)
	return map[string]any{
		"content": []any{
			map[string]any{"type": "text", "text": string(body)},
			resourceLink(item),
		},
		"structuredContent": metadata,
	}
}

func resourceMetadata(item *archive.ResourceRecord, base string) map[string]any {
	return map[string]any{
		"id": item.ID, "article_id": item.ArticleID, "kind": item.Kind,
		"filename": item.Filename, "content_type": item.ContentType, "byte_size": item.ByteSize,
		"sha256": item.SHA256, "status": item.Status, "resource_uri": resourceURI(item.ID),
		"download_url": base + "/assets/" + strconv.FormatInt(item.ID, 10),
	}
}

func resourceLink(item *archive.ResourceRecord) map[string]any {
	name := item.Filename
	if name == "" {
		name = "resource-" + strconv.FormatInt(item.ID, 10)
	}
	return map[string]any{
		"type": "resource_link", "uri": resourceURI(item.ID), "name": name,
		"title": name, "description": resourceDescription(item), "mimeType": item.ContentType,
		"size": item.ByteSize,
	}
}

func resourceDescription(item *archive.ResourceRecord) string {
	if item.Kind == "image" {
		return "文章图片"
	}
	return "文章附件"
}

func resourceTemplates() []map[string]any {
	return []map[string]any{
		{
			"uriTemplate": "swjtu://articles/{id}", "name": "article-by-id", "title": "文章正文",
			"description": "按文章 ID 读取 Markdown 正文及图片、附件链接", "mimeType": "text/markdown",
		},
		{
			"uriTemplate": "swjtu://resources/{id}", "name": "resource-by-id", "title": "文章图片或附件",
			"description": "按资源 ID 读取图片或附件原始字节",
		},
	}
}

func (h *Handler) listResources(params json.RawMessage) (any, *rpcError) {
	var input struct {
		Cursor string `json:"cursor"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &input); err != nil {
			return nil, &rpcError{Code: -32602, Message: "Invalid params", Data: err.Error()}
		}
	}
	page := 1
	if input.Cursor != "" {
		var err error
		page, err = strconv.Atoi(input.Cursor)
		if err != nil || page < 1 {
			return nil, &rpcError{Code: -32602, Message: "Invalid cursor"}
		}
	}
	const pageSize = 50
	items, total, err := h.store.FindArticles(archive.ArticleFilter{Page: page, PageSize: pageSize})
	if err != nil {
		return nil, &rpcError{Code: -32603, Message: "Internal error", Data: err.Error()}
	}
	resources := make([]map[string]any, 0, len(items))
	for _, item := range items {
		metadata := make([]string, 0, 2)
		if item.SiteName != "" {
			metadata = append(metadata, item.SiteName)
		}
		if item.PublishedAt != "" {
			metadata = append(metadata, item.PublishedAt)
		}
		resources = append(resources, map[string]any{
			"uri": articleURI(item.ID), "name": "article-" + strconv.FormatInt(item.ID, 10),
			"title": item.Title, "description": strings.Join(metadata, " · "), "mimeType": "text/markdown",
		})
	}
	result := map[string]any{"resources": resources}
	if page*pageSize < total {
		result["nextCursor"] = strconv.Itoa(page + 1)
	}
	return result, nil
}

func (h *Handler) handleReadResource(w http.ResponseWriter, r *http.Request, message request, modern bool) {
	var input struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(message.Params, &input); err != nil || input.URI == "" {
		writeRPCError(w, http.StatusOK, message.ID, -32602, "Invalid params", "uri is required")
		return
	}
	kind, id, err := parseResourceURI(input.URI)
	if err != nil {
		writeRPCError(w, http.StatusOK, message.ID, -32602, "Invalid params", err.Error())
		return
	}
	if kind == "articles" {
		item, err := h.store.GetArticle(id)
		if err != nil {
			writeRPCError(w, http.StatusOK, message.ID, -32002, "Resource not found", nil)
			return
		}
		result := map[string]any{"contents": []any{map[string]any{
			"uri": input.URI, "mimeType": "text/markdown", "text": articleMarkdown(item, requestBase(r)),
		}}}
		if modern {
			modernResult(message.Method, result)
		}
		writeJSON(w, http.StatusOK, response{JSONRPC: "2.0", ID: message.ID, Result: result})
		return
	}
	item, err := h.store.Resource(id)
	if err != nil || item.Status != "success" {
		writeRPCError(w, http.StatusOK, message.ID, -32002, "Resource not found", nil)
		return
	}
	file, err := h.store.OpenResource(item)
	if err != nil {
		writeRPCError(w, http.StatusOK, message.ID, -32002, "Resource not found", nil)
		return
	}
	defer file.Close()
	writeBlobResponse(w, message.ID, input.URI, item.ContentType, file, modern)
}

func writeBlobResponse(w http.ResponseWriter, id json.RawMessage, uri, contentType string, body io.Reader, modern bool) {
	idJSON, _ := json.Marshal(json.RawMessage(id))
	uriJSON, _ := json.Marshal(uri)
	typeJSON, _ := json.Marshal(contentType)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{`, idJSON)
	if modern {
		metaJSON, _ := json.Marshal(serverMetadata())
		_, _ = fmt.Fprintf(w, `"resultType":"complete","_meta":%s,"ttlMs":300000,"cacheScope":"public",`, metaJSON)
	}
	_, _ = fmt.Fprintf(w, `"contents":[{"uri":%s,"mimeType":%s,"blob":"`, uriJSON, typeJSON)
	encoder := base64.NewEncoder(base64.StdEncoding, w)
	_, copyErr := io.Copy(encoder, body)
	closeErr := encoder.Close()
	if copyErr == nil && closeErr == nil {
		_, _ = io.WriteString(w, `"}]}}`)
	}
}

func articleMarkdown(item *archive.ArticleRecord, base string) string {
	var b strings.Builder
	b.WriteString("# " + item.Title + "\n\n")
	meta := make([]string, 0, 5)
	for _, value := range []string{item.SiteName, item.PublishedAt, item.Source, item.Author} {
		if strings.TrimSpace(value) != "" {
			meta = append(meta, value)
		}
	}
	if len(meta) > 0 {
		b.WriteString("> " + strings.Join(meta, " · ") + "\n\n")
	}
	b.WriteString(strings.TrimSpace(item.Content))
	b.WriteString("\n")
	for _, kind := range []string{"image", "attachment"} {
		heading := "图片"
		if kind == "attachment" {
			heading = "附件"
		}
		var lines []string
		for i := range item.Resources {
			resource := &item.Resources[i]
			if resource.Kind != kind || resource.Status != "success" {
				continue
			}
			name := resource.Filename
			if name == "" {
				name = "resource-" + strconv.FormatInt(resource.ID, 10)
			}
			lines = append(lines, fmt.Sprintf("- [%s](%s)（[HTTP 下载](%s/assets/%d)，%d bytes）", name, resourceURI(resource.ID), base, resource.ID, resource.ByteSize))
		}
		if len(lines) > 0 {
			b.WriteString("\n## " + heading + "\n\n")
			b.WriteString(strings.Join(lines, "\n") + "\n")
		}
	}
	if item.CanonicalURL != "" {
		b.WriteString("\n原文：" + item.CanonicalURL + "\n")
	}
	return b.String()
}

func articleURI(id int64) string  { return "swjtu://articles/" + strconv.FormatInt(id, 10) }
func resourceURI(id int64) string { return "swjtu://resources/" + strconv.FormatInt(id, 10) }

func parseResourceURI(value string) (string, int64, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "swjtu" || (parsed.Host != "articles" && parsed.Host != "resources") {
		return "", 0, errors.New("uri 必须是 swjtu://articles/{id} 或 swjtu://resources/{id}")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", 0, errors.New("资源 URI 不能包含查询参数或片段")
	}
	id, err := strconv.ParseInt(strings.Trim(parsed.Path, "/"), 10, 64)
	if err != nil || id <= 0 || strings.Count(strings.Trim(parsed.Path, "/"), "/") != 0 {
		return "", 0, errors.New("资源 ID 必须是正整数")
	}
	return parsed.Host, id, nil
}

func requestBase(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwarded := firstHeaderValue(r.Header.Get("X-Forwarded-Proto")); forwarded == "http" || forwarded == "https" {
		scheme = forwarded
	}
	host := r.Host
	if forwarded := firstHeaderValue(r.Header.Get("X-Forwarded-Host")); forwarded != "" {
		host = forwarded
	}
	return scheme + "://" + host
}

func validOrigin(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return false
	}
	host := r.Host
	if forwarded := firstHeaderValue(r.Header.Get("X-Forwarded-Host")); forwarded != "" {
		host = forwarded
	}
	return strings.EqualFold(parsed.Host, host)
}

func firstHeaderValue(value string) string {
	first, _, _ := strings.Cut(value, ",")
	return strings.TrimSpace(first)
}

func writeRPCError(w http.ResponseWriter, status int, id json.RawMessage, code int, message string, data any) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	writeJSON(w, status, response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message, Data: data}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
