#!/usr/bin/env bash
# Dumps the production Postgres to backups/meowshot-YYYYmmdd-HHMMSS.sql.gz and keeps 14 days.
# Run from the repo root on the VPS; schedule with cron, e.g.:
#   0 3 * * * cd ~/apps/meowshot-api && ./scripts/backup-db.sh >> backups/backup.log 2>&1
set -euo pipefail

# Rootless Docker needs the user's runtime dir, which cron does not set.
export XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}"

cd "$(dirname "$0")/.."
mkdir -p backups
out="backups/meowshot-$(date +%Y%m%d-%H%M%S).sql.gz"
trap 'rm -f "$out"' ERR

docker compose -f compose.prod.yml --env-file .env.production exec -T db \
  pg_dump -U meowshot -d meowshot --no-owner | gzip > "$out"

find backups -name 'meowshot-*.sql.gz' -mtime +14 -delete
echo "backup written: $out"
