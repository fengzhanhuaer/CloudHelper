# 华硕路由器 JFFS 虚拟路由插件

独立 C 程序，安装在原厂固件的 `/jffs/cloudhelper-router`。不编译、刷写系统，不包含 Go 或 QUIC；只使用固件已有的 OpenSSL、libcurl、json-c、libdl 和系统库。主控和其他 Probe 按现有 TLS/WebSocket 协议互通。主控HTTP和TLS连接在原有超时预算内尝试最多4个DNS返回地址，证书/主机名或SPKI校验不变；已完成TLS的POST不自动重放。配置拉取暂时失败保留当前策略和控制连接，周期重试。

主控下发分流规则时热更新策略和 Fake IP 缓存出口，保持原 TTL、NAT、物理槽位和控制流。拓扑增删或地址、密钥、公钥、指纹变化只重建受影响链路；槽位数减少只关闭多余槽位。新票据用于后续握手，不延长现有会话认证期限。虚拟 IP、虚拟网段、监听端口、启用状态或 DNS 上游变化仍会重建引擎。网关发布的私网路由按增删差异应用，失败回滚该次系统路由变更。

普通 IPv4 CIDR 路由规则支持 `cidr:`、`ip_cidr:`、`ip-cidr:` 和 `ip:`，按规则顺序匹配 direct、reject、probe_exit。仅 LAN 入站的匹配流量进入独立策略表，物理直连网段优先保留；插件自身的主控、载体与出口套接字不被截获。已经建立的 TCP NAT 流固定原目标和出口，删除 CIDR 后仍通过连接标记保持转发，新连接采用新规则；不关闭系统或物理网卡的反向路径检查。远端私网目标仍需网关代理 ACL 授权。

TLS SNI 嗅探使用独立开关 `tls_sni_enabled`，默认关闭，不依赖 DNS 分流开关。显式开启后，仅截获新建 LAN IPv4 TCP/443，读取 ClientHello 的明文域名选择直连、拒绝或 Probe 出口；远程 TCP 使用 Probe Node 原有 main=8 协议。支持 TCP 分段和跨 TLS record，无 SNI、畸形或超过 16 KiB/2 秒预算时按原目标 IP 规则回退。不解密 HTTPS，不支持 ECH 内层域名和 QUIC。开关热应用不重建载体：开启保留已有直连，关闭保留已建透明会话，仅改变新连接入口。最多 64 个透明/出口 TCP 会话，每方向 64 KiB 缓冲，空闲 120 秒释放；按需分配约 8 MiB RAM。

管理入口直接位于原厂后台的“网络工具”下方，名称为“虚拟路由”，使用同一管理端口和原厂登录。插件本身只提供 root 所有的 Unix socket，不开 TCP 管理端口，不要求额外管理口令。页面按运行状态、链路状态、节点测速、主控设置、网关代理、DNS 设置、SNI、日志分为独立标签，保留配置导入、Node ID/密钥、公网连接、各 DNS 上游开关和在线升级。运行状态显示当前已认证控制流实际使用的域名或备用 IP 与端口，断开后清空。主控、网关、DNS 和 SNI 分别保存，其他标签的未保存内容保持不变；SNI 与其他日志分别显示，仍共享最近 64 条内存记录，不写文件。监听端口统一在主控设置。

载体按控制、业务、测速分队列：控制预留 32 KiB，测速最多 96 KiB，每槽位队列节点和数据合计最多 256 KiB。完整帧之间控制优先，业务每 8 帧让出一次测速机会；TCP 会话的 open/data/close 与测速 start/data/finish 保持各自 FIFO。既有业务流不迁移槽位，队列满保留原背压。TLS 单次最多写 16 KiB，支持的内核对载体启用 `TCP_NOTSENT_LOWAT=16384`，限制尚未发出的积压而不缩小 TCP 发送窗口；旧内核不支持时兼容回退。状态接口追加各类队列字节、队首等待时间、内核发送/未发送字节（不支持时为 -1）。链路 RTT 仍包含排队和对端处理时间，调度改进不能消除底层丢包或建连抖动。

主控节点类型统一显示“路由器插件”，内部继续使用 `asus_56uv2` 兼容已注册节点及安装 JSON。按机型分别发布 RT-AX56U V2（内部 RT-AX55）和 RT-BE86U 插件包；两者使用 ARM32 C 程序，BE86U 的 aarch64 内核运行 ARM32 用户态。

插件不绑定固件版本号或 HTTPD 哈希。安装和启动检测实际运行能力：固件已有库能加载程序、原厂菜单结构可识别、唯一认证 ASP 处理表符合接口布局，软件中心自启动调用链存在。HTTPD 隐藏符号时按 ELF 段权限、字符串和回调结构动态定位，不使用固定地址；TLS 客户端通过公开 OpenSSL 接口识别。原厂登录认证回调及 LAN 来源校验保留。固件升级后重新检查接口；如果厂商改变了不兼容的 ABI，保留原厂后台并报告兼容性错误。CI 固定的四个固件镜像仅为回归样本，不是安装白名单。

启动时在私有 RAM 目录复制原版 httpd，通过可撤销的启动入口加载扩展；原版可执行文件内容不变。停止撤销挂载并恢复原版 Web，Flash 系统分区不变。原厂 SSL 限制程序执行路径，故服务从 RAM 的 `/usr/local/cloudhelper-router` 运行，JFFS 保存发布文件。未知目录布局或外来挂载拒绝覆盖。BE86U 的 BusyBox 挂载限制由 root 专用 `--runtime-mount` 固定操作处理，不开放远程任意路径挂载接口。

软件中心手动安装：选择与机型对应的 `cloudhelper-asus56uv2-jffs.tar.gz` 或 `cloudhelper-asusbe86u-jffs.tar.gz`，上传整个压缩包，无需自行解压。一级目录为 `cloudhelper`，包含 `install.sh`、`hnd` 平台标记、软件中心页面和卸载脚本。成功后“已安装”列表显示插件及版本，点击进入原厂后台虚拟路由页。卸载保留 JFFS 身份、DNS、网关配置和缓存。

在线升级：首次安装支持升级的新版包后，可在插件页“运行状态”检查更新并升级，或从主控探针列表下发升级。旧插件没有能力上报时不显示升级按钮。默认经过主控认证代理下载，也支持主控选择直连 GitHub；后台下载支持 Range 续传，按机型、稳定版本、发布 SHA256 和清单校验后安装，失败保留当前版本或恢复 previous。仅更新插件，不刷系统、不重启路由器；下载/安装进度保存在 RAM，不写日志文件。

GitHub Actions 随 Probe Node 同一版本自动编译两款插件，发布两个固定名称包及各自清单。`ci-inputs.lock.json` 固定公开 SDK 和回归镜像的大小与 SHA256；不依赖开发机目录，不上传厂商二进制到仓库。首次构建准备 SDK，之后复用缓存。可独立复现：

```sh
docker build -f router_plugin/Dockerfile.ci -t cloudhelper-router-ci router_plugin
mkdir -p /tmp/chvr-sdk /tmp/chvr-inputs
docker run --rm -v "$PWD/router_plugin:/module" -v /tmp/chvr-sdk:/sdk -v /tmp/chvr-inputs:/inputs cloudhelper-router-ci python3 tools/prepare-ci.py /sdk --cache /inputs
docker run --rm -v "$PWD/router_plugin:/module" -v /tmp/chvr-sdk:/sdk -e CHVR_CI_SDK=/sdk -e CHVR_VERSION=v0.4.69-dev cloudhelper-router-ci bash tools/build-ci.sh
```

构建与安装：

```sh
# 本机 WSL，使用官方 ARM 工具链和原厂 rootfs 验证。
# CHVR_VENDOR_ROOT 指向外部 ASUS 53297 构建目录（默认 $HOME/cloudhelper-firmware-official-53297），
# 需要 opt-toolchains/、source/asuswrt/，以及 inspection/stock-52332 原厂解包内容。
# 原厂源码、SDK、原厂镜像及生成文件均不提交 Git。
bash router_plugin/tools/build-arm.sh
# 正式发布包：CHVR_VERSION=v<实际发布版本> bash router_plugin/tools/build-arm.sh
# 省略时使用 v<仓库version>-dev；CLI、状态页、主控上报及清单使用同一版本。
python router_plugin/tools/package-jffs.py
# BE86U：先用本机只读提取的原厂库验证，不把厂商二进制提交Git。
python router_plugin/tools/check-stock-runtime.py <BE86U-rootfs> --profile be86u --baseline 'RT-BE86U official 3.0.0.6.102_39436'
python router_plugin/tools/check-stock-runtime.py <BE86U-koolcenter-rootfs> --profile be86u-koolcenter --baseline 'RT-BE86U 39125_koolcenter'
python router_plugin/tools/package-jffs.py --profile be86u
# Windows 本机；settings 为私有原厂配置备份，仅在内存提取凭据做验收。
python router_plugin/tools/install-router.py <生成的tar.gz> --settings <私有settings.cfg> --report router_plugin/artifacts/jffs/device-stock-web.json
```

安装包只含本插件的程序、httpd 扩展、服务脚本和 ASP 页面，以及安装器/校验清单。不会打包整个上游、系统库或备份。ARM ELF/原厂动态库、SSL 真正初始化、httpd 导出契约、ARM 扩展加载及原厂 BusyBox 语法验证通过后才允许打包。

连接主控：在主控探针列表找到华硕节点，点击“安装”，弹窗显示包含主控地址、Node ID、节点密钥的 JSON，点击“复制”。进入华硕后台“虚拟路由 → 从主控导入”并粘贴，页面会自动校验并填入连接字段，再按需启用并“保存并应用”。导入保留 DNS 和公网连接设置；无效配置不覆盖原值，密钥填入密码框，成功后清空粘贴框。

```sh
# 路由器 SSH，或已认证的串口 shell。
/jffs/cloudhelper-router/service start
/jffs/cloudhelper-router/service status
/jffs/cloudhelper-router/service stop
# 解包后的安装器支持 --rollback 和 --uninstall。
```

`stop` 恢复原厂菜单、Web 启动入口和受管 DNS/防火墙。安装保留活动与上一个版本；回滚不允许恢复旧的独立端口管理方案。卸载保留用户配置及历史版本遗留的私有密钥/证书，但当前方案不生成或使用独立管理凭据。

**开机自启**：未修改的原厂 52332 仍需 SSH/串口执行 `service start`，`cru` 仅看护当前启动周期。`firmware/asuswrt/build-autostart-firmware.sh` 制作的最小官方定制固件提供专用 JFFS 异步启动入口；需配合新版插件包，实机重启验证须单独完成。创建 `/jffs/cloudhelper-router/autostart.disabled` 禁用该入口，删除恢复。管理页接入会短暂重启 Web 进程，不重启路由器。

BE86U 官改 39125_koolcenter 已接入软件中心实际调用的 `/koolshare/init.d/V99cloudhelper.sh` 和 `T99cloudhelper.sh`，安装后注册异步启动及停止入口；保留原有 services-start/stop 和其他插件脚本。要求 jffs2_scripts=1 且已有软件中心调用链完整；同名外来文件或符号链接拒绝覆盖。禁用标记同上，卸载仅移除本插件自有入口；`service boot-status` 查看状态。2026-10-17实机观察到新 boot_id 后插件自动恢复、主控在线；原厂39436仍须手动启动，不把旧残留视为有效入口。AX56U V2 的固件不能刷入 BE86U。新安装默认关闭业务；更新保留既有身份和配置。Web 密码不在可读 NVRAM 中时，由用户用原厂登录验收页面。

AX56U V2 官改 `386_49559_koolshare` 同样使用上述软件中心 V/T 入口自启，精确验证该版 HTTPD 的导出认证接口和现有运行库；恢复安装保留 JFFS 节点身份、DNS、网关配置及原有软件中心脚本。固件升级后按实际接口重新检查兼容性。

网关代理可以单独开启，无需勾选“启用虚拟路由”；共享同一节点身份、认证及承载连接。仅网关代理运行时不接管路由器 DNS，配置中的 DNS 选择保留供虚拟路由再次启用时使用。

DNS 由原厂 dnsmasq 继续处理 LAN 名称，外部查询交给插件；原厂上游文件在 RAM 备份，SIGHUP 重新加载，停用恢复。公开的上游文件以0644供已降权为nobody的dnsmasq读取，私有备份和身份配置保持0600；自动修复旧插件留下的0600上游文件。代理规则的 fake IP 和映射由主控权威提供，直连返回真实 IP，拒绝明确响应。fake IP/必要虚拟网段、获准的远端代理网段和普通 CIDR 代理/拒绝规则进入 TUN，其余上网仍走原厂路径；启用 SNI 时增加上述 TCP/443 透明入口。监听与对端连接端口由主控节点表统一配置（默认 12040），页面不再单独设置；旧本地port字段保留兼容但不决定实际监听。LAN 始终监听；开启公网连接才额外绑定 WAN 并放行，所有链路需要节点认证。

DNS 上游按 `DoH → DoT → 普通 DNS → 系统 DNS` 依次回退，前三类各一个，默认 `https://dns.alidns.com/dns-query`、`dns.alidns.com:853`、`223.5.5.5:53`。主控路由页的“节点 DNS 上游”通过配置响应顶层 `dns_upstreams` 下发；收到后整体校验并保存至 `/jffs/cloudhelper-router/config.json`，省略字段保留原值、相同值不重复写入。旧版 `dns_upstream` 迁移保留自定义普通 DNS。DoH/DoT验证证书和主机名，普通UDP截断自动转TCP。系统DNS来自原厂上游RAM文件/受管前备份，过滤本机和fake网段，不回指dnsmasq；当前统一配置只接受IPv4普通DNS和IPv4/域名加密端点。

资源有界：最多 8 条拓扑，每条最多 4 个槽位，每槽发送队列 256 KiB（总计最多 8 MiB）；2 个握手任务；4 个 DNS 工作线程/16 个任务槽；1 个映射工作线程/16 个待处理包；1024 映射、2048 NAT 流。测速采用Probe Node的每方向最多10秒/128 MiB（先达到为准），一次一个任务；接收端按请求时长（最长10秒）加500毫秒宽限结算，结束帧延迟不再卡住结果。日志为最近64条固定内存环，相同连续错误合并；仅查看日志标签时读取，同时输出stderr/console，不写日志文件、不引入数据库。静态 RAM 挂载上限 2 MiB，按需占用；早期单槽版本原厂 B 实机管理空闲时服务 RSS 约 3.5 MiB、运行文件约 680 KiB，业务峰值待测。

fake IP映射先查本地缓存，命中不请求主控；与Probe Node一致，映射最长48小时，主控expires_at更早时优先；DNS回答最长600秒且不超过映射剩余寿命。过期再请求主控，同一目标的并发查询合并，失败冷却1秒。域名、fake IP、rule_id、exit_node_id及到期时间保存在`/jffs/cloudhelper-router/fake-ip-cache.json`，保留出口信息；不保存真实IP解析结果或日志。正常每60秒最多合并写入一次，停止补存，突发断电最多丢失最近未保存的变更。文件严格小于524287字节，连同原子更新临时文件合计小于1MiB；超限保留最近使用项，启动清理中断写入残留。重连/拓扑变化/插件重启恢复未过期映射且不延长TTL；主控、身份、密钥、fake网段或路由规则变化使旧库失效。日志环约17KiB，编译期硬性限制小于256KiB，仍只保存在RAM及输出console。

已有验证包括核心/管理 API、原厂 Web 认证接入、原厂 DNS 上游切换恢复、现有 Probe 双角色 Carrier/Ping/上传下载互操作、隔离 Linux 内核 TCP/UDP fake IP 转发与启停清理，以及安装/升级/回滚/卸载。测试的 Go 对端只在电脑运行，不进入插件。特权测试只在标记为 `CHVR_DISPOSABLE_TEST=1` 的临时 Docker 网络中运行，禁止 host 网络。

当前边界为 IPv4 TCP/UDP/ICMP echo；代理域名的 AAAA/SVCB/HTTPS 返回空答案，未提供 IPv6 代理、IP 分片重组或备用出口自动切换。2026-10-17在用户已配置身份的华硕25上验证了节点23的12041链路、双向测速、LAN UDP/TCP DNS、代理AAAA抑制及25→23→24真实fake IP HTTPS200；最小自启固件的两次重启验收见账本。本轮只更新JFFS插件，未重启或刷固件。弱网下主控仍有偶发超时，曾出现约12秒首次TCP等待；长期吞吐、无线加速、WAN重建和其他出口仍待单独验收。新安装默认关闭业务。

需求、历史固件恢复证据和后续验收见 [任务账本](../doc/tasks/2026-10-14-asuswrt-router-plugin.md)。

“网关代理”标签独立于DNS/连接设置。可发布32个私有IPv4网段，选择最多64个允许访问节点；主控只向获准源节点发布对应路由，出口再次核对来源虚拟IP、路径终点和允许列表。华硕通过现有TUN、iptables/conntrack转发和SNAT；普通Probe采用已有用户态转发组件，协议仍为普通IPv4帧。此能力不依赖旁路由或单臂路由模式。

本机设置保存在config.json的gateway_proxy中。收到的完整策略和本节点证书分别保存route-cache.json、node-certificate.json，0600且原子替换，相同内容不重写。主控不可达时可恢复有效缓存；认证票据过期仍拒绝建链，持久化不绕过链路授权。收到撤销配置后覆盖缓存，重建受管路由。与本地物理网段重叠的远端路由跳过，防止接管本地管理连接；最多安装128条远端CIDR路由。

Probe状态上报ipv4包含去重的LAN/WAN地址，并以lan_ip/wan_ip区分来源。system.network_rx_bytes/network_tx_bytes只读取活动WAN接口的累计收发字节（含该接口所有流量），由主控现有增量逻辑累计；WAN无地址或计数不可读时network_traffic_available=false，不混入LAN或TUN流量。地址变更检查每10秒，普通状态上报约20秒。

DNS 上游提供独立的 DoH、DoT、普通 DNS 开关，默认全部开启；仅按此顺序尝试开启的上游，最后保留系统 DNS 回退。开关写入本机 JSON，主控同步地址时保留本机开关选择；旧版仅有三个地址的配置继续兼容。关闭某项会跳过该传输，也不再用普通 DNS 的已关闭地址引导加密上游（系统 DNS 独立回退）。

主控通知后获取到的有效配置会直接交给下一轮引擎并写入原有路由配置缓存，不重复下载或额外等待15秒。刷新失败保留当前配置和控制流，按5/10/20/30秒退避重试；常规巡检仍为60秒。保存本机身份后会清除旧身份的待应用配置。Probe Link使用节点名称及对端域名；入站对端域名未下发时显示「—」。

固件提供 Broadcom `SKIPLOG` 目标时，仅对插件监听端口的入站承载跳过 BLOG 加速，避免 BE86U 上 TLS 握手后收到异常记录。规则位于自有 CHVR_IN 链最前，继续执行原认证和防火墙检查；健康检查会恢复丢失规则，停止随自有链清理。其他流量继续按固件设置加速，不更改全局加速或 NVRAM。固件不提供该目标时沿用原有规则。未知的 OpenSSL 错误保留数值码，避免误报为普通关闭。

业务帧沿用Probe Node的VR二进制封装、校验和及WebSocket流；省略/空路径按已认证连接的入站路径处理，路径最多3跳，逐帧检查前一跳及拓扑授权。业务路径不合法时只丢弃该帧并记录有界内存日志，保留承载连接；封装/校验和损坏仍关闭连接。断开日志区分VR封装、WebSocket格式及对端关闭码，避免把所有错误统称为invalid carrier frame。该互操作范围不代表已实现Probe的全部管理调试消息或IPv6能力。

每链路缓存一份不超过1KiB、已通过授权的control及路径，仅字节完全相同才复用路径检查，仍逐帧检查封装校验和；策略或对端变化使用新引擎，重连清除缓存。8条拓扑×4槽位总增量约48KiB；大control正常解析。TLS TCP设置TCP_NODELAY，减少小控制包合并等待。2026-10-17同一千兆有线25↔9实机复测，下载30.5→121Mbps、上传89.2→94.7Mbps；进程仍接近单核满载，不能将千兆协商速率当作插件已支持千兆吞吐。

Carrier客户端与服务端支持每条拓扑4个槽位（0～3），沿用Probe默认值及槽位/多连接能力HTTP头。客户端先连接槽0，收到多槽协商才创建其余连接；旧服务端保持单连接。每槽独立TLS/WS状态、心跳、发送队列与重连；替换同一槽位不会关闭其余槽。IP流采用Probe相同的正反向端点FNV键并保持已有绑定；转发Proxy帧按session_id绑定。仅失败槽位上的流重新选择，队列满不会跨槽发送导致乱序。控制和测速帧使用首个健康槽，测速仍为单流测试，不代表多个槽位的合计吞吐。

每槽发送缓存256KiB，最多8条拓扑×4槽位＝8MiB队列，流绑定最多2048条、握手任务仍最多2个；不写连接日志到Flash。主控根据router_plugin_carrier_slots上报，在双方快照中将华硕边限制为4，旧插件/未知能力为1，高档16/64/264降至4而不改变原拓扑存储；插件降级后下次report恢复单槽。Probe Link/API按拓扑汇总计数，另提供carrier_count、carrier_slots与分槽carriers明细（含队列/错误/收发），节点名与域名继续显示。
