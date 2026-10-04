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
| done | Publish repo to GitHub (`karlrissland/homelab-api2mcp`, public) + `.github/workflows/release.yml` publishing `ghcr.io/karlrissland/homelab-api2mcp:latest` on push to `main` — confirmed pullable with no credentials; resolves the "no image to pull yet" blocker flagged in homelab#216 |

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
| done | `internal/passthrough`: JSON-RPC relay client |
| done | Upstream tool-name tier tagging / filtering |
| done | Integration test: tier filtering + relay correctness |

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
| done | Metrics middleware stage (`/metrics`, Prometheus) |
| done | Structured stdout logging |
| done | Grafana dashboard JSON + alert rules |
| not started | Test: Prometheus scrape succeeds; dashboard shows pilot traffic — local tests now cover `/metrics` exposition and metrics recording, but live-cluster scrape validation + pilot-traffic dashboard verification still depend on Phase 11 provisioning and Phase 12 traffic |

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
design (one Secret per app instance, native multi-key `Secret.data`,
full in-memory preload + manual `reload_cache`, fail-open on missing
credentials).

| Status | Task |
|---|---|
| done | `internal/manifest`: per-tool `UpstreamCredentialEnv`/`UpstreamMCPURL` overrides + `EffectiveCredentialEnv`/`EffectiveUpstreamMCPURL` fallback helpers; `Validate()` now checks the per-tool effective passthrough URL |
| done | New `internal/upstreamcreds` package: in-memory `Cache`, `Reload(ctx, apps)` full-preload, `Credential(toolType, appInstance, key)` lookup, `APIKeysSecretName`/`MCPKeysSecretName` helpers — fail-open (missing Secret/key → `""`, never an error) |
| done | `internal/render.Context`: new `Credential` field + `credential` Liquid binding, mirroring the existing `caller` binding |
| done | `internal/pipeline`: new `CredentialResolver` interface; `NewRenderRequestStage` resolves `tool.EffectiveCredentialEnv(app)` into `RenderContext.Credential` before every call (rendered and passthrough alike); `RuntimeStages` threads the resolver through |
| done | `internal/passthrough/relay.go`: `Call` takes a `credential` parameter; non-empty values are injected as a bearer-scheme Authorization header via a wrapping `http.RoundTripper` (v1 passthrough auth convention -- no per-app configurable scheme yet) |
| done | `internal/adminapi`: new `reload_cache` admin-tier MCP tool, `CredentialReloader` interface, wired to `internal/upstreamcreds.Cache` |
| done | `cmd/mcp2rest/main.go`: construct the credential cache (namespace from `MCP2REST_NAMESPACE`, default `mcp2rest`), initial `Reload()` at startup, wire into both the admin API and the runtime handler |
| done | `manifests/rbac/upstream-creds-role.yaml`: new namespaced `Role` (not ClusterRole) granting `get`-only on Secrets in mcp2rest's own namespace |
| done | Unit tests: manifest fallback methods, `upstreamcreds` cache (incl. fail-open + deregister-drop behavior), pipeline credential binding, passthrough header injection, adminapi `reload_cache` |
| done | File/update a `homelab` issue: hlctl must write `<app-instance>-api-keys`/`<app-instance>-mcp-keys` Secrets (converging/upsert semantics) into mcp2rest's namespace, and bind the new namespaced Role to mcp2rest's ServiceAccount — filed `homelab#217` |
| done | Reply to/close `homelab-api2mcp#1` summarizing the implemented design |

## Phase 17 — `get_key` admin tool (homelab-api2mcp#2)

Resolves `homelab-api2mcp#2` (flat `keys.json` array has no tier/app
metadata, blocking hlctl from reliably resolving a user-tier key for
OpenClaw auth-header wiring). See `docs/decisions/mcp2rest-plan.md`
§3.9 for the full resolved design: a narrow, explicit exception to
"hlctl never touches raw key material," scoped to config-templated MCP
client registrations (OpenClaw's `provision-mcp-server.sh`) that have no
indirect secret-reference mechanism of their own.

| Status | Task |
|---|---|
| done | `internal/adminapi`: new admin-tier `get_key(agentInstance, namespace, tier)` MCP tool; resolves tier server-side by cross-referencing the Secret's raw key array against mcp2rest's own in-memory key store (same resolution `ensureTierKey`/`appendTierKey` already perform) -- no `keys.json` format change |
| done | Unit tests: `TestGetKeyResolvesByTierNotArrayPosition`, non-admin-rejection coverage for `get_key` |
| done | Reply to/close `homelab-api2mcp#2` summarizing the resolution |
| done | Comment on `homelab#220` directing hlctl to call `get_key` instead of reading the Secret directly |

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
| done | Reply to/close `homelab-api2mcp#4` summarizing the fix |

## Phase 19 — `MCP2REST_DISABLE_AUTH` escape hatch

Operational request: temporarily disable authn/authz cluster-wide to
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
| done | `internal/mcpserver.Info`: add a `Description` explaining mcp2rest is a shared REST-to-MCP proxy, not an app itself |
| done | `internal/mcpserver/runtime.go`: build distinct `ServerOptions.Instructions` per route -- `managementInstructions` for `/mcp` (explains this is the control plane, most tools need admin tier, and that calling an app's real tools means connecting a NEW session to that app's own `/{app-name}/mcp` endpoint) and `appInstructions(appName)` for `/{app}/mcp` (explains these tools are real REST calls into that specific app, generic/app-name-driven so it's never a one-off for any single app) |
| done | `internal/adminapi`: sharpen `list_apps`/`get_manifest` tool descriptions to explicitly state they do NOT return a connection URL or API key and do NOT register/connect anything by themselves |
| done | Regression test: assert `InitializeResult().Instructions`/`.ServerInfo.Description` are non-empty and route-appropriate for both a management and an app session |

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
| done | Verified live: `kubectl set env deployment/mcp2rest -n mcp2rest MCP2REST_DISABLE_AUTH=true`, confirmed a no-key `tools/call list_apps` failed before this fix and (after rebuild/redeploy) should succeed |

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
| done | Verified via an ad hoc local test that a live `tools/list` round trip no longer contains `ttlMs`/`cacheScope`/`serverInfo.description` |
| done | Full build/vet/test/lint clean after the downgrade |
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
| done | Verified live against the production cluster: re-pointed OpenClaw's `MeTube MCP Tool` connector at the internal URL via `openclaw mcp set` -- `openclaw mcp probe` went from 0 tools to 5, `openclaw mcp doctor` reports `ok` |
| note | `homelab`#226 is the long-term fix (hlctl should read/render `mcpInternalUrl` automatically for every agent instance it wires up); this phase only fixes mcp2rest's own side of the contract so that work (and any human operator working today) has a correct field to read from |

