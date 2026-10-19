# WAHA runtime behavior not described by the OpenAPI spec

Source read: `https://github.com/devlikeapro/waha`, branch `core`, commit
`0858b4f0efa5d2afc085c41e3873aae29154f6c0` (2026-10-14, `src/version.ts` says
`2026.9.2`). All paths below are relative to that repository and that commit.
This is the **Core** edition: `src/plus` is not public, so anything that only
exists in Plus is out of reach and marked as such. The OpenAPI pinned in
`contract/waha-openapi.json` is 2026.8.2; nothing below contradicts it, it only
fills gaps.

Framework facts that shape every answer: NestJS 11 on Express, `class-validator`
0.14, `axios` 1.x with `axios-retry` 4.x for webhooks. NestJS defaults apply
unless stated: `POST` handlers answer **201**, `GET`/`PUT`/`DELETE` answer
**200**, and a handler that returns `null`/`undefined` sends an **empty body**
with that status (no `null` literal).

Notation: `GOWS` is the engine we emulate. "NOWEB/WEBJS" notes are only given
where they differ in a way a client could notice.

---

## 1. Error responses

All errors go through `src/nestjs/AllExceptionsFilter.ts`:

```ts
if (exception.code === 'ENOENT') { 404 {error:{code:404,key:'FILE_NOT_FOUND',message:'File not found',details:'File not found or no longer available'}} }
if (exception instanceof HttpException) { res.status(exception.getStatus()).json(exception.getResponse()) }
else 500 {statusCode, timestamp, exception: serializeError(e), request:{path,method,body,query}, version: VERSION}
```

So the body of an `HttpException` is whatever NestJS `HttpException.createBody`
builds (verified against `@nestjs/common@11.1.6`):

| Thrown as | Body |
|---|---|
| `new XException()` (no argument) | `{"message":"<Default Description>","statusCode":N}` |
| `new XException("text")` | `{"message":"text","error":"<Default Description>","statusCode":N}` |
| `new XException({...obj})` | the object itself, verbatim, no `statusCode` added |

### (a) Unknown session

The answer depends on which helper the route uses:

| Route kind | Status | Body |
|---|---|---|
| `GET /api/sessions/{s}`, `PUT /api/sessions/{s}`, `POST .../{s}/start`, `.../logout`, `.../restart` | 404 | `{"message":"Session not found","error":"Not Found","statusCode":404}` (`src/core/services/SessionService.ts`, `src/core/abc/manager.abc.ts` `restart`) |
| Routes using `SessionPipe` (`GET /api/sessions/{s}/me`, `/capping`, `/timelock`, passkey routes in `src/api/auth.controller.ts`) | 404 | `{"message":"We didn't find a session with name 's'.\nPlease start it first by using POST /api/sessions/s/start request","error":"Not Found","statusCode":404}`. Same answer when the session exists but is not running. (`src/core/manager.core.ts` `getSession`) |
| Every route that needs a working session: all `POST /api/send*`, `/api/reaction`, `/api/{session}/chats/...`, groups, contacts, labels, etc. (`manager.getWorkingSession` or `WorkingSessionParam`) | 422 | `{"error":"Session \"s\" does not exist","session":"s"}` |
| `POST /api/sessions/{s}/stop` | 201 | empty body (`getSessionInfo` returns `null`) |
| `DELETE /api/sessions/{s}` | 200 | empty body (no existence check, idempotent) |

The 422 path **waits** before failing: up to 5 s (polling every 500 ms) for the
session to be running (`WAIT_SESSION_RUNNING_TIMEOUT = 5_000` in
`src/core/abc/manager.abc.ts`).

### (b) Session exists but is not in the expected status

`waitUntilStatus` in `src/core/abc/manager.abc.ts`. After the running check
above, it polls up to 10 s (`WAIT_STATUS_TIMEOUT = 10_000`, every 500 ms) for
the status to be one of `expected`. On failure, **422** with this exact object
(no `statusCode`, no `message`):

```json
{"error":"Session status is not as expected. Try again later or restart the session",
 "session":"default","status":"SCAN_QR_CODE","expected":["WORKING"]}
```

If the session exists but is not running at all, the same object is returned
with `"status":"STOPPED"`. QR routes use `expected: ["SCAN_QR_CODE"]`
(`QRCodeSessionPipe`, `src/nestjs/pipes/SessionPipe.ts`). A missing session
name in this helper gives 422 `{"error":"Session name is required","session":...}`.

### (c) Validation errors

`src/nestjs/pipes/WAHAValidationPipe.ts` wraps Nest `ValidationPipe` with
`transform: true`, `whitelist: true`, and `forbidNonWhitelisted` only when
`WAHA_HTTP_STRICT_MODE` is truthy. It also deletes `x-api-key` from query
objects before validating. Default exception factory, so: **400**

```json
{"message":["name must be a string","config.webhooks.0.url must be a URL address"],
 "error":"Bad Request","statusCode":400}
```

`message` is a flat array of constraint strings; nested properties are
prefixed with the dotted parent path (`config.webhooks.0.`). Unknown
properties are silently stripped unless strict mode is on, in which case they
add `"property X should not exist"`.

Important: **the chatting controller has no validation pipe** on most send
routes (`/api/sendText`, `sendImage`, `sendFile`, `sendVoice`, `sendVideo`,
`sendSticker`, `forwardMessage`, `sendSeen`, `startTyping`, `stopTyping`,
`reaction`, `star`, `sendLocation`, `sendContactVcard`, `reply`; see
`src/api/chatting.controller.ts`). Their bodies reach the engine as raw JSON:
no type checking, no 400, DTO default values (`session = "default"`,
`linkPreview = true`, video `caption = "Just watch at this!"`) are **not**
applied. Bad input surfaces later as 422 or 500.

Session name rule (`src/nestjs/validation/SessionName.ts`): string, max 54
chars, `^[a-zA-Z0-9_-]*$`, message `Session name can only contain alphanumeric
characters, hyphens, and underscores (a-z, A-Z, 0-9, -, _) or be empty`.

**Session name extracted by the auth guard.** Routes whose policy reads the
session from the body or query (`FromBody('session')`, `FromQuery('session')`,
e.g. every `/api/send*`, `GET /api/contacts/*`, `GET /api/messages`) throw
before validation if it is missing or blank, for every caller including admin:
**400** `{"message":"Session name is required","error":"Bad Request","statusCode":400}`
(`src/core/auth/policies.ts` `requireSessionName`). So the documented
`session` default of `"default"` never takes effect on those routes.

### (d) Missing or invalid API key

`src/core/auth/api-key-auth.middleware.ts`: **401**
`{"message":"Unauthorized","statusCode":401}` for a missing key, an unknown
key, an inactive key, or a repository error. No `WWW-Authenticate` header.
Authenticated but not allowed (CASL policy): **403**
`{"message":"Forbidden","statusCode":403}` (`src/core/auth/policies.guard.ts`).
A route with no `@CheckPolicies` also answers 403.

### (e) Generic 500

```json
{"statusCode":500,"timestamp":"2026-10-19T12:00:00.000Z",
 "exception":{"message":"...","name":"Error","stack":"Error: ...\n    at ..."},
 "request":{"path":"/api/sendText","method":"POST","body":{...},"query":{}},
 "version":{"version":"2026.9.2","engine":"GOWS","tier":"CORE","browser":null,
            "platform":"linux/x64","worker":{"id":null}}}
```

`exception` holds every own property of the error plus `name`, with `stack`
moved last (`serializeError`). For gRPC errors from GOWS that includes `code`,
`details`, `metadata`. The request body is echoed back in full (base64 media
included). Thrown plain `Error`s inside handlers (e.g. a malformed message id,
`"text" must include the URL provided in the "preview.url"`) end up here as 500.

### (f) Not supported by the engine

`src/core/exceptions.ts`, `NotImplementedByEngineError extends
NotImplementedException`: **501**

```json
{"message":"The method is not implemented by 'GOWS' engine. Check the docs and try another engine: https://waha.devlike.pro/",
 "error":"Not Implemented","statusCode":501}
```

An optional prefix can precede the sentence (`"<msg> The method is ..."`). The
engine name is `WHATSAPP_DEFAULT_ENGINE`. Plus-only features answer **422**:
`"<feature> is available only in Plus version for 'GOWS' engine. Check this out: https://waha.devlike.pro/"`
(or without the engine clause, `AvailableInPlusVersionAll`), in the
string-body shape (`message`, `error: "Unprocessable Entity"`, `statusCode`).
Examples on GOWS core: `PUT /api/star` and `POST /api/send/buttons/reply` answer 501.

Other notable literal 422s: `Session 'x' already exists. Use PUT to update it.`
(create), `Can request code only in SCAN_QR_CODE status. The current status is X`
(GOWS request-code), forwarding an unknown message:
`Message with id '<id>' not found`.

Unknown route: Nest default 404 `{"message":"Cannot GET /x","error":"Not Found","statusCode":404}`.

---

## 2. API key authentication

### Where the key comes from

`src/core/auth/HeaderOrQueryApiKeyStrategy.ts`:

```ts
const headerKey = req.headers['x-api-key'];
const queryKey = req.query['x-api-key'];
const apiKey = headerKey ?? queryKey;
```

Only the `X-Api-Key` header (case-insensitive, as all HTTP headers) or the
`x-api-key` query parameter. Header wins. No `Authorization: Bearer`, no basic
auth for the API. The query parameter is removed from `req.query` after auth
and redacted in access logs. WebSocket: query parameter only, matched
case-insensitively, first occurrence wins (`src/core/auth/WebSocketAuth.ts`).

### Server key from the environment

`src/core/auth/config.ts`, `src/core/auth/ApiKeyAuthFactory.ts`, `src/core/auth/auth.ts`:

- `WHATSAPP_API_KEY`, when set, overwrites `WAHA_API_KEY` (legacy alias).
- If `WAHA_API_KEY` is unset, empty, or one of the weak values
  (`123`, `321`, `waha`, `admin`, 32 zeros, 32 ones, or a specific
  `sha512:98b6d1...` hash), a random 32-hex key (`randomUUID` without dashes)
  is **generated** and printed in the logs as `WAHA_API_KEY=...`, unless
  `WAHA_NO_API_KEY` is truthy. With `WAHA_NO_API_KEY` and no key, auth is off
  (`NoAuth`: every request is admin).
- `sha512:<hex>`: the presented key is hashed with SHA-512, hex digest
  compared with `<hex>` (constant-time, `crypto.timingSafeEqual`). Any other
  value is a plaintext key, compared constant-time.
- Docker image only: `entrypoint.sh` hashes a plaintext `WAHA_API_KEY` /
  `WHATSAPP_API_KEY` into `sha512:<hex>` before starting node and unsets the
  plaintext. `WAHA_API_KEY_PLAIN` exists for apps that need to call WAHA back.
- An env key always yields an **admin** user (`{isAdmin: true, session: null}`).
  Otherwise the key is looked up in the API-key table (`getActiveByKey`,
  `isActive = 1`) and yields `{isAdmin, session, actions}`.

### Which paths are protected

`src/core/app.module.core.ts` registers path contributions
(`src/plugins/HttpPathsService.ts`):

```ts
{ prefix: '/ping',   include: { accessLog: false, authBasic: false } },
{ prefix: '/api/',   include: { authBasic: false } },
{ prefix: 'api',     include: { authApiKey: true } },
{ prefix: '/health', include: { authBasic: false } },
{ prefix: 'health',  include: { authApiKey: true } },
{ prefix: '/ws',     include: { authBasic: false } },
```

- **API key** middleware covers `/api/**` and `/health` only. That includes
  `/api/files/**` (media; any valid active key works, there is no per-session
  check on files because ServeStatic has no guard) and `/api/server/*`.
- **No auth at all**: `/ping` (returns `{"message":"pong"}`).
- `/health` needs a key **and** admin (`CanServer(Action.Manage)`).
- `/ws`: authenticated inside the gateway (section 8), not by the middleware.
- Everything else (Swagger UI served at `/`, `/dashboard`) is behind **HTTP
  basic auth** (`express-basic-auth`, `challenge: true`), not the API key.
  Swagger credentials: `WHATSAPP_SWAGGER_USERNAME`/`WHATSAPP_SWAGGER_PASSWORD`
  (password defaults to the dashboard password; disabled by
  `WHATSAPP_SWAGGER_NO_PASSWORD`). Dashboard: `WAHA_DASHBOARD_USERNAME`
  (default `admin`) / `WAHA_DASHBOARD_PASSWORD` (random if unset or weak, unless
  `WAHA_DASHBOARD_NO_PASSWORD`).
- `WHATSAPP_API_KEY_EXCLUDE_PATH`: comma-separated paths excluded from the API
  key middleware and from basic auth. With no user the CASL factory grants
  admin rules (`src/core/auth/casl.ability.ts`).

### `GET /api/keys`

Admin only (`CanServer(Action.Manage)`, `src/api/apikeys.controller.ts`).
Returns the **full plaintext** key: keys are stored in plaintext in the
`key` column and `toDTO` copies it (`src/core/services/ApiKeyService.ts`):

```json
[{"id":"key_id_01jxxxxxxxxxxxxxxxxxxxxxxx","key":"key_<32 alnum>","isActive":true,
  "isAdmin":false,"session":"default","actions":null}]
```

Generated values: `id = "key_id_" + lowercase ULID`, `key = "key_" + 32 chars
from [0-9a-zA-Z]`. Invariants (422 on violation): admin keys cannot have a
`session`; non-admin keys must have one; `actions` only on session keys;
session must exist. `POST /api/keys/media` and `POST /api/keys/control` create
or return a deterministic key `key_id_media_<session>` / `key_id_control_<session>`.
Delete returns `{"result":true}`.

### Session keys and `actions`

`src/core/auth/casl.rules.ts`. When `actions` is `null`, the default is:

```ts
{ delete: false, setting: true, control: true, app: true, read: true, send: true }
```

Every session key may always: `GET /api/sessions` (list, filtered to its own
session), and server `retrieve` (`/api/server/version`, `/api/server/status`,
`/api/version`, apps catalogue). Never: create sessions, `/api/keys` CRUD,
`/api/server/environment`, `/api/server/stop`, `/api/server/debug/*`, `/health`
(all `Manage`). A missing action flag counts as false when `actions` is set.

| Action | Route groups (from `@CheckPolicies` in `src/api/*.controller.ts`) |
|---|---|
| `read` | `GET /api/sessions/{s}`, `/me`, `/capping`, `/timelock`; all `GET` under `/api/{s}/chats` (list, overview incl. `POST overview`, picture, messages, one message), contacts reads (`GET /api/contacts/*`, `GET /api/{s}/contacts/*`), groups reads, labels reads, presence reads, channels reads, lids, `/api/{s}/media/convert/*`, `GET /api/{s}/new-message-id`, deprecated `GET /api/messages` and `GET /api/checkNumberStatus`, `POST /api/keys/media` |
| `send` | every `POST /api/send*`, `/api/reply`, `/api/forwardMessage`, `/api/sendSeen`, `/api/startTyping`, `/api/stopTyping`, `PUT /api/reaction`, `PUT /api/star`, deprecated `GET /api/sendText`; chat writes (delete chat, read, pin/unpin, delete/edit message, archive, unarchive, unread); group/label/channel/contact/profile writes; `POST /api/{s}/presence` and subscribe; `/api/{s}/status/*`; `/api/{s}/events`; `/api/{s}/calls/*` |
| `control` | `POST /api/sessions/{s}/start`, `/stop`, `/logout`, `/restart`, deprecated `/api/sessions/start|stop|logout`; all of `/api/{s}/auth/*` (QR, request-code, passkey); `GET /api/screenshot`; `POST /api/keys/control` |
| `setting` | `PUT /api/sessions/{s}` |
| `delete` | `DELETE /api/sessions/{s}` |
| `app` | session-scoped `/api/apps/*` |

---

## 3. Webhooks

Code: `src/modules/waha-webhook/WebhookPlugin.sender.ts`,
`WebhookPlugin.ts`, `webhook.config.ts`, envelope built by
`populateSessionInfo` in `src/core/abc/manager.abc.ts`.

### Request

- `POST <url>`, body is `JSON.stringify(envelope)` (compact, no spaces).
- Headers set per instance: `content-type: application/json`,
  `User-Agent: WAHA/<version>` (e.g. `WAHA/2026.9.2`), plus each
  `customHeaders[]` `{name, value}`.
- Headers set per request:
  - `X-Webhook-Request-Id`: `uniqid()` output (base36 time-plus-host id, about
    18 chars, **not** a UUID despite the comment), new per event and kept
    across retries.
  - `X-Webhook-Timestamp`: `envelope.timestamp.toString()` (Unix **ms**).
  - Only when `hmac.key` is set: `X-Webhook-Hmac` = lowercase hex
    `HMAC-SHA512(key, body)` over the exact body bytes sent, and
    `X-Webhook-Hmac-Algorithm: sha512`. The algorithm is fixed.
- HTTPS agent has `rejectUnauthorized: false` (self-signed endpoints accepted).
  Keep-alive agents (`agentkeepalive`).
- Fire-and-forget: each event is sent on `setImmediate`, independently,
  concurrently; no ordering guarantee, no queue, nothing persisted.

### Envelope

```ts
{ id, timestamp, event, session, metadata, me, payload, engine, environment }
```

Key order in the JSON is exactly that. Values:

- `id`: `"evt_" + lowercase ULID` (26 chars), assigned once when the event is
  produced, so every webhook and WebSocket client sees the same id.
- `timestamp`: `Date.now()` at production, ms.
- `session`: session name. `metadata`: `config.metadata` (key omitted when
  undefined). `engine`: `"GOWS"`.
- `me`: `session.getSessionMeInfo()`, `null` before login (section 4).
- `environment`: `{"version":"2026.9.2","engine":"GOWS","tier":"CORE","browser":null,"platform":"linux/x64","worker":{"id":null}}`
  (`src/version.ts`; `worker.id` = `WAHA_WORKER_ID` or null).
- `payload`: the event body with internal `_eventId`/`_timestampMs` removed.

### Retries

```ts
const DEFAULT_RETRY_DELAY_SECONDS = 2;
const DEFAULT_RETRY_ATTEMPTS = 15;
axiosRetry(instance, { retries: attempts, retryDelay, retryCondition: (error) => true })
```

- Failure = anything axios rejects: network error, or any status outside
  2xx (axios default `validateStatus`), **including 4xx**. `retryCondition`
  always returns true.
- `attempts` is the number of **retries**, so up to `1 + attempts` requests
  (16 by default).
- No request timeout (axios default `timeout: 0`).
- Delay (ms = `delaySeconds * 1000`): `constant` (default when policy unset):
  `max(ms, Retry-After)`; `linear`: `axios-retry` `linearDelay(ms)` (retry
  number times ms, at least `Retry-After`); `exponential`:
  `2^n * ms` plus 0 to 20% random jitter, at least `Retry-After`;
  `delaySeconds: 0`: no delay except `Retry-After`.
- Exhausted retries are only logged.

### Global webhook from env

Attached to **every** session in addition to `config.webhooks[]`
(`src/modules/waha-webhook/webhook.plugins.ts`). Created only when both URL and
events are non-empty:

| Env | Meaning |
|---|---|
| `WHATSAPP_HOOK_URL` | URL (http/https required, TLD not required) |
| `WHATSAPP_HOOK_EVENTS` | comma list, e.g. `message,session.status` or `*`; no default |
| `WHATSAPP_HOOK_HMAC_KEY` | HMAC key |
| `WHATSAPP_HOOK_RETRIES_POLICY` | `linear` / `exponential` / `constant` |
| `WHATSAPP_HOOK_RETRIES_DELAY_SECONDS` | number |
| `WHATSAPP_HOOK_RETRIES_ATTEMPTS` | number |
| `WHATSAPP_HOOK_CUSTOM_HEADERS` | `Name:value;Name2:value2` (a value containing `:` is rejected) |

Invalid global config aborts startup with `Invalid global webhook config: ...`.

### Event selection and `*`

`src/utils/events.ts` `EventWildUnmask`, `src/structures/enums.dto.ts`:

- `*` expands to every event **except** `state.change` and `engine.event`.
- A value containing `*` is a prefix match: `message.*` matches
  `message.reaction`, `message.any`, `message.ack`, `message.ack.group`,
  `message.waiting`, `message.revoked`, `message.edited`, but not `message`.
- Unknown names are dropped with a warning (validation in the session DTO
  only accepts the enum values plus `*`). Duplicates removed.

Full event list: `session.status, message, message.reaction, message.any,
message.ack, message.ack.group, message.waiting, message.revoked,
message.edited, state.change, group.join, group.leave, group.v2.join,
group.v2.leave, group.v2.update, group.v2.participants,
group.v2.participants.join-request, presence.update, poll.vote,
poll.vote.failed, chat.archive, call.received, call.accepted, call.rejected,
label.upsert, label.deleted, label.chat.added, label.chat.deleted,
event.response, event.response.failed, engine.event`.

---

## 4. Session lifecycle

### Statuses and GOWS transitions

Enum: `STOPPED, STARTING, SCAN_QR_CODE, PASSKEY_REQUIRED,
PASSKEY_CONFIRMATION_REQUIRED, WORKING, FAILED`. GOWS
(`src/core/engines/gows/session.gows.core.ts` `start`, `subscribeEvents`):

| Trigger | New status |
|---|---|
| `start()` | `STARTING` |
| gRPC `StartSession` fails | `FAILED` (and the error propagates) |
| QR item `code` | `SCAN_QR_CODE`, set again on **every** new QR code (event fires each time) unless a passkey step is pending |
| QR item `passkey-request` | `PASSKEY_REQUIRED`, `data` = WebAuthn challenge |
| QR item `passkey-confirmation` | `PASSKEY_CONFIRMATION_REQUIRED`, `data` = `{code}` |
| QR item other than code/success/passkey | `FAILED` |
| `ConnectedEventData` | `WORKING` |
| `Disconnected` / `KeepAliveTimeout` (when not already STARTING) | `STARTING`, presence reset to null |
| `KeepAliveRestored` | `WORKING` |
| `LoggedOut` | `FAILED` |
| `stop()` | `STOPPED` |
| Apps hook fails before start | `FAILED` (session not started) |

### `session.status` payload

Built in the `WhatsappSession` constructor (`src/core/abc/session.abc.ts`):

```json
{"name":"default","status":"WORKING",
 "statuses":[{"status":"STOPPED","timestamp":1760000000000},
             {"status":"STARTING","timestamp":1760000000100},
             {"status":"WORKING","timestamp":1760000002200}],
 "data":null}
```

- Fires on every status assignment. Consecutive `WORKING` with equal `data`
  is deduplicated; other repeats are not.
- `WORKING` is delayed 2 s when `me` has no `id`/`pushName` yet. On GOWS the
  status is set before `me` is filled, so the first `WORKING` after connect
  is always about 2 s late.
- `statuses`: last 3 points (oldest first, current last), ms timestamps.
- `data`: null for most statuses; passkey info (see table); for `WORKING`,
  `{reachoutTimelock}` and/or `{messageCapping}` when an account restriction is
  active (re-issued as a new `WORKING` event when those change).
- The manager stores `envelope.me` on every `session.status` event, so a
  stopped session still reports its last `me`.

### Create, start, delete, restart

`src/core/services/SessionService.ts`, `src/core/manager.core.ts`.

- `POST /api/sessions` with no `name`: `session_` + lowercase ULID, e.g.
  `session_01k7x9m2q3r4s5t6v7w8x9y0za` (`generatePrefixedId`,
  `src/utils/ids.ts`). Existing name: 422 `Session 'x' already exists. Use PUT to update it.`
- **`start` default**: Swagger declares `default: true`, but the DTO field has
  no initializer and the service does `if (request.start) { start }`. An
  omitted `start` therefore **creates without starting** (status `STOPPED`).
  Response 201 = `SessionDetailedInfo` (below), `engine: {}` when not running.
- `PUT /api/sessions/{s}`: 404 if missing; stops the session if running,
  saves the new config (full replace of `config`), restarts it only if it was
  running.
- `DELETE /api/sessions/{s}`: unassign worker, **unpair** (GOWS `Logout`,
  removes the linked device from the phone), stop, clean auth and stored `me`,
  delete config and apps. 200, empty body. Session keys need `delete`
  (default false).
- `POST .../stop`: stop only (auth kept), returns session info (201).
- `POST .../logout`: unpair + stop + clean auth; restarts if it was running
  (so it comes back in `SCAN_QR_CODE`). 404 if the session does not exist.
- `POST .../restart`: 404 if missing; otherwise stop (errors ignored) and
  start, returns session info. Start is idempotent: already running returns
  without changes.
- After a stop the manager sleeps 10 ms for GOWS (`SESSION_STOP_TIMEOUT`).
- On boot, sessions assigned to this worker are restarted by default
  (`WAHA_WORKER_RESTART_SESSIONS=true`), or all stored sessions with
  `WHATSAPP_RESTART_ALL_SESSIONS=true`; `WHATSAPP_START_SESSION=a,b` starts
  named sessions; `WAHA_AUTO_START_DELAY_SECONDS` spaces restarts.

### `GET /api/sessions`

- Default: only **running** sessions (runtime map).
- `all=true` (accepts `true/false/1/0` style booleans): stored sessions too;
  stored-only ones appear as
  `{"name":..,"status":"STOPPED","config":<cfg or null>,"me":<last me or null>,"presence":null,"timestamps":{"activity":null}}`.
  Runtime info wins on name clash.
- Every list item also gets `assignedWorker` (worker id from the worker table,
  may be absent).
- `expand=apps` adds `apps`.

Runtime item (`src/modules/waha-session-runtime-info/SessionRuntimeInfoPlugin.ts`):
`{name, status, config, me, presence, timestamps:{activity}}`.
`GET /api/sessions/{s}` adds `engine`, for GOWS
`{"engine":"GOWS","grpc":{"client":"READY","stream":"READY"},"gows":{...GetSessionState}}`
(3 s timeout; `{}` when not running).

### `me` for GOWS

From `ConnectedEventData` and `getSessionMeInfo`:

```json
{"id":"5511999999999@c.us","pushName":"Name","lid":"123456789012345@lid",
 "jid":"5511999999999:12@s.whatsapp.net","reachoutTimelock":null,"messageCapping":null}
```

`id` is the device-less phone JID in `@c.us` form, `lid` device-less, `jid`
keeps the device number. `pushName` updates on `PushNameSetting`.
`reachoutTimelock` = `{enforcementType,isActive,timeEnforcementEnds}` or null,
`messageCapping` = `{cappingStatus,totalQuota,usedQuota,cycleStart,cycleEnd,mvStatus,oteStatus}` or null.
`null` (whole `me`) before the first connect.

### `presence` and `timestamps`

- `presence`: `"online"`, `"offline"` or `null` (chat presences like typing
  are not stored). Set by `setPresence` and reset to null on disconnect.
- With `WAHA_PRESENCE_AUTO_ONLINE` (default **on**), any API call marked
  `@Activity()` (sends, reactions, typing, profile, etc.) sets the account
  online first, and it goes back offline after
  `WAHA_PRESENCE_AUTO_ONLINE_DURATION_SECONDS` (default 90) without activity
  (`src/modules/waha-maintain-online-status/MaintainOnlineStatusPlugin.ts`).
- `timestamps.activity`: ms of the last such activity while `WORKING`, else
  null. Always `null` for stopped sessions or when auto-online is off.

---

## 5. Message ids and chat ids (GOWS)

### Serialized message id

`buildMessageId` in `src/core/engines/gows/session.gows.core.ts`:

```ts
const chatId = toCusFormat(info.Chat);
const participant = toCusFormat(info.Sender);
if (info.IsGroup) return `${info.IsFromMe}_${chatId}_${info.ID}_${participant}`;
return `${info.IsFromMe}_${chatId}_${info.ID}`;
```

`toCusFormat` (`src/core/utils/jids.ts`): groups, any `@broadcast`, `@newsletter`
unchanged; `@lid` kept with the `:device` part removed; anything else becomes
`<number>@c.us` (device removed).

| Case | Format |
|---|---|
| DM received | `false_5511999999999@c.us_3EB0ABCDEF` |
| DM sent by me (phone or API) | `true_5511999999999@c.us_3EB0ABCDEF` (chat is the peer) |
| DM in a LID-addressed chat | `false_123456789012345@lid_3EB0ABCDEF` |
| Group, received | `false_120363000000000000@g.us_3EB0ABCDEF_5511888888888@c.us` (or `..._123456789012345@lid` when the group is LID-addressed) |
| Group, mine | `true_120363000000000000@g.us_3EB0ABCDEF_<my id>` |
| Newsletter | `false_123456789@newsletter_<ID>` (IsGroup is false) |
| `status@broadcast` | `<fromMe>_status@broadcast_<ID>_<sender>`; this relies on whatsmeow setting `IsGroup` for broadcast chats (whatsmeow behavior, not visible in this repo). WAHA's own send path treats broadcast as group: `IsGroup: isJidGroup(jid) \|\| isJidBroadcast(jid)` |

Responses of send endpoints use the same builder with `IsFromMe: true`,
`Sender: me.id` (`messageResponse`), so groups and status get `_<me.id>` in
`@c.us` form. `message.ack` ids invert the receipt's `IsFromMe` and use
`MessageSender` or `me.id` as participant.

### Parsing incoming ids

`parseMessageIdSerialized` (`src/core/utils/ids.ts`): split on `_`; 3 or 4 parts
required, else `Error('Message id be in format false_11111111111@c.us_AAAAAAAAAAAAAAAAAAAA[_participant]')`
(500). `fromMe = parts[0] == 'true'`, chat and participant go through `toJID`.
With `soft = true` a string without `_` is accepted as a bare WhatsApp id.

- `reply_to`: `getMessageIdFromSerialized` uses soft parsing and keeps **only
  the raw id** (`parts[2]`); chat, fromMe and participant are discarded and
  GOWS resolves the quoted message from its own store. A bare id works.
- `PUT /api/reaction`: strict (full serialized id required); `fromMe` decides
  the reaction key sender (`me.id` or participant or chat).
- Edit, forward, poll vote, get one message: soft (bare id accepted).
- `sendSeen`: strict per id; ids with `fromMe = true` are skipped.

### Chat id normalization

Two session plugins on the `wid.chat` and `wid.mention` hooks:

1. `WidEnsureSuffixPlugin` (all engines, `src/modules/waha-wid-suffix/`):
   no `@` in the value means append `@c.us`.
2. `WidToJIDPlugin` (NOWEB and GOWS only, `src/modules/waha-wid-jid/`):
   `normalizeJid(toJID(wid))`: groups, broadcast, newsletter, `@lid`,
   `@meta.ai` unchanged; otherwise `<part before @>@s.whatsapp.net`; any
   `:device` removed.

So `5511999999999`, `5511999999999@c.us` and `5511999999999@s.whatsapp.net`
all reach GOWS as `5511999999999@s.whatsapp.net`. A leading `+` is **not**
stripped by these plugins (only `checkNumberStatus` strips `+`). Country
specific rewriting (e.g. Brazilian 9th digit) happens only when the matching
phone-number app is configured for the session (`src/apps/*-phone-numbers`).

---

## 6. Message payloads (GOWS)

### `message` and `message.any`

Source: raw `events.Message` from the GOWS stream, filtered by the session
`ignore` config. `message` = not from me; `message.any` = both. Mine get
`Status = ServerAck`, others `Status = DeliveryAck` before conversion. Deduped
by `fromMe + raw id` (60 s window). Dropped (no `message`/`message.any`):
reactions, poll updates, event responses, any `protocolMessage` (revoke,
edit, etc.), `deviceSentMessage` wrappers, sender-key-distribution-only
messages. `toWAMessage`:

```ts
{ id, timestamp: seconds (null for zero time), from: toCusFormat(Info.Chat), fromMe: Info.IsFromMe,
  source, body, to: Info.IsGroup ? Sender : null, participant: Info.IsGroup ? Sender : null,
  hasMedia, media: null, mediaUrl: message.media?.url, ack, location, vCards, ackName, replyTo, _data: message }
```

Notes:

- `from` is always the **chat** (even for my own messages); `to` is `null` in
  DMs and the sender in groups (same as `participant`). This differs from the
  DTO description and from NOWEB.
- `body`: `conversation`, else `extendedTextMessage.text`, else media
  caption, else edited text, else button/template reply text, else list
  title/description joined by `\n`; `null` when none
  (`extractBody`, `src/core/engines/noweb/session.noweb.core.ts`).
- `hasMedia`: true for document, image, video, audio, ptv, sticker (also
  inside templates and associated child messages).
- `media`: after download, `{url, mimetype, filename}` (plus `s3` with S3
  storage). Download disabled or mimetype filtered: `{url:null, filename,
  mimetype}`. Failure: same object plus `error` (the Error object serialized:
  own fields such as `code`, `details`, plus `details` = stack). `null` when
  there is no media.
- `mediaUrl`: always undefined on GOWS (the raw event has no `media` field),
  so the key is absent. Clients must use `media.url`.
- `ack`/`ackName`: from `Status` via `statusToAck`; GOWS `Error` (0) is mapped
  to `null`, giving `ackName: "UNKNOWN"`. Live events: mine `1/"SERVER"`,
  others `2/"DEVICE"`.
- `source`: `"app"` for messages not from me; for mine `"api"` when the
  sender device number equals the session's device (`me.jid`), else `"app"`.
- `replyTo`: from the message's `contextInfo`: `{id: stanzaID (raw id),
  participant: toCusFormat(participant), body, hasMedia, media, _data: quotedMessage}`;
  quoted media is downloaded with the same options. `null` when not a reply.
- `location`: `{live:false, latitude, longitude, name, address, url,
  description (comment), thumbnail (base64)}` for `locationMessage`;
  live location `{live:true, latitude, longitude, description (caption), thumbnail}`.
  Coordinates are **strings**. `null` otherwise
  (`src/core/engines/waproto/locaiton.ts`).
- `vCards`: `[vcard]` for `contactMessage`, list for `contactsArrayMessage`,
  else `null` (`src/core/engines/waproto/vcards.ts`).
- `_data`: the raw GOWS event object (`Info`, `Message` in whatsmeow JSON
  casing, `Status`, etc.).

### Ack enum

`src/structures/enums.dto.ts`: `ERROR=-1, PENDING=0, SERVER=1, DEVICE=2,
READ=3, PLAYED=4`, names identical to keys, unknown is `"UNKNOWN"`.
GOWS `MessageStatus`: `Error=0, Pending=1, ServerAck=2, DeliveryAck=3, Read=4, Played=5`.

### `message.ack` / `message.ack.group`

From `events.Receipt` (`receiptToMessageAck`). Groups go to
`message.ack.group`, the rest to `message.ack`. Receipt `Type` mapping:
`""`, `inactive`, `active` to `DEVICE`; `read` to `READ`; `played` to
`PLAYED`; `server-error` to `ERROR`; any other type is dropped (no `SERVER`
ack is ever produced from receipts). One payload per id in `MessageIDs`:

```json
{"id":"true_5511999999999@c.us_3EB0ABCDEF","from":"5511999999999@c.us","to":null,
 "participant":null,"fromMe":true,"ack":3,"ackName":"READ","_data":{...receipt}}
```

Deduped on `id-ack-participant` for 60 s.

### `message.reaction`

```json
{"id":"false_5511999999999@c.us_3EB0REACT","timestamp":1760000000,"from":"5511999999999@c.us",
 "fromMe":false,"source":"app","to":null,"participant":null,
 "reaction":{"text":"👍","messageId":"true_5511999999999@c.us_3EB0ORIGINAL"}}
```

`text` is `""` when the reaction is removed. `messageId` is rebuilt from the
reaction key (`buildMessageIdFromKey`). No `_data`.

### `message.revoked`

```json
{"after":<WAMessage of the protocol message>,"before":null,"revokedMessageId":"3EB0ORIGINAL","_data":{...}}
```

`revokedMessageId` is the **raw** WhatsApp id, not a serialized one. `before`
is always null on GOWS.

### `message.edited`

`WAMessage` of the edit protocol message (its `id` is the edit event's id)
with `body` replaced by the new text and `editedMessageId` = raw id of the
original message; `_data` raw event.

---

## 7. Media

### Storage and serving (default `WAHA_MEDIA_STORAGE=LOCAL`)

`src/core/media/local/*`, `src/core/media/MediaManager.ts`:

- Folder: `WHATSAPP_FILES_FOLDER` (relative paths resolved from cwd), default
  `/tmp/whatsapp-files`.
- File key: `<session>/<raw message id>.<ext>`, `ext` from `mime-types`
  (`audio/ogg` gives `oga`, `image/jpeg` gives `jpeg`, `video/mp4` gives
  `mp4`, `application/was` gives `zip`).
- URL: `<baseUrl>/api/files/<session>/<raw id>.<ext>`, e.g.
  `http://localhost:3000/api/files/default/3EB0ABCDEF.jpeg`.
  `baseUrl` = `WAHA_BASE_URL` (trailing `/` stripped) or
  `${WHATSAPP_API_SCHEMA}://${WHATSAPP_API_HOSTNAME}:${PORT || WHATSAPP_API_PORT}`
  (defaults `http`, `localhost`, `3000`).
- Served by `@nestjs/serve-static` under `/api/files`, behind the API key
  middleware (any valid key).
- Lifetime: `WHATSAPP_FILES_LIFETIME` seconds, default **180**; `0` keeps
  files forever. Each save schedules an unlink; at boot existing files are
  rescheduled by mtime (expired ones within 0 to 10 s).
- If the file already exists it is not downloaded again. Each step
  (check, fetch, save, URL) retries 5 times (1 to 3 s) unless the GOWS error is
  definitive (`FAILED_PRECONDITION`, `NOT_FOUND`, `INVALID_ARGUMENT`,
  `PERMISSION_DENIED`, `UNIMPLEMENTED`). GOWS download deadline 10 min.
- Other backends exist in core: `S3` and `POSTGRESQL` (`WAHA_S3_*`,
  `WAHA_MEDIA_POSTGRESQL_URL`).

### What is downloaded automatically

`src/config.service.ts`: `WHATSAPP_DOWNLOAD_MEDIA` default `true`,
`WHATSAPP_FILES_MIMETYPES` default empty = **all** mimetypes. When set, a comma
list matched with `mimetype.startsWith(entry)` (so `image` matches all
images). Overrides: `WAHA_EVENTS_DOWNLOAD_MEDIA` /
`WAHA_EVENTS_DOWNLOAD_MEDIA_MIMETYPES` for webhooks and WebSocket,
`WAHA_API_DOWNLOAD_MEDIA` / `WAHA_API_DOWNLOAD_MEDIA_MIMETYPES` for API reads
(which also take per-request `downloadMedia`).

### Sending files

`fileToMedia` / `sendMedia` in `session.gows.core.ts`:

- `file` with a `url` key: fetched with axios (`arraybuffer`, random browser
  User-Agent, TLS verification off, `src/utils/fetch.ts`); otherwise `data` is
  base64-decoded. `mimetype` and `filename` passed through.
- Default mimetype when missing: image `image/jpeg`, voice
  `audio/ogg; codecs=opus`, video and note `video/mp4`, sticker `image/webp`,
  document detected from content.
- Voice: waveform and duration computed before sending. Video: duration;
  `gifPlayback` when `file.mimetype == "image/gif"`. `asNote: true` sends a
  PTV (round video).
- `caption` used as text; `mentions` resolved through `wid.mention`.

### `convert: true`

Exists on `sendVoice` and `sendVideo` (also voice/video status). It runs
ffmpeg (`src/core/utils/ffmpeg.ts`), then sets the mimetype to
`audio/ogg; codecs=opus` or `video/mp4`. Voice pipeline (input written as
`input.mp3`, ffmpeg sniffs the real format):

```
ffmpeg -hide_banner -loglevel error -nostdin -y -i input.mp3 -vn -sn -dn -map 0:a:0 -map_metadata -1 -ac 1 -ar 48000 -c:a pcm_s16le input.wav
ffmpeg -hide_banner -loglevel error -nostdin -i input.wav -c:a libopus -b:a 32k -ar 48000 -ac 1 output.opus
opustags --in-place --delete ENCODER --delete encoder --set-vendor Recorder output.opus   (only if opustags exists)
```

Video:

```
ffmpeg -hide_banner -loglevel error -nostdin -i input.mp4 -c:v libx264 -map 0 -pix_fmt yuv420p -vf scale=trunc(iw/2)*2:trunc(ih/2)*2 -movflags +faststart output.mp4
```

A non-zero exit throws (500). The same converters back
`POST /api/{session}/media/convert/voice` and `/convert/video`, which return
the binary (`output.opus` / `output.mp4`).

---

## 8. WebSocket `/ws`

`src/api/websocket.gateway.core.ts`:

- URL: `ws(s)://<host>/ws?session=<name|*>&events=<e1,e2|*>&x-api-key=<key>`.
- `session`: default `*` (all sessions). A non-admin key asking for `*` is
  narrowed to its own session; it then needs `read` on that session.
- `events`: repeatable and/or comma-separated, default `*`; same unmasking as
  webhooks (section 3).
- Auth: `x-api-key` query parameter only (case-insensitive name). Failure:
  close code **1008** reason `Unauthorized`; not allowed: 1008 `Forbidden`.
- Each event is sent as one text frame: `JSON.stringify` of the same envelope
  as webhooks (`id, timestamp, event, session, metadata, me, payload,
  engine, environment`), same `id` as the webhook for that event.
- Server pings every 60 s and terminates clients that did not pong since the
  previous ping. Client messages are ignored.

---

## 9. Send endpoint responses (GOWS)

| Endpoint | Status | Body |
|---|---|---|
| `POST /api/sendText`, `sendImage`, `sendFile`, `sendVoice`, `sendVideo`, `sendSticker`, `sendLocation`, `sendContactVcard`, `sendPoll`, `sendList`, `send/link-custom-preview`, `forwardMessage`, `reply` | 201 | `{"id":"true_<chat>_<ID>[_<me.id>]","_data":<message>}` |
| `PUT /api/reaction` | 200 | same `{id, _data}` shape, id built from the reacted message chat |
| `PUT /api/star` | 501 | not implemented on GOWS (NOWEB/WEBJS/WPP implement it and return an empty 200) |
| `POST /api/sendSeen` with `messageId`/`messageIds` | 201 | empty body |
| `POST /api/sendSeen` without ids | 201 | `{"ids":[...]}`: marks unread incoming messages of the last 7 days (limit 100 for groups, 30 otherwise) |
| `POST /api/startTyping`, `/api/stopTyping` | 201 | `{"result":true}` |
| `POST /api/send/buttons/reply` | 501 | |

`_data` is the JSON-decoded `message` field of the GOWS gRPC
`MessageResponse` (`{id, timestamp, message: Json}`), i.e. the sent WhatsApp
proto message as serialized by the Go side; it is not a `WAMessage`. Its exact
contents are produced by the closed GOWS binary and are not visible in this
repository. The GOWS `timestamp` is not returned.

NOWEB returns the Baileys `WAMessage` proto in a different shape; WEBJS/WPP
return their library message objects. Clients that work across engines only
rely on `id`.

---

## 10. Server-wide environment variables

A compatible server should honor at least these (file: `src/config.service.ts`
unless noted):

| Variable | Default | Meaning |
|---|---|---|
| `PORT`, `WHATSAPP_API_PORT` | `3000` | listen port; `PORT` wins |
| `WHATSAPP_API_HOSTNAME` | `localhost` | used only to build `baseUrl` |
| `WHATSAPP_API_SCHEMA` | `http` | `http`/`https`, used in `baseUrl` |
| `WAHA_BASE_URL` | derived | public base URL for media links |
| `WAHA_API_KEY` / `WHATSAPP_API_KEY` | generated | API key, plain or `sha512:<hex>` (section 2) |
| `WAHA_NO_API_KEY` | false | allow running without a key |
| `WHATSAPP_API_KEY_EXCLUDE_PATH` | empty | comma paths without auth |
| `WHATSAPP_DEFAULT_ENGINE` | `WEBJS` | engine name (`src/config.ts`) |
| `WAHA_PRINT_QR` | true | print QR in logs (`src/core/config/EngineConfigService.ts`) |
| `WAHA_LOG_LEVEL` | `info` | `fatal,error,warn,info,debug,trace` (`src/utils/logging.ts`); `DEBUG` set forces debug |
| `WAHA_LOG_FORMAT` | `PRETTY` | `JSON` for raw JSON logs |
| `WAHA_HTTP_LOG_LEVEL` | `info` | access log level |
| `WAHA_HTTP_STRICT_MODE` | false | reject unknown body properties |
| `WAHA_DEBUG_MODE` | false | enables debug endpoints |
| `WHATSAPP_START_SESSION` | empty | comma list of sessions to start at boot |
| `WHATSAPP_RESTART_ALL_SESSIONS` | false | restart all stored sessions at boot |
| `WAHA_WORKER_ID`, `WAHA_WORKER_RESTART_SESSIONS` | empty, true | worker identity; restart this worker's sessions at boot |
| `WAHA_AUTO_START_DELAY_SECONDS` | 0 | delay between boot restarts |
| `WHATSAPP_HOOK_*` | | global webhook (section 3) |
| `WHATSAPP_DOWNLOAD_MEDIA`, `WHATSAPP_FILES_MIMETYPES`, `WAHA_EVENTS_DOWNLOAD_MEDIA*`, `WAHA_API_DOWNLOAD_MEDIA*` | true, all | media download policy (section 7) |
| `WHATSAPP_FILES_FOLDER`, `WHATSAPP_FILES_LIFETIME` | `/tmp/whatsapp-files`, 180 | local media storage |
| `WAHA_MEDIA_STORAGE` | `LOCAL` | `LOCAL`, `S3`, `POSTGRESQL` |
| `WAHA_SESSION_CONFIG_IGNORE_STATUS`, `_GROUPS`, `_CHANNELS`, `_BROADCAST` | false | default `config.ignore` for every session |
| `WAHA_PRESENCE_AUTO_ONLINE`, `WAHA_PRESENCE_AUTO_ONLINE_DURATION_SECONDS` | true, 90 | auto online presence (section 4) |
| `WHATSAPP_PROXY_SERVER`, `WHATSAPP_PROXY_SERVER_LIST`, `WHATSAPP_PROXY_SERVER_USERNAME`, `WHATSAPP_PROXY_SERVER_PASSWORD`, `WHATSAPP_PROXY_SERVER_INDEX_PREFIX` | | global proxy when the session has none |
| `WAHA_DASHBOARD_ENABLED`, `WAHA_DASHBOARD_USERNAME`, `WAHA_DASHBOARD_PASSWORD`, `WAHA_DASHBOARD_NO_PASSWORD` | true, `admin`, generated | dashboard |
| `WHATSAPP_SWAGGER_ENABLED`, `WHATSAPP_SWAGGER_USERNAME`, `WHATSAPP_SWAGGER_PASSWORD`, `WHATSAPP_SWAGGER_NO_PASSWORD`, `WHATSAPP_SWAGGER_TITLE`, `WHATSAPP_SWAGGER_DESCRIPTION`, `WHATSAPP_SWAGGER_EXTERNAL_DOC_URL`, `WHATSAPP_SWAGGER_CONFIG_ADVANCED` | true | Swagger UI at `/` |
| `WAHA_HTTPS_ENABLED`, `WAHA_HTTPS_PATH_KEY`, `WAHA_HTTPS_PATH_CERT`, `WAHA_HTTPS_PATH_CA` | false | native HTTPS (`src/nestjs/HttpsExpress.ts`) |
| `WAHA_LOCAL_STORE_BASE_DIR` | `./.sessions` | local session storage |
| `WHATSAPP_SESSIONS_MONGO_URL`, `WHATSAPP_SESSIONS_POSTGRESQL_URL` | | external session storage |
| `WAHA_NAMESPACE`, `WAHA_SESSION_NAMESPACE` | engine name | storage prefixes (`src/config.ts`) |
| `WAHA_CLIENT_DEVICE_NAME`, `WAHA_CLIENT_BROWSER_NAME` | null | default `config.client` (`src/core/env.ts`) |
| `WHATSAPP_HEALTH_MEDIA_FILES_THRESHOLD_MB`, `WHATSAPP_HEALTH_SESSION_FILES_THRESHOLD_MB`, `WHATSAPP_HEALTH_MONGO_TIMEOUT_MS` | 100, 100, 3000 | `/health` checks |
| `WAHA_PROMETHEUS_*` | | Prometheus module (`src/modules/waha-prometheus`) |

GOWS-internal (`WAHA_GOWS_PATH`, `WAHA_GOWS_SOCKET`) and app variables
(`WAHA_APPS_*`, Chatwoot) are not relevant to a compatible server.

---

## Not determined

- The exact JSON of `_data` in send responses and of the raw GOWS event
  fields: produced by the GOWS Go binary, which is not in this repository.
- `status@broadcast` ids rely on whatsmeow marking broadcast chats as
  `IsGroup`; inferred from whatsmeow, not verified in this code.
- Plus edition behavior (multi-worker orchestration, Plus-only storage): not
  public. Core in this commit already supports several sessions.
- The docs site (`waha.devlike.pro/docs`) was not consulted; everything here is
  from source.
