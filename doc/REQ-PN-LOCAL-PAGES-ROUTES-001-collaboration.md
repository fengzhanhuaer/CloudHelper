# 协作文档

- 适用规则: AI协作规则
- 后续工作传递声明: 本文档必须传递给后续阶段与后续角色。
- 需求编号: REQ-PN-LOCAL-PAGES-ROUTES-001
- 需求前缀: REQ-PN-LOCAL-PAGES-ROUTES-001
- 当前角色: Architect
- 工作依据文档: [`doc/ai-coding-collaboration.md`](doc/ai-coding-collaboration.md:1)、[`probe_controller/internal/core/mng_pages/panel.html`](probe_controller/internal/core/mng_pages/panel.html:1)、[`probe_node/local_console.go`](probe_node/local_console.go:1771)、[`probe_node/local_pages.go`](probe_node/local_pages.go:5)、[`probe_node/local_pages/panel.html`](probe_node/local_pages/panel.html:197)、[`probe_node/local_pages_routes_test.go`](probe_node/local_pages_routes_test.go:37)
- 状态: 进行中

## 第1章 Architect章节
- 章节责任角色: Architect
- 状态: 进行中

### 1.1 需求定义
- 状态: 已完成

#### 1.1.1 需求目标
- 将 [`/local/panel`](probe_node/local_console.go:1783) 改造成参考主控磁贴主页的入口页。
- 让 [`/local/panel`](probe_node/local_console.go:1880) 只负责入口导航，不再承载四个功能区的完整 Tab 内容。
- 将原本聚合在单页中的代理、DNS、运行日志、系统设置拆分为独立页面和独立路由。
- 保持单页控制台入口体验稳定，同时降低后续页面维护和冲突范围。

#### 1.1.2 需求范围
- 页面入口范围: [`/local/panel`](probe_node/local_console.go:1783) 改为磁贴式入口主页，样式参考 [`probe_controller/internal/core/mng_pages/panel.html`](probe_controller/internal/core/mng_pages/panel.html:1)。
- 路由范围: 新增独立页面路由 `/local/proxy`、`/local/dns`、`/local/logs`、`/local/system`。
- 页面范围: 每个页面都是完整 HTML 文档，具备独立 `<html>`、`<head>`、`<body>` 与页面脚本。
- embed 范围: 扩展 [`probe_node/local_pages.go`](probe_node/local_pages.go:5) 的页面 embed。
- 测试范围: 更新 [`probe_node/local_pages_routes_test.go`](probe_node/local_pages_routes_test.go:37)、[`probe_node/local_console_methods_test.go`](probe_node/local_console_methods_test.go:8)、必要时更新 [`probe_node/local_console_test.go`](probe_node/local_console_test.go:1501)。

#### 1.1.3 非范围
- 不修改登录页 [`probe_node/local_pages/login.html`](probe_node/local_pages/login.html:1) 的整体语义。
- 不把 `/local/panel` 改为重定向到某个子页面。
- 不把四个页面改成前端按需 fetch 的碎片加载模式。
- 不引入新的外部页面模板引擎依赖。
- 不改变现有 API 语义与控制面业务逻辑。

#### 1.1.4 验收标准
- 访问 [`/local/panel`](probe_node/local_console.go:1783) 后呈现磁贴式入口页，磁贴风格与 [`probe_controller/internal/core/mng_pages/panel.html`](probe_controller/internal/core/mng_pages/panel.html:35) 一致。
- 磁贴分别指向 `/local/proxy`、`/local/dns`、`/local/logs`、`/local/system`。
- 四个子页面均为完整 HTML 文档且可独立访问。
- [`/local/panel`](probe_node/local_console.go:1783) 不再包含旧单页 Tab 结构。
- 现有登录、根路由跳转、页面鉴权和 method guard 不回归。

#### 1.1.5 风险
- 若子页面与入口页共享脚本过多，可能导致后续独立维护仍然耦合。
- 若磁贴首页仍保留旧 Tab DOM，测试会误把入口页识别为旧控制台。
- 若独立页面路由与 method guard 不一致，可能出现页面能看但鉴权测试失败。
- 若子页面各自重复过多公共逻辑，后续维护成本会上升。

#### 1.1.6 遗留事项
- 无。

#### 1.1.7 结论
- 采用“磁贴式入口页 + 四个独立完整 HTML 页面 + 单一登录域”的方案。

### 1.2 总体架构
- 状态: 已完成

#### 1.2.1 架构目标
- 让 [`/local/panel`](probe_node/local_console.go:1880) 成为入口主页，承担导航职责。
- 让代理、DNS、日志、系统设置四个页面各自拥有完整的 HTML、样式与脚本边界。
- 保持页面入口结构与主控磁贴面板一致，提升可发现性。
- 让页面路由、页面测试和页面模板能独立演进。

#### 1.2.2 总体设计
- 保留 [`/local/panel`](probe_node/local_console.go:1880) 作为磁贴主页，入口内容参考 [`probe_controller/internal/core/mng_pages/panel.html`](probe_controller/internal/core/mng_pages/panel.html:1) 的 tile 布局。
- 新增四个完整页面文件:
  - [`probe_node/local_pages/proxy.html`](probe_node/local_pages/proxy.html)
  - [`probe_node/local_pages/dns.html`](probe_node/local_pages/dns.html)
  - [`probe_node/local_pages/logs.html`](probe_node/local_pages/logs.html)
  - [`probe_node/local_pages/system.html`](probe_node/local_pages/system.html)
- 在 [`probe_node/local_pages.go`](probe_node/local_pages.go:5) 中扩展 `//go:embed`，为每个完整页面提供独立字符串。
- 在 [`probe_node/local_console.go`](probe_node/local_console.go:1778) 中新增对应页面路由与 handler。
- 子页面保持独立完整文档，避免继续使用 Tab 片段拼装。

```mermaid
flowchart TD
  A[/local/panel] --> B[磁贴入口页]
  B --> C[/local/proxy]
  B --> D[/local/dns]
  B --> E[/local/logs]
  B --> F[/local/system]
  C --> G[代理控制与状态]
  D --> H[DNS 状态与映射]
  E --> I[运行日志]
  F --> J[TUN 与系统设置]
```

#### 1.2.3 关键模块
| 模块编号 | 模块名称 | 职责 | 输入 | 输出 |
|---|---|---|---|---|
| M1 | Panel Tile Home | 提供磁贴入口与导航 | 登录态 | home html |
| M2 | Proxy Page | 提供代理状态与控制 | 登录态 + proxy api | proxy html |
| M3 | DNS Page | 提供 DNS 状态与映射 | 登录态 + dns api | dns html |
| M4 | Logs Page | 提供运行日志查看 | 登录态 + logs api | logs html |
| M5 | System Page | 提供 TUN 与升级入口 | 登录态 + system api | system html |
| M6 | Local Pages Embed | 为所有页面提供静态嵌入 | html files | embedded html strings |

#### 1.2.4 关键流程
- 入口流程:
  1. 用户登录后访问 [`/local/panel`](probe_node/local_console.go:1880)。
  2. 页面显示磁贴入口。
  3. 用户点击磁贴进入各自独立页面。
- 子页面流程:
  1. 子页面路由验证 session。
  2. 返回对应完整 HTML。
  3. 页面内部继续调用既有 API 获取数据与执行操作。
- 根路由流程:
  1. 访问 `/` 时按现有登录态重定向到 [`/local/panel`](probe_node/local_console.go:1860) 或登录页。
  2. 不改变现有根路由语义。

#### 1.2.5 结论
- 该方案把入口页与功能页彻底拆开，且保持浏览入口的可发现性与可维护性。

### 1.3 单元设计
- 状态: 已完成

#### 1.3.1 U1 磁贴入口页单元
- 目标: 将 [`/local/panel`](probe_node/local_console.go:1880) 改造为磁贴式入口页。
- 变更点:
  - 保留首页 `<html>`、`<head>`、`<body>`、基础布局与会话信息。
  - 入口区采用磁贴卡片，分别指向 `/local/proxy`、`/local/dns`、`/local/logs`、`/local/system`。
  - 入口页不再包含旧的代理/DNS/日志/系统详细 Tab 内容。
- 验收: 页面样式与主控磁贴主页相近，且不保留旧 Tab DOM。

#### 1.3.2 U2 代理独立页面单元
- 目标: 新增 [`probe_node/local_pages/proxy.html`](probe_node/local_pages/proxy.html) 作为完整 HTML。
- 内容范围:
  - 代理状态。
  - 代理组选链。
  - 代理启用/关闭/直接/拒绝等交互。
  - 代理刷新按钮与状态展示。
- 验收: 页面可独立访问，且不依赖入口页 DOM。

#### 1.3.3 U3 DNS 独立页面单元
- 目标: 新增 [`probe_node/local_pages/dns.html`](probe_node/local_pages/dns.html) 作为完整 HTML。
- 内容范围:
  - DNS 状态。
  - TUN DNS 监听信息。
  - Fake IP 网段与映射表。
- 验收: 页面可独立访问，保留 DNS 状态与映射查看能力。

#### 1.3.4 U4 日志独立页面单元
- 目标: 新增 [`probe_node/local_pages/logs.html`](probe_node/local_pages/logs.html) 作为完整 HTML。
- 内容范围:
  - 日志来源。
  - 过滤器。
  - 自动刷新。
  - 日志内容展示。
- 验收: 页面可独立访问，且日志查询逻辑不回归。

#### 1.3.5 U5 系统独立页面单元
- 目标: 新增 [`probe_node/local_pages/system.html`](probe_node/local_pages/system.html) 作为完整 HTML。
- 内容范围:
  - TUN 状态。
  - TUN 安装/检查。
  - 系统升级。
  - 重启与退出。
- 验收: 页面可独立访问，系统升级与 TUN 入口不回归。

#### 1.3.6 U6 页面嵌入与路由单元
- 目标: 在 [`probe_node/local_pages.go`](probe_node/local_pages.go:5) 与 [`probe_node/local_console.go`](probe_node/local_console.go:1778) 中完成页面嵌入和路由接入。
- 规则:
  - 每个页面对应单独 `//go:embed` 变量。
  - 每个路由直接返回对应完整 HTML。
  - 新页面路由与 method guard 维持一致。
- 验收: 路由测试与页面内容测试通过。

### 1.4 Code任务执行包
- 状态: 已完成

| 任务编号 | 需求编号 | 单元编号 | 目标文件 | 操作类型 | 任务说明 |
|---|---|---|---|---|---|
| T-001 | REQ-PN-LOCAL-PAGES-ROUTES-001 | U1 | [`probe_node/local_pages/panel.html`](probe_node/local_pages/panel.html:197) | 修改 | 将入口页改为磁贴式主页，保留导航和会话信息 |
| T-002 | REQ-PN-LOCAL-PAGES-ROUTES-001 | U2 U3 U4 U5 | [`probe_node/local_pages/proxy.html`](probe_node/local_pages/proxy.html)、[`probe_node/local_pages/dns.html`](probe_node/local_pages/dns.html)、[`probe_node/local_pages/logs.html`](probe_node/local_pages/logs.html)、[`probe_node/local_pages/system.html`](probe_node/local_pages/system.html) | 新增 | 四个独立完整 HTML 页面 |
| T-003 | REQ-PN-LOCAL-PAGES-ROUTES-001 | U6 | [`probe_node/local_pages.go`](probe_node/local_pages.go:5)、[`probe_node/local_console.go`](probe_node/local_console.go:1778) | 修改 | 扩展 embed 并增加四个页面路由 |
| T-004 | REQ-PN-LOCAL-PAGES-ROUTES-001 | U6 | [`probe_node/local_pages_routes_test.go`](probe_node/local_pages_routes_test.go:37)、[`probe_node/local_console_methods_test.go`](probe_node/local_console_methods_test.go:8)、[`probe_node/local_console_test.go`](probe_node/local_console_test.go:1501) | 修改 | 更新页面断言、method guard 与路由行为测试 |
| T-005 | REQ-PN-LOCAL-PAGES-ROUTES-001 | U6 | [`doc/REQ-PN-LOCAL-PAGES-ROUTES-001-collaboration.md`](doc/REQ-PN-LOCAL-PAGES-ROUTES-001-collaboration.md) | 修改 | 回填 Code 阶段执行证据、测试结果与接口矩阵 |

### 1.5 Architect需求跟踪矩阵
- 状态: 已完成

| 需求编号 | 需求说明 | 架构单元 | Code任务 | 状态 | 验收口径 |
|---|---|---|---|---|---|
| REQ-PN-LOCAL-PAGES-ROUTES-001-R1 | `/local/panel` 改为磁贴入口页 | U1 U6 | T-001 T-003 | 进行中 | 页面为入口页而非功能页 |
| REQ-PN-LOCAL-PAGES-ROUTES-001-R2 | 四个功能区拆成独立页面 | U2 U3 U4 U5 | T-002 | 进行中 | 四个独立 HTML 页面可访问 |
| REQ-PN-LOCAL-PAGES-ROUTES-001-R3 | 页面嵌入与路由接入 | U6 | T-003 | 进行中 | `/local/proxy` 等路由返回完整页面 |
| REQ-PN-LOCAL-PAGES-ROUTES-001-R4 | 页面与测试不回归 | U6 | T-004 T-005 | 进行中 | 页面断言与 method guard 通过 |

### 1.6 Architect关键接口跟踪矩阵
- 状态: 已完成

| 接口编号 | 需求编号 | 接口 | 所属文件 | 输入 | 输出 | 状态 | 说明 |
|---|---|---|---|---|---|---|---|
| IF-001 | REQ-PN-LOCAL-PAGES-ROUTES-001-R1 | `GET /local/panel` | [`probe_node/local_console.go`](probe_node/local_console.go:1880) | session | panel home html | 既有修改 | 入口页磁贴化 |
| IF-002 | REQ-PN-LOCAL-PAGES-ROUTES-001-R2 | `GET /local/proxy` | [`probe_node/local_console.go`](probe_node/local_console.go:1778) | session | proxy html | 待新增 | 代理独立页面 |
| IF-003 | REQ-PN-LOCAL-PAGES-ROUTES-001-R2 | `GET /local/dns` | [`probe_node/local_console.go`](probe_node/local_console.go:1778) | session | dns html | 待新增 | DNS 独立页面 |
| IF-004 | REQ-PN-LOCAL-PAGES-ROUTES-001-R2 | `GET /local/logs` | [`probe_node/local_console.go`](probe_node/local_console.go:1778) | session | logs html | 待新增 | 日志独立页面 |
| IF-005 | REQ-PN-LOCAL-PAGES-ROUTES-001-R2 | `GET /local/system` | [`probe_node/local_console.go`](probe_node/local_console.go:1778) | session | system html | 待新增 | 系统独立页面 |
| IF-006 | REQ-PN-LOCAL-PAGES-ROUTES-001-R3 | page embed variables | [`probe_node/local_pages.go`](probe_node/local_pages.go:5) | html files | embedded html | 待新增 | 每页独立 embed |

### 1.7 门禁裁判
- 状态: 已完成

#### 1.7.1 阶段性裁判输入
- 用户已确认目标形态为独立页面与独立路由。
- 用户进一步确认 [`/local/panel`](probe_node/local_console.go:1783) 保留为磁贴式入口页，参考主控主页布局。
- 当前方案与既有本地控制台单页入口语义一致，仅改变入口承载方式。

#### 1.7.2 门禁结论
- 结论: 通过。
- 放行状态: 放行进入 Code 实施。
- 条件: Code 必须保持 [`/local/panel`](probe_node/local_console.go:1783) 为磁贴入口页，四个功能页必须是独立完整 HTML，且不得退回 Tab 单页模式。

## 第2章 Code章节
- 章节责任角色: Code
- 状态: 已完成

### 2.1 Code需求跟踪矩阵
- 状态: 已完成

| 需求编号 | Code任务 | 影响文件 | 实现状态 | 测试状态 | 证据 | 备注 |
|---|---|---|---|---|---|---|
| REQ-PN-LOCAL-PAGES-ROUTES-001-R1 | T-001 T-003 | [`probe_node/local_pages/panel.html`](probe_node/local_pages/panel.html:1)、[`probe_node/local_console.go`](probe_node/local_console.go:1783) | 已完成 | 已通过 | [`TestProbeLocalPanelServedAfterLogin()`](probe_node/local_pages_routes_test.go:37) | [`/local/panel`](probe_node/local_console.go:1783) 已改为磁贴入口页 |
| REQ-PN-LOCAL-PAGES-ROUTES-001-R2 | T-002 | [`probe_node/local_pages/proxy.html`](probe_node/local_pages/proxy.html:1)、[`probe_node/local_pages/dns.html`](probe_node/local_pages/dns.html:1)、[`probe_node/local_pages/logs.html`](probe_node/local_pages/logs.html:1)、[`probe_node/local_pages/system.html`](probe_node/local_pages/system.html:1) | 已完成 | 已通过 | [`TestProbeLocalStandalonePagesServedAfterLogin()`](probe_node/local_pages_routes_test.go:69) | 四个功能页均为完整 HTML |
| REQ-PN-LOCAL-PAGES-ROUTES-001-R3 | T-003 | [`probe_node/local_pages.go`](probe_node/local_pages.go:5)、[`probe_node/local_console.go`](probe_node/local_console.go:1783) | 已完成 | 已通过 | [`go test ./...`](probe_node/go.mod:1) | 已扩展 embed 与页面 handler |
| REQ-PN-LOCAL-PAGES-ROUTES-001-R4 | T-004 T-005 | [`probe_node/local_pages_routes_test.go`](probe_node/local_pages_routes_test.go:37)、[`probe_node/local_console_test.go`](probe_node/local_console_test.go:171)、[`doc/REQ-PN-LOCAL-PAGES-ROUTES-001-collaboration.md`](doc/REQ-PN-LOCAL-PAGES-ROUTES-001-collaboration.md:216) | 已完成 | 已通过 | `ok github.com/cloudhelper/probe_node 9.755s` | 页面路由与鉴权断言已更新 |

### 2.2 Code关键接口跟踪矩阵
- 状态: 已完成

| 接口编号 | 需求编号 | 实现位置 | 接口说明 | 实现状态 | 测试证据 | 备注 |
|---|---|---|---|---|---|---|
| IF-001 | REQ-PN-LOCAL-PAGES-ROUTES-001-R1 | [`probeLocalPanelPageHandler()`](probe_node/local_console.go:1884) | `GET /local/panel` | 已完成 | [`TestProbeLocalPanelServedAfterLogin()`](probe_node/local_pages_routes_test.go:37) | 输出磁贴入口页 |
| IF-002 | REQ-PN-LOCAL-PAGES-ROUTES-001-R2 | [`probeLocalProxyPageHandler()`](probe_node/local_console.go:1888) | `GET /local/proxy` | 已完成 | [`TestProbeLocalStandalonePagesServedAfterLogin()`](probe_node/local_pages_routes_test.go:69) | 代理独立页面 |
| IF-003 | REQ-PN-LOCAL-PAGES-ROUTES-001-R2 | [`probeLocalDNSPageHandler()`](probe_node/local_console.go:1892) | `GET /local/dns` | 已完成 | [`TestProbeLocalStandalonePagesServedAfterLogin()`](probe_node/local_pages_routes_test.go:69) | DNS 独立页面 |
| IF-004 | REQ-PN-LOCAL-PAGES-ROUTES-001-R2 | [`probeLocalLogsPageHandler()`](probe_node/local_console.go:1896) | `GET /local/logs` | 已完成 | [`TestProbeLocalStandalonePagesServedAfterLogin()`](probe_node/local_pages_routes_test.go:69) | 日志独立页面 |
| IF-005 | REQ-PN-LOCAL-PAGES-ROUTES-001-R2 | [`probeLocalSystemPageHandler()`](probe_node/local_console.go:1900) | `GET /local/system` | 已完成 | [`TestProbeLocalStandalonePagesServedAfterLogin()`](probe_node/local_pages_routes_test.go:69) | 系统设置独立页面 |
| IF-006 | REQ-PN-LOCAL-PAGES-ROUTES-001-R3 | [`probe_node/local_pages.go`](probe_node/local_pages.go:5) | page embed variables | 已完成 | [`go test ./...`](probe_node/go.mod:1) | 已 embed 五个 local 页面 |

### 2.3 Code测试项跟踪矩阵
- 状态: 已完成

| 测试项编号 | 需求编号 | Code任务 | 测试说明 | 状态 | 证据 | 备注 |
|---|---|---|---|---|---|---|
| TC-001 | REQ-PN-LOCAL-PAGES-ROUTES-001-R1 | T-001 | 验证 [`/local/panel`](probe_node/local_console.go:1783) 为磁贴入口且不再包含旧 Tab DOM | 已通过 | [`TestProbeLocalPanelServedAfterLogin()`](probe_node/local_pages_routes_test.go:37) | 覆盖四个磁贴入口 |
| TC-002 | REQ-PN-LOCAL-PAGES-ROUTES-001-R2 | T-002 T-003 | 验证四个独立页面返回完整 HTML 并仅包含自身页面 DOM | 已通过 | [`TestProbeLocalStandalonePagesServedAfterLogin()`](probe_node/local_pages_routes_test.go:69) | 覆盖 `/local/proxy`、`/local/dns`、`/local/logs`、`/local/system` |
| TC-003 | REQ-PN-LOCAL-PAGES-ROUTES-001-R3 | T-003 | 验证未登录访问页面统一跳转登录 | 已通过 | [`TestProbeLocalProtectedRoutesRequireSession()`](probe_node/local_console_test.go:171) | 覆盖五个页面路由 |
| TC-004 | REQ-PN-LOCAL-PAGES-ROUTES-001-R4 | T-004 | 验证页面路由只允许 GET | 已通过 | [`TestProbeLocalPanelMethodNotAllowed()`](probe_node/local_pages_routes_test.go:175) | 覆盖五个页面路由 |
| TC-005 | REQ-PN-LOCAL-PAGES-ROUTES-001-R4 | T-004 | 执行完整包级回归 | 已通过 | `ok github.com/cloudhelper/probe_node 9.755s` | 已执行 [`go test ./...`](probe_node/go.mod:1) |

### 2.4 Code缺陷跟踪矩阵
- 状态: 已完成

| 缺陷编号 | 需求编号 | 关联测试 | 缺陷说明 | 严重级别 | 状态 | 修复证据 | 备注 |
|---|---|---|---|---|---|---|---|
| 无 | 无 | 无 | 无 | 无 | 已完成 | 无 | 本轮未发现新增缺陷 |

### 2.5 Code执行证据
- 状态: 已完成

#### 2.5.1 修改接口
- 保持 [`/local/panel`](probe_node/local_console.go:1783) 路由不变，但输出内容改为磁贴入口页。
- 新增 [`/local/proxy`](probe_node/local_console.go:1784) 页面路由，handler 为 [`probeLocalProxyPageHandler()`](probe_node/local_console.go:1888)。
- 新增 [`/local/dns`](probe_node/local_console.go:1785) 页面路由，handler 为 [`probeLocalDNSPageHandler()`](probe_node/local_console.go:1892)。
- 新增 [`/local/logs`](probe_node/local_console.go:1786) 页面路由，handler 为 [`probeLocalLogsPageHandler()`](probe_node/local_console.go:1896)。
- 新增 [`/local/system`](probe_node/local_console.go:1787) 页面路由，handler 为 [`probeLocalSystemPageHandler()`](probe_node/local_console.go:1900)。
- 新增通用页面输出 helper [`serveProbeLocalHTMLPage()`](probe_node/local_console.go:1904)。

#### 2.5.2 配置文件
- 无配置文件变更。

#### 2.5.3 执行报告
- 已将 [`probe_node/local_pages/panel.html`](probe_node/local_pages/panel.html:1) 改造为磁贴入口页。
- 已新增四个独立完整页面: [`proxy.html`](probe_node/local_pages/proxy.html:1)、[`dns.html`](probe_node/local_pages/dns.html:1)、[`logs.html`](probe_node/local_pages/logs.html:1)、[`system.html`](probe_node/local_pages/system.html:1)。
- 已扩展 [`probe_node/local_pages.go`](probe_node/local_pages.go:5) 的 `//go:embed` 页面变量。
- 已更新页面路由、未登录保护、页面内容和 method guard 测试。

#### 2.5.4 影响文件
- [`probe_node/local_pages/panel.html`](probe_node/local_pages/panel.html:1)
- [`probe_node/local_pages/proxy.html`](probe_node/local_pages/proxy.html:1)
- [`probe_node/local_pages/dns.html`](probe_node/local_pages/dns.html:1)
- [`probe_node/local_pages/logs.html`](probe_node/local_pages/logs.html:1)
- [`probe_node/local_pages/system.html`](probe_node/local_pages/system.html:1)
- [`probe_node/local_pages.go`](probe_node/local_pages.go:5)
- [`probe_node/local_console.go`](probe_node/local_console.go:1783)
- [`probe_node/local_console_test.go`](probe_node/local_console_test.go:171)
- [`probe_node/local_pages_routes_test.go`](probe_node/local_pages_routes_test.go:37)
- [`doc/REQ-PN-LOCAL-PAGES-ROUTES-001-collaboration.md`](doc/REQ-PN-LOCAL-PAGES-ROUTES-001-collaboration.md:216)

#### 2.5.5 自测结果
- 已执行 [`gofmt`](probe_node/go.mod:1): `gofmt -w local_pages.go local_console.go local_console_test.go local_pages_routes_test.go`。
- 已执行 [`go test ./...`](probe_node/go.mod:1)。
- 测试输出: `ok github.com/cloudhelper/probe_node 9.755s`。
