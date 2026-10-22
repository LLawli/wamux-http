# syntax=docker/dockerfile:1
#
# Two images, both FROM scratch, both running the same static binary as uid
# 10001 (the wamux image's own uid, so the core's 0660 socket opens):
#
#   docker build .                  the binary alone
#   docker build --target ffmpeg .  plus static ffmpeg and ffprobe, for voice
#                                   and video conversion, waveforms and
#                                   durations
#
# BINARY picks where the binary comes from. "build" (the default) compiles
# it here. The release passes BINARY=prebuilt with dist/wamux-http, the very
# file it put in the tarball, so the image compiles nothing and carries the
# same bytes.
ARG BINARY=build

FROM golang:1.27.3-bookworm@sha256:972b4110ba0f47539a975c31e52c64765e1ff19f2d74469ae4a595c6676e80a5 AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags="-s -w -X github.com/LLawli/wamux-http/internal/config.Release=${VERSION}" \
      -o /out/wamux-http ./cmd/wamux-http

FROM scratch AS prebuilt
COPY dist/wamux-http /out/wamux-http

FROM ${BINARY} AS binary

# GPL-3.0 build of ffmpeg 9.0.2 with libopus and libx264, statically linked.
# Source and build scripts: https://github.com/wader/static-ffmpeg
FROM mwader/static-ffmpeg:9.0.2@sha256:7d9bdaaf887f7e6ce6151f67325c344074b5ff1fb75316011c3376503e449a7b AS static-ffmpeg

FROM scratch AS ffmpeg
# scratch has no shell to create the state and temp directories, so they
# come from the context, owned by the user that writes them.
COPY --chown=10001:10001 packaging/rootfs/ /
COPY --from=binary /out/wamux-http /usr/local/bin/wamux-http
COPY --from=static-ffmpeg /ffmpeg /ffprobe /usr/local/bin/
ENV PATH=/usr/local/bin \
    TMPDIR=/tmp \
    WAMUX_HTTP_DATABASE_URL=sqlite:///var/lib/wamux-http/wamux-http.db \
    WHATSAPP_FILES_FOLDER=/var/lib/wamux-http/files \
    WAMUX_HTTP_CORE_SOCKET=/run/wamux/wamux.sock
WORKDIR /var/lib/wamux-http
USER 10001:10001
EXPOSE 3000
ENTRYPOINT ["/usr/local/bin/wamux-http"]

FROM scratch AS default
COPY --chown=10001:10001 packaging/rootfs/ /
COPY --from=binary /out/wamux-http /usr/local/bin/wamux-http
ENV PATH=/usr/local/bin \
    TMPDIR=/tmp \
    WAMUX_HTTP_DATABASE_URL=sqlite:///var/lib/wamux-http/wamux-http.db \
    WHATSAPP_FILES_FOLDER=/var/lib/wamux-http/files \
    WAMUX_HTTP_CORE_SOCKET=/run/wamux/wamux.sock
WORKDIR /var/lib/wamux-http
USER 10001:10001
EXPOSE 3000
ENTRYPOINT ["/usr/local/bin/wamux-http"]
