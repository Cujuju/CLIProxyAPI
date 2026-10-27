# Desktop supervisor API

Routes for a desktop app that launches the proxy, shows per-account usage and
stops it. All sit under `/v8/management` and take the management key as
`Authorization: Bearer <key>` or `X-Management-Key: <key>`.

## Management key

- `MANAGEMENT_SUPERVISOR_PASSWORD` (recommended; random, ≥ 32 characters): read
  once at startup, then cleared from the proxy's environment, so plugins and
  child processes never see it. Accepted from loopback only. Exempt from the
  failed-key IP ban; other wrong keys are still counted and banned. Nothing is
  written to `config.yaml`.
- `MANAGEMENT_PASSWORD`: enables the routes without writing a key to disk.
- `config.yaml` (v8 layout; the legacy `remote-management:` block is still accepted):

```yaml
management:
  allow-remote: false   # localhost only
  secret-key: "<choose a key>"   # plaintext is hashed in memory at load
```

## Failed management keys

Counted per client IP. 5 failures lock the IP out of management for 5 minutes
(`403`). After a lockout, 3 more failures lock it out for 30 minutes, as does
every later lockout. A correct key, or 2 hours without a request, resets the IP
to the first step.

A client key (`access.api-keys`) sent as the management key gets `401` (`this
is a client API key; management calls need the management key`) and is not
counted.

- `GET /server/lockouts`: IPs with failures or an active lockout:
  `{"lockouts":[{"ip":"127.0.0.1","failures":2,"failures_allowed":3,"locked_until":"2026-10-26T19:30:00Z","lockouts":1}]}`.
  `failures_allowed` is the count that triggers the next lockout; `locked_until`
  is `null` when not locked out.
- `DELETE /server/lockouts`: lifts every lockout and clears all failures. A
  locked-out IP can't call it; the supervisor password can.

## Process lifecycle

- `-parent-pid <pid>`: the proxy shuts down gracefully when that process exits.
  Windows waits on the process handle; other platforms poll every
  `ParentPollInterval` (2s).
- `POST /server/shutdown`: replies `202 {"status":"shutting down"}`, then stops
  the proxy as SIGINT does. `403` when the TCP peer or resolved client IP is not
  loopback, even with `allow-remote: true`.

## Routing

- `routing.strategy: soonest-reset` (aliases `soonestreset`, `sr`): each new
  conversation goes to the available account whose weekly window resets first,
  then the 5-hour window, then credential ID. Unknown or past resets rank last.
  A cooling-down account is skipped until it recovers. With
  `routing.session-affinity: true`, a conversation stays on its account.
- `routing.prefer-auth-kind`: `oauth` uses subscriptions before API keys,
  `api-key` the reverse; unset mixes both. With none of the preferred kind
  available, requests use the other kind. A recovered preferred credential takes
  the next request, even in a conversation bound to a fallback. Explicit
  credential pins take precedence.
- `disabled: true` on an entry under `api-keys.<family>[].keys` stops routing to
  that key without deleting it. The key keeps its credential identity.

Read and write through the v8 config API (writes accept `If-Match: <ETag from the GET>`):

```http
GET /v8/management/config/routing/strategy          -> "soonest-reset"
PUT /v8/management/config/routing/strategy          body: "soonest-reset"
GET /v8/management/config/routing/session-affinity  -> true
PUT /v8/management/config/routing/session-affinity  body: true
```

## Claude usage lookups

Traffic headers carry only the 5-hour, weekly and weekly Fable windows, and are
not persisted. The proxy therefore looks up each enabled Claude OAuth
credential's usage (`GET https://api.anthropic.com/api/oauth/usage`), which also
reports per-model and spend windows. Lookups are stored apart from header
readings: traffic updates the windows its headers carry and never removes
lookup-only windows.

Every minute the proxy looks up credentials with no lookup, one older than 15
minutes, or a window that reset since the last lookup. Lookups run one at a
time, 2s apart. A failed credential is retried after 15 minutes. An expired
access token is not a failure: the lookup retries 10s later, after renewal.

## GET /usage/windows

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
      "account_view": false,
      "windows": [
        {"key": "five_hour", "label": "5-hour", "used_percent": 12, "resets_at": "2026-10-25T18:00:00Z", "status": "allowed", "duration_seconds": 18000, "scoped": false, "reset_cadence": "on_use"},
        {"key": "seven_day", "label": "Weekly", "used_percent": 41, "resets_at": "2026-10-29T09:00:00Z", "status": "allowed_warning", "duration_seconds": 604800, "scoped": false, "reset_cadence": "fixed"},
        {"key": "seven_day_fable", "label": "Weekly · Fable", "used_percent": 100, "resets_at": "2026-10-30T09:00:00Z", "status": "rejected", "duration_seconds": 604800, "scoped": true, "reset_cadence": "fixed"}
      ]
    }
  ]
}
```

- `name`: the value for `DELETE /credentials?name=<name>` and the `name` field
  of `PATCH /credentials/status` and `/credentials/fields`.
- `label`: derived account label. `note`: user-set note. `email`, `note` and
  `priority` are omitted when unset.
- `status_message`: `""`, a proxy reason (e.g. `disabled via management API`), or
  the provider's last error response body verbatim, often JSON such as
  `{"type":"error","error":{"type":"rate_limit_error","message":"…"}}`. Raw API
  keys in it are masked.
- `cooldown_until`: when a benched credential becomes eligible again, else `null`.
- `weight`: the `weighted-round-robin` weight (default 1; 0 excludes the credential).
- `plan`: the subscription. Claude: from the OAuth profile, looked up every 6
  hours. Codex: the login token's `plan_type` (`{"tier": "plus", "type": "plus", "label": "Plus"}`).
  `null` before the first lookup and for other providers. `multiplier`: 5-hour
  usage relative to Claude Pro, from the tier (`..._5x` = 5; Pro = 1); `null`
  when unpublished (Team, Enterprise, every Codex plan).
- `reset_credits`: a Codex account's rate-limit reset credits from ChatGPT's
  usage document, looked up every 15 minutes (the lookup also records windows):
  `{"available": 1, "applicable": 0, "checked_at": "2026-10-26T12:00:00Z"}`.
  `available`: credits held. `applicable`: credits usable now, against a reached
  limit. `null` before the first lookup, when the account reports none, and for
  other providers.
- `observed_at`: time of the latest usage snapshot, else `null`.
- `next_pick`: the credential the strategy would choose for a new conversation
  within its provider. Rotating strategies (`round-robin`) mark none.
  Config-file credentials are ranked but not listed, so a provider may have no
  listed `next_pick`.
- `account_view`: the Codex credential that Codex's ChatGPT account calls
  (`/backend-api/`: usage, plan) reach, also under rotating strategies and when
  every account is cooling down (then the one that recovers first). `false` for
  every other credential.
- `windows`: account-wide first, shorter before longer. `used_percent`,
  `resets_at`, `status` and `duration_seconds` (window length) are `null` when
  unknown. `scoped: true` marks model-, feature- or spend-specific windows; only
  unscoped windows describe the account and drive `soonest-reset`.
  `reset_cadence`: `"fixed"` (next period starts at the reset), `"on_use"` (starts
  with the first request after the reset) or `null` (unknown: Codex, Devin).
  Claude reports `on_use` for 5-hour and `fixed` for weekly windows. Only Claude,
  Codex and Devin report windows; other providers return `[]`.

Config-backed API keys are listed, disabled ones included, with
`"api_key": {"masked": "sk-a…1234", "success": 7, "failed": 2}` (counts since
the proxy started). Raw keys are never returned. Keys without observed quota
have `"windows": []`. Usage lookups and login renewal don't apply to API keys.

## POST /usage/refresh

Body `{"auth_index": "3f2a9c"}` refreshes one Claude or Codex credential; an
empty body refreshes every enabled Claude and Codex OAuth credential, 2s apart.
A Codex lookup reads ChatGPT's usage document, so its windows and reset credits
appear without Codex traffic. Returns the `GET /usage/windows` shape, plus
`"errors": {"<id>": "<message>"}` for failed lookups.

- `404`: unknown `auth_index`.
- `400`: not an enabled Claude or Codex OAuth credential, or invalid body.

## POST /usage/reset-credit

Spends one of a Codex account's rate-limit reset credits (ChatGPT's
`POST /backend-api/wham/rate-limit-reset-credits/consume`, as Codex's `/usage`
panel does). Body `{"auth_index": "<auth_index>"}`; enabled Codex logins only.
Each call sends a fresh `redeem_request_id`, so it spends at most one credit.

```json
{"code": "reset", "windows_reset": 2, "usage": { "...": "GET /usage/windows payload" }}
```

`code`: `reset` (spent; reached limits cleared, cooldown lifted),
`nothing_to_reset` (no limit reached, nothing spent), `no_credit` or
`already_redeemed`. The credit count is then looked up again; a failed lookup
adds `refresh_error`.

- `400`: not an enabled Codex login. `404`: unknown index. `502`: ChatGPT failed.

## GET / PUT /routing/preferred-auth

`PUT {"provider":"claude","auth_index":"3f2a9c"}` sends that provider's new
requests to the credential while it is available, ahead of the strategy and
session affinity. When it is cooling down, disabled or already tried in the
request, routing is automatic. `auth_index: ""` clears it. Both methods return
`{"preferred":{"claude":"3f2a9c"}}`. Held in memory only; re-apply after a
restart. `GET /usage/windows` marks it `"preferred": true` and moves
`next_pick` to it while usable. Sessions bound during the override keep their
binding until session affinity expires.

- `400`: missing provider, or the credential belongs to another provider.
- `404`: unknown `auth_index`.

## GET /usage/events

Server-sent events: `event: ready` on connect, then one `event: request` per
upstream attempt, successful or failed:

```json
{"at":"…","request_id":"…","selection_id":"…","provider":"claude","model":"…",
 "auth_id":"…","auth_index":"3f2a9c","stream":true,"failed":false,"status_code":0,
 "latency_ms":5400,"ttft_ms":900,"input_tokens":1200,"output_tokens":300,"cached_tokens":0,
 "cache_creation_tokens":0}
```

- `cached_tokens`: cache reads. `cache_creation_tokens`: cache writes. Claude
  counts both apart from `input_tokens`; OpenAI includes `cached_tokens` in
  `input_tokens` and reports no cache writes.
- Failed attempts add `status_code` and a sanitized `error` summary. Attempts of
  one credential selection share `selection_id`.
- `event: resync`: the reader fell behind; reload `GET /usage/windows`.
- `event: usage` (data `{}`): a usage lookup recorded new windows; reload
  `GET /usage/windows`. Lookups before the reader catches up share one event.
- `: keep-alive` comment lines every 15s.

## Adding a Claude account (OAuth)

1. `GET /oauth/auth-url?provider=claude&is_webui=true` returns
   `{"status":"ok","url":"<authorize URL>","state":"<state>"}`. Open `url` in a
   browser. With `is_webui=true` the proxy takes the redirect on `localhost:54545`.
2. Without `is_webui`, post the final redirect URL:
   `POST /oauth/callback` `{"provider":"claude","redirect_url":"<url>"}`.
3. Poll `GET /oauth/status?state=<state>`: `{"status":"wait"}` while pending,
   `{"status":"ok"}` when saved, `{"status":"error","error":"<message>"}` on
   failure. Sessions time out after 5 minutes;
   `DELETE /oauth/session?state=<state>` cancels one.

## Pool usage limit (429)

When every credential serving a model is spent, the request fails with `429`
and error type `usage_limit_reached`, including the request that spends the
last credential. `X-Should-Retry: false` stops client retries. The soonest reset
is repeated in each client's limit headers:

- Claude Code: `Anthropic-Ratelimit-Unified-Status: rejected`, `-Reset`,
  `-Representative-Claim` (`five_hour` or `seven_day`), and that window's
  `-5h-`/`-7d-` `Status`, `Reset` and `Utilization: 1`. Read only when Claude
  Code is signed in to a subscription, not with `ANTHROPIC_AUTH_TOKEN`.
- Codex: `X-Codex-Primary-Used-Percent: 100`, `X-Codex-Primary-Reset-At`, and
  `X-Codex-Primary-Window-Minutes` when the window is known.

## Codex account calls (`/backend-api/*`)

With `chatgpt_base_url = "http://<proxy>/backend-api/"`, Codex's ChatGPT
account calls (`/wham/usage`, reset credits, plugins, tasks) are forwarded to
`https://chatgpt.com` as one pooled Codex account: the one new requests route
to (preferred credential, then the strategy), or the one that recovers first
when all are spent. The proxy replaces `Authorization` and `Chatgpt-Account-Id`
with that account's, strips the client key, and drops cookies both ways.

Forwarded paths (`codexAccountRoutes` in `codex_backend_paths.go`, from codex-rs
`rust-v0.164.0`):

| Path under `/backend-api/` | Methods |
|---|---|
| `wham/…` (usage, plan, reset credits, cloud tasks, environments, settings) | any |
| `codex/…` not claimed by a model route (model list, analytics) | any |
| `ps/plugins/…`, `ps/apps/batch`, `plugins/featured`, `public/plugins/workspace/…` | any |
| `connectors/directory/list`, `connectors/directory/list_workspace` | any |
| `accounts/verified_access` | `GET` |
| `files`, `files/…` (upload only) | `POST` |

Other paths (conversations, memories, account admin, file downloads, the apps
MCP) answer `404` without reaching chatgpt.com and log a warning naming the
path; a new Codex account call shows up there. Paths with dot segments, doubled
slashes, percent-escapes or backslashes are refused.

The caller authenticates with a client key (`access.api-keys`). Codex sends its
`auth.json` access token, so that token must be a client key; Codex needs no
ChatGPT login of its own. A `401` from chatgpt.com is returned as `502`, so
Codex doesn't try to refresh its own login.

### Codex model, image and search calls (`/backend-api/codex/*`)

With `openai_base_url = "http://<proxy>/backend-api/codex"`, Codex keeps its
built-in OpenAI provider and sends model calls here with the same `auth.json`
token. A custom `model_provider` turns off tools Codex offers only on that
provider, such as image generation.

| Route | Served by |
|---|---|
| `GET /responses` (websocket), `POST /responses`, `POST /responses/compact` | the Responses API, as `/v1/responses` |
| `POST /alpha/search` | standalone web search |
| `POST /images/generations`, `POST /images/edits` | the Images API, as `/v1/images/*` (`gpt-image-2` routes to Codex accounts) |

Other `/backend-api/codex/*` paths (model list, analytics) go to the account
forwarder above. A client that disconnects before chatgpt.com answers gets
`499`, not `502`.

Codex's apps MCP (`/backend-api/ps/mcp`) gets no credential, since Codex sends
its ChatGPT token to an apps MCP server only on chatgpt.com. Through the proxy
those calls answer `401` (`404` with a client key; the pool token never reaches
connected apps).

## Claude Code connectivity check

`GET`/`HEAD /api/hello` answers `200 {"message":"hello"}` without a key, as
api.anthropic.com does. Claude Code probes it at startup.
