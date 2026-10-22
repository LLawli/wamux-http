# Releasing

```sh
bin/release X.Y.Z
```

It needs an empty working-copy commit and entries under `## [Unreleased]`.
It dates the changelog, points `compose.yaml` and the README at the new
image tag, runs `scripts/ci.sh`, commits, pushes `main` and then pushes a
signed tag `vX.Y.Z`. Every pushed commit and the tag ask for a YubiKey touch.
`--no-push` stops after the local commit, to read it first.

The tag runs `.github/workflows/release.yml`:

1. **release** runs the gates, takes the notes from the CHANGELOG section
   (`scripts/changelog-section.sh`, which fails on a missing or empty one),
   and builds the binary once (`scripts/release-build.sh`): `CGO_ENABLED=0`,
   the version stamped with `-ldflags`, and refused when it has an
   interpreter, needs a shared library, was built with cgo, or reports
   another version. It publishes `wamux-http-X.Y.Z-linux-amd64.tar.gz` and
   its `.sha256`, with a build provenance attestation.
2. **image** downloads that same binary, checks it against the tarball's,
   and builds both targets of the `Dockerfile` with `BINARY=prebuilt`, so the
   Go stage never runs: `ghcr.io/llawli/wamux-http:vX.Y.Z` (and `latest`)
   and `vX.Y.Z-ffmpeg` (and `latest-ffmpeg`). It pulls what it pushed, runs
   `--version`, compares the binary inside with the tarball's, and attests
   each image.

To publish an existing tag again, run the workflow by hand with the tag as
input; the release assets are replaced and the images pushed again.

## Checking a release

```sh
gh release download vX.Y.Z -R LLawli/wamux-http -p '*.tar.gz*'
sha256sum -c wamux-http-X.Y.Z-linux-amd64.tar.gz.sha256
gh attestation verify wamux-http-X.Y.Z-linux-amd64.tar.gz -R LLawli/wamux-http
gh attestation verify oci://ghcr.io/llawli/wamux-http:vX.Y.Z -R LLawli/wamux-http
docker run --rm ghcr.io/llawli/wamux-http:vX.Y.Z --version
```

A new GHCR package starts private: after the first release, make it public
in the package settings so `docker pull` works without logging in.
