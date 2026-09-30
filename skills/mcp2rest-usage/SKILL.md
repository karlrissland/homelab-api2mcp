---
name: "mcp2rest-usage"
description: "How agents discover and use mcp2rest tools, scopes, tiers, built-in skills, and error signals"
domain: "platform/mcp2rest"
confidence: "high"
source: "this repo (docs/decisions/mcp2rest-plan.md)"
---

## Context

mcp2rest exists because agent-over-HTTP support varies across harnesses, but MCP usage is stable. It gives homelab-catalog apps a standard MCP surface by exposing app APIs as MCP tools, either by rendering app-owned Liquid request/response templates around REST calls or by relaying to an app's native MCP server in passthrough mode. From an agent's point of view, this keeps app integration uniform: discover tools through MCP, call them through MCP, and let mcp2rest handle authn, authz, routing, rendering, and upstream execution.

## Patterns

### Single MCP surface

mcp2rest presents one MCP surface with three tool namespaces:

- **Per-app proxied tools:** the tools for a specific registered app instance
- **Built-in skills tools:** `list_skills`, `get_skill`, `create_skill`, `update_skill`, `delete_skill`
- **Reserved management tools:** `register_app`, `deregister_app`, `list_apps`, `create_key`, `list_keys`, `revoke_key`, `rotate_key`, `get_manifest`

Treat all of them as normal MCP tools. Discover them with standard MCP flow such as `initialize` and `tools/list`; do not hardcode assumptions about what is present.

### Discovering what your key can use

A key is not global. In the plan, keys are issued for a specific app and a specific tier:

- **App scope:** your key is for a given app instance, so use the app-instance MCP endpoint/path you were given
- **Tier:** your key is issued as `user` or `admin` for that app; `admin` implies `user`

In practice, discovery should be iterative:

1. Connect to the intended app-instance MCP surface.
2. Call `tools/list` instead of guessing tool names.
3. Use only the tools that surface for that app instance and your current key.

If a tool is missing, assume scope or tier may be the reason before assuming the app is broken.

### Two-tier user/admin behavior

Per-app tools can be marked in the manifest as `tier: user` or `tier: admin`. mcp2rest enforces that split in the authz stage:

- A **user-tier key** can call `user` tools
- An **admin-tier key** can call both `user` and `admin` tools
- A **user-tier key calling an admin-tier tool is rejected before any upstream call happens**

Interpret that rejection as "this key is not allowed to do this." It does **not** mean the upstream app failed, and retrying with the same key is not useful.

Do not confuse the two admin concepts:

- **Per-app `admin` tier:** data-plane access to that app's admin-tagged tools
- **Proxy-wide admin scope:** control-plane access to mcp2rest's reserved management tools and the write-side built-in skills tools

### Built-in skills tools

The built-in skills namespace is always part of mcp2rest:

- `list_skills`, `get_skill` are available to **any authenticated key**
- `create_skill`, `update_skill`, `delete_skill` are **admin-scope only**

Read access is cluster-wide rather than limited to the apps your key can call. This Skill is itself part of that model. Agents should expect to retrieve it through `list_skills` and `get_skill`, not through a separate documentation path.

### Per-instance identity and multi-user apps

Keys are minted per agent instance, not per app in the abstract. For multi-user apps, mcp2rest carries caller context for the upstream request path, including `agentInstance`, `username`, `tier`, and `appInstance`.

For agent behavior, the important implication is that caller identity is part of the intended authorization model. If a multi-user action appears to target the wrong user or lacks access, do not assume the fix is to retry with the same request or to find a bypass path; the right resolution is usually a different key, tier, or agent-instance context.

### Passthrough mode

Some apps expose their own MCP server and are registered as passthrough instead of rendered REST-backed tools. From an agent's point of view, that distinction should not change behavior:

- Discover tools normally
- Call tools normally
- Respect the same key scope and tier model

Do not special-case passthrough tools and do not treat them as a way around mcp2rest policy checks. mcp2rest still performs its normal authn/authz pipeline before relaying the JSON-RPC call.

### Interpreting pipeline failures

Use the pipeline stage that failed to decide whether retrying makes sense:

| Failure area | What it usually means | Retry guidance |
|---|---|---|
| Authn | Missing, invalid, revoked, or wrong key | **Do not retry with the same key** |
| Authz | Right server, wrong app scope or insufficient tier/admin scope | **Do not retry with the same key** |
| Tool resolve | Wrong app instance or wrong tool assumption | Refresh discovery with `tools/list`; do not guess |
| Render request / render response | Proxy-side manifest or template problem | Usually report; retry only if there is evidence of a transient restart or rollout issue |
| Upstream call | The upstream app or network path failed | Retry can make sense if the failure looks transient; otherwise report the upstream failure clearly |

The most important distinction is that **authn/authz failures are permission problems, not transient transport problems**.

## Examples

### Discover and use tools safely

When you need to act on an app through mcp2rest:

1. Connect to the correct app-instance MCP surface.
2. Run `tools/list`.
3. Pick from the returned tool set instead of assuming a tool exists everywhere.
4. If an expected tool is absent or rejected, treat that as a scope/tier signal first.

### Use built-in skills tools

If you need operational documentation:

1. Call `list_skills`.
2. Call `get_skill` for the relevant Skill.
3. Remember that reading Skills is open to any authenticated key, but creating or changing one requires proxy-wide admin scope.

### Decide whether to retry

- If a call fails during **authn** or **authz**, report the permission problem and stop retrying with the same key.
- If a call fails during **upstream call**, a retry may be reasonable if the app appears temporarily unavailable.
- If the failure suggests a **render** problem, report it as a proxy or manifest issue rather than pretending the upstream app rejected the action.

## Anti-Patterns

- Assuming every key can see or call every tool
- Hardcoding tool names instead of calling `tools/list`
- Treating a user-tier rejection on an admin-tier tool as an upstream outage
- Confusing per-app `admin` tier with proxy-wide admin scope
- Special-casing passthrough tools instead of using them like any other MCP tools
- Trying to bypass tier checks by calling passthrough tools directly or through a different namespace
