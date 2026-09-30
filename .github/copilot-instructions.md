# Copilot instructions — homelab-api2mcp (mcp2rest)

## What this repo is

`mcp2rest` is a shared, low-overhead Kubernetes proxy that exposes any
`homelab-catalog` app's REST API to in-cluster AI agents as standardized
MCP (Model Context Protocol) tools. App owners supply Liquid templates +
a manifest; mcp2rest renders/executes them per request. See
`docs/decisions/mcp2rest-plan.md` (imported from the design session) for
full architecture and rationale before making structural changes.

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
│   └── mcp2rest/            # main package — flag/env parsing, wiring, startup only
│       └── main.go
├── internal/
│   ├── mcpserver/           # MCP Streamable HTTP transport + JSON-RPC dispatch
│   ├── pipeline/            # ordered, named middleware stages (authn→authz→...→respond)
│   ├── render/              # Liquid render context + execution (request/response templates)
│   ├── discovery/           # ConfigMap informer, routing table
│   ├── keys/                # per-agent-instance key mint/store/revoke, Secret delivery
│   ├── adminapi/            # register_app/list_apps/create_key/... management tools
│   ├── skillstools/         # list_skills/get_skill/create_skill/... built-in tool namespace
│   ├── passthrough/         # upstream-MCP relay mode
│   └── k8s/                 # thin Kubernetes client wrapper shared by the above
├── manifests/                # Kubernetes Deployment/Service/Ingress/RBAC for mcp2rest itself
├── skills/
│   └── mcp2rest-usage/
│       └── SKILL.md         # "how to use mcp2rest" — bundled + applied as a Skill CRD at deploy time
├── docs/
│   ├── decisions/           # architecture/design-decision docs (mirrors the homelab convention)
│   └── mcp2rest.md          # user-facing contract (mirrors homelab's docs/ai-providers.md)
├── testdata/                 # fixtures for unit/integration tests (manifests, templates)
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

- No real governance/security policy logic yet (stages are pass-through
  stubs) — see `plan.md` Phase 15.
- No WASM plugin loader yet — the pipeline stage interface just needs to
  stay WASM-loadable-shaped.
- No automatic key rotation — manual only.
- No per-(app,user) credential storage — impersonation-header-only via
  the `caller` render context in v1.
