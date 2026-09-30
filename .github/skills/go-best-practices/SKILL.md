---
name: "go-best-practices"
description: "Go language conventions for readability, code reuse, and idiomatic style, adapted from Effective Go, the Google Go Style Guide, and the Uber Go Style Guide"
domain: "go, code-style, code-reuse"
confidence: "high"
source: "earned — synthesized from Effective Go (go.dev), Google Go Style Guide, and the Uber Go Style Guide"
---

## Context

`homelab-api2mcp` is a new Go service. This skill exists so agents write
idiomatic, reusable, readable Go from the first commit instead of
reverse-engineering conventions later. It complements
`.github/copilot-instructions.md`, which points here for language-level
guidance and keeps the instructions file itself focused on this repo's
own architecture and layout.

## Patterns

### Clarity over cleverness
- Optimize for the reader, not the author. Prefer the obvious
  implementation over a clever one.
- Comments explain **why**, not what — the code already says what.
- Every exported identifier (type, func, const, var) has a doc comment
  starting with its own name (`// Client connects to the upstream API.`).
- Package comments start with `// Package foo ...` and describe purpose,
  not implementation.

### Naming
- Package names: short, lowercase, no underscores/mixedCaps
  (`render`, not `RequestRender` or `request_render`).
- Exported: `CapitalCamelCase`. Unexported: `camelCase`.
- Single-method interfaces get an `-er` suffix (`Renderer`, `Resolver`).
- Avoid stutter: `render.Context`, not `render.RenderContext`.
- Receiver names are short (1-2 letters), and consistent across all
  methods of the same type.

### Errors
- Always check and handle errors explicitly — never `_ = err` unless the
  ignore is deliberate and commented.
- Wrap with context using `%w`, not `%v`, so `errors.Is`/`errors.As` keep
  working up the call stack: `fmt.Errorf("render request template: %w", err)`.
- Return errors early; avoid deep nesting (guard clauses over
  if/else pyramids).
- Define sentinel errors (`var ErrKeyNotFound = errors.New(...)`) for
  conditions callers need to branch on with `errors.Is`.
- Reserve `panic` for truly unrecoverable programmer errors (e.g. invalid
  internal invariants at startup), never for expected failure modes like
  a bad upstream response or a malformed manifest.

### Code reuse
- Define interfaces in the **consuming** package, sized to what that
  caller actually needs — not in the implementing package as a "public
  API" speculatively sized for every future caller.
- Prefer small, composable interfaces over large ones; a one-method
  interface is a good default, not a smell.
- Reuse via composition/embedding, not inheritance-style hierarchies.
- Before writing a new helper, search the module for an existing one —
  duplicate `formatDuration`-style helpers are exactly the kind of drift
  this skill exists to prevent (see the analogous `go-cli-architecture`
  skill in `homelab`'s `hlctl` for what this looks like once the package
  tree exists here).
- Dependency-inject collaborators (HTTP clients, Kubernetes clients,
  clocks) via constructor/struct fields — never reach for package-level
  globals or `init()`-time singletons for anything that needs to be
  faked in a test.

### Structure
- Keep functions short and single-purpose; a function doing two
  unrelated things should be two functions.
- Keep files focused — one clear responsibility per file, named for that
  responsibility (`render.go`, not `helpers.go`/`utils.go` grab-bags).
- Group imports: standard library, blank line, third-party, blank line,
  internal packages.
- Always run `gofmt`/`goimports` — formatting is not up for debate.
- Use `context.Context` as the first parameter of any function that does
  I/O or can block, and propagate it — never store it on a struct.

### Concurrency
- Prefer channels and clear ownership over shared mutable state guarded
  by ad hoc locking.
- Every goroutine you start has an obvious owner responsible for waiting
  for it to finish (`sync.WaitGroup`, errgroup, or an explicit done
  channel) — no "fire and forget" goroutines in request-handling code.
- Never write to shared state from a goroutine without a documented
  synchronization strategy.

### Testing
- Every exported function/type gets test coverage; table-driven tests
  are the default shape for anything with more than two input cases.
- Tests assert behavior through the public API of a package, not its
  internals, wherever possible.
- Use interfaces + fakes (not heavyweight mocking frameworks) for
  external dependencies (Kubernetes client, HTTP upstream, clock).

## Examples

✓ **Correct — guard clause, wrapped error, small interface:**
```go
// Renderer executes a Liquid template against a render Context.
type Renderer interface {
    Render(ctx context.Context, tpl string, data Context) (string, error)
}

func (p *Pipeline) renderRequest(ctx context.Context, tool Tool, call Call) (string, error) {
    if tool.RequestTemplate == "" {
        return "", ErrNoRequestTemplate
    }
    out, err := p.renderer.Render(ctx, tool.RequestTemplate, call.Context())
    if err != nil {
        return "", fmt.Errorf("render request template for tool %q: %w", tool.Name, err)
    }
    return out, nil
}
```

✗ **Incorrect — swallowed error, deep nesting, stutter:**
```go
func (p *RenderPipeline) RenderPipelineRequest(t Tool, c Call) string {
    if t.RequestTemplate != "" {
        out, _ := p.renderer.RenderPipelineRenderer(t.RequestTemplate, c.Context())
        return out
    } else {
        return ""
    }
}
```

## Anti-Patterns

- **Package-level mutable globals** for clients/config — breaks testing
  and hides dependencies.
- **`utils`/`helpers` grab-bag packages** — every helper belongs to the
  domain it serves.
- **Returning `error` as the last of many bare return values without
  wrapping** — loses the call chain when something fails three layers
  down.
- **Interfaces defined next to their implementation, sized for every
  imaginable caller** — leads to fat interfaces nobody fully implements
  in tests.
- **Ignoring `context.Context` cancellation** in anything that calls an
  upstream API — request timeouts must actually propagate.
