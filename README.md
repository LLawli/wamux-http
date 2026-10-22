# wamux-http

An HTTP server that speaks the [WAHA](https://waha.devlike.pro/) API on top of
the [wamux](https://github.com/LLawli/wamux) WhatsApp core. If you have
software written against WAHA (n8n's WAHA nodes, scripts calling
`/api/sendText`, webhook receivers) and want to run it on wamux, point it here
instead and it keeps working.

> Unofficial: wamux talks to WhatsApp through an unofficial library. Using it
> may break WhatsApp's terms of service and get an account banned.

## Install

You need a running wamux daemon (its Unix socket) and this server with access
to that socket. The easiest way is both as containers, with the reference
[compose.yaml](compose.yaml): put a `.env` next to it with `WAHA_API_KEY`,
then

```sh
docker compose up -d
```

The image comes in two variants, both `FROM scratch` (one static binary, no
operating system), running as uid 10001 like the core so its socket opens:

| Image | Size | |
|---|---|---|
| `ghcr.io/llawli/wamux-http:v0.1.1` | 35 MB | the binary alone |
| `ghcr.io/llawli/wamux-http:v0.1.1-ffmpeg` | 440 MB | plus ffmpeg and ffprobe |

Without ffmpeg, `convert: true` on voice and video sends and the
`/media/convert` routes answer an error, and voice notes and videos go out
without waveform and duration (they still play). Pick the `-ffmpeg` variant
if you send voice or video; the reference compose does.

Without containers, take the static binary from the
[releases](https://github.com/LLawli/wamux-http/releases) (Linux x86_64, no
libc needed), or build it:

```sh
go install github.com/LLawli/wamux-http/cmd/wamux-http@latest
WAHA_API_KEY=change-me WAMUX_HTTP_CORE_SOCKET=$HOME/.local/state/wamux/wamux.sock wamux-http
```

Conversion then uses ffmpeg, ffprobe and opustags from the `PATH` when they
are installed.

## Use

It is WAHA's API. Create and start a session, scan the QR, send a message:

```sh
curl -X POST localhost:3000/api/sessions -H 'X-Api-Key: change-me' \
  -H 'Content-Type: application/json' -d '{"name":"default","start":true}'
curl localhost:3000/api/default/auth/qr -H 'X-Api-Key: change-me' -o qr.png
curl -X POST localhost:3000/api/sendText -H 'X-Api-Key: change-me' \
  -H 'Content-Type: application/json' \
  -d '{"session":"default","chatId":"5511999999999@c.us","text":"hi"}'
```

Or open the panel at `localhost:3000/dashboard`, unlock it with the admin
key, and pair, start and stop sessions, edit their webhooks and manage API
keys there (`WAHA_DASHBOARD_ENABLED=false` turns it off).

WAHA's environment variables are read under their WAHA names
(`WAHA_API_KEY`, `WHATSAPP_HOOK_URL`, `WAHA_BASE_URL`, `PORT`, ...). The
server's own settings:

| Variable | Default | |
|---|---|---|
| `WAMUX_HTTP_CORE_SOCKET` | `/run/wamux/wamux.sock` | the wamux daemon's socket |
| `WAMUX_HTTP_DATABASE_URL` | `sqlite://./wamux-http.db` | the store |
| `WAMUX_HTTP_LISTEN` | `:$PORT` | bind address |
| `WAMUX_HTTP_EVENT_RETENTION` | `24h` | how long the event log keeps events |
| `WAMUX_HTTP_WEBHOOK_TIMEOUT` | `30s` | one webhook attempt |
| `WAMUX_HTTP_MEDIA_ALLOW_PRIVATE` | `false` | let client URLs reach private addresses (trusted setups only) |
| `WAMUX_HTTP_MEDIA_MAX_BYTES` | 100 MiB | largest file fetched from a client URL |
| `WAMUX_HTTP_MEDIA_FETCH_TIMEOUT` | `60s` | one fetch of a client URL |

What is implemented and what answers 501: [docs/coverage.md](docs/coverage.md).
Where this server knowingly differs from WAHA: [docs/differences.md](docs/differences.md).
How it is built: [docs/architecture.md](docs/architecture.md).

## Deploy

`bin/deploy` builds the image from this checkout, pushes it to your registry
and rolls `compose.yaml` on a host over ssh, with `WAMUX_HTTP_IMAGE` pointing
at what it pushed; it reads `config/deploy.env` (copy
`config/deploy.env.example`). `docker build .` makes the scratch image and
`docker build --target ffmpeg .` the one with ffmpeg.

Releases: [docs/releasing.md](docs/releasing.md).

## Develop

`./scripts/ci.sh` runs every gate (format, generated code, workflow lint, vet, lint, tests
with the race detector, coverage document, vulnerabilities); CI runs the same
script. Tests run against an in-process fake of the core.
`DASHBOARD_DEV=127.0.0.1:3199 go test ./internal/dashboard -run TestDevServer -v -timeout 0`
runs the panel on that fake, with URLs that play the phone (scan the QR, log
out).
`WAMUX_BIN=/path/to/wamux scripts/live-smoke.sh` checks the server against a
real wamux daemon with a throwaway store, up to a QR code from WhatsApp (no
phone pairs).

## License

Licensed under either of

- Apache License, Version 2.0 ([LICENSE-APACHE](LICENSE-APACHE))
- MIT license ([LICENSE-MIT](LICENSE-MIT))

at your option.

The binary links Go modules under their own licenses (BSD, MIT, ISC,
Apache-2.0, MPL-2.0); [THIRD-PARTY-LICENSES.md](THIRD-PARTY-LICENSES.md) has
their texts and where each one's source is, and ships in the tarball and in
both images (`/usr/local/share/doc/wamux-http`). It also covers what the panel
embeds: [Datastar](https://data-star.dev) (MIT) and the IBM Plex fonts (SIL
Open Font License 1.1).

The `-ffmpeg` image also carries ffmpeg 9.0.2 and ffprobe, GPL-3.0-or-later,
as built by [wader/static-ffmpeg](https://github.com/wader/static-ffmpeg). The
image ships the GPL text and the license of every component they link
(`/usr/local/share/doc/ffmpeg`), and their complete corresponding source is
the [static-ffmpeg-9.0.2-source](https://github.com/LLawli/wamux-http/releases/tag/static-ffmpeg-9.0.2-source)
release; [packaging/ffmpeg](packaging/ffmpeg/README.md) has how it is
collected and checked.

Unless you explicitly state otherwise, any contribution intentionally submitted
for inclusion in the work by you, as defined in the Apache-2.0 license, shall be
dual licensed as above, without any additional terms or conditions.
