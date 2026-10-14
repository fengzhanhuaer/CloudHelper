# 探针流量统计与月度周期

- 任务标识：`2026-10-13-probe-traffic-dashboard`
- 状态：`实现完成，待发布`
- 创建时间：`2026-10-13 Asia/Singapore`
- 更新时间：`2026-10-13 Asia/Singapore`
- 用户原始需求：Dashboard 增加探针总流量（进/出分开）及月度流量；月度默认从每月1号开始，可在探针编辑页为每个探针单独设置月度起始日。
- 用户最新指令：流量只统计实际物理出口，排除 probe node 等虚拟网卡。
- 启用方式：明确长任务条件；涉及探针跨平台采集、WS上报、主控持久化、配置/API和Dashboard多个模块。

## 一、需求定义

### 1.1 背景与问题

Dashboard 当前探针卡片只显示 CPU、内存、磁盘和开机时长，没有主机网络累计流量。管理探针已有 `/mng/api/probe/node/update` 更新接口和单节点编辑弹窗，可扩展月度周期配置。

### 1.2 目标

在 Dashboard 每个探针状态项展示累计总流量及当前月度周期流量，RX/TX 分开；支持每个探针独立配置月度起始日，默认1号。

### 1.3 范围、非范围与约束

- 范围内：Linux/Android/Windows 探针只读取启用中的硬件设备接口累计 RX/TX，排除 TUN/veth 等无硬件设备关联的虚拟接口；报告协议兼容扩展；主控差值累计持久化；管理页月度起始日配置；Dashboard 显示。
- 范围外：流量限额/告警、速率曲线、按应用/路由/目标分类、数据导出、历史账单调整。
- 约束：旧探针不带新计数时仍可连接；统计口径从全部非loopback切换到硬件出口后旧口径累计归零并重新建立baseline；起始日限于1至31，短月夹到当月最后一天；每日分桶保留400天。

### 1.4 需求与验收标准

| 需求编号 | 需求描述 | 验收标准 | 优先级 | 状态 | 来源或最新变更 |
|---|---|---|---|---|---|
| REQ-001 | 探针上报物理出口流量计数 | Linux/Android/Windows 报告启用中的硬件设备接口累计 RX/TX，不包含 TUN 等纯虚拟接口；不破坏旧 JSON 上报 | 高 | `进行中` | 用户最新指令 |
| REQ-002 | 统计总流量和当前月度流量 | 每探针显示累计 RX/TX 与当前周期 RX/TX，主控重启后总量仍保留；按每日分桶重算月度值 | 高 | `进行中` | 用户原始需求 |
| REQ-003 | 每探针单独设置月度起始日 | 编辑页可配置1–31号，默认1号，保存后重载保持；短月使用最后一天 | 高 | `进行中` | 用户最新指令 |

## 二、总体架构

### 2.1 当前现状

探针 `sendProbeReport` 每60秒发送 `probeReportPayload.System`，Android 由 `mobilecore.sendReport` 发送另一套 `systemStatus`。Controller WebSocket 在 `probe_ws.go` 解码 `probeReportMessage` 并更新内存 runtime。`probeSystemMetrics/systemStatus` 当前只有 CPU、内存、交换区和磁盘字段。公开 Dashboard `/dashboard/probes` 由 `publicDashboardProbeMetrics` 输出脱敏节点卡片。管理页更新 DTO 位于 `probe_registry.go`，UI 在 `mng_pages/probe.html`。

### 2.2 目标架构

各平台探针系统状态新增累计 `network_rx_bytes/network_tx_bytes`。主控接收上报后按节点持久化上一计数器、差值累计总量和最近400天 UTC 日增量；计数器下降按接口重置/重启处理，将当前计数作为该次增量。Dashboard API 在响应时按节点配置的月度起算日汇总日增量。探针配置持久化 `monthly_traffic_start_day`，缺省归一为1。

### 2.3 关键模块与职责

| 模块 | 当前职责 | 目标职责 | 输入 | 输出 | 依赖 |
|---|---|---|---|---|---|
| `probe_node` | 采集 CPU/RAM/Disk 并上报 | 采集启用中的硬件设备接口RX/TX累计计数并报告 | OS接口统计 | 两个uint64字节计数 | sysfs、Windows IP Helper |
| `probe_controller/internal/core/probe_ws.go` | 解码状态报告 | 接受流量字段并更新runtime/持久计量 | report JSON | 每节点实时计数 | WebSocket |
| `probe_traffic_store.go` | 不适用 | 持久化累计差值、日分桶及查询月度汇总 | nodeID、counter、timestamp | total/month usage | 文件原子替换 |
| `probe_registry.go`, `mng_pages/probe.html` | 探针元数据编辑 | 保存并编辑月度起算日 | day 1–31 | 节点配置 | ProbeStore JSON、管理API |
| `status.go`, `dashboard/page.html` | 公开状态卡片 | 展示RX/TX总量和当前月度周期值 | runtime+usage+start day | Dashboard JSON/文本 | Controller stores |

### 2.4 关键流程

| 流程 | 发起方 | 处理方 | 数据或状态变化 | 失败处理 | 关联需求 |
|---|---|---|---|---|---|
| 采集并上报 | 探针定时报告 | 主控WS处理器 | 上报两方向累计接口计数 | 字段缺失按旧报告处理；采集失败不伪造零差值 | REQ-001 |
| 计量累计 | 主控WS报告处理 | per-node traffic store | 对单调 counter 求差、更新 lifetime 和 UTC daily bucket | 文件错误记录 warning；保留上次可读文件 | REQ-002 |
| 管理周期配置 | 探针编辑页 | node update API | 保存并归一化月度起始日 | 非法值返回400；旧客户端缺字段默认1 | REQ-003 |
| Dashboard显示 | `/dashboard/probes` | status + traffic store | 按起始日计算当前周期并返回RX/TX | 无样本显示“采集中”或0，不展示伪速率 | REQ-002/003 |

### 2.5 接口记录

| 接口编号 | 接口名称 | 调用方 | 提供方 | 输入、输出与错误契约 | 实现位置 | 兼容要求 | 关联需求、任务与测试 | 状态与证据 |
|---|---|---|---|---|---|---|---|---|
| IF-001 | Probe `report` WebSocket JSON | probe_node/android | probe_controller | 新增 `system.network_rx_bytes/network_tx_bytes` uint64，旧字段缺省为0 | probe report payloads、`probe_ws.go` | JSON additive，旧探针继续可连 | REQ-001/TASK-001/TEST-001 | 已实现，定向编译通过 |
| IF-002 | `POST /mng/api/probe/node/update` | 管理页 | controller | 新增 `monthly_traffic_start_day`，接受1–31；省略按1 | registry DTO、handler、probe page | 旧管理客户端兼容 | REQ-003/TASK-003/TEST-003 | 已实现，registry测试通过 |
| IF-003 | `GET /dashboard/probes` | Dashboard | controller | 每项增加 traffic total/month rx/tx、周期起始和可用状态 | status API | additive JSON | REQ-002/003/TASK-004/TEST-004 | 已实现，控制器编译通过 |

### 2.6 架构决策引用

| 决策编号 | 对架构的影响 | 相关模块或接口 |
|---|---|---|
| DEC-001 | 使用OS接口累计RX/TX；只统计启用中的硬件设备接口，排除TUN等纯虚拟接口 | probe report、Dashboard |
| DEC-002 | 主控累计counter差值、每日UTC增量和总量；counter下降时视为接口重建/重启 | traffic store、IF-001/003 |
| DEC-003 | 月度起始日为每月日号1–31，缺省1，短月夹至当月末；日桶保留400天 | probe config、IF-002/003 |

## 三、单元设计

### 3.1 受影响单元

| 单元编号 | 文件或位置 | 职责 | 输入 | 输出 | 依赖 | 关联需求 |
|---|---|---|---|---|---|---|
| UNIT-001 | probe_node system metrics/report，mobilecore | 跨平台收集并上报RX/TX计数 | Linux `/proc/net/dev`、Windows `GetIfTable2Ex` | 两个累计计数 | OS API、已有 `x/sys` | REQ-001 |
| UNIT-002 | controller runtime、`probe_traffic_store.go` | 解码后持久计量和按日期汇总 | node/counters/time/day | persistent totals and month usage | file store | REQ-001/002 |
| UNIT-003 | `probe_registry.go`, `probe_store.go`, `mng_pages/probe.html` | 配置序列化、校验、编辑 | node metadata/update JSON | saved start day | ProbeStore | REQ-003 |
| UNIT-004 | `status.go`, `dashboard/page.html` | 输出/展示探针流量 | total/month stats | RX/TX文字 | Dashboard API | REQ-002/003 |

### 3.2 处理与异常规则

| 单元编号 | 正常处理规则 | 异常处理规则 | 兼容要求 | 验证方式 |
|---|---|---|---|---|
| UNIT-001 | 每次既有report周期采集启用中的硬件设备接口累计字节 | 采集失败字段标记不可用；排除无sysfs device link的Linux/Android虚拟设备及Windows非硬件接口 | 旧报告字段缺省；平台文件build tags隔离 | Linux/Android/Windows编译及接口过滤测试 |
| UNIT-002 | 第一次采集建立baseline；之后差值进总量和UTC日桶 | counter回退以当前值为重启后增量；临时文件+rename写入 | 不改已有探针状态契约 | 单元测试首样本、增量、重启、月度边界、重载 |
| UNIT-003 | 日号默认1，接受1–31 | 无效输入拒绝；旧字段零值归一成1 | additive JSON字段，不改旧节点 | API/UI测试和存储归一测试 |
| UNIT-004 | 探针卡片分别显示总接收/发送、本周期接收/发送和起止日期 | 无采样显示“采集中”；数值用可读单位 | 保留CPU等现有卡片 | handler/UI验证 |

## 四、执行任务

### 4.1 当前交接

- 当前阶段：实现与验证
- 当前计划步骤：TASK-001 至 TASK-004
- 当前门禁：流量功能实现和定向验证完成；完整 controller 测试存在网络监控/仪表盘用例失败，需区分上一批未提交改动的影响。
- 最近完成检查点：探针跨平台报告、主控持久累计、月度配置及 dashboard 展示已实现。
- 工作区状态：存在上一需求的未提交修改 `probe_network_monitor_store.go`、`status.go`、`dashboard/page.html` 和其任务账本；需原样保留并避免冲突覆盖。
- 下一步唯一动作：如用户要求发布，走 CloudHelper 发布流程；若要求先清理全量测试，先排查网络监控用例失败。
- 恢复时先读取：本账本、git status/diff、`probe_node/main.go`、`probe_node/mobilecore`、`probe_runtime.go`、`probe_ws.go`、管理probe页面和Dashboard页面。

### 4.2 任务计划

| 任务编号 | 工作内容 | 状态 | 关联需求 | 文件或接口范围 | 完成条件 |
|---|---|---|---|---|---|
| TASK-001 | 跨平台接口流量采集与报告扩展 | `完成` | REQ-001 | probe_node, mobilecore, controller report structs | Linux/Android/Windows具备RX/TX累计上报，兼容旧字段 |
| TASK-002 | 主控流量差值、持久化、月度汇总 | `完成` | REQ-001/002 | runtime、traffic store、status | 总量持久；月度按配置日分桶重算；counter reset合理 |
| TASK-003 | 探针月度起算日编辑与保存 | `完成` | REQ-003 | registry、handlers、probe page | 每节点可选1–31、默认1并持久保存 |
| TASK-004 | Dashboard流量显示与端到端验证 | `实现完成` | REQ-002/003 | dashboard API/UI | 每节点显示RX/TX总量与月度量；定向编译/脚本检查通过 |

### 4.3 变更记录

| 文件、配置或接口 | 变更内容 | 原因 | 关联需求与任务 | 验证方式 | 回滚引用 |
|---|---|---|---|---|---|

## 五、测试与验证

### 5.1 测试计划与结果

| 测试编号 | 测试目标 | 关联需求与任务 | 方法或准确命令 | 预期结果 | 实际结果 | 状态 | 证据 |
|---|---|---|---|---|---|---|---|
| TEST-001 | 上报字段兼容、物理接口过滤与跨编译 | REQ-001/TASK-001 | `go test . ./mobilecore`; Windows build; Android mobilecore build | 虚拟接口不计入；各平台编译通过 | Linux probe packages passed including physical-versus-virtual filter tests; Windows amd64 `go build ./...` passed; Android arm64 mobilecore build passed | `通过` | controller定向编译通过 |
| TEST-002 | baseline、正向增量、重启回退、持久化重载、旧口径切换 | REQ-002/TASK-002 | controller store单测 | RX/TX总量及日桶精确；旧口径不混入 | baseline、增量、counter reset、文件重载、旧统计口径清零测试通过 | `通过` | `go test ./internal/core -run 'Test(AccumulateProbeTraffic|ProbeTrafficPeriodStartClampsShortMonths|UpdateProbeNodeLockedStoresLocalConsoleEnabled)'` |
| TEST-003 | start day 默认、1/31、无效输入与短月夹取 | REQ-003/TASK-003 | registry/API测试 | 配置规范化正确 | registry保存测试通过；周期1号、31号、短月和缺省用例通过 | `通过` | 同 TEST-002 命令 |
| TEST-004 | Dashboard API月度汇总及UI显示 | REQ-002/003/TASK-004 | handler测试、页面静态检查 | JSON正确且两方向值清晰 | controller package编译通过；两页面脚本语法检查通过；全量 controller 测试含网络监控/网络 dashboard 用例失败 | `部分通过` | `go test ./...` 有 `TestProbeNetworkMonitorResultsPersistToPerNodeGobFiles` 和三个 `TestDashboardNetwork...` 失败 |

### 5.2 未执行测试

| 测试编号 | 未执行原因 | 影响 | 替代证据 | 后续动作 |
|---|---|---|---|---|

## 六、端到端追踪

| 需求编号 | 验收标准 | 架构或单元 | 任务编号 | 文件、配置或接口 | 测试编号 | 结果与证据 | 状态 |
|---|---|---|---|---|---|---|---|
| REQ-001 | 报告启用中的硬件出口RX/TX累计值并兼容旧探针 | DEC-001/UNIT-001 | TASK-001 | IF-001 | TEST-001 | Linux接口过滤、Android/Windows构建通过；probe测试通过 | `完成` |
| REQ-002 | Dashboard分别显示持久的总流量与当前月度流量 | DEC-002/UNIT-002/004 | TASK-002/004 | IF-003 | TEST-002/004 | 主控差值累计、日桶持久化和月度重算测试通过；UI脚本语法通过 | `完成` |
| REQ-003 | 每节点单独设置月度起始日，默认1号 | DEC-003/UNIT-003/004 | TASK-003 | IF-002/003 | TEST-003/004 | 1–31输入、缺省1及短月夹取测试通过 | `完成` |

## 七、决策与冲突记录

### 7.1 决策记录

| 决策编号 | 触发原因 | 采用方案 | 理由与证据 | 替代方案 | 影响范围 | 替代关系 | 状态 |
|---|---|---|---|---|---|---|---|
| DEC-001 | 用户要求只统计实际出口 | 汇总启用中的硬件设备接口RX/TX，排除TUN等无硬件关联虚拟接口 | Linux/Android sysfs device link；Windows MIB_IF_ROW2 HardwareInterface flag | 汇总所有非loopback接口 | Probe report、Dashboard | 替代原全接口口径 | `有效` |
| DEC-002 | 主控需要跨重启保留总量并按任意月度起日查询 | 持久化counter差值总量及400天UTC日桶 | 只存当月累加值无法在设置起日后重算 | 只用探针当前网卡counter | 存储/API | 无 | `有效` |
| DEC-003 | 用户指定可分别设置月度起始日期 | 采用月内日号1–31，缺省1，短月截到月末 | 与“默认从1号开始”一致，且可自然表达各探针账期 | 每探针输入完整年月日 | 管理UI/API | 无 | `有效` |

### 7.2 冲突记录

无。

## 八、缺陷记录

无。

## 九、回滚方案

探针上报JSON字段为可选新增字段，可回滚Controller/Probe代码而无需改旧报文。节点配置新增字段缺省为1号，旧版本忽略此字段。流量持久文件独立于节点配置；回滚代码后不读取但不影响其他数据。不得在回滚时删除流量文件。

## 十、已验证事实

| 事实编号 | 已验证事实 | 证据 | 对任务的影响 |
|---|---|---|---|
| FACT-001 | Controller普通探针report周期默认60秒，system metrics以JSON随report发送 | `probe_node/main.go` `sendProbeReport`; `probe_ws.go` `probeReportMessage` | 可在现有周期增加两个累计字段 |
| FACT-002 | Android使用独立`mobilecore` reporter和systemStatus类型 | `mobilecore_reporter.go`, `mobilecore.go` | Android字段需同步更新 |
| FACT-003 | 节点配置标准化和更新请求集中于`probe_registry.go`，编辑UI集中于`mng_pages/probe.html` | `normalizeProbeNodes`, `updateProbeNodeLocked` | 起始日添加至现有DTO和编辑表单 |
| FACT-004 | Windows依赖已有`golang.org/x/sys/windows`并提供`GetIfTable2Ex`与`MibIfRow2` | module cache `x/sys@v0.46.0/windows` | 不必新增第三方依赖即可读Windows接口计数 |

## 十一、风险与阻塞

| 编号 | 类型 | 描述与证据 | 影响 | 缓解或所需动作 | 状态 |
|---|---|---|---|---|---|
| RISK-001 | 统计口径 | 部分驱动可能未暴露标准硬件接口标识 | 真实出口可能漏计 | Linux/Android 使用sysfs设备链接，Windows使用硬件标志；以目标探针实机状态复核 | 待实机观察 |
| RISK-002 | 计数器重置 | 探针重启或接口重建时累计计数变小 | 差值算法误算 | 回退时把新counter视为新epoch增量，记录行为测试 | 待验证 |
| RISK-003 | 日桶边界 | 月度切换在UTC日期边界计算 | 与用户本地时区账期有一天差异 | 明确页面展示UTC周期；如业务需要本地时区须改配置模型 | 已知假设 |

## 十二、质量门禁

### 12.1 准备门禁

- 门禁结论：有条件通过
- 条件及关闭要求：已按用户最新要求切换为启用中的硬件接口；旧口径存量会在新口径首次样本时清零并baseline，防止历史虚拟网卡字节混入。

### 12.2 完成门禁

- 门禁结论：功能验收通过；全量回归有未解决失败。
- 条件及关闭要求：流量定向测试通过；全量 controller 测试发现网络监控存储测试和三个 Dashboard 网络监控测试失败，关联上一批未提交网络指标改动。发布前应单独确认并修复或确认基线。

## 十三、检查点

| 时间 | 已完成 | 新发现或变化 | 影响 | 下一步唯一动作 |
|---|---|---|---|---|
| 2026-10-14 | 根据最新口径新增Linux/Android sysfs设备接口筛选、Windows硬件接口标志筛选，并使旧统计口径重新baseline；对应定向测试和跨平台构建通过 | Linux/Android以sysfs `device`关联作为硬件设备判定；Windows以MIB_IF_ROW2硬件标志判定 | 虚拟接口流量不再混入；新口径首次样本后总量从零开始 | 等待用户指示发布或继续处理全量网络指标测试失败 |

## 十四、完成摘要

- 交付结果：已实现 dashboard 总流量与月度 RX/TX、单探针月度起始日配置及只统计硬件出口接口的跨平台采集。
- 需求验收：REQ-001/002/003 完成。
- 测试结论：流量定向测试、Linux物理接口过滤测试、Windows/Android交叉构建通过；controller全量测试未通过，失败集中在网络监控用例。
- 缺陷与风险：RISK-001/002/003。
- 回滚说明：第九章。
- 完成门禁：功能通过；完整回归待清理既有网络指标测试失败。
- 下一步唯一动作：等待用户指示发布或排查已记录的网络指标测试失败。
