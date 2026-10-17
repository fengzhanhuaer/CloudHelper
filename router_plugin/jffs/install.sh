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
    case "$(openssl dgst -sha256 "$stock_httpd" | awk '{print $NF}')" in
     8804d44c2633046d7740d356e7bdcd5e46b1d2b3ac9b1e967dda01e99150823e|1b7148dabccb0954b13b8bf70f853afbcd133160cf9f4cb201789bccdb5143e8) ;;
     *)
        echo 'Unsupported RT-BE86U firmware; original web server retained' >&2;exit 1;
        ;;
    esac;;
 *) echo 'Unsupported router model or userland profile' >&2;exit 1;;
esac
awk -v fs="$filesystem" '$2=="/jffs" && $3==fs {ok=1} END {exit !ok}' /proc/mounts || exit 1
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
if test -f "$base/active";then
    active=$(cat "$base/active")
    case "$active" in ''|*[!a-f0-9]*) exit 1;; esac
    if test "$active" = "$id";then "$base/service" start;"$base/service" boot-install;exit;fi
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
