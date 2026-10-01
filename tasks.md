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
| done | `internal/render`: Liquid render context + execute |
| done | Fixture manifest + `request.liquid`/`response.liquid` (Gitea) |
| done | Wire fixture into Phase 1's tool dispatch — real runtime dispatch now routes authenticated per-app MCP calls through `internal/pipeline` and `internal/render.Renderer.Execute`, replacing the earlier echo-only path for rendered tools while leaving passthrough explicitly deferred. |
| done | Integration test: full request→render→call→render→respond |

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
| done | RBAC: namespace-label-scoped ClusterRole for Secret write — `manifests/rbac/keys-secrets-clusterrole.yaml` declares get/create/update on Secrets; actual namespace-label scoping is applied via the ClusterRoleBinding `hlctl` provisions (see homelab#216), since a ClusterRole itself cannot carry namespace scope. |
| done | Integration test: minted key's Secret lands correctly + authenticates against Phase 3's authz stage — dynamic key lookup is now wired from `internal/keys.Store` into the runtime pipeline, with end-to-end tests covering Secret delivery plus authenticated rendered-tool execution. |

## Phase 6 — Management tool set + human-admin bootstrap

| Status | Task |
|---|---|
| done | Bootstrap admin-scope key mint-and-print-once on first startup |
| done | `register_app` / `deregister_app` |
| done | `list_apps` / `get_manifest` |
| done | `create_key` / `list_keys` / `revoke_key` / `rotate_key` |
| done | Integration test: register app live, confirm discovery without restart |
| done | Integration test: non-admin key rejected on all management tools |

## Phase 7 — Built-in `skills` tool namespace

| Status | Task |
|---|---|
| done | RBAC: cluster-wide `Skill` CRD read; admin-gated write — `manifests/rbac/skills-clusterrole.yaml` grants cluster-wide read and write access to `skills.skills.homelab.dev`; tool-level admin gating still controls `create_skill`/`update_skill`/`delete_skill` because Kubernetes RBAC applies to the pod, not the individual MCP caller. |
| done | `list_skills` / `get_skill` (any authenticated key) |
| done | `create_skill` / `update_skill` / `delete_skill` (admin only) |
| done | Integration test: CRD created out-of-band is visible via tools |
| done | Integration test: write path tier-gated correctly |

## Phase 8 — Multi-user impersonation (`caller` context)

| Status | Task |
|---|---|
| done | Complete Liquid render context `caller` wiring — Phase 6 already populated `agentInstance`/`tier`/`appInstance`; Phase 8 now threads per-call `username` via reserved `__mcp2rest_caller_username` and strips it from `args` before Liquid sees the tool parameters |
| done | Pilot manifest exercising `caller.username` impersonation |
| done | Integration test: two callers → two distinct rendered upstream requests |

## Phase 9 — Passthrough mode

| Status | Task |
|---|---|
| not started | `internal/passthrough`: JSON-RPC relay client |
| not started | Upstream tool-name tier tagging / filtering |
| not started | Integration test: tier filtering + relay correctness |

## Phase 10 — `hlctl` integration (homelab repo)

> Tracked upstream: [homelab#215](https://github.com/karlrissland/homelab/issues/215).
> This repo does not implement `homelab`-side work directly — per
> cross-repo policy, changes needed in another repo are requested via a
> GitHub issue for that team, not made here.

| Status | Task |
|---|---|
| not started | `mcpTools:` app.yaml schema block |
| not started | `internal/mcpproxy`: render/validate at `app validate` time |
| not started | Minimal MCP/JSON-RPC client for `register_app`/`deregister_app` |
| not started | Deploy-step wiring (mirrors `EnsureMCPServer`) |
| not started | Test: `app validate` fails fast on malformed manifest |
| not started | Test: real `app setup` results in app visible via `list_apps` |

## Phase 11 — mcp2rest platform-service provisioning (homelab repo)

> Tracked upstream: [homelab#216](https://github.com/karlrissland/homelab/issues/216).

| Status | Task |
|---|---|
| not started | Provisioning package (Deployment/Service/Ingress/RBAC), mirrors `EnsureMCPServer` |
| not started | Wire into platform bootstrap (default-on, no opt-in) |
| not started | Test: fresh cluster bootstrap → mcp2rest reachable at `mcp2rest.<dns.zone>` |

## Phase 12 — Catalog pilot: MeTube (homelab-catalog repo)

> Tracked upstream: [homelab-catalog#103](https://github.com/karlrissland/homelab-catalog/issues/103).
> Pilot app switched from Gitea to MeTube 2026-09-30 — lower resource
> footprint to iterate against, and no API auth to wire up for the pilot.

| Status | Task |
|---|---|
| not started | `mcpTools:` manifest + `maps/mcp/**` templates for MeTube |
| not started | Reuse `metube-api` Skill research for tool/operation selection |
| not started | End-to-end test: real agent calls real MeTube-backed tool |

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
