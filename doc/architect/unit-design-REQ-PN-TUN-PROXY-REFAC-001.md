# 单元设计文档

- 适用规则: AI协作规则
- 后续工作传递声明: 本文档必须传递给后续阶段与后续角色。
- 需求编号: REQ-PN-TUN-PROXY-REFAC-001
- 需求后缀: REQ-PN-TUN-PROXY-REFAC-001
- 当前角色: Architect
- 工作依据文档: [doc/architect/requirements-REQ-PN-TUN-PROXY-REFAC-001.md](doc/architect/requirements-REQ-PN-TUN-PROXY-REFAC-001.md:1)、[doc/architect/architecture-REQ-PN-TUN-PROXY-REFAC-001.md](doc/architect/architecture-REQ-PN-TUN-PROXY-REFAC-001.md:1)、[resolveProbeLocalProxyRouteDecisionByDomain()](probe_node/local_route_decision.go:5)、[decideProbeLocalRouteForTarget()](probe_node/local_tun_route.go:39)、[openProbeLocalTunnelConnWithAssociation()](probe_node/local_tun_route.go:117)
- 状态: 进行中

## 单元清单
| 单元编号 | 单元名称 | 所属模块 | 职责 | 输入 | 输出 |
|---|---|---|---|---|---|
| U1 | Group Selection Unit | M1 | 管理组动作与 `selected_chain_id` | group action selected_chain_id | normalized group selection |
| U2 | Group Attached Runtime Unit | M2 | 为每个组维护唯一的 `tun proxy` 客户端 runtime | group selection chain metadata connect events | runtime pointer and snapshot |
| U3 | Route Runtime Binding Unit | M3 | 命中组后直接绑定该组 runtime 指针 | target domain group selection runtime registry | route decision with runtime reference |
| U4 | TUN Entry Client Unit | M4 | 基于组 runtime 执行链路入口连接与后续代理行为 | group runtime target network | tunnel stream or error |
| U5 | Probe Link Chain Internal Unit | M5 | 维护本节点相关的链路内部互联 | chain topology hop config lifecycle events | internal link state |
| U6 | Group Runtime Status Unit | M6 | 把组 runtime 状态输出给控制面 | group runtime snapshot connect result | unavailable or reachable view |

## 单元设计
### 单元编号 U1
- 单元名称: Group Selection Unit
- 职责: 管理每个组选择哪条 chain，并统一持久化 `selected_chain_id`。
- 输入: 组名、动作、`selected_chain_id`。
- 输出: 规范化后的组选链配置。
- 处理规则:
  - group key 统一标准化处理。
  - `action=tunnel` 时必须存在 `selected_chain_id`。
  - `action=direct` 与 `action=reject` 时清理不适用的 chain 字段。
- 异常规则:
  - group 为空返回参数错误。
  - chain 不存在于 [`proxy_chain.json`](probe_node/local_console.go:42) 时直接返回错误。

### 单元编号 U2
- 单元名称: Group Attached Runtime Unit
- 职责: 为每个组维护唯一的 `tun proxy` 客户端 runtime，并把 runtime 挂接到组上。
- 输入: 组选链配置、链路入口元数据、连接建立与失败事件。
- 输出: 该组 runtime 指针与快照。
- 处理规则:
  - 一组只能有一个 runtime。
  - runtime 至少包含 `selected_chain_id`、入口地址、socket 或 session 持有体、连接状态、最近错误、更新时间。
  - 组切换 chain 时，旧 runtime 需要失效或回收，再挂接新 runtime。
- 异常规则:
  - runtime 初始化失败时返回结构化错误。
  - 组配置与 runtime 快照不一致时优先报告错误，不静默修正。

### 单元编号 U3
- 单元名称: Route Runtime Binding Unit
- 职责: route 命中组后，直接取该组挂接的 runtime 指针进入后续代理行为。
- 输入: 域名、目标地址、组选链配置、group runtime registry。
- 输出: 携带 runtime 指针的路由决策结构。
- 处理规则:
  - fake IP 改写后再做组匹配。
  - `tunnel` 决策必须直接绑定组 runtime 指针。
  - 不允许在这个阶段再去借用 [getProbeChainRuntime()](probe_node/link_chain_runtime.go:2512) 查内部链路 runtime。
- 异常规则:
  - 命中 `tunnel` 但组上没有挂接 runtime 时，返回结构化错误。
  - runtime 已失效时直接失败，不自动回退为 `direct`。

### 单元编号 U4
- 单元名称: TUN Entry Client Unit
- 职责: 基于组 runtime 执行入口连接与后续代理行为。
- 输入: 组 runtime 指针、目标地址、网络类型。
- 输出: TCP UDP 可用连接流或错误。
- 处理规则:
  - [`tun proxy`](probe_node/local_tun_route.go:117) 只消费组 runtime 指针。
  - 入口连接、socket 生命周期、状态变更均记入组 runtime。
  - 可消费任意 chain，不要求本节点属于该 chain。
- 异常规则:
  - 入口不可达时直接失败。
  - 开流失败时写入组 runtime 错误状态。
  - 禁止失败后自动回退到 `direct`。

### 单元编号 U5
- 单元名称: Probe Link Chain Internal Unit
- 职责: 维护本节点相关的 chain 内部互联，与 `tun proxy` 组选链 runtime 隔离。
- 输入: chain topology、hop config、lifecycle events。
- 输出: 本节点相关的内部链路状态。
- 处理规则:
  - [`probe link chain`](probe_node/probe_link_chains_sync.go:97) 继续只维护与本节点有关的内部互联。
  - 本单元不为 `tun proxy` 提供组选链 runtime。
- 异常规则:
  - 内部互联失败只报告内部链路状态，不得直接替代组 runtime 状态。

### 单元编号 U6
- 单元名称: Group Runtime Status Unit
- 职责: 输出组 runtime 的真实客户端可用性状态。
- 输入: 组 runtime 快照、入口连接结果。
- 输出: 控制面状态字段与前端可渲染视图。
- 处理规则:
  - 状态必须来源于组 runtime，而不是内部链路 runtime。
  - 入口可达显示可用。
  - 入口不可达、开流失败、runtime 缺失显示不可用。
- 异常规则:
  - 状态字段缺失时按不可用处理。
  - 刷新失败不改变无回退约束。

## 风险
- U2 与 U3 若没有统一 runtime 生命周期，容易出现空指针或陈旧连接。
- U4 若仍混入内部链路 runtime 取值，业务边界会再次混淆。
- U6 若继续读取错误状态源，控制面会误报链路可用性。

## 结论
- 单元设计已明确“每组一个客户端 runtime、route 直接绑定 runtime 指针、内部链路互联独立维护”的落地方向，可直接指导 [`code`](probe_node/main.go:1) 模式实施。
