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
  - `probe_node` 中与组选链状态、组挂接 runtime、route 决策、TUN 出站、状态展示相关的实现与测试文件。
  - 需求对应的 Architect 与 Code 跟踪文档。
- 禁止修改:
  - `probe_manager` 运行时代码。
  - [`probe link chain`](probe_node/probe_link_chains_sync.go:97) 的内部互联业务语义。
  - 与本需求无关的认证、备份、系统升级、非代理控制面逻辑。

## 任务清单
| 任务编号 | 需求编号 | 单元编号 | 文件范围 | 操作类型 | 验收标准 |
|---|---|---|---|---|---|
| T-001 | REQ-PN-TUN-PROXY-REFAC-001 | U1 | `probe_node/local_console.go` `probe_node/local_route_decision.go` | 修改 | 代理组运行态与请求响应字段统一改为 `selected_chain_id` 语义 |
| T-002 | REQ-PN-TUN-PROXY-REFAC-001 | U2 | `probe_node/local_console.go` `probe_node/local_tun_group_runtime.go` | 修改 新增 | 为每个组建立并挂接唯一的 `tun proxy` 客户端 runtime，承载 socket 状态 错误 入口信息 |
| T-003 | REQ-PN-TUN-PROXY-REFAC-001 | U3 | `probe_node/local_route_decision.go` `probe_node/local_tun_route.go` | 修改 | route 命中组后直接绑定并传递组 runtime 指针，不再把 [`getProbeChainRuntime()`](probe_node/link_chain_runtime.go:2512) 作为主路径依赖 |
| T-004 | REQ-PN-TUN-PROXY-REFAC-001 | U4 | `probe_node/local_tun_route.go` `probe_node/local_tun_stack_windows.go` `probe_node/local_tun_group_runtime.go` | 修改 新增 | 基于组 runtime 独立维护链路入口客户端连接与后续代理行为 |
| T-005 | REQ-PN-TUN-PROXY-REFAC-001 | U5 | `probe_node/probe_link_chains_sync.go` `probe_node/link_chain_runtime.go` | 审核性最小修改 | 仅在元数据暴露 注释说明 或测试补强需要时最小修改，不得把内部链路 runtime 改造成组选链 runtime |
| T-006 | REQ-PN-TUN-PROXY-REFAC-001 | U6 | `probe_node/local_console.go` `probe_node/local_console_test.go` | 修改 | 状态输出必须来自组挂接 runtime，失败显示不可用，不做直连回退 |
| T-007 | REQ-PN-TUN-PROXY-REFAC-001 | U1 U2 U3 U4 U6 | `probe_node/local_console_test.go` `probe_node/local_route_decision_test.go` `probe_node/local_tun_route_test.go` `probe_node/local_tun_stack_windows_test.go` | 修改 新增 | 覆盖每组一个 runtime 指针 任意 chain 消费 入口开流失败 不可用无回退等回归场景 |
| T-008 | REQ-PN-TUN-PROXY-REFAC-001 | U1 U2 U3 U4 U5 U6 | `doc/Code/requirement-trace-matrix-REQ-PN-TUN-PROXY-REFAC-001.md` `doc/Code/interface-trace-matrix-REQ-PN-TUN-PROXY-REFAC-001.md` `doc/Code/test-item-trace-matrix-REQ-PN-TUN-PROXY-REFAC-001.md` `doc/Code/defect-trace-matrix-REQ-PN-TUN-PROXY-REFAC-001.md` | 新增 | 代码变更完成后补齐证据、接口映射、测试项与缺陷跟踪矩阵 |

## 源码修改规则
- 必须使用 [encoding_tools/README.md](encoding_tools/README.md:1) 描述的接口。
- 必须优先使用 [encoding_tools/encoding_safe_patch.py](encoding_tools/encoding_safe_patch.py:1)。
- 禁止直接普通编辑源代码。
- 必须保持“组挂接客户端 runtime”与“内部链路互联 runtime”业务隔离。

## 交付物
- `selected_chain_id` 语义重命名与控制面 API 调整。
- 每组一个 `tun proxy` 客户端 runtime 的实现。
- route 直接消费组 runtime 指针的出站代理逻辑。
- 不可用无回退的状态展示与回归测试。
- Code 阶段矩阵文档。

## 门禁输入
- 变更文件清单与 `encoding_tools` 执行记录。
- 单元测试与回归测试结果。
- 组 runtime 结构、生命周期与状态字段证据。
- route 直接消费组 runtime 指针的验证证据。
- 不再错误依赖 [`getProbeChainRuntime()`](probe_node/link_chain_runtime.go:2512) 作为组选链主路径的实现证据。
- 需求与接口追踪矩阵更新结果。

## 结论
- 任务包已按“每组挂接一个 `tun proxy` 客户端 runtime”重写，Code 阶段必须围绕该模型实施。
