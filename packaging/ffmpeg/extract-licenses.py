#!/usr/bin/env python3
"""Builds packaging/ffmpeg/doc, which the -ffmpeg image ships in
/usr/local/share/doc/ffmpeg, from the corresponding-source bundle that
collect-source.py made:

  COPYING.GPLv3    the license ffmpeg and ffprobe are distributed under
  LICENSES/        the license files of every component they link (each
                   upstream source, each cloned repository, each Rust crate
                   and each Alpine package), as the components ship them
  SOURCE.md        where the corresponding source is, and its digests
  versions.json    the component versions, from the image

usage: extract-licenses.py BUNDLE VERSIONS_JSON SOURCE_URL DOC_DIR
"""

import io
import json
import os
import re
import shutil
import sys
import tarfile

LICENSE_NAME = re.compile(r"^(copying|licen[cs]e|copyright|notice|unlicense|patents)([._-].*)?$", re.I)


def is_license(name):
    # The REUSE layout keeps each license text in LICENSES/<SPDX id>.txt.
    return bool(LICENSE_NAME.match(os.path.basename(name))) or "/LICENSES/" in "/" + name


def depth(name):
    return name.strip("/").count("/")


def from_tar(path, dest, max_depth):
    """Copies license files at most max_depth directories below the
    archive's top directory; returns how many."""
    n = 0
    with tarfile.open(path) as t:
        for m in t:
            reuse = "/LICENSES/" in "/" + m.name
            if not m.isfile() or not is_license(m.name) or depth(m.name) > max_depth + reuse:
                continue
            if m.size > 512 << 10:
                continue  # not a license text
            target = os.path.join(dest, m.name.lstrip("./"))
            os.makedirs(os.path.dirname(target), exist_ok=True)
            with t.extractfile(m) as src, open(target, "wb") as f:
                shutil.copyfileobj(src, f)
            n += 1
    return n


def crates_index(path, dest):
    """Lists every vendored crate with the license its Cargo.toml declares:
    some crates ship no license file, only that declaration."""
    rows = []
    with tarfile.open(path) as t:
        for m in t:
            if m.isfile() and m.name.count("/") == 2 and m.name.endswith("/Cargo.toml"):
                toml = t.extractfile(m).read().decode(errors="replace")

                def field(key):
                    hit = re.search(r'^' + key + r'\s*=\s*"([^"]*)"', toml, re.M)
                    return hit.group(1) if hit else ""

                rows.append((m.name.split("/")[1], field("license") or field("license-file") or "unknown", field("repository")))
    with open(dest, "w") as out:
        out.write("# Vendored Rust crates\n\nCrate | License (Cargo.toml) | Repository\n---|---|---\n")
        for crate, license_, repo in sorted(rows):
            out.write(f"{crate} | {license_} | {repo}\n")


def main():
    if len(sys.argv) != 5:
        sys.exit(__doc__)
    bundle, versions, source_url, doc = sys.argv[1:]
    manifest = json.load(open(os.path.join(bundle, "MANIFEST.json")))
    if os.path.isdir(doc):
        shutil.rmtree(doc)
    lic = os.path.join(doc, "LICENSES")
    os.makedirs(lic)

    missing = []
    for e in manifest["files"]:
        f = e["file"]
        path = os.path.join(bundle, f)
        comp = re.sub(r"[^A-Za-z0-9._+-]", "_", e["component"])
        if f.startswith(("upstream/", "git/", "build/")):
            n = from_tar(path, os.path.join(lic, comp), 1)
        elif f.startswith("extra/"):
            # cargo vendor and meson subprojects: one level per crate or
            # subproject; deps.sh's third_party, one level per dependency.
            n = from_tar(path, os.path.join(lic, comp, "bundled"), 2)
            if f.endswith("--cargo-vendor.tar.gz"):
                crates_index(path, os.path.join(lic, comp, "bundled", "CRATES.md"))
        elif f.startswith("alpine/aports/"):
            with tarfile.open(path) as t:
                apkbuild = next(m for m in t if m.name.endswith("/APKBUILD"))
                text = t.extractfile(apkbuild).read().decode()
            spdx = re.search(r'^license="([^"]*)"', text, re.M)
            target = os.path.join(lic, comp, "APKBUILD-license")
            os.makedirs(os.path.dirname(target), exist_ok=True)
            with open(target, "w") as out:
                out.write((spdx.group(1) if spdx else "unknown") + "\n")
            n = 1
        elif f.startswith("alpine/distfiles/") and re.search(r"\.(tar(\.\w+)?|tgz|tbz2|txz)$", f):
            try:
                n = from_tar(path, os.path.join(lic, comp), 1)
            except tarfile.ReadError:
                n = 0
        else:
            continue
        if n == 0 and not f.startswith("alpine/distfiles/"):
            missing.append(f)

    ffmpeg = next(e for e in manifest["files"] if e["component"] == "ffmpeg")
    with tarfile.open(os.path.join(bundle, ffmpeg["file"])) as t:
        gpl = next(m for m in t if m.name.endswith("/COPYING.GPLv3"))
        with open(os.path.join(doc, "COPYING.GPLv3"), "wb") as out:
            out.write(t.extractfile(gpl).read())
    shutil.copy(versions, os.path.join(doc, "versions.json"))

    with open(os.path.join(doc, "SOURCE.md"), "w") as out:
        out.write(f"""# ffmpeg and ffprobe: source

`/usr/local/bin/ffmpeg` and `/usr/local/bin/ffprobe` are ffmpeg 9.0.2, built
by wader/static-ffmpeg (commit {manifest['static_ffmpeg_commit']}) and
copied unchanged from its image `mwader/static-ffmpeg:9.0.2`. They are
licensed under the GNU General Public License, version 3 or later
(`COPYING.GPLv3`), and link statically the components whose licenses are in
`LICENSES/`, with their versions in `versions.json`.

Their complete corresponding source (the build scripts, every component's
source at the version built, what those builds fetch on their own, and the
Alpine packages linked, at aports {manifest['alpine_aports']}) is published
at:

{source_url}

for as long as images carrying these binaries are distributed. Its
`MANIFEST.json` lists every file with its origin and sha256.
""")
    if missing:
        print("no license file found in:", *missing, sep="\n  ", file=sys.stderr)
    print(f"wrote {doc}")


if __name__ == "__main__":
    main()
