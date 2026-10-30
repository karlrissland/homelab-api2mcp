// Package discovery watches labeled ConfigMaps cluster-wide and keeps an
// in-memory routing table of manifest.App values keyed by app name.
//
// A client-go SharedIndexInformer handles the initial list, incremental
// updates, and cache synchronization. Snapshot reads persisted registrations
// directly for explicit credential reloads that must not depend on informer
// delivery. App names identify routes globally, not within a namespace.
package discovery
