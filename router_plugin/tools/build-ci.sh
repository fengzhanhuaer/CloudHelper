#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")/.."
sdk_root="${CHVR_CI_SDK:?Set CHVR_CI_SDK to the prepared public SDK}"
build_version="${CHVR_VERSION:?Set CHVR_VERSION to the release tag}"
[[ "$build_version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.+-][a-zA-Z0-9.+-]+)?$ ]]
sdk="$sdk_root/toolchain"
rootfs="$sdk_root/rootfs/ax56uv2"
make VERSION="$build_version" all test bin/native/test-upgrade bin/native/test-httpd-profile bin/native/httpd-fixture bin/native/test-carrier bin/native/test-measure
bin/native/test-measure
bin/native/test-httpd-profile
bin/native/cloudhelper-router-plugin --check-httpd bin/native/httpd-fixture
python3 tests/test_upgrade.py
python3 tests/test_service.py bin/native/cloudhelper-router-plugin
python3 tests/test_httpd_bridge.py
export LD_LIBRARY_PATH="$sdk/lib"
mkdir -p "$sdk_root/link"
for spec in 'curl libcurl.so.4' 'ssl libssl.so.1.1' 'crypto libcrypto.so.1.1' 'json-c libjson-c.so.2';do
    read -r name library <<< "$spec"
    target=$(find "$rootfs/usr/lib" "$rootfs/lib" -name "$library" -print -quit)
    test -n "$target"
    ln -sfn "$target" "$sdk_root/link/lib$name.so"
done
make BUILD=bin/arm VERSION="$build_version" CC="$sdk/bin/arm-buildroot-linux-gnueabi-gcc" \
    CFLAGS='-Os -g -std=c99 -Wall -Wextra -Werror -Wformat=2 -fstack-protector-strong -march=armv7-a -mfloat-abi=soft' \
    DEP_CPPFLAGS="-I$sdk_root/include -I$sdk_root/openssl/include" \
    DEP_LDFLAGS="-L$sdk_root/link -Wl,-rpath-link,$rootfs/usr/lib:$rootfs/lib" \
    all bin/arm/test-core bin/arm/httpd-fixture bin/arm/test-carrier
"$sdk/bin/arm-buildroot-linux-gnueabi-strip" bin/arm/cloudhelper-router-plugin bin/arm/httpd-bridge.so
unset LD_LIBRARY_PATH
for profile in ax56uv2 ax56uv2-koolshare be86u be86u-koolcenter;do
    python3 tools/check-stock-runtime.py "$sdk_root/rootfs/$profile" --profile "$profile" \
        --baseline "ci-inputs.lock.json:$profile" --readelf "$sdk/bin/arm-buildroot-linux-gnueabi-readelf"
done
for profile in ax56uv2 be86u;do python3 tools/package-jffs.py --profile "$profile";done
