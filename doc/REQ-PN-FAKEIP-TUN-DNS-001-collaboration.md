# 协作文档

- 适用规则: AI协作规则
- 后续工作传递声明: 本文档必须传递给后续阶段与后续角色。
- 需求编号: REQ-PN-FAKEIP-TUN-DNS-001
- 需求前缀: REQ-PN-FAKEIP-TUN-DNS-001
- 当前阶段: Gate
- 最近更新角色: Architect
- 最近更新时间: 2026-05-13 10:15:00 +08:00
- 工作依据文档: doc/ai-coding-collaboration.md; 用户需求: probe node TUN 改为仅承接默认 DNS 并靠 fake IP 导入需代理流量，避免频繁操作路由表；DNS upstream 增加系统原默认 DNS，优先级在已添加 DNS 后边。
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

#### 1.1.2 需求范围
- 修改 Windows 本地代理接管逻辑。
- 修改本地 DNS upstream 候选构造逻辑。
- 修改 fake IP 分配条件，确保 fake IP 仅用于 tunnel 动作。
- 更新单元测试覆盖新路由与 DNS upstream 行为。

#### 1.1.3 非范围
- 不新增旧模式与新模式的 UI 切换。
- 不实现 WFP、DoH 劫持或对应用内置 DNS 的强制拦截。
- 不处理 Linux 接管语义变更。
- 不改变链路协议、链路组配置文件结构或远端 probe chain 行为。

#### 1.1.4 验收标准
- AC1: Windows takeover 不再创建 `0.0.0.0/1` 与 `128.0.0.0/1` 全局 split route。
- AC2: Windows takeover 创建 fake IP CIDR 到 TUN 的稳定路由，并保留局域网段直连路由需求为非必须。
- AC3: TUN direct 出站不再因直连目标新增/删除 /32 bypass route。
- AC4: DNS upstream 候选顺序为已配置 DoH proxy、DoH、DoT、DNS，最后追加系统原默认 DNS。
- AC5: tunnel 域名 A 记录返回 fake IP；direct 域名不再返回 fake IP。
- AC6: `go test ./...` 在 `probe_node` 模块通过。

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

#### 1.2.4 关键接口
| 接口编号 | 接口名称 | 调用方 | 提供方 | 说明 |
|---|---|---|---|---|
| IF1 | applyProbeLocalProxyTakeover | local_console | local_proxy_takeover_windows | 启用 Windows fake-ip TUN 接管 |
| IF2 | restoreProbeLocalProxyDirect | local_console | local_proxy_takeover_windows | 恢复 Windows 接管状态 |
| IF3 | currentProbeLocalDNSUpstreamCandidatesForDecision | DNS resolver | local_dns_service | 返回 DNS upstream 候选 |
| IF4 | shouldUseProbeLocalDNSFakeIP | DNS resolver | local_dns_service | 判断查询是否返回 fake IP |
| IF5 | openProbeLocalTUNOutboundTCP/UDP | gVisor netstack | local_tun_stack_windows | 打开 TUN 出站连接 |

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

#### 1.3.3 风险
- local DNS 读取需要新增 Windows DNS 字段解析，结构体字段必须与 `GetAdaptersAddresses` 兼容。

#### 1.3.4 结论
- 单元设计满足直接切换最终 fake-ip TUN 模式。

### 1.4 Code任务执行包
- 状态: 已完成

#### 1.4.1 执行边界
- 允许修改: `probe_node/local_proxy_takeover_windows.go`; `probe_node/local_proxy_takeover_windows_test.go`; `probe_node/local_windows_netapi.go`; `probe_node/local_dns_service.go`; `probe_node/local_route_decision_test.go`; `probe_node/local_tun_route_test.go`; `probe_node/local_tun_stack_windows.go`; `probe_node/local_tun_stack_windows_test.go`; `probe_node/local_console.go`; `probe_node/local_console_test.go`; `probe_node/local_proxy_takeover.go`; `probe_node/local_proxy_takeover_linux.go`; `doc/REQ-PN-FAKEIP-TUN-DNS-001-collaboration.md`
- 禁止修改: 链路协议文件、控制器接口、Linux takeover 行为、页面 UI、C/C++ 文件、第三方依赖文件。

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

### 1.6 Architect关键接口跟踪矩阵
- 状态: 已完成

| 接口编号 | 需求编号 | 接口名称 | 调用方 | 提供方 | 输入 | 输出 | 状态 | 备注 |
|---|---|---|---|---|---|---|---|---|
| IF1 | REQ-PN-FAKEIP-TUN-DNS-001-R1 | applyProbeLocalProxyTakeover | local_console | local_proxy_takeover_windows | 无 | error | 进行中 | 改为 fake-ip route |
| IF2 | REQ-PN-FAKEIP-TUN-DNS-001-R1 | restoreProbeLocalProxyDirect | local_console | local_proxy_takeover_windows | 无 | error | 进行中 | 删除 fake-ip route |
| IF3 | REQ-PN-FAKEIP-TUN-DNS-001-R3 | currentProbeLocalDNSUpstreamCandidatesForDecision | DNS resolver | local_dns_service | decision | upstream list | 进行中 | local dns 后置 |
| IF4 | REQ-PN-FAKEIP-TUN-DNS-001-R4 | shouldUseProbeLocalDNSFakeIP | DNS resolver | local_dns_service | domain,qtype,decision | bool | 进行中 | tunnel-only |
| IF5 | REQ-PN-FAKEIP-TUN-DNS-001-R2 | openProbeLocalTUNOutboundTCP/UDP | gVisor netstack | local_tun_stack_windows | target | conn | 进行中 | direct 无 bypass |

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
| Code章节存在 | 待评审 | 第2章 | 待 Code 更新 |
| 必需子章节存在 | 待评审 | 全文章节 | 待 Code 更新 |
| 需求前缀一致 | 通过 | REQ-PN-FAKEIP-TUN-DNS-001 | 初始检查 |
| 需求编号一致 | 通过 | R1-R4 | 初始检查 |
| 接口编号一致 | 通过 | IF1-IF5 | 初始检查 |
| 模板字段完整 | 待评审 | 全文 | 待 Code 更新 |
| Code使用encoding_tools | 待评审 | Code证据 | 非 C/C++ 可直接编辑 |
| Code证据完整 | 待评审 | 第2.5节 | 待 Code 更新 |
| Code任务反馈已处理 | 待评审 | 第2.6节 | 待 Code 更新 |
| 验收标准可测试 | 通过 | AC1-AC6 | 可测试 |
| 需求任务覆盖完整 | 通过 | T1-T6 | 已覆盖 |
| 任务自测覆盖完整 | 待评审 | 第2.3节 | 待 Code 更新 |
| 修改文件在允许范围内 | 待评审 | git diff | 待 Code 更新 |
| 测试失败已记录缺陷 | 待评审 | 第2.4节 | 待 Code 更新 |
| 未执行测试原因完整 | 待评审 | 第2.5节 | 待 Code 更新 |
| 遗留风险可接受 | 待评审 | 第2.5节 | 待 Code 更新 |

#### 1.7.3 冲突记录
| 冲突编号 | 冲突条款 | 最终采用条款 | 裁决人 | 裁决结论 |
|---|---|---|---|---|
| 无 | 无 | 无 | 无 | 无 |

#### 1.7.4 裁判结论
- 结论: 有条件通过
- 放行阻塞: 放行
- 条件: Code 必须按第1.4节任务包执行并补齐第2章证据。
- 责任方: Code
- 关闭要求: `go test ./...` 通过，且所有修改文件位于允许范围内。
- 整改要求: 无

#### 1.7.5 结论
- Architect 阶段放行 Code 执行。

## 第2章 Code章节
- 章节责任角色: Code
- 状态: 已完成

### 2.1 Code需求跟踪矩阵
- 状态: 已完成

| 需求编号 | 任务编号 | 实现文件 | 实现状态 | 自测状态 | 证据 | 备注 |
|---|---|---|---|---|---|---|
| REQ-PN-FAKEIP-TUN-DNS-001-R1 | T1 | `probe_node/local_proxy_takeover_windows.go`; `probe_node/local_proxy_takeover_windows_test.go` | 已完成 | 已完成 | fake-ip route 替换 split default route；takeover/restore 测试通过 | 旧模式直接替换 |
| REQ-PN-FAKEIP-TUN-DNS-001-R2 | T4,T5 | `probe_node/local_tun_stack_windows.go`; `probe_node/local_console.go`; 对应测试文件 | 已完成 | 已完成 | direct TCP/UDP 与 bootstrap prewarm 不再写动态 bypass route | 工具函数保留但运行路径不再依赖 |
| REQ-PN-FAKEIP-TUN-DNS-001-R3 | T2 | `probe_node/local_windows_netapi.go`; `probe_node/local_dns_service.go`; `probe_node/local_proxy_takeover*.go` | 已完成 | 已完成 | 追加系统原默认 DNS，非 Windows 空返回 | 去重后尾部追加 |
| REQ-PN-FAKEIP-TUN-DNS-001-R4 | T3 | `probe_node/local_dns_service.go`; `probe_node/local_tun_route_test.go` | 已完成 | 已完成 | fake IP 仅用于 tunnel 决策 | direct 域名返回真实解析 |

### 2.2 Code关键接口跟踪矩阵
- 状态: 已完成

| 接口编号 | 需求编号 | 实现文件 | 调用方 | 提供方 | 实现状态 | 证据 | 备注 |
|---|---|---|---|---|---|---|---|
| IF1 | REQ-PN-FAKEIP-TUN-DNS-001-R1 | `probe_node/local_proxy_takeover_windows.go` | local_console | local_proxy_takeover_windows | 已完成 | `applyProbeLocalProxyTakeover` 改为 fake-ip route + 主出口 DNS 切换 | 记录 routeDefs 与原 DNS |
| IF2 | REQ-PN-FAKEIP-TUN-DNS-001-R1 | `probe_node/local_proxy_takeover_windows.go` | local_console | local_proxy_takeover_windows | 已完成 | `restoreProbeLocalProxyDirect` 删除 fake-ip route 并恢复 DNS | 恢复失败会返回错误 |
| IF3 | REQ-PN-FAKEIP-TUN-DNS-001-R3 | `probe_node/local_dns_service.go` | DNS resolver | local_dns_service | 已完成 | 已配置 upstream 后追加 `probeLocalDNSSystemServers()` | 顺序受测试覆盖 |
| IF4 | REQ-PN-FAKEIP-TUN-DNS-001-R4 | `probe_node/local_dns_service.go` | DNS resolver | local_dns_service | 已完成 | `shouldUseProbeLocalDNSFakeIP` 仅对 tunnel 返回 true | direct/reject/fallback 关闭 fake IP |
| IF5 | REQ-PN-FAKEIP-TUN-DNS-001-R2 | `probe_node/local_tun_stack_windows.go` | gVisor netstack | local_tun_stack_windows | 已完成 | direct TCP/UDP 直接 dial，不调用 bypass route | packet stack direct 路径也不再 ensure bypass |

### 2.3 Code测试项跟踪矩阵
- 状态: 已完成

| 测试项编号 | 需求编号 | 任务编号 | 测试目标 | 测试方法 | 结果 | 证据 | 未执行原因 | 备注 |
|---|---|---|---|---|---|---|---|---|
| TC1 | REQ-PN-FAKEIP-TUN-DNS-001-R1 | T1 | fake IP CIDR route 生成与 takeover 成功路径 | `go test ./...` | 通过 | `TestProbeLocalWindowsFakeIPRoutePrefixAndMask`; `TestApplyProbeLocalProxyTakeoverSuccessWithFakeIPRouteOnly` | 无 | 覆盖 DNS 切换与 route state |
| TC2 | REQ-PN-FAKEIP-TUN-DNS-001-R1 | T1 | takeover 失败回滚 | `go test ./...` | 通过 | `TestApplyProbeLocalProxyTakeoverRollbackOnFakeIPRouteFailure` | 无 | 覆盖 route 回滚 |
| TC3 | REQ-PN-FAKEIP-TUN-DNS-001-R3 | T2 | local dns 追加顺序 | `go test ./...` | 通过 | `TestCurrentProbeLocalDNSUpstreamCandidatesAppendsSystemDNSLast` | 无 | 校验去重与尾部追加 |
| TC4 | REQ-PN-FAKEIP-TUN-DNS-001-R4 | T3 | direct 决策不使用 fake IP | `go test ./...` | 通过 | `TestShouldUseProbeLocalDNSFakeIPSkipsDirectDecision` | 无 | tunnel fake IP 旧测试仍保留 |
| TC5 | REQ-PN-FAKEIP-TUN-DNS-001-R2 | T4,T5 | 代理启用与 direct 出站路径不做 bypass 预热 | `go test ./...` | 通过 | `TestProbeLocalProxyEnableWithSelectionUpdatesRuntimeState`; direct path tests compiled and passed | 无 | 预热目标断言改为 0 |
| TC6 | REQ-PN-FAKEIP-TUN-DNS-001-R1,R2,R3,R4 | T6 | 模块级回归 | `go test ./...` | 通过 | `ok github.com/cloudhelper/probe_node 9.213s` | 无 | 在 `probe_node` 目录执行 |

### 2.4 Code缺陷跟踪矩阵
- 状态: 已完成

| 缺陷编号 | 需求编号 | 测试项编号 | 缺陷描述 | 严重级别 | 修复状态 | 修复证据 | 备注 |
|---|---|---|---|---|---|---|---|
| DEF-001 | REQ-PN-FAKEIP-TUN-DNS-001-R1 | TC1 | fake-ip 模式下 DNS host 初始回退到了 gateway `198.18.0.1` 而非 TUN 接口地址 `198.18.0.2` | 中 | 已修复 | `resolveProbeLocalTUNDNSListenHostForGateway` 增加 `probeLocalTUNInterfaceIPv4` 优先级；测试通过 | 已关闭 |

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

#### 2.5.2 配置文件
- 无新增配置文件。
- 继续复用 `proxy_group.json` 的 `fake_ip_cidr`。

#### 2.5.3 执行报告
- Windows takeover 从 split default route 改为 fake IP CIDR 稳定路由。
- 启用 takeover 时切换主出口网卡 DNS 到本地 TUN DNS 地址，并保存原 DNS 以供恢复。
- DNS upstream 列表在已配置 DoH proxy、DoH、DoT、DNS 后追加系统原默认 DNS。
- fake IP 仅用于 `action=tunnel` 的 A 记录查询。
- direct TCP/UDP 出站与代理启用前预热不再写动态 bypass route。

#### 2.5.4 影响文件
- `probe_node/local_console.go`
- `probe_node/local_console_test.go`
- `probe_node/local_dns_service.go`
- `probe_node/local_proxy_takeover.go`
- `probe_node/local_proxy_takeover_linux.go`
- `probe_node/local_proxy_takeover_windows.go`
- `probe_node/local_proxy_takeover_windows_test.go`
- `probe_node/local_tun_route_test.go`
- `probe_node/local_tun_stack_windows.go`
- `probe_node/local_windows_netapi.go`
- `doc/REQ-PN-FAKEIP-TUN-DNS-001-collaboration.md`

#### 2.5.5 测试命令
- `go test ./...`

#### 2.5.6 自测结果
- `go test ./...` 通过，结果: `ok github.com/cloudhelper/probe_node 9.213s`

#### 2.5.7 未执行测试原因
- 无

#### 2.5.8 遗留风险
- fake-ip 最终模式仍无法捕获直接访问 IP 的应用流量。
- 若主出口网卡原 DNS 为空，恢复阶段仅记录 warning，不强行推断恢复值。
- 应用自带 DoH/私有 DNS 仍可绕过本地 DNS，本需求不处理。

#### 2.5.9 回滚方案
- 回滚本次修改文件到旧版本即可恢复全局 split default route 模型。
- 若需要运行时恢复系统网络，可执行 `restoreProbeLocalProxyDirect` 先删除 fake-ip route 并恢复已记录的主网卡 DNS。

#### 2.5.10 结论
- Code 已按任务包完成实现与测试，满足 AC1-AC6。

### 2.6 Code任务反馈
- 状态: 已完成

| 反馈编号 | 任务编号 | 反馈类型 | 反馈描述 | 阻塞影响 | Code建议 | Architect处理状态 | Architect处理结论 |
|---|---|---|---|---|---|---|---|
| FB-001 | T5 | 文件范围缺失 | 为彻底消除动态 bypass，需修改 `probe_node/local_console.go` 的 bootstrap prewarm 逻辑；为跨平台编译还需在 `probe_node/local_proxy_takeover.go` 与 `probe_node/local_proxy_takeover_linux.go` 增加 `currentProbeLocalSystemDNSServers` stub | 若不补充，修改文件不在允许范围且编译不完整 | Architect 将上述文件补入允许范围 | 已处理 | 已在 1.4.1 与 T2/T5 中补充允许文件与任务覆盖 |

#### 2.6.1 结论
- 任务反馈已处理完毕，无未决阻塞。
