# 测试项跟踪矩阵

- 适用规则: AI协作规则
- 后续工作传递声明: 本文档必须传递给后续阶段与后续角色。
- 需求编号: REQ-PN-TUN-PROXY-REFAC-001
- 需求后缀: REQ-PN-TUN-PROXY-REFAC-001
- 当前角色: Code
- 工作依据文档: doc/architect/code-task-package-REQ-PN-TUN-PROXY-REFAC-001.md
- 状态: 已完成

| 测试项编号 | 需求编号 | 任务编号 | 测试目标 | 测试方法 | 结果 | 证据 | 备注 |
|---|---|---|---|---|---|---|---|
| TI-001 | REQ-PN-TUN-PROXY-REFAC-001-R1 | T-001 | 组级 runtime 写入唯一性与状态字段正确性 | go test 定向运行 TestProbeLocalProxyFlowWithSession | 通过 | 命令: go test -run "TestProbeLocalProxyFlowWithSession" ./... | 覆盖 local_console |
| TI-002 | REQ-PN-TUN-PROXY-REFAC-001-R1 | T-002 | 域名策略到 tunnel 决策绑定正确 | go test 定向运行 TestResolveProbeLocalProxyRouteDecisionByDomainTunnel | 通过 | 命令: go test -run "TestResolveProbeLocalProxyRouteDecisionByDomainTunnel" ./... | 覆盖 local_route_decision |
| TI-003 | REQ-PN-TUN-PROXY-REFAC-001-R1 | T-002,T-003 | route 决策消费组级 runtime，tunnel 缺节点/无 runtime 错误路径可诊断 | go test 定向运行 TestDecideProbeLocalRouteForTargetTunnelByDomainRule, TestProbeLocalTUNSimplePacketStackWriteTunnelValidatesNodeUDPFakeIP | 通过 | 命令: go test -run "TestDecideProbeLocalRouteForTargetTunnelByDomainRule|TestProbeLocalTUNSimplePacketStackWriteTunnelValidatesNodeUDPFakeIP" ./... | 覆盖 tun route + tun stack |
| TI-004 | REQ-PN-TUN-PROXY-REFAC-001-R1 | T-004,T-005 | TCP/UDP 调试字段语义与 manager 对齐 | 静态字段校验 + go test 定向运行 TestSnapshotProbeUDPAssociationsIncludesSourceFields | 通过 | 命令: go test -run "TestSnapshotProbeUDPAssociationsIncludesSourceFields" ./... | 覆盖 tcp_debug/udp_assoc |
| TI-005 | REQ-PN-TUN-PROXY-REFAC-001-R2 | T-007 | panel 延迟语义与 60s 轮询存在性 | go test 定向运行 TestProbeLocalPanelServedAfterLogin（静态页面断言） | 通过 | 命令: go test -run "TestProbeLocalPanelServedAfterLogin" ./... | 断言 selected_chain_latency_status/不可达/60000 |
| TI-006 | REQ-PN-TUN-PROXY-REFAC-001-R1,R2 | T-001~T-007 | 全量回归 | go test ./... | 通过 | 输出: ok github.com/cloudhelper/probe_node | 全部测试通过 |

## 结论
- 任务包范围内测试项已覆盖并通过，满足提交门禁的自测要求。
