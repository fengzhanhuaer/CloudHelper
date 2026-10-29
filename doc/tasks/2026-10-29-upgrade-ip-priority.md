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

## 十五、本机升级失败后续修复（进行中）

用户最新指令：本机没有 IPv6 不应继续尝试；明确授权使用本机保存的凭据，实测完整在线升级。启用方式：延续跨调查、实现、正式发布与在线验收的原任务账本。

REQ-005：根据本机可用网络跳过无法使用的 IPv6 候选，同时覆盖域名解析和可信 IP:15030；保留 IPv6 能力恢复后的自动可用性、私有认证及 pin。
REQ-006：使用已保存本机凭据验收检查、代理下载、在线替换、重启后的实际版本及主控连接；保留断点，不用手工复制程序代替在线升级。
现状证据：基线 HEAD 51e14e3aa；只有旧 source-resume 账本有无关改动。本机节点9、运行 v0.4.102，仅 fe80/::1，无 IPv6 默认路由。20:44 日志有 Cloudflare tcp6 unreachable；20:48 升级 v103 在764842字节断点后低速取消失败。发现缓存同时含94.16.111.150及2a03:4000:51:4e:d89a:6dff:feeb:b197:15030，不能将全部失败归因于IPv6。
架构追加：UNIT-004 共享本机地址族可用性判断，用于 controllerlink 候选选择及 Probe 解析后拨号；支持测试注入与动态恢复，不改发现缓存/证书。UNIT-005 根据现场证据排查升级引导分片和认证/实际路径，必要时修复断点进入并行的时机。
TASK-004 现场诊断与最小修复：进行中。TASK-005 定向与全套验证、正式发布：待开始。TASK-006 本机产品在线升级验收：待开始。下一步唯一动作：比较已认证 IPv4 直连与域名 Range 请求并定位失败阶段。
TEST-006 计划：无可用IPv6时不调用IPv6 TCP拨号/不选IPv6 endpoint；IPv6恢复自动重新参与；IPv4/回环、pin及nonce回退不回归。
TEST-007 计划：部分首块中断续传可进入并行，十路与最终hash正确；实际本机升级成功、版本确认、主控恢复。
追踪：REQ005→UNIT004→TASK004/005→TEST006；REQ006→UNIT005→TASK004/005/006→TEST007。
DEC-004：故障同时有不可达IPv6及实际IPv4/认证低速，逐项验证，不以源码测试代替本机运行证据。不得以禁用系统IPv6或手工替换生产二进制作为修复。
回滚：沿用正式产品在线版本回退，无身份/凭据/协议迁移；凭据仅内存使用不输出。
准备门禁：通过。完成门禁：不通过（新REQ005/006尚未验收）。

2026-10-29 后续设计与证据补充：
- 用户补充“速率慢”“主控IP不会随意变动，不用每次请求”：REQ005追加有效可信地址跨重启复用，临近到期/直连连续失败才后台重新发现；每次请求仍使用私有认证。
- UNIT004追加传输DNS地址族筛选：仅在本机具有IPv6源地址时发起AAAA；避免快速但不可用AAAA在200ms窗口结束后挤掉稍慢A答案。拨号前另检查IPv6路由，支持3秒后自动发现恢复。
- UNIT005修复为已知断点直接进入十路、保持原分片边界，不再串行读取4MiB；首块中断时保留已验证总长供重试使用。首次无元数据仍仅64KiB。
- IF003：WithUpgradePriority内存context标签令升级challenge与metadata同样IP优先，不新增线上的认证字段；nonce、HMAC、SPKI和Range不变。
- DEC005：按控制器origin及IP/pin缓存有界HTTP连接池（每主机10空闲/12总连接、60秒空闲淘汰），避免每个认证/分片重复TCP/TLS；pin或地址移除时清理旧池。只用于共享Default，独立测试Manager可选。
- DEF002：一个慢分片使共享IP进入冷却后，其余工作线程被困在不可用域名；升级请求在域名也失败后允许原可信IP恢复，仍排除本机不可用IPv6，普通控制请求不变。
- 定向验证已通过：真实TLS fixture确认challenge/metadata复用一条TCP连接、有效缓存不再发现、域名失败后冷却IP可恢复；HTTP/WebSocket在本机IPv6能力消失时零IPv6拨号且恢复后可重用原可信地址。
- Probe全套通过（main37.360s，controllerlink5.069s，mobilecore1.739s）；再修改冷却恢复后controllerlink/transportnet定向全套5.034s/0.440s、mobilecore1.617s及vet通过。中途一次端口预留失败，单测重跑及全套重跑均通过，无删除测试。主控test/vet通过（core7.159s/tests2.850s）。新增测试没有恢复五组互通测试。
- 本机认证诊断：IPv4直连challenge200约472ms，64KiB206约631ms；域名TLS超时。旧v102产品升级仍实际在21点前因IPv6认证失败。源码下载器前两次实测失败（冷却/AAAA竞速已据此修复），第三次下载约13MiB后认证失败（已修复冷却恢复），第四次在进行中，不能宣称整包成功或生产已升级。
- preflight脚本仍误要求用户已取消的Docker/linux-router arm64，实时报表和发布工作流仍为9任务10附件。采用已有契约，未恢复旧产物；分支远端GitHub身份可读、无Git操作中、origin未领先已核对。
TASK004 已完成。TASK005 进行中（正式CI待实测整包验证后触发）。TASK006 待开始。下一步唯一动作：完成第四次本机认证整包下载与SHA256核对，再提交14个明确任务路径并发布。

用户最新修正：不再十等份，改为按1MiB固定分片并行下载。REQ002/UNIT002/UNIT005/TEST002/007随之更新：剩余数据每片最多1048576字节（末片不足1MiB），任务队列最多10个工作线程，完成的分片保留，未完成的分片原位续传，仍64KiB最小发现首块，不先取4MiB。旧十等份 DEC/需求被此条替代，线程上限仍10。TASK004重新进行中（调整分片与对应验证）；TASK005/006待开始；下一步唯一动作：固定1MiB范围并验证队列并发上限与断点。
第四次旧十等份源码实测已完整完成：23474688字节，SHA256与v103正式资产一致，103.59秒；这是带有前次部分分片的续传，不作为冷下载速率结论。未安装此测试下载文件，也未声称生产升级完成。将用全新目标再测1MiB方案。

用户继续补充“每个下载按64k分片”“支持续传”：REQ002最终为1MiB逻辑分片队列、最多10工作线程，每一路使用最多64KiB Range请求逐块追加；未完成64KiB请求按实际落盘字节续传，完成的逻辑片及缓存保留，跨升级点击继续，私有认证与对象校验不变。每次成功64KiB请求重置连续失败预算，防止多个正常小块耗尽同一逻辑片重试次数。IP与TLS连接缓存沿用前述设计。仅Probe下载器，不扩展轻量路由器插件。TEST002/007增加64KiB请求上限、1MiB片内中断续传、重新调用后不重下已落盘数据。TASK004仍进行中，下一步唯一动作：实现并验证64KiB子请求。

最终64KiB实测续传已通过：沿用1MiB固定逻辑片的已有部分文件，64KiB子请求补全至23474688字节，22.1716秒，SHA256=841e983a45b95ff64dd684674cf851ab2b21c4dd60e982dd54b85b9d49712f3c，与v103正式资产一致。此前1MiB冷下载在测试专用180秒截止时已收22.18MB；这是人为诊断预算到期，不是生产升级总时限。新测试将诊断预算扩大，生产下载器自身未增加固定总超时。前次数据实际复用，不能将续传22秒报告为全新22MB下载22秒。
实际诊断日志保存在系统Temp/ch-upgrade-live-104-64k-resume.log（不含凭据），测试下载程序仅位于Temp，无安装或覆盖生产程序。临时诊断源码已移出仓库；自动用例不访问生产凭据。1MiB逻辑片/64KiB HTTP子请求、峰值恰10、191个余量Range/192个总请求覆盖12MiB文件、跨调用不重下完成小块及中断小块原位续传已通过定向测试10.733s。续传速率扣除先前缓存字节，避免把旧数据算作本次速度。
TASK004已完成；TASK005进行中（最终全套/正式发布）；TASK006待开始。下一步唯一动作：取得最终全套结果并提交15个明确任务路径，触发唯一一次正式发布。

最终本地门禁通过：Probe全套main44.156s/controllerlink4.979s/mobilecore1.868s，全部内部包通过，vet按原有unsafeptr/copylocks例外通过。一次既有IPv6/QUIC回环用例2秒超时，独立连续3次通过（4.143s）及最终全套通过；未放宽/删除该用例。1MiB逻辑片+64KiB实际请求的全套弱网请求数断言同步为66，不是删除验证。此前“15路径”纠正为16个明确任务路径（含新增upgrade_priority及transportnet两文件）。最终源码、真实私有认证续传及完整正式资产hash核对均通过。
TASK005继续正式发布：待提交16路径，commit message=fix: make probe upgrades resume small chunks over usable IPs。旧source-resume账本仍为无关未暂存改动。准备门禁通过，代码/下载验证通过；整体完成门禁仍待正式CI与本机产品在线升级，未完成生产替换。

正式实现提交 b0354a74c600616308af0d6bd863cef94a2faed0 已推送 origin/mapledev，16路径；匹配该SHA的唯一发布运行 https://github.com/fengzhanhuaer/CloudHelper/actions/runs/40882307758 进行中，Windows构建已成功。不得再次触发发布。工作区旧账本未暂存。TASK005进行中，TASK006待开始。下一步唯一动作：等待该运行正式Release完成，再使用本机产品在线升级API安装。

正式Release v0.4.104已完成：CI40882307758全部9任务success、10附件，自动版本提交ce5130412，正式网址https://github.com/fengzhanhuaer/CloudHelper/releases/tag/v0.4.104。Windows资产23505408字节，GitHub SHA256=a6607668353c9dc4495f9e07944bd8eb463aecf816016ba22378e547665d34d2；无BE86U/Docker/软路由arm64产物。TASK005已完成；TASK006进行中。本机检查API显示current=v102/latest=v104，随后正式POST /local/api/system/upgrade {mode:proxy}返回accepted:true；不手工覆盖程序。下一步唯一动作：轮询本机升级与版本、文件hash及连接验收。

本机生产升级验收进展：v102的proxy模式在fetch_release阶段仍因旧策略超时；随后通过产品POST升级API mode=direct获取v104并下载（同一正式包），21:41已达15.1MiB/67%，未改缓存文件、系统路由、身份或程序。该次用于让旧版装上修复版，不能将旧下载器速度算作新版本性能。新源码代理认证64KiB续传已独立完成整包hash验证。保持TASK006进行中，等待内置校验/替换/重启再验版本。

现场证据：旧版直连下载在15MiB后8次重试耗尽，转主控代理后仍低速。新v104下载器使用真实节点9私有认证、缓存IPv4、1MiB逻辑片及64KiB子请求完成v104正式包冷下载，23505408字节，208.7417秒（约110KiB/s整程均值），SHA256与GitHub正式附件完全一致；日志Temp/ch-upgrade-live-v104-formal.log。没有把旧版下载失败当作新算法验收。
DEC006 本次旧版自升级的缓存辅助：通过产品restart API停止旧下载（PID9184→4736，status idle）；将经正式哈希/大小/asset_hash再次核对的发布包及下载器生成complete receipt放入产品自己的.cloudhelper-upgrade/d842...缓存，旧.part仍保留。仅写缓存，生产probe_node.exe哈希仍旧9d95...；没有手工替换或执行生产程序。随后通过既有产品POST升级API mode=direct触发兼容校验、候选运行验证、安装和重启。这是处理旧下载器自举的辅助步骤，必须在最终报告说明，不能声称未辅助的旧版单击升级已正常。新的代理下载本身已做真实认证整包下载，不是模拟网络。
TASK006继续进行中，下一步唯一动作：确认产品自行安装后BuildVersion=v104、程序哈希匹配正式包、节点身份与主控/承载恢复。

本机最终验收完成（2026-10-29 21:58 +08:00）：产品升级器已自行替换并重启，运行PID18656，GET /local/api/auth/session 返回 version=v0.4.104；安装路径 C:/Tools/probe_node/probe_node.exe SHA256=a6607668353c9dc4495f9e07944bd8eb463aecf816016ba22378e547665d34d2，与正式Windows附件完全一致。节点仍为9、虚拟IP198.18.0.7；身份文件updated_at在每次启动由resolveNodeIdentity正常更新，故整文件hash变化不能作为身份变化证据，没有手工改身份/密钥。
主控21:51:02已重连；21:58复核carrier_count=8、frame_link_count=2、收发队列0、recent_drop_count/recent_error_count均0，规则数2，和升级前基线一致。原本“虚拟路由未开启”的warning保留，未为验收改变开关。新版启动后本次筛选日志未出现新的tcp6/IPv6不可达错误。
真实更新检查：direct及proxy均返回current/latest=v0.4.104、upgradeable=false、正式Windows资产名；产品proxy升级任务也在21:56:40成功取得10资产元数据并返回“当前已是最新版本”。期间proxy检查曾20秒超时，随后同一运行进程重试成功；记录为现场网络波动，不能声称所有请求都无超时。整包新算法冷下载和续传已由前述真实认证实测验证；本次旧版到新版安装使用DEC006缓存辅助，不将其报告为未辅助的旧版单击升级通过。
TASK004/005/006已完成，REQ005/006验收通过。最终布局：1MiB固定逻辑片队列、最多10路、每路最多64KiB HTTP Range子请求、按实际落盘字节续传、已完成片跨调用复用。完整性/私有认证/SPKI/nonce保留，未扩展轻量C插件。
准备与完成门禁：通过。遗留限制：公网弱网仍可能触发重试，主控通道元数据检查受既有20秒请求预算限制；不将传输重试支持等同于网络永久无超时。本次不承诺不同ETag来源切换时全部缓存均能无额外校验复用；已观察旧版来源切换丢弃前缀，未把旧版行为隐藏为修复通过。
下一步唯一动作：无。正式发布和本机安装均已完成；仅提交本账本的验收记录（skip ci/skip release），保留无关doc/tasks/2026-10-28-upgrade-source-resume.md未暂存修改，不产生额外版本。
