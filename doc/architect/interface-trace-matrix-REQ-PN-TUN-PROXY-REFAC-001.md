# 关键接口跟踪矩阵

- 适用规则: AI协作规则
- 后续工作传递声明: 本文档必须传递给后续阶段与后续角色。
- 需求编号: REQ-PN-TUN-PROXY-REFAC-001
- 需求后缀: REQ-PN-TUN-PROXY-REFAC-001
- 当前角色: Architect
- 工作依据文档: [doc/architect/architecture-REQ-PN-TUN-PROXY-REFAC-001.md](doc/architect/architecture-REQ-PN-TUN-PROXY-REFAC-001.md:1)
- 状态: 进行中

| 接口编号 | 需求编号 | 接口名称 | 调用方 | 提供方 | 输入 | 输出 | 状态 | 备注 |
|---|---|---|---|---|---|---|---|---|
| IF-001 | REQ-PN-TUN-PROXY-REFAC-001-R1 | GetOrInitGroupRuntime | Route Runtime Binding | Group Runtime Registry | group | runtime state | 进行中 | 每组唯一对象 |
| IF-002 | REQ-PN-TUN-PROXY-REFAC-001-R1 | UpdateGroupRuntimeByPolicy | 控制面策略写入 | Group Runtime Registry | group action tunnel_node_id | updated runtime | 进行中 | 兼容旧写入接口 |
| IF-003 | REQ-PN-TUN-PROXY-REFAC-001-R1 | ResolveRouteByDomainWithRuntime | TUN Route | Route Runtime Binding | domain target rules runtime | route decision | 进行中 | direct reject tunnel |
| IF-004 | REQ-PN-TUN-PROXY-REFAC-001-R1 | EnsureGroupTunnelRuntime | TCP UDP 出站 | Tunnel Runtime Resolver | route decision runtime | stream or error | 进行中 | chain runtime 校验 |
| IF-005 | REQ-PN-TUN-PROXY-REFAC-001-R1 | BuildGroupRuntimeSnapshot | 状态接口 调试接口 | Group Runtime Registry | runtime store | snapshot payload | 进行中 | 对齐 manager 语义 |
| IF-006 | REQ-PN-TUN-PROXY-REFAC-001-R1 | ProjectTCPDebugPayload | TCP forwarder | Debug Projection | tcp relay route runtime | tcp debug payload | 进行中 | 字段对齐 |
| IF-007 | REQ-PN-TUN-PROXY-REFAC-001-R1 | ProjectUDPDebugPayload | UDP association | Debug Projection | udp lifecycle route runtime | udp debug payload | 进行中 | 字段对齐 |
| IF-008 | REQ-PN-TUN-PROXY-REFAC-001-R2 | BuildProxyLatencyStatusView | Proxy Status Handler | Panel Latency Status Sync | chain keepalive latency probe result | latency status view | 进行中 | 支持 `reachable` `unreachable` |
| IF-009 | REQ-PN-TUN-PROXY-REFAC-001-R2 | StartPanelProxyStatusPolling | Panel Script Runtime | Panel Latency Status Sync | poll trigger page lifecycle | periodic `loadProxyStatus` call | 进行中 | 固定 60 秒且单定时器 |
