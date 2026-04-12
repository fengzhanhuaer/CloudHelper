# 编码阶段文档 — manager_service W2 主控探针交互迁移

- 日期: 2026-04-12
- 备注: 按架构师阶段文档执行，W2 四个执行单元包逐项落地，行为等价迁移原则严格遵守。
- 风险:
  - 链路探测 HTTP/3 依赖 quic-go，与 probe_manager 版本需保持一致（当前 v0.55.0）。
  - 网络助手 (W2-03) 采用代理转发模式，依赖 probe_manager 运行状态，不做直接迁移。
  - 升级执行（二进制替换）未在 manager_service 实现，保留在 probe_manager 内（RQ-008）。
- 遗留事项:
  - 网络助手 W2-03 代理客户端需在 W3 阶段与真实 probe_manager API 联调验证。
  - 升级二进制执行路径待 W3 前端接入后确认触发方式。
- 进度状态: 已完成
- 完成情况: PKG-W2-01 ~ PKG-W2-04 全部完成
- 检查表:
  - [x] PKG-W2-01 主控鉴权登录代理能力迁移
  - [x] PKG-W2-02 节点管理与链路探测能力迁移
  - [x] PKG-W2-03 网络助手核心接口迁移
  - [x] PKG-W2-04 升级与日志相关能力迁移
- 跟踪表状态: 已完成
- 结论记录: W2 四个执行单元包全部实现，自测 21/21 PASS，提请架构师执行 G3 后端门禁核查。

## 执行单元包编号与需求编号映射

| 执行单元包 | 需求编号 | 状态 |
|---|---|---|
| PKG-W2-01 | RQ-004 | 已完成 |
| PKG-W2-02 | RQ-004 | 已完成 |
| PKG-W2-03 | RQ-004 | 已完成 |
| PKG-W2-04 | RQ-004 | 已完成 |

## 变更点清单

- `internal/adapter/controller/session.go` — 主控三步登录协议（nonce→sign→token），Session 管理
- `internal/adapter/node/node_store.go` — 节点 CRUD 持久化存储，行为等价于 probe_manager
- `internal/adapter/node/probelink.go` — HTTP/HTTPS/HTTP3 链路探测 + 遗留 service/public 路径
- `internal/adapter/node/node_store_test.go` — 9 项节点存储单元测试
- `internal/adapter/netassist/client.go` — 网络助手代理客户端（status/mode 转发）
- `internal/adapter/upgrade/upgrade.go` — GitHub release 查询与 asset 匹配
- `internal/adapter/logview/logview.go` — 日志尾读与过滤，行为等价于 probe_manager
- `internal/api/handler/node_handler.go` — 节点 CRUD + 链路探测 HTTP Handler
- `internal/api/handler/upgrade_handler.go` — 升级查询 + 日志查询 HTTP Handler
- `internal/api/router.go` — 扩展 W2 路由注册（RouterOptions 重构）
- `main.go` — 注入 node.Store 和 logDir，使用 RouterOptions
- `go.mod` — 增加 quic-go v0.55.0 依赖

## 自测结果

- `go build ./...` ✅ 零错误
- `go vet ./...` ✅ 零警告
- `go test ./internal/auth/... -v` ✅ **12/12 PASS** (W1 回归)
- `go test ./internal/adapter/node/... -v` ✅ **9/9 PASS** (1.345s)
  - TestListEmpty ✅
  - TestCreateAndList ✅
  - TestCreateDuplicateName ✅
  - TestCreateIncrementNodeNo ✅
  - TestUpdate ✅
  - TestUpdateNotFound ✅
  - TestUpdateInvalidSystem ✅
  - TestReplace ✅
  - TestPersistence ✅

## 待测试移交项

- TST-W2-01 主控探针交互等价回归（集成测试阶段，需联调 probe_controller）
- TST-W2-02 节点 API 端对端测试（curl / Postman 验证 CRUD 接口）
- TST-W2-03 链路探测接口测试（HTTP/HTTPS mock 探针端点验证）
- TST-W2-04 升级查询 API 测试（GitHub release 真实响应验证）
- TST-W2-05 日志查询 API 测试（运行期日志文件读取验证）
