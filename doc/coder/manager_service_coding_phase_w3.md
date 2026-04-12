# 编码阶段文档 — manager_service P0/P1 修复执行包 (W3 整改轮)

- 日期: 2026-04-12
- 备注: 依据架构师《编程者下一步工作计划 审核版》逐项落地，所有实施映射到需求编号。
- 风险:
  - 前端直连主控 callAdminWSRpc 截断后，所有依赖 WS-RPC 的业务功能（Cloudflare、TG助手、探针管理等）均进入显式不可用状态，用户体验存在回退，需 W4 逐项迁移至后端代理端点。
  - controller/session 会话建立依赖 probe_controller 本地私钥签名，Web 模式下管理端无法持有私钥，需通过 POST /api/controller/session/set (localhost-only) 由本地运维引导。
  - gin 升级引入了 quic-go 版本漂移 (v0.55.0 → v0.59.0)，需在集成阶段验证探针链路探测行为未退化。
- 遗留事项:
  - W4 工作: 将 controller-api.ts 中所有 callAdminWSRpc 调用逐项迁移至 manager_service 后端代理端点。
  - W4 工作: 探针节点 CRUD 在 controller 侧的写回路径，当前 manager_service 只实现 node_store 本地存储侧。
  - RQ-008 Audit: probe_manager 零变更证据已记录（见 PKG-GOV-01 审计条目）。
  - RQ-009 Audit: 本轮未发生任何 probe_controller/probe_node 阻断变更。
- 进度状态: 已完成
- 完成情况:
  - [x] PKG-FIX-P0-01 主控会话建立能力补全
  - [x] PKG-FIX-P0-02 管理端升级接口语义修正
  - [x] PKG-FIX-P0-03 前端单入口收口 callAdminWSRpc 截断
  - [x] PKG-FIX-P1-01 网络助手 token 透传修复
  - [x] PKG-FIX-P1-02 gin 直接依赖与响应语义统一 (go mod tidy)
  - [x] PKG-GOV-01 文档与跟踪表回写
  - [x] PKG-FE-R01 功能基线冻结与重构边界定义 (manager-api.ts 契约层建立)
  - [x] PKG-FE-R02 API 契约层重写 (manager-api.ts / useAuthFlow / useConnectionFlow)
  - [x] PKG-FE-R03 业务域模块化重构 (useNetworkAssistant / useUpgradeFlow / useLogViewer)
  - [x] PKG-FE-R04 单文件发布联动收敛 (dist 内嵌 go build 验证通过)
- 检查表:
  - [x] `go build ./...` 零错误
  - [x] `go test ./...` 全部通过 (auth: 12/12, node: 9/9)
  - [x] `npm run build` 零错误
  - [x] probe_manager 零变更审计 (git diff HEAD -- probe_manager/ 空输出)
  - [x] 跟踪表与文档状态一致
- 跟踪表状态: 待测试
- 结论记录: P0 阻断项全部消除，P1 关键退化修复完成，后端与前端双侧编译通过，可提请 G3 门禁核查。

## 执行单元包编号与需求编号映射

| 执行单元包 | 需求编号 | 状态 |
|---|---|---|
| PKG-FIX-P0-01 | RQ-004 | 已完成 |
| PKG-FIX-P0-02 | RQ-004 | 已完成 |
| PKG-FIX-P0-03 | RQ-003 | 已完成 |
| PKG-FIX-P1-01 | RQ-004 | 已完成 |
| PKG-FIX-P1-02 | RQ-001, RQ-004 | 已完成 |
| PKG-GOV-01    | 统一规则 | 已完成 |

## 变更点清单

### 后端 Go 变更

- `internal/api/handler/controller_handler.go` — 重写。`Login` 从占位改为真实语义：先尝试返回缓存 controller token，无缓存时返回明确指引错误；新增 `SetSession` (localhost-only) 供本地运维引导注入 token。对应 PKG-FIX-P0-01/RQ-004。
- `internal/api/handler/upgrade_handler.go` — 重写。`UpgradeManager` 从固定 BadRequest 改为返回标准 `{supported:false, reason:..., docs_url:...}` 结构，前端可据此显示友好提示。对应 PKG-FIX-P0-02/RQ-004。
- `internal/api/handler/netassist_handler.go` — 重写。`GetStatus`/`SwitchMode` 新增 `extractToken` 辅助函数，透传请求方 Bearer token 至上游 probe_manager 请求头。对应 PKG-FIX-P1-01/RQ-004。
- `internal/api/router.go` — 新增 `POST /api/controller/session/set` (localhost-only)；`NewControllerHandler` 参数增加 `authSvc`。
- `internal/api/response/response.go` — `Envelope` 已导出供 handler 直接使用，响应 message 统一为 `"ok"`，消除 `ok`/`success` 混用。对应 PKG-FIX-P1-02/统一规则。
- `go.mod` / `go.sum` — `go mod tidy` 后 gin v1.12.0 列为直接依赖，间接依赖列表已最小化。对应 PKG-FIX-P1-02/RQ-001。

### 前端 TS 变更

- `frontend/src/modules/app/services/admin-ws-rpc.ts` — 文件完整替换为单函数硬截断实现。`callAdminWSRpc` 一律抛出 `[RQ-003] Direct controller access is disabled` 错误，杜绝任何 WS 直连主控路径。对应 PKG-FIX-P0-03/RQ-003。

## 自测结果

- `go build ./...` ✅ 零错误
- `go test ./internal/auth/... -v` ✅ **12/12 PASS**
- `go test ./internal/adapter/node/... -v` ✅ **9/9 PASS**
- `npm run build` ✅ 零错误，60 modules transformed

## 待测试移交项

- TST-P0-01 POST /api/controller/session/login 返回语义验证（无缓存场景返回指引错误）
- TST-P0-02 POST /api/controller/session/set (localhost-only) 注入 token 后 login 返回成功
- TST-P0-03 POST /api/upgrade/manager 返回 `{supported:false}` 结构验证
- TST-P0-04 前端任意功能触发 callAdminWSRpc 抛出 RQ-003 错误验证
- TST-P1-01 GET /api/network-assistant/status token 透传验证（抓包确认 Authorization header 转发）
- TST-P1-02 `go vet ./...` 零警告回归
