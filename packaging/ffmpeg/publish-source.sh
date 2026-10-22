#!/usr/bin/env bash
# Publishes the corresponding-source bundle as the assets of a GitHub
# release of this repository, where it stays for as long as images with
# these ffmpeg binaries are distributed.
#
# usage: packaging/ffmpeg/publish-source.sh BUNDLE TAG
#   BUNDLE  the directory collect-source.py filled
#   TAG     the release tag, e.g. static-ffmpeg-9.0.2-source; the tag must
#           already be pushed (signed, like every tag of this repository)
set -euo pipefail

bundle=${1:?usage: publish-source.sh BUNDLE TAG}
tag=${2:?usage: publish-source.sh BUNDLE TAG}
repo=LLawli/wamux-http
# Next to the bundle: close to a GB, too much for a tmpfs /tmp.
stage=$(mktemp -d "$(dirname "$(realpath "$bundle")")/publish-XXXXXX")
trap 'rm -rf "$stage"' EXIT

# One tar per part: each stays under GitHub's 2 GiB asset limit, and the
# files inside are already compressed.
for part in build upstream git extra alpine; do
  tar -cf "$stage/$tag-$part.tar" -C "$bundle" "$part"
done
cp "$bundle/MANIFEST.json" "$stage/$tag-MANIFEST.json"
(cd "$stage" && sha256sum ./*.tar ./*.json > "$tag-SHA256SUMS")

notes=$(cat <<EOF
Corresponding source of the \`ffmpeg\` and \`ffprobe\` binaries in the
\`ghcr.io/llawli/wamux-http:*-ffmpeg\` images: ffmpeg 9.0.2 as built by
[wader/static-ffmpeg](https://github.com/wader/static-ffmpeg) (image
\`mwader/static-ffmpeg:9.0.2\`), GPL-3.0-or-later.

| Asset | Holds |
|---|---|
| \`$tag-build.tar\` | the build scripts (wader/static-ffmpeg at the commit the image was built from) |
| \`$tag-upstream.tar\` | every source tarball the build downloads, as pinned by sha256 |
| \`$tag-git.tar\` | every repository it clones, at the pinned commit, with submodules |
| \`$tag-extra.tar\` | what component builds fetch themselves: libjxl's third_party, cargo crates, meson wraps |
| \`$tag-alpine.tar\` | the Alpine 3.20 packages linked statically: APKBUILDs, patches and source files |
| \`$tag-MANIFEST.json\` | every file, its origin and sha256 |

The bundle is complete: the build was run again from it with no network
(packaging/ffmpeg in this repository has the scripts that collect it, rebuild
from it and extract the license texts the image ships).
EOF
)

if gh release view "$tag" -R "$repo" >/dev/null 2>&1; then
  gh release upload "$tag" -R "$repo" --clobber "$stage"/*
else
  # --latest=false: this is not a wamux-http release and must not become the
  # repository's latest.
  gh release create "$tag" -R "$repo" --verify-tag --latest=false --title "Source of the static ffmpeg ($tag)" \
    --notes "$notes" "$stage"/*
fi
