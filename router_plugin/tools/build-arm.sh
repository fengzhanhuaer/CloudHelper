#!/bin/bash
set -euo pipefail
module_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
task_root="${CHVR_VENDOR_ROOT:-$HOME/cloudhelper-firmware-official-53297}"
test -d "$task_root/opt-toolchains"
test -d "$task_root/source/asuswrt/release/src/router"
build_version="${CHVR_VERSION:-v$(tr -d '\r\n' < "$module_root/../version")-dev}"
[[ "$build_version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.+-][a-zA-Z0-9.+-]+)?$ ]] || { echo 'Invalid plugin version' >&2;exit 1; }
docker run --rm --user 0 --cpus 2 --memory 2g \
    --env "CHVR_VERSION=$build_version" \
    --tmpfs /usr/local:rw,exec,nodev,nosuid,size=2m,mode=1777 \
    --mount "type=bind,src=$module_root,dst=/module" \
    --mount "type=bind,src=$task_root,dst=/workspace,readonly" \
    --mount "type=bind,src=$task_root/opt-toolchains,dst=/opt/toolchains,readonly" \
    --workdir /module cloudhelper-router-c-test:ubuntu18 bash -c '
set -euo pipefail
sdk=/opt/toolchains/crosstools-arm-gcc-5.5-linux-4.1-glibc-2.26-binutils-2.28.1
router=/workspace/source/asuswrt/release/src/router
rootfs=/workspace/source/asuswrt/release/src-rt-5.02axhnd.675x/targets/947622GW/fs
export LD_LIBRARY_PATH="$sdk/lib"
make BUILD=bin/arm VERSION="$CHVR_VERSION" CC="$sdk/usr/bin/arm-buildroot-linux-gnueabi-gcc" \
    CFLAGS="-Os -g -std=c99 -Wall -Wextra -Werror -Wformat=2 -fstack-protector-strong -march=armv7-a -mfloat-abi=soft" \
    DEP_CPPFLAGS="-I$router -I$router/openssl-1.1/include -I$router/curl/include" \
    DEP_LDFLAGS="-L$router/openssl-1.1 -L$router/json-c/.libs -L$router/curl/lib/.libs -Wl,-rpath-link,$rootfs/usr/lib:$rootfs/lib" \
    all bin/arm/test-core bin/arm/httpd-fixture
"$sdk/usr/bin/arm-buildroot-linux-gnueabi-readelf" -h -d bin/arm/test-core
unset LD_LIBRARY_PATH
qemu-arm -L "$rootfs" -E LD_LIBRARY_PATH=/usr/lib:/lib bin/arm/test-core
qemu-arm -L "$rootfs" -E LD_LIBRARY_PATH=/usr/lib:/lib bin/arm/cloudhelper-router-plugin --version
"$sdk/usr/bin/arm-buildroot-linux-gnueabi-strip" bin/arm/cloudhelper-router-plugin bin/arm/httpd-bridge.so
python3 tools/check-elf-deps.py bin/arm/cloudhelper-router-plugin "$rootfs" --readelf "$sdk/usr/bin/arm-buildroot-linux-gnueabi-readelf"
stat -c "%n %s bytes" bin/arm/cloudhelper-router-plugin
if test -d /workspace/inspection/stock-52332; then
    python3 tools/check-stock-runtime.py /workspace/inspection/stock-52332 --readelf "$sdk/usr/bin/arm-buildroot-linux-gnueabi-readelf"
fi
'
