# 协作文档

- 适用规则: AI协作规则
- 后续工作传递声明: 本文档必须传递给后续阶段与后续角色。
- 需求编号: REQ-PN-TUN-LOCAL-BYPASS-001
- 需求前缀: REQ-PN-TUN-LOCAL-BYPASS-001
- 当前角色: Architect / Code
- 工作依据文档: [`doc/ai-coding-collaboration.md`](doc/ai-coding-collaboration.md:1)、[`probe_node/local_proxy_takeover_windows.go`](probe_node/local_proxy_takeover_windows.go:36)、[`probe_node/local_proxy_takeover_linux.go`](probe_node/local_proxy_takeover_linux.go:36)、[`probe_node/local_tun_route.go`](probe_node/local_tun_route.go:40)、[`probe_node/local_proxy_takeover_windows_test.go`](probe_node/local_proxy_takeover_windows_test.go:69)
- 状态: 进行中

## 第1章 Architect章节
- 章节责任角色: Architect
- 状态: 进行中

### 1.1 需求定义
- 状态: 进行中

#### 1.1.1 需求目标
- 在 [`probe_node`](probe_node) TUN 代理启用时，为常见 IPv4 本地网段预置系统级 bypass 路由，降低直连访问局域网资源时被 TUN 抢占造成的体验问题。
- 保持 [`decideProbeLocalRouteForTarget()`](probe_node/local_tun_route.go:40) 的现有 direct 或 tunnel 判定语义不变，本次只增强启动期路由接管编排。
- 保证代理关闭时对称回收新增 bypass 路由，避免残留系统路由污染。

#### 1.1.2 需求范围
- Windows 路径改造聚焦 [`applyProbeLocalProxyTakeover()`](probe_node/local_proxy_takeover_windows.go:36) 与 [`restoreProbeLocalProxyDirect()`](probe_node/local_proxy_takeover_windows.go:89)。
- Linux 路径改造聚焦 [`applyProbeLocalProxyTakeover()`](probe_node/local_proxy_takeover_linux.go:36) 与 [`restoreProbeLocalProxyDirect()`](probe_node/local_proxy_takeover_linux.go:95)。
- 测试范围包含补强 [`probe_node/local_proxy_takeover_windows_test.go`](probe_node/local_proxy_takeover_windows_test.go:69) 与新增 [`probe_node/local_proxy_takeover_linux_test.go`](probe_node/local_proxy_takeover_linux_test.go)。
- 本次仅覆盖 IPv4 私网段 [`10.0.0.0/8`](probe_node/local_proxy_takeover_linux.go:17)、`172.16.0.0/12`、`192.168.0.0/16`。

#### 1.1.3 非范围
- 不调整 [`startProbeLocalTUNDataPlane()`](probe_node/local_tun_dataplane_windows.go:42) 与数据面收发路径。
- 不修改 [`decideProbeLocalRouteForTarget()`](probe_node/local_tun_route.go:40) 的 direct 或 tunnel 策略判断口径。
- 不处理 IPv6 本地网段与 [`169.254.0.0/16`](probe_node/local_proxy_takeover_linux.go:17) 等附加网段。
- 不引入新的 UI 配置入口或用户自定义 bypass 规则能力。

#### 1.1.4 验收标准
- 启用 TUN 代理后，Windows 与 Linux 都会额外写入 [`10.0.0.0/8`](probe_node/local_proxy_takeover_linux.go:17)、`172.16.0.0/12`、`192.168.0.0/16` 的 bypass 路由。
- 关闭 TUN 代理后，上述 bypass 路由会被对应删除，启停过程保持对称。
- 现有半默认接管路由 [`0.0.0.0/1`](probe_node/local_proxy_takeover_linux.go:17) 与 [`128.0.0.0/1`](probe_node/local_proxy_takeover_linux.go:18) 的接管逻辑不回归。
- 现有 direct 单目标绕行能力，如 [`ensureProbeLocalDirectBypassForTarget()`](probe_node/local_tun_stack_windows.go:964) 不受影响。
- 自动化测试覆盖 Windows 成功 回滚 删除路径，以及 Linux 成功 回滚 恢复路径。

#### 1.1.5 风险
- 预置私网 bypass 属于系统路由改写，若路由 metric 处理不当，可能影响用户已有手工路由优先级。
- Windows 与 Linux 的路由命令格式不同，若抽象层设计不当会导致平台差异处理混乱。
- 启用与恢复路径若新增路由列表不一致，可能出现关闭代理后残留局域网路由。

#### 1.1.6 遗留事项
- 后续可评估是否扩展 [`169.254.0.0/16`](probe_node/local_proxy_takeover_linux.go:17) 与 IPv6 本地网段。
- 后续可评估是否将 bypass 网段做成可配置项，而不是当前固定常量。

#### 1.1.7 结论
- 本需求适合在现有代理接管编排层局部扩展实现，不需要触碰路由策略决策层与 TUN 数据面。

### 1.2 总体架构
- 状态: 进行中

#### 1.2.1 架构目标
- 在不改变 [`probe_node/local_tun_route.go`](probe_node/local_tun_route.go:1) 语义的前提下，补齐系统层对本地私网段的静态绕行。
- 让“代理开启时接管默认流量”和“本地网段始终直连”两类路由规则在同一接管入口中统一编排。

#### 1.2.2 总体设计
- Windows: 在 [`applyProbeLocalProxyTakeover()`](probe_node/local_proxy_takeover_windows.go:36) 中，先写入半默认 TUN 接管路由，再写入本地私网 bypass 路由；在 [`restoreProbeLocalProxyDirect()`](probe_node/local_proxy_takeover_windows.go:89) 中按记录顺序反向删除。
- Linux: 在 [`applyProbeLocalProxyTakeover()`](probe_node/local_proxy_takeover_linux.go:36) 中，先写入半默认 TUN 接管路由，再写入本地私网 bypass 路由；在 [`restoreProbeLocalProxyDirect()`](probe_node/local_proxy_takeover_linux.go:95) 中对称删除。
- 路由策略层保持不变，仍由 [`decideProbeLocalRouteForTarget()`](probe_node/local_tun_route.go:40) 对域名与 fake IP 执行 direct 或 tunnel 决策。
- 测试层通过平台各自命令 mock 校验 route add、route change、route delete 的参数与回滚行为。

#### 1.2.3 关键模块
| 模块编号 | 模块名称 | 职责 | 输入 | 输出 |
|---|---|---|---|---|
| M1 | Windows Takeover Route Orchestrator | 编排 Windows TUN 接管与本地网段 bypass 路由 | TUN gateway ifIndex | 系统 route add 或 delete 调用 |
| M2 | Linux Takeover Route Orchestrator | 编排 Linux TUN 接管与本地网段 bypass 路由 | TUN dev gateway | ip route replace 或 del 调用 |
| M3 | Route Restore Symmetry Guard | 维护启停对称与失败回滚 | 已应用路由列表 | 清理后的系统路由状态 |
| M4 | Route Regression Tests | 校验平台路由命令参数与回滚恢复 | mock command output | 自动化测试结果 |

#### 1.2.4 关键接口
| 接口编号 | 接口名称 | 调用方 | 提供方 | 说明 |
|---|---|---|---|---|
| IF-001 | [`applyProbeLocalProxyTakeover()`](probe_node/local_proxy_takeover_windows.go:36) | [`enableProxy()`](probe_node/local_console.go:590) | Windows 接管编排 | 启用 TUN 代理时下发 Windows 接管与 bypass 路由 |
| IF-002 | [`restoreProbeLocalProxyDirect()`](probe_node/local_proxy_takeover_windows.go:89) | [`directProxy()`](probe_node/local_console.go:646) | Windows 恢复编排 | 关闭 TUN 代理时删除 Windows 接管与 bypass 路由 |
| IF-003 | [`applyProbeLocalProxyTakeover()`](probe_node/local_proxy_takeover_linux.go:36) | [`enableProxy()`](probe_node/local_console.go:590) | Linux 接管编排 | 启用 TUN 代理时下发 Linux 接管与 bypass 路由 |
| IF-004 | [`restoreProbeLocalProxyDirect()`](probe_node/local_proxy_takeover_linux.go:95) | [`directProxy()`](probe_node/local_console.go:646) | Linux 恢复编排 | 关闭 TUN 代理时删除 Linux 接管与 bypass 路由 |
| IF-005 | [`ensureProbeLocalDirectBypassForTarget()`](probe_node/local_tun_stack_windows.go:964) | TUN 直连出站路径 | Windows 单目标 direct bypass | 保持现有按目标动态直连绕行能力，不作为本次改造点 |

#### 1.2.5 关键约束
- 本次只能在现有接管入口扩展，不改变 [`probe_node/local_console.go`](probe_node/local_console.go:590) 的启停控制语义。
- 需保持 Windows 与 Linux 侧新增 bypass 网段集合一致。
- 回滚与恢复逻辑必须可在部分路由失败时正确清理已成功写入的前序项。

#### 1.2.6 风险
- Windows 路由表如果已有同前缀项，可能需要沿用 [`ensureProbeLocalWindowsSplitRoute()`](probe_node/local_proxy_takeover_windows.go:138) 类似的 ADD 或 CHANGE 兼容模式。
- Linux 当前没有现成测试文件，新增测试时需保证 mock 粒度足够覆盖 replace 与 del 行为。

#### 1.2.7 结论
- 推荐按 平台接管实现扩展 + 平台测试补齐 两批任务推进。

### 1.3 单元设计
- 状态: 进行中

#### 1.3.1 单元清单
| 单元编号 | 单元名称 | 所属模块 | 职责 | 输入 | 输出 |
|---|---|---|---|---|---|
| U1 | Windows Local CIDR Bypass Unit | M1 | 为 Windows 启动期追加本地私网 bypass 路由 | gateway ifIndex | route add 或 change 调用 |
| U2 | Linux Local CIDR Bypass Unit | M2 | 为 Linux 启动期追加本地私网 bypass 路由 | dev gateway | ip route replace 调用 |
| U3 | Symmetric Restore Unit | M3 | 在 direct 恢复与失败回滚时删除新增本地网段路由 | 已应用路由信息 | 删除命令序列 |
| U4 | Platform Route Test Unit | M4 | 覆盖 Windows 与 Linux 路由接管测试 | mock command hooks | 回归测试结果 |

#### 1.3.2 单元设计
##### 单元编号 U1
- 单元名称: Windows Local CIDR Bypass Unit
- 职责: 在 [`applyProbeLocalProxyTakeover()`](probe_node/local_proxy_takeover_windows.go:36) 中追加私网 bypass 路由。
- 输入: `gateway`、`ifIndex` 与固定 CIDR 列表。
- 输出: 面向系统 `route` 命令的 ADD 或 CHANGE 调用。
- 处理规则: 复用现有 [`ensureProbeLocalWindowsSplitRoute()`](probe_node/local_proxy_takeover_windows.go:138) 风格封装，新增面向 CIDR 的 helper，统一处理 ADD 已存在时的 CHANGE 回退。
- 异常规则: 任一私网 bypass 写入失败时，需回滚本次已成功写入的私网 bypass 与半默认接管路由。

##### 单元编号 U2
- 单元名称: Linux Local CIDR Bypass Unit
- 职责: 在 [`applyProbeLocalProxyTakeover()`](probe_node/local_proxy_takeover_linux.go:36) 中追加私网 bypass 路由。
- 输入: `dev`、`gateway` 与固定 CIDR 列表。
- 输出: 面向 `ip route replace` 的命令调用。
- 处理规则: 为私网 bypass 建立固定列表，按顺序执行 `replace`；与半默认接管共同纳入已应用列表。
- 异常规则: 任一私网路由失败时，需逆序删除本次已成功写入的所有私网 bypass 与半默认接管路由。

##### 单元编号 U3
- 单元名称: Symmetric Restore Unit
- 职责: 保证 [`restoreProbeLocalProxyDirect()`](probe_node/local_proxy_takeover_windows.go:89) 与 [`restoreProbeLocalProxyDirect()`](probe_node/local_proxy_takeover_linux.go:95) 可以对称删除新增 bypass。
- 输入: 启用态记录与固定路由前缀集合。
- 输出: 删除后的系统路由状态。
- 处理规则: 关闭代理时删除顺序应覆盖半默认接管与私网 bypass；缺失路由应按现有 missing tolerant 逻辑忽略。
- 异常规则: 删除单项失败时聚合错误，但继续尝试清理其余项。

##### 单元编号 U4
- 单元名称: Platform Route Test Unit
- 职责: 补齐平台侧路由接管回归测试。
- 输入: mock command output 与期望路由参数。
- 输出: Windows 与 Linux 的自动化测试覆盖。
- 处理规则: Windows 在 [`probe_node/local_proxy_takeover_windows_test.go`](probe_node/local_proxy_takeover_windows_test.go:69) 扩展调用断言；Linux 新增 [`probe_node/local_proxy_takeover_linux_test.go`](probe_node/local_proxy_takeover_linux_test.go) 覆盖成功 回滚 恢复。
- 异常规则: 若命令序列缺失任一私网段断言，应视为测试失败。

#### 1.3.3 风险
- 若固定列表散落在多个 helper 中，后续扩展网段时容易出现 Windows 与 Linux 不一致。
- 若 restore 不复用相同列表源，可能导致启动新增三条而关闭只删除两条的残留问题。

#### 1.3.4 结论
- 建议将私网 bypass 列表常量化，并在启用 恢复 回滚 三条路径统一复用。

### 1.4 Code任务执行包
- 状态: 进行中

#### 1.4.1 执行边界
- 允许修改: [`probe_node/local_proxy_takeover_windows.go`](probe_node/local_proxy_takeover_windows.go:36)、[`probe_node/local_proxy_takeover_linux.go`](probe_node/local_proxy_takeover_linux.go:36)、[`probe_node/local_proxy_takeover_windows_test.go`](probe_node/local_proxy_takeover_windows_test.go:69)、[`probe_node/local_proxy_takeover_linux_test.go`](probe_node/local_proxy_takeover_linux_test.go)
- 禁止修改: [`probe_node/local_tun_route.go`](probe_node/local_tun_route.go:40)、[`probe_node/local_tun_stack_windows.go`](probe_node/local_tun_stack_windows.go:964)、[`probe_node/local_tun_dataplane_windows.go`](probe_node/local_tun_dataplane_windows.go:42)

#### 1.4.2 任务清单
| 任务编号 | 需求编号 | 单元编号 | 文件范围 | 操作类型 | 验收标准 |
|---|---|---|---|---|---|
| T-001 | REQ-PN-TUN-LOCAL-BYPASS-001-R1 | U1 U3 | [`probe_node/local_proxy_takeover_windows.go`](probe_node/local_proxy_takeover_windows.go:36) | 修改 | Windows 启用与关闭代理时可对称新增 删除 [`10.0.0.0/8`](probe_node/local_proxy_takeover_linux.go:17) `172.16.0.0/12` `192.168.0.0/16` bypass 路由 |
| T-002 | REQ-PN-TUN-LOCAL-BYPASS-001-R1 | U2 U3 | [`probe_node/local_proxy_takeover_linux.go`](probe_node/local_proxy_takeover_linux.go:36) | 修改 | Linux 启用与关闭代理时可对称新增 删除 [`10.0.0.0/8`](probe_node/local_proxy_takeover_linux.go:17) `172.16.0.0/12` `192.168.0.0/16` bypass 路由 |
| T-003 | REQ-PN-TUN-LOCAL-BYPASS-001-R1 | U4 | [`probe_node/local_proxy_takeover_windows_test.go`](probe_node/local_proxy_takeover_windows_test.go:69) | 修改 | Windows 测试覆盖成功 回滚 恢复路径，并断言三条私网 bypass 命令存在 |
| T-004 | REQ-PN-TUN-LOCAL-BYPASS-001-R1 | U4 | [`probe_node/local_proxy_takeover_linux_test.go`](probe_node/local_proxy_takeover_linux_test.go) | 新增 | Linux 测试覆盖成功 回滚 恢复路径，并断言三条私网 bypass 命令存在 |

#### 1.4.3 源码修改规则
- 必须使用 `encoding_tools/README.md` 描述的接口。
- 对 C 或 C++ 源代码 `.c` `.cc` `.cpp` `.cxx` `.h` `.hpp` 必须使用 `encoding_tools/encoding_safe_patch.py`。
- 对非 C 或 C++ 源代码可直接编辑，不强制使用 `encoding_tools/encoding_safe_patch.py`。

#### 1.4.4 交付物
- 更新后的 Windows 与 Linux 路由接管实现。
- Windows 回归测试与新增 Linux 路由接管测试。
- 已填写的第2章 Code章节证据。

#### 1.4.5 门禁输入
- Code 需提供四个任务对应的影响文件清单。
- Code 需提供至少一条 Windows 路由接管测试证据与一条 Linux 路由接管测试证据。
- Code 需明确说明未修改 [`probe_node/local_tun_route.go`](probe_node/local_tun_route.go:40) 与 [`probe_node/local_tun_stack_windows.go`](probe_node/local_tun_stack_windows.go:964) 的原因。

#### 1.4.6 结论
- Code 阶段可直接按 T-001 到 T-004 执行。

### 1.5 Architect需求跟踪矩阵
- 状态: 进行中

| 需求编号 | 需求描述 | 架构章节 | 单元设计章节 | Code任务章节 | 状态 | 备注 |
|---|---|---|---|---|---|---|
| REQ-PN-TUN-LOCAL-BYPASS-001-R1 | TUN 代理启动时为常见 IPv4 私网段预置 bypass 并在关闭时对称清理 | 1.2 | 1.3 U1 U2 U3 U4 | 1.4 T-001 T-002 T-003 T-004 | 进行中 | 不改 direct 或 tunnel 决策语义 |

### 1.6 Architect关键接口跟踪矩阵
- 状态: 进行中

| 接口编号 | 需求编号 | 接口名称 | 调用方 | 提供方 | 输入 | 输出 | 状态 | 备注 |
|---|---|---|---|---|---|---|---|---|
| IF-001 | REQ-PN-TUN-LOCAL-BYPASS-001-R1 | Windows 接管启用接口 | [`enableProxy()`](probe_node/local_console.go:590) | [`applyProbeLocalProxyTakeover()`](probe_node/local_proxy_takeover_windows.go:36) | gateway ifIndex | route add 或 change | 进行中 | 扩展私网 bypass |
| IF-002 | REQ-PN-TUN-LOCAL-BYPASS-001-R1 | Windows 接管恢复接口 | [`directProxy()`](probe_node/local_console.go:646) | [`restoreProbeLocalProxyDirect()`](probe_node/local_proxy_takeover_windows.go:89) | 启用态记录 | route delete | 进行中 | 对称删除私网 bypass |
| IF-003 | REQ-PN-TUN-LOCAL-BYPASS-001-R1 | Linux 接管启用接口 | [`enableProxy()`](probe_node/local_console.go:590) | [`applyProbeLocalProxyTakeover()`](probe_node/local_proxy_takeover_linux.go:36) | dev gateway | ip route replace | 进行中 | 扩展私网 bypass |
| IF-004 | REQ-PN-TUN-LOCAL-BYPASS-001-R1 | Linux 接管恢复接口 | [`directProxy()`](probe_node/local_console.go:646) | [`restoreProbeLocalProxyDirect()`](probe_node/local_proxy_takeover_linux.go:95) | 启用态记录 | ip route del | 进行中 | 对称删除私网 bypass |
| IF-005 | REQ-PN-TUN-LOCAL-BYPASS-001-R1 | Windows 单目标直连绕行接口 | TUN 直连路径 | [`ensureProbeLocalDirectBypassForTarget()`](probe_node/local_tun_stack_windows.go:964) | targetAddr | 单目标 host route | 进行中 | 本次不改，仅验证不回归 |

### 1.7 门禁裁判
- 状态: 未开始

#### 1.7.1 门禁输入
| 文档 | 路径 | 状态 |
|---|---|---|
| 协作文档 | [`doc/REQ-PN-TUN-LOCAL-BYPASS-001-collaboration.md`](doc/REQ-PN-TUN-LOCAL-BYPASS-001-collaboration.md) | 已创建 |

#### 1.7.2 裁判检查
| 检查项 | 结果 | 证据 | 备注 |
|---|---|---|---|
| 协作文档存在 | 待检查 | [`doc/REQ-PN-TUN-LOCAL-BYPASS-001-collaboration.md`](doc/REQ-PN-TUN-LOCAL-BYPASS-001-collaboration.md) |  |
| Architect章节存在 | 待检查 | 同上 |  |
| Code章节存在 | 待检查 | 同上 | 由 Code 后续填写 |
| 必需子章节存在 | 待检查 | 同上 |  |
| 需求前缀一致 | 待检查 | 同上 |  |
| 需求编号一致 | 待检查 | 同上 |  |
| 接口编号一致 | 待检查 | 同上 |  |
| 模板字段完整 | 待检查 | 同上 |  |
| Code使用encoding_tools | 待检查 | 待 Code 证据 |  |
| Code证据完整 | 待检查 | 待 Code 证据 |  |

#### 1.7.3 冲突记录
| 冲突编号 | 冲突条款 | 最终采用条款 | 裁决人 | 裁决结论 |
|---|---|---|---|---|
| 无 | 无 | 无 | Architect | 无 |

#### 1.7.4 裁判结论
- 结论: 有条件通过
- 放行阻塞: 阻塞
- 条件: 需由 Code 严格按 1.4 节任务包完成实现与测试，并补齐第2章证据。
- 整改要求: 禁止扩展到 IPv6 或额外网段，禁止改动 [`probe_node/local_tun_route.go`](probe_node/local_tun_route.go:40) 与 [`probe_node/local_tun_stack_windows.go`](probe_node/local_tun_stack_windows.go:964) 的既有语义。

#### 1.7.5 结论
- Architect 规划已完成，可切换到 [`Code`](doc/ai-coding-collaboration.md:51) 阶段实施。

## 第2章 Code章节
- 章节责任角色: Code
- 状态: 完成

### 2.1 Code需求跟踪矩阵
- 状态: 完成

| 需求编号 | 任务编号 | 实现文件 | 实现状态 | 自测状态 | 证据 | 备注 |
|---|---|---|---|---|---|---|
| REQ-PN-TUN-LOCAL-BYPASS-001-R1 | T-001 | [`probe_node/local_proxy_takeover_windows.go`](probe_node/local_proxy_takeover_windows.go:45) | 已完成 | 通过 | [`applyProbeLocalProxyTakeover()`](probe_node/local_proxy_takeover_windows.go:45)、[`restoreProbeLocalProxyDirect()`](probe_node/local_proxy_takeover_windows.go:101) | Windows 启用期追加三条私网 bypass，关闭期对称删除 |
| REQ-PN-TUN-LOCAL-BYPASS-001-R1 | T-002 | [`probe_node/local_proxy_takeover_linux.go`](probe_node/local_proxy_takeover_linux.go:36) | 已完成 | 编译检查通过 | [`probeLocalLinuxTakeoverRoutePrefixes()`](probe_node/local_proxy_takeover_linux.go:134) | Linux route prefix 集合包含半默认与三条私网 bypass |
| REQ-PN-TUN-LOCAL-BYPASS-001-R1 | T-003 | [`probe_node/local_proxy_takeover_windows_test.go`](probe_node/local_proxy_takeover_windows_test.go:139) | 已完成 | 通过 | [`TestApplyProbeLocalProxyTakeoverSuccessWithRouteOnly()`](probe_node/local_proxy_takeover_windows_test.go:180)、[`TestRestoreProbeLocalProxyDirectDeletesLocalBypassRoutes()`](probe_node/local_proxy_takeover_windows_test.go:232) | 覆盖成功 回滚 恢复路径 |
| REQ-PN-TUN-LOCAL-BYPASS-001-R1 | T-004 | [`probe_node/local_proxy_takeover_linux_test.go`](probe_node/local_proxy_takeover_linux_test.go:13) | 已完成 | 编译检查通过，运行受宿主限制 | [`TestApplyProbeLocalProxyTakeoverLinuxSuccessWithLocalBypass()`](probe_node/local_proxy_takeover_linux_test.go:13)、[`TestRestoreProbeLocalProxyDirectLinuxDeletesLocalBypassRoutes()`](probe_node/local_proxy_takeover_linux_test.go:110) | Windows 宿主可交叉编译 Linux 测试二进制，但无法直接执行 Linux 二进制 |

### 2.2 Code关键接口跟踪矩阵
- 状态: 完成

| 接口编号 | 需求编号 | 实现文件 | 调用方 | 提供方 | 实现状态 | 证据 | 备注 |
|---|---|---|---|---|---|---|---|
| IF-001 | REQ-PN-TUN-LOCAL-BYPASS-001-R1 | [`probe_node/local_proxy_takeover_windows.go`](probe_node/local_proxy_takeover_windows.go:45) | [`enableProxy()`](probe_node/local_console.go:590) | [`applyProbeLocalProxyTakeover()`](probe_node/local_proxy_takeover_windows.go:45) | 已完成 | [`probeLocalWindowsLocalBypassRouteDefs()`](probe_node/local_proxy_takeover_windows.go:166) | 复用非 TUN 默认出口作为本地私网 bypass 下一跳 |
| IF-002 | REQ-PN-TUN-LOCAL-BYPASS-001-R1 | [`probe_node/local_proxy_takeover_windows.go`](probe_node/local_proxy_takeover_windows.go:101) | [`directProxy()`](probe_node/local_console.go:646) | [`restoreProbeLocalProxyDirect()`](probe_node/local_proxy_takeover_windows.go:101) | 已完成 | [`deleteProbeLocalWindowsRoute()`](probe_node/local_proxy_takeover_windows.go:199) | 删除半默认接管与私网 bypass 路由 |
| IF-003 | REQ-PN-TUN-LOCAL-BYPASS-001-R1 | [`probe_node/local_proxy_takeover_linux.go`](probe_node/local_proxy_takeover_linux.go:36) | [`enableProxy()`](probe_node/local_console.go:590) | [`applyProbeLocalProxyTakeover()`](probe_node/local_proxy_takeover_linux.go:36) | 已完成 | [`ensureProbeLocalLinuxSplitRoute()`](probe_node/local_proxy_takeover_linux.go:144) | 通过 `ip -4 route replace` 下发五个 prefix |
| IF-004 | REQ-PN-TUN-LOCAL-BYPASS-001-R1 | [`probe_node/local_proxy_takeover_linux.go`](probe_node/local_proxy_takeover_linux.go:95) | [`directProxy()`](probe_node/local_console.go:646) | [`restoreProbeLocalProxyDirect()`](probe_node/local_proxy_takeover_linux.go:95) | 已完成 | [`deleteProbeLocalLinuxSplitRoute()`](probe_node/local_proxy_takeover_linux.go:157) | 对同一 prefix 集合执行删除 |
| IF-005 | REQ-PN-TUN-LOCAL-BYPASS-001-R1 | [`probe_node/local_tun_stack_windows.go`](probe_node/local_tun_stack_windows.go:964) | TUN 直连路径 | [`ensureProbeLocalDirectBypassForTarget()`](probe_node/local_tun_stack_windows.go:964) | 未修改 | [`probe_node/local_tun_stack_windows.go`](probe_node/local_tun_stack_windows.go:964) | 本次只增强启动期系统路由，不改变单目标 direct bypass 语义 |

### 2.3 Code测试项跟踪矩阵
- 状态: 完成

| 测试项编号 | 需求编号 | 任务编号 | 测试目标 | 测试方法 | 结果 | 证据 | 备注 |
|---|---|---|---|---|---|---|---|
| TC-001 | REQ-PN-TUN-LOCAL-BYPASS-001-R1 | T-001 T-003 | Windows 启用成功路径包含三条私网 bypass | 当前平台执行 `go test -run "Test(ApplyProbeLocalProxyTakeover|RestoreProbeLocalProxyDirect)" .` | 通过 | 输出 `ok github.com/cloudhelper/probe_node 0.801s` | 覆盖 route ADD 与状态保存 |
| TC-002 | REQ-PN-TUN-LOCAL-BYPASS-001-R1 | T-001 T-003 | Windows 启用失败时回滚已创建路由 | 当前平台执行同上 | 通过 | [`TestApplyProbeLocalProxyTakeoverRollbackOnLocalBypassFailure()`](probe_node/local_proxy_takeover_windows_test.go:139) | 失败 prefix 不作为已创建项删除 |
| TC-003 | REQ-PN-TUN-LOCAL-BYPASS-001-R1 | T-001 T-003 | Windows 关闭代理删除三条私网 bypass | 当前平台执行同上 | 通过 | [`TestRestoreProbeLocalProxyDirectDeletesLocalBypassRoutes()`](probe_node/local_proxy_takeover_windows_test.go:232) | 同时删除两条半默认路由 |
| TC-004 | REQ-PN-TUN-LOCAL-BYPASS-001-R1 | T-002 T-004 | Linux 启用 恢复 测试代码可在 Linux 目标编译 | Windows 宿主执行 `set GOOS=linux&& go test -c -o linux_takeover.test local_proxy_takeover_linux.go local_proxy_takeover_linux_test.go tmp_linux_takeover_stubs.go` | 通过 | 命令退出码 0 | 仅验证新增 Linux takeover 文件集合，临时 stub 已删除 |
| TC-005 | REQ-PN-TUN-LOCAL-BYPASS-001-R1 | T-002 T-004 | Linux 全包测试运行 | Windows 宿主执行 `set GOOS=linux&& go test -run "Test(ApplyProbeLocalProxyTakeoverLinux|RestoreProbeLocalProxyDirectLinux)" .` | 阻塞 | [`probe_node/local_console_test.go`](probe_node/local_console_test.go:475) | 既有 Windows/Wintun 测试 hook 在 Linux build 下未定义，非本次改造引入 |

### 2.4 Code缺陷跟踪矩阵
- 状态: 完成

| 缺陷编号 | 需求编号 | 测试项编号 | 缺陷描述 | 严重级别 | 修复状态 | 修复证据 | 备注 |
|---|---|---|---|---|---|---|---|
| DEF-001 | REQ-PN-TUN-LOCAL-BYPASS-001-R1 | TC-005 | Linux 全包测试在 Windows 宿主交叉构建时失败，原因是既有 [`local_console_test.go`](probe_node/local_console_test.go:475) 引用 Windows/Wintun 测试 hook 未做 Linux 隔离 | 中 | 未在本任务修复 | 无 | 超出 1.4 执行边界，未修改 [`probe_node/local_console_test.go`](probe_node/local_console_test.go:475) |

### 2.5 Code执行证据
- 状态: 完成

#### 2.5.1 修改接口
- Windows: [`applyProbeLocalProxyTakeover()`](probe_node/local_proxy_takeover_windows.go:45)、[`restoreProbeLocalProxyDirect()`](probe_node/local_proxy_takeover_windows.go:101)、[`probeLocalWindowsLocalBypassRouteDefs()`](probe_node/local_proxy_takeover_windows.go:166)。
- Linux: [`applyProbeLocalProxyTakeover()`](probe_node/local_proxy_takeover_linux.go:36)、[`restoreProbeLocalProxyDirect()`](probe_node/local_proxy_takeover_linux.go:95)、[`probeLocalLinuxTakeoverRoutePrefixes()`](probe_node/local_proxy_takeover_linux.go:134)。

#### 2.5.2 配置文件
- 未新增或修改配置文件；本次固定覆盖 [`10.0.0.0/8`](probe_node/local_proxy_takeover_linux.go:138)、[`172.16.0.0/12`](probe_node/local_proxy_takeover_linux.go:139)、[`192.168.0.0/16`](probe_node/local_proxy_takeover_linux.go:140)。

#### 2.5.3 执行报告
- 已执行 `gofmt -w local_proxy_takeover_windows.go local_proxy_takeover_linux.go local_proxy_takeover_windows_test.go local_proxy_takeover_linux_test.go`。
- 已执行当前平台测试 `go test -run "Test(ApplyProbeLocalProxyTakeover|RestoreProbeLocalProxyDirect)" .`，结果通过。
- 已执行 Linux 目标编译检查 `set GOOS=linux&& go test -c -o linux_takeover.test local_proxy_takeover_linux.go local_proxy_takeover_linux_test.go tmp_linux_takeover_stubs.go`，结果通过，临时 stub 与产物已删除。
- 已尝试 Linux 全包测试 `set GOOS=linux&& go test -run "Test(ApplyProbeLocalProxyTakeoverLinux|RestoreProbeLocalProxyDirectLinux)" .`，结果被既有 [`local_console_test.go`](probe_node/local_console_test.go:475) 平台隔离问题阻塞。

#### 2.5.4 影响文件
- [`probe_node/local_proxy_takeover_windows.go`](probe_node/local_proxy_takeover_windows.go:45)
- [`probe_node/local_proxy_takeover_linux.go`](probe_node/local_proxy_takeover_linux.go:36)
- [`probe_node/local_proxy_takeover_windows_test.go`](probe_node/local_proxy_takeover_windows_test.go:139)
- [`probe_node/local_proxy_takeover_linux_test.go`](probe_node/local_proxy_takeover_linux_test.go:13)
- [`doc/REQ-PN-TUN-LOCAL-BYPASS-001-collaboration.md`](doc/REQ-PN-TUN-LOCAL-BYPASS-001-collaboration.md:234)

#### 2.5.5 自测结果
- 通过: 当前平台 [`go test`](probe_node/local_proxy_takeover_windows_test.go:180) 相关测试。
- 通过: Linux 目标编译检查，验证新增 Linux takeover 文件集合可构建。
- 阻塞: Linux 全包测试受既有 [`local_console_test.go`](probe_node/local_console_test.go:475) Windows/Wintun 测试 hook 影响，未在本任务边界内修复。

#### 2.5.6 结论
- 本次 Code 实现已完成约定的 IPv4 私网 bypass 范围，未修改 [`probe_node/local_tun_route.go`](probe_node/local_tun_route.go:40)、[`probe_node/local_tun_stack_windows.go`](probe_node/local_tun_stack_windows.go:964)、[`probe_node/local_tun_dataplane_windows.go`](probe_node/local_tun_dataplane_windows.go:42) 的既有语义。
