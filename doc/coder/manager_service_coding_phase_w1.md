# 编码阶段文档 — manager_service W1 后端骨架与鉴权

- 日期: 2026-04-12
- 备注: 按架构师阶段文档 `manager_service_final_architect_doc.md` 执行，W1 四个执行单元包逐项落地。
- 风险:
  - bcrypt 首次初始化若数据目录不可写将阻断登录。
  - 内存会话 Token 在进程重启后失效，前端需重新登录。
  - gin 路由中间件注册顺序错误可能导致鉴权短路。
- 遗留事项:
  - 后端 API 字段字典与错误码字典待 G3 门禁前冻结。
  - 本机重置入口（reset-local）安全约束需在测试阶段验证。
- 进度状态: 已完成
- 完成情况: PKG-W1-01 ~ PKG-W1-04 全部完成
- 检查表:
  - [x] PKG-W1-01 项目结构与启动入口
  - [x] PKG-W1-02 监听配置 127.0.0.1:16033
  - [x] PKG-W1-03 单账户登录、会话、鉴权中间件
  - [x] PKG-W1-04 用户名密码修改与本机重置入口
- 跟踪表状态: 已完成
- 结论记录: W1 四个执行单元包全部实现，自测 12/12 PASS，提请架构师执行 G3 门禁核查。

## 执行单元包编号与需求编号映射

| 执行单元包 | 需求编号 | 状态 |
|---|---|---|
| PKG-W1-01 | RQ-001 | 已完成 |
| PKG-W1-02 | RQ-002 | 已完成 |
| PKG-W1-03 | RQ-005 | 已完成 |
| PKG-W1-04 | RQ-006 | 已完成 |

## 变更点清单

- `manager_service/main.go` — 服务入口，优雅启停，监听 127.0.0.1:16033
- `internal/config/config.go` — 配置加载，硬编码 MandatoryListenAddr，支持数据目录自动初始化
- `internal/logging/logging.go` — 轮转日志，写入 log/manager_service.log + stderr
- `internal/auth/auth.go` — bcrypt 单账户、内存会话 Token、改密改名、本机重置
- `internal/api/response/response.go` — 统一响应信封 {code, message, data, request_id}
- `internal/api/middleware/middleware.go` — RequestID注入、访问日志、Token鉴权、LocalhostOnly
- `internal/api/handler/auth_handler.go` — login / logout / change-password / reset-local
- `internal/api/handler/system_handler.go` — /healthz / /api/system/version
- `internal/api/router.go` — 路由装配，公开/鉴权/本机限定分组正确
- `go.mod` — 仅依赖 golang.org/x/crypto（bcrypt）

## 自测结果

- `go build ./...` ✅ 零错误
- `go vet ./...` ✅ 零警告
- `go test ./internal/auth/... -v` ✅ **12/12 PASS** (10.53s)
  - TestLoginSuccess ✅
  - TestLoginWrongPassword ✅
  - TestLoginWrongUsername ✅
  - TestValidateToken ✅
  - TestValidateInvalidToken ✅
  - TestLogout ✅
  - TestChangeCredentials ✅
  - TestChangeCredentialsWrongOldPassword ✅
  - TestChangeCredentialsInvalidatesSession ✅
  - TestChangeUsername ✅
  - TestResetLocal ✅
  - TestPersistence ✅

## 待测试移交项

- TST-W1-01 独立项目启动验证（集成测试阶段手动执行）
- TST-W1-02 本机监听验证（netstat 确认仅 127.0.0.1:16033）
- TST-AUTH-01 登录矩阵（自测已覆盖，集成阶段 HTTP curl 验证）
- TST-AUTH-02 改密改名后重登（自测已覆盖，集成阶段验证）
