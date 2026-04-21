# 编码阶段文档 — `probe_controller` `/mng` Cloudflare 磁贴与管理页面实现

## 工作依据与规则传递声明
- 当前角色: 编码者
- 工作依据文档: `doc/ai-coding-unified-rules.md`
- 适用规则: AI协作统一规则 单一规范
- 规则遵循声明: 必须遵守本规则。
- 协作传递要求: 后续接手者与协作者必须遵守同一规则。

- 日期: 2026-04-21
- 备注: 按用户确认口径实现“系统设置同构”方案，即 Cloudflare 在 `/mng` 下走 HTTP + Cookie + `mng` 鉴权，不直接走 admin websocket。
- 风险:
  - Cloudflare 外部网络相关错误在运行期可能出现波动，当前仅实现透传与分层状态码映射。
  - 当前自动化测试已覆盖路由鉴权、页面关键文案与 Cloudflare 关键错误分支；外部网络波动相关场景仍建议在线上联调回归。
- 遗留事项:
  - 无（本轮计划内编码与测试项已完成）
- 进度状态: 已完成
- 完成情况: 已完成 Cloudflare 磁贴、独立页面、`/mng/api/cloudflare/*` 薄封装接口与错误分支测试，`go test ./...` 通过。
- 检查表:
  - [x] 工作依据与规则传递声明完整
  - [x] Cloudflare 页面与 embed 已落盘
  - [x] `/mng/cloudflare` 页面路由与 `/mng/api/cloudflare/*` 接口已接入
  - [x] 走 HTTP + Cookie + `mngAuthRequiredMiddleware` 模式
  - [x] `gofmt` 与 `go test ./...` 通过
- 跟踪表状态: 已完成
- 结论记录: 本次实现满足“在 `/mng` 新增 Cloudflare 磁贴并进入新页面，页面含基础设置/DDNS/ZeroTrust 子Tab，接口沿用系统设置同构模式”的要求。

## 执行单元包编号与需求编号映射

| 执行单元包 | 需求编号 | 状态 |
|---|---|---|
| PKG-MNG-CF-01 | RQ-MNG-013 | 已完成 |
| PKG-MNG-CF-01 PKG-MNG-CF-02 | RQ-MNG-014 | 已完成 |
| PKG-MNG-CF-04 | RQ-MNG-015 | 已完成 |
| PKG-MNG-CF-03 | RQ-MNG-016 | 已完成 |
| PKG-MNG-CF-02 PKG-MNG-CF-05 | RQ-MNG-017 | 已完成 |

## 变更点清单

### 页面与资源
- `probe_controller/internal/core/mng_pages/cloudflare.html`（新增）
  - 新增 `/mng/cloudflare` 页面。
  - 新增三个子Tab：基础设置、DDNS、ZeroTrust。
  - 页面交互全部使用 `fetch('/mng/api/cloudflare/*', { credentials: 'same-origin' })`。
  - 登录态检查复用 `/mng/api/session`，失败跳转 `/mng`。

- `probe_controller/internal/core/mng_pages.go`
  - 新增 `//go:embed mng_pages/cloudflare.html`，注册 `mngCloudflarePageHTML`。

- `probe_controller/internal/core/mng_pages/panel.html`
  - 新增 Cloudflare 磁贴：跳转 `/mng/cloudflare`。
  - 更新面板描述文案，纳入 Cloudflare 管理入口说明。

### 路由与接口
- `probe_controller/internal/core/mng_cloudflare_handlers.go`（新增）
  - 新增页面 handler：`mngCloudflarePageHandler`。
  - 新增 Cloudflare 薄封装接口 handler：
    - `GET/POST /mng/api/cloudflare/api`
    - `GET/POST /mng/api/cloudflare/zone`
    - `GET /mng/api/cloudflare/ddns/records`
    - `POST /mng/api/cloudflare/ddns/apply`
    - `GET/POST /mng/api/cloudflare/zerotrust/whitelist`
    - `POST /mng/api/cloudflare/zerotrust/whitelist/run`
  - 内部复用既有 `cloudflare_assistant.go` 逻辑函数，不重复实现业务。
  - 增加 JSON body 解析辅助与 Cloudflare 错误映射函数。

- `probe_controller/internal/core/server.go`
  - 注册 `/mng/cloudflare` 页面路由，挂载 `mngAuthRequiredMiddleware`。
  - 注册上述 `/mng/api/cloudflare/*` 接口路由，统一挂载 `mngAuthRequiredMiddleware`。

### 测试
- `probe_controller/tests/mng_auth_test.go`
  - 增加未登录访问 `/mng/cloudflare` 重定向断言。
  - 增加未登录访问 `/mng/api/cloudflare/api` 返回 `401` 断言。
  - 增加已登录后 `/mng/panel` 包含 “Cloudflare 管理” 磁贴断言。
  - 增加已登录访问 `/mng/cloudflare` 含 “基础设置/DDNS/ZeroTrust” 文案断言。
  - 增加已登录访问 `/mng/api/cloudflare/api` 返回结构含 `configured` 断言。
  - 新增 `TestMngCloudflareAPIErrorBranches`，覆盖：
    - `POST /mng/api/cloudflare/ddns/apply` 无效 JSON 与 datastore 未初始化错误映射。
    - `POST /mng/api/cloudflare/zerotrust/whitelist` 无效 JSON 与 datastore 未初始化错误映射。
    - `POST /mng/api/cloudflare/zerotrust/whitelist/run` 无效 JSON 与 datastore 未初始化错误映射。

- `probe_controller/internal/core/mng_pages/tg.html`（补齐）
  - 补齐被 `//go:embed mng_pages/tg.html` 引用但缺失的页面文件，修复 `go test ./...` 编译期失败。

## 自测结果
- `cd probe_controller && gofmt -w internal/core/mng_cloudflare_handlers.go internal/core/server.go internal/core/mng_pages.go tests/mng_auth_test.go` ✅
- `cd probe_controller && go test ./tests -run TestMngCloudflareAPIErrorBranches -v` ✅
- `cd probe_controller && go test ./...` ✅
  - `internal/core` 通过
  - `tests` 通过

## 待测试移交项
- TST-MNG-CF-01: 登录后点击 `/mng/panel` 的 Cloudflare 磁贴，确认新页面打开 `/mng/cloudflare`。
- TST-MNG-CF-02: 基础设置页验证 API Key 与 Zone 保存链路。
- TST-MNG-CF-03: DDNS 页执行 apply 并核对返回记录列表。
- TST-MNG-CF-04: ZeroTrust 页验证保存与立即同步链路及错误提示。
- TST-MNG-CF-05: 未登录访问 `/mng/cloudflare` 与 `/mng/api/cloudflare/*` 的鉴权行为回归。
- TST-MNG-CF-06: 校验 `mng_pages/tg.html` 占位页可正常打开 `/mng/tg`，避免 embed 资源缺失回归。
