# @workspace/mcp

The official MCP server SDK, adapted to a native Worker Request/Response handler.
`createMcpEndpoint` creates a server per request: it never shares a user's
identity, transport, or tool closures with another request. The SDK supplies
Streamable HTTP and protocol negotiation; there is no separate Node server.

## Structure

- `packages/mcp`: transport and result helpers, with no application imports.
- `packages/auth`: OAuth provider, signing keys, client registration, consent,
  hive grants, token validation and revocation checks.
- `apps/web/mcp/handler.ts`: authenticate, enforce origin and request budget.
- `apps/web/mcp/tools.ts`: the hive's tools and the server instructions.
- `apps/web/operations`: business operations used by MCP and browser routes.
  Put membership, scope, validation and mutation rules here, not in a tool.
- `apps/web/worker/index.ts`: dispatch `/mcp` and discovery before vinext.

## Connect

Point an OAuth-capable MCP client at `https://YOUR_APP/mcp`. Discovery is at
`/.well-known/oauth-protected-resource` and
`/.well-known/oauth-authorization-server/api/auth`. The provider supports
public dynamic client registration, authorization code with PKCE, and refresh
tokens. A person signs in with GitHub, selects one hive and approves the
listed scopes. A client is given `hive:read` by default and must explicitly
request `hive:write`; only a hive owner or admin can approve it.

`/docs/agents` on the site has the command for each client, and `/llms.txt`
says the same to a model. The server's `instructions` are written from
`@workspace/pricing` in `apps/web/mcp/tools.ts`.

CIMD URL client IDs are not enabled. Add them only with a Worker-compatible
SSRF-safe metadata fetch implementation; ordinary `fetch` is insufficient.
Cross-origin browser MCP calls are rejected; native clients do not send an
Origin header. Add an explicit trusted-origin policy and CORS handling if a
product needs browser-hosted external clients.

## Tools and scopes

| Tool               | Required scope | Role                        |
| ------------------ | -------------- | --------------------------- |
| `get_hive`         | `hive:read`    | Member                      |
| `get_current_user` | `hive:read`    | Member                      |
| `list_machines`    | `hive:read`    | Member                      |
| `list_jobs`        | `hive:read`    | Member                      |
| `get_job`          | `hive:read`    | Member                      |
| `get_usage`        | `hive:read`    | Member                      |
| `get_leaderboard`  | `hive:read`    | Member                      |
| `get_pricing`      | `hive:read`    | Member                      |
| `get_workflow`     | `hive:read`    | Member                      |
| `update_settings`  | `hive:write`   | Owner                       |
| `set_budget`       | `hive:write`   | Admin or owner              |
| `set_auto_top_up`  | `hive:write`   | Admin or owner              |
| `remove_machine`   | `hive:write`   | Machine owner or hive owner |

Names, titles and descriptions live in `apps/web/mcp/tool-list.ts`, which the
docs page and `llms.txt` render too. Every tool delegates to
`apps/web/operations/hive.ts`; nothing in a tool touches the database.

`get_hive` is the one call that answers most questions: balance, whether a
job can start and what blocks it, machines, labels with prices, budget,
settings and `nextSteps`. `profile` and `email` govern whether
`get_current_user` returns those fields. `update_settings` takes the
`version` from `get_hive` and refuses a stale one with `CONFLICT`.
`remove_machine` takes a machine out of the granted hive only; it never
retires the machine. No tool takes a payment or saves a card. Because consent
refuses `hive:write` to anyone below admin, a plain member cannot remove
their own machine through an agent; they can in the browser or with
`hived unlink`. Tool annotations describe read/write and retry behavior, and
a failed tool returns `isError` with the app's error code and a request id.

JWT access tokens expire after five minutes. Every request also checks the
stored grant, consent, verified account and current hive membership.
Changing a browser's active hive cannot change a grant. `/connections`
revokes the grant, consent and refresh tokens; already-issued JWTs stop working
immediately. The Durable Object limits authenticated MCP requests per user.

## Validation

`pnpm test:worker` builds, migrates a fresh local D1, starts Wrangler, exercises
OAuth and real MCP requests plus account lifecycle flows, then stops the Worker
and removes its state. Mail uses Cloudflare's local simulation. No external
client or real email account is required. Failures retain local diagnostics.
