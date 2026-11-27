# 任务：三端外部下载测速

- 任务标识：`2026-11-27-external-speedtest`
- 状态：已完成
- 用户最新指令：Probe Node、路由器插件、安卓均增加外部 speedtest，可选直连或某个节点。
- 启用方式：明确跨调查、设计、实现和验证，涉及三个模块的长任务。
- 基线：2e2098e90（v0.4.136，功能父提交 6fd56ac1）；接管时仅 Probe 外部测速两个新文件与 local_console.go 路由修改，属于本任务。

## 一、需求定义

目标：比较设备直连外部下载源与经选定节点的真实 HTTPS 下载速度。保留已有节点间测速。

| 编号 | 需求及验收 | 状态 |
|---|---|---|
| REQ-001 | Probe 本地界面可选直连/出口节点，启动、查询、停止外部测速 | 已完成 |
| REQ-002 | 插件节点测速页提供同样能力，结果 Mbps、MB/s、字节、路径 | 已完成 |
| REQ-003 | 安卓界面与 Go bridge 提供同样能力 | 已完成 |
| REQ-004 | 不修改路由规则；单连接、10 秒、64 MiB 上限；TLS 校验，失败不回退直连；无文件缓存 | 已完成 |

非范围：设备自动升级、Ookla 二进制、并行测速、自定义任意下载 URL。

## 二、总体架构

默认固定 Cloudflare HTTPS 下载源。Go 两端共享下载计数与临时 TCP netstack；节点模式通过现有 main=1 IP 帧承载，回复按唯一 TCP 元组和固定路径交回测速会话。插件使用受限 libcurl 任务，节点模式通过本机 TUN 与特定元组重写接入现有转发，不改普通业务选路。

| 接口 | 输入/输出 | 兼容 |
|---|---|---|
| IF-001 Probe /local/api/virtual_router/external_speed | GET 状态；POST action=start/stop, node_id 空为直连 | 已有本地认证 |
| IF-002 安卓 VRouteExternalSpeed | action,node_id；JSON 状态 | 新 bridge，不改旧测速 |
| IF-003 插件 measure external / measure_stop | node=direct 或节点 ID；measurement 状态 | 沿用 CSRF |

## 三、单元设计

| 单元 | 文件范围 | 处理与验证 |
|---|---|---|
| UNIT-001 | probe_node/internal/externalspeed | 有界 HTTP 读取、取消、错误响应不计速；测试 TLS/超时/上限与 IP 帧隔离 |
| UNIT-002 | probe_external_speed.go, local_pages/virtual_router.html | 本地任务串行与直连旁路；认证和 UI 请求测试 |
| UNIT-003 | mobilecore, Android bridge/assets | 复用载体且不依赖改变 VPN 规则；bridge 与 UI 校验 |
| UNIT-004 | router_plugin/src/external_speed.c, dataplane.c, measure.c, web | 工作线程与元组绑定；C 回归与页面测试 |

## 四、执行任务

| 编号 | 工作 | 状态 | 关联 |
|---|---|---|---|
| TASK-001 | Go 共享下载器、临时 IP TCP 会话、Probe 接口 | 已完成 | REQ-001,004 |
| TASK-002 | 安卓与 Probe 页面 | 已完成 | REQ-001,003 |
| TASK-003 | 插件实现与界面 | 已完成 | REQ-002,004 |
| TASK-004 | 三端测试、构建、发布检查 | 已完成 | 全部 |

当前交接：实现、验证与发布已完成。准备门禁及完成门禁通过。下一步唯一动作：无；设备升级与现场速率验收未在本任务执行。

## 五、测试与验证

| 编号 | 方法 | 状态 |
|---|---|---|
| TEST-001 | Go 有界下载、取消、错误页面、临时 TCP 收发隔离；Probe 认证 | 待执行；初始未实现的 RED 已验证 |
| TEST-002 | Probe/安卓/插件 UI 在桌面与窄屏启动和停止 payload | 待执行 |
| TEST-003 | 插件编译与数据面、任务状态回归 | 待执行 |
| TEST-004 | Go 全模块测试、构建、release 工作流 | 待执行 |

## 六、端到端追踪

| 需求 | 单元/任务 | 测试 | 证据 |
|---|---|---|---|
| REQ-001 | UNIT-001,002 / TASK-001,002 | TEST-001,002,004 | 待补 |
| REQ-002 | UNIT-004 / TASK-003 | TEST-002,003,004 | 待补 |
| REQ-003 | UNIT-001,003 / TASK-002 | TEST-001,002,004 | 待补 |
| REQ-004 | 全部 / TASK-001,003 | TEST-001,003,004 | 待补 |

## 七、决策与冲突

DEC-001：用户将 Probe 仅直连扩为三端均支持直连与节点，按最新指令统一。
DEC-002：真实 main=1 TCP 数据面，避免把节点间合成数据测速误当外部下载。
冲突：无。

## 八、缺陷

无已确认新缺陷。

## 九、回滚

新任务只用内存和临时会话，不持久化路由策略；停止任务关闭套接字、释放缓冲。若发布后出现问题，在线回滚上一版本；无配置迁移。

## 十、已验证事实

Go Probe 与安卓已有 main=1 IP 帧及通用出口；插件存在 TUN 转发。旧测速继续使用 main=4，不替代。

## 十一、风险与阻塞

外部下载源可限速，测速结果代表所选路径到该源；不能等同 Telegram 服务器性能。设备现场三端验收须与源码/受控测试分开报告。

## 十二、门禁与检查点

准备门禁：通过。完成门禁：通过；本地 Go/C/Android/浏览器验证与发布作业 9/9 成功，发布资产 10/10。


### 2026-11-27 实现与验证检查点

- 三端后端和界面已实现，当前 TASK-001/002/003 已完成，TASK-004 进行中。
- Probe/安卓临时 TCP 经 main=1 数据帧；受控 gVisor 两端 HTTPS 下载 64 KiB 成功，错误源、取消、限时、限流、唯一元组已测。
- 插件 native 编译、external-speed/dataplane/measure C 测试、管理 API/CSRF 生命周期通过。
- Linux 独立网络命名空间验证发现仅 FREEBIND 不足以使未分配虚拟源 IP 连出；已修复为单套接字 FREEBIND + TRANSPARENT，实际 TUN 双向 TCP 测试通过。无全局虚拟 IP 赋值。
- Android Kotlin 离线编译通过，旧 deprecated allNetworks 警告保留。
- 浏览器三端真实源码界面在 1280/390 宽度验证 start/stop 请求、直连/节点、Mbps/MB/s；截图位于 temp/ch-external-speed-ui-x67z5b。
- Probe 主包与内部包全量通过；首次全量 mobilecore 有旧 TestMobileVRouteNodeIPPacketUsesCarrierWithoutExitRule 偶发先收到 main=3 RTT 帧，mobilecore 单独全量重跑通过。未隐藏首次失败。
- 主控全量测试与 vet 通过；Probe vet 排除既有 copylocks/unsafeptr 检查通过；Linux router amd64/arm64 构建通过。
- race 测试未执行：WSL 没有 Go，Windows 没有 CGO 编译器；未为可选检查下载依赖。
- 下一步唯一动作：复核修复后的插件构建、Probe 认证测试，然后发布。

- 准备门禁条件已关闭：Go 两端原始 IP 帧 HTTPS 与 Linux TUN 往返验证通过。
- 本地完成门禁：通过；发布门禁待 Actions。首次旧移动端测试偶发问题在完整 mobilecore 回归已连续两次通过，新测试未屏蔽该断言。
- 使用当前工作流匹配的临时预检（沿用用户先前批准范围），ready=true，9 作业/10 资产合同未变。未添加 Docker 或 BE86U 构建。

- 最终 Probe `go test ./... -count=1` 全部通过（主包 52.4 秒、mobilecore 3.1 秒），vet 通过。随后增加本机 TCP 端口保留：只 bind、不 listen/connect，确保临时 gVisor 测速元组不会撞上普通本机套接字；随会话释放，无新增入站服务。相关定向测试通过后提交。
- 最终接收钩子闲置时只检查原子标记；安卓复用现有已解析路径，不对普通包额外解码 JSON。
- 发布提交范围为三端新测速模块/入口/页面、插件 Makefile 与回归、README 和此账本；提交信息 `feat: add direct and selected-exit external speed tests across clients`。

- Windows 交叉编译共享测速测试程序并在 WSL 原生执行成功：任务取消/并发、读取窗口、公开地址校验、端口保留、完整 TCP 数据面 HTTPS 全部通过；Linux amd64/arm64 router 最终构建通过。
- 当前状态：本地验收完成，待发布构建；下一步唯一动作：提交并推送当前已暂存的 34 个文件。


### 首次发布失败与修复

- 提交 bfd65d8bf；首次 run 46006670594，目标版本 v0.4.137。
- 主控、Windows/Linux Probe、Linux router 已构建成功；插件作业失败，日志确认 test-carrier 链接 src/measure.c 后缺 vr_external_tick/start。生产主程序本身已编译成功。
- DEF-001：新模块的测试目标依赖不完整；补齐 test-carrier、test-platform、test-path-selection 的实际 external_speed.c 链接，并将新的有界测速回归加入现有 CI native 检查。工作流文件不变。
- 下一步唯一动作：本地编译全部受影响测试目标，验证后提交修复重发。

- DEF-001 复测通过：三个受影响测试目标全部链接成功；path-selection 与 external-speed 用例通过。首次 run 最终为 failure，未创建可用 release。
- 修复提交范围仅 router_plugin/Makefile、router_plugin/tools/build-ci.sh、任务账本；不改生产功能代码与工作流文件。

### 最终交付与验收（2026-11-27）

- 正式发布：v0.4.138；release https://github.com/fengzhanhuaer/CloudHelper/releases/tag/v0.4.138 。非 draft / 非 prerelease。
- 功能提交 bfd65d8bf7dcec80bfe4482e43076be583ff7593；构建依赖修复 5708bed76fba957fc6d017d9e7a026ec2d9700ed；版本提交 7df7d596c。
- 成功工作流 46007228323，https://github.com/fengzhanhuaer/CloudHelper/actions/runs/46007228323 ，9/9 作业 success；AX56U V2 tar.gz 与 manifest、安卓 APK、三平台 Probe、主控、Linux Router/manifest、LICENSE 共 10 个资产均已通过元数据核对。没有额外下载发布二进制。
- REQ-001：Probe 本地虚拟路由页面“外部下载测速”，本地认证 API 和接口输入限制通过 TEST-001/002。
- REQ-002：插件“节点测速”中新增外部测试，main=1 TCP NAT 路径恢复与单元组隔离通过 TEST-003；native 与 ARM 构建通过。
- REQ-003：安卓路由页入口、反射桥接与 mobilecore 接口，Kotlin 编译、Go 回归、浏览器交互和最终 AAR/APK 作业通过 TEST-001/002/004。
- REQ-004：限时、限流、取消、非 200、私有地址拒绝、节点失败不回退、端口保留与回收路径验证通过；常规数据包闲置钩子只有原子状态检查。
- TEST-001/002/003/004 最终均通过。此前首次 full mobilecore 的已有测试偶发先读到 RTT 帧及首次插件 CI 测试链接依赖错误均保留在前述记录，不覆盖历史证据。
- 回滚：可在线退回 v0.4.136，不涉及配置迁移；无生产设备自动升级，未宣称 Telegram 或三台实机吞吐已验收。
- 结束状态：已完成；无待处理实现或发布项。后续设备升级由用户另行发起。
