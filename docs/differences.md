# Where wamux-http differs from WAHA

The reference is WAHA's runtime behavior as read from its source
([waha-behavior.md](waha-behavior.md)), not only its OpenAPI document. Each
difference below is deliberate; anything else that differs is a bug.

## API keys are stored as hashes

WAHA stores `/api/keys` keys in plain text and `GET /api/keys` returns them in
full. Here only a SHA-256 of each key is stored:

- The plain key appears once, in the answer to `POST /api/keys`. Listings
  and `PUT` show it masked as `key_****<last 4>`.
- `POST /api/keys/media` and `POST /api/keys/control` "create or get" a key
  with a fixed id in WAHA. Here they rotate it: the old key stops working and
  the new one is returned.
- The media key gets `{"read": true}` and the control key `{"control": true}`:
  there is no narrower "files only" grant.

## A session key can cover several sessions

WAHA ties a session key to one session, in the `session` field. Here a key
covers a set of sessions, and `/api/keys` carries it in an extra `sessions`
field:

- A request may send `session`, `sessions` or both; the key covers their
  union. `PUT` replaces the set, as it replaces the whole key in WAHA.
- Answers always carry `sessions`, sorted. `session` holds the name only when
  the key covers exactly one session, and is `null` otherwise. A WAHA client
  reading a key that covers several sessions sees `session: null`, which in
  WAHA marks an admin key; `isAdmin` still tells them apart.
- Requests made with the key are unchanged: each names its session, which
  must be in the key's set. On `/ws`, a session key asking for every session
  gets the events of its own sessions.

## Webhooks are durable

WAHA sends each webhook fire-and-forget from memory, with no request timeout
and with TLS verification off. Here:

- Deliveries sit in an outbox in the store, so a restart does not lose them,
  and any instance can deliver them.
- One attempt times out after `WAMUX_HTTP_WEBHOOK_TIMEOUT` (30 s by default).
- TLS certificates are verified.

Headers, signature, body, retry counts and delays follow WAHA.

## Error bodies

- A 500 does not echo the request body back (WAHA's does, base64 media
  included); the rest of WAHA's 500 shape is kept.
- When the core relays WhatsApp's own error code, the error body carries it
  as `waCode` (and `waText`), next to WAHA's fields.

## Identity

- `engine` is `GOWS` everywhere: WAHA's webhook schema allows only its own
  engine names, and GOWS (whatsmeow) is the closest in shape to the
  `whatsapp-rust` library under wamux.
- `environment.tier` is `PLUS`, since several sessions and session keys are
  Plus features in WAHA.
- `_data` carries wamux's own message, never GOWS's. No client should rely on
  `_data` in any engine.

## Sends answer a full WAMessage

GOWS answers a send with `{id, _data}` only; WAHA's document and its other
engines answer a WAMessage. Here the answer is both: a WAMessage (`id`,
`timestamp`, `from`, `fromMe`, `source`, `to`, `participant`, `body`,
`hasMedia`, `ack`) with `_data` set to the core's own result. A client that
reads only `id` sees no difference. The core gives no send time, so
`timestamp` is the edge's clock.

## More than GOWS does

GOWS answers 501 for these (verified in its source); the core can do them,
so they work here:

- `PUT /api/star`, `POST /api/send/buttons/reply`, `POST /api/sendLinkPreview`.
- `GET /api/contacts/about`.
- Block and unblock a contact (answering an empty 201, as WEBJS does). The
  core needs the contact's LID<->phone pair to block: without it the answer
  is 422 and nothing is sent.
- Archive, unarchive and delete chat (answering `{"success": true}`).
- The events GOWS never emits: `chat.archive`, and the legacy
  `group.join` / `group.leave`.
- `message.revoked` carries the stored original message in `before` (GOWS
  always sends null), and the revoked message leaves the store.
- `label.chat.added` / `label.chat.deleted` carry the stored label instead
  of null.
- A `reply_to` whose message is in another chat quotes it there: a private
  reply to a group message, or a reply to a status. The chat comes from the
  stored message, else from a full WAHA id (`false_<chat>_<id>[_participant]`);
  GOWS always quotes in the request's chat. Needs wamux v0.2.2: an older core
  quotes in the request's chat.
- `me.reachoutTimelock`, and the `data` of `session.status` `WORKING`, follow
  the reachout restriction the core reports (wamux v0.2.2). The deadline is
  read as unix seconds or milliseconds, or an RFC 3339 time. `GET
  /api/sessions/{session}/timelock` still answers 501: WAHA queries WhatsApp
  there, and the core only relays the notification.

## Reads come from the store

Chats, messages, labels and presences are answered from the store even while
the session is stopped, where WAHA answers 422. Contacts too: a contact's
name is what the phone synced or the push name last seen.

## Stricter than WAHA

- `/api/files` needs `read` on the session in the path; WAHA serves its
  files to any valid key.
- Client URLs (media, link previews, pictures) are fetched behind an SSRF
  guard: loopback, private, link-local, multicast and reserved addresses
  are refused, after DNS resolution and on every redirect, and TLS is
  verified. A refused or failed fetch answers 422 (WAHA: 500).
  `WAMUX_HTTP_MEDIA_ALLOW_PRIVATE=true` turns the guard off.
- A missing `chatId` or `text`, or a malformed message id, answers 400
  (WAHA: 500).
- A missing picture answers `url: null` only when the core says the picture
  does not exist or is not visible; any other core failure is an error
  (WAHA swallows every error into null).

## Smaller differences

- `sendText` previews the first link unless `linkPreview` is false, as GOWS
  does (`?? true`): the server fetches that URL, behind the SSRF guard.
  `linkPreviewHighQuality` is ignored.
- `sendContactVcard` with several contacts sends one message per contact and
  answers with the last one.
- `sendSeen` with no ids also marks the chat read on the account's other
  devices.
- `reply_to` on `sendPoll` is ignored: the core's poll has no quote.
- A text status with no `backgroundColor` uses WAHA's documented `#38b42f`.
  With no `contacts`, a status goes to every `@c.us` contact in the store,
  and its delete reuses that list.
- Group answers use `@c.us` ids where GOWS shows whatsmeow's
  `@s.whatsapp.net`.
- A channel is looked up, followed and muted by its `@newsletter` id only
  (an invite code answers 501); `invite` is empty, and the preview's
  `viewCount` is 0 with no media downloaded.
- Creating a channel with a `picture` answers 501 and creates nothing: the
  core's create takes no picture.
- Quoted media in `replyTo` is never downloaded (`media: null`).
- The LID list holds only mappings that have both sides.

## Dashboard

WAHA's dashboard asks for a username and password
(`WAHA_DASHBOARD_USERNAME`, `WAHA_DASHBOARD_PASSWORD`) and then for an API
key. This one asks only for the admin key, kept in the browser tab's
`sessionStorage`; the username and password variables are not read.
`WAHA_DASHBOARD_ENABLED=false` still turns it off. It is a different panel,
not a copy of WAHA's: it covers sessions, pairing, webhooks, metadata, the
account's profile and API keys.

## Not available

Operations the core has no mechanism for answer WAHA's 501, as GOWS itself
does for several of them (delete group, for one). The list is
[coverage.md](coverage.md).

Voting in a channel poll (`POST /api/sendPollVote` with a `@newsletter`
chat) answers 501: the core votes there by the poll's server id with hashed
options, and this edge keeps neither.

Block and unblock, profile status, the group approval and add modes, the
channel writes and call rejection use RPCs added in wamux v0.2.1. Against a
v0.2.0 core they answer 501 too.
