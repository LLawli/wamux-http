# ffmpeg and ffprobe: source

`/usr/local/bin/ffmpeg` and `/usr/local/bin/ffprobe` are ffmpeg 9.0.2, built
by wader/static-ffmpeg (commit dfa84ce4b2c705c104d87927fc1f74c822725c9d) and
copied unchanged from its image `mwader/static-ffmpeg:9.0.2`. They are
licensed under the GNU General Public License, version 3 or later
(`COPYING.GPLv3`), and link statically the components whose licenses are in
`LICENSES/`, with their versions in `versions.json`.

Their complete corresponding source (the build scripts, every component's
source at the version built, what those builds fetch on their own, and the
Alpine packages linked, at aports a410d2306e5f169444986d3329f154f2a9f7f12e) is published
at:

https://github.com/LLawli/wamux-http/releases/tag/static-ffmpeg-9.0.2-source

for as long as images carrying these binaries are distributed. Its
`MANIFEST.json` lists every file with its origin and sha256.
