# 任务：路由器插件上报补齐与发布范围调整

- 任务标识：`2026-10-23-router-telemetry`
- 状态：进行中
- 创建/更新时间：2026-10-23 Asia/Singapore
- 用户原始需求：插件没有上送公网 IP、路由状态与缓冲状态。
- 用户最新指令：参照 Probe Node，普通节点链路不发送域名 SNI，修复 ALI-99-SH 握手；追加询问另一台机器为何仍出现 LAN/WAN changed; rebinding，待确认机器身份；仍停止后续 BE86U 发布。
- 启用方式：明确长任务条件，跨采集、控制流、主控兼容和发布验证。

## 一、需求定义

范围为 C 路由器插件遥测和发布流程；保留私有认证与现有路由协议，不刷机、不删除旧 Release，不向 BE86U 部署。公网查询失败不能阻塞转发或断开主控连接。

| 需求 | 验收 | 状态 |
|---|---|---|
| REQ-001 公网 IP | IPv4/IPv6 按 Probe 格式去重上报；NAT 公网探测有界、异步 | 本地与在线验收通过 |
| REQ-002 路由状态 | relay_status 含实际路由和每槽连接状态，主控可识别 | 主控实际接收 9 条拓扑，通过 |
| REQ-003 缓冲状态 | 实际 Carrier 收发缓冲当前值、容量、峰值随报告上送，支持召唤与重置 | 主控实际接收 72 缓冲；重置与满容量测试通过 |
| REQ-004 停发 BE86U | 后续 Release 仅含 AX56U 插件包和清单，保留兼容源代码 | v0.4.83/84/85 正式资产核验通过 |
| REQ-005 本机显示 | 运行状态显示公网 IPv4/IPv6；尚未获取显示未获取，刷新不触发网络查询 | 浏览器、在线 API 与部署页面哈希通过 |
| REQ-006 相邻拓扑扩容 | 用户明确扩大为 32 条、每条 4 槽；满容量报告不超过上限，保留路由配置，在线恢复 | 边界、满容量和在线恢复通过 |
| REQ-007 持续运行与初始化故障 | 修复实机误报地址变化导致的反复重建，继续验证路由初始化与上报 | 确定性回归通过；上线超过 6 分钟无重建，后续策略更新保留连接 |
| REQ-008 节点链路无 SNI | 普通 relay 不发送 SNI；保留证书验证、HMAC/ticket；主控/DoT 不受影响；ALI 实际连接恢复 | 进行中 |

## 二、总体架构

修改前基线：report.c 仅采集 LAN/WAN IPv4 和 WAN 流量；控制流单帧限制 16 KiB，缺少 relay_status / virtual_router_buffers。主控已有标准接收和展示逻辑。数据平面主线程拥有 carrier，控制线程不可直接读取可变链路。

目标：主线程发布不可变 JSON 快照，控制线程锁内复制文本后独立解析上报，兼容旧 json-c 非原子引用计数；公共地址由有界后台探测缓存。大型报告通过现有 yamux 流分段发送，遵守窗口，不引入新认证入口。

### 接口

| 接口 | 契约 | 关联 |
|---|---|---|
| IF-001 report | 沿用 Probe ipv4/ipv6、relay_status、virtual_router_buffers JSON；只含运行信息，无密钥 | REQ-001/002/003 |
| IF-002 控制命令 | report_once 与 virtual_router_buffers_reset；重置统计不清空业务队列 | REQ-003 |
| IF-003 Release | 不上传 cloudhelper-asusbe86u-jffs.tar.gz 及 JSON；AX56U 保持兼容 | REQ-004 |
| IF-004 插件状态页 | status 公网地址与主控报告使用相同缓存结果，HTML 按文本展示 | REQ-005 |
| IF-005 Relay TLS | carrier 专用无 SNI 入口；普通 TLS 入口保持 SNI；依 Probe 保留 api_copilot_ Cloudflare 入口所需 SNI；证书验证策略不放宽 | REQ-008 |

## 三、单元设计

| 单元 | 位置 | 规则与验证 |
|---|---|---|
| UNIT-001 地址采集 | report.c/control.c | 接口与公网地址分离；探测失败保留短期缓存；禁用 IPv6 不探测 IPv6 |
| UNIT-002 快照/统计 | carrier.c/engine.h/engine.c | 主线程更新实际队列峰值，互斥锁交换快照；控制线程只读 |
| UNIT-003 控制流 | control.c | 单个有界待发报告，按窗口分段，接收 ACK 与窗口更新 |
| UNIT-004 发布 | release.yml/build-ci.sh/release_workflow_linux_router_test.go | 移除 BE86U 打包与发布断言，保留运行兼容验证 |
| UNIT-005 本机状态页 | engine.c / VirtualRouter.asp | 用已有状态刷新展示公网地址；只读缓存，不增加查询和磁盘写入 |
| UNIT-006 Relay TLS | tls.c/transport.h/carrier.c | 拆分 relay 与主控/DoT 的 SNI 选择，复用原 CA/域名或 SPKI 校验及有界握手；服务端观测测试 |

## 四、执行任务

| 任务 | 内容 | 状态 | 需求 | 完成条件 |
|---|---|---|---|---|
| TASK-001 | 停发 BE86U | 已完成 | REQ-004 | 发布资产契约测试通过 |
| TASK-002 | 地址及路由/缓冲采集与安全上报 | 已完成 | REQ-001/002/003 | 原生 C 与控制流测试通过 |
| TASK-003 | 验证、发布、AX56U 在线复验 | 已完成 | 全部 | 正式资产与运行结果有证据 |
| TASK-004 | 运行状态显示公网 IP | 已完成 | REQ-005 | Chrome 有值/空值及只查询 status 的刷新验证通过 |
| TASK-005 | 相邻拓扑扩容并恢复启动 | 已完成 | REQ-006 | 32 条拓扑/128 槽位、报告及容量边界通过，AX 在线恢复 |
| TASK-006 | 修复子进程输出读取竞态并复验 | 已完成 | REQ-007 | 退出后不丢 NVRAM 输出，实机持续运行通过 |
| TASK-007 | 对齐 Probe 的节点 SNI 并发布复验 | 进行中 | REQ-008 | 服务端观测无 SNI、证书负例/正常 TLS/互通通过，ALI 在线 4 槽恢复 |

当前交接：REQ-008 原生、ARM 四固件 ABI 及 5 组既有 Probe 双向/四槽/旧单槽互通全部通过；主控全套/vet、Probe 全套和 Linux Router amd64/arm64 构建通过。8 个真实 SNI/身份正负例通过；初次脚本 Python 3.6 兼容问题已纠正。ALI 实际 SPKI 与下发指纹一致。下一步唯一动作：提交指定 9 个文件、推送正式发布，随后仅在线升级节点 25 复验 ALI；另一台 rebinding 只读调查继续，未获现场信息前不推断原因。

## 五、测试与验证

| 测试 | 内容 | 关联 | 结果 |
|---|---|---|---|
| TEST-001 | Go Release 资产契约和 YAML 依赖 | REQ-004/TASK-001 | `go -C probe_node test . -run TestReleaseWorkflow -count=1` 通过，资产列表 10 个 |
| TEST-002 | C 地址、队列峰值/重置、TLS/WS/yamux 分段报告 | REQ-001/002/003/TASK-002 | 32 Carrier、64 缓冲、8 次报告/重置通过；主控实际解析 C 产出的快照通过 |
| TEST-003 | C 全套 CI + ARM 固件运行兼容，主控/Probe Go 测试及 vet | 全部/TASK-003 | 全套 CI、原生及四固件 ABI Probe 互通通过；追加 UI/DNS 修正后的原生控制/API 与 ARM 实机库复验通过；最终主控全套含 C fixture/vet 通过；Probe 全套重跑通过，Linux Router amd64/arm64 交叉构建通过 |
| TEST-004 | 正式 Release 资产与 AX56U 在线上报 | 全部/TASK-003 | v0.4.83/84/85 均精确 10 资产、无 BE；v85 包 SHA256 2d0ca32463ac7a9d74c0b7a8559a5164b0a383aa7e2bbd6d8121e2b4da794947。实机二进制/页面与正式清单相同；节点 25 向主控上送公网 IPv4 123.232.8.66、9 拓扑、72 缓冲；BE86U 不部署 |
| TEST-005 | 本机状态公网 IP 与空值、刷新行为 | REQ-005/TASK-004 | Chrome headless 实际运行页面 render/refresh：公网 IPv4/IPv6 分行与未获取显示通过，两次刷新仅请求 status；截图已核对 |
| TEST-006 | 32 拓扑边界、满容量报告、真实 TLS 分段与主控解析 | REQ-006/TASK-005 | 第 9/32 条接受、第 33 条拒绝、禁用不计数；128 Carrier / 256 缓冲快照约 163 KiB，主控解析通过；原生真实 TLS/控制/API/升级、ARM 编译与四固件运行兼容全套通过 |
| TEST-007 | NVRAM 子进程退出时序、截断和失败退出码 | REQ-007/TASK-006 | 强制首次 read=EAGAIN，子进程在 waitpid 前写入并退出：旧代码丢输出断言失败，修复后完整值/截断/失败退出码均通过；原生全套、ARM 编译及四固件运行兼容全套通过。上线后 14:08:07Z 至 14:14:51Z stats_started_at 未改变，无 LAN/WAN changed 或初始化失败，14:10:11Z 策略更新保持既有连接 |
| TEST-008 | Relay 无 SNI、CA/域名/SPKI 负例及普通 TLS 正例；正式升级后 ALI 槽位 | REQ-008/TASK-007 | 真实服务端观测 8 例通过：普通 relay 无 SNI、域名/CA/指纹错误拒绝、正确 pin 接受、普通 TLS 和 api_copilot_ 保留 SNI；ALI 在线 SPKI 符合下发指纹。原生/ARM 全套、四固件 ABI、5 组 Probe 双向/四槽/旧单槽互通与 Go 全套通过；待正式升级 |

## 六、端到端追踪

| 需求 | 单元/接口 | 任务 | 测试 | 结果 |
|---|---|---|---|---|
| REQ-001 | UNIT-001/IF-001 | TASK-002/003 | TEST-002/003/004 | 源码测试及主控实际公网 IPv4 接收通过；设备无全局 IPv6，未执行真实公网 IPv6 查询，模拟 IPv6 测试通过 |
| REQ-002 | UNIT-002/003/IF-001 | TASK-002/003 | TEST-002/003/004 | runtime ID、实际 7 条可用链路各 4 槽及故障链路 0 槽接收通过 |
| REQ-003 | UNIT-002/003/IF-001/002 | TASK-002/003 | TEST-002/003/004 | 实机 72 缓冲持续更新；满容量 256 缓冲、峰值/reset/窗口测试通过 |
| REQ-004 | UNIT-004/IF-003 | TASK-001/003 | TEST-001/003/004 | 三次正式 Release 均 10 资产且无 BE 包，通过 |
| REQ-005 | UNIT-005/IF-004 | TASK-004/003 | TEST-005/004 | Chrome 有值/空值和刷新通过；在线 API 及页面哈希通过 |
| REQ-006 | UNIT-002/003/IF-001/002 | TASK-005/003 | TEST-006/004 | 第 9/32/33 条边界、128 Carrier 报告、实机 9 拓扑恢复通过；未在生产创建 32 条测试拓扑 |
| REQ-007 | UNIT-001/002 | TASK-006/003 | TEST-007/004 | 竞态修复前失败、修复后通过；实机连续超过 6 分钟无错误重建 |
| REQ-008 | UNIT-006/IF-005 | TASK-007 | TEST-008/003 | 待验证 |

## 七、决策与冲突

- DEC-001：依用户最新方向停止 BE86U 后续包生成和上传；旧包及兼容源码保留。
- DEC-002：采集不得触发 LAN/WAN 地址变化检测，避免仅遥测更新导致重连。
- DEC-003：公网探测复用 Probe 的 ipify/icanhazip 两组 HTTPS 服务，单请求连接/总超时 2/3 秒；成功 5 分钟刷新、失败 1 分钟重试，缓存 30 分钟失效；无全局 IPv6 时不探测 IPv6。后台线程 256 KiB 栈，不写文件。
- DEC-004：IPv4 查询复用主控连接的有界 WAN DNS 解析链，避免经过自身 dnsmasq/Fake IP；DNS 时间另受现有链路超时约束。IPv6 外部探测失败时可使用当前全局接口地址。C 报告使用与 Probe 完全相同的派生 runtime route ID 和 websocket 协议标识。
- CON-001：发布 Skill 预检仍要求已退休的 Docker 和 Linux Router ARM64 资产；基线工作流不含这些条目。沿用已有只移除此三项过时断言的临时预检，分支/仓库/版本/认证/禁用退出探针等检查全部通过；本次 BE86U 退休由用户最新指令授权。
- 先前两机型发布要求由最新指令替代。
- DEC-005：对齐 Probe 普通 relay 的无 SNI 行为，api_copilot_ 兼容例外保留；不复制 Probe 的 InsecureSkipVerify，插件继续校验 CA/域名或控制器 SPKI，既有 HMAC/ticket 不变。主控和 DNS 上游不使用新 relay 入口。

## 八、缺陷

- DEF-001：插件报告缺少公网 NAT 地址、路由和 Carrier 缓冲字段；已修复并通过 C/主控互通验证。
- DEF-002：报告仅单帧发送，大型快照会导致控制流失败；已改为单个最大 256 KiB 待发报告、8 KiB 分段和窗口背压，真实 TLS/WS/yamux 测试通过。
- DEF-003：当前第 9 条拓扑超过原 8 条上限导致冷启动失败；用户授权扩大为 32 条，仍保留有限队列、握手和报告上限。
- DEF-004：command() 在非阻塞空读与 waitpid 间出现子进程写入/退出时，未读取剩余管道就关闭；可丢失 NVRAM 输出误判地址变化。添加退出后最后一次 drain，保持超时和输出容量限制；确定性回归修复前失败、修复后通过。
- DEF-005：carrier 复用总是发送域名 SNI 的 TLS 入口，与 Probe 普通 relay 行为不一致；ALI 链路在当前来源带 SNI 握手被重置，无 SNI 时证书域名验证成功。按 REQ-008 修复中。
- DIAG-001：用户追加另一机器 rebinding；源码在地址读取失败或快照变化时共用该错误，不能仅凭日志认定地址真实变化。主控 14:37Z 显示节点 25 连续在线、统计起点未改变；节点 26 HM_A_R 最近版本也为 v0.4.85，短时离线。已请求用户确认机器，先做只读诊断，不以旧版本解释，也不向 BE86U 部署。

## 九、回滚

回滚代码提交并重新正式发布/在线升级；无配置迁移。重置统计不删除队列或连接。若报告影响控制连接，恢复前版控制流并保留数据平面。不得 SSH/SCP 替换生产二进制。

## 十、已验证事实

实机节点 25 / localrouter / 192.168.52.1 在线 v0.4.85；主控 v0.4.79 已接收新字段，无需升级主控。控制连接经 94.16.111.150:15030；运行状态 public_ipv4=123.232.8.66、public_ipv6 为空。LAN/WAN 分别为 192.168.52.1 / 172.18.54.247。

ALI-99-SH 节点 4 在线；主控下发 api.codex.na.002339.xyz:30010，IPv4 为 101.132.66.44。路由器 eth0 强制 IPv4 的 TCP 连接成功，带该域名的 TLS ClientHello 后被重置；本机同样失败。直接对 IPv4 的诊断请求不发送域名 SNI 时完成 TLS 1.3 并收到 HTTP 200，替代测试 SNI 能收到证书但预期地不匹配该测试域名。主控服务器经相同域名/IPv4/端口，强制 HTTP/1.1、TLS 1.3 后正常验证证书并收到 200；TLS 1.2 在两侧都重置。因此 ALI 问题定位为与路径/ClientHello/SNI 有关的 TLS 故障，不能仅凭此确认重置来自服务端还是中间设备，不能归因于无 IPv6。未更换方向、禁用生产证书验证、修改拓扑或修改私有认证。

## 十一、风险与阻塞

容量、分段和跨线程一致性已通过专门测试。ALI 链路仍为 0 槽，属于 TLS 握手故障；本次遥测与容量修复不声称解决该外部故障。BE86U 节点 26 已离线，遵循用户指令未恢复或部署。IPv6 公网实机验收受设备无全局 IPv6 限制，模拟覆盖有效。

## 十二、质量门禁

- 准备门禁：通过；最新范围、接口、单元、测试、基线、回滚已记录。
- 完成门禁：不通过；REQ-001 至 007 已验收，新追加 REQ-008 待实现、测试、发布和在线验证。

## 十三、检查点

2026-10-23：恢复调查上下文，记录用户停发 BE86U 最新指令，尚未修改产品代码。

2026-10-23：已实现地址后台探测、路由/会话/帧计数、真实 Carrier 队列与 decoder 峰值、reset、分段报告；Go 主控解析实际 C fixture 通过。主控与 Probe 全套通过（Probe 首次因本机 TCP/UDP 测试端口预留失败，完整重跑通过）。证据在 Windows Temp/router-telemetry-*.log 与 node-tests.jsonl，不含凭据。等待 ARM CI 后发布。

2026-10-23 用户追加 REQ-005：本机运行状态显示公网 IP，新增 UNIT-005/IF-004/TASK-004/TEST-005。ARM CI、最终原生测试与 Go 主控全套/vet 已通过。主控 HTTPS CF 路径读取 nodes 超时，将改用已授权主控 SSH 回环诊断，不绕过认证。

2026-10-23：发布前验收全部通过。Chrome 图片位于 Temp/router-telemetry-ui/{public,empty}.png；原生/ARM 最终证据为 router-telemetry-acceptance-{native,arm}.log。经主控 SSH 加密转发至其回环、使用原管理认证确认节点 25 Ies_A_R 在线 v0.4.81，原 ipv4 仅 LAN/WAN 私网地址，buffers.items=0。准备正式发布后用相同 API 复验，禁止向 BE86U 部署。

2026-10-23：用户明确将容量扩大至 32。边界及全套 CI 成功，证据 Temp/router-capacity32-{boundary,final-ci}.log；Go 主控解析实际满容量 C fixture 成功。先前容量构建因执行中调整脚本导致保护环境断言失败，已停止并在最终稳定脚本上完整重跑通过。准备推送容量修复，发布 v0.4.84 后经带 CSRF 的插件自身在线升级恢复节点 25。

2026-10-23：f19eadb0f81279b5021607219e23a040fa04cf1b 的 Actions 39961237585 全部成功，v0.4.84 正式 10 资产及清单哈希核验通过。节点 25 经插件自有 CSRF 在线接口升级成功，实机二进制和页面 SHA256 与 Release 相同；9 拓扑、72 缓冲、IPv4 123.232.8.66 已上报主控。持续观察出现 LAN/WAN changed 循环和 published route initialization failed；当前地址保持 192.168.52.1 / 172.18.54.247，已确定性复现并修复 NVRAM 读取竞态，待新版本持续验收。用户询问 IPv6/SNI：确认 IPv4-only 受支持，此故障不能归因于无 IPv6；SNI 仅新 LAN TCP/443 有界嗅探。

2026-10-23：NVRAM 竞态修复 4e8b5d1449d894fb1d59e73fff1c22fd61fc0575 已推送；原生/ARM 全套构建与运行检查成功，证据 Temp/router-command-{before,after,final-ci}.log。正在监控匹配 Actions 39965373105；下一步唯一动作：正式发布 v0.4.85、核验资产后通过插件自身在线升级节点 25，并跨多个地址检查周期确认持续运行。

2026-10-23 最终验收：v0.4.85 正式发布并经插件自有 CSRF 在线接口安装；全部资产及部署文件 SHA256 匹配。14:08:07Z 至 14:14:51Z 运行统计起点不变，主控已收 9 拓扑 / 72 缓冲，公网 IPv4 正常；可用 7 链路各 4 槽，策略更新未断开既有连接。最新 ALI 截图已从节点 25、Windows 本机、主控服务器三个来源核对，结论见已验证事实。源代码已发布；仅剩本账本跳过发布的文档提交与临时 SSH 转发清理。

2026-10-23 追加 REQ-008：用户明确参照 Probe 普通节点链路不发送域名 SNI。新 relay TLS 入口复用原证书校验，只改变 SNI 选择，carrier 改用该入口。8 个真实 TLS 正负例通过，证据 Windows Temp/router-nosni-targeted.log；主控全套/vet 通过。正在执行 router-nosni-final-ci.log 与 router-nosni-node-test.log 对应全套，完成后自动提交、正式发布并通过插件在线更新节点 25，禁止手工覆盖生产二进制。

2026-10-23 发布前：原生/ARM 全套、5 组 Probe Carrier 互通全部成功（router-nosni-final-ci.log）；主控全套/vet、Probe 全套及 Linux Router 双架构构建通过。用户追加另一台 rebinding，主控确认节点 26 最近也为 v85，故不能解释为旧版本；homerouter SSH 当前拒绝，待用户确认机器/现场，未修改该设备。正式发布预检旧资产三条断言仍过时，沿 CON-001 使用原有临时预检，其余检查通过。
