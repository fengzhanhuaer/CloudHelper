# 编码阶段文档 — probe_controller `/mng` 探针管理磁贴与子页签实现

## 工作依据与规则传递声明
- 当前角色: 编码者
- 工作依据文档: `doc/ai-coding-unified-rules.md`
- 适用规则: AI协作统一规则 单一规范
- 规则遵循声明: 必须遵守本规则。
- 协作传递要求: 后续接手者与协作者必须遵守同一规则。

- 日期: 2026-04-20
- 备注: 在既有 `/mng` 独立认证域基础上，新增“探针管理”磁贴与新页面，并实现列表/状态/日志/Shell 子tab；复用现有探针领域能力，避免引入并行实现。
- 风险:
  - 新增页面脚本为原生 JS，后续若继续扩展复杂交互，维护复杂度会上升。
  - 探针操作依赖在线探针会话，离线节点执行日志/Shell/升级会返回预期错误，需在运维侧理解该限制。
- 遗留事项:
  - 可后续将 `/mng/probe` 页面抽离为独立静态资源与模块化脚本。
  - 可补充 `/mng/api/probe/*` 更细粒度 E2E 测试（如 Shell 会话错误分支、日志过滤边界）。
- 进度状态: 已完成
- 完成情况: 已完成磁贴入口、新页面、子tab及后端接口联通，测试通过。
- 检查表:
  - [x] 工作依据与规则传递声明完整
  - [x] 页面与路由落盘完成
  - [x] 探针增删改升级接口可访问
  - [x] 探针状态/日志/Shell 与快捷命令接口可访问
  - [x] gofmt 与 go test 通过
- 跟踪表状态: 待复盘
- 结论记录: 本次实现满足“主控 mng 添加探针管理磁贴，点击后新页面打开探针管理，并包含探针列表/状态/日志/Shell 子tab”的需求。

## 执行单元包编号与需求编号映射

| 执行单元包 | 需求编号 | 状态 |
|---|---|---|
| PKG-MNG-PROBE-01 | RQ-MNG-009 | 已完成 |
| PKG-MNG-PROBE-02 | RQ-MNG-010 | 已完成 |
| PKG-MNG-PROBE-03 | RQ-MNG-011 | 已完成 |
| PKG-MNG-PROBE-04 | RQ-MNG-012 | 已完成 |

## 变更点清单

### 路由与页面嵌入
- `probe_controller/internal/core/mng_pages.go`
  - 新增 `//go:embed mng_pages/probe.html`，注册 `mngProbePageHTML`。
- `probe_controller/internal/core/server.go`
  - 新增 `/mng/probe` 页面路由（受 `mngAuthRequiredMiddleware` 保护）。
  - 新增 `/mng/api/probe/*` 接口路由：
    - `/mng/api/probe/nodes`
    - `/mng/api/probe/node/create`
    - `/mng/api/probe/node/update`
    - `/mng/api/probe/node/delete`
    - `/mng/api/probe/node/restore`
    - `/mng/api/probe/status`
    - `/mng/api/probe/logs`
    - `/mng/api/probe/upgrade`
    - `/mng/api/probe/upgrade/all`
    - `/mng/api/probe/shell/session/start`
    - `/mng/api/probe/shell/session/exec`
    - `/mng/api/probe/shell/session/stop`
    - `/mng/api/probe/shell/shortcuts`
    - `/mng/api/probe/shell/shortcuts/upsert`
    - `/mng/api/probe/shell/shortcuts/delete`

### 面板磁贴入口
- `probe_controller/internal/core/mng_pages/panel.html`
  - 新增“探针管理”磁贴（`target="_blank"` 新页面打开 `/mng/probe`）。
  - 更新副标题，说明系统设置与探针管理入口。

### 探针管理页面与子tab
- `probe_controller/internal/core/mng_pages/probe.html`（新增）
  - 子tab：探针列表、探针状态、探针日志、探针 Shell。
  - 探针列表：实现创建、编辑、删除、恢复、单节点升级、一键升级。
  - 探针状态：实现即时刷新与定时轮询展示。
  - 探针日志：按节点、行数、时间窗口、级别读取日志并展示。
  - 探针 Shell：支持会话启动/执行/停止，支持快捷命令增删与执行填充。

### 探针管理后端处理
- `probe_controller/internal/core/mng_probe_handlers.go`（新增）
  - 提供 `/mng/probe` 页面 handler。
  - 提供 `/mng/api/probe/*` 的 HTTP handler，实现对现有核心能力的复用：
    - 复用 `createProbeNodeLocked`、`updateProbeNodeLocked`、`deleteProbeNodeLocked`、`restoreDeletedProbeNodeLocked`
    - 复用 `loadProbeNodeStatusLocked`、`loadProbeNodeStatusByIDLocked`
    - 复用 `fetchProbeLogsFromNode`
    - 复用 `dispatchUpgradeToProbe`
    - 复用 `dispatchProbeShellSessionControl`
    - 复用 `loadProbeShellShortcutsLocked`、`upsertProbeShellShortcutLocked`、`removeProbeShellShortcutLocked`
  - 增加 `ProbeStore` 空值保护，避免在测试初始化场景出现空指针。

### 测试更新
- `probe_controller/tests/mng_auth_test.go`
  - 增加对 `/mng/probe` 未登录重定向验证。
  - 增加对 `/mng/api/probe/nodes` 未登录 401 验证。
  - 增加对 `/mng/panel` 含“探针管理”磁贴文案验证。
  - 增加对已登录访问 `/mng/probe` 页面内容验证。
  - 增加对已登录访问 `/mng/api/probe/nodes` 基础返回结构验证。

## 自测结果
- `gofmt -w internal/core/mng_probe_handlers.go internal/core/server.go tests/mng_auth_test.go` ✅
- `cd probe_controller && go test ./...` ✅
  - `internal/core` 通过
  - `tests` 通过

## 待测试移交项
- TST-MNG-PROBE-01: 登录后从 `/mng/panel` 点击“探针管理”磁贴，确认新窗口打开 `/mng/probe`。
- TST-MNG-PROBE-02: 列表页执行探针创建/编辑/删除/恢复/升级联调。
- TST-MNG-PROBE-03: 状态页在线/离线节点轮询展示与字段完整性验证。
- TST-MNG-PROBE-04: 日志页按参数读取返回与错误路径提示验证。
- TST-MNG-PROBE-05: Shell 会话启动/执行/停止与快捷命令增删验证。
