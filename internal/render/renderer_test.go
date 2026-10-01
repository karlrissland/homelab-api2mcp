package render

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/karlrissland/homelab-api2mcp/internal/manifest"
)

func TestRendererExecuteRoundTrip(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("request method = %q, want %q", r.Method, http.MethodGet)
		}
		if r.URL.Path != "/api/v1/user/repos" {
			t.Fatalf("request path = %q, want %q", r.URL.Path, "/api/v1/user/repos")
		}
		if got := r.URL.Query().Get("page"); got != "2" {
			t.Fatalf("page query = %q, want %q", got, "2")
		}
		if got := r.URL.Query().Get("limit"); got != "50" {
			t.Fatalf("limit query = %q, want %q", got, "50")
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Fatalf("accept header = %q, want %q", got, "application/json")
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode([]map[string]any{
			{"full_name": "octocat/hello-world", "private": false},
			{"full_name": "octocat/private-repo", "private": true},
		}); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	defer upstream.Close()

	app, tool := loadFixture(t, "gitea")
	app.UpstreamBaseURL = upstream.URL

	renderer := New(upstream.Client())
	result, err := renderer.Execute(context.Background(), app, tool, Context{
		Args: map[string]any{
			"page":  2,
			"limit": 50,
		},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.Request.Method != http.MethodGet {
		t.Fatalf("request method = %q, want %q", result.Request.Method, http.MethodGet)
	}
	if got := result.Response.Status; got != http.StatusOK {
		t.Fatalf("response status = %d, want %d", got, http.StatusOK)
	}
	wantContent := "- octocat/hello-world\n- octocat/private-repo (private)"
	if got := normalizeNewlines(strings.TrimSpace(result.Content)); got != wantContent {
		t.Fatalf("content = %q, want %q", got, wantContent)
	}
}

func TestRendererRenderRequest(t *testing.T) {
	t.Parallel()

	renderer := New(nil)

	tests := []struct {
		name        string
		app         manifest.App
		tool        manifest.Tool
		data        Context
		wantMethod  string
		wantURL     string
		wantBody    string
		wantErrText string
	}{
		{
			name: "resolves relative path against base URL",
			app: manifest.App{
				UpstreamBaseURL: "https://gitea.example.test/base/",
			},
			tool: manifest.Tool{
				Name:             "list_my_repos",
				Type:             manifest.ToolTypeRendered,
				RequestTemplate:  `{"method":"get","path":"/api/v1/user/repos?page={{ args.page }}"}`,
				ResponseTemplate: `ok`,
			},
			data: Context{
				Args: map[string]any{"page": 3},
			},
			wantMethod: http.MethodGet,
			wantURL:    "https://gitea.example.test/api/v1/user/repos?page=3",
		},
		{
			name: "serializes structured JSON body",
			app: manifest.App{
				UpstreamBaseURL: "https://gitea.example.test",
			},
			tool: manifest.Tool{
				Name:             "create_repo",
				Type:             manifest.ToolTypeRendered,
				RequestTemplate:  `{"method":"POST","path":"/api/v1/user/repos","headers":{"Content-Type":"application/json"},"body":{"name":"demo","private":true}}`,
				ResponseTemplate: `ok`,
			},
			wantMethod: http.MethodPost,
			wantURL:    "https://gitea.example.test/api/v1/user/repos",
			wantBody:   `{"name":"demo","private":true}`,
		},
		{
			name: "renders caller username from lower camel-case binding",
			app: manifest.App{
				UpstreamBaseURL: "https://gitea.example.test",
			},
			tool: manifest.Tool{
				Name:             "impersonated_call",
				Type:             manifest.ToolTypeRendered,
				RequestTemplate:  `{"method":"GET","path":"/api/v1/user/repos?page={{ args.page }}","headers":{"X-Impersonate-User":"{{ caller.username }}","X-Agent-Instance":"{{ caller.agentInstance }}","X-App-Instance":"{{ caller.appInstance }}","X-Tier":"{{ caller.tier }}"}}`,
				ResponseTemplate: `ok`,
			},
			data: Context{
				Args: map[string]any{"page": 3},
				Caller: &Caller{
					AgentInstance: "hermes-alice",
					Username:      "alice",
					Tier:          manifest.TierUser,
					AppInstance:   "demo",
				},
			},
			wantMethod: http.MethodGet,
			wantURL:    "https://gitea.example.test/api/v1/user/repos?page=3",
		},
		{
			name: "rejects missing method",
			app: manifest.App{
				UpstreamBaseURL: "https://gitea.example.test",
			},
			tool: manifest.Tool{
				Name:             "broken",
				Type:             manifest.ToolTypeRendered,
				RequestTemplate:  `{"path":"/api/v1/user/repos"}`,
				ResponseTemplate: `ok`,
			},
			wantErrText: "must set method",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req, err := renderer.renderRequest(context.Background(), tt.app, tt.tool, tt.data)
			if tt.wantErrText != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErrText) {
					t.Fatalf("renderRequest() error = %v, want substring %q", err, tt.wantErrText)
				}
				return
			}
			if err != nil {
				t.Fatalf("renderRequest() error = %v", err)
			}

			if req.Method != tt.wantMethod {
				t.Fatalf("method = %q, want %q", req.Method, tt.wantMethod)
			}
			if req.URL != tt.wantURL {
				t.Fatalf("url = %q, want %q", req.URL, tt.wantURL)
			}
			if got := string(req.Body); got != tt.wantBody {
				t.Fatalf("body = %q, want %q", got, tt.wantBody)
			}
			if tt.name == "renders caller username from lower camel-case binding" {
				if got := req.Headers.Get("X-Impersonate-User"); got != "alice" {
					t.Fatalf("X-Impersonate-User = %q, want %q", got, "alice")
				}
				if got := req.Headers.Get("X-Agent-Instance"); got != "hermes-alice" {
					t.Fatalf("X-Agent-Instance = %q, want %q", got, "hermes-alice")
				}
				if got := req.Headers.Get("X-App-Instance"); got != "demo" {
					t.Fatalf("X-App-Instance = %q, want %q", got, "demo")
				}
				if got := req.Headers.Get("X-Tier"); got != "user" {
					t.Fatalf("X-Tier = %q, want %q", got, "user")
				}
			}
		})
	}
}

func TestRenderableResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		body     string
		wantRaw  string
		wantJSON bool
	}{
		{
			name:     "json body stays structured",
			body:     `{"name":"demo"}`,
			wantRaw:  `{"name":"demo"}`,
			wantJSON: true,
		},
		{
			name:     "plain text body stays text",
			body:     "not json",
			wantRaw:  "not json",
			wantJSON: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(tt.body)),
			}

			got, err := renderableResponse(resp)
			if err != nil {
				t.Fatalf("renderableResponse() error = %v", err)
			}
			if got.RawBody != tt.wantRaw {
				t.Fatalf("raw body = %q, want %q", got.RawBody, tt.wantRaw)
			}
			_, isMap := got.Body.(map[string]any)
			if isMap != tt.wantJSON {
				t.Fatalf("body structured = %t, want %t", isMap, tt.wantJSON)
			}
		})
	}
}

func loadFixture(t *testing.T, name string) (manifest.App, manifest.Tool) {
	t.Helper()

	dir := filepath.Join("testdata", name)

	manifestBytes, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest fixture: %v", err)
	}

	var app manifest.App
	if err := json.Unmarshal(manifestBytes, &app); err != nil {
		t.Fatalf("unmarshal manifest fixture: %v", err)
	}
	if len(app.Tools) != 1 {
		t.Fatalf("fixture tools = %d, want 1", len(app.Tools))
	}

	requestTemplate, err := os.ReadFile(filepath.Join(dir, "request.liquid"))
	if err != nil {
		t.Fatalf("read request fixture: %v", err)
	}
	responseTemplate, err := os.ReadFile(filepath.Join(dir, "response.liquid"))
	if err != nil {
		t.Fatalf("read response fixture: %v", err)
	}

	tool := app.Tools[0]
	tool.RequestTemplate = string(requestTemplate)
	tool.ResponseTemplate = string(responseTemplate)

	if err := tool.Validate(); err != nil {
		t.Fatalf("validate fixture tool: %v", err)
	}

	return app, tool
}

func normalizeNewlines(s string) string {
	return strings.ReplaceAll(s, "\r\n", "\n")
}
