# Human-v1/widget prerequisite checkpoint

Checkpoint date: 2026-11-25. Feature branch:
`karlrissland-human-mcp-credentials`; original base:
`4d03bc7fd6e367dec396e067a10beff559f30714`.
This preserves unfinished source for a PC move, not final
[#7](https://github.com/karlrissland/homelab-api2mcp/issues/7) acceptance,
a main integration, PR or production release.

## Implemented provider

`internal/keys/humans.go` owns durable canonical-human eligibility and tier
credentials independently of namespace-qualified agents. Startup restores
human Secrets before serving. `internal/adminapi/humans.go` exposes
`human_key_capabilities`, `put_human`, `get_human`, `create_human_key` and
`get_human_key` on management `/mcp`, with API/persistence version 1 and
real stored-admin authentication even when the debugging bypass is enabled.

The [wire/lifecycle contract](human-credentials.md) is authoritative here.
Identity put does not issue; create is deliberate; get never mints, rotates
or repairs persistent state. Key calls pin the validated positive revision
returned by put. Admin loss removes admin material only; disable removes
both tiers; reactivation requires explicit reissuance; permanent deletion
retains a terminal tombstone. Duplicate/malformed/storage uncertainty fails
closed without returning candidate keys. Human-only verifier withdrawal
preserves agents/bootstrap. Existing app tier semantics remain unchanged.

One active replica and existing in-cluster configuration/RBAC are required.
`MCP2REST_NAMESPACE` defaults to `mcp2rest`, Service name to `mcp2rest`, and
listen address to `:8080`; keep `MCP2REST_DISABLE_AUTH` unset. Gateway namespace
Secrets need `get/list/create/update`. Discovery/registration/Skills retain
their documented permissions. There is no kubeconfig mode or new probe route;
check authenticated capabilities and the intended app, not `/metrics` alone.

## Companion contract and remaining acceptance

[karlrissland/homelab#261](https://github.com/karlrissland/homelab/issues/261)
owns stable managed-human IDs, authoritative applied active/admin state,
freshness/admission, explicit reconciliation/issuance and protected Hub Secret
delivery. The full normative r2 source packet was acknowledged, manifest
SHA256 `b2acb194a3603b6aa514ffeab50530dc6bfaf06d8e591224c724f433ccf4012f`;
its invariants file is
`0da778822374d2f6bfdb842e92dcb6a1644429ce6af6ab1109dea82caa052ff3`.
The receipt formula and provider pairing rules are preserved in
[the contract](human-credentials.md#agreed-producer-source-r2); the producer
owns its full schema and observer/history/admission implementation.

Reconciliation order is gateway revision -> fresh complete admitted source
-> put; every conflict repeats both reads. Standalone lookup cannot enroll,
write source/Lease/identity or issue on errors. Blocked source withholds
projection, not automatic gateway revocation. Upstream claims and user
mapping must use stable IDs, never a username-derived substitute.

[karlrissland/homelab-hub#11](https://github.com/karlrissland/homelab-hub/issues/11)
owns consumption and widget/session lifetime enforcement;
the [homelab-catalog companion](https://github.com/karlrissland/homelab-catalog)
owns the coordinated Home Assistant widget pilot. This provider checkpoint
does not attest companion implementation, issue completion or their test results.

The remaining gate is actual protected new-install producer/provider/Hub
pairing and useful widget execution, including restart, role/deactivation,
denial and stale-session behavior in the disposable integration environment.
No live-cluster acceptance is claimed here. Scope is greenfield; automated
legacy account/key/layout conversion is not required. The existing home.lab
installation can be migrated manually separately.

## Published compatible test image

Use the immutable single-platform `linux/amd64` OCI manifest:

```text
ghcr.io/karlrissland/homelab-api2mcp@sha256:0f1cbff3fbdfee782b770b11a21b1f78ef7765dff43f4d033a4425d12d65d79b
```

Tag: `human-v1-test-ea1abc56fc53-20261125-1531`.
Config digest:
`sha256:28a167f5fa1c418922863be4f3c1119c2ac402fabc5e592b1e22d59889adb0af`.
Runtime binary SHA256:
`a29c52917ce5fdb3250606471b711900aa4840e857219d7ed8d4786869964801`.

The image was built and pushed with explicit user authorization before this
source checkpoint, from the original base plus 16 uncommitted source/document
files fingerprinted as
`ea1abc56fc537056f8c8299aff60c93622872e7af0948b7ad24c590d0d2f3dab`.
The 81-file Docker-context fingerprint is
`161f3681d2e99b396fe9ca64ee7f0be400dff3ac5f62163f480b20aa06e1dd47`.
Both hash sorted Windows-relative backslash paths, each as UTF8(path), NUL,
exact file bytes, NUL. The later checkpoint only refreshes/adds documentation;
it does not rebuild or relabel that image as a clean commit build.

The unchanged Dockerfile uses static stripped Go 1.27.1 and
distroless/nonroot; only `/mcp2rest` is added to the final base. OCI labels
explicitly record `4d03bc7fd6e367dec396e067a10beff559f30714-dirty`, the
base/dirty/context fingerprints and API version. The executable version
remains `dev`; use authenticated capability detection. Registry-resolved
manifest/config/final-layer bytes and the shipped binary were independently
verified; all 13 layer descriptors were anonymously accessible at publication,
so no registry imagePullSecret was needed then. Network/DNS/TLS access and
normal gateway bearer authentication are still required.

This image is not the clean baseline tag, latest, a semver release, a
multi-platform index or a signed provenance attestation. No deployment or
cluster credential operation occurred in this provider session.

## Verification and resume

Source validation commands:

```text
go build ./...
go vet ./...
go test ./...
go test ./internal/keys ./internal/adminapi -run 'Human.*(Concurrent|Serializes)' -count=25
go test ./internal/keys ./internal/adminapi ./internal/mcpserver -run Human -count=1
git diff --cached --check
```

The build/vet/full test gates, 25-repeat concurrency checks and focused Human
tests passed again for this checkpoint. The full suite used Go's valid cached
results; focused/repeated cases executed explicitly. Focused Human tests also
passed immediately before the image build. Persistence/API tests use
Kubernetes's fake client. Loopback HTTP acceptance proves a restored
human key executes a rendered read returning `{"temperature":21}`, enforces
user/admin tiers and denies an already-connected user after disable. These
are disposable checks, not real IdP/source/Hub integration acceptance.
The earlier race-detector attempt was unavailable because cgo/gcc was absent;
repeated concurrency checks are not a race-detector substitute.

All essential provider source/tests and this handoff belong to this branch;
no session-state file, binary, local lab data, raw config/key or build cache
is needed to build it. Companion source/contracts must be resumed from their
own repositories' feature-branch checkpoints. Do not infer joint completion
or change main/release/deployment state just because this checkpoint exists.
