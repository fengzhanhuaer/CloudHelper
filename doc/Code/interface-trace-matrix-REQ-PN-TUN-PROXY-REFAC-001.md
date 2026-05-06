# 关键接口跟踪矩阵

- 适用规则: AI协作规则
- 后续工作传递声明: 本文档必须传递给后续阶段与后续角色。
- 需求编号: REQ-PN-TUN-PROXY-REFAC-001
- 需求后缀: REQ-PN-TUN-PROXY-REFAC-001
- 当前角色: Code
- 工作依据文档: doc/architect/code-task-package-REQ-PN-TUN-PROXY-REFAC-001.md
- 状态: 已完成

| 接口编号 | 需求编号 | 实现文件 | 调用方 | 提供方 | 实现状态 | 证据 | 备注 |
|---|---|---|---|---|---|---|---|
| IF-001 | REQ-PN-TUN-PROXY-REFAC-001-R1 | probe_node/local_console.go | Route Decision Adapter | Group Runtime Registry | 已完成 | upsertProbeLocalRuntimeStateGroup 按 group 命中更新或新增，保持单组唯一条目 | 对应 T-001 |
| IF-002 | REQ-PN-TUN-PROXY-REFAC-001-R1 | probe_node/local_console.go | 控制面策略写入流程 | Group Runtime Registry | 已完成 | /local/api/proxy/enable,/direct,/reject 统一写入 group/action/tunnel_node_id/runtime_status | 对应 T-001 |
| IF-003 | REQ-PN-TUN-PROXY-REFAC-001-R1 | probe_node/local_route_decision.go; probe_node/local_tun_route.go | TUN route 层 | Route Decision Adapter | 已完成 | resolveProbeLocalProxyRouteDecisionByDomain + decideProbeLocalRouteForTarget 完成 direct/reject/tunnel 决策绑定 | 对应 T-002 |
| IF-004 | REQ-PN-TUN-PROXY-REFAC-001-R1 | probe_node/local_tun_route.go | TUN TCP/UDP 出站 | Tunnel Runtime Resolver | 已完成 | openProbeLocalTunnelConnWithAssociation 在 chain runtime 缺失时返回结构化错误 | 对应 T-003 |
| IF-005 | REQ-PN-TUN-PROXY-REFAC-001-R1 | probe_node/local_console.go | 状态接口/调试接口 | Group Runtime Registry | 已完成 | proxy/state 与 proxy/status 输出组与链路运行态快照字段 | 对应 T-001,T-006 |
| IF-006 | REQ-PN-TUN-PROXY-REFAC-001-R1 | probe_node/tcp_debug.go | TCP forwarder | Debug Projection | 已完成 | TCP active/failure 均投影 group,node_id,route_target,direct,transport | 对应 T-004 |
| IF-007 | REQ-PN-TUN-PROXY-REFAC-001-R1 | probe_node/udp_assoc_debug.go; probe_node/link_chain_udp_assoc.go | UDP association | Debug Projection | 已完成 | UDP 投影包含 assoc_key_v2,flow_id,route_target,route_fingerprint,group,node_id,transport | 对应 T-005 |
| IF-008 | REQ-PN-TUN-PROXY-REFAC-001-R2 | probe_node/local_console.go | Proxy Status Handler | Panel Latency Status Sync | 已完成 | resolveProbeLocalChainKeepaliveAndLatency + proxy/status 输出 selected_chain_latency_status | 对应 T-007 |
| IF-009 | REQ-PN-TUN-PROXY-REFAC-001-R2 | probe_node/local_pages/panel.html | Panel Script Runtime | Panel Latency Status Sync | 已完成 | startProxyStatusPolling 固定 60000ms 且单定时器，formatRuntimeStatusText 按 reachable/unreachable 渲染 | 对应 T-007 |

## 结论
- 接口编号 IF-001~IF-009 与 Architect 侧保持一致，均已在代码中落地。
