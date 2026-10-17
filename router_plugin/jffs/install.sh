#!/bin/sh
set -eu
umask 077
base=/jffs/cloudhelper-router
bundle=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
test "$(awk '$1=="Uid:" {print $2}' /proc/self/status)" = 0 || { echo 'Root is required' >&2;exit 1; }
case "$(cat "$bundle/device-profile"):$(nvram get productid):$(nvram get odmpid):$(uname -m)" in
 ax56uv2:RT-AX55:RT-AX56U_V2:armv7l) filesystem=jffs2;;
 be86u:RT-BE86U::aarch64)
    filesystem=ubifs
    stock_httpd=/usr/sbin/httpd
    if awk '$2=="/usr/sbin/httpd" {ok=1} END {exit !ok}' /proc/mounts &&
       grep -q CHVR_HTTPD_WRAPPER /usr/sbin/httpd;then
        stock_httpd=/usr/local/cloudhelper-router/httpd
    fi
    # The service checks the actual handler ABI, independent of firmware version.
    ;;
 *) echo 'Unsupported router model or userland profile' >&2;exit 1;;
esac
awk -v fs="$filesystem" '$2=="/jffs" && $3==fs {ok=1} END {exit !ok}' /proc/mounts || exit 1
dbus_bin=/usr/bin/dbus
if ! test -x "$dbus_bin";then dbus_bin=/koolshare/bin/dbus;fi
softcenter_register() {
    test -d /koolshare/webs && test -d /koolshare/scripts && test -x "$dbus_bin" || return 0
    cp "$bundle/webs/Module_cloudhelper.asp" /koolshare/webs/Module_cloudhelper.asp
    cp "$bundle/scripts/uninstall_cloudhelper.sh" /koolshare/scripts/uninstall_cloudhelper.sh
    chmod 700 /koolshare/scripts/uninstall_cloudhelper.sh
    cp "$bundle/install.sh" "$base/installer.sh.new"
    chmod 700 "$base/installer.sh.new"
    mv "$base/installer.sh.new" "$base/installer.sh"
    version=$("$base/releases/$(cat "$base/active")/cloudhelper-router-plugin" --version | awk '{print $2}')
    "$dbus_bin" set softcenter_module_cloudhelper_name=cloudhelper
    "$dbus_bin" set 'softcenter_module_cloudhelper_title=CloudHelper 路由器插件'
    "$dbus_bin" set 'softcenter_module_cloudhelper_description=虚拟路由、DNS 和远程网关代理'
    "$dbus_bin" set "softcenter_module_cloudhelper_version=$version"
    "$dbus_bin" set softcenter_module_cloudhelper_install=1
}
case "${1:-}" in
 --uninstall)
    if test -x "$base/service";then
        if "$base/service" boot-status >/dev/null 2>&1;then "$base/service" boot-remove;fi
        "$base/service" stop
    fi
    for path in "$base"/releases/*;do
        test -d "$path" && ! test -L "$path" || continue
        id=${path##*/}
        case "$id" in ''|*[!a-f0-9]*) continue;; esac
        test "${#id}" -eq 16 || continue
        rm -f "$path/cloudhelper-router-plugin" "$path/service" "$path/web/index.html" "$path/web/app.js" "$path/httpd-bridge.so" "$path/web/VirtualRouter.asp"
        rmdir "$path/web" "$path" 2>/dev/null || true
    done
    rm -f "$base/service" "$base/active" "$base/previous"
    if test -f /koolshare/webs/Module_cloudhelper.asp && grep -q CHVR_SOFTCENTER_ENTRY /koolshare/webs/Module_cloudhelper.asp;then rm -f /koolshare/webs/Module_cloudhelper.asp;fi
    if test -f /koolshare/scripts/uninstall_cloudhelper.sh && grep -q CHVR_SOFTCENTER_UNINSTALL /koolshare/scripts/uninstall_cloudhelper.sh;then rm -f /koolshare/scripts/uninstall_cloudhelper.sh;fi
    if test -x "$dbus_bin";then
        for key in name title description version install;do "$dbus_bin" remove "softcenter_module_cloudhelper_$key";done
    fi
    rm -f "$base/installer.sh"
    rmdir "$base/releases" 2>/dev/null || true
    echo 'Uninstalled. Configuration, management key and certificate retained in JFFS.'
    exit
    ;;
 --rollback)
    test -f "$base/previous" || { echo 'No previous release' >&2;exit 1; }
    old=$(cat "$base/previous")
    case "$old" in ''|*[!a-f0-9]*) exit 1;; esac
    test -x "$base/releases/$old/service" || exit 1
    test -f "$base/releases/$old/httpd-bridge.so" || { echo 'Previous release uses obsolete standalone management' >&2;exit 1; }
    "$base/service" stop
    current=$(cat "$base/active")
    printf '%s\n' "$old" > "$base/active.new"
    mv "$base/active.new" "$base/active"
    printf '%s\n' "$current" > "$base/previous"
    "$base/service" start
    exit
    ;;
 '') ;;
 *) echo 'usage: install.sh [--rollback|--uninstall]' >&2;exit 2;;
esac
cd "$bundle"
while read -r expected file;do
    case "$file" in payload/cloudhelper-router-plugin|payload/service|payload/httpd-bridge.so|payload/web/VirtualRouter.asp) ;;
      *) echo 'Invalid manifest path' >&2;exit 1;; esac
    actual=$(openssl dgst -sha256 "$file" | awk '{print $NF}')
    test "$actual" = "$expected" || { echo "Checksum mismatch: $file" >&2;exit 1; }
done < checksums
test "$(wc -l < checksums)" -eq 4 || exit 1
# Check the real stock loader and all eagerly-bound runtime symbols before copying.
./payload/cloudhelper-router-plugin --version
id=$(cat release-id)
case "$id" in ''|*[!a-f0-9]*) exit 1;; esac
test "${#id}" -eq 16 || exit 1
free=$(df -k /jffs | awk 'END {print $4}')
test "$free" -ge 2048 || { echo 'Keep at least 2 MiB free for install/rollback' >&2;exit 1; }
mkdir -p "$base/releases"
chmod 700 "$base" "$base/releases"
cp "$bundle/device-profile" "$base/device-profile.new"
mv "$base/device-profile.new" "$base/device-profile"
if test -f "$base/active";then
    active=$(cat "$base/active")
    case "$active" in ''|*[!a-f0-9]*) exit 1;; esac
    if test "$active" = "$id";then "$base/service" start;"$base/service" boot-install;softcenter_register;exit;fi
    "$base/service" stop
    printf '%s\n' "$active" > "$base/previous"
fi
test ! -e "$base/releases/$id" || { echo 'Release path already exists; verify it before reuse' >&2;exit 1; }
mkdir "$base/releases/$id"
cp -R payload/. "$base/releases/$id/"
chmod 700 "$base/releases/$id/cloudhelper-router-plugin" "$base/releases/$id/service"
cat > "$base/service.new" <<'LAUNCHER'
#!/bin/sh
set -eu
base=/jffs/cloudhelper-router
active=$(cat "$base/active")
case "$active" in ''|*[!a-f0-9]*) exit 1;; esac
exec "$base/releases/$active/service" "$@"
LAUNCHER
chmod 700 "$base/service.new"
mv "$base/service.new" "$base/service"
printf '%s\n' "$id" > "$base/active.new"
mv "$base/active.new" "$base/active"
if ! "$base/service" start;then
    "$base/service" stop || exit 1
    previous=$(cat "$base/previous" 2>/dev/null || true)
    if test -n "$previous" && test -f "$base/releases/$previous/httpd-bridge.so";then
        cp "$base/previous" "$base/active.new"
        mv "$base/active.new" "$base/active"
        "$base/service" start
        echo 'New release failed; restored previous release' >&2
    else
        echo 'Installation retained for diagnosis' >&2
    fi
    exit 1
fi
"$base/service" boot-install
softcenter_register
# Retain only the active and previous releases, removing only known payload files.
previous=$(cat "$base/previous" 2>/dev/null || true)
for path in "$base"/releases/*;do
    test -d "$path" && ! test -L "$path" || continue
    old=${path##*/}
    case "$old" in ''|*[!a-f0-9]*) continue;; esac
    test "${#old}" -eq 16 || continue
    if test "$old" = "$id" || test "$old" = "$previous";then continue;fi
    rm -f "$path/cloudhelper-router-plugin" "$path/service" "$path/web/index.html" "$path/web/app.js" "$path/httpd-bridge.so" "$path/web/VirtualRouter.asp"
    rmdir "$path/web" "$path" 2>/dev/null || true
done
echo 'Installed. Sign in to the original ASUS web UI and open Virtual Router below Network Tools.'
if test "$("$base/service" boot-status)" = 'koolcenter enabled';then
    echo 'KoolCenter startup hooks registered; plugin starts automatically after reboot.'
elif test -x /rom/etc/cloudhelper-jffs-start;then
    echo 'Minimal firmware startup hook detected; plugin starts automatically after reboot.'
else
    echo 'No verified stock boot hook: after reboot run /jffs/cloudhelper-router/service start via SSH.'
fi
