# Copilot instructions — homelab-api2mcp (mcp2rest)

## What this repo is

`mcp2rest` is a shared, low-overhead Kubernetes proxy that exposes any
`homelab-catalog` app's REST API to in-cluster AI agents as standardized
MCP (Model Context Protocol) tools. App owners supply a manifest with
Liquid-rendered REST tools, individually declared MCP passthrough tools,
or a native MCP endpoint with no locally declared tools. See
`docs/decisions/mcp2rest-plan.md` (imported from the design session) for
architecture and rationale before making structural changes. The plan is
historical design context, not proof that a feature is implemented; check
the runtime source for current behavior and `docs/mcp2rest.md` for its
user-facing contract.

## Priorities, in order

1. **Correctness** — this sits in the request path between agents and
   real app APIs; a bad render or auth bypass has real consequences.
2. **Readability** — code is read far more often than written. Prefer the
   obvious implementation over the clever one. See the `go-best-practices`
   skill.
3. **Code reuse** — before adding a new helper, type, or package, search
   for an existing one that already does it (or almost does it, and can
   be generalized slightly). Duplicated logic across packages is a bug
   waiting to diverge, not a shortcut.
4. **Low overhead** — this is explicitly meant to be a fast, low-resource
   pod (see the plan's Guiding Principles). Don't add heavyweight
   dependencies, reflection-heavy abstractions, or unnecessary allocations
   in the request hot path without a clear reason.

## Repo layout

```
homelab-api2mcp/
├── cmd/
│   └── mcp2rest/            # main package — env parsing, wiring, startup only
│       └── main.go
├── internal/
│   ├── mcpserver/           # management/app routing, MCP transport + native dispatch
│   ├── pipeline/            # ordered, named middleware stages (authn→authz→...→respond)
│   ├── render/              # Liquid render context + execution (request/response templates)
│   ├── discovery/           # ConfigMap informer, routing table
│   ├── keys/                # durable namespace-qualified agent keys + bootstrap
│   ├── manifest/            # shared app/tool models and validation
│   ├── adminapi/            # register_app/list_apps/create_key/... management tools
│   ├── skillstools/         # generic management + app-scoped Skill CRD tools
│   ├── passthrough/         # per-tool MCP relay + opaque native endpoint proxy
│   └── upstreamcreds/       # gateway-namespace upstream Secret credential cache
├── manifests/                # RBAC, Prometheus alert rules, Grafana dashboard
├── skills/
│   └── mcp2rest-usage/
│       └── SKILL.md         # usage source; provisioners must deploy a Skill CR to expose it
├── docs/
│   ├── decisions/           # architecture/design-decision docs (mirrors the homelab convention)
│   └── mcp2rest.md          # user-facing contract (mirrors homelab's docs/ai-providers.md)
├── testdata/                 # reserved; render fixtures live in internal/render/testdata/
├── Dockerfile
├── .dockerignore
├── go.mod / go.sum
└── tasks.md                  # phase-by-phase implementation tracker
```

**Rules:**
- `cmd/mcp2rest/main.go` only wires dependencies and starts the server —
  no business logic lives in `main`.
- Each `internal/*` package has one clear responsibility matching the
  table above. If a change doesn't obviously belong in an existing
  package, that's a signal to stop and reconsider the package boundary
  before adding a new top-level directory.
- `internal/k8s` is the **only** package that imports `client-go`/
  `controller-runtime` directly — every other package depends on it
  through an interface it defines for what it needs (mirrors
  `go-best-practices`' "define interfaces in the consumer" rule).
- Tests live next to the code they test (`foo.go` + `foo_test.go`), not in
  a parallel `tests/` tree, except for `testdata/` fixtures and
  cluster-level integration tests that need a real/fake API server.

**Existing implementation versus required boundaries:** the `internal/k8s`
rule above is a required architectural boundary, not a currently
implemented package (the directory has no Go source). Existing packages
including `discovery`, `keys`, `adminapi`, `skillstools`, `mcpserver`, and
`upstreamcreds`, as well as `cmd/mcp2rest`, import `client-go` directly.
This is existing technical debt, not permission to weaken the rule or add
more direct imports. Preserve these deliberate requirements and flag
conflicts; keep boundary refactors separate from documentation fixes.

## Runtime contract reminders

- `/mcp` is management plus cluster-wide Skill tools, not app API tools.
  All management calls require the same gateway `admin` tier; management
  discovery is not filtered by authorization. Skill reads accept any valid
  tier. Declared app tools live on `/{app}/mcp`, with tier-filtered discovery
  and call-time enforcement, plus app/global Skill reads under app-specific
  names.
- Gateway keys identify agent namespace+instance+tier, not app scope.
  `authorizedAgents` ensures keys; it is not an app allowlist. There is no
  distinct proxy-admin scope. Do not document planned isolation as enforced.
- Native endpoints (no declared tools, `upstreamMCPURL` set) authenticate a
  valid gateway key but bypass the declared-tool pipeline and tier filtering.
  Their protocol bodies and tool surface are upstream-owned; the gateway
  injects neither management nor Skill tools.
- Upstream credentials are separate server-owned values from gateway-
  namespace `<app>-api-keys`/`<app>-mcp-keys` Secrets. Authoritative agent
  keys and the persistent bootstrap Secret also live in the gateway
  namespace; harness/agent-namespace delivery is the provisioner's job.
  `get_key` supports explicit admin-only retrieval of a persisted raw key.
- Read the execution mode before describing schema validation, rendering,
  caller identity, or result/error fidelity. Native forwarding is opaque;
  declared passthrough flattens results, whereas rendered REST uses Liquid.
- Derive runtime guarantees from registrations, authorization gates, and
  dispatch code rather than historical design proposals.

## Required skills

- **`go-best-practices`** (`.github/skills/go-best-practices/SKILL.md`) —
  Go idioms, error handling, code reuse, naming. Read before writing Go
  in this repo.
- **`docker-container-best-practices`**
  (`.github/skills/docker-container-best-practices/SKILL.md`) — required
  reading before touching the `Dockerfile`.

## Testing and validation

- Run `go build ./...`, `go vet ./...`, and `go test ./...` before
  considering any change complete.
- New packages need table-driven unit tests for exported behavior.
- Integration tests that need a Kubernetes API (informers, RBAC) should
  use `envtest` or an equivalent fake, not a hand-rolled mock of
  `client-go`.
- Don't fix unrelated pre-existing issues in the same change — flag them
  instead.

## Non-goals reminders (don't silently build these)

- No governance/decision-engine security policy yet — see
  `docs/decisions/mcp2rest-plan.md` Phase 15. Authentication and declared-tool
  tier authorization are implemented; do not call those enforcement stages
  pass-through stubs.
- No WASM plugin loader yet — the pipeline stage interface just needs to
  stay WASM-loadable-shaped.
- No automatic key rotation — manual only.
- No per-(app,user) credential storage — impersonation-header-only via
  the `caller` render context in v1.
