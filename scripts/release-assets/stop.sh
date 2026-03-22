#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENV_FILE="$SCRIPT_DIR/.env"
COMPOSE_FILE="$SCRIPT_DIR/docker-compose.release.yml"

die() { echo "[stop] $*" >&2; exit 1; }

[[ -f "$COMPOSE_FILE" ]] || die "missing docker-compose.release.yml"

command -v docker >/dev/null 2>&1 || die "docker is required"
docker compose version >/dev/null 2>&1 || die "docker compose v2 is required"

if [[ -f "$ENV_FILE" ]]; then
  set -a
  . "$ENV_FILE"
  set +a
fi

VOICE_SERVER_VERSION="${VOICE_SERVER_VERSION:-latest}"
export VOICE_SERVER_VERSION

docker compose -f "$COMPOSE_FILE" down --remove-orphans

echo "[stop] stopped zenmind-voice-server $VOICE_SERVER_VERSION"
