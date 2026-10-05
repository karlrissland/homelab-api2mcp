// Package skillstools exposes the built-in Skill CRD MCP tools used by
// mcp2rest's Phase 7 "skills" namespace.
package skillstools

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/karlrissland/homelab-api2mcp/internal/manifest"
	"github.com/karlrissland/homelab-api2mcp/internal/pipeline"
)

const (
	// Group is the Skill CRD API group shared with homelab's skillscrd package.
	Group = "skills.homelab.dev"
	// Version is the Skill CRD API version shared with homelab's skillscrd package.
	Version = "v1alpha1"
	// Kind is the Skill CRD kind shared with homelab's skillscrd package.
	Kind = "Skill"
	// Resource is the Skill CRD plural resource name shared with homelab's skillscrd package.
	Resource = "skills"
	// labelApp mirrors homelab's skillscrd.labelApp -- the label hlctl
	// already applies to every app-scoped Skill CR (skills.homelab.dev/app=<appName>).
	// Used to scope list_skills/get_skill to one app's own Skills on a
	// per-app proxied session.
	labelApp = "skills.homelab.dev/app"
)

var skillGVR = schema.GroupVersionResource{Group: Group, Version: Version, Resource: Resource}

// API serves the built-in Skill CRD MCP tool set.
type API struct {
	client dynamic.Interface
	lookup pipeline.KeyLookup
}

// SkillSpec mirrors the shared Skill CRD spec shape from homelab's skillscrd package.
type SkillSpec struct {
	SkillName   string `json:"skillName"`
	Description string `json:"description"`
	Domain      string `json:"domain"`
	Type        string `json:"type"`
	Parent      string `json:"parent,omitempty"`
	Confidence  string `json:"confidence"`
	Source      string `json:"source"`
	Content     string `json:"content"`
}

// Skill is the MCP-facing view of one Skill custom resource.
type Skill struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	Labels    map[string]string `json:"labels,omitempty"`
	Spec      SkillSpec         `json:"spec"`
}

// SkillSummary intentionally omits spec.content so list_skills stays compact.
type SkillSummary struct {
	Name        string `json:"name"`
	Namespace   string `json:"namespace"`
	SkillName   string `json:"skillName"`
	Domain      string `json:"domain"`
	Description string `json:"description"`
}

// ListSkillsParams declares the list_skills tool input.
type ListSkillsParams struct{}

// ListSkillsResult reports the compact cluster-wide Skill summary list.
type ListSkillsResult struct {
	Skills []SkillSummary `json:"skills"`
}

// GetSkillParams declares the get_skill tool input.
type GetSkillParams struct {
	Name      string `json:"name" jsonschema:"Skill resource name"`
	Namespace string `json:"namespace,omitempty" jsonschema:"Optional namespace when more than one Skill shares the same name"`
}

// GetSkillResult returns the full Skill resource, including spec.content.
type GetSkillResult struct {
	Skill Skill `json:"skill"`
}

// CreateSkillParams declares the create_skill tool input.
type CreateSkillParams struct {
	Name      string            `json:"name" jsonschema:"Skill resource name"`
	Namespace string            `json:"namespace" jsonschema:"Namespace holding the Skill resource"`
	Labels    map[string]string `json:"labels,omitempty" jsonschema:"Optional metadata.labels to apply to the Skill resource"`
	Spec      SkillSpec         `json:"spec" jsonschema:"Full Skill CRD spec payload"`
}

// CreateSkillResult returns the created Skill resource.
type CreateSkillResult struct {
	Skill Skill `json:"skill"`
}

// UpdateSkillParams declares the update_skill tool input.
type UpdateSkillParams struct {
	Name      string            `json:"name" jsonschema:"Skill resource name"`
	Namespace string            `json:"namespace" jsonschema:"Namespace holding the Skill resource"`
	Labels    map[string]string `json:"labels,omitempty" jsonschema:"Replacement metadata.labels for the Skill resource"`
	Spec      SkillSpec         `json:"spec" jsonschema:"Replacement Skill CRD spec payload"`
}

// UpdateSkillResult returns the updated Skill resource.
type UpdateSkillResult struct {
	Skill Skill `json:"skill"`
}

// DeleteSkillParams declares the delete_skill tool input.
type DeleteSkillParams struct {
	Name      string `json:"name" jsonschema:"Skill resource name"`
	Namespace string `json:"namespace" jsonschema:"Namespace holding the Skill resource"`
}

// DeleteSkillResult reports the deleted Skill resource identity.
type DeleteSkillResult struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Deleted   bool   `json:"deleted"`
}

// New constructs the built-in Skill CRD tool API.
func New(client dynamic.Interface, lookup pipeline.KeyLookup) (*API, error) {
	switch {
	case client == nil:
		return nil, fmt.Errorf("new skills api: dynamic client is required")
	case lookup == nil:
		return nil, fmt.Errorf("new skills api: key lookup is required")
	default:
		return &API{client: client, lookup: lookup}, nil
	}
}

// ToolNameSlug lowercases s and replaces every character outside [a-z0-9_]
// with "_", so an app name is always safe to splice into an MCP tool name.
// Exported so callers constructing descriptive text (e.g. session
// Instructions) can refer to the exact tool names RegisterReadTools derives
// from an app name.
func ToolNameSlug(s string) string {
	lower := strings.ToLower(s)
	var b strings.Builder
	b.Grow(len(lower))
	for _, r := range lower {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

// RegisterReadTools adds Skill read tools to server. Both tools require any
// valid authenticated key. appScope, when non-empty, scopes both tools to
// Skills labeled skills.homelab.dev/app=<appScope> -- used on a per-app
// proxied session (the app's own name) so that app's tool set only ever
// surfaces its own Skills, and also renames the tools to
// list_<app>_skills/get_<app>_skill (instead of the generic
// list_skills/get_skill) so an agent juggling multiple apps' sessions in one
// harness can't confuse one app's Skills tool for another's or for
// mcp2rest's cluster-wide management tool of the same generic name. Pass an
// empty appScope for mcp2rest's management session, which keeps the generic
// list_skills/get_skill names and deliberately stays cluster-wide (any
// admin-tier caller legitimately needs to browse/manage every app's Skills,
// not just one).
func (a *API) RegisterReadTools(server *mcp.Server, apiKey string, appScope string) {
	listName := "list_skills"
	getName := "get_skill"
	listDescription := "List every Skill custom resource cluster-wide as compact summaries (name, namespace, skillName, domain, description)."
	getDescription := "Fetch one Skill custom resource by metadata.name, returning the full Skill content."
	if appScope != "" {
		slug := ToolNameSlug(appScope)
		listName = fmt.Sprintf("list_%s_skills", slug)
		getName = fmt.Sprintf("get_%s_skill", slug)
		listDescription = fmt.Sprintf("List this app's (%q) own Skill custom resources as compact summaries (name, namespace, skillName, domain, description). Skills belonging to other apps are not visible here.", appScope)
		getDescription = fmt.Sprintf("Fetch one of this app's (%q) own Skill custom resources by metadata.name, returning the full Skill content. Skills belonging to other apps are not visible here.", appScope)
	}

	mcp.AddTool(server, &mcp.Tool{
		Name:        listName,
		Description: listDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ ListSkillsParams) (*mcp.CallToolResult, ListSkillsResult, error) {
		if _, err := a.requireAuthenticated(apiKey); err != nil {
			return nil, ListSkillsResult{}, err
		}
		result, err := a.ListSkills(ctx, appScope)
		return nil, result, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        getName,
		Description: getDescription,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":      map[string]any{"type": "string"},
				"namespace": map[string]any{"type": "string"},
			},
			"required": []string{"name"},
		},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, params GetSkillParams) (*mcp.CallToolResult, GetSkillResult, error) {
		if _, err := a.requireAuthenticated(apiKey); err != nil {
			return nil, GetSkillResult{}, err
		}
		result, err := a.GetSkill(ctx, params, appScope)
		return nil, result, err
	})
}

// RegisterWriteTools adds create_skill/update_skill/delete_skill to server.
// Every write path is gated by the caller's resolved admin tier.
func (a *API) RegisterWriteTools(server *mcp.Server, apiKey string) {
	skillInputSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":      map[string]any{"type": "string"},
			"namespace": map[string]any{"type": "string"},
			"labels": map[string]any{
				"type":                 "object",
				"additionalProperties": map[string]any{"type": "string"},
			},
			"spec": skillSpecSchema(),
		},
		"required": []string{"name", "namespace", "spec"},
	}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "create_skill",
		Description: "Create one Skill custom resource. Admin tier required.",
		InputSchema: skillInputSchema,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, params CreateSkillParams) (*mcp.CallToolResult, CreateSkillResult, error) {
		if err := a.requireAdmin(apiKey); err != nil {
			return nil, CreateSkillResult{}, err
		}
		result, err := a.CreateSkill(ctx, params)
		return nil, result, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "update_skill",
		Description: "Replace one existing Skill custom resource's labels and spec. Admin tier required.",
		InputSchema: skillInputSchema,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, params UpdateSkillParams) (*mcp.CallToolResult, UpdateSkillResult, error) {
		if err := a.requireAdmin(apiKey); err != nil {
			return nil, UpdateSkillResult{}, err
		}
		result, err := a.UpdateSkill(ctx, params)
		return nil, result, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "delete_skill",
		Description: "Delete one Skill custom resource by name and namespace. Admin tier required.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":      map[string]any{"type": "string"},
				"namespace": map[string]any{"type": "string"},
			},
			"required": []string{"name", "namespace"},
		},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, params DeleteSkillParams) (*mcp.CallToolResult, DeleteSkillResult, error) {
		if err := a.requireAdmin(apiKey); err != nil {
			return nil, DeleteSkillResult{}, err
		}
		result, err := a.DeleteSkill(ctx, params)
		return nil, result, err
	})
}

// ListSkills returns the Skill list as compact summaries so the MCP
// response stays small even when the CRD content itself is large. appScope,
// when non-empty, restricts the result to Skills either unlabeled for any
// app (cluster-wide/global Skills, e.g. mcp2rest's own usage Skill, remain
// visible from every app's session) or labeled
// skills.homelab.dev/app=<appScope> -- used on a per-app proxied session so
// an app's own tool set only surfaces that app's own Skills plus global
// ones, never another app's. An empty appScope lists cluster-wide
// unfiltered, as used on mcp2rest's management session.
func (a *API) ListSkills(ctx context.Context, appScope string) (ListSkillsResult, error) {
	list, err := a.client.Resource(skillGVR).Namespace(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return ListSkillsResult{}, fmt.Errorf("list skills: %w", err)
	}

	skills := make([]SkillSummary, 0, len(list.Items))
	for _, item := range list.Items {
		if !skillVisibleInScope(&item, appScope) {
			continue
		}
		skill, err := skillFromUnstructured(&item)
		if err != nil {
			return ListSkillsResult{}, fmt.Errorf("list skills: decode %s/%s: %w", item.GetNamespace(), item.GetName(), err)
		}
		skills = append(skills, SkillSummary{
			Name:        skill.Name,
			Namespace:   skill.Namespace,
			SkillName:   skill.Spec.SkillName,
			Domain:      skill.Spec.Domain,
			Description: skill.Spec.Description,
		})
	}
	slices.SortFunc(skills, func(a, b SkillSummary) int {
		switch {
		case a.Namespace < b.Namespace:
			return -1
		case a.Namespace > b.Namespace:
			return 1
		case a.Name < b.Name:
			return -1
		case a.Name > b.Name:
			return 1
		default:
			return 0
		}
	})

	return ListSkillsResult{Skills: skills}, nil
}

// skillVisibleInScope reports whether item should be visible to a caller
// scoped to appScope. An empty appScope (cluster-wide/management callers)
// always sees everything. A non-empty appScope sees Skills with no
// skills.homelab.dev/app label at all (global Skills, not owned by any one
// app) plus Skills whose app label exactly matches appScope.
func skillVisibleInScope(item *unstructured.Unstructured, appScope string) bool {
	if appScope == "" {
		return true
	}
	appLabel, has := item.GetLabels()[labelApp]
	return !has || appLabel == appScope
}

// GetSkill fetches one Skill by resource name, optionally disambiguated by
// namespace. appScope, when non-empty, restricts matches the same way
// ListSkills does (see skillVisibleInScope) -- a Skill belonging to a
// different app is reported as not found rather than leaking its existence.
func (a *API) GetSkill(ctx context.Context, params GetSkillParams, appScope string) (GetSkillResult, error) {
	name := strings.TrimSpace(params.Name)
	if name == "" {
		return GetSkillResult{}, fmt.Errorf("get skill: name is required")
	}

	namespace := strings.TrimSpace(params.Namespace)
	if namespace != "" {
		item, err := a.client.Resource(skillGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return GetSkillResult{}, fmt.Errorf("get skill %q in namespace %q: %w", name, namespace, err)
		}
		if !skillVisibleInScope(item, appScope) {
			return GetSkillResult{}, fmt.Errorf("get skill: skill %q not found", name)
		}
		skill, err := skillFromUnstructured(item)
		if err != nil {
			return GetSkillResult{}, fmt.Errorf("get skill %q in namespace %q: decode: %w", name, namespace, err)
		}
		return GetSkillResult{Skill: skill}, nil
	}

	list, err := a.client.Resource(skillGVR).Namespace(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return GetSkillResult{}, fmt.Errorf("get skill %q: list skills: %w", name, err)
	}

	matches := make([]unstructured.Unstructured, 0, 1)
	for _, item := range list.Items {
		if item.GetName() == name && skillVisibleInScope(&item, appScope) {
			matches = append(matches, item)
		}
	}
	switch len(matches) {
	case 0:
		return GetSkillResult{}, fmt.Errorf("get skill: skill %q not found", name)
	case 1:
		skill, err := skillFromUnstructured(&matches[0])
		if err != nil {
			return GetSkillResult{}, fmt.Errorf("get skill %q: decode: %w", name, err)
		}
		return GetSkillResult{Skill: skill}, nil
	default:
		namespaces := make([]string, 0, len(matches))
		for _, match := range matches {
			namespaces = append(namespaces, match.GetNamespace())
		}
		slices.Sort(namespaces)
		return GetSkillResult{}, fmt.Errorf("get skill: multiple skills named %q found in namespaces %s; specify namespace", name, strings.Join(namespaces, ", "))
	}
}

// CreateSkill creates one Skill custom resource.
func (a *API) CreateSkill(ctx context.Context, params CreateSkillParams) (CreateSkillResult, error) {
	name, namespace, spec, labels, err := normalizedSkillInput(params.Name, params.Namespace, params.Spec, params.Labels, "create skill")
	if err != nil {
		return CreateSkillResult{}, err
	}

	item, err := a.client.Resource(skillGVR).Namespace(namespace).Create(ctx, buildSkillObject(name, namespace, labels, spec), metav1.CreateOptions{})
	if err != nil {
		return CreateSkillResult{}, fmt.Errorf("create skill %q in namespace %q: %w", name, namespace, err)
	}

	skill, err := skillFromUnstructured(item)
	if err != nil {
		return CreateSkillResult{}, fmt.Errorf("create skill %q in namespace %q: decode: %w", name, namespace, err)
	}
	return CreateSkillResult{Skill: skill}, nil
}

// UpdateSkill replaces one existing Skill custom resource's labels and spec.
func (a *API) UpdateSkill(ctx context.Context, params UpdateSkillParams) (UpdateSkillResult, error) {
	name, namespace, spec, labels, err := normalizedSkillInput(params.Name, params.Namespace, params.Spec, params.Labels, "update skill")
	if err != nil {
		return UpdateSkillResult{}, err
	}

	existing, err := a.client.Resource(skillGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return UpdateSkillResult{}, fmt.Errorf("update skill %q in namespace %q: get existing skill: %w", name, namespace, err)
	}
	existing.SetLabels(labels)
	existing.Object["spec"] = skillSpecToMap(spec)

	item, err := a.client.Resource(skillGVR).Namespace(namespace).Update(ctx, existing, metav1.UpdateOptions{})
	if err != nil {
		return UpdateSkillResult{}, fmt.Errorf("update skill %q in namespace %q: %w", name, namespace, err)
	}

	skill, err := skillFromUnstructured(item)
	if err != nil {
		return UpdateSkillResult{}, fmt.Errorf("update skill %q in namespace %q: decode: %w", name, namespace, err)
	}
	return UpdateSkillResult{Skill: skill}, nil
}

// DeleteSkill deletes one Skill custom resource.
func (a *API) DeleteSkill(ctx context.Context, params DeleteSkillParams) (DeleteSkillResult, error) {
	name := strings.TrimSpace(params.Name)
	if name == "" {
		return DeleteSkillResult{}, fmt.Errorf("delete skill: name is required")
	}
	namespace := strings.TrimSpace(params.Namespace)
	if namespace == "" {
		return DeleteSkillResult{}, fmt.Errorf("delete skill for %q: namespace is required", name)
	}

	if err := a.client.Resource(skillGVR).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
		return DeleteSkillResult{}, fmt.Errorf("delete skill %q in namespace %q: %w", name, namespace, err)
	}
	return DeleteSkillResult{Name: name, Namespace: namespace, Deleted: true}, nil
}

func (a *API) requireAuthenticated(apiKey string) (pipeline.KeyRecord, error) {
	// Look up before checking for an empty key: when the store has auth
	// disabled (MCP2REST_DISABLE_AUTH), lookup succeeds unconditionally,
	// including for an empty key. Checking apiKey == "" first would
	// bypass that and always reject no-key callers even with auth
	// disabled.
	record, ok := a.lookup(apiKey)
	if !ok {
		if apiKey == "" {
			return pipeline.KeyRecord{}, fmt.Errorf("api key is required")
		}
		return pipeline.KeyRecord{}, fmt.Errorf("api key is invalid")
	}
	if !record.Tier.Valid() {
		return pipeline.KeyRecord{}, fmt.Errorf("api key for agent instance %q has invalid tier %q", record.AgentInstance, record.Tier)
	}
	return record, nil
}

func (a *API) requireAdmin(apiKey string) error {
	record, err := a.requireAuthenticated(apiKey)
	if err != nil {
		return err
	}
	if !record.Tier.Satisfies(manifest.TierAdmin) {
		return fmt.Errorf("admin tier is required")
	}
	return nil
}

func normalizedSkillInput(name, namespace string, spec SkillSpec, labels map[string]string, verb string) (string, string, SkillSpec, map[string]string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", "", SkillSpec{}, nil, fmt.Errorf("%s: name is required", verb)
	}
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		return "", "", SkillSpec{}, nil, fmt.Errorf("%s for %q: namespace is required", verb, name)
	}
	spec, err := normalizeSkillSpec(spec, name, verb)
	if err != nil {
		return "", "", SkillSpec{}, nil, err
	}
	return name, namespace, spec, copyStringMap(labels), nil
}

func normalizeSkillSpec(spec SkillSpec, name, verb string) (SkillSpec, error) {
	spec.SkillName = strings.TrimSpace(spec.SkillName)
	spec.Description = strings.TrimSpace(spec.Description)
	spec.Domain = strings.TrimSpace(spec.Domain)
	spec.Type = strings.TrimSpace(spec.Type)
	spec.Parent = strings.TrimSpace(spec.Parent)
	spec.Confidence = strings.TrimSpace(spec.Confidence)
	spec.Source = strings.TrimSpace(spec.Source)

	required := []struct {
		field string
		value string
	}{
		{field: "spec.skillName", value: spec.SkillName},
		{field: "spec.description", value: spec.Description},
		{field: "spec.domain", value: spec.Domain},
		{field: "spec.type", value: spec.Type},
		{field: "spec.confidence", value: spec.Confidence},
		{field: "spec.source", value: spec.Source},
		{field: "spec.content", value: strings.TrimSpace(spec.Content)},
	}
	for _, field := range required {
		if field.value == "" {
			return SkillSpec{}, fmt.Errorf("%s for %q: %s is required", verb, name, field.field)
		}
	}
	switch spec.Type {
	case "parent":
	case "child":
		if spec.Parent == "" {
			return SkillSpec{}, fmt.Errorf("%s for %q: spec.parent is required when spec.type is %q", verb, name, spec.Type)
		}
	default:
		return SkillSpec{}, fmt.Errorf("%s for %q: spec.type %q is invalid", verb, name, spec.Type)
	}
	return spec, nil
}

func buildSkillObject(name, namespace string, labels map[string]string, spec SkillSpec) *unstructured.Unstructured {
	item := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": Group + "/" + Version,
			"kind":       Kind,
			"metadata": map[string]any{
				"name":      name,
				"namespace": namespace,
			},
			"spec": skillSpecToMap(spec),
		},
	}
	if len(labels) > 0 {
		item.SetLabels(labels)
	}
	return item
}

func skillSpecToMap(spec SkillSpec) map[string]any {
	out := map[string]any{
		"skillName":   spec.SkillName,
		"description": spec.Description,
		"domain":      spec.Domain,
		"type":        spec.Type,
		"confidence":  spec.Confidence,
		"source":      spec.Source,
		"content":     spec.Content,
	}
	if spec.Parent != "" {
		out["parent"] = spec.Parent
	}
	return out
}

func skillFromUnstructured(item *unstructured.Unstructured) (Skill, error) {
	spec, err := readSkillSpec(item)
	if err != nil {
		return Skill{}, err
	}
	return Skill{
		Name:      item.GetName(),
		Namespace: item.GetNamespace(),
		Labels:    copyStringMap(item.GetLabels()),
		Spec:      spec,
	}, nil
}

func readSkillSpec(item *unstructured.Unstructured) (SkillSpec, error) {
	if item == nil {
		return SkillSpec{}, fmt.Errorf("skill resource is required")
	}
	spec := SkillSpec{}
	var err error
	if spec.SkillName, err = nestedRequiredString(item.Object, "spec", "skillName"); err != nil {
		return SkillSpec{}, err
	}
	if spec.Description, err = nestedRequiredString(item.Object, "spec", "description"); err != nil {
		return SkillSpec{}, err
	}
	if spec.Domain, err = nestedRequiredString(item.Object, "spec", "domain"); err != nil {
		return SkillSpec{}, err
	}
	if spec.Type, err = nestedRequiredString(item.Object, "spec", "type"); err != nil {
		return SkillSpec{}, err
	}
	if spec.Parent, err = nestedOptionalString(item.Object, "spec", "parent"); err != nil {
		return SkillSpec{}, err
	}
	if spec.Confidence, err = nestedRequiredString(item.Object, "spec", "confidence"); err != nil {
		return SkillSpec{}, err
	}
	if spec.Source, err = nestedRequiredString(item.Object, "spec", "source"); err != nil {
		return SkillSpec{}, err
	}
	if spec.Content, err = nestedRequiredString(item.Object, "spec", "content"); err != nil {
		return SkillSpec{}, err
	}
	return spec, nil
}

func nestedRequiredString(obj map[string]any, fields ...string) (string, error) {
	value, found, err := unstructured.NestedString(obj, fields...)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", strings.Join(fields, "."), err)
	}
	if !found || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("read %s: field is missing", strings.Join(fields, "."))
	}
	return value, nil
}

func nestedOptionalString(obj map[string]any, fields ...string) (string, error) {
	value, found, err := unstructured.NestedString(obj, fields...)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", strings.Join(fields, "."), err)
	}
	if !found {
		return "", nil
	}
	return value, nil
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func skillSpecSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"skillName":   map[string]any{"type": "string"},
			"description": map[string]any{"type": "string"},
			"domain":      map[string]any{"type": "string"},
			"type":        map[string]any{"type": "string", "enum": []string{"parent", "child"}},
			"parent":      map[string]any{"type": "string"},
			"confidence":  map[string]any{"type": "string"},
			"source":      map[string]any{"type": "string"},
			"content":     map[string]any{"type": "string"},
		},
		"required": []string{"skillName", "description", "domain", "type", "confidence", "source", "content"},
	}
}
