package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	"github.com/karlrissland/homelab-api2mcp/internal/manifest"
	yaml "gopkg.in/yaml.v2"
)

const (
	// AppLabelKey marks ConfigMaps that publish mcp2rest app manifests.
	AppLabelKey = "mcp2rest.homelab.dev/app"

	manifestYAMLKey = "manifest.yaml"
	manifestYMLKey  = "manifest.yml"
	manifestJSONKey = "manifest.json"
)

// Table maintains the live app routing table populated from ConfigMaps.
type Table struct {
	informer cache.SharedIndexInformer

	mu          sync.RWMutex
	routes      map[string]*manifest.App
	sourceToApp map[string]string
	appToSource map[string]string
}

// New builds a routing table backed by a ConfigMap informer that watches
// all namespaces for ConfigMaps carrying AppLabelKey.
func New(client kubernetes.Interface) (*Table, error) {
	if client == nil {
		return nil, errors.New("create discovery table: client is required")
	}

	factory := informers.NewSharedInformerFactoryWithOptions(
		client,
		0,
		informers.WithNamespace(metav1.NamespaceAll),
		informers.WithTweakListOptions(func(opts *metav1.ListOptions) {
			opts.LabelSelector = AppLabelKey
		}),
	)

	table := &Table{
		informer:    factory.Core().V1().ConfigMaps().Informer(),
		routes:      make(map[string]*manifest.App),
		sourceToApp: make(map[string]string),
		appToSource: make(map[string]string),
	}

	if _, err := table.informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    table.onAdd,
		UpdateFunc: table.onUpdate,
		DeleteFunc: table.onDelete,
	}); err != nil {
		return nil, fmt.Errorf("create discovery table: add informer handler: %w", err)
	}

	return table, nil
}

// Run starts the informer, waits for the initial cache sync, and keeps the
// routing table updated until ctx is canceled.
func (t *Table) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("run discovery table: context is required")
	}

	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		t.informer.Run(ctx.Done())
	}()

	if !cache.WaitForCacheSync(ctx.Done(), t.informer.HasSynced) {
		if err := ctx.Err(); err != nil {
			<-runDone
			return nil
		}
		return errors.New("run discovery table: wait for cache sync: informer did not sync")
	}

	<-ctx.Done()
	<-runDone
	return nil
}

// Get returns the app registered under appName.
func (t *Table) Get(appName string) (*manifest.App, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	app, ok := t.routes[appName]
	if !ok {
		return nil, false
	}

	return cloneApp(app), true
}

// List returns all discovered apps sorted by name.
func (t *Table) List() []*manifest.App {
	t.mu.RLock()
	defer t.mu.RUnlock()

	apps := make([]*manifest.App, 0, len(t.routes))
	for _, app := range t.routes {
		apps = append(apps, cloneApp(app))
	}

	sort.Slice(apps, func(i, j int) bool {
		return apps[i].Name < apps[j].Name
	})

	return apps
}

// ParseConfigMap decodes a labeled ConfigMap into a validated manifest.App.
func ParseConfigMap(cm *corev1.ConfigMap) (*manifest.App, error) {
	if cm == nil {
		return nil, errors.New("parse configmap: configmap is required")
	}

	labelValue, ok := cm.Labels[AppLabelKey]
	if !ok || labelValue == "" {
		return nil, fmt.Errorf("parse configmap %s/%s: missing label %q", cm.Namespace, cm.Name, AppLabelKey)
	}

	sourceKey, rawManifest, err := selectManifestSource(cm)
	if err != nil {
		return nil, fmt.Errorf("parse configmap %s/%s: %w", cm.Namespace, cm.Name, err)
	}

	app, err := decodeManifest(sourceKey, rawManifest)
	if err != nil {
		return nil, fmt.Errorf("parse configmap %s/%s: decode %q: %w", cm.Namespace, cm.Name, sourceKey, err)
	}

	if app.Name != labelValue {
		return nil, fmt.Errorf(
			"parse configmap %s/%s: manifest name %q does not match label %s=%q",
			cm.Namespace,
			cm.Name,
			app.Name,
			AppLabelKey,
			labelValue,
		)
	}

	app.Namespace = cm.Namespace
	if err := app.Validate(); err != nil {
		return nil, fmt.Errorf("parse configmap %s/%s: validate manifest: %w", cm.Namespace, cm.Name, err)
	}

	return app, nil
}

func (t *Table) onAdd(obj any) {
	cm, ok := obj.(*corev1.ConfigMap)
	if !ok {
		log.Printf("discovery: ignoring add event with unexpected object type %T", obj)
		return
	}

	t.upsert(cm)
}

func (t *Table) onUpdate(_, newObj any) {
	cm, ok := newObj.(*corev1.ConfigMap)
	if !ok {
		log.Printf("discovery: ignoring update event with unexpected object type %T", newObj)
		return
	}

	t.upsert(cm)
}

func (t *Table) onDelete(obj any) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		log.Printf("discovery: ignoring delete event with no cache key: %v", err)
		return
	}

	t.removeBySource(key)
}

func (t *Table) upsert(cm *corev1.ConfigMap) {
	if _, ok := cm.Labels[AppLabelKey]; !ok {
		return
	}

	app, err := ParseConfigMap(cm)
	if err != nil {
		log.Printf("discovery: skipping invalid manifest from %s/%s: %v", cm.Namespace, cm.Name, err)
		t.removeBySource(cacheKey(cm))
		return
	}

	sourceKey := cacheKey(cm)

	t.mu.Lock()
	defer t.mu.Unlock()

	if existingAppName, ok := t.sourceToApp[sourceKey]; ok && existingAppName != app.Name {
		delete(t.routes, existingAppName)
		delete(t.appToSource, existingAppName)
		delete(t.sourceToApp, sourceKey)
	}

	if existingSource, ok := t.appToSource[app.Name]; ok && existingSource != sourceKey {
		log.Printf(
			"discovery: skipping manifest from %s/%s: app %q already provided by %s",
			cm.Namespace,
			cm.Name,
			app.Name,
			existingSource,
		)
		return
	}

	t.routes[app.Name] = cloneApp(app)
	t.sourceToApp[sourceKey] = app.Name
	t.appToSource[app.Name] = sourceKey
}

func (t *Table) removeBySource(sourceKey string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	appName, ok := t.sourceToApp[sourceKey]
	if !ok {
		return
	}

	delete(t.sourceToApp, sourceKey)
	delete(t.appToSource, appName)
	delete(t.routes, appName)
}

func cacheKey(cm *corev1.ConfigMap) string {
	return fmt.Sprintf("%s/%s", cm.Namespace, cm.Name)
}

func selectManifestSource(cm *corev1.ConfigMap) (string, string, error) {
	switch {
	case cm.Data == nil:
		return "", "", errors.New("configmap data is empty")
	case cm.Data[manifestYAMLKey] != "":
		return manifestYAMLKey, cm.Data[manifestYAMLKey], nil
	case cm.Data[manifestYMLKey] != "":
		return manifestYMLKey, cm.Data[manifestYMLKey], nil
	case cm.Data[manifestJSONKey] != "":
		return manifestJSONKey, cm.Data[manifestJSONKey], nil
	case len(cm.Data) == 1:
		for key, value := range cm.Data {
			return key, value, nil
		}
	}

	return "", "", fmt.Errorf(
		"expected %q, %q, %q, or a single manifest entry",
		manifestYAMLKey,
		manifestYMLKey,
		manifestJSONKey,
	)
}

func decodeManifest(sourceKey, rawManifest string) (*manifest.App, error) {
	var app manifest.App
	if strings.EqualFold(sourceKey, manifestJSONKey) {
		if err := json.Unmarshal([]byte(rawManifest), &app); err != nil {
			return nil, err
		}
		return &app, nil
	}

	var decoded any
	if err := yaml.Unmarshal([]byte(rawManifest), &decoded); err != nil {
		return nil, err
	}

	normalized, err := normalizeYAMLValue(decoded)
	if err != nil {
		return nil, err
	}

	jsonBytes, err := json.Marshal(normalized)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(jsonBytes, &app); err != nil {
		return nil, err
	}

	return &app, nil
}

func cloneApp(app *manifest.App) *manifest.App {
	if app == nil {
		return nil
	}

	cloned := &manifest.App{
		Name:                  app.Name,
		Namespace:             app.Namespace,
		UpstreamBaseURL:       app.UpstreamBaseURL,
		UpstreamCredentialEnv: app.UpstreamCredentialEnv,
		UpstreamMCPURL:        app.UpstreamMCPURL,
		Tools:                 make([]manifest.Tool, len(app.Tools)),
	}

	for i, tool := range app.Tools {
		cloned.Tools[i] = manifest.Tool{
			Name:             tool.Name,
			Description:      tool.Description,
			Tier:             tool.Tier,
			Type:             tool.Type,
			RequestTemplate:  tool.RequestTemplate,
			ResponseTemplate: tool.ResponseTemplate,
			UpstreamToolName: tool.UpstreamToolName,
			InputSchema:      cloneStringAnyMap(tool.InputSchema),
		}
	}

	return cloned
}

func cloneStringAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}

	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = cloneAny(value)
	}

	return out
}

func cloneAny(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneStringAnyMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneAny(item)
		}
		return out
	default:
		return typed
	}
}

func normalizeYAMLValue(value any) (any, error) {
	switch typed := value.(type) {
	case map[any]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			keyString, ok := key.(string)
			if !ok {
				return nil, fmt.Errorf("yaml object key has type %T, want string", key)
			}
			normalized, err := normalizeYAMLValue(item)
			if err != nil {
				return nil, err
			}
			out[keyString] = normalized
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			normalized, err := normalizeYAMLValue(item)
			if err != nil {
				return nil, err
			}
			out[key] = normalized
		}
		return out, nil
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			normalized, err := normalizeYAMLValue(item)
			if err != nil {
				return nil, err
			}
			out[i] = normalized
		}
		return out, nil
	default:
		return typed, nil
	}
}
