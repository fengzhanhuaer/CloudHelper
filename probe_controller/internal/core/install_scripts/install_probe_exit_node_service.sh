#!/usr/bin/env bash
set -euo pipefail

RELEASE_REPO="${RELEASE_REPO:-fengzhanhuaer/CloudHelper}"
RELEASE_TAG="${RELEASE_TAG:-latest}"
INSTALL_DIR="${INSTALL_DIR:-/opt/cloudhelper/probe_exit_node}"
SERVICE_NAME="${SERVICE_NAME:-probe_exit_node}"
PROGRAM_ASSET="cloudhelper-probe-exit-node-linux-amd64"
MIHOMO_VERSION="v1.19.29"
MIHOMO_ASSET="mihomo-linux-amd64-compatible-v1.19.29.gz"
MIHOMO_SHA256="5612e698e96c8b8ad15abc4c0a4f098eba9234354b4f248cb97f2528e215b094"

log() { echo "[cloudhelper-probe-exit-node] $*"; }
die() { echo "[cloudhelper-probe-exit-node][ERROR] $*" >&2; exit 1; }

install_packages() {
  if command -v apk >/dev/null 2>&1; then
    apk add --no-cache "$@"
  elif command -v apt-get >/dev/null 2>&1; then
    apt-get update
    apt-get install -y "$@"
  elif command -v dnf >/dev/null 2>&1; then
    dnf install -y "$@"
  elif command -v yum >/dev/null 2>&1; then
    yum install -y "$@"
  else
    die "no supported package manager found; install required packages manually: $*"
  fi
}

ensure_dependencies() {
  local missing=()
  command -v curl >/dev/null 2>&1 || missing+=("curl")
  command -v gzip >/dev/null 2>&1 || missing+=("gzip")
  command -v jq >/dev/null 2>&1 || missing+=("jq")
  command -v sha256sum >/dev/null 2>&1 || missing+=("coreutils")
  if [[ ${#missing[@]} -gt 0 ]]; then
    log "installing missing dependencies: ${missing[*]}"
    install_packages "${missing[@]}"
  fi
  if [[ ! -f /etc/ssl/certs/ca-certificates.crt && ! -f /etc/ssl/cert.pem ]]; then
    install_packages ca-certificates
  fi
  command -v update-ca-certificates >/dev/null 2>&1 && update-ca-certificates >/dev/null 2>&1 || true
}

[[ "${EUID}" -eq 0 ]] || die "please run as root"
[[ "$(uname -s)" == "Linux" && "$(uname -m)" == "x86_64" ]] || die "probe_exit_node supports Linux x86_64 only"
ensure_dependencies
command -v systemctl >/dev/null 2>&1 || die "systemd is required for native installation; use the Docker shell on non-systemd Linux"
[[ -n "${PROBE_NODE_ID:-}" ]] || die "PROBE_NODE_ID is required"
[[ -n "${PROBE_NODE_SECRET:-}" ]] || die "PROBE_NODE_SECRET is required"
[[ "${PROBE_CONTROLLER_URL:-}" == https://* ]] || die "PROBE_CONTROLLER_URL must use HTTPS"
[[ "${PROBE_NODE_ID}${PROBE_NODE_SECRET}${PROBE_CONTROLLER_URL}" != *$'\n'* && "${PROBE_NODE_ID}${PROBE_NODE_SECRET}${PROBE_CONTROLLER_URL}" != *$'\r'* ]] || die "identity values must not contain newlines"

mkdir -p "${INSTALL_DIR}/data" "${INSTALL_DIR}/log" "${INSTALL_DIR}/temp"
chmod 0700 "${INSTALL_DIR}/data"
work_dir="$(mktemp -d "${INSTALL_DIR}/temp/install.XXXXXX")"
transaction_started=0
had_program=0
had_mihomo=0
had_license=0
had_defaults=0
had_service=0
was_active=0
was_enabled=0

rollback_install() {
  log "install failed; restoring the previous probe/Mihomo pair"
  systemctl stop "${SERVICE_NAME}" >/dev/null 2>&1 || true
  if [[ "${had_program}" -eq 1 ]]; then
    install -m 0755 "${work_dir}/probe_exit_node.backup" "${INSTALL_DIR}/probe_exit_node"
  else
    rm -f "${INSTALL_DIR}/probe_exit_node"
  fi
  if [[ "${had_mihomo}" -eq 1 ]]; then
    install -m 0755 "${work_dir}/mihomo.backup" "${INSTALL_DIR}/data/mihomo"
  else
    rm -f "${INSTALL_DIR}/data/mihomo"
  fi
  if [[ "${had_license}" -eq 1 ]]; then
    install -m 0644 "${work_dir}/mihomo-LICENSE.backup" "${INSTALL_DIR}/data/mihomo-LICENSE"
  else
    rm -f "${INSTALL_DIR}/data/mihomo-LICENSE"
  fi
  if [[ "${had_defaults}" -eq 1 ]]; then
    install -m 0600 "${work_dir}/service-defaults.backup" "/etc/default/${SERVICE_NAME}"
  else
    rm -f "/etc/default/${SERVICE_NAME}"
  fi
  if [[ "${had_service}" -eq 1 ]]; then
    install -m 0644 "${work_dir}/service-unit.backup" "/etc/systemd/system/${SERVICE_NAME}.service"
  else
    rm -f "/etc/systemd/system/${SERVICE_NAME}.service"
  fi
  systemctl daemon-reload >/dev/null 2>&1 || true
  if [[ "${was_enabled}" -eq 1 ]]; then
    systemctl enable "${SERVICE_NAME}" >/dev/null 2>&1 || true
  else
    systemctl disable "${SERVICE_NAME}" >/dev/null 2>&1 || true
  fi
  if [[ "${was_active}" -eq 1 && "${had_program}" -eq 1 && "${had_mihomo}" -eq 1 ]]; then
    systemctl restart "${SERVICE_NAME}" >/dev/null 2>&1 || true
  fi
}

finish_install() {
  rc=$?
  trap - EXIT
  if [[ "${rc}" -ne 0 && "${transaction_started}" -eq 1 ]]; then
    rollback_install
  fi
  rm -rf "${work_dir}"
  exit "${rc}"
}
trap finish_install EXIT

if [[ "${RELEASE_TAG}" == "latest" ]]; then
  program_url="https://github.com/${RELEASE_REPO}/releases/latest/download/${PROGRAM_ASSET}"
  manifest_url="https://github.com/${RELEASE_REPO}/releases/latest/download/cloudhelper-probe-exit-node-manifest.json"
  license_url="https://github.com/${RELEASE_REPO}/releases/latest/download/mihomo-LICENSE"
else
  program_url="https://github.com/${RELEASE_REPO}/releases/download/${RELEASE_TAG}/${PROGRAM_ASSET}"
  manifest_url="https://github.com/${RELEASE_REPO}/releases/download/${RELEASE_TAG}/cloudhelper-probe-exit-node-manifest.json"
  license_url="https://github.com/${RELEASE_REPO}/releases/download/${RELEASE_TAG}/mihomo-LICENSE"
fi
curl -fL --retry 5 --connect-timeout 15 "${manifest_url}" -o "${work_dir}/manifest.json"
jq -e --arg version "${MIHOMO_VERSION}" --arg asset "${MIHOMO_ASSET}" --arg sha "${MIHOMO_SHA256}" '
  .schema_version == 1 and .build_kind == "mihomo_exit" and .os == "linux" and .arch == "amd64"
  and (.version | test("^v[0-9]+\\.[0-9]+\\.[0-9]+$"))
  and .compatible_program_versions.min == .version and .compatible_program_versions.max == .version
  and .program.asset == "cloudhelper-probe-exit-node-linux-amd64"
  and (.program.sha256 | test("^[0-9a-f]{64}$"))
  and .mihomo.version == $version and .mihomo.asset == $asset and .mihomo.sha256 == $sha
  and (.mihomo.url | startswith("https://github.com/MetaCubeX/mihomo/releases/download/"))
' "${work_dir}/manifest.json" >/dev/null || die "paired upgrade manifest is invalid"
program_sha256="$(jq -r '.program.sha256' "${work_dir}/manifest.json")"
mihomo_url="$(jq -r '.mihomo.url' "${work_dir}/manifest.json")"

log "downloading probe_exit_node"
curl -fL --retry 5 --connect-timeout 15 "${program_url}" -o "${work_dir}/probe_exit_node"
echo "${program_sha256}  ${work_dir}/probe_exit_node" | sha256sum -c -
chmod 0755 "${work_dir}/probe_exit_node"
"${work_dir}/probe_exit_node" --upgrade-verify --upgrade-verify-duration=5 --upgrade-verify-build-kind=mihomo_exit

log "downloading Mihomo ${MIHOMO_VERSION}"
curl -fL --retry 5 --connect-timeout 15 "${mihomo_url}" -o "${work_dir}/${MIHOMO_ASSET}"
echo "${MIHOMO_SHA256}  ${work_dir}/${MIHOMO_ASSET}" | sha256sum -c -
gzip -dc "${work_dir}/${MIHOMO_ASSET}" > "${work_dir}/mihomo"
chmod 0755 "${work_dir}/mihomo"
"${work_dir}/mihomo" -v
curl -fL --retry 5 --connect-timeout 15 "${license_url}" -o "${work_dir}/mihomo-LICENSE"
[[ -s "${work_dir}/mihomo-LICENSE" ]] || die "Mihomo license asset is empty"

if [[ -f "${INSTALL_DIR}/probe_exit_node" ]]; then
  had_program=1
  cp -p "${INSTALL_DIR}/probe_exit_node" "${work_dir}/probe_exit_node.backup"
fi
if [[ -f "${INSTALL_DIR}/data/mihomo" ]]; then
  had_mihomo=1
  cp -p "${INSTALL_DIR}/data/mihomo" "${work_dir}/mihomo.backup"
fi
if [[ -f "${INSTALL_DIR}/data/mihomo-LICENSE" ]]; then
  had_license=1
  cp -p "${INSTALL_DIR}/data/mihomo-LICENSE" "${work_dir}/mihomo-LICENSE.backup"
fi
if [[ -f "/etc/default/${SERVICE_NAME}" ]]; then
  had_defaults=1
  cp -p "/etc/default/${SERVICE_NAME}" "${work_dir}/service-defaults.backup"
fi
if [[ -f "/etc/systemd/system/${SERVICE_NAME}.service" ]]; then
  had_service=1
  cp -p "/etc/systemd/system/${SERVICE_NAME}.service" "${work_dir}/service-unit.backup"
fi
if systemctl is-active --quiet "${SERVICE_NAME}"; then
  was_active=1
fi
if systemctl is-enabled --quiet "${SERVICE_NAME}"; then
  was_enabled=1
fi

install -m 0755 "${work_dir}/probe_exit_node" "${INSTALL_DIR}/probe_exit_node.new"
install -m 0755 "${work_dir}/mihomo" "${INSTALL_DIR}/data/mihomo.new"
install -m 0644 "${work_dir}/mihomo-LICENSE" "${INSTALL_DIR}/data/mihomo-LICENSE.new"
transaction_started=1
mv -f "${INSTALL_DIR}/probe_exit_node.new" "${INSTALL_DIR}/probe_exit_node"
mv -f "${INSTALL_DIR}/data/mihomo.new" "${INSTALL_DIR}/data/mihomo"
mv -f "${INSTALL_DIR}/data/mihomo-LICENSE.new" "${INSTALL_DIR}/data/mihomo-LICENSE"

systemd_env_value() {
  local value="$1"
  value="${value//\\/\\\\}"
  value="${value//\"/\\\"}"
  printf '"%s"' "${value}"
}
{
  printf 'PROBE_NODE_ID=%s\n' "$(systemd_env_value "${PROBE_NODE_ID}")"
  printf 'PROBE_NODE_SECRET=%s\n' "$(systemd_env_value "${PROBE_NODE_SECRET}")"
  printf 'PROBE_CONTROLLER_URL=%s\n' "$(systemd_env_value "${PROBE_CONTROLLER_URL}")"
} > "/etc/default/${SERVICE_NAME}"
chmod 0600 "/etc/default/${SERVICE_NAME}"

cat > "/etc/systemd/system/${SERVICE_NAME}.service" <<EOF
[Unit]
Description=CloudHelper Mihomo Exit Probe
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=${INSTALL_DIR}
EnvironmentFile=/etc/default/${SERVICE_NAME}
ExecStart=${INSTALL_DIR}/probe_exit_node
Restart=always
RestartSec=3
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=${INSTALL_DIR}
LimitNOFILE=262144
MemoryMax=1G
TasksMax=512

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable "${SERVICE_NAME}"
systemctl restart "${SERVICE_NAME}"
transaction_started=0
systemctl --no-pager --full status "${SERVICE_NAME}" || true
log "installed at ${INSTALL_DIR}"
