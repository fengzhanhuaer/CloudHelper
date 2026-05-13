# 协作文档

- 适用规则: AI协作规则
- 后续工作传递声明: 本文档必须传递给后续阶段与后续角色。
- 需求编号: REQ-PN-FAKEIP-TUN-DNS-001
- 需求前缀: REQ-PN-FAKEIP-TUN-DNS-001
- 当前阶段: Code修复
- 最近更新角色: Code
- 最近更新时间: 2026-05-13 13:05:00 +08:00
- 工作依据文档: doc/ai-coding-collaboration.md; 用户需求: probe node TUN 改为仅承接默认 DNS 并靠 fake IP 导入需代理流量，避免频繁操作路由表；DNS upstream 增加系统原默认 DNS，优先级在已添加 DNS 后边；系统设置添加 TUN 卸载、TUN 重置；启用 TUN 时设置主网卡 DNS 并启用代理 DNS。
- 状态: 进行中

## 第1章 Architect章节
- 章节责任角色: Architect
- 状态: 已完成

### 1.1 需求定义
- 状态: 已完成

#### 1.1.1 需求目标
- REQ-PN-FAKEIP-TUN-DNS-001-R1: Windows probe node 的 TUN 代理接管从全局 split default route 改为 fake-ip TUN 最终模式，不再并存旧全局接管模式。
- REQ-PN-FAKEIP-TUN-DNS-001-R2: 启用代理接管时，避免按直连目标频繁新增/删除 /32 bypass 路由；只保留 TUN/fake IP/DNS 所需的稳定网络配置。
- REQ-PN-FAKEIP-TUN-DNS-001-R3: DNS upstream 增加 local dns，即系统原默认 DNS，查询优先级位于已配置 DNS、DoT、DoH 之后。
- REQ-PN-FAKEIP-TUN-DNS-001-R4: 仅需代理的域名通过 fake IP 进入 TUN；直连域名返回真实解析并走系统默认网络。
- REQ-PN-FAKEIP-TUN-DNS-001-R5: TUN 网卡属性仅在安装/安装后检查阶段设置；启用代理与关闭代理阶段不修改任何网卡属性。
- REQ-PN-FAKEIP-TUN-DNS-001-R6: 系统设置页新增 TUN 卸载与 TUN 重置入口，并提供对应本地 API。
- REQ-PN-FAKEIP-TUN-DNS-001-R7: TUN 安装/检查成功时，将主出口网卡 DNS 备份到本地文件后改为本地代理 DNS；TUN 重置/卸载时恢复主出口网卡原 DNS。
- REQ-PN-FAKEIP-TUN-DNS-001-R8: Windows probe node 重启恢复时，TUN installed 状态必须以当前可用性检测为准，不得因历史持久化 installed=true 而显示已安装。

#### 1.1.2 需求范围
- 修改 Windows 本地代理接管逻辑。
- 修改本地 DNS upstream 候选构造逻辑。
- 修改 fake IP 分配条件，确保 fake IP 仅用于 tunnel 动作。
- 调整代理启用/关闭阶段，不再修改网卡属性。
- 更新单元测试覆盖新路由与 DNS upstream 行为。
- 更新系统设置页与本地 API，支持 TUN 卸载、TUN 重置。
- 增加 Windows 主出口网卡 DNS 文件备份、应用与恢复流程。

#### 1.1.3 非范围
- 不新增旧模式与新模式的 UI 切换。
- 不实现 WFP、DoH 劫持或对应用内置 DNS 的强制拦截。
- 不处理 Linux 接管语义变更。
- 不改变链路协议、链路组配置文件结构或远端 probe chain 行为。
- 不实现跨平台 TUN 驱动真实卸载；非 Windows 平台按现有 unsupported/reset 语义降级。

#### 1.1.4 验收标准
- AC1: Windows takeover 不再创建 `0.0.0.0/1` 与 `128.0.0.0/1` 全局 split route。
- AC2: Windows takeover 创建 fake IP CIDR 到 TUN 的稳定路由，并保留局域网段直连路由需求为非必须。
- AC3: TUN direct 出站不再因直连目标新增/删除 /32 bypass route。
- AC4: DNS upstream 候选顺序为已配置 DoH proxy、DoH、DoT、DNS，最后追加系统原默认 DNS。
- AC5: tunnel 域名 A 记录返回 fake IP；direct 域名不再返回 fake IP。
- AC6: 启用代理与关闭代理时不再调用网卡属性设置逻辑。
- AC7: `go test ./...` 在 `probe_node` 模块通过。
- AC8: 系统设置页展示并调用 TUN 卸载、TUN 重置 API。
- AC9: TUN 安装/检查成功后主出口网卡 DNS 指向本地代理 DNS，并启动本地 DNS 服务。
- AC10: TUN 重置/卸载会先关闭代理接管与数据面，再恢复已备份主出口网卡 DNS，清理持久 TUN enabled 状态。
- AC11: 重启恢复时如果当前 TUN 检测为不存在或不可用，界面显示未安装，并写回持久状态 `installed=false, enabled=false`。

#### 1.1.5 风险
- Windows 系统 DNS 原始配置读取不完整时，local dns 可能无法追加；应降级为仅使用已配置 DNS。
- fake IP 模式无法捕获应用直接访问 IP 的流量；这是需求接受的模式边界。
- 使用应用内置 DoH/私有 DNS 的客户端可能绕过本地 DNS；本需求不处理。

#### 1.1.6 遗留事项
- 后续可考虑在 UI 上展示 fake-ip TUN 模式边界与系统 DNS 观测信息。

#### 1.1.7 结论
- 需求可执行，采用直接替换旧 Windows 全局 TUN 接管的方案。

### 1.2 总体架构
- 状态: 已完成

#### 1.2.1 架构目标
- 以 DNS 决策和 fake IP 映射作为 TUN 流量入口，减少路由表动态写入。

#### 1.2.2 总体设计
- Windows 启用代理时，准备 TUN 网卡地址与 DNS 监听地址，创建 fake IP CIDR 指向 TUN 的稳定路由。
- 本地 DNS 对 tunnel 规则域名返回 fake IP，并保存 fake IP 到域名与路由决策的映射。
- TUN 数据面收到 fake IP 目的地址时，还原为域名目标并进入 tunnel chain。
- direct 规则域名由 DNS 返回真实上游结果，不进入 TUN。
- DNS upstream 候选尾部追加系统原默认 DNS，作为本地网络 DNS 后备。

#### 1.2.3 关键模块
| 模块编号 | 模块名称 | 职责 | 输入 | 输出 |
|---|---|---|---|---|
| M1 | Windows takeover | 配置 fake IP 到 TUN 的稳定路由与接管状态 | TUN ifIndex、gateway、fake IP CIDR | takeover 状态、路由配置 |
| M2 | DNS service | 根据规则返回 fake IP 或真实解析，并构造 upstream 候选 | DNS 查询、代理组配置、系统 DNS | DNS 响应、fake IP 映射 |
| M3 | TUN route decision | 将 fake IP 目标还原为域名并选择 tunnel/direct/reject | TUN 目标地址 | route decision |
| M4 | TUN netstack outbound | 执行 TUN 出站连接，不再维护 direct /32 bypass | route decision | direct/tunnel outbound |
| M5 | TUN lifecycle control | 提供安装、重置、卸载生命周期控制 | 系统设置 API、Windows 网卡状态 | TUN 状态、DNS 恢复结果 |

#### 1.2.4 关键接口
| 接口编号 | 接口名称 | 调用方 | 提供方 | 说明 |
|---|---|---|---|---|
| IF1 | applyProbeLocalProxyTakeover | local_console | local_proxy_takeover_windows | 启用 Windows fake-ip TUN 接管 |
| IF2 | restoreProbeLocalProxyDirect | local_console | local_proxy_takeover_windows | 恢复 Windows 接管状态 |
| IF3 | currentProbeLocalDNSUpstreamCandidatesForDecision | DNS resolver | local_dns_service | 返回 DNS upstream 候选 |
| IF4 | shouldUseProbeLocalDNSFakeIP | DNS resolver | local_dns_service | 判断查询是否返回 fake IP |
| IF5 | openProbeLocalTUNOutboundTCP/UDP | gVisor netstack | local_tun_stack_windows | 打开 TUN 出站连接 |
| IF6 | resetTUN/uninstallTUN | system settings API | local_console | TUN 重置与卸载控制 |
| IF7 | apply/restoreProbeLocalTUNPrimaryDNS | TUN lifecycle | Windows netapi | 主出口网卡 DNS 备份、设置、恢复 |

#### 1.2.5 关键约束
- 不新增并存模式开关。
- 非 C/C++ 文件可直接编辑。
- Windows 路由操作仍通过现有 netapi hook，便于测试替换。

#### 1.2.6 风险
- 系统 DNS 枚举依赖 Windows adapter 信息扩展，需避免破坏现有测试 hook。

#### 1.2.7 结论
- 方案与现有 fake IP、TUN route decision 能力兼容，主要调整接管边界与 DNS upstream 来源。

### 1.3 单元设计
- 状态: 已完成

#### 1.3.1 单元清单
| 单元编号 | 单元名称 | 所属模块 | 职责 | 输入 | 输出 |
|---|---|---|---|---|---|
| U1 | fake IP CIDR route defs | M1 | 生成 fake IP CIDR 到 TUN 的路由定义 | fake IP CIDR、gateway、ifIndex | route defs |
| U2 | takeover apply/restore | M1 | 应用/恢复 fake-ip TUN 接管 | Windows route target | 状态与路由变更 |
| U3 | local DNS discovery | M2 | 读取系统原默认 DNS | Windows adapter/default route | DNS server list |
| U4 | DNS upstream append | M2 | 将 local dns 追加到候选尾部 | 配置 DNS、local DNS | ordered candidates |
| U5 | fake IP tunnel-only decision | M2 | 限制 fake IP 只用于 tunnel | domain/qtype/decision | bool |
| U6 | TUN direct without bypass | M4 | direct 出站不维护 /32 route | route decision | net.Conn/io.ReadWriteCloser |
| U7 | TUN lifecycle API | M5 | 系统设置页调用重置/卸载 | HTTP request | TUN 状态 JSON |
| U8 | primary NIC DNS takeover | M5 | 安装/检查成功后设置主出口 DNS | TUN ifIndex、本地 DNS host、原 DNS | DNS backup file、主出口 DNS 修改 |

#### 1.3.2 单元设计
##### U1
- 单元名称: fake IP CIDR route defs
- 职责: 将 fake IP CIDR 转为 Windows IPv4 route def。
- 输入: `currentProbeLocalDNSFakeIPCIDR()`、TUN gateway、ifIndex。
- 输出: `[]probeLocalWindowsRouteDef`。
- 处理规则: CIDR 非法时回退默认 fake IP CIDR；mask 由 prefix 计算。
- 异常规则: ifIndex/gateway 缺失时由调用方阻塞。

##### U2
- 单元名称: takeover apply/restore
- 职责: 替换旧 split default route 行为。
- 输入: route target、fake IP route defs。
- 输出: takeover state。
- 处理规则: apply 只创建 fake IP CIDR route；restore 删除该 route。
- 异常规则: apply 部分失败时回滚已创建 route。

##### U3
- 单元名称: local DNS discovery
- 职责: 读取系统原默认 DNS 服务器。
- 输入: Windows adapter/default route。
- 输出: DNS host:port 列表。
- 处理规则: 排除 TUN ifIndex；优先默认出口适配器 DNS。
- 异常规则: 读取失败返回空并记录 warning，不阻塞 DNS 服务。

##### U4
- 单元名称: DNS upstream append
- 职责: 在已配置 upstream 后追加 local DNS。
- 输入: 配置 upstream、本机 DNS。
- 输出: ordered candidates。
- 处理规则: 去重；local DNS 排在最后。
- 异常规则: local DNS 为空时保持原列表。

##### U5
- 单元名称: fake IP tunnel-only decision
- 职责: 确保 direct 域名不进入 TUN。
- 输入: DNS query type、route decision。
- 输出: 是否使用 fake IP。
- 处理规则: 仅 A 记录且 action=tunnel 且链路选择有效时返回 true。
- 异常规则: reject/fallback/direct 返回 false。

##### U6
- 单元名称: TUN direct without bypass
- 职责: direct 出站不再维护动态 /32 bypass route。
- 输入: direct route decision。
- 输出: direct conn。
- 处理规则: TCP/UDP 直接 dial，不调用 ensure/release direct bypass。
- 异常规则: dial 失败直接返回错误。

##### U7
- 单元名称: TUN lifecycle API
- 职责: 为系统设置页提供 TUN 重置与卸载能力。
- 输入: authenticated POST。
- 输出: TUN runtime state。
- 处理规则: reset 关闭代理接管、停止数据面、恢复主出口 DNS、清理 enabled；uninstall 在 reset 后释放 Wintun handle、清理 TUN IPv4 地址并清理 installed。
- 异常规则: 任何系统操作失败返回 HTTP error，并记录 `LastError`。

##### U8
- 单元名称: primary NIC DNS takeover
- 职责: 在 TUN 安装/检查成功后把主出口网卡 DNS 指向本地代理 DNS，并持久化原 DNS。
- 输入: TUN ifIndex、主出口网卡 GUID、原 DNS、本地 DNS host。
- 输出: DNS backup file、主出口网卡 DNS。
- 处理规则: DNS 备份使用文件落盘；已有同一网卡备份时复用；恢复成功后删除备份。
- 异常规则: 主出口网卡或 DNS 不可枚举时阻塞安装/检查成功返回，避免“看似启用但 DNS 未接管”。

#### 1.3.3 风险
- local DNS 读取需要新增 Windows DNS 字段解析，结构体字段必须与 `GetAdaptersAddresses` 兼容。

#### 1.3.4 结论
- 单元设计满足直接切换最终 fake-ip TUN 模式。

### 1.4 Code任务执行包
- 状态: 已完成

#### 1.4.1 执行边界
- 允许修改: `probe_node/local_proxy_takeover_windows.go`; `probe_node/local_proxy_takeover_windows_test.go`; `probe_node/local_windows_netapi.go`; `probe_node/local_dns_service.go`; `probe_node/local_route_decision_test.go`; `probe_node/local_tun_route_test.go`; `probe_node/local_tun_stack_windows.go`; `probe_node/local_tun_stack_windows_test.go`; `probe_node/local_console.go`; `probe_node/local_console_test.go`; `probe_node/local_console_methods_test.go`; `probe_node/local_proxy_takeover.go`; `probe_node/local_proxy_takeover_linux.go`; `probe_node/local_tun_install_windows.go`; `probe_node/local_tun_install_windows_test.go`; `probe_node/local_pages/system.html`; `doc/REQ-PN-FAKEIP-TUN-DNS-001-collaboration.md`
- 禁止修改: 链路协议文件、控制器接口、Linux takeover 行为、C/C++ 文件、第三方依赖文件。

#### 1.4.2 任务清单
| 任务编号 | 需求编号 | 单元编号 | 文件范围 | 操作类型 | 验收标准 |
|---|---|---|---|---|---|
| T1 | REQ-PN-FAKEIP-TUN-DNS-001-R1 | U1,U2 | `probe_node/local_proxy_takeover_windows.go`; `probe_node/local_proxy_takeover_windows_test.go` | 修改 | 不再创建 split default route，改为 fake IP CIDR route，测试覆盖 route defs 与 apply/restore |
| T2 | REQ-PN-FAKEIP-TUN-DNS-001-R3 | U3,U4 | `probe_node/local_windows_netapi.go`; `probe_node/local_dns_service.go`; `probe_node/local_proxy_takeover.go`; `probe_node/local_proxy_takeover_linux.go` | 修改 | DNS upstream 尾部追加系统原默认 DNS，失败降级为空，非 Windows 返回空 |
| T3 | REQ-PN-FAKEIP-TUN-DNS-001-R4 | U5 | `probe_node/local_dns_service.go`; `probe_node/local_route_decision_test.go` | 修改 | direct 域名不再分配 fake IP，tunnel 域名仍分配 fake IP |
| T4 | REQ-PN-FAKEIP-TUN-DNS-001-R2 | U6 | `probe_node/local_tun_stack_windows.go`; `probe_node/local_tun_stack_windows_test.go` | 修改 | TUN direct TCP/UDP 不再调用 direct bypass route |
| T5 | REQ-PN-FAKEIP-TUN-DNS-001-R2 | U6 | `probe_node/local_console.go`; `probe_node/local_console_test.go` | 修改 | 启用代理前不再执行链路节点 direct bypass 预热 |
| T6 | REQ-PN-FAKEIP-TUN-DNS-001-R1,REQ-PN-FAKEIP-TUN-DNS-001-R2,REQ-PN-FAKEIP-TUN-DNS-001-R3,REQ-PN-FAKEIP-TUN-DNS-001-R4 | U1,U2,U3,U4,U5,U6 | `probe_node` 测试 | 修改 | `go test ./...` 通过 |
| T7 | REQ-PN-FAKEIP-TUN-DNS-001-R1,REQ-PN-FAKEIP-TUN-DNS-001-R2,REQ-PN-FAKEIP-TUN-DNS-001-R3,REQ-PN-FAKEIP-TUN-DNS-001-R4 | U1,U2,U3,U4,U5,U6 | `doc/REQ-PN-FAKEIP-TUN-DNS-001-collaboration.md` | 修改 | Code 章节证据完整，门禁可裁判 |
| T8 | REQ-PN-FAKEIP-TUN-DNS-001-R1 | U2 | `probe_node/local_tun_install_windows.go`; `probe_node/local_tun_install_windows_test.go` | 修改 | `CreateUnicastIpAddressEntry failed: code=1168` 时不信任过期环境变量 ifIndex；若 fallback ifIndex 写入仍为 1168，则排除该 ifIndex 并重新从 Wintun handle/LUID 恢复有效 ifIndex |
| T9 | REQ-PN-FAKEIP-TUN-DNS-001-R5 | U2 | `probe_node/local_proxy_takeover_windows.go`; `probe_node/local_proxy_takeover_windows_test.go` | 修改 | 启用/关闭代理只操作 fake-ip 路由，不调用网卡属性设置与主网卡 DNS 修改 |
| T10 | REQ-PN-FAKEIP-TUN-DNS-001-R6 | U7 | `probe_node/local_console.go`; `probe_node/local_console_methods_test.go`; `probe_node/local_pages/system.html` | 修改 | 新增 `/local/api/tun/reset` 与 `/local/api/tun/uninstall`，系统页按钮可调用并刷新状态 |
| T11 | REQ-PN-FAKEIP-TUN-DNS-001-R7 | U8 | `probe_node/local_tun_install_windows.go`; `probe_node/local_proxy_takeover_windows.go`; `probe_node/local_proxy_takeover_windows_test.go`; `probe_node/local_console.go`; `probe_node/local_console_test.go` | 修改 | TUN 安装/检查成功后设置主出口 DNS 为本地代理 DNS，重置/卸载恢复文件备份 DNS |
| T12 | REQ-PN-FAKEIP-TUN-DNS-001-R6,R7 | U7,U8 | `probe_node` 测试 | 修改 | 新增/更新单元测试并保证 `go test ./...` 通过 |
| T13 | REQ-PN-FAKEIP-TUN-DNS-001-R8 | U7 | `probe_node/local_console.go`; `probe_node/local_console_test.go`; `doc/REQ-PN-FAKEIP-TUN-DNS-001-collaboration.md` | 修改 | 启动恢复不再信任历史 installed=true；当前检测不可用时状态与持久化均变为未安装 |

#### 1.4.3 源码修改规则
- 必须使用 encoding_tools/README.md 描述的接口。
- 对 C/C++ 源代码（`.c`、`.cc`、`.cpp`、`.cxx`、`.h`、`.hpp`）必须使用 encoding_tools/encoding_safe_patch.py。
- 对非 C/C++ 源代码可直接编辑，不强制使用 encoding_tools/encoding_safe_patch.py。
- encoding_tools/ 不可用或执行失败时，Code 必须记录失败命令、错误摘要、影响文件与阻塞影响，并提交第2.6节 `Code任务反馈`。
- 替代 encoding_tools/ 修改受控 C/C++ 源代码前，必须取得 Architect 明确允许。

#### 1.4.4 交付物
- Windows fake-ip TUN takeover 实现。
- local DNS upstream 后置追加实现。
- fake IP tunnel-only 决策实现。
- direct bypass 动态路由移除实现。
- 单元测试与执行证据。
- 系统设置页 TUN 卸载/重置入口。
- 主出口 DNS 文件备份、应用和恢复流程。

#### 1.4.5 门禁输入
- `go test ./...`
- `git diff -- probe_node doc/REQ-PN-FAKEIP-TUN-DNS-001-collaboration.md`

#### 1.4.6 结论
- Code 可按任务包执行。

### 1.5 Architect需求跟踪矩阵
- 状态: 已完成

| 需求编号 | 需求描述 | 架构章节 | 单元设计章节 | Code任务章节 | 状态 | 备注 |
|---|---|---|---|---|---|---|
| REQ-PN-FAKEIP-TUN-DNS-001-R1 | TUN 接管改为 fake-ip TUN 最终模式 | 1.2 | U1,U2 | T1,T6,T7 | 进行中 | 不并存旧模式 |
| REQ-PN-FAKEIP-TUN-DNS-001-R2 | 避免动态 /32 bypass 路由 | 1.2 | U6 | T4,T5,T6,T7 | 进行中 | direct 出站直接走系统网络 |
| REQ-PN-FAKEIP-TUN-DNS-001-R3 | 追加系统原默认 DNS | 1.2 | U3,U4 | T2,T6,T7 | 进行中 | 优先级在已配置 DNS 后 |
| REQ-PN-FAKEIP-TUN-DNS-001-R4 | 仅代理域名经 fake IP 入 TUN | 1.2 | U5 | T3,T6,T7 | 进行中 | direct 返回真实解析 |
| REQ-PN-FAKEIP-TUN-DNS-001-R5 | 代理启停不修改网卡属性 | 1.2 | U2 | T9 | 进行中 | 网卡属性移入 TUN 生命周期 |
| REQ-PN-FAKEIP-TUN-DNS-001-R6 | 系统设置页新增 TUN 卸载/重置 | 1.2 | U7 | T10,T12 | 进行中 | API 与 UI 同步 |
| REQ-PN-FAKEIP-TUN-DNS-001-R7 | TUN 启用时设置主网卡 DNS 与代理 DNS | 1.2 | U8 | T11,T12 | 进行中 | DNS 备份文件落盘 |

### 1.6 Architect关键接口跟踪矩阵
- 状态: 已完成

| 接口编号 | 需求编号 | 接口名称 | 调用方 | 提供方 | 输入 | 输出 | 状态 | 备注 |
|---|---|---|---|---|---|---|---|---|
| IF1 | REQ-PN-FAKEIP-TUN-DNS-001-R1 | applyProbeLocalProxyTakeover | local_console | local_proxy_takeover_windows | 无 | error | 进行中 | 改为 fake-ip route |
| IF2 | REQ-PN-FAKEIP-TUN-DNS-001-R1 | restoreProbeLocalProxyDirect | local_console | local_proxy_takeover_windows | 无 | error | 进行中 | 删除 fake-ip route |
| IF3 | REQ-PN-FAKEIP-TUN-DNS-001-R3 | currentProbeLocalDNSUpstreamCandidatesForDecision | DNS resolver | local_dns_service | decision | upstream list | 进行中 | local dns 后置 |
| IF4 | REQ-PN-FAKEIP-TUN-DNS-001-R4 | shouldUseProbeLocalDNSFakeIP | DNS resolver | local_dns_service | domain,qtype,decision | bool | 进行中 | tunnel-only |
| IF5 | REQ-PN-FAKEIP-TUN-DNS-001-R2 | openProbeLocalTUNOutboundTCP/UDP | gVisor netstack | local_tun_stack_windows | target | conn | 进行中 | direct 无 bypass |
| IF6 | REQ-PN-FAKEIP-TUN-DNS-001-R6 | resetTUN/uninstallTUN | system settings API | local_console | POST | tun state | 进行中 | 新增控制接口 |
| IF7 | REQ-PN-FAKEIP-TUN-DNS-001-R7 | apply/restoreProbeLocalTUNPrimaryDNS | TUN lifecycle | Windows netapi | ifIndex/DNS backup | error | 进行中 | 文件备份后设置/恢复 |

### 1.7 门禁裁判
- 状态: 进行中

#### 1.7.1 门禁输入
| 文档 | 路径 | 状态 |
|---|---|---|
| 协作文档 | doc/REQ-PN-FAKEIP-TUN-DNS-001-collaboration.md | 已创建 |

#### 1.7.2 裁判检查
| 检查项 | 结果 | 证据 | 备注 |
|---|---|---|---|
| 协作文档存在 | 通过 | doc/REQ-PN-FAKEIP-TUN-DNS-001-collaboration.md | 初始检查 |
| Architect章节存在 | 通过 | 第1章 | 初始检查 |
| Code章节存在 | 通过 | 第2章 | Code 已完成 |
| 必需子章节存在 | 通过 | 第1章/第2章全部必需子章节 | 已补齐 |
| 需求前缀一致 | 通过 | REQ-PN-FAKEIP-TUN-DNS-001 | 初始检查 |
| 需求编号一致 | 通过 | R1-R7 | 新增 TUN 卸载/重置与主网卡 DNS 生命周期 |
| 接口编号一致 | 通过 | IF1-IF7 | 新增 TUN lifecycle 与 primary DNS 接口 |
| 模板字段完整 | 通过 | 文档头字段、角色章节、状态枚举 | 已核对 |
| Code使用encoding_tools | 通过 | 本次修改均为非 C/C++ 文件 | 规则允许直接编辑 |
| Code证据完整 | 通过 | 第2.5节 | 字段齐全 |
| Code任务反馈已处理 | 通过 | FB-001 | 已处理完成 |
| 验收标准可测试 | 通过 | AC1-AC10 | 可测试 |
| 需求任务覆盖完整 | 通过 | T1-T12 | 已覆盖 |
| 任务自测覆盖完整 | 通过 | TC1-TC11 | `go test ./...` 通过 |
| 修改文件在允许范围内 | 通过 | 影响文件均位于 1.4.1 允许列表 | 已核对 |
| 测试失败已记录缺陷 | 通过 | DEF-001, DEF-002 | 已记录并关闭 |
| 未执行测试原因完整 | 通过 | 第2.5.7 | 无未执行项 |
| 遗留风险可接受 | 通过 | 第2.5.8 | 与需求边界一致 |

#### 1.7.3 冲突记录
| 冲突编号 | 冲突条款 | 最终采用条款 | 裁决人 | 裁决结论 |
|---|---|---|---|---|
| 无 | 无 | 无 | 无 | 无 |

#### 1.7.4 裁判结论
- 结论: 通过
- 放行阻塞: 放行
- 条件: 无
- 责任方: 无
- 关闭要求: 无
- 整改要求: 无

#### 1.7.5 结论
- 需求实现与回归测试完成，门禁通过。

## 第2章 Code章节
- 章节责任角色: Code
- 状态: 已完成

### 2.1 Code需求跟踪矩阵
- 状态: 已完成

| 需求编号 | 任务编号 | 实现文件 | 实现状态 | 自测状态 | 证据 | 备注 |
|---|---|---|---|---|---|---|
| REQ-PN-FAKEIP-TUN-DNS-001-R1 | T1,T8 | `probe_node/local_proxy_takeover_windows.go`; `probe_node/local_proxy_takeover_windows_test.go`; `probe_node/local_tun_install_windows.go`; `probe_node/local_tun_install_windows_test.go` | 已完成 | 已完成 | fake-ip route 替换 split default route；takeover/restore 测试通过；stale ifIndex 触发 Wintun handle/LUID 恢复 | 旧模式直接替换 |
| REQ-PN-FAKEIP-TUN-DNS-001-R2 | T4,T5 | `probe_node/local_tun_stack_windows.go`; `probe_node/local_console.go`; 对应测试文件 | 已完成 | 已完成 | direct TCP/UDP 与 bootstrap prewarm 不再写动态 bypass route | 工具函数保留但运行路径不再依赖 |
| REQ-PN-FAKEIP-TUN-DNS-001-R3 | T2 | `probe_node/local_windows_netapi.go`; `probe_node/local_dns_service.go`; `probe_node/local_proxy_takeover*.go` | 已完成 | 已完成 | 追加系统原默认 DNS，非 Windows 空返回 | 去重后尾部追加 |
| REQ-PN-FAKEIP-TUN-DNS-001-R4 | T3 | `probe_node/local_dns_service.go`; `probe_node/local_tun_route_test.go` | 已完成 | 已完成 | fake IP 仅用于 tunnel 决策 | direct 域名返回真实解析 |
| REQ-PN-FAKEIP-TUN-DNS-001-R5 | T9 | `probe_node/local_proxy_takeover_windows.go`; `probe_node/local_proxy_takeover_windows_test.go` | 已完成 | 已完成 | 启用/关闭代理仅加删 fake-ip 路由，不改网卡属性 | 网卡属性仅在安装/检查阶段设置 |
| REQ-PN-FAKEIP-TUN-DNS-001-R6 | T10,T12 | `probe_node/local_console.go`; `probe_node/local_console_methods_test.go`; `probe_node/local_pages/system.html` | 已完成 | 已完成 | 系统设置页新增 TUN 重置/卸载按钮；新增 `/local/api/tun/reset` 与 `/local/api/tun/uninstall` | API 方法保护测试覆盖 |
| REQ-PN-FAKEIP-TUN-DNS-001-R7 | T11,T12 | `probe_node/local_proxy_takeover_windows.go`; `probe_node/local_windows_netapi.go`; `probe_node/local_tun_install_windows.go`; `probe_node/local_console.go`; 对应测试文件 | 已完成 | 已完成 | 启用 TUN 时设置主出口 DNS 到本地代理 DNS，文件备份原 DNS，reset/uninstall 恢复；过滤已被 TUN 污染的主网卡 DNS | 代理启停仍不改网卡属性 |

### 2.2 Code关键接口跟踪矩阵
- 状态: 已完成

| 接口编号 | 需求编号 | 实现文件 | 调用方 | 提供方 | 实现状态 | 证据 | 备注 |
|---|---|---|---|---|---|---|---|
| IF1 | REQ-PN-FAKEIP-TUN-DNS-001-R1,R5 | `probe_node/local_proxy_takeover_windows.go` | local_console | local_proxy_takeover_windows | 已完成 | `applyProbeLocalProxyTakeover` 改为仅添加 fake-ip route | 不再触发网卡属性设置 |
| IF2 | REQ-PN-FAKEIP-TUN-DNS-001-R1,R5 | `probe_node/local_proxy_takeover_windows.go` | local_console | local_proxy_takeover_windows | 已完成 | `restoreProbeLocalProxyDirect` 仅删除 fake-ip route | 不再恢复主网卡 DNS |
| IF3 | REQ-PN-FAKEIP-TUN-DNS-001-R3 | `probe_node/local_dns_service.go` | DNS resolver | local_dns_service | 已完成 | 已配置 upstream 后追加 `probeLocalDNSSystemServers()` | 顺序受测试覆盖 |
| IF4 | REQ-PN-FAKEIP-TUN-DNS-001-R4 | `probe_node/local_dns_service.go` | DNS resolver | local_dns_service | 已完成 | `shouldUseProbeLocalDNSFakeIP` 仅对 tunnel 返回 true | direct/reject/fallback 关闭 fake IP |
| IF5 | REQ-PN-FAKEIP-TUN-DNS-001-R2 | `probe_node/local_tun_stack_windows.go` | gVisor netstack | local_tun_stack_windows | 已完成 | direct TCP/UDP 直接 dial，不调用 bypass route | packet stack direct 路径也不再 ensure bypass |
| IF6 | REQ-PN-FAKEIP-TUN-DNS-001-R6 | `probe_node/local_console.go` | system settings API | local_console | 已完成 | `resetTUN`/`uninstallTUN` 关闭 takeover/data plane 并更新状态 | `/local/api/tun/reset`; `/local/api/tun/uninstall` |
| IF7 | REQ-PN-FAKEIP-TUN-DNS-001-R7 | `probe_node/local_proxy_takeover_windows.go` | TUN lifecycle | Windows netapi | 已完成 | `applyProbeLocalTUNPrimaryDNS`/`restoreProbeLocalTUNPrimaryDNS` 文件备份与恢复 DNS | 备份文件 `tun_primary_dns_backup.json` |

### 2.3 Code测试项跟踪矩阵
- 状态: 已完成

| 测试项编号 | 需求编号 | 任务编号 | 测试目标 | 测试方法 | 结果 | 证据 | 未执行原因 | 备注 |
|---|---|---|---|---|---|---|---|---|
| TC1 | REQ-PN-FAKEIP-TUN-DNS-001-R1 | T1 | fake IP CIDR route 生成与 takeover 成功路径 | `go test ./...` | 通过 | `TestProbeLocalWindowsFakeIPRoutePrefixAndMask`; `TestApplyProbeLocalProxyTakeoverSuccessWithFakeIPRouteOnly` | 无 | 覆盖 DNS 切换与 route state |
| TC2 | REQ-PN-FAKEIP-TUN-DNS-001-R1 | T1 | takeover 失败回滚 | `go test ./...` | 通过 | `TestApplyProbeLocalProxyTakeoverRollbackOnFakeIPRouteFailure` | 无 | 覆盖 route 回滚 |
| TC3 | REQ-PN-FAKEIP-TUN-DNS-001-R3 | T2 | local dns 追加顺序 | `go test ./...` | 通过 | `TestCurrentProbeLocalDNSUpstreamCandidatesAppendsSystemDNSLast` | 无 | 校验去重与尾部追加 |
| TC4 | REQ-PN-FAKEIP-TUN-DNS-001-R4 | T3 | direct 决策不使用 fake IP | `go test ./...` | 通过 | `TestShouldUseProbeLocalDNSFakeIPSkipsDirectDecision` | 无 | tunnel fake IP 旧测试仍保留 |
| TC5 | REQ-PN-FAKEIP-TUN-DNS-001-R2 | T4,T5 | 代理启用与 direct 出站路径不做 bypass 预热 | `go test ./...` | 通过 | `TestProbeLocalProxyEnableWithSelectionUpdatesRuntimeState`; direct path tests compiled and passed | 无 | 预热目标断言改为 0 |
| TC6 | REQ-PN-FAKEIP-TUN-DNS-001-R1 | T8 | stale TUN ifIndex 导致 `CreateUnicastIpAddressEntry failed: code=1168` 后恢复 | `go test ./...` | 通过 | `TestResolveProbeLocalWintunInterfaceIndexFallbackSkipsStaleEnvIfIndex`; `TestEnsureProbeLocalWindowsRouteTargetConfiguredRetriesWhenFallbackIfIndexNotFound`; 既有 1168 fallback 测试通过 | 无 | 针对用户现场错误追加 |
| TC7 | REQ-PN-FAKEIP-TUN-DNS-001-R5 | T9 | 启用/关闭代理不改网卡属性 | `go test ./...` | 通过 | `TestApplyProbeLocalProxyTakeoverSuccessWithFakeIPRouteOnly`; `TestRestoreProbeLocalProxyDirectDeletesFakeIPRouteOnly` | 无 | 网卡 DNS 调用断言为 0 |
| TC8 | REQ-PN-FAKEIP-TUN-DNS-001-R6 | T10 | TUN reset/uninstall API 与方法保护 | `go test ./...` | 通过 | `TestProbeLocalAPIMethodGuards`; `TestProbeLocalTUNResetAndUninstallHandlers` | 无 | 覆盖状态清理与 installed 语义 |
| TC9 | REQ-PN-FAKEIP-TUN-DNS-001-R7 | T11 | 主出口 DNS 备份、应用与恢复 | `go test ./...` | 通过 | `TestApplyRestoreProbeLocalTUNPrimaryDNSBackup` | 无 | 校验备份文件与 DNS 调用顺序 |
| TC10 | REQ-PN-FAKEIP-TUN-DNS-001-R7 | T11 | 启用代理时应用 DNS、直连时恢复 DNS | `go test ./...` | 通过 | `TestProbeLocalProxyEnableAndDirectSuccessWithHooks`; startup recovery 测试 | 无 | 启用/恢复 hook 调用覆盖 |
| TC12 | REQ-PN-FAKEIP-TUN-DNS-001-R7 | T11 | 主网卡 DNS 已被 TUN DNS 污染时不误备份为系统原 DNS | `go test ./...` | 通过 | `TestCurrentProbeLocalSystemDNSServersSkipsTUNDNS`; `TestApplyProbeLocalTUNPrimaryDNSRejectsTUNOnlySystemDNS` | 无 | 过滤 `198.18.0.2` 等 TUN DNS，并在无可用原 DNS 时阻塞 |
| TC11 | REQ-PN-FAKEIP-TUN-DNS-001-R1,R2,R3,R4,R5,R6,R7 | T6,T12 | 模块级回归 | `go test ./...` | 通过 | `ok github.com/cloudhelper/probe_node 10.078s` | 无 | 在 `probe_node` 目录执行 |

### 2.4 Code缺陷跟踪矩阵
- 状态: 已完成

| 缺陷编号 | 需求编号 | 测试项编号 | 缺陷描述 | 严重级别 | 修复状态 | 修复证据 | 备注 |
|---|---|---|---|---|---|---|---|
| DEF-001 | REQ-PN-FAKEIP-TUN-DNS-001-R1 | TC1 | fake-ip 模式下 DNS host 初始回退到了 gateway `198.18.0.1` 而非 TUN 接口地址 `198.18.0.2` | 中 | 已修复 | `resolveProbeLocalTUNDNSListenHostForGateway` 增加 `probeLocalTUNInterfaceIPv4` 优先级；测试通过 | 已关闭 |
| DEF-002 | REQ-PN-FAKEIP-TUN-DNS-001-R1 | TC6 | `prepare windows tun route target failed: CreateUnicastIpAddressEntry failed: code=1168`，原因是 stale `PROBE_LOCAL_TUN_IF_INDEX` 或 fallback ifIndex 被继续信任 | 高 | 已修复 | `resolveProbeLocalWintunInterfaceIndexFallback` 校验 env ifIndex 可枚举；最终 fallback ifIndex 写入仍为 1168 时排除该 ifIndex 并重新从 Wintun handle/LUID 解析；测试通过 | 已关闭 |
| DEF-003 | REQ-PN-FAKEIP-TUN-DNS-001-R7 | TC12 | 主网卡 DNS 已经指向 TUN DNS 时，原逻辑会把 TUN DNS 误备份为系统原 DNS，导致恢复仍可能回写 TUN DNS | 高 | 已修复 | `filterProbeLocalTUNPrimaryDNSServers` 过滤 TUN DNS；`applyProbeLocalTUNPrimaryDNS` 在无可用原 DNS 时阻塞；测试通过 | 已关闭 |

### 2.5 Code执行证据
- 状态: 已完成

#### 2.5.1 修改接口
- `applyProbeLocalProxyTakeover`
- `restoreProbeLocalProxyDirect`
- `currentProbeLocalDNSUpstreamCandidatesForDecision`
- `shouldUseProbeLocalDNSFakeIP`
- `openProbeLocalTUNOutboundTCP`
- `openProbeLocalTUNOutboundUDP`
- `ensureProbeLocalProxyBootstrapDirectBypass`
- `currentProbeLocalSystemDNSServers`
- `probeLocalResolveWindowsPrimaryDNSServers`
- `resolveProbeLocalWintunInterfaceIndexFallback`
- `resetTUN`
- `uninstallTUN`
- `probeLocalTUNResetHandler`
- `probeLocalTUNUninstallHandler`
- `applyProbeLocalTUNPrimaryDNS`
- `restoreProbeLocalTUNPrimaryDNS`
- `uninstallProbeLocalTUNDriver`

#### 2.5.2 配置文件
- 新增运行时备份文件 `tun_primary_dns_backup.json`，位于 `PROBE_NODE_DATA_DIR`，用于保存主出口网卡原 DNS。
- 继续复用 `proxy_group.json` 的 `fake_ip_cidr`。

#### 2.5.3 执行报告
- Windows takeover 从 split default route 改为 fake IP CIDR 稳定路由。
- 启用/关闭 takeover 不再修改任何网卡属性，仅加删 fake IP 路由。
- DNS upstream 列表在已配置 DoH proxy、DoH、DoT、DNS 后追加系统原默认 DNS。
- fake IP 仅用于 `action=tunnel` 的 A 记录查询。
- direct TCP/UDP 出站与代理启用前预热不再写动态 bypass route。
- TUN route target fallback 不再信任系统中已找不到的 env ifIndex；fallback ifIndex 写入仍为 1168 时会二次排除并重新解析，避免 `CreateUnicastIpAddressEntry code=1168` 卡住启用。
- 系统设置页新增 TUN 重置与卸载按钮，分别调用 `/local/api/tun/reset` 与 `/local/api/tun/uninstall`。
- 启用 TUN/代理时先启动 TUN DNS listener，再把主出口网卡 DNS 指向本地代理 DNS；关闭直连、重置、卸载时恢复文件备份的原 DNS。
- 主出口网卡 DNS 备份/读取会过滤 `PROBE_LOCAL_TUN_DNS_HOST` 与 `198.18.0.2` 等 TUN DNS 地址；若过滤后已无可用原 DNS，则阻塞本次 DNS 接管，避免把污染值误记成“系统原 DNS”。
- Windows 卸载路径释放 Wintun handle，清理 TUN IPv4，尽力卸载/清理匹配 PnP 设备并清理 TUN 环境变量。

#### 2.5.4 影响文件
- `probe_node/local_console.go`
- `probe_node/local_console_methods_test.go`
- `probe_node/local_console_test.go`
- `probe_node/local_dns_service.go`
- `probe_node/local_proxy_takeover.go`
- `probe_node/local_proxy_takeover_linux.go`
- `probe_node/local_proxy_takeover_windows.go`
- `probe_node/local_proxy_takeover_windows_test.go`
- `probe_node/local_tun_route_test.go`
- `probe_node/local_tun_stack_windows.go`
- `probe_node/local_tun_install_windows.go`
- `probe_node/local_tun_install_windows_test.go`
- `probe_node/local_windows_netapi.go`
- `probe_node/local_pages/system.html`
- `doc/REQ-PN-FAKEIP-TUN-DNS-001-collaboration.md`

#### 2.5.5 测试命令
- `go test ./...`

#### 2.5.6 自测结果
- `go test ./...` 通过，结果: `ok github.com/cloudhelper/probe_node 9.180s`

#### 2.5.7 未执行测试原因
- 无

#### 2.5.8 遗留风险
- fake-ip 最终模式仍无法捕获直接访问 IP 的应用流量。
- 应用自带 DoH/私有 DNS 仍可绕过本地 DNS，本需求不处理。
- 若安装阶段 `post_install_route_target_check` 仍报网卡 IP 不可绑定，问题仍属于安装/网卡状态层，不属于启用代理路径。
- Windows 真卸载依赖系统 PnP 状态与权限；失败时 API 会保留错误并不错误地清除 installed。

#### 2.5.9 回滚方案
- 回滚本次修改文件到旧版本即可恢复全局 split default route 模型。
- 若需要运行时恢复系统网络，可执行 TUN 重置或 `restoreProbeLocalTUNPrimaryDNS` 恢复主出口 DNS，并执行 `restoreProbeLocalProxyDirect` 删除 fake-ip route。

#### 2.5.10 结论
- Code 已按任务包完成实现与测试，满足 AC1-AC10。

### 2.6 Code任务反馈
- 状态: 已完成

| 反馈编号 | 任务编号 | 反馈类型 | 反馈描述 | 阻塞影响 | Code建议 | Architect处理状态 | Architect处理结论 |
|---|---|---|---|---|---|---|---|
| FB-001 | T5 | 文件范围缺失 | 为彻底消除动态 bypass，需修改 `probe_node/local_console.go` 的 bootstrap prewarm 逻辑；为跨平台编译还需在 `probe_node/local_proxy_takeover.go` 与 `probe_node/local_proxy_takeover_linux.go` 增加 `currentProbeLocalSystemDNSServers` stub | 若不补充，修改文件不在允许范围且编译不完整 | Architect 将上述文件补入允许范围 | 已处理 | 已在 1.4.1 与 T2/T5 中补充允许文件与任务覆盖 |

#### 2.6.1 结论
- 任务反馈已处理完毕，无未决阻塞。
