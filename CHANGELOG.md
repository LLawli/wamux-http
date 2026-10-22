# Changelog

All notable changes to this project are documented here. The format is based
on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] - 2026-10-22

### Added

- WAHA-compatible HTTP server on top of the wamux core (contract pinned to
  wamux v0.2.1 and WAHA 2026.8.2).
- Sessions: create, list, get, update, delete, start, stop, restart, logout,
  `me`, and the deprecated body-named forms. A session is a wamux account;
  its desired state survives restarts and it reconnects after a drop, except
  after a logout or a ban.
- Pairing by QR (PNG, base64 JSON or raw value) and by phone code.
- API keys in the WAHA Plus model: the admin key from `WAHA_API_KEY`
  (plain or `sha512:`), and session keys with `actions`. Keys are stored as
  hashes, so listings show them masked.
- A session key can cover several sessions: `/api/keys` takes and returns
  `sessions` next to WAHA's `session`.
- Container images on GHCR, both `FROM scratch` with one static binary and
  run as uid 10001: `ghcr.io/llawli/wamux-http:vX.Y.Z` (35 MB) and
  `vX.Y.Z-ffmpeg` (430 MB, with ffmpeg and ffprobe for voice and video
  conversion). A static Linux x86_64 tarball with every release; the tarball
  and both images carry the same binary, with provenance attestations.
- Reference `compose.yaml` running the core and this edge from GHCR (it
  replaces `deploy/docker-compose.yml`).
- `wamux-http --version`.
- CA roots compiled in, used when the host has none (a scratch container):
  https webhooks and media fetches verify there too.
- WebP images get thumbnails without ffmpeg.
- Web panel at `/dashboard`, unlocked with the admin key: pair sessions by QR
  or phone code with live status, start, stop, restart, log out and delete
  them, edit their webhooks and metadata and the account's name, about and
  picture, and create, deactivate and revoke API keys.
  `WAHA_DASHBOARD_ENABLED=false` turns it off.
- Webhooks with WAHA's envelope, headers, HMAC-SHA512 signature and retry
  policies, delivered from a durable outbox.
- Every WAHA operation not implemented yet answers WAHA's 501;
  `docs/coverage.md` lists them.
- Built to run several instances on one store: sessions are owned by lease.
- `/ws`: every event, in the webhook envelope, filtered by session and event.
- Incoming messages, acks, reactions, edits, revokes, poll votes, presence,
  group, call, label and archive events as WAHA webhooks, honoring
  `config.ignore`; history sync fills chats, messages and contacts without
  webhooks.
- Chats (list, overview, messages, picture, read, archive, unread, delete,
  edit), label reads, presence, and the deprecated `GET /api/messages`.
- Sends: text with link previews, image, file, voice, video, sticker,
  contact, poll and poll vote, buttons reply, reactions, stars, read
  receipts and typing. Sends answer a WAMessage carrying `_data`.
- Text, image and video statuses, and status delete.
- Voice and video conversion with ffmpeg, and `/api/files` serving received
  media, downloaded from the core on first request.
- Client URLs are fetched behind an SSRF guard
  (`WAMUX_HTTP_MEDIA_ALLOW_PRIVATE`, `WAMUX_HTTP_MEDIA_MAX_BYTES`,
  `WAMUX_HTTP_MEDIA_FETCH_TIMEOUT`).
- Contacts and LIDs, block and unblock, groups (create, list, participants,
  admins, invites, join requests, security settings, picture), profile
  (name, about, picture), channels (list, info, message preview, create,
  follow, unfollow, mute, unmute) and call rejection. Block, about, the group
  approval and add modes, the channel writes and call rejection need wamux
  v0.2.1; on v0.2.0 they answer 501.

[Unreleased]: https://github.com/LLawli/wamux-http/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/LLawli/wamux-http/releases/tag/v0.1.0
