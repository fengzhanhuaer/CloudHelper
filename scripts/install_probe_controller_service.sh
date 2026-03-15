#!/usr/bin/env bash
set -euo pipefail

RELEASE_REPO="${RELEASE_REPO:-fengzhanhuaer/CloudHelper}"
RELEASE_TAG="${RELEASE_TAG:-latest}"
ASSET_NAME="${ASSET_NAME:-}"

INSTALL_DIR="${INSTALL_DIR:-/opt/cloudhelper/probe_controller}"
SERVICE_NAME="${SERVICE_NAME:-probe_controller}"
SERVICE_USER="${SERVICE_USER:-cloudhelper}"
SERVICE_GROUP="${SERVICE_GROUP:-cloudhelper}"

SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"
ENV_FILE="/etc/default/${SERVICE_NAME}"
BIN_PATH="${INSTALL_DIR}/probe_controller"
DATA_DIR="${INSTALL_DIR}/data"

INSTALLED_RELEASE_TAG=""
INSTALLED_ASSET_NAME=""

log() {
  echo "[cloudhelper-install] $*"
}

die() {
  echo "[cloudhelper-install][ERROR] $*" >&2
  exit 1
}

require_root() {
  if [[ "${EUID}" -ne 0 ]]; then
    die "please run as root (use sudo)"
  fi
}

install_packages() {
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

ensure_systemd() {
  command -v systemctl >/dev/null 2>&1 || die "systemctl not found, this script requires systemd"
}

ensure_dependencies() {
  ensure_cmd curl curl
  ensure_cmd jq jq
  ensure_cmd tar tar
}

ensure_service_account() {
  if ! getent group "${SERVICE_GROUP}" >/dev/null 2>&1; then
    log "creating group ${SERVICE_GROUP}"
    groupadd --system "${SERVICE_GROUP}"
  fi

  if ! id -u "${SERVICE_USER}" >/dev/null 2>&1; then
    log "creating user ${SERVICE_USER}"
    useradd \
      --system \
      --gid "${SERVICE_GROUP}" \
      --home-dir "${INSTALL_DIR}" \
      --create-home \
      --shell /usr/sbin/nologin \
      "${SERVICE_USER}"
  fi
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

pick_asset_url() {
  local release_json="$1"
  local url=""
  local controller_pattern='probe[-_]?controller'
  local known_arch_pattern='amd64|x86_64|arm64|aarch64|armv7|armv7l|386|i386|x86|ppc64le|s390x|riscv64'

  if [[ -n "${ASSET_NAME}" ]]; then
    url="$(echo "${release_json}" | jq -r --arg name "${ASSET_NAME}" '.assets[] | select(.name==$name) | .browser_download_url' | head -n1)"
  else
    url="$(echo "${release_json}" | jq -r --arg os "${TARGET_OS}" --arg arch "${TARGET_ARCH_PATTERN}" --arg p "${controller_pattern}" '
      .assets[]
      | select(.name | test($p; "i"))
      | select(.name | test($os; "i"))
      | select(.name | test($arch; "i"))
      | .browser_download_url
    ' | head -n1)"

    if [[ -z "${url}" ]]; then
      url="$(echo "${release_json}" | jq -r --arg os "${TARGET_OS}" --arg p "${controller_pattern}" --arg known_arch "${known_arch_pattern}" '
        .assets[]
        | select(.name | test($p; "i"))
        | select(.name | test($os; "i"))
        | select((.name | test($known_arch; "i")) | not)
        | .browser_download_url
      ' | head -n1)"
    fi

    if [[ -z "${url}" ]]; then
      url="$(echo "${release_json}" | jq -r --arg p "${controller_pattern}" --arg known_arch "${known_arch_pattern}" '
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
      -name 'probe_controller' -o -name 'probe_controller_*' -o \
      -name 'probe-controller' -o -name 'probe-controller-*' -o \
      -name '*probe_controller*' -o -name '*probe-controller*' \
    \) | grep -E -v '\.exe$' | head -n1 || true)"

  if [[ -z "${binary_candidate}" ]]; then
    local lower_asset_name="${asset_name,,}"
    if [[ "${lower_asset_name}" == *probe_controller* ]] || [[ "${lower_asset_name}" == *probe-controller* ]]; then
      if [[ "${asset_name}" != *.exe ]]; then
        binary_candidate="${extract_dir}/${asset_name}"
      fi
    fi
  fi

  [[ -n "${binary_candidate}" ]] || die "failed to locate linux probe_controller binary inside asset ${asset_name}"
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
    die "failed to find a matching probe_controller release asset for ${TARGET_OS}/${TARGET_ARCH}; set ASSET_NAME to override"
  }

  asset_name="$(echo "${release_json}" | jq -r --arg url "${asset_url}" '.assets[] | select(.browser_download_url==$url) | .name' | head -n1)"
  [[ -n "${asset_name}" ]] || die "failed to resolve asset name from selected download url"

  tmp_dir="$(mktemp -d)"
  trap "rm -rf -- \"${tmp_dir}\"" EXIT

  asset_file="${tmp_dir}/${asset_name}"
  log "downloading ${asset_name} from ${tag_name}"
  github_download "${asset_url}" "${asset_file}"

  binary_src="$(extract_binary_from_asset "${asset_file}" "${asset_name}" "${tmp_dir}")"

  mkdir -p "${INSTALL_DIR}" "${DATA_DIR}"
  if [[ -f "${BIN_PATH}" ]]; then
    local backup="${BIN_PATH}.bak.$(date +%Y%m%d%H%M%S)"
    log "backing up existing binary to ${backup}"
    cp -f "${BIN_PATH}" "${backup}"
  fi

  install -m 0755 "${binary_src}" "${BIN_PATH}"
  chown -R "${SERVICE_USER}:${SERVICE_GROUP}" "${INSTALL_DIR}"
  chmod 0750 "${INSTALL_DIR}" "${DATA_DIR}"

  INSTALLED_RELEASE_TAG="${tag_name}"
  INSTALLED_ASSET_NAME="${asset_name}"
}

write_env_file() {
  if [[ ! -f "${ENV_FILE}" ]]; then
    log "creating ${ENV_FILE}"
    cat >"${ENV_FILE}" <<EOF
# Optional runtime overrides for probe_controller service.
# Admin authentication now uses signature verification with data/admin_public_key.pem.
EOF
    chmod 0640 "${ENV_FILE}"
  fi
}

write_service_file() {
  log "writing ${SERVICE_FILE}"
  cat >"${SERVICE_FILE}" <<EOF
[Unit]
Description=CloudHelper Probe Controller
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
ReadWritePaths=${DATA_DIR}

[Install]
WantedBy=multi-user.target
EOF
}

enable_and_start_service() {
  log "reloading systemd"
  systemctl daemon-reload
  log "enabling ${SERVICE_NAME}"
  systemctl enable "${SERVICE_NAME}" >/dev/null
  log "restarting ${SERVICE_NAME}"
  systemctl restart "${SERVICE_NAME}"
}

show_summary() {
  log "installed successfully"
  log "service: ${SERVICE_NAME}"
  log "binary:  ${BIN_PATH}"
  log "data:    ${DATA_DIR}"
  log "release: ${INSTALLED_RELEASE_TAG}"
  log "asset:   ${INSTALLED_ASSET_NAME}"
  log "next steps:"
  log "  1) systemctl status ${SERVICE_NAME} --no-pager"
  log "  2) check first-start key: ${DATA_DIR}/initial_admin_private_key.pem"
  log "  3) place reverse proxy in front and pass X-Forwarded-Proto=https"
}

main() {
  require_root
  ensure_dependencies
  ensure_systemd
  resolve_platform
  ensure_service_account
  download_and_install
  write_env_file
  write_service_file
  enable_and_start_service
  show_summary
}

main "$@"

