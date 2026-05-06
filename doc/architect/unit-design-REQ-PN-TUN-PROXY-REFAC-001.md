# 单元设计文档

- 适用规则: AI协作规则
- 后续工作传递声明: 本文档必须传递给后续阶段与后续角色。
- 需求编号: REQ-PN-TUN-PROXY-REFAC-001
- 需求后缀: REQ-PN-TUN-PROXY-REFAC-001
- 当前角色: Architect
- 工作依据文档: [doc/architect/requirements-REQ-PN-TUN-PROXY-REFAC-001.md](doc/architect/requirements-REQ-PN-TUN-PROXY-REFAC-001.md:1)、[doc/architect/architecture-REQ-PN-TUN-PROXY-REFAC-001.md](doc/architect/architecture-REQ-PN-TUN-PROXY-REFAC-001.md:1)、[probe_node/local_console.go](probe_node/local_console.go:1163)、[probe_node/local_tun_route.go](probe_node/local_tun_route.go:39)、[probe_node/tcp_debug.go](probe_node/tcp_debug.go:17)、[probe_node/udp_assoc_debug.go](probe_node/udp_assoc_debug.go:13)
- 状态: 进行中

## 单元清单
| 单元编号 | 单元名称 | 所属模块 | 职责 | 输入 | 输出 |
|---|---|---|---|---|---|
| U1 | Group Runtime State Unit | M1 | 管理每组唯一 runtime 对象与状态迁移 | group action tunnel_node_id lifecycle events | group runtime state snapshot |
| U2 | Route Runtime Binding Unit | M2 | 将域名规则决策绑定到组级 runtime 并产出路由 | target domain rules runtime state | route decision |
| U3 | Tunnel Runtime Resolve Unit | M3 | 按组确保隧道 runtime 可用并开流 | route decision group runtime | tunnel stream open result |
| U4 | TCP Debug Projection Unit | M4 | 对齐 TCP 调试输出字段口径 | tcp relay events route runtime | tcp debug payload |
| U5 | UDP Debug Projection Unit | M4 | 对齐 UDP 调试输出字段口径 | udp assoc events route runtime | udp debug payload |
| U6 | Runtime Snapshot API Unit | M5 | 提供组级 runtime 快照输出并兼容旧接口 | runtime state store api request | runtime snapshot response |
| U7 | Panel Latency Status Unit | M6 | 输出延迟可达性语义并驱动 60 秒轮询展示 | selected chain runtime dial result panel poll tick | latency status view and refreshed ui state |

## 单元设计
### 单元编号 U1
- 单元名称: Group Runtime State Unit
- 职责: 保证每组只有一个 runtime 状态对象，统一维护连接状态、失败计数、重试时间、最后错误与快照。
- 输入: 组名、策略动作、隧道节点标识、连接建立与关闭事件。
- 输出: 组级 runtime 状态对象与只读快照。
- 处理规则:
  - group key 统一标准化处理。
  - `action=tunnel` 时维护 `tunnel_node_id` 与连接状态。
  - `action=direct/reject` 时清理不再适用的隧道字段。
- 异常规则:
  - group 为空返回参数错误。
  - 并发冲突通过统一锁域序列化处理。

### 单元编号 U2
- 单元名称: Route Runtime Binding Unit
- 职责: 在域名规则匹配后，将策略动作映射到组级 runtime 并产出 direct reject tunnel 决策。
- 输入: 域名、目标地址、规则匹配结果、runtime 快照。
- 输出: 路由决策结构体。
- 处理规则:
  - fake IP 改写后再做策略绑定。
  - `tunnel` 必须校验 runtime 中 `tunnel_node_id` 非空。
- 异常规则:
  - runtime 缺失时返回可诊断错误。
  - tunnel 缺少节点时拒绝继续执行。

### 单元编号 U3
- 单元名称: Tunnel Runtime Resolve Unit
- 职责: 将组级 runtime 的节点信息解析为可用链路 runtime，并执行开流。
- 输入: route decision group runtime snapshot。
- 输出: tcp udp 可用连接流或错误。
- 处理规则:
  - 统一复用节点归一化逻辑。
  - 开流前后都记录组级状态变迁。
- 异常规则:
  - chain runtime 不存在时返回明确错误。
  - 开流失败写入 `last_error` 与 `failure_count`。

### 单元编号 U4
- 单元名称: TCP Debug Projection Unit
- 职责: 将 TCP 生命周期事件投影为 manager 对齐字段。
- 输入: relay 事件、route decision、runtime snapshot。
- 输出: TCP debug active 与 failures 项。
- 处理规则:
  - 输出 `group` `node_id` `route_target` `direct` `transport`。
  - 字段空值语义与 manager 保持一致。
- 异常规则:
  - route 上下文缺失时降级填充 `target`，同时写入 reason。

### 单元编号 U5
- 单元名称: UDP Debug Projection Unit
- 职责: 对齐 UDP association 调试字段语义并绑定组级 runtime。
- 输入: association 生命周期事件、route runtime 信息。
- 输出: UDP association debug 项。
- 处理规则:
  - 输出 `assoc_key_v2` `flow_id` `route_target` `route_fingerprint` `group` `node_id`。
  - source refs 与 active 状态按实时值输出。
- 异常规则:
  - association 元信息缺失时输出默认空值并保留可追踪 key。

### 单元编号 U6
- 单元名称: Runtime Snapshot API Unit
- 职责: 输出组级 runtime 快照并兼容既有控制面接口。
- 输入: runtime state store、控制面查询请求。
- 输出: 组级状态数组与当前组选择信息。
- 处理规则:
  - 输出字段顺序稳定，便于前端和自动化测试断言。
  - 兼容 `proxy_state` 现有读写路径。
- 异常规则:
  - store 初始化失败时返回结构化错误，不返回空成功。
  
  ### 单元编号 U7
  - 单元名称: Panel Latency Status Unit
  - 职责: 统一最近测试延迟在成功与失败场景的展示语义，并在前端建立固定 60 秒刷新。
  - 输入: proxy status 原始字段、链路探测结果、前端轮询触发事件。
  - 输出: `selected_chain_latency_status` `selected_chain_latency_ms` 渲染输入与轮询后的最新展示状态。
  - 处理规则:
    - 后端成功探测时输出 `selected_chain_latency_status=reachable` 与非负毫秒值。
    - 后端失败探测时输出 `selected_chain_latency_status=unreachable`，允许毫秒值缺省。
    - 前端按状态优先渲染，`reachable` 显示毫秒值，`unreachable` 显示 `不可达`。
    - 前端只注册一个 60 秒轮询定时器，定时调用 `loadProxyStatus`。
  - 异常规则:
    - 状态字段缺失时前端降级为 `不可达`，并记录可诊断日志。
    - 轮询请求失败不终止定时器，下一周期继续重试。
  
  ## 风险
- U1 与 U3 的状态一致性是最大风险点，必须避免连接对象与快照分离。
- U4 U5 口径对齐若缺少字段映射测试，会在联调阶段暴露问题。
- U7 若未约束单定时器机制，可能出现重复轮询导致接口压力与展示抖动。

## 结论
- 单元拆分覆盖了状态收敛、路由绑定、隧道开流、调试投影与面板延迟状态同步五条主线，可直接用于 Code 任务执行包拆解。
