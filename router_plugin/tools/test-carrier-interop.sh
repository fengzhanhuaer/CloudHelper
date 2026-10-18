#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")/.."
fixture="$(pwd)/artifacts/c-interop.test"
test -x "$fixture"
CHVR_CARRIER_TEST="$(pwd)/bin/native/test-carrier" "$fixture" \
    -test.run '^TestRouterCCarrierWithExistingProbe$' -test.v -test.timeout 180s
wrapper=$(mktemp /tmp/chvr-carrier.XXXXXX)
runtime=$(mktemp -d /usr/local/chvr-interop.XXXXXX)
trap 'rm -f "$wrapper" "$runtime/test-carrier"; rmdir "$runtime"' EXIT
# Vendor SSL intentionally exits when its executable is outside /usr; use the
# same RAM runtime path as the installed plugin, not the source bind mount.
cp bin/arm/test-carrier "$runtime/test-carrier"
for profile in ax56uv2 ax56uv2-koolshare be86u be86u-koolcenter;do
    printf '#!/bin/sh\nexec qemu-arm -L "%s/rootfs/%s" -E LD_LIBRARY_PATH=/usr/lib:/lib "%s/test-carrier" "$@"\n' \
        "${CHVR_CI_SDK:?}" "$profile" "$runtime" > "$wrapper"
    chmod 700 "$wrapper"
    CHVR_CARRIER_TEST="$wrapper" "$fixture" \
        -test.run '^TestRouterCCarrierWithExistingProbe$' -test.v -test.timeout 240s
done
