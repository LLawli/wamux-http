# Build stage: the Go toolchain and nothing else.
FROM golang:1.27.2-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/wamux-http ./cmd/wamux-http

# Final stage: the binary and ffmpeg (voice and video conversion), as a
# non-root user. The core's socket is mounted in and is 0660 for the core's
# group; uid and gid 10001 are the wamux image's own, so this user opens it.
FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ffmpeg ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && groupadd --gid 10001 wamux \
 && useradd --uid 10001 --gid 10001 --home /var/lib/wamux-http --create-home wamux-http
COPY --from=build /out/wamux-http /usr/local/bin/wamux-http
USER wamux-http
WORKDIR /var/lib/wamux-http
ENV WAMUX_HTTP_DATABASE_URL=sqlite:///var/lib/wamux-http/wamux-http.db \
    WHATSAPP_FILES_FOLDER=/var/lib/wamux-http/files \
    WAMUX_HTTP_CORE_SOCKET=/run/wamux/wamux.sock
EXPOSE 3000
ENTRYPOINT ["/usr/local/bin/wamux-http"]
