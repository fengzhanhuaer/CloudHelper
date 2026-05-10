# 需求文档

- 适用规则: AI协作规则
- 后续工作传递声明: 本文档必须传递给后续阶段与后续角色。
- 需求编号: REQ-PN-TUN-PROXY-REFAC-001
- 需求后缀: REQ-PN-TUN-PROXY-REFAC-001
- 当前角色: Architect
- 工作依据文档: [doc/ai-coding-collaboration.md](doc/ai-coding-collaboration.md:1)、[resolveProbeLocalProxyRouteDecisionByDomain()](probe_node/local_route_decision.go:5)、[decideProbeLocalRouteForTarget()](probe_node/local_tun_route.go:39)、[openProbeLocalTunnelConnWithAssociation()](probe_node/local_tun_route.go:117)、[getProbeChainRuntime()](probe_node/link_chain_runtime.go:2512)、[startProbeLinkChainsSyncLoop()](probe_node/probe_link_chains_sync.go:97)
- 状态: 进行中

## 需求目标
- 明确 [`probe link chain`](probe_node/probe_link_chains_sync.go:97) 与 [`tun proxy`](probe_node/local_tun_route.go:117) 属于两个独立业务域。
- 将代理组选链语义从旧的 [`tunnel_node_id`](probe_node/local_console.go:127) 统一收敛为 `selected_chain_id`。
- 让 [`tun proxy`](probe_node/local_tun_route.go:117) 按 [`proxy group`](probe_node/local_console.go:106) 为每个组独立维护一个代理链路 runtime。
- 组 runtime 必须挂接在组上，保存与该组选中链路有关的 socket、连接状态、错误信息、入口元数据、更新时间等运行态信息。
- 当路由决策命中某个组时，后续代理行为直接消费该组挂接的 runtime 指针，而不是再去借用 [`getProbeChainRuntime()`](probe_node/link_chain_runtime.go:2512) 返回的内部链路 runtime。
- 当选中链路不可用时，只显示不可用并直接失败，不允许自动回退到直连。

## 现状摘要
- 当前域名命中后由 [resolveProbeLocalProxyRouteDecisionByDomain()](probe_node/local_route_decision.go:5) 读取 [`proxy_state.json`](probe_node/local_console.go:40) 中的组动作与旧字段 [`tunnel_node_id`](probe_node/local_console.go:127)。
- 当前 TUN 开流在 [openProbeLocalTunnelConnWithAssociation()](probe_node/local_tun_route.go:117) 中依赖 [getProbeChainRuntime()](probe_node/link_chain_runtime.go:2512)。
- [getProbeChainRuntime()](probe_node/link_chain_runtime.go:2512) 提供的是当前 [`probe_node`](probe_node) 进程中、与本节点相关的 chain 内部互联运行态句柄，不适合作为 `tun proxy` 的组选中链路 runtime。
- 当前模型把“链路内部互联 runtime”和“链路外部客户端 runtime”混在一起，导致 [`tun proxy`](probe_node/local_tun_route.go:117) 无法正确消费与本节点无关但被组选择到的 chain。

## 需求范围
- 覆盖 [`proxy group`](probe_node/local_console.go:106) 的选链配置、持久化结构、状态输出与命名调整。
- 覆盖 `tun proxy` 侧的组选中链路 runtime 模型，要求一组一个 runtime，并挂接在组上。
- 覆盖 route 决策逻辑，使其在命中组后直接携带或引用该组 runtime 指针进入后续代理行为。
- 覆盖 `tun proxy` 到链路入口的独立客户端连接维护逻辑。
- 覆盖 [`probe link chain`](probe_node/probe_link_chains_sync.go:97) 与 `tun proxy` 的职责隔离说明与相关测试。
- 覆盖链路不可用展示语义，但不扩展到无关 UI 功能面。

## 非范围
- 不改变 [`probe link chain`](probe_node/probe_link_chains_sync.go:97) 的拓扑生成、成员角色判定与内部互联维护算法。
- 不调整 `probe_manager` 代码。
- 不新增新的代理协议或第三方依赖。
- 不在 Architect 阶段直接修改 Go 源码。

## 验收标准
- 对外请求、持久化、状态接口不再把“选中的链路”表达成节点语义字段。
- `tun proxy` 为每个命中的组维护独立的组选链 runtime，并且该 runtime 挂接在组上。
- 组选链 runtime 至少承载 `selected_chain_id`、入口连接信息、socket 或 session 持有体、状态、错误、更新时间等信息。
- route 决策命中组后，后续代理行为直接消费该组 runtime 指针，不再调用 [getProbeChainRuntime()](probe_node/link_chain_runtime.go:2512) 作为业务主路径。
- `tun proxy` 可以消费任意 chain，不以“本节点是否属于该 chain”作为前提。
- [`probe link chain`](probe_node/probe_link_chains_sync.go:97) 仍然只维护与本节点有关的链路内部互联。
- 选中链路不存在、入口不可达、入口开流失败时，连接直接失败并显示不可用，不发生 `direct` 自动回退。

## 风险
- 旧字段 [`tunnel_node_id`](probe_node/local_console.go:127) 分布在请求、状态、测试多处，改名容易遗漏。
- 若 route 层只改命名但不改运行态归属，仍会继续错误依赖 [getProbeChainRuntime()](probe_node/link_chain_runtime.go:2512)。
- 若组选链 runtime 与组配置同步不完整，可能出现组已切换但旧连接仍被复用的问题。
- UDP association 元信息可能继续残留旧 node 语义，需要在 Code 阶段统一校正。

## 关键业务边界
### [`probe link chain`](probe_node/probe_link_chains_sync.go:97)
- 独立按配置建立链路。
- 仅建立与本节点有关的链路内部互联。
- 维护 chain 内部的拓扑、成员角色、上下游连接与自动恢复。

### [`tun proxy`](probe_node/local_tun_route.go:117)
- 是链路外部客户端。
- 可以消费任意被组选中的 chain。
- 需要为每个组独立维护组选中链路 runtime。
- 后续代理行为直接使用组上挂接的 runtime 指针。

### [`proxy group`](probe_node/local_console.go:106)
- 只负责选择哪条 chain。
- 每个组挂接一个 `tun proxy` 专属 runtime。
- 组切换 chain 时，需要同步切换挂接 runtime。

## 结论
- 本需求已统一为“选链归组、运行态归组、代理行为直接消费组 runtime 指针、内部互联仍归 [`probe link chain`](probe_node/probe_link_chains_sync.go:97)”的模型。
- 后续 Architect 与 Code 阶段必须严格按这条边界推进实现。
