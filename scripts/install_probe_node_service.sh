#!/usr/bin/env bash
set -euo pipefail

RELEASE_REPO="${RELEASE_REPO:-fengzhanhuaer/CloudHelper}"
RELEASE_TAG="${RELEASE_TAG:-latest}"
ASSET_NAME="${ASSET_NAME:-}"

INSTALL_DIR="${INSTALL_DIR:-/opt/cloudhelper/probe_node}"
SERVICE_NAME="${SERVICE_NAME:-probe_node}"
SERVICE_USER="${SERVICE_USER:-cloudhelper}"
SERVICE_GROUP="${SERVICE_GROUP:-cloudhelper}"
RUNTIME_MODE="auto"
MANUAL_ENABLE_BOOT="${MANUAL_ENABLE_BOOT:-true}" # true | false

BIN_PATH="${INSTALL_DIR}/probe_node"
DATA_DIR="${INSTALL_DIR}/data"
RUN_DIR="${INSTALL_DIR}/run"
LOG_DIR="${INSTALL_DIR}/log"
PID_FILE="${RUN_DIR}/${SERVICE_NAME}.pid"
LOG_FILE="${LOG_DIR}/${SERVICE_NAME}.log"

ENV_FILE="/etc/default/${SERVICE_NAME}"
SYSTEMD_SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"
OPENRC_SERVICE_FILE="/etc/init.d/${SERVICE_NAME}"
MANUAL_CTL_FILE="/usr/local/bin/${SERVICE_NAME}-ctl"
RC_LOCAL_FILE=""
MANUAL_BOOT_STATUS="not-applicable"

SERVICE_IMPL=""
INSTALLED_RELEASE_TAG=""
INSTALLED_ASSET_NAME=""

log() {
  echo "[cloudhelper-probe-node-install] $*"
}

die() {
  echo "[cloudhelper-probe-node-install][ERROR] $*" >&2
  exit 1
}

require_root() {
  if [[ "${EUID}" -ne 0 ]]; then
    die "please run as root (use sudo)"
  fi
}

install_packages() {
  if command -v apk >/dev/null 2>&1; then
    apk add --no-cache "$@"
    return
  fi
  if command -v apt-get >/dev/null 2>&1; then
    apt-get update
    apt-get install -y "$@"
    return
  fi
  if command -v dnf >/dev/null 2>&1; then
    dnf install -y "$@"
    return
  fi
  if command -v yum >/dev/null 2>&1; then
    yum install -y "$@"
    return
  fi
  die "no supported package manager found, install required packages manually: $*"
}

ensure_cmd() {
  local cmd="$1"
  local pkg="$2"
  if ! command -v "${cmd}" >/dev/null 2>&1; then
    log "${cmd} not found, installing package ${pkg}..."
    install_packages "${pkg}"
  fi
}

ensure_dependencies() {
  local missing=()
  command -v curl >/dev/null 2>&1 || missing+=("curl")
  command -v jq >/dev/null 2>&1 || missing+=("jq")
  command -v tar >/dev/null 2>&1 || missing+=("tar")

  if [[ ${#missing[@]} -gt 0 ]]; then
    log "installing missing dependencies: ${missing[*]}"
    install_packages "${missing[@]}"
  fi

  if [[ ! -f /etc/ssl/certs/ca-certificates.crt ]] && [[ ! -f /etc/ssl/cert.pem ]]; then
    log "ca certificates not found, installing ca-certificates"
    install_packages ca-certificates
  fi

  if command -v update-ca-certificates >/dev/null 2>&1; then
    update-ca-certificates >/dev/null 2>&1 || true
  else
    if [[ -f /etc/alpine-release ]]; then
      apk add --no-cache ca-certificates >/dev/null 2>&1 || true
    fi
  fi

  if [[ -f /etc/alpine-release ]]; then
    # Compatibility for binaries linked with glibc loader in some releases.
    apk add --no-cache gcompat >/dev/null 2>&1 || true
    apk add --no-cache libc6-compat >/dev/null 2>&1 || true
  fi
}

resolve_platform() {
  local os
  local arch
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  arch="$(uname -m)"

  if [[ "${os}" != "linux" ]]; then
    die "this installer currently supports linux only"
  fi

  case "${arch}" in
    x86_64)
      TARGET_ARCH="amd64"
      TARGET_ARCH_PATTERN='amd64|x86_64'
      ;;
    aarch64|arm64)
      TARGET_ARCH="arm64"
      TARGET_ARCH_PATTERN='arm64|aarch64'
      ;;
    armv7l|armv7)
      TARGET_ARCH="armv7"
      TARGET_ARCH_PATTERN='armv7|armv7l'
      ;;
    *)
      TARGET_ARCH="${arch}"
      TARGET_ARCH_PATTERN="${arch}"
      ;;
  esac

  TARGET_OS="linux"
}

detect_service_impl() {
  if command -v systemctl >/dev/null 2>&1 && [[ -d /run/systemd/system ]]; then
    SERVICE_IMPL="systemd"
  elif command -v rc-service >/dev/null 2>&1 && [[ -d /etc/init.d ]]; then
    SERVICE_IMPL="openrc"
  else
    die "no supported service manager detected (need systemd or openrc)"
  fi

  log "runtime mode selected: ${SERVICE_IMPL}"
}

ensure_service_account() {
  if ! group_exists "${SERVICE_GROUP}"; then
    log "creating group ${SERVICE_GROUP}"
    if command -v groupadd >/dev/null 2>&1; then
      groupadd --system "${SERVICE_GROUP}"
    elif command -v addgroup >/dev/null 2>&1; then
      addgroup -S "${SERVICE_GROUP}"
    else
      die "no supported group add command found (groupadd/addgroup)"
    fi
  fi

  if ! user_exists "${SERVICE_USER}"; then
    log "creating user ${SERVICE_USER}"
    local nologin_shell
    nologin_shell="$(resolve_nologin_shell)"
    if command -v useradd >/dev/null 2>&1; then
      useradd \
        --system \
        --gid "${SERVICE_GROUP}" \
        --home-dir "${INSTALL_DIR}" \
        --create-home \
        --shell "${nologin_shell}" \
        "${SERVICE_USER}"
    elif command -v adduser >/dev/null 2>&1; then
      adduser -S -D -h "${INSTALL_DIR}" -s "${nologin_shell}" -G "${SERVICE_GROUP}" "${SERVICE_USER}"
    else
      die "no supported user add command found (useradd/adduser)"
    fi
  fi
}

group_exists() {
  local group_name="$1"
  if command -v getent >/dev/null 2>&1; then
    getent group "${group_name}" >/dev/null 2>&1
    return
  fi
  grep -qE "^${group_name}:" /etc/group 2>/dev/null
}

user_exists() {
  local user_name="$1"
  if command -v getent >/dev/null 2>&1; then
    getent passwd "${user_name}" >/dev/null 2>&1
    return
  fi
  id -u "${user_name}" >/dev/null 2>&1
}

resolve_nologin_shell() {
  if [[ -x /usr/sbin/nologin ]]; then
    echo "/usr/sbin/nologin"
    return
  fi
  if [[ -x /sbin/nologin ]]; then
    echo "/sbin/nologin"
    return
  fi
  echo "/bin/false"
}

github_api_get() {
  local url="$1"
  if [[ -n "${GITHUB_TOKEN:-}" ]]; then
    curl -fsSL \
      -H "Authorization: Bearer ${GITHUB_TOKEN}" \
      -H "Accept: application/vnd.github+json" \
      "${url}"
  else
    curl -fsSL -H "Accept: application/vnd.github+json" "${url}"
  fi
}

github_download() {
  local url="$1"
  local output="$2"
  if [[ -n "${GITHUB_TOKEN:-}" ]]; then
    curl -fL \
      -H "Authorization: Bearer ${GITHUB_TOKEN}" \
      -H "Accept: application/octet-stream" \
      "${url}" \
      -o "${output}"
  else
    curl -fL "${url}" -o "${output}"
  fi
}

pick_asset_url() {
  local release_json="$1"
  local url=""
  local node_pattern='probe[-_]?node'
  local known_arch_pattern='amd64|x86_64|arm64|aarch64|armv7|armv7l|386|i386|x86|ppc64le|s390x|riscv64'

  if [[ -n "${ASSET_NAME}" ]]; then
    url="$(echo "${release_json}" | jq -r --arg name "${ASSET_NAME}" '.assets[] | select(.name==$name) | .browser_download_url' | head -n1)"
  else
    url="$(echo "${release_json}" | jq -r --arg os "${TARGET_OS}" --arg arch "${TARGET_ARCH_PATTERN}" --arg p "${node_pattern}" '
      .assets[]
      | select(.name | test($p; "i"))
      | select(.name | test($os; "i"))
      | select(.name | test($arch; "i"))
      | .browser_download_url
    ' | head -n1)"

    if [[ -z "${url}" ]]; then
      url="$(echo "${release_json}" | jq -r --arg os "${TARGET_OS}" --arg p "${node_pattern}" --arg known_arch "${known_arch_pattern}" '
        .assets[]
        | select(.name | test($p; "i"))
        | select(.name | test($os; "i"))
        | select((.name | test($known_arch; "i")) | not)
        | .browser_download_url
      ' | head -n1)"
    fi

    if [[ -z "${url}" ]]; then
      url="$(echo "${release_json}" | jq -r --arg p "${node_pattern}" --arg known_arch "${known_arch_pattern}" '
        .assets[]
        | select(.name | test($p; "i"))
        | select((.name | test($known_arch; "i")) | not)
        | select((.name | test("windows|\\.exe"; "i")) | not)
        | .browser_download_url
      ' | head -n1)"
    fi
  fi

  echo "${url}"
}

extract_binary_from_asset() {
  local asset_file="$1"
  local asset_name="$2"
  local work_dir="$3"
  local extract_dir="${work_dir}/extract"
  local binary_candidate=""

  mkdir -p "${extract_dir}"

  case "${asset_name}" in
    *.tar.gz|*.tgz)
      tar -xzf "${asset_file}" -C "${extract_dir}"
      ;;
    *.zip)
      ensure_cmd unzip unzip
      unzip -q "${asset_file}" -d "${extract_dir}"
      ;;
    *)
      cp -f "${asset_file}" "${extract_dir}/${asset_name}"
      ;;
  esac

  binary_candidate="$(find "${extract_dir}" -type f \( \
      -name 'probe_node' -o -name 'probe_node_*' -o \
      -name 'probe-node' -o -name 'probe-node-*' -o \
      -name 'cloudhelper-probe-node*' -o -name '*probe_node*' -o -name '*probe-node*' \
    \) | grep -E -v '\.exe$' | head -n1 || true)"

  if [[ -z "${binary_candidate}" ]]; then
    local lower_asset_name="${asset_name,,}"
    if [[ "${lower_asset_name}" == *probe_node* ]] || [[ "${lower_asset_name}" == *probe-node* ]]; then
      if [[ "${asset_name}" != *.exe ]]; then
        binary_candidate="${extract_dir}/${asset_name}"
      fi
    fi
  fi

  [[ -n "${binary_candidate}" ]] || die "failed to locate linux probe_node binary inside asset ${asset_name}"
  [[ -f "${binary_candidate}" ]] || die "binary candidate does not exist: ${binary_candidate}"

  echo "${binary_candidate}"
}

download_and_install() {
  local api_url
  local release_json
  local asset_url
  local tag_name
  local asset_name
  local tmp_dir
  local asset_file
  local binary_src

  if [[ "${RELEASE_TAG}" == "latest" ]]; then
    api_url="https://api.github.com/repos/${RELEASE_REPO}/releases/latest"
  else
    api_url="https://api.github.com/repos/${RELEASE_REPO}/releases/tags/${RELEASE_TAG}"
  fi

  log "fetching release metadata from ${api_url}"
  release_json="$(github_api_get "${api_url}")"

  tag_name="$(echo "${release_json}" | jq -r '.tag_name // empty')"
  [[ -n "${tag_name}" ]] || die "failed to resolve release tag from GitHub API"

  asset_url="$(pick_asset_url "${release_json}")"
  [[ -n "${asset_url}" ]] || {
    echo "${release_json}" | jq -r '.assets[].name' >&2 || true
    die "failed to find a matching probe_node release asset for ${TARGET_OS}/${TARGET_ARCH}; set ASSET_NAME to override"
  }

  asset_name="$(echo "${release_json}" | jq -r --arg url "${asset_url}" '.assets[] | select(.browser_download_url==$url) | .name' | head -n1)"
  [[ -n "${asset_name}" ]] || die "failed to resolve asset name from selected download url"

  tmp_dir="$(mktemp -d)"
  trap "rm -rf -- \"${tmp_dir}\"" EXIT

  asset_file="${tmp_dir}/${asset_name}"
  log "downloading ${asset_name} from ${tag_name}"
  github_download "${asset_url}" "${asset_file}"

  binary_src="$(extract_binary_from_asset "${asset_file}" "${asset_name}" "${tmp_dir}")"

  mkdir -p "${INSTALL_DIR}" "${DATA_DIR}" "${RUN_DIR}" "${LOG_DIR}"
  if [[ -f "${BIN_PATH}" ]]; then
    local backup="${BIN_PATH}.bak.$(date +%Y%m%d%H%M%S)"
    log "backing up existing binary to ${backup}"
    cp -f "${BIN_PATH}" "${backup}"
  fi

  install -m 0755 "${binary_src}" "${BIN_PATH}"
  chown -R "${SERVICE_USER}:${SERVICE_GROUP}" "${INSTALL_DIR}"
  chmod 0750 "${INSTALL_DIR}" "${DATA_DIR}" "${RUN_DIR}" "${LOG_DIR}"

  INSTALLED_RELEASE_TAG="${tag_name}"
  INSTALLED_ASSET_NAME="${asset_name}"
}

write_env_file() {
  mkdir -p "$(dirname "${ENV_FILE}")"

  if [[ ! -f "${ENV_FILE}" ]]; then
    log "creating ${ENV_FILE}"
    cat >"${ENV_FILE}" <<EOF
# Optional runtime overrides for ${SERVICE_NAME}.
#PROBE_NODE_LISTEN=:16030
#PROBE_NODE_ID=
#PROBE_NODE_SECRET=
#PROBE_CONTROLLER_URL=https://controller.example.com
EOF
  fi

  chown root:"${SERVICE_GROUP}" "${ENV_FILE}" >/dev/null 2>&1 || true
  chmod 0640 "${ENV_FILE}"

  upsert_env_value "PROBE_NODE_ID" "${PROBE_NODE_ID:-}"
  upsert_env_value "PROBE_NODE_SECRET" "${PROBE_NODE_SECRET:-}"
  upsert_env_value "PROBE_CONTROLLER_URL" "${PROBE_CONTROLLER_URL:-}"
}

upsert_env_value() {
  local key="$1"
  local value="$2"

  if [[ -z "${value}" ]]; then
    return
  fi

  local escaped
  escaped="$(printf '%s' "${value}" | sed 's/[\/&]/\\&/g')"

  if grep -qE "^${key}=" "${ENV_FILE}"; then
    sed -i "s|^${key}=.*|${key}=${escaped}|" "${ENV_FILE}"
  else
    printf '%s=%s\n' "${key}" "${value}" >>"${ENV_FILE}"
  fi
}

write_systemd_service_file() {
  log "writing ${SYSTEMD_SERVICE_FILE}"
  cat >"${SYSTEMD_SERVICE_FILE}" <<EOF
[Unit]
Description=CloudHelper Probe Node
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${SERVICE_USER}
Group=${SERVICE_GROUP}
WorkingDirectory=${INSTALL_DIR}
EnvironmentFile=-${ENV_FILE}
ExecStart=${BIN_PATH}
Restart=always
RestartSec=3
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
ProtectSystem=full
ReadWritePaths=${DATA_DIR} ${RUN_DIR} ${LOG_DIR}

[Install]
WantedBy=multi-user.target
EOF
}

enable_and_start_systemd() {
  log "reloading systemd"
  systemctl daemon-reload
  log "enabling ${SERVICE_NAME}"
  systemctl enable "${SERVICE_NAME}" >/dev/null
  log "restarting ${SERVICE_NAME}"
  systemctl restart "${SERVICE_NAME}"
}

write_openrc_service_file() {
  log "writing ${OPENRC_SERVICE_FILE}"
  cat >"${OPENRC_SERVICE_FILE}" <<EOF
#!/sbin/openrc-run

name="CloudHelper Probe Node"
description="CloudHelper probe node daemon"
command="${BIN_PATH}"
command_user="${SERVICE_USER}:${SERVICE_GROUP}"
pidfile="${PID_FILE}"
command_background=true
directory="${INSTALL_DIR}"
output_log="${LOG_FILE}"
error_log="${LOG_FILE}"

if [ -f "${ENV_FILE}" ]; then
  set -a
  . "${ENV_FILE}"
  set +a
fi

depend() {
  need net
}

start_pre() {
  checkpath -d -m 0750 -o ${SERVICE_USER}:${SERVICE_GROUP} "${RUN_DIR}"
  checkpath -d -m 0750 -o ${SERVICE_USER}:${SERVICE_GROUP} "${LOG_DIR}"
}
EOF
  chmod 0755 "${OPENRC_SERVICE_FILE}"
}

enable_and_start_openrc() {
  rc-update add "${SERVICE_NAME}" default >/dev/null 2>&1 || true
  rc-service "${SERVICE_NAME}" restart >/dev/null 2>&1 || rc-service "${SERVICE_NAME}" start
}

write_manual_ctl_script() {
  log "writing manual control script ${MANUAL_CTL_FILE}"
  cat >"${MANUAL_CTL_FILE}" <<EOF
#!/usr/bin/env bash
set -euo pipefail

SERVICE_NAME="${SERVICE_NAME}"
SERVICE_USER="${SERVICE_USER}"
SERVICE_GROUP="${SERVICE_GROUP}"
INSTALL_DIR="${INSTALL_DIR}"
BIN_PATH="${BIN_PATH}"
ENV_FILE="${ENV_FILE}"
PID_FILE="${PID_FILE}"
LOG_FILE="${LOG_FILE}"

log() {
  echo "[\${SERVICE_NAME}-ctl] \$*"
}

is_running() {
  if [[ ! -f "\${PID_FILE}" ]]; then
    return 1
  fi

  local pid
  pid="\$(cat "\${PID_FILE}" 2>/dev/null || true)"
  if [[ -z "\${pid}" ]]; then
    return 1
  fi

  kill -0 "\${pid}" >/dev/null 2>&1
}

run_as_service_user() {
  local script="\$1"
  if command -v runuser >/dev/null 2>&1; then
    runuser -u "\${SERVICE_USER}" -- sh -c "\${script}"
    return
  fi

  if command -v su >/dev/null 2>&1; then
    su -s /bin/sh - "\${SERVICE_USER}" -c "\${script}"
    return
  fi

  echo "[\${SERVICE_NAME}-ctl] ERROR: runuser/su not found" >&2
  exit 1
}

start() {
  if is_running; then
    local running_pid
    running_pid="\$(cat "\${PID_FILE}")"
    log "already running (pid=\${running_pid})"
    return 0
  fi

  mkdir -p "\$(dirname "\${PID_FILE}")" "\$(dirname "\${LOG_FILE}")"
  chown -R "\${SERVICE_USER}:\${SERVICE_GROUP}" "\$(dirname "\${PID_FILE}")" "\$(dirname "\${LOG_FILE}")" >/dev/null 2>&1 || true

  run_as_service_user '
    set -e
    cd "'"\${INSTALL_DIR}"'"
    set -a
    [ -f "'"\${ENV_FILE}"'" ] && . "'"\${ENV_FILE}"'"
    set +a
    nohup "'"\${BIN_PATH}"'" >> "'"\${LOG_FILE}"'" 2>&1 &
    echo \$! > "'"\${PID_FILE}"'"
  '

  sleep 1
  if is_running; then
    local pid
    pid="\$(cat "\${PID_FILE}")"
    log "started (pid=\${pid}, log=\${LOG_FILE})"
  else
    log "failed to start, check log: \${LOG_FILE}"
    return 1
  fi
}

stop() {
  if ! is_running; then
    rm -f "\${PID_FILE}"
    log "already stopped"
    return 0
  fi

  local pid
  pid="\$(cat "\${PID_FILE}")"
  kill "\${pid}" >/dev/null 2>&1 || true

  for _ in \$(seq 1 15); do
    if kill -0 "\${pid}" >/dev/null 2>&1; then
      sleep 1
    else
      break
    fi
  done

  if kill -0 "\${pid}" >/dev/null 2>&1; then
    kill -9 "\${pid}" >/dev/null 2>&1 || true
  fi

  rm -f "\${PID_FILE}"
  log "stopped"
}

status() {
  if is_running; then
    local pid
    pid="\$(cat "\${PID_FILE}")"
    log "running (pid=\${pid})"
  else
    log "stopped"
    return 3
  fi
}

show_log() {
  if [[ -f "\${LOG_FILE}" ]]; then
    tail -n 100 "\${LOG_FILE}"
  else
    log "log file not found: \${LOG_FILE}"
  fi
}

case "\${1:-}" in
  start)
    start
    ;;
  stop)
    stop
    ;;
  restart)
    stop
    start
    ;;
  status)
    status
    ;;
  log)
    show_log
    ;;
  *)
    echo "Usage: \${0} {start|stop|restart|status|log}"
    exit 2
    ;;
esac
EOF

  chmod 0755 "${MANUAL_CTL_FILE}"
}

enable_and_start_manual() {
  write_manual_ctl_script
  "${MANUAL_CTL_FILE}" restart || "${MANUAL_CTL_FILE}" start
  configure_manual_boot
}

select_rc_local_file() {
  if [[ -f /etc/rc.local ]]; then
    RC_LOCAL_FILE="/etc/rc.local"
    return
  fi

  if [[ -f /etc/rc.d/rc.local ]]; then
    RC_LOCAL_FILE="/etc/rc.d/rc.local"
    return
  fi

  RC_LOCAL_FILE="/etc/rc.local"
}

configure_manual_boot() {
  local normalized
  normalized="$(echo "${MANUAL_ENABLE_BOOT}" | tr '[:upper:]' '[:lower:]')"
  if [[ "${normalized}" != "true" && "${normalized}" != "1" && "${normalized}" != "yes" ]]; then
    MANUAL_BOOT_STATUS="disabled"
    return
  fi

  select_rc_local_file

  if [[ ! -f "${RC_LOCAL_FILE}" ]]; then
    cat >"${RC_LOCAL_FILE}" <<'EOF'
#!/usr/bin/env sh
exit 0
EOF
  fi

  local start_marker="# cloudhelper-probe-node-start"
  local end_marker="# cloudhelper-probe-node-end"
  local start_cmd="${MANUAL_CTL_FILE} start >/dev/null 2>&1 || true"

  if grep -q "${start_marker}" "${RC_LOCAL_FILE}"; then
    chmod 0755 "${RC_LOCAL_FILE}"
    MANUAL_BOOT_STATUS="configured (${RC_LOCAL_FILE})"
    return
  fi

  local tmp
  local inserted=0
  tmp="$(mktemp)"

  while IFS= read -r line; do
    if [[ ${inserted} -eq 0 && "${line}" =~ ^exit[[:space:]]+0[[:space:]]*$ ]]; then
      {
        echo "${start_marker}"
        echo "${start_cmd}"
        echo "${end_marker}"
      } >>"${tmp}"
      inserted=1
    fi
    echo "${line}" >>"${tmp}"
  done <"${RC_LOCAL_FILE}"

  if [[ ${inserted} -eq 0 ]]; then
    {
      echo "${start_marker}"
      echo "${start_cmd}"
      echo "${end_marker}"
    } >>"${tmp}"
  fi

  mv "${tmp}" "${RC_LOCAL_FILE}"
  chmod 0755 "${RC_LOCAL_FILE}"
  MANUAL_BOOT_STATUS="configured (${RC_LOCAL_FILE})"
}

show_summary() {
  log "installed successfully"
  log "service: ${SERVICE_NAME} (${SERVICE_IMPL})"
  log "binary:  ${BIN_PATH}"
  log "data:    ${DATA_DIR}"
  log "release: ${INSTALLED_RELEASE_TAG}"
  log "asset:   ${INSTALLED_ASSET_NAME}"

  if [[ "${SERVICE_IMPL}" == "systemd" ]]; then
    log "next steps:"
    log "  1) systemctl status ${SERVICE_NAME} --no-pager"
    log "  2) journalctl -u ${SERVICE_NAME} -f"
  elif [[ "${SERVICE_IMPL}" == "openrc" ]]; then
    log "next steps:"
    log "  1) rc-service ${SERVICE_NAME} status"
    log "  2) tail -f ${LOG_FILE}"
  elif [[ "${SERVICE_IMPL}" == "manual" ]]; then
    log "next steps:"
    log "  1) ${MANUAL_CTL_FILE} status"
    log "  2) ${MANUAL_CTL_FILE} log"
    log "  3) boot autostart: ${MANUAL_BOOT_STATUS}"
  else
    die "unsupported service implementation: ${SERVICE_IMPL}"
  fi
}

main() {
  require_root
  ensure_dependencies
  resolve_platform
  detect_service_impl
  ensure_service_account
  download_and_install
  write_env_file

  if [[ "${SERVICE_IMPL}" == "systemd" ]]; then
    write_systemd_service_file
    enable_and_start_systemd
  elif [[ "${SERVICE_IMPL}" == "openrc" ]]; then
    write_openrc_service_file
    enable_and_start_openrc
  elif [[ "${SERVICE_IMPL}" == "manual" ]]; then
    enable_and_start_manual
  else
    die "unsupported service implementation: ${SERVICE_IMPL}"
  fi

  show_summary
}

main "$@"
