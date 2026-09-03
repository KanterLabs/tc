#!/usr/bin/env bash
# Atomically switch the active Helm-compatible release and recover the previous one if
# the candidate does not become healthy.
set -Eeuo pipefail

compat_env() {
	local canonical=$1 legacy=$2 default_value=${3:-} canonical_value legacy_value
	canonical_value=${!canonical:-}
	legacy_value=${!legacy:-}
	[[ -z "$canonical_value" || -z "$legacy_value" || "$canonical_value" = "$legacy_value" ]] || {
		printf '[helm-rollback] %s and %s must match when both are set\n' "$canonical" "$legacy" >&2
		exit 1
	}
	printf '%s' "${canonical_value:-${legacy_value:-$default_value}}"
}

STATE_DIR=$(compat_env HELM_STATE_DIR ROADMAP_STATE_DIR /var/lib/roadmap)
RELEASES_DIR="$STATE_DIR/releases"
CURRENT_LINK="$STATE_DIR/current"
LOCK_PATH="$STATE_DIR/deploy.lock"
CONFIG_DIR=$(compat_env HELM_CONFIG_DIR ROADMAP_CONFIG_DIR /etc/roadmap)
SERVICE_STOP_TIMEOUT=30
SHA=${1:-}

fail() {
	printf '[helm-rollback] %s\n' "$*" >&2
	exit 1
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

[[ "$(id -u)" -eq 0 ]] || fail 'must run as root'
[[ "$SHA" =~ ^[0-9a-f]{40}$ ]] || fail 'usage: helm-rollback <40-character git sha>'
[[ -d "$STATE_DIR" && ! -L "$STATE_DIR" ]] || fail 'state directory is unavailable'
[[ "$(stat -c '%U:%G' -- "$STATE_DIR")" = root:root ]] || fail 'state directory owner is invalid'
[[ "$(stat -c '%a' -- "$STATE_DIR")" = 755 ]] || fail 'state directory mode is invalid'
[[ -d "$RELEASES_DIR" && ! -L "$RELEASES_DIR" ]] || fail 'release directory is unavailable'
TARGET="$RELEASES_DIR/$SHA"
[[ -d "$TARGET" && ! -L "$TARGET" ]] || fail 'requested release is not retained'
TARGET_BINARY=$(release_binary_name "$TARGET") || fail 'requested release has no verified binary layout'
[[ -f "$TARGET/roadmap.env" && ! -L "$TARGET/roadmap.env" ]] \
	|| fail 'requested release has no release environment'
validate_release_env "$TARGET/roadmap.env" "$SHA" ||
	fail 'requested release environment revision is invalid'
(cd "$TARGET" && sha256sum --check --strict "$TARGET_BINARY.sha256" >/dev/null) \
	|| fail 'requested release binary checksum failed'

if [[ -e "$LOCK_PATH" || -L "$LOCK_PATH" ]]; then
	[[ -f "$LOCK_PATH" && ! -L "$LOCK_PATH" ]] || fail 'deployment lock is not a regular file'
	[[ "$(stat -c '%U:%G' -- "$LOCK_PATH")" = root:root ]] || fail 'deployment lock owner is invalid'
fi
exec 9>"$LOCK_PATH"
chmod 0600 "$LOCK_PATH"
flock -x 9

previous_target=
if [[ -L "$CURRENT_LINK" ]]; then
	previous_target=$(readlink "$CURRENT_LINK")
	[[ "$previous_target" = "$RELEASES_DIR"/* ]] || fail 'current release link escapes release directory'
	[[ -d "$previous_target" && ! -L "$previous_target" ]] || fail 'current release is unavailable'
elif [[ -e "$CURRENT_LINK" ]]; then
	fail 'current release path is not a symlink'
fi
previous_sha=
if [[ -n "$previous_target" ]]; then
	previous_sha=${previous_target##*/}
	[[ "$previous_sha" =~ ^[0-9a-f]{40}$ ]] || fail 'current release directory name is invalid'
	previous_binary=$(release_binary_name "$previous_target") || fail 'current release binary layout is invalid'
	(cd "$previous_target" && sha256sum --check --strict "$previous_binary.sha256" >/dev/null) ||
		fail 'current release binary checksum failed'
	[[ -f "$previous_target/roadmap.env" && ! -L "$previous_target/roadmap.env" ]] ||
		fail 'current release has no release environment'
	validate_release_env "$previous_target/roadmap.env" "$previous_sha" ||
		fail 'current release environment revision is invalid'
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

start_and_verify_previous() {
	local reason=$1
	[[ -n "$previous_target" ]] || {
		printf '[helm-rollback] %s; no previous release exists\n' "$reason" >&2
		return 1
	}

	printf '[helm-rollback] %s; restoring previous release\n' "$reason" >&2
	# Stop both units before switching the link. This makes recovery the same
	# transaction for application and connector failures, and prevents a
	# connector from retaining target-release configuration during the switch.
	stop_unit cloudflared.service || return 1
	stop_unit roadmap.service || return 1
	stop_unit helm.service || return 1
	install_release_env "$previous_target" "$previous_sha" || return 1
	atomic_switch "$previous_target" || return 1
	systemctl start helm.service || return 1
	healthy_revision "$previous_sha" || return 1
	systemctl is-active --quiet helm.service || return 1
	systemctl is-active --quiet roadmap.service || return 1
	systemctl start cloudflared.service || return 1
	systemctl is-active --quiet cloudflared.service || return 1
	return 0
}

recovery_needed=0
recovery_in_progress=0
on_exit() {
	local status=$?
	# Recovery itself must never re-enter this transaction if a command calls
	# exit (for example, an unexpected system utility failure).
	trap - EXIT
	rm -f -- "$CONFIG_DIR/roadmap.env.new" || true
	if [[ "$status" -ne 0 && "$recovery_needed" -eq 1 && "$recovery_in_progress" -eq 0 ]]; then
		recovery_in_progress=1
		if ! start_and_verify_previous 'deployment failed before completion'; then
			printf '[helm-rollback] previous release recovery failed; inspect systemd and the local health endpoint\n' >&2
		fi
		recovery_in_progress=0
	fi
	exit "$status"
}
trap on_exit EXIT

recovery_needed=1
stop_unit cloudflared.service || fail 'could not stop cloudflared.service and verify it is inactive'
stop_unit roadmap.service || fail 'could not stop roadmap.service and verify it is inactive'
stop_unit helm.service || fail 'could not stop helm.service and verify it is inactive'
install_release_env "$TARGET" "$SHA" || fail 'could not install requested release environment'
atomic_switch "$TARGET" || fail 'could not switch to requested release'

# Validate the application before starting the connector. Either this health
# failure or the connector failure below must restore the prior release.
if ! systemctl start helm.service || ! healthy_revision "$SHA" || ! systemctl is-active --quiet helm.service || ! systemctl is-active --quiet roadmap.service; then
	if start_and_verify_previous 'requested release failed app health'; then
		recovery_needed=0
		exit 1
	fi
	fail 'requested release failed app health and previous release recovery failed'
fi

if ! systemctl start cloudflared.service || ! systemctl is-active --quiet cloudflared.service; then
	if start_and_verify_previous 'requested release failed cloudflared validation'; then
		recovery_needed=0
		exit 1
	fi
	fail 'requested release failed cloudflared validation and previous release recovery failed'
fi

recovery_needed=0
printf 'rollback_sha=%s\n' "$SHA"
