# 任务：Probe按规则本地选择出口与通用出口

- 状态：进行中；日期：2026-10-29 Asia/Singapore。
- 启用方式：明确跨配置、选路、出口承接、界面、验证和发布的长任务。
- 用户最新指令：Probe每条规则独立本地出口；安卓修改现有选路，选择仅本地保存不再同步主控，添加恢复默认；插件同样加入。三端均不编辑主控规则内容，出口按请求通用承接。

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
IF-001：新增本地会话保护API GET/POST /local/api/virtual_router/exits，返回只读规则、默认/有效出口和候选节点；POST只接受rule_id/exit_node_id覆盖或reset恢复，拒绝规则内容写入。
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

TASK-001 调查与设计：已完成。TASK-002 本地覆盖与选路：已完成。TASK-003 通用出口与兼容：已完成。TASK-004 UI/API及验证：已完成。TASK-005 正式发布：进行中。
下一步唯一动作：提交当前任务文件并推送现有发布工作流，核对正式产物。

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

准备门禁：通过。已确认IngressPath按runtime身份和userID验证所有路径边，通用出口仅放在该边界后；Fake IP共享库保留原始出口，来源按当前有效规则选路。完成门禁：实现与验证通过，正式发布待完成。

## 检查点：实现验证完成

三端页面在Playwright 1366x1000和390x844通过渲染、保存实际payload、恢复默认、原条目保留和零页面异常检查（Browser plugin not available，模拟固件资源/本地API/安卓桥）。截图系统临时目录ch-exits-{plugin,probe,android}-{desktop,mobile}.png。C core/UDP Fake IP与CIDR真实往返/SNI远端TCP回显/本地管理API持久化与热重置通过；同配置保存不重写JFFS。插件覆盖上限32，主控规则列表最多2048只读摘要，不复制匹配内容。普通Probe/Linux-router与安卓均不改主控默认规则；旧出口要同步升级。

回滚补充：插件有本地覆盖时先点击恢复默认再退回旧版，否则旧版严格配置解析不认识exit_overrides。本轮未在线升级生产设备；仅正式发布，安装/升级由产品入口另行触发。既有旧账本保持未暂存。
