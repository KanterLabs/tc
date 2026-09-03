#!/usr/bin/env bash
# Verify a Helm release using the retained Roadmap v1 envelope before any guest extraction or Proxmox
# state change.  This file is installed root-owned on the PVE host by
# bootstrap-proxmox.sh; the same implementation is exercised locally by the
# deployment security regression tests.
set -Eeuo pipefail

ARCHIVE=${1:-}
REQUESTED_SHA=${2:-}
PUBLIC_KEY=${3:-/etc/roadmap-deploy/release-signing-public.pem}
ARCHIVE_MAX_BYTES=536870912
ARCHIVE_MAX_UNCOMPRESSED_BYTES=536870912
MANIFEST_MAX_BYTES=131072
SIGNATURE_BYTES=64
MANIFEST_HEADER='roadmap-release-manifest-v1'
# Listing and extraction run under a bounded helper. The size checks below
# stop the pipeline as soon as a header exceeds the aggregate cap; these
# process limits cover malformed archives that spend excessive CPU or memory
# before producing their next tar header.
TAR_WALL_CLOCK_LIMIT=30s
TAR_CPU_LIMIT_SECONDS=20
TAR_MEMORY_LIMIT_KIB=262144
TAR_LISTING_LIMIT_BYTES=1048576

PAYLOAD_MEMBERS=(
	cloudflared
	cloudflared.service
	cloudflared.token
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
ALL_MEMBERS=(
	cloudflared
	cloudflared.service
	cloudflared.token
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
ARCHIVE_MAX_MEMBERS=${#ALL_MEMBERS[@]}

fail() {
	printf '[helm-verify-release] %s\n' "$*" >&2
	exit 1
}

usage() {
	printf 'usage: %s <archive> <40-character git sha> <ed25519-public-key>\n' "$0" >&2
	exit 64
}

[[ $# -eq 3 ]] || usage
[[ "$REQUESTED_SHA" =~ ^[0-9a-f]{40}$ ]] || fail 'requested release SHA is invalid'

regular_file() {
	local path=$1 label=$2
	[[ -f "$path" && ! -L "$path" ]] || fail "$label is missing or not a regular file"
}

regular_file "$ARCHIVE" release-archive
regular_file "$PUBLIC_KEY" release-signing-public-key

archive_size=$(stat -c '%s' -- "$ARCHIVE")
[[ "$archive_size" =~ ^[0-9]+$ && "$archive_size" -le "$ARCHIVE_MAX_BYTES" ]] \
	|| fail 'compressed release archive exceeds the 512 MiB cap'

command -v tar >/dev/null 2>&1 || fail 'tar is required'
command -v openssl >/dev/null 2>&1 || fail 'openssl is required'
command -v sha256sum >/dev/null 2>&1 || fail 'sha256sum is required'
command -v stat >/dev/null 2>&1 || fail 'stat is required'
command -v awk >/dev/null 2>&1 || fail 'awk is required'
command -v timeout >/dev/null 2>&1 || fail 'timeout is required'
command -v mkfifo >/dev/null 2>&1 || fail 'mkfifo is required'
command -v setsid >/dev/null 2>&1 || fail 'setsid is required'

# A public key is not release data.  The PVE gateway separately checks its
# root ownership and mode; this verifier also rejects a different key type.
key_description=$(openssl pkey -pubin -in "$PUBLIC_KEY" -text -noout 2>/dev/null | sed -n '1p') \
	|| fail 'release-signing public key is invalid'
[[ "$key_description" = ED25519\ Public-Key:* ]] \
	|| fail 'release-signing key must be Ed25519'

work=$(mktemp -d "${TMPDIR:-/tmp}/roadmap-release-verify.XXXXXX")
listing_pid=
cleanup() {
	local status=$?
	if [[ -n "${listing_pid:-}" ]]; then
		kill -TERM -- "-$listing_pid" 2>/dev/null || true
		wait "$listing_pid" 2>/dev/null || true
	fi
	rm -rf -- "$work"
	exit "$status"
}
trap cleanup EXIT
chmod 0700 "$work"

# Keep one bounded listing before storing it. A compressed archive can contain
# an arbitrary number of names or a member whose declared size is enormous.
# awk exits on the first count/size violation, so tar receives SIGPIPE before
# it skips that member's payload. This avoids an unbounded tar -t/-tv pass
# through a high-ratio compressed bomb.
bounded_tar() {
	timeout --foreground --kill-after=2s "$TAR_WALL_CLOCK_LIMIT" \
		bash -c 'ulimit -t "$1"; ulimit -v "$2"; shift 2; exec tar "$@"' \
		roadmap-bounded-tar "$TAR_CPU_LIMIT_SECONDS" "$TAR_MEMORY_LIMIT_KIB" "$@"
}

bounded_tar_listing() {
	# Run in a dedicated process group so the consumer can abort tar while it
	# is skipping a member payload and not waiting for another listing line.
	exec setsid --wait timeout --foreground --kill-after=2s "$TAR_WALL_CLOCK_LIMIT" \
		bash -c 'ulimit -t "$1"; ulimit -v "$2"; shift 2; exec tar "$@"' \
		roadmap-bounded-tar "$TAR_CPU_LIMIT_SECONDS" "$TAR_MEMORY_LIMIT_KIB" "$@"
}

tar_verbose_file="$work/tar-verbose"
tar_listing_fifo="$work/tar-listing.fifo"
mkfifo -m 0600 "$tar_listing_fifo"
bounded_tar_listing --numeric-owner --full-time -tvzf "$ARCHIVE" > "$tar_listing_fifo" 2>/dev/null &
listing_pid=$!
if (
	ulimit -v "$TAR_MEMORY_LIMIT_KIB"
	awk -v max_members="$ARCHIVE_MAX_MEMBERS" \
		-v max_bytes="$ARCHIVE_MAX_UNCOMPRESSED_BYTES" \
		-v max_listing_bytes="$TAR_LISTING_LIMIT_BYTES" '
		function reject(message) { print message > "/dev/stderr"; exit 1 }
		{
			if (length($0) + 1 > max_listing_bytes - listing_bytes) reject("release archive listing exceeds its byte cap")
			listing_bytes += length($0) + 1
			if (NR > max_members) reject("release archive exceeds the member cap")
			if (NF < 6) reject("release archive member metadata is malformed")
			if (substr($1, 1, 1) != "-") reject("release archive members must all be regular files")
			if ($3 !~ /^[0-9]+$/) reject("release archive member size is invalid")
			if (($3 + 0) > max_bytes - total) reject("release archive exceeds aggregate uncompressed size cap")
			total += ($3 + 0)
			print
		}
		END { if (NR != max_members) exit 1 }
	' < "$tar_listing_fifo" > "$tar_verbose_file"
); then
	listing_status=0
else
	listing_status=1
fi
if (( listing_status != 0 )); then
	kill -TERM -- "-$listing_pid" 2>/dev/null || true
	wait "$listing_pid" 2>/dev/null || true
	listing_pid=
	fail 'release archive is not a valid gzip tar or exceeds its member/size cap'
fi
if ! wait "$listing_pid"; then
	listing_pid=
	fail 'release archive is not a valid gzip tar'
fi
listing_pid=
mapfile -t tar_verbose < "$tar_verbose_file"
[[ -n "${tar_verbose[*]}" ]] || fail 'release archive is empty'
[[ ${#tar_verbose[@]} -eq "$ARCHIVE_MAX_MEMBERS" ]] \
	|| fail 'release archive has an unexpected member count'

tar_members=()
for listing in "${tar_verbose[@]}"; do
	read -r mode owner member_size member_date member_time member extra <<< "$listing"
	tar_members+=("$member")
done

declare -A seen_members=()
for member in "${tar_members[@]}"; do
	member_key=
	# Resolve only after an exact case match.  This keeps untrusted tar names
	# out of associative-array subscripts and rejects ./ aliases, traversal,
	# absolute paths, whitespace, and every unlisted file.
	case "$member" in
		cloudflared) member_key=cloudflared ;;
		cloudflared.service) member_key=cloudflared.service ;;
		cloudflared.token) member_key=cloudflared.token ;;
		compose.yaml) member_key=compose.yaml ;;
		install-inside-lxc.sh) member_key=install-inside-lxc.sh ;;
		nftables.conf) member_key=nftables.conf ;;
		roadmap) member_key=roadmap ;;
		roadmap-backup.service) member_key=roadmap-backup.service ;;
		roadmap-backup.sh) member_key=roadmap-backup.sh ;;
		roadmap-backup.timer) member_key=roadmap-backup.timer ;;
		roadmap.env) member_key=roadmap.env ;;
		roadmap-restore.sh) member_key=roadmap-restore.sh ;;
		roadmap-rollback.sh) member_key=roadmap-rollback.sh ;;
		roadmap.service) member_key=roadmap.service ;;
		roadmap.sha256) member_key=roadmap.sha256 ;;
		release.sha) member_key=release.sha ;;
		release.manifest) member_key=release.manifest ;;
		release.manifest.sig) member_key=release.manifest.sig ;;
		*) fail "release archive contains a disallowed member: $member" ;;
	esac
	[[ -z "${seen_members[$member_key]+x}" ]] || fail "release archive contains duplicate member: $member"
	seen_members[$member_key]=1
done
for member in "${ALL_MEMBERS[@]}"; do
	[[ -n "${seen_members[$member]+x}" ]] || fail "release archive is missing member: $member"
done

# The bounded listing already checked each member's type and declared size.
# Re-parse it here for exact names and the aggregate accounting used below.
header_aggregate=0
declare -A archive_member_sizes=()
for listing in "${tar_verbose[@]}"; do
	read -r mode owner member_size member_date member_time member extra <<< "$listing"
	[[ -z "$extra" && "$mode" = -* && -n "$member" ]] \
		|| fail 'release archive members must all be regular files'
	case "$member" in
		cloudflared|cloudflared.service|cloudflared.token|compose.yaml|install-inside-lxc.sh|nftables.conf|roadmap|roadmap-backup.service|roadmap-backup.sh|roadmap-backup.timer|roadmap.env|roadmap-restore.sh|roadmap-rollback.sh|roadmap.service|roadmap.sha256|release.manifest|release.manifest.sig|release.sha) ;;
		*) fail "release archive metadata names a disallowed member: $member" ;;
	esac
	[[ "$member_size" =~ ^[0-9]+$ ]] || fail "release member size is invalid: $member"
	(( member_size <= ARCHIVE_MAX_UNCOMPRESSED_BYTES )) || fail 'release member exceeds the uncompressed size cap'
	if (( member_size > ARCHIVE_MAX_UNCOMPRESSED_BYTES - header_aggregate )); then
		fail 'release archive exceeds aggregate uncompressed size cap'
	fi
	header_aggregate=$((header_aggregate + member_size))
	archive_member_sizes[$member]=$member_size
done

[[ "${archive_member_sizes[release.manifest]:-}" =~ ^[0-9]+$ &&
	"${archive_member_sizes[release.manifest]}" -le "$MANIFEST_MAX_BYTES" ]] \
	|| fail 'release manifest exceeds its size cap'
[[ "${archive_member_sizes[release.manifest.sig]:-}" = "$SIGNATURE_BYTES" ]] \
	|| fail 'release manifest signature has an invalid size'

# Only the two signed manifest envelope members are materialized in this
# private temporary directory.  No release member is extracted to the guest
# or used until the signature and every payload hash have passed.
bounded_tar -xOf "$ARCHIVE" release.manifest > "$work/release.manifest" \
	|| fail 'release manifest cannot be read'
bounded_tar -xOf "$ARCHIVE" release.manifest.sig > "$work/release.manifest.sig" \
	|| fail 'release manifest signature cannot be read'
manifest_size=$(stat -c '%s' -- "$work/release.manifest")
[[ "$manifest_size" =~ ^[0-9]+$ && "$manifest_size" -le "$MANIFEST_MAX_BYTES" ]] \
	|| fail 'release manifest exceeds its size cap'
signature_size=$(stat -c '%s' -- "$work/release.manifest.sig")
[[ "$signature_size" = "$SIGNATURE_BYTES" ]] || fail 'release manifest signature has an invalid size'

openssl pkeyutl -verify -rawin -pubin -inkey "$PUBLIC_KEY" \
	-in "$work/release.manifest" -sigfile "$work/release.manifest.sig" \
	>/dev/null 2>&1 || fail 'release manifest signature verification failed'

header=$(head -n 1 "$work/release.manifest")
[[ "$header" = "$MANIFEST_HEADER" ]] || fail 'release manifest header is invalid'
[[ "$(tail -c 1 "$work/release.manifest" | od -An -t x1 | tr -d '[:space:]')" = 0a ]] \
	|| fail 'release manifest must end with a newline'

declare -A seen_payload=()
payload_count=0
aggregate_bytes=0
while IFS=$'\t' read -r name size digest extra; do
	[[ -n "$name" && -n "$size" && -n "$digest" && -z "$extra" ]] \
		|| fail 'release manifest contains a malformed line'
	[[ "$name" = "${PAYLOAD_MEMBERS[$payload_count]}" ]] \
		|| fail 'release manifest is not in canonical member order'
	case "$name" in
		cloudflared|cloudflared.service|cloudflared.token|compose.yaml|install-inside-lxc.sh|nftables.conf|roadmap|roadmap-backup.service|roadmap-backup.sh|roadmap-backup.timer|roadmap.env|roadmap-restore.sh|roadmap-rollback.sh|roadmap.service|roadmap.sha256|release.sha) ;;
		*) fail "release manifest names a non-payload member: $name" ;;
	esac
	[[ -z "${seen_payload[$name]+x}" ]] || fail "release manifest contains duplicate member: $name"
	seen_payload[$name]=1
	[[ "$size" =~ ^(0|[1-9][0-9]*)$ ]] || fail "release manifest size is invalid for $name"
	[[ "$digest" =~ ^[0-9a-f]{64}$ ]] || fail "release manifest hash is invalid for $name"
	(( size <= ARCHIVE_MAX_UNCOMPRESSED_BYTES )) \
		|| fail "release member exceeds the uncompressed size cap: $name"
	actual_size=$(bounded_tar -xOf "$ARCHIVE" "$name" | wc -c) \
		|| fail "could not read release member: $name"
	actual_size=${actual_size//[[:space:]]/}
	[[ "$actual_size" = "$size" ]] || fail "release member size does not match manifest: $name"
	actual_digest=$(bounded_tar -xOf "$ARCHIVE" "$name" | sha256sum | awk '{print $1}') \
		|| fail "could not hash release member: $name"
	[[ "$actual_digest" = "$digest" ]] || fail "release member hash does not match manifest: $name"
	if (( actual_size > ARCHIVE_MAX_UNCOMPRESSED_BYTES - aggregate_bytes )); then
		fail 'release archive exceeds aggregate uncompressed size cap'
	fi
	aggregate_bytes=$((aggregate_bytes + actual_size))
	(( payload_count += 1 ))
done < <(tail -n +2 "$work/release.manifest")
[[ "$payload_count" -eq "${#PAYLOAD_MEMBERS[@]}" ]] \
	|| fail 'release manifest does not enumerate every payload member'

# Include the signed envelope itself in the aggregate cap, while deliberately
# excluding it from the manifest list to avoid a circular self-hash.
if (( manifest_size > ARCHIVE_MAX_UNCOMPRESSED_BYTES - aggregate_bytes )); then
	fail 'release archive exceeds aggregate uncompressed size cap'
fi
aggregate_bytes=$((aggregate_bytes + manifest_size))
if (( signature_size > ARCHIVE_MAX_UNCOMPRESSED_BYTES - aggregate_bytes )); then
	fail 'release archive exceeds aggregate uncompressed size cap'
fi
aggregate_bytes=$((aggregate_bytes + signature_size))
[[ "$header_aggregate" -eq "$aggregate_bytes" ]] || fail 'release archive member sizes are inconsistent'

release_sha=$(bounded_tar -xOf "$ARCHIVE" release.sha) || fail 'release SHA cannot be read'
[[ "$release_sha" = "$REQUESTED_SHA" ]] || fail 'archive release SHA does not match the requested SHA'

printf 'release_verified_sha=%s\n' "$REQUESTED_SHA"
