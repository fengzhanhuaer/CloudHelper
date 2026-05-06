# 门禁裁判文档

- 适用规则: AI协作规则
- 后续工作传递声明: 本文档必须传递给后续阶段与后续角色。
- 需求编号: REQ-PN-TUN-PROXY-REFAC-001
- 需求后缀: REQ-PN-TUN-PROXY-REFAC-001
- 当前角色: Architect
- 工作依据文档: [doc/architect/requirements-REQ-PN-TUN-PROXY-REFAC-001.md](doc/architect/requirements-REQ-PN-TUN-PROXY-REFAC-001.md:1)
- 状态: 进行中

## 门禁输入
| 文档 | 路径 | 状态 |
|---|---|---|
| 需求文档 | [requirements-REQ-PN-TUN-PROXY-REFAC-001.md](doc/architect/requirements-REQ-PN-TUN-PROXY-REFAC-001.md) | 已提供 |
| 总体架构文档 | [architecture-REQ-PN-TUN-PROXY-REFAC-001.md](doc/architect/architecture-REQ-PN-TUN-PROXY-REFAC-001.md) | 已提供 |
| 单元设计文档 | [unit-design-REQ-PN-TUN-PROXY-REFAC-001.md](doc/architect/unit-design-REQ-PN-TUN-PROXY-REFAC-001.md) | 已提供 |
| Code任务执行包文档 | [code-task-package-REQ-PN-TUN-PROXY-REFAC-001.md](doc/architect/code-task-package-REQ-PN-TUN-PROXY-REFAC-001.md) | 已提供 |
| 需求跟踪矩阵 | [requirement-trace-matrix-REQ-PN-TUN-PROXY-REFAC-001.md](doc/architect/requirement-trace-matrix-REQ-PN-TUN-PROXY-REFAC-001.md) | 已提供 |
| 关键接口跟踪矩阵 | [interface-trace-matrix-REQ-PN-TUN-PROXY-REFAC-001.md](doc/architect/interface-trace-matrix-REQ-PN-TUN-PROXY-REFAC-001.md) | 已提供 |

## 裁判检查
| 检查项 | 结果 | 证据 | 备注 |
|---|---|---|---|
| 必需文档存在 | 通过 | 上述文档均已生成 | 待 Code 文档生成后进入下一次门禁 |
| 需求后缀一致 | 通过 | 文件名均为 `-REQ-PN-TUN-PROXY-REFAC-001.md` | 一致 |
| 需求编号一致 | 通过 | 各文档头部 `需求编号` 一致 | 一致 |
| 接口编号一致 | 通过 | 接口矩阵 IF-001 至 IF-009 | 已纳入 panel 延迟新增接口 |
| 模板字段完整 | 通过 | 各文档包含规则要求字段 | 一致 |
| 延迟语义一致性 | 通过 | 需求与单元设计均定义失败 `不可达` 成功毫秒值 | 待 Code 验证字段实现 |
| 60秒刷新机制定义 | 通过 | 架构 IF-009 与任务 T-007 已定义单定时器 60 秒轮询 | 待 Code 验证前端实现 |
| Code使用encoding_tools | 无 | Code 阶段未开始 | 待执行 |
| Code证据完整 | 无 | Code 阶段未开始 | 待执行 |

## 冲突记录
| 冲突编号 | 冲突条款 | 最终采用条款 | 裁决人 | 裁决结论 |
|---|---|---|---|---|
| 无 | 无 | 无 | Architect | 无 |

## 裁判结论
- 结论: 有条件通过
- 放行阻塞: 阻塞
- 条件:
  - Code 阶段必须按 [code-task-package-REQ-PN-TUN-PROXY-REFAC-001.md](doc/architect/code-task-package-REQ-PN-TUN-PROXY-REFAC-001.md) 执行。
  - Code 阶段必须实现 `selected_chain_latency_status` 语义与前端 `不可达` 渲染。
  - Code 阶段必须实现 60 秒固定轮询 `loadProxyStatus` 且保持单定时器。
  - Code 阶段必须输出 `doc/Code` 四份矩阵文档并提供 `encoding_tools` 执行证据。
- 整改要求:
  - 无需整改 Architect 文档。
  - 进入 Code 后完成执行证据回填并触发二次门禁。

## 结论
- Architect 阶段文档包已齐备，当前门禁为有条件通过且阻塞到 Code 证据提交完成。
