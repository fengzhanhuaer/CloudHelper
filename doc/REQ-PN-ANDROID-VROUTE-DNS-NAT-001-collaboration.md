# 协作文档

- 适用规则: AI协作规则
- 后续工作传递声明: 本文档必须传递给后续阶段与后续角色。
- 需求编号: REQ-PN-ANDROID-VROUTE-DNS-NAT-001
- 需求前缀: REQ-PN-ANDROID-VROUTE-DNS-NAT-001
- 当前阶段: Code完成
- 最近更新角色: Code
- 最近更新时间: 2026-07-11T09:35:00+08:00
- 工作依据文档: doc/ai-coding-collaboration.md
- 状态: 已完成

## 第1章 需求定义

### 1.1 背景

Android 端已能建立 VpnService TUN、同步 Android VRoute 配置、连接 VRoute carrier，并具备基础 fake-ip DNS 与 IPv4 VRoute carrier 转发能力。但现场日志显示，部分 Android 应用会绕过或复用 DNS 结果，直接对公网真实 IP 建立 TLS 连接，例如 `play.googleapis.com` 对应的 `216.239.*` 或 IPv6 地址。此类连接命中非直连域名规则后，当前 mobilecore 会报错:

`non-direct tcp packet should be handled by vroute ip data plane`

虚拟路由数据面不支持传播真实公网 IP。Android 端必须保持与 PC 端一致的 fake-ip/虚拟 IP 语义: VRoute carrier 内只传播 fake-ip 或虚拟路由 IP，不传播公网真实 IP。

### 1.2 目标

- Android 端补齐接近 PC 端的 VRoute DNS/fake-ip 接管能力。
- Android VPN 内 DNS 同时支持 UDP/53 与 TCP/53。
- 非直连域名统一分配 `198.18.0.0/15` fake-ip。
- 对已经命中 SNI 的非直连域名，建立手机本地真实 IPv4 到 fake-ip 的映射。
- 真实 IPv4 到 fake-ip 的转换仅发生在 Android 本地 TUN 数据面，VRoute carrier 内仍只看到 fake-ip。
- Android mobilecore VRoute carrier 支持 `websocket-h3`，可按主控下发的 http3/h3/websocket-h3 邻接链路拨号。
- 暂不支持 IPv6 VRoute 传播；Android VPN 不声明 IPv6 地址、IPv6 默认路由或 IPv6 DNS，VPN 内 AAAA 统一空答以强制应用回落 IPv4。
- 在 Android 状态页暴露 DNS/fake-ip/NAT 映射与漏网真实 IP 诊断信息。

### 1.3 非目标

- 不扩展 VRoute frame 协议以承载 IPv6。
- 不允许 VRoute carrier 传播公网真实 IP。
- 不实现 Android root/iptables/system DNS 修改。
- 不把 PC Wintun/Linux TUN 安装逻辑迁移到 Android。

### 1.4 术语

- fake-ip: Android/PC VRoute DNS 为非直连域名分配的 `198.18.0.0/15` 地址。
- real-ip NAT: Android 本地 TUN 数据面将已识别非直连域名的公网真实 IPv4 连接改写为 fake-ip 连接，并在回包时改写回真实 IPv4。
- VRoute carrier: Android mobilecore 与相邻节点之间承载 VRoute frame 的链路。
- 非直连域名: 主控下发的 VRoute 规则中 action 为 `probe_exit` 且出口不是本机直连的域名规则。

## 第2章 范围与验收

### 2.1 功能范围

| 编号 | 功能 | 范围 |
| --- | --- | --- |
| F001 | DNS UDP/53 | 保留现有 Android VPN UDP DNS 解析、fake-ip 分配、AAAA 抑制逻辑 |
| F002 | DNS TCP/53 | 新增 Android VPN TCP DNS 查询解析，复用同一个 DNS/fake-ip 规则 |
| F003 | SNI fake-ip 预热 | TCP SNI 命中非直连域名时，预热该域名 fake-ip |
| F004 | real-ip 到 fake-ip 映射 | TCP SNI 命中非直连域名且原始目标是公网 IPv4 时，建立本地 real-ip -> fake-ip 映射 |
| F005 | 出向包改写 | Android TUN 收到匹配 real-ip 映射的 IPv4 TCP/UDP 包时，目的 IP 改写为 fake-ip，并重算 IPv4/传输层 checksum |
| F006 | 回向包改写 | Android TUN 写回 app 前，将匹配映射的源 fake-ip 改写回原真实 IPv4，并重算 checksum |
| F007 | IPv6 边界 | Android VPN 不捕获 `::/0`，不下发 IPv6 DNS；VPN DNS 对 AAAA 统一空答，避免应用优先走未支持的 IPv6 |
| F008 | websocket-h3 carrier | Android mobilecore 支持 `websocket-h3`/`http3` VRoute carrier 拨号，认证头与 bridge role 与 websocket carrier 保持一致 |
| F009 | 状态可见化 | `vpnStatus().dns` 暴露 fake-ip、route hint、real-ip NAT 映射数量与最近事件；`vpnStatus().vroute.capabilities` 暴露 h3 支持状态；手机端不暴露路由规则明细 |
| F010 | Android 前端展示 | 手机状态页显示 DNS 接管、fake-ip、NAT 映射、h3 能力与最近漏网诊断；不显示路由规则列表 |

### 2.2 验收标准

- A001: `play.googleapis.com` 这类非直连域名的 A 查询返回 `198.18.*` fake-ip。
- A002: Android VPN 内任意 AAAA 查询不返回公网 IPv6，应用应回落 A/IPv4。
- A003: Android VPN TCP/53 查询与 UDP/53 查询行为一致。
- A004: 当 TLS SNI 识别到非直连域名且原始目标是公网 IPv4 时，后续同源真实 IPv4 包在本地改写为对应 fake-ip 后进入 VRoute carrier。
- A005: VRoute carrier frame 中不得出现被映射的公网真实 IPv4 作为目的地址。
- A006: 回包写回 Android app 前恢复为应用原本连接的公网真实 IPv4，避免破坏 socket 语义。
- A007: Android VpnService 不声明 IPv6 地址、IPv6 默认路由或 IPv6 DNS，mobilecore TUN 栈只注册 IPv4。
- A008: 主控下发 `http3`/`h3`/`websocket-h3` 邻接规则时，Android mobilecore 使用 HTTP/3 websocket carrier 拨号，不再返回 unsupported。
- A009: `vpnStatus().vroute.capabilities.websocket_h3` 为 true。
- A010: `go test ./mobilecore` 通过，新增覆盖 DNS TCP、real-ip NAT、fake-ip-only carrier 边界与 h3 carrier 能力的测试。

## 第3章 任务拆分

| 任务 | 状态 | 说明 | 主要文件 |
| --- | --- | --- | --- |
| T001 | 已完成 | 建立需求跟踪与验收边界 | `doc/REQ-PN-ANDROID-VROUTE-DNS-NAT-001-collaboration.md` |
| T002 | 已完成 | 补 Android VPN TCP/53 DNS 处理 | `probe_node/mobilecore/vpn_tun.go` |
| T003 | 已完成 | 建立 SNI 触发的 fake-ip 预热与 real-ip NAT 映射 | `probe_node/mobilecore/vpn_tun.go` |
| T004 | 已完成 | 实现 IPv4 TCP/UDP 包改写与 checksum 重算 | `probe_node/mobilecore/vpn_tun.go` |
| T005 | 已完成 | 确保 VRoute carrier 仅承载 fake-ip/虚拟 IP | `probe_node/mobilecore/mobile_vrouter_dataplane.go` |
| T006 | 已完成 | 补 Android mobilecore `websocket-h3` carrier 拨号 | `probe_node/mobilecore/mobile_vrouter_dataplane.go` |
| T007 | 已完成 | 状态 payload 增加 NAT 映射、最近事件与 h3 能力 | `probe_node/mobilecore/vpn_tun.go`; `probe_node/mobilecore/mobile_vrouter_dataplane.go` |
| T008 | 已完成 | Android 状态页展示 DNS/NAT/h3 诊断 | `probe_node_android/app/src/main/assets/app.js`; `status.html`; `app.css` |
| T009 | 已完成 | 单元测试与回归验证 | `probe_node/mobilecore/*_test.go` |

## 第4章 设计约束

- D001: VRoute carrier 内不得传播公网真实 IP。
- D002: real-ip NAT 只在 Android 本机 TUN 边界发生。
- D003: fake-ip 映射必须沿用 `198.18.0.0/15`。
- D004: IPv6 本需求明确禁用 Android VPN 捕获与 DNS 返回，不扩协议。
- D005: DNS UDP/53 与 TCP/53 必须共用 `resolveAndroidVPNDNSPacket()`，避免规则分叉。
- D006: NAT 映射必须有 TTL，过期后自动清理。
- D007: 状态页展示运行事实，不用文案声称具备 PC 完整等价能力。
- D008: `websocket-h3` 只补 carrier 拨号能力，不改变 VRoute frame 格式与 fake-ip 语义。
- D009: Android 手机端状态 payload 与前端不展示路由规则明细，仅保留规则数量与出口/承载诊断。

## 第5章 风险与开放问题

- R001: Android 应用可能使用 DoH/QUIC/内置 DNS，VPN 内 DNS 不一定能覆盖所有解析来源。
- R002: 已建立的真实 IP TCP 连接无法无缝改变远端地址；需要从首个已识别连接开始建立映射，并让后续包按本地 NAT 转换。
- R003: checksum 改写错误会导致 app 或 gVisor 丢包，需要单测覆盖 TCP/UDP。
- R004: IPv6 暂不支持时必须阻止 App 优先拿 AAAA 后进入不可达 IPv6 拨号；少数依赖 IPv6-only 的目标会失败，待后续 IPv6 VRoute 协议能力再补。
- R005: Android 网络环境可能阻断 QUIC/UDP，`websocket-h3` 失败时必须保留明确错误；是否 fallback 到 websocket 由主控链路策略/下发规则决定。

## 第6章 实施记录

- 2026-07-11: 创建需求跟踪。确认边界: Android 端补 DNS TCP/53、SNI fake-ip 预热、真实 IPv4 到 fake-ip 的本地 NAT 映射；VRoute carrier 内仍只允许 fake-ip/虚拟 IP；暂不支持 IPv6 VRoute 传播。
- 2026-07-11: 用户追加要求补齐 `websocket-h3`。更新范围: 本需求纳入 Android mobilecore h3 carrier 拨号与状态能力展示，但不改变 VRoute frame 与 fake-ip-only 语义。
- 2026-07-11: 完成实现。`go test ./mobilecore` 通过。实现 Android VPN TCP/53 DNS、SNI 触发 fake-ip 预热、真实 IPv4 到 fake-ip 的 TCP/UDP 本地 NAT 改写、回包恢复、HTTP/3 carrier 拨号、状态 payload 与 Android 摘要展示。
- 2026-07-11: 现场回归日志仍出现 `[2408:...] network is unreachable`，确认为 Android VpnService 仍捕获 IPv6 且 DNS 可能返回 AAAA。补充整改: VpnService 仅声明 IPv4 地址、IPv4 默认路由和 IPv4 DNS；mobilecore TUN 栈仅注册 IPv4；Android VPN DNS 对 AAAA 统一空答。
- 2026-07-11: 用户要求手机端不显示路由规则。补充整改: `vpnStatus().vroute.config` 移除 `route_rule_items`，Android 状态页移除路由规则列表，仅保留规则数量、出口节点、承载连接与能力边界。
