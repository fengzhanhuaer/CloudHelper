# 华硕固件定制构建

当前采用官方 ASUS GPL 53297 加最小 JFFS 启动补丁。业务仍由 [`router_plugin`](../../router_plugin/README.md) 的 C 插件提供；系统仅增加异步启动入口与只读 HTTPD 哈希声明，不编入业务服务、页面或配置。历史的完整业务集成补丁不是当前构建入口。

原厂源码、SDK、镜像、备份、串口日志和私有凭据均留在本机忽略目录；Git 只保存自有源码、测试、来源锁及说明。签名密钥和密码不得放进该目录的受控源码。

## 最小自启固件

在已准备的官方基线环境执行（WSL 侧需 `git`、`python3`、`qemu-arm`、`readelf`、`unsquashfs` 和 Docker）：

```powershell
wsl -d BuildServer -u maple -- bash /mnt/d/Code/CloudHelper/firmware/asuswrt/build-autostart-firmware.sh
```

`prepare-autostart.py` 校验锁定的官方归档，撤销已知的旧业务集成补丁，并拒绝未知源码改动；应用 `0002-jffs-autostart.patch`。原厂服务完成后异步执行 `/rom/etc/cloudhelper-jffs-start`，仅 RT-AX55/RT-AX56U_V2 且 JFFS2 已挂载时调用 `/jffs/cloudhelper-router/service start`。插件缺失、禁用或启动失败不阻塞原厂启动。成功标记仅在 RAM；日志到控制台，不产生文件日志。创建 `/jffs/cloudhelper-router/autostart.disabled` 可禁用下次自启，删除可恢复。

须同时安装新版 JFFS 插件包，以识别只读 ROM 中声明的 HTTPD 精确哈希；未知版本继续拒绝接入。插件启停会重启 Web 进程。系统镜像离线解包验证与实机自启验证分别记录；不得将前者当作后者。

验证通过后仅将无 CFE 前缀镜像复制到被忽略的 `artifacts/autostart/`，附 SHA256 和 JSON 报告。保留 A/CFE；B ONCE 行为未解决前不得自动刷写或设置长期 B。

2026-10-16 实机验收通过：本轮无 CFE 镜像35,258,388字节，SHA256 `dcb1a7f5486bc373fe6ce4ff8b7f65aa70ba94508164255bef17feab3cf3d5dd`，配套JFFS发布 `df505ecea68822cf`。两次B启动均由串口确认TRY2/0x03400000与插件自动启动完成；运行PID1完整哈希与镜像一致，配置/密钥、Wi-Fi、LAN TCP/UDP DNS、经Wi-Fi路由器出站HTTPS、原厂认证菜单/API均通过。当前与下次均B；A全部269逻辑载荷及CFE原始哈希保留。B ONCE仍直接选A，本轮沿会话已授权普通B例外启动，不能声称ONCE/故障自动回退已验证。备份和完整报告在 `D:/Code/RouterFirmware/router-backups/20261016-jffs-autostart`，当前状态在 `baseline.lock.json` 的 `minimal_jffs_autostart_upgrade`。插件业务关闭，待用户填写主控/节点身份；开发期noconsole保持0，整体开发完成后恢复1。

## Python 串口调试

工具在 `tools/router_serial.py`，依赖 `tools/requirements-serial.txt`；已安装到本地忽略的 `artifacts/serial-venv`，同一脚本同步到用户技能 `asuswrt-router-upgrade-debug/scripts`。

```powershell
$py = 'D:/Code/CloudHelper/firmware/asuswrt/artifacts/serial-venv/Scripts/python.exe'
$uart = 'D:/Code/CloudHelper/firmware/asuswrt/tools/router_serial.py'
& $py $uart ports
& $py $uart capture --port COM4 --seconds 120
& $py $uart exec --port COM4 --ssh-bootstrap localrouter -c 'uname -a; uptime'
& $py $uart terminal --port COM4 --no-log
```

默认115200/8N1、无流控、DTR/RTS关闭。已有登录shell时exec去掉`--ssh-bootstrap`即可独立于SSH；该参数只通过SSH临时接入RAM shell，不跨重启。terminal需要交互终端，Ctrl+]本地退出。默认日志在`~/router-debug-logs`，也可用`--log-root`指定忽略目录；RX可能含设备回显，输入密码时使用`--no-log`。capture只接收；exec用随机标记核实执行与退出码，超时不重发，关闭连接不代表远端命令已停止。

内核已有`console=ttyAMA0`；官方源码已有受noconsole控制的login流程。2026-10-15按用户授权将`noconsole`从1设0并commit，新SSH会话回读0；后续完整串口捕获的一次重启中，已验证0保持、原生登录认证及独立UART查询通过。开发完成后须恢复原值1并commit。详细启用/恢复与认证边界见用户技能的`references/serial-debug.md`。

验证：`python -m unittest discover -s firmware/asuswrt/tools -p test_router_serial.py -v`，从仓库根执行；Windows跳过真实POSIX shell项，本地WSL执行全部8项。实机另验证COM4查询、非零退出码、超时清理、纯接收抓日志及交互输入/退出。

## 固件基线

2026-10-15 18:32 当前实机状态：按用户要求，已在A原厂系统下使用原生升级器，将B恢复为原始A备份的原厂52332-gfbf3b9c。当前/下次均B；运行PID1与厂商原厂52332一致，网络/后台/JFFS/配置检查通过，A全部269个逻辑载荷和CFE保留。恢复证据在`D:/Code/RouterFirmware/router-backups/20261015-restore-b-from-a`。下述53297构建/启动是历史验证记录，当前没有运行自编译53297或安装业务插件；该记录为历史恢复状态；2026-10-16 用户另授权制作仅增加 JFFS 自启入口的最小定制固件。

活动基线为华硕官方 RT-AX55 GPL `3.0.0.4.386_53297-gee411da`，目标是已核验的 BCM6755 RT-AX56U V2，构建入口 `make rt-ax55`，板级 profile `947622GW`。普通 RT-AX56U 及其他芯片批次不能套用本结论。

Git 仅保存来源锁、构建工具、我们的补丁/定制源码和任务账本。完整官方输入位于本仓库被忽略的 `upstream/official/GPL_RT-AX55_3.0.0.4.386.53297-gee411da.tgz`；Linux 工作树在本地 WSL BuildServer `/home/maple/cloudhelper-firmware-official-53297/source/asuswrt`。官方 ZIP/TGZ 的 SHA256 和实际验证状态以 `baseline.lock.json` 为准。旧 SWRT 锁/补丁/产物保留为本地历史，不是当前构建来源。

原配置基础编译/升级验证已完成。用户随后明确要求开发 CloudHelper 业务，现已进入 C 服务与固件集成；模块精简仍等待用户名单，不自行删减原厂功能。

## 输入与构建

1. 在 Windows 执行 `tools/download-baseline.ps1 -SourceZipPath <官方ZIP路径>`。它校验并导入官方源码，准备锁定的旧 HND 辅助工具输入；已有归档哈希正确时复用。
2. 在本地 BuildServer 构建或复用 `cloudhelper-asuswrt-build:ubuntu18`（`Dockerfile.baseline`，Python2、32位运行库、普通 UID1000）；不用 HomePc 构建或测试。
3. 执行 `wsl -d BuildServer -u maple -- bash /mnt/d/Code/CloudHelper/firmware/asuswrt/prepare-baseline.sh`，校验官方 TGZ，准备 Linux 原始路径与 SDK 映射。
4. 执行 `wsl -d BuildServer -u maple -- bash /mnt/d/Code/CloudHelper/firmware/asuswrt/build-baseline.sh`，限6CPU/12GiB，运行原配置 `make rt-ax55 PARALLEL_BUILD=-j6`。日志和成功后的镜像放在该任务根的 `logs/`、`artifacts/`。
5. `collect-baseline.sh` 可独立收集已生成镜像；`tools/verify-baseline.py <任务根>` 检查 WFI CRC/47622/NAND、UBI头、51MiB容量、ARM程序哈希、SquashFS和TUN模块。

主 ARM GCC5.5 与库来自官方 GPL 的无线 SDK；部分 ASUS 配方要求 `usr/bin/`，脚本在源码之外提供兼容路径并设置 SDK 的库搜索路径。缺少的 `hndtools-armeabi-2013.11` / `fwtag.ini` 辅助工具使用锁定 SDK 输入，不复用 SWRT 固件源码或其路由器 prebuild 对象。

官方 GPL 已将本目标设为 `BRCM_NAND_JFFS2=y`，原生挂载 `misc2`。旧 SWRT 的 UBIFS/JFFS2兼容补丁退出活动补丁目录；新增构建修复或定制修改保存在 `patches/*.patch`，路径相对于官方 `asuswrt/` 根。`apply-overlays.sh` 对固定归档输入应用补丁，重复应用会识别，冲突立即失败。

## 升级验证

以来源锁中的本轮结果为准；不能将此前 SWRT 两次启动证据当成官方源码构建成功。只选无额外 CFE ROM 前缀的 `*_puresqubi.w`，原生校验后从原厂 A 写 inactive B；写后回读确认 A/CFE ROM 未变，再选择 B ONCE，健康检查通过才显式提升长期 B 并再次重启。

保留现有分区、配置和SSH主机密钥；备份与过程报告仅本地忽略。健康检查须比对运行 `/proc/1/exe` 与本轮构建 `sbin/rc` 完整哈希，并验证自动 JFFS2、原密钥、Wi-Fi/Web、LAN TCP/UDP DNS、WAN及绑定路由器 Wi-Fi 的 HTTPS。ONCE 的实际消费与故障自动回退分别记录，不根据命令帮助承诺失败自动回退。

## 本轮实际结果

完整官方构建和离线校验通过，生成 BusyBox 与 dnsmasq 2.90 在 QEMU 中运行通过。系统镜像为 `RT-AX55_3.0.0.4_386_53297-gee411da_puresqubi.w`，36,044,820 字节，SHA256 `b7ec46c022c8981e9c79820a609c04d2f9b0089f2642bab2257e4f82d028ab12`，副本位于忽略的 `artifacts/official-53297/`。设备原生校验和 inactive B 写入通过；写入后 B 的全部系统载荷回读与构建镜像相同，只有两份启动元数据的序号/提交状态由平台调整。

2026-10-15 11:36：用户明确授权正式B启动后，bcm_bootstate7及一次重启已成功。串口TRY2/CFE地址0x03400000、构建内核日期和53297版本，以及运行PID1完整hash均证明官方自编译B正在运行；当前/下次均Second。JFFS自动挂载、原SSH密钥/配置、Web认证、无线/WAN、LAN TCP/UDP DNS及经路由器HTTPS200通过，A/CFE保留通过。此前B ONCE尝试未通过，其机制和故障自动回退仍未验证，不能由普通B成功推导ONCE可用。

A 重启时 UBI 将两份元数据从物理块0/1搬到300/301，因此 A 的原始分区哈希发生变化。原始备份及原预期哈希保持保留；按逻辑卷对比，包含系统和元数据的全部269个有效逻辑块内容完全一致，启动 ROM 原始哈希仍一致。保留校验同时记录物理哈希和逻辑载荷，不能将正常 UBI 搬迁表述成全分区原始字节不变，也不能用修改预期哈希掩盖系统内容变化。

本轮备份和检查报告位于 `D:/Code/RouterFirmware/router-backups/20261014-official53297`；其中配置和SSH备份包含秘密，仅本地保留。

2026-10-15 07:33启动日志复核：保留的syslog有本轮三次原厂52332启动、无官方53297启动；当前内核及分区也是A。系统没有持久崩溃日志、未采集CFE串口，因此不能排除B在日志服务启动之前曾尝试并失败。此前ONCE故障原因尚未确定，最终A与B早期未尝试是两个不同结论。审计副本位于上述备份目录的boot-log-audit-20261015。


2026-10-15 11:23补充：本次完整COM4启动日志明确直接选择A（previous image，0x00100000），仅一次TRY1/CFE/Linux，未观察到B启动后回退；此前无串口记录的试验仍不能追溯定因。B ONCE未达预期，原因未确认。原厂A/CFE保留检查通过。noconsole=0跨重启及原生串口认证/查询已实测通过，不依赖SSH bootstrap；开发结束恢复1。

本次执行bcm_bootstate5未清除独立ONCE mailbox，按官方board ioctl读取/清除后确认当前/下次均A、Acommit1/Bcommit0；未再次重启。证据在外部备份b-once-uart-20261015-105733目录的boot-selection-markers.txt、conclusion.json与trial-marker-cleanup.json。不能只凭bcm_bootstate设置命令退出0判断恢复完成。


普通B成功证据：外部备份`b-normal-uart-20261015-113047/boot-selection-markers.txt`、`conclusion.json`及父目录`normal-b-20261015-113047-health.json`。本轮仅一次正式B重启，未另做重复B重启；B原生串口登录已验证，开发期noconsole保持0。该记录只证明基础官方固件运行，不证明之后新增 C 业务运行。

## C 虚拟路由固件构建

入口 `build-router-firmware.sh` 先编译仓库 `router_plugin` C 源码，运行 ARM/QEMU 核心测试和现有动态库门禁，再暂存二进制、原生页面、服务脚本及来源清单。`0001-virtual-router-integration.patch` 将它们集成到 rootfs，同时加入 httpd 认证管理桥、网络工具下方的虚拟路由菜单、rc 启停/恢复以及 dnsmasq 接入。

运行 `wsl -d BuildServer -- bash /mnt/d/Code/CloudHelper/firmware/asuswrt/build-router-firmware.sh`。成功后的专用无 CFE 镜像和 SHA256/源码清单位于忽略的 `artifacts/router/`。最终检查包括完整镜像 CRC/UBI/分区容量、rootfs 中程序/页面/脚本哈希、来源清单和禁止宿主 core 转储。源码原厂工具的崩溃转储仅按 ELF ET_CORE 识别后清理；这不精简固件模块。

普通 `build-baseline.sh` 不主动应用业务补丁；工作树已应用业务补丁时要求使用专用入口，避免把业务镜像误称纯基线。自有源码与覆盖保存在 Git，官方完整源树、SDK、构建中间文件和镜像不入 Git。升级仍单独执行设备校验、A/CFE 保留和串口取证，不由构建脚本自动刷写。
