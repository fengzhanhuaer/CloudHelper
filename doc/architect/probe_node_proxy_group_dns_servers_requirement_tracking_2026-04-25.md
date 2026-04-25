# 需求跟踪表 `probe_node` proxy_group 顶层 DNS 服务器配置扩展

## 工作依据与规则传递声明
- 当前角色: 架构师
- 工作依据文档: `doc/ai-coding-unified-rules.md`
- 适用规则: AI协作统一规则 单一规范
- 规则遵循声明: 必须遵守本规则。
- 协作传递要求: 后续接手者与协作者必须遵守同一规则，不得降级或替换执行口径。

- 日期: 2026-04-25
- 备注: 本跟踪表对应 `proxy_group.json` 顶层新增全局 DNS 服务器配置字段，并增加 `doh_proxy_servers` 用于代理组 DNS 解析。
- 风险:
  - 旧版配置兼容处理若缺失默认填充，可能影响持久化一致性。
  - URL 与地址格式校验边界若不统一，可能造成前后端行为差异。
- 遗留事项:
  - 本期不实现 manager 级 DNS 数据面，仅交付配置模型扩展。
- 进度状态: 进行中
- 完成情况: 已完成需求拆分与架构映射，待编码与测试。
- 检查表:
  - [x] 已建立需求编号
  - [x] 已完成执行单元包映射
  - [x] 已确认字符集编码基线 UTF-8 无 BOM LF
  - [ ] 待编码实现
  - [ ] 待测试回归
- 跟踪表状态: 待实现
- 结论记录: 采用顶层新增 `dns_servers` `dot_servers` `doh_servers` `doh_proxy_servers` 方案，保持 `groups` 结构不变，其中 `doh_proxy_servers` 供代理组 DNS 解析。

## 字符集编码基线
- 字符集类型: UTF-8
- BOM 策略: 无 BOM
- 换行符规则: LF
- 跨平台兼容要求: 本次新增与改造文件统一按该基线执行。
- 历史文件迁移策略: 不做全量迁移，按本次改动范围执行。

## 需求跟踪表
| 需求编号 | 需求描述 | 执行单元包 | 编码状态 | 测试状态 | 当前责任角色 | 风险与遗留 | 最新更新时间 |
|---|---|---|---|---|---|---|---|
| RQ-PN-DNSCFG-001 | `proxy_group.json` 顶层新增 `dns_servers` | PKG-PN-DNSCFG-01 | 待实现 | 待测试 | 编码工程师 | 顶层字段新增需保证 JSON 兼容读取 | 2026-04-25 |
| RQ-PN-DNSCFG-002 | `proxy_group.json` 顶层新增 `dot_servers` | PKG-PN-DNSCFG-01 | 待实现 | 待测试 | 编码工程师 | 地址格式校验需兼容 IPv4 IPv6 域名 | 2026-04-25 |
| RQ-PN-DNSCFG-003 | `proxy_group.json` 顶层新增 `doh_servers` | PKG-PN-DNSCFG-01 | 待实现 | 待测试 | 编码工程师 | URL 校验需明确允许范围 | 2026-04-25 |
| RQ-PN-DNSCFG-004 | `proxy_group.json` 顶层新增 `doh_proxy_servers` 供代理组 DNS 解析 | PKG-PN-DNSCFG-01 PKG-PN-DNSCFG-02 | 待实现 | 待测试 | 编码工程师 | 需明确与 `doh_servers` 的职责边界并校验 URL | 2026-04-25 |
| RQ-PN-DNSCFG-005 | 四字段属于全局配置且 `groups` 结构保持不变 | PKG-PN-DNSCFG-01 | 待实现 | 待测试 | 编码工程师 | 防止误改每组结构导致兼容破坏 | 2026-04-25 |
| RQ-PN-DNSCFG-006 | 读写接口兼容旧文件 缺失字段按空数组处理 | PKG-PN-DNSCFG-03 | 待实现 | 待测试 | 编码工程师 | 需验证旧文件自动补齐与回写一致性 | 2026-04-25 |
| RQ-PN-DNSCFG-007 | 保存接口维持严格 JSON 校验并输出明确错误 | PKG-PN-DNSCFG-02 PKG-PN-DNSCFG-04 | 待实现 | 待测试 | 编码工程师 | `DisallowUnknownFields` 行为需保持 | 2026-04-25 |
| RQ-PN-DNSCFG-008 | 更新示例文档与回归测试 | PKG-PN-DNSCFG-05 | 待实现 | 待测试 | 编码工程师 | 文档与实现契约一致性 | 2026-04-25 |

## 执行单元包
- PKG-PN-DNSCFG-01: 扩展 `proxy_group` 顶层结构字段
- PKG-PN-DNSCFG-02: 实现 DNS 字段校验与规范化
- PKG-PN-DNSCFG-03: 默认文件与旧版兼容迁移逻辑
- PKG-PN-DNSCFG-04: API 错误语义与行为回归
- PKG-PN-DNSCFG-05: 文档示例与测试更新

## 门禁记录
- G1 需求门: 通过
- G2 架构门: 通过
- G3 编码核查门: 待执行
- G4 测试核查门: 待执行
- G5 复盘门: 待执行

## 处置动作
- 待切换编码模式按执行单元包实施并回写编码状态。
- 编码完成后进入测试模式回写测试状态并更新门禁记录。
