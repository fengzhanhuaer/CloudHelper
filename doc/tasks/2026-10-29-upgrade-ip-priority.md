# 任务：升级代理下载优先直连主控 IP 与十路并行

- 状态：已完成；日期：2026-10-29 Asia/Singapore。
- 用户最新指令：程序升级代理下载 IP 直连优先；Probe Node 分十份并行，速率小于 10k 可断开；路由器插件体积小，保持轻量。
- 启用方式：明确长任务条件，涉及 Go 共享承载、Probe 下载及 C 插件认证、验证与正式发布。

## 一、需求定义

REQ-001：代理升级下载优先已认证发现的主控 IP:15030，不可用则回退域名；保留私有认证和 SPKI 校验。覆盖 Probe/Android 共享承载及路由器插件，普通控制连接策略不变。
REQ-002：Probe Node 代理下载取得总长度/验证器后将剩余内容分为十份并行（极小文件最多按字节数分份），支持断点、Range/对象校验、取消和有界重试。首次只取 64KiB，避免先串行等待 4MiB。
REQ-003：Probe 代理下载每路连续观察 10 秒平均低于 10KiB/s 时主动取消，保留断点并重试/切换通道；首字节有既有空闲超时保护。正常完成的小文件不误断。GitHub 直连下载策略不变。
REQ-004：验证后按现有工作流正式发布；不 SSH/SCP 替换生产程序，不新增 BE86U/Docker 产物，不恢复五组耗时互通测试。

## 二、总体架构

现状：Go controllerlink.RoundTrip 根据健康状态域名优先；升级取 4MiB 后只启动两个工作线程。C upgrade.fetch 的 challenge 和下载目标也均使用普通自适应选择。慢而有响应的域名可以长期占用下载。

目标：Go 仅对 GET/HEAD /api/probe/proxy/download 优先可用 pinned IP，失败沿用新 nonce 回退。Probe 首块缩短，剩余十等份且独立断点，增加下载体低速看门狗。C 增加下载专用目标选择与固定目标 challenge，下载前失败切换下一 IP/域名；写入部分字节后交回原断点重试流程，禁止原偏移重复写。

IF-001：Go 下载路径的内部路由优先级；无 HTTP 认证/协议字段变化，Range/If-Range 原样保留。
IF-002：C vr_controller_link_download_target、vr_controller_auth_at 及精确 endpoint 反馈，仅内部 ABI；普通选择器和原认证函数保持兼容。

## 三、单元设计

UNIT-001：internal/controllerlink/link.go 与 TLS 测试，保证健康域名下 IP 优先、失败新 nonce 回退、错误 pin 不放行。
UNIT-002：upgrade.go/upgrade_parallel.go/transient_error.go，十等份、64KiB 初始 Range、每路 10 秒低速检测，错误可重试，数据与总长校验不变。
UNIT-003：controller_link.c/controller.c/upgrade.c 及头文件，下载目标独立选择、challenge 固定所选目标、失败 IP 冷却与域名回退，已写入内容只通过断点续传。

## 四、执行任务

TASK-001 调查与准备：已完成。HEAD 7c98e2878；仅旧 doc/tasks/2026-10-28-upgrade-source-resume.md 有用户修改，不纳入提交。
TASK-002 实现与定向验证：已完成；范围 UNIT001/002/003。
TASK-003 全套验证与正式发布：已完成。
下一步唯一动作：无。

## 五、测试与验证

TEST-001 已通过：controllerlink 真实 TLS fixture 验证健康域名仍先 IP、常规接口先域名、失败新 nonce 签名回退且 Range/If-Range 不丢、错误 pin 拒绝；主动低速失败只冷却所选 IP。
TEST-002 已通过：十路同时等待栅栏、一次性 nonce/HMAC、完整文件/不重叠范围、单分片中断后原位续传；旧跨 ETag 内容验证/串行降级仍通过。
TEST-003 已通过：持续慢、快后停滞取消并返回可重试错误；健康持续流与快速完成小文件不误断。观察窗参数缩短到 30/40ms，未新增十秒级等待测试。
TEST-004 已通过：WSL 原生全程序编译、test-controller-link 真实 TLS 下载优先和一次性认证断点回退、错误 pin、缓存恢复、域名隔离，test-upgrade 和 test-core 单元通过。make 使用新宿主 OpenSSL/curl 的既有弃用告警例外，其余 -Werror。完整升级安装夹具只允许一次性容器，交由正式 CI。
TEST-005 已通过：`go -C probe_node test -p 1 ./... -count=1` 全通过（main 46.895s、controllerlink 4.970s、mobilecore 1.680s）；最后范围计算和中断夹具增强后定向 `. ./internal/controllerlink` 全通过（7.237s/0.743s）。Probe vet 使用既有 unsafeptr=false/copylocks=false 例外通过。主控全套 test/vet 通过；Linux-router amd64/arm64 交叉构建通过。正式 CI 40827413507 全部 9 任务成功，10 附件与当前工作流一致，无 BE86U/Docker/软路由 arm64 包。正式插件包与内外 manifest、全部 payload 字节数/hash、页面源码比对均通过。
未执行：生产公网速度对比与设备在线升级；发布不等于已部署或实测提速。

## 六、端到端追踪

REQ001 → UNIT001/003 → TASK002 → IF001/002 → TEST001/004，已完成。
REQ002 → UNIT002 → TASK002 → TEST002/005，已完成。
REQ003 → UNIT002 → TASK002 → TEST003/005，已完成。
REQ004 → TASK003 → TEST005，已完成。

## 七、决策与冲突记录

DEC-001：10k 按 UI 现有单位解释为 10KiB/s，单路 10 秒观察窗；不因瞬时抖动断开，不改普通控制请求。
DEC-002：十路只用于 Probe Node，内存紧张的 C 插件保留两路，仅升级直连优先。Android 共享传输继承 IP 优先，分片数量保持现状。
DEC-003：下载体低速属于该 endpoint 失败并进入短暂冷却，使重试可选其他 IP/域名；响应准备缓存的 503 仍按原等待语义，不算低速下载。
冲突：无；旧断点账本为已交付历史且有无关修改，单独保留。

## 八、缺陷记录

DEF-001 已修：健康但慢的 Cloudflare 下载通道不会主动让位 IP。下载优先选择可信 IP，低速取消并冷却该 IP，回退保持认证和断点。

## 九、回滚方案

如新下载策略造成回归，通过正式产品在线升级回退版本；无配置/认证/数据迁移。断点范围文件名包含对象与范围，旧分片不会错拼。

## 十、已验证事实

Go shared transport 覆盖 Probe 和 mobilecore；C 使用独立实现。原 Probe 两线程、4MiB 分片，低速仅 60 秒完全无流量保护。公网瓶颈未现场测速，不能将路径策略问题等同于全部吞吐原因。

## 十一、风险与阻塞

十路会增加主控连接数，固定上限十路；慢链路可能频繁重试，仍受原重试预算约束。C 并行反馈需以实际目标 IP 归属，不能因全局 index 变化误冷却别的 endpoint。

## 十二、质量门禁

准备门禁：通过（范围、验收、架构、单元、测试、已有改动和回退已记录）。
完成门禁：通过（REQ001–004 已实现并经定向、全套、本地构建、正式 CI/附件核对；无范围漂移或遗留失败，原有无关修改保留）。

## 十三、检查点

2026-10-29：用户补充十路并行与低速断开，已纳入 REQ002/003；未修改生产设备。

用户补充插件保持轻量：与 DEC002 一致，C 插件只新增 IP 优先与认证失败回退，不增加并发线程、测速或低速看门狗；原有并行数量不改。

本地验证门禁通过。完成门禁仅待正式 CI 和资产核对；待提交 19 个明确任务路径，旧断点账本修改不纳入。

正式提交 c688476c87dd2cef2d57003c37714bebcac51702 已推送 origin/mapledev。19 个任务路径与预期一致，旧断点账本保持未暂存。匹配该 SHA 的 release.yml 运行 https://github.com/fengzhanhuaer/CloudHelper/actions/runs/40827413507 已排队；不得重复触发发布，待 workflow 自动版本和产物完成。

最终发布：v0.4.102，自动版本提交 247b06385，9 任务成功、10 正式附件。https://github.com/fengzhanhuaer/CloudHelper/releases/tag/v0.4.102 。下载正式附件首次遇到 release-assets 主机连接错误；使用 IPv4 curl 成功，包 SHA256=d0c633d7772f0303f9fa04f7e9bd203cc43030a1e5eaecda68ed291fb3408c42，127593 字节；C 程序 SHA256=15580e9e1216af4f51298cf97c3f1fef1d0848cd3f07d9a900b3b59c654014fa，与包 manifest 一致。临时核验产物位于系统 Temp/ch-upgrade-ip-priority-102，未写入路由器 JFFS。

用户询问服务器侧：已说明主控源码无改动，现有认证、IP:15030、缓存及 Range 能服务多路并发；缓存 8 槽为不同资产数量，并非单文件并发上限。本轮主控仅随统一 Release 重新构建，未对生产在线升级。客户端升级到新版后采用新策略，不声称生产吞吐已提高。

发布预检：内置 preflight.ps1 仍要求已退役的 Docker 和 linux-router arm64，报告 missing markers；实时 release.yml、TestReleaseWorkflowDefinesLinuxRouterArtifacts 和已发布 v0.4.101 均明确禁止恢复它们。采用当前经过验证的 mapledev 自动版本/tag、9 任务/10 附件契约，未修改工作流或版本号。分支/远端/无进行中 Git 操作、GitHub 身份与 Actions/Release 读取均已独立核对；上游未领先。此为旧脚本契约滞后，不以改回旧产物绕过。

## 十四、完成摘要

交付 v0.4.102：Probe/Android 共享下载承载优先可信 IP；Probe 64KiB 首块后十路分片、每路连续 10 秒低于 10KiB/s 主动取消并续传；C 插件仅 IP 优先/失败回退，不新增并发或测速机制。认证、pin、对象/Range 校验保留。全部验收及正式资产核对通过。生产设备在线升级和现场速度对比未执行，需另行操作；无需服务端协议修改。回滚按第九节，下一步唯一动作无。
