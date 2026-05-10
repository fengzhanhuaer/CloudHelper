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
| REQ-PN-TUN-PROXY-REFAC-001-R1 | T-001,T-002 | probe_node/local_tun_group_runtime.go; probe_node/local_tun_route.go; probe_node/local_tun_stack_windows.go | 已完成 | 通过 | 新增每组一个 tun proxy 客户端 runtime；route decision 直接挂接 GroupRuntime 指针；TCP/UDP 出站经组选 runtime 开流 | 覆盖“每组挂接一个客户端 runtime”主模型 |
| REQ-PN-TUN-PROXY-REFAC-001-R2 | T-003,T-004 | probe_node/local_console.go; probe_node/local_dns_service.go; probe_node/local_route_decision.go | 已完成 | 通过 | 控制面、DNS 路由提示、路由决策统一以 selected_chain_id 为主语义，同时保留 tunnel_node_id 兼容别名 | 覆盖 API/状态/路由语义迁移 |
| REQ-PN-TUN-PROXY-REFAC-001-R3 | T-005,T-006 | probe_node/local_console.go; probe_node/local_console_test.go; probe_node/local_route_decision_test.go; probe_node/local_tun_route_test.go; probe_node/local_tun_stack_windows_test.go | 已完成 | 通过 | proxy/status 改为读取组选 runtime keepalive/latency；不可用时输出运行态而非回退；相关回归测试通过 | 覆盖“不可用即不可用，无自动回退” |

## 结论
- 需求已按 Architect 边界实现：[`probe_node/local_tun_group_runtime.go`](probe_node/local_tun_group_runtime.go) 承载组选 tun proxy 客户端 runtime，[`probe_node/local_tun_route.go`](probe_node/local_tun_route.go) 直接消费 runtime 指针，[`probe_node/local_console.go`](probe_node/local_console.go) 与相关测试完成 `selected_chain_id` 控制面迁移。
- 自测结果为 `go test ./...` 通过。
