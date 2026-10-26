# Desktop supervisor API

Endpoints and flags for a desktop app that launches the proxy, shows per-account
usage, and stops it. All routes sit under `/v8/management` and use the normal
management authentication: `Authorization: Bearer <key>` or `X-Management-Key: <key>`.

## Enabling management for a local supervisor

Recommended: start the proxy with `MANAGEMENT_SUPERVISOR_PASSWORD` set to a fresh
random value of at least 32 characters. The proxy reads it once, clears it from
its own environment (plugins and child processes never see it), and accepts it
only from loopback. It skips the failed-attempt IP ban: every local process
shares 127.0.0.1, so another program sending wrong keys would otherwise lock
the supervisor out. Wrong keys are still counted and banned as
usual. No key is written to `config.yaml`, so the proxy never rewrites one.

## Failed management keys

Wrong keys are counted per client IP. 5 failures lock that IP out of management
for 5 minutes (`403`). After a lockout, 3 more failures lock it out for 30
minutes, and every later lockout is 30 minutes. A correct key, or 2 hours
without a request, returns the IP to the first step.

A proxy client key (`access.api-keys`) sent as the management key gets `401`
with `this is a client API key; management calls need the management key` and
does not count: it is a setup mix-up, not a guess.

- `GET /v8/management/server/lockouts`: IPs with failures or an active lockout:
  `{"lockouts":[{"ip":"127.0.0.1","failures":2,"failures_allowed":3,"locked_until":"2026-10-26T19:30:00Z","lockouts":1}]}`.
  `failures_allowed` is the total that earns the next lockout; `locked_until`
  is `null` when not locked out.
- `DELETE /v8/management/server/lockouts`: lifts every lockout and forgets all
  failures. A locked-out IP can't call it; the supervisor password can.

Alternatively, set a key in `config.yaml` (v8 layout):

```yaml
management:
  allow-remote: false   # localhost only
  secret-key: "<choose a key>"   # plaintext is hashed in memory at load
```

or start the proxy with the `MANAGEMENT_PASSWORD` environment variable set,
which enables the routes without writing a key to disk. The legacy
`remote-management:` block is still accepted.

## Process lifecycle

- `-parent-pid <pid>`: the proxy shuts down gracefully when that process exits.
  On Windows it waits on the process handle; elsewhere it polls every
  `ParentPollInterval` (2s).
- `POST /v8/management/server/shutdown`: replies `202 {"status":"shutting down"}`
  and then stops the proxy the same way SIGINT does. Requests whose TCP peer or
  resolved client IP is not loopback get `403`, even with `allow-remote: true`.

## Routing strategy

`routing.strategy: soonest-reset` (aliases `soonestreset`, `sr`) sends each new
conversation to the available account whose weekly window resets first, then
the 5-hour window, then credential ID. Accounts with unknown or past resets
rank last. Cooldowns (for example a hit 5-hour limit) take the account out
until it recovers, after which it ranks again. Pair it with
`routing.session-affinity: true` so a conversation stays on its account and
only new conversations move to the soonest-resetting one.

Read and write through the v8 config API (writes accept an optional
`If-Match: <ETag from the GET>`):

```http
GET /v8/management/config/routing/strategy          -> "soonest-reset"
PUT /v8/management/config/routing/strategy          body: "soonest-reset"
GET /v8/management/config/routing/session-affinity  -> true
PUT /v8/management/config/routing/session-affinity  body: true
```

## Claude usage lookups

Reset times come from response headers during traffic and are not persisted,
so after a restart the proxy looks up each enabled Claude OAuth credential's
usage (`GET https://api.anthropic.com/api/oauth/usage`). It checks every
minute and looks up credentials with no snapshot, a snapshot older than 15
minutes, or an account window whose reset has passed. Lookups run one at a
time, 2s apart; a failing credential is retried after 15 minutes.

## GET /v8/management/usage/windows

```json
{
  "strategy": "soonest-reset",
  "credentials": [
    {
      "id": "claude-work.json",
      "auth_index": "3f2a9c",
      "name": "claude-work.json",
      "provider": "claude",
      "label": "Work account",
      "email": "work@example.com",
      "note": "Work",
      "disabled": false,
      "status": "active",
      "status_message": "",
      "priority": 0,
      "cooldown_until": null,
      "weight": 1,
      "plan": {"tier": "default_claude_max_5x", "type": "claude_max", "label": "Max 5x", "multiplier": 5},
      "reset_credits": null,
      "observed_at": "2026-10-25T12:00:00Z",
      "next_pick": true,
      "windows": [
        {"key": "five_hour", "label": "5-hour", "used_percent": 12, "resets_at": "2026-10-25T18:00:00Z", "status": "allowed", "duration_seconds": 18000, "scoped": false, "reset_cadence": "on_use"},
        {"key": "seven_day", "label": "Weekly", "used_percent": 41, "resets_at": "2026-10-29T09:00:00Z", "status": "allowed_warning", "duration_seconds": 604800, "scoped": false, "reset_cadence": "fixed"},
        {"key": "seven_day_fable", "label": "Weekly · Fable", "used_percent": 100, "resets_at": "2026-10-30T09:00:00Z", "status": "rejected", "duration_seconds": 604800, "scoped": true, "reset_cadence": "fixed"}
      ]
    }
  ]
}
```

- `name` is the value used by `DELETE /v8/management/credentials?name=<name>`
  and by the `name` field of `PATCH /credentials/status` and `/credentials/fields`.
- `label` is the derived account label; `note` is the user-set note. `email`,
  `note` and `priority` are omitted when unset.
- `cooldown_until` is when a benched credential becomes eligible again, else `null`.
- `weight` is the credential's `weighted-round-robin` weight (default 1; 0 excludes it).
- `plan` is the subscription. Claude's comes from the account's OAuth profile,
  looked up every 6 hours and stored on the credential; Codex's is the login
  token's `plan_type` (`{"tier": "plus", "type": "plus", "label": "Plus"}`).
  `null` before the first lookup and for other providers. `multiplier` is 5-hour
  session usage relative to Claude Pro, read from the tier (`..._5x` = 5) or 1 for
  Pro; `null` when unpublished (Team, Enterprise, every Codex plan).
- `reset_credits` are a Codex account's rate-limit reset credits from ChatGPT's
  usage document, looked up every 15 minutes:
  `{"available": 1, "applicable": 0, "checked_at": "2026-10-26T12:00:00Z"}`.
  `available` is how many the account holds; `applicable` is how many can be
  used now, against a limit it has reached. `null` before the first lookup, when
  the account reports none, and for other providers (Claude reports no such credit).
- `observed_at` is the time of the latest usage snapshot, else `null`.
- `next_pick` marks the credential the strategy would choose for a new
  conversation within its provider. Rotating strategies (`round-robin`) mark none.
  Credentials defined in the config file are ranked but not listed, so a provider
  may have no listed `next_pick`.
- `windows` are sorted account-wide first, shorter before longer. `used_percent`,
  `resets_at`, `status` and `duration_seconds` are `null` when unknown.
  `duration_seconds` is the window length. `scoped` is `true` for model-, feature-
  or spend-specific windows; only unscoped windows describe the whole account and
  drive `soonest-reset` routing. `reset_cadence` is `"fixed"` when the next period
  starts at the reset (resets every `duration_seconds`), `"on_use"` when it starts
  with the first request after the reset, and `null` when unknown (Codex, Devin).
  Claude credentials report `on_use` for the 5-hour window and `fixed` for weekly ones. Claude, Codex and Devin
  credentials report windows; other providers return `[]`.

## POST /v8/management/usage/refresh

Body `{"auth_index": "3f2a9c"}` refreshes one Claude credential; an empty body
refreshes every enabled Claude OAuth credential (2s apart, so this can take a
while with many accounts). Returns the same shape as `GET /usage/windows`, plus
`"errors": {"<id>": "<message>"}` when a lookup failed.

- `404` unknown `auth_index`.
- `400` the credential is not an enabled Claude OAuth credential, or the body is invalid.

## GET / PUT /v8/management/routing/preferred-auth

The "use this account" override. `PUT {"provider":"claude","auth_index":"3f2a9c"}`
makes new requests for that provider use the credential while it is available,
ahead of the routing strategy and session affinity. When it is cooling down,
disabled or already tried in the request, routing falls back to automatic.
`auth_index: ""` clears it. Both methods return `{"preferred":{"claude":"3f2a9c"}}`.
The preference lives in memory only; a supervisor re-applies it after a restart.
`GET /usage/windows` marks it with `"preferred": true` and moves `next_pick` to
it while it is usable. Sessions bound during the override keep their binding
until session affinity expires.

- `400` missing provider, or the credential belongs to another provider.
- `404` unknown `auth_index`.

## GET /v8/management/usage/events

Server-sent events. `event: ready` on connect, then one `event: request` per
upstream attempt (successful or failed):

```json
{"at":"…","request_id":"…","selection_id":"…","provider":"claude","model":"…",
 "auth_id":"…","auth_index":"3f2a9c","stream":true,"failed":false,"status_code":0,
 "latency_ms":5400,"ttft_ms":900,"input_tokens":1200,"output_tokens":300,"cached_tokens":0,
 "cache_creation_tokens":0}
```

`cached_tokens` counts cache reads; `cache_creation_tokens` counts cache writes.
For Claude both are separate from `input_tokens`; for OpenAI, `input_tokens`
includes `cached_tokens` and cache writes are not reported.

Failed attempts add `status_code` and a sanitized `error` summary. Attempts of
one credential selection share `selection_id`. A reader too slow for a burst
gets `event: resync` and should reload `GET /usage/windows`. Comment lines
(`: keep-alive`) arrive every 15s.

## Adding a Claude account (OAuth)

1. `GET /v8/management/oauth/auth-url?provider=claude&is_webui=true` returns
   `{"status":"ok","url":"<authorize URL>","state":"<state>"}`.
   With `is_webui=true` the proxy listens on `localhost:54545` for the browser
   redirect. Open `url` in the browser.
2. Without `is_webui`, post the final redirect URL instead:
   `POST /v8/management/oauth/callback` `{"provider":"claude","redirect_url":"<url>"}`.
3. Poll `GET /v8/management/oauth/status?state=<state>`:
   `{"status":"wait"}` while pending, `{"status":"ok"}` when saved,
   `{"status":"error","error":"<message>"}` on failure. The session times out
   after 5 minutes. `DELETE /v8/management/oauth/session?state=<state>` cancels it.

## Pool usage limit (429)

When every credential that serves a model is spent, the request fails with
`429` and error type `usage_limit_reached`, including the request that spends
the last credential. `X-Should-Retry: false` stops client retries. Clients read
limits from headers, never the body, so the response repeats the soonest reset
in each client's own header family:

- Claude Code: `Anthropic-Ratelimit-Unified-Status: rejected`, `-Reset`,
  `-Representative-Claim` (`five_hour` or `seven_day`), and that window's
  `-5h-`/`-7d-` `Status`, `Reset` and `Utilization: 1`. Claude Code reads them
  only when signed in to a subscription, not with `ANTHROPIC_AUTH_TOKEN`.
- Codex: `X-Codex-Primary-Used-Percent: 100`, `X-Codex-Primary-Reset-At`, and
  `X-Codex-Primary-Window-Minutes` when the window is known.

Hosts such as T3 Code schedule a resume from these.

## Codex account calls (`/backend-api/*`)

With `chatgpt_base_url = "http://<proxy>/backend-api/"`, Codex sends its
ChatGPT account calls (`/wham/usage`, rate-limit reset credits, plugins, tasks)
here. Any `/backend-api/` path that no model route claims is forwarded to
`https://chatgpt.com` as one pooled Codex account: the one new requests route
to (preferred credential, then the routing strategy), or the one that
recovers first when all are spent. The proxy replaces `Authorization` and
`Chatgpt-Account-Id` with that account's, and strips the client key.

The caller authenticates with a client key (`access.api-keys`). Codex sends its
`auth.json` access token, so that token must be a client key; Codex then needs
no ChatGPT login of its own. A `401` from chatgpt.com is returned as `502`: it
means the pooled credential failed, and Codex would otherwise try to refresh
its own login.

### Codex model, image and search calls (`/backend-api/codex/*`)

With `openai_base_url = "http://<proxy>/backend-api/codex"`, Codex keeps its
built-in OpenAI provider and sends its model calls here, authenticated with the
same `auth.json` token. A custom `model_provider` would turn off the tools Codex
offers only on that provider, such as image generation.

| Route | Served by |
|---|---|
| `GET /responses` (websocket), `POST /responses`, `POST /responses/compact` | the Responses API, as `/v1/responses` |
| `POST /alpha/search` | standalone web search |
| `POST /images/generations`, `POST /images/edits` | the Images API, as `/v1/images/*` (`gpt-image-2` routes to Codex accounts) |

Other `/backend-api/codex/*` paths (model list, analytics) fall through to the
account forwarder above.

A client that disconnects before chatgpt.com answers gets `499`, not `502`.

Codex's apps MCP (`/backend-api/ps/mcp`) receives no credential: Codex sends its
ChatGPT token to an apps MCP server only on chatgpt.com, so through the proxy
those calls answer `401`. Codex's other features are unaffected.

## Claude Code connectivity check

`GET`/`HEAD /api/hello` answers `200 {"message":"hello"}` without a key, as
api.anthropic.com does. Claude Code probes it at startup.
