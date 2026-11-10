#!/usr/bin/env bash
# Build, test, lint and sign the AURA Android client (Blueprint D.1 P7, ADR-0052).
#   sudo deploy/android-build.sh <version> [version-code]
# The release keystore lives in /opt/aura/release/android (created on first use, never in the repo or the image).
# Output: /opt/aura/releases/android/aura-<version>.apk and .sha256.
set -euo pipefail
cd "$(dirname "$0")/.."
VERSION=${1:?usage: android-build.sh <version> [code]}
CODE=${2:-$(date +%y%m%d%H)}
KEYDIR=/opt/aura/release/android
OUT=/opt/aura/releases/android
docker build -q -f deploy/android.Dockerfile -t aura-android-build:local deploy >/dev/null
mkdir -p "$KEYDIR" "$OUT" && chmod 0700 "$KEYDIR"
if [ ! -f "$KEYDIR/aura-release.jks" ]; then
  umask 077
  head -c 24 /dev/urandom | base64 | tr -d '/+=' > "$KEYDIR/keystore.pass"
  docker run --rm -v "$KEYDIR:/k" aura-android-build:local keytool -genkeypair -keystore /k/aura-release.jks -alias aura \
    -keyalg RSA -keysize 4096 -validity 10000 -storepass:file /k/keystore.pass -keypass:file /k/keystore.pass \
    -dname "CN=AURA, O=AURA" >/dev/null
  chmod 0400 "$KEYDIR/aura-release.jks" "$KEYDIR/keystore.pass"
fi
docker run --rm -v "$PWD/android:/src" -v aura-gradle:/root/.gradle -v "$KEYDIR:/k:ro" \
  -e AURA_KEYSTORE=/k/aura-release.jks -e AURA_KEYSTORE_PASSWORD_FILE=/k/keystore.pass \
  -e AURA_APP_VERSION="$VERSION" -e AURA_APP_CODE="$CODE" aura-android-build:local \
  bash -c 'gradle --no-daemon -q testReleaseUnitTest lintRelease assembleRelease && apksigner verify --print-certs app/build/outputs/apk/release/app-release.apk | head -3'
cp android/app/build/outputs/apk/release/app-release.apk "$OUT/aura-$VERSION.apk"
( cd "$OUT" && sha256sum "aura-$VERSION.apk" > "aura-$VERSION.apk.sha256" && cat "aura-$VERSION.apk.sha256" )
chown -R "$(stat -c %u:%g android)" android/app/build android/.gradle 2>/dev/null || true
echo "built $OUT/aura-$VERSION.apk"
