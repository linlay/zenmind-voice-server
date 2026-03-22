#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
RELEASE_ASSETS_DIR="$SCRIPT_DIR/release-assets"

die() { echo "[release] $*" >&2; exit 1; }

VERSION="${VERSION:-$(cat "$REPO_ROOT/VERSION" 2>/dev/null || echo "")}"
[[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "VERSION must match vX.Y.Z (got: ${VERSION:-<empty>})"

if [[ -z "${ARCH:-}" ]]; then
  case "$(uname -m)" in
    x86_64|amd64) ARCH=amd64 ;;
    arm64|aarch64) ARCH=arm64 ;;
    *) die "cannot detect ARCH from $(uname -m); pass ARCH=amd64|arm64" ;;
  esac
fi

PLATFORM="linux/$ARCH"
BACKEND_IMAGE="voice-server-backend:$VERSION"
FRONTEND_IMAGE="voice-server-frontend:$VERSION"
BUNDLE_DIR_NAME="zenmind-voice-server"
BUNDLE_NAME="${BUNDLE_DIR_NAME}-${VERSION}-linux-${ARCH}"
BUNDLE_TAR="$REPO_ROOT/dist/release/${BUNDLE_NAME}.tar.gz"

echo "[release] VERSION=$VERSION ARCH=$ARCH PLATFORM=$PLATFORM"

command -v docker >/dev/null 2>&1 || die "docker is required"

TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/zenmind-voice-release.XXXXXX")"
trap 'rm -rf "$TMP_DIR"' EXIT

IMAGES_DIR="$TMP_DIR/images"
mkdir -p "$IMAGES_DIR"

echo "[release] building backend image..."
docker buildx build \
  --platform "$PLATFORM" \
  --file "$REPO_ROOT/Dockerfile" \
  --tag "$BACKEND_IMAGE" \
  --output "type=docker,dest=$IMAGES_DIR/voice-server-backend.tar" \
  "$REPO_ROOT"

echo "[release] building frontend image..."
docker buildx build \
  --platform "$PLATFORM" \
  --file "$REPO_ROOT/frontend/Dockerfile" \
  --tag "$FRONTEND_IMAGE" \
  --output "type=docker,dest=$IMAGES_DIR/voice-server-frontend.tar" \
  "$REPO_ROOT/frontend"

BUNDLE_ROOT="$TMP_DIR/$BUNDLE_DIR_NAME"
mkdir -p "$BUNDLE_ROOT/images"

cp "$RELEASE_ASSETS_DIR/docker-compose.release.yml" "$BUNDLE_ROOT/docker-compose.release.yml"
cp "$RELEASE_ASSETS_DIR/start.sh" "$BUNDLE_ROOT/start.sh"
cp "$RELEASE_ASSETS_DIR/stop.sh" "$BUNDLE_ROOT/stop.sh"
cp "$RELEASE_ASSETS_DIR/README.txt" "$BUNDLE_ROOT/README.txt"
cp "$REPO_ROOT/.env.example" "$BUNDLE_ROOT/.env.example"
cp "$IMAGES_DIR/voice-server-backend.tar" "$BUNDLE_ROOT/images/"
cp "$IMAGES_DIR/voice-server-frontend.tar" "$BUNDLE_ROOT/images/"

sed -i.bak "s/^VOICE_SERVER_VERSION=.*/VOICE_SERVER_VERSION=$VERSION/" "$BUNDLE_ROOT/.env.example"
rm -f "$BUNDLE_ROOT/.env.example.bak"

chmod +x "$BUNDLE_ROOT/start.sh" "$BUNDLE_ROOT/stop.sh"

mkdir -p "$(dirname "$BUNDLE_TAR")"
tar -czf "$BUNDLE_TAR" -C "$TMP_DIR" "$BUNDLE_DIR_NAME"

echo "[release] done: $BUNDLE_TAR"
