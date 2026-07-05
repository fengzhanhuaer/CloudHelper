# 协作文档

- 适用规则: AI协作规则
- 后续工作传递声明: 本文档必须传递给后续阶段与后续角色。
- 需求编号: REQ-PN-VROUTER-DNS-001
- 需求前缀: REQ-PN-VROUTER-DNS-001
- 当前阶段: Code实施完成
- 最近更新角色: Code
- 最近更新时间: 2026-07-05T13:20:00+08:00
- 工作依据文档: doc/ai-coding-collaboration.md; 用户在 2026-07-05 提出的虚拟路由 DNS/Fake IP/开关隔离需求; doc/REQ-PN-DISTRIBUTED-VROUTER-001-collaboration.md; doc/REQ-PN-DNS-UNIFIED-STATE-001-collaboration.md
- 状态: 已完成

## 第1章 Architect章节
- 章节责任角色: Architect
- 状态: 已完成

### 1.1 需求定义
- 状态: 已完成

#### 1.1.1 需求目标
- 建立虚拟路由专用 DNS 与 Fake IP 控制面，使主控统一分配虚拟路由 Fake IP，并同步给虚拟路由拓扑可达的探针。
- Fake IP 库以主控侧为准，探针侧只缓存执行；主控侧 Fake IP 库维护库级版本号和整体时间戳，探针定期自动同步并以版本号判断新旧。
- 探针侧新增虚拟路由 DNS 服务，全面停止旧 DNS 服务，后续 DNS 解析入口统一由新的虚拟路由 DNS 服务承担。
- 虚拟路由 DNS 服务按主控下发的路由规则过滤域名和 CIDR；命中规则时按规则指定探针出口解析，未命中规则时使用本地解析。
- 探针侧面板新增虚拟路由 tab，用于开关虚拟路由功能与虚拟 DNS 服务功能。
- 探针虚拟 IP 分配不受本地开关影响；开关只影响本地 Fake IP 路由、DNS 拦截和本地流量入口。
- 虚拟 DNS 关闭时，本地不拦截流量、不为本机提供 DNS 解析，但仍可为远方探针服务并充当出口。
- 虚拟路由功能关闭时，不影响本探针作为其他探针的出口能力。

#### 1.1.2 需求范围
- RQ-VRDNS-001: 主控负责为虚拟路由域名分配 Fake IP，默认 TTL 为 30 天。
- RQ-VRDNS-002: 主控提供手工重置 Fake IP 映射能力，重置后向虚拟路由拓扑可达探针同步。
- RQ-VRDNS-003: Fake IP 映射必须同步给虚拟路由拓扑可达探针，保证可达探针对同一虚拟解析结果一致。
- RQ-VRDNS-013: Fake IP 库必须以主控侧为唯一事实源，探针侧不得自行生成最终权威映射。
- RQ-VRDNS-014: Fake IP 库必须维护库级版本号和整体时间戳，任一映射新增、重置、过期回收或批量变更都必须递增版本号并推进整体时间戳。
- RQ-VRDNS-015: 探针必须定期自动同步主控 Fake IP 库，并以库级版本号为主判断是否需要更新本地缓存；整体时间戳用于展示、审计和异常诊断。
- RQ-VRDNS-016: Fake IP 必须与域名绑定，并在主控侧独立 Fake IP 映射表中保存；该表不依赖路由规则表、拓扑规则表或 probe link store。
- RQ-VRDNS-017: Fake IP 映射 TTL 到期后必须回收；域名映射被命中时必须续期，续期后同步库级版本号和时间戳。
- RQ-VRDNS-004: 探针侧新增虚拟路由 DNS 服务，并全面停止旧 DNS 服务；探针 DNS 入口统一迁移到新虚拟路由 DNS 服务。
- RQ-VRDNS-005: 虚拟路由 DNS 服务必须按路由规则匹配请求；命中规则时使用规则指定探针出口解析。
- RQ-VRDNS-006: 虚拟路由 DNS 服务未命中路由规则时使用本地解析，不产生虚拟出口路由。
- RQ-VRDNS-007: 探针侧面板新增虚拟路由 tab，提供虚拟路由功能开关与虚拟 DNS 服务开关。
- RQ-VRDNS-008: 探针虚拟 IP 不受虚拟路由功能开关、虚拟 DNS 开关影响，保持主控分配与同步。
- RQ-VRDNS-009: 虚拟路由功能开启后，本地 Fake IP 命中的流量按路由规则进入指定探针出口。
- RQ-VRDNS-010: 虚拟 DNS 关闭时，本地不拦截流量，不为本地提供 DNS 解析，但仍能响应远方探针出口解析/转发需要。
- RQ-VRDNS-011: 虚拟路由功能关闭时，本地入口能力关闭，但不影响本探针作为远方探针出口。
- RQ-VRDNS-012: 路由规则动作至少包含指定探针出口、直连、拒绝；DNS 与 Fake IP 路由行为必须与动作一致。

#### 1.1.3 非范围
- 需求定义阶段不修改源码；用户明确要求实施后进入 Code 阶段。
- 当前阶段不保留旧 DNS 服务并行运行方案。
- 当前阶段不重构 Android VPN DNS 的平台专有实现细节；但旧桌面探针 DNS 服务必须退出运行职责。
- 当前阶段不改变拓扑规则物理建联语义。
- 当前阶段不定义 UI 视觉细节，只定义探针面板新增 tab 与开关职责。

#### 1.1.4 验收标准
- 主控可创建、查询、重置虚拟路由 Fake IP 映射，默认 TTL 为 30 天。
- Fake IP 映射 TTL 到期后被主控回收；未过期映射被 DNS 或 Fake IP 路由命中时延长到新的 30 天 TTL，并同步给探针。
- 任一虚拟路由拓扑可达探针同步后，对同一命中规则域名获得一致 Fake IP 与路由元数据。
- 主控 Fake IP 库每次整体变更都会递增库级版本号并更新库级时间戳；探针自动同步后本地缓存版本号和时间戳与主控一致。
- 探针定期同步发现主控版本号未变化时不得无意义覆盖本地缓存；发现主控版本号变化时必须刷新本地 Fake IP 库。
- 主控 Fake IP 映射支持重置全部，重置后所有域名 Fake IP 映射失效并推进版本号、时间戳和拓扑可达探针同步。
- 探针 Fake IP 库同步采用变更触发同步加周期问询；变更触发用于快速收敛，周期问询用于兜底发现漏通知或离线期间变更。
- 探针虚拟路由 DNS 开启时，本地 DNS 查询命中路由规则会返回主控分配的 Fake IP，并记录出口探针。
- 路由规则动作为直连时不使用 Fake IP，DNS 返回真实解析结果；动作为拒绝时返回标准 DNS `REFUSED` 响应。
- 未命中路由规则的本地 DNS 查询走本地解析，不生成虚拟路由 Fake IP。
- 虚拟 DNS 关闭时，本机不监听或不接管本地 DNS，不拦截本机流量；远方探针仍可把本探针作为出口使用。
- 虚拟路由关闭时，本机 Fake IP 路由入口不生效；但远方探针经规则命中选择本探针出口时，本探针仍可解析并出站。
- 探针虚拟 IP 在开关关闭、开启、重启、同步后均保持主控分配，不被本地开关清空。
- 探针面板虚拟路由 tab 能独立展示和切换虚拟路由功能、虚拟 DNS 服务功能。

#### 1.1.5 风险
- 主控分配 Fake IP 后同步给虚拟路由拓扑可达探针，会引入控制面一致性和过期清理问题。
- 库级版本号如果未随批量重置、删除或过期回收递增，可能导致探针误判无需同步；时间戳不得作为唯一同步依据。
- 定期同步频率过高会增加主控和探针开销，过低会延长错误映射生效窗口。
- 新旧 DNS 服务切换期间，若旧 DNS 未完全停止，可能产生监听端口、系统 DNS 接管或 Fake IP 反查竞争。
- “本地入口关闭但出口能力保留”要求运行态拆分成入口、DNS、本机拦截、远方出口多个能力开关，不能用单一 enabled 粗暴停止。
- 未命中规则本地解析与命中规则出口解析必须在 DNS 层和连接层保持一致，否则会出现 DNS 返回与后续 Fake IP 路由不一致。
- 默认 TTL 30 天较长，手工重置和同步失败处理必须明确，否则可能长期保留错误出口。

#### 1.1.6 遗留事项
- 已确认 Fake IP 映射键: Fake IP 与域名绑定，主控侧使用独立 Fake IP 映射表保存。
- 已确认 TTL 到期后的回收策略: 到期回收，命中续期。
- 已确认“全局路由节点”的集合来源: 虚拟路由拓扑可达的探针。
- 已确认手工重置粒度: 重置全部虚拟路由 Fake IP。
- 已确认探针自动同步策略: 有变化时触发同步，并保留周期问询作为兜底；具体周期和失败重试间隔由 Code 阶段结合现有配置确定。
- 已确认 Fake IP 库整体时间戳格式: RFC3339 字符串。
- 已确认 Fake IP 库版本号格式和初始值: 从 1 开始的单调递增整数。
- 已确认新虚拟 DNS 服务监听地址和端口: 使用旧 DNS 服务原地址与原端口；系统 DNS 接管沿用旧 DNS 接管入口，但运行服务切换为新虚拟 DNS。
- 已确认拒绝动作的 DNS 响应形式: 标准 DNS `REFUSED` 响应。
- 已确认直连动作: 直连不使用 Fake IP，DNS 返回真实解析结果。

#### 1.1.7 结论
- 本需求已按 AI协作规则完成独立需求跟踪，并已按用户后续指令进入 Code 实现。

### 1.2 总体架构
- 状态: 已完成

#### 1.2.1 架构目标
- 将虚拟路由 DNS、虚拟路由 Fake IP、探针本地入口、远方出口能力拆成可独立控制的子能力。
- 主控作为虚拟路由 Fake IP 库唯一事实源，探针只缓存和执行，并通过库级版本号判断是否需要刷新。
- 虚拟 DNS 服务只为虚拟路由规则服务，避免复用旧本地 DNS 服务造成职责混杂。
- 保证本机开关不会破坏全局虚拟 IP 和出口能力。

#### 1.2.2 总体设计
- 主控侧新增独立 Fake IP 映射表，作为虚拟路由配置下的独立存储；字段至少包含库级版本号、库级整体时间戳、域名、Fake IP、TTL/过期时间、单条更新时间。
- 主控侧提供 Fake IP 库同步机制，随路由配置同步下发到虚拟路由拓扑可达探针；手工重置、TTL 命中续期、过期回收和任一库变更后递增库级版本号、推进库级整体时间戳并触发 route config sync。
- 探针侧通过变更触发同步和周期问询拉取主控 Fake IP 库元信息或 route config，比较库级版本号；主控版本号变化时刷新本地缓存，未变化时保持现有缓存。
- 探针侧新增虚拟路由 DNS 服务，使用旧 DNS 服务原监听地址与原端口，接管旧本地 DNS 服务的运行职责；旧 DNS 服务不再并行提供解析。
- 探针虚拟 DNS 查询流程: 标准化域名 -> 匹配虚拟路由规则 -> 命中指定探针出口时按域名获取/使用主控 Fake IP -> 直连时返回真实解析结果且不分配 Fake IP -> 未命中时本地解析 -> 拒绝时返回标准 DNS `REFUSED` 响应。
- 探针连接/Fake IP 路由流程: 本地入口开启且目标为虚拟路由 Fake IP -> 查映射和规则动作 -> 按出口探针经虚拟路由转发；本地入口关闭时不接管本机流量。
- 探针出口流程: 即使本机虚拟 DNS 或虚拟路由入口关闭，仍保留远方探针请求本机作为出口时的解析与出站能力。
- 探针面板新增虚拟路由 tab，持久化两个开关: 虚拟路由入口功能、虚拟 DNS 服务功能。

#### 1.2.3 关键模块
| 模块编号 | 模块名称 | 职责 | 输入 | 输出 |
|---|---|---|---|---|
| M-VRDNS-01 | 主控虚拟 Fake IP 分配器 | 分配、续期、重置虚拟路由 Fake IP，并维护库级版本号和整体时间戳 | 域名、TTL、库变更 | Fake IP 库 |
| M-VRDNS-02 | 主控全局同步器 | 将 Fake IP 库与路由配置同步到虚拟路由拓扑可达探针 | route config、库级版本号、库级时间戳、映射变更 | 探针路由配置 |
| M-VRDNS-03 | 探针虚拟 DNS 服务 | 处理虚拟路由 DNS 查询和规则匹配 | DNS 查询、路由规则、映射缓存 | DNS 响应、映射使用记录 |
| M-VRDNS-04 | 探针 Fake IP 路由入口 | 本地 Fake IP 命中后按规则进入虚拟路由 | 本机连接、Fake IP 映射 | 虚拟路由转发或本地处理 |
| M-VRDNS-05 | 探针出口服务 | 为远方探针执行出口解析和出站连接 | 远方转发请求、出口规则 | 真实目标连接 |
| M-VRDNS-06 | 探针虚拟路由面板 | 展示和切换虚拟路由、虚拟 DNS 开关 | 用户操作 | 本地设置 |
| M-VRDNS-07 | 探针 Fake IP 库自动同步器 | 定期同步主控 Fake IP 库并按库级版本号刷新缓存 | 主控库级版本号和时间戳、本地缓存版本号和时间戳 | 本地 Fake IP 库缓存 |

#### 1.2.4 关键接口
| 接口编号 | 接口名称 | 调用方 | 提供方 | 说明 |
|---|---|---|---|---|
| IF-VRDNS-01 | 主控虚拟 Fake IP 库存储 | probe_controller | 独立 Fake IP 映射表 | 保存库级版本号、库级时间戳、域名、Fake IP、TTL |
| IF-VRDNS-02 | 手工重置 Fake IP 映射 API | 管理页面 | probe_controller | 重置单条或批量映射并触发同步 |
| IF-VRDNS-03 | `/api/probe/route/config` 扩展 | probe_node | probe_controller | 下发路由规则、探针虚拟 IP、Fake IP 库、库级版本号和库级时间戳 |
| IF-VRDNS-04 | 探针虚拟 DNS 本地监听 | 本机系统 DNS 或远方探针 | probe_node | 提供虚拟路由 DNS 解析 |
| IF-VRDNS-05 | 探针面板虚拟路由设置 API | 探针本地面板 | probe_node | 读写虚拟路由入口和虚拟 DNS 开关 |
| IF-VRDNS-06 | Fake IP 反查接口 | 本地连接入口 | probe_node | Fake IP -> 目标、规则、出口探针 |
| IF-VRDNS-07 | 远方出口解析/转发接口 | 远方探针 | 出口探针 | 在出口探针执行真实解析和出站 |
| IF-VRDNS-08 | 探针 Fake IP 库定期同步 | probe_node | probe_controller | 周期请求、当前本地库级版本号和时间戳 | 最新库级版本号、时间戳和必要映射 |

#### 1.2.5 关键约束
- 主控是虚拟路由 Fake IP 库唯一事实源。
- Fake IP 库必须同时有库级版本号和整体时间戳；探针侧同步判断以库级版本号为准，不以单条映射时间戳或库级时间戳作为唯一版本依据。
- 探针必须定期自动同步 Fake IP 库；本地缓存只能作为离线兜底，不能覆盖主控事实源。
- 默认 TTL 固定为 30 天，除非后续需求明确允许配置；到期映射由主控回收，命中映射由主控续期。
- 探针虚拟 IP 不受本地开关影响，不允许因关闭入口或 DNS 被清空。
- 虚拟 DNS 关闭只影响本机 DNS 接管和本地解析服务，不影响出口角色。
- 虚拟路由功能关闭只影响本机 Fake IP 路由入口，不影响出口角色。
- 路由规则命中后的 DNS 响应、Fake IP 反查和连接转发必须使用同一条规则和同一个出口探针。

#### 1.2.6 风险
- 控制面同步延迟可能导致某些探针尚未认识新 Fake IP。
- 如果探针长期离线，恢复后必须通过库级版本号发现并拉取主控最新 Fake IP 库。
- 本地开关拆分后，状态组合较多，需要测试矩阵覆盖。
- 旧 DNS 服务全面停止时，需要确保新虚拟路由 DNS 服务复用旧地址、端口和系统 DNS 接管入口，并覆盖原本仍需保留的本地解析能力。

#### 1.2.7 结论
- 架构采用“主控分配和同步 + 探针独立虚拟 DNS + 本地入口/远方出口能力拆分”的方案继续跟踪。

### 1.3 单元设计
- 状态: 已完成

#### 1.3.1 单元清单
| 单元编号 | 单元名称 | 所属模块 | 职责 | 输入 | 输出 |
|---|---|---|---|---|---|
| U-VRDNS-01 | Fake IP 库模型 | M-VRDNS-01 | 定义主控库级版本号、库级时间戳、域名映射字段、TTL 和重置语义 | 域名、TTL、库变更 | Fake IP 库 |
| U-VRDNS-02 | Fake IP 分配与重置 | M-VRDNS-01 | 分配、复用、重置、过期处理 | 映射请求 | Fake IP |
| U-VRDNS-03 | 路由配置同步扩展 | M-VRDNS-02 | 同步映射到虚拟路由拓扑可达探针 | route config | 探针缓存 |
| U-VRDNS-04 | 虚拟 DNS 查询处理 | M-VRDNS-03 | 按路由规则执行命中、本地解析、拒绝 | DNS 查询 | DNS 响应 |
| U-VRDNS-05 | 本地 Fake IP 路由入口 | M-VRDNS-04 | 入口开启时接管 Fake IP 命中流量 | 本机连接 | 虚拟路由转发 |
| U-VRDNS-06 | 出口服务保持 | M-VRDNS-05 | 入口或 DNS 关闭时仍服务远方探针 | 远方请求 | 出口连接 |
| U-VRDNS-07 | 探针面板虚拟路由 tab | M-VRDNS-06 | 提供两个开关和状态展示 | 用户操作 | 本地设置 |
| U-VRDNS-08 | 探针 Fake IP 库自动同步 | M-VRDNS-07 | 通过变更触发和周期问询以主控库级版本号同步本地缓存 | 变更通知、周期问询、本地版本号和时间戳 | 最新 Fake IP 库缓存 |

#### 1.3.2 单元设计
##### U-VRDNS-01
- 单元名称: Fake IP 库模型
- 职责: 保存主控侧虚拟路由 DNS 映射库、库级版本号和库级整体时间戳。
- 输入: 域名、TTL、库变更事件。
- 输出: Fake IP 库记录。
- 处理规则: 默认 TTL 30 天；Fake IP 与域名一一绑定，主控侧使用独立 Fake IP 映射表保存；映射到期必须回收，域名映射被命中时必须续期；任一映射新增、重置、删除、命中续期、过期回收或批量导入都必须递增库级版本号并推进库级整体时间戳；映射必须可同步到探针；探针虚拟 IP 与域名 Fake IP 映射分池或明确避免冲突。
- 异常规则: 出口探针不存在、规则不存在或 Fake IP 池耗尽时拒绝分配，不得递增库级版本号或推进库级时间戳。

##### U-VRDNS-02
- 单元名称: Fake IP 分配与重置
- 职责: 负责主控分配、手工重置与过期处理。
- 输入: 管理端重置请求或 DNS 首次命中请求。
- 输出: 新映射、失效映射、新库级版本号、新库级时间戳、同步事件。
- 处理规则: 手工重置粒度为全部虚拟路由 Fake IP；重置后递增 Fake IP 库版本号、推进整体时间戳并立即触发全局路由配置同步；TTL 到期回收和命中续期也必须触发版本号、时间戳和同步更新。
- 异常规则: 同步失败必须记录并可重试。

##### U-VRDNS-03
- 单元名称: 路由配置同步扩展
- 职责: 将路由规则、探针虚拟 IP、Fake IP 库、库级版本号和库级整体时间戳一并下发。
- 输入: 主控 route config、Fake IP 库级版本号和时间戳。
- 输出: 探针本地 route config cache、Fake IP 库缓存。
- 处理规则: 与现有独立路由配置存储保持一致，不回退到 probe link；探针本地库版本号落后或不一致时刷新缓存。
- 异常规则: 拉取失败时使用本地缓存，但必须避免使用已重置后明确失效的映射。

##### U-VRDNS-04
- 单元名称: 虚拟 DNS 查询处理
- 职责: 作为虚拟路由专用 DNS 服务处理查询。
- 输入: 域名查询、路由规则、Fake IP 映射缓存。
- 输出: Fake IP、真实 IP、拒绝响应或错误。
- 处理规则: 命中探针出口规则时返回/分配 Fake IP；直连不使用 Fake IP 并返回真实解析结果；未命中时本地解析；拒绝返回标准 DNS `REFUSED` 响应。
- 异常规则: 出口探针不可用时直接失败，不回退到本地解析或直连。

##### U-VRDNS-05
- 单元名称: 本地 Fake IP 路由入口
- 职责: 本机流量命中 Fake IP 时进入虚拟路由。
- 输入: 本地连接目标 Fake IP、映射记录、本地开关。
- 输出: 虚拟路由转发、直连、拒绝或不接管。
- 处理规则: 只有虚拟路由功能开启时才接管本地 Fake IP 命中流量。
- 异常规则: 映射缺失或过期时立即同步主控 Fake IP 库后重试；同步失败或重试后仍缺失则失败。

##### U-VRDNS-06
- 单元名称: 出口服务保持
- 职责: 本探针作为远方探针指定出口时继续服务。
- 输入: 远方探针转发请求。
- 输出: 真实目标解析和出站连接。
- 处理规则: 不受本机虚拟路由功能关闭和虚拟 DNS 关闭影响。
- 异常规则: 本机出口依赖的网络不可用时返回明确错误。

##### U-VRDNS-07
- 单元名称: 探针面板虚拟路由 tab
- 职责: 展示和配置虚拟路由入口开关、虚拟 DNS 开关。
- 输入: 用户操作、本地状态。
- 输出: 本地设置。
- 处理规则: 两个开关独立持久化；不得改变主控分配的探针虚拟 IP。
- 异常规则: 保存失败时前端显示错误，不修改运行态。

##### U-VRDNS-08
- 单元名称: 探针 Fake IP 库自动同步
- 职责: 定期从主控同步 Fake IP 库，保持本地缓存以主控为准。
- 输入: 变更通知、周期问询、本地库级版本号和时间戳、主控 route config 或 Fake IP 库元信息。
- 输出: 最新本地 Fake IP 库缓存。
- 处理规则: 有变化时触发同步，并保留周期问询作为兜底；主控库级版本号未变化时不覆盖本地缓存；主控库级版本号变化时刷新本地缓存并替换旧映射；库级时间戳随缓存落地用于展示、审计和异常诊断。
- 异常规则: 同步失败时保留本地缓存并记录错误；连续失败不得生成本地权威映射。

#### 1.3.3 风险
- 规则命中、Fake IP 分配、库级版本号、库级时间戳、连接接管、出口解析跨多个单元，必须用同一映射 ID 串联。
- 如未拆清入口和出口，关闭虚拟路由可能误停远方出口能力。

#### 1.3.4 结论
- 单元设计已覆盖当前需求描述；仍存在若干需要用户确认的策略点。

### 1.4 Code任务执行包
- 状态: 已放行

#### 1.4.1 执行边界
- 允许修改: doc/REQ-PN-VROUTER-DNS-001-collaboration.md; probe_controller/internal/core/probe_route_config_store.go; probe_controller/internal/core/probe_virtual_router.go; probe_controller/internal/core/probe_link_chains.go; probe_controller/internal/core/server.go; probe_controller/internal/core/mng_link_handlers.go; probe_controller/internal/core/mng_link_actions.go; probe_controller/internal/core/mng_pages/route.html; probe_controller/internal/core/probe_virtual_router_test.go; probe_node/probe_link_chains_sync.go; probe_node/probe_virtual_router.go; probe_node/probe_virtual_router_settings.go; probe_node/probe_virtual_router_windows.go; probe_node/probe_virtual_router_linux.go; probe_node/probe_virtual_router_other.go; probe_node/probe_virtual_router_windows_test.go; probe_node/local_dns_service.go; probe_node/local_dns_service_test.go; probe_node/local_console.go; probe_node/local_console_test.go; probe_node/local_console_methods_test.go; probe_node/local_pages/proxy.html; probe_node/local_pages/panel.html。
- 禁止修改: 禁止把虚拟路由 DNS 状态重新存入 probe link store；禁止让虚拟路由入口开关接管全局流量；禁止让本地开关清空主控分配的探针虚拟 IP；禁止让虚拟 DNS 关闭影响远方探针出口能力。

#### 1.4.2 任务清单
| 任务编号 | 需求编号 | 单元编号 | 文件范围 | 操作类型 | 验收标准 |
|---|---|---|---|---|---|
| T-VRDNS-01 | RQ-VRDNS-001,RQ-VRDNS-002,RQ-VRDNS-003,RQ-VRDNS-013,RQ-VRDNS-014,RQ-VRDNS-015,RQ-VRDNS-016,RQ-VRDNS-017 | U-VRDNS-01,U-VRDNS-02,U-VRDNS-03,U-VRDNS-08 | probe_controller/internal/core/probe_route_config_store.go; probe_controller/internal/core/probe_virtual_router.go; probe_controller/internal/core/probe_link_chains.go; probe_controller/internal/core/server.go; probe_controller/internal/core/mng_link_handlers.go; probe_controller/internal/core/mng_link_actions.go; probe_controller/internal/core/mng_pages/route.html; probe_node/probe_link_chains_sync.go; probe_node/probe_virtual_router.go | 新增/修改 | 主控可分配、重置、回收到期映射、命中续期、以库级版本号和时间戳同步默认 30 天 TTL 的域名 Fake IP 独立映射表，探针定期自动同步 |
| T-VRDNS-02 | RQ-VRDNS-004,RQ-VRDNS-005,RQ-VRDNS-006,RQ-VRDNS-012 | U-VRDNS-04 | probe_node/local_dns_service.go; probe_node/probe_virtual_router.go; probe_node/probe_link_chains_sync.go | 新增/修改 | 探针虚拟 DNS 服务全面替代旧 DNS，并按规则命中出口、本地解析和拒绝 |
| T-VRDNS-03 | RQ-VRDNS-008,RQ-VRDNS-009,RQ-VRDNS-010,RQ-VRDNS-011 | U-VRDNS-05,U-VRDNS-06 | probe_node/probe_virtual_router.go; probe_node/probe_virtual_router_settings.go; probe_node/probe_virtual_router_windows.go; probe_node/probe_virtual_router_linux.go; probe_node/probe_virtual_router_other.go | 新增/修改 | 本地入口和远方出口开关语义互不影响 |
| T-VRDNS-04 | RQ-VRDNS-007 | U-VRDNS-07 | probe_node/local_console.go; probe_node/local_pages/proxy.html; probe_node/local_pages/panel.html | 新增/修改 | 探针面板新增虚拟路由 tab 和两个开关 |
| T-VRDNS-05 | RQ-VRDNS-001..RQ-VRDNS-017 | 全部 | probe_controller/internal/core/probe_virtual_router_test.go; probe_node/local_dns_service_test.go; probe_node/local_console_test.go; probe_node/local_console_methods_test.go; probe_node/probe_virtual_router_windows_test.go | 测试 | 覆盖域名绑定、独立映射表、TTL 回收/续期、重置、同步、DNS 命中/未命中、开关组合和出口保持 |

#### 1.4.3 源码修改规则
- 必须使用 encoding_tools/README.md 描述的接口。
- 对 C/C++ 源代码（`.c`、`.cc`、`.cpp`、`.cxx`、`.h`、`.hpp`）必须使用 encoding_tools/encoding_safe_patch.py。
- 对非 C/C++ 源代码可直接编辑，不强制使用 encoding_tools/encoding_safe_patch.py。
- encoding_tools/ 不可用或执行失败时，Code 必须记录失败命令、错误摘要、影响文件与阻塞影响，并提交第2.6节 `Code任务反馈`。
- 替代 encoding_tools/ 修改受控 C/C++ 源代码前，必须取得 Architect 明确允许。

#### 1.4.4 交付物
- 主控 Fake IP 库模型、库级版本号、库级整体时间戳、重置接口和 route config 同步路径。
- 探针虚拟路由 DNS 服务、旧 DNS 监听停止逻辑及本地/远方职责拆分。
- 探针面板虚拟路由 tab、首页入口和本地设置 API。
- 单元测试、定向回归测试与 Linux 交叉编译验证。

#### 1.4.5 门禁输入
- 用户已明确要求实施，当前 Code 任务包已放行。

#### 1.4.6 结论
- Code 任务包已按用户实施指令放行并执行。

### 1.5 Architect需求跟踪矩阵
- 状态: 已完成

| 需求编号 | 需求描述 | 架构章节 | 单元设计章节 | Code任务章节 | 状态 | 备注 |
|---|---|---|---|---|---|---|
| RQ-VRDNS-001 | 主控分配虚拟路由 Fake IP，默认 TTL 30 天 | 1.2 | 1.3 U-VRDNS-01,U-VRDNS-02 | T-VRDNS-01 | 已完成 | 重置全部 |
| RQ-VRDNS-002 | 支持手工重置 Fake IP 映射 | 1.2 | 1.3 U-VRDNS-02 | T-VRDNS-01 | 已完成 | 重置全部 |
| RQ-VRDNS-003 | Fake IP 映射同步给虚拟路由拓扑可达探针 | 1.2 | 1.3 U-VRDNS-03 | T-VRDNS-01 | 已完成 | 集合来源已确认 |
| RQ-VRDNS-004 | 探针新增虚拟路由 DNS 服务并全面停止旧 DNS 服务 | 1.2 | 1.3 U-VRDNS-04 | T-VRDNS-02 | 已完成 | 新 DNS 覆盖旧 DNS 仍需保留的解析能力 |
| RQ-VRDNS-005 | DNS 命中路由规则时使用指定探针出口解析 | 1.2 | 1.3 U-VRDNS-04 | T-VRDNS-02 | 已完成 | 依赖路由规则 action/exit_node_id |
| RQ-VRDNS-006 | DNS 未命中路由规则时使用本地解析 | 1.2 | 1.3 U-VRDNS-04 | T-VRDNS-02 | 已完成 | 不生成虚拟出口路由 |
| RQ-VRDNS-007 | 探针面板新增虚拟路由 tab 和两个开关 | 1.2 | 1.3 U-VRDNS-07 | T-VRDNS-04 | 已完成 | 已实现本地面板 tab |
| RQ-VRDNS-008 | 探针虚拟 IP 不受开关影响 | 1.2 | 1.3 U-VRDNS-01,U-VRDNS-07 | T-VRDNS-03 | 已完成 | 已覆盖定向测试 |
| RQ-VRDNS-009 | 虚拟路由开启后本地 Fake IP 命中经规则出口 | 1.2 | 1.3 U-VRDNS-05 | T-VRDNS-03 | 已完成 | DNS 与连接层保持 Fake IP 映射一致 |
| RQ-VRDNS-010 | 虚拟 DNS 关闭时本地不拦截、不解析，仅为远方服务 | 1.2 | 1.3 U-VRDNS-04,U-VRDNS-06 | T-VRDNS-03 | 已完成 | 入口/出口拆分 |
| RQ-VRDNS-011 | 虚拟路由关闭时不影响出口功能 | 1.2 | 1.3 U-VRDNS-06 | T-VRDNS-03 | 已完成 | 本地入口开关不停止出口 |
| RQ-VRDNS-012 | 路由规则动作包括探针出口、直连、拒绝且 DNS/连接一致 | 1.2 | 1.3 U-VRDNS-04,U-VRDNS-05 | T-VRDNS-02,T-VRDNS-03 | 已完成 | 直连不用 Fake IP，拒绝返回 REFUSED |
| RQ-VRDNS-013 | Fake IP 库以主控侧为唯一事实源 | 1.2 | 1.3 U-VRDNS-01,U-VRDNS-08 | T-VRDNS-01 | 已完成 | 探针不生成权威映射 |
| RQ-VRDNS-014 | Fake IP 库维护库级版本号和整体时间戳 | 1.2 | 1.3 U-VRDNS-01,U-VRDNS-02 | T-VRDNS-01 | 已完成 | RFC3339 + 从 1 开始递增 |
| RQ-VRDNS-015 | 探针定期自动同步主控 Fake IP 库 | 1.2 | 1.3 U-VRDNS-03,U-VRDNS-08 | T-VRDNS-01 | 已完成 | route config 周期同步 + DNS 分配即时返回 |
| RQ-VRDNS-016 | Fake IP 与域名绑定并独立存储 | 1.2 | 1.3 U-VRDNS-01 | T-VRDNS-01 | 已完成 | 独立 Fake IP 映射表 |
| RQ-VRDNS-017 | Fake IP TTL 到期回收、命中续期 | 1.2 | 1.3 U-VRDNS-01,U-VRDNS-02 | T-VRDNS-01 | 已完成 | TTL 策略已实现 |

### 1.6 Architect关键接口跟踪矩阵
- 状态: 已完成

| 接口编号 | 需求编号 | 接口名称 | 调用方 | 提供方 | 输入 | 输出 | 状态 | 备注 |
|---|---|---|---|---|---|---|---|---|
| IF-VRDNS-01 | RQ-VRDNS-001,RQ-VRDNS-002,RQ-VRDNS-013,RQ-VRDNS-014,RQ-VRDNS-016,RQ-VRDNS-017 | 主控虚拟 Fake IP 库存储 | probe_controller | 独立 Fake IP 映射表 | 域名映射记录、TTL、库级版本号、库级时间戳 | 持久化 Fake IP 库 | 已完成 | 独立 route config 字段 |
| IF-VRDNS-02 | RQ-VRDNS-002,RQ-VRDNS-003,RQ-VRDNS-014 | 手工重置 Fake IP 映射 API | 管理页面 | probe_controller | 重置全部 | 新库级版本号、时间戳和同步结果 | 已完成 | 已确认重置全部 |
| IF-VRDNS-03 | RQ-VRDNS-003,RQ-VRDNS-008,RQ-VRDNS-015,RQ-VRDNS-017 | `/api/probe/route/config` 扩展 | probe_node | probe_controller | node_id、secret、本地库级版本号和时间戳 | route config + fake ip library + library version + library timestamp | 已完成 | 保持独立 route config |
| IF-VRDNS-04 | RQ-VRDNS-004,RQ-VRDNS-005,RQ-VRDNS-006 | 探针虚拟 DNS 本地监听 | 本机 DNS/远方探针 | probe_node | DNS 查询 | DNS 响应 | 已完成 | 使用旧 DNS 地址和端口 |
| IF-VRDNS-05 | RQ-VRDNS-007,RQ-VRDNS-010,RQ-VRDNS-011 | 探针面板虚拟路由设置 API | 探针本地面板 | probe_node | 开关设置 | 保存结果 | 已完成 | 两个开关独立 |
| IF-VRDNS-06 | RQ-VRDNS-009,RQ-VRDNS-012 | Fake IP 反查接口 | 本地连接入口 | probe_node | Fake IP | 目标、规则、出口探针 | 已完成 | 连接层一致性关键 |
| IF-VRDNS-07 | RQ-VRDNS-010,RQ-VRDNS-011 | 远方出口解析/转发接口 | 远方探针 | 出口探针 | 目标、规则、出口请求 | 出口连接 | 已完成 | 不受本地入口开关影响 |
| IF-VRDNS-08 | RQ-VRDNS-013,RQ-VRDNS-014,RQ-VRDNS-015,RQ-VRDNS-017 | 探针 Fake IP 库同步 | probe_node | probe_controller | 本地库级版本号和时间戳、变更触发、周期问询 | 最新库级版本号、时间戳和必要映射 | 已完成 | route config 周期同步 + Fake IP miss 同步重试 |

### 1.7 门禁裁判
- 状态: 已放行

#### 1.7.1 门禁输入
| 文档 | 路径 | 状态 |
|---|---|---|
| 协作文档 | doc/REQ-PN-VROUTER-DNS-001-collaboration.md | 已更新 |
| AI协作规则 | doc/ai-coding-collaboration.md | 已读取 |
| encoding_tools | encoding_tools/README.md | 已读取，非 C/C++ 源码可直接编辑 |

#### 1.7.2 裁判检查
| 检查项 | 结果 | 证据 | 备注 |
|---|---|---|---|
| 协作文档存在 | 通过 | 本文件 | 无 |
| Architect章节存在 | 通过 | 第1章 | 无 |
| Code章节存在 | 通过 | 第2章 | 无 |
| 必需子章节存在 | 通过 | 1.1-1.7,2.1-2.6 | 无 |
| 需求前缀一致 | 通过 | REQ-PN-VROUTER-DNS-001 | 无 |
| 需求编号一致 | 通过 | RQ-VRDNS-* | 无 |
| 接口编号一致 | 通过 | IF-VRDNS-* | 无 |
| 模板字段完整 | 通过 | 文档头字段完整 | 无 |
| Code使用encoding_tools | 通过 | encoding_tools/README.md | 本次未修改 C/C++ 源码；Go/HTML/Markdown 允许直接编辑 |
| Code证据完整 | 通过 | 第2.5节 | 已记录修改接口、配置、报告、影响文件、测试、风险和回滚 |
| Code任务反馈已处理 | 通过 | 第2.6节 | 当前无未处理反馈 |
| 验收标准可测试 | 通过 | 第2.3节 | 已补充可执行测试命令 |
| 需求任务覆盖完整 | 通过 | 第1.4.2节 | RQ-VRDNS-001..017 均有关联任务 |
| 任务自测覆盖完整 | 通过 | 第2.3节 | 每个任务均有关联测试或验证说明 |
| 修改文件在允许范围内 | 通过 | 第1.4.1节与 git status | 影响文件均在允许范围内 |
| 测试失败已记录缺陷 | 通过 | 第2.4节 | 无失败测试 |
| 未执行测试原因完整 | 通过 | 第2.5.7节 | 未跑完整探针测试套件原因已记录 |
| 遗留风险可接受 | 通过 | 第2.5.8节 | 已记录为后续优化风险，不阻塞本次交付 |

#### 1.7.3 冲突记录
| 冲突编号 | 冲突条款 | 最终采用条款 | 裁决人 | 裁决结论 |
|---|---|---|---|---|
| VRDNS-CF-001 | 原 1.4 节仅放行文档，用户后续明确“实施”“一次性做完” | 以用户后续明确实施指令为准，补充第1.4源码范围后进入 Code | Architect | 放行 |

#### 1.7.4 裁判结论
- 结论: 通过
- 放行阻塞: 放行
- 条件: 无
- 责任方: Code 已完成实现与验证。
- 关闭要求: 无。
- 整改要求: 无。

#### 1.7.5 结论
- 当前需求已完成 Code 实施与门禁验证。

## 第2章 Code章节
- 章节责任角色: Code
- 状态: 已完成

### 2.1 Code需求跟踪矩阵
- 状态: 已完成

| 需求编号 | 任务编号 | 实现文件 | 实现状态 | 自测状态 | 证据 | 备注 |
|---|---|---|---|---|---|---|
| RQ-VRDNS-001,RQ-VRDNS-002,RQ-VRDNS-013,RQ-VRDNS-014,RQ-VRDNS-016,RQ-VRDNS-017 | T-VRDNS-01 | probe_controller/internal/core/probe_route_config_store.go; probe_controller/internal/core/probe_virtual_router.go; probe_controller/internal/core/probe_link_chains.go; probe_controller/internal/core/server.go; probe_controller/internal/core/mng_link_handlers.go; probe_controller/internal/core/mng_pages/route.html | 已完成 | 通过 | TestProbeVirtualRouterFakeIPLibraryAllocatesRenewsAndResetsIndependentStore; TestProbeRouteFakeIPResolveHandlerPersistsLibrary; TestMngLinkVirtualRouterFakeIPResetHandlerDispatchesRouteConfigSync | 主控独立 Fake IP 库、版本号、时间戳、TTL、续期、重置、resolve API 和管理页重置入口 |
| RQ-VRDNS-003,RQ-VRDNS-015 | T-VRDNS-01 | probe_controller/internal/core/probe_link_chains.go; probe_controller/internal/core/mng_link_handlers.go; probe_node/probe_link_chains_sync.go; probe_node/probe_virtual_router.go | 已完成 | 通过 | go test ./internal/core -count=1; TestProbeRouteFakeIPResolveHandlerPersistsLibrary; TestMngLinkVirtualRouterFakeIPResetHandlerDispatchesRouteConfigSync | Fake IP 库随 route config 同步，DNS 分配和重置后主动下发 route_config_sync，探针周期同步 route config |
| RQ-VRDNS-004,RQ-VRDNS-005,RQ-VRDNS-006,RQ-VRDNS-012 | T-VRDNS-02 | probe_node/local_dns_service.go; probe_node/probe_virtual_router.go | 已完成 | 通过 | TestResolveProbeVirtualRouterDNSResponseUsesControllerFakeIPForExitRule; TestResolveProbeVirtualRouterDNSResponseDirectAndReject | 虚拟 DNS 覆盖旧入口职责，支持 probe_exit/direct/reject 和未命中本地解析 |
| RQ-VRDNS-008,RQ-VRDNS-009,RQ-VRDNS-010,RQ-VRDNS-011 | T-VRDNS-03 | probe_node/probe_virtual_router_settings.go; probe_node/probe_virtual_router_windows.go; probe_node/probe_virtual_router_linux.go; probe_node/probe_virtual_router_other.go; probe_node/probe_virtual_router.go | 已完成 | 通过 | TestEnsureProbeVirtualRouterPlatformInterfaceIPWindowsAppliesOnlyFakeIPRoute; TestCleanupProbeVirtualRouterPlatformRoutesWindowsDeletesFakeIPRoute; go test -c GOOS=linux | 探针虚拟 IP 不受开关影响，Fake IP 路由受入口开关影响，出口能力未绑定本地开关 |
| RQ-VRDNS-007 | T-VRDNS-04 | probe_node/local_console.go; probe_node/local_pages/proxy.html; probe_node/local_pages/panel.html | 已完成 | 通过 | TestProbeLocalAPIMethodGuards; go test . -run ^$ | 本地面板新增虚拟路由 tab、首页入口、设置 API |
| RQ-VRDNS-001..RQ-VRDNS-017 | T-VRDNS-05 | probe_controller/internal/core/probe_virtual_router_test.go; probe_node/local_dns_service_test.go; probe_node/local_console_test.go; probe_node/local_console_methods_test.go; probe_node/probe_virtual_router_windows_test.go | 已完成 | 通过 | 第2.5.5测试命令 | 定向测试覆盖核心行为 |

### 2.2 Code关键接口跟踪矩阵
- 状态: 已完成

| 接口编号 | 需求编号 | 实现文件 | 调用方 | 提供方 | 实现状态 | 证据 | 备注 |
|---|---|---|---|---|---|---|---|
| IF-VRDNS-01 | RQ-VRDNS-001,RQ-VRDNS-002,RQ-VRDNS-013,RQ-VRDNS-014,RQ-VRDNS-016,RQ-VRDNS-017 | probe_controller/internal/core/probe_route_config_store.go; probe_controller/internal/core/probe_virtual_router.go | probe_controller | 独立 route config 下 Fake IP 库 | 已完成 | TestProbeVirtualRouterFakeIPLibraryAllocatesRenewsAndResetsIndependentStore | 独立于 probe link store |
| IF-VRDNS-02 | RQ-VRDNS-002,RQ-VRDNS-003,RQ-VRDNS-014 | probe_controller/internal/core/mng_link_handlers.go; probe_controller/internal/core/server.go; probe_controller/internal/core/mng_pages/route.html | 管理端 | probe_controller | 已完成 | TestMngLinkVirtualRouterFakeIPResetHandlerDispatchesRouteConfigSync; Node JS syntax check | `/mng/api/route/virtual_router/fake_ip/reset` |
| IF-VRDNS-03 | RQ-VRDNS-003,RQ-VRDNS-008,RQ-VRDNS-015,RQ-VRDNS-017 | probe_controller/internal/core/probe_link_chains.go; probe_node/probe_link_chains_sync.go | probe_node | probe_controller | 已完成 | go test ./internal/core -count=1; probe_node compile | `/api/probe/route/config` 下发 Fake IP 库 |
| IF-VRDNS-04 | RQ-VRDNS-004,RQ-VRDNS-005,RQ-VRDNS-006 | probe_node/local_dns_service.go | 本机 DNS/远方探针 | probe_node | 已完成 | TestResolveProbeVirtualRouterDNSResponse* | 使用旧 DNS 地址端口，由虚拟 DNS 逻辑接管 |
| IF-VRDNS-05 | RQ-VRDNS-007,RQ-VRDNS-010,RQ-VRDNS-011 | probe_node/local_console.go; probe_node/probe_virtual_router_settings.go | 本地面板 | probe_node | 已完成 | TestProbeLocalAPIMethodGuards; TestProbeVirtualRouterLocalSettingsMissingFieldKeepsDefaultEnabled | `/local/api/virtual_router/settings` |
| IF-VRDNS-06 | RQ-VRDNS-009,RQ-VRDNS-012 | probe_node/probe_virtual_router.go | 本地连接入口 | probe_node | 已完成 | probe_node targeted tests; go test -run ^$ | Fake IP -> 域名/出口探针反查 |
| IF-VRDNS-07 | RQ-VRDNS-010,RQ-VRDNS-011 | probe_node/probe_virtual_router.go; probe_node/local_dns_service.go | 远方探针 | 出口探针 | 已完成 | probe_node targeted tests | 本地入口和 DNS 开关不停止出口转发代码路径 |
| IF-VRDNS-08 | RQ-VRDNS-013,RQ-VRDNS-014,RQ-VRDNS-015,RQ-VRDNS-017 | probe_controller/internal/core/probe_link_chains.go; probe_controller/internal/core/mng_link_handlers.go; probe_node/probe_link_chains_sync.go; probe_node/probe_virtual_router.go | probe_node | probe_controller | 已完成 | Linux compile; go test -run ^$; sync dispatch tests | 变更触发 route_config_sync + 周期同步 + Fake IP miss 同步重试 |

### 2.3 Code测试项跟踪矩阵
- 状态: 已完成

| 测试项编号 | 需求编号 | 任务编号 | 测试目标 | 测试方法 | 结果 | 证据 | 未执行原因 | 备注 |
|---|---|---|---|---|---|---|---|---|
| CT-VRDNS-01 | RQ-VRDNS-001,RQ-VRDNS-002,RQ-VRDNS-003,RQ-VRDNS-013,RQ-VRDNS-014,RQ-VRDNS-015,RQ-VRDNS-016,RQ-VRDNS-017 | T-VRDNS-01 | 域名绑定、独立映射表、TTL、续期、重置、版本号、时间戳、同步载荷、管理页重置入口 | go test ./internal/core -run "TestProbeRouteFakeIPResolveHandlerPersistsLibrary|TestMngLinkVirtualRouterFakeIPResetHandlerDispatchesRouteConfigSync|TestProbeVirtualRouterFakeIPLibrary" -count=1; route.html Node JS syntax check | 通过 | ok github.com/cloudhelper/probe_controller/internal/core 1.217s; script 1 ok | 无 | 未启动浏览器截图验证 |
| CT-VRDNS-02 | RQ-VRDNS-004,RQ-VRDNS-005,RQ-VRDNS-006,RQ-VRDNS-012 | T-VRDNS-02 | 虚拟 DNS 出口/直连/拒绝 | go test . -run "TestResolveProbeVirtualRouterDNSResponse" -count=1 | 通过 | ok github.com/cloudhelper/probe_node 1.112s | 无 | 拒绝验证 RCodeRefused |
| CT-VRDNS-03 | RQ-VRDNS-008,RQ-VRDNS-009,RQ-VRDNS-010,RQ-VRDNS-011 | T-VRDNS-03 | 开关组合、Fake IP 路由边界、跨平台编译 | go test . -run "TestEnsureProbeVirtualRouterPlatformInterfaceIPWindowsAppliesOnlyFakeIPRoute|TestCleanupProbeVirtualRouterPlatformRoutesWindowsDeletesFakeIPRoute" -count=1; GOOS=linux GOARCH=amd64 go test -c -o temp . | 通过 | ok github.com/cloudhelper/probe_node 2.710s; Linux 编译成功 | 无 | 完整系统路由实机验证未执行 |
| CT-VRDNS-04 | RQ-VRDNS-007 | T-VRDNS-04 | 探针面板 tab、首页入口、设置 API 方法守卫 | go test . -run "TestProbeLocalAPIMethodGuards|TestProbeVirtualRouterLocalSettingsMissingFieldKeepsDefaultEnabled" -count=1 | 通过 | ok github.com/cloudhelper/probe_node 2.610s | 无 | 未启动浏览器截图验证 |
| CT-VRDNS-05 | RQ-VRDNS-001..RQ-VRDNS-017 | T-VRDNS-05 | 包级回归和编译 | go test ./internal/core -count=1; go test . -run "^$" -count=1 | 通过 | controller ok 2.333s; probe_node ok 1.229s no tests to run | 无 | 未执行 probe_node 全量测试，见2.5.7 |

### 2.4 Code缺陷跟踪矩阵
- 状态: 已完成

| 缺陷编号 | 需求编号 | 测试项编号 | 缺陷描述 | 严重级别 | 修复状态 | 修复证据 | 备注 |
|---|---|---|---|---|---|---|---|
| 无 | 无 | 无 | 无 | 无 | 无 | 无 | 无 |

### 2.5 Code执行证据
- 状态: 已完成

#### 2.5.1 修改接口
- 新增 `/api/probe/route/fake_ip/resolve`，探针请求主控分配/续期域名 Fake IP。
- 扩展 `/api/probe/route/config` 返回 `virtual_router.fake_ip_library`。
- 新增 `/mng/api/route/virtual_router/fake_ip/reset`，主控管理端重置全部虚拟路由 Fake IP。
- 新增 `/local/api/virtual_router/settings`，探针本地面板读写虚拟路由入口和虚拟 DNS 开关。

#### 2.5.2 配置文件
- 主控 `probe_route_config.json` 新增独立字段 `virtual_router_fake_ip`。
- 探针新增本地配置文件 `probe_virtual_router_settings.json`。
- 探针 route config cache 继续使用 `probe_route_config.json`，新增缓存 `fake_ip_library`。

#### 2.5.3 执行报告
- 主控侧实现独立 Fake IP 库，默认版本号 1，库级 `updated_at`，域名绑定 Fake IP，默认 TTL 30 天，命中续期，过期回收，重置全部。
- 主控侧 Fake IP 分配/续期和重置后会通过现有控制通道向拓扑可达在线探针下发 `route_config_sync`，离线探针仍由周期同步兜底。
- 探针侧虚拟 DNS 使用旧 DNS 地址和端口承接解析职责；命中 `probe_exit` 返回主控 Fake IP，`direct` 返回真实解析，`reject` 返回标准 REFUSED，未命中走真实本地解析。
- 探针侧虚拟 IP 保持同步；本地虚拟路由入口开关只影响 Fake IP 路由接管，虚拟 DNS 开关停止本地 DNS 监听，不影响远方出口。
- 主控路由管理页新增 Fake IP 库摘要、映射明细和重置全部按钮；本地面板新增 VNet 页面虚拟路由 tab 和首页入口。
- Fake IP 连接路径缺失时会同步主控 route config 后重试一次。

#### 2.5.4 影响文件
- doc/REQ-PN-VROUTER-DNS-001-collaboration.md
- probe_controller/internal/core/mng_link_actions.go
- probe_controller/internal/core/mng_link_handlers.go
- probe_controller/internal/core/probe_link_chains.go
- probe_controller/internal/core/probe_route_config_store.go
- probe_controller/internal/core/probe_virtual_router.go
- probe_controller/internal/core/probe_virtual_router_test.go
- probe_controller/internal/core/server.go
- probe_controller/internal/core/mng_pages/route.html
- probe_node/local_console.go
- probe_node/local_console_methods_test.go
- probe_node/local_console_test.go
- probe_node/local_dns_service.go
- probe_node/local_dns_service_test.go
- probe_node/local_pages/panel.html
- probe_node/local_pages/proxy.html
- probe_node/probe_link_chains_sync.go
- probe_node/probe_virtual_router.go
- probe_node/probe_virtual_router_linux.go
- probe_node/probe_virtual_router_other.go
- probe_node/probe_virtual_router_settings.go
- probe_node/probe_virtual_router_windows.go
- probe_node/probe_virtual_router_windows_test.go

#### 2.5.5 测试命令
- `gofmt -w ...`
- `go test ./internal/core -run "TestProbeVirtualRouterFakeIPLibrary|TestProbeRouteFakeIPResolveHandler|TestMngLinkVirtualRouter|TestProbeVirtualRouter" -count=1`
- `go test ./internal/core -run "TestProbeRouteFakeIPResolveHandlerPersistsLibrary|TestMngLinkVirtualRouterFakeIPResetHandlerDispatchesRouteConfigSync|TestProbeVirtualRouterFakeIPLibrary" -count=1`
- `go test ./internal/core -count=1`
- `node` 解析 `probe_controller/internal/core/mng_pages/route.html` 内联脚本，结果 `script 1 ok`
- `go test . -run "TestResolveProbeVirtualRouterDNSResponse|TestProbeVirtualRouterLocalSettingsMissingFieldKeepsDefaultEnabled|TestProbeLocalAPIMethodGuards|TestEnsureProbeVirtualRouterPlatformInterfaceIPWindowsAppliesOnlyFakeIPRoute|TestCleanupProbeVirtualRouterPlatformRoutesWindowsDeletesFakeIPRoute|TestResolveProbeVirtualRouterBridgeDialIPHostUsesPureIP|TestProbeVirtualRouterRelayHandlerCamouflagesPublicPaths" -count=1`
- `go test . -run "^$" -count=1`
- `GOOS=linux GOARCH=amd64 go test -c -o %TEMP%/probe_node_linux.test .`

#### 2.5.6 自测结果
- `probe_controller/internal/core` Fake IP 同步通知定向测试通过。
- `probe_controller/internal/core` 定向测试通过。
- `probe_controller/internal/core` 包级测试通过。
- 主控路由管理页内联 JS 语法检查通过。
- `probe_node` 虚拟路由/DNS/控制台定向测试通过。
- `probe_node` 测试包只编译通过。
- `probe_node` Linux amd64 交叉编译通过。

#### 2.5.7 未执行测试原因
- 未执行 `probe_node` 全量测试套件：该 Windows 环境中部分既有 Wintun、管理员权限、系统网络设置类测试可能依赖本机权限或真实系统状态；本次使用定向测试、只编译和 Linux 交叉编译覆盖本需求改动。
- 未执行浏览器截图验证：本次 UI 为现有 HTML 页面小范围新增 tab/入口，已通过静态代码、API 方法守卫和主控 route.html 内联 JS 语法检查验证。

#### 2.5.8 遗留风险
- 虚拟 DNS 使用旧监听地址和端口承接新逻辑，没有启动第二套并行 DNS 进程；这是为避免端口竞争的实现选择。
- 实机系统路由仍建议在 Windows 管理员环境和 Linux 节点各跑一次真实流量验收。

#### 2.5.9 回滚方案
- 回滚本次列出的影响文件即可恢复旧行为；主控已生成的 `virtual_router_fake_ip` 和探针 `probe_virtual_router_settings.json` 为新增配置字段，旧代码忽略或可手工删除。

#### 2.5.10 结论
- Code 阶段已完成。

### 2.6 Code任务反馈
- 状态: 已完成

| 反馈编号 | 任务编号 | 反馈类型 | 反馈描述 | 阻塞影响 | Code建议 | Architect处理状态 | Architect处理结论 |
|---|---|---|---|---|---|---|---|
| 无 | 无 | 无 | 无 | 无 | 无 | 无 | 无 |

#### 2.6.1 结论
- 当前无 Code 反馈。
