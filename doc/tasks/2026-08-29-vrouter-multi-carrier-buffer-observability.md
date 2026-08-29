# 任务：虚拟路由多 Carrier 与缓冲可观测性

- 任务标识：`2026-08-29-vrouter-multi-carrier-buffer-observability`
- 状态：`已完成`
- 创建时间：`2026-08-29 13:18 +08:00`
- 更新时间：`2026-08-29 14:50 +08:00`
- 用户原始需求：将探针间单物理连接升级为多连接负载均衡，不拆分单个 TCP/UDP 连接。
- 用户最新指令：避免内存浪费；虚拟路由收发完整数据路径涉及的各级固定数量缓冲（不仅是 Carrier）全部升级为初始较小、按需扩容并可回收的有界缓冲；探针周期上报且支持被主控召唤，显示各级缓冲当前状态和最大使用情况，支持重新开始统计；主控虚拟路由增加独立 Tab 展示和召唤各探针状态。
- 启用方式：明确长任务条件

## 一、需求定义

### 1.1 背景与问题

当前每条虚拟路由物理边只有一个 `probeVirtualRouterPhysicalCarrier`，新连接会替换旧连接并清空链路缓冲；发送、接收、健康状态和页面展示均按单 Carrier 建模。多连接需要在保持单个 TCP/UDP 流顺序的同时，避免按 Carrier 复制现有大缓冲。现有探针控制 WebSocket/Yamux 通道已支持周期 `report` 和主控 `report_once`，应复用该通道承载缓冲状态。

### 1.2 目标

交付兼容单连接的多 Carrier 物理边、按 IP 五元组固定流的负载均衡、低内存的共享/分层缓冲，以及可周期上报、可召唤、可重置峰值的探针缓冲状态主控页面。

### 1.3 范围、非范围与约束

- 范围内：桌面/软路由探针虚拟路由物理 Carrier 池；拓扑规则连接数配置；按流固定 Carrier；单 Carrier 故障降级；TUN 收发、frameLink、RX 分发、出口 netstack 和 Carrier 前后完整数据路径各级队列的初始小容量、按需扩容、低水位回收与硬上限；各级缓冲当前值、分配容量、硬上限和峰值；周期上报、主控召唤、峰值重置；主控虚拟路由独立缓冲状态 Tab。
- 范围外：单个 TCP/UDP 流跨 Carrier 分片；修改 vRouter wire frame 固定头；发布、部署和远程升级；Android/mobilecore 多 Carrier。
- 约束：默认与旧配置兼容；新探针对旧探针只能使用 slot 0；不按 Carrier 复制现有 RX 大缓冲；同一流存活期间不因负载变化迁移；不清理用户已有改动。

### 1.4 需求与验收标准

| 需求编号 | 需求描述 | 验收标准 | 优先级 | 状态 | 来源或最新变更 |
|---|---|---|---|---|---|
| REQ-001 | 每条物理拓扑边支持 1-4 个 Carrier | 新旧配置默认 1；配置 N 后新版本双端可同时保持 N 个槽位；同槽重连只替换同槽 | 高 | `已验收` | 用户要求多连接、透明 |
| REQ-002 | IP 负载按双向五元组固定 Carrier | 同一 TCP/UDP 双向流固定槽位，不做单流分片；不同流可分布到不同槽位 | 高 | `已验收` | 用户明确不考虑单连接分流 |
| REQ-003 | Carrier 池故障透明 | 至少一个 Carrier 存活时逻辑路由在线；单 Carrier 断开不清空共享缓冲 | 高 | `已验收` | 用户要求透明 |
| REQ-004 | 避免缓冲内存线性放大 | 现有 TX/RX 大队列按逻辑链路共享；每 Carrier 仅增加小型有界发送缓冲 | 高 | `已验收` | 用户明确避免内存浪费 |
| REQ-005 | 探针采集完整数据路径各级缓冲当前值与历史峰值 | 快照覆盖 TUN 收发、共享 TX/RX、RX 分片、出口 netstack、代理 TCP 入站和各 Carrier 缓冲；包含当前/分配容量/硬上限/峰值和统计起点 | 高 | `已验收` | 用户明确“不仅是 Carrier” |
| REQ-006 | 周期上报和主控召唤 | 缓冲快照随现有周期 report 上报；主控可对在线探针即时 report_once | 高 | `已验收` | 用户最新指令 |
| REQ-007 | 峰值可重新开始统计 | 主控可对指定探针发送重置命令，探针重置峰值和统计起点并立即回报 | 高 | `已验收` | 用户最新指令 |
| REQ-008 | 主控虚拟路由独立 Tab | 新 Tab 按探针显示更新时间、Carrier 和各缓冲状态，提供召唤与重置操作 | 高 | `已验收` | 用户最新指令 |
| REQ-009 | 完整收发路径固定缓冲升级为按需扩缩容 | TUN、frameLink、RX 分片、出口 netstack、代理 TCP 入站和 Carrier 队列初始容量显著小于旧固定容量；压力下按需增长但不超过硬上限；持续低水位后可缩回且不丢帧 | 高 | `已验收` | 用户明确“不仅是 Carrier” |

## 二、总体架构

### 2.1 当前现状

- `probeVirtualRouterFrameLink.carrier` 是单指针；`AttachCarrier` 替换旧连接并清空共享 TX/RX。
- `runTXWorker` 和 `runRXWorker` 分别直接操作唯一 Carrier。
- 现有 `probeVirtualRouterPacketFlowHash` 已提供双向 IPv4 五元组哈希。
- 探针 `sendProbeReport` 周期上报；主控已有 `report_once` 下发和虚拟路由状态召唤接口。
- 主控 `/mng/route` 已有拓扑、规则、Fake IP、路由状态等 Tab，但没有独立缓冲状态页。

### 2.2 目标架构

虚拟路由完整收发路径统一使用带通知机制的并发可伸缩有界队列，覆盖 TUN 收发、逻辑 `frameLink` 共享优先级入口、共享 RX/分片、出口 netstack 和 Carrier 前后队列；使用小初始容量、倍增扩容、硬上限和带冷却时间的低水位收缩。新增固定槽位 Carrier 池，一个轻量 TX 分发器按五元组选择槽位，各 Carrier 使用小型可伸缩有界优先级发送队列和独立 writer；每个 Carrier 独立 reader 汇入共享 RX。各级缓冲统一登记统计，随通用 report 上报；主控缓存最新状态并通过现有控制通道召唤或重置。

### 2.3 关键模块与职责

| 模块 | 当前职责 | 目标职责 | 输入 | 输出 | 依赖 |
|---|---|---|---|---|---|
| 探针 frameLink | 单 Carrier 收发 | Carrier 池、流选择、共享缓冲、聚合健康 | vRouter frame | 多 Carrier 收发 | 现有流哈希 |
| 探针遥测 | 通用运行状态上报 | 采集缓冲当前/峰值/起点并响应重置 | 周期/控制消息 | report payload | reporter 通道 |
| 主控运行状态 | 保存探针 report | 保存每探针缓冲快照并下发召唤/重置 | probe report、管理 API | 缓冲状态 API | probe command |
| 主控 route 页面 | 路由配置与状态 | 独立缓冲状态 Tab | 管理 API | 表格与操作反馈 | mng auth |

### 2.4 关键流程

| 流程 | 发起方 | 处理方 | 数据或状态变化 | 失败处理 | 关联需求 |
|---|---|---|---|---|---|
| IP 帧发送 | frameLink | TX 分发器/Carrier writer | 五元组固定槽位；Carrier 内批处理 | 槽位失败后受影响流重选 | REQ-002/003/004 |
| 周期缓冲上报 | 探针 reporter | 主控 runtime store | 更新探针最新缓冲快照 | 离线保留最后值并标记时间 | REQ-005/006 |
| 主控召唤 | 管理员 | 主控/探针 | 下发 report_once 并更新快照 | 返回在线、下发、失败数量 | REQ-006/008 |
| 重置峰值 | 管理员 | 主控/指定探针 | 清零峰值基线、更新统计起点并立即 report | 离线或超时明确返回 | REQ-007/008 |
| 缓冲扩缩容 | enqueue/dequeue | 可伸缩队列 | 满足负载时扩容；持续低水位后收缩 | 达硬上限时沿用现有背压/超时/丢弃语义 | REQ-004/005/009 |

### 2.5 接口记录

| 接口编号 | 接口名称 | 调用方 | 提供方 | 输入、输出与错误契约 | 实现位置 | 兼容要求 | 关联需求、任务与测试 | 状态与证据 |
|---|---|---|---|---|---|---|---|---|
| IF-001 | 拓扑规则 `carrier_count` | 主控/探针 | route config | 省略或非法为 1，范围 1-4 | controller/probe config structs | 旧探针忽略字段 | REQ-001 / TASK-002 / TEST-001 | `已验收` |
| IF-002 | Carrier 槽位与能力响应 | 拨号探针 | 监听探针 | slot 0 向后兼容；仅确认能力后建立额外槽位 | vRouter relay headers/transport | 新到旧降级 1 | REQ-001/003 / TASK-002 / TEST-002 | `已验收` |
| IF-003 | probe report 缓冲快照 | 探针 | 主控 | 可选字段；旧主控忽略，新主控接受缺失 | reporter/runtime payload | 双向兼容 | REQ-005/006 / TASK-003 / TEST-005 | `已验收` |
| IF-004 | 缓冲状态管理 API | route 页面 | 主控 | GET 列表；POST 召唤；POST reset 指定节点 | mng route handlers | 管理鉴权 | REQ-006/007/008 / TASK-004 / TEST-006 | `已验收` |

### 2.6 架构决策引用

| 决策编号 | 对架构的影响 | 相关模块或接口 |
|---|---|---|
| DEC-001 | 不做单 TCP/UDP 分片，复用双向五元组哈希固定 Carrier | frameLink、REQ-002 |
| DEC-002 | 共享大缓冲，小型每 Carrier 出口缓冲 | frameLink、REQ-004/005 |
| DEC-003 | 复用现有周期 report/report_once，不新增常驻控制连接 | reporter、IF-003/004 |
| DEC-005 | 固定 channel 替换为小初始、倍增、硬上限、低水位冷却收缩的队列 | frameLink、REQ-004/005/009 |

## 三、单元设计

### 3.1 受影响单元

| 单元编号 | 文件或位置 | 职责 | 输入 | 输出 | 依赖 | 关联需求 |
|---|---|---|---|---|---|---|
| UNIT-001 | `probe_node/probe_virtual_router_runtime.go`、`probe_virtual_router.go` | Carrier 池、frameLink 收发、流亲和、共享可伸缩缓冲与峰值 | frame、carrier slot | wire frame、snapshot | flow hash | REQ-001..005/009 |
| UNIT-006 | `probe_virtual_router_tun_dataplane_*`、`probe_virtual_router_exit_netstack.go` 及队列调用点 | TUN/出口各级可伸缩缓冲与统一统计 | IP packet/frame | 下一处理级 | queue registry | REQ-004/005/009 |
| UNIT-002 | `probe_node/route_relay_client_transport.go` | 多 Carrier 能力与槽位握手 | route、slot | net.Conn + capability | WebSocket/H3 | REQ-001/003 |
| UNIT-003 | `probe_node/main.go`、report payload | 周期/召唤上报与重置控制 | reporter tick/control | buffer snapshot | existing yamux | REQ-005..007 |
| UNIT-004 | `probe_controller/internal/core` runtime/command/route handlers | 缓冲快照存储和管理 API | probe report/admin request | JSON/command | runtime store | REQ-006..008 |
| UNIT-005 | `probe_controller/internal/core/mng_pages/route.html` | 独立缓冲 Tab | management JSON | table/actions | route APIs | REQ-008 |

### 3.2 处理与异常规则

| 单元编号 | 正常处理规则 | 异常处理规则 | 兼容要求 | 验证方式 |
|---|---|---|---|---|
| UNIT-001 | 每流固定健康槽位；各 Carrier 独立收发；队列从小容量按需增长并在持续低水位后收缩 | 单槽断开仅清该槽队列；池空才离线；达硬上限保持现有背压语义 | count=1 等价旧行为 | 单元、扩缩容与竞态测试 |
| UNIT-002 | slot0 协商能力后补齐槽位 | 对端无能力时仅保持 slot0 | 新旧互通 | transport 测试 |
| UNIT-003 | 快照无阻塞采样；峰值单调至 reset | reset 后以当前深度为新基线 | 可选 JSON 字段 | payload/control 测试 |
| UNIT-004 | 缓存最新状态并按节点命令 | 离线/无通道返回明确结果 | 缺失快照显示未上报 | handler 测试 |
| UNIT-005 | 表格展示当前/容量/峰值和时间 | 空数据、离线、失败有稳定状态 | 不影响其他 Tab | HTML marker/handler 测试 |

## 四、执行任务

### 4.1 当前交接

- 当前阶段：完成
- 当前计划步骤：无
- 当前门禁：完成门禁通过
- 最近完成检查点：完成多 Carrier 池、流亲和、能力协商、完整数据路径自适应队列、缓冲报告/重置、主控 API 与独立 Tab；Windows 探针和主控全量测试、Linux amd64/arm64 普通及软路由构建、页面语法和并发重复测试通过。
- 工作区状态：基线 `89b85b2`，首次检查无用户未提交改动；本账本为新增文件。
- 下一步唯一动作：无。
- 恢复时先读取：本账本、`probe_node/probe_virtual_router_runtime.go`、`probe_node/probe_virtual_router.go`、`probe_node/main.go`、`probe_controller/internal/core/mng_route_handlers.go`、`mng_pages/route.html`、`git status`。

### 4.2 任务计划

| 任务编号 | 工作内容 | 状态 | 关联需求 | 文件或接口范围 | 完成条件 |
|---|---|---|---|---|---|
| TASK-001 | 调查现有数据、控制、上报和页面路径并完成设计门禁 | `已完成` | REQ-001..009 | 上述 UNIT 文件 | 接口与回滚可实现，无方向冲突 |
| TASK-002 | 实现通用可伸缩队列、完整路径迁移、Carrier 池、配置、兼容握手和按流发送 | `已完成` | REQ-001..005/009 | UNIT-001/002/006、IF-001/002 | 各级扩缩容、count=1兼容、N槽并存和故障降级测试通过 |
| TASK-003 | 实现缓冲当前/峰值统计、周期上报和重置控制 | `已完成` | REQ-005..007 | UNIT-001/003、IF-003 | 快照与 reset 测试通过 |
| TASK-004 | 实现主控状态存储、召唤/重置 API 和独立 Tab | `已完成` | REQ-006..008 | UNIT-004/005、IF-004 | handler/UI marker 测试通过 |
| TASK-005 | 完成跨模块、竞态和回归验证，收口账本 | `已完成` | REQ-001..009 | probe/controller tests | 完成门禁通过 |

### 4.3 变更记录

| 文件、配置或接口 | 变更内容 | 原因 | 关联需求与任务 | 验证方式 | 回滚引用 |
|---|---|---|---|---|---|
| 本账本 | 建立需求、架构、单元、任务和测试追踪 | 长任务恢复与防偏离 | 全部 / TASK-001 | 文档检查 | RB-001 |
| `probe_adaptive_queue.go` 及 TUN/frameLink/netstack 调用点 | 新增并迁移通用并发自适应有界队列、统一缓冲注册表与快照 | 完整路径小初始容量、按需扩容、硬上限和低水位回收 | REQ-004/005/009 / TASK-002 | 定向 Go tests | RB-001 |
| Carrier 配置、握手与 frameLink | 新增 1-4 槽位、slot0 能力确认后并行补齐、按双向流固定槽位及独立读写 | 多连接负载均衡且不拆单流 | REQ-001..004 / TASK-002 | 多槽/故障/兼容测试 | RB-001 |
| probe report/control 与 controller runtime/API/page | 周期缓冲快照、即时召唤、统计重置、独立缓冲状态 Tab | 提供容量优化所需观测面 | REQ-005..008 / TASK-003/004 | report/handler/UI tests | RB-001 |

## 五、测试与验证

### 5.1 测试计划与结果

| 测试编号 | 测试目标 | 关联需求与任务 | 方法或准确命令 | 预期结果 | 实际结果 | 状态 | 证据 |
|---|---|---|---|---|---|---|---|
| TEST-001 | 配置归一化和默认兼容 | REQ-001 / TASK-002 | 定向 Go tests | 缺失=1，范围1-4 | 主控和探针归一化、默认值测试通过 | `通过` | controller/probe 全量测试 |
| TEST-002 | 多槽并存、同槽替换、旧端降级 | REQ-001/003 / TASK-002 | Carrier/transport tests | 行为符合槽位契约 | 多槽、同槽替换和能力协商测试通过 | `通过` | probe 全量测试 |
| TEST-003 | IP 双向流固定且多流分布 | REQ-002 / TASK-002 | frameLink flow tests | 同流同槽，多流可分散 | 五元组及代理会话稳定哈希测试通过；重点用例重复 10 次通过 | `通过` | `go test -count=10 -run 'TestProbeVirtualRouterFrameLinkMultiCarrierKeepsFlowAffinity|TestProbeVirtualRouterFrameLinkCarrierFailureOnlyRemapsAffectedFlows|TestProbeAdaptiveQueueConcurrentProducersAndConsumers' .` |
| TEST-004 | 缓冲不线性复制且故障不清共享队列 | REQ-003/004 / TASK-002 | queue/failure tests | 单槽仅清自身 | 单 Carrier 故障仅重映射受影响流，共享队列保持 | `通过` | probe 全量和重点重复测试 |
| TEST-005 | 当前/容量/峰值/reset/上报 | REQ-005..007 / TASK-003 | snapshot/reporter tests | 峰值单调，reset 更新时间 | 自适应队列快照、重置、probe report 和主控存储测试通过 | `通过` | probe/controller 全量测试 |
| TEST-006 | 主控查询、召唤、重置和页面 | REQ-006..008 / TASK-004 | controller handler/auth HTML tests | API 和独立 Tab 可用 | handler、命令分发、页面标记与内联 JavaScript 语法通过 | `通过` | controller 全量测试；Node `new Function` 校验 |
| TEST-007 | 全仓回归和竞态重点 | REQ-001..008 / TASK-005 | `go test`、重点 `-race`（环境允许时） | 无新增失败/竞态 | Windows 探针和主控全量测试通过；Linux amd64/arm64 普通及 `linux_router` 构建通过；race 因本机无 gcc 未执行 | `通过（有限制）` | probe/controller `go test -count=1 ./...`；四种 Linux release 等价构建 |
| TEST-008 | 完整数据路径缓冲按需扩容、硬上限和低水位收缩 | REQ-004/005/009 / TASK-002/003 | 通用 queue、TUN、frameLink、RX 分片、netstack 与 Carrier 单元/并发测试 | 各级初始小、无丢帧扩容、硬上限背压、冷却后回收并可观测 | 通用队列、TUN、frameLink、RX 分片、netstack、代理 TCP 入站和 Carrier 队列测试通过 | `通过` | probe 全量测试及并发生产/消费重复测试 |

### 5.2 未执行测试

- `go test -race`：本机 Go race detector 需要 CGO，但环境没有 `gcc`；已用并发生产/消费重复测试、Windows 全量测试和 Linux 多架构交叉编译补充覆盖，最终仍需在带 C 工具链的 CI/开发机补跑。

## 六、端到端追踪

| 需求编号 | 验收标准 | 架构或单元 | 任务编号 | 文件、配置或接口 | 测试编号 | 结果与证据 | 状态 |
|---|---|---|---|---|---|---|---|
| REQ-001 | 1-4 Carrier、槽位替换、默认兼容 | UNIT-001/002 | TASK-002 | IF-001/002 | TEST-001/002 | 配置、槽位和兼容协商测试通过 | `已验收` |
| REQ-002 | 同流固定、多流分布 | UNIT-001 | TASK-002 | frameLink | TEST-003 | 五元组和代理会话流亲和测试通过 | `已验收` |
| REQ-003 | 单槽故障路由保持 | UNIT-001/002 | TASK-002 | Carrier pool | TEST-002/004 | 单槽失败不清共享队列测试通过 | `已验收` |
| REQ-004 | 共享大缓冲、小型每槽缓冲 | UNIT-001 | TASK-002 | queue design | TEST-004 | 逻辑链路共享队列、每槽小队列已验证 | `已验收` |
| REQ-005 | 完整数据路径当前/分配容量/硬上限/峰值/起点 | UNIT-001/003/006 | TASK-002/003 | IF-003 | TEST-005/008 | 队列快照、峰值和完整路径迁移测试通过 | `已验收` |
| REQ-006 | 周期与召唤 | UNIT-003/004 | TASK-003/004 | IF-003/004 | TEST-005/006 | report 与 report_once 路径测试通过 | `已验收` |
| REQ-007 | 重置峰值 | UNIT-003/004 | TASK-003/004 | IF-004 | TEST-005/006 | reset 命令和立即回报测试通过 | `已验收` |
| REQ-008 | 独立 Tab | UNIT-004/005 | TASK-004 | route page/API | TEST-006 | Tab、筛选、召唤、重置和语法检查通过 | `已验收` |
| REQ-009 | 完整数据路径初始小、按需增长、低水位回收、硬上限 | UNIT-001/006 | TASK-002/003 | resizable queues/telemetry | TEST-008 | 自适应队列行为和迁移路径测试通过 | `已验收` |

## 七、决策与冲突记录

### 7.1 决策记录

| 决策编号 | 触发原因 | 采用方案 | 理由与证据 | 替代方案 | 影响范围 | 替代关系 | 状态 |
|---|---|---|---|---|---|---|---|
| DEC-001 | 用户排除单 TCP/UDP 分流 | 双向五元组固定 Carrier | 现有 hash 已实现且避免乱序 | 帧级轮询和重排 | 数据面 | 无 | `有效` |
| DEC-002 | 用户要求避免内存浪费 | 共享大队列，每槽小队列 | 多连接数不应线性复制 RX 4096/分片队列 | 每槽完整 frameLink | 内存/队列 | 无 | `有效` |
| DEC-003 | 已有周期和召唤能力 | 扩展通用 report/report_once | 减少常驻连接和重复状态机 | 新建独立 WebSocket | 控制面 | 无 | `有效` |
| DEC-004 | 新旧探针可能混跑 | slot0 能力确认后才扩容 | 旧端仍为替换式 AttachCarrier | 仅靠版本号 | 握手/升级 | 无 | `有效` |
| DEC-005 | 用户要求完整收发路径固定缓冲改为按需扩容且避免浪费 | 通用并发可伸缩有界队列覆盖 TUN、frameLink、RX 分片、netstack、Carrier；小初始容量、倍增、硬上限、低水位冷却收缩 | Go channel 容量不可安全动态调整；只增长不能长期回收峰值内存；局部迁移无法提供完整数据 | 固定 channel、无界 slice 或仅 Carrier 迁移 | 数据面缓冲与遥测 | 无 | `有效` |

### 7.2 冲突记录

| 缺陷编号 | 描述 | 影响 | 修复 | 验证 | 状态 |
|---|---|---|---|---|---|
| DEF-001 | 代理 TCP 会话原有 RX 哈希包含 subtype，若直接用于 Carrier 选择会使 open/data/close 进入不同槽位 | 单会话乱序或连接失败 | 新增独立稳定会话哈希，忽略 subtype 并对同一 session 固定槽位 | 流亲和与多 Carrier 重复测试通过 | `已关闭` |

## 八、缺陷记录

无。

## 九、回滚方案

| 变更或风险 | 触发条件 | 回滚步骤 | 数据与兼容影响 | 回滚后验证 | 状态 |
|---|---|---|---|---|---|
| RB-001 多 Carrier/遥测/API/UI | 回归、资源或兼容故障 | 将 `carrier_count` 设为1；回退新增 pool/字段/handler/page；旧可选字段无需迁移 | 不改持久业务数据；旧配置仍可读 | 单 Carrier、report、route status 回归 | `可用` |

## 十、已验证事实

| 事实编号 | 已验证事实 | 证据 | 对任务的影响 |
|---|---|---|---|
| FACT-001 | frameLink 当前只有一个 carrier，新 Attach 会关闭旧 Carrier 并清空全部缓冲 | `probe_virtual_router_runtime.go:176-211`、`probe_virtual_router.go:4383-4411` | 必须改池和清理粒度 |
| FACT-002 | 现有 RX 已按双向 IPv4 五元组分片 | `probe_virtual_router.go:4999-5097` | 可复用作 TX 流键 |
| FACT-003 | 周期 report 和 report_once 已存在 | `probe_node/main.go:732-847,910,1128-1133` | 复用控制通道 |
| FACT-004 | route 页面已有 route status GET/POST 召唤 | `mng_route_handlers.go:174`、`route.html:648-665` | 新缓冲 Tab 可复用命令分发模式 |
| FACT-005 | 当前工作区首次检查无未提交改动 | `git status --short` 空；HEAD `89b85b2` | 可准确归属后续差异 |
| FACT-006 | 应用层数据队列分布在 TUN 入/出站与入站分片、frameLink TX/RX 与 RX 分片、exit netstack 输出分片 | `rg make(chan ...)` 与各 runner 类型 | 通用队列需支持字节包、frame 和 netstack packet 泛型 |
| FACT-007 | WebSocket Dial 和 H3 ReadResponse 都可读取响应头；监听端 Upgrade/WriteHeader 可声明能力 | `route_relay_client_transport.go:1094-1158,1161-1317`、`probe_virtual_router_runtime.go:742-796` | 可做 slot0 能力确认而无需版本猜测 |
| FACT-008 | 主控 runtime store 由 `probe_ws.go` 解码 report 后更新，route status POST 已复用 `report_once` | `probe_ws.go:96-123`、`probe_runtime.go:246-317`、`mng_route_handlers.go:174-187` | 缓冲快照可作为可选 report 字段加入 |
| FACT-009 | 通用自适应队列已支持 FIFO、倍增至硬上限、冷却收缩、阻塞截止时间、统一快照和统计重置；首批完整路径迁移已通过定向测试 | `probe_adaptive_queue.go`、TUN/frameLink/netstack 迁移及 TEST-008 当前证据 | Carrier 池可直接复用同一队列和注册表，不再新增缓冲实现 |
| FACT-010 | 新端仅在 slot0 响应明确能力头后启动额外槽位；旧端缺少能力头时保持单连接 | WebSocket/H3 请求响应头与协商测试 | 支持滚动升级而不在旧监听端反复替换连接 |
| FACT-011 | Windows 探针全量测试、主控全量测试、Linux amd64 与 arm64 探针测试二进制交叉编译均通过 | TASK-005 当前验证命令 | 主要桌面、主控与软路由构建面已覆盖 |

## 十一、风险与阻塞

| 编号 | 类型 | 描述与证据 | 影响 | 缓解或所需动作 | 状态 |
|---|---|---|---|---|---|
| RISK-001 | 兼容 | 新端直接拨多连接会在旧端反复替换 | 路由抖动 | slot0 能力头明确确认后才扩容，旧端保持单连接 | `已关闭` |
| RISK-002 | 顺序 | Carrier 集合变化可能使存量流重映射 | TCP 乱序 | 有界流绑定表；仅故障槽位上的流重选，恢复槽位只接新流 | `已关闭` |
| RISK-003 | 内存 | 每槽复制现有大队列会线性放大 | 软路由资源压力 | 共享入口/RX，每槽仅初始 8、上限 64 的小型有界出口 | `已关闭` |
| RISK-004 | 抖动 | 频繁扩缩容可能增加分配和 GC | 高峰吞吐波动 | 倍增扩容、2 分钟低水位冷却后收缩，统计界面持续观测 | `已关闭` |

## 十二、质量门禁

### 12.1 准备门禁

| 检查项 | 结论 | 证据或条件 |
|---|---|---|
| 最新目标、范围、非范围和约束已记录 | 通过 | 第一章 |
| 验收标准可观察、可测试 | 通过 | REQ-001..008 |
| 必要架构和单元设计达到可实现程度 | 通过 | 队列清单、并发契约、握手、report store 和页面入口已定位 |
| 每项需求已有任务、范围和测试思路 | 通过 | 第四、五、六章 |
| 工作区基线和用户已有改动已识别 | 通过 | FACT-005 |
| 高风险变更已有回滚思路 | 通过 | RB-001 |
| 无改变实现方向的未解决冲突 | 通过 | 冲突记录为空 |

- 门禁结论：通过
- 条件及关闭要求：无。

### 12.2 完成门禁

| 检查项 | 结论 | 证据或条件 |
|---|---|---|
| 用户最新目标和有效需求逐项验收 | 通过 | REQ-001..009 均有实现和测试证据 |
| 端到端追踪闭合 | 通过 | 第六章全部已验收 |
| 测试已执行或缺口影响已准确记录 | 通过 | TEST-001..008；race 环境限制见 5.2 |
| 缺陷已关闭或成为用户接受的遗留风险 | 通过 | DEF-001 已关闭 |
| 决策、冲突、回滚、风险和阻塞状态已更新 | 通过 | DEC-001..005、RB-001、RISK-001..004 |
| 最终差异无范围漂移、无关回退和调试残留 | 通过 | `git diff --check` 无空白错误；变更仅覆盖本任务代码、测试和账本 |
| 账本与工作区一致，下一步唯一动作为“无” | 通过 | 当前交接和完成摘要已收口 |

- 门禁结论：通过
- 条件及关闭要求：无。

## 十三、检查点

| 时间 | 已完成 | 新发现或变化 | 影响 | 下一步唯一动作 |
|---|---|---|---|---|
| 2026-08-29 13:18 +08:00 | 建立账本并记录需求、架构、任务和测试 | 已有 report_once 和 route status 召唤可复用 | 缩小控制面改造范围 | 完成 TASK-001 精确接口调查 |
| 2026-08-29 13:25 +08:00 | 纳入固定缓冲升级为按需扩缩容 | Go channel 不可动态扩容，需要可伸缩有界队列 | 扩大 UNIT-001、TEST-008 和缓冲遥测字段 | 完成可伸缩队列并发契约与现有调用点调查 |
| 2026-08-29 13:30 +08:00 | 将扩缩容与统计范围扩大到完整收发路径 | 不仅 Carrier，TUN、frameLink、RX 分片和出口仍有固定 channel | 新增 UNIT-006，扩大 REQ-005/009、TASK-002、TEST-008 | 盘点全部 vRouter 数据路径有界队列及调用语义 |
| 2026-08-29 13:42 +08:00 | 完成 TASK-001 和准备门禁 | 队列、report、command、页面、WebSocket/H3 握手均有明确入口 | 可以开始最小连贯实现 | 新增通用可伸缩队列与注册表 |
| 2026-08-29 13:51 +08:00 | 完成通用自适应队列、统一注册表并迁移 TUN、共享 frameLink 与出口 netstack；定向测试通过 | 旧测试需按初始容量与硬上限分别断言，数据路径语义保持 | TASK-002 的自适应队列基础完成，进入 Carrier 池 | 将单 Carrier 指针重构为固定槽位池并实现按流分发 |
| 2026-08-29 14:42 +08:00 | 完成 TASK-002..004；全量 Windows/主控测试、Linux amd64/arm64 编译、页面 JS 语法和并发重复测试通过 | race detector 因本机缺少 gcc 无法运行；代理 TCP 入站队列也纳入完整路径 | 进入最终验证与差异审查 | 执行最终门禁并收口账本 |
| 2026-08-29 14:50 +08:00 | 完成全量回归、Linux 普通与软路由双架构构建、重点并发重复测试、页面语法和最终差异检查 | 发现并修复代理会话 subtype 哈希可能跨槽的问题；race 环境缺口已准确记录 | REQ-001..009 全部验收，完成门禁通过 | 无 |

## 十四、完成摘要

- 交付结果：多 Carrier、完整路径自适应缓冲、探针遥测与主控缓冲状态 Tab 已实施完成，未发布。
- 需求验收：REQ-001..009 全部验收通过。
- 测试结论：Windows 探针/主控全量测试、Linux amd64/arm64 普通及软路由构建、重点并发重复测试和页面语法通过；race detector 受本机缺少 gcc 限制未执行。
- 缺陷与风险：DEF-001 已关闭，RISK-001..004 已关闭。
- 回滚说明：RB-001。
- 完成门禁：通过。
- 下一步唯一动作：无。
