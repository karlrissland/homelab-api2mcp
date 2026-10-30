# mcp2rest — Task Tracker

Status legend: `not started` · `in progress` · `blocked` · `done` ·
`deferred` · `unverified` (external implementation/rollout not audited) ·
`historical` (recorded observation, not current validation) · `note`.

This tracker follows the [decision plan](docs/decisions/mcp2rest-plan.md)
§7, then records later work. Do not reorder phases. Reviewed against this
repo's implementation on 2026-10-30; `done` means the listed local
deliverable exists, not that a downstream deployment or live acceptance
test has completed. Old issue-close, CI and cluster observations are
historical records, not freshly verified claims.

Use [README](README.md) for setup and [the runtime contract](docs/mcp2rest.md)
for current behavior. In particular:

The [recorded 2026-10-28 integration acceptance](README.md#recorded-integration-acceptance)
supersedes earlier blanket rollout blockers for bootstrap, durable agent
credentials, and Hermes header delivery. Downstream rows marked
`unverified` mean their source or full phase acceptance was not audited
in this documentation pass; they do not mean the accepted handoff is
still blocked.

| Area | Current status / boundary |
|---|---|
| Key persistence | Bootstrap and namespace-qualified agent metadata are restored from gateway-namespace Secrets before HTTP startup (`cmd/mcp2rest/main.go`, `internal/keys/{bootstrap,agents}.go`). No mint-and-print bootstrap or raw-array recovery. |
| Delivery | Gateway owns authoritative records; hlctl owns retrieval and harness delivery. Legacy agent-namespace raw-only Secrets require reissuance/re-delivery. |
| Replicas | One active replica. Persistence is not live coordination of verification/revocation or upstream-credential caches. |
| Authorization | Global user/admin tiers, not per-app grants or a distinct management-admin scope; admin keys can call management/Skill writes. |
| Native MCP | No tools + `upstreamMCPURL` selects transparent HTTP forwarding, not a combined local/native inventory; no local Skill injection or per-tool tiers. Explicit per-tool passthrough remains supported. |
| Credentials | hlctl/provisioner owns gateway-namespace upstream Secrets. Startup/manual `reload_cache`, no watch or implicit registration reload. Native configured credentials fail closed; missing legacy mapped-tool credentials yield an empty string. |
| Downstream work | Phases 10–12 and provisioning/cluster acceptance in 13–14 are **unverified here**, not established incomplete or complete by old reports. |

## Phase 0 — Repo scaffolding

| Status | Task |
|---|---|
| done | Initialize Go module (`go.mod`) and root `README.md` |
| deferred | Choose/add `LICENSE` — original record found no org precedent; no license file is supplied here |
| done | Scaffold implementation packages; the original layout proposal is not an exact current inventory (there is no `internal/k8s` wrapper; owning packages use client-go directly) |
| done | Add `.github/copilot-instructions.md` |
| done | Add Go best-practices + Docker best-practices skills under `.github/skills/` (or org's skill convention) |
| done | Add `.github/workflows/ci.yml` (build + `go vet` + lint + `go test`) |
| done | Historical skeleton `main.go` milestone; replaced by real startup wiring |
| done | `.github/workflows/release.yml` defines GHCR image publishing; current registry accessibility and CI/deployment health are not revalidated by this tracker |

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
| done | Wire rendered tools into runtime dispatch through `internal/pipeline` and `internal/render.Renderer.Execute`; passthrough was deferred at this milestone and subsequently implemented in Phase 9/native follow-up |
| done | Integration test: full request→render→call→render→respond |

## Phase 3 — Two-tier (user/admin) authz

| Status | Task |
|---|---|
| done | `internal/pipeline`: ordered named-stage chain |
| done | Historical static-key test milestone; production startup now restores persistent keys, not hardcoded dev keys |
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

## Phase 5 — Key issuance + per-instance persistence (delivery design revised)

Current contract: authoritative agent Secrets and identity/tier/creation
metadata are persisted in the gateway namespace and restored before HTTP
startup. hlctl retrieves keys through `create_key`/`get_key` and owns harness
delivery. This supersedes the historical direct-delivery tasks below; see
README for namespace-scoped RBAC and the required one-time legacy reissue.

| Status | Task |
|---|---|
| done | `internal/keys`: mint/store/revoke |
| done | Namespace-qualified gateway-owned records: `keys.AgentSecretName(agentInstance, namespace)` → `mcp2rest-agent-<identity-hash>`; versioned `keys.json` includes tier/creation metadata (`internal/keys/agents.go`) |
| done | Own-namespace `manifests/rbac/agent-keys-role.yaml` (get/list/create/update). Original agent-namespace Secret writer and `keys-secrets-clusterrole.yaml` are legacy; do not bind the latter for current persistence. Kubernetes RBAC has no namespace-label-scoped ClusterRoleBinding. |
| done | `internal/keys/agents_test.go`: restart-safe issue/get/revoke/rotate, namespace isolation, malformed-state rejection, failed-write stability and concurrent/conflicting mutations; runtime tests authenticate persisted keys |
| unverified | hlctl-owned harness delivery/re-delivery and migration of any deployed legacy raw-only agent Secrets — external rollout, not established by local tests |

## Phase 6 — Management tool set + human-admin bootstrap

| Status | Task |
|---|---|
| done | Persistent bootstrap admin credential: create/load gateway-namespace `mcp2rest-bootstrap-admin`, restore verification on restart, fail closed on invalid data/access errors, and never print the key. Namespace-scoped RBAC and hlctl retrieval contract are documented in README. |
| done | `register_app` / `deregister_app` |
| done | `list_apps` / `get_manifest` |
| done | `create_key` / `list_keys` / `revoke_key` / `rotate_key` (durable agent repository); `get_key` and `reload_cache` added later |
| done | Local fake-client/runtime tests: register app, observe discovery without restart; routing visibility follows informer events, not a synchronous registration-table mutation |
| done | Integration test: non-admin key rejected on all management tools |

## Phase 7 — Built-in `skills` tool namespace

| Status | Task |
|---|---|
| done | RBAC: cluster-wide `Skill` CRD read; admin-gated write — `manifests/rbac/skills-clusterrole.yaml` grants cluster-wide read and write access to `skills.skills.homelab.dev`; tool-level admin gating still controls `create_skill`/`update_skill`/`delete_skill` because Kubernetes RBAC applies to the pod, not the individual MCP caller. |
| done | `list_skills` / `get_skill` on `/mcp` (any authenticated key); mapped app routes now use app-scoped names/filtering (Phase 25); native routes inject none |
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
| done | `internal/passthrough`: JSON-RPC relay client |
| done | Upstream tool-name tier tagging / filtering |
| done | Integration test: tier filtering + relay correctness |

This phase covers **explicit mapped passthrough tools**, not automatic
native discovery merged with local tools. The transparent native endpoint
implementation is recorded in the verified follow-ups below.

## Phase 10 — `hlctl` integration (homelab repo)

> Tracked upstream: [homelab#215](https://github.com/karlrissland/homelab/issues/215).
> This repo does not implement `homelab`-side work directly — per
> cross-repo policy, changes needed in another repo are requested via a
> GitHub issue for that team, not made here.
> All rows are **unverified**, replacing stale `not started` labels.
> The plan's 2026-10-01 report described partial progress; neither that
> report nor this repository establishes current downstream status.

| Status | Task |
|---|---|
| unverified | `mcpTools:` app.yaml schema block |
| unverified | Downstream render/validate at `app validate` time (original proposed package: `internal/mcpproxy`) |
| unverified | MCP client for registration, key retrieval/delivery and `reload_cache`; bootstrap contract uses full-admin persisted Secret, not a narrow registration-only key |
| unverified | Deploy-step wiring (mirrors `EnsureMCPServer`) |
| unverified | Test: `app validate` fails fast on malformed manifest |
| unverified | Test: real `app setup` results in app visible via `list_apps` |

## Phase 11 — mcp2rest platform-service provisioning (homelab repo)

> Tracked upstream: [homelab#216](https://github.com/karlrissland/homelab/issues/216).

| Status | Task |
|---|---|
| unverified | Provisioning package (Deployment/Service/Ingress/RBAC), mirrors `EnsureMCPServer`; this repo supplies RBAC/observability, not workload manifests |
| unverified | Wire into platform bootstrap (original intent: default-on, no opt-in); run one active gateway replica pending live cache coordination |
| unverified | Test: fresh cluster bootstrap → mcp2rest reachable at `mcp2rest.<dns.zone>`, persistent bootstrap/agent credentials restored and auth enabled |

## Phase 12 — Catalog pilot: MeTube (homelab-catalog repo)

> Tracked upstream: [homelab-catalog#103](https://github.com/karlrissland/homelab-catalog/issues/103).
> Pilot app switched from Gitea to MeTube 2026-09-30 — lower resource
> footprint to iterate against, and no API auth to wire up for the pilot.

| Status | Task |
|---|---|
| unverified | `mcpTools:` manifest + `maps/mcp/**` templates for MeTube |
| unverified | Reuse `metube-api` Skill research for tool/operation selection |
| unverified | End-to-end test: real agent calls real MeTube-backed tool; later live-debugging notes are historical observations, not current acceptance |

## Phase 13 — mcp2rest's own usage Skill

| Status | Task |
|---|---|
| done | Author `SKILL.md` content (how to use mcp2rest) |
| unverified | Bundle into Phase 11's downstream provisioning step |
| unverified | Test: `list_skills`/`get_skill` return it post-bootstrap, no extra step |

## Phase 14 — Observability

| Status | Task |
|---|---|
| done | Metrics middleware stage (`/metrics`, Prometheus) |
| done | Structured stdout logging |
| done | Grafana dashboard JSON + alert rules |
| done | Local `/metrics` exposition and request-recording tests (`internal/mcpserver/runtime_metrics_test.go`, `internal/pipeline/metrics_test.go`) |
| unverified | Live Prometheus scrape and dashboard pilot traffic; requires external provisioning/traffic, not proven by local metrics tests |

## Phase 15 — Deferred (explicitly out of scope for v1)

| Status | Task |
|---|---|
| deferred | Real governance/security pipeline stages (PII redaction, rate limiting, policy engine) |
| deferred | WASM (`wazero`) plugin loader implementation |
| deferred | Automatic key rotation |
| deferred | Per-(app, user) credential storage for non-impersonation-capable apps |
| deferred | Full per-tool ACLs beyond two-tier user/admin |

## Phase 16 — Per-tool upstream credential injection (homelab-api2mcp#1)

Resolves the credential-storage/injection gap flagged in
`homelab-api2mcp#1`: `App.UpstreamCredentialEnv`/`UpstreamMCPURL` existed
but nothing actually stored or injected a credential value. See
`docs/decisions/mcp2rest-plan.md` Open Decision 9 for the full resolved
design (two named upstream Secrets per app instance, native multi-key
`Secret.data`, full in-memory preload + manual `reload_cache`). The
original missing-credential fallback now applies only to mapped tools;
configured native endpoint credentials are required.

| Status | Task |
|---|---|
| done | `internal/manifest`: per-tool `UpstreamCredentialEnv`/`UpstreamMCPURL` overrides + `EffectiveCredentialEnv`/`EffectiveUpstreamMCPURL` fallback helpers; `Validate()` now checks the per-tool effective passthrough URL |
| done | `internal/upstreamcreds`: in-memory `Cache`, atomic `Reload(ctx, apps)` full-preload, typed lookup and Secret-name helpers. Missing mapped credentials yield `""`; API errors and missing configured native credentials fail reload, retaining the previous snapshot. |
| done | `internal/render.Context`: new `Credential` field + `credential` Liquid binding, mirroring the existing `caller` binding |
| done | `internal/pipeline`: new `CredentialResolver` interface; `NewRenderRequestStage` resolves `tool.EffectiveCredentialEnv(app)` into `RenderContext.Credential` before every call (rendered and passthrough alike); `RuntimeStages` threads the resolver through |
| done | `internal/passthrough/relay.go`: `Call` takes a `credential` parameter; non-empty values are injected as a bearer-scheme Authorization header via a wrapping `http.RoundTripper` (v1 passthrough auth convention -- no per-app configurable scheme yet) |
| done | `internal/adminapi`: new `reload_cache` admin-tier MCP tool, `CredentialReloader` interface, wired to `internal/upstreamcreds.Cache` |
| done | `cmd/mcp2rest/main.go`: construct the credential cache (namespace from `MCP2REST_NAMESPACE`, default `mcp2rest`), initial `Reload()` at startup, wire into both the admin API and the runtime handler |
| done | `manifests/rbac/upstream-creds-role.yaml`: new namespaced `Role` (not ClusterRole) granting `get`-only on Secrets in mcp2rest's own namespace |
| done | Unit tests: manifest fallback methods, `upstreamcreds` cache (incl. fail-open + deregister-drop behavior), pipeline credential binding, passthrough header injection, adminapi `reload_cache` |
| historical | Requested downstream upstream-Secret upsert/RBAC wiring via `homelab#217`; current implementation there is unverified |
| historical | Recorded resolution of `homelab-api2mcp#1`; current issue state not rechecked |

## Phase 17 — `get_key` admin tool (homelab-api2mcp#2)

Resolves `homelab-api2mcp#2` (flat `keys.json` array has no tier/app
metadata, blocking hlctl from reliably resolving a user-tier key for
OpenClaw auth-header wiring). See `docs/decisions/mcp2rest-plan.md`
§3.9 for the original config-templating rationale. Persistence later
replaced the raw-array mechanism: hlctl-owned delivery is now the general
contract, not just an exception for static config.

| Status | Task |
|---|---|
| done | Admin-tier `get_key(agentInstance, namespace, tier)`; current implementation reads the versioned authoritative document via `keys.AgentKeys.Get`, selects persisted tier metadata and reconciles verification. The original raw-array cross-reference/no-format-change design is superseded. |
| done | Unit tests: `TestGetKeyResolvesByTierNotArrayPosition`, non-admin-rejection coverage for `get_key` |
| historical | Recorded resolution of `homelab-api2mcp#2` |
| historical | Directed `homelab#220` to `get_key` rather than direct Secret reads; downstream adoption unverified |

## Phase 18 — `register_app` mints key tier from `AuthorizedAgent.tier` (homelab-api2mcp#4)

Resolves `homelab-api2mcp#4`: `register_app` always minted a `user`-tier
key for every entry in `authorizedAgents`, ignoring the agent instance's
own role. `AuthorizedAgent` had no `tier` field at all, so even after
`homelab#223`'s hlctl-side fix started sending one, mcp2rest silently
dropped it -- an Admin-tier agent (e.g. hlctl's Cluster Agent singleton)
never got an admin-tier key minted via registration, breaking its later
`get_key(tier: admin)` call indefinitely, with no self-healing on
redeploy.

| Status | Task |
|---|---|
| done | `internal/adminapi`: add `AuthorizedAgent.Tier` (`manifest.Tier`, optional, defaults to `user`); `RegisterApp` now mints/ensures a key matching each agent's own declared tier instead of a hardcoded `user` tier |
| done | Validate an explicitly-set invalid tier is rejected (not silently coerced) |
| done | Rename `RegisterAppResult.CreatedUserKeys` -> `CreatedKeys` (field could now legitimately contain admin-tier keys) |
| done | Unit test `TestRegisterAppMintsKeyTierFromAuthorizedAgent`: admin-tier agent gets an admin key even when the registering app's manifest only declares user-tier tools; omitted tier defaults to user; invalid tier is rejected |
| historical | Recorded resolution of `homelab-api2mcp#4`; current downstream adoption/issue status unverified |

## Phase 19 — `MCP2REST_DISABLE_AUTH` escape hatch

**Debug-only, not a deployment step.** With this enabled, every caller,
including an anonymous one, has admin privileges. Normal deployments
must leave it unset; this repository cannot confirm any cluster's current
setting. Historical operational request: temporarily disable authn/authz to
reduce moving parts while troubleshooting an unrelated OpenClaw/MeTube
onboarding issue, without ripping out the pipeline stages themselves.

| Status | Task |
|---|---|
| done | `internal/keys.Store`: add `DisableAuth()` -- once called, `Lookup` succeeds unconditionally (synthetic admin-tier record), for any key including an empty one |
| done | `internal/pipeline` authn stage: reorder the empty-key check after the lookup call, so a lookup that is unconditionally OK (e.g. a disabled store) can authenticate a request with no key presented at all |
| done | `cmd/mcp2rest/main.go`: read `MCP2REST_DISABLE_AUTH=true`, call `store.DisableAuth()` and log a loud warning at startup |
| done | Unit tests: `TestStoreDisableAuthBypassesEveryLookup`, `TestAuthenticationStageAllowsEmptyKeyWhenLookupIsAlwaysOK` |
| done | `README.md`: document the env var as a debugging-only escape hatch, not for production |

This single store-level toggle covers every call site that checks a key
(per-app tool calls' authn/authz stages and `appToolListFilter`'s
`tools/list` filtering) because they all ultimately read through the
same `*keys.Store` instance -- no separate flag needed per subsystem.
**Correction (see Phase 21): `adminapi`/`skillstools`'s own
`requireAdmin` checks were NOT actually covered by this toggle at the
time this phase was written** -- they each had their own empty-key
short-circuit that ran before ever consulting the store.

## Phase 20 — MCP session `instructions` and sharper tool descriptions

OpenClaw was getting confused about what mcp2rest is and how to use it
(e.g. trying to pass `get_manifest` output as if it both registered and
connected a tool). Root cause: every MCP session -- management and
per-app alike -- passed `nil` `ServerOptions` to `mcp.NewServer`, so the
`initialize` result's `instructions` field (the MCP-spec-blessed "how to
use this server" field) was always empty, and `Info` (the
`Implementation` sent as `serverInfo`) had no `Description` either.

| Status | Task |
|---|---|
| historical | Added `internal/mcpserver.Info.Description`; removed by the SDK pin in Phase 23. Current route-specific `Instructions` carry the guidance. |
| done | `internal/mcpserver/runtime.go`: build distinct `ServerOptions.Instructions` per route -- `managementInstructions` for `/mcp` (explains this is the control plane, most tools need admin tier, and that calling an app's real tools means connecting a NEW session to that app's own `/{app-name}/mcp` endpoint) and `appInstructions(appName)` for `/{app}/mcp` (explains these tools are real REST calls into that specific app, generic/app-name-driven so it's never a one-off for any single app) |
| done | `internal/adminapi`: sharpen `list_apps`/`get_manifest` tool descriptions to explicitly state they do NOT return a connection URL or API key and do NOT register/connect anything by themselves |
| done | Regression test: route-appropriate non-empty `InitializeResult().Instructions` on management/local app sessions. Historical `ServerInfo.Description` assertion was removed in Phase 23; native initialization is owned by upstream. |

## Phase 21 — Fix `requireAdmin`/`requireAuthenticated` empty-key short-circuit

Live-cluster bug found while validating `MCP2REST_DISABLE_AUTH=true` end
to end: `adminapi.requireAdmin` and `skillstools.requireAuthenticated`
(used by `skillstools.requireAdmin`) each rejected an empty `apiKey`
*before* ever calling into the key store, so a disabled store's
unconditional-success `Lookup` was never consulted for admin-tier tool
calls. Result: `initialize` succeeded with no key (handled by the
pipeline's authn stage, already fixed in Phase 19), but calling
`list_apps`/`get_manifest`/`create_skill`/etc. with no key still failed
with "admin key is required" even with auth disabled cluster-wide.

| Status | Task |
|---|---|
| done | `internal/adminapi.requireAdmin`: reorder to call `store.Lookup` first; only check `apiKey == ""` to pick the error message once lookup has already failed |
| done | `internal/skillstools.requireAuthenticated`: same reordering (its `requireAdmin` calls through to it) |
| done | Regression tests: `TestRequireAdminAllowsEmptyKeyWhenAuthDisabled`/`TestRequireAdminRejectsEmptyKeyWhenAuthEnabled` in both `adminapi` and `skillstools` packages |
| historical | Earlier debugging reproduced an empty-key management rejection with auth disabled. The original "after rebuild/redeploy should succeed" note was not proof of post-fix live acceptance; local regression tests verify the fix. No auth-disable rollout command is a setup instruction. |

## Phase 22 — Surface a literal `mcpEndpointPath` in list_apps/get_manifest

OpenClaw onboarded an app via `get_manifest`, then tried to call that
app's tools (e.g. `queue_download`) directly on the SAME management
session, and failed with "tool not found". Root cause: the only place
mcp2rest explained "these tools live on a different session, at
`/{app-name}/mcp`" was prose in the session-level `initialize`
`instructions` string (Phase 20) -- a field set once at connection time
that's easy for an LLM-driven client to lose track of by the time it's
reasoning about a `get_manifest` result several turns later. The actual
tool-call *result* payload (`manifest.App`) had no field at all carrying
this information, so the agent had no in-band way to rediscover it.

| Status | Task |
|---|---|
| done | `internal/manifest.App`: add `MCPEndpointPath` (e.g. `"/metube/mcp"`), documented as read-only/derived -- never required or read back from a `register_app` input payload, never persisted to the app's discovery ConfigMap |
| done | `internal/adminapi`: add `mcpEndpointPath(appName)` helper (`"/" + appName + "/mcp"`, mirrors `runtime.go`'s routing convention); populate it on copies returned by `list_apps` and `get_manifest` (never mutate the discovery table's own stored `*manifest.App` pointers) |
| done | Sharpen `list_apps`/`get_manifest` tool descriptions to point at the new field, spell out the full external URL template, and explicitly warn that calling a listed tool name on the current session will fail |
| done | Regression tests: assert `MCPEndpointPath` is populated correctly via both `API.GetManifest` and a real `list_apps`/`get_manifest` tool-call round trip |

## Phase 23 — Pin go-sdk below SEP-2549 "Cacheable list results" (OpenClaw strict-schema rejection)

OpenClaw reported `list_apps`/a proxied app's `tools/list` response had
"additional properties that don't match the expected schema" and refused
to use the tool at all. Root cause: `github.com/modelcontextprotocol/go-sdk`
v1.7.0+ unconditionally embeds two new top-level fields -- `ttlMs` and
`cacheScope` -- on every `tools/list`, `prompts/list`, `resources/list`,
`resources/templates/list`, and `resources/read` result (SEP-2549,
shipped in the go-sdk's `2026-07-28` protocol-version release). These
fields have no `omitempty` and are NOT gated by the negotiated protocol
version, so they appear even when the client negotiates the stable
`2024-11-05`/`2025-06-18` protocol -- a backward-compatibility gap in the
SDK itself. Any MCP client (like OpenClaw) that strictly validates a
result against the stable, published `ListToolsResult` JSON Schema (which
has no `ttlMs`/`cacheScope` properties and typically `additionalProperties:
false`) rejects the response outright.

| Status | Task |
|---|---|
| done | Pin `github.com/modelcontextprotocol/go-sdk` to `v1.6.1` (last version before SEP-2549's `Cacheable` fields and the `Implementation.Description` field were introduced) |
| done | `internal/mcpserver.Info`: drop `Description` (not present on `mcp.Implementation` in v1.6.1) -- the equivalent guidance already lives entirely in the per-session `Instructions` string (Phase 20), so no information is lost |
| done | Drop the one test assertion on `ServerInfo.Description` (`internal/mcpserver/runtime_test.go`) |
| historical | At the downgrade, an ad hoc local `tools/list` test reported no `ttlMs`/`cacheScope`/`serverInfo.description`; transparent native responses now remain upstream-owned |
| historical | Build/vet/test/lint reported clean after the downgrade, not a claim about current CI |
| note | Revisit pinning once go-sdk either version-gates SEP-2549 fields to the negotiated protocol version, or OpenClaw's client stops strictly validating against the pre-SEP-2549 schema -- tracked here, not a permanent architectural decision |

## Phase 24 — Surface `mcpInternalUrl` so in-cluster callers never need the external, TLS-fronted hostname

Live-debugging OpenClaw found its `MeTube MCP Tool` connector was
configured with the external ingress URL (`https://mcp2rest.prox.lab/
metube/mcp`) and permanently failed with `UNABLE_TO_VERIFY_LEAF_SIGNATURE`
-- Node.js inside the OpenClaw pod doesn't trust the ingress's cert CA.
Root cause: `list_apps`/`get_manifest` only ever told callers how to
build the *external* URL (`mcpEndpointPath` + prose "combine with the
cluster's DNS zone"), even though every agent harness mcp2rest serves
runs inside the same cluster and should never need to leave it (or trust
an external cert) to reach mcp2rest. This is not app-specific -- it would
recur for any app onboarded the same way, and is exactly the kind of
thing `homelab`#226 ("auto-wire agent harnesses' MCP connectors at
install time") needs to get right by construction, not by convention.

| Status | Task |
|---|---|
| done | `internal/manifest.App`: add `MCPInternalURL` (e.g. `"http://mcp2rest.mcp2rest.svc.cluster.local:8080/metube/mcp"`), documented the same way as `MCPEndpointPath` -- read-only/derived, never required or read back from a `register_app` payload |
| done | `internal/adminapi.API`: add `SetInternalBaseURL`/`internalMCPURL` (setter rather than a `New(...)` parameter, so every existing call site is unaffected); populate `MCPInternalURL` on both `list_apps` and `get_manifest` |
| done | `cmd/mcp2rest/main.go`: add `internalBaseURL(serviceName, namespace, addr)` helper and a new `MCP2REST_SERVICE_NAME` env var (default `"mcp2rest"`, mirrors the existing `MCP2REST_NAMESPACE` convention); wire `admin.SetInternalBaseURL(...)` at startup |
| done | Sharpen `list_apps`/`get_manifest` tool descriptions to tell any caller (human operator or future `hlctl` automation) to PREFER `mcpInternalUrl`, citing the exact `UNABLE_TO_VERIFY_LEAF_SIGNATURE` failure mode as the reason |
| done | Regression tests: `internalBaseURL` port-parsing edge cases (`cmd/mcp2rest/main_test.go`); `MCPInternalURL` populated correctly via both `API.GetManifest` and a real `list_apps`/`get_manifest` tool-call round trip, and left empty when unset (`internal/adminapi/adminapi_test.go`) |
| historical | Earlier live debugging reported an OpenClaw MeTube connector changed from 0 to 5 tools after switching to the internal URL, with doctor reporting `ok`; not current production health or fleet-wide rollout acceptance |
| note | `homelab`#226 is the long-term fix (hlctl should read/render `mcpInternalUrl` automatically for every agent instance it wires up); this phase only fixes mcp2rest's own side of the contract so that work (and any human operator working today) has a correct field to read from |

## Phase 25 — Scope `list_skills`/`get_skill` per app, with per-app tool names

Live usage found OpenClaw, asked via MeTube's own MCP session "what skills
do you have", returned the *entire cluster's* Skill catalog (Authentik,
cert-manager, Traefik, NFS, K3s, ...) instead of MeTube's own Skills. Root
cause: `RegisterReadTools` always listed/fetched Skills cluster-wide,
unconditionally -- this was Open Decision 8/plan §3.4, deliberately
accepted at the time ("revisit only if real usage surfaces a problem") and
now reversed because real usage did surface a problem. No `homelab` change
was needed: hlctl already labels every Skill CR with
`skills.homelab.dev/app=<appName>` (see `internal/skillscrd` in `homelab`),
so this was entirely mcp2rest-side filtering.

Also renamed the per-app tools from the generic `list_skills`/`get_skill`
to `list_<app>_skills`/`get_<app>_skill` (e.g. `list_metube_skills`,
`get_metube_skill`) so a weak model juggling several apps' MCP sessions in
one harness can't confuse one app's Skill tool for another's, or for
mcp2rest's own cluster-wide `list_skills`/`get_skill` on the management
endpoint (which keeps generic names and stays unfiltered). **Current
boundary:** management Skill reads accept any authenticated key, not
just admins. The later transparent native path injects no local Skill tools.

| Status | Task |
|---|---|
| done | `internal/skillstools.ListSkills`/`GetSkill`: add an `appScope string` parameter; Skills carrying `skills.homelab.dev/app=<other-app>` are excluded, Skills with no app label at all (global/cluster-wide Skills, e.g. mcp2rest's own usage Skill) remain visible from every app's session, `appScope=""` (mcp2rest's management session) stays fully unfiltered |
| done | `internal/skillstools.RegisterReadTools`: thread `appScope` through; when non-empty, rename the registered tools to `list_<slug>_skills`/`get_<slug>_skill` (new exported `ToolNameSlug` helper: lowercase, non-`[a-z0-9_]` -> `_`) and rewrite their descriptions to say so explicitly; empty `appScope` keeps `list_skills`/`get_skill` |
| done | `internal/mcpserver.serverForRequest`: management path calls `RegisterReadTools(server, apiKey, "")` (unchanged behavior); per-app path calls `RegisterReadTools(server, apiKey, app.Name)` |
| done | `internal/mcpserver.appInstructions`: documents the actual per-app tool names (`list_<app>_skills`/`get_<app>_skill`) and explicitly warns not to confuse them with the management endpoint's generic names |
| done | Tests: `internal/skillstools` -- new `TestListSkillsAppScopeFiltersToOwnAppPlusGlobal`, `TestGetSkillAppScopeRejectsOtherApp` (cross-app fetch reported as not-found, not leaked), `TestRegisterReadToolsNamesAreAppScoped` (asserts exact tool names for both a per-app and the management registration); updated existing per-app `ListTools()` assertions in `internal/mcpserver/runtime_test.go` and `runtime_passthrough_test.go` to the new `list_<app>_skills`/`get_<app>_skill` names |
| historical | Build/vet/format/lint/test were recorded clean at this milestone, not a current CI assertion |

## Verified implementation follow-ups — persistence and native endpoints

These are current local deliverables, not new proposed features or
claims about downstream deployment. They supplement the numbered
historical phases without renumbering them.

| Status | Deliverable / evidence |
|---|---|
| done | Persistent bootstrap `EnsureBootstrap`: create/load before listening, handle concurrent creation, validate existing data, never expose raw value in startup logs (`internal/keys/bootstrap.go`, `bootstrap_test.go`, `cmd/mcp2rest/main.go`) |
| done | Durable `AgentKeys`: versioned namespace-qualified metadata; startup restore; persist-before-verification mutation; create/ensure/get/revoke/rotate; corrupt-state/failed-write/conflict tests (`internal/keys/agents.go`, `agents_test.go`) |
| done | Native endpoint selection/validation: no `tools` plus absolute HTTP(S) `upstreamMCPURL`; explicit tools retain mapped behavior (`internal/manifest/manifest.go`, `manifest_test.go`) |
| done | Native gateway auth and transparent forwarding: upstream-owned inventory/session/SSE, gateway Authorization/cookie stripping, server-only bearer injection, configured-credential fail-closed, no synthetic Skill/mapped inventory (`internal/mcpserver/runtime.go`, `runtime_native_test.go`, `internal/passthrough/proxy.go`, `proxy_test.go`) |
| done | Upstream reload snapshots persisted ConfigMaps (including pending informer events), swaps cache only after full successful preload; native credential rotation/failure tests (`internal/adminapi/adminapi.go`, `adminapi_test.go`, `internal/upstreamcreds/upstreamcreds_test.go`) |
| deferred | Multi-active-replica verification/revocation coordination. Persistent Kubernetes state alone does not synchronize running processes; use one active gateway replica. |
| unverified | hlctl rollout of persistent-bootstrap retrieval, own-namespace RBAC, upstream-Secret upsert/reload, harness delivery, and legacy-key migration; catalog adoption and live fleet health |

### Known contract caveats (not completed work)

- Admin-tier keys authorize both mapped admin tools and control-plane
  management/Skill writes; there is no narrower bootstrap or per-app ACL.
- `register_app`'s typed payload/handler accept `authorizedAgents[].tier`,
  but its manually declared MCP input schema omits that field
  (`internal/adminapi/adminapi.go`). Do not treat schema omission as a
  separate supported privilege boundary.
- `register_app` upserts the ConfigMap before key assurance; an error does
  not imply all earlier writes were rolled back. Routing follows the
  informer; credential reload is explicit.
- `manifests/rbac/discovery-clusterrole.yaml` grants ConfigMap reads,
  not registration create/update/delete permissions; downstream
  provisioning must supply those additional grants.
- Legacy direct-delivery helpers/ClusterRole remain in the tree, but are
  not the authoritative runtime key repository. No namespace-label-based
  Secret RBAC restriction exists.
- Deferred governance/WASM, automatic rotation and per-user upstream
  credentials remain deferred (Phase 15), not silently completed by the
  existence of middleware or `caller.username`.
