#!/bin/sh
set -eu

RELEASE_REPO="${RELEASE_REPO:-fengzhanhuaer/CloudHelper}"
RELEASE_TAG="${RELEASE_TAG:-latest}"
INSTALL_DIR="${INSTALL_DIR:-/opt/cloudhelper/probe_router}"
SERVICE_NAME="${SERVICE_NAME:-probe_router}"

log() { echo "[cloudhelper-probe-router] $*"; }
die() { echo "[cloudhelper-probe-router][ERROR] $*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "please run as root"
[ "$(uname -s)" = "Linux" ] || die "probe_router supports Linux only"
command -v apk >/dev/null 2>&1 || die "probe_router native installation requires Alpine Linux"
command -v rc-service >/dev/null 2>&1 || die "OpenRC is required"
[ -n "${PROBE_NODE_ID:-}" ] || die "PROBE_NODE_ID is required"
[ -n "${PROBE_NODE_SECRET:-}" ] || die "PROBE_NODE_SECRET is required"
case "${PROBE_CONTROLLER_URL:-}" in https://*) ;; *) die "PROBE_CONTROLLER_URL must use HTTPS" ;; esac

case "$(uname -m)" in
  x86_64) GOARCH="amd64" ;;
  aarch64|arm64) GOARCH="arm64" ;;
  *) die "unsupported architecture: $(uname -m); only x86_64 and aarch64 are supported" ;;
esac
PROGRAM_ASSET="cloudhelper-probe-router-linux-${GOARCH}"

apk add --no-cache ca-certificates curl iproute2 nftables openrc
update-ca-certificates >/dev/null 2>&1 || true
mkdir -p "${INSTALL_DIR}/data" "${INSTALL_DIR}/log" "${INSTALL_DIR}/temp"
chmod 0700 "${INSTALL_DIR}/data"
work_dir="$(mktemp -d "${INSTALL_DIR}/temp/install.XXXXXX")"
cleanup() { rm -rf "${work_dir}"; }
trap cleanup EXIT INT TERM

if [ "${RELEASE_TAG}" = "latest" ]; then
  program_url="https://github.com/${RELEASE_REPO}/releases/latest/download/${PROGRAM_ASSET}"
else
  program_url="https://github.com/${RELEASE_REPO}/releases/download/${RELEASE_TAG}/${PROGRAM_ASSET}"
fi

log "downloading ${PROGRAM_ASSET}"
curl -fL --retry 5 --connect-timeout 15 "${program_url}" -o "${work_dir}/probe_router"
chmod 0755 "${work_dir}/probe_router"
"${work_dir}/probe_router" --upgrade-verify --upgrade-verify-duration=5 --upgrade-verify-build-kind=linux_router

had_program=0
was_started=0
if [ -f "${INSTALL_DIR}/probe_router" ]; then
  had_program=1
  cp -p "${INSTALL_DIR}/probe_router" "${work_dir}/probe_router.backup"
fi
if rc-service "${SERVICE_NAME}" status >/dev/null 2>&1; then
  was_started=1
  rc-service "${SERVICE_NAME}" stop >/dev/null 2>&1 || true
fi

rollback() {
  log "installation failed; restoring previous binary"
  rc-service "${SERVICE_NAME}" stop >/dev/null 2>&1 || true
  if [ "${had_program}" -eq 1 ]; then
    install -m 0755 "${work_dir}/probe_router.backup" "${INSTALL_DIR}/probe_router"
    [ "${was_started}" -eq 1 ] && rc-service "${SERVICE_NAME}" start >/dev/null 2>&1 || true
  else
    rm -f "${INSTALL_DIR}/probe_router"
  fi
}

install -m 0755 "${work_dir}/probe_router" "${INSTALL_DIR}/probe_router.new"
mv -f "${INSTALL_DIR}/probe_router.new" "${INSTALL_DIR}/probe_router"

escape_conf() { printf '%s' "$1" | sed "s/'/'\\\\''/g"; }
{
  echo "PROBE_NODE_ID='$(escape_conf "${PROBE_NODE_ID}")'"
  echo "PROBE_NODE_SECRET='$(escape_conf "${PROBE_NODE_SECRET}")'"
  echo "PROBE_CONTROLLER_URL='$(escape_conf "${PROBE_CONTROLLER_URL}")'"
} > "/etc/conf.d/${SERVICE_NAME}"
chmod 0600 "/etc/conf.d/${SERVICE_NAME}"

cat > "/etc/init.d/${SERVICE_NAME}" <<EOF
#!/sbin/openrc-run
name="CloudHelper Linux Router Probe"
description="CloudHelper Linux side-router probe"
command="${INSTALL_DIR}/probe_router"
command_background="yes"
directory="${INSTALL_DIR}"
pidfile="/run/\${RC_SVCNAME}.pid"
output_log="${INSTALL_DIR}/log/openrc.log"
error_log="${INSTALL_DIR}/log/openrc.log"
export PROBE_NODE_ID PROBE_NODE_SECRET PROBE_CONTROLLER_URL

depend() {
  need net
  after firewall
}
EOF
chmod 0755 "/etc/init.d/${SERVICE_NAME}"

if ! rc-update add "${SERVICE_NAME}" default >/dev/null 2>&1 || ! rc-service "${SERVICE_NAME}" restart; then
  rollback
  die "OpenRC service failed to start"
fi
log "installed ${PROGRAM_ASSET} at ${INSTALL_DIR}"
