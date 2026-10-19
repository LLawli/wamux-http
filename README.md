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

What is implemented and what answers 501: [docs/coverage.md](docs/coverage.md).
Where this server knowingly differs from WAHA: [docs/differences.md](docs/differences.md).
How it is built: [docs/architecture.md](docs/architecture.md).

## Develop

`./scripts/ci.sh` runs every gate (format, generated code, vet, lint, tests
with the race detector, coverage document, vulnerabilities); CI runs the same
script.
