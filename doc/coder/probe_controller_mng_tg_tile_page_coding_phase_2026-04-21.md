# 编码阶段文档 — `probe_controller` `/mng` TG 助手磁贴与完整管理页实现

## 工作依据与规则传递声明
- 当前角色: 编码者
- 工作依据文档: `doc/ai-coding-unified-rules.md`
- 适用规则: AI协作统一规则 单一规范
- 规则遵循声明: 必须遵守本规则。
- 协作传递要求: 后续接手者与协作者必须遵守同一规则。

- 日期: 2026-04-21
- 备注: 按架构方案实现“系统设置同构”模式，TG 助手在 `/mng` 下走 HTTP + Cookie + `mng` 鉴权，不走 admin websocket。
- 风险:
  - TG 运行能力依赖 Telegram 外部网络，联调时可能出现网络波动与超时。
  - 页面为单文件实现，后续可继续拆分为模块化静态资源降低维护成本。
- 遗留事项:
  - 后续补充 TG 业务失败分支自动化测试（当前已完成鉴权与基础可达性覆盖）。
- 进度状态: 已完成
- 完成情况: 已完成 TG 磁贴、`/mng/tg` 完整管理页、`/mng/api/tg/*` 路由挂载与基础测试，`go test ./...` 通过。
- 检查表:
  - [x] 工作依据与规则传递声明完整
  - [x] TG 页面与 embed 已落盘
  - [x] `/mng/tg` 页面路由与 `/mng/api/tg/*` 接口已接入
  - [x] 走 HTTP + Cookie + `mngAuthRequiredMiddleware` 模式
  - [x] `gofmt` 与 `go test ./...` 通过
- 跟踪表状态: 已完成
- 结论记录: 本次实现满足“在 `/mng/panel` 增加 TG 助手磁贴并进入 `/mng/tg`，页面覆盖账号/登录/任务/Bot，接口沿用系统设置同构模式”的要求。

## 执行单元包编号与需求编号映射

| 执行单元包 | 需求编号 | 状态 |
|---|---|---|
| PKG-MNG-TG-01 | RQ-MNG-018 | 已完成 |
| PKG-MNG-TG-01 PKG-MNG-TG-02 | RQ-MNG-019 | 已完成 |
| PKG-MNG-TG-04 | RQ-MNG-020 | 已完成 |
| PKG-MNG-TG-03 | RQ-MNG-021 | 已完成 |
| PKG-MNG-TG-02 PKG-MNG-TG-05 | RQ-MNG-022 | 已完成 |

## 变更点清单

### 页面与资源
- `probe_controller/internal/core/mng_pages/panel.html`
  - 新增 TG 助手磁贴，入口 `/mng/tg`。
  - 更新面板描述文案，纳入 TG 助手入口说明。

- `probe_controller/internal/core/mng_pages/tg.html`
  - 将占位页替换为完整管理页。
  - 提供四个主视图：共享 API Key、账号与登录、任务管理、Bot 管理。
  - 页面交互统一使用 `fetch('/mng/api/tg/*', { credentials: 'same-origin' })`。
  - 登录态检查复用 `/mng/api/session`，失败跳转 `/mng`。

- `probe_controller/internal/core/mng_pages.go`
  - 继续使用 `//go:embed mng_pages/tg.html`，新页面已与 embed 对齐。

### 路由与接口
- `probe_controller/internal/core/server.go`
  - 新增 `/mng/tg` 页面路由，挂载 `mngAuthRequiredMiddleware`。
  - 新增并挂载 `/mng/api/tg/*` 路由到 `mng` 鉴权中间件：
    - `GET /mng/api/tg/api/get`
    - `POST /mng/api/tg/api/set`
    - `GET /mng/api/tg/accounts/list`
    - `POST /mng/api/tg/accounts/refresh`
    - `POST /mng/api/tg/account/add`
    - `POST /mng/api/tg/account/remove`
    - `POST /mng/api/tg/account/send_code`
    - `POST /mng/api/tg/account/sign_in`
    - `POST /mng/api/tg/account/logout`
    - `POST /mng/api/tg/bot/get`
    - `POST /mng/api/tg/bot/set`
    - `POST /mng/api/tg/bot/test_send`
    - `POST /mng/api/tg/targets/list`
    - `POST /mng/api/tg/targets/refresh`
    - `POST /mng/api/tg/schedule/list`
    - `POST /mng/api/tg/schedule/add`
    - `POST /mng/api/tg/schedule/update`
    - `POST /mng/api/tg/schedule/remove`
    - `POST /mng/api/tg/schedule/set_enabled`
    - `POST /mng/api/tg/schedule/send_now`
    - `POST /mng/api/tg/schedule/history`
    - `POST /mng/api/tg/schedule/pending`

- `probe_controller/internal/core/mng_tg_handlers.go`
  - 复用已有 TG 逻辑函数，仅做 JSON 参数解析与响应透传。
  - 统一错误映射由 `writeMngTGError` 处理。

### 测试
- `probe_controller/tests/mng_auth_test.go`
  - 增加未登录访问 `/mng/tg` 重定向断言。
  - 增加未登录访问 `/mng/api/tg/api/get` 返回 `401` 断言。
  - 增加已登录后 `/mng/panel` 包含 “TG 助手” 磁贴断言。
  - 增加已登录访问 `/mng/tg` 含 “共享 API Key/账号与登录/任务管理/Bot 管理” 文案断言。
  - 增加已登录访问 `/mng/api/tg/api/get` 返回结构含 `configured` 断言。

## 自测结果
- `cd probe_controller && gofmt -w internal/core/server.go tests/mng_auth_test.go` ✅
- `cd probe_controller && go test ./...` ✅
  - `internal/core` 通过
  - `tests` 通过

## 待测试移交项
- TST-MNG-TG-01: 登录后点击 `/mng/panel` 的 TG 助手磁贴，确认新页面打开 `/mng/tg`。
- TST-MNG-TG-02: 共享 API Key 保存与回显链路验证。
- TST-MNG-TG-03: 账号新增、发送验证码、登录、登出、删除链路验证。
- TST-MNG-TG-04: 任务增改删、启停、立即发送、历史与待执行链路验证。
- TST-MNG-TG-05: Bot 配置保存、读取、测试发送链路与错误提示验证。
- TST-MNG-TG-06: 未登录访问 `/mng/tg` 与 `/mng/api/tg/*` 鉴权行为回归。
