package render

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/karlrissland/homelab-api2mcp/internal/manifest"
)

func TestRendererResponseSchema(t *testing.T) {
	for _, key := range []string{"items", "results"} {
		t.Run(key, func(t *testing.T) {
			for _, tt := range []struct {
				name    string
				body    string
				status  int
				want    string
				isError bool
			}{
				{"empty", fmt.Sprintf(`{"%s":[],"secret":"synthetic-sensitive-marker"}`, key), 200, "", false},
				{"populated", fmt.Sprintf(`{"%s":[{"name":"synthetic-example","secret":"synthetic-sensitive-marker"}]}`, key), 200, "synthetic-example", false},
				{"missing", `{"secret":"synthetic-sensitive-marker"}`, 200, "", true},
				{"null", fmt.Sprintf(`{"%s":null}`, key), 200, "", true},
				{"object", fmt.Sprintf(`{"%s":{"secret":"synthetic-sensitive-marker"}}`, key), 200, "", true},
				{"string", fmt.Sprintf(`{"%s":"synthetic-sensitive-marker"}`, key), 200, "", true},
				{"number", fmt.Sprintf(`{"%s":17}`, key), 200, "", true},
				{"boolean", fmt.Sprintf(`{"%s":true}`, key), 200, "", true},
				{"null root", `null`, 200, "", true},
				{"array root", `[]`, 200, "", true},
				{"string root", `"synthetic-sensitive-marker"`, 200, "", true},
				{"number root", `17`, 200, "", true},
				{"boolean root", `true`, 200, "", true},
				{"malformed JSON", `{"secret":"synthetic-sensitive-marker"`, 200, "", true},
				{"empty body", ``, 200, "", true},
				{"trailing JSON", fmt.Sprintf(`{"%s":[]} {"secret":"synthetic-sensitive-marker"}`, key), 200, "", true},
				{"no content", ``, 204, "", true},
				{"HTTP error", `{"secret":"synthetic-sensitive-marker"}`, 403, "Upstream HTTP 403", true},
				{"HTTP error with valid collection", fmt.Sprintf(`{"%s":[]}`, key), 500, "Upstream HTTP 500", true},
			} {
				t.Run(tt.name, func(t *testing.T) {
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.Method != http.MethodGet || r.URL.Path != "/synthetic/"+key {
							t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
						}
						w.Header().Set("X-Synthetic-Secret", "synthetic-sensitive-marker")
						w.WriteHeader(tt.status)
						if _, err := io.WriteString(w, tt.body); err != nil {
							t.Error(err)
						}
					}))
					defer upstream.Close()
					tool := manifest.Tool{
						Name: "read", Tier: manifest.TierUser, Type: manifest.ToolTypeRendered,
						RequestTemplate:  fmt.Sprintf(`{"method":"GET","path":"/synthetic/%s","headers":{"Authorization":"{{ credential }}"}}`, key),
						ResponseTemplate: fmt.Sprintf(`{%s for item in response.body.%s %s}{{ item.name }}{%s endfor %s}`, "%", key, "%", "%", "%"),
						ResponseSchema: map[string]any{
							"type": "object", "required": []any{key},
							"properties": map[string]any{key: map[string]any{"type": "array"}},
						},
					}
					if err := tool.Validate(); err != nil {
						t.Fatal(err)
					}
					result, err := New(upstream.Client()).Execute(context.Background(), manifest.App{UpstreamBaseURL: upstream.URL}, tool, Context{Credential: "synthetic-credential-marker"})
					if err != nil {
						t.Fatal(err)
					}
					if result.IsError != tt.isError || result.Response.Status != tt.status {
						t.Fatalf("error=%t status=%d, want error=%t status=%d", result.IsError, result.Response.Status, tt.isError, tt.status)
					}
					if tt.isError && result.Content == "" {
						t.Fatal("validation failure had empty content")
					}
					if (!tt.isError || tt.want != "") && result.Content != tt.want {
						t.Fatalf("content=%q, want %q", result.Content, tt.want)
					}
					for _, marker := range []string{"synthetic-sensitive-marker", "synthetic-credential-marker"} {
						if strings.Contains(result.Content, marker) {
							t.Fatal("response content exposed a sensitive marker")
						}
					}
					if tt.isError && (result.Response.Body != nil || result.Response.RawBody != "" || result.Response.Headers != nil) {
						t.Fatal("validation failure retained the upstream payload")
					}
				})
			}
		})
	}
}

func TestRendererResponseSchemaIsOptIn(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := io.WriteString(w, `not JSON`); err != nil {
			t.Error(err)
		}
	}))
	defer upstream.Close()
	tool := manifest.Tool{Name: "read", Type: manifest.ToolTypeRendered, RequestTemplate: `{"method":"GET","path":"/"}`, ResponseTemplate: `{{ response.rawBody }}`}
	renderer := New(upstream.Client())
	result, err := renderer.Execute(context.Background(), manifest.App{UpstreamBaseURL: upstream.URL}, tool, Context{})
	if err != nil || result.IsError || result.Content != "not JSON" {
		t.Fatalf("legacy behavior changed: content=%q, error=%v", result.Content, err)
	}
	tool.ResponseSchema = map[string]any{}
	result, err = renderer.Execute(context.Background(), manifest.App{UpstreamBaseURL: upstream.URL}, tool, Context{})
	if err != nil || !result.IsError {
		t.Fatalf("empty schema did not require JSON: error=%v", err)
	}
}
