#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENV_FILE="$SCRIPT_DIR/.env"
COMPOSE_FILE="$SCRIPT_DIR/docker-compose.release.yml"
IMAGES_DIR="$SCRIPT_DIR/images"
NETWORK_NAME="zenmind-network"

die() { echo "[start] $*" >&2; exit 1; }

[[ -f "$ENV_FILE" ]] || die "missing .env (copy from .env.example first)"
[[ -f "$COMPOSE_FILE" ]] || die "missing docker-compose.release.yml"

command -v docker >/dev/null 2>&1 || die "docker is required"
docker compose version >/dev/null 2>&1 || die "docker compose v2 is required"

set -a
. "$ENV_FILE"
set +a

VOICE_SERVER_VERSION="${VOICE_SERVER_VERSION:-latest}"
BACKEND_IMAGE="voice-server-backend:$VOICE_SERVER_VERSION"
FRONTEND_IMAGE="voice-server-frontend:$VOICE_SERVER_VERSION"

load_image() {
  local ref="$1"
  local tar="$2"
  if docker image inspect "$ref" >/dev/null 2>&1; then
    return 0
  fi
  [[ -f "$tar" ]] || die "missing image tar: $tar"
  docker load -i "$tar" >/dev/null
  docker image inspect "$ref" >/dev/null 2>&1 || die "failed to load image: $ref"
}

ensure_network() {
  if docker network inspect "$NETWORK_NAME" >/dev/null 2>&1; then
    return 0
  fi
  docker network create "$NETWORK_NAME" >/dev/null
}

load_image "$BACKEND_IMAGE" "$IMAGES_DIR/voice-server-backend.tar"
load_image "$FRONTEND_IMAGE" "$IMAGES_DIR/voice-server-frontend.tar"
ensure_network

export VOICE_SERVER_VERSION
docker compose -f "$COMPOSE_FILE" up -d

echo "[start] started zenmind-voice-server $VOICE_SERVER_VERSION"
echo "[start] frontend: http://127.0.0.1:${FRONTEND_PORT:-11954}"
echo "[start] backend: http://127.0.0.1:${SERVER_PORT:-11953}"
