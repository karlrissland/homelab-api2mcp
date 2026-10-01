package discovery

import (
	"context"
	"testing"
	"time"

	"github.com/karlrissland/homelab-api2mcp/internal/manifest"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestBuildConfigMapRoundTrip(t *testing.T) {
	t.Parallel()

	app := manifest.App{
		Name:            "demo",
		UpstreamBaseURL: "https://demo.example.invalid",
		Tools: []manifest.Tool{
			{
				Name:             "list_repos",
				Description:      "List repos",
				Tier:             manifest.TierUser,
				Type:             manifest.ToolTypeRendered,
				RequestTemplate:  `{"method":"GET","path":"/repos"}`,
				ResponseTemplate: `{{ response.rawBody }}`,
			},
		},
	}

	cm, err := BuildConfigMap("apps", "demo-tools", app)
	if err != nil {
		t.Fatalf("BuildConfigMap() error = %v", err)
	}

	parsed, err := ParseConfigMap(cm)
	if err != nil {
		t.Fatalf("ParseConfigMap() error = %v", err)
	}
	if parsed.Name != app.Name || parsed.Namespace != "apps" || len(parsed.Tools) != 1 {
		t.Fatalf("round-trip app = %+v, want name=%q namespace=%q and one tool", parsed, app.Name, "apps")
	}
}

func TestTableReflectsConfigMapLifecycle(t *testing.T) {
	client := fake.NewSimpleClientset()
	table, err := New(client)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	configMap := newValidConfigMap("default", "gitea-tools", "gitea")
	if _, err := client.CoreV1().ConfigMaps(configMap.Namespace).Create(ctx, configMap, metav1.CreateOptions{}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	runErr := make(chan error, 1)
	go func() {
		runErr <- table.Run(ctx)
	}()

	eventually(t, 3*time.Second, func() bool {
		app, ok := table.Get("gitea")
		if !ok {
			return false
		}
		return app.Namespace == "default" && len(app.Tools) == 1 && app.Tools[0].Name == "list_repos"
	})

	apps := table.List()
	if len(apps) != 1 || apps[0].Name != "gitea" {
		t.Fatalf("List() = %#v, want one gitea app", apps)
	}

	apps[0].Tools[0].Name = "mutated"
	again, ok := table.Get("gitea")
	if !ok {
		t.Fatal("Get(gitea) unexpectedly missing after mutation test")
	}
	if again.Tools[0].Name != "list_repos" {
		t.Fatalf("Get() returned shared mutable state, tool name = %q", again.Tools[0].Name)
	}

	if err := client.CoreV1().ConfigMaps(configMap.Namespace).Delete(ctx, configMap.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	eventually(t, 3*time.Second, func() bool {
		_, ok := table.Get("gitea")
		return !ok
	})

	cancel()
	if err := <-runErr; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestTableIgnoresConfigMapsWithoutDiscoveryLabel(t *testing.T) {
	client := fake.NewSimpleClientset()
	table, err := New(client)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErr := make(chan error, 1)
	go func() {
		runErr <- table.Run(ctx)
	}()

	configMap := newValidConfigMap("default", "ignored-tools", "ignored")
	delete(configMap.Labels, AppLabelKey)

	if _, err := client.CoreV1().ConfigMaps(configMap.Namespace).Create(ctx, configMap, metav1.CreateOptions{}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	consistently(t, 500*time.Millisecond, func() bool {
		_, ok := table.Get("ignored")
		return !ok
	})

	cancel()
	if err := <-runErr; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestTableRejectsInvalidManifestAndKeepsRunning(t *testing.T) {
	client := fake.NewSimpleClientset()
	table, err := New(client)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErr := make(chan error, 1)
	go func() {
		runErr <- table.Run(ctx)
	}()

	invalid := newValidConfigMap("default", "broken-tools", "broken")
	invalid.Data["manifest.yaml"] = `name: broken
tools:
  - name: broken
    type: rendered
    tier: user
`

	if _, err := client.CoreV1().ConfigMaps(invalid.Namespace).Create(ctx, invalid, metav1.CreateOptions{}); err != nil {
		t.Fatalf("Create invalid configmap error = %v", err)
	}

	consistently(t, 500*time.Millisecond, func() bool {
		_, ok := table.Get("broken")
		return !ok
	})

	valid := newValidConfigMap("default", "gitea-tools", "gitea")
	if _, err := client.CoreV1().ConfigMaps(valid.Namespace).Create(ctx, valid, metav1.CreateOptions{}); err != nil {
		t.Fatalf("Create valid configmap error = %v", err)
	}

	eventually(t, 3*time.Second, func() bool {
		_, ok := table.Get("gitea")
		return ok
	})

	cancel()
	if err := <-runErr; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func newValidConfigMap(namespace, name, appName string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
			Labels: map[string]string{
				AppLabelKey: appName,
			},
		},
		Data: map[string]string{
			"manifest.yaml": `name: ` + appName + `
upstreamBaseURL: https://gitea.example.invalid/api/v1
upstreamCredentialEnv: GITEA_TOKEN
tools:
  - name: list_repos
    description: List repositories
    tier: user
    type: rendered
    inputSchema:
      type: object
      properties:
        page:
          type: integer
    requestTemplate: |
      {"method":"GET","path":"/user/repos"}
    responseTemplate: |
      {{ upstream.body }}
`,
		},
	}
}

func eventually(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}

	t.Fatal("condition not met before timeout")
}

func consistently(t *testing.T, duration time.Duration, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		if !condition() {
			t.Fatal("condition became false before duration elapsed")
		}
		time.Sleep(25 * time.Millisecond)
	}
}
