#!/usr/bin/env bash
set -Eeuo pipefail

RELEASE_DIR=${1:-/root/roadmap-release}
STATE_DIR=/var/lib/roadmap
DATA_DIR="$STATE_DIR/data"
RELEASES_DIR="$STATE_DIR/releases"
BACKUPS_DIR="$STATE_DIR/backups"
CURRENT_LINK="$STATE_DIR/current"
CONFIG_DIR=/etc/roadmap
LOCK_PATH="$STATE_DIR/deploy.lock"
SERVICE_STOP_TIMEOUT=30

log() { printf '[helm-install] %s\n' "$*"; }
fail() { log "$*" >&2; exit 1; }

compat_env() {
	local canonical=$1 legacy=$2 default_value=${3:-} canonical_value legacy_value
	canonical_value=${!canonical:-}
	legacy_value=${!legacy:-}
	[[ -z "$canonical_value" || -z "$legacy_value" || "$canonical_value" = "$legacy_value" ]] ||
		fail "$canonical and $legacy must match when both are set"
	printf '%s' "${canonical_value:-${legacy_value:-$default_value}}"
}

unit_state() {
	systemctl is-active "$1" 2>/dev/null || true
}

stop_unit() {
	local unit=$1 state load_state
	load_state=$(systemctl show "$unit" --property=LoadState --value 2>/dev/null) || return 1
	[[ "$load_state" = not-found ]] && return 0
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

release_revision() {
	local path=$1 helm_count roadmap_count helm_revision roadmap_revision revision
	[[ -f "$path" && ! -L "$path" ]] || return 1
	helm_count=$(awk -F= '$1 == "HELM_RELEASE_SHA" { count++ } END { print count + 0 }' "$path") || return 1
	roadmap_count=$(awk -F= '$1 == "ROADMAP_RELEASE_SHA" { count++ } END { print count + 0 }' "$path") || return 1
	helm_revision=$(awk -F= '$1 == "HELM_RELEASE_SHA" { print substr($0, index($0, "=") + 1) }' "$path") || return 1
	roadmap_revision=$(awk -F= '$1 == "ROADMAP_RELEASE_SHA" { print substr($0, index($0, "=") + 1) }' "$path") || return 1
	[[ "$helm_count" -le 1 && "$roadmap_count" -le 1 ]] || return 1
	[[ "$helm_count" -eq 1 || "$roadmap_count" -eq 1 ]] || return 1
	if [[ "$helm_count" -eq 1 && "$roadmap_count" -eq 1 ]]; then
		[[ "$helm_revision" = "$roadmap_revision" ]] || return 1
	fi
	revision=${helm_revision:-$roadmap_revision}
	[[ "$revision" =~ ^[0-9a-f]{40}$ ]] || return 1
	printf '%s' "$revision"
}

release_binary_name() {
	local target=$1
	if [[ -x "$target/helm" && ! -L "$target/helm" && -f "$target/helm.sha256" && ! -L "$target/helm.sha256" ]]; then
		printf 'helm'
		return 0
	fi
	if [[ -x "$target/roadmap" && ! -L "$target/roadmap" && -f "$target/roadmap.sha256" && ! -L "$target/roadmap.sha256" ]]; then
		printf 'roadmap'
		return 0
	fi
	return 1
}

verify_release_binary() {
	local target=$1 binary
	binary=$(release_binary_name "$target") || return 1
	(cd "$target" && sha256sum --check --strict "$binary.sha256" >/dev/null)
}

verify_codex_binary() {
	local target=$1
	[[ -x "$target/codex" && ! -L "$target/codex" ]] || return 1
	[[ -f "$target/codex.sha256" && ! -L "$target/codex.sha256" ]] || return 1
	(cd "$target" && sha256sum --check --strict codex.sha256 >/dev/null)
}

validate_release_env() {
	local path=$1 expected=$2 revision
	revision=$(release_revision "$path") || return 1
	[[ "$revision" = "$expected" ]] || return 1
}

install_release_env() {
	local target=$1 expected=$2 temporary="$CONFIG_DIR/roadmap.env.new"
	validate_release_env "$target/roadmap.env" "$expected" || return 1
	[[ ! -e "$temporary" && ! -L "$temporary" ]] || return 1
	install -m 0640 -o root -g roadmap "$target/roadmap.env" "$temporary" || return 1
	mv -T -- "$temporary" "$CONFIG_DIR/roadmap.env" || return 1
}

single_kv_value() {
	local path=$1 key=$2
	awk -F= -v key="$key" '
		$1 == key { count++; value = $2 }
		END {
			if (count != 1) exit 1
			print value
		}
	' "$path"
}

single_output_value() {
	local output=$1 key=$2
	awk -F= -v key="$key" '
		$1 == key { count++; value = $2 }
		END {
			if (count != 1) exit 1
			print value
		}
	' <<<"$output"
}

[[ "$(id -u)" -eq 0 ]] || fail 'must run as root'
[[ "$RELEASE_DIR" = /* && "$RELEASE_DIR" != *$'\n'* && "$RELEASE_DIR" != *$'\r'* ]] \
	|| fail 'release directory must be an absolute path without control characters'
[[ "$RELEASE_DIR" != *'/../'* && "$RELEASE_DIR" != */.. ]] || fail 'release path traversal is not allowed'
[[ -d "$RELEASE_DIR" && ! -L "$RELEASE_DIR" ]] || fail 'release directory is missing'

# The release is intentionally tied to the reviewed production image.
# shellcheck disable=SC1091
source /etc/os-release
[[ "$ID" = debian && "${VERSION_ID%%.*}" = 12 ]] \
	|| fail 'Helm production requires Debian 12'

for file in roadmap cloudflared cloudflared.token codex codex.sha256 roadmap.env roadmap.service cloudflared.service roadmap-backup.service roadmap-backup.timer nftables.conf compose.yaml roadmap-backup.sh roadmap-restore.sh roadmap-rollback.sh release.sha roadmap.sha256 release.manifest release.manifest.sig; do
	[[ -f "$RELEASE_DIR/$file" && ! -L "$RELEASE_DIR/$file" ]] || fail "release member is missing: $file"
done

SHA=$(tr -d '[:space:]' < "$RELEASE_DIR/release.sha")
[[ "$SHA" =~ ^[0-9a-f]{40}$ ]] || fail 'release SHA is invalid'
[[ -x "$RELEASE_DIR/roadmap" && -x "$RELEASE_DIR/cloudflared" && -x "$RELEASE_DIR/codex" ]] || fail 'release binaries must be executable'
sha256sum "$RELEASE_DIR/roadmap" >/dev/null || fail 'could not hash release binary'
(cd "$RELEASE_DIR" && sha256sum --check --strict codex.sha256 >/dev/null) || fail 'release Codex checksum failed'
validate_release_env "$RELEASE_DIR/roadmap.env" "$SHA"

export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install --yes --no-install-recommends bubblewrap ca-certificates curl nftables sqlite3 unattended-upgrades
apt-get clean
rm -rf -- /var/lib/apt/lists/*

getent group roadmap >/dev/null 2>&1 || groupadd --system roadmap
roadmap_user_existing=0
if ! id roadmap >/dev/null 2>&1; then
	useradd --system --gid roadmap --home-dir "$STATE_DIR" --shell /usr/sbin/nologin --no-create-home roadmap
else
	roadmap_user_existing=1
fi
[[ "$(id -gn roadmap)" = roadmap ]] || fail 'roadmap user must use the roadmap primary group'
if (( roadmap_user_existing == 1 )); then
	usermod --groups '' roadmap || fail 'could not clear roadmap supplemental groups'
fi
[[ "$(id -Gn roadmap)" = roadmap ]] || fail 'roadmap user has unexpected supplemental groups'
getent group cloudflared >/dev/null 2>&1 || groupadd --system cloudflared
cloudflared_user_existing=0
if ! id cloudflared >/dev/null 2>&1; then
	useradd --system --gid cloudflared --home-dir /nonexistent --shell /usr/sbin/nologin --no-create-home cloudflared
else
	cloudflared_user_existing=1
fi
[[ "$(id -gn cloudflared)" = cloudflared ]] || fail 'cloudflared user must use the cloudflared primary group'
if (( cloudflared_user_existing == 1 )); then
	usermod --groups '' cloudflared || fail 'could not clear cloudflared supplemental groups'
fi
[[ "$(id -Gn cloudflared)" = cloudflared ]] || fail 'cloudflared user has unexpected supplemental groups'

for directory in "$STATE_DIR" "$DATA_DIR" "$RELEASES_DIR" "$BACKUPS_DIR" "$CONFIG_DIR"; do
	[[ ! -L "$directory" && ( ! -e "$directory" || -d "$directory" ) ]] \
		|| fail "Roadmap path is not a directory: $directory"
done
install -d -m 0755 -o root -g root "$STATE_DIR"
install -d -m 0750 -o roadmap -g roadmap "$DATA_DIR"
install -d -m 0755 -o root -g root "$RELEASES_DIR"
install -d -m 0700 -o root -g root "$BACKUPS_DIR"
install -d -m 0711 -o root -g root "$CONFIG_DIR"

# Deployment, rollback, and database restore all mutate the same release and
# database state. Serialize them inside the guest as well as at the PVE
# gateway; otherwise an approved console operator could race an install after
# the host-side lock was released.
if [[ -e "$LOCK_PATH" || -L "$LOCK_PATH" ]]; then
	[[ -f "$LOCK_PATH" && ! -L "$LOCK_PATH" ]] || fail 'deployment lock is not a regular file'
	[[ "$(stat -c '%U:%G' -- "$LOCK_PATH")" = root:root ]] || fail 'deployment lock owner is invalid'
fi
exec 9>"$LOCK_PATH"
chown root:root "$LOCK_PATH"
chmod 0600 "$LOCK_PATH"
flock -x 9

if [[ -L "$STATE_DIR/roadmap.db" || -L "$STATE_DIR/roadmap.db-wal" || -L "$STATE_DIR/roadmap.db-shm" ]]; then
	fail 'legacy database paths must not be symlinks'
fi

previous_target=
if [[ -L "$CURRENT_LINK" ]]; then
	previous_target=$(readlink "$CURRENT_LINK")
	[[ "$previous_target" = "$RELEASES_DIR"/* ]] || fail 'current release link escapes release directory'
	[[ -d "$previous_target" && ! -L "$previous_target" ]] || fail 'current release target is unavailable'
elif [[ -e "$CURRENT_LINK" ]]; then
	fail 'current release path is not a symlink'
fi

previous_sha=
if [[ -n "$previous_target" ]]; then
	previous_sha=${previous_target##*/}
	[[ "$previous_sha" =~ ^[0-9a-f]{40}$ ]] || fail 'current release directory name is invalid'
fi

migrate_previous_env() {
	[[ -n "$previous_target" ]] || return 0

	local previous_env="$previous_target/roadmap.env"
	if [[ -e "$previous_env" || -L "$previous_env" ]]; then
		[[ -f "$previous_env" && ! -L "$previous_env" ]] || fail 'current release environment path is invalid'
	else
		local current_env="$CONFIG_DIR/roadmap.env"
		local helm_count roadmap_count current_revision migration
		[[ -f "$current_env" && ! -L "$current_env" ]] || fail 'current release environment is unavailable'
		helm_count=$(awk -F= '$1 == "HELM_RELEASE_SHA" { count++ } END { print count + 0 }' "$current_env") || fail 'cannot inspect current release environment'
		roadmap_count=$(awk -F= '$1 == "ROADMAP_RELEASE_SHA" { count++ } END { print count + 0 }' "$current_env") || fail 'cannot inspect current release environment'
		if current_revision=$(release_revision "$current_env"); then
			[[ "$current_revision" = "$previous_sha" ]] || fail 'current release environment revision does not match the current release'
		else
			[[ "$helm_count" = 0 && "$roadmap_count" = 0 ]] || fail 'current release environment has invalid release revisions'
			migration="$STATE_DIR/.helm-env-$previous_sha.$$"
			[[ ! -e "$migration" && ! -L "$migration" ]] || fail 'temporary current release environment path already exists'
			install -m 0640 -o root -g root "$current_env" "$migration"
			printf '\nROADMAP_RELEASE_SHA=%s\n' "$previous_sha" >> "$migration"
			mv -T -- "$migration" "$previous_env"
		fi
	fi
	validate_release_env "$previous_env" "$previous_sha"
}

new_target="$RELEASES_DIR/.helm-$SHA.$$"
if [[ -e "$RELEASES_DIR/$SHA" || -L "$RELEASES_DIR/$SHA" ]]; then
	[[ -d "$RELEASES_DIR/$SHA" && ! -L "$RELEASES_DIR/$SHA" ]] || fail 'retained release is not a directory'
	retained_binary=$(release_binary_name "$RELEASES_DIR/$SHA") || fail 'retained release binary is invalid'
	[[ -f "$RELEASES_DIR/$SHA/release.manifest" && ! -L "$RELEASES_DIR/$SHA/release.manifest" ]] || fail 'retained release has no signed manifest'
	[[ -f "$RELEASES_DIR/$SHA/release.manifest.sig" && ! -L "$RELEASES_DIR/$SHA/release.manifest.sig" ]] || fail 'retained release has no manifest signature'
	verify_release_binary "$RELEASES_DIR/$SHA" || fail 'retained release checksum failed'
	verify_codex_binary "$RELEASES_DIR/$SHA" || fail 'retained release Codex checksum failed'
	if [[ "$(sha256sum "$RELEASES_DIR/$SHA/$retained_binary" | awk '{print $1}')" != "$(sha256sum "$RELEASE_DIR/roadmap" | awk '{print $1}')" ]]; then
		fail 'same SHA was previously retained with different bytes'
	fi
	if [[ "$(sha256sum "$RELEASES_DIR/$SHA/codex" | awk '{print $1}')" != "$(sha256sum "$RELEASE_DIR/codex" | awk '{print $1}')" ]]; then
		fail 'same SHA was previously retained with different Codex bytes'
	fi
	if [[ -e "$RELEASES_DIR/$SHA/roadmap.env" || -L "$RELEASES_DIR/$SHA/roadmap.env" ]]; then
		[[ -f "$RELEASES_DIR/$SHA/roadmap.env" && ! -L "$RELEASES_DIR/$SHA/roadmap.env" ]] ||
			fail 'retained release environment path is invalid'
		cmp -s "$RELEASE_DIR/roadmap.env" "$RELEASES_DIR/$SHA/roadmap.env" ||
			fail 'same SHA was previously retained with different environment'
	else
		install -m 0640 -o root -g root "$RELEASE_DIR/roadmap.env" "$RELEASES_DIR/$SHA/roadmap.env"
	fi
	validate_release_env "$RELEASES_DIR/$SHA/roadmap.env" "$SHA"
	release_target="$RELEASES_DIR/$SHA"
else
	install -d -m 0755 -o root -g root "$new_target"
	install -m 0755 -o root -g root "$RELEASE_DIR/roadmap" "$new_target/helm"
	install -m 0755 -o root -g root "$RELEASE_DIR/codex" "$new_target/codex"
	install -m 0644 -o root -g root "$RELEASE_DIR/codex.sha256" "$new_target/codex.sha256"
	install -m 0644 -o root -g root "$RELEASE_DIR/release.sha" "$new_target/release.sha"
	(cd "$new_target" && sha256sum helm > helm.sha256)
	chmod 0644 "$new_target/helm.sha256"
	install -m 0644 -o root -g root "$RELEASE_DIR/release.manifest" "$new_target/release.manifest"
	install -m 0644 -o root -g root "$RELEASE_DIR/release.manifest.sig" "$new_target/release.manifest.sig"
	install -m 0640 -o root -g root "$RELEASE_DIR/roadmap.env" "$new_target/roadmap.env"
	verify_release_binary "$new_target" || fail 'new release binary checksum failed'
	verify_codex_binary "$new_target" || fail 'new release Codex checksum failed'
	mv -T -- "$new_target" "$RELEASES_DIR/$SHA"
	release_target="$RELEASES_DIR/$SHA"
fi

atomic_switch() {
	local target=$1 tmp="$STATE_DIR/.current.$$"
	[[ ! -e "$tmp" && ! -L "$tmp" ]] || return 1
	ln -s -- "$target" "$tmp" || return 1
	if ! mv -T -- "$tmp" "$CURRENT_LINK"; then
		rm -f -- "$tmp"
		return 1
	fi
}

install_helm_runner() {
	local temporary=/usr/local/sbin/helm-run-current.new
	[[ ! -e "$temporary" && ! -L "$temporary" ]] || return 1
	# Retained Roadmap releases remain immutable and executable during the
	# transition. The fixed-path runner selects the canonical Helm binary for
	# new releases and the legacy binary for a verified rollback target.
	cat > "$temporary" <<'RUNNER'
#!/usr/bin/env bash
set -Eeuo pipefail

STATE_DIR=/var/lib/roadmap
RELEASES_DIR="$STATE_DIR/releases"
CURRENT_LINK="$STATE_DIR/current"

fail() {
	printf '[helm-run-current] %s\n' "$*" >&2
	exit 1
}

[[ -L "$CURRENT_LINK" ]] || fail 'current release link is unavailable'
target=$(readlink "$CURRENT_LINK") || fail 'current release link cannot be read'
[[ "$target" = "$RELEASES_DIR"/* ]] || fail 'current release link escapes release directory'
[[ -d "$target" && ! -L "$target" ]] || fail 'current release target is invalid'
sha=${target##*/}
[[ "$sha" =~ ^[0-9a-f]{40}$ ]] || fail 'current release directory name is invalid'

if [[ -x "$target/helm" && ! -L "$target/helm" ]]; then
	binary="$target/helm"
	checksum="$target/helm.sha256"
elif [[ -x "$target/roadmap" && ! -L "$target/roadmap" ]]; then
	binary="$target/roadmap"
	checksum="$target/roadmap.sha256"
else
	fail 'current release has no executable Helm-compatible binary'
fi
[[ -f "$checksum" && ! -L "$checksum" ]] || fail 'current release has no binary checksum'
(cd "$target" && sha256sum --check --strict "${checksum##*/}" >/dev/null) ||
	fail 'current release binary checksum failed'
exec "$binary" "$@"
RUNNER
	chown root:root "$temporary" || return 1
	chmod 0755 "$temporary" || return 1
	mv -T -- "$temporary" /usr/local/sbin/helm-run-current
}

install_compat_symlink() {
	local canonical=$1 legacy=$2 temporary
	temporary="/usr/local/sbin/.${legacy}.new.$$"
	[[ "$canonical" =~ ^helm-[a-z-]+$ && "$legacy" =~ ^roadmap-[a-z-]+$ ]] || return 1
	[[ ! -e "$temporary" && ! -L "$temporary" ]] || return 1
	ln -s -- "$canonical" "$temporary" || return 1
	if ! mv -T -- "$temporary" "/usr/local/sbin/$legacy"; then
		rm -f -- "$temporary"
		return 1
	fi
}

install_unit_alias() {
	local canonical=$1 legacy=$2 temporary
	temporary="/etc/systemd/system/.${legacy}.new.$$"
	[[ "$canonical" =~ ^helm[-a-z]*\.(service|timer)$ && "$legacy" =~ ^roadmap[-a-z]*\.(service|timer)$ ]] || return 1
	[[ ! -e "$temporary" && ! -L "$temporary" ]] || return 1
	ln -s -- "$canonical" "$temporary" || return 1
	if ! mv -T -- "$temporary" "/etc/systemd/system/$legacy"; then
		rm -f -- "$temporary"
		return 1
	fi
}

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

healthy_revision() {
	local expected=$1 attempt response
	for attempt in $(seq 1 30); do
		if response=$(curl --fail --silent --show-error --max-time 3 --dump-header - http://127.0.0.1:8080/healthz) &&
			grep -Eq '^X-Roadmap-Revision:[[:space:]]*'"$expected"'[[:space:]]*$' <<<"$response" &&
			grep -Eq '"revision"[[:space:]]*:[[:space:]]*"'"$expected"'"' <<<"$response"; then
			return 0
		fi
		sleep 1
	done
	return 1
}

loopback_listener() {
	ss -ltn | grep -Eq '^[[:space:]]*LISTEN[[:space:]]+[^[:space:]]+[[:space:]]+[^[:space:]]+[[:space:]]+(127\.0\.0\.1:8080|\[::1\]:8080)([[:space:]]|$)'
}

# Roadmap does not provide mail delivery. Disable and mask the Debian Postfix
# aggregate, template, and default instance units independently so an absent
# or already-masked unit cannot make install fail and package actions cannot
# reactivate this unused local SMTP service.
disable_unused_postfix() {
	local unit state enabled_state
	for unit in postfix.service postfix@-.service; do
		state=$(unit_state "$unit")
		case "$state" in
			active|activating|deactivating)
				stop_unit "$unit" || fail "could not stop $unit and verify it is inactive"
				;;
			inactive|failed|dead|unknown|not-found)
				;;
			*)
				fail "unexpected $unit active state: $state"
				;;
		esac
	done

	for unit in postfix.service postfix@.service postfix@-.service; do
		enabled_state=$(systemctl is-enabled "$unit" 2>/dev/null || true)
		case "$enabled_state" in
			enabled|enabled-runtime|linked|linked-runtime|alias)
				systemctl disable "$unit" 2>/dev/null || fail "could not disable $unit"
				;;
			disabled|static|generated|transient|indirect|masked|not-found)
				;;
			*)
				fail "unexpected $unit enablement state: ${enabled_state:-unknown}"
				;;
		esac
		systemctl mask "$unit" || fail "could not mask $unit"
	done

	for unit in postfix.service postfix@.service postfix@-.service; do
		enabled_state=$(systemctl is-enabled "$unit" 2>/dev/null || true)
		[[ "$enabled_state" = masked ]] || fail "$unit is not masked"
	done
	for unit in postfix.service postfix@-.service; do
		state=$(unit_state "$unit")
		case "$state" in
			inactive|failed|dead|unknown|not-found)
				;;
			*)
				fail "$unit remains active after it was masked"
				;;
		esac
	done
}

restore_previous() {
	local app_unit=helm.service
	[[ -n "$previous_target" ]] || return 1
	stop_unit cloudflared.service || return 1
	stop_unit roadmap.service || return 1
	stop_unit helm.service || return 1
	install_release_env "$previous_target" "$previous_sha" || return 1
	atomic_switch "$previous_target" || return 1
	if ! systemctl start "$app_unit"; then
		# On the first Helm upgrade the canonical unit may not have been
		# installed yet. The retained Roadmap unit is still the recovery path.
		app_unit=roadmap.service
		systemctl start "$app_unit" || return 1
	fi
	healthy_revision "$previous_sha" || return 1
	systemctl is-active --quiet "$app_unit" || return 1
	systemctl start cloudflared.service || return 1
	systemctl is-active --quiet cloudflared.service
}

# Any failure after services have been stopped must leave an existing guest
# serving the previous release. The explicit health branches below provide
# useful diagnostics; this EXIT trap covers package/configuration failures too.
recovery_needed=1
recovery_in_progress=0
upgrade_transaction_started=0
on_exit() {
	local status=$?
	# Recovery itself must not recursively invoke this EXIT transaction.
	trap - EXIT
	rm -f -- "$CONFIG_DIR/roadmap.env.new" || true
	rm -f -- /usr/local/sbin/helm-run-current.new || true
	if [[ -n "${fresh_preflight_source:-}" ]]; then
		rm -f -- "$fresh_preflight_source" || true
	fi
	if [[ "$status" -ne 0 && "$recovery_needed" -eq 1 && "$upgrade_transaction_started" -eq 1 && "$recovery_in_progress" -eq 0 && -n "$previous_target" ]]; then
		recovery_in_progress=1
		log 'deployment failed before completion; restoring the previous release'
		restore_previous || log 'previous release recovery failed; inspect systemd and the local health endpoint'
		recovery_in_progress=0
	fi
	exit "$status"
}
trap on_exit EXIT

migrate_previous_env

# Automatic upgrades support only the current data-directory layout. Moving a
# live legacy SQLite pathname safely would require an offline maintenance
# transaction and a rollback-environment rewrite. Refuse that obsolete layout
# before stopping either service; an operator can then schedule an explicit,
# separately backed-up maintenance migration without weakening the normal
# pre-downtime backup/preflight guarantee.
if [[ -e "$STATE_DIR/roadmap.db" ]]; then
	[[ -f "$STATE_DIR/roadmap.db" ]] || fail 'legacy database path is not a regular file'
	[[ ! -e "$DATA_DIR/roadmap.db" ]] || fail 'both legacy and data database paths exist'
	fail 'legacy database layout requires an explicit offline maintenance migration; no services were stopped'
fi

# Take and verify the pre-upgrade backup while Helm is still online. The
# helper records the source schema version and migration digest, then the new
# binary migrates a private copy of that exact backup before any service stop
# or release-link switch.
verified_backup=
fresh_preflight_source=
proof_backup=none
proof_source_schema=
proof_candidate_schema=
proof_latest_schema=
proof_migration_digest=
proof_checksum=
proof_integrity=
proof_fk=
proof_preflight=
if [[ -f "$DATA_DIR/roadmap.db" && ! -L "$DATA_DIR/roadmap.db" ]]; then
	# The candidate binary owns the metadata format. A retained prior binary
	# may predate migration-info, especially on the first TC-33 deployment.
	migration_info_binary="$RELEASE_DIR/roadmap"
	backup_retention=$(compat_env HELM_BACKUP_RETENTION ROADMAP_BACKUP_RETENTION 14)
	backup_output=$(HELM_STATE_DIR="$STATE_DIR" HELM_DATA_DIR="$DATA_DIR" HELM_DB_PATH="$DATA_DIR/roadmap.db" HELM_BACKUP_DIR="$BACKUPS_DIR" HELM_DEPLOY_LOCK_HELD=1 \
		HELM_BACKUP_RETENTION="$backup_retention" HELM_MIGRATION_INFO_BINARY="$migration_info_binary" HELM_MIGRATION_DIGEST= \
		"$RELEASE_DIR/roadmap-backup.sh" "$SHA") \
		|| fail 'could not create the verified pre-upgrade backup'
	[[ "$(grep -Ec '^backup=/[^[:cntrl:]]+$' <<<"$backup_output")" = 1 ]] \
		|| fail 'backup helper returned an invalid result'
	verified_backup=${backup_output#backup=}
	[[ "$verified_backup" = "$BACKUPS_DIR"/roadmap-*.db && -f "$verified_backup" && ! -L "$verified_backup" ]] \
		|| fail 'verified backup path is invalid'
	proof_backup=$(basename -- "$verified_backup")
	[[ "$proof_backup" =~ ^roadmap-[0-9]{8}T[0-9]{6}Z-(manual|daily|pre-restore|[0-9a-f]{40})\.db$ ]] \
		|| fail 'verified backup basename is invalid'
	checksum_sidecar="$verified_backup.sha256"
	metadata_sidecar="$verified_backup.metadata"
	[[ -f "$checksum_sidecar" && ! -L "$checksum_sidecar" && -f "$metadata_sidecar" && ! -L "$metadata_sidecar" ]] \
		|| fail 'verified backup sidecars are missing'
	[[ "$(stat -c '%U:%G' -- "$checksum_sidecar")" = root:root && "$(stat -c '%a' -- "$checksum_sidecar")" = 600 ]] \
		|| fail 'verified backup checksum sidecar ownership or mode is invalid'
	[[ "$(stat -c '%U:%G' -- "$metadata_sidecar")" = root:root && "$(stat -c '%a' -- "$metadata_sidecar")" = 600 ]] \
		|| fail 'verified backup metadata sidecar ownership or mode is invalid'
	checksum_count=$(awk -v name="$proof_backup" 'NF == 2 && $2 == name { count++ } END { print count + 0 }' "$checksum_sidecar") \
		|| fail 'verified backup checksum sidecar cannot be read'
	[[ "$checksum_count" = 1 ]] || fail 'verified backup checksum sidecar names an unexpected file'
	checksum_digest=$(awk -v name="$proof_backup" 'NF == 2 && $2 == name { print $1 }' "$checksum_sidecar") \
		|| fail 'verified backup checksum sidecar cannot be read'
	[[ "$checksum_digest" =~ ^[0-9a-f]{64}$ ]] || fail 'verified backup checksum is invalid'
	(cd "$BACKUPS_DIR" && sha256sum --check --strict "$(basename -- "$checksum_sidecar")" >/dev/null) \
		|| fail 'verified backup checksum validation failed'
	proof_source_schema=$(single_kv_value "$metadata_sidecar" schema_version) \
		|| fail 'verified backup source schema metadata is invalid'
	source_migration_digest=$(single_kv_value "$metadata_sidecar" migration_digest) \
		|| fail 'verified backup migration metadata is invalid'
	[[ "$proof_source_schema" =~ ^[0-9]+$ ]] || fail 'verified backup source schema is invalid'
	[[ "$source_migration_digest" =~ ^[0-9a-f]{64}$ ]] || fail 'verified backup migration digest is invalid'
	[[ "$(sqlite3 "$verified_backup" 'PRAGMA integrity_check;')" = ok ]] \
		|| fail 'verified backup integrity validation failed'
	[[ -z "$(sqlite3 "$verified_backup" 'PRAGMA foreign_key_check;')" ]] \
		|| fail 'verified backup foreign-key validation failed'
	proof_checksum=valid
	proof_integrity=ok
	proof_fk=ok
	preflight_output=$("$RELEASE_DIR/roadmap" schema-preflight "$verified_backup") \
		|| fail 'candidate schema preflight failed'
	proof_candidate_schema=$(single_output_value "$preflight_output" schema_version) \
		|| fail 'candidate schema preflight returned an invalid schema'
	proof_latest_schema=$(single_output_value "$preflight_output" latest_schema_version) \
		|| fail 'candidate schema preflight returned an invalid latest schema'
	preflight_digest=$(single_output_value "$preflight_output" migration_digest) \
		|| fail 'candidate schema preflight returned an invalid migration digest'
	preflight_integrity=$(single_output_value "$preflight_output" integrity_check) \
		|| fail 'candidate schema preflight returned an invalid integrity result'
	preflight_fk=$(single_output_value "$preflight_output" foreign_key_check) \
		|| fail 'candidate schema preflight returned an invalid foreign-key result'
	preflight_status=$(single_output_value "$preflight_output" status) \
		|| fail 'candidate schema preflight returned an invalid status'
	[[ "$proof_candidate_schema" =~ ^[0-9]+$ && "$proof_latest_schema" =~ ^[0-9]+$ ]] \
		|| fail 'candidate schema preflight returned non-numeric schema versions'
	[[ "$proof_candidate_schema" = "$proof_latest_schema" ]] \
		|| fail 'candidate schema preflight did not reach the latest schema'
	[[ "$preflight_digest" =~ ^[0-9a-f]{64}$ ]] \
		|| fail 'candidate schema preflight migration digest is invalid'
	# The backup sidecar identifies the retained source binary's migration set;
	# the preflight digest identifies the candidate set. They are expected to
	# differ when this deploy adds a migration, so the public proof records the
	# candidate digest after both artifacts have been validated independently.
	proof_migration_digest=$preflight_digest
	[[ "$preflight_integrity" = ok && "$preflight_fk" = ok && "$preflight_status" = ok ]] \
		|| fail 'candidate schema preflight integrity validation failed'
	proof_preflight=ok
elif [[ -e "$DATA_DIR/roadmap.db" ]]; then
	fail 'database path is not a regular file'
else
	# A fresh guest has no online source to back up. Exercise the same candidate
	# migration/preflight gate against a disposable empty SQLite database so the
	# proof still records the candidate schema, digest, and integrity predicates.
	fresh_preflight_source=$(mktemp "$STATE_DIR/.helm-preflight.XXXXXX") \
		|| fail 'could not create fresh-install preflight source'
	chmod 0600 "$fresh_preflight_source"
	sqlite3 "$fresh_preflight_source" 'VACUUM;' \
		|| fail 'could not initialize fresh-install preflight source'
	proof_source_schema=0
	[[ "$(sqlite3 "$fresh_preflight_source" 'PRAGMA integrity_check;')" = ok ]] \
		|| fail 'fresh-install preflight source integrity validation failed'
	[[ -z "$(sqlite3 "$fresh_preflight_source" 'PRAGMA foreign_key_check;')" ]] \
		|| fail 'fresh-install preflight source foreign-key validation failed'
	sha256sum "$fresh_preflight_source" >/dev/null \
		|| fail 'could not checksum fresh-install preflight source'
	proof_checksum=valid
	proof_integrity=ok
	proof_fk=ok
	preflight_output=$("$RELEASE_DIR/roadmap" schema-preflight "$fresh_preflight_source") \
		|| fail 'fresh-install candidate schema preflight failed'
	proof_candidate_schema=$(single_output_value "$preflight_output" schema_version) \
		|| fail 'fresh-install preflight returned an invalid schema'
	proof_latest_schema=$(single_output_value "$preflight_output" latest_schema_version) \
		|| fail 'fresh-install preflight returned an invalid latest schema'
	preflight_digest=$(single_output_value "$preflight_output" migration_digest) \
		|| fail 'fresh-install preflight returned an invalid migration digest'
	preflight_integrity=$(single_output_value "$preflight_output" integrity_check) \
		|| fail 'fresh-install preflight returned an invalid integrity result'
	preflight_fk=$(single_output_value "$preflight_output" foreign_key_check) \
		|| fail 'fresh-install preflight returned an invalid foreign-key result'
	preflight_status=$(single_output_value "$preflight_output" status) \
		|| fail 'fresh-install preflight returned an invalid status'
	[[ "$proof_candidate_schema" =~ ^[0-9]+$ && "$proof_latest_schema" =~ ^[0-9]+$ ]] \
		|| fail 'fresh-install preflight returned non-numeric schema versions'
	[[ "$proof_candidate_schema" = "$proof_latest_schema" ]] \
		|| fail 'fresh-install preflight did not reach the latest schema'
	[[ "$preflight_digest" =~ ^[0-9a-f]{64}$ ]] \
		|| fail 'fresh-install preflight migration digest is invalid'
	proof_migration_digest=$preflight_digest
	[[ "$preflight_integrity" = ok && "$preflight_fk" = ok && "$preflight_status" = ok ]] \
		|| fail 'fresh-install preflight integrity validation failed'
	proof_preflight=ok
fi

[[ "$proof_backup" = none || "$proof_backup" =~ ^roadmap-[0-9]{8}T[0-9]{6}Z-(manual|daily|pre-restore|[0-9a-f]{40})\.db$ ]] \
	|| fail 'pre-upgrade backup proof has an invalid basename'
[[ "$proof_source_schema" =~ ^[0-9]+$ && "$proof_candidate_schema" =~ ^[0-9]+$ && "$proof_latest_schema" =~ ^[0-9]+$ ]] \
	|| fail 'pre-upgrade backup proof has invalid schema versions'
[[ "$proof_migration_digest" =~ ^[0-9a-f]{64}$ ]] || fail 'pre-upgrade backup proof has an invalid migration digest'
[[ "$proof_checksum" = valid && "$proof_integrity" = ok && "$proof_fk" = ok && "$proof_preflight" = ok ]] \
	|| fail 'pre-upgrade backup proof predicates are incomplete'
printf 'pre_upgrade_backup=%s source_schema=%s candidate_schema=%s latest_schema=%s migration_digest=%s checksum=%s integrity=%s fk=%s preflight=%s\n' \
	"$proof_backup" "$proof_source_schema" "$proof_candidate_schema" "$proof_latest_schema" \
	"$proof_migration_digest" "$proof_checksum" "$proof_integrity" "$proof_fk" "$proof_preflight"

log 'Stopping the application before the atomic release switch'
upgrade_transaction_started=1
stop_unit cloudflared.service || fail 'could not stop cloudflared.service and verify it is inactive'
stop_unit roadmap-backup.timer || fail 'could not stop roadmap-backup.timer and verify it is inactive'
stop_unit helm-backup.timer || fail 'could not stop helm-backup.timer and verify it is inactive'
stop_unit roadmap.service || fail 'could not stop roadmap.service and verify it is inactive'
stop_unit helm.service || fail 'could not stop helm.service and verify it is inactive'

# Configuration and helpers are installed atomically.  The tunnel token is
# copied only into the LXC's root-owned configuration directory and is never
# retained in a release directory.
install -m 0755 -o root -g root "$RELEASE_DIR/roadmap-backup.sh" /usr/local/sbin/helm-backup
install -m 0755 -o root -g root "$RELEASE_DIR/roadmap-restore.sh" /usr/local/sbin/helm-restore
install -m 0755 -o root -g root "$RELEASE_DIR/roadmap-rollback.sh" /usr/local/sbin/helm-rollback
install_helm_runner || fail 'could not install the dual-generation Helm runner'
install_compat_symlink helm-backup roadmap-backup || fail 'could not install roadmap-backup compatibility alias'
install_compat_symlink helm-restore roadmap-restore || fail 'could not install roadmap-restore compatibility alias'
install_compat_symlink helm-rollback roadmap-rollback || fail 'could not install roadmap-rollback compatibility alias'
install -m 0644 -o root -g root "$RELEASE_DIR/roadmap.service" /etc/systemd/system/helm.service
install -m 0644 -o root -g root "$RELEASE_DIR/cloudflared.service" /etc/systemd/system/cloudflared.service
install -m 0644 -o root -g root "$RELEASE_DIR/roadmap-backup.service" /etc/systemd/system/helm-backup.service
install -m 0644 -o root -g root "$RELEASE_DIR/roadmap-backup.timer" /etc/systemd/system/helm-backup.timer
for legacy_unit in roadmap.service roadmap-backup.service roadmap-backup.timer; do
	[[ ! -d "/etc/systemd/system/$legacy_unit" ]] || fail "legacy unit alias path is a directory: $legacy_unit"
	rm -f -- "/etc/systemd/system/$legacy_unit"
done
install_unit_alias helm.service roadmap.service || fail 'could not install roadmap.service compatibility alias'
install_unit_alias helm-backup.service roadmap-backup.service || fail 'could not install roadmap-backup.service compatibility alias'
install_unit_alias helm-backup.timer roadmap-backup.timer || fail 'could not install roadmap-backup.timer compatibility alias'
install -m 0644 -o root -g root "$RELEASE_DIR/nftables.conf" /etc/nftables.conf
install_release_env "$release_target" "$SHA" || fail 'could not install requested release environment'
install -m 0640 -o root -g cloudflared "$RELEASE_DIR/cloudflared.token" "$CONFIG_DIR/cloudflared.token.new"
mv -T -- "$CONFIG_DIR/cloudflared.token.new" "$CONFIG_DIR/cloudflared.token"
install -m 0755 -o root -g root "$RELEASE_DIR/cloudflared" /usr/local/bin/cloudflared.new
mv -T -- /usr/local/bin/cloudflared.new /usr/local/bin/cloudflared

systemctl disable --now ssh.service ssh.socket sshd.service 2>/dev/null || true
systemctl mask ssh.service ssh.socket sshd.service 2>/dev/null || true
disable_unused_postfix

nft -c -f /etc/nftables.conf
atomic_switch "$release_target" || fail 'could not switch to requested release'
systemctl daemon-reload
systemd-analyze verify /etc/systemd/system/helm.service /etc/systemd/system/cloudflared.service /etc/systemd/system/helm-backup.service /etc/systemd/system/helm-backup.timer
systemctl enable nftables.service
systemctl restart nftables.service
systemctl enable helm.service cloudflared.service
systemctl enable --now helm-backup.timer

systemctl start helm.service
if ! healthy_revision "$SHA"; then
	log 'new release failed the local health check; attempting automatic rollback'
	if restore_previous; then
		recovery_needed=0
		fail 'deployment rolled back after failed health check'
	fi
	fail 'new release failed health and the previous release could not be restored'
fi
systemctl is-active --quiet helm.service || fail 'helm.service is not active after health check'
systemctl is-active --quiet roadmap.service || fail 'roadmap.service compatibility alias is not active after health check'

systemctl start cloudflared.service
if ! systemctl is-active --quiet cloudflared.service; then
	log 'cloudflared failed after the new release; attempting automatic rollback'
	if restore_previous; then
		recovery_needed=0
		fail 'deployment rolled back after cloudflared failure'
	fi
	fail 'cloudflared and the previous release could not be restored'
fi

if command -v ss >/dev/null 2>&1; then
	loopback_listener || fail 'Helm is not listening on loopback port 8080'
fi

# Retain the active release plus the newest previous releases.  Do not prune
# backups here; the backup helper owns their independent retention policy.
RETENTION=$(compat_env HELM_RELEASE_RETENTION ROADMAP_RELEASE_RETENTION 5)
[[ "$RETENTION" =~ ^[1-9][0-9]*$ ]] || fail 'release retention must be a positive integer'
mapfile -t releases < <(find "$RELEASES_DIR" -mindepth 1 -maxdepth 1 -type d -name '[0-9a-f]*' -printf '%T@ %p\n' | sort -nr)
if (( ${#releases[@]} > RETENTION )); then
	for entry in "${releases[@]:RETENTION}"; do
		path=${entry#* }
		[[ "$path" != "$previous_target" && "$path" != "$release_target" ]] || continue
		rm -rf -- "$path"
	done
fi

rm -f -- "$RELEASE_DIR/cloudflared.token"
recovery_needed=0
log "deployed release $SHA"
printf 'deployed_sha=%s\n' "$SHA"
