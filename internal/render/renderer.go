package render

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/osteele/liquid"

	"github.com/karlrissland/homelab-api2mcp/internal/manifest"
)

// HTTPClient issues HTTP requests. It is defined in the consuming package
// so tests can inject an httptest-backed client.
type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

// Request is the rendered upstream HTTP request.
type Request struct {
	Method  string
	URL     string
	Headers http.Header
	Body    []byte
}

// Response is the upstream HTTP response exposed to response.liquid.
type Response struct {
	Status     int
	StatusText string
	Headers    map[string]string
	Body       any
	RawBody    string
}

// Result is one rendered tool execution.
type Result struct {
	Request  Request
	Response Response
	Content  string
}

// Renderer renders request/response Liquid templates around an upstream
// HTTP call.
type Renderer struct {
	client HTTPClient
	engine *liquid.Engine
}

type requestSpec struct {
	Method  string            `json:"method"`
	URL     string            `json:"url,omitempty"`
	Path    string            `json:"path,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    any               `json:"body,omitempty"`
}

// New builds a Renderer. If client is nil, it uses http.DefaultClient.
func New(client HTTPClient) *Renderer {
	if client == nil {
		client = http.DefaultClient
	}

	return &Renderer{
		client: client,
		engine: liquid.NewEngine(),
	}
}

// Execute renders tool.RequestTemplate into an HTTP request, executes it
// against app.UpstreamBaseURL, then renders tool.ResponseTemplate into the
// final MCP tool content string.
func (r *Renderer) Execute(ctx context.Context, app manifest.App, tool manifest.Tool, data Context) (Result, error) {
	if tool.Type != manifest.ToolTypeRendered {
		return Result{}, fmt.Errorf("tool %q: unsupported type %q", tool.Name, tool.Type)
	}
	if tool.RequestTemplate == "" {
		return Result{}, fmt.Errorf("tool %q: requestTemplate is required", tool.Name)
	}
	if tool.ResponseTemplate == "" {
		return Result{}, fmt.Errorf("tool %q: responseTemplate is required", tool.Name)
	}

	req, err := r.renderRequest(ctx, app, tool, data)
	if err != nil {
		return Result{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, req.Method, req.URL, bytes.NewReader(req.Body))
	if err != nil {
		return Result{}, fmt.Errorf("build upstream request for tool %q: %w", tool.Name, err)
	}
	httpReq.Header = req.Headers.Clone()

	httpResp, err := r.client.Do(httpReq)
	if err != nil {
		return Result{}, fmt.Errorf("execute upstream request for tool %q: %w", tool.Name, err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	renderableResp, err := renderableResponse(httpResp)
	if err != nil {
		return Result{}, fmt.Errorf("read upstream response for tool %q: %w", tool.Name, err)
	}

	content, err := r.renderResponse(tool, renderableResp, req)
	if err != nil {
		return Result{}, err
	}

	return Result{
		Request:  req,
		Response: renderableResp,
		Content:  content,
	}, nil
}

func (r *Renderer) renderRequest(_ context.Context, app manifest.App, tool manifest.Tool, data Context) (Request, error) {
	var err error

	rendered, err := r.engine.ParseAndRenderString(tool.RequestTemplate, data.bindings())
	if err != nil {
		return Request{}, fmt.Errorf("render request template for tool %q: %w", tool.Name, err)
	}

	var spec requestSpec
	if err := json.Unmarshal([]byte(rendered), &spec); err != nil {
		return Request{}, fmt.Errorf("decode rendered request template for tool %q as JSON: %w", tool.Name, err)
	}

	if strings.TrimSpace(spec.Method) == "" {
		return Request{}, fmt.Errorf("tool %q: rendered request template must set method", tool.Name)
	}

	target, err := resolveRequestURL(app.UpstreamBaseURL, spec)
	if err != nil {
		return Request{}, fmt.Errorf("resolve request URL for tool %q: %w", tool.Name, err)
	}

	body, err := encodeRequestBody(spec.Body)
	if err != nil {
		return Request{}, fmt.Errorf("encode request body for tool %q: %w", tool.Name, err)
	}

	headers := make(http.Header, len(spec.Headers))
	for name, value := range spec.Headers {
		headers.Set(name, value)
	}

	return Request{
		Method:  strings.ToUpper(spec.Method),
		URL:     target,
		Headers: headers,
		Body:    body,
	}, nil
}

func (r *Renderer) renderResponse(tool manifest.Tool, response Response, request Request) (string, error) {
	var err error

	rendered, err := r.engine.ParseAndRenderString(tool.ResponseTemplate, liquid.Bindings{
		"request":  request.bindings(),
		"response": response.bindings(),
	})
	if err != nil {
		return "", fmt.Errorf("render response template for tool %q: %w", tool.Name, err)
	}

	return rendered, nil
}

func (c Context) bindings() liquid.Bindings {
	args := c.Args
	if args == nil {
		args = map[string]any{}
	}

	return liquid.Bindings{
		"args":   args,
		"caller": c.Caller,
	}
}

func (r Request) bindings() liquid.Bindings {
	return liquid.Bindings{
		"method":  r.Method,
		"url":     r.URL,
		"headers": flattenHeaders(r.Headers),
		"body":    string(r.Body),
	}
}

func (r Response) bindings() liquid.Bindings {
	return liquid.Bindings{
		"status":     r.Status,
		"statusText": r.StatusText,
		"headers":    r.Headers,
		"body":       r.Body,
		"rawBody":    r.RawBody,
	}
}

func resolveRequestURL(baseURL string, spec requestSpec) (string, error) {
	target := spec.URL
	if target == "" {
		target = spec.Path
	}
	if strings.TrimSpace(target) == "" {
		return "", fmt.Errorf("rendered request template must set url or path")
	}

	targetURL, err := url.Parse(target)
	if err != nil {
		return "", fmt.Errorf("parse rendered URL %q: %w", target, err)
	}
	if targetURL.IsAbs() {
		return targetURL.String(), nil
	}

	if strings.TrimSpace(baseURL) == "" {
		return "", fmt.Errorf("relative path %q requires app upstreamBaseURL", target)
	}

	base, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parse app upstreamBaseURL %q: %w", baseURL, err)
	}

	return base.ResolveReference(targetURL).String(), nil
}

func encodeRequestBody(body any) ([]byte, error) {
	if body == nil {
		return nil, nil
	}

	switch v := body.(type) {
	case string:
		return []byte(v), nil
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		return encoded, nil
	}
}

func renderableResponse(resp *http.Response) (Response, error) {
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return Response{}, err
	}

	rawBody := string(bodyBytes)
	body := any(rawBody)
	if len(bodyBytes) > 0 {
		var parsed any
		if err := json.Unmarshal(bodyBytes, &parsed); err == nil {
			body = parsed
		}
	}

	return Response{
		Status:     resp.StatusCode,
		StatusText: http.StatusText(resp.StatusCode),
		Headers:    flattenHeaders(resp.Header),
		Body:       body,
		RawBody:    rawBody,
	}, nil
}

func flattenHeaders(headers http.Header) map[string]string {
	flat := make(map[string]string, len(headers))
	for name, values := range headers {
		flat[name] = strings.Join(values, ", ")
	}
	return flat
}
