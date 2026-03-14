# CloudHelper 前后端模块拆分说明

本文档用于说明当前代码按功能拆分后的模块边界，便于后续继续演进。

## 1. 后端（probe_controller）模块

目录：`probe_controller/internal/core`

- `config.go`
  - 服务常量配置（监听地址、数据目录、认证时效参数）。
- `store.go`
  - JSON 数据存储初始化与持久化（`Store`、`initStore`、`Save`）。
- `middleware.go`
  - 通用中间件：CORS、鉴权、HTTPS 强制。
- `status.go`
  - 公共状态处理：`/api/ping`、`/dashboard/status`、首页跳转、JSON 输出。
- `auth.go`
  - Challenge-Response 登录、nonce/session、黑名单、Root CA/管理员证书初始化。
- `version_upgrade.go`
  - 主控版本查询与主控自升级逻辑。
- `proxy_handlers.go`
  - 代理查询 Release 与代理下载转发接口。
- `ws_status.go`
  - 管理端 WebSocket 实时状态推送。
- `server.go`
  - 仅保留服务启动与路由装配（`Run`、`NewMux`）。

目录：`probe_controller/internal/dashboard`

- `page.go`
  - `/dashboard` 页面渲染模块（与 API 路由分离）。

## 2. 前端（probe_manager/frontend）模块

目录：`probe_manager/frontend/src`

- `App.tsx`
  - 应用状态编排与页面路由壳层（登录态、Tab 切换、按钮动作绑定），不再承载具体 UI 片段与底层 API 细节。
- `modules/app/types.ts`
  - 统一类型定义（登录响应、状态响应、升级响应、Tab 类型）。
- `modules/app/constants.ts`
  - 统一常量（本地存储 key、默认升级项目、Tab 配置）。
- `modules/app/authz.ts`
  - 授权与角色解析（`normalizeClaim`、`resolveTabs`）。
- `modules/app/services/controller-api.ts`
  - 主控 HTTP 接口调用封装（状态、nonce、登录、版本、升级）。
- `modules/app/utils/url.ts`
  - URL/WS 地址处理工具（base URL 规范化、WebSocket 地址生成）。
- `modules/app/components/*.tsx`
  - 页面组件拆分：
  - `LoginPanel.tsx` 登录面板
  - `Sidebar.tsx` 侧边栏与 Tab 导航
  - `OverviewTab.tsx` 概要状态页
  - `SystemSettingsTab.tsx` 系统设置与升级页
  - `PlaceholderTab.tsx` 占位页
- `App.css` / `style.css`
  - UI 样式层。

## 3. 模块边界原则

- 认证与授权逻辑优先在独立模块维护，不散落在页面或路由装配代码中。
- 路由装配文件只负责“拼装”，不承载具体业务逻辑。
- 类型、常量、权限规则统一出口，避免重复定义。
- 升级、代理、WebSocket 等特性按能力拆分成独立文件，降低耦合。

## 4. 后续可继续拆分建议

- 前端：将 `App.tsx` 继续拆为 `views/login`、`views/system-settings`、`hooks/useAuth`、`hooks/useUpgrade`。
- 后端：将 `version_upgrade.go` 与 `proxy_handlers.go` 中的 GitHub 客户端逻辑抽成 `release_client` 模块。
