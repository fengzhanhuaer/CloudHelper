# 任务：Probe、安卓、路由器按规则本地选择出口与通用出口

- 状态：已完成（正式发布已核对；生产设备未升级）；日期：2026-10-29 Asia/Singapore。
- 启用方式：明确跨配置、选路、出口承接、界面、验证和发布的长任务。
- 用户最新指令：2026-11-05插件本地允许选择直连或拒绝（详见第十六节）；此前Probe每条规则独立本地出口；安卓修改现有选路，选择仅本地保存不再同步主控，添加恢复默认；插件同样加入。三端均不编辑主控规则内容，出口按请求通用承接。

## 一、需求定义

REQ-001：主控规则是默认配置。Probe按规则ID独立保存出口覆盖，重启保留，不上送或修改主控规则；同步主控新规则不静默覆盖本地选择。
REQ-002：本地页面展示规则、主控默认、当前选择，可单条跟随主控或一键全部恢复；规则内容和动作只读，仅代理规则可选出口。
REQ-003：DNS/Fake IP、CIDR、HTTP/SOCKS和TUN选路采用本地出口；Fake IP映射地址及主控共享库保持不变；新请求使用新选择，不重建承载连接。
REQ-004：已认证路径末端的出口按请求承接TCP/UDP/Fake IP/CIDR，不再限于主控把该规则指定给自己；私有认证、拓扑/租户授权、目标检查保留。路由器作为出口也核查兼容。
REQ-005：有针对性的功能测试、API持久化及浏览器交互验证后按正式流程发布。既有用户未提交账本不混入本次提交；不恢复五组耗时互通、不发布BE86U、不SSH/SCP替换生产二进制。
REQ-006：安卓现有选路入口改为本地覆盖，恢复默认，主控同步保留本地选择；不再调用主控规则更新API。
REQ-007：路由器插件增加规则级本地出口/恢复默认，保存JFFS并重启保留，仅用户变更写盘；热应用不重启承载。

## 二、总体架构

现状：probe_virtual_router.go持有主控原始规则及Fake IP库；规则匹配返回默认出口。backup_exit/exit_netstack/exit_target/vroute_proxy_route按默认出口校验，阻止自选节点承接。
目标：独立本地出口覆盖文件、按规则ID覆盖匹配结果副本，原规则/共享Fake IP库不变。远端收到认证路径后以路径末端决定出口职责，不重新以出口本机规则决定来源请求。
IF-001：新增本地会话保护API GET/POST /local/api/virtual_router/exits，返回只读规则、默认/有效出口和候选节点；POST只接受{overrides:{规则ID:节点ID}}覆盖或{reset:true}恢复，拒绝规则内容写入。
IF-003：Android桥VRouteLocalSettings(主控地址/节点身份/configDir)返回本地有效出口视图，SaveVRouteSettings接受exit_nodes或reset，只写本机文件；独立文件原子保存，Fake IP元数据同步而地址/TTL保持，mobilecore全套与WebView桥模拟payload验证通过。
IF-004：插件status返回只读route_rules摘要，configure接收exit_overrides规则ID到节点ID表，已保存选择可保留失效节点以便展示，新选择须当前已知规则/节点，最多32；CSRF认证、JFFS持久化、热应用和UI保存/重置验证通过。
IF-002：现有链路协议与主控配置保持兼容；无新认证旁路。旧出口不能通用承接时必须明确报错，不伪称兼容。
DEC-001：本地覆盖独立文件，不写入主控route config缓存或上报；主控普通同步只更新默认值。删除规则的覆盖不生效，重新使用相同ID仍按持久化选择。
DEC-003：插件固定最多32条本地覆盖，配置仅显式保存变更时写JFFS；空表不改变旧配置格式。旧出口版本对非默认请求仍可能拒绝，来源和出口都需升级。
DEC-002：本地明确选择后不偷偷回到主控备用出口；取消覆盖才恢复主控主/备逻辑。不可达/节点删除在界面提示并保留选择，避免无声改路。

## 三、单元设计

UNIT-001：probe_virtual_router_exit_settings.go，本地覆盖加载/校验/原子保存/重置，状态和只读规则视图。
UNIT-002：probe_virtual_router.go及相关选路入口，匹配结果副本应用覆盖；Fake IP选路按有效规则获取出口，映射库不污染。
UNIT-003：probe_vroute_proxy_route.go、exit_netstack/exit_target/fake_ip_recovery，认证路径末端通用承接，维持合法目标与映射验证。
UNIT-004：local_console.go与local_pages/virtual_router.html，独立出口API及规则级选择，重置不影响其他设置。
UNIT-005：核查router_plugin出口限制，按同样已认证路径语义适配必要入口。
UNIT-006：Android现有规则选择UI/mobilecore设置和选路，独立本地覆盖。
UNIT-007：插件配置固定上限覆盖表、policy有效出口与Web UI；原始主控规则仍只读。

## 四、执行任务

TASK-001 调查与设计：已完成。TASK-002 本地覆盖与选路：已完成。TASK-003 通用出口与兼容：已完成。TASK-004 UI/API及验证：已完成。TASK-005 正式发布：已完成。
下一步唯一动作：无剩余实施动作；生产设备可从产品入口升级，来源与所选出口都应采用支持通用出口的版本。

## 五、测试与验证

TEST-001：覆盖副本/多规则独立/主控同步保留/重启与恢复/禁止修改匹配内容/非法节点输入。
TEST-002：域名、CIDR、Fake IP、HTTP/SOCKS与TUN选到本地出口，保留默认备选逻辑，不改变共享映射。
TEST-003：非默认出口能承接认证请求，错误路径/未知映射/非法目标仍拒绝；往返数据验证。
TEST-004：会话API认证、保存/重置、桌面/手机版UI真实payload；必要Go/C组件检查和正式CI。
已验证：Probe新增覆盖/API/通用路径测试通过(1.782s)；Android settings与Fake IP默认分配检查纳入mobilecore全套通过(1.991s)。原生C Fake IP/CIDR UDP往返及SNI远端TCP回显通过，出口本机默认不同仍可承接，LAN ACL和错误响应来源仍拒绝。插件管理API生命周期通过。日志在系统临时目录ch-local-exit-*.log，构建在WSL UbuntuServer /tmp/ch-exits-*.log。

## 六、端到端追踪

REQ001→UNIT001/002→TASK002→TEST001；REQ002→UNIT004→TASK004→TEST004；REQ003→UNIT002→TASK002→TEST002；REQ004→UNIT003/005→TASK003→TEST003；REQ005→TASK005→TEST004/正式CI。
REQ006→UNIT006→TASK004→TEST001/002/004；REQ007→UNIT007→TASK004→TEST001/002/004/C热应用测试。

## 七、冲突与风险

工作区仅旧任务账本doc/tasks/2026-10-28-upgrade-source-resume.md未提交，保留不修改。通用出口必须在已认证路径内工作，不开放匿名公共代理。Fake IP共享地址不能按来源出口重复分配。

## 八、缺陷

DEC-004：发布skill静态preflight仍要求已退役Docker及Linux-router arm64项，当前仓库约束测试明确禁止这些项。已暂停推送核对，live release.yml、历史v0.4.100发布10产物、TestReleaseWorkflowDefinesLinuxRouterArtifacts完全一致；沿用当前已验证契约，未修改工作流、未恢复BE86U或互通组。
DEF-001：本次原生插件状态新增规则列表未防御策略尚未就绪的NULL，测试启动网关时崩溃；已加入就绪检查，管理API生命周期复测通过。
TEST基线：首次Go全套仅既有 Windows TCP/UDP测试端口保留失败（TestProbeVRouteProxyListenersWorkWithoutTUN），单独复测通过；mobilecore全套通过，准备最终全套。测试夹具缺失规则名/缓存封装的假设错误已修正，未放宽行为断言。最终go -C probe_node test -p 1 ./... -count=1全套通过（主包37.496s、mobilecore2.223s），未改测试、无跳过。主控test全套及vet通过，Probe vet -unsafeptr=false -copylocks=false通过（沿用既有排除），仓库发布契约测试通过。

## 九、回滚

本地恢复主控出口清空独立覆盖文件；旧版忽略独立文件，无配置迁移。生产程序仅通过正式产品在线升级回退。

## 十、门禁与检查点

准备门禁：通过。已确认IngressPath按runtime身份和userID验证所有路径边，通用出口仅放在该边界后；Fake IP共享库保留原始出口，来源按当前有效规则选路。完成门禁：通过。实现、组件/界面验证及正式产物均已核对；未把CI或模拟页面结果当作生产设备业务验收。

## 检查点：实现验证完成

三端页面在Playwright 1366x1000和390x844通过渲染、保存实际payload、恢复默认、原条目保留和零页面异常检查（Browser plugin not available，模拟固件资源/本地API/安卓桥）。截图系统临时目录ch-exits-{plugin,probe,android}-{desktop,mobile}.png。C core/UDP Fake IP与CIDR真实往返/SNI远端TCP回显/本地管理API持久化与热重置通过；同配置保存不重写JFFS。插件覆盖上限32，主控规则列表最多2048只读摘要，不复制匹配内容。普通Probe/Linux-router与安卓均不改主控默认规则；旧出口要同步升级。

回滚补充：插件有本地覆盖时先点击恢复默认再退回旧版，否则旧版严格配置解析不认识exit_overrides。本轮未在线升级生产设备；仅正式发布，安装/升级由产品入口另行触发。既有旧账本保持未暂存。

## 检查点：正式CI进行中

功能提交28dec88ce3b6cf38774632a1b9796697f55d773f已推送origin/mapledev。工作流40798633390：https://github.com/fengzhanhuaer/CloudHelper/actions/runs/40798633390，自动生成v0.4.101标签；Probe Windows/Linux amd64/arm64、主控与Linux-router amd64已完成，安卓/AX56U插件仍进行中。Linux-router amd64/arm64交叉构建也已本地通过（arm64只作为编译验证，不恢复已退役发布产物）。最终C复测全部通过，diff --check和JS语法通过。

## 最终交付：v0.4.101

- 功能提交：28dec88ce3b6cf38774632a1b9796697f55d773f；workflow生成版本提交/标签：d230d172b3db1903e051fe284ea54741d9467667 / v0.4.101。
- CI 40798633390 九个任务全部success，正式Release：https://github.com/fengzhanhuaer/CloudHelper/releases/tag/v0.4.101，非draft/非prerelease，10个资产齐全（主控、三款Probe、安卓APK、Linux-router amd64及清单、许可证、AX56U插件及清单）。没有BE86U、Docker或Linux-router arm64发布。
- 下载正式插件核对：tar.gz SHA256 0ea2c4156394c45c51855d44b7dd82e2530aed390d4709a79d531bda4c2f940d；页面与本次源码逐字节相等，程序SHA256 8b7baee756e981186c1c12872acde50e669e7b7273ff68ca056871f647ed2f82与manifest一致，manifest版本v0.4.101、ABI PASS。
- REQ001/002/003/006：Probe与mobilecore全套、API文件持久化/同步/恢复检查及三端UI payload均通过；REQ004：认证路径/租户/目标限制测试及C UDP/TCP真实往返通过；REQ007：配置边界/重启保持/同值不写盘/热重置及CI固件ABI通过；REQ005：正式发布、包与页面/程序哈希通过。
- 保留边界：未在线升级生产设备、未做手机真机VPN或路由器现场业务验收。旧出口版本需同步升级；私网依旧需要网关ACL。插件回退旧版本前应先恢复默认。
- 交付文档仅补记验收，提交带skip ci/skip release，不再触发版本递增。既有2026-10-28账本保持未暂存。

## 后续调整：Probe 独立规则出口标签（2026-10-29）

- 状态：已完成；用户最新要求：Probe 出口规则不展示匹配条目，仅选择出口/恢复默认，单独一个 tab。
- REQ-008：保留每条规则独立出口选择，隐藏域名/CIDR 等 entries，独立“规则出口”标签；保存本机与恢复主控默认可用，其他标签和设置保留。
- 架构/UNIT-008：仅调整 local_pages/virtual_router.html 的面板归属、tab/hash 切换和渲染，不更改覆盖 API、匹配或出口运行逻辑。IF-005：#routeExits 可直达并刷新保持。
- TASK-006：界面调整及浏览器验证，已完成；TASK-007：正式发布与附件核对，已完成。下一步唯一动作：无。
- TEST-006：Browser plugin not available，复用现有 Playwright；1366x1000 与 390x844 验证独立 tab、隐藏条目、原标签保留、保存真实 payload、恢复默认 payload、hash 刷新和零页面错误。现有本地 API 定向测试及正式 CI 复核。
- 追踪：REQ008→UNIT008/IF005→TASK006/007→TEST006。DEC-005：沿用此前用户按规则独立选择，不改成一个全局出口；本次限 Probe（含复用该页面的 Linux-router），安卓/插件未要求调整。
- 工作区：仅旧断点账本有未提交修改，保留。回滚：还原页面后通过正式在线升级，不迁移配置。准备门禁通过，完成门禁待验证与发布。
- UNIT008 已实现：独立 #routeExits；两列规则名和本机出口，主控默认出口合并到“跟随主控”选项；只保留保存/恢复默认两个按钮。手机下不横向裁掉选择框。其他八个标签和规则级覆盖存储/选路不变。
- TEST006 已通过：原始完整 HTML + 模拟认证本地 API，Playwright/Chromium 桌面 1366x1000 与手机 390x844；页面身份、非空、无错误覆盖层、零 pageerror、全部旧 tab、隐藏匹配条目、POST {overrides:{domain:'3'}} / {reset:true}、刷新 hash 和已选出口保持均通过。截图 Temp/ch-probe-exits-tab-{desktop,mobile}.png，已目视检查手机截图。无生产页面部署/实机验证。
- 发布契约：live release.yml 与刚完成的 v0.4.102 一致；preflight 旧脚本仍要求已退役 Docker/router arm64，沿用已记录 DEC004 的当前契约核验，不恢复旧产物。分支/远端/GitHub API、上游未领先、diff --check 均确认。
- 最终本地验证：`go -C probe_node test -p 1 ./... -count=1` 全套通过（主包 38.316s、controllerlink 4.882s、mobilecore 2.031s）；仅页面和任务账本修改，无需重跑未变更的主控/C 业务套件，正式工作流仍核验全部产物。
- 发布进行中：提交 a2de4541dea7cd179c168cbc1748b7f871bb53e0 推送 mapledev，匹配 CI 40864941839，https://github.com/fengzhanhuaer/CloudHelper/actions/runs/40864941839 。仅两个任务路径，旧断点账本未纳入。下一步唯一动作：等待正式构建并核验产物内嵌页面。
- 正式交付 v0.4.103：https://github.com/fengzhanhuaer/CloudHelper/releases/tag/v0.4.103 ，自动版本提交 a483b88d7。CI 9 任务全部成功，10 个正式附件/名称/大小/digest 已通过 GitHub Release API 核对，无 BE86U 或旧产物恢复。Windows 程序 23474688 字节，API digest 841e983a45b95ff64dd684674cf851ab2b21c4dd60e982dd54b85b9d49712f3c。
- 附件重下载限制：Windows curl 报 missing close_notify、gh api 报 unexpected EOF、WSL curl 报 partial file；只取得截断文件，未将它当正式完整程序运行或声称本机 SHA/内嵌字节核验成功。本轮资产来源由匹配提交 CI、tag 和 API digest 确认；页面原文经真实浏览器验证且由既有 go:embed 编译，下载不稳定不构成源码/发布失败。设备在线升级和生产页面未执行。
- REQ008 验收完成：独立标签、隐藏条目、两列布局、规则级选择/保存/恢复、hash 刷新、旧标签保留和桌面/手机交互已通过；完成门禁通过。只改页面和账本，未改变服务端/路由/存储/手机与插件页面。回滚无需迁移配置，下一步唯一动作无。

## 十六、2026-11-05 插件本地动作选择（已完成）

用户最新指令：路由器插件本地可以选择“直连”或“拒绝”，截图中的主控直连规则也应允许本机覆盖。本次仅修改C插件，Probe/安卓原有配置接口不扩展。此前REQ002“动作只读”对插件被本次REQ009替代；主控原始规则内容与默认值仍只读。

### 需求定义
REQ-009：插件所有规则可选跟随主控/直连/拒绝；原代理规则仍可选节点。保存与恢复只作用本机，重启保留，重复保存不写JFFS，保存不重启主控或承载。
REQ-010：本机动作在DNS、SNI、真实IP/CIDR和已缓存Fake IP新流量上生效；已有TCP继续原出口；默认Fake IP映射不被本机覆盖污染。主控直连CIDR改拒绝时同步补齐本机捕获路由。
验收：浏览器选择/保存payload/恢复/刷新一致；API拒绝未知规则/非法动作；真实C DNS返回真实地址或REFUSED，Fake IP转本地直连不再发往远端、拒绝无包；回程与既有流保留，重复保存mtime不变。

### 总体架构与接口
IF-005：现有configure.exit_overrides扩展值@direct/@reject，保留节点ID旧格式。保留@前缀以免与节点名字冲突；仅这两个特殊值可用。默认空表跟随主控。UI所有规则显示动作选项，非代理规则不增加节点选项。
DEC-005：DNS仅对动作覆盖改变动作；节点覆盖继续用主控原始出口申请共享Fake IP，转发时再应用本机出口。旧Fake IP按缓存rule应用本机动作，direct在本机解析/NAT，reject丢弃。远端通用出口按来源认证请求承接，不套用出口本机动作。
DEC-006：只对主控direct被本机reject的CIDR增加捕获；其他内核路由沿用原发布规则。构造短生命周期路由视图，不改主控策略，不为所有direct CIDR额外加用户态转发。变更失败保留旧有效配置与连接，5秒后重试。

### 单元设计
UNIT-008 config.c/main.c/policy.c/web：特殊值解析、当前规则校验、动作副本覆盖和下拉框；CSRF/32条上限/原子去重写盘保持。
UNIT-009 dns_service.c/dataplane.c：DNS缓存快速路径尊重动作；异步映射解析支持本机direct与通用远端出口；Fake IP reject/direct新流处理，TCP原路径保持。
UNIT-010 platform.c/engine.c/policy_update.c：本机动作所需的CIDR视图在启动、热更新和主控同步时一致应用；持有旧内核路由直到成功。

### 执行任务与测试
TASK-006 实现动作覆盖及捕获：进行中。TASK-007 组件/API/页面验证：待开始。TASK-008 正式发布核验：待开始。
TEST-005 配置/策略及API：特殊值持久化、未知规则拒绝、重置与无变更mtime、无worker重启。
TEST-006 数据路径：DNS动作与Fake IP/真实IP拒绝/直连、新旧TCP、默认映射不变、CIDR增删及失败回滚。
TEST-007 真浏览器桌面/手机：选项、实际payload、保存回显/刷新/恢复与主控默认不变；页面异常记录。Browser plugin not available，使用本机已有Playwright，不安装依赖。
TEST-008 必要Go/C回归、正式CI与附件；不恢复五组耗时互通、不发布BE86U、不手工装生产程序。
追踪：REQ009→UNIT008→TASK006/007/008→TEST005/007/008；REQ010→UNIT009/010→TASK006/007→TEST006/008。

### 门禁、风险与回滚
基线89b833f93，仅旧source-resume账本存在无关未暂存修改，保持不动；无需新竞争账本。准备门禁通过：范围/接口/验收/单元/测试/回滚明确。完成门禁不通过（待实现验证发布）。
回滚：先点恢复默认清除特殊值，再通过正式在线升级回旧版本；未迁移主控/共享Fake IP数据。CIDR更新失败保留旧有效动作，状态日志明确重试。
已知事实：现页面把direct/reject写死“由主控管理”，后端只校验代理规则及节点，Fake IP转发只读取覆盖出口，故仅改UI不足以实现。
下一步唯一动作：实现IF005及DNS/Fake IP/CIDR有效动作处理。

2026-11-05 实现与验证检查点：TASK006已完成，TASK007进行中。IF005特殊动作、全规则UI、DNS缓存/完整查询、缓存Fake IP直连/拒绝与异步解析均已落地；CIDR使用只读投影视图在启动/主控更新/本机热更新时应用，失败保留旧有效动作与连接，重复保存不写盘。
TEST005已通过真实C管理dispatcher：已知direct/reject/proxy规则配置保存、加载、未知规则/非法动作拒绝、工作代数不变、mtime不变与恢复默认。新增test-exits加入原CI短测试循环，无新增互通组。TEST006通过C core/DNS/Fake-IP NAT往返、cold direct异步DNS、CIDR增删/失败回滚、policy热更新保留socket/TLS/NAT、SNI分段ClientHello直连/拒绝及已建连接保留、原管理进程重启持久化。
环境区别：WSL UbuntuServer新版curl将既有CURLOPT_PROTOCOLS弃用警告视为错误；仅本地构建加-Wno-error=deprecated-declarations，其他Werror保持，不改生产兼容代码。DNS用例53端口受当前网络/权限影响，改用unshare -Urn并仅启用隔离lo后通过，未改变宿主DNS。
TEST007已通过真实Chromium1280x1000及390x844：原ASP页面连接临时本地HTTP桥及原生C管理dispatcher，真实CSRF/payload保存（direct→@reject、reject→@direct、proxy→@reject）、刷新保留、主控默认栏不变、恢复空表；无pageerror。无浏览器插件，使用frontend-testing-debugging的Playwright后备，既有依赖，无安装。截图与payload位于系统Temp/ch-router-actions-browser/；没有将夹具页面当作生产华硕后台验收。
TEST008：主控全套test/vet通过（core7.537s/tests3.521s）。Probe全套首轮仅既有TestProbeVirtualRouterTUNPacketDropsFakeIPWhenExitCarrierUnavailable断言frame links=1/want0失败，源码Go未改；正在单独三次及全套复查，不删除或放宽断言。日志Temp/ch-router-actions-probe-test.log。
发布预检沿用既有DEC004：静态脚本仍要求退役Docker/linux-router arm64，live工作流仍9任务/10附件、BE86U仅ABI夹具，禁止恢复旧产物。origin/mapledev与HEAD一致，GitHub身份/远端读取已核对；不改工作流契约。下一步唯一动作：取得最终Probe回归结果并完成差异核对，随后仅提交本任务文件发布。

最终本地门禁通过：Probe单独失败用例连续3次通过（1.735s），最终全套通过（main44.093s、controllerlink4.967s、mobilecore1.654s及全部内部包），vet按既有unsafeptr/copylocks例外通过。主控全套/vet通过。未修改任何Go测试断言/源码；初次失败按原样记录。C新增冷Fake IP本机direct异步解析与回程通过；SNI用例覆盖主控proxy→本机direct/reject、reject→direct、direct→reject，原流保留。没有新增五组互通。
TASK006/007已完成，TASK008进行中；REQ009/010本地实现与验证通过。准备门禁通过；完成门禁待正式CI/附件核对。仅提交本账本与21个router_plugin路径（含新test_exits.c），共22路径；不包含无关source-resume账本。提交说明feat: allow local direct and reject choices in router plugin。下一步唯一动作：提交、推送并监控匹配SHA的唯一正式发布。

发布检查点：实现提交6771b1157abf7856dc72d4a0233c83abd8022e35已推送origin/mapledev，22个明确路径，旧source-resume账本未暂存。唯一匹配该SHA的正式CI为41973090286：https://github.com/fengzhanhuaer/CloudHelper/actions/runs/41973090286 ，进行中。不重复触发发布；下一步唯一动作：等待此运行完成并核验Release附件内页面/manifest。
页面范围补充：390px视口沿用原华硕插件固定宽度容器，截图页面宽约768px，控件通过横向滚动可操作；本次没有改动布局，不将此报告成移动自适应重排通过。桌面及窄视口交互、保存/恢复与无JS异常均已验证。

正式CI41973090286首轮失败：所有其他构建成功，华硕Build and Verify AX56U V2 Plugin Package步骤（含本次core/API/DNS/Fake IP/SNI/CIDR、ARM编译与包ABI）已成功；后续既有Verify Router Address Updates Preserve Established Connections在测试夹具初始化iptables -P INPUT DROP时exit3，尚未启动被测二进制，错误日志Temp/ch-router-actions-ci-failed.log。并非动作断言失败，未跳过该门禁。先仅重跑失败job并打开debug，验证执行机环境是否瞬态；若重复再补充环境诊断，不直接删除验证或放宽断言。TASK008仍进行中，完成门禁未通过，v105尚无正式Release。

DEC-007 / DEF-002 CI执行环境：首轮日志确认runner Ubuntu26.04.1（image ubuntu-26.04/20261025.179.1），上次成功v104使用24.04.5（image ubuntu-24.04/20261025.353.1）。重跑仍在Verify Router Address Updates Preserve Established Connections失败，BuildPackage已再次通过；首轮明确iptables初始化exit3。重跑完整debug日志下载遇unexpected EOF，未从缺失日志推断更细内核原因。将仅华硕job的runs-on固定为已验证ubuntu-24.04，Ubuntu18.04 ABI容器和全部网络断言保持；夹具run函数只在异常时补打原命令stderr再原样抛出异常，不改变通过条件。
此为关闭发布阻塞所需的环境修复，主控/Probe平台任务、9任务10附件契约和固件ABI不变。发布契约全组TestReleaseWorkflow通过1.519s，Python AST通过；产品业务源码未再改，不重复已通过的组件套件。准备门禁通过，最终环境兼容仍由新CI验证；v105因发布门禁失败尚无Release，下一正式版本由工作流自动生成，不手改version/tag。
下一步唯一动作：提交上述2个环境文件与本账本，触发新运行并核验完整测试。

环境修复提交feeb9fb66448dd688e0701f386be6381cdd23af6已推送。匹配新SHA的发布运行41974134052：https://github.com/fengzhanhuaer/CloudHelper/actions/runs/41974134052 ，进行中；此运行验证ubuntu24固定后完整门禁，旧41973090286失败记录保留。TASK008继续，下一步唯一动作：等待41974134052并核验其自动版本与正式插件附件。


### 2026-11-05 最终验收与完成摘要
正式Release v0.4.106：https://github.com/fengzhanhuaer/CloudHelper/releases/tag/v0.4.106 。最终CI41974134052全部9任务success（包括完整地址/连接保持测试及主控遥测互通），10正式附件名称/大小/digest已核对，无BE86U/Docker/Linux-router arm64恢复。自动版本提交8f213d117，功能6771b1157，CI环境修复feeb9fb66；v105失败流程未创建Release，没有绕过门禁。
已下载正式AX56U V2安装包与manifest到系统Temp/ch-router-actions-release-106；包128470字节，SHA256=849278787a0a54b944c641b8e1c84f62612a1790ca20dd03e61b367f201fa06e，与GitHub digest及外部manifest一致。解包只读校验所有manifest文件大小/sha256均通过；VirtualRouter.asp与本次源码字节一致并包含@direct/@reject，程序SHA256=b97d4848096d14e3d77e5f64b44e80e9ac94a19238eaf6bea66818ba72572259。未将CI或本地夹具当作生产路由器验收，未连接/升级任何生产设备、未写其JFFS。
REQ009验收通过：所有规则跟随/直连/拒绝、代理规则原节点选项；本机保存/刷新/重启加载/恢复默认、配置mtime去重、CSRF与当前规则校验、工作代数不变均有TEST005/007证据。
REQ010验收通过：DNS缓存及普通请求、Fake IP缓存拒绝/本机直连及冷异步解析、真实CIDR捕获增删/失败回滚、SNI动作与已建TCP保留、共享映射出口不改，有TEST006及正式CI证据。IF005/UNIT008–010、TASK006–008、TEST005–008闭合，DEF002环境兼容已由固定Ubuntu24后的完整CI成功关闭。准备与完成门禁均通过。
遗留边界：保持原华硕固定宽度布局；尚未做生产设备在线升级与现场流量验收。用户可从运行状态检查更新/在线升级到v106。回滚先恢复默认再用正式在线升级回退。额外提交仅此账本完成记录，skip ci/skip release，不触发新版本；无关source-resume账本保留。下一步唯一动作：无。
