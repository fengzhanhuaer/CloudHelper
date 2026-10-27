# 任务：AX56U V2 配置同步与在线升级恢复

- 状态：v0.4.95已正式发布，节点25现场恢复通过；节点26等待局域网页面操作后恢复。更新日期：2026-10-28（Asia/Singapore）。
- 2026-10-28追加：用户要求192.168.52.1也升级，纳入节点25现场验证。
- 用户最新指令：51.1后台可打开但未连接主控，先在52.1测试解决；随后报告51.1的v0.4.94服务不可用。
- 启用方式：明确长任务条件，跨现场调查、C实现、验证、正式发布和产品在线升级。

## 一、需求定义

目标：节点26（HM_A_R，192.168.51.1）能接受合法的新策略并完成在线升级。

|需求|验收|状态|
|---|---|---|
|REQ-001|兼容Probe的空规则entries；保留畸形数据拒绝和鉴权，合法策略在线应用|代码/CI/节点25通过，节点26待恢复|
|REQ-002|升级子进程在严格内存模式下无需fork，SHA256/包校验/回退保留|完成；节点25严格内存策略下在线94→95成功|
|REQ-003|正式发布并经产品CSRF在线升级节点26和25，校验版本/文件hash/现场错误|v95发布/节点25完成；节点26待恢复|
|REQ-004|安卓按其他节点虚拟IP自动选路；三端空entries兼容验证，路径鉴权保留|代码/定向测试/签名APK发布完成；手机现场未验证|

约束：32条相邻链路、每条4槽位；不放宽路径授权；保留可用连接；不恢复五组耗时互通测试；不发布BE86U；不SSH/SCP替换生产二进制。不改变控制器策略内容。

## 二、总体架构

主控签名API -> 插件policy校验 -> 原子发布策略 -> carrier按拓扑授权；升级走产品CSRF API、验证官方包、执行已验证payload。

IF-001：entries为缺失/null/[]时表示空匹配规则；其他非数组仍拒绝。对齐Go切片语义。
IF-002：升级子进程改posix_spawn+POSIX_SPAWN_USEVFORK；锁fd3、描述符隔离与回退保持。

## 三、单元设计

UNIT-001：policy.c 空匹配兼容；test_core.c验证空规则、畸形类型、32/33容量和真实策略。
UNIT-002：upgrade.c 统一spawn，分离SHA失败与解压启动错误；test_upgrade.c/py验证fork失败仍可解包、spawn失败原因及既有安全/回退/下载。
UNIT-003：节点26在线升级与状态/日志核对。
追加节点25（192.168.52.1，RT-AX56U_V2、386_49559_koolshare）同一正式产品在线升级与核对。
UNIT-004：mobile_vrouter.go 节点IP匹配优先于出口CIDR规则，复用现有授权路径规划；安卓和Probe补空规则兼容验证。

## 四、执行任务

|任务|范围|状态|
|---|---|---|
|TASK-001|现场确认和实现，REQ-001/002|完成|
|TASK-002|定向C/既有回退下载验证，REQ-001/002|完成|
|TASK-003|正式发布和节点26/25产品升级，REQ-003|节点25完成；节点26待局域网页面配合|
|TASK-004|安卓节点IP选路和三端空规则兼容验证，REQ-004|代码/测试/正式APK完成|

当前交接：52.1运行v0.4.95，ready=true、controller_connected=true，原虚拟路由和网关代理均已恢复，SNI保持启用。51.1仍离线，SSH超时；已请用户在其页面临时关闭虚拟路由和网关代理后保存，以保留管理进程进行正式在线升级。未收到操作结果，不推定已执行。

## 五、测试与验证

TEST-001：真实签名配置，15节点、18规则、23拓扑、节点26相邻9条；rr-16 entries=null导致C校验失败。
TEST-002：core空规则/无效类型/32允许33拒绝、缺票据拒绝、真实签名策略完整通过。
TEST-003：包装fork为失败，真实解包/CRC/遍历/子进程启动失败/回退/HTTPS并行及断点续传通过。Docker Desktop本机启动失败，使用本机WSL bwrap独立进程/文件系统容器验证，/etc和/jffs均隔离，未改宿主CA或JFFS。
TEST-004：节点25产品CSRF在线94→95通过；未调整overcommit_memory=2/ratio=60，版本、运行二进制和实际/www页面hash均匹配正式包。恢复原enabled=true、gateway_proxy.enabled=true后，PID6477持续运行；主控侧确认节点25在线v95。新缓存15节点（含19）、23拓扑、17规则、9相邻，8个在线对端各4槽位，节点26离线。近期无invalid controller configuration；个别槽位仍有关闭后自动重连，不声称链路零断开。节点26恢复待执行。
TEST-005：安卓无出口规则的节点IP直达、多跳、失效边/缺凭据拒绝、真实VR数据帧及源地址重写通过；mobilecore全套通过。Probe缺失/null/[]规则与既有节点路径回归通过。vet发现原有mobile_vrouter_test.go:71/114测试复制mutex警告，未修改旧帮助器；关闭copylocks后的其他vet检查通过。
五组已删除互通测试不恢复。追加Go mobilecore变更，执行Go与Android对应检查及正式CI。

## 六、端到端追踪

REQ-001 -> UNIT-001/IF-001 -> TASK-001/002 -> TEST-001/002/004。
REQ-002 -> UNIT-002/IF-002 -> TASK-001/002 -> TEST-003/004。
REQ-003 -> UNIT-003 -> TASK-003 -> TEST-004。
REQ-004 -> UNIT-004 -> TASK-004 -> TEST-005。

## 七、决策与冲突

DEC-001：兼容空规则，不删用户主控规则、不绕过鉴权。
DEC-002：旧v0.4.91升级器自身仍fork；若证据确认严格commit限制，可临时提高commit额度仅供一次产品升级，finally恢复原值；不永久修改系统内存策略。
无未解决冲突。release skill旧Docker/arm64断言如仍过时，按现有workflow的9job/10asset核对。

## 八、缺陷记录

DEF-001：空entries使整份新策略被拒绝，旧拓扑导致新增边unauthorized path edge。v95修复，节点25新策略已应用。
DEF-002：升级解包仍fork；strict overcommit=2、CommitLimit150428k、Committed_AS134932k，父进程约44MB，fork承诺不足；错误被泛化为校验失败。spawn修复已通过严格内存策略下真实在线升级。
DEF-003：v94仅修改空规则校验，遗漏published_routes和vr_policy_needs_ip消费者；旧json-c对NULL执行array_length会SIGSEGV。节点25内核LR=0x2e048对应正式ARM二进制published_routes调用，确认非OOM。v95补两处防护和真实系统路由fixture，节点25恢复通过。

## 九、回滚方案

回归时通过产品正式旧版升级回退。现场临时overcommit_ratio必须finally恢复；不重置固件防火墙和整机服务。策略schema不变，无迁移。

## 十、已验证事实

FACT-001：homerouter SSH解析192.168.51.1:10022；productid RT-AX55、odmpid RT-AX56U_V2、386_49559_koolshare。
FACT-002：调查开始时节点26运行v0.4.91且主控已连接；旧缓存8相邻，新主控配置9相邻，容量上限为32。当前26页面报告v94不可用，主控仍保留离线前v91记录，不能将该记录当当前版本。
FACT-003：真实新配置rr-16 entries不是数组（null/缺失），其余初步校验通过；完整配置仅存本机私有临时目录，不入库、不输出认证字段。

## 十一、风险与阻塞

节点26插件v94崩溃且SSH依赖已断开的虚拟链路；用户可在局域网打开后台，等待页面暂停数据面后恢复管理进程。26临时overcommit_ratio曾60→100，失联后恢复尚未验证；不可声称已还原，重启会清除临时值。节点25临时bootstrap额度已恢复60并核实。手机APK未做真实设备验收。

## 十二、质量门禁

准备门禁：通过。代码/CI/节点25现场门禁：通过。整体完成门禁：未通过（节点26恢复待配合，手机未做现场验收）。

## 十三、检查点

2026-10-27：已回复用户上限32、现场相邻9，已定位空规则和fork提交额度问题。尚未修改生产内存参数或升级。
2026-10-27 23:45：d2fcd007c已推送；自动v0.4.93未发布，CI 40533020156的旧IPv6 transport fixture --controller退出时SIGSEGV，安卓/其余编译通过。本机ASAN同一路径通过；发现fixture退出不等待detached TLS discovery，补fixture完成通知后退出，保留真实鉴权断言。下一步定向验证此生命周期修正再推送；不重跑五组旧互通测试。
现场补充：旧缓存8相邻/14节点/22拓扑，新主控9相邻/15节点/23拓扑，故页面停在8，不是容量上限。
2026-10-28：b97a4e912触发CI 40535797732，9job全部success；自动v0.4.94正式发布，10asset（含签名APK和AX56U V2插件），无BE86U/Docker发布。插件包SHA256=16aea47b54915fdbd333e6887c569af06941be2f0fd46253d42ec2dae03037fb；payload=d1777ceb53bb5c6ea6bf919ee56d2581f2d2ea0fb8fcb1ebdf9b9e0350f4c823。
节点26产品CSRF升级进行中；旧升级器严格commit额度临时60->100，Python finally恢复60。节点25升级待执行。
2026-10-28现场缺陷：v0.4.94节点26已安装但服务崩溃，用户要求先集中52.1；26 SSH依赖overlay无法连接，overcommit_ratio=100尚未证实恢复（重启会失效），不得称升级成功。节点25旧升级器ratio60失败，临时80完成产品升级并恢复60；随后同样SIGSEGV。内核LR=0x2e048，正式payload反汇编为published_routes中json_object_array_length(NULL)，r1指向entries字符串，确认漏改platform/policy_needs_ip两处消费。补空规则防护与真实系统路由fixture，准备v0.4.95；25暂存原enabled/gateway开关并暂停数据面保留管理，随后经产品升级和恢复。禁止重复声称94已恢复。

## 十四、完成摘要

2026-10-28：c5f7a670c触发CI [40551933556](https://github.com/fengzhanhuaer/CloudHelper/actions/runs/40551933556)，9job全部success，自动版本提交28dcc7a9f生成[v0.4.95](https://github.com/fengzhanhuaer/CloudHelper/releases/tag/v0.4.95)，10asset含签名Android APK和AX56U V2插件；无BE86U/Docker发布。

正式插件包SHA256：bb4f0589cb7549628474060d233ce707d68710b7a9f8c3c5005027985046fc33；payload：99164a7a3dd677e83045a7b1005b75eed1adb3a09e579aad977a81469d9eec5f；实际/www/user/cloudhelper-router/VirtualRouter.asp：a16f45ad604236bd3f78731418b3696d87ec6602b3e6a7a42bc8a46ff36aa25e。节点25均匹配，active=fff15d63d10c128d。

节点25通过产品接口在线升级、原开关恢复和主控双侧核验：主控94.16.111.150:15030连接，ready=true，主控汇报v95在线，SNI保持启用，内存策略2/60。节点26尚未修复，不作为整体完成；临时ratio100恢复待验证。后续先由51.1局域网页面暂停虚拟路由/网关代理，再产品在线95、恢复原开关、核对内存参数/版本/hash/策略/连接。
