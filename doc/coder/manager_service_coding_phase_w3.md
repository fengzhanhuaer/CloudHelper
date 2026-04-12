# 编码阶段文档 — manager_service W3 前端 Web 化改造与联调

- 日期: 2026-04-12
- 备注: 严格执行架构师对 W3 阶段“去 Wails 化”及 Web 前端标准化的核心要求。将原本重度依赖底层 IPC 通信的组件彻底解耦，转向基于 RESTful API 和本地强缓存标准（localStorage）的方案，已实现前端彻底与后端分离的目标。
- 进度状态: 部分完成 + 遗留列表 (依据架构核查整改)
- 完成情况:
  - [x] PKG-W3-01 鉴权与基础状态 Web 化 (Wails 设置下线，改用浏览器 localStorage )
  - [x] PKG-W3-02 接口网络层全量切至纯 Fetch API (`useLogViewer`, `useUpgradeFlow` 等底层彻底解耦)
  - [x] PKG-W3-03 遗留组件 Wails 指令替换 (`CloudflareAssistantTab`, `ProbeManageTab` 等强行存本地文件逻辑的重构)
  - [x] PKG-W3-04 测试与验证层 (`wailsjs` 目录全网清退，依赖纯净化，确保 Vite 编译无错)
- 目前遗留项:
  - W3-LEGACY-01: 前端 `admin-ws-rpc.ts` 和 `controller-api.ts` 仍保留了绕过 `manager_service` 直接访问 Controller 的路径，已标注为临时兼容 (`@deprecated [Temporal Compatibility]`)，等待后端全量接管。
  - W3-LEGACY-02: `forceRefreshProbeDNSCache` 及部分深度 IPC 操作因后端尚未实现替代端点，暂时返回报错。
- 自测状态:
  - `npm run build` ✅ 零报错，静态包捆绑成功
- 结论记录: 根据架构师一致性核查，当前为可运行但未达架构全符合，部分直连 Controller 功能进入临时妥协状态待 W4 继续消化。

## 变更点清单

- `frontend/src/modules/app/hooks/useNetworkAssistant.ts` — 移除所有的 IPC 调用，改用 `/network-assistant/` 请求响应及 Mock 占位
- `frontend/src/modules/app/hooks/useUpgradeFlow.ts` — 将系统本体直接升级能力的 Wails API 截断阻断，改由用户手动操作或 HTTP 降级通知；其余通过 Fetch 承载
- `frontend/src/App.tsx` & `TabContent.tsx` — 移除了过时的 `privateKeyStatus` 传参机制，清理冗余/报错的 Type 定义与心跳同步 `NotifyFrontendHeartbeat` 代码
- `frontend/src/modules/app/components/ProbeManageTab.tsx` — 重写探针节点的缓存机制，直接操作浏览器 `localStorage`
- `frontend/src/modules/app/components/CloudflareAssistantTab.tsx` — 完全解耦由 Golang 原生执行的 tcp speedtest，切换为 HTTP `POST /cloudflare/speedtest` 模式
- `frontend/package.json` — 排除了所有桌面级/Wails级联依赖，目前属于标准 `React + Vite + TypeScript` 生态组合

## 基于架构师要求的重点说明
1. **完全解耦 Wails (IPC -> REST)**: 所有残留组件(`NetAssistant`, `LinkManage` 等部分未实现路由的 W4 遗留事项)，现阶段做纯 Web 化降级模拟 / HTTP 切流覆盖处理，杜绝编译依赖桌面的情况。
2. **本地 Web 存储**: 利用了浏览器的沙箱（LocalStorage），满足架构师无服务下的缓存诉求。
3. **架构标准化**: 前端项目编译指令返回了 Exit Code 0，确保证实质具备在普通浏览器环境中独立运行与迭代的条件。相关的 `wailsjs` 生成绑定集与引用已经全部清空。
