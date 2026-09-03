#!/usr/bin/env bash
# Create a consistent, integrity-checked SQLite backup before a release
# changes the executable or runs embedded migrations.
set -Eeuo pipefail
umask 077

compat_env() {
	local canonical=$1 legacy=$2 default_value=${3:-} canonical_value legacy_value
	canonical_value=${!canonical:-}
	legacy_value=${!legacy:-}
	[[ -z "$canonical_value" || -z "$legacy_value" || "$canonical_value" = "$legacy_value" ]] || {
		printf '[helm-backup] %s and %s must match when both are set\n' "$canonical" "$legacy" >&2
		exit 1
	}
	printf '%s' "${canonical_value:-${legacy_value:-$default_value}}"
}

STATE_DIR=$(compat_env HELM_STATE_DIR ROADMAP_STATE_DIR /var/lib/roadmap)
DATA_DIR=$(compat_env HELM_DATA_DIR ROADMAP_DATA_DIR "$STATE_DIR/data")
DB_PATH=$(compat_env HELM_DB_PATH ROADMAP_DB_PATH "$DATA_DIR/roadmap.db")
BACKUP_DIR=$(compat_env HELM_BACKUP_DIR ROADMAP_BACKUP_DIR "$STATE_DIR/backups")
RETENTION=$(compat_env HELM_BACKUP_RETENTION ROADMAP_BACKUP_RETENTION 14)
RELEASE_SHA=${1:-manual}
MIGRATION_DIGEST=$(compat_env HELM_MIGRATION_DIGEST ROADMAP_MIGRATION_DIGEST)
default_migration_binary="$STATE_DIR/current/helm"
[[ -x "$default_migration_binary" && ! -L "$default_migration_binary" ]] || default_migration_binary="$STATE_DIR/current/roadmap"
MIGRATION_INFO_BINARY=$(compat_env HELM_MIGRATION_INFO_BINARY ROADMAP_MIGRATION_INFO_BINARY "$default_migration_binary")
DEPLOY_LOCK_HELD=$(compat_env HELM_DEPLOY_LOCK_HELD ROADMAP_DEPLOY_LOCK_HELD 0)

fail() {
	printf '[helm-backup] %s\n' "$*" >&2
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
		# During a binary-only rollback, current may point to an older retained
		# executable without migration-info. Reuse the newest validated digest
		# already recorded by a candidate backup; this keeps manual/daily backups
		# identifiable without executing an untrusted or mismatched binary.
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
		# No prior metadata exists on a fresh installation. This deterministic
		# compatibility identity is only a last resort for retained binaries;
		# subsequent candidate backups carry the real embedded digest.
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

# A backup is usable only when every sidecar names this exact file, the
# metadata agrees with the filename, the checksum matches, and SQLite can
# read the complete database. Retention uses this same predicate, so a
# partially published or damaged set never consumes a retention slot.
backup_set_is_complete() {
	local path=$1 name parent checksum_path metadata_path checksum checksum_count
	local expected_release metadata_release metadata_created checksum_name
	local metadata_schema metadata_digest actual_schema schema_count digest_count
	name=$(basename -- "$path")
	[[ "$name" =~ ^roadmap-[0-9]{8}T[0-9]{6}Z-(manual|daily|pre-restore|[0-9a-f]{40})\.db$ ]] \
		|| return 1
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
	[[ "$metadata_release" = "$expected_release" ]] || return 1
	[[ "$metadata_created" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]] || return 1
	schema_count=$(awk -F= '$1 == "schema_version" { count++ } END { print count + 0 }' "$metadata_path") || return 1
	digest_count=$(awk -F= '$1 == "migration_digest" { count++ } END { print count + 0 }' "$metadata_path") || return 1
	if [[ "$schema_count" = 0 && "$digest_count" = 0 ]]; then
		# Backups created before schema metadata was introduced remain valid
		# historical restore sources. Their staged candidate is migrated by the
		# current binary before any current-schema fields are referenced.
		:
	else
		[[ "$schema_count" = 1 && "$digest_count" = 1 ]] || return 1
		metadata_schema=$(awk -F= '$1 == "schema_version" { print $2 }' "$metadata_path") || return 1
		metadata_digest=$(awk -F= '$1 == "migration_digest" { print $2 }' "$metadata_path") || return 1
		[[ "$metadata_schema" =~ ^[0-9]+$ && "$metadata_digest" =~ ^[0-9a-f]{64}$ ]] || return 1
		# Read schema_version from this backup artifact, never from the live
		# source, so a concurrent online write cannot produce mismatched sidecars.
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

cleanup_staging_dirs() {
	local staging owner mode
	while IFS= read -r -d '' staging; do
		[[ -d "$staging" && ! -L "$staging" ]] || continue
		owner=$(stat -c '%U:%G' -- "$staging" 2>/dev/null) || continue
		mode=$(stat -c '%a' -- "$staging" 2>/dev/null) || continue
		# Never recursively remove a path that is not an expected root-owned
		# staging directory. Backup_DIR is root-owned, but keep this check in
		# case an interrupted run left a surprising entry behind.
		[[ "$owner" = root:root && "$mode" = 700 ]] || continue
		rm -rf -- "$staging" || fail 'could not clean stale backup staging'
	done < <(find "$BACKUP_DIR" -mindepth 1 -maxdepth 1 -type d -name '.roadmap-backup.*' -print0)
}

cleanup_orphaned_sidecars() {
	local suffix sidecar base owner mode
	for suffix in sha256 metadata; do
		while IFS= read -r -d '' sidecar; do
			base=${sidecar%.$suffix}
			# A sidecar with no database is the only final-name residue that
			# can be safely recognized after a process dies between commits.
			[[ -e "$base" || -L "$base" ]] && continue
			backup_filename_is_valid "$(basename -- "$base")" || continue
			owner=$(stat -c '%U:%G' -- "$sidecar" 2>/dev/null) || continue
			mode=$(stat -c '%a' -- "$sidecar" 2>/dev/null) || continue
			[[ "$owner" = root:root && "$mode" = 600 ]] || continue
			rm -f -- "$sidecar" || fail 'could not clean orphaned backup sidecar'
		done < <(find "$BACKUP_DIR" -mindepth 1 -maxdepth 1 -type f \
			-name "roadmap-*.db.$suffix" -print0)
	done
}

remove_backup_set() {
	local path=$1 artifact
	backup_set_is_complete "$path" || return 1
	# Remove the database first. If pruning is interrupted, a final-name .db
	# can therefore never remain visible without both validated sidecars.
	for artifact in "$path" "$path.sha256" "$path.metadata"; do
		[[ -f "$artifact" && ! -L "$artifact" ]] || return 1
		rm -f -- "$artifact" || return 1
	done
}

[[ "$(id -u)" -eq 0 ]] || fail 'must run as root'
[[ "$RELEASE_SHA" =~ ^([0-9a-f]{40}|manual|daily)$ ]] || fail 'invalid release SHA'
[[ "$RETENTION" =~ ^[1-9][0-9]*$ ]] || fail 'backup retention must be a positive integer'
[[ "$DB_PATH" = /* && "$BACKUP_DIR" = /* ]] || fail 'backup paths must be absolute'
[[ "$(dirname -- "$DB_PATH")" = "$DATA_DIR" && "$(basename -- "$DB_PATH")" = roadmap.db ]] \
	|| fail 'database path must be the Roadmap data database'
[[ "$DB_PATH" != *$'\n'* && "$DB_PATH" != *$'\r'* ]] || fail 'database path contains a control character'
[[ "$BACKUP_DIR" != *$'\n'* && "$BACKUP_DIR" != *$'\r'* ]] || fail 'backup path contains a control character'
[[ "$DB_PATH" != *"'"* && "$BACKUP_DIR" != *"'"* ]] || fail 'backup paths contain an unsupported quote'
[[ -d "$STATE_DIR" && ! -L "$STATE_DIR" ]] || fail 'state directory is unavailable'
[[ "$(stat -c '%U:%G' -- "$STATE_DIR")" = root:root && "$(stat -c '%a' -- "$STATE_DIR")" = 755 ]] \
	|| fail 'state directory ownership or mode is invalid'
[[ -d "$DATA_DIR" && ! -L "$DATA_DIR" ]] || fail 'database data directory is unavailable'
[[ "$(stat -c '%U:%G' -- "$DATA_DIR")" = roadmap:roadmap && "$(stat -c '%a' -- "$DATA_DIR")" = 750 ]] \
	|| fail 'database data directory ownership or mode is invalid'
[[ ! -L "$DB_PATH" && ! -L "$BACKUP_DIR" ]] || fail 'database or backup directory must not be a symlink'

# The installer already owns this lock while invoking the helper for its
# pre-deploy backup. Standalone/manual and timer invocations acquire it here
# so they cannot race an install, rollback, or restore.
LOCK_PATH="$STATE_DIR/deploy.lock"
if [[ "$DEPLOY_LOCK_HELD" != 1 ]]; then
	if [[ -e "$LOCK_PATH" || -L "$LOCK_PATH" ]]; then
		[[ -f "$LOCK_PATH" && ! -L "$LOCK_PATH" ]] || fail 'deployment lock is not a regular file'
		[[ "$(stat -c '%U:%G' -- "$LOCK_PATH")" = root:root ]] || fail 'deployment lock owner is invalid'
	fi
	exec 9>"$LOCK_PATH"
	chown root:root "$LOCK_PATH"
	chmod 0600 "$LOCK_PATH"
	flock -x 9
fi

[[ -f "$DB_PATH" && ! -L "$DB_PATH" ]] || fail 'database is missing or not a regular file'
[[ "$(stat -c '%U:%G' -- "$DB_PATH")" = roadmap:roadmap ]] || fail 'database owner is invalid'
command -v sqlite3 >/dev/null 2>&1 || fail 'sqlite3 is required for online backups'
command -v sha256sum >/dev/null 2>&1 || fail 'sha256sum is required for backup integrity'
install -d -m 0700 -o root -g root "$BACKUP_DIR"
[[ "$(stat -c '%U:%G' -- "$BACKUP_DIR")" = root:root && "$(stat -c '%a' -- "$BACKUP_DIR")" = 700 ]] \
	|| fail 'backup directory ownership or mode is invalid'

# An interrupted process may leave a root-owned staging directory or final
# sidecars behind. They cannot be used as backups; clean only entries with the
# expected names, owner, and mode, and leave anything surprising untouched.
cleanup_staging_dirs
cleanup_orphaned_sidecars

timestamp=$(date -u +%Y%m%dT%H%M%SZ)
work=$(mktemp -d "$BACKUP_DIR/.roadmap-backup.XXXXXX")
backup_name="roadmap-${timestamp}-${RELEASE_SHA}.db"
backup_path="$work/$backup_name"
final_backup_path="$BACKUP_DIR/$backup_name"
publication_started=0
published=0

cleanup() {
	local status=$? artifact destination
	# The database is the final rename in the commit sequence. If publication
	# fails before the set is marked published, remove any moved sidecars and
	# database, but never remove a pre-existing or unexpected destination.
	if (( status != 0 && publication_started == 1 && published == 0 )); then
		for artifact in "$backup_name" "$backup_name.sha256" "$backup_name.metadata"; do
			destination="$BACKUP_DIR/$artifact"
			if [[ -f "$destination" && ! -L "$destination" ]]; then
				if [[ "$(stat -c '%U:%G' -- "$destination" 2>/dev/null || true)" = root:root && \
					"$(stat -c '%a' -- "$destination" 2>/dev/null || true)" = 600 ]]; then
					rm -f -- "$destination" || true
				fi
			fi
		done
	fi
	if [[ -n "${work:-}" && -d "$work" && ! -L "$work" ]]; then
		rm -rf -- "$work" || true
	fi
	exit "$status"
}
trap cleanup EXIT
chown root:root "$work"
chmod 0700 "$work"

# Keep a descriptor open while the online backup runs. sqlite3 still opens
# the normal path (so it can include that path's WAL), while the descriptor's
# identity and the pathname's identity are compared before and after the
# operation to detect a replacement race. The deploy lock coordinates with
# installer/restore operations; the application remains online for SQLite's
# consistent backup API.
source_fd=
exec {source_fd}<"$DB_PATH" || fail 'could not open database for backup'
source_fd_path="/proc/$$/fd/$source_fd"
source_identity=$(stat -Lc '%d:%i:%u:%g' -- "$source_fd_path") \
	|| fail 'could not inspect opened database'
[[ "$(stat -Lc '%U:%G' -- "$source_fd_path")" = roadmap:roadmap ]] \
	|| fail 'opened database owner is invalid'
[[ -f "$DB_PATH" && ! -L "$DB_PATH" ]] || fail 'database changed before backup'
[[ "$(stat -Lc '%d:%i:%u:%g' -- "$DB_PATH")" = "$source_identity" ]] \
	|| fail 'database changed before backup'

# `.backup` uses SQLite's online-backup API and therefore captures a WAL
# database consistently even if an operator invokes this helper manually.
sqlite3 -cmd '.timeout 5000' "$DB_PATH" ".backup '$backup_path'" || fail 'SQLite backup failed'
[[ -s "$backup_path" && ! -L "$backup_path" ]] || fail 'SQLite produced an empty backup'
chmod 0600 "$backup_path"
chown root:root "$backup_path"

[[ -f "$DB_PATH" && ! -L "$DB_PATH" ]] || fail 'database changed during backup'
[[ "$(stat -Lc '%d:%i:%u:%g' -- "$DB_PATH")" = "$source_identity" ]] \
	|| fail 'database path changed during backup'
[[ "$(stat -Lc '%d:%i:%u:%g' -- "$source_fd_path")" = "$source_identity" ]] \
	|| fail 'opened database changed during backup'
[[ "$(stat -Lc '%U:%G' -- "$DB_PATH")" = roadmap:roadmap ]] \
	|| fail 'database owner changed during backup'
exec {source_fd}<&-
source_fd=

[[ "$(sqlite3 "$backup_path" 'PRAGMA integrity_check;')" = ok ]] || fail 'SQLite integrity check failed'
schema_version=$(schema_version_from_database "$backup_path") \
	|| fail 'could not inspect completed backup schema version'
[[ "$schema_version" =~ ^[0-9]+$ ]] || fail 'completed backup schema version is invalid'
resolve_migration_digest "$schema_version" || fail 'migration digest is unavailable'
source_schema_digest="$MIGRATION_DIGEST"
[[ "$source_schema_digest" =~ ^[0-9a-f]{64}$ ]] || fail 'source migration digest is invalid'
(cd "$work" && sha256sum "$backup_name" > "$backup_name.sha256") || fail 'could not checksum backup'
printf 'release_sha=%s\ncreated_at=%s\nschema_version=%s\nmigration_digest=%s\n' \
	"$RELEASE_SHA" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$schema_version" "$source_schema_digest" > "$work/$backup_name.metadata"
chmod 0600 "$work/$backup_name.sha256" "$work/$backup_name.metadata"
chown root:root "$work/$backup_name.sha256" "$work/$backup_name.metadata"

# Validate the complete staged set before exposing any final-name artifact.
backup_set_is_complete "$backup_path" || fail 'staged backup set validation failed'

for artifact in "$backup_name" "$backup_name.sha256" "$backup_name.metadata"; do
	destination="$BACKUP_DIR/$artifact"
	[[ ! -e "$destination" && ! -L "$destination" ]] || fail 'backup destination already exists'
done

# POSIX has no multi-file rename. Publish both validated sidecars first and
# rename the database last; a final-name .db therefore never appears without
# its checksum and metadata. A normal failure rolls back moved artifacts, and
# the next invocation cleans orphan sidecars left by a hard process death.
publication_started=1
for artifact in "$backup_name.sha256" "$backup_name.metadata"; do
	destination="$BACKUP_DIR/$artifact"
	mv -T -- "$work/$artifact" "$destination" \
		|| fail "could not publish backup $artifact"
done
mv -T -- "$backup_path" "$final_backup_path" \
	|| fail "could not publish backup $backup_name"
backup_set_is_complete "$final_backup_path" || fail 'published backup set validation failed'
published=1

# Keep the newest complete backups. The glob is constrained to this directory
# and regular files; the predicate also verifies both sidecars, checksum,
# metadata, and SQLite integrity before a set counts toward retention.
mapfile -t candidate_backups < <(find "$BACKUP_DIR" -maxdepth 1 -type f \
	-name 'roadmap-*.db' -printf '%T@ %p\n' | sort -nr)
valid_backups=()
for entry in "${candidate_backups[@]}"; do
	path=${entry#* }
	if backup_set_is_complete "$path"; then
		valid_backups+=("$entry")
	fi
done
if (( ${#valid_backups[@]} > RETENTION )); then
	for entry in "${valid_backups[@]:RETENTION}"; do
		path=${entry#* }
		backup_set_is_legacy "$path" && continue
		remove_backup_set "$path" \
			|| fail "could not prune invalidated backup set $path"
	done
fi

printf 'backup=%s\n' "$BACKUP_DIR/$backup_name"
