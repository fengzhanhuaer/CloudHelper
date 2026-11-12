# 任务：三端按跳数和延迟选路，插件支持双栈 Ping 上报

- 状态：已完成
- 创建时间：2026-11-12 Asia/Singapore
- 启用方式：明确长任务条件，跨 Probe、安卓核心、C 插件实现和发布验证。
- 用户最新指令：跳数优先，同跳数选延迟低的；Probe、安卓使用相同策略；插件执行主控下发 IPv4/IPv6 Ping 并汇报。

## 一、需求定义

| 编号 | 需求及验收 | 状态 |
|---|---|---|
| REQ-001 | 三端优先可用最少跳数，同跳数使用测量 RTT；未知 RTT 不当作零；相同分数稳定排序 | 已完成 |
| REQ-002 | 新流选择低延迟承载，保留多槽位分摊和现有流绑定；不主动重建主控/承载 | 已完成 |
| REQ-003 | 插件处理 network_monitor_tasks / network_monitor_test，周期执行 IPv4/IPv6 ICMP 并按现有协议上报真实延迟、丢包 | 已完成 |
| REQ-004 | 有界 RAM、后台探测不阻塞控制流、不写 JFFS；IPv6 探测经 WAN，不扩大 LAN IPv6 范围 | 已完成 |
| REQ-005 | 针对性测试及必要组件验证通过，沿既有 Actions 发布；不主动升级生产设备 | 已完成 |

范围：选路与探测，无新增路由器手动 Ping 页面；不改认证、证书、LAN IPv6、防火墙及用户规则。
基线：mapledev；已有无关改动 doc/tasks/2026-10-28-upgrade-source-resume.md 保留不提交。

## 二、总体架构

Probe 已有 hop / RTT / lexicographic 比较，但同一相邻节点的多承载取第一个；安卓和插件为 BFS 配置顺序。
保留现有线上帧 main=2/3 RTT 协议，后台有界缓存测量最短路径，未测量时回退相邻 RTT；已建立 TCP 会话保留原路径。
插件原控制流忽略 network_monitor_tasks / test。增加单个独立探测 worker、有界任务和结果队列；唯一控制线程发送结果，避免并发写 TLS/yamux。任务与结果只在 RAM 保存。

### 接口记录

| 编号 | 接口 | 契约及验证 |
|---|---|---|
| IF-001 | main=2/3 Ping/RTT | 保留 request_id/path/source/target 和应答协议，无需主控变更；相邻 RTT 请求固定承载以测准不同端点 |
| IF-002 | network_monitor_tasks / test / result | 复用 Probe 与主控字段，IPv4/IPv6 literal；count 1..10、timeout 300..5000ms、cycle 5..86400s；回报 sent/received/loss/min/avg/max |

## 三、单元设计

| 编号 | 单元 | 设计与异常处理 | 验证 |
|---|---|---|---|
| UNIT-001 | probe_node/probe_virtual_router.go | 保留跳数优先比较；相邻承载按在线状态、RTT、稳定 ID 选择 | Go 路径/承载回归 |
| UNIT-002 | probe_node/mobilecore/mobile_vrouter*.go | 最短候选路径按 RTT 比较，后台限量测量，不阻塞首包；已有会话不重建 | Go 手机核心行为测试 |
| UNIT-003 | router_plugin/src/carrier.c / measure.c | 最短路径候选和有界 RTT 记录；新流低 RTT 承载，保留槽位绑定；相邻 Ping 固定承载 | C 路径/队列/RTT 单测 |
| UNIT-004 | router_plugin/src/network_monitor.c / control.c | 单 worker 取消、任务热更新及有界结果交接；IPv6 ICMP 绑定 WAN；失败如实上报 | C 双栈 loopback、丢包/超时、控制流集成 |

## 四、执行任务

| 编号 | 内容 | 状态 | 关联需求 |
|---|---|---|---|
| TASK-001 | 完成三端路径与承载选路及回归 | 已完成 | REQ-001,002,004 |
| TASK-002 | 完成双栈 Ping 任务、结果、取消与控制流接入 | 已完成 | REQ-003,004 |
| TASK-003 | 组件验证、审查差异和资源边界 | 已完成 | 全部 |
| TASK-004 | 提交发布、监控 Actions、核验资产 | 已完成 | REQ-005 |

当前交接：实现、本地验证及 v0.4.119 发布完成；下一步唯一动作：无。

## 五、测试与验证

| 编号 | 验证 | 状态 |
|---|---|---|
| TEST-001 | Probe 与 mobilecore：直达 200ms 胜两跳 80ms，同跳数低 RTT 胜；未知/相等 RTT；多承载与流绑定 | 已完成 |
| TEST-002 | 插件 C：相同上述路径场景、链路断开回退、测量请求固定承载 | 已完成 |
| TEST-003 | 插件 IPv4/IPv6 loopback ICMP，失联目标丢包，参数校验、更新/取消、yamux 结果 | 已完成 |
| TEST-004 | Go 组件测试 / vet，WSL C 编译与相关回归；不恢复用户删除的五组耗时互通 | 已完成 |
| TEST-005 | Actions 成功、版本提交与标签归属、发布资产非空及精确集合 | 已完成 |

## 六、端到端追踪

| 需求 | 单元 | 任务 | 测试 |
|---|---|---|---|
| REQ-001 | UNIT-001,002,003 / IF-001 | TASK-001,003 | TEST-001,002,004 |
| REQ-002 | UNIT-001,002,003 | TASK-001,003 | TEST-001,002 |
| REQ-003 | UNIT-004 / IF-002 | TASK-002,003 | TEST-003,004 |
| REQ-004 | UNIT-002,003,004 | TASK-001,002,003 | TEST-001,002,003,004 |
| REQ-005 | 发布工作流 | TASK-004 | TEST-005 |

## 七、决策与冲突

- DEC-001：用户明确跳数优先；直达 200ms 优于两跳 80ms。
- DEC-002：不新增 UI；Ping 仅执行主控任务并汇报。未知 RTT 回退稳定路径并后台测量，不等待测量阻塞业务。
- DEC-003：沿当前 release.yml 的十项资产发布；已退役的 Docker、BE86U 与 ARM64 路由器资产不恢复。
- 冲突：无。

## 八、缺陷记录

DEF-001（已修复）：原插件相邻 Ping 经 vr_send 会被第一个 route 抢走，不能准确区分多 endpoint RTT；改为固定实际承载发送，TEST-002 已验证。

## 九、回滚方案

上线出现新回归时通过在线升级回退上个发行版；仅 RAM 测量/任务状态，无 JFFS 数据迁移。验证主控在线、已有承载和任务恢复。

## 十、已验证事实

- Probe probeVirtualRouterPathLess 已先比较 len(path)，再比较 RTT/missing，再比较节点 ID。
- 修改前安卓 mobileVRouteShortestPath 及插件 vr_path 按邻居配置顺序返回首个 BFS 路径；现在同跳数按测量 RTT 和稳定顺序比较，TEST-001/002 通过。
- 主控现有 network_monitor_result 协议已含 IPv4/IPv6、丢包和延迟字段，无需新增主控接口。

## 十一、风险与阻塞

- 候选路径及 RTT 缓存必须限量，不枚举无限组合、不以 ICMP 失败直接断开业务承载。
- 真机安装与升级不在本次授权动作内；本地双栈测试与发布证据分别记录。

## 十二、质量门禁

- 准备门禁：通过。最新范围、接口、单元、验收、基线和回滚已记录。
- 完成门禁：通过。REQ-001..005、TEST-001..005 均有实现与验证证据；既有 vet 提示、race 环境限制如实记录，未改变无关文件或部署生产。


## 检查点：实现及初步验证

- 实现：Probe 相邻承载按在线/RTT/稳定 ID 比较；安卓最短候选、64 条后台样本与原流路径/承载绑定；插件同策略及 64 条索引 RTT 缓存，原始包沿用已有流的路径。主控消息按需扩容到最大 64KiB，避免合法多任务快照触发 16KiB 旧限制。
- Ping：C 原生 ICMP，任务内 20 目标并行、单 worker，不执行命令、不改防火墙。IPv6 绑定 WAN。周期更新原子替换，取消轮询不超过 100ms；结果仅由控制线程写入 yamux。
- 已通过：Go Probe/安卓选路针对性；mobilecore 全量；Probe 全量 JSON 重跑（日志 `%TEMP%/cloudhelper-path-probe-tests.jsonl`）；Controller 全量及 vet；C 原生路径、IPv4/IPv6 真实 ICMP、测量时窗、队列与数据面；IPv4/IPv6 TLS+WS+yamux 真实任务与结果传输，超过 16KiB 快照后保持同一控制流。
- Go 首次全量运行失败，未保留完整首轮输出；后续同源全量 JSON 运行通过，不将首轮标作通过。原 `go vet ./...` 有既有 Windows unsafe.Pointer 和手机测试 copylocks 提示；关闭这两类检查后的全模块 vet 通过，未修改这些无关路径。
- 环境：UbuntuServer 新版 curl 的旧 API 弃用警告导致 -Werror 失败，切换仓库固定 Ubuntu18 CI 容器后严格编译通过；不是放宽产品警告。Windows `-race` 因 CGO_DISABLED 且无 GCC 未运行，后续以普通 Go、真实协议和 CI 编译验证。
- 发布预检：原 skill 脚本仍要求已退役 Docker 与 ARM64 router 标记，按用户既有发布范围复用十资产临时预检；live workflow SHA256 保持 `0cfd7f3cd28ef86665c8a47a73bd1554d42cc7c3cdd5ac25fd52a2e341457143`。临时预检已通过，未改 workflow 或全局 skill。
- 尚未部署到生产；等待最后 ARM/ABI 和本次最终差异核验再提交发布。

## 检查点：本地验证完成、准备发布

- 固定 Ubuntu18 SDK 的 `router_plugin/tools/build-ci.sh` 全流程退出 0：原生回归、IPv4/IPv6 控制流与升级、ARM 编译、四种固件运行库/QEMU 检查及 JFFS 打包通过。输出 manifest 的主 ABI 与三个 additional ABI 均 PASS；本地包版本为 v0.4.118-dev，仅用于验证。
- 最后调整测量应答必须 ok 后，重跑 test-measure 与 test-path-selection 均 PASS；Linux router amd64/arm64 交叉编译均退出 0。最终 diff --check 通过。未恢复五组耗时互通测试。
- 资源边界：最短路径候选及样本各最多 64；C 原始流增加紧凑路径索引，约 32KiB；任务最多 32、每任务 20 目标、单次任务队列 4、结果队列 8；无 JFFS 写入。
- 真实生产安装未执行；本地测试不能替代设备在线升级验收。Go vet 两类既有提示与未执行 race 的限制如上记录。
- 发布预检 ready=true；当前版本 0.4.118，预期下个版本由 Actions 自动生成；十资产集合以 live workflow 为准，临时预检输出中的旧 routerArtifacts 展示字段不作为资产证据。

## 检查点：已推送，等待发布

- 提交 `23a41b170ca258fb4ee75b037b8388d873fa5f31`，22 个任务文件；无关 upgrade-source-resume 账本未暂存、未提交。
- 推送 `origin/mapledev` 成功；精确 SHA 对应 Actions： https://github.com/fengzhanhuaer/CloudHelper/actions/runs/43252595507 。
- 流水线正在运行，版本准备、主控、Linux Probe 与 Linux router 已成功；继续等待 Android 与 C 插件及发布，不提前标记完成。

## 检查点：发布完成

- Actions 43252595507 最终 completed/success，9 个作业全部 success，包含 Android、插件 ABI/打包及主控遥测兼容检查。
- 自动版本提交 `68810d6ef642a68207f7cea03259129e1746a513` 的父提交为本任务 `23a41b170ca258fb4ee75b037b8388d873fa5f31`；标签 v0.4.119 指向该版本提交。
- Release： https://github.com/fengzhanhuaer/CloudHelper/releases/tag/v0.4.119 ，正式发布、非草稿、非预发布；十项资产精确匹配 workflow，全部非空。插件包 136685 字节、manifest 4701 字节，Android APK 18879809 字节。
- 资产为 controller-linux-amd64、node-linux-amd64/arm64/windows-amd64、router-linux-amd64 及 manifest、Android arm64 APK、AX56U V2 JFFS 包及 manifest、mihomo-LICENSE；未恢复 BE86U、Docker 或 ARM64 router 资产。
- 未执行生产升级。用户已有 `doc/tasks/2026-10-28-upgrade-source-resume.md` 改动保留。完成后仅提交本账本的发布记录，带 skip ci / skip release，避免产生额外版本。
