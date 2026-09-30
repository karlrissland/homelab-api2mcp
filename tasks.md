# mcp2rest — Task Tracker

Status legend: `not started` · `in progress` · `blocked` · `done`

This file mirrors the phases in `plan.md` §7. Update status as work
progresses. Do not reorder phases — dependencies are listed in `plan.md`.

## Phase 0 — Repo scaffolding

| Status | Task |
|---|---|
| done | Initialize Go module (`go.mod`), root `README.md`, `LICENSE` — LICENSE deferred, no org precedent found, needs explicit user choice |
| done | Create directory layout per `.github/copilot-instructions.md` |
| done | Add `.github/copilot-instructions.md` |
| done | Add Go best-practices + Docker best-practices skills under `.github/skills/` (or org's skill convention) |
| done | Add `.github/workflows/ci.yml` (build + `go vet` + lint + `go test`) |
| done | Skeleton `main.go` (prints version, exits 0) — CI green |

## Phase 1 — MCP server skeleton

| Status | Task |
|---|---|
| done | Vendor/select official Go MCP SDK — `github.com/modelcontextprotocol/go-sdk` |
| done | `internal/mcpserver`: Streamable HTTP transport wiring |
| done | One hardcoded "echo" tool |
| done | Unit test: JSON-RPC envelope round trip |

## Phase 2 — Static single-app Liquid transform

| Status | Task |
|---|---|
| not started | `internal/render`: Liquid render context + execute |
| not started | Fixture manifest + `request.liquid`/`response.liquid` (Gitea) |
| not started | Wire fixture into Phase 1's tool dispatch |
| not started | Integration test: full request→render→call→render→respond |

## Phase 3 — Two-tier (user/admin) authz

| Status | Task |
|---|---|
| done | `internal/pipeline`: ordered named-stage chain |
| done | Hardcoded `user`/`admin` keys for dev |
| done | Authn stage (key lookup) |
| done | Authz stage (tier check vs. tool tier) |
| done | Unit tests: accept/reject matrix |

## Phase 4 — ConfigMap-based manifest discovery

| Status | Task |
|---|---|
| done | `internal/discovery`: informer (list+watch by label) |
| done | Routing table build/update from informer events |
| done | RBAC: cluster-wide ConfigMap list/watch |
| done | Fake-clientset integration tests: add/remove ConfigMap reflected live; unlabeled/invalid manifests ignored |

## Phase 5 — Key issuance + per-instance Secret delivery

| Status | Task |
|---|---|
| done | `internal/keys`: mint/store/revoke |
| done | Secret naming: `<agent-instance>-mcp2rest-keys` |
| not started | RBAC: namespace-label-scoped ClusterRole for Secret write |
| done | Integration test: minted key's Secret lands correctly + authenticates |

## Phase 6 — Management tool set + human-admin bootstrap

| Status | Task |
|---|---|
| not started | Bootstrap admin-scope key mint-and-print-once on first startup |
| not started | `register_app` / `deregister_app` |
| not started | `list_apps` / `get_manifest` |
| not started | `create_key` / `list_keys` / `revoke_key` / `rotate_key` |
| not started | Integration test: register app live, confirm discovery without restart |
| not started | Integration test: non-admin key rejected on all management tools |

## Phase 7 — Built-in `skills` tool namespace

| Status | Task |
|---|---|
| not started | RBAC: cluster-wide `Skill` CRD read; admin-gated write |
| not started | `list_skills` / `get_skill` (any authenticated key) |
| not started | `create_skill` / `update_skill` / `delete_skill` (admin only) |
| not started | Integration test: CRD created out-of-band is visible via tools |
| not started | Integration test: write path tier-gated correctly |

## Phase 8 — Multi-user impersonation (`caller` context)

| Status | Task |
|---|---|
| not started | Extend Liquid render context with `caller` object |
| not started | Pilot manifest exercising `caller.username` impersonation |
| not started | Integration test: two callers → two distinct rendered upstream requests |

## Phase 9 — Passthrough mode

| Status | Task |
|---|---|
| not started | `internal/passthrough`: JSON-RPC relay client |
| not started | Upstream tool-name tier tagging / filtering |
| not started | Integration test: tier filtering + relay correctness |

## Phase 10 — `hlctl` integration (homelab repo)

| Status | Task |
|---|---|
| not started | `mcpTools:` app.yaml schema block |
| not started | `internal/mcpproxy`: render/validate at `app validate` time |
| not started | Minimal MCP/JSON-RPC client for `register_app`/`deregister_app` |
| not started | Deploy-step wiring (mirrors `EnsureMCPServer`) |
| not started | Test: `app validate` fails fast on malformed manifest |
| not started | Test: real `app setup` results in app visible via `list_apps` |

## Phase 11 — mcp2rest platform-service provisioning (homelab repo)

| Status | Task |
|---|---|
| not started | Provisioning package (Deployment/Service/Ingress/RBAC), mirrors `EnsureMCPServer` |
| not started | Wire into platform bootstrap (default-on, no opt-in) |
| not started | Test: fresh cluster bootstrap → mcp2rest reachable at `mcp2rest.<dns.zone>` |

## Phase 12 — Catalog pilot: Gitea (homelab-catalog repo)

| Status | Task |
|---|---|
| not started | `mcpTools:` manifest + `maps/mcp/**` templates for Gitea |
| not started | Reuse `gitea-api` Skill research for tool/operation selection |
| not started | End-to-end test: real agent calls real Gitea-backed tool |

## Phase 13 — mcp2rest's own usage Skill

| Status | Task |
|---|---|
| done | Author `SKILL.md` content (how to use mcp2rest) |
| not started | Bundle into Phase 11's provisioning step |
| not started | Test: `list_skills`/`get_skill` return it post-bootstrap, no extra step |

## Phase 14 — Observability

| Status | Task |
|---|---|
| not started | Metrics middleware stage (`/metrics`, Prometheus) |
| not started | Structured stdout logging |
| not started | Grafana dashboard JSON + alert rules |
| not started | Test: Prometheus scrape succeeds; dashboard shows pilot traffic |

## Phase 15 — Deferred (explicitly out of scope for v1)

| Status | Task |
|---|---|
| deferred | Real governance/security pipeline stages (PII redaction, rate limiting, policy engine) |
| deferred | WASM (`wazero`) plugin loader implementation |
| deferred | Automatic key rotation |
| deferred | Per-(app, user) credential storage for non-impersonation-capable apps |
| deferred | Full per-tool ACLs beyond two-tier user/admin |
