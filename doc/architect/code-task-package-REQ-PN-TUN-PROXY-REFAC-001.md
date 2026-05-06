# Code任务执行包文档

- 适用规则: AI协作规则
- 后续工作传递声明: 本文档必须传递给后续阶段与后续角色。
- 需求编号: REQ-PN-TUN-PROXY-REFAC-001
- 需求后缀: REQ-PN-TUN-PROXY-REFAC-001
- 当前角色: Architect
- 工作依据文档: [doc/architect/requirements-REQ-PN-TUN-PROXY-REFAC-001.md](doc/architect/requirements-REQ-PN-TUN-PROXY-REFAC-001.md:1)、[doc/architect/architecture-REQ-PN-TUN-PROXY-REFAC-001.md](doc/architect/architecture-REQ-PN-TUN-PROXY-REFAC-001.md:1)、[doc/architect/unit-design-REQ-PN-TUN-PROXY-REFAC-001.md](doc/architect/unit-design-REQ-PN-TUN-PROXY-REFAC-001.md:1)
- 状态: 进行中

## 执行边界
- 允许修改:
  - `probe_node` 中与组级策略运行态 路由决策 TUN 出站调试投影 面板链路状态相关的实现与测试文件。
  - 需求对应的 Architect 与 Code 跟踪文档。
- 禁止修改:
  - `probe_manager` 运行时代码。
  - 与本需求无关的控制面业务接口与认证逻辑。

## 任务清单
| 任务编号 | 需求编号 | 单元编号 | 文件范围 | 操作类型 | 验收标准 |
|---|---|---|---|---|---|
| T-001 | REQ-PN-TUN-PROXY-REFAC-001 | U1 | `probe_node/local_console.go` `probe_node/local_route_decision.go` | 修改 | 形成每组唯一 runtime 状态模型，避免同组多状态来源 |
| T-002 | REQ-PN-TUN-PROXY-REFAC-001 | U2 | `probe_node/local_tun_route.go` `probe_node/local_dns_service.go` | 修改 | route 决策消费组级 runtime，tunnel 缺节点时返回结构化错误 |
| T-003 | REQ-PN-TUN-PROXY-REFAC-001 | U3 | `probe_node/local_tun_stack_windows.go` `probe_node/link_chain_runtime.go` | 修改 | tunnel 开流路径按组级 runtime 解析并输出一致错误口径 |
| T-004 | REQ-PN-TUN-PROXY-REFAC-001 | U4 | `probe_node/tcp_debug.go` | 修改 | TCP 调试输出字段语义对齐 manager，包含 `group` `node_id` `route_target` `direct` `transport` |
| T-005 | REQ-PN-TUN-PROXY-REFAC-001 | U5 | `probe_node/udp_assoc_debug.go` `probe_node/link_chain_udp_assoc.go` | 修改 | UDP 调试输出字段语义对齐 manager，保留 association 生命周期关键信息 |
| T-006 | REQ-PN-TUN-PROXY-REFAC-001 | U6 | `probe_node/local_console_test.go` `probe_node/local_route_decision_test.go` `probe_node/local_tun_route_test.go` `probe_node/local_tun_stack_windows_test.go` | 修改 新增 | 增加组级 runtime 唯一性与字段对齐测试，回归通过 |
| T-007 | REQ-PN-TUN-PROXY-REFAC-001 | U7 | `probe_node/local_console.go` `probe_node/local_pages/panel.html` `probe_node/local_console_test.go` `probe_node/local_pages_routes_test.go` | 修改 新增 | 后端输出延迟可达性状态字段，前端失败显示 `不可达` 成功显示毫秒值，并以 60 秒单定时器自动刷新 |

## 源码修改规则
- 必须使用 [`encoding_tools/README.md`](encoding_tools/README.md:1) 描述的接口。
- 必须优先使用 [`encoding_tools/encoding_safe_patch.py`](encoding_tools/encoding_safe_patch.py:1)。
- 禁止直接普通编辑源代码。

## 交付物
- `probe_node` 组级 runtime 模型重构代码。
- TCP UDP 调试口径对齐实现与测试。
- `local/panel` 最近测试延迟状态语义修复与 60 秒自动刷新实现及测试。
- Code 阶段矩阵文档:
  - `doc/Code/requirement-trace-matrix-REQ-PN-TUN-PROXY-REFAC-001.md`
  - `doc/Code/interface-trace-matrix-REQ-PN-TUN-PROXY-REFAC-001.md`
  - `doc/Code/test-item-trace-matrix-REQ-PN-TUN-PROXY-REFAC-001.md`
  - `doc/Code/defect-trace-matrix-REQ-PN-TUN-PROXY-REFAC-001.md`

## 门禁输入
- 变更文件清单与 `encoding_tools` 执行记录。
- 单元测试与回归测试结果。
- manager 与 node 字段映射对齐证据。
- 面板延迟不可达与 60 秒刷新行为的验证证据。
- 需求与接口追踪矩阵更新结果。

## 结论
- 任务包已明确实现边界、任务拆分与验收口径，可直接进入 Code 模式执行。
