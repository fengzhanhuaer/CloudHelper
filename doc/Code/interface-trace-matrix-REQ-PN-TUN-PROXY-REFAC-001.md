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
| IF-001 | REQ-PN-TUN-PROXY-REFAC-001-R1 | probe_node/local_tun_group_runtime.go | 路由决策层 / TUN 出站层 | Group Runtime Registry | 已完成 | ensureProbeLocalTUNGroupRuntime、currentProbeLocalTUNGroupRuntime、resolveProbeLocalTUNGroupRuntimeKeepaliveAndLatency 已落地 | 每组独立维护 selected chain、session、状态、错误 |
| IF-002 | REQ-PN-TUN-PROXY-REFAC-001-R1 | probe_node/local_tun_route.go | TUN TCP/UDP 开流路径 | Group Runtime Registry | 已完成 | decideProbeLocalRouteForTarget 返回 GroupRuntime；openProbeLocalTunnelConnWithGroupRuntime 使用组选 runtime 开流 | 组选链主路径不再依赖 getProbeChainRuntime |
| IF-003 | REQ-PN-TUN-PROXY-REFAC-001-R2 | probe_node/local_console.go | /local/api/proxy/enable, /select | Proxy Control Plane | 已完成 | 请求体支持 selected_chain_id，响应 selection 以 selected_chain_id 为主并保留 tunnel_node_id 兼容字段 | 完成控制面语义迁移 |
| IF-004 | REQ-PN-TUN-PROXY-REFAC-001-R2 | probe_node/local_route_decision.go; probe_node/local_dns_service.go | 域名规则决策 / Fake-IP 提示 | Route Hint Projection | 已完成 | DNS route decision、fake IP entry、tunnel route decision 均携带 selected_chain_id | 打通 DNS -> route -> tunnel 语义链 |
| IF-005 | REQ-PN-TUN-PROXY-REFAC-001-R3 | probe_node/local_console.go | /local/api/proxy/status | Group Runtime Status Projection | 已完成 | proxy/status 从组选 runtime 读取 keepalive/latency，不再从链路内部 runtime 推导组选状态 | 保持链路内部 runtime 与 tun proxy runtime 边界分离 |
| IF-006 | REQ-PN-TUN-PROXY-REFAC-001-R3 | probe_node/local_console.go; probe_node/local_console_test.go | proxy state 持久化/查询 | Proxy Runtime State Store | 已完成 | state group entry 同时持久化 selected_chain_id 与 tunnel_node_id，direct/reject 不清空既有选链 | 满足“关闭转发仍保留组选链” |

## 结论
- 本轮接口改造已完成三条主链路闭环：控制面写入、DNS/路由决策投影、TUN 出站 runtime 消费。
- 组选 tun proxy 客户端 runtime 与 probe link chain 内部 runtime 已在接口层彻底分离。
