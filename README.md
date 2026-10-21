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
to that socket.

```sh
go install github.com/LLawli/wamux-http/cmd/wamux-http@latest
WAHA_API_KEY=change-me WAMUX_HTTP_CORE_SOCKET=$HOME/.local/state/wamux/wamux.sock wamux-http
```

Or with Docker, mounting the core's socket directory:

```sh
docker build -t wamux-http .
docker run -p 3000:3000 -e WAHA_API_KEY=change-me \
  -v /run/wamux:/run/wamux -v wamux-http:/var/lib/wamux-http wamux-http
```

The container user must be able to open the socket (the core makes it `0660`
for its group).

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

`deploy/docker-compose.yml` runs the core and this server side by side,
sharing the core's socket through a volume. `bin/deploy` builds and pushes
the image and rolls the compose project on a host over ssh; it reads
`config/deploy.env` (copy `config/deploy.env.example`).

## Develop

`./scripts/ci.sh` runs every gate (format, generated code, vet, lint, tests
with the race detector, coverage document, vulnerabilities); CI runs the same
script. Tests run against an in-process fake of the core.
`DASHBOARD_DEV=127.0.0.1:3199 go test ./internal/dashboard -run TestDevServer -v -timeout 0`
runs the panel on that fake, with URLs that play the phone (scan the QR, log
out).
`WAMUX_BIN=/path/to/wamux scripts/live-smoke.sh` checks the server against a
real wamux daemon with a throwaway store, up to a QR code from WhatsApp (no
phone pairs).
