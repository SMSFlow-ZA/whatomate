# Dependency and runtime security

The managed image retains upstream `v0.1.1-beta-0.17` application behavior with
the fork's documented changes. Security dependency updates do not require an
unrelated upstream application upgrade.

## Go dependencies

The module requires Go 1.26. The managed Dockerfile pins Go 1.26.9 by image digest;
use that patched compiler for release builds, rather than only the minimum module
version. See the [Go release history](https://go.dev/doc/devel/release).

The 15 October 2026 review updated `golang.org/x/crypto` to 0.57.0 and
`golang.org/x/net` to 0.60.0, with their required transitive versions. The official
Go vulnerability analyzer additionally identified these updates:

| Dependency | Fixed version used | Advisory and exposure |
| --- | --- | --- |
| `github.com/pion/dtls/v3` | 3.1.4 | [GO-2026-6165](https://pkg.go.dev/vuln/GO-2026-6165), reachable from calling |
| `github.com/pion/stun/v3` | 3.1.5 | [GO-2026-6163](https://pkg.go.dev/vuln/GO-2026-6163), reachable from calling |
| `github.com/redis/go-redis/v9` | 9.6.3 | [GO-2025-3540](https://pkg.go.dev/vuln/GO-2025-3540), reachable during Redis connection setup |
| `github.com/valyala/fasthttp` | 1.70.0 | [GO-2026-4950](https://pkg.go.dev/vuln/GO-2026-4950), imported HTTP server package; patched defensively despite no vulnerable symbol trace |
| `github.com/klauspost/compress` | 1.18.7 | [GO-2026-5841](https://pkg.go.dev/vuln/GO-2026-5841), affected `s2` package was not imported |

`govulncheck` 1.9.0 with Go 1.26.9 reported zero reachable or imported-package
vulnerabilities in this resolved graph. It still reports
[GO-2026-5932](https://pkg.go.dev/vuln/GO-2026-5932) for the unused
`golang.org/x/crypto/openpgp` package at module scope; no fixed version exists and
the application does not import it. This is a dated scan result, not a guarantee
against future findings. Recheck source and the built image before each release.

```sh
go mod verify
govulncheck -show verbose ./...
go test -p 1 ./...
```

Supply disposable PostgreSQL and Redis using `TEST_DATABASE_URL` and
`TEST_REDIS_URL`; otherwise integration tests may skip.

## Frontend build dependencies

The existing frontend lockfile is retained. `npm audit --omit=dev` on 15 October
reported six package entries (five high, one moderate) in the Tailwind 3 build
chain, including `braces`, `chokidar`, `fast-glob`, `micromatch` and
`postcss-selector-parser`. The
[braces advisory](https://github.com/advisories/GHSA-vfj7-8cjw-p6xm) has no patched
version; the
[selector parser advisory](https://github.com/advisories/GHSA-rj75-hqrm-r3gf) is
fixed in 7.1.6. These parsers process trusted repository build input. They are
not used to compile customer CSS or patterns in the deployed application.
The final image contains the compiled static frontend embedded in the Go binary,
without the Node build stage or its `node_modules`. The dependency declaration
through the Tailwind plugin explains why audit includes build tools even when
development dependencies are omitted.

The reviewed disposition is build-time exposure: keep build input trusted and
builds isolated, and revisit the chain when compatible patches are available.
Changing the Tailwind major version requires a separate frontend compatibility
review. This disposition does not describe the npm audit as clean.

## Runtime packages and audio

The upstream Piper asset stage is pinned to
`sha256:8091fd13661bc51d5e14e7fbf25a4792e781b85e10b715172bf22aa87ed1e669`.
The managed runtime uses official Debian 13 slim pinned to
`sha256:a29215f6a35e51e22adffa17f89e9d2ef06214e64a2bad10d765c46aea49f11f`.
It installs distribution Opus tools, eSpeak, certificates and timezone data and
retains only the upstream Piper executable, native libraries, speech data and voice
model. The upstream application and old OS packages are not copied into the final
image. Available Debian 12 updates left substantial unpatched codec findings, so
the runtime moved to Debian 13 while preserving audio functionality. Debian 13's
general FFmpeg package still carried parser findings; the final image instead uses
an FFmpeg build limited to the application's audio contract.
Package repositories change over time: refresh the update layer using
`docker build --no-cache`, then record and deploy the resulting image digest with
its exact corresponding source.

Distribution security updates do not imply that every scanner finding is fixed.
Native codec packages can remain affected while the distribution has no patch.
Audio conversion accepts application input, so affected parsers must not be
classified as unreachable solely because Node is absent from the final image or
because the Go scan passes. The non-root runtime, bounded conversion times/output,
instance storage quotas, bounded temporary storage and host resource limits reduce
impact; they do not prove a vulnerable codec safe. Review remaining image findings
before rollout and track vendor updates. Piper libraries and models copied by the
upstream image also require upstream provenance review beyond the package scan.

The patched runtime was smoke-tested offline as user/group `10001:10001` with a
read-only root filesystem and a bounded temporary filesystem: Piper generated a
WAV, `opusenc` encoded it, and FFmpeg decoded and converted it to the application's
mono Opus format. This establishes preserved functionality, not malformed-media
security acceptance.

## FFmpeg source and supported audio

The media stage builds [official FFmpeg 9.0.2 source](https://ffmpeg.org/download.html).
The Dockerfile checks SHA-256
`8c3850283eb25fa026482078a04051e0be17347b09ef81a0849bec15a96e002e`
and the release signature against fingerprint
`FCF986EA15E6E293A5644F10B4322F04D67658D8`, published in the official release
verification instructions. The image includes the source archive, signature,
public key, license files and build configuration under
`/usr/share/doc/whatomate-media/`; `ffmpeg -version` also reports the configuration.

The build accepts the application's OGG/Opus/Vorbis, MP3, AAC, M4A/MP4 (AAC or
ALAC), WAV (enabled PCM and ADPCM variants), WebM audio and FLAC inputs. Outputs
are OGG/Opus and PCM WAV. The `loudnorm`, `volume`, `amix` and resampling filters
remain available for call audio mixing. Network protocols, devices, video and
subtitle codecs, and unrelated input demuxers are disabled. CAF, ADX/AAX and other
formats outside this audio contract are intentionally rejected, including when
uploaded with a generic MIME type. Synthetic acceptance covers each listed
container/codec family, mixing, Piper/Opus conversion and CAF/ADX rejection.

A custom binary may be invisible to a package scanner's version/CPE matching.
An image scan without FFmpeg findings is not proof that FFmpeg has no
vulnerabilities. Compare the pinned source and compiled components with the
[upstream security advisories](https://ffmpeg.org/security.html), retain the
build manifest, and review new parser findings before refreshing the image.

## Residual image findings, 15 October 2026

Trivy 0.76.0 scanned the saved candidate image offline after a separate database
download. It reported 187 OS package/advisory entries (84 unique advisories),
including 47 high entries and no critical entries. None had a distribution fixed
version available in that database. The Go binary report contained only the
module-level OpenPGP advisory described above. Repeated package entries are not
distinct vulnerabilities, and these counts are not a statement of exploitability.

The remaining high findings concern local utility libraries and tools (ncurses,
systemd-homed, ACL, util-linux, Perl archive handling), X11 libraries, and
`libsndfile`'s IMA reader. The server does not invoke mount/nsenter, Perl archive
handling, terminal interfaces, systemd-homed or an X server. Its `opusenc` use reads
only the PCM WAV generated by Piper; uploaded media goes through the separate
FFmpeg build. This limits the identified paths, while retaining the findings for
review if runtime use changes. Containers still require restricted privileges,
bounded temporary storage and host resource limits.

For the replaced FFmpeg path, Debian's tracker records 9.0.2 as fixed for the
[ADX/AAX decoder](https://security-tracker.debian.org/tracker/CVE-2026-64835),
[CAF/MACE decoder](https://security-tracker.debian.org/tracker/CVE-2026-66039),
[S/PDIF muxer](https://security-tracker.debian.org/tracker/CVE-2026-64833) and
[PNG encoder](https://security-tracker.debian.org/tracker/CVE-2026-66040)
findings seen in the distribution build. These components are additionally absent
from the configured application build. The retained upstream Piper/ONNX libraries
are not certified clean by this package scan; models remain immutable trusted
image assets, and their provenance and new advisories need separate review.
