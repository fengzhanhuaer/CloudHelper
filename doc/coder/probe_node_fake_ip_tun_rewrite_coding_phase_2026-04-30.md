# Code阶段文档 probe_node fake IP 出入站改写与 TCP/UDP 对齐

## 工作依据与规则传递声明
- 当前角色: Code 编码者
- 工作依据文档:
  - `doc/ai-coding-unified-rules.md`
  - `doc/architect/manager_node_fake_ip_dns_remote_compare_architect_2026-04-30.md`
- 适用规则:
  - S3 编码与自测
  - 8.7 Code实现与证据口径
  - 6.2 放行与阻塞规则

## 日期
- 2026-04-30

## 备注
- 本次仅落实“DNS 解析策略不变”的前提下，补齐 fake IP 出站改写能力。
- 改动范围限定在 `probe_node`，未改 `probe_manager`。

## 风险
- 当前仅完成 U1（出站改写）；U2/U3 仍待后续实现。
- fake 命中后改写为域名再决策，依赖本地 fake 映射有效期，极端情况下可能受 TTL 过期影响。

## 遗留事项
- U2：fake IP TTL 与缓存回填跨端对齐未实施。
- U3：UDP association 元数据与调试口径统一未实施。
- Debug 阶段 G4 证据未产出。

## 进度状态
- 已完成（本次编码范围）

## 完成情况
- 已完成 fake IP 目标改写入口实现并接入路由决策。
- 已补齐 TCP/UDP 测试用例并通过全量 `probe_node` 单测。
- 已同步架构文档状态。

## 检查表
- [x] 已声明工作依据与规则传递
- [x] 已包含日期
- [x] 已包含备注
- [x] 已包含风险
- [x] 已包含遗留事项
- [x] 已包含进度状态
- [x] 已包含完成情况
- [x] 已包含检查表
- [x] 已包含跟踪表状态
- [x] 已包含结论记录

## 跟踪表状态
- 当前状态: 待测试（跨端联调）
- 当前责任角色: Debug
- 最近更新时间: 2026-04-30

## 结论记录
1. 在 TUN 模式下，命中 fake IP 的目标地址会先改写为 `domain:port`，再沿用既有 direct/tunnel/reject 分流。
2. DNS 解析策略、fake IP 启用判定（`UseTunnelDNS` + 白名单）保持不变。
3. TCP 与 UDP 路径均覆盖 fake IP 改写后的分流验证。

## 执行单元包与需求映射
- NA-FAKEIP-ALIGN-001 / U1
  - 目标: node 补齐 fake-IP 出站改写
  - 结果: 已实现并通过单测
- NA-FAKEIP-ALIGN-002 / U2
  - 目标: fake IP TTL 与缓存回填对齐
  - 结果: 未实施（保持待实现）
- NA-UDP-ASSOC-ALIGN-003 / U3
  - 目标: UDP association 元数据与调试口径统一
  - 结果: 未实施（保持待实现）

## 变更点清单
- `probe_node/local_tun_route.go`
  - 调整 `decideProbeLocalRouteForTarget`，在 TUN 模式接入 fake IP 改写分支。
  - 新增 `rewriteProbeLocalRouteTargetForFakeIP` 统一改写入口。
- `probe_node/local_tun_route_test.go`
  - 增加 fake IP 改写后 `TargetAddr` 断言。
  - 新增 direct 场景 fake IP 改写测试。
- `probe_node/local_tun_stack_windows_test.go`
  - 新增 UDP fake IP 隧道路径测试。
- `doc/architect/manager_node_fake_ip_dns_remote_compare_architect_2026-04-30.md`
  - 同步实现状态（U1 已实现，U2/U3 待实现）。

## 8.7证据索引
- 命令: `go test -run "TestDecideProbeLocalRouteForTarget|TestProbeLocalTUNSimplePacketStackWrite" ./...`
  - 结果: 通过
- 命令: `go test ./...`
  - 结果: 通过
- 证据补充:
  - 本次未触发编码豁免参数（`--allow-empty` / `--no-check-bom`）
  - 本次未涉及 safe patch 回滚

## 自测结果与G3结论
- 自测范围: `probe_node` 包全部测试
- 自测结论: 全部通过
- G3结论: 通过（就本次编码范围）

## 待测试移交项
- 需要 Debug 阶段执行跨端回归：
  - fake IP + tunnel TCP
  - fake IP + tunnel UDP
  - fake IP + direct TCP/UDP
  - reject 规则与白名单绕过
  - 与 manager 端行为一致性对比

## 映射关系与跟踪表更新说明
| 需求编号 | 需求描述 | 执行单元包 | 编码状态 | 测试状态 | 当前责任角色 | 风险与遗留 | 最新更新时间 |
|---|---|---|---|---|---|---|---|
| NA-FAKEIP-ALIGN-001 | node 补齐 fake-IP 出站改写 | U1 | 已实现 | 已测试（单测） | Debug | 需补跨端联调 | 2026-04-30 |
| NA-FAKEIP-ALIGN-002 | manager node fake IP TTL 与缓存回填对齐 | U2 | 待实现 | 待测试 | Code | 跨模块改动范围大 | 2026-04-30 |
| NA-UDP-ASSOC-ALIGN-003 | UDP association 元数据与调试口径统一 | U3 | 待实现 | 待测试 | Code | 需联动调试视图 | 2026-04-30 |
