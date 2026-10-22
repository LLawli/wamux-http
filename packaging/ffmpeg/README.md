# ffmpeg in the -ffmpeg image

The `ffmpeg` target of the `Dockerfile` copies `ffmpeg` and `ffprobe` from
`mwader/static-ffmpeg` (pinned by digest), built by
[wader/static-ffmpeg](https://github.com/wader/static-ffmpeg). They are
GPL-3.0-or-later and link statically 61 components plus the Alpine static
libraries the build installs. Distributing them in our image obliges us to:

- ship the GPL text and the license notices of every component with them:
  `doc/` here, copied to `/usr/local/share/doc/ffmpeg` in the image;
- make their complete corresponding source available, for as long as images
  carrying them are distributed: a release of this repository named
  `static-ffmpeg-<version>-source`, whose address `doc/SOURCE.md` gives.

Our own binary runs ffmpeg as a separate program; it stays MIT OR
Apache-2.0.

## Current pin

| | |
|---|---|
| image | `mwader/static-ffmpeg:9.0.2@sha256:7d9bdaaf887f7e6ce6151f67325c344074b5ff1fb75316011c3376503e449a7b` |
| build scripts | wader/static-ffmpeg `dfa84ce4b2c705c104d87927fc1f74c822725c9d` (tag 9.0.2) |
| Alpine packages | aports 3.20-stable at `a410d2306e5f169444986d3329f154f2a9f7f12e`, the branch as it stood when the image was built |
| source release | `static-ffmpeg-9.0.2-source` |

The commit is the one whose Dockerfile pins the versions the image reports
in its `/versions.json` (all 45 pinned there match); the Alpine packages'
versions match the 11 the image reports.

## Changing the pin

1. Pick the image and its wader/static-ffmpeg commit; set `COMMIT` and
   `ALPINE_APORTS` in `collect-source.py` (aports `3.N-stable`, its last
   commit before the image was built).
2. Collect the source, on the data disk (about a GB):

   ```sh
   python3 collect-source.py /mnt/dados/caches/static-ffmpeg-source/out
   ```

   It needs git, curl, cargo and meson (the run for 9.0.2 used
   `rust:1.98-bookworm` with meson added). It fails on a download it does not
   recognize or one without a pinned digest.
3. Prove it complete: rebuild with no network from it.

   ```sh
   python3 offline-dockerfile.py CONTEXT/Dockerfile OUT > offline.Dockerfile
   docker buildx build --build-context src=OUT -f offline.Dockerfile \
     --target verified -o type=local,dest=result CONTEXT
   ```

   `CONTEXT` is the bundle's `build/` tarball unpacked. Every `RUN` but the
   `apk add` has `--network=none`; the Dockerfile's own `sha256sum -c` checks
   still run on the copies. Compare `result/versions.json` with the image's.
4. Regenerate `doc/`:

   ```sh
   python3 extract-licenses.py OUT VERSIONS_JSON \
     https://github.com/LLawli/wamux-http/releases/tag/static-ffmpeg-VERSION-source doc
   ```

   `VERSIONS_JSON` is `/versions.json` from the image.
5. Push a signed tag `static-ffmpeg-VERSION-source`, then
   `./publish-source.sh OUT static-ffmpeg-VERSION-source`.
6. Update the digest in the `Dockerfile` and the table above, in the same
   change as `doc/`.

Keep every source release while any published image carries its binaries.
Issue #1 measures replacing this build with a minimal one of our own.
