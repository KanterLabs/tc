#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
DIST_DIR="$ROOT_DIR/dist"
SHA=${1:-}

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

SIGNING_KEY_FILE=$(resolve_compat_var HELM_RELEASE_SIGNING_KEY_FILE ROADMAP_RELEASE_SIGNING_KEY_FILE)
CLOUDFLARED_TOKEN_FILE=$(resolve_compat_var HELM_CLOUDFLARED_TOKEN_FILE ROADMAP_CLOUDFLARED_TOKEN_FILE "$DIST_DIR/cloudflared.token")
OWNER_ENV_FILE=$(resolve_compat_var HELM_OWNER_ENV_FILE ROADMAP_OWNER_ENV_FILE "$DIST_DIR/owner.env")

PAYLOAD_MEMBERS=(
	cloudflared
	cloudflared.service
	cloudflared.token
	codex
	codex.sha256
	compose.yaml
	install-inside-lxc.sh
	nftables.conf
	roadmap
	roadmap-backup.service
	roadmap-backup.sh
	roadmap-backup.timer
	roadmap.env
	roadmap-restore.sh
	roadmap-rollback.sh
	roadmap.service
	roadmap.sha256
	release.sha
)
ENVELOPE_MEMBERS=(release.manifest release.manifest.sig)
BUNDLE_MEMBERS=(
	cloudflared
	cloudflared.service
	cloudflared.token
	codex
	codex.sha256
	compose.yaml
	install-inside-lxc.sh
	nftables.conf
	roadmap
	roadmap-backup.service
	roadmap-backup.sh
	roadmap-backup.timer
	roadmap.env
	roadmap-restore.sh
	roadmap-rollback.sh
	roadmap.service
	roadmap.sha256
	release.manifest
	release.manifest.sig
	release.sha
)

if [[ ! "$SHA" =~ ^[0-9a-f]{40}$ ]]; then
	printf 'usage: %s <40-character git sha>\n' "$0" >&2
	exit 64
fi

safe_file() {
	local path=$1 label=$2 mode
	[[ -f "$path" && ! -L "$path" ]] || {
		printf '%s is missing or not a regular file: %s\n' "$label" "$path" >&2
		exit 1
	}
	mode=$(stat -c '%a' -- "$path")
	[[ "$mode" =~ ^[0-7]+$ ]] || {
		printf 'could not inspect permissions for %s\n' "$label" >&2
		exit 1
	}
}

safe_file "$DIST_DIR/helm" helm-binary
[[ -x "$DIST_DIR/helm" ]] || {
	printf 'helm binary must be executable\n' >&2
	exit 1
}
safe_file "$DIST_DIR/codex" codex-binary
[[ -x "$DIST_DIR/codex" ]] || {
	printf 'codex binary must be executable\n' >&2
	exit 1
}
[[ -n "$SIGNING_KEY_FILE" ]] || {
	printf 'HELM_RELEASE_SIGNING_KEY_FILE is required to sign a release\n' >&2
	exit 1
}
safe_file "$SIGNING_KEY_FILE" release-signing-private-key
signing_key_mode=$(stat -c '%a' -- "$SIGNING_KEY_FILE")
(( (8#$signing_key_mode & 077) == 0 )) || {
	printf 'release-signing private key must not be group/world accessible\n' >&2
	exit 1
}
command -v openssl >/dev/null 2>&1 || {
	printf 'openssl is required to sign a release\n' >&2
	exit 1
}
key_description=$(openssl pkey -in "$SIGNING_KEY_FILE" -text -noout 2>/dev/null | sed -n '1p') || {
	printf 'release-signing private key is invalid\n' >&2
	exit 1
}
[[ "$key_description" = ED25519\ Private-Key:* ]] || {
	printf 'release-signing private key must be Ed25519\n' >&2
	exit 1
}
safe_file "$CLOUDFLARED_TOKEN_FILE" cloudflared-token
safe_file "$OWNER_ENV_FILE" owner-environment
chmod 0600 "$CLOUDFLARED_TOKEN_FILE" "$OWNER_ENV_FILE"

# Keep this pin in lockstep with the reviewed Stashlet deployment. Existing
# downloads are checked as well; a stale or tampered cache is never bundled.
CLOUDFLARED_VERSION=2026.8.2
CLOUDFLARED_SHA256=fcfb02b575a52ca1af2e3267af4e1517bcdeb30ac48c834c69abaed3c0576ad2
CLOUDFLARED_PATH="$DIST_DIR/cloudflared"

if [[ -e "$CLOUDFLARED_PATH" && ( ! -f "$CLOUDFLARED_PATH" || -L "$CLOUDFLARED_PATH" ) ]]; then
	printf 'cloudflared cache is not a regular file\n' >&2
	exit 1
fi

if [[ ! -f "$CLOUDFLARED_PATH" ]]; then
	tmp=$(mktemp "$DIST_DIR/.cloudflared.XXXXXX")
	trap 'rm -f "$tmp"' EXIT
	curl --fail --location --show-error --proto '=https' --tlsv1.2 \
		"https://github.com/cloudflare/cloudflared/releases/download/${CLOUDFLARED_VERSION}/cloudflared-linux-amd64" \
		--output "$tmp"
	echo "$CLOUDFLARED_SHA256  $tmp" | sha256sum --check --strict >/dev/null
	chmod 0755 "$tmp"
	mv -T -- "$tmp" "$CLOUDFLARED_PATH"
	trap - EXIT
fi

echo "$CLOUDFLARED_SHA256  $CLOUDFLARED_PATH" | sha256sum --check --strict >/dev/null
chmod 0755 "$CLOUDFLARED_PATH"

BUNDLE_DIR=$(mktemp -d)
cleanup() { rm -rf -- "$BUNDLE_DIR"; }
trap cleanup EXIT
chmod 0700 "$BUNDLE_DIR"

# The Proxmox verifier is a separately managed trust boundary. Preserve its
# exact Roadmap v1 member names and manifest header while the payload itself
# transitions to Helm. The guest installer converts these envelope members to
# canonical Helm runtime names and retains compatibility aliases for rollback.
install -m 0755 "$DIST_DIR/helm" "$BUNDLE_DIR/roadmap"
install -m 0755 "$DIST_DIR/codex" "$BUNDLE_DIR/codex"
install -m 0755 "$CLOUDFLARED_PATH" "$BUNDLE_DIR/cloudflared"
install -m 0600 "$CLOUDFLARED_TOKEN_FILE" "$BUNDLE_DIR/cloudflared.token"
install -m 0640 "$OWNER_ENV_FILE" "$BUNDLE_DIR/roadmap.env"
install -m 0755 "$ROOT_DIR/deploy/install-inside-lxc.sh" "$BUNDLE_DIR/install-inside-lxc.sh"
install -m 0755 "$ROOT_DIR/deploy/helm-backup.sh" "$BUNDLE_DIR/roadmap-backup.sh"
install -m 0755 "$ROOT_DIR/deploy/helm-restore.sh" "$BUNDLE_DIR/roadmap-restore.sh"
install -m 0755 "$ROOT_DIR/deploy/helm-rollback.sh" "$BUNDLE_DIR/roadmap-rollback.sh"
install -m 0644 "$ROOT_DIR/deploy/helm-backup.service" "$BUNDLE_DIR/roadmap-backup.service"
install -m 0644 "$ROOT_DIR/deploy/helm-backup.timer" "$BUNDLE_DIR/roadmap-backup.timer"
install -m 0644 "$ROOT_DIR/deploy/helm.service" "$BUNDLE_DIR/roadmap.service"
install -m 0644 "$ROOT_DIR/deploy/cloudflared.service" "$BUNDLE_DIR/cloudflared.service"
install -m 0644 "$ROOT_DIR/deploy/nftables.conf" "$BUNDLE_DIR/nftables.conf"
install -m 0644 "$ROOT_DIR/compose.yaml" "$BUNDLE_DIR/compose.yaml"
if grep -Eq '^(HELM|ROADMAP)_RELEASE_SHA=' "$BUNDLE_DIR/roadmap.env"; then
	printf 'owner environment already contains a release SHA\n' >&2
	exit 1
fi
printf 'HELM_RELEASE_SHA=%s\nROADMAP_RELEASE_SHA=%s\n' "$SHA" "$SHA" >> "$BUNDLE_DIR/roadmap.env"
chmod 0640 "$BUNDLE_DIR/roadmap.env"
printf '%s\n' "$SHA" > "$BUNDLE_DIR/release.sha"
chmod 0644 "$BUNDLE_DIR/release.sha"
(cd "$BUNDLE_DIR" && sha256sum roadmap > roadmap.sha256)
chmod 0644 "$BUNDLE_DIR/roadmap.sha256"
(cd "$BUNDLE_DIR" && sha256sum codex > codex.sha256)
chmod 0644 "$BUNDLE_DIR/codex.sha256"

# The manifest is the canonical signed description of every payload member.
# The two envelope members are intentionally excluded to avoid a circular
# self-hash; they are validated by exact name, size, and signature checks on
# the PVE host.
{
	printf 'roadmap-release-manifest-v1\n'
	for member in "${PAYLOAD_MEMBERS[@]}"; do
		bytes=$(stat -c '%s' -- "$BUNDLE_DIR/$member")
		digest=$(sha256sum -- "$BUNDLE_DIR/$member" | awk '{print $1}')
		printf '%s\t%s\t%s\n' "$member" "$bytes" "$digest"
	done
} > "$BUNDLE_DIR/release.manifest"
chmod 0644 "$BUNDLE_DIR/release.manifest"
openssl pkeyutl -sign -rawin -inkey "$SIGNING_KEY_FILE" \
	-in "$BUNDLE_DIR/release.manifest" -out "$BUNDLE_DIR/release.manifest.sig" \
	|| {
		printf 'could not sign release manifest\n' >&2
		exit 1
	}
chmod 0644 "$BUNDLE_DIR/release.manifest.sig"

ARCHIVE="$DIST_DIR/helm-$SHA.tar.gz"
rm -f -- "$ARCHIVE"
# Normalize tar metadata so the archive is reproducible for a given source
# tree, while retaining strict, non-writable member modes from above.
GZIP=-n tar --sort=name --owner=0 --group=0 --numeric-owner --mtime='@0' \
	-czf "$ARCHIVE" -C "$BUNDLE_DIR" "${BUNDLE_MEMBERS[@]}"
chmod 0600 "$ARCHIVE"

# A local sanity check catches accidental aliases or omitted members before
# the archive reaches the Proxmox gateway. The host verifier repeats this
# check after receiving the stream.
mapfile -t archive_members < <(tar -tzf "$ARCHIVE")
[[ ${#archive_members[@]} -eq ${#BUNDLE_MEMBERS[@]} ]] || {
	printf 'bundle member count is not canonical\n' >&2
	exit 1
}
for index in "${!BUNDLE_MEMBERS[@]}"; do
	[[ "${archive_members[$index]}" = "${BUNDLE_MEMBERS[$index]}" ]] || {
		printf 'bundle member list is not canonical\n' >&2
		exit 1
	}
done

printf '%s\n' "$ARCHIVE"
