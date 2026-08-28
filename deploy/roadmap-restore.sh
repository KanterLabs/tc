#!/usr/bin/env bash
# Restore one explicitly selected, retained SQLite backup. This is a
# credential-revoking database recovery operation, not a release rollback:
# sessions, application tokens, and idempotency keys from the backup are
# deleted before the database is made active.
set -Eeuo pipefail
umask 077

STATE_DIR=${ROADMAP_STATE_DIR:-/var/lib/roadmap}
DATA_DIR=${ROADMAP_DATA_DIR:-$STATE_DIR/data}
DB_PATH=${ROADMAP_DB_PATH:-$DATA_DIR/roadmap.db}
BACKUP_DIR=${ROADMAP_BACKUP_DIR:-$STATE_DIR/backups}
RETENTION=${ROADMAP_BACKUP_RETENTION:-14}
BACKUP_PATH=${1:-}
LOCK_PATH="$STATE_DIR/deploy.lock"
SERVICE_STOP_TIMEOUT=30
MIGRATION_DIGEST=${ROADMAP_MIGRATION_DIGEST:-}
MIGRATION_INFO_BINARY=${ROADMAP_MIGRATION_INFO_BINARY:-$STATE_DIR/current/roadmap}
MIGRATION_BINARY=${ROADMAP_MIGRATION_BINARY:-$STATE_DIR/current/roadmap}

fail() {
	printf '[roadmap-restore] %s\n' "$*" >&2
	exit 1
}

backup_filename_is_valid() {
	local name=$1
	[[ "$name" =~ ^roadmap-[0-9]{8}T[0-9]{6}Z-(manual|daily|pre-restore|[0-9a-f]{40})\.db$ ]]
}

secure_backup_artifact() {
	local path=$1 owner mode
	[[ -f "$path" && ! -L "$path" && -s "$path" ]] || return 1
	owner=$(stat -c '%U:%G' -- "$path") || return 1
	mode=$(stat -c '%a' -- "$path") || return 1
	[[ "$owner" = root:root && "$mode" = 600 ]]
}

resolve_migration_digest() {
	local schema_hint=${1:-0} info digest count metadata_path metadata_digest metadata_count metadata_db
	if [[ -z "$MIGRATION_DIGEST" ]]; then
		if [[ "$MIGRATION_INFO_BINARY" = /* && -f "$MIGRATION_INFO_BINARY" && ! -L "$MIGRATION_INFO_BINARY" && -x "$MIGRATION_INFO_BINARY" ]]; then
			if info=$("$MIGRATION_INFO_BINARY" migration-info 2>/dev/null); then
				count=$(awk -F= '$1 == "migration_digest" { count++ } END { print count + 0 }' <<<"$info") || return 1
				if [[ "$count" = 1 ]]; then
					digest=$(awk -F= '$1 == "migration_digest" { print $2 }' <<<"$info") || return 1
					[[ "$digest" =~ ^[0-9a-f]{64}$ ]] && MIGRATION_DIGEST=$digest
				fi
			fi
		fi
	fi
	if [[ -z "$MIGRATION_DIGEST" ]]; then
		while IFS= read -r -d '' metadata_path; do
			[[ -f "$metadata_path" && ! -L "$metadata_path" ]] || continue
			metadata_db=${metadata_path%.metadata}
			backup_set_is_complete "$metadata_db" || continue
			[[ "$(stat -c '%U:%G' -- "$metadata_path" 2>/dev/null || true)" = root:root ]] || continue
			[[ "$(stat -c '%a' -- "$metadata_path" 2>/dev/null || true)" = 600 ]] || continue
			metadata_count=$(awk -F= '$1 == "migration_digest" { count++ } END { print count + 0 }' "$metadata_path") || continue
			[[ "$metadata_count" = 1 ]] || continue
			metadata_digest=$(awk -F= '$1 == "migration_digest" { print $2 }' "$metadata_path") || continue
			if [[ "$metadata_digest" =~ ^[0-9a-f]{64}$ ]]; then
				MIGRATION_DIGEST=$metadata_digest
				break
			fi
		done < <(find "$BACKUP_DIR" -maxdepth 1 -type f -name 'roadmap-*.db.metadata' -printf '%T@ %p\n' 2>/dev/null | sort -nr | cut -d' ' -f2- | while IFS= read -r path; do printf '%s\0' "$path"; done)
	fi
	if [[ -z "$MIGRATION_DIGEST" ]]; then
		MIGRATION_DIGEST=$(printf 'roadmap-migration-digest-fallback-v1\nschema_version=%s\n' "$schema_hint" | sha256sum | awk '{print $1}')
	fi
	[[ "$MIGRATION_DIGEST" =~ ^[0-9a-f]{64}$ ]]
}

schema_version_from_database() {
	local path=$1 value
	value=$(sqlite3 "$path" 'SELECT COALESCE(MAX(version), 0) FROM schema_migrations;') || return 1
	[[ "$value" =~ ^[0-9]+$ ]] || return 1
	printf '%s' "$value"
}

# A backup can be selected or counted for retention only when its database,
# checksum sidecar, and metadata sidecar form one complete, trusted set. Keep
# this predicate identical to the one used by roadmap-backup.sh so restore
# never consumes a partially published or damaged set.
backup_set_is_complete() {
	local path=$1 name parent checksum_path metadata_path checksum checksum_count
	local expected_release metadata_release metadata_created checksum_name
	local metadata_schema metadata_digest actual_schema schema_count digest_count
	name=$(basename -- "$path")
	backup_filename_is_valid "$name" || return 1
	expected_release=${BASH_REMATCH[1]}
	parent=$(dirname -- "$path")
	checksum_path="$path.sha256"
	metadata_path="$path.metadata"
	secure_backup_artifact "$path" || return 1
	secure_backup_artifact "$checksum_path" || return 1
	secure_backup_artifact "$metadata_path" || return 1

	checksum_name=$(basename -- "$checksum_path")
	checksum_count=$(awk -v name="$name" 'NF == 2 && $2 == name { count++ } END { print count + 0 }' \
		"$checksum_path") || return 1
	[[ "$checksum_count" = 1 ]] || return 1
	checksum=$(awk -v name="$name" 'NF == 2 && $2 == name { print $1 }' "$checksum_path") || return 1
	[[ "$checksum" =~ ^[0-9a-f]{64}$ ]] || return 1
	(
		cd -- "$parent" || exit 1
		sha256sum --check --strict -- "$checksum_name" >/dev/null 2>&1
	) || return 1

	metadata_release=$(awk -F= '$1 == "release_sha" { print $2 }' "$metadata_path") || return 1
	metadata_created=$(awk -F= '$1 == "created_at" { print $2 }' "$metadata_path") || return 1
	# A mismatch means the metadata release does not match its filename.
	[[ "$metadata_release" = "$expected_release" ]] || return 1
	[[ "$metadata_created" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]] || return 1
	schema_count=$(awk -F= '$1 == "schema_version" { count++ } END { print count + 0 }' "$metadata_path") || return 1
	digest_count=$(awk -F= '$1 == "migration_digest" { count++ } END { print count + 0 }' "$metadata_path") || return 1
	if [[ "$schema_count" = 0 && "$digest_count" = 0 ]]; then
		# Historical backups predate schema metadata. They remain selectable;
		# restore migrates their disposable candidate before using current-schema
		# authorization columns.
		:
	else
		[[ "$schema_count" = 1 && "$digest_count" = 1 ]] || return 1
		metadata_schema=$(awk -F= '$1 == "schema_version" { print $2 }' "$metadata_path") || return 1
		metadata_digest=$(awk -F= '$1 == "migration_digest" { print $2 }' "$metadata_path") || return 1
		[[ "$metadata_schema" =~ ^[0-9]+$ && "$metadata_digest" =~ ^[0-9a-f]{64}$ ]] || return 1
		actual_schema=$(schema_version_from_database "$path") || return 1
		[[ "$actual_schema" = "$metadata_schema" ]] || return 1
	fi
	[[ "$(sqlite3 "$path" 'PRAGMA integrity_check;')" = ok ]] || return 1
	[[ -z "$(sqlite3 "$path" 'PRAGMA foreign_key_check;')" ]] || return 1
}

backup_set_is_legacy() {
	local metadata_path=$1 schema_count digest_count
	metadata_path="$metadata_path.metadata"
	[[ -f "$metadata_path" && ! -L "$metadata_path" ]] || return 1
	schema_count=$(awk -F= '$1 == "schema_version" { count++ } END { print count + 0 }' "$metadata_path") || return 1
	digest_count=$(awk -F= '$1 == "migration_digest" { count++ } END { print count + 0 }' "$metadata_path") || return 1
	[[ "$schema_count" = 0 && "$digest_count" = 0 ]]
}

# Remove the database first so an interrupted prune cannot leave a final-name
# database visible without its sidecars. A sidecar-only residue is harmless
# and is deliberately not treated as a backup by backup_set_is_complete.
remove_backup_set() {
	local path=$1
	backup_set_is_complete "$path" || return 1
	rm -f -- "$path" || return 1
	rm -f -- "$path.sha256" "$path.metadata" || return 1
}

cleanup_stale_restore_temps() {
	local path owner mode
	while IFS= read -r -d '' path; do
		owner=$(stat -c '%U:%G' -- "$path" 2>/dev/null) || continue
		mode=$(stat -c '%a' -- "$path" 2>/dev/null) || continue
		# A temporary is created as a regular roadmap-owned 0640 file. Leave
		# anything surprising untouched rather than recursively cleaning data.
		[[ "$owner" = roadmap:roadmap && "$mode" = 640 ]] || continue
		rm -f -- "$path" || fail 'could not clean stale restore temporary'
	done < <(find "$DATA_DIR" -mindepth 1 -maxdepth 1 -type f \
		-name '.roadmap.db.restore.*' -print0)
}

unit_state() {
	systemctl is-active "$1" 2>/dev/null || true
}

stop_unit() {
	local unit=$1 state load_state
	load_state=$(systemctl show "$unit" --property=LoadState --value 2>/dev/null) || return 1
	[[ "$load_state" = loaded || "$load_state" = masked ]] || return 1
	state=$(unit_state "$unit")
	case "$state" in
		inactive|failed|dead) return 0 ;;
		active|activating|deactivating) ;;
		*) return 1 ;;
	esac
	systemctl stop "$unit" || return 1
	for _ in $(seq 1 "$SERVICE_STOP_TIMEOUT"); do
		state=$(unit_state "$unit")
		case "$state" in
			inactive|failed|dead) return 0 ;;
			active|activating|deactivating) sleep 1 ;;
			*) return 1 ;;
		esac
	done
	return 1
}

usage() {
	printf 'usage: %s /var/lib/roadmap/backups/roadmap-<timestamp>-<sha-or-manual>.db\n' "$0" >&2
	exit 64
}

[[ "$(id -u)" -eq 0 ]] || fail 'must run as root'
[[ $# -eq 1 ]] || usage

for path in "$STATE_DIR" "$DATA_DIR" "$BACKUP_DIR" "$DB_PATH" "$BACKUP_PATH"; do
	[[ "$path" = /* && "$path" != *$'\n'* && "$path" != *$'\r'* ]] \
		|| fail 'paths must be absolute and free of control characters'
	[[ "$path" != *"'"* ]] || fail 'paths may not contain a quote'
done
[[ -d "$STATE_DIR" && ! -L "$STATE_DIR" ]] || fail 'state directory is unavailable'
[[ "$(stat -c '%U:%G' -- "$STATE_DIR")" = root:root ]] || fail 'state directory owner is invalid'
[[ "$(stat -c '%a' -- "$STATE_DIR")" = 755 ]] || fail 'state directory mode is invalid'
[[ -d "$DATA_DIR" && ! -L "$DATA_DIR" ]] || fail 'database data directory is unavailable'
[[ "$(stat -c '%U:%G' -- "$DATA_DIR")" = roadmap:roadmap ]] || fail 'database data directory owner is invalid'
[[ "$(stat -c '%a' -- "$DATA_DIR")" = 750 ]] || fail 'database data directory mode is invalid'
[[ -d "$BACKUP_DIR" && ! -L "$BACKUP_DIR" ]] || fail 'backup directory is unavailable'
[[ "$(stat -c '%U:%G' -- "$BACKUP_DIR")" = root:root ]] || fail 'backup directory owner is invalid'
[[ "$(stat -c '%a' -- "$BACKUP_DIR")" = 700 ]] || fail 'backup directory mode is invalid'
[[ "$(dirname -- "$DB_PATH")" = "$DATA_DIR" && "$(basename -- "$DB_PATH")" = roadmap.db ]] \
	|| fail 'database path must be the Roadmap data database'
[[ "$(dirname -- "$BACKUP_PATH")" = "$BACKUP_DIR" ]] || fail 'backup must be directly inside the retained backup directory'
[[ "$RETENTION" =~ ^[1-9][0-9]*$ ]] || fail 'backup retention must be a positive integer'
[[ ! -L "$DB_PATH" ]] || fail 'database path must not be a symlink'
for sidecar in "$DB_PATH-wal" "$DB_PATH-shm"; do
	[[ ! -L "$sidecar" ]] || fail 'database journal paths must not be symlinks'
done

# The selected path is validated only after taking the same lock used by the
# backup, installer, and rollback helpers. Otherwise a valid file could be
# replaced between validation and use by a concurrent state mutation.
if [[ -e "$LOCK_PATH" || -L "$LOCK_PATH" ]]; then
	[[ -f "$LOCK_PATH" && ! -L "$LOCK_PATH" ]] || fail 'deployment lock is not a regular file'
	[[ "$(stat -c '%U:%G' -- "$LOCK_PATH")" = root:root ]] || fail 'deployment lock owner is invalid'
fi
exec 9>"$LOCK_PATH"
chown root:root "$LOCK_PATH"
chmod 0600 "$LOCK_PATH"
flock -x 9

command -v sqlite3 >/dev/null 2>&1 || fail 'sqlite3 is required for database restore'
command -v sha256sum >/dev/null 2>&1 || fail 'sha256sum is required for database restore'
cleanup_stale_restore_temps

backup_name=$(basename -- "$BACKUP_PATH")
backup_filename_is_valid "$backup_name" \
	|| fail 'backup filename is not a retained Roadmap backup'
backup_release_label=${BASH_REMATCH[1]}
[[ -f "$BACKUP_PATH" && ! -L "$BACKUP_PATH" ]] \
	|| fail 'selected backup is missing or not a regular file'
selected_validated_identity=$(stat -Lc '%d:%i:%u:%g' -- "$BACKUP_PATH") \
	|| fail 'could not inspect selected backup inode'
[[ "$(stat -Lc '%U:%G' -- "$BACKUP_PATH")" = root:root ]] \
	|| fail 'selected backup owner is invalid'
[[ "$(stat -Lc '%a' -- "$BACKUP_PATH")" = 600 ]] \
	|| fail 'selected backup mode must be 0600'

# Keep the selected backup's validated inode open for the entire transaction.
# Copies use this descriptor rather than reopening the mutable pathname; the
# pathname is checked against the descriptor before and after every use.
selected_fd=
exec {selected_fd}<"$BACKUP_PATH" || fail 'could not open selected backup'
selected_fd_path="/proc/$$/fd/$selected_fd"
selected_identity=$(stat -Lc '%d:%i:%u:%g' -- "$selected_fd_path") \
	|| fail 'could not inspect selected backup inode'
[[ "$selected_identity" = "$selected_validated_identity" ]] \
	|| fail 'selected backup changed while opening it'
[[ "$(stat -Lc '%U:%G' -- "$selected_fd_path")" = root:root ]] \
	|| fail 'selected backup owner changed while opening it'
[[ "$(stat -Lc '%a' -- "$selected_fd_path")" = 600 ]] \
	|| fail 'selected backup mode changed while opening it'

assert_selected_backup_identity() {
	[[ -n "$selected_fd" ]] || return 1
	[[ "$(stat -Lc '%d:%i:%u:%g' -- "$selected_fd_path")" = "$selected_identity" ]] || return 1
	[[ "$(stat -Lc '%U:%G' -- "$selected_fd_path")" = root:root ]] || return 1
	[[ "$(stat -Lc '%a' -- "$selected_fd_path")" = 600 ]] || return 1
	[[ -f "$BACKUP_PATH" && ! -L "$BACKUP_PATH" ]] || return 1
	[[ "$(stat -Lc '%d:%i:%u:%g' -- "$BACKUP_PATH")" = "$selected_identity" ]] || return 1
	[[ "$(stat -Lc '%U:%G' -- "$BACKUP_PATH")" = root:root ]] || return 1
	[[ "$(stat -Lc '%a' -- "$BACKUP_PATH")" = 600 ]] || return 1
}

backup_set_is_complete "$BACKUP_PATH" \
	|| fail 'selected backup is incomplete, invalid, or failed SQLite integrity check'
selected_checksum=$(awk -v name="$backup_name" 'NF == 2 && $2 == name { print $1 }' \
	"$BACKUP_PATH.sha256")
# A failed complete-set check covers "selected backup checksum does not match"
# and "selected backup failed SQLite integrity check" without allowing either
# condition to reach the restore transaction.
[[ "$selected_checksum" =~ ^[0-9a-f]{64}$ ]] || fail 'selected backup checksum sidecar is invalid'
assert_selected_backup_identity \
	|| fail 'selected backup changed after validation'

live_database_present=0
live_database_identity=
if [[ -f "$DB_PATH" && ! -L "$DB_PATH" ]]; then
	live_database_present=1
	live_database_identity=$(stat -Lc '%d:%i:%u:%g' -- "$DB_PATH") \
		|| fail 'could not inspect active database inode'
	[[ "$(stat -Lc '%U:%G' -- "$DB_PATH")" = roadmap:roadmap ]] \
		|| fail 'active database owner is invalid'
elif [[ -e "$DB_PATH" || -L "$DB_PATH" ]]; then
	fail 'active database path is not a regular file'
fi

roadmap_state_before=$(unit_state roadmap.service)
cloudflared_state_before=$(unit_state cloudflared.service)
case "$roadmap_state_before" in
	active|activating|deactivating) roadmap_was_active=1 ;;
	inactive|failed|dead) roadmap_was_active=0 ;;
	*) fail 'could not determine roadmap.service state' ;;
esac
case "$cloudflared_state_before" in
	active|activating|deactivating) cloudflared_was_active=1 ;;
	inactive|failed|dead) cloudflared_was_active=0 ;;
	*) fail 'could not determine cloudflared.service state' ;;
esac

work=$(mktemp -d "$BACKUP_DIR/.roadmap-restore.XXXXXX")
chown root:root "$work"
chmod 0700 "$work"
current_snapshot=
current_snapshot_ready=0
recovery_publication_started=0
recovery_published=0
candidate=
services_stopped=0

healthy() {
	local attempt
	for attempt in $(seq 1 30); do
		if curl --fail --silent --show-error --max-time 3 http://127.0.0.1:8080/healthz >/dev/null 2>&1; then
			return 0
		fi
		sleep 1
	done
	return 1
}

assert_live_database_identity() {
	if (( live_database_present == 1 )); then
		[[ -f "$DB_PATH" && ! -L "$DB_PATH" ]] || return 1
		[[ "$(stat -Lc '%d:%i:%u:%g' -- "$DB_PATH")" = "$live_database_identity" ]] || return 1
		[[ "$(stat -Lc '%U:%G' -- "$DB_PATH")" = roadmap:roadmap ]] || return 1
	else
		[[ ! -e "$DB_PATH" && ! -L "$DB_PATH" ]] || return 1
	fi
}

restore_database_file() {
	local source=$1 temporary="$DATA_DIR/.roadmap.db.restore.$$" sidecar
	[[ -f "$source" && ! -L "$source" ]] || return 1
	[[ ! -L "$DB_PATH" ]] || return 1
	[[ ! -e "$temporary" && ! -L "$temporary" ]] || return 1
	if ! install -m 0640 -o roadmap -g roadmap "$source" "$temporary"; then
		rm -f -- "$temporary" || true
		return 1
	fi
	if [[ "$(sqlite3 "$temporary" 'PRAGMA integrity_check;')" != ok ]]; then
		rm -f -- "$temporary" || true
		return 1
	fi
	for sidecar in "$DB_PATH-wal" "$DB_PATH-shm"; do
		[[ ! -L "$sidecar" ]] || {
			rm -f -- "$temporary" || true
			return 1
		}
	done
	rm -f -- "$DB_PATH-wal" "$DB_PATH-shm" || {
		rm -f -- "$temporary" || true
		return 1
	}
	if ! mv -T -- "$temporary" "$DB_PATH"; then
		rm -f -- "$temporary" || true
		return 1
	fi
	chown roadmap:roadmap "$DB_PATH" || return 1
	chmod 0640 "$DB_PATH" || return 1
}

migrate_staged_candidate() {
	local candidate=$1 output candidate_schema
	[[ -f "$candidate" && ! -L "$candidate" ]] || return 1
	[[ "$MIGRATION_BINARY" = /* && -f "$MIGRATION_BINARY" && ! -L "$MIGRATION_BINARY" && -x "$MIGRATION_BINARY" ]] || return 1
	[[ "$(stat -c '%U:%G' -- "$MIGRATION_BINARY")" = root:root ]] || return 1
	output=$("$MIGRATION_BINARY" migration-apply "$candidate" 2>/dev/null) || return 1
	grep -Fx 'status=ok' <<<"$output" >/dev/null || return 1
	candidate_schema=$(schema_version_from_database "$candidate") || return 1
	[[ "$candidate_schema" =~ ^[0-9]+$ ]] || return 1
	[[ -z "$(sqlite3 "$candidate" 'PRAGMA foreign_key_check;')" ]] || return 1
}

restore_prior_services() {
	local status=0
	if (( roadmap_was_active == 1 )); then
		if ! systemctl start roadmap.service || ! systemctl is-active --quiet roadmap.service || ! healthy; then
			status=1
		fi
	else
		stop_unit roadmap.service || status=1
	fi
	if (( cloudflared_was_active == 1 )); then
		if ! systemctl start cloudflared.service || ! systemctl is-active --quiet cloudflared.service; then
			status=1
		fi
	else
		stop_unit cloudflared.service || status=1
	fi
	return "$status"
}

recover_current() {
	(( current_snapshot_ready == 1 )) || return 1
	backup_set_is_complete "$current_snapshot" || return 1
	stop_unit cloudflared.service || return 1
	stop_unit roadmap.service || return 1
	restore_database_file "$current_snapshot" || return 1
	restore_prior_services
}

preserve_live_authorization() {
	local database=$1
	if [[ -n "$current_snapshot" ]]; then
		# Merge the current authorization plane onto restored project/task data.
		# Current actors survive even if they were created after the selected
		# backup; actors found only in the old backup are retained as disabled,
		# credential-less records so historical foreign keys remain valid.
		sqlite3 "$database" <<SQL
PRAGMA foreign_keys = ON;
ATTACH DATABASE '$current_snapshot' AS current_auth;
BEGIN IMMEDIATE;
UPDATE actors
   SET kind = (SELECT c.kind FROM current_auth.actors AS c WHERE c.id = actors.id),
       name = (SELECT c.name FROM current_auth.actors AS c WHERE c.id = actors.id),
       email = (SELECT c.email FROM current_auth.actors AS c WHERE c.id = actors.id),
       password_hash = (SELECT c.password_hash FROM current_auth.actors AS c WHERE c.id = actors.id),
       admin = (SELECT c.admin FROM current_auth.actors AS c WHERE c.id = actors.id),
       disabled_at = (SELECT c.disabled_at FROM current_auth.actors AS c WHERE c.id = actors.id),
       created_at = (SELECT c.created_at FROM current_auth.actors AS c WHERE c.id = actors.id),
       updated_at = (SELECT c.updated_at FROM current_auth.actors AS c WHERE c.id = actors.id),
       description = (SELECT c.description FROM current_auth.actors AS c WHERE c.id = actors.id)
 WHERE id IN (SELECT id FROM current_auth.actors);
UPDATE actors
   SET email = NULL,
       password_hash = NULL,
       admin = 0,
       disabled_at = COALESCE(disabled_at, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
 WHERE NOT EXISTS (SELECT 1 FROM current_auth.actors AS c WHERE c.id = actors.id);
INSERT INTO actors (id, kind, name, email, password_hash, admin, disabled_at, created_at, updated_at, description)
SELECT c.id, c.kind, c.name, c.email, c.password_hash, c.admin, c.disabled_at, c.created_at, c.updated_at, c.description
  FROM current_auth.actors AS c
 WHERE NOT EXISTS (SELECT 1 FROM actors AS a WHERE a.id = c.id);
DELETE FROM auth_setup;
INSERT INTO auth_setup (id, completed_at)
SELECT id, completed_at FROM current_auth.auth_setup;
DELETE FROM actor_projects;
INSERT INTO actor_projects (actor_id, project_id)
SELECT c.actor_id, c.project_id
  FROM current_auth.actor_projects AS c
  JOIN actors AS a ON a.id = c.actor_id
  JOIN projects AS p ON p.id = c.project_id;
CREATE TABLE IF NOT EXISTS actor_resource_usage (
    actor_id TEXT PRIMARY KEY REFERENCES actors(id) ON DELETE CASCADE,
    reserved_bytes INTEGER NOT NULL CHECK (reserved_bytes >= 0),
    updated_at TEXT NOT NULL
);
DELETE FROM actor_resource_usage;
INSERT INTO actor_resource_usage (actor_id, reserved_bytes, updated_at)
SELECT c.actor_id, c.reserved_bytes, c.updated_at
  FROM current_auth.actor_resource_usage AS c
  JOIN actors AS a ON a.id = c.actor_id;
DELETE FROM sessions;
DELETE FROM tokens;
DELETE FROM idempotency_keys;
COMMIT;
DETACH DATABASE current_auth;
SQL
	else
		# With no current database there is no authorization state to restore;
		# fail closed by disabling every historical actor and clearing setup.
		sqlite3 "$database" <<'SQL'
PRAGMA foreign_keys = ON;
BEGIN IMMEDIATE;
CREATE TABLE IF NOT EXISTS actor_resource_usage (
    actor_id TEXT PRIMARY KEY REFERENCES actors(id) ON DELETE CASCADE,
    reserved_bytes INTEGER NOT NULL CHECK (reserved_bytes >= 0),
    updated_at TEXT NOT NULL
);
UPDATE actors
   SET email = NULL,
       password_hash = NULL,
       admin = 0,
       disabled_at = COALESCE(disabled_at, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
DELETE FROM auth_setup;
DELETE FROM actor_projects;
DELETE FROM actor_resource_usage;
DELETE FROM sessions;
DELETE FROM tokens;
DELETE FROM idempotency_keys;
COMMIT;
SQL
	fi
}

prune_backups() {
	local protected_recovery=$1 protected_selected=$2 entry path kept=0
	local -a backups valid_backups
	mapfile -t backups < <(find "$BACKUP_DIR" -maxdepth 1 -type f \
		-name 'roadmap-*.db' -printf '%T@ %p\n' | sort -nr)

	# First filter by the complete-set predicate. Incomplete or damaged files
	# do not consume retention slots and are left untouched for inspection.
	for entry in "${backups[@]}"; do
		path=${entry#* }
		backup_set_is_complete "$path" && valid_backups+=("$entry")
	done

	for entry in "${valid_backups[@]}"; do
		path=${entry#* }
		# Keep the recovery snapshot and selected source even when a low
		# retention setting would otherwise remove them during this restore.
		if [[ "$path" = "$protected_recovery" || "$path" = "$protected_selected" ]]; then
			continue
		fi
		# Do not silently age out historical backups that lack the newer schema
		# metadata fields; they remain valid explicit restore sources.
		backup_set_is_legacy "$path" && continue
		kept=$((kept + 1))
		(( kept <= RETENTION )) && continue
		remove_backup_set "$path" || return 1
	done
}

cleanup_recovery_publication() {
	local artifact
	# The database is removed first if a crash/failure left a partial set; this
	# prevents cleanup itself from exposing a final-name .db without sidecars.
	for artifact in "$current_snapshot" "$current_snapshot.sha256" "$current_snapshot.metadata"; do
		[[ -f "$artifact" && ! -L "$artifact" ]] || continue
		[[ "$(stat -c '%U:%G' -- "$artifact" 2>/dev/null || true)" = root:root ]] || continue
		[[ "$(stat -c '%a' -- "$artifact" 2>/dev/null || true)" = 600 ]] || continue
		rm -f -- "$artifact" || true
	done
}

cleanup() {
	local status=$?
	# Recovery executes while this transaction is unwinding; disable the EXIT
	# trap first so a recovery failure cannot recursively re-enter cleanup.
	trap - EXIT

	if (( recovery_publication_started == 1 && recovery_published == 0 )); then
		# A hard interruption after the final database rename may have completed
		# a valid set before the process could mark it published. Preserve that
		# valid set; remove only this run's incomplete final-name artifacts.
		if [[ -n "$current_snapshot" ]] && backup_set_is_complete "$current_snapshot"; then
			current_snapshot_ready=1
			recovery_published=1
		else
			cleanup_recovery_publication
		fi
	fi

	if [[ -n "$candidate" && -e "$candidate" && ! -L "$candidate" ]]; then
		rm -f -- "$candidate" || true
	fi
	if [[ -n "${live_fd:-}" ]]; then
		exec {live_fd}<&- || true
	fi
	if [[ "$status" -ne 0 && "$services_stopped" -eq 1 ]]; then
		# A failed restore must not leave the application stranded offline. Once
		# a complete active snapshot exists, put that exact database back before
		# restoring the service states observed at the start of the transaction.
		if (( current_snapshot_ready == 1 )); then
			recover_current >/dev/null 2>&1 || true
		else
			restore_prior_services >/dev/null 2>&1 || true
		fi
	fi
	if [[ -n "${selected_fd:-}" ]]; then
		exec {selected_fd}<&- || true
	fi
	if [[ -n "${work:-}" && "$work" = "$BACKUP_DIR"/.roadmap-restore.* &&
		-d "$work" && ! -L "$work" &&
		"$(stat -c '%U:%G' -- "$work" 2>/dev/null || true)" = root:root &&
		"$(stat -c '%a' -- "$work" 2>/dev/null || true)" = 700 ]]; then
		rm -rf -- "$work" || true
	fi
	exit "$status"
}
trap cleanup EXIT

services_stopped=1
stop_unit cloudflared.service || fail 'could not stop cloudflared.service and verify it is inactive'
stop_unit roadmap.service || fail 'could not stop roadmap.service and verify it is inactive'

# Preserve the active database through SQLite's online backup API before any
# replacement. Stage all three files first, validate them as a complete set,
# and publish sidecars before the database so no final-name .db is exposed
# without its checksum and metadata.
if (( live_database_present == 1 )); then
	assert_live_database_identity || fail 'active database changed before snapshot'
	live_fd=
	exec {live_fd}<"$DB_PATH" || fail 'could not open active database'
	live_fd_path="/proc/$$/fd/$live_fd"
	[[ "$(stat -Lc '%d:%i:%u:%g' -- "$live_fd_path")" = "$live_database_identity" ]] \
		|| fail 'active database changed while opening snapshot source'
	current_name="roadmap-$(date -u +%Y%m%dT%H%M%SZ)-pre-restore.db"
	current_tmp="$work/$current_name"
	sqlite3 -cmd '.timeout 5000' "$DB_PATH" ".backup '$current_tmp'" \
		|| fail 'could not preserve the active database'
	[[ -s "$current_tmp" && ! -L "$current_tmp" ]] || fail 'active database snapshot is empty'
	chmod 0600 "$current_tmp"
	chown root:root "$current_tmp"
	assert_live_database_identity || fail 'active database changed during snapshot'
	[[ "$(stat -Lc '%d:%i:%u:%g' -- "$live_fd_path")" = "$live_database_identity" ]] \
		|| fail 'opened active database changed during snapshot'
	if [[ -n "${live_fd:-}" ]]; then
		exec {live_fd}<&-
		live_fd=
	fi
	[[ "$(sqlite3 "$current_tmp" 'PRAGMA integrity_check;')" = ok ]] \
		|| fail 'active database snapshot failed SQLite integrity check'
	[[ -z "$(sqlite3 "$current_tmp" 'PRAGMA foreign_key_check;')" ]] \
		|| fail 'active database snapshot failed SQLite foreign-key check'
	migrate_staged_candidate "$current_tmp" \
		|| fail 'could not migrate active database snapshot with the current binary'
	current_snapshot="$BACKUP_DIR/$current_name"
	for artifact in "$current_snapshot" "$current_snapshot.sha256" "$current_snapshot.metadata"; do
		[[ ! -e "$artifact" && ! -L "$artifact" ]] || fail 'active database snapshot name already exists'
	done
	current_schema_version=$(schema_version_from_database "$current_tmp") \
		|| fail 'could not inspect active snapshot schema version'
	[[ "$current_schema_version" =~ ^[0-9]+$ ]] \
		|| fail 'active snapshot schema version is invalid'
	resolve_migration_digest "$current_schema_version" || fail 'migration digest is unavailable'
	current_checksum=$(sha256sum -- "$current_tmp" | awk '{print $1}')
	printf '%s  %s\n' "$current_checksum" "$current_name" > "$work/$current_name.sha256"
	printf 'release_sha=pre-restore\ncreated_at=%s\nschema_version=%s\nmigration_digest=%s\n' \
		"$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$current_schema_version" "$MIGRATION_DIGEST" > "$work/$current_name.metadata"
	chmod 0600 "$work/$current_name.sha256" "$work/$current_name.metadata"
	chown root:root "$work/$current_name.sha256" "$work/$current_name.metadata"
	backup_set_is_complete "$current_tmp" || fail 'active database snapshot set validation failed'

	# POSIX has no multi-file rename. Publish sidecars first and the database
	# last; cleanup knows this exact destination and never removes other sets.
	recovery_publication_started=1
	for artifact in "$current_name.sha256" "$current_name.metadata"; do
		mv -T -- "$work/$artifact" "$BACKUP_DIR/$artifact" \
			|| fail "could not publish active database snapshot $artifact"
	done
	mv -T -- "$current_tmp" "$current_snapshot" \
		|| fail 'could not publish active database snapshot'
	backup_set_is_complete "$current_snapshot" \
		|| fail 'published active database snapshot set validation failed'
	current_snapshot_ready=1
	recovery_published=1
	prune_backups "$current_snapshot" "$BACKUP_PATH" \
		|| fail 'could not prune retained backup sets'
fi

candidate="$work/roadmap.db"
assert_selected_backup_identity \
	|| fail 'selected backup changed before restore copy'
install -m 0640 -o roadmap -g roadmap "$selected_fd_path" "$candidate" \
	|| fail 'could not stage selected backup'
[[ "$(sha256sum -- "$candidate" | awk '{print $1}')" = "$selected_checksum" ]] \
	|| fail 'selected backup changed while staging restore candidate'
assert_selected_backup_identity \
	|| fail 'selected backup changed during restore copy'
[[ "$(sqlite3 "$candidate" 'PRAGMA integrity_check;')" = ok ]] \
	|| fail 'restore candidate failed SQLite integrity check'
[[ -z "$(sqlite3 "$candidate" 'PRAGMA foreign_key_check;')" ]] \
	|| fail 'restore candidate failed SQLite foreign-key check'

# Retained backups may predate the current schema. Upgrade the disposable
# candidate with the active binary before the authorization overlay references
# columns introduced by newer migrations (description, grants, and quotas).
migrate_staged_candidate "$candidate" \
	|| fail 'could not migrate restore candidate with the current binary'

# Restore data while preserving the current authorization plane. Every table
# named below is part of the supported schema; a missing table is a hard
# failure rather than a partial restore.
preserve_live_authorization "$candidate"
[[ "$(sqlite3 "$candidate" 'SELECT (SELECT COUNT(*) FROM sessions) || "|" || (SELECT COUNT(*) FROM tokens) || "|" || (SELECT COUNT(*) FROM idempotency_keys);')" = '0|0|0' ]] \
	|| fail 'restored credential tables were not emptied'
[[ "$(sqlite3 "$candidate" 'PRAGMA integrity_check;')" = ok ]] \
	|| fail 'revoked restore candidate failed SQLite integrity check'
[[ -z "$(sqlite3 "$candidate" 'PRAGMA foreign_key_check;')" ]] \
	|| fail 'revoked restore candidate failed SQLite foreign-key check'

assert_selected_backup_identity \
	|| fail 'selected backup changed before restore activation'
[[ ! -L "$DB_PATH" ]] || fail 'active database path must not be a symlink'
assert_live_database_identity || fail 'active database changed before restore activation'
restore_database_file "$candidate" || fail 'could not activate the restored database'
candidate=

if [[ -n "${selected_fd:-}" ]]; then
	exec {selected_fd}<&-
	selected_fd=
fi

systemctl start roadmap.service
if ! healthy; then
	fail 'restored database failed the application health check; active snapshot retained for recovery'
fi

systemctl start cloudflared.service
systemctl is-active --quiet roadmap.service || fail 'roadmap service is not active after restore'
systemctl is-active --quiet cloudflared.service || fail 'cloudflared service is not active after restore'

printf 'restored_backup=%s\n' "$BACKUP_PATH"
printf 'credentials_revoked=sessions,tokens,idempotency_keys\n'
