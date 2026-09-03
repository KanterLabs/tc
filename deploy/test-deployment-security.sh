#!/usr/bin/env bash
# Deployment protocol and release-provenance regressions. These checks avoid
# any Proxmox or Cloudflare connection; the signed-archive cases exercise the
# same verifier that the root gateway invokes.
set -Eeuo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
DEPLOY_DIR="$ROOT_DIR/deploy"
GATEWAY="$DEPLOY_DIR/helm-deploy-gateway"
BOOTSTRAP="$DEPLOY_DIR/bootstrap-proxmox.sh"
DEPLOY_CI="$DEPLOY_DIR/deploy-ci.sh"
VERIFY="$DEPLOY_DIR/verify-release.sh"
BUILD_BUNDLE="$DEPLOY_DIR/build-bundle.sh"
BACKUP="$DEPLOY_DIR/helm-backup.sh"
RESTORE="$DEPLOY_DIR/helm-restore.sh"
ROLLBACK="$DEPLOY_DIR/helm-rollback.sh"
INSTALL="$DEPLOY_DIR/install-inside-lxc.sh"
SERVICE="$DEPLOY_DIR/helm.service"
CLOUDFLARE="$DEPLOY_DIR/cloudflare.sh"
VALIDATE="$DEPLOY_DIR/validate-live.sh"
WORKFLOW="$ROOT_DIR/.github/workflows/ci.yml"
DOCS="$ROOT_DIR/docs/OPERATIONS.md"
DOCKERIGNORE="$ROOT_DIR/.dockerignore"

fail() { printf '[deployment-security] %s\n' "$*" >&2; exit 1; }
contains() {
	local needle=$1 file=$2
	grep -F -- "$needle" "$file" >/dev/null || fail "missing reviewed text in $file: $needle"
}
not_contains() {
	local needle=$1 file=$2
	if grep -F -- "$needle" "$file" >/dev/null; then
		fail "unexpected reviewed text in $file: $needle"
	fi
}
count_contains() {
	local expected=$1 needle=$2 file=$3 actual
	actual=$(grep -Fc -- "$needle" "$file" || true)
	[[ "$actual" = "$expected" ]] || fail "expected $expected occurrences of $needle in $file, got $actual"
}

for file in "$GATEWAY" "$BOOTSTRAP" "$DEPLOY_CI" "$VERIFY" "$BUILD_BUNDLE" \
	"$BACKUP" "$RESTORE" "$ROLLBACK" "$INSTALL" "$SERVICE" "$CLOUDFLARE" "$VALIDATE" "$WORKFLOW" "$DOCS"; do
	[[ -f "$file" && ! -L "$file" ]] || fail "deployment file is missing: $file"
done

fixture=$(mktemp -d "${TMPDIR:-/tmp}/helm-deploy-security.XXXXXX")
cleanup_fixture() { rm -rf -- "$fixture"; }
trap cleanup_fixture EXIT
# Mocked backup publication tests do not have a release binary from which to
# query migration-info; use a deterministic non-secret fixture digest.
export HELM_MIGRATION_DIGEST=0000000000000000000000000000000000000000000000000000000000000000

# The SSH key enters one fixed root command through non-interactive sudo. The
# gateway sees SSH_ORIGINAL_COMMAND, while local argv is an explicit root-only
# test escape hatch. No deploy-key holder can request a shell or arbitrary sudo.
contains 'command=\"sudo -n /usr/local/sbin/helm-deploy-gateway\"' "$BOOTSTRAP"
contains 'env_keep += "SSH_ORIGINAL_COMMAND SSH_CONNECTION"' "$BOOTSTRAP"
contains 'ALL=(root) NOPASSWD: /usr/local/sbin/helm-deploy-gateway' "$BOOTSTRAP"
contains '/var/lib/roadmap-deploy/staging' "$BOOTSTRAP"
contains 'mktemp -d "$base/bootstrap.XXXXXX"' "$BOOTSTRAP"
contains 'release-signing-public.pem' "$BOOTSTRAP"
contains '[[ ${SSH_ORIGINAL_COMMAND+x} ]]' "$GATEWAY"
contains 'REQUEST=${SSH_ORIGINAL_COMMAND:-}' "$GATEWAY"
contains 'HELM_GATEWAY_LOCAL_TEST=1' "$GATEWAY"
contains '[[ $# -eq 0 ]] || fail '\''SSH gateway does not accept command-line arguments'\''' "$GATEWAY"
contains 'flock -x 8' "$GATEWAY"
contains 'timeout --foreground "$ARCHIVE_INGEST_TIMEOUT"' "$GATEWAY"
contains 'head -c "$((ARCHIVE_MAX_BYTES + 1))" > "$ARCHIVE"' "$GATEWAY"
contains 'release provenance verification failed' "$GATEWAY"
contains '"$VERIFY_SCRIPT" "$ARCHIVE" "$SHA" "$SIGNING_PUBLIC_KEY"' "$GATEWAY"

# The verifier must run before any deploy-branch pct state change. Status and
# rollback are intentionally payload-free actions and do not need a release
# signature.
verify_line=$(grep -n 'verified=\$("\$VERIFY_SCRIPT"' "$GATEWAY" | cut -d: -f1)
deploy_pct_line=$(grep -n 'if ! pct config "\$CTID"' "$GATEWAY" | cut -d: -f1)
[[ -n "$verify_line" && -n "$deploy_pct_line" && "$verify_line" -lt "$deploy_pct_line" ]] \
	|| fail 'gateway does not verify release provenance before deploy pct state changes'

# Deploy payloads are streamed to the gateway. status/rollback carry no
# payload, and the caller never invokes scp or puts sudo/gateway text in the
# SSH request.
contains 'ssh -o BatchMode=yes -T "$PVE_DEPLOY_USER@$PVE_HOST" "deploy $SHA" < "$ARCHIVE"' "$DEPLOY_CI"
contains 'ssh -o BatchMode=yes -T "$PVE_DEPLOY_USER@$PVE_HOST" status < /dev/null' "$DEPLOY_CI"
contains 'ssh -o BatchMode=yes -T "$PVE_DEPLOY_USER@$PVE_HOST" "rollback $SHA" < /dev/null' "$DEPLOY_CI"
not_contains 'scp -q' "$DEPLOY_CI"
not_contains '/usr/local/sbin/helm-deploy-gateway' "$DEPLOY_CI"

# Only the intended command alphabet can reach gateway action dispatch.
for meta in "*';'*" "*'&'*" "*'|'*" "*'\$'*" "*'\`'*" "*'<'*" "*'>'*" "*'('*" "*')'*" "*'\\\\'*"; do
	contains "$meta" "$GATEWAY"
done

# Regression for the old decimal-prefix bug: group/world write bits are
# rejected even when a mode begins with an otherwise permitted 06 prefix.
archive_mode_allowed() {
	local mode=$1
	(( (8#$mode & 022) == 0 ))
}
archive_mode_allowed 600 || fail '0600 archive mode should be accepted'
archive_mode_allowed 640 || fail '0640 archive mode should be accepted'
archive_mode_allowed 620 && fail '0620 archive mode must be rejected'
archive_mode_allowed 602 && fail '0602 archive mode must be rejected'
archive_mode_allowed 666 && fail '0666 archive mode must be rejected'

# Signed release envelope and exact caps are part of the trusted boundary.
contains 'ARCHIVE_MAX_BYTES=536870912' "$VERIFY"
contains 'ARCHIVE_MAX_MEMBERS=${#ALL_MEMBERS[@]}' "$VERIFY"
contains 'ARCHIVE_MAX_UNCOMPRESSED_BYTES=536870912' "$VERIFY"
contains 'MANIFEST_MAX_BYTES=131072' "$VERIFY"
contains 'release.manifest.sig' "$VERIFY"
contains 'openssl pkeyutl -verify' "$VERIFY"
contains 'bounded_tar()' "$VERIFY"
contains 'timeout --foreground --kill-after=2s' "$VERIFY"
contains 'ulimit -t "$1"' "$VERIFY"
contains 'ulimit -v "$2"' "$VERIFY"
contains 'bounded_tar_listing()' "$VERIFY"
contains 'bounded_tar_listing --numeric-owner --full-time -tvzf "$ARCHIVE"' "$VERIFY"
contains 'mkfifo -m 0600 "$tar_listing_fifo"' "$VERIFY"
contains 'kill -TERM -- "-$listing_pid"' "$VERIFY"
contains 'max_listing_bytes="$TAR_LISTING_LIMIT_BYTES"' "$VERIFY"
contains 'awk -v max_members="$ARCHIVE_MAX_MEMBERS"' "$VERIFY"
archive_cap_line=$(grep -n 'archive_size=.*stat' "$VERIFY" | cut -d: -f1)
bounded_tar_line=$(grep -n '^bounded_tar()' "$VERIFY" | cut -d: -f1)
[[ -n "$archive_cap_line" && -n "$bounded_tar_line" && "$archive_cap_line" -lt "$bounded_tar_line" ]] \
	|| fail 'compressed archive cap is not checked before tar processing'
contains 'ARCHIVE_INGEST_TIMEOUT=120' "$GATEWAY"
contains 'HELM_RELEASE_SIGNING_KEY_FILE' "$BUILD_BUNDLE"
contains 'HELM_RELEASE_SHA=%s' "$BUILD_BUNDLE"
contains 'owner environment already contains a release SHA' "$BUILD_BUNDLE"
contains 'CLOUDFLARED_VERSION=2026.8.2' "$BUILD_BUNDLE"
contains 'CLOUDFLARED_SHA256=fcfb02b575a52ca1af2e3267af4e1517bcdeb30ac48c834c69abaed3c0576ad2' "$BUILD_BUNDLE"

# Guest state ownership and lifecycle safeguards.
contains 'DATA_DIR="$STATE_DIR/data"' "$INSTALL"
contains 'install -d -m 0755 -o root -g root "$STATE_DIR"' "$INSTALL"
contains 'install -d -m 0750 -o roadmap -g roadmap "$DATA_DIR"' "$INSTALL"
contains 'HELM_DEPLOY_LOCK_HELD=1' "$INSTALL"
contains 'healthy_revision()' "$INSTALL"
contains 'validate_release_env()' "$INSTALL"
contains 'sha256sum --check --strict "${checksum##*/}"' "$INSTALL"
contains 'X-Roadmap-Revision' "$INSTALL"
contains 'loopback_listener()' "$INSTALL"
contains '127\.0\.0\.1:8080|\[::1\]:8080' "$INSTALL"
contains 'install -m 0640 -o root -g root "$RELEASE_DIR/roadmap.env" "$new_target/roadmap.env"' "$INSTALL"
contains 'stop_unit cloudflared.service' "$INSTALL"
contains 'stop_unit helm.service' "$INSTALL"
contains 'HELM_MIGRATION_INFO_BINARY' "$INSTALL"
contains 'schema-preflight' "$INSTALL"
contains 'verified pre-upgrade backup' "$INSTALL"
contains "pre_upgrade_backup=%s source_schema=%s candidate_schema=%s latest_schema=%s migration_digest=%s checksum=%s integrity=%s fk=%s preflight=%s" "$INSTALL"
contains 'proof_backup=$(basename -- "$verified_backup")' "$INSTALL"
contains 'proof_migration_digest=$preflight_digest' "$INSTALL"
not_contains 'preflight_digest" = "$proof_migration_digest' "$INSTALL"
contains 'sha256sum --check --strict "$(basename -- "$checksum_sidecar")"' "$INSTALL"
contains 'PRAGMA integrity_check;' "$INSTALL"
contains 'PRAGMA foreign_key_check;' "$INSTALL"
contains 'fresh-install candidate schema preflight' "$INSTALL"
contains 'legacy database layout requires an explicit offline maintenance migration; no services were stopped' "$INSTALL"
contains 'migration-info' "$BACKUP"
contains 'migration-info' "$ROOT_DIR/cmd/helm/main.go"
not_contains 'systemctl stop cloudflared.service 2>/dev/null || true' "$INSTALL"
not_contains 'systemctl stop helm.service 2>/dev/null || true' "$INSTALL"
contains 'Keep guest installer stdout/stderr attached' "$GATEWAY"
contains 'pre_upgrade_backup=...' "$GATEWAY"

# A retained binary may predate migration-info. The installer must ask the
# candidate binary for metadata rather than selecting the retained executable
# and failing before the first metadata-bearing backup is published.
old_binary="$fixture/retained-old-roadmap"
printf '#!/usr/bin/env bash\nexit 64\n' > "$old_binary"
chmod 0755 "$old_binary"
if "$old_binary" migration-info >/dev/null 2>&1; then
	fail 'old retained binary fixture unexpectedly implements migration-info'
fi
contains 'migration_info_binary="$RELEASE_DIR/roadmap"' "$INSTALL"

# Postfix is not used by Roadmap. Keep the aggregate, template, and generated
# default instance names explicit so the active postfix@-.service unit is
# stopped and future package actions cannot reactivate any of them.
contains 'disable_unused_postfix()' "$INSTALL"
contains 'for unit in postfix.service postfix@-.service; do' "$INSTALL"
contains 'for unit in postfix.service postfix@.service postfix@-.service; do' "$INSTALL"
contains 'systemctl disable "$unit"' "$INSTALL"
contains 'systemctl mask "$unit"' "$INSTALL"
contains 'systemctl is-enabled "$unit"' "$INSTALL"
not_contains 'systemctl stop postfix*' "$INSTALL"
not_contains 'systemctl disable postfix*' "$INSTALL"
not_contains 'systemctl mask postfix*' "$INSTALL"
not_contains 'apt-get purge postfix' "$INSTALL"

# Exercise the exact-unit handling with an active default instance, an
# indirect template, and a second idempotent invocation. The template does
# not have an instantiated state to inspect, so this mock fails if the
# implementation tries to query it with `systemctl show`.
source <(awk '/^unit_state\(\)/,/^}/' "$INSTALL")
SERVICE_STOP_TIMEOUT=30
source <(awk '/^stop_unit\(\)/,/^}/' "$INSTALL")
source <(awk '/^disable_unused_postfix\(\)/,/^}/' "$INSTALL")
postfix_call_log="$fixture/postfix.calls"
postfix_service_state=active
postfix_instance_state=active
postfix_mock_absent=0
postfix_template_show_attempt=0
declare -A postfix_masked=()
: > "$postfix_call_log"
postfix_mock_systemctl() {
	local action=${1:-} unit=${2:-} state
	case "$action" in
		show)
			case "$unit" in
				postfix.service|postfix@-.service) printf 'loaded\n' ;;
				postfix@.service) postfix_template_show_attempt=1; return 1 ;;
				*) return 1 ;;
			esac
			;;
		is-active)
			case "$unit" in
				postfix.service) state=$postfix_service_state ;;
				postfix@-.service) state=$postfix_instance_state ;;
				*) state=unknown ;;
			esac
			printf '%s\n' "$state"
			;;
		stop)
			printf 'stop %s\n' "$unit" >> "$postfix_call_log"
			case "$unit" in
				postfix.service) postfix_service_state=inactive ;;
				postfix@-.service) postfix_instance_state=inactive ;;
				*) return 1 ;;
			esac
			;;
		is-enabled)
			if [[ "${postfix_masked[$unit]:-0}" = 1 ]]; then
				printf 'masked\n'
				return 1
			fi
			if (( postfix_mock_absent )); then
				printf 'not-found\n'
				return 1
			fi
			case "$unit" in
				postfix.service) printf 'enabled\n' ;;
				postfix@.service) printf 'indirect\n' ;;
				postfix@-.service) printf 'enabled-runtime\n' ;;
				*) return 1 ;;
			esac
			;;
		disable)
			printf 'disable %s\n' "$unit" >> "$postfix_call_log"
			;;
		mask)
			printf 'mask %s\n' "$unit" >> "$postfix_call_log"
			postfix_masked[$unit]=1
			;;
		*) return 1 ;;
	esac
}
systemctl() { postfix_mock_systemctl "$@"; }
postfix_expected_first=$'stop postfix.service\nstop postfix@-.service\ndisable postfix.service\nmask postfix.service\nmask postfix@.service\ndisable postfix@-.service\nmask postfix@-.service'
disable_unused_postfix || fail 'postfix hardening rejected an active default instance'
[[ "$postfix_template_show_attempt" = 0 ]] || fail 'postfix hardening queried the uninstantiated template state'
[[ "$(sed -n '1,7p' "$postfix_call_log")" = "$postfix_expected_first" ]] \
	|| fail 'postfix hardening did not stop, disable, and mask the exact units'
[[ "$postfix_service_state" = inactive && "$postfix_instance_state" = inactive ]] \
	|| fail 'postfix hardening left an active service or default instance'
disable_unused_postfix || fail 'postfix hardening was not idempotent'
[[ "$(sed -n '8,10p' "$postfix_call_log")" = $'mask postfix.service\nmask postfix@.service\nmask postfix@-.service' ]] \
	|| fail 'postfix hardening repeated more than the exact mask operations'

# A postfix-free image has no service/default instance state; all three masks
# must still be created and verified so a later package installation cannot
# enable the service.
postfix_mock_absent=1
postfix_service_state=unknown
postfix_instance_state=unknown
postfix_masked=()
: > "$postfix_call_log"
disable_unused_postfix || fail 'postfix-free image was not handled idempotently'
[[ "$(<"$postfix_call_log")" = $'mask postfix.service\nmask postfix@.service\nmask postfix@-.service' ]] \
	|| fail 'postfix-free image did not receive all three exact masks'
unset -f systemctl postfix_mock_systemctl
unset postfix_service_state postfix_instance_state postfix_mock_absent postfix_template_show_attempt
unset postfix_call_log postfix_masked
printf 'postfix_hardening_runtime_tests=ok\n'

# The listener check must recognize only an exact loopback local-address field
# in ss output. It must reject non-loopback listeners, port-prefix matches,
# loopback peers, and an ss command failure.
source <(awk '/^loopback_listener\(\)/,/^}/' "$INSTALL")
loopback_ss_output=$'State  Recv-Q Send-Q Local Address:Port  Peer Address:Port\nLISTEN 0 128 127.0.0.1:8080 0.0.0.0:*\nLISTEN 0 128 [::1]:8080 [::]:*'
ss() { [[ "$1" = -ltn ]] && printf '%s\n' "$loopback_ss_output"; }
loopback_listener || fail 'listener check rejected canonical IPv4/IPv6 loopback output'
for loopback_bad_ss_output in \
	$'State  Recv-Q Send-Q Local Address:Port  Peer Address:Port\nLISTEN 0 128 10.0.0.38:8080 0.0.0.0:*' \
	$'State  Recv-Q Send-Q Local Address:Port  Peer Address:Port\nLISTEN 0 128 127.0.0.1:80801 0.0.0.0:*' \
	$'State  Recv-Q Send-Q Local Address:Port  Peer Address:Port\nLISTEN 0 128 0.0.0.0:1234 127.0.0.1:8080'; do
	loopback_ss_output=$loopback_bad_ss_output
	if loopback_listener; then
		fail "listener check accepted invalid ss output: $loopback_bad_ss_output"
	fi
done
ss() { return 1; }
if loopback_listener; then
	fail 'listener check accepted an ss command failure'
fi
unset -f ss
printf 'loopback_listener_runtime_tests=ok\n'

# The service ExecStart resolves through the current release link. Switch it
# before systemd verifies the unit and before starting the new application so
# a clean install cannot fail verification on a missing executable.
install_switch_line=$(grep -n '^atomic_switch "\$release_target"' "$INSTALL" | cut -d: -f1 || true)
install_verify_line=$(grep -n '^systemd-analyze verify ' "$INSTALL" | cut -d: -f1 || true)
install_helm_start_line=$(grep -n '^systemctl start helm\.service$' "$INSTALL" | cut -d: -f1 || true)
[[ -n "$install_switch_line" && -n "$install_verify_line" && -n "$install_helm_start_line" ]] \
	|| fail 'install ordering regression checks could not find the release switch, unit verification, and Helm start'
[[ "$install_switch_line" -lt "$install_verify_line" ]] \
	|| fail 'install switches the current release after systemd unit verification'
[[ "$install_switch_line" -lt "$install_helm_start_line" ]] \
	|| fail 'install starts Helm before switching the current release'
contains 'WorkingDirectory=/var/lib/roadmap/data' "$SERVICE"
contains 'ReadWritePaths=/var/lib/roadmap/data' "$SERVICE"
not_contains 'ReadWritePaths=/var/lib/roadmap ' "$SERVICE"
contains 'RandomizedDelaySec=1h' "$DEPLOY_DIR/helm-backup.timer"
contains 'Persistent=true' "$DEPLOY_DIR/helm-backup.timer"
contains 'helm-backup.timer' "$INSTALL"
contains 'HELM_DATA_DIR' "$BACKUP"
contains 'HELM_DATA_DIR' "$RESTORE"
contains 'HELM_DEPLOY_LOCK_HELD' "$BACKUP"
contains 'schema_version' "$BACKUP"
contains 'migration_digest' "$BACKUP"
contains 'PRAGMA foreign_key_check' "$BACKUP"
contains 'schema_version' "$RESTORE"
contains 'migration_digest' "$RESTORE"
contains 'PRAGMA foreign_key_check' "$RESTORE"
contains 'migration-apply' "$RESTORE"
contains 'migrate_staged_candidate "$candidate"' "$RESTORE"
contains 'metadata release does not match its filename' "$RESTORE"
contains 'email = NULL' "$RESTORE"
contains 'password_hash = NULL' "$RESTORE"
contains 'admin = 0' "$RESTORE"
contains 'description = (SELECT c.description' "$RESTORE"
contains 'current_auth.actors' "$RESTORE"
contains 'current_auth.auth_setup' "$RESTORE"
contains 'current_auth.actor_projects' "$RESTORE"
contains 'current_auth.actor_resource_usage' "$RESTORE"
contains 'CREATE TABLE IF NOT EXISTS actor_resource_usage' "$RESTORE"
contains 'RETENTION=$(compat_env HELM_BACKUP_RETENTION ROADMAP_BACKUP_RETENTION 14)' "$RESTORE"
contains 'prune_backups()' "$RESTORE"
contains 'prune_backups "$current_snapshot" "$BACKUP_PATH"' "$RESTORE"
contains 'protected_recovery' "$RESTORE"
contains 'protected_selected' "$RESTORE"
contains '[[ $# -eq 1 ]] || usage' "$RESTORE"
contains 'backup must be directly inside the retained backup directory' "$RESTORE"
contains 'backup filename is not a retained Roadmap backup' "$RESTORE"
contains 'DELETE FROM sessions' "$RESTORE"
contains 'DELETE FROM tokens' "$RESTORE"
contains 'DELETE FROM idempotency_keys' "$RESTORE"
contains 'Never swap a raw' "$DOCS"
not_contains 'atomically move it to /var/lib/roadmap/roadmap.db' "$DOCS"

# The candidate gate must complete while the application is still online and
# before the installer stops either unit or switches the current release link.
install_backup_line=$(grep -n 'roadmap-backup.sh" "\$SHA"' "$INSTALL" | cut -d: -f1 || true)
install_preflight_line=$(grep -n 'schema-preflight' "$INSTALL" | sed -n '1p' | cut -d: -f1 || true)
install_proof_line=$(grep -n "^printf 'pre_upgrade_backup=" "$INSTALL" | cut -d: -f1 || true)
install_stop_line=$(grep -n '^stop_unit cloudflared\.service' "$INSTALL" | tail -n 1 | cut -d: -f1 || true)
install_switch_line=$(grep -n '^atomic_switch "\$release_target"' "$INSTALL" | cut -d: -f1 || true)
legacy_refusal_line=$(grep -n 'legacy database layout requires an explicit offline maintenance migration' "$INSTALL" | cut -d: -f1 || true)
[[ -n "$install_backup_line" && -n "$install_preflight_line" && -n "$install_proof_line" && -n "$install_stop_line" && -n "$install_switch_line" && -n "$legacy_refusal_line" ]] \
	|| fail 'install migration-gate ordering checks could not find backup/preflight/proof/stop/switch'
[[ "$install_backup_line" -lt "$install_preflight_line" && "$install_preflight_line" -lt "$install_proof_line" && "$install_proof_line" -lt "$install_stop_line" && "$install_stop_line" -lt "$install_switch_line" ]] \
	|| fail 'install does not gate the proof and atomic switch on a verified preflight'
[[ "$legacy_refusal_line" -lt "$install_stop_line" ]] \
	|| fail 'legacy database refusal must occur before any normal deployment downtime'

# The proof is deliberately one line, basename-only, and machine-parseable.
# Keep this assertion in the deployment-security suite so a future change
# cannot accidentally put credentials or broad guest paths on the CI stream.
assert_proof_format() {
	local proof=$1
	[[ "$proof" =~ ^pre_upgrade_backup=(none|roadmap-[0-9]{8}T[0-9]{6}Z-(manual|daily|pre-restore|[0-9a-f]{40})\.db)[[:space:]]source_schema=[0-9]+[[:space:]]candidate_schema=[0-9]+[[:space:]]latest_schema=[0-9]+[[:space:]]migration_digest=[0-9a-f]{64}[[:space:]]checksum=valid[[:space:]]integrity=ok[[:space:]]fk=ok[[:space:]]preflight=ok$ ]] \
		|| fail "invalid sanitized deployment proof: $proof"
}
assert_proof_format 'pre_upgrade_backup=roadmap-20260828T173000Z-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.db source_schema=9 candidate_schema=10 latest_schema=10 migration_digest=0000000000000000000000000000000000000000000000000000000000000000 checksum=valid integrity=ok fk=ok preflight=ok'
assert_proof_format 'pre_upgrade_backup=none source_schema=0 candidate_schema=10 latest_schema=10 migration_digest=ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff checksum=valid integrity=ok fk=ok preflight=ok'
not_contains 'roadmap.db' "$ROLLBACK"

# Exercise the gateway cleanup predicate with a mocked pct boundary. A failed
# pct create is allowed to leave a partial configuration, but the transaction
# must not mark that CT as owned. If a CT was marked created, cleanup must
# re-read its reviewed configuration after stopping it and refuse destruction
# when the configuration has drifted.
source <(awk '/^ct_config_field\(\)/,/^}/' "$GATEWAY")
source <(awk '/^ct_option_matches\(\)/,/^}/' "$GATEWAY")
source <(awk '/^ct_net0_is_exact\(\)/,/^}/' "$GATEWAY")
source <(awk '/^ct_rootfs_is_exact\(\)/,/^}/' "$GATEWAY")
source <(awk '/^ct_tags_are_exact\(\)/,/^}/' "$GATEWAY")
source <(awk '/^target_vmid_state\(\)/,/^}/' "$GATEWAY")
source <(awk '/^target_vmid_is_lxc_for_cleanup\(\)/,/^}/' "$GATEWAY")
source <(awk '/^ct_config_matches_helm_identity\(\)/,/^}/' "$GATEWAY")
source <(awk '/^cleanup_deploy\(\)/,/^}/' "$GATEWAY")
log() { printf '[gateway-fixture] %s\n' "$*" >&2; }
CTID=103
CT_NAME=roadmap
CT_IP=10.0.0.38
CT_ARCH=amd64
CT_CORES=1
CT_MEMORY_MB=2048
CT_SWAP_MB=512
CT_ROOTFS_STORAGE=local-lvm
CT_ROOTFS_SIZE=16G
CT_NET_NAME=eth0
CT_NET_BRIDGE=vmbr0
CT_NET_GATEWAY=10.0.0.1
CT_NET_TYPE=veth
qm() {
	printf "Configuration file 'nodes/pve/qemu-server/%s.conf' does not exist\n" "$CTID" >&2
	return 1
}
gateway_canonical_config=$'hostname: roadmap\nunprivileged: 1\nnet0: name=eth0,bridge=vmbr0,gw=10.0.0.1,hwaddr=BC:24:11:12:34:56,ip=10.0.0.38/24,type=veth\narch: amd64\nonboot: 1\nostype: debian\ncores: 1\nmemory: 2048\nswap: 512\nrootfs: local-lvm:vm-103-disk-0,size=16G\nnameserver: 10.0.0.1 1.1.1.1\nsearchdomain: lan\nstartup: order=5,up=10,down=30\ntags: lan;roadmap;service'

# A QEMU guest occupying the reviewed VMID must fail status before pct is
# consulted; otherwise pct config would report "not found" and status would
# incorrectly return current_sha=none for the user's VM.
gateway_status_script="$fixture/roadmap-gateway-status-test.sh"
sed -e '/must run as root/d' -e '/root deployment directory owner is invalid/d' \
	-e "s|DEPLOY_ROOT=/var/lib/roadmap-deploy|DEPLOY_ROOT=$fixture/gateway-status-deploy|" \
	"$GATEWAY" > "$gateway_status_script"
chmod 0755 "$gateway_status_script"
gateway_status_calls="$fixture/gateway-status.calls"
gateway_status_output="$fixture/gateway-status.out"
: > "$gateway_status_calls"
mkdir -m 0700 "$fixture/gateway-status-deploy"
gateway_status_qm() { printf 'name: ubuntu-gaming\n'; }
gateway_status_pct() {
	printf '%s\n' "$*" >> "$GATEWAY_STATUS_CALLS"
	return 1
}
if (
	export GATEWAY_STATUS_CALLS="$gateway_status_calls"
	qm() { gateway_status_qm "$@"; }
	pct() { gateway_status_pct "$@"; }
	export -f qm pct gateway_status_qm gateway_status_pct
	HELM_GATEWAY_LOCAL_TEST=1 "$gateway_status_script" status
) >"$gateway_status_output" 2>&1; then
	fail 'gateway status accepted a QEMU VMID collision'
fi
contains 'VMID 103 is assigned to a QEMU VM' "$gateway_status_output"
not_contains 'current_sha=none' "$gateway_status_output"
[[ ! -s "$gateway_status_calls" ]] || fail 'gateway status touched pct during a QEMU VMID collision'
printf 'gateway_qemu_vmid_collision_test=ok\n'

gateway_drift_config=${gateway_canonical_config/hostname: roadmap/hostname: unrelated}
gateway_extra_option_config="$gateway_canonical_config"$'\nfeatures: nesting=1'
gateway_extra_net_config=${gateway_canonical_config/type=veth/type=veth,rate=100}
ct_config_matches_helm_identity "$gateway_canonical_config" \
	|| fail 'canonical Helm CT configuration was rejected'
if ct_config_matches_helm_identity "$gateway_extra_option_config"; then
	fail 'Helm CT configuration accepted an unreviewed top-level option'
fi
if ct_config_matches_helm_identity "$gateway_extra_net_config"; then
	fail 'Helm CT configuration accepted an unreviewed network option'
fi
gateway_pct_mode=
gateway_pct_config_calls=
gateway_pct_calls=
pct() {
	local action=${1:-} count
	case "$action" in
		config)
			count=$(wc -l < "$gateway_pct_config_calls")
			count=$((count + 1))
			printf '%s\n' "$count" >> "$gateway_pct_config_calls"
			if [[ "$gateway_pct_mode" = drift-after-stop && "$count" -ge 3 ]]; then
				printf '%s\n' "$gateway_drift_config"
			else
				printf '%s\n' "$gateway_canonical_config"
			fi
			;;
		create|exec|stop|destroy)
			printf '%s\n' "$*" >> "$gateway_pct_calls"
			if [[ "$action" = create && "$gateway_pct_mode" = partial-create ]]; then
				return 1
			fi
			;;
		*) return 1 ;;
	esac
}

gateway_pct_mode=partial-create
gateway_pct_config_calls="$fixture/gateway-partial-config.calls"
gateway_pct_calls="$fixture/gateway-partial.calls"
: > "$gateway_pct_config_calls"
: > "$gateway_pct_calls"
ARCHIVE="$fixture/gateway-partial.archive"
printf 'partial archive\n' > "$ARCHIVE"
created=0
healthy=0
if pct create "$CTID" reviewed-template; then
	created=1
fi
[[ "$created" = 0 ]] || fail 'failed pct create incorrectly marked the CT as created'
if (set +e; false; cleanup_deploy) >"$fixture/gateway-partial.out" 2>&1; then
	fail 'partial pct create cleanup unexpectedly succeeded'
fi
! grep -Eq '^destroy ' "$gateway_pct_calls" \
	|| fail 'partial pct create cleanup attempted to destroy a CT'

gateway_pct_mode=drift-after-stop
gateway_pct_config_calls="$fixture/gateway-drift-config.calls"
gateway_pct_calls="$fixture/gateway-drift.calls"
: > "$gateway_pct_config_calls"
: > "$gateway_pct_calls"
ARCHIVE="$fixture/gateway-drift.archive"
printf 'drift archive\n' > "$ARCHIVE"
created=1
healthy=0
if (set +e; false; cleanup_deploy) >"$fixture/gateway-drift.out" 2>&1; then
	fail 'configuration-drift cleanup unexpectedly succeeded'
fi
grep -Eq '^stop ' "$gateway_pct_calls" \
	|| fail 'configuration-drift cleanup did not stop the CT before rechecking it'
! grep -Eq '^destroy ' "$gateway_pct_calls" \
	|| fail 'configuration-drift cleanup destroyed a changed CT'
contains 'refusing to destroy CT 103 after its configuration changed' "$fixture/gateway-drift.out"

# Rollback is transactional across both units: an app health failure and a
# connector start/active failure restore the previous link, restart the prior
# app and tunnel, and report the requested rollback as failed.
contains 'start_and_verify_previous()' "$ROLLBACK"
contains 'install_release_env "$TARGET" "$SHA"' "$ROLLBACK"
contains 'healthy_revision "$SHA"' "$ROLLBACK"
contains 'X-Roadmap-Revision' "$ROLLBACK"
contains 'validate_ct_config()' "$GATEWAY"
contains 'pct config "$CTID"' "$GATEWAY"
contains 'current_sha=none' "$GATEWAY"
contains 'created=1' "$GATEWAY"
not_contains 'systemctl stop cloudflared.service 2>/dev/null || true' "$ROLLBACK"
not_contains 'systemctl stop helm.service 2>/dev/null || true' "$ROLLBACK"
not_contains 'systemctl stop cloudflared.service >/dev/null 2>&1 || true' "$RESTORE"
not_contains 'systemctl stop helm.service >/dev/null 2>&1 || true' "$RESTORE"
contains "start_and_verify_previous 'requested release failed app health'" "$ROLLBACK"
contains "start_and_verify_previous 'requested release failed cloudflared validation'" "$ROLLBACK"
count_contains 2 'if start_and_verify_previous' "$ROLLBACK"
contains 'atomic_switch "$previous_target"' "$ROLLBACK"
contains 'systemctl start helm.service || return 1' "$ROLLBACK"
contains 'systemctl start cloudflared.service || return 1' "$ROLLBACK"
contains 'systemctl is-active --quiet helm.service || return 1' "$ROLLBACK"
contains 'systemctl is-active --quiet roadmap.service || return 1' "$ROLLBACK"
contains 'systemctl is-active --quiet cloudflared.service || return 1' "$ROLLBACK"
contains 'exit 1' "$ROLLBACK"

# Run the real rollback transaction against a fixture state root. The service,
# health, install, and rename boundaries are mocked, while release checks,
# link switching, EXIT recovery, and exact environment restoration remain
# production code. Non-root developer runs use a temporary copy with only the
# root-owner preflight removed; no production file is changed.
rollback_test_script=$ROLLBACK
if [[ "$(id -u)" -ne 0 ]]; then
	rollback_test_script="$fixture/roadmap-rollback-test.sh"
	sed -e '/must run as root/d' -e '/state directory owner is invalid/d' \
		"$ROLLBACK" > "$rollback_test_script"
	chmod 0755 "$rollback_test_script"
fi

rollback_systemctl() {
	local action=${1:-} unit=${2:-} state_file state current
	case "$action" in
		show)
			printf 'loaded\n'
			;;
		is-active)
			unit=${!#}
			[[ "$unit" = roadmap.service ]] && unit=helm.service
			state_file="$ROLLBACK_SERVICE_STATE_DIR/${unit%.service}"
			state=$(<"$state_file")
			if [[ "${1:-}" = is-active && "${2:-}" = --quiet ]]; then
				[[ "$state" = active ]]
			else
				printf '%s\n' "$state"
			fi
			;;
		stop)
			unit=$2
			[[ "$unit" = roadmap.service ]] && unit=helm.service
			state_file="$ROLLBACK_SERVICE_STATE_DIR/${unit%.service}"
			printf 'stop %s\n' "$unit" >> "$ROLLBACK_CALL_LOG"
			printf 'inactive\n' > "$state_file"
			;;
		start)
			unit=$2
			[[ "$unit" = roadmap.service ]] && unit=helm.service
			state_file="$ROLLBACK_SERVICE_STATE_DIR/${unit%.service}"
			current=$(readlink -- "$ROLLBACK_CURRENT_LINK" 2>/dev/null || true)
			if [[ "$current" = "$ROLLBACK_TARGET_DIR" &&
				( "$ROLLBACK_MODE" = app-restart-failure && "$unit" = helm.service ||
				  "$ROLLBACK_MODE" = connector-restart-failure && "$unit" = cloudflared.service ) &&
				! -e "$ROLLBACK_START_FAILURE_MARKER" ]]; then
				: > "$ROLLBACK_START_FAILURE_MARKER"
				printf 'start-failure %s\n' "$unit" >> "$ROLLBACK_CALL_LOG"
				printf 'failed\n' > "$state_file"
				return 1
			fi
			printf 'start %s\n' "$unit" >> "$ROLLBACK_CALL_LOG"
			printf 'active\n' > "$state_file"
			;;
		*) return 1 ;;
	esac
}

rollback_curl() {
	local current revision
	current=$(readlink -- "$ROLLBACK_CURRENT_LINK" 2>/dev/null || true)
	revision=${current##*/}
	printf 'HTTP/1.1 200 OK\nX-Roadmap-Revision: %s\n\n{"revision":"%s"}\n' \
		"$revision" "$revision"
}

rollback_install() {
	local -a args=()
	while [[ $# -gt 0 ]]; do
		case "$1" in
			-o|-g) shift 2 ;;
			*) args+=("$1"); shift ;;
		esac
	done
	local source=${args[$(( ${#args[@]} - 2 ))]} destination=${args[$(( ${#args[@]} - 1 ))]}
	if [[ "$ROLLBACK_MODE" = install-before-switch-failure &&
		"$source" = "$ROLLBACK_TARGET_DIR/roadmap.env" &&
		! -e "$ROLLBACK_INSTALL_FAILURE_MARKER" ]]; then
		: > "$ROLLBACK_INSTALL_FAILURE_MARKER"
		printf 'install-failure\n' >> "$ROLLBACK_CALL_LOG"
		return 1
	fi
	command install "${args[@]}"
	chmod 0640 -- "$destination"
}

rollback_mv() {
	local -a args=("$@") source destination
	source=${args[$(( ${#args[@]} - 2 ))]}
	destination=${args[$(( ${#args[@]} - 1 ))]}
	if [[ "$ROLLBACK_MODE" = symlink-switch-failure &&
		"$destination" = "$ROLLBACK_CURRENT_LINK" && -L "$source" &&
		"$(readlink -- "$source")" = "$ROLLBACK_TARGET_DIR" &&
		! -e "$ROLLBACK_SWITCH_FAILURE_MARKER" ]]; then
		: > "$ROLLBACK_SWITCH_FAILURE_MARKER"
		printf 'switch-failure\n' >> "$ROLLBACK_CALL_LOG"
		return 1
	fi
	command mv "$@"
}

rollback_sleep() { :; }

rollback_run_case() {
	local mode=$1 case_dir="$fixture/rollback-$1" status
	local previous_sha=1111111111111111111111111111111111111111
	local target_sha=2222222222222222222222222222222222222222
	local state_dir="$case_dir/state" releases_dir="$case_dir/state/releases"
	local config_dir="$case_dir/config" service_state_dir="$case_dir/services"
	local previous_dir="$releases_dir/$previous_sha" target_dir="$releases_dir/$target_sha"
	local call_log="$case_dir/calls.log" output="$case_dir/output.log"
	/usr/bin/install -d -m 0755 "$releases_dir" "$config_dir" "$service_state_dir"
	chmod 0755 "$state_dir"
	/usr/bin/install -d -m 0755 "$previous_dir" "$target_dir"
	printf 'previous release binary\n' > "$previous_dir/roadmap"
	printf 'target release binary\n' > "$target_dir/helm"
	chmod 0755 "$previous_dir/roadmap" "$target_dir/helm"
	(
		cd "$previous_dir" && sha256sum roadmap > roadmap.sha256
	)
	(
		cd "$target_dir" && sha256sum helm > helm.sha256
	)
	printf 'ROADMAP_RELEASE_SHA=%s\nROADMAP_ENV_MARKER=previous\n' "$previous_sha" > "$previous_dir/roadmap.env"
	printf 'HELM_RELEASE_SHA=%s\nROADMAP_RELEASE_SHA=%s\nHELM_ENV_MARKER=target\n' "$target_sha" "$target_sha" > "$target_dir/roadmap.env"
	cp -- "$previous_dir/roadmap.env" "$case_dir/previous.env.ref"
	ln -s -- "$previous_dir" "$state_dir/current"
	printf 'ROADMAP_RELEASE_SHA=%s\nROADMAP_ENV_MARKER=live-before\n' "$previous_sha" > "$config_dir/roadmap.env"
	printf 'active\n' > "$service_state_dir/helm"
	printf 'active\n' > "$service_state_dir/cloudflared"
	: > "$call_log"

	ROLLBACK_MODE=$mode
	ROLLBACK_STATE_DIR="$state_dir"
	ROLLBACK_CURRENT_LINK="$state_dir/current"
	ROLLBACK_TARGET_DIR="$target_dir"
	ROLLBACK_CALL_LOG="$call_log"
	ROLLBACK_SERVICE_STATE_DIR="$service_state_dir"
	ROLLBACK_START_FAILURE_MARKER="$case_dir/start-failure"
	ROLLBACK_INSTALL_FAILURE_MARKER="$case_dir/install-failure"
	ROLLBACK_SWITCH_FAILURE_MARKER="$case_dir/switch-failure"
	ROLLBACK_TARGET_SHA="$target_sha"
	ROLLBACK_CONFIG_DIR="$config_dir"
	export ROLLBACK_MODE ROLLBACK_CURRENT_LINK ROLLBACK_TARGET_DIR ROLLBACK_CALL_LOG \
		ROLLBACK_SERVICE_STATE_DIR ROLLBACK_START_FAILURE_MARKER ROLLBACK_INSTALL_FAILURE_MARKER \
		ROLLBACK_SWITCH_FAILURE_MARKER ROLLBACK_TARGET_SHA
	set +e
	(
		export ROADMAP_STATE_DIR="$state_dir" ROADMAP_CONFIG_DIR="$config_dir"
		export -f rollback_systemctl rollback_curl rollback_install rollback_mv rollback_sleep
		systemctl() { rollback_systemctl "$@"; }
		curl() { rollback_curl "$@"; }
		install() { rollback_install "$@"; }
		mv() { rollback_mv "$@"; }
		sleep() { rollback_sleep "$@"; }
		export -f systemctl curl install mv sleep
		"$rollback_test_script" "$target_sha"
	) >"$output" 2>&1
	status=$?
	set -e
	[[ "$status" -ne 0 ]] || fail "rollback $mode unexpectedly succeeded"
	cmp -s "$case_dir/previous.env.ref" "$config_dir/roadmap.env" \
		|| fail "rollback $mode did not restore the exact previous environment"
	[[ "$(readlink -- "$state_dir/current")" = "$previous_dir" ]] \
		|| fail "rollback $mode did not restore the previous current link"
	[[ "$(<"$service_state_dir/helm")" = active && "$(<"$service_state_dir/cloudflared")" = active ]] \
		|| fail "rollback $mode did not restore both prior services"
	grep -Eq '^start helm\.service$' "$call_log" \
		|| fail "rollback $mode did not restart helm.service"
	grep -Eq '^start cloudflared\.service$' "$call_log" \
		|| fail "rollback $mode did not restart cloudflared.service"
}

rollback_run_case install-before-switch-failure
rollback_run_case symlink-switch-failure
rollback_run_case app-restart-failure
rollback_run_case connector-restart-failure
printf 'rollback_recovery_runtime_tests=ok\n'

# Cloudflare API bearer material is supplied through a mode-0600 curl config
# header file, never in a curl argv. Policy checks are exact-set checks: any
# unknown or bypass policy causes prepare/live validation to fail.
for file in "$CLOUDFLARE" "$VALIDATE"; do
	contains 'CF_HEADER_FILE=$(mktemp' "$file"
	contains 'chmod 0600 "$CF_HEADER_FILE"' "$file"
	contains '--header "@$CF_HEADER_FILE"' "$file"
	contains 'trap cleanup EXIT' "$file"
	not_contains '--header "Authorization: Bearer' "$file"
done
contains "SERVICE_POLICY_NAME='Helm agents Service Auth'" "$CLOUDFLARE"
contains 'audTag:[$ui,$aud]' "$CLOUDFLARE"
contains 'validate_policy_set' "$CLOUDFLARE"
contains 'duration:"8760h"' "$CLOUDFLARE"
contains 'HELM_REQUIRE_DURABLE_SERVICE_TOKEN_CAPTURE' "$CLOUDFLARE"
contains 'without a durable secret-capture destination' "$CLOUDFLARE"
contains 'expires_at // .expiration_time' "$CLOUDFLARE"
contains 'validate_owner_env()' "$CLOUDFLARE"
contains 'gsub(/@CLOUDFLARE_ISSUER@/, issuer, line)' "$CLOUDFLARE"
contains 'if ! awk -v owner_email=' "$CLOUDFLARE"
contains 'if ! cf_request GET "/accounts/$ACCOUNT_ID/cfd_tunnel/$tunnel_id/token"' "$CLOUDFLARE"
contains 'if ! validate_owner_env "$owner_tmp"' "$CLOUDFLARE"
contains 'restore_prepare_output()' "$CLOUDFLARE"
contains 'token_backup=' "$CLOUDFLARE"
contains 'owner_backup=' "$CLOUDFLARE"
contains 'if ! mv -T -- "$token_tmp" "$TOKEN_OUTPUT"' "$CLOUDFLARE"
contains 'if ! mv -T -- "$owner_tmp" "$OWNER_ENV_OUTPUT"' "$CLOUDFLARE"
contains 'prior outputs restored' "$CLOUDFLARE"
contains 'if ! write_prepare_outputs' "$CLOUDFLARE"
not_contains 'sed -e "s/@CLOUDFLARE_ISSUER@/' "$CLOUDFLARE"
contains '@CLOUDFLARE_ISSUER@' "$DEPLOY_DIR/helm.env.template"
contains '@UI_AUDIENCE@' "$DEPLOY_DIR/helm.env.template"
contains '@API_AUDIENCE@' "$DEPLOY_DIR/helm.env.template"
contains 'HELM_CF_ACCESS_AUDIENCES' "$DEPLOY_DIR/helm.env.template"
contains 'API Access app policy set is not exactly owner Allow plus Service Auth' "$VALIDATE"
contains 'tunnel_config=' "$VALIDATE"
contains 'CF_ACCESS_CLIENT_ID' "$VALIDATE"
contains 'CF_ACCESS_CLIENT_SECRET' "$VALIDATE"
contains 'HELM_REQUIRE_SERVICE_AUTH_PROBE' "$VALIDATE"
contains 'X-Request-ID: $expected_request_id' "$VALIDATE"
contains 'JSON unauthorized' "$VALIDATE"
contains 'validate_access_app()' "$VALIDATE"
contains 'identity_provider_id()' "$VALIDATE"
contains 'access_team_name()' "$VALIDATE"
contains 'restrict_to_account_members' "$CLOUDFLARE"
contains 'restrict_to_account_members' "$VALIDATE"
contains 'identity providers are ambiguous or nonconforming' "$CLOUDFLARE"
contains 'identity provider is missing, ambiguous, or nonconforming' "$VALIDATE"
not_contains 'onetimepin' "$CLOUDFLARE"
not_contains 'onetimepin' "$VALIDATE"
contains '.result.session_duration == "168h"' "$VALIDATE"
contains '.result.allowed_idps // []) == [$idp]' "$VALIDATE"
contains '.result.service_auth_401_redirect // false) == $service401' "$VALIDATE"
contains '$ingress[0].originRequest.access.teamName == $team' "$VALIDATE"
contains '! -L "$SSH_CONFIG"' "$DEPLOY_CI"
contains 'HELM_SSH_CONFIG must be owned by the invoking user and mode 0600 or stricter' "$DEPLOY_CI"

# A freshly-published DNS record can briefly return resolver/connect failures
# or a non-Access status. Exercise the bounded retry loop with a curl sequence
# mock and a no-op sleep, so these regressions never use the network or wait.
source <(awk '/^access_probe_backoff\(\)/,/^}/' "$VALIDATE")
source <(awk '/^access_status\(\)/,/^}/' "$VALIDATE")
source <(awk '/^expect_access\(\)/,/^}/' "$VALIDATE")
PUBLIC_URL=https://tc.shanekanterman.dev
ACCESS_PROBE_MAX_ATTEMPTS=3
ACCESS_PROBE_INITIAL_DELAY_SECONDS=1
ACCESS_PROBE_MAX_DELAY_SECONDS=30
probe_curl() {
	local call event output_file=header_file= arg
	call=$(wc -l < "$PROBE_CALL_LOG")
	call=$((call + 1))
	printf '%s\n' "$call" >> "$PROBE_CALL_LOG"
	event=$(sed -n "${call}p" "$PROBE_RESPONSES")
	if [[ "$PROBE_KIND" = service ]]; then
		while [[ $# -gt 0 ]]; do
			arg=$1
			case "$arg" in
				--output|--dump-header)
					if [[ "$arg" = --output ]]; then
						output_file=$2
					else
						header_file=$2
					fi
					shift 2
					;;
				--write-out) shift 2 ;;
				*) shift ;;
			esac
		done
	fi
	case "$event" in
		dns) return 6 ;;
		connect) return 7 ;;
		302|303|401|500)
			if [[ "$PROBE_KIND" = service ]]; then
				printf 'HTTP/1.1 %s Fixture\r\nContent-Type: application/json\r\nX-Request-ID: helm-service-auth-probe\r\n\r\n' \
					"$event" > "$header_file"
				printf '%s' '{"error":{"code":"unauthorized","message":"fixture"}}' > "$output_file"
			fi
			printf '%s' "$event"
			;;
		*) return 1 ;;
	esac
}
sleep() { printf '%s\n' "$1" >> "$PROBE_SLEEP_LOG"; }
curl() { probe_curl "$@"; }
run_access_probe_case() {
	local name=$1 sequence=$2 expected=$3 case_dir="$fixture/live-probe-$1" output status
	install -d -m 0700 "$case_dir"
	printf '%s\n' "$sequence" > "$case_dir/responses"
	: > "$case_dir/calls"
	: > "$case_dir/sleeps"
	PROBE_KIND=access PROBE_RESPONSES="$case_dir/responses" PROBE_CALL_LOG="$case_dir/calls" \
		PROBE_SLEEP_LOG="$case_dir/sleeps"
	if output=$(expect_access /healthz 2>"$case_dir/error"); then
		status=0
	else
		status=$?
	fi
	if [[ "$expected" = success ]]; then
		[[ "$status" = 0 && "$output" = 'access_healthz=302' ]] \
			|| fail "live Access retry $name did not recover to the expected status"
	else
		[[ "$status" -ne 0 ]] || fail "live Access retry $name unexpectedly succeeded"
		contains 'after 3 attempts' "$case_dir/error"
		contains 'HTTP 500' "$case_dir/error"
	fi
	[[ "$(wc -l < "$case_dir/calls")" = 3 ]] || fail "live Access retry $name did not honor its attempt bound"
	[[ "$(<"$case_dir/sleeps")" = $'1\n2' ]] || fail "live Access retry $name did not use bounded exponential backoff"
}
run_access_probe_case transient-success $'dns\n500\n302' success
run_access_probe_case eventual-failure $'dns\nconnect\n500' failure

# Service Auth runs after the unauthenticated probes but can hit the same
# propagation window. It gets the same bounded retry treatment while retaining
# exact status, header, and JSON assertions on the successful attempt.
source <(awk '/^service_auth_probe\(\)/,/^}/' "$VALIDATE")
ACCESS_PROBE_MAX_ATTEMPTS=2
CF_ACCESS_CLIENT_ID=fixture-client-id
CF_ACCESS_CLIENT_SECRET=fixture-client-secret
run_service_probe_case() {
	local name=$1 sequence=$2 expected=$3 case_dir="$fixture/service-probe-$1" output status
	install -d -m 0700 "$case_dir"
	printf '%s\n' "$sequence" > "$case_dir/responses"
	: > "$case_dir/calls"
	: > "$case_dir/sleeps"
	PROBE_KIND=service PROBE_RESPONSES="$case_dir/responses" PROBE_CALL_LOG="$case_dir/calls" \
		PROBE_SLEEP_LOG="$case_dir/sleeps"
	CF_SERVICE_HEADER_FILE=
	CF_SERVICE_RESPONSE_HEADERS=
	CF_SERVICE_RESPONSE_BODY=
	if service_auth_probe >"$case_dir/output" 2>"$case_dir/error"; then
		status=0
	else
		status=$?
	fi
	rm -f -- "$CF_SERVICE_HEADER_FILE" "$CF_SERVICE_RESPONSE_HEADERS" "$CF_SERVICE_RESPONSE_BODY"
	if [[ "$expected" = success ]]; then
		[[ "$status" = 0 ]] || fail "Service Auth retry $name did not recover"
		contains 'service_auth_probe=ok' "$case_dir/output"
	else
		[[ "$status" -ne 0 ]] || fail "Service Auth retry $name unexpectedly succeeded"
		contains 'after 2 attempts' "$case_dir/error"
		contains 'curl exit 7' "$case_dir/error"
	fi
	[[ "$(wc -l < "$case_dir/calls")" = 2 ]] || fail "Service Auth retry $name did not honor its attempt bound"
	[[ "$(<"$case_dir/sleeps")" = 1 ]] || fail "Service Auth retry $name did not back off once"
	not_contains 'fixture-client-secret' "$case_dir/output"
	not_contains 'fixture-client-secret' "$case_dir/error"
}
run_service_probe_case transient-success $'dns\n401' success
run_service_probe_case eventual-failure $'dns\nconnect' failure
unset -f curl sleep probe_curl
unset CF_ACCESS_CLIENT_ID CF_ACCESS_CLIENT_SECRET PROBE_KIND PROBE_RESPONSES PROBE_CALL_LOG PROBE_SLEEP_LOG

# Exercise Cloudflare's exact origin, ingress, and DNS predicates with mocked
# API responses. The production functions reject alternate loopback, host,
# scheme, route, fallback, and DNS forms rather than normalizing them.
if CLOUDFLARE_API_TOKEN=fixture-token ROADMAP_PUBLIC_ORIGIN=https://evil.example \
	"$CLOUDFLARE" publish >"$fixture/noncanonical-cloudflare-origin.out" 2>&1; then
	fail 'Cloudflare prepare accepted a noncanonical public origin'
fi
if CLOUDFLARE_API_TOKEN=fixture-token ROADMAP_PUBLIC_ORIGIN=https://evil.example \
	"$VALIDATE" >"$fixture/noncanonical-live-origin.out" 2>&1; then
	fail 'live validation accepted a noncanonical public origin'
fi

source <(awk '/^validate_tunnel_config\(\)/,/^}/' "$CLOUDFLARE")
source <(awk '/^validate_dns_record\(\)/,/^}/' "$CLOUDFLARE")
ACCOUNT_ID=fixture-account
ZONE_ID=fixture-zone
PUBLIC_HOST=tc.shanekanterman.dev
cloudflare_fixture_team=team
cloudflare_fixture_ui_aud=ui-audience
cloudflare_fixture_api_aud=api-audience
cloudflare_fixture_tunnel_id=tunnel-fixture
cloudflare_topology_response=
cf_request() { printf '%s' "$cloudflare_topology_response"; }
cloudflare_canonical_tunnel=$(jq -cn \
	--arg host "$PUBLIC_HOST" --arg team "$cloudflare_fixture_team" \
	--arg ui "$cloudflare_fixture_ui_aud" --arg api "$cloudflare_fixture_api_aud" \
	'{result:{config:{ingress:[
		{hostname:$host,service:"http://127.0.0.1:8080",originRequest:{access:{required:true,teamName:$team,audTag:[$ui,$api]}}},
		{service:"http_status:404"}
	]}}}')
cloudflare_topology_response=$cloudflare_canonical_tunnel
validate_tunnel_config "$cloudflare_fixture_team" "$cloudflare_fixture_ui_aud" \
	"$cloudflare_fixture_api_aud" "$cloudflare_fixture_tunnel_id" \
	>"$fixture/cloudflare-canonical-tunnel.out" 2>&1 \
	|| fail 'canonical Cloudflare tunnel ingress was rejected'
for cloudflare_origin in \
	http://localhost:8080 \
	https://127.0.0.1:8080 \
	http://10.0.0.38:8080 \
	http://roadmap:8080 \
	http://127.0.0.1:8080.evil.example; do
	cloudflare_topology_response=$(jq -c --arg origin "$cloudflare_origin" \
		'.result.config.ingress[0].service=$origin' <<<"$cloudflare_canonical_tunnel")
	if validate_tunnel_config "$cloudflare_fixture_team" "$cloudflare_fixture_ui_aud" \
		"$cloudflare_fixture_api_aud" "$cloudflare_fixture_tunnel_id" \
		>"$fixture/cloudflare-origin-${cloudflare_origin//[^A-Za-z0-9]/_}.out" 2>&1; then
		fail "Cloudflare accepted noncanonical tunnel origin: $cloudflare_origin"
	fi
done
for cloudflare_bad_topology in extra-route wrong-fallback extra-audience extra-origin-key; do
	case "$cloudflare_bad_topology" in
		extra-route)
		cloudflare_topology_response=$(jq -c \
			'.result.config.ingress += [{hostname:"unexpected.example",service:"http://127.0.0.1:8080"}]' \
			<<<"$cloudflare_canonical_tunnel") ;;
		wrong-fallback)
		cloudflare_topology_response=$(jq -c \
			'.result.config.ingress[1].service="http://127.0.0.1:8080"' \
			<<<"$cloudflare_canonical_tunnel") ;;
		extra-audience)
		cloudflare_topology_response=$(jq -c \
			'.result.config.ingress[0].originRequest.access.audTag += ["unexpected"]' \
			<<<"$cloudflare_canonical_tunnel") ;;
		extra-origin-key)
		cloudflare_topology_response=$(jq -c \
			'.result.config.ingress[0].originRequest.access.extra=true' \
			<<<"$cloudflare_canonical_tunnel") ;;
	esac
	if validate_tunnel_config "$cloudflare_fixture_team" "$cloudflare_fixture_ui_aud" \
		"$cloudflare_fixture_api_aud" "$cloudflare_fixture_tunnel_id" \
		>"$fixture/cloudflare-topology-$cloudflare_bad_topology.out" 2>&1; then
		fail "Cloudflare accepted malformed tunnel topology: $cloudflare_bad_topology"
	fi
done

cloudflare_canonical_dns=$(jq -cn --arg host "$PUBLIC_HOST" \
	--arg target "$cloudflare_fixture_tunnel_id.cfargotunnel.com" \
	'{result:[{id:"dns-fixture",name:$host,type:"CNAME",content:$target,proxied:true}]}')
cloudflare_topology_response=$cloudflare_canonical_dns
validate_dns_record "$PUBLIC_HOST" "$cloudflare_fixture_tunnel_id" \
	>"$fixture/cloudflare-canonical-dns.out" 2>&1 \
	|| fail 'canonical Cloudflare DNS record was rejected'
for cloudflare_bad_dns in wrong-target unproxied extra-record wrong-type; do
	case "$cloudflare_bad_dns" in
		wrong-target)
			cloudflare_topology_response=$(jq -c \
				'.result[0].content="other.cfargotunnel.com"' <<<"$cloudflare_canonical_dns") ;;
		unproxied)
			cloudflare_topology_response=$(jq -c '.result[0].proxied=false' <<<"$cloudflare_canonical_dns") ;;
		extra-record)
			cloudflare_topology_response=$(jq -c \
				'.result += [{id:"dns-extra",name:"tc.shanekanterman.dev",type:"CNAME",content:"other.cfargotunnel.com",proxied:true}]' \
				<<<"$cloudflare_canonical_dns") ;;
		wrong-type)
			cloudflare_topology_response=$(jq -c '.result[0].type="A"' <<<"$cloudflare_canonical_dns") ;;
	esac
	if validate_dns_record "$PUBLIC_HOST" "$cloudflare_fixture_tunnel_id" \
		>"$fixture/cloudflare-dns-$cloudflare_bad_dns.out" 2>&1; then
		fail "Cloudflare accepted malformed DNS topology: $cloudflare_bad_dns"
	fi
done

# Drive prepare through the real Cloudflare script with a deterministic curl
# API mock. Each injected failure occurs after the one-time service token is
# created; the EXIT trap must issue exactly one DELETE and remove any captured
# output even when prepare fails in a later API or local-output step.
cloudflare_prepare_curl() {
	local method=GET path= data=arg idp_fixture=idp-fixture
	[[ "${CF_MOCK_IDP_MODE:-reuse}" = create ]] && idp_fixture=idp-created-fixture
	while [[ $# -gt 0 ]]; do
		arg=$1
		case "$arg" in
			--request) method=$2; shift 2 ;;
			--data) data=$2; shift 2 ;;
			https://api.cloudflare.com/client/v4/*)
				path=${arg#https://api.cloudflare.com/client/v4}; shift ;;
			*) shift ;;
		esac
	done
	printf '%s %s\n' "$method" "$path" >> "$CF_MOCK_LOG"
	if [[ "$CF_MOCK_FAIL" = "$method $path" ]]; then
		return 22
	fi
	local base="/accounts/$ACCOUNT_ID"
	if [[ "$method" = DELETE ]]; then
		printf '{"success":true,"result":{}}'
	elif [[ "$method" = GET && "$path" = "$base/access/identity_providers" ]]; then
		case "${CF_MOCK_IDP_MODE:-reuse}" in
			create) printf '{"success":true,"result":[]}' ;;
			ambiguous) printf '{"success":true,"result":[{"id":"idp-fixture-a","type":"cloudflare","config":{"restrict_to_account_members":true}},{"id":"idp-fixture-b","type":"cloudflare","config":{"restrict_to_account_members":true}}]}' ;;
			nonconforming) printf '{"success":true,"result":[{"id":"idp-fixture","type":"cloudflare","config":{"restrict_to_account_members":false}}]}' ;;
			*) printf '{"success":true,"result":[{"id":"otp-fixture","name":"Cloudflare","type":"onetimepin"},{"id":"idp-fixture","name":"Account SSO","type":"cloudflare","config":{"restrict_to_account_members":true}}]}' ;;
		esac
	elif [[ "$method" = POST && "$path" = "$base/access/identity_providers" ]]; then
		[[ "${CF_MOCK_IDP_MODE:-}" = create ]] || return 1
		[[ "$data" = '{"name":"Cloudflare","type":"cloudflare","config":{"restrict_to_account_members":true}}' ]] || return 1
		printf '{"success":true,"result":{"id":"idp-created-fixture","name":"Cloudflare","type":"cloudflare","config":{"restrict_to_account_members":true}}}'
	elif [[ "$method" = GET && "$path" = "$base/access/organizations" ]]; then
		printf '{"success":true,"result":{"auth_domain":"team.cloudflareaccess.com"}}'
	elif [[ "$method" = GET && "$path" = "$base/access/service_tokens?per_page=100" ]]; then
		if [[ "${CF_MOCK_SERVICE_TOKEN_MODE:-new}" = legacy ]]; then
			if grep -Fq "PUT $base/access/service_tokens/service-token-fixture" "$CF_MOCK_LOG"; then
				printf '{"success":true,"result":[{"id":"service-token-fixture","name":"Helm agents","client_id":"client-fixture","expires_at":"%s"}]}' "$CF_MOCK_EXPIRY"
			else
				printf '{"success":true,"result":[{"id":"service-token-fixture","name":"Roadmap agents","client_id":"client-fixture","expires_at":"%s"}]}' "$CF_MOCK_EXPIRY"
			fi
		else
			printf '{"success":true,"result":[]}'
		fi
	elif [[ "$method" = POST && "$path" = "$base/access/service_tokens" ]]; then
		printf '{"success":true,"result":{"id":"service-token-fixture","client_id":"client-fixture","client_secret":"secret-fixture","expires_at":"%s"}}' "$CF_MOCK_EXPIRY"
	elif [[ "$method" = GET && "$path" = "$base/access/apps?per_page=100" ]]; then
		printf '{"success":true,"result":[]}'
	elif [[ "$method" = POST && "$path" = "$base/access/apps" ]]; then
		if [[ "$data" == *'/api/v1/'* ]]; then
			printf '{"success":true,"result":{"id":"api-app-fixture"}}'
		else
			printf '{"success":true,"result":{"id":"ui-app-fixture"}}'
		fi
	elif [[ "$method" = GET && "$path" = "$base/access/apps/ui-app-fixture" ]]; then
		printf '{"success":true,"result":{"name":"Helm owner UI","domain":"tc.shanekanterman.dev","type":"self_hosted","session_duration":"168h","auto_redirect_to_identity":true,"allowed_idps":["%s"],"app_launcher_visible":false,"service_auth_401_redirect":false,"aud":"ui-audience"}}' "$idp_fixture"
	elif [[ "$method" = GET && "$path" = "$base/access/apps/api-app-fixture" ]]; then
		printf '{"success":true,"result":{"name":"Helm agents API","domain":"tc.shanekanterman.dev/api/v1/*","type":"self_hosted","session_duration":"168h","auto_redirect_to_identity":true,"allowed_idps":["%s"],"app_launcher_visible":false,"service_auth_401_redirect":true,"aud":"api-audience"}}' "$idp_fixture"
	elif [[ "$method" = GET && "$path" = "$base/access/apps/ui-app-fixture/policies" ]]; then
		printf '%s' '{"success":true,"result":[{"id":"ui-owner-policy","name":"Helm owner only","decision":"allow","precedence":1,"include":[{"email":{"email":"owner@example.com"}}]}]}'
	elif [[ "$method" = GET && "$path" = "$base/access/apps/api-app-fixture/policies" ]]; then
		printf '%s' '{"success":true,"result":[{"id":"api-owner-policy","name":"Helm owner only","decision":"allow","precedence":2,"include":[{"email":{"email":"owner@example.com"}}]},{"id":"api-service-policy","name":"Helm agents Service Auth","decision":"non_identity","precedence":1,"include":[{"service_token":{"token_id":"service-token-fixture"}}]}]}'
	elif [[ "$method" = PUT || "$method" = POST ]]; then
		printf '{"success":true,"result":{}}'
	elif [[ "$method" = GET && "$path" = "$base/cfd_tunnel?is_deleted=false&per_page=100" ]]; then
		printf '{"success":true,"result":[{"id":"tunnel-fixture","name":"roadmap-homelab"}]}'
	elif [[ "$method" = PUT && "$path" = "$base/cfd_tunnel/tunnel-fixture/configurations" ]]; then
		printf '{"success":true,"result":{}}'
	elif [[ "$method" = GET && "$path" = "$base/cfd_tunnel/tunnel-fixture/configurations" ]]; then
		printf '%s' '{"success":true,"result":{"config":{"ingress":[{"hostname":"tc.shanekanterman.dev","service":"http://127.0.0.1:8080","originRequest":{"access":{"required":true,"teamName":"team","audTag":["ui-audience","api-audience"]}}},{"service":"http_status:404"}]}}}'
	elif [[ "$method" = GET && "$path" = "$base/cfd_tunnel/tunnel-fixture/token" ]]; then
		printf '{"success":true,"result":"tunnel-token-fixture"}'
	else
		return 1
	fi
}

cloudflare_prepare_provider_case() {
	local name=$1 mode=$2 expected_status=$3 token_mode=${4:-new} case_dir="$fixture/cloudflare-provider-$1"
	local output="$case_dir/output.log" status expected_idp_calls=1
	[[ "$mode" = create ]] && expected_idp_calls=2
	/usr/bin/install -d -m 0700 "$case_dir"
	CF_MOCK_LOG="$case_dir/api.calls"
	CF_MOCK_FAIL=
	CF_MOCK_EXPIRY=$(date -u -d '+30 days' +%Y-%m-%dT%H:%M:%SZ)
	CF_MOCK_IDP_MODE=$mode
	CF_MOCK_SERVICE_TOKEN_MODE=$token_mode
	export CF_MOCK_LOG CF_MOCK_FAIL CF_MOCK_EXPIRY CF_MOCK_IDP_MODE CF_MOCK_SERVICE_TOKEN_MODE
	set +e
	(
		export CLOUDFLARE_API_TOKEN=fixture-token HELM_ADMIN_EMAIL=owner@example.com
		export HELM_REQUIRE_DURABLE_SERVICE_TOKEN_CAPTURE=0
		unset HELM_PUBLIC_ORIGIN ROADMAP_PUBLIC_ORIGIN
		export -f cloudflare_prepare_curl
		curl() { cloudflare_prepare_curl "$@"; }
		export -f curl
		"$CLOUDFLARE" prepare "$case_dir/tunnel.token" "$case_dir/owner.env" "$case_dir/service.env"
	) >"$output" 2>&1
	status=$?
	set -e
	if [[ "$expected_status" = success ]]; then
		[[ "$status" = 0 ]] || fail "Cloudflare provider $name unexpectedly failed"
		contains 'cloudflare_prepare=ok' "$output"
	else
		[[ "$status" -ne 0 ]] || fail "Cloudflare provider $name unexpectedly succeeded"
		[[ "$(grep -Fc -- 'POST /accounts/090ae73dce25f4eca9a53ee396fdc916/access/service_tokens' "$CF_MOCK_LOG" || true)" = 0 ]] \
			|| fail "Cloudflare provider $name continued into service-token creation"
	fi
	[[ "$(grep -Fc -- '/access/identity_providers' "$CF_MOCK_LOG" || true)" = "$expected_idp_calls" ]] \
		|| fail "Cloudflare provider $name performed an unexpected number of IdP requests"
	if [[ "$token_mode" = legacy && "$expected_status" = success ]]; then
		[[ "$(grep -Fc -- 'PUT /accounts/090ae73dce25f4eca9a53ee396fdc916/access/service_tokens/service-token-fixture' "$CF_MOCK_LOG" || true)" = 1 ]] \
			|| fail 'Cloudflare legacy service token was not renamed in place'
		[[ "$(grep -Fc -- 'POST /accounts/090ae73dce25f4eca9a53ee396fdc916/access/service_tokens' "$CF_MOCK_LOG" || true)" = 0 ]] \
			|| fail 'Cloudflare legacy service token migration rotated the credential'
	fi
}

cloudflare_prepare_provider_case reuse reuse success
cloudflare_prepare_provider_case create create success
cloudflare_prepare_provider_case legacy-token reuse success legacy
cloudflare_prepare_provider_case ambiguous ambiguous failure
cloudflare_prepare_provider_case nonconforming nonconforming failure
CF_MOCK_IDP_MODE=reuse
export CF_MOCK_IDP_MODE

cloudflare_prepare_failure_case() {
	local name=$1 failure=$2 output_mode=${3:-file} case_dir="$fixture/cloudflare-prepare-$1"
	local token_output="$case_dir/tunnel.token" owner_output="$case_dir/owner.env"
	local service_output="$case_dir/service.env" output="$case_dir/output.log" status
	/usr/bin/install -d -m 0700 "$case_dir"
	if [[ "$output_mode" = directory ]]; then
		/usr/bin/install -d -m 0700 "$owner_output"
	fi
	CF_MOCK_LOG="$case_dir/api.calls"
	CF_MOCK_FAIL=$failure
	CF_MOCK_EXPIRY=$(date -u -d '+30 days' +%Y-%m-%dT%H:%M:%SZ)
	export CF_MOCK_LOG CF_MOCK_FAIL CF_MOCK_EXPIRY
	set +e
	(
		export CLOUDFLARE_API_TOKEN=fixture-token HELM_ADMIN_EMAIL=owner@example.com
		export HELM_REQUIRE_DURABLE_SERVICE_TOKEN_CAPTURE=0
		unset HELM_PUBLIC_ORIGIN ROADMAP_PUBLIC_ORIGIN
		export -f cloudflare_prepare_curl
		curl() { cloudflare_prepare_curl "$@"; }
		export -f curl
		"$CLOUDFLARE" prepare "$token_output" "$owner_output" "$service_output"
	) >"$output" 2>&1
	status=$?
	set -e
	[[ "$status" -ne 0 ]] || fail "Cloudflare prepare $name unexpectedly succeeded"
	[[ "$(grep -Fc -- 'DELETE /accounts/090ae73dce25f4eca9a53ee396fdc916/access/service_tokens/service-token-fixture' "$CF_MOCK_LOG" || true)" = 1 ]] \
		|| fail "Cloudflare prepare $name did not revoke its newly-created service token"
	[[ ! -e "$service_output" ]] \
		|| fail "Cloudflare prepare $name left a one-time service-token output"
}

cloudflare_prepare_failure_case app-create \
	'POST /accounts/090ae73dce25f4eca9a53ee396fdc916/access/apps'
cloudflare_prepare_failure_case api-app-read \
	'GET /accounts/090ae73dce25f4eca9a53ee396fdc916/access/apps/api-app-fixture'
cloudflare_prepare_failure_case owner-policy-update \
	'PUT /accounts/090ae73dce25f4eca9a53ee396fdc916/access/apps/ui-app-fixture/policies/ui-owner-policy'
cloudflare_prepare_failure_case tunnel-discovery \
	'GET /accounts/090ae73dce25f4eca9a53ee396fdc916/cfd_tunnel?is_deleted=false&per_page=100'
cloudflare_prepare_failure_case tunnel-config \
	'PUT /accounts/090ae73dce25f4eca9a53ee396fdc916/cfd_tunnel/tunnel-fixture/configurations'
cloudflare_prepare_failure_case tunnel-token \
	'GET /accounts/090ae73dce25f4eca9a53ee396fdc916/cfd_tunnel/tunnel-fixture/token'
cloudflare_prepare_failure_case invalid-output '' directory
printf 'cloudflare_prepare_recovery_runtime_tests=ok\n'

# Production mutation jobs alone share the fixed non-canceling lock; PR/check
# traffic remains ref-scoped. Manual deployment/rollback is main-only and its
# scripts are checked out at the selected main SHA.
contains 'group: helm-${{ github.workflow }}-${{ github.ref }}' "$WORKFLOW"
contains "cancel-in-progress: \${{ github.ref != 'refs/heads/main' }}" "$WORKFLOW"
count_contains 2 'group: helm-production' "$WORKFLOW"
count_contains 2 'cancel-in-progress: false' "$WORKFLOW"
contains "github.ref == 'refs/heads/main'" "$WORKFLOW"
contains 'ref: ${{ github.sha }}' "$WORKFLOW"
contains 'actions/upload-artifact@' "$WORKFLOW"
contains '# v7.0.1' "$WORKFLOW"
contains 'actions/download-artifact@' "$WORKFLOW"
contains '# v8.0.1' "$WORKFLOW"
contains 'actions/checkout@' "$WORKFLOW"
contains '# v7.0.1' "$WORKFLOW"
contains 'actions/setup-go@' "$WORKFLOW"
contains '# v7.0.0' "$WORKFLOW"
contains 'actions/setup-node@' "$WORKFLOW"
contains 'ROADMAP_RELEASE_SIGNING_KEY' "$WORKFLOW"
contains 'ROADMAP_CF_ACCESS_CLIENT_ID' "$WORKFLOW"
contains 'ROADMAP_CF_ACCESS_CLIENT_SECRET' "$WORKFLOW"
contains 'HELM_REQUIRE_DURABLE_SERVICE_TOKEN_CAPTURE: "1"' "$WORKFLOW"
contains 'cloudflare_dir="$RUNNER_TEMP/helm-cloudflare"' "$WORKFLOW"
contains 'HELM_CLOUDFLARED_TOKEN_FILE=%s' "$WORKFLOW"
contains 'HELM_OWNER_ENV_FILE=%s' "$WORKFLOW"
count_contains 2 'rm -rf -- "$RUNNER_TEMP/helm-ssh" "$RUNNER_TEMP/helm-cloudflare"' "$WORKFLOW"
count_contains 2 'rm -f -- dist/cloudflared.token dist/owner.env dist/helm-access-token.env' "$WORKFLOW"
contains 'HELM_CLOUDFLARED_TOKEN_FILE' "$ROOT_DIR/deploy/build-bundle.sh"
contains 'HELM_OWNER_ENV_FILE' "$ROOT_DIR/deploy/build-bundle.sh"
contains 'Capture previous release for recovery' "$WORKFLOW"
contains 'automatic rollback' "$WORKFLOW"
contains 'chmod 0755 dist/helm' "$WORKFLOW"
contains 'container_smoke=ok' "$WORKFLOW"
contains 'ready=false' "$WORKFLOW"
contains 'docker exec "$container_id" /usr/local/bin/helm healthcheck' "$WORKFLOW"
contains '/usr/local/bin/helm healthcheck' "$WORKFLOW"
not_contains 'persist-credentials: true' "$WORKFLOW"
checkout_count=$(grep -Fc -- 'uses: actions/checkout@' "$WORKFLOW" || true)
persisted_checkout_count=$(grep -Fc -- 'persist-credentials: false' "$WORKFLOW" || true)
[[ "$checkout_count" = "$persisted_checkout_count" ]] \
	|| fail 'every workflow checkout must disable credential persistence'
awk '
  /^[[:space:]]*-[[:space:]]+uses:[[:space:]]+actions\/checkout@/ {
    if (seen && !persisted) exit 1
    seen = 1
    persisted = 0
    next
  }
  /^[[:space:]]*-[[:space:]]+(uses:|name:)/ {
    if (seen && !persisted) exit 1
  }
  /persist-credentials:[[:space:]]+false/ { if (seen) persisted = 1 }
  END { if (!seen || !persisted) exit 1 }
' "$WORKFLOW" || fail 'a workflow checkout block lacks persist-credentials: false'

contains 'HEALTHCHECK' "$ROOT_DIR/Dockerfile"
contains 'test: ["CMD", "/usr/local/bin/helm", "healthcheck"]' "$ROOT_DIR/compose.yaml"

contains '/var/lib/roadmap/data/roadmap.db' "$ROOT_DIR/README.md"
contains '`ROADMAP_RELEASE_SIGNING_KEY`' "$ROOT_DIR/README.md"

# Local key material must never enter a Docker build context. .gitignore is
# intentionally owned by the repository integration agent; test its Docker
# counterpart here and report missing Git exclusions to the parent.
for secret_pattern in helm-deploy-key helm-deploy-key.pub helm-release-signing-private.pem helm-release-signing-public.pem roadmap-deploy-key roadmap-deploy-key.pub roadmap-release-signing-private.pem roadmap-release-signing-public.pem '*.pem' '*.key' '*.pub'; do
	contains "$secret_pattern" "$DOCKERIGNORE"
done

# Exercise the output writer without contacting Cloudflare. Source only the
# production function definitions and provide a controlled API response so an
# HTTPS issuer proves literal replacement (including its slashes). Then force
# both API and template failures from a conditional call: neither may return
# success or replace existing valid outputs.
source <(awk '/^valid_email\(\)/,/^}/' "$CLOUDFLARE")
source <(awk '/^validate_owner_env\(\)/,/^}/' "$CLOUDFLARE")
source <(awk '/^restore_prepare_output\(\)/,/^}/' "$CLOUDFLARE")
source <(awk '/^write_prepare_outputs\(\)/,/^}/' "$CLOUDFLARE")
cf_request() {
	if [[ "${CF_REQUEST_FAIL:-0}" = 1 ]]; then
		return 1
	fi
	printf '{"success":true,"result":"fixture-tunnel-token"}'
}
ACCOUNT_ID=fixture-account
cloudflare_test_root_dir=$ROOT_DIR
TOKEN_OUTPUT="$fixture/generated.token"
OWNER_ENV_OUTPUT="$fixture/generated.owner.env"
CF_REQUEST_FAIL=0
if ! write_prepare_outputs tunnel-id owner@example.com \
	'https://team.cloudflareaccess.com' ui-audience api_audience; then
	fail 'owner-environment writer rejected an HTTPS issuer'
fi
grep -Fx 'ROADMAP_CLOUDFLARE_ISSUER=https://team.cloudflareaccess.com' "$OWNER_ENV_OUTPUT" >/dev/null \
	|| fail 'owner-environment writer did not preserve HTTPS issuer'
grep -Fx 'ROADMAP_CF_ACCESS_AUDIENCES=ui-audience,api_audience' "$OWNER_ENV_OUTPUT" >/dev/null \
	|| fail 'owner-environment writer did not preserve both audiences'
[[ "$(stat -c '%a' -- "$TOKEN_OUTPUT")" = 600 && "$(stat -c '%a' -- "$OWNER_ENV_OUTPUT")" = 600 ]] \
	|| fail 'owner-environment outputs are not mode 0600'

# CI must not create a one-time service-token secret on an ephemeral runner.
# The guard must fail before the POST while the default/manual mode remains
# available for the explicitly persisted first bootstrap.
source <(awk '/^ensure_service_token\(\)/,/^}/' "$CLOUDFLARE")
SERVICE_TOKEN_NAME='Roadmap agents'
SERVICE_TOKEN_OUTPUT="$fixture/guard-service-token.env"
REQUIRE_DURABLE_SERVICE_TOKEN_CAPTURE=1
SERVICE_TOKEN_CALL_LOG="$fixture/service-token-calls.log"
: > "$SERVICE_TOKEN_CALL_LOG"
cf_request() {
	printf '%s\n' "$1 $2" >> "$SERVICE_TOKEN_CALL_LOG"
	if [[ "$1" = GET ]]; then
		printf '{"success":true,"result":[]}'
	else
		printf '{"success":true,"result":{"id":"unexpected"}}'
	fi
}
if ensure_service_token >"$fixture/service-token-guard.out" 2>&1; then
	fail 'service-token durability guard allowed an unpersistable token creation'
fi
[[ "$(wc -l < "$SERVICE_TOKEN_CALL_LOG")" = 1 && "$(cut -d' ' -f1 < "$SERVICE_TOKEN_CALL_LOG")" = GET && ! -e "$SERVICE_TOKEN_OUTPUT" ]] \
	|| fail 'service-token durability guard did not stop before token creation'
REQUIRE_DURABLE_SERVICE_TOKEN_CAPTURE=0
cf_request() {
	if [[ "${CF_REQUEST_FAIL:-0}" = 1 ]]; then
		return 1
	fi
	printf '{"success":true,"result":"fixture-tunnel-token"}'
}

printf 'old-token\n' > "$TOKEN_OUTPUT"
printf 'old-owner\n' > "$OWNER_ENV_OUTPUT"
CF_REQUEST_FAIL=1
if write_prepare_outputs tunnel-id owner@example.com \
	'https://team.cloudflareaccess.com' ui-audience api_audience \
	>"$fixture/cloudflare-api-failure.out" 2>&1; then
	fail 'failed Cloudflare request was reported as successful'
fi
[[ "$(<"$TOKEN_OUTPUT")" = old-token && "$(<"$OWNER_ENV_OUTPUT")" = old-owner ]] \
	|| fail 'failed Cloudflare request replaced existing outputs'

CF_REQUEST_FAIL=0
ROOT_DIR="$fixture/missing-template-root"
if write_prepare_outputs tunnel-id owner@example.com \
	'https://team.cloudflareaccess.com' ui-audience api_audience \
	>"$fixture/template-failure.out" 2>&1; then
	fail 'template generation failure was reported as successful'
fi
[[ "$(<"$TOKEN_OUTPUT")" = old-token && "$(<"$OWNER_ENV_OUTPUT")" = old-owner ]] \
	|| fail 'template failure replaced existing outputs'
ROOT_DIR="$cloudflare_test_root_dir"

# Force only the second output rename to fail. The writer must restore both
# pre-existing files byte-for-byte from its same-directory backup names.
printf 'old-token\n' > "$fixture/old-token.ref"
printf 'old-owner\n' > "$fixture/old-owner.ref"
cp -- "$fixture/old-token.ref" "$TOKEN_OUTPUT"
cp -- "$fixture/old-owner.ref" "$OWNER_ENV_OUTPUT"
mv() {
	local move_args=("$@") source destination
	source=${move_args[$(( ${#move_args[@]} - 2 ))]}
	destination=${move_args[$(( ${#move_args[@]} - 1 ))]}
	if [[ "${CLOUDFLARE_FAIL_OWNER_COMMIT:-0}" = 1 && "$destination" = "$OWNER_ENV_OUTPUT" && "$source" != *'.backup.'* ]]; then
		return 1
	fi
	command mv "$@"
}
CLOUDFLARE_FAIL_OWNER_COMMIT=1
if write_prepare_outputs tunnel-id owner@example.com \
	'https://team.cloudflareaccess.com' ui-audience api_audience \
	>"$fixture/owner-commit-failure.out" 2>&1; then
	fail 'second owner-environment rename failure was reported as successful'
fi
unset -f mv
cmp -s "$fixture/old-token.ref" "$TOKEN_OUTPUT" \
	|| fail 'second rename failure did not restore the prior tunnel token byte-for-byte'
cmp -s "$fixture/old-owner.ref" "$OWNER_ENV_OUTPUT" \
	|| fail 'second rename failure did not restore the prior owner environment byte-for-byte'

# Exercise the prepare caller itself with a deliberately failed output writer.
# This keeps the test independent of Cloudflare while proving the final
# cloudflare_prepare=ok marker is unreachable when a critical child fails.
source <(awk '/^prepare\(\)/,/^}/' "$CLOUDFLARE")
PUBLIC_HOST=tc.shanekanterman.dev
API_PATH="$PUBLIC_HOST/api/v1/*"
UI_APP_NAME='Roadmap owner UI'
API_APP_NAME='Roadmap agents API'
owner_email() { printf 'owner@example.com'; }
identity_provider_id() { printf 'fixture-idp'; }
access_team_domain() { printf 'team.cloudflareaccess.com'; }
ensure_service_token() { SERVICE_TOKEN_ID=fixture-service-token; }
ensure_app() {
	if [[ "$2" = "$PUBLIC_HOST" ]]; then
		APP_ID=fixture-ui-id
		APP_AUD=fixture-ui-aud
	else
		APP_ID=fixture-api-id
		APP_AUD=fixture-api-aud
	fi
}
upsert_owner_policy() { :; }
upsert_service_policy() { :; }
validate_policy_set() { :; }
find_tunnel_id() { printf 'fixture-tunnel-id'; }
configure_tunnel() { :; }
write_prepare_outputs() { return 1; }
if prepare >"$fixture/prepare-failure.out" 2>&1; then
	fail 'prepare reported success after a failed output writer'
fi
not_contains 'cloudflare_prepare=ok' "$fixture/prepare-failure.out"

# Build a small, valid signed release with the same canonical member set as
# build-bundle.sh. This proves the verifier accepts the intended archive before
# the negative cases mutate one property at a time.
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
BUNDLE_MEMBERS=(
	cloudflared cloudflared.service cloudflared.token compose.yaml
	install-inside-lxc.sh nftables.conf roadmap roadmap-backup.service
	roadmap-backup.sh roadmap-backup.timer roadmap.env roadmap-restore.sh
	roadmap-rollback.sh roadmap.service roadmap.sha256 release.manifest
	release.manifest.sig release.sha
)
SHA=$(printf 'a%.0s' {1..40})
source_dir="$fixture/source"
install -d -m 0700 "$source_dir"
for member in "${PAYLOAD_MEMBERS[@]}"; do
	if [[ "$member" = release.sha ]]; then
		printf '%s\n' "$SHA" > "$source_dir/$member"
	else
		printf 'fixture payload for %s\n' "$member" > "$source_dir/$member"
	fi
done
{
	printf 'roadmap-release-manifest-v1\n'
	for member in "${PAYLOAD_MEMBERS[@]}"; do
		bytes=$(stat -c '%s' -- "$source_dir/$member")
		digest=$(sha256sum -- "$source_dir/$member" | awk '{print $1}')
		printf '%s\t%s\t%s\n' "$member" "$bytes" "$digest"
	done
} > "$source_dir/release.manifest"
openssl genpkey -algorithm ED25519 -out "$fixture/private.pem" >/dev/null 2>&1
chmod 0600 "$fixture/private.pem"
openssl pkey -in "$fixture/private.pem" -pubout -out "$fixture/public.pem" >/dev/null 2>&1
openssl pkeyutl -sign -rawin -inkey "$fixture/private.pem" -in "$source_dir/release.manifest" -out "$source_dir/release.manifest.sig"

make_archive() {
	local output=$1 directory=$2
	GZIP=-n tar --sort=name --owner=0 --group=0 --numeric-owner --mtime='@0' \
		-czf "$output" -C "$directory" "${BUNDLE_MEMBERS[@]}"
}
make_archive "$fixture/valid.tar.gz" "$source_dir"
"$VERIFY" "$fixture/valid.tar.gz" "$SHA" "$fixture/public.pem" >/dev/null \
	|| fail 'valid signed release archive was rejected'

expect_verify_fail() {
	local label=$1 archive=$2
	if "$VERIFY" "$archive" "$SHA" "$fixture/public.pem" >"$fixture/$label.out" 2>&1; then
		fail "$label release archive was unexpectedly accepted"
	fi
}

# High-ratio bomb: a sparse member is only a few bytes on disk but declares
# more than the aggregate uncompressed cap. The bounded tar listing must stop
# on that header promptly, before tar skips/decompresses the member payload.
high_ratio_dir="$fixture/high-ratio"
cp -a "$source_dir" "$high_ratio_dir"
truncate -s 536870913 "$high_ratio_dir/roadmap"
make_archive "$fixture/high-ratio.tar.gz" "$high_ratio_dir"
if timeout --foreground 10s "$VERIFY" "$fixture/high-ratio.tar.gz" "$SHA" "$fixture/public.pem" \
	>"$fixture/high-ratio.out" 2>&1; then
	fail 'high-ratio release archive was unexpectedly accepted'
else
	high_ratio_status=$?
fi
[[ "$high_ratio_status" -ne 124 && "$high_ratio_status" -ne 137 ]] \
	|| fail 'high-ratio release archive was not rejected promptly'

# Unsigned archive: omitting the detached signature fails the exact envelope
# count before extraction.
GZIP=-n tar --sort=name --owner=0 --group=0 --numeric-owner --mtime='@0' \
	-czf "$fixture/unsigned.tar.gz" -C "$source_dir" \
		cloudflared cloudflared.service cloudflared.token compose.yaml install-inside-lxc.sh \
		nftables.conf roadmap roadmap-backup.service roadmap-backup.sh roadmap-backup.timer \
		roadmap.env roadmap-restore.sh roadmap-rollback.sh roadmap.service roadmap.sha256 \
		release.manifest release.sha
expect_verify_fail unsigned "$fixture/unsigned.tar.gz"

# Tampered payload: the signed manifest remains unchanged, so its digest check
# must reject the archive.
tampered_dir="$fixture/tampered"
cp -a "$source_dir" "$tampered_dir"
printf 'tampered payload\n' > "$tampered_dir/roadmap"
make_archive "$fixture/tampered.tar.gz" "$tampered_dir"
expect_verify_fail tampered "$fixture/tampered.tar.gz"

# Duplicate archive member: exact member count/canonical set rejects a second
# copy even though all individual payload bytes are otherwise valid.
GZIP=-n tar --owner=0 --group=0 --numeric-owner --mtime='@0' \
	-czf "$fixture/duplicate.tar.gz" -C "$source_dir" "${BUNDLE_MEMBERS[@]}" roadmap
expect_verify_fail duplicate "$fixture/duplicate.tar.gz"

# Canonical path alias: GNU tar preserves the explicit ./ spelling, which is
# rejected before any payload extraction.
alias_args=()
for member in "${BUNDLE_MEMBERS[@]}"; do alias_args+=("./$member"); done
GZIP=-n tar --sort=name --owner=0 --group=0 --numeric-owner --mtime='@0' \
	-czf "$fixture/alias.tar.gz" -C "$source_dir" "${alias_args[@]}"
expect_verify_fail canonical_alias "$fixture/alias.tar.gz"

# Oversized manifest: sign it so this case specifically exercises the
# manifest-size cap rather than the detached-signature failure path.
oversized_dir="$fixture/oversized"
cp -a "$source_dir" "$oversized_dir"
dd if=/dev/zero of="$oversized_dir/release.manifest" bs=1 count=131073 status=none
openssl pkeyutl -sign -rawin -inkey "$fixture/private.pem" -in "$oversized_dir/release.manifest" -out "$oversized_dir/release.manifest.sig"
make_archive "$fixture/oversized.tar.gz" "$oversized_dir"
expect_verify_fail oversized_manifest "$fixture/oversized.tar.gz"

# Duplicate manifest record: its signature is valid, but canonical order and
# exact payload cardinality reject the repeated name.
duplicate_manifest_dir="$fixture/duplicate-manifest"
cp -a "$source_dir" "$duplicate_manifest_dir"
first_record=$(sed -n '2p' "$duplicate_manifest_dir/release.manifest")
{ head -n 1 "$duplicate_manifest_dir/release.manifest"; printf '%s\n' "$first_record"; tail -n +2 "$duplicate_manifest_dir/release.manifest"; } > "$duplicate_manifest_dir/release.manifest.new"
mv -T -- "$duplicate_manifest_dir/release.manifest.new" "$duplicate_manifest_dir/release.manifest"
openssl pkeyutl -sign -rawin -inkey "$fixture/private.pem" -in "$duplicate_manifest_dir/release.manifest" -out "$duplicate_manifest_dir/release.manifest.sig"
make_archive "$fixture/duplicate-manifest.tar.gz" "$duplicate_manifest_dir"
expect_verify_fail duplicate_manifest "$fixture/duplicate-manifest.tar.gz"

# Exercise the authorization overlay with adversarial state when sqlite3 is
# available: an old backup tries to re-enable an actor with an old password,
# while the live database has a disabled/deleted owner and a newer actor. The
# expected result mirrors the SQL in roadmap-restore.sh and catches accidental
# credential, grant, setup, or quota resurrection.
if command -v sqlite3 >/dev/null 2>&1; then
	auth_fixture="$fixture/auth"
	install -d -m 0700 "$auth_fixture"
	current_db="$auth_fixture/current.db"
	candidate_db="$auth_fixture/candidate.db"
	for database in "$current_db" "$candidate_db"; do
		sqlite3 "$database" <<'SQL'
PRAGMA foreign_keys = ON;
CREATE TABLE actors (id TEXT PRIMARY KEY, kind TEXT NOT NULL, name TEXT NOT NULL, email TEXT, password_hash TEXT, admin INTEGER NOT NULL DEFAULT 0, disabled_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, description TEXT NOT NULL DEFAULT '');
CREATE UNIQUE INDEX actors_email_unique ON actors(lower(email)) WHERE email IS NOT NULL;
CREATE TABLE auth_setup (id INTEGER PRIMARY KEY CHECK (id = 1), completed_at TEXT NOT NULL);
CREATE TABLE projects (id TEXT PRIMARY KEY);
CREATE TABLE actor_projects (actor_id TEXT NOT NULL REFERENCES actors(id) ON DELETE CASCADE, project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE, PRIMARY KEY(actor_id, project_id));
CREATE TABLE sessions (id TEXT PRIMARY KEY, actor_id TEXT NOT NULL);
CREATE TABLE tokens (id TEXT PRIMARY KEY, actor_id TEXT NOT NULL);
CREATE TABLE idempotency_keys (actor_id TEXT NOT NULL, key TEXT NOT NULL, PRIMARY KEY(actor_id, key));
CREATE TABLE actor_resource_usage (actor_id TEXT PRIMARY KEY, reserved_bytes INTEGER NOT NULL, updated_at TEXT NOT NULL);
SQL
	done
	sqlite3 "$current_db" <<'SQL'
INSERT INTO actors VALUES ('owner','human','Owner','current@example.test','CURRENT_HASH',1,'2026-08-27T00:00:00Z','old','current','current description');
INSERT INTO actors VALUES ('new','agent','New agent','new@example.test','NEW_HASH',0,NULL,'current','current','new description');
INSERT INTO projects VALUES ('p1');
INSERT INTO projects VALUES ('p2');
INSERT INTO auth_setup VALUES (1,'current-setup');
INSERT INTO actor_projects VALUES ('owner','p1');
INSERT INTO actor_projects VALUES ('new','p2');
INSERT INTO actor_resource_usage VALUES ('owner',17,'current');
INSERT INTO actor_resource_usage VALUES ('new',19,'current');
SQL
	sqlite3 "$candidate_db" <<'SQL'
INSERT INTO actors VALUES ('owner','human','Owner','old@example.test','OLD_HASH',0,NULL,'old','old','old description');
INSERT INTO actors VALUES ('old','agent','Old agent','old-only@example.test','OLD_ONLY_HASH',1,NULL,'old','old','old-only description');
INSERT INTO projects VALUES ('p1');
INSERT INTO projects VALUES ('p2');
INSERT INTO auth_setup VALUES (1,'old-setup');
INSERT INTO actor_projects VALUES ('owner','p2');
INSERT INTO actor_resource_usage VALUES ('owner',999,'old');
INSERT INTO actor_resource_usage VALUES ('old',888,'old');
INSERT INTO sessions VALUES ('s1','owner');
INSERT INTO tokens VALUES ('t1','owner');
INSERT INTO idempotency_keys VALUES ('owner','k1');
SQL
	sqlite3 "$candidate_db" <<SQL
PRAGMA foreign_keys = ON;
ATTACH DATABASE '$current_db' AS current_auth;
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
INSERT INTO auth_setup (id, completed_at) SELECT id, completed_at FROM current_auth.auth_setup;
DELETE FROM actor_projects;
INSERT INTO actor_projects (actor_id, project_id)
SELECT c.actor_id, c.project_id FROM current_auth.actor_projects AS c
JOIN actors AS a ON a.id = c.actor_id JOIN projects AS p ON p.id = c.project_id;
CREATE TABLE IF NOT EXISTS actor_resource_usage (actor_id TEXT PRIMARY KEY, reserved_bytes INTEGER NOT NULL, updated_at TEXT NOT NULL);
DELETE FROM actor_resource_usage;
INSERT INTO actor_resource_usage (actor_id, reserved_bytes, updated_at)
SELECT c.actor_id, c.reserved_bytes, c.updated_at FROM current_auth.actor_resource_usage AS c
JOIN actors AS a ON a.id = c.actor_id;
DELETE FROM sessions;
DELETE FROM tokens;
DELETE FROM idempotency_keys;
COMMIT;
DETACH DATABASE current_auth;
SQL
	owner_result=$(sqlite3 -separator '|' "$candidate_db" "SELECT password_hash,admin,disabled_at,email,description FROM actors WHERE id='owner';")
	[[ "$owner_result" = 'CURRENT_HASH|1|2026-08-27T00:00:00Z|current@example.test|current description' ]] \
		|| fail 'restore auth regression resurrected old owner credentials or state'
	old_password=$(sqlite3 "$candidate_db" "SELECT COALESCE(password_hash,'') FROM actors WHERE id='old';")
	old_admin=$(sqlite3 "$candidate_db" "SELECT admin FROM actors WHERE id='old';")
	old_disabled=$(sqlite3 "$candidate_db" "SELECT COALESCE(disabled_at,'') FROM actors WHERE id='old';")
	old_email=$(sqlite3 "$candidate_db" "SELECT COALESCE(email,'') FROM actors WHERE id='old';")
	[[ -z "$old_password" && "$old_admin" = 0 && -n "$old_disabled" && -z "$old_email" ]] \
		|| fail 'restore auth regression left old-only actor usable'
	new_result=$(sqlite3 -separator '|' "$candidate_db" "SELECT password_hash,admin,COALESCE(disabled_at,''),email FROM actors WHERE id='new';")
	[[ "$new_result" = 'NEW_HASH|0||new@example.test' ]] || fail 'restore auth regression dropped current-only actor state'
	[[ "$(sqlite3 "$candidate_db" 'SELECT completed_at FROM auth_setup WHERE id=1;')" = current-setup ]] \
		|| fail 'restore auth regression resurrected stale auth setup'
	[[ "$(sqlite3 -separator '|' "$candidate_db" 'SELECT actor_id,project_id FROM actor_projects ORDER BY actor_id,project_id;')" = $'new|p2\nowner|p1' ]] \
		|| fail 'restore auth regression resurrected stale project grants'
	[[ "$(sqlite3 -separator '|' "$candidate_db" 'SELECT actor_id,reserved_bytes FROM actor_resource_usage ORDER BY actor_id;')" = $'new|19\nowner|17' ]] \
		|| fail 'restore auth regression resurrected stale quota state'
	[[ "$(sqlite3 "$candidate_db" 'SELECT (SELECT COUNT(*) FROM sessions) || "|" || (SELECT COUNT(*) FROM tokens) || "|" || (SELECT COUNT(*) FROM idempotency_keys);')" = '0|0|0' ]] \
		|| fail 'restore auth regression left replayable credentials'
	printf 'restore_auth_runtime_test=ok\n'
else
	printf 'restore_auth_runtime_test=skipped (requires sqlite3)\n'
fi

# The online backup helper must work from an arbitrary cwd. Run this only in
# an actual root/integration environment where the production service user and
# sqlite3 are available; static checks above still run on ordinary developers'
# machines.
if [[ "$(id -u)" -eq 0 && -n "$(id -u roadmap 2>/dev/null || true)" && $(command -v sqlite3 >/dev/null 2>&1; echo $?) -eq 0 ]]; then
	backup_fixture="$fixture/backup-state"
	install -d -m 0755 -o root -g root "$backup_fixture"
	install -d -m 0750 -o roadmap -g roadmap "$backup_fixture/data"
	install -d -m 0700 -o root -g root "$backup_fixture/backups"
	sqlite3 "$backup_fixture/data/roadmap.db" 'CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL); INSERT INTO schema_migrations VALUES (8, "fixture"); CREATE TABLE smoke (id INTEGER PRIMARY KEY); INSERT INTO smoke VALUES (1);'
	chown roadmap:roadmap "$backup_fixture/data/roadmap.db"
	chmod 0640 "$backup_fixture/data/roadmap.db"
	backup_output=$(cd /tmp && ROADMAP_STATE_DIR="$backup_fixture" ROADMAP_DATA_DIR="$backup_fixture/data" \
		ROADMAP_DB_PATH="$backup_fixture/data/roadmap.db" ROADMAP_BACKUP_DIR="$backup_fixture/backups" \
		ROADMAP_BACKUP_RETENTION=2 "$BACKUP" manual)
	backup_path=${backup_output#backup=}
	[[ -f "$backup_path" && -f "$backup_path.sha256" && -f "$backup_path.metadata" ]] \
		|| fail 'backup helper did not produce complete artifacts outside its backup cwd'
	printf 'backup_runtime_test=ok\n'
else
	printf 'backup_runtime_test=skipped (requires root, roadmap user, and sqlite3)\n'
fi

# Exercise backup publication and retention with a mocked sqlite3 API. The
# helper still performs its real descriptor identity, checksum, metadata, and
# publication ordering checks; only SQLite and ownership capabilities are
# mocked so this remains deterministic on ordinary developer hosts.
backup_mock_id() {
	if [[ "${1:-}" = -u ]]; then
		printf '0\n'
	else
		command id "$@"
	fi
}

backup_mock_install() {
	local -a args=()
	while [[ $# -gt 0 ]]; do
		case "$1" in
			-o|-g) shift 2 ;;
			*) args+=("$1"); shift ;;
		esac
	done
	command install "${args[@]}"
}

backup_mock_chown() { :; }

backup_mock_stat() {
	local format= path= arg stat_option=-c
	while [[ $# -gt 0 ]]; do
		arg=$1
		case "$arg" in
			-c|-Lc)
				format=$2
				stat_option=$arg
				shift 2
				;;
			*)
				path=$arg
				shift
				;;
		esac
	done
	if [[ "$format" = '%U:%G' ]]; then
		if [[ "$path" = "$BACKUP_MOCK_DB_PATH" || "$path" = "${BACKUP_MOCK_DB_PATH%/*}" || "$path" = /proc/*/fd/* ]]; then
			printf 'roadmap:roadmap\n'
		else
			printf 'root:root\n'
		fi
		return 0
	fi
	command stat "$stat_option" "$format" -- "$path"
}

backup_mock_sqlite3() {
	local arg backup_path= sql
	for arg in "$@"; do
		if [[ "$arg" = .backup\ * ]]; then
			backup_path=${arg#".backup '"}
			backup_path=${backup_path%\'}
		fi
	done
	if [[ -n "$backup_path" ]]; then
		printf 'fixture sqlite backup\n' > "$backup_path"
		if [[ "${BACKUP_MOCK_SOURCE_DRIFT:-0}" = 1 ]]; then
			/bin/mv -- "$BACKUP_MOCK_DB_PATH" "$BACKUP_MOCK_DB_PATH.original"
			printf 'replacement database\n' > "$BACKUP_MOCK_DB_PATH"
		fi
		return 0
	fi
	for sql in "$@"; do
		if [[ "$sql" = 'SELECT COALESCE(MAX(version), 0) FROM schema_migrations;' ]]; then
			printf '8\n'
			return 0
		fi
		if [[ "$sql" = 'PRAGMA integrity_check;' ]]; then
			printf 'ok\n'
			return 0
		fi
		if [[ "$sql" = 'PRAGMA foreign_key_check;' ]]; then
			return 0
		fi
	done
	return 1
}

backup_mock_mv() {
	local -a args=("$@") source destination
	source=${args[$(( ${#args[@]} - 2 ))]}
	destination=${args[$(( ${#args[@]} - 1 ))]}
	if [[ "${BACKUP_MOCK_MV_FAILURE:-}" = metadata &&
		"$destination" = "$BACKUP_MOCK_DIR"/roadmap-*.db.metadata &&
		! -e "$BACKUP_MOCK_FAILURE_MARKER" ]]; then
		: > "$BACKUP_MOCK_FAILURE_MARKER"
		return 1
	fi
	if [[ "${BACKUP_MOCK_MV_FAILURE:-}" = database &&
		"$destination" = "$BACKUP_MOCK_DIR"/roadmap-*.db &&
		! -e "$BACKUP_MOCK_FAILURE_MARKER" ]]; then
		: > "$BACKUP_MOCK_FAILURE_MARKER"
		return 1
	fi
	command mv "$@"
}

backup_mock_setup() {
	local case_dir=$1
	/usr/bin/install -d -m 0755 "$case_dir/state/data" "$case_dir/state/backups"
	chmod 0755 "$case_dir/state"
	chmod 0750 "$case_dir/state/data"
	chmod 0700 "$case_dir/state/backups"
	printf 'fixture source database\n' > "$case_dir/state/data/roadmap.db"
}

backup_mock_run() {
	local name=$1 mode=$2 status case_dir
	local state_dir db_path backup_dir output
	case_dir="$fixture/backup-$name"
	state_dir="$case_dir/state"
	db_path="$state_dir/data/roadmap.db"
	backup_dir="$state_dir/backups"
	output="$case_dir/output.log"
	/usr/bin/install -d -m 0755 "$case_dir"
	backup_mock_setup "$case_dir"
	BACKUP_MOCK_DB_PATH=$db_path
	BACKUP_MOCK_DIR=$backup_dir
	BACKUP_MOCK_MV_FAILURE=$mode
	BACKUP_MOCK_SOURCE_DRIFT=0
	BACKUP_MOCK_FAILURE_MARKER="$case_dir/failure.marker"
	export BACKUP_MOCK_DB_PATH BACKUP_MOCK_DIR BACKUP_MOCK_MV_FAILURE BACKUP_MOCK_SOURCE_DRIFT BACKUP_MOCK_FAILURE_MARKER
	set +e
	(
		export ROADMAP_STATE_DIR="$state_dir" ROADMAP_DATA_DIR="$state_dir/data" \
			ROADMAP_DB_PATH="$db_path" ROADMAP_BACKUP_DIR="$backup_dir" ROADMAP_BACKUP_RETENTION=2
		export -f backup_mock_id backup_mock_install backup_mock_chown backup_mock_stat \
			backup_mock_sqlite3 backup_mock_mv
		id() { backup_mock_id "$@"; }
		install() { backup_mock_install "$@"; }
		chown() { backup_mock_chown "$@"; }
		stat() { backup_mock_stat "$@"; }
		sqlite3() { backup_mock_sqlite3 "$@"; }
		mv() { backup_mock_mv "$@"; }
		export -f id install chown stat sqlite3 mv
		"$BACKUP" manual
	) >"$output" 2>&1
	status=$?
	set -e
	[[ "$status" -ne 0 ]] || fail "backup $name unexpectedly succeeded"
	if compgen -G "$backup_dir/roadmap-*.db*" >/dev/null; then
		fail "backup $name left a partially published artifact"
	fi
}

# The helper must remain usable during a binary-only rollback when current
# points at an older executable that does not implement migration-info.
backup_old_binary_case="$fixture/backup-old-binary"
backup_mock_setup "$backup_old_binary_case"
backup_old_binary_state="$backup_old_binary_case/state"
backup_old_binary_dir="$backup_old_binary_state/backups"
backup_old_binary_path="$backup_old_binary_state/data/roadmap.db"
backup_old_binary_failure="$backup_old_binary_case/failure.marker"
set +e
(
	export ROADMAP_STATE_DIR="$backup_old_binary_state" ROADMAP_DATA_DIR="$backup_old_binary_state/data" \
		ROADMAP_DB_PATH="$backup_old_binary_path" ROADMAP_BACKUP_DIR="$backup_old_binary_dir" ROADMAP_BACKUP_RETENTION=2 \
		ROADMAP_MIGRATION_INFO_BINARY="$old_binary"
	unset ROADMAP_MIGRATION_DIGEST
	BACKUP_MOCK_DB_PATH="$backup_old_binary_path" BACKUP_MOCK_DIR="$backup_old_binary_dir" \
		BACKUP_MOCK_MV_FAILURE= BACKUP_MOCK_SOURCE_DRIFT=0 BACKUP_MOCK_FAILURE_MARKER="$backup_old_binary_failure"
	export BACKUP_MOCK_DB_PATH BACKUP_MOCK_DIR BACKUP_MOCK_MV_FAILURE BACKUP_MOCK_SOURCE_DRIFT BACKUP_MOCK_FAILURE_MARKER
	export -f backup_mock_id backup_mock_install backup_mock_chown backup_mock_stat backup_mock_sqlite3 backup_mock_mv
	id() { backup_mock_id "$@"; }
	install() { backup_mock_install "$@"; }
	chown() { backup_mock_chown "$@"; }
	stat() { backup_mock_stat "$@"; }
	sqlite3() { backup_mock_sqlite3 "$@"; }
	mv() { backup_mock_mv "$@"; }
	export -f id install chown stat sqlite3 mv
	"$BACKUP" manual
) >"$backup_old_binary_case/output.log" 2>&1
backup_old_binary_status=$?
set -e
[[ "$backup_old_binary_status" -eq 0 ]] || fail 'backup failed with an old current binary lacking migration-info'
backup_old_binary_metadata=$(find "$backup_old_binary_dir" -maxdepth 1 -type f -name '*.db.metadata' -print -quit)
[[ -n "$backup_old_binary_metadata" ]] || fail 'old-binary backup did not publish metadata'
grep -Eq '^migration_digest=[0-9a-f]{64}$' "$backup_old_binary_metadata" \
	|| fail 'old-binary backup did not publish deterministic migration digest'
printf 'backup_old_binary_compatibility_test=ok\n'

backup_mock_run publication-metadata-failure metadata
backup_mock_run publication-database-failure database

backup_retention_case="$fixture/backup-retention"
/usr/bin/install -d -m 0755 "$backup_retention_case"
backup_mock_setup "$backup_retention_case"
backup_retention_state="$backup_retention_case/state"
backup_retention_dir="$backup_retention_state/backups"
backup_old="$backup_retention_dir/roadmap-20000101T000000Z-manual.db"
backup_incomplete="$backup_retention_dir/roadmap-20000101T000001Z-manual.db"
backup_corrupt="$backup_retention_dir/roadmap-20000101T000002Z-manual.db"
printf 'old valid backup\n' > "$backup_old"
printf 'incomplete backup\n' > "$backup_incomplete"
printf 'corrupt backup\n' > "$backup_corrupt"
(
	cd "$backup_retention_dir"
	sha256sum "$(basename -- "$backup_old")" > "$(basename -- "$backup_old").sha256"
)
printf 'release_sha=manual\ncreated_at=2000-01-01T00:00:00Z\n' > "$backup_old.metadata"
printf '%064d  %s\n' 0 "$(basename -- "$backup_corrupt")" > "$backup_corrupt.sha256"
printf 'release_sha=manual\ncreated_at=2000-01-01T00:00:00Z\n' > "$backup_corrupt.metadata"
chmod 0600 "$backup_old" "$backup_old.sha256" "$backup_old.metadata" \
	"$backup_incomplete" "$backup_corrupt" "$backup_corrupt.sha256" "$backup_corrupt.metadata"
touch -d '2000-01-01T00:00:00Z' "$backup_old" "$backup_old.sha256" "$backup_old.metadata"
BACKUP_MOCK_DB_PATH="$backup_retention_state/data/roadmap.db"
BACKUP_MOCK_DIR=$backup_retention_dir
BACKUP_MOCK_MV_FAILURE=
BACKUP_MOCK_SOURCE_DRIFT=0
BACKUP_MOCK_FAILURE_MARKER="$backup_retention_case/failure.marker"
export BACKUP_MOCK_DB_PATH BACKUP_MOCK_DIR BACKUP_MOCK_MV_FAILURE BACKUP_MOCK_SOURCE_DRIFT BACKUP_MOCK_FAILURE_MARKER
set +e
(
	export ROADMAP_STATE_DIR="$backup_retention_state" ROADMAP_DATA_DIR="$backup_retention_state/data" \
		ROADMAP_DB_PATH="$BACKUP_MOCK_DB_PATH" ROADMAP_BACKUP_DIR="$backup_retention_dir" ROADMAP_BACKUP_RETENTION=1
	export -f backup_mock_id backup_mock_install backup_mock_chown backup_mock_stat \
		backup_mock_sqlite3 backup_mock_mv
	id() { backup_mock_id "$@"; }
	install() { backup_mock_install "$@"; }
	chown() { backup_mock_chown "$@"; }
	stat() { backup_mock_stat "$@"; }
	sqlite3() { backup_mock_sqlite3 "$@"; }
	mv() { backup_mock_mv "$@"; }
	export -f id install chown stat sqlite3 mv
	"$BACKUP" manual
) >"$backup_retention_case/output.log" 2>&1
backup_retention_status=$?
set -e
[[ "$backup_retention_status" -eq 0 ]] || fail 'backup retention fixture failed unexpectedly'
[[ -f "$backup_old" && -f "$backup_old.sha256" && -f "$backup_old.metadata" ]] \
	|| fail 'retention removed a valid historical backup without schema metadata'
[[ -f "$backup_incomplete" && -f "$backup_corrupt" && -f "$backup_corrupt.sha256" && -f "$backup_corrupt.metadata" ]] \
	|| fail 'retention removed or counted an invalid/incomplete backup set'

backup_drift_case="$fixture/backup-source-drift"
/usr/bin/install -d -m 0755 "$backup_drift_case"
backup_mock_setup "$backup_drift_case"
backup_drift_state="$backup_drift_case/state"
backup_drift_dir="$backup_drift_state/backups"
BACKUP_MOCK_DB_PATH="$backup_drift_state/data/roadmap.db"
BACKUP_MOCK_DIR=$backup_drift_dir
BACKUP_MOCK_MV_FAILURE=
BACKUP_MOCK_SOURCE_DRIFT=1
BACKUP_MOCK_FAILURE_MARKER="$backup_drift_case/failure.marker"
export BACKUP_MOCK_DB_PATH BACKUP_MOCK_DIR BACKUP_MOCK_MV_FAILURE BACKUP_MOCK_SOURCE_DRIFT BACKUP_MOCK_FAILURE_MARKER
set +e
(
	export ROADMAP_STATE_DIR="$backup_drift_state" ROADMAP_DATA_DIR="$backup_drift_state/data" \
		ROADMAP_DB_PATH="$BACKUP_MOCK_DB_PATH" ROADMAP_BACKUP_DIR="$backup_drift_dir" ROADMAP_BACKUP_RETENTION=2
	export -f backup_mock_id backup_mock_install backup_mock_chown backup_mock_stat \
		backup_mock_sqlite3 backup_mock_mv
	id() { backup_mock_id "$@"; }
	install() { backup_mock_install "$@"; }
	chown() { backup_mock_chown "$@"; }
	stat() { backup_mock_stat "$@"; }
	sqlite3() { backup_mock_sqlite3 "$@"; }
	mv() { backup_mock_mv "$@"; }
	export -f id install chown stat sqlite3 mv
	"$BACKUP" manual
) >"$backup_drift_case/output.log" 2>&1
backup_drift_status=$?
set -e
[[ "$backup_drift_status" -ne 0 ]] || fail 'backup accepted a source identity replacement race'
contains 'database path changed during backup' "$backup_drift_case/output.log"
if compgen -G "$backup_drift_dir/roadmap-*.db*" >/dev/null; then
	fail 'source identity drift left a published backup artifact'
fi
printf 'backup_publication_runtime_tests=ok\n'

printf 'deployment_security_tests=ok\n'
