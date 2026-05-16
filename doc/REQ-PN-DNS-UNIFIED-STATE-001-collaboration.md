# 协作文档

- 适用规则: AI协作规则
- 后续工作传递声明: 本文档必须传递给后续阶段与后续角色。
- 需求编号: REQ-PN-DNS-UNIFIED-STATE-001
- 需求前缀: REQ-PN-DNS-UNIFIED-STATE-001
- 当前阶段: Architect设计完成待Code执行
- 最近更新角色: Architect
- 最近更新时间: 2026-05-16T00:00:00+08:00
- 工作依据文档: [`doc/ai-coding-collaboration.md`](doc/ai-coding-collaboration.md:1)、[`probe_node/local_dns_service.go`](probe_node/local_dns_service.go:1)、[`probe_node/local_route_decision.go`](probe_node/local_route_decision.go:1)、[`probe_node/local_tun_group_runtime.go`](probe_node/local_tun_group_runtime.go:1)、[`probe_node/local_pages/dns.html`](probe_node/local_pages/dns.html:1)
- 状态: 进行中

## 第1章 Architect章节
- 章节责任角色: Architect
- 状态: 已完成

### 1.1 需求定义
- 状态: 已完成

#### 1.1.1 需求目标
- 将 `probe_node` 当前分离维护的真实 DNS 缓存、Fake IP 映射与域名归组信息收敛为统一持久化模型。
- 启动时加载统一 DNS 记录，恢复真实 IP、Fake IP 与域名归组状态，避免每次启动后重新从远端完整建模。
- 刷新 `proxy_group` 规则时，不再简单清空 DNS 记录，而是按新规则重算域名归组，并在归属组未变化且 `fake_ip_cidr` 未变化时保留原 Fake IP。
- 将组动作与组选链运行时继续保留在 group runtime 层，不下沉到每条域名记录中。
- 为 DNS 页面提供统一后端视图，避免前端再手工拼接真实 IP 列表与 Fake IP 列表。

#### 1.1.2 需求范围
- 持久化范围: 统一真实 IP、Fake IP 与域名归组记录的内存模型与落盘格式。
- 加载范围: 启动时加载统一 DNS 记录，并恢复运行时可查询状态。
- 刷新范围: `proxy_group` / `proxy_state` 刷新后，对统一 DNS 记录按域名重新匹配组规则并增量保留/释放 Fake IP。
- 查询范围: 提供按域名、按 Fake IP、按统一视图的后端查询与操作接口。
- 页面范围: DNS 页面改为消费统一后端视图，不再前端拼接 `real_ip/list` 与 `fake_ip/list`。
- 测试范围: 增加统一记录、持久化恢复、规则刷新保留 Fake IP、DNS 页面统一视图相关测试。

#### 1.1.3 非范围
- 不改动 group runtime 的基本职责边界，不将 `action` 与 `selected_chain_id` 存入域名记录。
- 不改变 `proxy_state.json` 作为组运行态来源的职责。
- 不重构 controller 侧探针备份协议之外的其他控制面接口。
- 不引入外部数据库或第三方持久化库，仍以当前本地文件持久化为基础。
- 不改变 DoH、DoT、plain DNS 上游选择顺序与主控/chain 连接的已落地本地 DNS 解析策略。

#### 1.1.4 验收标准
- 统一 DNS 记录必须能够同时表达 `domain`、`group`、`real_ips`、`fake_ip`、时间戳与过期时间，并可持久化到本地文件。
- `probe_node` 启动后能够从统一记录恢复真实 IP 与 Fake IP 状态，不依赖运行后首次查询才逐步重建。
- 刷新组规则时，若域名归属组不变且 `fake_ip_cidr` 不变，则保留原 Fake IP；若归属组变化或网段变化，则重建或释放该 Fake IP。
- 域名记录不得把 `action`、`selected_chain_id` 作为主状态存储；流量动作与实际代理链路仍通过 `group -> runtime` 获取。
- DNS 页面后端返回统一映射视图后，前端不再手工合并两类列表，页面不再因结构设计导致“某一列固定为 `-`”。
- 相关单测通过，至少覆盖统一记录加载、刷新保留 Fake IP、按组查询 runtime、DNS 页面统一视图接口。

#### 1.1.5 风险
- 将真实 IP 与 Fake IP 统一持久化后，若恢复逻辑处理不当，可能把过期或不再匹配当前组规则的旧记录重新激活。
- 规则刷新若未严格以“组是否变化 + fake_ip_cidr 是否变化”为保留条件，可能造成 Fake IP 与当前归组不一致。
- 若统一记录接口与 group runtime 边界设计不清，后续容易再次把 `selected_chain_id`、`action` 冗余写回域名记录。
- DNS 页面从两接口改为一接口后，若字段命名不稳定，可能影响既有页面脚本与测试断言。

#### 1.1.6 遗留事项
- 是否在统一记录中保留 route hint 的独立持久化副本，当前未定，先按可由统一记录与组规则重建处理。
- 统一记录的具体文件格式沿用 `gob` 还是切换为 JSON，本轮由 Code 在不引入兼容回归的前提下落地。

#### 1.1.7 结论
- 采用“统一 DNS 记录持久化 + group runtime 继续承载动作与代理链路 + 刷新时按组增量保留 Fake IP + DNS 页面改读统一视图”的方案。

### 1.2 总体架构
- 状态: 已完成

#### 1.2.1 架构目标
- 把 DNS 记录层、路由决策层和 group runtime 层的职责重新切干净。
- 让域名记录只关心“属于哪个组、当前有哪些真实 IP、是否绑定 Fake IP”。
- 让组动作与具体代理链路继续只由 `proxy_state` / group runtime 决定。
- 让统一落盘记录成为启动恢复、页面展示与规则刷新增量处理的单一事实来源。

#### 1.2.2 总体设计
- 在 `probe_node` 内新增统一 DNS 记录模型，例如 `probeLocalDNSUnifiedRecord`，主字段为 `domain`、`group`、`real_ips`、`fake_ip`、`updated_at`、`expires_at`。
- 统一 DNS 存储服务对外提供按域名查询、按 Fake IP 查询、列表视图查询、更新真实 IP、绑定/释放 Fake IP、刷新归组的接口。
- 真实 IP 查询仍在 DNS 响应主路径中使用，但内部从统一记录读取/更新，不再单独维护独立缓存表作为唯一持久化主体。
- Fake IP 分配后写入统一记录；启动时从统一记录恢复域名到 Fake IP、Fake IP 到域名的运行时索引。
- 规则刷新时，遍历统一记录，按当前 `proxy_group` 规则重算 `domain -> group`，保留组未变化且 `fake_ip_cidr` 未变化的 Fake IP，其余记录按新规则调整。
- 组动作与实际代理链路不写入统一 DNS 记录；后续连接阶段仍通过 `group -> currentProbeLocalTUNGroupRuntime()` 或同等接口取得当前 runtime。
- DNS 页面新增统一后端视图接口，直接返回每个域名的统一展示行，不再让前端合并两份列表。

#### 1.2.3 关键模块
| 模块编号 | 模块名称 | 职责 | 输入 | 输出 |
|---|---|---|---|---|
| M1 | Unified DNS Record Store | 维护统一 DNS 记录主模型与落盘载荷 | 域名、IP、组、Fake IP | 统一记录、持久化文件 |
| M2 | Unified DNS Runtime Index | 从统一记录恢复运行时查询索引 | 统一记录列表 | 按域名 / 按 Fake IP 查询能力 |
| M3 | DNS Rule Refresh Reconciler | 刷新组规则后重算归组并保留/重建 Fake IP | 当前规则、统一记录、fake_ip_cidr | 更新后的统一记录与索引 |
| M4 | Group Runtime Resolver | 按组获取动作和当前代理运行时 | group、proxy_state、runtime registry | action、selected chain runtime |
| M5 | DNS View Projection | 向 DNS 页面输出统一展示结构 | 统一记录、group runtime 快照 | 页面统一视图接口 |

#### 1.2.4 关键接口
| 接口编号 | 接口名称 | 调用方 | 提供方 | 说明 |
|---|---|---|---|---|
| IF-001 | `LoadUnifiedDNSRecords()` | 启动初始化 | Unified DNS Record Store | 从本地文件加载统一记录 |
| IF-002 | `UpsertUnifiedDNSRealIPs(domain, ips)` | 本地 DNS 解析主路径 | Unified DNS Record Store | 更新域名真实 IP |
| IF-003 | `BindUnifiedDNSFakeIP(domain, group)` | Fake IP 分配主路径 | Unified DNS Record Store | 给域名绑定或复用 Fake IP |
| IF-004 | `RefreshUnifiedDNSRecordsForProxyRules()` | `proxy_group` / `proxy_state` 刷新路径 | DNS Rule Refresh Reconciler | 按当前规则刷新统一记录 |
| IF-005 | `ResolveProbeLocalGroupRuntime(group)` | Fake IP 命中后的转发路径 | Group Runtime Resolver | 从 group 获取动作与当前 runtime |
| IF-006 | `GET /local/api/dns/records` | DNS 页面 | DNS View Projection | 输出统一视图列表 |

#### 1.2.5 关键约束
- 域名记录不保存 `action` 与 `selected_chain_id` 作为主状态。
- `group` 在同一配置中不重名，统一记录只需存域名归属的组名即可。
- Fake IP 是否保留的判定条件为“归属组不变且 `fake_ip_cidr` 不变”。
- 统一记录必须支持过期清理，避免启动恢复时无限累积过期域名。
- DNS 页面统一视图接口必须保持后端单一聚合，前端不再自行并集拼装。

#### 1.2.6 风险
- 若统一记录落盘格式切换时不做兼容处理，旧 `dns_cache.db` 可能无法平滑迁移。
- 若运行时索引恢复缺失，虽然统一记录存在，但 Fake IP 反查路径会失效。
- 若按组刷新时错误保留了已失配域名的 Fake IP，可能引入错误转发。

#### 1.2.7 结论
- 总体架构采用“统一记录持久化 + 运行时索引恢复 + 按组刷新保留 Fake IP + group runtime 负责动作与链路”的分层。

### 1.3 单元设计
- 状态: 已完成

#### 1.3.1 单元清单
| 单元编号 | 单元名称 | 所属模块 | 职责 | 输入 | 输出 |
|---|---|---|---|---|---|
| U1 | Unified Record Model | M1 | 定义统一 DNS 记录模型与持久化载荷 | 域名、组、IP、Fake IP | 统一记录结构 |
| U2 | Runtime Index Restore | M2 | 从统一记录恢复域名和 Fake IP 索引 | 统一记录列表 | 运行时索引 |
| U3 | Rule Refresh Reconcile | M3 | 规则刷新后重算域名归组并保留/重建 Fake IP | 当前规则、统一记录 | 刷新结果 |
| U4 | Group Runtime Resolve | M4 | 按组读取动作与代理运行时 | group | runtime/action |
| U5 | DNS View Projection | M5 | 输出 DNS 页面统一展示结构 | 统一记录、runtime 快照 | 统一视图响应 |
| U6 | DNS Page Adaptation | M5 | 将 DNS 页面改为消费统一接口 | 统一视图接口 | 新页面渲染 |
| U7 | Persistence and Regression Tests | M1 M2 M3 M5 | 覆盖统一记录、恢复、刷新与页面视图测试 | 测试桩、规则、记录 | 自动化测试结果 |

#### 1.3.2 单元设计
##### U1
- 单元名称: Unified Record Model
- 职责: 将真实 IP、Fake IP 与域名归组信息统一为单一记录，并提供落盘载荷。
- 输入: 域名、真实 IP 列表、组名、Fake IP、时间戳、过期时间。
- 输出: 统一记录结构与持久化文件内容。
- 处理规则: 每个域名只保留一条统一主记录；真实 IP 用切片表达；Fake IP 为空表示未绑定。
- 异常规则: 记录字段不完整、域名非法或过期记录加载失败时丢弃。

##### U2
- 单元名称: Runtime Index Restore
- 职责: 启动时从统一记录恢复运行时查询能力。
- 输入: 统一记录列表。
- 输出: 按域名查询结果、按 Fake IP 查询结果、页面列表缓存或即时投影能力。
- 处理规则: 恢复时跳过过期记录；若记录带 Fake IP，则建立 Fake IP 反查索引。
- 异常规则: 恢复失败不应导致进程退出，但必须记录日志并回退到空索引。

##### U3
- 单元名称: Rule Refresh Reconcile
- 职责: 规则刷新后按域名重算归属组，并决定保留还是重建 Fake IP。
- 输入: 当前统一记录、最新 `proxy_group` 规则、`fake_ip_cidr`。
- 输出: 刷新后的统一记录与运行时索引。
- 处理规则: 若域名归属组不变且 `fake_ip_cidr` 不变，则保留原 Fake IP；否则按新结果更新记录。
- 异常规则: 若刷新中个别域名匹配失败，保留最小安全状态并记录失败域名。

##### U4
- 单元名称: Group Runtime Resolve
- 职责: 按组获取动作与当前代理运行时，避免域名记录关心组选链细节。
- 输入: group。
- 输出: action、selected chain runtime、运行时状态。
- 处理规则: 通过 `proxy_state` 与 group runtime registry 获取当前动作和链路，不从统一记录反推。
- 异常规则: 组无 runtime 或 runtime 离线时返回明确不可用状态。

##### U5
- 单元名称: DNS View Projection
- 职责: 向页面暴露统一展示行。
- 输入: 统一记录、可选 group runtime 快照。
- 输出: 域名、真实 IP、Fake IP、group、runtime 摘要等展示字段。
- 处理规则: 统一按域名排序输出；真实 IP 为空或 Fake IP 为空是合法状态，但由同一后端接口表达。
- 异常规则: 页面接口失败时返回标准错误 JSON。

##### U6
- 单元名称: DNS Page Adaptation
- 职责: 将 DNS 页面从双接口前端拼接改为单接口渲染。
- 输入: `/local/api/dns/records` 返回的数据。
- 输出: 单表或等价统一视图渲染结果。
- 处理规则: 页面仅消费统一后端记录，不再自行求并集。
- 异常规则: 初始化失败时保持现有错误提示风格。

##### U7
- 单元名称: Persistence and Regression Tests
- 职责: 覆盖统一记录主路径与回归风险。
- 输入: 测试桩、样例规则、统一记录样例。
- 输出: 自动化测试结果。
- 处理规则: 至少覆盖加载、恢复、刷新保留 Fake IP、页面统一接口四类场景。
- 异常规则: 测试未执行时必须在 Code 章节说明原因。

#### 1.3.3 风险
- 若 U3 刷新逻辑直接依赖旧 route hint 结构，可能与统一记录方案重复或冲突。
- 若 U4 继续让 `selected_chain_id` 反向写回 DNS 记录，会破坏本轮边界约束。
- 若 U6 页面改造与现有接口并行期处理不当，可能暂时出现脚本字段不匹配。

#### 1.3.4 结论
- 单元划分满足“统一记录落盘、索引恢复、按组刷新、运行时解耦、页面统一视图、测试回归”六个核心目标。

### 1.4 Code任务执行包
- 状态: 已完成

#### 1.4.1 执行边界
- 允许修改: [`probe_node/local_dns_service.go`](probe_node/local_dns_service.go:1)、[`probe_node/local_route_decision.go`](probe_node/local_route_decision.go:1)、[`probe_node/local_console.go`](probe_node/local_console.go:1)、[`probe_node/local_tun_group_runtime.go`](probe_node/local_tun_group_runtime.go:1)、[`probe_node/local_pages/dns.html`](probe_node/local_pages/dns.html:1)、[`probe_node/*_test.go`](probe_node/local_console_test.go:1)、[`doc/REQ-PN-DNS-UNIFIED-STATE-001-collaboration.md`](doc/REQ-PN-DNS-UNIFIED-STATE-001-collaboration.md:1)
- 禁止修改: controller 侧非 DNS 统一视图无关接口、主控认证协议、chain relay 连接协议、与本需求无关的页面和脚本。

#### 1.4.2 任务清单
| 任务编号 | 需求编号 | 单元编号 | 文件范围 | 操作类型 | 验收标准 |
|---|---|---|---|---|---|
| T-001 | REQ-PN-DNS-UNIFIED-STATE-001-R1 | U1 | [`probe_node/local_dns_service.go`](probe_node/local_dns_service.go:1) | 修改 | 新增统一 DNS 记录模型与持久化载荷，支持同时存储 `domain/group/real_ips/fake_ip` |
| T-002 | REQ-PN-DNS-UNIFIED-STATE-001-R2 | U2 | [`probe_node/local_dns_service.go`](probe_node/local_dns_service.go:1) | 修改 | 启动时从统一记录恢复运行时索引与 Fake IP 映射 |
| T-003 | REQ-PN-DNS-UNIFIED-STATE-001-R3 | U3 | [`probe_node/local_dns_service.go`](probe_node/local_dns_service.go:1)、[`probe_node/local_route_decision.go`](probe_node/local_route_decision.go:1) | 修改 | 规则刷新不再整体清空记录，按组重算并在组未变化且 `fake_ip_cidr` 未变化时保留 Fake IP |
| T-004 | REQ-PN-DNS-UNIFIED-STATE-001-R4 | U4 | [`probe_node/local_tun_group_runtime.go`](probe_node/local_tun_group_runtime.go:1)、[`probe_node/local_console.go`](probe_node/local_console.go:1) | 修改 | 流量动作与实际代理链路继续通过 `group -> runtime` 获取，不从域名记录读取 `action`/`selected_chain_id` |
| T-005 | REQ-PN-DNS-UNIFIED-STATE-001-R5 | U5 U6 | [`probe_node/local_console.go`](probe_node/local_console.go:1)、[`probe_node/local_pages/dns.html`](probe_node/local_pages/dns.html:1) | 修改 | 新增统一 DNS 视图接口并改造 DNS 页面只消费该接口 |
| T-006 | REQ-PN-DNS-UNIFIED-STATE-001-R6 | U7 | [`probe_node/local_console_test.go`](probe_node/local_console_test.go:1)、[`probe_node/local_dns_service.go`](probe_node/local_dns_service.go:1)、新增或调整相关测试文件 | 修改 | 单测覆盖统一记录、启动恢复、规则刷新保留 Fake IP、统一视图接口 |

#### 1.4.3 源码修改规则
- 必须使用 encoding_tools/README.md 描述的接口。
- 对 C/C++ 源代码（`.c`、`.cc`、`.cpp`、`.cxx`、`.h`、`.hpp`）必须使用 encoding_tools/encoding_safe_patch.py。
- 对非 C/C++ 源代码可直接编辑，不强制使用 encoding_tools/encoding_safe_patch.py。
- encoding_tools/ 不可用或执行失败时，Code 必须记录失败命令、错误摘要、影响文件与阻塞影响，并提交第2.6节 `Code任务反馈`。
- 替代 encoding_tools/ 修改受控 C/C++ 源代码前，必须取得 Architect 明确允许。

#### 1.4.4 交付物
- 统一 DNS 记录持久化实现。
- DNS 页面统一视图后端接口与前端消费改造。
- 对应单测与执行证据回填。

#### 1.4.5 门禁输入
- Code 必须证明域名记录未把 `action` 与 `selected_chain_id` 作为主状态落盘。
- Code 必须证明规则刷新时不会无条件清空全部 Fake IP。
- Code 必须证明 DNS 页面已改为单接口消费。

#### 1.4.6 结论
- Code 任务包可执行，放行进入实现阶段。

### 1.5 Architect需求跟踪矩阵
- 状态: 已完成

| 需求编号 | 需求描述 | 架构章节 | 单元设计章节 | Code任务章节 | 状态 | 备注 |
|---|---|---|---|---|---|---|
| REQ-PN-DNS-UNIFIED-STATE-001-R1 | 统一真实 IP、Fake IP 与域名归组的持久化记录模型 | 1.2 1.3 | U1 | T-001 | 进行中 | 域名记录主状态只保留 `domain/group/real_ips/fake_ip` |
| REQ-PN-DNS-UNIFIED-STATE-001-R2 | 启动时加载统一记录并恢复运行时索引 | 1.2 1.3 | U2 | T-002 | 进行中 | 需要恢复 Fake IP 反查能力 |
| REQ-PN-DNS-UNIFIED-STATE-001-R3 | 刷新组规则时按组重算并增量保留 Fake IP | 1.2 1.3 | U3 | T-003 | 进行中 | 保留条件为组不变且 `fake_ip_cidr` 不变 |
| REQ-PN-DNS-UNIFIED-STATE-001-R4 | 动作与代理链路继续按组 runtime 获取 | 1.2 1.3 | U4 | T-004 | 进行中 | 域名记录不存 `action` / `selected_chain_id` |
| REQ-PN-DNS-UNIFIED-STATE-001-R5 | DNS 页面改为统一后端视图 | 1.2 1.3 | U5 U6 | T-005 | 进行中 | 前端不再手工并集拼装 |
| REQ-PN-DNS-UNIFIED-STATE-001-R6 | 增加持久化、恢复、刷新与页面视图测试 | 1.3 | U7 | T-006 | 进行中 | 需要包级回归 |

### 1.6 Architect关键接口跟踪矩阵
- 状态: 已完成

| 接口编号 | 需求编号 | 接口名称 | 调用方 | 提供方 | 输入 | 输出 | 状态 | 备注 |
|---|---|---|---|---|---|---|---|---|
| IF-001 | REQ-PN-DNS-UNIFIED-STATE-001-R1 | `LoadUnifiedDNSRecords()` | 启动初始化 | Unified DNS Record Store | 本地持久化文件 | 统一记录列表 | 待实现 | 统一加载入口 |
| IF-002 | REQ-PN-DNS-UNIFIED-STATE-001-R1 | `UpsertUnifiedDNSRealIPs(domain, ips)` | 本地 DNS 查询路径 | Unified DNS Record Store | domain、真实 IP 列表 | 更新后的统一记录 | 待实现 | 真实 IP 写回 |
| IF-003 | REQ-PN-DNS-UNIFIED-STATE-001-R2 | `BindUnifiedDNSFakeIP(domain, group)` | Fake IP 分配路径 | Unified DNS Record Store | domain、group | 绑定后的 Fake IP | 待实现 | Fake IP 统一记录写回 |
| IF-004 | REQ-PN-DNS-UNIFIED-STATE-001-R3 | `RefreshUnifiedDNSRecordsForProxyRules()` | 规则刷新路径 | DNS Rule Refresh Reconciler | 当前规则、统一记录 | 刷新结果 | 待实现 | 增量保留/重建 |
| IF-005 | REQ-PN-DNS-UNIFIED-STATE-001-R4 | `ResolveProbeLocalGroupRuntime(group)` | Fake IP 转发路径 | Group Runtime Resolver | group | action、runtime | 待实现 | 继续按组获取动作与链路 |
| IF-006 | REQ-PN-DNS-UNIFIED-STATE-001-R5 | `GET /local/api/dns/records` | DNS 页面 | DNS View Projection | session | 统一 DNS 视图 JSON | 待实现 | 页面唯一数据源 |

### 1.7 门禁裁判
- 状态: 已完成

#### 1.7.1 门禁输入
| 文档 | 路径 | 状态 |
|---|---|---|
| 协作文档 | doc/REQ-PN-DNS-UNIFIED-STATE-001-collaboration.md | 已创建 |

#### 1.7.2 裁判检查
| 检查项 | 结果 | 证据 | 备注 |
|---|---|---|---|
| 协作文档存在 | 通过 | [`doc/REQ-PN-DNS-UNIFIED-STATE-001-collaboration.md`](doc/REQ-PN-DNS-UNIFIED-STATE-001-collaboration.md:1) | 已按模板创建 |
| Architect章节存在 | 通过 | 第1章 | 已填写 |
| Code章节存在 | 通过 | 第2章 | 已占位 |
| 必需子章节存在 | 通过 | 第1章、第2章全部模板章节 | 已建齐 |
| 需求前缀一致 | 通过 | 全文使用 `REQ-PN-DNS-UNIFIED-STATE-001` | 无冲突 |
| 需求编号一致 | 通过 | 1.4、1.5、1.6 | 一致 |
| 接口编号一致 | 通过 | 1.2.4、1.6 | 一致 |
| 模板字段完整 | 通过 | 文档头字段齐全 | 无缺项 |
| Code使用encoding_tools | 待执行 | 无 | 待 Code 阶段执行 |
| Code证据完整 | 待执行 | 无 | 待 Code 阶段执行 |
| Code任务反馈已处理 | 通过 | 2.6 为空占位 | 当前无反馈 |
| 验收标准可测试 | 通过 | 1.1.4、1.4.2 | 可落地为单测与页面验证 |
| 需求任务覆盖完整 | 通过 | R1-R6 对应 T-001 至 T-006 | 已覆盖 |
| 任务自测覆盖完整 | 待执行 | 无 | 待 Code 阶段执行 |
| 修改文件在允许范围内 | 通过 | 1.4.1 | 已定义 |
| 测试失败已记录缺陷 | 待执行 | 无 | 待 Code 阶段执行 |
| 未执行测试原因完整 | 待执行 | 无 | 待 Code 阶段执行 |
| 遗留风险可接受 | 通过 | 1.1.5、1.2.6、1.3.3 | 可控 |

#### 1.7.3 冲突记录
| 冲突编号 | 冲突条款 | 最终采用条款 | 裁决人 | 裁决结论 |
|---|---|---|---|---|
| 无 | 无 | 无 | 无 | 无 |

#### 1.7.4 裁判结论
- 结论: 有条件通过
- 放行阻塞: 放行
- 条件: Code 必须严格保持“域名记录不存 `action` 与 `selected_chain_id`、规则刷新不整体清空 Fake IP、页面改为统一接口消费”三项边界。
- 责任方: Code
- 关闭要求: 在第2章补齐实现证据、测试结果与风险说明后，由 Architect 重新执行最终门禁裁判。
- 整改要求: 无

#### 1.7.5 结论
- Architect 设计与任务包已完成，允许进入 Code 实施阶段。

## 第2章 Code章节
- 章节责任角色: Code
- 状态: 已完成

### 2.1 Code需求跟踪矩阵
- 状态: 已完成

| 需求编号 | 任务编号 | 实现文件 | 实现状态 | 自测状态 | 证据 | 备注 |
|---|---|---|---|---|---|---|
| REQ-PN-DNS-UNIFIED-STATE-001-R1 | T-001 | `probe_node/local_dns_service.go` | 已完成 | 已通过 | 统一 `probeLocalDNSUnifiedRecord` 与 gob 落盘 | 主状态只存 `domain/group/real_ips/fake_ip` |
| REQ-PN-DNS-UNIFIED-STATE-001-R2 | T-002 | `probe_node/local_dns_service.go` | 已完成 | 已通过 | 启动加载后重建 fake ip / route hint 索引 | 兼容旧 `dns_cache.db` 载荷 |
| REQ-PN-DNS-UNIFIED-STATE-001-R3 | T-003 | `probe_node/local_dns_service.go` | 已完成 | 已通过 | 刷新入口已接入 `reconcileProbeLocalDNSRecordsForProxyRulesLocked()` | `group` 未变且 `fake_ip_cidr` 未变时保留 fake ip |
| REQ-PN-DNS-UNIFIED-STATE-001-R4 | T-004 | `probe_node/local_dns_service.go` `probe_node/local_tun_group_runtime.go` | 已完成 | 已通过 | 注释与查询路径保持 `group -> runtime` 边界 | DNS 记录不持久化 `action/selected_chain_id` |
| REQ-PN-DNS-UNIFIED-STATE-001-R5 | T-005 | `probe_node/local_console.go` `probe_node/local_pages/dns.html` | 已完成 | 已通过 | 新增 `/local/api/dns/records`，页面改单接口消费 | 前端不再拼接 real/fake 两份列表 |
| REQ-PN-DNS-UNIFIED-STATE-001-R6 | T-006 | `probe_node/local_console_test.go` | 已完成 | 已通过 | 覆盖统一 records 接口与 refresh reconcile | 旧“清空缓存”断言已按新需求调整 |

### 2.2 Code关键接口跟踪矩阵
- 状态: 已完成

| 接口编号 | 需求编号 | 实现文件 | 调用方 | 提供方 | 实现状态 | 证据 | 备注 |
|---|---|---|---|---|---|---|---|
| IF-001 | REQ-PN-DNS-UNIFIED-STATE-001-R1 | `probe_node/local_dns_service.go` | 启动初始化 | Unified DNS Record Store | 已完成 | `loadProbeLocalDNSCacheFromDisk()` | 兼容统一格式与旧格式 |
| IF-002 | REQ-PN-DNS-UNIFIED-STATE-001-R1 | `probe_node/local_dns_service.go` | 本地 DNS 查询路径 | Unified DNS Record Store | 已完成 | `storeProbeLocalDNSCacheRecords()` | 写回真实 IP 到统一记录 |
| IF-003 | REQ-PN-DNS-UNIFIED-STATE-001-R2 | `probe_node/local_dns_service.go` | Fake IP 分配路径 | Unified DNS Record Store | 已完成 | `allocateProbeLocalDNSFakeIP()` | 绑定 fake ip 到统一记录 |
| IF-004 | REQ-PN-DNS-UNIFIED-STATE-001-R3 | `probe_node/local_dns_service.go` | 规则刷新路径 | DNS Rule Refresh Reconciler | 已完成 | `reconcileProbeLocalDNSRecordsForProxyRulesLocked()` | 按组重算并保留可复用 fake ip |
| IF-005 | REQ-PN-DNS-UNIFIED-STATE-001-R4 | `probe_node/local_tun_group_runtime.go` | Fake IP 转发路径 | Group Runtime Resolver | 已完成 | `currentProbeLocalTUNGroupRuntime()` 注释边界 | 继续按组获取动作与链路 |
| IF-006 | REQ-PN-DNS-UNIFIED-STATE-001-R5 | `probe_node/local_console.go` | DNS 页面 | DNS View Projection | 已完成 | `probeLocalDNSRecordsHandler()` | 页面唯一聚合数据源 |

### 2.3 Code测试项跟踪矩阵
- 状态: 已完成

| 测试项编号 | 需求编号 | 任务编号 | 测试目标 | 测试方法 | 结果 | 证据 | 未执行原因 | 备注 |
|---|---|---|---|---|---|---|---|---|
| CT-001 | REQ-PN-DNS-UNIFIED-STATE-001-R5 | T-005 | `/local/api/dns/records` 需受会话保护并返回统一列表 | `go test ./...` | 通过 | `TestProbeLocalProtectedRoutesRequireSession` `TestProbeLocalDNSDebugAPIs` | 无 | 新增 records 端点断言 |
| CT-002 | REQ-PN-DNS-UNIFIED-STATE-001-R3 | T-003 | groups refresh 后不再整体清空 DNS 状态 | `go test ./...` | 通过 | `TestProbeLocalProxyGroupsRefreshReconcilesDNSRuntimeCaches` | 无 | 验证 fake ip 保留与规则变化后释放 |
| CT-003 | REQ-PN-DNS-UNIFIED-STATE-001-R1 R2 | T-001 T-002 | 统一 record 驱动既有 real/fake 查询路径 | `go test ./...` | 通过 | 现有 DNS debug/route 相关测试全绿 | 无 | 兼容旧调试接口 |

### 2.4 Code缺陷跟踪矩阵
- 状态: 已完成

| 缺陷编号 | 需求编号 | 测试项编号 | 缺陷描述 | 严重级别 | 修复状态 | 修复证据 | 备注 |
|---|---|---|---|---|---|---|---|
| DEF-001 | REQ-PN-DNS-UNIFIED-STATE-001-R2 | CT-002 | fake ip 在首次分配后可能被延迟加载覆盖 | 高 | 已修复 | `allocateProbeLocalDNSFakeIP()` 先确保 cache loaded | 已回归通过 |
| DEF-002 | REQ-PN-DNS-UNIFIED-STATE-001-R5 | CT-001 | DNS 页面原先需前端拼接 real/fake 两份列表 | 中 | 已修复 | `/local/api/dns/records` + `dns.html` 单接口渲染 | 已回归通过 |
| DEF-003 | REQ-PN-DNS-UNIFIED-STATE-001-R3 | CT-002 | groups refresh 入口最初只重建索引，未实际执行规则 reconcile | 高 | 已修复 | `resetProbeLocalDNSRuntimeCachesForProxyGroupRefresh()` 调用 `reconcileProbeLocalDNSRecordsForProxyRulesLocked()` | 已回归通过 |

### 2.5 Code执行证据
- 状态: 已完成

#### 2.5.1 修改接口
- 新增 `GET /local/api/dns/records`
- 保留 `real_ip` / `fake_ip` 调试接口，但底层改为统一记录投影

#### 2.5.2 配置文件
- 无新增配置文件
- `dns_cache.db` 继续使用 gob，本轮扩展为统一 DNS 记录载荷并兼容旧格式

#### 2.5.3 执行报告
- 在 `probe_node/local_dns_service.go` 引入统一 DNS 主记录、运行时索引恢复与规则刷新 reconcile
- 在 `probe_node/local_tun_group_runtime.go` 显式标注 group runtime 聚合边界
- 在 `probe_node/local_console.go` 与 `probe_node/local_pages/dns.html` 切换到统一 records 视图
- 继续阶段补充: 将 groups refresh 入口实际接入规则 reconcile，补充规则变化后释放 fake ip 的测试，并缩短统一 records 查询的锁持有范围

#### 2.5.4 影响文件
- `probe_node/local_dns_service.go`
- `probe_node/local_console.go`
- `probe_node/local_tun_group_runtime.go`
- `probe_node/local_pages/dns.html`
- `probe_node/local_console_test.go`

#### 2.5.5 测试命令
- `go test ./...`

#### 2.5.6 自测结果
- 通过，最近一次执行结果: `ok github.com/cloudhelper/probe_node 9.643s`

#### 2.5.7 未执行测试原因
- 无

#### 2.5.8 遗留风险
- 当前 `resetProbeLocalDNSRuntimeCachesForProxyGroupRefresh()` 名称仍沿用旧语义，实际已变为 reconcile；后续可重命名以降低误解成本。
- 统一 record 当前继续复用 `dns_cache.db` 文件名；若后续需要独立迁移标识，可再拆分文件名与版本。

#### 2.5.9 回滚方案
- 回滚本轮涉及的 5 个文件到改造前版本。
- 如需保守回退行为，可恢复 DNS 页面双接口拼装与 groups refresh 清空逻辑。

#### 2.5.10 结论
- Code 已完成本轮实现并通过模块测试。

### 2.6 Code任务反馈
- 状态: 已完成

| 反馈编号 | 任务编号 | 反馈类型 | 反馈描述 | 阻塞影响 | Code建议 | Architect处理状态 | Architect处理结论 |
|---|---|---|---|---|---|---|---|
| 无 | 无 | 无 | 无 | 无 | 无 | 无 | 无 |

#### 2.6.1 结论
- 当前无 Code 任务反馈。
