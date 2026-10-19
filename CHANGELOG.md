# Changelog

All notable changes to this project are documented here. The format is based
on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- WAHA-compatible HTTP server on top of the wamux core (contract pinned to
  wamux v0.2.0 and WAHA 2026.8.2).
- Sessions: create, list, get, update, delete, start, stop, restart, logout,
  `me`, and the deprecated body-named forms. A session is a wamux account;
  its desired state survives restarts and it reconnects after a drop, except
  after a logout or a ban.
- Pairing by QR (PNG, base64 JSON or raw value) and by phone code.
- API keys in the WAHA Plus model: the admin key from `WAHA_API_KEY`
  (plain or `sha512:`), and session keys with `actions`. Keys are stored as
  hashes, so listings show them masked.
- Webhooks with WAHA's envelope, headers, HMAC-SHA512 signature and retry
  policies, delivered from a durable outbox.
- Every WAHA operation not implemented yet answers WAHA's 501;
  `docs/coverage.md` lists them.
- Built to run several instances on one store: sessions are owned by lease.
