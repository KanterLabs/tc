#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

resolve_compat_var() {
	local canonical=$1 legacy=$2 default_value=${3:-} canonical_value legacy_value
	canonical_value=${!canonical:-}
	legacy_value=${!legacy:-}
	if [[ -n "$canonical_value" && -n "$legacy_value" && "$canonical_value" != "$legacy_value" ]]; then
		printf '%s and %s must match when both are set\n' "$canonical" "$legacy" >&2
		exit 1
	fi
	printf '%s' "${canonical_value:-${legacy_value:-$default_value}}"
}

CONFIG_FILE=$(resolve_compat_var HELM_DEPLOY_CONFIG ROADMAP_DEPLOY_CONFIG "$ROOT_DIR/.helm-deploy.env")
ACTION=deploy
SHA=
if [[ ${1:-} = deploy || ${1:-} = rollback || ${1:-} = status ]]; then
	ACTION=$1
	shift
fi
SHA=${1:-}

if [[ "$CONFIG_FILE" = "$ROOT_DIR/.helm-deploy.env" && ! -f "$CONFIG_FILE" && -f "$ROOT_DIR/.tc-deploy.env" ]]; then
	CONFIG_FILE="$ROOT_DIR/.tc-deploy.env"
elif [[ ! -f "$CONFIG_FILE" && -f "$ROOT_DIR/.helm-deploy.env.example" ]]; then
	CONFIG_FILE="$ROOT_DIR/.helm-deploy.env.example"
elif [[ ! -f "$CONFIG_FILE" && -f "$ROOT_DIR/.tc-deploy.env.example" ]]; then
	CONFIG_FILE="$ROOT_DIR/.tc-deploy.env.example"
fi
[[ -f "$CONFIG_FILE" ]] || {
	printf 'missing deployment config: %s\n' "$CONFIG_FILE" >&2
	exit 1
}
[[ ! -L "$CONFIG_FILE" ]] || {
	printf 'deployment config must not be a symlink: %s\n' "$CONFIG_FILE" >&2
	exit 1
}
config_owner=$(stat -c '%u' -- "$CONFIG_FILE")
config_mode=$(stat -c '%a' -- "$CONFIG_FILE")
[[ "$config_owner" = "$EUID" && "$config_mode" =~ ^[0-7]+$ && $((8#$config_mode & 022)) -eq 0 ]] || {
	printf 'deployment config must be owned by the invoking user and not group/world writable: %s\n' "$CONFIG_FILE" >&2
	exit 1
}
# shellcheck source=/dev/null
source "$CONFIG_FILE"
: "${PVE_HOST:?PVE_HOST is required}"
: "${PVE_DEPLOY_USER:?PVE_DEPLOY_USER is required}"
[[ "$PVE_DEPLOY_USER" = roadmap-deploy ]] || {
	# The deployment account is an intentional legacy allowlist entry on the
	# separately managed Proxmox gateway.
	printf 'PVE_DEPLOY_USER must be roadmap-deploy\n' >&2
	exit 1
}

case "$ACTION" in
	deploy|rollback)
		[[ "$SHA" =~ ^[0-9a-f]{40}$ && $# -eq 1 ]] || {
			printf 'usage: %s [deploy|rollback] <40-character git sha>\n' "$0" >&2
			exit 64
		} ;;
	status)
		[[ -z "$SHA" && $# -eq 0 ]] || { printf 'usage: %s status\n' "$0" >&2; exit 64; } ;;
	*) printf 'usage: %s [deploy|rollback] <40-character git sha> | status\n' "$0" >&2; exit 64 ;;
esac

SSH_CONFIG=$(resolve_compat_var HELM_SSH_CONFIG ROADMAP_SSH_CONFIG)
if [[ -n "$SSH_CONFIG" ]]; then
	[[ "$SSH_CONFIG" = /* && -f "$SSH_CONFIG" && ! -L "$SSH_CONFIG" ]] || {
		printf 'HELM_SSH_CONFIG must be an absolute regular file and not a symlink\n' >&2
		exit 1
	}
	ssh_config_owner=$(stat -c '%u' -- "$SSH_CONFIG")
	ssh_config_mode=$(stat -c '%a' -- "$SSH_CONFIG")
	[[ "$ssh_config_owner" = "$EUID" && "$ssh_config_mode" =~ ^[0-7]+$ && $((8#$ssh_config_mode & 077)) -eq 0 ]] || {
		printf 'HELM_SSH_CONFIG must be owned by the invoking user and mode 0600 or stricter\n' >&2
		exit 1
	}
fi

if [[ "$ACTION" = status ]]; then
	if [[ -n "$SSH_CONFIG" ]]; then
		ssh -F "$SSH_CONFIG" -o BatchMode=yes -T "$PVE_DEPLOY_USER@$PVE_HOST" status < /dev/null
	else
		ssh -o BatchMode=yes -T "$PVE_DEPLOY_USER@$PVE_HOST" status < /dev/null
	fi
	exit 0
fi

if [[ "$ACTION" = rollback ]]; then
	if [[ -n "$SSH_CONFIG" ]]; then
		ssh -F "$SSH_CONFIG" -o BatchMode=yes -T "$PVE_DEPLOY_USER@$PVE_HOST" "rollback $SHA" < /dev/null
	else
		ssh -o BatchMode=yes -T "$PVE_DEPLOY_USER@$PVE_HOST" "rollback $SHA" < /dev/null
	fi
	exit 0
fi

ARCHIVE=$("$ROOT_DIR/deploy/build-bundle.sh" "$SHA")
[[ -f "$ARCHIVE" && ! -L "$ARCHIVE" ]] || {
	printf 'bundle was not produced\n' >&2
	exit 1
}
cleanup() { rm -f -- "$ARCHIVE"; }
trap cleanup EXIT

# The key's forced command supplies sudo and the gateway reads this stream
# into a root-owned, size-capped staging file. No scp subsystem or remote
# shell is available to the deployment identity.
if [[ -n "$SSH_CONFIG" ]]; then
	ssh -F "$SSH_CONFIG" -o BatchMode=yes -T "$PVE_DEPLOY_USER@$PVE_HOST" "deploy $SHA" < "$ARCHIVE"
else
	ssh -o BatchMode=yes -T "$PVE_DEPLOY_USER@$PVE_HOST" "deploy $SHA" < "$ARCHIVE"
fi
