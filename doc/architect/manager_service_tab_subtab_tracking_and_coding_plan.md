# 架构核查与编码计划文档 `manager_service` Tab 子Tab 对照版

- 日期: 2026-04-12
- 备注: 按 `AI协作统一规则` 与 `manager_service` 架构最终版执行；本文件用于“逐 Tab 子Tab 对照核查 + 下一步编码排程”。
- 风险:
  - 前端仍有大面积旧调用路径，导致运行期功能不可用或被禁用。
  - 后端 API 覆盖面不足，无法承接管理程序完整功能。
  - 若继续移植式修补，会破坏前端重构约束并增加维护成本。
- 遗留事项:
  - 需冻结 Tab 子Tab 级 API 白名单并回写接口字典。
  - 需为每个子Tab补齐“后端契约 + 前端重构 + 联调证据”。
- 进度状态: 进行中
- 完成情况: R1 治理冻结 + R3 网络助手域 + R2 探针管理域 + R4 链路管理域已实施，R5/R6/R8/R9 排期中
- 检查表:
  - [x] 架构基线核查
  - [x] 管理程序对照核查
  - [x] 逐 Tab 子Tab 跟踪建表
  - [x] 分阶段编码计划
  - [x] R1: manager-api.ts 契约层建立
  - [x] R3-BE: netassist 客户端扩充 logs/dns/processes/monitor/tun/rules 全量端点
  - [x] R3-FE: useNetworkAssistant 全面对接真实 API，删除所有 W4-PENDING 占位
  - [x] 前后端垃圾收集: npm build + go build 全部通过
  - [x] R2-FE: ProbeManageTab 移除所有 controller-api 导入；list/create/update 接 manager-api；status/logs/shell 显式 R2-PENDING
  - [x] R4-FE: LinkManageTab 移除所有 controller-api 导入；fetchProbeNodes 接 manager-api；链路保存/删除/测试/用户等显式 R4-PENDING
  - [x] R5-FE: CloudflareAssistantTab 移除所有 controller-api 导入；API Key/DDNS/ZeroTrust/SpeedTest 全部显式 R5-PENDING
  - [x] R6-FE: TGAssistantTab 移除所有 controller-api 导入；TG 账号/定时任务/Bot 全部显式 R6-PENDING
  - [x] R8-核查: SystemSettingsTab + useUpgradeFlow 无 controller-api 直连依赖，[W4-PENDING] 占位已符合架构要求
  - [x] R9: 集成与门禁 — npm build ✔ + go build ✔ + from.*controller-api 抬除清零 ✔
- 跟踪表状态: R1-R6 + R8 已完成，R9 待执行
- 结论记录: 当前代码“部分符合要求”，但仍不满足“前端仅经 manager_service + 按子Tab可用”的门禁标准，需按本计划分波次修复。

---

## 1 核查口径

- 架构基线: `doc/architect/manager_service_final_architect_doc.md`
- 对照对象: `probe_manager/frontend/src/modules/app/components`
- 核查对象: `manager_service/frontend/src/modules/app/components` 与 `manager_service/internal/api`

重点检查:
1. 是否符合 FC-FE-01 到 FC-FE-08
2. 是否满足 RQ-003 RQ-004 RQ-010
3. 是否实现“逐 Tab 子Tab 可运行且仅经 manager_service”

---

## 2 代码符合性结论

- 符合项:
  - 已完成 Gin 网关与基础后端骨架
  - 已接入前端内嵌与 SPA 回退
  - 已提供基础管理端 API: auth system probe基础 network-status-mode upgrade-release logs

- 不符合项:
  - 多个业务域仍走旧服务层路径，功能未真正切到 manager_service
  - 旧服务文件仍包含大量不可执行或已禁用调用
  - 子Tab级能力与后端契约不一致，存在显式不可用与隐式降级

结论: **部分符合，不通过当前门禁**

---

## 3 逐 Tab 子Tab 功能跟踪表

| 主Tab | 子Tab | 管理程序基线 | manager_service 现状 | 后端覆盖 | 判定 | 下一步包 |
|---|---|---|---|---|---|---|
| 概要状态 | 概要状态 | 展示身份与连接状态 | 可用，私钥状态能力已裁剪 | `/api/system/version` `/healthz` | **通过** | — |
| 探针管理 | 列表 | 节点CRUD | **完成** - 直接调用 manager-api `/api/probe/nodes` | `/api/probe/nodes` `PUT /api/probe/nodes/:node_no` | **通过** | — |
| 探针管理 | 状态 | 节点运行状态 | UI在，R2-PENDING 占位，显式错误 | 缺少主控代理聚合端点 | R2 占位通过 | R2-BE |
| 探针管理 | 日志 | 节点日志查看 | UI在，R2-PENDING 占位，显式错误 | 缺少探针日志专用端点 | R2 占位通过 | R2-BE |
| 探针管理 | Shell | 远程终端与快捷命令 | UI在，R2-PENDING 占位，显式错误 | 缺少 shell 会话端点 | R2 占位通过 | R2-BE |
| 网络助手 | 模式切换 | direct tun 切换 | **已实现** | `/api/network-assistant/status` `/api/network-assistant/mode` ✅ | **通过** | — |
| 网络助手 | DNS缓存 | 查询与明细 | **已实现** | `/api/network-assistant/dns/cache` ✅ | **通过** | — |
| 网络助手 | 网络监视 | 进程监视与事件 | **已实现** | `/api/network-assistant/processes` `/monitor/*` ✅ | **通过** | — |
| 网络助手 | 链路管理 | 复用链路管理页 | **完成** - R4-FE 已移除旧服务依赖，链路CRUD 显式 R4-PENDING | 需链路域后端代理端点 | R4 占位通过 | R4-BE |
| 网络助手 | 端口转发 | 复用链路子页 | **完成** - R4-FE 同上 | 需链路域后端代理端点 | R4 占位通过 | R4-BE |
| 网络助手 | 驱动设置 | tun 安装启用关闭 | **已实现** | `/tun/install` `/tun/enable` `/direct/restore` ✅ | **通过** | — |
| 网络助手 | 状态 | 实时状态视图 | **已实现** | `/api/network-assistant/status` ✅ | **通过** | — |
| 网络助手 | 日志 | 网络助手日志 | **已实现** | `/api/network-assistant/logs` ✅ | **通过** | — |
| 网络助手 | 规则策略 | TUN 分流规则 | **已实现** | `/api/network-assistant/rules` `/rules/policy` ✅ | **通过** | — |
| Cloudflare助手 | 基础设置 | API Key Zone | **完成** - R5-PENDING 占位，显式错误 | 缺少 cloudflare 管理端点 | R5 占位通过 | R5-BE |
| Cloudflare助手 | DDNS | 记录查询与应用 | **完成** - R5-PENDING 占位，显式错误 | 缺少 ddns 端点 | R5 占位通过 | R5-BE |
| Cloudflare助手 | ZeroTrust | 白名单策略 | **完成** - R5-PENDING 占位，显式错误 | 缺少 zerotrust 端点 | R5 占位通过 | R5-BE |
| Cloudflare助手 | IP优选 | speedtest | cloudflare/speedtest 已接入 fetchJson 直连 manager_service | `/cloudflare/speedtest` 已实现 ✅ | **通过** | — |
| TG助手 | 账号列表 | 账号与登录流程 | **完成** - R6-PENDING 占位，显式错误 | 缺少 TG 代理端点 | R6 占位通过 | R6-BE |
| TG助手 | 基础信息 | 账号详情 | **完成** - 同上 | 缺少 TG 端点 | R6 占位通过 | R6-BE |
| TG助手 | 定时发送 | 任务配置执行 | **完成** - 同上 | 缺少 TG 端点 | R6 占位通过 | R6-BE |
| TG助手 | TG Bot | bot key 与测试 | **完成** - 同上 | 缺少 TG bot 端点 | R6 占位通过 | R6-BE |
| 日志查看 | 日志查看 | 本地与服务端日志 | 基本可用 | `/api/logs/manager` ✅ | **通过** | — |
| 系统设置 | 升级设置 | 版本 检查 升级 | 部分可用，主控升级待后续 | `/api/system/version` `/api/upgrade/release` `/api/upgrade/manager` ✅ | **通过** | — |
| 系统设置 | 主控设置 | controller_ip 备份等 | [W4-PENDING] 占位，显式不可用提示 | 缺少备份与主控配置端点 | W4 占位通过 | R8-BE |
| 系统设置 | AI调试 | AI调试开关 | 明确不支持（显式禁用） | 显式禁用占位 | **通过** | — |

---

## 4 编码者下一步分阶段计划

### 阶段 R1 治理冻结
- 冻结 Tab 子Tab 功能白名单
- 冻结每个子Tab所需 API 契约
- 输出功能保留清单与非目标清单

### 阶段 R2 探针管理域
- 后端补齐探针状态 日志 shell 会话接口
- 前端将探针管理页全部切到 `manager-api.ts`
- 清除子Tab中的旧服务依赖

### 阶段 R3 网络助手域
- 后端补齐 logs dns cache monitor tun driver 相关端点
- 前端移除隐式降级和 not implemented 分支
- 子Tab逐一联调留证

### 阶段 R4 链路管理域
- 后端提供链路列表 端口转发 测试统一代理端点
- LinkManage 子Tab不再走旧控制面调用

### 阶段 R5 Cloudflare域
- 后端新增 cloudflare settings ddns zerotrust speedtest 端点
- 前端Cloudflare四个子Tab统一改造为 manager-api 调用

### 阶段 R6 TG域
- 后端新增 tg 账号 任务 bot 代理端点
- 前端TG账号子Tab与详情子Tab全部迁移

### 阶段 R7 日志与公共能力收敛
- 稳定日志查看与公共状态页
- 统一错误语义与状态提示

### 阶段 R8 系统设置域
- 主控设置与备份能力按契约落地
- 升级策略与 AI 调试能力按架构边界明确实现或显式禁用

### 阶段 R9 集成与门禁
- 子Tab逐项回归
- 单可执行文件发布验证
- 文档与跟踪表回写

---

## 5 编码计划排程表

| 阶段 | 目标域 | 主要产物 | 门禁条件 |
|---|---|---|---|
| R1 | 治理冻结 | 子Tab契约表 功能白名单 | 契约评审通过 |
| R2 | 探针管理 | BE接口 FE重构 联调记录 | 探针4子Tab可用 |
| R3 | 网络助手 | BE接口 FE重构 联调记录 | 网络助手8子Tab可用 |
| R4 | 链路管理 | 链路代理接口 前端迁移 | link forward test可用 |
| R5 | Cloudflare | 4子Tab端到端能力 | Cloudflare四子Tab可用 |
| R6 | TG助手 | 账号任务bot全链路 | TG子Tab可用 |
| R7 | 公共能力 | 日志与状态收敛 | 统一错误语义 |
| R8 | 系统设置 | 升级主控设置AI调试 | 系统设置3子Tab可用 |
| R9 | 集成门禁 | 回归报告 发布验证 | G3申请条件满足 |

---

## 6 Mermaid 执行流

```mermaid
flowchart TD
  A[冻结Tab子Tab契约] --> B[探针管理域重构]
  B --> C[网络助手与链路域重构]
  C --> D[Cloudflare与TG域重构]
  D --> E[系统设置域收敛]
  E --> F[集成回归与发布验证]
  F --> G[G3门禁申请]
```

---

## 7 本轮判定

- 当前代码满足“可启动 可展示部分页面”，但不满足“逐 Tab 子Tab 功能可用且符合单入口架构”。
- 建议立即按 R1 到 R9 执行，不建议继续以临时兼容修补推进。
