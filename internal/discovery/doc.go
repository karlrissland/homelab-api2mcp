// Package discovery watches labeled ConfigMaps cluster-wide and keeps an
// in-memory routing table of manifest.App values keyed by app name.
//
// It uses a client-go SharedIndexInformer rather than a hand-rolled
// list+watch loop because the informer already handles the initial list,
// incremental watch updates, and cache synchronization in a way that is
// easy to exercise with fake.NewSimpleClientset in tests. As a short-term
// simplification for Phase 4, this package imports client-go directly;
// a later pass can move the raw Kubernetes wiring behind internal/k8s.
package discovery
