# mcp2rest — REST→MCP Proxy: Feasibility & Architecture Plan

## Guiding Principles (north star for every design choice below)

This plan is intentionally still being designed on the fly — the
architecture below is our current best thinking, not locked-in. Every
choice should keep optimizing for:

- **Simple** — fewest moving parts/protocols/identities that solve the
  problem today (e.g. one MCP surface instead of MCP + a separate admin
  REST API).
- **Fast, low resource** — a shared, low-footprint pod; avoid anything
  that adds idle cost or a heavyweight runtime.
- **Grows with the project** — the pipeline/tool-definition shape must
  leave room for governance/security stages, per-tool scoping, key
  rotation, etc. later, without a rewrite. We are deliberately deferring
  those, not designing them away.

Expect this document to keep changing shape as we think it through further
— sections below are marked with revision notes where a later conversation
turn changed an earlier decision, so the reasoning trail isn't lost.

## 1. Problem & Goal

Agent-over-HTTP capability varies wildly across harnesses; MCP usage is
stable. Goal: a standardized, low-overhead way to expose any homelab-catalog
app's REST API to in-cluster agents as proper MCP tools, so agent
integration is uniform regardless of harness. The app/service owner supplies
Liquid templates (request + response) plus a small manifest describing the
MCP surface; a shared proxy pod renders/executes them at request time. The
pipeline shape must allow AI governance/security stages to be added later
without re-architecting.

## 2. Feasibility Assessment: **HIGH — no architectural blockers**

Strong existing precedent in `homelab` / `homelab-catalog` de-risks most of
this:

- **Liquid-as-integration-contract is already proven**: `providermap`
  (`internal/providermap/providermap.go`) renders per-app Liquid templates
  against a deterministic context and injects the result into Helm values —
  same idea, one-shot render instead of per-request.
- **A generalized "hlctl provisions a dedicated MCP server, apps register
  into it" hook already exists**: `clusteragent.EnsureMCPServer` deploys
  `kubernetes-mcp-server` per Cluster-Agent instance; the
  `provisionMcpServer` app.yaml hook already patches Hermes'
  `config.yaml` with `{type: streamable-http, url: ...}` entries
  (`hooks/provision-mcp-server.py`). mcp2rest slots into this exact
  registration contract.
- **The exact gap is already documented and reasoned about**:
  `docs/decisions/mcp-server-and-cluster-agent.md` §2.8/§2.8a/§5 — today,
  "apps with an API" are discovered via a `Skill` CRD labeled
  `skills.homelab.dev/topic=api`, documented as raw curl examples, and
  accessed with **full admin-level credentials** handed to the agent
  (explicitly flagged there as deferred hardening work, §5). mcp2rest is the
  natural next increment: real MCP tool schemas instead of curl-in-docs, and
  narrower, per-agent scoped keys instead of admin credentials.
- **Cross-namespace, label-selector discovery of app-owned K8s objects is
  already a working pattern**: `internal/skillscrd` + its
  `hlctl-skill-reader` ClusterRole. mcp2rest's proxy-side discovery reuses
  the same shape.
- **Credential injection without ever touching Helm/cluster.yaml is already
  solved**: `internal/credentials` + `internal/aicreds` (secret staging,
  per-app Secret upsert, env-var-by-reference).

**What is genuinely net-new** (the real effort, not just wiring):
1. A **runtime, per-request, bidirectional Liquid transform engine**
   (request in → REST call → response out) — `providermap` only proves
   one-shot render; nothing today executes Liquid per inbound MCP call.
2. A **shared, multi-tenant MCP proxy** with per-app-instance routing,
   K8s-native config discovery (push + watch/informer), and **per-agent
   API-key issuance/authorization** — none of this exists yet anywhere in
   the stack.
3. **Cross-repo wiring**: a new admin API contract between `hlctl`
   (`homelab`) and the new proxy (`homelab-api2mcp`), plus a new
   `homelab-catalog` authoring convention (`mcpTools:` + `maps/mcp/**`).

None of this is blocked by anything structural — it is genuinely buildable
with the existing conventions — but it is a **three-repo effort**
(`homelab-api2mcp`, `homelab`, `homelab-catalog`) with real integration
surface, not a weekend project.

## 3. Confirmed Decisions

- **Topology**: one shared, multi-tenant proxy Deployment (not one pod per
  app) — 2+ replicas behind a single ClusterIP Service.
- **External routing**: single host, path-routed per app instance —
  `https://mcp2rest.<dns.zone>/<app-instance>/mcp`.
- **Transport**: MCP Streamable HTTP (matches `kubernetes-mcp-server` and
  Hermes' existing `type: streamable-http` registration).
- **Tech stack**: **Go**. All three languages (Go/Rust/C#) have Tier-1
  official MCP SDKs with Streamable HTTP support and comparable per-request
  overhead for a proxy workload — the deciding factor is ecosystem fit, not
  raw throughput: `hlctl` and `kubernetes-mcp-server` are already Go, the
  team already depends on `github.com/osteele/liquid` in Go, and a second
  toolchain (Rust/C#) buys negligible real-world benefit here while adding a
  second build/release pipeline to maintain. Single static binary,
  distroless image, low idle footprint — matches the "low overhead pod"
  goal directly.
- **Governance pipeline**: build the request path now as an **ordered,
  named middleware chain** (authn → authz → tool-resolve →
  render-request → upstream-call → render-response → respond), with any
  future governance/security stage insertable without re-architecting.
  No real policy logic in v1 — stages beyond authn/authz are no-ops or
  pure observability (logging/metrics) for now.
- **Tool definition shape** (your design, adopted): for each REST operation
  an app wants exposed, the app author supplies a **`request.liquid`** and
  a **`response.liquid`**, plus one **manifest (YAML/JSON)** per app
  describing the MCP surface — what the MCP server is, which
  APIs/operations/tools it exposes, and each tool's parameters
  (JSON-schema-shaped). All of this (manifest + templates) is delivered as
  **one ConfigMap scoped to the app's own namespace** — Kubernetes is the
  durable state store; no separate database.
- **Registration flow (revised — see 2026-09-27 refinement below)**: at
  deploy time, `hlctl` writes/updates that ConfigMap, then calls
  mcp2rest to register it. The proxy **validates** the manifest+templates
  and either returns an error (surfaced to the operator like any other
  deploy-time validation failure) or accepts the registration. **mcp2rest
  — not `hlctl` — mints the per-agent API keys and writes the Secret
  directly into each authorized agent's own namespace.** `hlctl` never
  handles raw key material; it only supplies the current allow-list
  (which agents/namespaces should be authorized) as part of the
  registration call.
- **Self-healing recommendation (added by me, confirm at kickoff)**: in
  addition to the push-on-deploy path, the proxy also **lists/watches**
  ConfigMaps by a label (e.g. `mcp2rest.homelab.dev/app=<name>`) cluster-wide
  on its own startup and on change — this is the same
  list/watch-by-label-selector shape `internal/skillscrd` already uses, and
  it means a proxy restart (or a missed push because the proxy was
  temporarily down during some other app's deploy) **self-heals** from the
  ConfigMaps already sitting in etcd, instead of requiring an operator to
  re-run `hlctl app setup` for every app. The ConfigMap remains the single
  source of truth either way; the admin-API push is purely the synchronous
  fast path that lets `hlctl` get an immediate validation result and API
  keys back in the same command invocation.
- **Upstream auth**: reuse `internal/credentials` + `internal/aicreds`
  as-is — the manifest references a credential env-var name; `hlctl`
  resolves it from the existing per-app credential store and hands the
  *value* to the proxy only via the registration call (never written to
  `app.yaml`/`cluster.yaml`/Helm release history).
- **Config-declaration location**: new `mcpTools:` block in `app.yaml`,
  schema-owned by `hlctl` (same ownership convention as `providerMap`).
- **Key/scope ownership (revised 2026-09-27)**: dropped the earlier
  `hlctl mcp2rest key create|list|revoke` CLI idea. mcp2rest owns key
  material end-to-end — minting, storing, rotating, revoking, and writing
  the Secret into the agent's own namespace — with **zero** involvement
  from `hlctl` beyond the one registration call. `hlctl`'s only remaining
  job is expressing *intent* (the `allowedApis` list) and triggering
  registration at deploy time.

### 3.1 Refinement (this session, 2026-09-27): single MCP surface, no separate admin API

Talking it through further, two more decisions:

- **No bespoke admin REST API at all.** mcp2rest exposes exactly **one**
  interface — MCP (Streamable HTTP) — with two tool namespaces:
  1. **Per-app proxied tools** — the regular case, one tool set per
     registered app instance, scoped by the caller's API key.
  2. **A reserved management tool set** on mcp2rest itself
     (`register_app`, `deregister_app`, `list_apps`, `create_key`,
     `list_keys`, `revoke_key`, `rotate_key`, `get_manifest`, …), gated by
     an **admin** scope. Ongoing key lifecycle management is done by
     *talking to an agent* that holds the admin scope — not a new hlctl
     CLI, not a second bespoke API. This is the "leverage our agents for
     this instead of building another API" call.
  - `hlctl` becomes a **minimal MCP client** for exactly one purpose:
    calling `register_app`/`deregister_app` at deploy time, authenticated
    with a bootstrap/admin key `hlctl` holds. Since Streamable HTTP MCP is
    JSON-RPC 2.0 over a single HTTP POST endpoint, this does not require
    pulling in a full MCP client SDK — a small typed JSON-RPC helper in
    `internal/mcpproxy` is enough for the one call `hlctl` needs to make.
  - This unifies the protocol surface (one auth model, one transport, one
    schema style) instead of running a REST admin API alongside the MCP
    server.

### 3.2 Per-tool admin/user tiers (added 2026-09-30)

An app can surface more than one MCP tool, and each tool can be annotated
in its manifest as `tier: user` or `tier: admin` (e.g. Gitea's
`list_my_repos` vs. `admin_create_user`). Resolved model (kept simple per
the Guiding Principles): **a key is issued as `user` or `admin` for a
given app**; `admin` implies `user`. A call to an `admin`-tier tool with a
`user`-tier key is rejected at the authz pipeline stage. Full per-tool
allow-lists (beyond this two-tier split) remain a Phase-2 refinement —
this is *not* the same thing as the separate "admin scope" that gates
mcp2rest's own management tool set (§3.1) — that is a proxy-wide
control-plane privilege; this is a per-app, per-tool data-plane tier.

### 3.3 Multi-user / per-caller identity scoping — e.g. Penpot (added 2026-09-30)

Some apps' REST APIs are themselves multi-user (Penpot, etc.) — an
agent acting "on behalf of" one end user must only touch that user's
records. Resolved for v1:

- **Keys are minted per agent *instance*, not per agent role/app.** This
  is a real, needed departure from the existing `aicreds.SecretName`
  precedent (keyed only by app name, shared across every Hermes user
  instance today) — Penpot-style scoping is meaningless without knowing
  *which* end user a given call is for. Concretely: one Hermes user's
  Helm-release instance gets its own mcp2rest key(s), not a key shared
  across every Hermes instance in the `hermes` namespace.
- **Secret naming must include the instance name**, not just the app
  name — `<agent-instance>-mcp2rest-keys`, delivered into that instance's
  namespace (see Open Decision 6 for the exact RBAC/naming shape).
- **v1 upstream-scoping mechanism: header/param impersonation only.** The
  render context for `request.liquid` gains a `caller` object
  (`{agentInstance, username, tier, appInstance}`). For apps whose shared
  admin-level credential already supports scoping a call to one end user
  via a header/query param/path segment (the pattern Penpot's own API, or
  Plex's managed-user model, would need to support), the template uses
  `caller.username` to build that. **No new per-(app,user) credential
  storage in v1** — that (storing a distinct upstream token per end user,
  for apps that require it instead of supporting impersonation) is an
  explicit Phase-2 candidate, deferred because it's meaningfully more
  work than reusing the existing per-app credential store.
- **Practical v1 consequence**: apps that require true per-user tokens
  (no shared-admin-plus-impersonation option) are simply **not yet
  supportable as multi-user mcp2rest tools** until Phase 2 lands. This is
  an explicit, accepted v1 gap, not a silent one.

### 3.4 Built-in "skills" tool namespace (added 2026-09-30)

In addition to per-app proxied tools and the management tool set,
mcp2rest ships a third, always-present built-in tool namespace wrapping
the existing `Skill` CRD system (`internal/skillscrd`):

- `list_skills`, `get_skill` — read, available to **any** authenticated
  key. Per your answer, these are **not** scoped down to the calling
  key's authorized apps — any valid key can read any Skill cluster-wide
  (see Open Decision 8 for the accepted-risk note on this).
- `create_skill`, `update_skill`, `delete_skill` — write, **admin-scope
  only** (the same proxy-wide admin scope from §3.1, not the per-app
  `admin` tier from §3.2 — writing a Skill is a control-plane action, not
  a per-app data-plane one).
- **Direction of travel: replaces the Cluster Agent's direct K8s RBAC
  read access to Skill CRDs** (per your answer) — today the Cluster Agent
  lists/reads `Skill` CRDs directly via its own `hlctl-skill-reader`
  ClusterRole; going forward, that read path should move behind this
  mcp2rest tool too, for every agent including the Cluster Agent. Note:
  actually retiring the Cluster Agent's existing direct RBAC is a
  `homelab`-side follow-up, not something this plan changes by itself —
  called out here so it isn't lost.
- mcp2rest therefore also needs its own read (and, for admin calls,
  write) RBAC to `Skill` CRDs cluster-wide — essentially inheriting
  `hlctl-skill-reader`'s scope itself, plus a write grant for the admin
  path.

### 3.5 Passthrough mode — apps with their own MCP server (added 2026-09-30)

For apps that already ship a native MCP server (nothing to transform),
a manifest can declare `type: passthrough` with an `upstreamMcpUrl`
instead of `request.liquid`/`response.liquid` pairs. mcp2rest then:

- Performs its normal authn/authz(scope) pipeline stages exactly as for a
  regular proxied app (same API-key/tier model, same ingress path
  convention) — the point is reusing the same auth/routing machinery, not
  rebuilding it per passthrough app.
- **Relays** the JSON-RPC call to the upstream MCP server, skipping the
  Liquid render stages entirely.
- Can **tag specific upstream tool names as `admin`-only** in the
  passthrough manifest, filtering what a `user`-tier key even sees in its
  tool list — this directly mirrors `kubernetes-mcp-server`'s own
  `toolsets`/`denied_resources` filtering precedent already in this
  codebase (`clusteragent/mcpserver.go`'s `buildMCPServerConfigTOML`), just
  applied to an arbitrary upstream MCP server instead of a bundled one.

### 3.6 Plugin extensibility — reserved, not built in v1 (added 2026-09-30)

Even though no 3rd-party plugin support ships in v1, the pipeline design
must not preclude it later. Resolved direction: **WASM-loadable pipeline
stages**, using a pure-Go WASM runtime (e.g. `wazero`) rather than Go's
native `plugin` package (which is fragile, platform/version-locked, and
effectively Go-only for plugin authors). This keeps mcp2rest itself a
single static Go binary while allowing a future governance/security
vendor (or anyone) to ship a sandboxed stage in whatever language compiles
to WASM, hot-loaded into the same ordered pipeline (§3 Confirmed
Decisions' `authn → authz → tool-resolve → render → call → render →
respond` chain) as an additional named stage. No implementation in v1 —
this only constrains the pipeline's internal stage interface to be
"WASM-loadable-shaped" from the start so it isn't a rewrite later.

### 3.7 Meta-Skill: teaching agents how to use mcp2rest itself (added 2026-09-30, RESOLVED 2026-09-30)

Separate from §3.4's built-in *skills* tool (which exposes *other apps'*
Skills), agents also need to know how mcp2rest itself works: how to
discover which app-instance paths/tools they're authorized for, how the
user/admin tier split behaves, and how errors from the pipeline surface.
**Resolved — keep it simple**: no new platform-skill directory
convention needed. `homelab-api2mcp` ships its own `Skill` CRD source
content (a `SKILL.md`-shaped file) alongside its own deployment manifests,
and when mcp2rest is deployed as a platform service, that Skill is applied
to the cluster like any other — discoverable via `list_skills`/
`get_skill` (§3.4) exactly the same way an app's own Skills are. mcp2rest
dogfoods its own built-in skills tool for its own documentation; no
separate mechanism required.

## 4. Open Decisions — confirm before/at implementation kickoff

Flagging these explicitly rather than assuming, matching this org's own
"resolve before implementation, don't re-litigate per PR" convention
(`docs/decisions/mcp-server-and-cluster-agent.md` §2.8a):

1. ~~Which agents get keys for which apps.~~ — **RESOLVED 2026-09-30:
   auto-grant.** Deploying an app with an `mcpTools` manifest is itself
   the opt-in — every agent instance automatically receives a
   `user`-tier key for every app registered with mcp2rest, no separate
   `cluster.yaml` allow-list needed. `admin`-tier keys still require an
   **explicit grant** (not auto-granted to anything) — see Open
   Decision 5 for who/how that's requested. No new `cluster.yaml` schema
   needed for this; `hlctl` simply requests a `user`-tier key for every
   authorized agent instance at registration time.
2. ~~Scope granularity in v1~~ — **RESOLVED 2026-09-30**, see §3.2: a
   key is issued as `user` or `admin` per app (`admin` implies `user`);
   full custom per-tool ACLs remain a Phase-2 refinement.
3. ~~Where does the proxy itself deploy from?~~ — **RESOLVED 2026-09-30**:
   `homelab-api2mcp` is containerized and published as a **public GHCR
   image** built by its own repo's CI/CD — the exact same distribution
   pattern `homelab-hub` already uses (`ghcr.io/karlrissland/homelab-hub`,
   `src/hub/app.yaml`'s `image.repository`/`image.tag` fields). `hlctl`
   pulls that image to deploy it, the same way it deploys Hub. The one
   difference from Hub: **mcp2rest is included by default as part of the
   platform**, not a `homelab-catalog` app a user opts into installing —
   it's provisioned automatically alongside the other platform/core
   services (Traefik, Prometheus, Grafana), with exactly one cluster-wide
   instance, no per-user choice to make. (Mechanically this still means
   `hlctl` needs a small new "pull this GHCR image + deploy its
   Deployment/Service/Ingress/RBAC" provisioning step, mirroring
   `EnsureMCPServer`'s own shape, rather than going through the
   `custom-helm` catalog-app install path Hub uses — since mcp2rest has no
   per-user Helm values to configure.)
4. ~~Key rotation/revocation lifecycle.~~ — **RESOLVED 2026-09-30**:
   manual-only for v1 — issue-once + manual revoke via mcp2rest's own
   `revoke_key` MCP tool (talk to an admin-scoped agent). Automatic
   rotation is explicitly **documented as a future feature**, not built,
   matching this org's own precedent of shipping permissive-but-working
   first and hardening once real usage is known (§5 of the same decision
   doc).
5. ~~Who holds the "admin" scope for mcp2rest's management tools?~~ —
   **RESOLVED 2026-09-30: the human cluster admin only** — not the
   Cluster Agent singleton (rejecting the earlier recommendation). At
   bootstrap (mcp2rest's first deploy as a platform service, Open
   Decision 3), mcp2rest mints exactly **one** admin-scope key and
   surfaces it out-of-band to the person running the install (mirrors
   `kubeadm`-style one-time bootstrap-token delivery — printed once at
   install time / written to a Secret only the installing operator's own
   kubeconfig can read), rather than writing it into any agent instance's
   namespace the way per-app `user`/`admin` keys are (§3.3). The cluster
   admin then drives mcp2rest's management tools (`register_app`,
   `create_key`, `revoke_key`, the `skills` write tools, etc.) from
   whatever agent/tool session they personally use — this CLI, `hlctl`,
   or any other MCP client they authenticate with that key. No other
   identity (Cluster Agent included) is auto-granted admin scope; if the
   admin later wants the Cluster Agent to perform admin-tier actions on
   its behalf, that's a distinct, explicit future decision (handing the
   Cluster Agent the admin key, or minting it a second admin-scope key),
   not something this plan does by default. `hlctl`'s own separate
   narrower bootstrap key (registration-only, scoped to just
   `register_app`/`deregister_app` for its one deploy-time call, §3.1) is
   unaffected by this — it remains a distinct, lower-privilege credential
   from the human admin's full admin-scope key.
6. **Secret-write privilege scope.** mcp2rest now needs **write** access
   to Secrets in every agent namespace (not just read/list like
   `hlctl-skill-reader`) — a real privilege increase worth a dedicated
   security note, not just an RBAC afterthought. **RESOLVED 2026-09-30:
   broad ClusterRole** (ship simple now, harden later) — rather than
   dynamically provisioning a per-namespace Role/RoleBinding at
   registration time (rejected as unnecessary moving parts for v1, since
   Kubernetes RBAC can't restrict by Secret *name* pattern anyway, only by
   namespace/label). Concretely: one ClusterRole granting
   create/update/get on Secrets, scoped to namespaces already carrying an
   agent-instance label (not truly cluster-wide-unscoped), bound to
   mcp2rest's ServiceAccount. Secrets are named `<agent-instance>-mcp2rest-keys`
   (not `<app>-mcp2rest-keys`, per §3.3's per-instance key model) —
   revisit toward per-namespace least-privilege once real usage is known
   (mirrors §5's own "ship working, harden later" precedent).
7. ~~Where does the mcp2rest usage Skill live?~~ — **RESOLVED
   2026-09-30**, see §3.7: `homelab-api2mcp` ships its own Skill CRD
   source alongside its deployment manifests; it's applied at deploy time
   like any other Skill and read back via mcp2rest's own `list_skills`/
   `get_skill` tools — no new platform-skill directory convention needed.
8. ~~Privilege note on `all-skills-any-key`~~ — **confirmed 2026-09-30,
   no change.** Any authenticated key — not just admin-tier or
   Cluster-Agent-equivalent — can read any Skill cluster-wide via the
   built-in skills tool, regardless of which apps that key is otherwise
   scoped to. Deliberate breadth-over-least-privilege choice (some
   Skills, e.g. an app's `security`/`authentication` topic, may contain
   more operational detail than a narrowly-scoped per-user agent strictly
   needs). Recorded, not re-litigated — revisit only if real usage
   surfaces a problem, same "ship working, harden later" posture as
   item 6.

## 5. Architecture

### 5.1 `homelab-api2mcp` (this repo) — the mcp2rest proxy binary

- Go; official MCP Go SDK (Streamable HTTP transport);
  `github.com/osteele/liquid` (same library `hlctl` already depends on, for
  consistent templating semantics across the platform).
- **Single MCP (Streamable HTTP) surface — no separate admin REST API.**
  Three tool namespaces on `POST /{app-instance}/mcp`:
  - **Per-app proxied tools** — the regular case, one tool set per
    registered app instance, Bearer-key authenticated, scope-checked
    against both the app-instance in the path and the key's `user`/`admin`
    tier (§3.2). Manifests may declare a tool as `type: passthrough`
    (§3.5) to relay directly to an app's own MCP server instead of
    rendering Liquid templates.
  - **Built-in `skills` tools** (§3.4) — `list_skills`/`get_skill` (any
    key), `create_skill`/`update_skill`/`delete_skill` (admin scope).
  - **Reserved management tools** on mcp2rest itself (`register_app`,
    `deregister_app`, `list_apps`, `create_key`, `list_keys`, `revoke_key`,
    `rotate_key`, `get_manifest`), gated by an **admin** scope. mcp2rest
    mints keys, owns them end-to-end, and **writes the Secret directly
    into the authorized agent instance's own namespace** itself —
    `hlctl` is never handed raw key material.
- **Keys are minted per agent *instance*** (§3.3), not per role/app — the
  render context passed to `request.liquid` includes a `caller` object
  (`{agentInstance, username, tier, appInstance}`) so multi-user apps
  (Penpot, etc.) can scope the upstream call via header/param
  impersonation in v1.
- **Routing table**: in-memory, keyed by app-instance → tool set; built
  from a K8s informer over ConfigMaps labeled
  `mcp2rest.homelab.dev/app=<name>` (list on startup, watch for updates),
  and updated synchronously the moment `register_app` is called.
- **Request pipeline** (ordered, named stages, each independently
  swappable/insertable later, and designed to be **WASM-loadable** so a
  future stage can be a sandboxed 3rd-party plugin without a rewrite,
  §3.6): `authn → authz(scope+tier) → tool-resolve → render(request.liquid)
  → upstream HTTP call (with injected credential) → render(response.liquid)
  → respond`. v1 governance-adjacent stages (logging/metrics) are
  pass-through observability only. Passthrough-mode tools skip the two
  `render(...)` stages and relay directly to the upstream MCP server.
- Structured stdout logging + `/metrics` (Prometheus) — fits the existing
  observability convention (every workload's logs/metrics are scraped the
  same way, per §2.10 of the decision doc).
- Distroless image, single static binary, minimal resource requests
  (mirrors `kubernetes-mcp-server`'s `25m`/`64Mi` baseline in
  `mcpserver.go`).
- K8s manifests: Deployment (2+ replicas), ClusterIP Service, Ingress at
  `mcp2rest.<dns.zone>`, and RBAC covering: (a) read/watch ConfigMaps
  across namespaces by label (mirrors `hlctl-skill-reader`), (b) read (and,
  for admin calls, write) `Skill` CRDs cluster-wide (§3.4), and (c) write
  access to Secrets named `<agent-instance>-mcp2rest-keys`, restricted to
  namespaces already carrying an agent-instance label (Open Decision 6).

### 5.2 `homelab` (`hlctl`) changes

- New `app.yaml` schema block `mcpTools:` (path to a manifest describing
  tools/tiers/passthrough-vs-rendered + `maps/mcp/**/request.liquid` /
  `response.liquid` per rendered tool, analogous to `providerMap.template`).
- New package `internal/mcpproxy` (mirrors `internal/providermap`):
  render + validate the manifest/templates at `app validate` time so
  authoring mistakes fail fast, before any deploy.
- Deploy-time wiring (mirrors `cluster_agent_reconcile.go`'s
  `EnsureMCPServer` call): after a successful app install, build/update the
  app's ConfigMap, then call mcp2rest's `register_app` MCP tool **once per
  authorized agent instance** (§3.3 — not once per app) via a small typed
  JSON-RPC helper — `hlctl` acting as a minimal, single-purpose MCP client,
  authenticated with its own bootstrap/admin key — passing the current
  set of authorized agent instances (per-app `user`-tier keys are
  auto-granted to every agent instance per Open Decision 1 — no
  `allowedApis` allow-list needed). `hlctl` does **not** receive or store
  any per-agent key — mcp2rest mints and delivers those itself, directly
  into that instance's namespace.
- No new `cluster.yaml` block needed for per-app grants — Open Decision 1
  resolved to auto-grant `user`-tier keys to every agent instance.
- No new `hlctl mcp2rest key` CLI — dropped in favor of mcp2rest's own
  management MCP tools, driven by the human cluster admin's own agent/tool
  session using their bootstrap admin-scope key (Open Decision 5).
- New docs: `docs/mcp2rest.md` (user-facing contract, mirrors
  `docs/ai-providers.md`) and a
  `docs/decisions/mcp2rest.md` capture doc (this org's own convention).

### 5.3 `homelab-catalog` changes

- Apps opt in with `mcpTools:` + `maps/mcp/**` in their own directory —
  same authoring shape as `providerMap`/`maps/*.liquid` today.
- **Recommended pilot app: Gitea.** It already has a mature `gitea-api`
  Skill enumerating exactly the endpoints worth exposing — that research is
  directly reusable as the first real `mcpTools` manifest, and validates
  the whole pipeline end-to-end on one real app before wider rollout
  (mirrors this org's own "thin vertical slice first" phasing principle).

## 6. Relationship to the existing "api" Skill / Cluster Agent credential flow

- Does **not** replace the `Skill` CRD `api` topic — it remains useful
  human/agent-readable documentation and a fallback for any endpoint not
  yet wrapped as an MCP tool.
- **Does** replace raw curl-with-admin-credential access for any endpoint
  wrapped as an MCP tool: narrower, schema-checked, and — via per-agent
  scoped keys — no longer requires handing an agent the app's full
  admin-level credential. This directly addresses the "app credential
  scope-down" hardening item this org already flagged as future work
  (§2.8/§5 of the existing decision doc).
- `EnsureMCPServer`/`kubernetes-mcp-server` (the Cluster Agent's own
  K8s-introspection MCP server) is unaffected and separate — that is
  cluster-control-plane access; mcp2rest is app-REST-API access.

## 7. Phased delivery plan (small, independently testable phases)

Each phase below is scoped to be independently buildable, independently
testable, and independently mergeable — no phase requires "big bang"
integration with a later phase to prove itself. Phases are numbered in
dependency order; a phase's "Depends on" lists strict prerequisites. This
replaces the earlier coarse Phase 1-5 grouping with something granular
enough to track in `tasks.md`.

- **Phase 0 — Repo scaffolding.**
  - *Goal*: `homelab-api2mcp` exists as a buildable, lintable, CI-checked
    Go module, with nothing functional yet.
  - *Deliverables*: `go.mod`, directory layout (§ Copilot instructions'
    layout section), `.github/workflows/ci.yml` (build + vet + lint +
    test), `.github/copilot-instructions.md`, root `README.md`, `LICENSE`.
  - *Test/acceptance*: CI passes on an empty/skeleton `main.go` that just
    prints a version string and exits 0.
  - *Depends on*: nothing.

- **Phase 1 — MCP server skeleton, no transform yet.**
  - *Goal*: a real MCP Streamable HTTP server boots, answers
    `initialize`/`tools/list`/`tools/call` for exactly one hardcoded
    "echo" tool (no Liquid, no REST upstream, no auth).
  - *Deliverables*: `internal/mcpserver` package wrapping the official Go
    MCP SDK; `cmd/mcp2rest/main.go`.
  - *Test/acceptance*: an MCP client (or raw JSON-RPC via `curl`) can call
    the echo tool and get back a deterministic response; unit test covers
    the JSON-RPC envelope.
  - *Depends on*: Phase 0.

- **Phase 2 — Static single-app Liquid transform.**
  - *Goal*: prove the genuinely-new piece — a real inbound MCP tool call
    is rendered through `request.liquid`, executed as a real HTTP call
    against one real upstream (Gitea, using a manually-supplied token via
    env var for this phase only), and the HTTP response is rendered back
    through `response.liquid` into the MCP tool result.
  - *Deliverables*: `internal/render` (Liquid render context + execute),
    one hardcoded manifest + template pair checked into a `testdata/`-style
    fixture (not yet ConfigMap-sourced).
  - *Test/acceptance*: an integration test (or documented manual test
    against a real/dev Gitea instance) exercises one full request→render→
    call→render→respond round trip end-to-end.
  - *Depends on*: Phase 1.

- **Phase 3 — Two-tier (`user`/`admin`) authz.**
  - *Goal*: add the authn (static API key, hardcoded for this phase) and
    authz (tier check) pipeline stages in front of Phase 2's tool call.
  - *Deliverables*: `internal/pipeline` with named, ordered stages
    (`authn → authz → tool-resolve → render → call → render → respond`);
    a `user`-tier key and an `admin`-tier key, both hardcoded for now.
  - *Test/acceptance*: unit tests prove a `user`-tier key is rejected on
    an `admin`-tier-tagged tool, and accepted on a `user`-tier tool; an
    invalid/missing key is rejected before any upstream call is made.
  - *Depends on*: Phase 2.

- **Phase 4 — ConfigMap-based manifest discovery.**
  - *Goal*: replace Phase 2's hardcoded fixture with a real Kubernetes
    informer that lists/watches ConfigMaps labeled
    `mcp2rest.homelab.dev/app=<name>` and builds the in-memory routing
    table from them.
  - *Deliverables*: `internal/discovery` (informer + routing table);
    RBAC manifests for cluster-wide ConfigMap list/watch.
  - *Test/acceptance*: an envtest/kind-based integration test creates a
    ConfigMap with a manifest+templates, confirms the tool becomes
    callable within one reconcile cycle, then deletes it and confirms the
    tool disappears.
  - *Depends on*: Phase 3.

- **Phase 5 — Key issuance + per-instance Secret delivery.**
  - *Goal*: replace Phase 3's hardcoded keys with real per-agent-instance
    key minting, storage, and delivery as a Secret
    (`<agent-instance>-mcp2rest-keys`) written into that instance's own
    namespace.
  - *Deliverables*: `internal/keys` (mint/store/revoke), Secret-write
    RBAC (Open Decision 6: namespace-label-scoped ClusterRole).
  - *Test/acceptance*: integration test proves a minted key's Secret lands
    in the correct namespace with the correct name, and that key
    authenticates successfully against Phase 3's authz stage.
  - *Depends on*: Phase 4.

- **Phase 6 — Management tool set + human-admin bootstrap.**
  - *Goal*: implement `register_app`, `deregister_app`, `list_apps`,
    `create_key`, `list_keys`, `revoke_key`, `rotate_key`, `get_manifest`
    as real MCP tools, gated by the one bootstrap admin-scope key minted
    at mcp2rest's first startup (Open Decision 5).
  - *Deliverables*: `internal/adminapi` (tool handlers), bootstrap-key
    mint-and-print-once logic on first startup.
  - *Test/acceptance*: integration test registers a real app via
    `register_app` and confirms Phase 4's discovery path picks it up
    without a pod restart; confirms a non-admin key cannot call any
    management tool.
  - *Depends on*: Phase 5.

- **Phase 7 — Built-in `skills` tool namespace.**
  - *Goal*: `list_skills`/`get_skill` (any authenticated key) and
    `create_skill`/`update_skill`/`delete_skill` (admin scope only),
    wrapping the existing `Skill` CRD.
  - *Deliverables*: `internal/skillstools`; RBAC for `Skill` CRD
    read (cluster-wide) and write (admin path).
  - *Test/acceptance*: integration test creates a `Skill` CRD directly via
    kubectl/client-go, confirms `list_skills`/`get_skill` see it; confirms
    `create_skill` via a `user`-tier key is rejected, via the admin key
    succeeds and is visible back via `get_skill`.
  - *Depends on*: Phase 6.

- **Phase 8 — Multi-user impersonation (`caller` context).**
  - *Goal*: add the `caller` object (`{agentInstance, username, tier,
    appInstance}`) to the Liquid render context so a manifest's
    `request.liquid` can scope an upstream call to one end user.
  - *Deliverables*: `internal/render` context extension; one pilot
    manifest exercising it (recommend Penpot once available, or a mock
    multi-user upstream if Penpot isn't ready yet).
  - *Test/acceptance*: integration test proves two different `caller`
    identities calling the same tool produce two different rendered
    upstream requests (e.g. different `X-Impersonate-User` header values).
  - *Depends on*: Phase 5 (needs per-instance keys to know "which caller").

- **Phase 9 — Passthrough mode.**
  - *Goal*: a manifest declaring `type: passthrough` +`upstreamMcpUrl`
    relays JSON-RPC directly to an upstream MCP server, skipping render
    stages, with tier-tagging of individual upstream tool names.
  - *Deliverables*: `internal/passthrough` (relay client + tool-name
    filter).
  - *Test/acceptance*: integration test against a real or stub upstream
    MCP server proves a `user`-tier key sees only non-admin-tagged tools
    in `tools/list`, and an `admin`-tagged tool call from a `user`-tier
    key is rejected before the relay happens.
  - *Depends on*: Phase 6 (reuses the same auth/routing pipeline).

- **Phase 10 — `hlctl` integration.**
  - *Tracked upstream*: [homelab#215](https://github.com/karlrissland/homelab/issues/215)
    — per cross-repo policy, this repo does not implement `homelab`-side
    work directly; it was requested via a GitHub issue for that team.
  - *Goal*: `homelab`'s `hlctl` gains the `mcpTools:` app.yaml schema
    block, `internal/mcpproxy` (render/validate at `app validate` time),
    and the minimal MCP/JSON-RPC client that calls `register_app`/
    `deregister_app` at deploy time using its own narrow bootstrap key.
  - *Deliverables*: schema change, `internal/mcpproxy`, deploy-step wiring
    (mirrors `EnsureMCPServer`'s call shape).
  - *Test/acceptance*: `hlctl app validate` fails fast on a malformed
    `mcpTools` manifest (bad Liquid syntax, schema violation); a real
    `hlctl app setup` against a dev cluster results in the app visible via
    mcp2rest's `list_apps`.
  - *Depends on*: Phase 6.

- **Phase 11 — mcp2rest platform-service provisioning in `hlctl`.**
  - *Tracked upstream*: [homelab#216](https://github.com/karlrissland/homelab/issues/216).
  - *Goal*: `hlctl` provisions mcp2rest itself — pulling its public GHCR
    image and deploying it by default as part of the platform bootstrap
    (Open Decision 3), the same way it provisions Traefik/Prometheus.
  - *Deliverables*: a new `internal/mcp2rest` (or similarly named)
    provisioning package in `homelab`, mirroring `EnsureMCPServer`'s
    Deployment/Service/Ingress/RBAC shape.
  - *Test/acceptance*: a fresh cluster bootstrap results in mcp2rest
    running and reachable at `mcp2rest.<dns.zone>` with no explicit
    per-app install step.
  - *Depends on*: Phase 6 (needs a real image/binary to provision).

- **Phase 12 — Catalog pilot: Gitea end-to-end.**
  - *Tracked upstream*: [homelab-catalog#103](https://github.com/karlrissland/homelab-catalog/issues/103).
  - *Goal*: validate the whole pipeline on one real `homelab-catalog` app.
  - *Deliverables*: `mcpTools:` manifest + `maps/mcp/**` templates for
    Gitea, reusing the research already captured in its `gitea-api` Skill.
  - *Test/acceptance*: a real agent (Hermes or the Cluster Agent) calls a
    real Gitea-backed MCP tool end-to-end through the deployed cluster.
  - *Depends on*: Phase 10, Phase 11.

- **Phase 13 — mcp2rest's own usage Skill.**
  - *Goal*: ship the "how to use mcp2rest" Skill (§3.7) bundled with
    mcp2rest's own deployment, applied as a `Skill` CRD at deploy time.
  - *Deliverables*: `SKILL.md` content in `homelab-api2mcp`, applied by
    Phase 11's provisioning step.
  - *Test/acceptance*: after a platform bootstrap, `list_skills`/
    `get_skill` (Phase 7) returns mcp2rest's own usage Skill without any
    extra step.
  - *Depends on*: Phase 7, Phase 11.

- **Phase 14 — Observability.**
  - *Goal*: structured logs + `/metrics` (Prometheus), Grafana
    dashboard/alerts for the shared proxy.
  - *Deliverables*: metrics middleware stage in the pipeline; Grafana
    dashboard JSON + alert rules (mirrors other apps'
    `monitoring.alerts` convention).
  - *Test/acceptance*: Prometheus scrapes mcp2rest successfully; a
    dashboard panel shows non-zero request counts after Phase 12's pilot
    traffic.
  - *Depends on*: Phase 6.

- **Phase 15 — Deferred / explicitly out of scope for v1 (stub only).**
  - Real governance/security pipeline stages (PII redaction, rate
    limiting, policy engine).
  - The WASM plugin loader itself (§3.6 only reserves the pipeline
    stage's shape as WASM-loadable from Phase 3 onward; it does not build
    the `wazero` loader).
  - Automatic key rotation (Open Decision 4 — manual only in v1).
  - Per-(app, user) credential storage for multi-user apps that can't use
    impersonation (§3.3 Phase-2 candidate).
  - Full per-tool ACLs beyond the two-tier user/admin split (§3.2
    Phase-2 candidate).

## 8. Explicit non-goals for this plan

- No implementation in this pass — this is the feasibility/architecture
  plan only.
- No decision on automatic key rotation (Open Decision 4).
- No full per-tool ACLs beyond the two-tier user/admin split (§3.2 —
  Phase 2+ candidate).
- No per-(app,user) credential storage for multi-user apps (§3.3 — Phase 2
  candidate); apps requiring true per-user tokens are out of scope until
  then.
- No real governance/policy logic or WASM plugin loader (Phase 5).
- No retiring of the Cluster Agent's existing direct `Skill` CRD RBAC
  (§3.4's "replaces" direction is recorded intent, not executed here).

## 9. Supporting artifacts (added 2026-09-30)

Alongside this plan, three supporting artifacts were drafted for review:

- **`tasks.md`** (session workspace `files/tasks.md`) — a per-phase task
  checklist mirroring §7, with a status column. This is the working
  tracker once implementation starts; update it instead of re-deriving
  status from this plan.
- **`copilot-instructions-draft.md`** (session workspace
  `files/copilot-instructions-draft.md`) — draft `.github/copilot-
  instructions.md` for the new repo: priorities (correctness >
  readability > code reuse > low overhead), the repo's package layout
  (`cmd/`, `internal/{mcpserver,pipeline,render,discovery,keys,adminapi,
  skillstools,passthrough,k8s}`, `manifests/`, `skills/`, `docs/`), and
  pointers to the two skills below. Not yet placed in the actual repo —
  pending your review and plan approval (repo files aren't mutated while
  still in plan mode).
- **Two new skills** (session workspace `files/skills/`), synthesized
  from public references since no existing skill in `homelab`/
  `homelab-catalog` covers generic Go/Docker practice (only
  repo-specific ones like `go-cli-architecture` exist):
  - `go-best-practices/SKILL.md` — Go idioms/readability/code-reuse
    conventions, adapted from Effective Go, the Google Go Style Guide,
    and the Uber Go Style Guide.
  - `docker-container-best-practices/SKILL.md` — multi-stage builds,
    distroless/minimal images, non-root, no-baked-secrets, adapted from
    Docker's own published best practices.

All four files are drafts awaiting your review/adjustment before being
placed into the actual `homelab-api2mcp` repo.
