# 华硕 RT-AX56U V2 虚拟路由插件

独立 C 程序，安装在原厂固件的 `/jffs/cloudhelper-router`。不编译、刷写系统，不包含 Go 或 QUIC；只使用固件已有的 OpenSSL、libcurl、json-c、libdl 和系统库。主控和其他 Probe 按现有 TLS/WebSocket 协议互通。

管理入口直接位于原厂后台的“网络工具”下方，名称为“虚拟路由”，使用同一管理端口和原厂登录。插件本身只提供 root 所有的 Unix socket，不开 TCP 管理端口，不要求额外管理口令。页面提供主控、Node ID/密钥、端口、公网连接、DNS 上游设置，以及 Probe Link 状态和手动测速。

原厂 Web 的接入采用一个约 9 KiB 的 httpd 扩展，保留原厂认证回调。严格匹配原厂 52332 的 httpd SHA256，或最小自启固件只读 ROM 声明的精确哈希（须同时存在专用启动入口）；未知版本拒绝接入。启动时在私有 RAM 目录复制原版 httpd，并通过可撤销的启动入口挂载加载扩展；原版可执行文件内容不变。停止时撤销挂载并重启原版 Web，Flash 系统分区不变。原厂 SSL 限制程序执行路径，故服务也从 RAM 的 `/usr/local/cloudhelper-router` 运行；JFFS 保存发布文件，RAM 中保留原来的 `share -> /tmp/share`，未知目录布局拒绝覆盖。

构建与安装：

```sh
# 本机 WSL，使用官方 ARM 工具链和原厂 rootfs 验证。
# CHVR_VENDOR_ROOT 指向外部 ASUS 53297 构建目录（默认 $HOME/cloudhelper-firmware-official-53297），
# 需要 opt-toolchains/、source/asuswrt/，以及 inspection/stock-52332 原厂解包内容。
# 原厂源码、SDK、原厂镜像及生成文件均不提交 Git。
bash router_plugin/tools/build-arm.sh
python router_plugin/tools/package-jffs.py
# Windows 本机；settings 为私有原厂配置备份，仅在内存提取凭据做验收。
python router_plugin/tools/install-router.py <生成的tar.gz> --settings <私有settings.cfg> --report router_plugin/artifacts/jffs/device-stock-web.json
```

安装包只含本插件的程序、httpd 扩展、服务脚本和 ASP 页面，以及安装器/校验清单。不会打包整个上游、系统库或备份。ARM ELF/原厂动态库、SSL 真正初始化、httpd 导出契约、ARM 扩展加载及原厂 BusyBox 语法验证通过后才允许打包。

```sh
# 路由器 SSH，或已认证的串口 shell。
/jffs/cloudhelper-router/service start
/jffs/cloudhelper-router/service status
/jffs/cloudhelper-router/service stop
# 解包后的安装器支持 --rollback 和 --uninstall。
```

`stop` 恢复原厂菜单、Web 启动入口和受管 DNS/防火墙。安装保留活动与上一个版本；回滚不允许恢复旧的独立端口管理方案。卸载保留用户配置及历史版本遗留的私有密钥/证书，但当前方案不生成或使用独立管理凭据。

**开机自启**：未修改的原厂 52332 仍需 SSH/串口执行 `service start`，`cru` 仅看护当前启动周期。`firmware/asuswrt/build-autostart-firmware.sh` 制作的最小官方定制固件提供专用 JFFS 异步启动入口；需配合新版插件包，实机重启验证须单独完成。创建 `/jffs/cloudhelper-router/autostart.disabled` 禁用该入口，删除恢复。管理页接入会短暂重启 Web 进程，不重启路由器。

DNS 由原厂 dnsmasq 继续处理 LAN 名称，外部查询交给插件；原厂上游文件在 RAM 备份，SIGHUP 重新加载，停用恢复。代理规则的 fake IP 和映射由主控权威提供，直连返回真实 IP，拒绝明确响应。仅 fake IP/必要虚拟网段及获准的远端代理网段进入 TUN，普通上网仍走原厂路径。LAN 始终监听配置端口（默认 12040）；开启公网连接才额外绑定 WAN 并放行，所有链路需要节点认证。

DNS 上游按 `DoH → DoT → 普通 DNS → 系统 DNS` 依次回退，前三类各一个，默认 `https://dns.alidns.com/dns-query`、`dns.alidns.com:853`、`223.5.5.5:53`。主控路由页的“节点 DNS 上游”通过配置响应顶层 `dns_upstreams` 下发；收到后整体校验并保存至 `/jffs/cloudhelper-router/config.json`，省略字段保留原值、相同值不重复写入。旧版 `dns_upstream` 迁移保留自定义普通 DNS。DoH/DoT验证证书和主机名，普通UDP截断自动转TCP。系统DNS来自原厂上游RAM文件/受管前备份，过滤本机和fake网段，不回指dnsmasq；当前统一配置只接受IPv4普通DNS和IPv4/域名加密端点。

资源有界：8 条链路，每链路发送队列 256 KiB；2 个握手任务；4 个 DNS 工作线程/16 个任务槽；1 个映射工作线程/16 个待处理包；1024 映射、2048 NAT 流。测速每方向最多 3 秒/16 MiB，一次一个任务。日志只 stdout/stderr/console，不写日志文件、不引入数据库。静态 RAM 挂载上限 2 MiB，按需占用；原厂 B 实机管理空闲时服务 RSS 约 3.5 MiB、运行文件约 680 KiB，业务峰值待测。

已有验证包括核心/管理 API、原厂 Web 认证接入、原厂 DNS 上游切换恢复、现有 Probe 双角色 Carrier/Ping/上传下载互操作、隔离 Linux 内核 TCP/UDP fake IP 转发与启停清理，以及安装/升级/回滚/卸载。测试的 Go 对端只在电脑运行，不进入插件。特权测试只在标记为 `CHVR_DISPOSABLE_TEST=1` 的临时 Docker 网络中运行，禁止 host 网络。

当前边界为 IPv4 TCP/UDP/ICMP echo；代理域名的 AAAA/SVCB/HTTPS 返回空答案，未提供 IPv6 代理、IP 分片重组或备用出口自动切换。真实主控地址、Node ID 和密钥由用户填写后再联调；长期吞吐、无线加速、WAN 重建和跨重启自启仍需验收。默认关闭业务，不能把管理页面成功当成业务全链路完成。

需求、历史固件恢复证据和后续验收见 [任务账本](../doc/tasks/2026-10-14-asuswrt-router-plugin.md)。

“网关代理”标签独立于DNS/连接设置。可发布32个私有IPv4网段，选择最多64个允许访问节点；主控只向获准源节点发布对应路由，出口再次核对来源虚拟IP、路径终点和允许列表。华硕通过现有TUN、iptables/conntrack转发和SNAT；普通Probe采用已有用户态转发组件，协议仍为普通IPv4帧。此能力不依赖旁路由或单臂路由模式。

本机设置保存在config.json的gateway_proxy中。收到的完整策略和本节点证书分别保存route-cache.json、node-certificate.json，0600且原子替换，相同内容不重写。主控不可达时可恢复有效缓存；认证票据过期仍拒绝建链，持久化不绕过链路授权。收到撤销配置后覆盖缓存，重建受管路由。与本地物理网段重叠的远端路由跳过，防止接管本地管理连接；最多安装128条远端CIDR路由。

Probe状态上报ipv4包含去重的LAN/WAN地址，并以lan_ip/wan_ip区分来源。system.network_rx_bytes/network_tx_bytes只读取活动WAN接口的累计收发字节（含该接口所有流量），由主控现有增量逻辑累计；WAN无地址或计数不可读时network_traffic_available=false，不混入LAN或TUN流量。地址变更检查每10秒，普通状态上报约20秒。
