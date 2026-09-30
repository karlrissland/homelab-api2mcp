---
name: "docker-container-best-practices"
description: "Containerizing Go services with multi-stage builds, distroless/minimal images, and security hardening"
domain: "docker, containers, ci-cd"
confidence: "high"
source: "earned — synthesized from Docker's official best practices, distroless project docs, and common Go production Dockerfile patterns"
---

## Context

`homelab-api2mcp` (mcp2rest) is explicitly meant to be a **low-overhead**
pod (per the plan's Guiding Principles) and is distributed as a public
GHCR image that `hlctl` pulls to deploy — this skill keeps the image
small, fast-starting, and low-privilege from the first Dockerfile.

## Patterns

### Multi-stage build
- Stage 1 (`builder`): full `golang:<version>-alpine` (or `-bookworm`)
  image; `go mod download` before copying source so dependency layers
  cache independently of source changes.
- Stage 2 (final): minimal runtime base — `distroless/static` for a
  fully static binary, `distroless/base` only if you need libc/CA certs
  and can't statically link them in.
- Copy **only** the compiled binary (and any required static assets,
  e.g. embedded templates if not using `//go:embed`) into the final
  stage — nothing else from the builder stage.

### Minimal, static binary
- Build with `CGO_ENABLED=0` so the binary has no dynamic library
  dependencies — this is what makes `distroless/static` (or `scratch`)
  viable at all.
- Strip debug info and local paths: `-ldflags="-s -w" -trimpath`.
- Pin the Go toolchain version in the builder stage image tag — don't
  float on `golang:latest`.

### Security / least privilege
- Run as a **non-root** user (`USER nonroot:nonroot` on distroless
  images, or an explicit numeric UID/GID on others) — never leave a
  container running as root by default.
- No shell, no package manager, no debugging tools in the final image —
  distroless gives you this by default; if a non-distroless base is ever
  used, explicitly remove `apk`/`apt`/`/bin/sh` or justify why not.
- Never bake secrets/tokens into image layers (build args are **not**
  secret-safe — they persist in image history). Use Kubernetes Secrets
  mounted at runtime instead, matching this org's existing
  `internal/credentials`/`internal/aicreds` pattern.
- Set a read-only root filesystem where the workload allows it, and drop
  all Linux capabilities except ones actually required.

### Image size / build hygiene
- `.dockerignore` excludes `.git`, test fixtures, docs, and anything not
  needed to build.
- Order Dockerfile instructions from least-to-most frequently changing
  (dependency manifests before source) to maximize layer cache hits in
  CI.
- Label images with version/commit/build-time metadata
  (`org.opencontainers.image.*` labels) for traceability in GHCR.

### Health checks and graceful shutdown
- Expose a liveness/readiness endpoint (mirrors this org's convention on
  other apps, e.g. Hub's `/healthz`) so Kubernetes probes work correctly
  out of the box.
- Handle `SIGTERM` to drain in-flight requests before exit — important
  for a proxy sitting in a request path, not just a batch job.

## Examples

✓ **Correct — multi-stage, static, non-root, minimal:**
```dockerfile
# syntax=docker/dockerfile:1

FROM golang:1.23-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/mcp2rest ./cmd/mcp2rest

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/mcp2rest /mcp2rest
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/mcp2rest"]
```

✗ **Incorrect — single stage, root, floating tag, bloated:**
```dockerfile
FROM golang:latest
WORKDIR /app
COPY . .
RUN go build -o mcp2rest ./cmd/mcp2rest
CMD ["./mcp2rest"]
```
Problems: ships the entire Go toolchain + source tree in the runtime
image, runs as root, floats on `latest` (non-reproducible builds), no
`.dockerignore` discipline, no non-root user.

## Anti-Patterns

- **Single-stage builds that ship the compiler** — multiplies image size
  and attack surface for no runtime benefit.
- **`FROM ... :latest`** in either stage — breaks reproducible builds.
- **Baking credentials into `ENV`/`ARG`** — visible in `docker history`
  even after a later layer "removes" them.
- **Running as root "because it's simpler"** — directly contradicts this
  org's least-privilege posture used for RBAC elsewhere in this plan.
- **Skipping health/readiness endpoints** "since it's just a proxy" —
  Kubernetes needs them to manage rollouts safely.
