#!/bin/bash
set -euo pipefail
sdk=/opt/toolchains/crosstools-arm-gcc-5.5-linux-4.1-glibc-2.26-binutils-2.28.1
export LD_LIBRARY_PATH="$sdk/lib${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
compiler=/opt/toolchains/crosstools-arm-gcc-5.5-linux-4.1-glibc-2.26-binutils-2.28.1/usr/bin/arm-buildroot-linux-gnueabi-gcc
mkdir -p /workspace/artifacts
cat > /workspace/artifacts/toolchain-smoke.c <<'SOURCE'
#include <stdio.h>
int main(void) {
    puts("Asuswrt ARMv7 toolchain smoke test");
    return 0;
}
SOURCE
"$compiler" -march=armv7-a -marm -static \
    /workspace/artifacts/toolchain-smoke.c -o /workspace/artifacts/toolchain-smoke-armv7
file /workspace/artifacts/toolchain-smoke-armv7
readelf -h /workspace/artifacts/toolchain-smoke-armv7
