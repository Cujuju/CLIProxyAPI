# Desktop supervisor API

Endpoints and flags for a desktop app that launches the proxy, shows per-account
usage, and stops it. All routes sit under `/v8/management` and use the normal
management authentication: `Authorization: Bearer <key>` or `X-Management-Key: <key>`.

## Enabling management for a local supervisor

Either set a key in `config.yaml` (v8 layout):

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
      "observed_at": "2026-10-25T12:00:00Z",
      "next_pick": true,
      "windows": [
        {"key": "five_hour", "label": "5-hour", "used_percent": 12, "resets_at": "2026-10-25T18:00:00Z", "status": "allowed"},
        {"key": "seven_day", "label": "Weekly", "used_percent": 41, "resets_at": "2026-10-29T09:00:00Z", "status": "allowed_warning"},
        {"key": "seven_day_fable", "label": "Weekly · Fable", "used_percent": 100, "resets_at": "2026-10-30T09:00:00Z", "status": "rejected"}
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
- `observed_at` is the time of the latest usage snapshot, else `null`.
- `next_pick` marks the credential the strategy would choose for a new
  conversation within its provider. Rotating strategies (`round-robin`) mark none.
  Credentials defined in the config file are ranked but not listed, so a provider
  may have no listed `next_pick`.
- `windows` are sorted account-wide first, shorter before longer. `used_percent`,
  `resets_at` and `status` are `null` when unknown. Claude, Codex and Devin
  credentials report windows; other providers return `[]`.

## POST /v8/management/usage/refresh

Body `{"auth_index": "3f2a9c"}` refreshes one Claude credential; an empty body
refreshes every enabled Claude OAuth credential (2s apart, so this can take a
while with many accounts). Returns the same shape as `GET /usage/windows`, plus
`"errors": {"<id>": "<message>"}` when a lookup failed.

- `404` unknown `auth_index`.
- `400` the credential is not an enabled Claude OAuth credential, or the body is invalid.

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
