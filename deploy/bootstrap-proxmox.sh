#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
if [[ -n "${HELM_DEPLOY_CONFIG:-}" && -n "${ROADMAP_DEPLOY_CONFIG:-}" && "$HELM_DEPLOY_CONFIG" != "$ROADMAP_DEPLOY_CONFIG" ]]; then
	printf 'HELM_DEPLOY_CONFIG and ROADMAP_DEPLOY_CONFIG must match when both are set\n' >&2
	exit 1
fi
REQUESTED_PROFILE=production
if [[ -n "${HELM_DEPLOY_ENVIRONMENT:-}" ]]; then
	case "$HELM_DEPLOY_ENVIRONMENT" in
		production) REQUESTED_PROFILE=production ;;
		beta) REQUESTED_PROFILE=beta ;;
		*)
			printf 'HELM_DEPLOY_ENVIRONMENT must be production or beta\n' >&2
			exit 1 ;;
	esac
fi
if [[ "$REQUESTED_PROFILE" = beta ]]; then
	DEFAULT_CONFIG="$ROOT_DIR/.helm-beta-deploy.env"
else
	DEFAULT_CONFIG="$ROOT_DIR/.helm-deploy.env"
fi
CONFIG_FILE=${HELM_DEPLOY_CONFIG:-${ROADMAP_DEPLOY_CONFIG:-"$DEFAULT_CONFIG"}}
if [[ "$REQUESTED_PROFILE" = production ]]; then
	if [[ "$CONFIG_FILE" = "$ROOT_DIR/.helm-deploy.env" && ! -f "$CONFIG_FILE" && -f "$ROOT_DIR/.tc-deploy.env" ]]; then
		CONFIG_FILE="$ROOT_DIR/.tc-deploy.env"
	fi
elif [[ "$CONFIG_FILE" = "$ROOT_DIR/.helm-beta-deploy.env" && ! -f "$CONFIG_FILE" && -f "$ROOT_DIR/.helm-beta-deploy.env.example" ]]; then
	CONFIG_FILE="$ROOT_DIR/.helm-beta-deploy.env.example"
fi
PUBLIC_KEY_FILE=${1:-}
SIGNING_PUBLIC_KEY_FILE=${2:-}

if [[ ! -f "$CONFIG_FILE" || -L "$CONFIG_FILE" || ! -f "$PUBLIC_KEY_FILE" || -L "$PUBLIC_KEY_FILE" || ! -f "$SIGNING_PUBLIC_KEY_FILE" || -L "$SIGNING_PUBLIC_KEY_FILE" ]]; then
	printf 'usage: %s <deploy-public-key-file> <release-signing-public-key-file>\n' "$0" >&2
	exit 64
fi
config_owner=$(stat -c '%u' -- "$CONFIG_FILE")
config_mode=$(stat -c '%a' -- "$CONFIG_FILE")
[[ "$config_owner" = "$EUID" && "$config_mode" =~ ^[0-7]+$ && $((8#$config_mode & 022)) -eq 0 ]] || {
	printf 'deployment config must be owned by the invoking user and not group/world writable: %s\n' "$CONFIG_FILE" >&2
	exit 1
}
# shellcheck source=/dev/null
source "$CONFIG_FILE"
: "${PVE_HOST:?PVE_HOST is required}"
case "${PVE_DEPLOY_USER:-}" in
	roadmap-deploy) PROFILE=production ;;
	helm-beta-deploy) PROFILE=beta ;;
	*)
		printf 'PVE_DEPLOY_USER must be roadmap-deploy or helm-beta-deploy\n' >&2
		exit 1 ;;
esac
if [[ "$PROFILE" != "$REQUESTED_PROFILE" ]]; then
	printf 'HELM_DEPLOY_ENVIRONMENT does not match PVE_DEPLOY_USER\n' >&2
	exit 1
fi

if [[ "$PROFILE" = beta ]]; then
	REMOTE_DEPLOY_ROOT=/var/lib/helm-beta-deploy
else
	REMOTE_DEPLOY_ROOT=/var/lib/roadmap-deploy
fi

# Allocate a root-owned remote staging directory before uploading anything.
# Predictable /tmp names would let another local PVE user replace a source
# file between scp and install.
REMOTE_BOOTSTRAP_DIR=$(ssh -o BatchMode=yes "root@$PVE_HOST" bash -s -- "$PROFILE" <<'REMOTE'
set -Eeuo pipefail
PROFILE=$1
case "$PROFILE" in
	production) base=/var/lib/roadmap-deploy ;;
	beta) base=/var/lib/helm-beta-deploy ;;
	*) exit 1 ;;
esac
[[ ! -L "$base" && ( ! -e "$base" || -d "$base" ) ]] || exit 1
install -d -m 0700 -o root -g root "$base"
dir=$(mktemp -d "$base/bootstrap.XXXXXX")
chown root:root "$dir"
chmod 0700 "$dir"
printf '%s\n' "$dir"
REMOTE
)
if [[ "$PROFILE" = production ]]; then
	[[ "$REMOTE_BOOTSTRAP_DIR" =~ ^/var/lib/roadmap-deploy/bootstrap\.[A-Za-z0-9]+$ ]] || {
		printf 'remote bootstrap staging path is invalid\n' >&2
		exit 1
	}
else
	[[ "$REMOTE_BOOTSTRAP_DIR" =~ ^/var/lib/helm-beta-deploy/bootstrap\.[A-Za-z0-9]+$ ]] || {
		printf 'remote beta bootstrap staging path is invalid\n' >&2
		exit 1
	}
fi
cleanup() {
	ssh -o BatchMode=yes "root@$PVE_HOST" "rm -rf -- '$REMOTE_BOOTSTRAP_DIR'" >/dev/null 2>&1 || true
}
trap cleanup EXIT

scp -q "$ROOT_DIR/deploy/helm-deploy-gateway" "root@$PVE_HOST:$REMOTE_BOOTSTRAP_DIR/gateway"
scp -q "$ROOT_DIR/deploy/verify-release.sh" "root@$PVE_HOST:$REMOTE_BOOTSTRAP_DIR/verifier"
scp -q "$PUBLIC_KEY_FILE" "root@$PVE_HOST:$REMOTE_BOOTSTRAP_DIR/deploy-key"
scp -q "$SIGNING_PUBLIC_KEY_FILE" "root@$PVE_HOST:$REMOTE_BOOTSTRAP_DIR/release-signing-key"
ssh -o BatchMode=yes "root@$PVE_HOST" bash -s -- \
	"$REMOTE_BOOTSTRAP_DIR/gateway" \
	"$REMOTE_BOOTSTRAP_DIR/verifier" \
	"$REMOTE_BOOTSTRAP_DIR/deploy-key" \
	"$REMOTE_BOOTSTRAP_DIR/release-signing-key" \
	"$PROFILE" <<'REMOTE'
set -Eeuo pipefail
GATEWAY_SOURCE=$1
VERIFIER_SOURCE=$2
KEY_SOURCE=$3
SIGNING_KEY_SOURCE=$4
PROFILE=$5
case "$PROFILE" in
	production)
		DEPLOY_USER=roadmap-deploy
		DEPLOY_ROOT=/var/lib/roadmap-deploy
		GATEWAY_PATH=/usr/local/sbin/helm-deploy-gateway
		GATEWAY_COMPAT_PATH=/usr/local/sbin/roadmap-deploy-gateway
		VERIFY_PATH=/usr/local/sbin/helm-verify-release
		VERIFY_COMPAT_PATH=/usr/local/sbin/roadmap-verify-release
		SIGNING_DIR=/etc/roadmap-deploy
		SUDOERS_PATH=/etc/sudoers.d/roadmap-deploy
		;;
	beta)
		DEPLOY_USER=helm-beta-deploy
		DEPLOY_ROOT=/var/lib/helm-beta-deploy
		GATEWAY_PATH=/usr/local/sbin/helm-beta-deploy-gateway
		VERIFY_PATH=/usr/local/sbin/helm-beta-verify-release
		VERIFY_COMPAT_PATH=
		SIGNING_DIR=/etc/helm-beta-deploy
		SUDOERS_PATH=/etc/sudoers.d/helm-beta-deploy
		;;
	*) exit 1 ;;
esac
STAGING_ROOT="$DEPLOY_ROOT/staging"

[[ -f "$GATEWAY_SOURCE" && ! -L "$GATEWAY_SOURCE" ]] || exit 1
[[ -f "$VERIFIER_SOURCE" && ! -L "$VERIFIER_SOURCE" ]] || exit 1
[[ -f "$KEY_SOURCE" && ! -L "$KEY_SOURCE" ]] || exit 1
[[ -f "$SIGNING_KEY_SOURCE" && ! -L "$SIGNING_KEY_SOURCE" ]] || exit 1
key_lines=$(awk 'NF { count++ } END { print count + 0 }' "$KEY_SOURCE")
[[ "$key_lines" = 1 ]] || { echo 'deploy key must contain one non-empty line' >&2; exit 1; }
key=$(sed -e 's/[[:space:]]*$//' "$KEY_SOURCE")
[[ "$key" =~ ^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp256|ecdsa-sha2-nistp384|ecdsa-sha2-nistp521)[[:space:]][A-Za-z0-9+/=]+([[:space:]][^[:space:]]+)?$ ]] || {
	echo 'unsupported deploy public-key format' >&2
	exit 1
}

command -v openssl >/dev/null 2>&1 || { echo 'openssl is required for release signing keys' >&2; exit 1; }
signing_key_description=$(openssl pkey -pubin -in "$SIGNING_KEY_SOURCE" -text -noout 2>/dev/null | sed -n '1p') || {
	echo 'release-signing public key is invalid' >&2
	exit 1
}
[[ "$signing_key_description" = ED25519\ Public-Key:* ]] || {
	echo 'release-signing public key must be Ed25519' >&2
	exit 1
}

if ! id "$DEPLOY_USER" >/dev/null 2>&1; then
	useradd --create-home --home-dir "/home/$DEPLOY_USER" --shell /bin/bash "$DEPLOY_USER"
fi
primary_group=$(id -gn "$DEPLOY_USER")
[[ "$primary_group" = "$DEPLOY_USER" ]] || { echo 'deploy user has an unexpected primary group' >&2; exit 1; }
supplementary=$(id -Gn "$DEPLOY_USER" | tr ' ' '\n' | grep -v -Fx "$DEPLOY_USER" || true)
[[ -z "$supplementary" ]] || { echo 'deploy user has unexpected supplementary groups' >&2; exit 1; }

install -d -m 0700 -o "$DEPLOY_USER" -g "$DEPLOY_USER" "/home/$DEPLOY_USER/.ssh"

# Production deliberately retains /var/lib/roadmap-deploy/staging; beta uses
# the separate /var/lib/helm-beta-deploy/staging root.
for staging_dir in "$DEPLOY_ROOT" "$STAGING_ROOT"; do
	if [[ -L "$staging_dir" || ( -e "$staging_dir" && ! -d "$staging_dir" ) ]]; then
		echo "deployment staging path is not a directory: $staging_dir" >&2
		exit 1
	fi
	install -d -m 0700 -o root -g root "$staging_dir"
done

if [[ "$PROFILE" = production ]]; then
	printf '%s\n' "command=\"sudo -n /usr/local/sbin/helm-deploy-gateway\",no-agent-forwarding,no-port-forwarding,no-pty,no-user-rc,no-X11-forwarding $key" \
		> "/home/$DEPLOY_USER/.ssh/authorized_keys.new"
else
	printf '%s\n' "command=\"sudo -n /usr/local/sbin/helm-beta-deploy-gateway\",no-agent-forwarding,no-port-forwarding,no-pty,no-user-rc,no-X11-forwarding $key" \
		> "/home/$DEPLOY_USER/.ssh/authorized_keys.new"
fi
chown "$DEPLOY_USER:$DEPLOY_USER" "/home/$DEPLOY_USER/.ssh/authorized_keys.new"
chmod 0600 "/home/$DEPLOY_USER/.ssh/authorized_keys.new"
mv -T -- "/home/$DEPLOY_USER/.ssh/authorized_keys.new" "/home/$DEPLOY_USER/.ssh/authorized_keys"

if [[ "$PROFILE" = production ]]; then
	install -m 0755 -o root -g root "$GATEWAY_SOURCE" /usr/local/sbin/helm-deploy-gateway.new
	mv -T -- /usr/local/sbin/helm-deploy-gateway.new /usr/local/sbin/helm-deploy-gateway
	ln -s -- helm-deploy-gateway /usr/local/sbin/roadmap-deploy-gateway.new
	mv -T -- /usr/local/sbin/roadmap-deploy-gateway.new /usr/local/sbin/roadmap-deploy-gateway
	install -m 0755 -o root -g root "$VERIFIER_SOURCE" /usr/local/sbin/helm-verify-release.new
	mv -T -- /usr/local/sbin/helm-verify-release.new /usr/local/sbin/helm-verify-release
	ln -s -- helm-verify-release /usr/local/sbin/roadmap-verify-release.new
	mv -T -- /usr/local/sbin/roadmap-verify-release.new /usr/local/sbin/roadmap-verify-release
	if [[ -L /etc/roadmap-deploy || ( -e /etc/roadmap-deploy && ! -d /etc/roadmap-deploy ) ]]; then
		echo 'release-signing key directory is invalid' >&2
		exit 1
	fi
	install -d -m 0700 -o root -g root /etc/roadmap-deploy
	install -m 0644 -o root -g root "$SIGNING_KEY_SOURCE" /etc/roadmap-deploy/release-signing-public.pem.new
	mv -T -- /etc/roadmap-deploy/release-signing-public.pem.new /etc/roadmap-deploy/release-signing-public.pem
	{
		printf 'Defaults:%s env_keep += "SSH_ORIGINAL_COMMAND SSH_CONNECTION"\n' "$DEPLOY_USER"
		printf '%s ALL=(root) NOPASSWD: /usr/local/sbin/helm-deploy-gateway\n' "$DEPLOY_USER"
	} > /etc/sudoers.d/roadmap-deploy
	chmod 0440 /etc/sudoers.d/roadmap-deploy
	visudo -cf /etc/sudoers.d/roadmap-deploy >/dev/null
else
	install -m 0755 -o root -g root "$GATEWAY_SOURCE" /usr/local/sbin/helm-beta-deploy-gateway.new
	mv -T -- /usr/local/sbin/helm-beta-deploy-gateway.new /usr/local/sbin/helm-beta-deploy-gateway
	install -m 0755 -o root -g root "$VERIFIER_SOURCE" /usr/local/sbin/helm-beta-verify-release.new
	mv -T -- /usr/local/sbin/helm-beta-verify-release.new /usr/local/sbin/helm-beta-verify-release
	if [[ -L /etc/helm-beta-deploy || ( -e /etc/helm-beta-deploy && ! -d /etc/helm-beta-deploy ) ]]; then
		echo 'beta release-signing key directory is invalid' >&2
		exit 1
	fi
	install -d -m 0700 -o root -g root /etc/helm-beta-deploy
	install -m 0644 -o root -g root "$SIGNING_KEY_SOURCE" /etc/helm-beta-deploy/release-signing-public.pem.new
	mv -T -- /etc/helm-beta-deploy/release-signing-public.pem.new /etc/helm-beta-deploy/release-signing-public.pem
	{
		printf 'Defaults:%s env_keep += "SSH_ORIGINAL_COMMAND SSH_CONNECTION"\n' "$DEPLOY_USER"
		printf '%s ALL=(root) NOPASSWD: /usr/local/sbin/helm-beta-deploy-gateway\n' "$DEPLOY_USER"
	} > /etc/sudoers.d/helm-beta-deploy
	chmod 0440 /etc/sudoers.d/helm-beta-deploy
	visudo -cf /etc/sudoers.d/helm-beta-deploy >/dev/null
fi
rm -f -- "$GATEWAY_SOURCE" "$VERIFIER_SOURCE" "$KEY_SOURCE" "$SIGNING_KEY_SOURCE"
REMOTE
