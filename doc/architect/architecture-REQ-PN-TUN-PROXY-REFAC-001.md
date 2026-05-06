# 总体架构文档

- 适用规则: AI协作规则
- 后续工作传递声明: 本文档必须传递给后续阶段与后续角色。
- 需求编号: REQ-PN-TUN-PROXY-REFAC-001
- 需求后缀: REQ-PN-TUN-PROXY-REFAC-001
- 当前角色: Architect
- 工作依据文档: [doc/architect/requirements-REQ-PN-TUN-PROXY-REFAC-001.md](doc/architect/requirements-REQ-PN-TUN-PROXY-REFAC-001.md:1)、[probe_node/local_tun_route.go](probe_node/local_tun_route.go:39)、[probe_node/local_console.go](probe_node/local_console.go:1163)、[probe_manager/backend/network_assistant.go](probe_manager/backend/network_assistant.go:95)、[probe_manager/backend/network_assistant_mux.go](probe_manager/backend/network_assistant_mux.go:1537)
- 状态: 进行中

## 架构目标
- 在 `probe_node` 落地“每个代理组一个 runtime”的唯一运行态模型。
- 将当前 `group + action + tunnel_node_id` 的分散状态管理，收敛为组级 runtime 事实来源。
- 对齐 manager 口径，使 `probe_node` 的组级 runtime 快照与 TCP/UDP 调试字段可稳定映射。
- 保持现有 TUN 出站语义不变，重构以结构收敛与可观测一致性为主。
- 修复 `local/panel` 最近测试延迟状态语义，失败展示 `不可达`，成功展示毫秒值，并保持状态可持续刷新。

## 总体设计
- 设计采用“控制面配置态 + 组级运行态 + 数据面消费态”三层模型。
- 控制面保留 [`proxy_group.json`](probe_node/local_console.go:39) 和 [`proxy_state.json`](probe_node/local_console.go:40) 作为配置与策略输入。
- 新增组级 runtime 聚合层，负责承载每组唯一 runtime 对象，维护 `action` `tunnel_node_id` `connected` `status` `failure_count` `retry_at` `last_error` 等状态。
- 数据面在 [`decideProbeLocalRouteForTarget()`](probe_node/local_tun_route.go:39) 读取策略时，优先按组级 runtime 口径输出路由决策并回填调试上下文。
- TUN TCP/UDP 出站执行逻辑保持原函数入口，重点把调试字段来源切换到组级 runtime 快照，减少分支散落取值。

## 关键模块
| 模块编号 | 模块名称 | 职责 | 输入 | 输出 |
|---|---|---|---|---|
| M1 | Group Runtime Registry | 每组维护唯一 runtime 状态对象，提供查询 更新 快照接口 | group action tunnel_node_id 连接事件 失败事件 | runtime state snapshot |
| M2 | Route Decision Adapter | 将域名匹配与运行态策略组装为 direct reject tunnel 决策 | target domain proxy_group proxy_state runtime snapshot | route decision target group node |
| M3 | Tunnel Runtime Resolver | 按组解析并确保对应 runtime 链路可用 | group runtime tunnel_node_id chain cache | runtime handle stream open result |
| M4 | Debug Projection | 将 TCP/UDP 生命周期事件投影为统一调试字段 | route decision runtime snapshot tcp udp events | tcp_debug udp_assoc_debug payload |
| M5 | Compatibility Facade | 兼容现有 API 与文件格式，屏蔽内部重构细节 | local api request old state files | unchanged api response with aligned runtime fields |
| M6 | Panel Latency Status Sync | 统一延迟可达性语义并提供固定周期刷新触发点 | selected chain runtime dial result panel polling cadence | latency status reachable unreachable with periodic refresh |

## 关键接口
| 接口编号 | 接口名称 | 调用方 | 提供方 | 说明 |
|---|---|---|---|---|
| IF-001 | GetOrInitGroupRuntime | Route Decision Adapter | Group Runtime Registry | 获取或初始化组级 runtime，保证每组唯一对象 |
| IF-002 | UpdateGroupRuntimeByPolicy | 控制面策略写入流程 | Group Runtime Registry | 根据 `action` 与 `tunnel_node_id` 更新组级 runtime |
| IF-003 | ResolveRouteByDomainWithRuntime | TUN route 层 | Route Decision Adapter | 按域名规则和组级 runtime 产出最终路由决策 |
| IF-004 | EnsureGroupTunnelRuntime | TUN TCP/UDP 出站 | Tunnel Runtime Resolver | 为组级 tunnel 决策解析并校验 runtime 可用性 |
| IF-005 | BuildGroupRuntimeSnapshot | 调试输出与状态接口 | Group Runtime Registry | 输出对齐 manager 的组级快照字段 |
| IF-006 | ProjectTCPDebugPayload | TCP forwarder | Debug Projection | 输出包含 `group` `node_id` `route_target` `direct` `transport` 的统一字段 |
| IF-007 | ProjectUDPDebugPayload | UDP association | Debug Projection | 输出包含 `group` `node_id` `route_target` `route_fingerprint` 的统一字段 |
| IF-008 | BuildProxyLatencyStatusView | Proxy Status Handler | Panel Latency Status Sync | 生成 `selected_chain_latency_ms` 与 `selected_chain_latency_status` 视图字段 |
| IF-009 | StartPanelProxyStatusPolling | Panel Script Runtime | Panel Latency Status Sync | 启动 60 秒周期 `loadProxyStatus` 轮询并避免重复注册 |

## 关键约束
- 单组唯一性: 任一时刻一个组仅允许绑定一个 runtime 状态对象。
- 分层约束: 配置态文件不直接承载连接对象，连接态只能存在于运行态内存模型。
- 兼容约束: 外部 API 请求体与核心响应字段保持兼容，避免破坏现有控制台调用。
- 语义约束: 对齐 manager 字段时，优先语义一致，其次名称一致。
- 回退约束: runtime 不可用时，必须输出可诊断错误，禁止静默降级为错误路径成功。
- 展示约束: 延迟不可达时必须输出明确状态供前端渲染 `不可达`，禁止仅依赖字段缺失触发 `-`。
- 刷新约束: `panel` 侧最多保留一个 proxy status 轮询定时器，轮询周期固定 60 秒。

## 风险
- 运行态收敛过程可能引入并发读写竞态，需通过统一锁域与快照拷贝控制。
- 若 route 层和 debug 层引用不同状态源，会再次形成口径分裂。
- 组级 runtime 与链路 runtime 的生命周期不一致时，可能出现 stale 状态。

## 结论
- 架构采用“每组唯一 runtime + 分层消费 + 调试口径投影”方案，满足 `probe_node` 对齐 manager 的核心目标，且可在不改变主业务语义前提下渐进落地。
