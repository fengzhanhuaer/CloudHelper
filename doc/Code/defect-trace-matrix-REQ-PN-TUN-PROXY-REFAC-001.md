# 缺陷跟踪矩阵

- 适用规则: AI协作规则
- 后续工作传递声明: 本文档必须传递给后续阶段与后续角色。
- 需求编号: REQ-PN-TUN-PROXY-REFAC-001
- 需求后缀: REQ-PN-TUN-PROXY-REFAC-001
- 当前角色: Code
- 工作依据文档: doc/architect/code-task-package-REQ-PN-TUN-PROXY-REFAC-001.md
- 状态: 已完成

| 缺陷编号 | 需求编号 | 测试项编号 | 缺陷描述 | 严重级别 | 修复状态 | 修复证据 | 备注 |
|---|---|---|---|---|---|---|---|
| DEF-001 | REQ-PN-TUN-PROXY-REFAC-001-R1 | TI-001 | TUN 代理此前误把组选链消费建立在 probe link chain 内部 runtime 上，边界错误 | 高 | 已完成 | 新增 [`probe_node/local_tun_group_runtime.go`](probe_node/local_tun_group_runtime.go)，按 group 独立维护 tun proxy 客户端 runtime | 已与链路内部 runtime 解耦 |
| DEF-002 | REQ-PN-TUN-PROXY-REFAC-001-R2 | TI-002,TI-003 | 控制面与路由层仍以 tunnel_node_id 为主语义，导致 API/状态表达与业务边界不一致 | 高 | 已完成 | enable/select/status、DNS route hint、route decision 已切换为 selected_chain_id 主语义，并保留 legacy alias | 完成兼容迁移 |
| DEF-003 | REQ-PN-TUN-PROXY-REFAC-001-R3 | TI-004,TI-005 | direct/reject 与不可用场景可能误清空组选链或走回退语义，削弱“不可用即不可用”要求 | 高 | 已完成 | proxy/status 读取组选 runtime；direct/reject 保留 selected chain 状态；相关测试通过 | 满足 fail-closed 要求 |
| DEF-000 | REQ-PN-TUN-PROXY-REFAC-001-R1,R2,R3 | TI-001~TI-006 | 本轮实现与回归未发现新增阻塞缺陷 | 无 | 已完成 | `go test ./...` 通过 | 无新增阻塞项 |

## 结论
- 本轮缺陷均围绕“边界纠偏、控制面纠偏、不可用语义纠偏”完成闭环修复。
- 当前提交范围内无新增阻塞缺陷。
