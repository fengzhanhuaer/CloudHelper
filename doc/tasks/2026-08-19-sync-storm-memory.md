# 任务：修复 CloudHelper 同步风暴与内存异常

- 任务标识：`2026-08-19-sync-storm-memory`
- 状态：`进行中`
- 创建时间：`2026-08-19 Asia/Taipei`
- 更新时间：`2026-08-19 Asia/Taipei`
- 用户原始需求：连接 netcup2o 排查高占用与崩溃，判断是否由主控引起。
- 用户最新指令：为 netcup2o 增加 2GB Swap，修复并排查 probe node、probe exit node 与主控异常；旁路由掉线不需要清理或广播；修复后发布，并通过在线升级更新测试环境后验证。
- 启用方式：明确长任务条件，涉及远端系统、主控、节点、调查、实现与验证。

## 一、需求定义

### 1.1 背景与问题

netcup2o 只有约 1.9 GiB RAM 且没有 Swap。日志确认 2026-08-18 至 2026-08-19 发生 8 次 OOM：前 6 次杀死 `probe_exit_node`，后 2 次杀死 `probe_controller`。当前启动周期内普通 probe 收到 3605 次路由配置同步，其中 2970 次失败；主控代码会在 Linux 旁路由状态变化或掉线时向所有已知节点异步广播，而节点收到同步后立即拉取配置并回报，缺少合并和并发边界。

### 1.2 目标

为 netcup2o 提供 2GB 持久 Swap 缓冲，并从主控与节点两侧消除路由配置同步风暴的无界并发，使断线不再广播、重复同步被有界合并，同时保留主动配置变更的最终一致性。

### 1.3 范围、非范围与约束

- 范围内：netcup2o Swap 创建与持久化；主控路由同步调度；普通 probe、exit node、Linux router 共用的同步命令处理；回归测试；GitHub 发布；通过在线升级更新 netcup2o 测试环境并复核资源。
- 范围外：提高 exit node 的 `MemoryMax`；修改业务路由配置；通过 SSH/SCP 手工替换线上 CloudHelper 二进制。
- 约束：保留用户已有工作区修改；运行时二进制只能通过在线升级流程部署；远端变更仅限用户明确授权的 2GB Swap；不得记录密钥或令牌。

### 1.4 需求与验收标准

| 需求编号 | 需求描述 | 验收标准 | 优先级 | 状态 | 来源或最新变更 |
|---|---|---|---|---|---|
| REQ-001 | netcup2o 增加 2GB 持久 Swap | `swapon --show` 显示约 2GB；`/etc/fstab` 有唯一有效条目；创建后内存与服务状态可读 | 高 | `已完成` | 用户最新指令 |
| REQ-002 | 旁路由掉线或重连不广播清理 | 会话断开只更新在线状态；重连保留上次报告，同一报告不触发全节点路由同步 | 高 | `已完成` | 用户明确“不需要清理”及 v0.4.3 实机复测 |
| REQ-003 | 主控同步广播有界合并 | 并发请求最多一个执行者，并在执行期间只保留一次待处理同步；主动配置变更最终送达 | 高 | `已完成` | OOM 与同步风暴诊断 |
| REQ-004 | 节点同步执行有界合并 | 同一进程不并发执行重复拉取与即时回报；突发请求合并且最后一次请求不会丢失 | 高 | `已完成` | node/exit node OOM 诊断 |
| REQ-005 | 完成回归与远端复核 | controller、probe normal、router tag 测试通过；远端 Swap 和当前资源状态复核完成 | 高 | `待开始` | 用户要求修复排查 |
| REQ-006 | 发布并更新测试环境 | release workflow 成功、资产齐全；netcup2o 通过在线升级运行新版本并完成实机验证 | 高 | `待开始` | 用户最新指令 |

## 二、总体架构

### 2.1 当前现状

- 主控 `unregisterProbeSession` 在 Linux router 掉线时启动全节点同步 goroutine。
- 主控 `ProbeWSHandler` 在 Linux router 路由报告变化时直接启动全节点同步 goroutine。
- `dispatchProbeRouteConfigSyncToNodes` 对每个会话串行写入，但多个调用者可无界并发，单次写最长阻塞 10 秒。
- 节点 `runProbeRouteConfigSyncControl` 对每个命令同步拉取配置并立即报告，没有 single-flight 或 pending 合并。
- exit node 与普通 probe 共用上述节点命令处理路径，因此会被主控风暴放大。

### 2.2 目标架构

- 掉线只维护在线状态，不触发清理广播。
- 主控内部增加进程级路由同步协调器：任一时刻一个广播执行；执行期间的重复请求合并为一次 pending 执行。
- 节点内部增加进程级路由同步协调器：任一时刻一个同步执行；执行期间保留最新控制参数并最多补跑一次。
- 主动配置修改仍可使用同步 API，并保持调用方需要的分发结果；自动异步触发统一走协调器。

### 2.3 关键模块与职责

| 模块 | 当前职责 | 目标职责 | 输入 | 输出 | 依赖 |
|---|---|---|---|---|---|
| 主控会话管理 | 注册、注销 probe 会话 | 注销仅更新在线状态 | node ID、session | 在线状态 | probe runtime store |
| 主控同步调度 | 直接全节点广播 | 自动触发去重、单飞、pending 合并 | 同步触发事件 | 有界广播 | probe sessions |
| 节点同步处理 | 每条命令立即同步并报告 | 单飞执行、合并重复请求 | route_config_sync 命令 | 配置更新与一次报告 | controller HTTP API |
| netcup2o 系统 | 运行 controller/probe/exit 等 | 提供 2GB Swap 缓冲 | swapfile | 内核 Swap | Debian/systemd |

### 2.4 关键流程

| 流程 | 发起方 | 处理方 | 数据或状态变化 | 失败处理 | 关联需求 |
|---|---|---|---|---|---|
| 节点掉线 | yamux/WebSocket | 主控会话管理 | Online=false，不广播 | 等待节点自然重连 | REQ-002 |
| 自动路由刷新 | router 状态变化 | 主控同步协调器 | 一次执行加至多一次 pending | 记录失败；后续触发可重试 | REQ-003 |
| 节点应用同步 | 主控命令 | probe 同步协调器 | 单次拉取与报告 | 合并期间请求并补跑一次 | REQ-004 |
| Swap 启用 | 运维操作 | Linux 内核 | 新增 2GB Swap | 关闭 Swap 并移除 fstab 条目 | REQ-001 |

### 2.5 接口记录

不修改网络协议或 JSON 契约；仅修改现有 `route_config_sync` 命令在进程内的调度语义。兼容旧节点和旧主控。

### 2.6 架构决策引用

| 决策编号 | 对架构的影响 | 相关模块或接口 |
|---|---|---|
| DEC-001 | 掉线不再触发路由清理广播 | 主控会话管理 |
| DEC-002 | 不提高内存上限，先消除无界工作与增加 Swap 缓冲 | netcup2o、controller、probe |
| DEC-003 | 自动触发使用 single-flight/pending 合并，显式管理 API 保留同步结果 | 主控同步调度 |

## 三、单元设计

### 3.1 受影响单元

| 单元编号 | 文件或位置 | 职责 | 输入 | 输出 | 依赖 | 关联需求 |
|---|---|---|---|---|---|---|
| UNIT-001 | `probe_controller/internal/core/probe_command.go` | 会话注销与路由同步调度 | node/session、自动触发 | 在线状态、合并广播 | probe sessions | REQ-002, REQ-003 |
| UNIT-002 | `probe_controller/internal/core/probe_runtime.go` | 维护在线状态 | node ID、online | runtime state | runtime store | REQ-002 |
| UNIT-003 | `probe_node/main.go` 及新增/既有同步单元 | 消费同步命令 | control message、identity | 配置同步与报告 | controller API | REQ-004 |
| UNIT-004 | netcup2o `/swapfile`、`/etc/fstab` | Swap 持久化 | 2GB 文件 | 活跃 Swap | Debian 内核 | REQ-001 |

### 3.2 处理与异常规则

| 单元编号 | 正常处理规则 | 异常处理规则 | 兼容要求 | 验证方式 |
|---|---|---|---|---|
| UNIT-001 | 自动触发同时最多一个广播，重复触发合并 | 写失败不无限重试，允许后续事件再触发 | 显式 API 返回值不变 | 并发单元测试、controller 全测 |
| UNIT-002 | 掉线仅设置 Online=false；重连等待新 router report | 空 node ID 忽略 | 管理页在线状态保持准确 | runtime 单元测试 |
| UNIT-003 | 同步同时最多一个，保留一次 pending | 错误记录一次，pending 仍可执行 | 命令 JSON 不变，三种 build kind 共用 | normal/router 测试 |
| UNIT-004 | 仅创建唯一 2GB swapfile 并追加唯一 fstab 条目 | 任一步失败时不留下重复 fstab 条目 | 不改现有服务与内存限制 | `swapon --show`、`free -h`、fstab 检查 |

## 四、执行任务

### 4.1 当前交接

- 当前阶段：验证
- 当前计划步骤：TASK-006 发布重连修复、在线升级测试环境并复验
- 当前门禁：准备门禁通过
- 最近完成检查点：v0.4.4 已发布并在线升级 netcup2o，但每 15 秒广播仍存在；最终定位为 router 应用主控配置后的 revision/SHA 变化被误判为 LAN 路由变化，已修复且定向测试 100 轮、controller 全测、vet 与 Linux 构建通过。
- 工作区状态：分支 `mapledev` 落后远端 1 个自动版本提交；2 个 controller 文件和本账本有本任务未提交修改。
- 下一步唯一动作：提交 revision/SHA 反馈环修复并触发新版本发布。
- 恢复时先读取：本账本、`git status`、`probe_command.go`、`probe_runtime.go`、`probe_node/main.go`。

### 4.2 任务计划

| 任务编号 | 工作内容 | 状态 | 关联需求 | 文件或接口范围 | 完成条件 |
|---|---|---|---|---|---|
| TASK-001 | 建立账本并执行准备门禁 | `已完成` | 全部 | 本账本 | 门禁通过 |
| TASK-002 | netcup2o 增加 2GB 持久 Swap | `已完成` | REQ-001 | `/swapfile`、`/etc/fstab` | Swap 活跃且持久配置唯一 |
| TASK-003 | 完成主控有界广播调度 | `已完成` | REQ-002, REQ-003 | controller core | 并发测试与全测通过 |
| TASK-004 | 完成节点有界同步调度 | `已完成` | REQ-004 | probe node | normal/router 测试通过 |
| TASK-005 | 完整验证与远端资源复核 | `已完成` | REQ-005 | controller/probe/远端 | 测试证据和资源快照齐全 |
| TASK-006 | 提交、发布、在线升级测试环境并验证 | `进行中` | REQ-006 | Git、GitHub Actions、netcup2o 在线升级 | 发布成功且实机新版本稳定 |

### 4.3 变更记录

| 文件、配置或接口 | 变更内容 | 原因 | 关联需求与任务 | 验证方式 | 回滚引用 |
|---|---|---|---|---|---|
| `probe_command.go` | 初步移除会话断开后的全节点广播 | 用户明确无需清理 | REQ-002 / TASK-003 | controller 全测已通过 | RB-002 |
| `probe_runtime.go` | `setProbeRuntimeOnline` 不再返回清理信号 | 移除断线广播耦合 | REQ-002 / TASK-003 | controller 全测已通过 | RB-002 |
| `probe_linux_router_test.go` | 调整掉线/重连状态断言 | 覆盖新语义 | REQ-002 / TASK-003 | 定向测试已通过 | RB-002 |
| netcup2o `/swapfile`、`/etc/fstab` | 新增 2GB Swap 与唯一持久化条目 | 缓冲瞬时内存峰值 | REQ-001 / TASK-002 | TEST-001 | RB-001 |
| controller `probe_command.go`、`probe_ws.go`、`probe_certificate.go` | 自动同步 single-flight/pending 合并，移除掉线广播 | 消除无界 goroutine 与掉线风暴 | REQ-002, REQ-003 / TASK-003 | TEST-002, TEST-003, TEST-005 | RB-002 |
| controller `probe_runtime.go` | health/fail-open 抖动不触发全网同步 | 避免运行状态形成反馈环 | REQ-003 / TASK-003 | TEST-002, TEST-005 | RB-002 |
| controller `probe_runtime.go`、`probe_linux_router_test.go` | 重连保留已有 Linux router 报告，同一报告不触发同步 | 消除路由探针短连导致的固定周期广播 | REQ-002, REQ-003 / TASK-003 | TEST-002, TEST-005, TEST-007 | RB-002 |
| controller `probe_runtime.go`、`probe_linux_router_test.go` | 应用 revision/SHA 变化不再触发广播，只比较本地代理开关、发布 CIDR 与 ACL | 切断主控广播导致 router 应用版本更新、再触发广播的反馈环 | REQ-002, REQ-003 / TASK-003 | TEST-002, TEST-005, TEST-007 | RB-002 |
| probe `main.go`、`probe_route_config_sync.go` | 控制同步单飞合并、底层同步串行化 | 消除 node/exit node 重复拉取和回报 | REQ-004 / TASK-004 | TEST-004, TEST-006 | RB-002 |

## 五、测试与验证

### 5.1 测试计划与结果

| 测试编号 | 测试目标 | 关联需求与任务 | 方法或准确命令 | 预期结果 | 实际结果 | 状态 | 证据 |
|---|---|---|---|---|---|---|---|
| TEST-001 | Swap 活跃与持久化 | REQ-001 / TASK-002 | `swapon --show --bytes`; 检查 `/etc/fstab`; `free -h` | 约 2GB、唯一条目 | 文件 2147483648 字节、0600、唯一条目；`swapfile.swap` active | `已完成` | netcup2o 2026-08-19 12:48 CEST |
| TEST-002 | 非路由语义变化不触发广播且状态正确 | REQ-002 / TASK-003 | controller 定向测试 | 断线仅 offline；重连保留报告；同一报告及 revision/SHA、health/fail-open 变化不触发同步 | 通过；定向用例 100 轮通过 | `已完成` | controller 定向测试 |
| TEST-003 | 主控并发触发被合并 | REQ-003 / TASK-003 | 新增并发单元测试 | 最大并发 1，pending 至多 1 | 100 次突发合并为 2 次，最大并发 1 | `已完成` | `TestProbeRouteConfigSyncSchedulerCoalescesBurst` 20 轮 |
| TEST-004 | 节点并发同步被合并 | REQ-004 / TASK-004 | 新增 normal/router 单元测试 | 最大并发 1，最终请求执行 | 100 次突发合并为 2 次，最大并发 1，保留最新 URL | `已完成` | normal/router 定向测试各 20 轮 |
| TEST-005 | controller 完整回归 | REQ-002, REQ-003, REQ-005 | `go test ./... -count=1` | 通过 | 通过；`go vet ./...` 通过 | `已完成` | controller 2026-08-19 本地输出 |
| TEST-006 | probe normal/router 完整回归 | REQ-004, REQ-005 | normal、`mihomo_exit`、`linux_router` 全测和发布目标交叉编译 | 通过 | 三套全测、controller/normal/exit/router 发布目标交叉编译及 workflow 契约测试通过 | `已完成` | probe 2026-08-19 本地输出 |
| TEST-007 | 发布与在线升级实机验证 | REQ-006 / TASK-006 | release workflow、资产核对、在线升级、服务/内存/同步频率观察 | 新版本运行且无同步风暴、无新增 OOM | 待执行 | `待开始` | Actions 与远端日志 |

### 5.2 未执行测试

当前无永久跳过项；线上修复效果需要发布后才能观察，本任务不含未经授权的发布或手工部署。

## 六、端到端追踪

| 需求编号 | 验收标准 | 架构或单元 | 任务编号 | 文件、配置或接口 | 测试编号 | 结果与证据 | 状态 |
|---|---|---|---|---|---|---|---|
| REQ-001 | 2GB Swap 活跃且持久 | UNIT-004 | TASK-002 | `/swapfile`, `/etc/fstab` | TEST-001 | `swapfile.swap` active，约 2GB，唯一 fstab 条目 | `已完成` |
| REQ-002 | 掉线不广播 | UNIT-001, UNIT-002 | TASK-003 | controller core | TEST-002, TEST-005 | 定向与完整测试通过 | `已完成` |
| REQ-003 | 主控广播有界合并 | UNIT-001 | TASK-003 | controller core | TEST-003, TEST-005 | 100 次突发合并为 2 次，最大并发 1 | `已完成` |
| REQ-004 | 节点同步有界合并 | UNIT-003 | TASK-004 | probe node | TEST-004, TEST-006 | normal/router 定向与全测通过 | `已完成` |
| REQ-005 | 完整回归与远端复核 | 全部 | TASK-005 | 全部 | TEST-001 至 TEST-006 | controller/probe 全测和远端 Swap 复核通过 | `已完成` |
| REQ-006 | 发布并在线升级测试环境 | 全部 | TASK-006 | release workflow、netcup2o | TEST-007 | 待执行 | `待开始` |

## 七、决策与冲突记录

### 7.1 决策记录

| 决策编号 | 触发原因 | 采用方案 | 理由与证据 | 替代方案 | 影响范围 | 替代关系 | 状态 |
|---|---|---|---|---|---|---|---|
| DEC-001 | 用户明确掉线无需清理 | 断线只更新 Online，不广播 | 广播在抖动时形成全网风暴 | 延迟广播或立即清理 | controller | 无 | `有效` |
| DEC-002 | 1.9 GiB 主机反复 OOM | 增加 2GB Swap，但不提高服务 MemoryMax | Swap 仅缓冲；提高上限会转为全局 OOM | 仅扩容 RAM | 远端系统 | 无 | `有效` |
| DEC-003 | 自动触发需有界且显式 API 需返回结果 | 自动触发 single-flight，显式 API 保持同步 | 兼顾兼容与资源边界 | 所有入口完全异步 | controller | 无 | `有效` |
| DEC-004 | 用户要求发布并更新测试 | 完整验证后走 release workflow 和在线升级 | 遵守 CloudHelper 部署边界并保留可追溯资产 | SSH/SCP 手工替换 | 发布与测试环境 | 无 | `有效` |

### 7.2 冲突记录

无。

## 八、缺陷记录

| 缺陷编号 | 关联需求与测试 | 严重程度 | 现象与根因 | 修复状态 | 修改位置 | 复测证据 |
|---|---|---|---|---|---|---|
| DEF-001 | REQ-002, REQ-003 / TEST-002, TEST-003 | 严重 | 掉线和 router 状态变化直接启动无界全节点广播 goroutine | 已修复 | controller core | 定向 20 轮及全测通过 |
| DEF-002 | REQ-004 / TEST-004 | 严重 | 节点对每条同步命令并发执行拉取与即时回报，无合并边界 | 已修复 | probe node | normal/router 定向 20 轮及全测通过 |
| DEF-003 | REQ-001 / TEST-001 | 高 | 约 1.9 GiB 主机无 Swap，内存峰值直接触发 OOM | 已修复 | netcup2o | `swapfile.swap` active |
| DEF-004 | REQ-002, REQ-003 / TEST-002, TEST-007 | 严重 | v0.4.3/v0.4.4 实机每 15 秒广播；重连清空是风险点，持续反馈环的根因是 router 应用主控配置后 revision/SHA 变化又被误判为 LAN 路由变化 | 已修复，待发布复验 | controller `probe_runtime.go` | 非路由语义变化定向测试 100 轮、controller 全测通过 |

## 九、回滚方案

| 变更或风险 | 触发条件 | 回滚步骤 | 数据与兼容影响 | 回滚后验证 | 状态 |
|---|---|---|---|---|---|
| RB-001 Swap | Swap 导致异常 I/O 或配置错误 | `swapoff` 精确 swapfile，移除唯一 fstab 条目，再删除 swapfile | 无业务数据变化 | `swapon --show` 无该项，fstab 可解析 | `有效` |
| RB-002 调度代码 | 测试或兼容回归 | 仅回退本任务对应提交/文件差异 | 恢复旧风暴风险，无数据迁移 | controller/probe 全测 | `有效` |

## 十、已验证事实

| 事实编号 | 已验证事实 | 证据 | 对任务的影响 |
|---|---|---|---|
| FACT-001 | netcup2o RAM 约 1.9 GiB、Swap 0 | `free -h` | 必须增加缓冲但不能替代代码修复 |
| FACT-002 | 8 次 OOM：6 次 exit node，2 次 controller | persistent journal/kernel OOM 记录 | controller 与 node 两侧都需治理 |
| FACT-003 | controller 两次峰值约 800.6/906.2 MiB 后 OOM | systemd status/journal | 主控存在无界工作累积 |
| FACT-004 | 一次启动普通 probe 收到 3605 次同步，2970 次失败 | journal 计数 | 同步风暴已证实 |
| FACT-005 | exit node 前 5 次为 cgroup OOM，第 6 次为全局 OOM | kernel journal 与 `MemoryMax=1GB` | 不应通过提高 MemoryMax 处理 |
| FACT-006 | 停止 controller 后同步风暴消失 | 实时远端状态 | controller 是直接触发方 |
| FACT-007 | netcup2o 已有 2GB 持久 Swap | `/swapfile` 2147483648 字节、0600；systemd `swapfile.swap` active | DEF-003 已关闭 |
| FACT-008 | 自动同步入口均经过协调器 | `rg` 无直接 `go dispatch...` 或 `go runProbeRouteConfigSyncControl` | 无旁路入口 |
| FACT-009 | v0.4.3 在线升级后 #1、#21 均成功回报新版本，但剩余同步严格每 15 秒一次 | netcup2o runtime API 与 exit runtime log | single-flight 限制了并发，但重连清空仍会持续触发 |
| FACT-010 | v0.4.4 保留重连报告后仍每 15 秒同步，主控约 5 分钟升至约 400 MiB 并导致管理登录超时 | netcup2o systemd、runtime log 与管理 API | 排除单纯重连清空，确认存在配置应用反馈环 |

## 十一、风险与阻塞

| 编号 | 类型 | 描述与证据 | 影响 | 缓解或所需动作 | 状态 |
|---|---|---|---|---|---|
| RISK-001 | 线上验证 | 实机长期内存曲线需发布与在线升级后观察 | 发布前只能本地证明有界性 | 完整测试后发布并在线升级验证 | 开放 |
| RISK-002 | 容量 | cloudflared 当前曾达到约 500 MiB | controller 停止时仍可能内存紧张 | Swap 缓冲并后续单独观察 cloudflared | 开放 |
| RISK-003 | 测试环境 | Go race detector 要求 CGO，但当前 `CGO_ENABLED=0` 且无已确认 C 编译器；probe vet 还有既有 mobilecore 锁复制与 Windows unsafe 告警 | 未获得动态数据竞争检测，vet 非全绿 | 以锁保护、定向并发 20 轮、三套全测和交叉编译替代；发布后实机观察 | 开放 |

## 十二、质量门禁

### 12.1 准备门禁

| 检查项 | 结论 | 证据或条件 |
|---|---|---|
| 最新目标、范围、非范围和约束已记录 | 通过 | 需求定义 1.2、1.3 |
| 验收标准可观察、可测试 | 通过 | REQ-001 至 REQ-006 |
| 必要架构和单元设计达到可实现程度 | 通过 | 第二、三章 |
| 每项需求已有任务、范围和测试思路 | 通过 | 第四至第六章 |
| 工作区基线和用户已有改动已识别 | 通过 | 当前交接与 git status |
| 高风险变更已有回滚思路 | 通过 | RB-001、RB-002 |
| 无改变实现方向的未解决冲突 | 通过 | 冲突记录为无 |

- 门禁结论：通过
- 条件及关闭要求：无

### 12.2 完成门禁

| 检查项 | 结论 | 证据或条件 |
|---|---|---|
| 用户最新目标和有效需求逐项验收 | 待检查 | 实现与验证未完成 |
| 端到端追踪闭合 | 待检查 | 实现与验证未完成 |
| 测试已执行或缺口影响已准确记录 | 待检查 | TEST-001 至 TEST-007 |
| 缺陷已关闭或成为用户接受的遗留风险 | 待检查 | DEF-001 至 DEF-003 |
| 决策、冲突、回滚、风险和阻塞状态已更新 | 待检查 | 完成前复核 |
| 最终差异无范围漂移、无关回退和调试残留 | 待检查 | 完成前复核 |
| 账本与工作区一致，下一步唯一动作为“无” | 待检查 | 完成前复核 |

- 门禁结论：不通过
- 条件及关闭要求：完成 TASK-002 至 TASK-006。

## 十三、检查点

| 时间 | 已完成 | 新发现或变化 | 影响 | 下一步唯一动作 |
|---|---|---|---|---|
| 2026-08-19 | 远端只读诊断、掉线广播初步移除、controller 全测 | OOM 与同步风暴存在直接相关；exit node 也为受害者；用户要求发布并在线升级测试 | 扩大到双侧治理、发布和实机验证 | 创建并验证 2GB Swap |
| 2026-08-19 | 创建并持久化 2GB `/swapfile` | Swap 0B 使用，业务服务未重启；fstab 虚拟光驱存在原有警告但无错误 | REQ-001、TASK-002、TEST-001 完成 | 实现主控有界广播调度 |
| 2026-08-19 | 完成主控与节点有界同步实现及第一轮全测 | race detector 因 CGO 禁用无法运行；无同步旁路入口 | REQ-002 至 REQ-004 完成，进入发布前验证 | 执行 probe vet、跨平台构建和 workflow 测试 |
| 2026-08-19 | 完成发布前验证 | probe vet 仅有既有告警；其余测试和发布目标构建通过 | TASK-005 完成，进入发布 | 提交并同步 origin/mapledev |
| 2026-08-19 | 发布 v0.4.3，主控及 #1/#21 在线升级成功 | 同步从每分钟数百次降至固定每 15 秒一次；定位到路由重连清空报告 | 新增 DEF-004，阻止 TASK-006 关闭 | 修复重连清空并再次发布 |
| 2026-08-19 | 发布并升级 v0.4.4 | 重连报告保留后仍每 15 秒广播；revision/SHA 随主控配置应用变化形成反馈 | 修正 DEF-004 根因，主控临时停止防止再次 OOM | 忽略非路由语义版本变化并发布复验 |

## 十四、完成摘要

- 交付结果：进行中
- 需求验收：进行中
- 测试结论：controller 初步修改全测通过，其余待执行
- 缺陷与风险：见 DEF-001 至 DEF-003、RISK-001 至 RISK-002
- 回滚说明：见 RB-001、RB-002
- 完成门禁：不通过
- 下一步唯一动作：提交并同步 origin/mapledev
