# 需求跟踪表 `probe_node` 本地控制台代理组与链路缓存升级

## 工作依据与规则传递声明
- 当前角色: 架构师
- 工作依据文档: `doc/ai-coding-unified-rules.md`
- 适用规则: AI协作统一规则 单一规范
- 规则遵循声明: 必须遵守本规则。
- 协作传递要求: 后续接手者与协作者必须遵守同一规则，不得降级或替换执行口径。

- 日期: 2026-04-24
- 备注: 本跟踪表对应 `probe_node` 本地控制台升级，覆盖会话时效、双 Tab 重构、`proxy_chain.json`、`proxy_group.json` 静态规则与 `proxy_state.json` 动态状态拆分，以及手动备份上传；`fallback` 为内置组且无需显式配置；`proxy_group` 规则采用 `rules_text` 行式文本 每行一条规则 不使用数组；新增 `proxy_host.txt` 保存静态路由 `dns,ip` 对。
- 风险:
  - 联调依赖风险: 主控备份上传接口若在联调期发生协议变更，可能导致接口对接返工。
  - 运行环境风险: 不同平台网络权限策略差异，可能影响 TUN 路由执行稳定性。
- 技术实现项:
  - `proxy_chain` 过滤、TUN 自动切换、`proxy_group` 与 `proxy_state` 拆分、备份鉴权与失败反馈均已定义为实现项，按执行单元包落地。
  - 与 `probe_manager` 当前实现差异已登记: `action` 与 `tunnel_node_id` 在本期 `probe_node` 方案中仅写入 `proxy_state.json` 运行态，不写入 `proxy_group.json`。
- 遗留事项:
  - 本期不引入历史版本与回滚设计，先聚焦当前需求落地。
  - `proxy_chain.json` 数据来源签名校验不在本期范围。
- 进度状态: 进行中
- 完成情况: 已完成需求拆分、架构方案与执行包映射，待进入编码与测试阶段。
- 检查表:
  - [x] 已建立需求编号
  - [x] 已完成执行单元包映射
  - [x] 已确认字符集编码基线
  - [ ] 待编码实现
  - [ ] 待测试回归
- 跟踪表状态: 待实现
- 结论记录: 采用本地控制台双 Tab 与规则配置方案，链路缓存仅保留 `proxy_chain`；文件收敛为 `proxy_group.json` `proxy_state.json` 与 `proxy_host.txt`，其中 `proxy_group` 仅承载静态分组匹配规则，`action` 与 `tunnel_node_id` 仅承载于 `proxy_state` 运行态，`fallback` 属于内置组无需显式配置，缺失文件时自动生成默认配置文件，`proxy_host.txt` 采用每行 `dns,ip` 且支持空行与 `#` 注释，备份上传仅手动触发且只上传 `proxy_group.json`。

## 字符集编码基线
- 字符集类型: UTF-8
- BOM策略: 启用 BOM
- 换行符规则: CRLF
- 跨平台兼容要求: 本次新增与改造文件按该基线执行。
- 历史文件迁移策略: 不做全量迁移，按改动范围落地。

## 需求跟踪表
| 需求编号 | 需求描述 | 执行单元包 | 编码状态 | 测试状态 | 当前责任角色 | 风险与遗留 | 最新更新时间 |
|---|---|---|---|---|---|---|---|
| RQ-PN-UPG-001 | 本地控制台会话过期时间改为30天 | PKG-PN-UPG-01 | 待实现 | 待测试 | 编码工程师 | 需校验 cookie 过期策略与会话清理一致性 | 2026-04-24 |
| RQ-PN-UPG-002 | 删除当前会话面板 | PKG-PN-UPG-02 | 待实现 | 待测试 | 编码工程师 | 需避免影响现有登录态校验逻辑 | 2026-04-24 |
| RQ-PN-UPG-003 | TUN状态与代理状态改为独立Tab并调换位置 | PKG-PN-UPG-02 | 待实现 | 待测试 | 编码工程师 | 需保证 Tab 切换与状态刷新互不干扰 | 2026-04-24 |
| RQ-PN-UPG-004 | 增加 `proxy_chain.json` 缓存，仅缓存 `proxy_chain` | PKG-PN-UPG-03 | 待实现 | 待测试 | 编码工程师 | 需严格过滤 `chain_type=proxy_chain`，排除 `port_forward` | 2026-04-24 |
| RQ-PN-UPG-005 | 参照 manager，启动 TUN 后进入代理模式 | PKG-PN-UPG-05 | 待实现 | 待测试 | 编码工程师 | 需处理自动切换失败回滚与错误反馈 | 2026-04-24 |
| RQ-PN-UPG-006 | 增加 JSON 模式 `proxy_group` 并拆分 `proxy_state`，按本期口径将动作与通道写入运行态 | PKG-PN-UPG-04 | 待实现 | 待测试 | 编码工程师 | `proxy_group` 仅维护静态分组匹配规则，规则字段使用 `rules_text` 行式文本 每行一条规则 不使用数组；`action` 与 `tunnel_node_id` 仅持久化到 `proxy_state` 运行态，`fallback` 为内置组无需显式配置，缺失文件自动生成默认配置 | 2026-04-24 |
| RQ-PN-UPG-007 | `proxy_group` 手动点击备份才上传，不自动上传 | PKG-PN-UPG-06 | 待实现 | 待测试 | 编码工程师 | 需明确手动入口、鉴权与失败提示，且上传范围仅 `proxy_group.json` | 2026-04-24 |
| RQ-PN-UPG-008 | 增加 `proxy_host.txt` 静态路由主机映射 | PKG-PN-UPG-07 | 待实现 | 待测试 | 编码工程师 | 文件名固定 `proxy_host.txt`，每行 `dns,ip`，支持空行与 `#` 注释，缺失时自动生成默认文件 | 2026-04-24 |

## 门禁记录
- G1 需求门: 通过
- G2 架构门: 通过
- G3 编码核查门: 待执行
- G4 测试核查门: 待执行
- G5 复盘门: 待执行

## 处置动作
- 已完成架构阶段产物并按最新口径收敛为 `proxy_group.json` 与 `proxy_state.json` 双文件，待你确认后切换编码模式实施。
- 编码完成后需同步回写本表编码状态与测试状态。
