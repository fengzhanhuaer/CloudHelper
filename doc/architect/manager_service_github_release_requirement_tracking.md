# 需求跟踪表增量 `manager_service` GitHub 发布与安装源

- 日期: 2026-04-13
- 备注: 本增量跟踪用于覆盖 CI 发布与安装脚本下载源头变更。
- 风险: release 资产缺失或命名偏差会导致安装失败。
- 遗留事项: 需补充 GitHub API 失败重试与离线兜底策略。
- 进度状态: 进行中
- 完成情况: 已完成架构与执行包定义，待编码实现。
- 检查表: 已建立
- 跟踪表状态: 待实现
- 结论记录: 安装源切换为 GitHub Release，支持 latest 默认与 version 覆盖。

| 需求编号 | 需求描述 | 执行单元包 | 编码状态 | 测试状态 | 当前责任角色 | 风险与遗留 | 最新更新时间 |
|---|---|---|---|---|---|---|---|
| RQ-013 | GitHub Action 自动编译发布 manager_service | PKG-CI-20 | 待实现 | 待测试 | 架构师 | workflow 需保证 tag 资产完整 | 2026-04-13 |
| RQ-014 | 安装脚本默认从 latest release 下载 | PKG-OPS-20 | 待实现 | 待测试 | 架构师 | API 访问失败处理 | 2026-04-13 |
| RQ-015 | 安装脚本支持 `-Version` 覆盖指定 tag | PKG-OPS-20 | 待实现 | 待测试 | 架构师 | tag 格式兼容 | 2026-04-13 |
| RQ-016 | 升级脚本支持 latest/version 下载并升级 | PKG-OPS-21 | 待实现 | 待测试 | 架构师 | 回滚可靠性 | 2026-04-13 |
| RQ-017 | 资产名固定 `cloudhelper-manager-service-windows-amd64.exe` | PKG-CI-20 PKG-DOC-20 | 待实现 | 待测试 | 架构师 | 命名漂移治理 | 2026-04-13 |
