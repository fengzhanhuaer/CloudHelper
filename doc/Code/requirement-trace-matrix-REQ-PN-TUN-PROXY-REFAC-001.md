# 需求跟踪矩阵

- 适用规则: AI协作规则
- 后续工作传递声明: 本文档必须传递给后续阶段与后续角色。
- 需求编号: REQ-PN-TUN-PROXY-REFAC-001
- 需求后缀: REQ-PN-TUN-PROXY-REFAC-001
- 当前角色: Code
- 工作依据文档: doc/architect/code-task-package-REQ-PN-TUN-PROXY-REFAC-001.md
- 状态: 已完成

| 需求编号 | 任务编号 | 实现文件 | 实现状态 | 自测状态 | 证据 | 备注 |
|---|---|---|---|---|---|---|
| REQ-PN-TUN-PROXY-REFAC-001-R1 | T-001,T-006 | probe_node/local_console.go; probe_node/local_route_decision.go; probe_node/local_console_test.go; probe_node/local_route_decision_test.go | 已完成 | 通过 | upsertProbeLocalRuntimeStateGroup 已按组唯一更新运行态；TestResolveProbeLocalProxyRouteDecisionByDomainTunnel 通过 | 覆盖 U1,U2 |
| REQ-PN-TUN-PROXY-REFAC-001-R1 | T-002,T-003,T-006 | probe_node/local_tun_route.go; probe_node/local_dns_service.go; probe_node/local_tun_stack_windows.go; probe_node/local_tun_route_test.go; probe_node/local_tun_stack_windows_test.go | 已完成 | 通过 | decideProbeLocalRouteForTarget/openProbeLocalTunnelConnWithAssociation 已消费组级决策并在缺节点时返回错误；对应测试通过 | 覆盖 U2,U3 |
| REQ-PN-TUN-PROXY-REFAC-001-R1 | T-004 | probe_node/tcp_debug.go | 已完成 | 通过 | TCP 调试项包含 group/node_id/route_target/direct/transport 字段 | 覆盖 U4 |
| REQ-PN-TUN-PROXY-REFAC-001-R1 | T-005 | probe_node/udp_assoc_debug.go; probe_node/link_chain_udp_assoc.go | 已完成 | 通过 | UDP 调试项包含 assoc_key_v2/flow_id/route_target/route_fingerprint/group/node_id/transport 字段 | 覆盖 U5 |
| REQ-PN-TUN-PROXY-REFAC-001-R2 | T-007 | probe_node/local_console.go; probe_node/local_pages/panel.html; probe_node/local_console_test.go; probe_node/local_pages_routes_test.go | 已完成 | 通过 | proxy/status 输出 selected_chain_latency_status；panel 60s 单定时器轮询并渲染“不可达/毫秒值” | 覆盖 U7 |

## 结论
- 按任务包 T-001 到 T-007 复核并完成实现闭环，需求项已全部落地并经测试通过。
