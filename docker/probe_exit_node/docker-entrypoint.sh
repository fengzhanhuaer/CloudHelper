#!/bin/sh
set -eu

install_dir="${INSTALL_DIR:-/opt/cloudhelper/probe_exit_node}"
program="${PROBE_EXIT_NODE_BIN:-${install_dir}/probe_exit_node}"
mihomo="${PROBE_MIHOMO_BINARY:-${install_dir}/data/mihomo}"
repo="${RELEASE_REPO:-fengzhanhuaer/CloudHelper}"
tag="${RELEASE_TAG:-latest}"
mihomo_version="v1.19.29"
mihomo_asset="mihomo-linux-amd64-compatible-v1.19.29.gz"
mihomo_sha256="5612e698e96c8b8ad15abc4c0a4f098eba9234354b4f248cb97f2528e215b094"

log() { echo "[cloudhelper-probe-exit-node-docker] $*"; }
die() { echo "[cloudhelper-probe-exit-node-docker][ERROR] $*" >&2; exit 1; }
[ "$(uname -m)" = "x86_64" ] || die "Linux x86_64 is required"
[ -n "${PROBE_NODE_ID:-}" ] || die "PROBE_NODE_ID is required"
[ -n "${PROBE_NODE_SECRET:-}" ] || die "PROBE_NODE_SECRET is required"
case "${PROBE_CONTROLLER_URL:-}" in https://*) ;; *) die "PROBE_CONTROLLER_URL must use HTTPS" ;; esac
mkdir -p "$(dirname "${program}")" "$(dirname "${mihomo}")" "${install_dir}/log" "${install_dir}/temp"

transaction_started=0
had_program=0
had_mihomo=0
rollback_bootstrap() {
  log "bootstrap failed; restoring the previous probe/Mihomo pair"
  if [ "${had_program}" -eq 1 ]; then
    mv -f "${program}.bootstrap.bak" "${program}"
  else
    rm -f "${program}"
  fi
  if [ "${had_mihomo}" -eq 1 ]; then
    mv -f "${mihomo}.bootstrap.bak" "${mihomo}"
  else
    rm -f "${mihomo}"
  fi
}
finish_bootstrap() {
  rc=$?
  trap - EXIT INT TERM
  if [ "${rc}" -ne 0 ] && [ "${transaction_started}" -eq 1 ]; then
    rollback_bootstrap
  fi
  exit "${rc}"
}
trap finish_bootstrap EXIT INT TERM

if [ ! -x "${program}" ] || [ ! -x "${mihomo}" ]; then
  if [ "${tag}" = "latest" ]; then
    program_url="https://github.com/${repo}/releases/latest/download/cloudhelper-probe-exit-node-linux-amd64"
    manifest_url="https://github.com/${repo}/releases/latest/download/cloudhelper-probe-exit-node-manifest.json"
  else
    program_url="https://github.com/${repo}/releases/download/${tag}/cloudhelper-probe-exit-node-linux-amd64"
    manifest_url="https://github.com/${repo}/releases/download/${tag}/cloudhelper-probe-exit-node-manifest.json"
  fi
  curl -fL --retry 5 --connect-timeout 15 "${manifest_url}" -o "${install_dir}/temp/manifest.json"
  jq -e --arg version "${mihomo_version}" --arg asset "${mihomo_asset}" --arg sha "${mihomo_sha256}" '
    .schema_version == 1 and .build_kind == "mihomo_exit" and .os == "linux" and .arch == "amd64"
    and (.version | test("^v[0-9]+\\.[0-9]+\\.[0-9]+$"))
    and .compatible_program_versions.min == .version and .compatible_program_versions.max == .version
    and .program.asset == "cloudhelper-probe-exit-node-linux-amd64"
    and (.program.sha256 | test("^[0-9a-f]{64}$"))
    and .mihomo.version == $version and .mihomo.asset == $asset and .mihomo.sha256 == $sha
    and (.mihomo.url | startswith("https://github.com/MetaCubeX/mihomo/releases/download/"))
  ' "${install_dir}/temp/manifest.json" >/dev/null || die "paired upgrade manifest is invalid"
  program_sha256="$(jq -r '.program.sha256' "${install_dir}/temp/manifest.json")"
  mihomo_url="$(jq -r '.mihomo.url' "${install_dir}/temp/manifest.json")"
  log "bootstrapping paired probe_exit_node and Mihomo"
  curl -fL --retry 5 --connect-timeout 15 "${program_url}" -o "${install_dir}/temp/probe_exit_node.new"
  echo "${program_sha256}  ${install_dir}/temp/probe_exit_node.new" | sha256sum -c -
  chmod 0755 "${install_dir}/temp/probe_exit_node.new"
  "${install_dir}/temp/probe_exit_node.new" --upgrade-verify --upgrade-verify-duration=5 --upgrade-verify-build-kind=mihomo_exit
  curl -fL --retry 5 --connect-timeout 15 "${mihomo_url}" -o "${install_dir}/temp/${mihomo_asset}"
  echo "${mihomo_sha256}  ${install_dir}/temp/${mihomo_asset}" | sha256sum -c -
  gzip -dc "${install_dir}/temp/${mihomo_asset}" > "${install_dir}/temp/mihomo.new"
  chmod 0755 "${install_dir}/temp/mihomo.new"
  "${install_dir}/temp/mihomo.new" -v
  if [ -e "${program}" ]; then
    had_program=1
    cp -p "${program}" "${program}.bootstrap.bak"
  fi
  if [ -e "${mihomo}" ]; then
    had_mihomo=1
    cp -p "${mihomo}" "${mihomo}.bootstrap.bak"
  fi
  transaction_started=1
  mv "${install_dir}/temp/probe_exit_node.new" "${program}"
  mv "${install_dir}/temp/mihomo.new" "${mihomo}"
  transaction_started=0
  rm -f "${program}.bootstrap.bak" "${mihomo}.bootstrap.bak"
fi

cd "${install_dir}"
trap - EXIT INT TERM
exec "${program}" "$@"
