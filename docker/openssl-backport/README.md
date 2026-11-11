This temporary Debian 13 OpenSSL backport builds from the authenticated Debian
source package and retains its existing distribution patches and package
layout. The managed patch recipe installs the three rebuilt runtime packages
together. The Debian package build runs the upstream test suite.

The November 10, 2026 [OpenSSL advisory](https://openssl-library.org/news/secadv/20261110.txt)
covers the five findings. The ordered revisions in this directory are from the
OpenSSL 3.5 branch:

| Advisory | Upstream revision |
| --- | --- |
| CVE-2026-88823 | 159e1de0c448 |
| CVE-2026-95692 | 257a3427aa0e |
| CVE-2026-88822 | 1a73dc590cba |
| CVE-2026-107496 | a0051cfe699b |
| CVE-2026-95691 | f6244675833e, with regression test bada6a2dd781 |

Remove this backport and refresh the managed runtime pin when Debian publishes
a package covering all five fixes. The managed release scanner still runs
against the final image.
