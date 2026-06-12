#!/bin/sh
set -eu

log() {
  echo "[cloudhelper-probe-node-docker] $*"
}

die() {
  echo "[cloudhelper-probe-node-docker][ERROR] $*" >&2
  exit 1
}

install_dir="${INSTALL_DIR:-/opt/cloudhelper/probe_node}"
bin_path="${PROBE_NODE_BIN:-${install_dir}/probe_node}"
repo="${RELEASE_REPO:-fengzhanhuaer/CloudHelper}"
tag="${RELEASE_TAG:-latest}"
asset="${ASSET_NAME:-cloudhelper-probe-node-linux-amd64}"
auto_install="${PROBE_NODE_AUTO_INSTALL:-true}"
force_install="${PROBE_NODE_FORCE_INSTALL:-false}"
download_url="${PROBE_NODE_DOWNLOAD_URL:-}"
controller_url="${PROBE_CONTROLLER_URL:-}"
proxy_base_url="${PROBE_PROXY_BASE_URL:-}"
node_id="${PROBE_NODE_ID:-}"
node_secret="${PROBE_NODE_SECRET:-}"
install_command="${PROBE_NODE_INSTALL_COMMAND:-}"

extract_install_var() {
  var_name="$1"
  command_text="$2"
  value="$(printf '%s\n' "${command_text}" | sed -n "s/.*${var_name}='\([^']*\)'.*/\1/p" | tail -n 1)"
  if [ -n "${value}" ]; then
    printf '%s\n' "${value}"
    return
  fi
  value="$(printf '%s\n' "${command_text}" | sed -n "s/.*${var_name}=\"\([^\"]*\)\".*/\1/p" | tail -n 1)"
  if [ -n "${value}" ]; then
    printf '%s\n' "${value}"
    return
  fi
  printf '%s\n' "${command_text}" | sed -n "s/.*${var_name}=\([^ ;]*\).*/\1/p" | tail -n 1
}

derive_controller_from_script_url() {
  script_url="$1"
  case "${script_url}" in
    */api/probe/proxy/probe-node/install-script*)
      printf '%s\n' "${script_url%%/api/probe/proxy/probe-node/install-script*}"
      ;;
  esac
}

if [ -n "${install_command}" ]; then
  parsed_node_id="$(extract_install_var "PROBE_NODE_ID" "${install_command}")"
  parsed_node_secret="$(extract_install_var "PROBE_NODE_SECRET" "${install_command}")"
  parsed_controller_url="$(extract_install_var "PROBE_CONTROLLER_URL" "${install_command}")"
  parsed_proxy_base_url="$(extract_install_var "PROBE_PROXY_BASE_URL" "${install_command}")"
  parsed_script_url="$(extract_install_var "SCRIPT_URL" "${install_command}")"

  [ -n "${node_id}" ] || node_id="${parsed_node_id}"
  [ -n "${node_secret}" ] || node_secret="${parsed_node_secret}"
  [ -n "${controller_url}" ] || controller_url="${parsed_controller_url}"
  [ -n "${proxy_base_url}" ] || proxy_base_url="${parsed_proxy_base_url}"
  if [ -z "${controller_url}" ] && [ -n "${parsed_script_url}" ]; then
    controller_url="$(derive_controller_from_script_url "${parsed_script_url}")"
  fi
  if [ -z "${proxy_base_url}" ] && [ -n "${controller_url}" ]; then
    proxy_base_url="${controller_url%/}/api/probe/proxy"
  fi
fi

[ -z "${node_id}" ] || export PROBE_NODE_ID="${node_id}"
[ -z "${node_secret}" ] || export PROBE_NODE_SECRET="${node_secret}"
[ -z "${controller_url}" ] || export PROBE_CONTROLLER_URL="${controller_url}"
[ -z "${proxy_base_url}" ] || export PROBE_PROXY_BASE_URL="${proxy_base_url}"

case "${download_url}" in
  "")
    if [ "${tag}" = "latest" ]; then
      download_url="https://github.com/${repo}/releases/latest/download/${asset}"
    else
      download_url="https://github.com/${repo}/releases/download/${tag}/${asset}"
    fi
    ;;
esac

build_controller_proxy_url() {
	if [ -z "${proxy_base_url}" ] && [ -n "${controller_url}" ]; then
		proxy_base_url="${controller_url%/}/api/probe/proxy"
	fi
  [ -n "${proxy_base_url}" ] || return 1
  [ -n "${node_id}" ] || return 1
  [ -n "${node_secret}" ] || return 1

	printf '%s/download?node_id=%s&secret=%s\n' "${proxy_base_url%/}" "${node_id}" "${node_secret}"
}

mkdir -p "${install_dir}" "${install_dir}/data" "${install_dir}/logs" "${install_dir}/temp"

need_install=false
if [ ! -x "${bin_path}" ]; then
  need_install=true
fi
case "${force_install}" in
  1|true|TRUE|yes|YES|on|ON)
    need_install=true
    ;;
esac

if [ "${need_install}" = "true" ]; then
  case "${auto_install}" in
    1|true|TRUE|yes|YES|on|ON)
      ;;
    *)
      die "probe binary not found or not executable: ${bin_path}"
      ;;
  esac

  tmp_path="${install_dir}/.probe_node.download.$$"
  rm -f "${tmp_path}"
  proxy_url=""
  if proxy_url="$(build_controller_proxy_url)"; then
    log "installing probe binary via controller proxy"
    if ! wget -q --header "X-CloudHelper-Download-URL: ${download_url}" -O "${tmp_path}" "${proxy_url}"; then
      log "controller proxy download failed, falling back to upstream release"
      rm -f "${tmp_path}"
    fi
  fi
  if [ ! -s "${tmp_path}" ]; then
    log "installing probe binary from upstream release: ${download_url}"
    if ! wget -q -O "${tmp_path}" "${download_url}"; then
      rm -f "${tmp_path}"
      die "failed to download probe binary"
    fi
  fi
  chmod 0755 "${tmp_path}"
  mv "${tmp_path}" "${bin_path}"
  log "probe binary ready: ${bin_path}"
fi

cd "${install_dir}"
exec "${bin_path}" "$@"
