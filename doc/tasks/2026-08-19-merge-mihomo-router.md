# 任务：合并 Mihomo 出口与 Linux 软路由产品

- 任务标识：`merge-mihomo-router-20260819`
- 状态：`已完成`
- 创建时间：`2026-08-19 22:21 +08:00`
- 更新时间：`2026-08-19 22:58 +08:00`
- 用户原始需求：取消单独的 Mihomo，功能合并入 Linux router。
- 用户最新指令：取消旧 Mihomo 候选，并卸载 netcup2o 上的 Mihomo 出口探针。
- 启用方式：任务跨探针运行时、主控、安装升级、发布工作流与测试，属于明确长任务条件。

## 一、需求定义

### 1.1 背景与问题

当前 `mihomo_exit` 与 `linux_router` 是两个互斥 Go build tag、两个节点类型和两套发布/安装入口。软路由若要同时提供局域网接入和 Mihomo 代理出口，需要重复产品形态；两套运行时还因同名产品生命周期函数无法共同编译。

### 1.2 目标

只保留 Linux router 作为新建、安装和发布的软路由产品，并让它同时具备旁路由接入与 Mihomo 代理出口能力；旧 `mihomo_exit` 节点保留滚动迁移所需的读取和运行兼容。

### 1.3 范围、非范围与约束

- 范围内：探针产品运行时、虚拟路由出口传输、主控配置下发、节点管理与线路界面、Alpine 安装脚本、在线升级伴随程序、GitHub 发布产物、自动化测试，以及用户明确要求的 netcup2o 旧出口探针卸载。
- 范围外：本次不发布新版本、不部署新二进制、不主动改写主控中已有节点类型、不删除已有 GitHub 历史版本资产。
- 约束：新产品仅使用 `linux_router`；旧 `mihomo_exit` 可继续连接和运行；Linux router 支持 amd64/arm64；本地软路由配置仍由本机管理，主控只下发 Mihomo 出口配置；用户已有修改不得回退。

### 1.4 需求与验收标准

| 需求编号 | 需求描述 | 验收标准 | 优先级 | 状态 | 来源或最新变更 |
|---|---|---|---|---|---|
| REQ-001 | Linux router 同时承载软路由和 Mihomo 出口运行时 | `linux_router` 构建成功；本地路由启动不依赖 Mihomo 配置；收到出口配置后可启动 Mihomo 并报告状态 | P0 | `已完成` | 用户最新指令 |
| REQ-002 | 取消新的独立 Mihomo 产品入口和候选 | 新建、安装、升级和二次分流候选不再提供 `mihomo_exit`；主控仍能认证和同步旧记录 | P0 | `已完成` | 用户补充“可以不要这个候选了” |
| REQ-003 | Linux router 安装与升级携带匹配架构的 Mihomo | amd64/arm64 安装脚本和在线升级通过各自清单获取、校验并替换 Mihomo | P0 | `已完成` | 合并后完整功能要求 |
| REQ-004 | 主控向 Linux router 下发 Mihomo 出口配置 | 线路配置只选择 Linux router，路由同步响应包含其 SpecialExit 快照 | P0 | `已完成` | 合并后数据链要求 |
| REQ-005 | 发布只生成 Linux router 产品资产且验证覆盖兼容边界 | 工作流不再构建独立 exit node；相关 Go/脚本/工作流契约测试通过 | P1 | `已完成` | 用户要求取消单独产品 |
| REQ-006 | 卸载 netcup2o 上的旧 Mihomo 出口探针 | service、环境文件、进程和安装目录消失；主控与普通探针保持 active | P0 | `已完成` | 用户追加指令 |

## 二、总体架构

### 2.1 当前现状

`probe_node` 通过互斥 build tag 生成普通探针、Mihomo 出口探针和 Linux router。Mihomo 与 router 文件各自定义产品生命周期函数，不能同时编译；主控仅向 `mihomo_exit` 下发 SpecialExit，发布工作流也分别生成两类程序。

### 2.2 目标架构

`linux_router` 产品组合两个内部运行单元：Linux router 数据面始终可独立启动，Mihomo 运行单元按主控 SpecialExit 配置启停。旧 `mihomo_exit` build tag 通过薄包装继续调用同一 Mihomo 内部运行单元。主控写接口只暴露 Linux router，新旧两种在线节点均可读取出口配置以支持迁移。

### 2.3 关键模块与职责

| 模块 | 当前职责 | 目标职责 | 输入 | 输出 | 依赖 |
|---|---|---|---|---|---|
| 探针产品运行时 | 两类产品互斥生命周期 | Router 组合 Router + 可选 Mihomo；旧 Exit 使用 Mihomo 包装 | 启动、RouteConfig | 运行状态、出口报告 | Mihomo 二进制、TUN |
| 主控路由配置 | 仅给 Mihomo 节点下发出口快照 | 给 Linux router 和旧 Mihomo 节点下发 | 节点类型、线路配置 | ProbeRouteConfig | 节点注册表 |
| 安装与升级 | Router 不带 Mihomo；Exit 仅 amd64 | Router 按架构成对安装/升级 | Release 清单 | 两个已校验程序 | GitHub release、代理下载 |
| 管理界面 | 可创建独立 Mihomo | 仅创建 Linux router，旧节点只读兼容 | 节点数据 | 表单与状态 | 管理 API |

### 2.4 关键流程

| 流程 | 发起方 | 处理方 | 数据或状态变化 | 失败处理 | 关联需求 |
|---|---|---|---|---|---|
| Router 启动 | systemd/OpenRC | 探针产品运行时 | 先启动路由数据面；有缓存出口快照时再启动 Mihomo | Mihomo 缺配置不阻止软路由启动 | REQ-001 |
| 出口配置同步 | 探针 | 主控 route handler | Linux router 获取 SpecialExit 快照并应用 | 无配置时停止 Mihomo，仅保留路由 | REQ-001, REQ-004 |
| 安装/升级 | 用户或探针 | 安装脚本/升级器 | 按架构校验并替换 Router 与 Mihomo | 任一校验失败不提交替换 | REQ-003 |
| 旧节点兼容 | 旧 Exit 探针 | 主控/共享 Mihomo 运行时 | 继续按原 kind 获取配置和上报 | 不提供新建或新发布入口 | REQ-002, REQ-005 |

### 2.5 接口记录

| 接口编号 | 接口名称 | 调用方 | 提供方 | 输入、输出与错误契约 | 实现位置 | 兼容要求 | 关联需求、任务与测试 | 状态与证据 |
|---|---|---|---|---|---|---|---|---|
| IF-001 | Probe route config | 探针 | 主控 | `expected_node_kind=linux_router` 时可返回 `special_exit`；无配置为 nil | `probe_route_handlers.go` | 旧 `mihomo_exit` 保持同步兼容 | REQ-001/004, TASK-002/003, TEST-002 | `已实现并测试` |
| IF-002 | Router 配对发布清单 | 安装器、升级器 | GitHub release | 每个架构包含 Router 程序资产及 Mihomo URL/SHA256 | `release.yml` 与安装/升级代码 | amd64/arm64 各自严格匹配 | REQ-003/005, TASK-004, TEST-004 | `已实现并测试` |
| IF-003 | 节点类型写入契约 | 管理界面/API | 主控 | 新写入不再创建 `mihomo_exit`；读取旧值不报错 | probe 管理 handler/UI | 旧记录可在线和查看但不能安装/升级/候选 | REQ-002, TASK-003, TEST-003 | `已实现并测试` |

### 2.6 架构决策引用

| 决策编号 | 对架构的影响 | 相关模块或接口 |
|---|---|---|
| DEC-001 | Linux router 成为唯一新产品并组合两个运行单元 | 探针生命周期、IF-001 |
| DEC-002 | 旧 Mihomo 类型保留读取和运行兼容，不继续独立发布 | 节点管理、release workflow、IF-003 |
| DEC-003 | Router 每个架构使用独立配对清单 | 安装升级、IF-002 |

## 三、单元设计

### 3.1 受影响单元

| 单元编号 | 文件或位置 | 职责 | 输入 | 输出 | 依赖 | 关联需求 |
|---|---|---|---|---|---|---|
| UNIT-001 | `probe_node/probe_special_exit_mihomo.go` 及产品包装 | 共享 Mihomo 内部生命周期 | RouteConfig、nodeID | 进程与报告 | Mihomo binary | REQ-001 |
| UNIT-002 | `probe_node/probe_linux_router.go` | 组合 Router 与 Mihomo | 启停、RouteConfig | 双运行时状态 | UNIT-001 | REQ-001 |
| UNIT-003 | 主控 route/special-exit/probe 管理 | 下发配置并收敛类型 | 节点 kind、线路 | 快照、UI/API | registry | REQ-002/004 |
| UNIT-004 | 安装、升级和 release workflow | 产出并消费成对资产 | OS/arch/release | 可运行安装 | GitHub、SHA256 | REQ-003/005 |

### 3.2 处理与异常规则

| 单元编号 | 正常处理规则 | 异常处理规则 | 兼容要求 | 验证方式 |
|---|---|---|---|---|
| UNIT-001 | 有出口快照时生成配置并启动 Mihomo | nil 快照停止 Mihomo；二进制或配置错误只影响出口单元 | 旧 build tag 使用相同逻辑 | Go 单测、双 tag 构建 |
| UNIT-002 | Router 先启动自身，再尝试 Mihomo | Mihomo 未配置不让 Router 启动失败 | Router 本地配置保持本地优先 | Go 单测与构建 |
| UNIT-003 | Router/旧 Exit 均可获取 SpecialExit | 新提交 `mihomo_exit` 拒绝或归并为 router | 旧值读取不变 | handler/UI 契约测试 |
| UNIT-004 | 安装和升级按 GOARCH 选择清单 | 下载、SHA 或版本校验失败不替换当前程序 | 旧 Exit 升级代码可编译 | 脚本测试与 workflow 测试 |

## 四、执行任务

### 4.1 当前交接

- 当前阶段：已完成
- 当前计划步骤：TASK-005 完成门禁
- 当前门禁：完成门禁通过
- 最近完成检查点：Controller、普通探针、Linux router、旧 Mihomo tagged 测试顺序执行全部通过；两架构交叉构建通过；netcup2o 旧出口服务已卸载并复核。
- 工作区状态：本任务代码、文档与删除项尚未提交；无用户并发修改。
- 下一步唯一动作：无。
- 恢复时先读取：本账本和 `git status --short`。

### 4.2 任务计划

| 任务编号 | 工作内容 | 状态 | 关联需求 | 文件或接口范围 | 完成条件 |
|---|---|---|---|---|---|
| TASK-001 | 建立需求账本和实现边界 | `已完成` | REQ-001~005 | 本文件 | 准备门禁通过 |
| TASK-002 | 合并探针运行时与出口传输 | `已完成` | REQ-001 | UNIT-001/002 | Router 构建并支持可选 Mihomo |
| TASK-003 | 收敛主控配置、节点类型和界面 | `已完成` | REQ-002/004 | UNIT-003、IF-001/003 | Router 可配置出口且无新 Mihomo 入口或候选 |
| TASK-004 | 合并安装、升级与发布资产 | `已完成` | REQ-003/005 | UNIT-004、IF-002 | 两架构配对资产链闭合，无独立 Exit job |
| TASK-005 | 全量验证与完成门禁 | `已完成` | REQ-001~005 | tests/build/diff | 计划测试通过，未发布缺口准确记录 |
| TASK-006 | 卸载 netcup2o 旧出口探针 | `已完成` | REQ-006 | 远端 `probe_exit_node.service` 与专属目录 | 旧服务/进程/目录消失，其他服务正常 |

### 4.3 变更记录

| 文件、配置或接口 | 变更内容 | 原因 | 关联需求与任务 | 验证方式 | 回滚引用 |
|---|---|---|---|---|---|
| `doc/tasks/2026-08-19-merge-mihomo-router.md` | 新增长任务账本 | 保持跨模块追踪 | 全部 / TASK-001 | 人工核对门禁 | RB-001 |
| 探针 Mihomo/Router runtime 与出口 transport | 共享 Mihomo 内部生命周期，Router 可选组合并保持直连/失败关闭边界 | 合并产品能力 | REQ-001 / TASK-002 | tagged test、专项 runtime test | RB-001 |
| 主控 registry、route handler 与管理页面 | 仅 Router 可新建、安装、升级和作为候选；Router 获取出口快照 | 取消独立产品与候选 | REQ-002/004 / TASK-003 | Controller 全量测试 | RB-001 |
| Router installer、upgrade companion、release workflow | 两架构配对清单与 Mihomo；删除独立脚本、Docker 和 release jobs/assets | 单一发布产品 | REQ-003/005 / TASK-004 | bash、workflow test、cross build | RB-001/002 |
| netcup2o 旧 Exit 服务 | 停用并删除 unit、环境文件和专属目录 | 用户明确卸载 | REQ-006 / TASK-006 | SSH 复核 | 无自动恢复；需按历史版本重装 |

## 五、测试与验证

### 5.1 测试计划与结果

| 测试编号 | 测试目标 | 关联需求与任务 | 方法或准确命令 | 预期结果 | 实际结果 | 状态 | 证据 |
|---|---|---|---|---|---|---|---|
| TEST-001 | Router 和旧 Exit 产品构建 | REQ-001/002, TASK-002 | 在 `probe_node` 顺序执行 `go test -tags linux_router ./...`、`go test -tags mihomo_exit ./...` | 均通过 | 两套均通过 | `通过` | Go test exit 0 |
| TEST-002 | Router 获取并应用出口配置 | REQ-001/004, TASK-002/003 | `TestProbeRouteConfigSendsSpecialExitSnapshotToLinuxRouter`、`TestLinuxRouterStartsWithoutMihomoAndDoesNotLeakConfiguredExit` | Router 有/无快照行为正确 | handler 下发、无快照直连、已配置失败关闭均通过 | `通过` | Controller/Router test suite |
| TEST-003 | 新 UI/API 无独立 Mihomo，旧值兼容 | REQ-002, TASK-003 | 管理 handler 与模板契约测试 | 新入口和候选消失，旧记录可读 | 创建拒绝、候选过滤、旧安装升级拒绝、旧同步兼容通过 | `通过` | Controller test suite |
| TEST-004 | 两架构安装升级清单闭环 | REQ-003/005, TASK-004 | workflow/installer tests、`bash -n`、两架构 `go build -tags linux_router` | amd64/arm64 资产、SHA、清单一致 | 契约、脚本语法和两架构构建通过 | `通过` | Go test、bash exit 0、build exit 0 |
| TEST-005 | 回归与差异检查 | REQ-001~005, TASK-005 | 两模块 `go test ./...`、tagged tests、`git diff --check` | 无回归和格式错误 | 顺序执行全部通过；diff check 无错误 | `通过` | 最终验证命令 exit 0 |
| TEST-006 | netcup2o 卸载复核 | REQ-006, TASK-006 | SSH 检查 unit、进程、目录及保留服务 | 旧出口消失，其他服务 active | unit/进程为空、目录 absent；controller/node active | `通过` | 远端只读复核输出 |

### 5.2 未执行测试

| 测试编号 | 未执行原因 | 影响 | 替代证据 | 后续动作 |
|---|---|---|---|---|
| TEST-LIVE-001 | 本次未获发布/部署授权 | 不验证真实 Alpine 在线安装和流量出口 | 本地构建、单测、脚本契约 | 用户要求发布时走发布与 Alpine 验证技能 |

## 六、端到端追踪

| 需求编号 | 验收标准 | 架构或单元 | 任务编号 | 文件、配置或接口 | 测试编号 | 结果与证据 | 状态 |
|---|---|---|---|---|---|---|---|
| REQ-001 | Router 双运行时且可独立启动 | UNIT-001/002 | TASK-002 | 探针运行时 | TEST-001/002 | tagged test 与 fail-closed test 通过 | `已完成` |
| REQ-002 | 无新独立 Mihomo 入口或候选，旧值兼容 | UNIT-003, IF-003 | TASK-003 | 主控管理/API/UI | TEST-003 | 创建/候选/安装/升级契约通过 | `已完成` |
| REQ-003 | 两架构配对安装升级 | UNIT-004, IF-002 | TASK-004 | installer/upgrader/workflow | TEST-004 | 两清单、脚本语法与交叉构建通过 | `已完成` |
| REQ-004 | Router 收到出口快照 | UNIT-003, IF-001 | TASK-003 | route handler | TEST-002 | handler 端到端测试通过 | `已完成` |
| REQ-005 | 仅发布 Router 并有回归证据 | UNIT-004 | TASK-004/005 | workflow/tests | TEST-004/005 | workflow 无独立 job/assets，回归通过 | `已完成` |
| REQ-006 | netcup2o 旧出口已卸载 | 远端 systemd/目录 | TASK-006 | netcup2o | TEST-006 | 旧服务消失且保留服务 active | `已完成` |

## 七、决策与冲突记录

### 7.1 决策记录

| 决策编号 | 触发原因 | 采用方案 | 理由与证据 | 替代方案 | 影响范围 | 替代关系 | 状态 |
|---|---|---|---|---|---|---|---|
| DEC-001 | 用户要求取消单独 Mihomo | Router 组合 Router + 可选 Mihomo | 功能归一且 Router 在无出口配置时仍可工作 | 两个二进制共同安装 | 运行时、配置 | 新决策 | `有效` |
| DEC-002 | 已有 Exit 节点不能瞬间失效 | 保留旧类型读取/运行，不再新建和发布 | 支持滚动迁移且符合取消独立产品 | 立即删除全部兼容代码 | 主控、旧探针 | 新决策 | `有效` |
| DEC-003 | Router 支持两种 CPU 架构 | 每架构独立清单绑定对应 Mihomo | 防止跨架构错误和保持 SHA 可验证 | 单清单含多架构数组 | 发布、安装升级 | 新决策 | `有效` |

### 7.2 冲突记录

无。

## 八、缺陷记录

无。

## 九、回滚方案

| 变更或风险 | 触发条件 | 回滚步骤 | 数据与兼容影响 | 回滚后验证 | 状态 |
|---|---|---|---|---|---|
| RB-001 代码与工作流合并 | 构建/测试失败或 Router 功能回归 | 在未发布前回退本任务提交；恢复独立 build tags/jobs/UI 选项 | 本任务不迁移持久数据；旧记录未改写 | 原三种 tagged build 与现有测试通过 | `可用` |
| RB-002 发布后伴随程序异常 | Router 升级后 Mihomo 不可用 | 在线升级回退至上一 Router release；保留当前二进制事务备份 | Router 本地配置与旧节点类型不变 | Router 健康及代理出口检查 | `待实现验证` |

## 十、已验证事实

| 事实编号 | 已验证事实 | 证据 | 对任务的影响 |
|---|---|---|---|
| FACT-001 | 当前工作区基线干净，分支为 `mapledev` | `git status --short --branch` | 可准确归因本任务改动 |
| FACT-002 | `mihomo_exit` 与 `linux_router` 同时构建会因产品生命周期符号重复失败 | 现有 tagged 源码及组合构建错误 | 必须拆出共享内部函数和产品包装 |
| FACT-003 | 主控目前仅对 `mihomo_exit` 下发 SpecialExit | `probe_route_handlers.go` 条件 | IF-001 必须扩展 Router |
| FACT-004 | 官方 Mihomo v1.19.29 提供 amd64 compatible 与 arm64 资产 | GitHub release 元数据 | 可为两架构生成严格清单 |
| FACT-005 | Linux router 的 amd64 与 arm64 交叉构建均成功 | 两条 `go build -tags linux_router` exit 0 | 发布矩阵可生成两架构程序 |
| FACT-006 | netcup2o 已无旧 Exit unit、进程和目录，主控与普通探针 active | SSH 卸载后复核 | 远端清理完成且未误停保留服务 |

## 十一、风险与阻塞

| 编号 | 类型 | 描述与证据 | 影响 | 缓解或所需动作 | 状态 |
|---|---|---|---|---|---|
| RISK-001 | 兼容 | 最新 release 不再含独立 Exit 资产后，旧节点不能继续跟随最新版升级 | 旧节点需迁移为 Router | 保留运行/读取兼容并在重装时选择 Router；历史 release 保留 | `已缓解` |
| RISK-002 | 资源 | Router 同时运行 TUN 与 Mihomo，会增加内存 | 低内存软路由可能 OOM | Mihomo 仅有出口配置时运行，失败不扩大为 Router 启动失败 | `已缓解，待真实负载观察` |
| RISK-003 | 架构 | arm64 伴随资产和清单错误会导致无法安装 | arm64 Router 出口不可用 | 架构独立清单、SHA、manifest test 与 arm64 交叉构建 | `已缓解` |

## 十二、质量门禁

### 12.1 准备门禁

| 检查项 | 结论 | 证据或条件 |
|---|---|---|
| 最新目标、范围、非范围和约束已记录 | 通过 | 1.2、1.3 |
| 验收标准可观察、可测试 | 通过 | REQ-001~006 |
| 必要架构和单元设计达到可实现程度 | 通过 | 第二、三章 |
| 每项需求已有任务、范围和测试思路 | 通过 | 第四至六章 |
| 工作区基线和用户已有改动已识别 | 通过 | FACT-001 |
| 高风险变更已有回滚思路 | 通过 | 第九、十一章 |
| 无改变实现方向的未解决冲突 | 通过 | 7.2 无冲突 |

- 门禁结论：通过
- 条件及关闭要求：无。

### 12.2 完成门禁

| 检查项 | 结论 | 证据或条件 |
|---|---|---|
| 用户最新目标和有效需求逐项验收 | 通过 | REQ-001~006 全部完成 |
| 端到端追踪闭合 | 通过 | 第六章均有任务、实现与测试 |
| 测试已执行或缺口影响已准确记录 | 通过 | TEST-001~006 通过；TEST-LIVE-001 明确未发布缺口 |
| 缺陷已关闭或成为用户接受的遗留风险 | 通过 | 无未关闭确认缺陷 |
| 决策、冲突、回滚、风险和阻塞状态已更新 | 通过 | 第七至十一章已更新 |
| 最终差异无范围漂移、无关回退和调试残留 | 通过 | diff check 与现行入口扫描通过 |
| 账本与工作区一致，下一步唯一动作为“无” | 通过 | 4.1 与本节一致 |

- 门禁结论：通过
- 条件及关闭要求：无。真实 Release/Alpine 流量验证留待用户明确发布时执行，不阻塞本次未发布代码交付。

## 十三、检查点

| 时间 | 已完成 | 新发现或变化 | 影响 | 下一步唯一动作 |
|---|---|---|---|---|
| 2026-08-19 22:21 +08:00 | 完成现状调查、架构边界与准备门禁 | 旧 Exit 需保留运行兼容；Router 需两架构伴随清单 | 采用组合运行时和架构独立清单 | 重构 Mihomo 生命周期并组合进 Router |
| 2026-08-19 22:37 +08:00 | 完成共享运行时、主控收敛、安装升级和 release workflow | 用户取消旧 Mihomo 候选 | UI/API 只列 Router，旧节点只留底层兼容 | 执行全量回归 |
| 2026-08-19 22:58 +08:00 | 顺序回归、交叉构建、差异检查及 netcup2o 卸载均完成 | 并行测试曾因端口竞争失败，顺序复测全部通过 | 不属于产品缺陷 | 无 |

## 十四、完成摘要

- 交付结果：Linux router 已合并 Mihomo 出口能力；独立创建、候选、安装、升级、Docker 与 Release 产物已取消；netcup2o 旧实例已卸载。
- 需求验收：REQ-001~006 全部完成，证据见第五、六章。
- 测试结论：Controller、普通探针、Router、旧 tagged 兼容测试均通过，amd64/arm64 构建和脚本语法通过；未运行真实新 Release 安装与流量测试。
- 缺陷与风险：无未关闭确认缺陷；真实负载内存与发布后 Alpine 流量验证为残余风险。
- 回滚说明：代码未发布可按 RB-001 回退；netcup2o 删除项需重装才能恢复。
- 完成门禁：通过。
- 下一步唯一动作：无。
