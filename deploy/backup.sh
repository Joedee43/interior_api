#!/usr/bin/env bash
# Back up the interior database and uploaded media (Linux counterpart of
# scripts/backup.ps1).
#
# Writes a pg_dump in custom format (restore with pg_restore) and a tar.gz of
# the media folder, side by side and with the same timestamp, so a restore
# brings both back to the same moment. Deletes backups older than KEEP_DAYS.
#
# Environment: DATABASE_URL (required), STORAGE_DIR (default: storage),
# BACKUP_DIR (default: /var/backups/interior), KEEP_DAYS (default: 14).
# Nothing printed contains the password. pg_dump receives DATABASE_URL as an
# argument, so on a server shared with other users prefer a ~/.pgpass file.
#
# A backup on the same disk does not survive losing the server: copy the
# files somewhere else regularly (docs/DEPLOYMENT.md).
set -euo pipefail

: "${DATABASE_URL:?DATABASE_URL is not set}"
storage_dir="${STORAGE_DIR:-storage}"
out_dir="${BACKUP_DIR:-/var/backups/interior}"
keep_days="${KEEP_DAYS:-14}"

umask 077 # backups contain customer data: owner-only files
mkdir -p "$out_dir"
stamp="$(date +%Y%m%d_%H%M%S)"
dump="$out_dir/interior_db_$stamp.dump"
media="$out_dir/interior_storage_$stamp.tar.gz"

pg_dump --format=custom --no-owner --file="$dump" "$DATABASE_URL"
echo "database -> $dump ($(du -k "$dump" | cut -f1) KB)"

if [ -d "$storage_dir" ]; then
	tar -czf "$media" -C "$storage_dir" .
	echo "media    -> $media"
else
	echo "media    -> skipped ($storage_dir does not exist)"
fi

find "$out_dir" -maxdepth 1 -type f \
	\( -name 'interior_db_*.dump' -o -name 'interior_storage_*.tar.gz' \) \
	-mtime +"$keep_days" -print -delete
