package passthrough

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/karlrissland/homelab-api2mcp/internal/manifest"
)

// Proxy forwards native MCP HTTP traffic without interpreting protocol bodies.
type Proxy struct {
	transport http.RoundTripper
	logger    *slog.Logger
}

// NewProxy builds an endpoint proxy. A nil transport uses http.DefaultTransport.
func NewProxy(transport http.RoundTripper, logger *slog.Logger) *Proxy {
	if transport == nil {
		transport = http.DefaultTransport
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Proxy{transport: transport, logger: logger}
}

// Forward proxies one request with the server-resolved upstream bearer credential.
// Gateway authentication must be checked by the caller before forwarding.
func (p *Proxy) Forward(w http.ResponseWriter, req *http.Request, app manifest.App, credential string) {
	if !app.IsNativeMCP() || app.Validate() != nil {
		p.logger.Error("native MCP endpoint configuration is invalid", "app", app.Name)
		http.Error(w, "native MCP endpoint configuration is invalid", http.StatusServiceUnavailable)
		return
	}
	if app.UpstreamCredentialEnv != "" && credential == "" {
		p.logger.Error("native MCP upstream credential is unavailable", "app", app.Name)
		http.Error(w, "upstream credential unavailable; provision the referenced Secret and call reload_cache", http.StatusServiceUnavailable)
		return
	}
	for _, c := range credential {
		if c < '!' || c > '~' {
			p.logger.Error("native MCP upstream bearer credential is invalid", "app", app.Name)
			http.Error(w, "upstream bearer credential is invalid", http.StatusServiceUnavailable)
			return
		}
	}

	// Validate above guarantees this URL can be parsed without exposing its
	// contents in an error response or log.
	target, err := url.Parse(app.UpstreamMCPURL)
	if err != nil {
		p.logger.Error("native MCP endpoint URL could not be parsed", "app", app.Name)
		http.Error(w, "native MCP endpoint configuration is invalid", http.StatusServiceUnavailable)
		return
	}
	proxy := &httputil.ReverseProxy{
		Transport:     p.transport,
		FlushInterval: -1,
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL.Scheme = target.Scheme
			r.Out.URL.Host = target.Host
			r.Out.URL.Path = target.Path
			r.Out.URL.RawPath = target.RawPath
			r.Out.URL.RawQuery = target.RawQuery
			if r.In.URL.RawQuery != "" {
				if r.Out.URL.RawQuery != "" {
					r.Out.URL.RawQuery += "&"
				}
				r.Out.URL.RawQuery += r.In.URL.RawQuery
			}
			r.Out.Host = target.Host
			r.Out.Header.Del("Authorization")
			r.Out.Header.Del("Cookie")
			if credential != "" {
				r.Out.Header.Set("Authorization", "Bearer "+credential)
			}
			// A streaming request body must never become replayable, even
			// when the client supplies an Idempotency-Key on an empty POST.
			r.Out.GetBody = nil
			if r.Out.Method != http.MethodGet && r.Out.Method != http.MethodHead && (r.Out.Body == nil || r.Out.Body == http.NoBody) {
				r.Out.Body = io.NopCloser(strings.NewReader(""))
			}
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, _ error) {
			if r.Context().Err() != nil {
				return
			}
			p.logger.Error("native MCP upstream request failed", "app", app.Name, "method", r.Method)
			http.Error(w, "native MCP upstream request failed", http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, req)
}
