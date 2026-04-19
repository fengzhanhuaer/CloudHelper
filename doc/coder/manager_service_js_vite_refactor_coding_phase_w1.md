# 编码阶段文档 `manager_service` 前端一次性重构为 JavaScript + Vite (W1)

## 工作依据与规则传递声明
- 当前角色: 编码者
- 工作依据文档: [`doc/ai-coding-unified-rules.md`](doc/ai-coding-unified-rules.md)
- 适用规则: AI协作统一规则 单一规范
- 规则遵循声明: 必须遵守本规则。
- 协作传递要求: 后续接手者与协作者必须遵守同一规则。

- 日期: 2026-04-19
- 备注: 依据 [`doc/architect/manager_service_js_vite_refactor_architect_plan.md`](doc/architect/manager_service_js_vite_refactor_architect_plan.md) 与 [`doc/architect/manager_service_js_vite_refactor_requirement_tracking.md`](doc/architect/manager_service_js_vite_refactor_requirement_tracking.md) 进入编码实施。
- 风险:
  - 一次性替换范围大，任何模块漏迁移都会直接影响构建与可用性。
  - 无框架状态管理引入事件风暴风险，需要严格约束订阅与销毁。
- 遗留事项:
  - 待完成全量页面迁移后执行一次性构建与回归。
  - 待补齐复杂模块失败路径验证（Network Assistant/TG/Link）。
- 进度状态: 进行中
- 完成情况:
  - [x] 编码阶段文档初始化并落盘
  - [x] 统一事件模型与基础设施首版落地（event-bus/store/app-shell）
  - [x] API 与状态层迁移首版落地（vanilla services + 状态扩展）
  - [ ] 视图层全量迁移（当前已覆盖 login/overview/system-settings/log-viewer/network-assistant）
  - [x] 去 React/TS 化首版落地（入口切换、vite React 插件移除、package 依赖收敛、build 通过）
- 检查表:
  - [x] 已声明工作依据与规则传递
  - [x] 已记录字符集基线
  - [ ] 已完成执行单元包级实现
  - [ ] 已完成自测并记录结果
- 跟踪表状态: 实现中
- 结论记录: 开始执行一次性重构，编码基线为 UTF-8 无 BOM + CRLF。

## 字符集编码基线
- 字符集类型: UTF-8 无 BOM
- BOM策略: 禁止 BOM
- 换行符规则: CRLF
- 跨平台兼容要求: Windows 优先，同时保证 Linux/macOS 可构建
- 历史文件迁移策略: 历史文件保持原样，仅修改文件按基线执行

## 执行单元包编号与需求编号映射
| 执行单元包 | 需求编号 | 状态 |
|---|---|---|
| PKG-JS-01 | RQ-JS-001 RQ-JS-003 | 已完成（入口切换至 `src/vanilla/main.js`） |
| PKG-JS-02 | RQ-JS-004 | 已完成（event-bus/store/状态订阅） |
| PKG-JS-03 | RQ-JS-002 | 进行中（已迁移 manager-api JS 版并接入 app-shell） |
| PKG-JS-04~PKG-JS-09 | RQ-JS-003 RQ-JS-005 | 进行中（已覆盖关键5页，其余页面待迁移） |
| PKG-JS-10 | RQ-JS-006 | 进行中（react/ts 依赖已移除，历史 TS 文件待清理） |
| PKG-QA-01 | RQ-JS-007 | 待实现 |

## 变更点清单
- 新增无框架基础设施：
  - `manager_service/frontend/src/vanilla/core/events.js`
  - `manager_service/frontend/src/vanilla/state/store.js`
  - `manager_service/frontend/src/vanilla/config/tabs.js`
  - `manager_service/frontend/src/vanilla/authz.js`
  - `manager_service/frontend/src/vanilla/services/fetch-json.js`
  - `manager_service/frontend/src/vanilla/services/core-api.js`
  - `manager_service/frontend/src/vanilla/services/api.js`
  - `manager_service/frontend/src/vanilla/services/manager-api.js`
  - `manager_service/frontend/src/vanilla/main.js`
  - `manager_service/frontend/src/vanilla/app-shell.js`
- 入口改造：`manager_service/frontend/index.html` 改为加载 `./src/vanilla/main.js`。
- 构建链路改造：
  - `manager_service/frontend/vite.config.ts` 移除 React 插件。
  - `manager_service/frontend/package.json` 构建脚本改为 `vite build`，移除 React/TS 依赖。

## 自测结果
- `npm run build`（manager_service/frontend）通过。

## 待测试移交项
- 待编码完成后补充。