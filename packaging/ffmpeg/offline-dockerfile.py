#!/usr/bin/env python3
"""Rewrites wader/static-ffmpeg's Dockerfile so it builds from a
corresponding-source bundle (collect-source.py) with no network.

Every RUN gets --network=none and the bundle mounted at /src, except the
`apk add` of Alpine's binary packages (their source is the bundle's
alpine/ part). Each download becomes a copy from the bundle, still checked
by the Dockerfile's own sha256sum; each clone becomes an extraction; what
component builds fetched themselves (libjxl's deps.sh, cargo crates, meson
wraps) is unpacked in place, with cargo pointed at the vendored crates.

If this Dockerfile builds, the bundle holds every source the build reads:
that is the check that it is complete. A final stage `verified` holds
ffmpeg, ffprobe and versions.json for comparison with the published image.

usage: offline-dockerfile.py DOCKERFILE BUNDLE > offline.Dockerfile
"""

import json
import os
import re
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from importlib import import_module  # noqa: E402

collect = import_module("collect-source")

# Where each clone lands in the Dockerfile: name -> directory.
CLONE_DIR = {
    "aom": "aom", "x264": "x264", "uavs3d": "uavs3d", "librtmp": "rtmpdump",
    "libgsm": "libgsm", "libgme": "game-music-emu", "libudfread": "contrib/libudfread",
}
CARGO_CONFIG = (
    r"""mkdir -p .cargo && printf '[source.crates-io]\nreplace-with = "vendored"\n"""
    r"""[source.vendored]\ndirectory = "cargo-vendor"\n' > .cargo/config.toml"""
)


def main():
    dockerfile_path, bundle = sys.argv[1:3]
    text = open(dockerfile_path).read()
    args = collect.parse_args(text)
    files = {e["file"] for e in json.load(open(os.path.join(bundle, "MANIFEST.json")))["files"]}

    def need(path):
        if path not in files:
            sys.exit(f"offline: the bundle has no {path}")
        return "/src/" + path

    # Downloads, and what each component fetched for itself.
    used_extra = set()
    for name, _url, out_file, _sha in collect.downloads(text, args):
        text, n = re.subn(
            r'wget \$WGET_OPTS -O ' + re.escape(out_file) + r' "\$[A-Z0-9_]+_URL"',
            f'cp {need(f"upstream/{name}--{out_file}")} {out_file}', text)
        assert n == 1, out_file
        extras = sorted(f for f in files if f.startswith(f"extra/{name}--") and "third_party" not in f)
        if not extras:
            continue
        steps = []
        for f in extras:
            used_extra.add(f)
            steps.append(f"tar -xzf /src/{f}")
            if f.endswith("--cargo-vendor.tar.gz"):
                steps.append(CARGO_CONFIG)
        top = f'"$(tar -tf {out_file} | head -n 1 | cut -d/ -f1)"'
        text, n = re.subn(
            r'(tar \$TAR_OPTS ' + re.escape(out_file) + r' && )',
            lambda m: m.group(1) + f"(cd {top} && " + " && ".join(steps) + ") && ", text)
        assert n == 1, f"no extraction of {out_file} to hook {extras} onto"

    # libjxl's deps.sh fetched its third_party sources.
    text, n = re.subn(r'\./deps\.sh', f'tar -xzf {need("extra/libjxl--third_party.tar.gz")}', text)
    assert n == 1
    used_extra.add("extra/libjxl--third_party.tar.gz")
    unused = {f for f in files if f.startswith("extra/")} - used_extra
    if unused:
        sys.exit(f"offline: bundle extras with no place in the build: {sorted(unused)}")

    # Clones.
    for name, (url, ref) in collect.GIT.items():
        commit = args[ref]
        tar = need(f"git/{name}-{commit[:12]}.tar.gz")
        dest = CLONE_DIR[name]
        # Straight into place: a release tarball can hold the destination
        # already, empty (libbluray's contrib/libudfread submodule).
        extract = f"mkdir -p {dest} && tar -xzf {tar} -C {dest} --strip-components=1"
        if url.endswith("_URL"):
            pattern = r'git clone (--depth 1 --branch \S+ )?"\$' + url + '"'
        else:
            pattern = r'git clone ' + re.escape(url) + r' ' + re.escape(dest)
        text, n = re.subn(pattern, extract, text)
        assert n == 1, name
    text, n = re.subn(r'git checkout --recurse-submodules \$[A-Z0-9_]+', 'true', text)
    assert n == 6, n
    text, n = re.subn(r'test \$\(git rev-parse HEAD\) = \$AOM_COMMIT', 'true', text)
    assert n == 1

    # No network, the bundle mounted, except for Alpine's packages.
    lines = []
    for line in text.splitlines():
        if line.startswith("RUN ") and "apk add" not in line:
            line = "RUN --network=none --mount=type=bind,from=src,target=/src " + line[4:]
        lines.append(line)
    text = "\n".join(lines) + "\n"
    without_apk = re.sub(r'RUN apk add(?:[^\n]*\\\n)*[^\n]*', '', text)
    leftover = re.findall(r'(wget |curl |git clone|deps\.sh)', without_apk)
    if leftover:
        sys.exit(f"offline: still fetching after the rewrite: {leftover}")

    text = "# syntax=docker/dockerfile:1\n" + text + (
        "\nFROM scratch AS verified\n"
        "COPY --from=builder /usr/local/bin/ffmpeg /usr/local/bin/ffprobe /versions.json /\n")
    sys.stdout.write(text)


if __name__ == "__main__":
    main()
