# mcp2rest

A shared, low-overhead Kubernetes proxy that exposes any
[`homelab-catalog`](https://github.com/karlrissland/homelab-catalog) app's
REST API to in-cluster AI agents as standardized
[MCP](https://modelcontextprotocol.io) (Model Context Protocol) tools.

App owners supply Liquid request/response templates plus a small manifest
describing the MCP surface; mcp2rest renders and executes them per
request, so agent integration is uniform regardless of which harness the
agent runs in.

## Status

Early implementation, following the phased plan in
[`docs/decisions/mcp2rest-plan.md`](docs/decisions/mcp2rest-plan.md).
Track progress in [`tasks.md`](tasks.md).

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
