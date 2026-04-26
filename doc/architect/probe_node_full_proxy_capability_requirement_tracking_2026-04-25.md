# 需求跟踪表 `probe_node` Windows 先行完整代理能力 2026-04-25

## 工作依据与规则传递声明
- 当前角色: 架构师
- 工作依据文档: `doc/ai-coding-unified-rules.md`
- 适用规则: AI协作统一规则 单一规范
- 规则遵循声明: 必须遵守本规则。
- 协作传递要求: 后续接手者与协作者必须遵守同一规则，不得降级或替换执行口径。

- 日期: 2026-04-25
- 备注: 对应 Windows 先行完整代理能力实施，目标覆盖 TUN 数据面 TCP UDP 分流与按组走代理链路。Linux 不回归。
- 风险:
  - 数据面接入后失败回滚复杂度提升。
  - 链路选择与规则状态一致性需重点验证。
- 遗留事项:
  - Linux 全量数据面对齐后续另立项。
- 进度状态: 未开始
- 完成情况: 已完成需求编号与执行包映射。
- 检查表:
  - [x] 已建立需求编号
  - [x] 已建立执行单元包映射
  - [x] 已建立门禁记录初版
- 跟踪表状态: 待实现
- 结论记录: 进入编码阶段按 PKG 顺序推进，优先 route engine 与 dataplane。

## 字符集编码基线
- 字符集类型: UTF-8
- BOM 策略: 无 BOM
- 换行符规则: LF
- 跨平台兼容要求: 新增与改造文件统一按该基线落盘。
- 历史文件迁移策略: 仅触达文件按基线对齐。

## 需求跟踪表
| 需求编号 | 需求描述 | 执行单元包 | 编码状态 | 测试状态 | 当前责任角色 | 风险与遗留 | 最新更新时间 |
|---|---|---|---|---|---|---|---|
| RQ-PN-FULLPROXY-001 | Windows 启用 TUN 后流量进入本地 TUN 数据面 | PKG-PN-FULLPROXY-02 | 待实现 | 待测试 | 编码工程师 | session 生命周期需可回滚 | 2026-04-25 |
| RQ-PN-FULLPROXY-002 | 数据面支持 TCP UDP 按组分流 | PKG-PN-FULLPROXY-03 | 待实现 | 待测试 | 编码工程师 | UDP 关联与回收需稳定 | 2026-04-25 |
| RQ-PN-FULLPROXY-003 | 规则命中统一使用 `rules` 字段 | PKG-PN-FULLPROXY-01 | 待实现 | 待测试 | 编码工程师 | 需保留旧数据兼容读 | 2026-04-25 |
| RQ-PN-FULLPROXY-004 | `action=tunnel` 通过 `proxy_chain` 通讯 | PKG-PN-FULLPROXY-01 PKG-PN-FULLPROXY-03 | 待实现 | 待测试 | 编码工程师 | 组到链路映射失败处理 | 2026-04-25 |
| RQ-PN-FULLPROXY-005 | `action=direct` 走直连并支持绕行出口 | PKG-PN-FULLPROXY-04 | 待实现 | 待测试 | 编码工程师 | 路由清理异常风险 | 2026-04-25 |
| RQ-PN-FULLPROXY-006 | `action=reject` 在 DNS 与连接面一致拒绝 | PKG-PN-FULLPROXY-01 PKG-PN-FULLPROXY-03 | 待实现 | 待测试 | 编码工程师 | 需避免半拒绝态 | 2026-04-25 |
| RQ-PN-FULLPROXY-007 | fake ip route hint 与连接决策一致 | PKG-PN-FULLPROXY-05 | 待实现 | 待测试 | 编码工程师 | fake ip 过期清理一致性 | 2026-04-25 |
| RQ-PN-FULLPROXY-008 | 状态接口输出数据面统计与错误 | PKG-PN-FULLPROXY-06 | 待实现 | 待测试 | 编码工程师 | 统计字段口径需稳定 | 2026-04-25 |
| RQ-PN-FULLPROXY-009 | 失败支持 direct 回退与清理 | PKG-PN-FULLPROXY-04 PKG-PN-FULLPROXY-06 | 待实现 | 待测试 | 编码工程师 | 异常路径门禁重点 | 2026-04-25 |
| RQ-PN-FULLPROXY-010 | Linux 不回归 | PKG-PN-FULLPROXY-07 | 待实现 | 待测试 | 测试工程师 | 覆盖回归矩阵 | 2026-04-25 |

## 执行单元包
- PKG-PN-FULLPROXY-01: 路由决策引擎
- PKG-PN-FULLPROXY-02: Wintun 适配器与 session 数据面
- PKG-PN-FULLPROXY-03: TUN netstack TCP UDP 分流
- PKG-PN-FULLPROXY-04: Windows direct bypass 路由维护
- PKG-PN-FULLPROXY-05: fake ip 与 route hint 连接桥接
- PKG-PN-FULLPROXY-06: 控制台状态与可观测
- PKG-PN-FULLPROXY-07: 回归测试与 Linux 不回归验证

## 门禁记录
- G1 需求门: 通过
- G2 架构门: 通过
- G3 编码核查门: 待执行
- G4 测试核查门: 待执行
- G5 复盘门: 待执行
