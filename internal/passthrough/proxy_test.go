package passthrough

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/karlrissland/homelab-api2mcp/internal/manifest"
)

func TestProxyPreservesNativeTraffic(t *testing.T) {
	for _, tt := range []struct {
		name, method, request, response string
		status                          int
	}{
		{"discovery", http.MethodPost, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"cursor":"next"}}`,
			`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"native","description":"Authoritative","inputSchema":{"type":"object","required":["name"],"properties":{"name":{"type":"string"}}},"outputSchema":{"type":"object"},"annotations":{"readOnlyHint":true},"_meta":{"extension":true}}],"nextCursor":"more"}}`, http.StatusOK},
		{"native error content", http.MethodPost, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"native","arguments":{"name":"demo"}}}`,
			`{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"refused"},{"type":"image","data":"aGVsbG8=","mimeType":"image/png"}],"structuredContent":{"reason":"refused"},"isError":true,"_meta":{"native":true}}}`, http.StatusOK},
		{"protocol error", http.MethodPost, `{"jsonrpc":"2.0","id":3,"method":"extension/method"}`,
			`{"jsonrpc":"2.0","id":3,"error":{"code":-32601,"message":"Unsupported native method","data":{"detail":true}}}`, http.StatusOK},
		{"cancellation", http.MethodPost, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":2,"reason":"cancelled"}}`, "", http.StatusAccepted},
		{"session deletion", http.MethodDelete, "", "", http.StatusNoContent},
		{"upstream HTTP refusal", http.MethodPost, `{"jsonrpc":"2.0","id":4,"method":"tools/list"}`, `{"error":"unauthorized"}`, http.StatusUnauthorized},
	} {
		t.Run(tt.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != tt.method || r.URL.RequestURI() != "/api/mcp?configured=yes&client=next" {
					t.Errorf("upstream request = %s %s", r.Method, r.URL.RequestURI())
				}
				if r.Header.Get("Authorization") != "Bearer upstream-token" || r.Header.Get("Cookie") != "" || r.Header.Get("Proxy-Authorization") != "" {
					t.Error("upstream authentication was not isolated from client credentials")
				}
				if r.Header.Get("Mcp-Session-Id") != "native-session" || r.Header.Get("Mcp-Protocol-Version") != "2025-11-25" || r.Header.Get("Last-Event-Id") != "event-7" {
					t.Error("MCP transport headers were not preserved")
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				if string(body) != tt.request {
					t.Errorf("request body = %q, want %q", body, tt.request)
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Mcp-Session-Id", "native-session")
				w.Header().Set("WWW-Authenticate", `Bearer realm="upstream"`)
				w.WriteHeader(tt.status)
				if _, err := io.WriteString(w, tt.response); err != nil {
					t.Error(err)
				}
			}))
			defer upstream.Close()
			proxy := NewProxy(upstream.Client().Transport, slog.New(slog.NewTextHandler(io.Discard, nil)))
			app := manifest.App{Name: "native", UpstreamMCPURL: upstream.URL + "/api/mcp?configured=yes", UpstreamCredentialEnv: "token"}
			req := httptest.NewRequest(tt.method, "/native/mcp?client=next", strings.NewReader(tt.request))
			req.Header.Set("Authorization", "Bearer gateway-key")
			req.Header.Set("Cookie", "gateway-session=private")
			req.Header.Set("Proxy-Authorization", "Basic private")
			req.Header.Set("Connection", "Authorization")
			req.Header.Set("Mcp-Session-Id", "native-session")
			req.Header.Set("Mcp-Protocol-Version", "2025-11-25")
			req.Header.Set("Last-Event-Id", "event-7")
			rec := httptest.NewRecorder()
			proxy.Forward(rec, req, app, "upstream-token")
			if rec.Code != tt.status || rec.Body.String() != tt.response {
				t.Fatalf("response = %d %q, want %d %q", rec.Code, rec.Body.String(), tt.status, tt.response)
			}
			if rec.Header().Get("Mcp-Session-Id") != "native-session" || rec.Header().Get("WWW-Authenticate") != `Bearer realm="upstream"` {
				t.Fatal("upstream session/challenge headers were not preserved")
			}
		})
	}
}

func TestProxyStreamsAndCancelsUpstream(t *testing.T) {
	done := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		w.Header().Set("Content-Type", "text/event-stream")
		if _, err := io.WriteString(w, "id: event-8\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\"}\n\n"); err != nil {
			t.Error(err)
			return
		}
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer upstream.Close()
	proxy := NewProxy(upstream.Client().Transport, nil)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxy.Forward(w, r, manifest.App{Name: "native", UpstreamMCPURL: upstream.URL + "/api/mcp"}, "")
	}))
	defer gateway.Close()
	defer upstream.CloseClientConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gateway.URL+"/native/mcp", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := gateway.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(resp.Body)
	line, readErr := reader.ReadString('\n')
	if err := resp.Body.Close(); err != nil {
		t.Error(err)
	}
	cancel()
	if readErr != nil || line != "id: event-8\n" {
		t.Fatalf("streamed first event = %q, error = %v", line, readErr)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("client disconnect did not cancel upstream stream")
	}
}

func TestProxyRejectsMissingOrInvalidCredential(t *testing.T) {
	for _, credential := range []string{"", "invalid\r\nheader", "contains space"} {
		t.Run(credential, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				calls.Add(1)
			}))
			defer upstream.Close()
			var logs bytes.Buffer
			proxy := NewProxy(nil, slog.New(slog.NewTextHandler(&logs, nil)))
			rec := httptest.NewRecorder()
			proxy.Forward(rec, httptest.NewRequest(http.MethodPost, "/native/mcp", nil), manifest.App{
				Name: "native", UpstreamMCPURL: upstream.URL + "/mcp", UpstreamCredentialEnv: "token",
			}, credential)
			if rec.Code != http.StatusServiceUnavailable || calls.Load() != 0 {
				t.Fatalf("status=%d, upstream calls=%d", rec.Code, calls.Load())
			}
			if credential != "" && strings.Contains(logs.String()+rec.Body.String(), credential) {
				t.Fatal("credential was exposed in diagnostics")
			}
		})
	}
}

func TestProxyDoesNotFollowRedirectsOrReplayAmbiguousPOST(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.Header().Set("Location", "/elsewhere")
			w.WriteHeader(http.StatusTemporaryRedirect)
		case 2:
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			if err := conn.Close(); err != nil {
				t.Error(err)
			}
		default:
			t.Error("proxy retried or followed a redirect")
		}
	}))
	defer upstream.Close()
	proxy := NewProxy(upstream.Client().Transport, slog.New(slog.NewTextHandler(io.Discard, nil)))
	app := manifest.App{Name: "native", UpstreamMCPURL: upstream.URL + "/mcp"}
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusBadGateway} {
		req := httptest.NewRequest(http.MethodPost, "/native/mcp", nil)
		req.Header.Set("Idempotency-Key", "must-not-enable-replay")
		rec := httptest.NewRecorder()
		proxy.Forward(rec, req, app, "")
		if rec.Code != status {
			t.Fatalf("status = %d, want %d", rec.Code, status)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("upstream calls = %d, want 2", calls.Load())
	}
}

func TestProxyTransportErrorDoesNotExposeCredentials(t *testing.T) {
	var logs bytes.Buffer
	proxy := NewProxy(proxyRoundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("transport detail including upstream-token")
	}), slog.New(slog.NewTextHandler(&logs, nil)))
	rec := httptest.NewRecorder()
	proxy.Forward(rec, httptest.NewRequest(http.MethodPost, "/native/mcp", nil), manifest.App{
		Name: "native", UpstreamMCPURL: "http://native.invalid/mcp", UpstreamCredentialEnv: "token",
	}, "upstream-token")
	if rec.Code != http.StatusBadGateway || strings.Contains(logs.String()+rec.Body.String(), "upstream-token") {
		t.Fatal("transport failure was not safely reported")
	}
}

type proxyRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f proxyRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
