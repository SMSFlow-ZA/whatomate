#!/bin/sh
set -eu

base='3.5.7-1~deb13u3'
version="${base}+smsflow1"
export DEBIAN_FRONTEND=noninteractive
export DEB_BUILD_OPTIONS="parallel=4 terse" DEB_BUILD_PROFILES=noudeb

sed -i 's/^Types: deb$/Types: deb deb-src/' /etc/apt/sources.list.d/debian.sources
apt-get update
apt-get install -y --no-install-recommends build-essential ca-certificates git dpkg-dev debhelper libzstd-dev zlib1g-dev
mkdir -p /build /packages
cd /build
apt-get source "openssl=$base"
cd openssl-3.5.7
while read -r revision; do
    git apply --check "/backport/$revision.patch"
    git apply "/backport/$revision.patch"
    cp "/backport/$revision.patch" debian/patches/
    printf '%s.patch\n' "$revision" >> debian/patches/series
done < /backport/revisions
{
    printf 'openssl (%s) trixie; urgency=high\n\n' "$version"
    printf '  * Backport five upstream November 2026 security fixes.\n\n'
    printf ' -- SMSFlow Packaging <packaging@smsflow.co.za>  Wed, 11 Nov 2026 00:00:00 +0000\n\n'
    cat debian/changelog
} > /tmp/openssl-changelog
cp /tmp/openssl-changelog debian/changelog
dpkg-buildpackage -b -us -uc -nc
arch=$(dpkg --print-architecture)
for package in libssl3t64 openssl openssl-provider-legacy; do
    cp "../${package}_${version}_${arch}.deb" /packages/
done
