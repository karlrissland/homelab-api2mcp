# mcp2rest

A shared, low-overhead Kubernetes proxy that exposes any
[`homelab-catalog`](https://github.com/karlrissland/homelab-catalog) app's
REST API to in-cluster AI agents as standardized
[MCP](https://modelcontextprotocol.io) (Model Context Protocol) tools.

App owners can proxy an existing native MCP endpoint, or supply Liquid
request/response templates plus a manifest to map MCP tools to REST.
Upstream authentication is owned by mcp2rest; agents receive gateway keys,
not the upstream credentials.

## Status

Early implementation, following the phased plan in
[`docs/decisions/mcp2rest-plan.md`](docs/decisions/mcp2rest-plan.md).
Track progress in [`tasks.md`](tasks.md).

## Bootstrap provisioning credentials

Before accepting HTTP requests, mcp2rest creates or loads an `Opaque`
Secret named `mcp2rest-bootstrap-admin` in `MCP2REST_NAMESPACE` (default
`mcp2rest`). Its `key` data entry contains the gateway bearer credential
for identity `cluster-admin`, tier `admin`. It is a reusable **full-admin**
credential, not a single-use token or registration-only key.

The Secret is the source of truth: an absent Secret is created atomically;
an existing valid key is restored into the in-memory verification store.
Concurrent startups use the creation winner's key, never their unused
candidates. Invalid/missing/empty Secret data, an unexpected Secret type,
or Kubernetes access errors stop startup rather than replacing the key
or starting without authentication. Raw bootstrap keys are never printed.
Initialization is required even with `MCP2REST_DISABLE_AUTH=true`.

### Gateway RBAC and hlctl handoff

Apply [`manifests/rbac/bootstrap-role.yaml`](manifests/rbac/bootstrap-role.yaml)
before deploying this version. It supplies a namespace-scoped Role and
RoleBinding for the default `mcp2rest` ServiceAccount: `get` on the named
bootstrap Secret and `create` on Secrets in the gateway namespace.
Kubernetes cannot constrain `create` by resource name. No bootstrap
list/watch/update/delete or cluster-wide Secret permission is needed.
Render the namespace and ServiceAccount fields for non-default deployments.

The provisioning client should:

1. Deploy the gateway with that RBAC and wait for it to be Ready. The
   Secret alone does not establish readiness if another startup step fails.
2. Read `mcp2rest-bootstrap-admin` using the provisioner's Kubernetes
   credentials. These need `get` on that exact Secret in the gateway
   namespace; they do not need Secret write access for this handoff.
3. Use `Secret.Data["key"]` as the raw bearer value in
   `Authorization: Bearer <key>` on every management MCP HTTP request,
   including initialization, to `/mcp`. A Kubernetes client's Secret
   data is already decoded; do not base64-decode it again. The default
   in-cluster endpoint is
   `http://mcp2rest.mcp2rest.svc.cluster.local:8080/mcp`.
4. Confirm an authenticated `list_apps` succeeds, then make provisioning
   calls such as `register_app` and `reload_cache`, checking their results.
   Do not scrape pod logs, generate a separate gateway key, or pass this
   full-admin credential to ordinary agent instances. Request their own
   keys through the management API and deliver them as described below.

Retain this Secret across pod restarts and upgrades; it has no pod or
Deployment owner reference. Protect it as a cluster-admin credential.
For controlled bootstrap regeneration, stop **all** gateway replicas,
delete this dedicated Secret using operator credentials, restart the
gateway, and have provisioners reread the new key after readiness.
Existing processes do not watch this Secret; `reload_cache` only reloads
upstream credentials. Per-agent `get_key`/`rotate_key`/`revoke_key` tools
do not manage the bootstrap Secret. Automatic rotation remains deferred.
Agent keys have their own persistent records as described below; neither
bootstrap nor agent-key persistence provides live multi-replica cache
coordination.

When upgrading from the old mint-and-print startup, hlctl must switch to
reading this Secret instead of reusing a log-delivered key. Install RBAC
first. Provisioning must authenticate successfully before an operator
removes any temporary `MCP2REST_DISABLE_AUTH=true` bypass; this change
does not alter a live deployment or that flag.

## Persistent agent keys and hlctl handoff

The gateway generates, stores, and validates agent keys. hlctl authenticates
to the management `/mcp` endpoint using the bootstrap admin key, requests
an agent key, and delivers **only that agent's key** to its harness.
There is no new token endpoint: the existing management tools are used.

| Tool | Arguments | Behavior |
|---|---|---|
| `create_key` | `agentInstance`, `namespace`, `tier` | Creates an additional key and returns it in `key`. |
| `get_key` | Same | Retrieves an existing key for that identity and tier, including after gateway restart. Reports an error if none exists. |
| `rotate_key` | Same | Persists a replacement, invalidates all prior keys of that tier, and returns `key` and `revoked`. |
| `revoke_key` | `namespace` plus either `rawKey` or `agentInstance`/`tier` | Persists revocation before changing the verification index. |

`namespace` identifies the **agent's owning namespace**, not the storage
location. It and `agentInstance` form the stable identity; identically
named agents in different namespaces have distinct keys. `tier` is `user`
or `admin`. The input fields and existing `key` response remain unchanged.
`list_keys` and `register_app.createdKeys` also report the agent namespace.
Repeated `create_key` calls create additional credentials; use `get_key`
for retrieval/reuse, not repeated minting. `register_app.authorizedAgents`
ensures/reuses the requested identity/tier key without rotating it.

### Authoritative storage and startup

All authoritative agent Secrets live in `MCP2REST_NAMESPACE` (default
`mcp2rest`), never in the agent's namespace. Each has:

- Type `Opaque`, label `mcp2rest.homelab.dev/agent-keys=true`, and no
  pod/Deployment owner reference.
- A deterministic name, `mcp2rest-agent-` plus the first 16 SHA-256 digest
  bytes of `namespace + NUL + agentInstance`, represented as lowercase hex.
- A `keys.json` entry containing a version-1 object with `agentInstance`,
  `namespace`, and `keys`: an array of `{key, tier, createdAt}` records.
  Multiple keys/tiers can coexist for an agent. An empty array records
  that all its keys have been revoked; the Secret is retained.

Before accepting HTTP requests, the gateway validates these Secrets and
atomically restores the verification index, including identity, tier, and
original creation time. The bootstrap key is preserved separately. The
same raw agent key authenticates after restart; re-registering an app or
retrieving the key does not replace it.

Invalid JSON/version/identity/key/tier/timestamp, duplicate credentials,
unexpected Secret ownership/type, or failed startup Secret reads stop
startup without partially loading credentials or printing key material.
Key creation, rotation, and revocation commit to Kubernetes before the
live verification index changes. Failed writes leave the previous
verification state intact and return an error. Kubernetes update
conflicts are surfaced for the caller to retry rather than overwriting
concurrent changes.

Apply [`manifests/rbac/agent-keys-role.yaml`](manifests/rbac/agent-keys-role.yaml)
**before upgrading the gateway**. Render its namespace and ServiceAccount
fields to match the deployment. The gateway needs `get/list/create/update`
on Secrets in its own namespace; no agent-namespace Secret permissions or
delete/watch verbs are needed. Kubernetes RBAC cannot restrict these
dynamic names or startup listing by label. The old cross-namespace
`keys-secrets-clusterrole.yaml` delivery bindings are no longer required
by the new gateway; assess/remove them during the downstream RBAC update.

hlctl does not need to read these agent Secrets directly: it uses the
admin-authenticated management tools, then installs the returned key as
the harness's bearer credential or writes a delivery Secret in the
agent's namespace. Kubernetes pods cannot directly mount another
namespace's Secret. Delivery copies are not authoritative and do not
grant the gateway access to those namespaces.

### Upgrade and operational limits

Legacy `<agent-instance>-mcp2rest-keys` Secrets containing only a raw
`keys.json` array cannot be safely restored because they lack tier and
creation metadata. They are **not automatically imported or assigned a
default tier**. On the first upgrade, hlctl must issue/ensure keys through
the new gateway and re-deliver them to existing agents; old raw-only
Secrets are not the source of truth. Keys issued through the new storage
remain stable on subsequent restarts.

Use the management API for rotation/revocation. There is no agent Secret
watcher or automatic rotation, and multiple active gateway replicas can
have stale verification caches until separately coordinated. This
change supports restart-safe **single-active-gateway** operation, not
general multi-replica consistency. Publication and cluster rollout are
separate steps; no live credentials or deployments are changed by local
implementation.

## Native MCP endpoint passthrough

A manifest with `upstreamMCPURL` and no declared `tools` proxies the entire
native Streamable HTTP endpoint. For example, submit this payload to the
existing admin-authenticated `register_app` management tool:

```json
{
  "namespace": "homeassistant",
  "manifest": {
    "name": "homeassistant-native",
    "upstreamMCPURL": "http://homeassistant.homeassistant.svc.cluster.local:8123/api/mcp",
    "upstreamCredentialEnv": "HA_TOKEN"
  },
  "authorizedAgents": [
    {"agentInstance": "cluster-agent", "namespace": "agents"}
  ]
}
```

The endpoint is `/{manifest.name}/mcp`. `upstreamMCPURL` must be an
absolute HTTP(S) URL without userinfo or a fragment. Do not put credentials
in URLs. There are no per-tool declarations, schema copies, discovery
caches, or Liquid templates for this path: native `initialize`,
`tools/list`, `tools/call`, errors, content blocks, structured output,
metadata, notifications, and session/streaming headers pass through.
Relist tools or reconnect according to the upstream's MCP behavior when
its available tools change.

Gateway authentication is checked on every forwarded HTTP request.
mcp2rest removes the caller's Authorization and Cookie headers and, when
configured, injects its server-resolved upstream bearer token. Upstream
HTTP statuses and MCP errors remain authoritative. The proxy does not
follow redirects or replay ambiguous POST requests. Origin headers are
preserved; browser clients must satisfy the upstream's origin policy.

Native sessions expose exactly the upstream surface, without gateway
Skill tools or per-tool gateway tier filtering. Existing gateway keys
still authenticate clients; HA permissions/exposure apply to the
configured upstream identity. A shared administrator token does not
preserve individual users' HA permissions. Use the management `/mcp`
endpoint for gateway Skill tools. Native traffic bypasses the local
tool pipeline and its per-tool metrics; upstream transport failures are
logged without credential values.

### Credential provisioning and rotation

1. Provision the upstream bearer credential out of band in
   `<app-instance>-mcp-keys` in mcp2rest's namespace (`MCP2REST_NAMESPACE`,
   default `mcp2rest`). The example uses Secret
   `homeassistant-native-mcp-keys`, data key `HA_TOKEN`.
2. Register the manifest through the existing authenticated management
   interface, then call the admin-only `reload_cache` tool. Reload reads
   persisted registrations, so a newly registered app is included even
   before its ConfigMap informer event arrives.
3. Routing ConfigMaps are applied asynchronously. Wait for `get_manifest`
   to return the registered app before connecting, or retry a transient
   HTTP 404. Connect the agent using its gateway key and the returned
   `mcpInternalUrl`.
   Startup waits for initial registration discovery before loading
   credentials. A native endpoint with a configured but unavailable
   credential returns HTTP 503 rather than sending an anonymous request.
4. To rotate, update the Secret, call `reload_cache`, check its result,
   and reconnect if the upstream requires reauthentication. Subsequent
   HTTP requests use the reloaded credential; already-open streams are
   not restarted. Failed reloads retain the last successful cache and
   report an error. Initial loading fails if a required native credential
   is missing.

Only pre-provisioned bearer tokens and anonymous upstreams are supported
here. Omit `upstreamCredentialEnv` for an anonymous upstream; the gateway
key is still required and is never passed upstream. Long-lived HA tokens
are replaced manually through the Secret/reload flow. OAuth login,
automatic refresh, and token creation are not implemented. Raw upstream
credentials must not enter manifests, templates, skills, agent arguments,
logs, or Helm values/history.

This is the gateway wire contract, not a new app.yaml schema. Existing
manifests that declare tools keep their rendered/per-tool relay behavior.
Compatibility of endpoint-only registration with released hlctl and
catalog provisioning still needs downstream verification; this change
does not modify those repositories or disable gateway authentication.

## Liquid REST mappings and Home Assistant scope

Existing tools with `type: rendered` continue to execute Liquid
request/response mappings against `upstreamBaseURL`. REST credentials
remain in `<app-instance>-api-keys`; templates reference `credential`
rather than containing a raw token. HTTP 4xx/5xx responses retain mapped
content but now set MCP `isError`, instead of appearing successful.
Individually declared `type: passthrough` tools retain their existing
per-tool behavior.

For HA, native passthrough requires its MCP Server integration to be
enabled at `/api/mcp`; the actual upstream tool surface is authoritative.
REST administration can use the existing mapping path. Automation
configuration requires HA to load `automations.yaml`; integration setup
may require hardware interaction or external authentication. No universal
device-creation operation is implied.

Native and REST-mapped surfaces can be registered as separate app instances;
they are not merged into one MCP session. MCP-to-WebSocket mapping,
including HA registry removal and schedule CRUD, is deferred. Do not
claim those operations unless the native upstream itself exposes them.
No live HA configuration, tokens, pairing, or device actions are performed
by this change.

## Repository layout

See [`.github/copilot-instructions.md`](.github/copilot-instructions.md)
for the full package layout and contribution conventions.

## Building and testing

```sh
go build ./...
go vet ./...
go test ./...
```

## Running locally

```sh
go run ./cmd/mcp2rest
```

Listens on `:8080` by default (override with `MCP2REST_ADDR`).

Set `MCP2REST_DISABLE_AUTH=true` to run with authentication/authorization
disabled cluster-wide: every request (per-app, management, and skills
tools) is treated as an authenticated admin-tier caller, regardless of any
key presented. This is a temporary debugging escape hatch only — never
run with this set in production.
