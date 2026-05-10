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
| TI-001 | REQ-PN-TUN-PROXY-REFAC-001-R1 | T-001,T-002 | 按组绑定 tun proxy 客户端 runtime，并在隧道路由决策中直接返回 runtime 指针 | 运行单测 [`probe_node/local_tun_route_test.go`](probe_node/local_tun_route_test.go) 中 tunnel 路由场景 | 通过 | `TestDecideProbeLocalRouteForTargetTunnelByDomainRule`、`TestDecideProbeLocalRouteForTargetTunnelByFakeIP` 断言 `GroupRuntime != nil` | 覆盖组选 runtime 主路径 |
| TI-002 | REQ-PN-TUN-PROXY-REFAC-001-R2 | T-003,T-004 | 控制面 primary field 切换到 selected_chain_id，兼容返回 tunnel_node_id | 运行单测 [`probe_node/local_console_test.go`](probe_node/local_console_test.go) 中 enable/select/status 场景 | 通过 | `TestProbeLocalProxyEnableSelectionWritesRuntimeState`、`TestProbeLocalProxySelectSelectionWritesRuntimeStateWithoutEnablingProxy`、状态查询场景断言通过 | 覆盖请求/响应/状态持久化 |
| TI-003 | REQ-PN-TUN-PROXY-REFAC-001-R2 | T-004 | 域名规则与 Fake-IP 路由提示携带 selected_chain_id | 运行单测 [`probe_node/local_route_decision_test.go`](probe_node/local_route_decision_test.go) 与 [`probe_node/local_tun_route_test.go`](probe_node/local_tun_route_test.go) | 通过 | `TestResolveProbeLocalProxyRouteDecisionByDomainTunnel` 与 Fake-IP tunnel 场景均断言 selected_chain_id | 覆盖 DNS -> route 投影 |
| TI-004 | REQ-PN-TUN-PROXY-REFAC-001-R3 | T-005 | direct/reject 不回退、不丢失已选链，仅改变转发动作 | 运行单测 [`probe_node/local_console_test.go`](probe_node/local_console_test.go) 中 direct/reject keep selected tunnel 场景 | 通过 | `TestProbeLocalProxyDirectKeepsSelectedTunnelWhenForwardingDisabled`、`TestProbeLocalProxyRejectKeepsSelectedTunnelWhenForwardingBlocked` | 覆盖 fail-closed 语义 |
| TI-005 | REQ-PN-TUN-PROXY-REFAC-001-R1,R3 | T-002,T-005 | Windows TUN TCP/UDP 假 IP 路径继续可走组选 runtime 决策 | 运行单测 [`probe_node/local_tun_stack_windows_test.go`](probe_node/local_tun_stack_windows_test.go) | 通过 | `TestProbeLocalTUNSimplePacketStackWriteTunnelValidatesSelectedChain`、`TestProbeLocalTUNSimplePacketStackWriteTunnelValidatesSelectedChainUDPFakeIP` | 覆盖 Windows 包转发入口 |
| TI-006 | REQ-PN-TUN-PROXY-REFAC-001-R1,R2,R3 | T-001~T-006 | probe_node 全量回归 | 执行 `go test ./...` | 通过 | 输出: `ok github.com/cloudhelper/probe_node` | 全量回归通过 |

## 结论
- 组选 runtime、selected_chain_id 控制面迁移、无回退行为与 Windows TUN 路径均已完成自测闭环。
