#!/usr/bin/env python3
"""Collects the GPL-3.0 corresponding source of the ffmpeg and ffprobe that
the -ffmpeg image copies from mwader/static-ffmpeg.

That build is wader/static-ffmpeg's Dockerfile at COMMIT. Its binaries link,
statically, every component the Dockerfile fetches and the Alpine static
libraries its builder installs. This collects, into OUT:

  build/             wader/static-ffmpeg at COMMIT (the build scripts)
  upstream/          every tarball the Dockerfile downloads, checked against
                     the sha256 the Dockerfile pins
  git/               every repository it clones, at the pinned commit, with
                     submodules (a tar of the worktree)
  extra/             what component builds fetch themselves: libjxl's
                     deps.sh, cargo crates (cargo vendor), meson wraps
  alpine/aports/     the APKBUILD and patches of each Alpine package whose
                     static library the build links, at ALPINE_APORTS
  alpine/distfiles/  those packages' source files, checked against their
                     APKBUILD's sha512sums
  MANIFEST.json      every file above with its origin and digest

It fails on anything it does not recognize in the Dockerfile (a new way of
downloading, a download without a pinned digest), so a bump of the pinned
image cannot leave a component out silently.

usage: collect-source.py OUT
"""

import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
import urllib.request

COMMIT = "dfa84ce4b2c705c104d87927fc1f74c822725c9d"  # tag 9.0.2
WADER = "https://github.com/wader/static-ffmpeg"
# aports 3.20-stable as it stood when the image was built (2026-09-20):
# its last commit before that date.
ALPINE_APORTS = "a410d2306e5f169444986d3329f154f2a9f7f12e"
ALPINE_DISTFILES = "https://distfiles.alpinelinux.org/distfiles/v3.20"
# The Alpine packages the builder installs whose libraries end up in the
# binaries (the -static and -dev library packages of its `apk add`, and
# musl). Tools (compilers, cmake, git, ...) are not part of the binaries;
# libgcc, libstdc++ and Rust's standard library come with the compilers and
# are System Libraries in GPL-3.0's sense.
ALPINE_PACKAGES = [
    "brotli", "bzip2", "expat", "fftw", "fontconfig", "freetype", "fribidi",
    "giflib", "graphite2", "libdrm", "libjpeg-turbo", "libpng",
    "libsamplerate", "libxml2", "musl", "numactl", "openssl", "snappy",
    "soxr", "tiff", "vo-amrwbenc", "xz", "zlib",
]
# The repositories the Dockerfile clones: name -> (URL or ARG, ref ARG).
GIT = {
    "aom": ("AOM_URL", "AOM_COMMIT"),
    "x264": ("X264_URL", "X264_VERSION"),
    "uavs3d": ("UAVS3D_URL", "UAVS3D_COMMIT"),
    "librtmp": ("LIBRTMP_URL", "LIBRTMP_COMMIT"),
    "libgsm": ("LIBGSM_URL", "LIBGSM_COMMIT"),
    "libgme": ("LIBGME_URL", "LIBGME_COMMIT"),
    "libudfread": ("https://code.videolan.org/videolan/libudfread.git", "LIBUDFREAD_COMMIT"),
}

manifest = []


def run(*cmd, cwd=None, env=None):
    subprocess.run(cmd, cwd=cwd, env=env, check=True)


def sha(path, algo="sha256"):
    h = hashlib.new(algo)
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def fetch(url, dest):
    run("curl", "-fsSL", "--retry", "5", "--retry-all-errors", "-o", dest, url)


def record(path, out, **origin):
    manifest.append({"file": os.path.relpath(path, out), "sha256": sha(path), **origin})


def tar_dir(src, dest, arcname):
    with tarfile.open(dest, "w:gz") as t:
        t.add(src, arcname=arcname, filter=lambda i: None if ".git" in i.name.split("/") else i)


def parse_args(dockerfile):
    """The Dockerfile's ARG values, with $VAR and ${VAR} expanded in order."""
    args = {}
    for name, value in re.findall(r'^ARG ([A-Za-z0-9_]+)=(.*)$', dockerfile, re.M):
        value = value.strip()
        if len(value) >= 2 and value[0] == value[-1] == '"':
            value = value[1:-1]
        value = re.sub(r'\$\{?([A-Za-z0-9_]+)\}?', lambda m: args.get(m.group(1), m.group(0)), value)
        args[name] = value
    return args


def downloads(dockerfile, args):
    """(name, url, file, sha256) for each `wget ... -O file "$X_URL"`."""
    found = []
    for out_file, var in re.findall(r'wget \$WGET_OPTS -O (\S+) "\$([A-Z0-9_]+_URL)"', dockerfile):
        m = re.search(r'echo "\$([A-Z0-9_]+_SHA256)  ' + re.escape(out_file) + r'"', dockerfile)
        if not m:
            sys.exit(f"collect: {out_file} is downloaded without a pinned sha256")
        found.append((var[: -len("_URL")].lower(), args[var], out_file, args[m.group(1)]))
    # Every wget of a source must have been matched above.
    if len(re.findall(r'wget \$WGET_OPTS', dockerfile)) != len(found):
        sys.exit("collect: a wget in the Dockerfile has a shape this script does not know")
    return found


def main():
    if len(sys.argv) != 2:
        sys.exit(__doc__)
    out = os.path.abspath(sys.argv[1])
    for d in ("build", "upstream", "git", "extra", "alpine/aports", "alpine/distfiles"):
        os.makedirs(os.path.join(out, d), exist_ok=True)
    work = tempfile.mkdtemp(prefix="ffmpeg-src-", dir=os.environ.get("WORK_DIR"))

    # The build scripts.
    wader = os.path.join(work, "static-ffmpeg")
    run("git", "clone", "--quiet", WADER + ".git", wader)
    run("git", "checkout", "--quiet", COMMIT, cwd=wader)
    build_tar = os.path.join(out, "build", f"static-ffmpeg-{COMMIT[:12]}.tar.gz")
    run("git", "archive", "--format=tar.gz", f"--prefix=static-ffmpeg-{COMMIT[:12]}/", "-o", build_tar, COMMIT, cwd=wader)
    record(build_tar, out, component="static-ffmpeg build scripts", origin=f"{WADER}/tree/{COMMIT}")
    dockerfile = open(os.path.join(wader, "Dockerfile")).read()
    args = parse_args(dockerfile)

    # Tarballs.
    for name, url, out_file, digest in downloads(dockerfile, args):
        dest = os.path.join(out, "upstream", f"{name}--{out_file}")
        fetch(url, dest)
        if sha(dest) != digest:
            sys.exit(f"collect: {url} does not match the Dockerfile's sha256")
        record(dest, out, component=name, origin=url, pinned_sha256=digest)

    # Repositories.
    if len(re.findall(r'git clone', dockerfile)) != len(GIT):
        sys.exit("collect: the Dockerfile clones a repository this script does not know")
    for name, (url, ref) in GIT.items():
        url = args.get(url, url)
        commit = args[ref]
        repo = os.path.join(work, "git-" + name)
        run("git", "clone", "--quiet", url, repo)
        if name == "aom":  # cloned by tag; the Dockerfile checks the commit
            run("git", "checkout", "--quiet", f"v{args['AOM_VERSION']}", cwd=repo)
            got = subprocess.run(["git", "rev-parse", "HEAD"], cwd=repo, capture_output=True, text=True, check=True).stdout.strip()
            if got != commit:
                sys.exit(f"collect: aom v{args['AOM_VERSION']} is {got}, not {commit}")
        else:
            run("git", "checkout", "--quiet", "--recurse-submodules", commit, cwd=repo)
        run("git", "submodule", "update", "--quiet", "--init", "--recursive", cwd=repo)
        dest = os.path.join(out, "git", f"{name}-{commit[:12]}.tar.gz")
        tar_dir(repo, dest, f"{name}-{commit[:12]}")
        record(dest, out, component=name, origin=url, commit=commit)

    # What component builds fetch on their own.
    for entry in list(manifest):
        if not entry["file"].startswith("upstream/"):
            continue
        name = entry["component"]
        src = os.path.join(work, "x-" + name)
        os.makedirs(src)
        run("tar", "-xf", os.path.join(out, entry["file"]), "-C", src)
        tops = os.listdir(src)
        root = os.path.join(src, tops[0]) if len(tops) == 1 else src
        if name == "libjxl":
            run("./deps.sh", cwd=root)
            dest = os.path.join(out, "extra", "libjxl--third_party.tar.gz")
            tar_dir(os.path.join(root, "third_party"), dest, "third_party")
            record(dest, out, component="libjxl", origin="libjxl deps.sh (third_party, fetched at build time)")
        for dirpath, _, files in os.walk(root):
            if "Cargo.lock" in files and "/vendor" not in dirpath and "/target" not in dirpath:
                run("cargo", "vendor", "--locked", "--versioned-dirs", "--quiet", "cargo-vendor", cwd=dirpath)
                rel = os.path.relpath(dirpath, root).replace("/", "_")
                dest = os.path.join(out, "extra", f"{name}--{rel}--cargo-vendor.tar.gz")
                tar_dir(os.path.join(dirpath, "cargo-vendor"), dest, "cargo-vendor")
                record(dest, out, component=name, origin=f"cargo vendor --locked in {os.path.relpath(dirpath, root)}")
        sub = os.path.join(root, "subprojects")
        if os.path.isdir(sub) and any(f.endswith(".wrap") for f in os.listdir(sub)):
            run("meson", "subprojects", "download", cwd=root)
            dest = os.path.join(out, "extra", f"{name}--meson-subprojects.tar.gz")
            tar_dir(sub, dest, "subprojects")
            record(dest, out, component=name, origin="meson subprojects download (wraps a build may fall back to)")
        shutil.rmtree(src)

    # Alpine packages: APKBUILD, patches, and their source files.
    aports = os.path.join(work, "aports")
    os.makedirs(aports)
    for pkg in ALPINE_PACKAGES:
        for repo in ("main", "community"):
            url = f"https://gitlab.alpinelinux.org/alpine/aports/-/archive/{ALPINE_APORTS}/aports.tar.gz?path={repo}/{pkg}"
            tgz = os.path.join(aports, f"{pkg}.tar.gz")
            try:
                fetch(url, tgz)
            except subprocess.CalledProcessError:
                continue
            listing = subprocess.run(["tar", "-tzf", tgz], capture_output=True, text=True, check=True).stdout
            if f"/{repo}/{pkg}/APKBUILD" in listing:
                break
        else:
            sys.exit(f"collect: no APKBUILD for {pkg} at {ALPINE_APORTS}")
        dest = os.path.join(out, "alpine", "aports", f"{repo}--{pkg}.tar.gz")
        shutil.copy(tgz, dest)
        record(dest, out, component=f"alpine {pkg}", origin=f"aports {repo}/{pkg} at {ALPINE_APORTS}")
        run("tar", "-xzf", tgz, "-C", aports)
        pkgdir = next(os.path.join(dp, pkg) for dp, dirs, _ in os.walk(aports) if pkg in dirs and dp.endswith("/" + repo))
        # The APKBUILD names its sources and their sha512; sourcing it is how
        # abuild reads it too.
        listed = subprocess.run(
            ["bash", "-c", 'set -e; CARCH=x86_64; CTARGET_ARCH=x86_64; . ./APKBUILD; printf "%s\\n" $source; echo ---; printf "%s\\n" "$sha512sums"'],
            cwd=pkgdir, capture_output=True, text=True, check=True).stdout
        sources, sums = listed.split("---\n")
        sums = dict((line.split()[1], line.split()[0]) for line in sums.splitlines() if line.strip())
        for item in sources.split():
            fname, _, url = item.partition("::")
            if not url:
                url, fname = item, item.rsplit("/", 1)[-1]
            if "://" not in url:
                continue  # a patch or file in the aports directory, already kept
            dest = os.path.join(out, "alpine", "distfiles", fname)
            if not os.path.exists(dest):
                try:
                    fetch(f"{ALPINE_DISTFILES}/{fname}", dest)
                except subprocess.CalledProcessError:
                    fetch(url, dest)
            if fname in sums and sha(dest, "sha512") != sums[fname]:
                sys.exit(f"collect: {fname} does not match {pkg}'s APKBUILD sha512")
            record(dest, out, component=f"alpine {pkg}", origin=url, alpine_distfile=f"{ALPINE_DISTFILES}/{fname}")

    with open(os.path.join(out, "MANIFEST.json"), "w") as f:
        json.dump({"static_ffmpeg_commit": COMMIT, "alpine_aports": ALPINE_APORTS, "files": manifest}, f, indent=1)
    shutil.rmtree(work)
    print(f"collected {len(manifest)} files into {out}")


if __name__ == "__main__":
    main()
