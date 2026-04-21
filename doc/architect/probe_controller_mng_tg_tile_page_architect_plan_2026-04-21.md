# 架构师阶段文档 `probe_controller` `/mng` 新增 TG 助手磁贴与完整管理页

## 工作依据与规则传递声明
- 当前角色: 架构师
- 工作依据文档: `doc/ai-coding-unified-rules.md`
- 适用规则: AI协作统一规则 单一规范
- 规则遵循声明: 必须遵守本规则。
- 协作传递要求: 后续接手者与协作者必须遵守同一规则。

- 日期: 2026-04-21
- 备注: 用户要求在 `/mng` 增加 TG 助手磁贴，点击后进入新页面，并对齐现有 TG 助手主要能力：账号 登录 任务 Bot。
- 风险:
  - TG 页面交互较复杂，若一次性拼装单文件脚本，后续维护成本较高。
  - TG 能力依赖外部 Telegram 网络，运行时错误概率高于本地功能，前端状态提示必须完整。
  - 若 `/mng/api/tg/*` 与既有 `admin.tg.*` 字段映射不一致，会导致联调失败。
- 遗留事项:
  - 后续可将 TG 页面拆分为模块化静态资源。
  - 后续补充更多自动化测试覆盖 TG 失败分支。
- 进度状态: 已完成
- 完成情况: 已完成需求拆解、能力映射、路由接口与页面信息架构设计。
- 检查表:
  - [x] 已记录工作依据与规则传递声明
  - [x] 已确认字符集基线并沿用
  - [x] 已完成接口映射与执行包拆解
  - [x] 已完成编码测试映射与验收口径
- 跟踪表状态: 待实现
- 结论记录: 采用系统设置同构模式，TG 助手在 `/mng` 下走 HTTP + Cookie + `mng` 鉴权，不直接走 websocket。

## 字符集编码基线
- 字符集类型: 新文件 UTF-8 无 BOM
- BOM策略: 新文件不使用 BOM
- 换行符规则: 沿用目标文件现有风格，不做历史文件统一迁移
- 跨平台兼容要求: 保持现有工具链可构建可运行
- 历史文件迁移策略: 原文件不强制改编码，仅在变更时延续原风格

## 统一需求主文档
- RQ-MNG-018: `/mng/panel` 新增 TG 助手磁贴。
- RQ-MNG-019: 点击 TG 助手磁贴进入 `/mng/tg` 新页面。
- RQ-MNG-020: `/mng/tg` 页面提供完整管理能力，覆盖账号 登录 任务 Bot。
- RQ-MNG-021: 新增 `/mng/api/tg/*` 薄封装接口并复用现有 TG 逻辑。
- RQ-MNG-022: TG 页面路由与接口纳入 `/mng` 会话鉴权与测试覆盖。

## 关键选型与取舍

### 选型1 通信模型
- 方案A 前端直连 `/api/admin/ws` 的 `admin.tg.*`
- 方案B 新增 `/mng/api/tg/*`，内部复用现有函数
- 结论: 选择方案B
- 依据: 与系统设置和 Cloudflare 同构，统一 `/mng` 页面的 HTTP + Cookie 模式，降低前端复杂度。

### 选型2 页面能力范围
- 方案A 仅展示账号列表
- 方案B 完整管理版，覆盖账号 登录 任务 Bot
- 结论: 选择方案B
- 依据: 用户明确要求完整管理版。

## 总体设计

```mermaid
flowchart TD
  A[访问 /mng/panel] --> B[点击 TG 助手磁贴]
  B --> C[进入 /mng/tg]
  C --> D[校验 /mng/api/session]
  D -->|未登录| E[跳转 /mng]
  D -->|已登录| F[加载 TG 基础数据]
  F --> G[账号与登录操作]
  F --> H[任务管理操作]
  F --> I[Bot 管理操作]
  G --> J[/mng/api/tg/accounts 与 login 相关接口]
  H --> K[/mng/api/tg/schedule 与 targets 接口]
  I --> L[/mng/api/tg/bot 接口]
```

## 单元设计

### U-MNG-TG-01 磁贴与页面接入
- 文件:
  - `probe_controller/internal/core/mng_pages/panel.html`
  - `probe_controller/internal/core/mng_pages/tg.html` 新增
  - `probe_controller/internal/core/mng_pages.go`
- 内容:
  - 新增 TG 助手磁贴
  - 新增 TG 页面并 embed

### U-MNG-TG-02 路由与鉴权接入
- 文件:
  - `probe_controller/internal/core/server.go`
  - `probe_controller/internal/core/mng_tg_handlers.go` 新增
- 内容:
  - 新增 `/mng/tg` 页面路由
  - 新增 `/mng/api/tg/*` 接口路由
  - 统一使用 `mngAuthRequiredMiddleware`

### U-MNG-TG-03 TG 薄封装接口
- 文件:
  - `probe_controller/internal/core/mng_tg_handlers.go` 新增
- 内容:
  - API Key: `/mng/api/tg/api/get` `/mng/api/tg/api/set`
  - 账号: `/mng/api/tg/accounts/list` `/refresh` `/add` `/remove`
  - 登录: `/mng/api/tg/account/send_code` `/sign_in` `/logout`
  - Bot: `/mng/api/tg/bot/get` `/set` `/test_send`
  - 目标: `/mng/api/tg/targets/list` `/refresh`
  - 任务: `/mng/api/tg/schedule/list` `/add` `/update` `/remove` `/set_enabled` `/send_now` `/history` `/pending`
  - 上述接口仅做参数解析与结果透传，复用现有 TG 函数

### U-MNG-TG-04 `/mng/tg` 完整管理页面
- 文件:
  - `probe_controller/internal/core/mng_pages/tg.html` 新增
- 内容:
  - 结构对齐现有 TG 管理主要能力
  - 主区域与子视图覆盖:
    - 共享 API Key 配置
    - 账号列表与状态刷新
    - 登录流程 发送验证码 完成登录 登出 删除
    - 任务管理 增改删 启停 立即发送 历史 待执行队列
    - Bot 配置 测试发送
  - 统一状态框与错误展示

### U-MNG-TG-05 测试与验收
- 文件:
  - `probe_controller/tests/mng_auth_test.go`
- 内容:
  - 未登录访问 `/mng/tg` 重定向
  - 未登录访问 `/mng/api/tg/*` 返回401
  - 已登录访问 `/mng/panel` 含 TG 助手磁贴
  - 已登录访问 `/mng/tg` 含 TG 核心文案与模块标识
  - 已登录访问至少一个 `/mng/api/tg/*` 基础接口成功

## 接口定义清单
- 页面:
  - `GET /mng/tg`
- 接口分组:
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

## 执行单元包拆分
- PKG-MNG-TG-01: 新增 TG 磁贴与页面 embed
- PKG-MNG-TG-02: 新增 `/mng/tg` 页面路由与鉴权
- PKG-MNG-TG-03: 新增 `/mng/api/tg/*` 薄封装接口
- PKG-MNG-TG-04: 实现 TG 完整管理页前端交互
- PKG-MNG-TG-05: 更新 `/mng` 测试覆盖与回归验证

## 编码测试映射
| 需求编号 | 执行单元包 | 验证口径 |
|---|---|---|
| RQ-MNG-018 | PKG-MNG-TG-01 | `/mng/panel` 出现 TG 助手磁贴 |
| RQ-MNG-019 | PKG-MNG-TG-01 PKG-MNG-TG-02 | 磁贴跳转 `/mng/tg` 且页面可访问 |
| RQ-MNG-020 | PKG-MNG-TG-04 | 页面具备账号 登录 任务 Bot 四类主要能力 |
| RQ-MNG-021 | PKG-MNG-TG-03 | `/mng/api/tg/*` 可复用 TG 现有逻辑并联通 |
| RQ-MNG-022 | PKG-MNG-TG-02 PKG-MNG-TG-05 | 鉴权行为与测试覆盖满足要求 |

## 需求跟踪表更新说明
- 本次新增 RQ-MNG-018 到 RQ-MNG-022。
- 状态初始化为 待实现，责任角色为编码工程师。
