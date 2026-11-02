#!/bin/sh
set -eu
for package in util-linux liblastlog2-2 libsqlite3-0; do
    if dpkg-query -W -f='${db:Status-Abbrev}' "$package" 2>/dev/null | grep -q '^ii'; then
        echo "Unused login/SQLite package remains: $package" >&2
        exit 1
    fi
done
test ! -e /usr/lib/x86_64-linux-gnu/libsqlite3.so.0
printf '%s\n' 'Application audio verification.' | timeout 40 piper --model /opt/piper/models/en_US-lessac-medium.onnx --output_file /tmp/runtime-test.wav >/tmp/runtime-piper.log 2>&1
timeout 20 opusenc --quiet /tmp/runtime-test.wav /tmp/runtime-encoded.ogg
timeout 20 ffmpeg -hide_banner -loglevel error -i /tmp/runtime-test.wav -ar 16000 -ac 1 -c:a libopus /tmp/runtime-converted.ogg
ffprobe -v error -select_streams a:0 -show_entries stream=codec_name -of default=noprint_wrappers=1:nokey=1 /tmp/runtime-converted.ogg | grep -qx opus
test -s /tmp/runtime-encoded.ogg
echo 'Offline Piper, Opus and FFmpeg verification passed; SQLite is absent.'
