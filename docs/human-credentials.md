# Canonical-human gateway credential contract v1

This describes the source implementation for
[#7](https://github.com/karlrissland/homelab-api2mcp/issues/7), not a production
release or live acceptance. An unreleased compatible test image is recorded
in the [checkpoint handoff](human-v1-checkpoint.md). Human credentials are
distinct from agent coordinates, bootstrap credentials, and upstream app tokens.

**The selected model uses stable managed-human IDs**, not immutable or
non-reusable usernames. The provider has acknowledged the normative r2
applied-human source contract owned by
[karlrissland/homelab#261](https://github.com/karlrissland/homelab/issues/261);
that agreement does not prove its implementation or actual client pairing.
Treat deployment as greenfield: automated legacy account/key/layout conversion
is not a completion gate, and the existing home.lab installation can be
migrated manually separately. Never infer identity from mutable/reactivatable
usernames. #7 is not fully accepted until the protected new-install
applied-source/client pairing is established.

## Authority and initial provisioning

hlctl owns the canonical `homelab_user_id` and authoritative user/role mapping.
The provider must not infer account bindings or existing state ownership from
username reuse; any later manual migration is separate from new-install
issuance.
The gateway accepts `source:"hlctl"` as an **authenticated administrative
assertion**, not independent proof of OIDC membership. hlctl must resolve the
canonical ID, active/disabled/deleted status, and current admin eligibility
from its trusted model, rejecting ambiguous/duplicate identities. A requested
tier, mutable username/email, or the caller-username tool argument is not
identity or eligibility authority.

Explicitly provision a human identity with `put_human`, then deliberately
issue each required tier with `create_human_key`. No historical human keys
exist to discover. `get_human_key` never issues, rotates, repairs, or writes
persistent state, including on missing, denied, malformed, or transient
errors. There is no ensure-on-lookup operation.

The existing gateway `admin` tier remains full administrative authority.
All human operations require a **real stored admin gateway key**.
`MCP2REST_DISABLE_AUTH`'s synthetic admin does not authorize them. An eligible
human admin has the same management authority as an agent admin; there is no
new per-app grant or control-plane scope.

## MCP wire contract

Use existing MCP Streamable HTTP on `/mcp`, with the administrator's Bearer
credential on every request. Successful results have `isError:false`.
Every human-tool result's `structuredContent` is an object with `apiVersion:1`
and exactly one payload: `capabilities`, `human`, `credential`, or `error`.
The SDK also supplies a JSON text content representation.

| Tool | Arguments | Success payload |
| --- | --- | --- |
| `human_key_capabilities` | `{}` | `capabilities` |
| `put_human` | `{apiVersion:1, homelabUserId, source:"hlctl", sourceRevision, state, admin:boolean, expectedRevision:integer}` | `human` |
| `get_human` | `{apiVersion:1, homelabUserId}` | `human` |
| `create_human_key` | `{apiVersion:1, homelabUserId, tier, expectedRevision:integer}` | `credential` |
| `get_human_key` | Same arguments as creation | `credential` |

`capabilities` is
`{apiVersion:1,persistenceVersion:1,tiers:["user","admin"],lookupReadOnly:true}`.
Detect support before using the new tools; older providers lack them.
An MCP server/build version is not a substitute for successful authenticated
capability detection and compatible output schemas.

`human` is
`{homelabUserId,source:"hlctl",sourceRevision,state,admin,revision:integer}`.
`credential` is
`{homelabUserId,tier,revision:integer,createdAt:"RFC3339 timestamp",key}`.
`key` retains the existing canonical 43-character base64url 32-byte format.
Never put response bodies/key values in logs, issues, Helm values, metadata,
templates, screenshots, browser responses, or source control.

`homelabUserId` is preserved byte-for-byte as an opaque nonblank UTF-8 value
(source storage bound: 4096 bytes). The provider imposes no username/ASCII
syntax or normalization, and syntax cannot establish immutable identity.
`sourceRevision` is opaque nonblank UTF-8 (source storage bound: 1024 bytes).
It identifies the owner-supplied applied-state snapshot but is **unordered**
at the provider; the agreed producer receipt below does not add provider-side
source verification or ordering.
`state` is exactly `active`, `disabled`, or `deleted`; non-active records must
have `admin:false`. Both booleans and revisions must be explicitly supplied.
`tier` is exactly `user` or `admin`.

`expectedRevision:0` creates a missing identity. Existing identity changes
require its last observed positive gateway revision. Changed snapshots
increment that revision; an identical snapshot at the correct revision is
idempotent without a write. Issuing a key does not change the identity
revision. Both credential operations require the exact current revision.
An existing credential causes creation to return `key_exists`, not another
key. Only a deliberate issuance workflow may respond to this specific result
with read-only lookup. Lookup errors never trigger creation.

**Gateway CAS only orders gateway writes**, not IdP snapshots. The client
must first read gateway identity/revision, then freshly read the agreed
authoritative applied-human state, then put with that expected revision.
Every conflict repeats both reads in that order, never replaying a cached
snapshot against a newer gateway revision. Reading authority before obtaining
a newer gateway revision can overwrite newer roles. Shared authority must
also guarantee fresh complete reads and fence stale source writers; local
desired config, an unordered hash, or an unchecked IdP update is insufficient.
No monotonic IdP revision, OIDC watcher, or independent canonical identity
verification is claimed by this provider.

### Agreed producer source r2

The normative producer packet's manifest SHA256 is
`b2acb194a3603b6aa514ffeab50530dc6bfaf06d8e591224c724f433ccf4012f`.
It specifies exact 41-byte IDs matching `^human-v1-[0-9a-f]{32}$`, with no
username/PK derivation or reassignment. This narrower syntax belongs to
hlctl, not the gateway's opaque-ID validator.

The resource is `authentik/hlctl-applied-humans`, exactly `Data["state.json"]`,
schemaVersion 1. Its ready/complete, blocked/diagnostic, inactive-admin,
binding/history and strict raw-parser rules belong to the producer. JSON
Schema alone does not establish those contextual guarantees. The exact
derived receipt, outside that JSON body, is:

```text
"applied-human-v1:" + lowercase_hex(SHA256(
  UTF8(committed-and-reread source ConfigMap metadata.uid) || byte(0x00) ||
  UTF8(committed-and-reread source ConfigMap metadata.resourceVersion)))
```

It is exactly 81 ASCII bytes. Do not normalize these metadata values,
substitute namespace/ownership/body coordinates or hash desired roles.
Source pre-observation CAS, complete observation, same UID-bound admission
and proof that old writers/uncertain upstream writes cannot resume remain
companion implementation requirements. A blocked source withholds consumer
projection/offers; it does not automatically revoke gateway keys or become
a new provider state/error.

Validate a successful `put_human.human` response's exact ID/source/
sourceRevision/state/admin and positive `revision`. Use that returned
positive revision for key calls, not the old expected revision or source RV;
credential results do not echo the source/Lease receipt.

### Stable failures

Handler failures have `isError:true` and only:

```json
{"apiVersion":1,"error":{"code":"key_not_found","message":"human credentials: key_not_found"}}
```

| Code | Meaning |
| --- | --- |
| `invalid_arguments` | Invalid explicit identity, provenance, tier, state, or revision |
| `unsupported_version` | Requested API version is not supported |
| `unauthorized` | No real stored admin-tier caller |
| `human_not_found` | No authoritative record for that canonical ID |
| `revision_conflict` | Gateway identity revision or Kubernetes optimistic write conflict |
| `human_disabled` / `human_deleted` | Identity is not active; deletion is terminal |
| `tier_ineligible` | Active non-admin requested admin tier |
| `key_not_found` / `key_exists` | Eligible tier has no key / explicit issuance found an existing key |
| `duplicate_record` | Duplicate human, tier/key record, or conflicting credential owner |
| `invalid_record` | Malformed/version-invalid document or unexpected Secret metadata |
| `storage_unavailable` | Authoritative read/reconciliation failed |
| `persistence_failed` | Mutation could not confirm persisted state; no candidate is returned |

Messages are fixed non-secret diagnostics; raw Kubernetes/JSON parser
diagnostics are not returned. Input-schema/protocol failures can occur before
the typed handler, and are also hard failures. Check `isError`, version,
payload shape, canonical ID, tier, and revision before using a credential.
Timeout/failed persistence may have committed remotely: reread authoritative
state before any explicitly authorized retry, never mint as a recovery default.

## Lifecycle and persistence

An active ordinary human can have only a user key. An active admin can have
separate user and admin keys. Removing admin eligibility durably removes only
the admin credential/verifier and preserves the user key. Disabling removes
both keys. Reactivation or admin-role restoration does not restore withdrawn
keys; deliberate issuance is required. Deletion retains a terminal tombstone,
withdraws both tiers, and cannot reactivate that ID. `deleted` is reserved for
a separately confirmed permanent managed-ID retirement, never ordinary config
removal or username-reuse inference. Ordinary managed config removal remains
disable/reactivatable. A different person needs a distinct stable managed ID;
source retirement confirmation is owner-side work, not an automated legacy
migration requirement.

Records use independently owned, labeled Opaque Secrets in the configured
gateway namespace. Each `keys.json` has persistence version 1, a `human`
snapshot, and at most one key per eligible tier with creation time. Clients
must not infer Secret names, read agent delivery Secrets, or supply storage
coordinates. Existing namespace-wide gateway Secret `get/list/create/update`
permissions suffice; no new namespace access, Secret watch/delete, or
identity-provider permission is introduced.

Startup validates/restores human state before listening. Duplicate
identities (including a second labeled Secret with a different name),
duplicate JSON properties, duplicate tiers/digests, unknown fields, invalid
encoding, impossible eligibility, or agent/bootstrap digest conflicts fail
closed. Managed successful mutations commit before updating the verifier.
Human administrative operations are serialized with caller authorization
and eligibility updates in one process. Persisted keys, creation metadata,
and deleted tombstones survive restart.

Administrative reads may reconcile the **in-memory verifier**, but never
write Secrets. Authoritative storage/parse/persistence uncertainty withdraws
all human verification until a successful administrative reconciliation or
restart; agents/bootstrap are preserved. A transient withdrawal may require
a still-valid non-human admin to initiate recovery. It does not constitute
user deletion. Failed mutation responses return no candidate credential.

The gateway still requires **one active replica**. App authentication uses
memory, not a Kubernetes read per request. hlctl must propagate role/lifecycle
changes explicitly; unreported IdP changes and out-of-band Secret edits are
not instantly observed. Existing agent APIs/tier semantics remain unchanged.
Human keys add no upstream impersonation; `caller.agentInstance` is empty for
humans and `__mcp2rest_caller_username` remains unverified caller input.
Native MCP keeps its existing opaque valid-key boundary, without local
per-tool tier filtering.

## Protected downstream projection

hlctl reconciles current authoritative eligibility, then retrieves the exact
canonical ID/tier at the returned gateway revision. Validate all response
coordinates against the trusted mapping; do not project admin material as a
user key. Supply a user key for user widgets even when the person is admin.
The existing Hub `config.json` schemaVersion 1 can hold these values under
`users[canonicalHomelabUserId].user` and, only if eligible, `.admin`.
Delivery belongs to hlctl in a protected namespace-local Secret, not Hub
management/bootstrap calls or provider writes into Hub's namespace.

Confirmed disable/deletion removes both projected tiers; confirmed admin-role
loss removes only admin. Missing/denied/parse/transient errors are not deletion
signals or permission to issue: withhold usable access and surface a safe
error according to the provisioner's fail-closed projection policy.

Source/disposable tests or the published test image do not establish a released
provider/client pair, real protected Hub delivery, or a live useful widget read.
Those joint checks and any live creation/revocation/lookup require separate
scoped authorization.
