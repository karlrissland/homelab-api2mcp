package skillstools

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"

	"github.com/karlrissland/homelab-api2mcp/internal/keys"
	"github.com/karlrissland/homelab-api2mcp/internal/manifest"
	"github.com/karlrissland/homelab-api2mcp/internal/pipeline"
)

func TestListSkillsReturnsOutOfBandSkills(t *testing.T) {
	t.Parallel()

	api, _ := newTestAPI(t,
		testSkillObject("apps", "alpha", SkillSpec{
			SkillName:   "Alpha overview",
			Description: "Alpha description",
			Domain:      "alpha",
			Type:        "parent",
			Confidence:  "high",
			Source:      "earned",
			Content:     "# Alpha",
		}, map[string]string{"skills.homelab.dev/topic": "overview"}),
		testSkillObject("default", "beta", SkillSpec{
			SkillName:   "Beta API",
			Description: "Beta api description",
			Domain:      "beta, api",
			Type:        "child",
			Parent:      "beta-overview",
			Confidence:  "medium",
			Source:      "earned",
			Content:     "# Beta",
		}, nil),
	)

	result, err := api.ListSkills(context.Background(), "")
	if err != nil {
		t.Fatalf("ListSkills() error = %v", err)
	}

	if got, want := len(result.Skills), 2; got != want {
		t.Fatalf("ListSkills() returned %d skills, want %d", got, want)
	}
	if result.Skills[0].Namespace != "apps" || result.Skills[0].Name != "alpha" {
		t.Fatalf("ListSkills()[0] = %+v, want apps/alpha first", result.Skills[0])
	}
	if result.Skills[1].Namespace != "default" || result.Skills[1].Name != "beta" {
		t.Fatalf("ListSkills()[1] = %+v, want default/beta second", result.Skills[1])
	}
	if result.Skills[0].Description == "" || result.Skills[0].Domain == "" || result.Skills[0].SkillName == "" {
		t.Fatalf("ListSkills()[0] = %+v, want populated summary fields", result.Skills[0])
	}
}

func TestGetSkillReturnsOneAndNotFound(t *testing.T) {
	t.Parallel()

	api, _ := newTestAPI(t, testSkillObject("apps", "alpha", SkillSpec{
		SkillName:   "Alpha overview",
		Description: "Alpha description",
		Domain:      "alpha",
		Type:        "parent",
		Confidence:  "high",
		Source:      "earned",
		Content:     "# Alpha",
	}, nil))

	got, err := api.GetSkill(context.Background(), GetSkillParams{Name: "alpha"}, "")
	if err != nil {
		t.Fatalf("GetSkill(alpha) error = %v", err)
	}
	if got.Skill.Name != "alpha" || got.Skill.Namespace != "apps" {
		t.Fatalf("GetSkill(alpha) = %+v, want apps/alpha", got.Skill)
	}
	if got.Skill.Spec.Content != "# Alpha" {
		t.Fatalf("GetSkill(alpha).Spec.Content = %q, want %q", got.Skill.Spec.Content, "# Alpha")
	}

	_, err = api.GetSkill(context.Background(), GetSkillParams{Name: "missing"}, "")
	if err == nil {
		t.Fatal("GetSkill(missing) error = nil, want not found error")
	}
	if !strings.Contains(err.Error(), `skill "missing" not found`) {
		t.Fatalf("GetSkill(missing) error = %v, want clear not found error", err)
	}
}

func TestListSkillsAppScopeFiltersToOwnAppPlusGlobal(t *testing.T) {
	t.Parallel()

	api, _ := newTestAPI(t,
		testSkillObject("apps", "metube-api", SkillSpec{
			SkillName:   "MeTube API",
			Description: "MeTube API description",
			Domain:      "metube",
			Type:        "parent",
			Confidence:  "high",
			Source:      "earned",
			Content:     "# MeTube API",
		}, map[string]string{"skills.homelab.dev/app": "metube"}),
		testSkillObject("apps", "penpot-api", SkillSpec{
			SkillName:   "Penpot API",
			Description: "Penpot API description",
			Domain:      "penpot",
			Type:        "parent",
			Confidence:  "high",
			Source:      "earned",
			Content:     "# Penpot API",
		}, map[string]string{"skills.homelab.dev/app": "penpot"}),
		testSkillObject("default", "mcp2rest-usage", SkillSpec{
			SkillName:   "mcp2rest usage",
			Description: "How to use mcp2rest",
			Domain:      "mcp2rest",
			Type:        "parent",
			Confidence:  "high",
			Source:      "earned",
			Content:     "# mcp2rest",
		}, map[string]string{"skills.homelab.dev/topic": "overview"}),
	)

	result, err := api.ListSkills(context.Background(), "metube")
	if err != nil {
		t.Fatalf("ListSkills(metube) error = %v", err)
	}

	names := make([]string, 0, len(result.Skills))
	for _, skill := range result.Skills {
		names = append(names, skill.Name)
	}
	if got, want := strings.Join(names, ","), "metube-api,mcp2rest-usage"; got != want {
		t.Fatalf("ListSkills(metube) names = %q, want %q (own app + global, never another app's)", got, want)
	}
}

func TestGetSkillAppScopeRejectsOtherApp(t *testing.T) {
	t.Parallel()

	api, _ := newTestAPI(t,
		testSkillObject("apps", "metube-api", SkillSpec{
			SkillName:   "MeTube API",
			Description: "MeTube API description",
			Domain:      "metube",
			Type:        "parent",
			Confidence:  "high",
			Source:      "earned",
			Content:     "# MeTube API",
		}, map[string]string{"skills.homelab.dev/app": "metube"}),
		testSkillObject("apps", "penpot-api", SkillSpec{
			SkillName:   "Penpot API",
			Description: "Penpot API description",
			Domain:      "penpot",
			Type:        "parent",
			Confidence:  "high",
			Source:      "earned",
			Content:     "# Penpot API",
		}, map[string]string{"skills.homelab.dev/app": "penpot"}),
	)

	// Own app's skill: visible, both with and without an explicit namespace.
	if _, err := api.GetSkill(context.Background(), GetSkillParams{Name: "metube-api"}, "metube"); err != nil {
		t.Fatalf("GetSkill(metube-api, scope=metube) error = %v, want success", err)
	}
	if _, err := api.GetSkill(context.Background(), GetSkillParams{Name: "metube-api", Namespace: "apps"}, "metube"); err != nil {
		t.Fatalf("GetSkill(metube-api, ns=apps, scope=metube) error = %v, want success", err)
	}

	// Another app's skill: must be reported not-found, not leaked, from either lookup path.
	if _, err := api.GetSkill(context.Background(), GetSkillParams{Name: "penpot-api"}, "metube"); err == nil {
		t.Fatal("GetSkill(penpot-api, scope=metube) error = nil, want not found")
	} else if !strings.Contains(err.Error(), `skill "penpot-api" not found`) {
		t.Fatalf("GetSkill(penpot-api, scope=metube) error = %v, want clear not found error", err)
	}
	if _, err := api.GetSkill(context.Background(), GetSkillParams{Name: "penpot-api", Namespace: "apps"}, "metube"); err == nil {
		t.Fatal("GetSkill(penpot-api, ns=apps, scope=metube) error = nil, want not found")
	} else if !strings.Contains(err.Error(), `skill "penpot-api" not found`) {
		t.Fatalf("GetSkill(penpot-api, ns=apps, scope=metube) error = %v, want clear not found error", err)
	}
}

func TestRegisterReadToolsNamesAreAppScoped(t *testing.T) {
	t.Parallel()

	api, _ := newTestAPI(t)

	server := mcp.NewServer(&mcp.Implementation{Name: "skills-test", Version: "0.0.0"}, nil)
	api.RegisterReadTools(server, "", "MeTube")
	session := connectSession(t, server)
	defer func() { _ = session.Close() }()

	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	names := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	if got, want := strings.Join(names, ","), "get_metube_skill,list_metube_skills"; got != want {
		t.Fatalf("RegisterReadTools(appScope=MeTube) tool names = %q, want %q", got, want)
	}

	managementServer := mcp.NewServer(&mcp.Implementation{Name: "skills-test", Version: "0.0.0"}, nil)
	api.RegisterReadTools(managementServer, "", "")
	managementSession := connectSession(t, managementServer)
	defer func() { _ = managementSession.Close() }()

	managementTools, err := managementSession.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	managementNames := make([]string, 0, len(managementTools.Tools))
	for _, tool := range managementTools.Tools {
		managementNames = append(managementNames, tool.Name)
	}
	sort.Strings(managementNames)
	if got, want := strings.Join(managementNames, ","), "get_skill,list_skills"; got != want {
		t.Fatalf("RegisterReadTools(appScope=\"\") tool names = %q, want %q", got, want)
	}
}

func TestWriteToolsRequireAdminAndMutate(t *testing.T) {
	t.Parallel()

	api, creds := newTestAPI(t)
	adminSession := connectSession(t, registerWriteServer(t, api, creds.adminKey))
	defer func() { _ = adminSession.Close() }()
	userSession := connectSession(t, registerWriteServer(t, api, creds.userKey))
	defer func() { _ = userSession.Close() }()

	createArgs := map[string]any{
		"name":      "alpha",
		"namespace": "apps",
		"labels":    map[string]any{"skills.homelab.dev/topic": "overview"},
		"spec": map[string]any{
			"skillName":   "Alpha overview",
			"description": "Alpha description",
			"domain":      "alpha",
			"type":        "parent",
			"confidence":  "high",
			"source":      "earned",
			"content":     "# Alpha",
		},
	}

	userCreate, err := userSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "create_skill", Arguments: createArgs})
	if err != nil {
		t.Fatalf("CallTool(create_skill,user) protocol error = %v", err)
	}
	if !userCreate.IsError {
		t.Fatal("CallTool(create_skill,user) IsError = false, want true")
	}
	_, err = api.GetSkill(context.Background(), GetSkillParams{Name: "alpha"}, "")
	if err == nil || !strings.Contains(err.Error(), `skill "alpha" not found`) {
		t.Fatalf("GetSkill(alpha) after rejected create = %v, want not found", err)
	}

	adminCreate, err := adminSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "create_skill", Arguments: createArgs})
	if err != nil {
		t.Fatalf("CallTool(create_skill,admin) protocol error = %v", err)
	}
	if adminCreate.IsError {
		t.Fatalf("CallTool(create_skill,admin) tool error: %s", textContent(adminCreate))
	}

	created := decodeStructured[CreateSkillResult](t, adminCreate.StructuredContent)
	if created.Skill.Name != "alpha" || created.Skill.Namespace != "apps" {
		t.Fatalf("create_skill structured result = %+v, want apps/alpha", created.Skill)
	}

	updateArgs := map[string]any{
		"name":      "alpha",
		"namespace": "apps",
		"labels":    map[string]any{"skills.homelab.dev/topic": "api"},
		"spec": map[string]any{
			"skillName":   "Alpha API",
			"description": "Updated description",
			"domain":      "alpha, api",
			"type":        "parent",
			"confidence":  "high",
			"source":      "earned",
			"content":     "# Updated",
		},
	}

	userUpdate, err := userSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "update_skill", Arguments: updateArgs})
	if err != nil {
		t.Fatalf("CallTool(update_skill,user) protocol error = %v", err)
	}
	if !userUpdate.IsError {
		t.Fatal("CallTool(update_skill,user) IsError = false, want true")
	}
	beforeUpdate, err := api.GetSkill(context.Background(), GetSkillParams{Name: "alpha", Namespace: "apps"}, "")
	if err != nil {
		t.Fatalf("GetSkill(alpha/apps) error = %v", err)
	}
	if beforeUpdate.Skill.Spec.Description != "Alpha description" {
		t.Fatalf("GetSkill(alpha/apps).Spec.Description = %q after rejected update, want original value", beforeUpdate.Skill.Spec.Description)
	}

	adminUpdate, err := adminSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "update_skill", Arguments: updateArgs})
	if err != nil {
		t.Fatalf("CallTool(update_skill,admin) protocol error = %v", err)
	}
	if adminUpdate.IsError {
		t.Fatalf("CallTool(update_skill,admin) tool error: %s", textContent(adminUpdate))
	}

	updated := decodeStructured[UpdateSkillResult](t, adminUpdate.StructuredContent)
	if updated.Skill.Spec.Description != "Updated description" || updated.Skill.Spec.Content != "# Updated" {
		t.Fatalf("update_skill structured result = %+v, want updated spec", updated.Skill)
	}

	userDelete, err := userSession.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "delete_skill",
		Arguments: map[string]any{"name": "alpha", "namespace": "apps"},
	})
	if err != nil {
		t.Fatalf("CallTool(delete_skill,user) protocol error = %v", err)
	}
	if !userDelete.IsError {
		t.Fatal("CallTool(delete_skill,user) IsError = false, want true")
	}
	_, err = api.GetSkill(context.Background(), GetSkillParams{Name: "alpha", Namespace: "apps"}, "")
	if err != nil {
		t.Fatalf("GetSkill(alpha/apps) after rejected delete error = %v, want skill to remain", err)
	}

	adminDelete, err := adminSession.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "delete_skill",
		Arguments: map[string]any{"name": "alpha", "namespace": "apps"},
	})
	if err != nil {
		t.Fatalf("CallTool(delete_skill,admin) protocol error = %v", err)
	}
	if adminDelete.IsError {
		t.Fatalf("CallTool(delete_skill,admin) tool error: %s", textContent(adminDelete))
	}

	_, err = api.GetSkill(context.Background(), GetSkillParams{Name: "alpha", Namespace: "apps"}, "")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("GetSkill(alpha/apps) after delete = %v, want not found", err)
	}
}

func TestDeleteSkillReturnsNotFound(t *testing.T) {
	t.Parallel()

	api, _ := newTestAPI(t)

	_, err := api.DeleteSkill(context.Background(), DeleteSkillParams{Name: "missing", Namespace: "apps"})
	if err == nil {
		t.Fatal("DeleteSkill(missing) error = nil, want not found")
	}
}

type testCredentials struct {
	adminKey string
	userKey  string
}

func TestRequireAdminAllowsEmptyKeyWhenAuthDisabled(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	client := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		skillGVR: Kind + "List",
	})

	store := keys.NewStore()
	store.DisableAuth()

	api, err := New(client, func(key string) (pipeline.KeyRecord, bool) {
		record, ok := store.Lookup(key)
		if !ok {
			return pipeline.KeyRecord{}, false
		}
		return pipeline.KeyRecord{
			AgentInstance: record.AgentInstance,
			Tier:          record.Tier,
		}, true
	})
	if err != nil {
		t.Fatalf("skillstools.New() error = %v", err)
	}

	// Regression test: requireAuthenticated/requireAdmin used to
	// short-circuit on an empty apiKey before ever consulting the
	// lookup function, so MCP2REST_DISABLE_AUTH never actually bypassed
	// admin-only skill tools (create_skill/update_skill/delete_skill)
	// for callers presenting no key at all.
	if err := api.requireAdmin(""); err != nil {
		t.Fatalf("requireAdmin(\"\") with auth disabled = %v, want nil", err)
	}
	if _, err := api.requireAuthenticated(""); err != nil {
		t.Fatalf("requireAuthenticated(\"\") with auth disabled = %v, want nil", err)
	}
}

func TestRequireAdminRejectsEmptyKeyWhenAuthEnabled(t *testing.T) {
	t.Parallel()

	api, _ := newTestAPI(t)

	if err := api.requireAdmin(""); err == nil {
		t.Fatal("requireAdmin(\"\") with auth enabled = nil error, want an error")
	}
}

func newTestAPI(t *testing.T, objects ...runtime.Object) (*API, testCredentials) {
	t.Helper()

	scheme := runtime.NewScheme()
	client := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		skillGVR: Kind + "List",
	}, objects...)

	store := keys.NewStore()
	adminKey, err := store.Mint("cluster-admin", manifest.TierAdmin)
	if err != nil {
		t.Fatalf("Mint(admin) error = %v", err)
	}
	userKey, err := store.Mint("hermes-alice", manifest.TierUser)
	if err != nil {
		t.Fatalf("Mint(user) error = %v", err)
	}

	api, err := New(client, func(key string) (pipeline.KeyRecord, bool) {
		record, ok := store.Lookup(key)
		if !ok {
			return pipeline.KeyRecord{}, false
		}
		return pipeline.KeyRecord{
			AgentInstance: record.AgentInstance,
			Tier:          record.Tier,
		}, true
	})
	if err != nil {
		t.Fatalf("skillstools.New() error = %v", err)
	}
	return api, testCredentials{adminKey: adminKey, userKey: userKey}
}

func testSkillObject(namespace, name string, spec SkillSpec, labels map[string]string) *unstructured.Unstructured {
	item := buildSkillObject(name, namespace, labels, spec)
	item.SetCreationTimestamp(metav1.Now())
	return item
}

func registerWriteServer(t *testing.T, api *API, apiKey string) *mcp.Server {
	t.Helper()

	server := mcp.NewServer(&mcp.Implementation{Name: "skills-test", Version: "0.0.0"}, nil)
	api.RegisterWriteTools(server, apiKey)
	return server
}

func connectSession(t *testing.T, server *mcp.Server) *mcp.ClientSession {
	t.Helper()

	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()

	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server.Connect() error = %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect() error = %v", err)
	}
	return session
}

func decodeStructured[T any](t *testing.T, value any) T {
	t.Helper()

	var out T
	bytes, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if err := json.Unmarshal(bytes, &out); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	return out
}

func textContent(result *mcp.CallToolResult) string {
	parts := make([]string, 0, len(result.Content))
	for _, content := range result.Content {
		text, ok := content.(*mcp.TextContent)
		if ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func TestNewValidatesDependencies(t *testing.T) {
	t.Parallel()

	_, err := New(nil, func(string) (pipeline.KeyRecord, bool) { return pipeline.KeyRecord{}, false })
	if err == nil || !strings.Contains(err.Error(), "dynamic client is required") {
		t.Fatalf("New(nil, lookup) error = %v, want dynamic client validation", err)
	}

	scheme := runtime.NewScheme()
	client := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		skillGVR: Kind + "List",
	})
	_, err = New(client, nil)
	if err == nil || !strings.Contains(err.Error(), "key lookup is required") {
		t.Fatalf("New(client, nil) error = %v, want key lookup validation", err)
	}
}

func TestGetSkillErrorsOnDuplicateNamesWithoutNamespace(t *testing.T) {
	t.Parallel()

	api, _ := newTestAPI(t,
		testSkillObject("apps", "shared", SkillSpec{
			SkillName:   "Shared apps",
			Description: "Apps copy",
			Domain:      "apps",
			Type:        "parent",
			Confidence:  "high",
			Source:      "earned",
			Content:     "# Shared apps",
		}, nil),
		testSkillObject("default", "shared", SkillSpec{
			SkillName:   "Shared default",
			Description: "Default copy",
			Domain:      "default",
			Type:        "parent",
			Confidence:  "high",
			Source:      "earned",
			Content:     "# Shared default",
		}, nil),
	)

	_, err := api.GetSkill(context.Background(), GetSkillParams{Name: "shared"}, "")
	if err == nil || !strings.Contains(err.Error(), "specify namespace") {
		t.Fatalf("GetSkill(shared) error = %v, want duplicate-name guidance", err)
	}
}

func TestDeleteSkillReturnsWrappedNotFound(t *testing.T) {
	t.Parallel()

	api, _ := newTestAPI(t)
	_, err := api.DeleteSkill(context.Background(), DeleteSkillParams{Name: "missing", Namespace: "apps"})
	if err == nil {
		t.Fatal("DeleteSkill(missing) error = nil, want wrapped not found")
	}
	if !strings.Contains(err.Error(), `delete skill "missing" in namespace "apps"`) {
		t.Fatalf("DeleteSkill(missing) error = %v, want contextual prefix", err)
	}
}

func TestListSkillsPropagatesDecodeErrors(t *testing.T) {
	t.Parallel()

	api, _ := newTestAPI(t, &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": Group + "/" + Version,
			"kind":       Kind,
			"metadata":   map[string]any{"name": "broken", "namespace": "apps"},
			"spec":       map[string]any{"skillName": "broken"},
		},
	})

	_, err := api.ListSkills(context.Background(), "")
	if err == nil {
		t.Fatal("ListSkills() error = nil, want decode failure")
	}
	if !strings.Contains(err.Error(), "read spec.description") {
		t.Fatalf("ListSkills() error = %v, want missing-field detail", err)
	}
}
