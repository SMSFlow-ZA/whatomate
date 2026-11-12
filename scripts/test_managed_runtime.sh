#!/bin/sh
set -eu
if [ -f /etc/alpine-release ]; then
    for package in glibc gcompat libc6-compat; do
        if apk info -e "$package"; then
            echo "glibc compatibility package present: $package" >&2
            exit 1
        fi
    done
    test -z "$(find /lib /usr/lib /usr/local/lib -name libc.so.6 -o -name 'ld-linux*.so*')"
    for binary in piper_exe ffmpeg ffprobe opusenc; do
        dependencies=$(ldd "$(command -v "$binary")")
        printf '%s\n' "$dependencies" | grep -q 'ld-musl-'
    done
fi
test "$(id -u)" = 10001
for package in util-linux liblastlog2-2 libsqlite3-0; do
    if dpkg-query -W -f='${db:Status-Abbrev}' "$package" 2>/dev/null | grep -q '^ii'; then
        echo "Unused login/SQLite package remains: $package" >&2
        exit 1
    fi
done
test ! -e /usr/lib/x86_64-linux-gnu/libsqlite3.so.0
printf '%s\n' 'Application audio verification.' | timeout 40 piper --model /opt/piper/models/en_US-lessac-medium.onnx --output_file /tmp/runtime-test.wav --length_scale 1.0 >/tmp/runtime-piper.log 2>&1
timeout 20 opusenc --quiet /tmp/runtime-test.wav /tmp/runtime-encoded.ogg
timeout 20 ffmpeg -hide_banner -loglevel error -i /tmp/runtime-test.wav -ar 16000 -ac 1 -c:a libopus /tmp/runtime-converted.ogg
ffprobe -v error -select_streams a:0 -show_entries stream=codec_name -of default=noprint_wrappers=1:nokey=1 /tmp/runtime-converted.ogg | grep -qx opus
test -s /tmp/runtime-encoded.ogg
echo 'Offline Piper, Opus and FFmpeg verification passed; SQLite is absent.'
