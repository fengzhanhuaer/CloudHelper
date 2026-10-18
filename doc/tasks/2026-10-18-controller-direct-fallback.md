# 任务：主控探针专用 IP/TLS 备用连接

- 状态：`完成`
- 用户最新指令：为弱网下Cloudflare不顺畅、代理升级反复失败提供IP:15030备用；Probe、安卓、华硕插件均支持。IP从主域名接口获取，直连自签证书不校验域名；IP入口仅允许探针，Web登录和页面只走Cloudflare。
- 启用方式：明确跨主控/三客户端/协议/持久化/升级/实机验证的长任务，使用manage-long-tasks；沿用已授权发布部署和在线升级流程，不提交凭据。

## 一、需求定义

REQ-001：主控公网IP:15030提供TLS探针专用入口；主域名HTTPS发现接口返回IP/端口/SPKI指纹；自签身份持久化。禁止公网直连Web/登录/伪造转发头越权，保留回环HTTP供Cloudflare和本机运维。
REQ-002：Probe和安卓共用连接选择、证书固定、失败切换和持久缓存；覆盖认证、配置、Fake IP、控制流和主控代理下载；域名正常校验，IP跳过域名但校验下发指纹及有效期。
REQ-003：C插件实现相同缓存/切换/认证/控制/升级行为；升级保留Range续传，不盲目重放不确定的POST，不双开同节点控制会话互踢。
REQ-004：空缓存首次启动、旧主控、DNS/CF失败、断网重启、错误证书、单次nonce和中断续传可测试；发布附件和在线升级实机验证，保留回滚。
REQ-005：按连接稳定性自选域名/IP，超时、慢响应及短控制断线触发备用；连续健康探测后恢复，避免频繁来回切换。节点私有HMAC/单次challenge认证不省略。
REQ-006：主控下载缓存专门放在运行目录下 ./temp/probe-download-cache，按URL哈希合并回源，闲置30分钟清理；Range并行分片与续传，客户端断开不取消共享回源。用户明确 ./temp 指主控缓存，而非要求移动全部临时文件。

## 二、总体架构

已验证现状：生产主控仅127.0.0.1:15030 HTTP，cloudflared回环接入；公网eth0=94.16.111.150。Go桌面集中resolved_dial，安卓mobilecore部分DefaultClient，C控制流/OpenSSL和HTTP/libcurl与升级下载分开。主控一次性challenge禁止同一凭证并发重放。

目标：保留回环HTTP，独立在非回环公网接口同端口绑定TLS监听（不与回环冲突）；专用handler明确只路由/api/probe相关端点，先拒绝其他路径再检查普通认证，不采信转发头放行Web。自签ECDSA身份保存data目录，发现接口提供最多4个IP和SPKI SHA256，缓存最多30天、每小时尝试从正常校验的主域名刷新。NAT部署可环境变量指定对外IP，禁止将Cloudflare A记录误当源站。

IF-001：GET /api/probe/controller/endpoints，经主域名HTTPS，返回version=1、expires_at、endpoints[{ip,port,tls_spki_sha256}]；旧端无接口时保留原连接。
IF-002：HTTPS IP:15030允许/api/probe及已有子端点（认证仍有效），其余路径404；回环HTTP不变；证书不可信/过期时拒绝直连。
IF-003：缓存按主控origin隔离，无secret；切换后重新获取challenge并签名，Range/If-Range保持；POST仅预选健康通道，不重复可能已执行的操作。控制流最多一个已注册会话。

## 三、单元设计

UNIT-001：probe_controller/internal/core/controller_direct.go，新监听、身份文件、IP发现/过滤、发现接口及隔离handler；测试双入口/Web拒绝/重启身份。
UNIT-002：probe_node/internal/controllerlink，共用持久缓存、固定证书、HTTP与WS选择/重试；桌面resolved_dial/upgrade与mobilecore入口接入，外部GitHub请求保持原行为。
UNIT-003：router_plugin控制连接模块，线程安全小缓存/JFFS文件、libcurl固定公钥、OpenSSL现有pin连接、控制/升级下载共用选择；4IP、单文件<8KiB，沿用下载中断续传。

## 四、执行任务

- TASK-001 `完成`：主控专用TLS、认证发现、持久身份；三端共享稳定性选择/私有认证、控制与升级；./temp缓存和双路分片。
- TASK-002 `完成`：测试与限制见下方检查点；已具备正式构建条件。
- TASK-003 `完成`：提交并发布v0.4.76，通过产品接口在线部署主控、25/26插件及9本机Probe，正式产物回读与连接观察通过。
- 下一步唯一动作：无；安卓已发布签名APK，但未进行真机安装或联网验收。

## 五、测试与验证

TEST-001：主控回环HTTP、独立TLS、管理/登录/伪造CF头拒绝、合法探针认证、持久证书、IP过滤。
TEST-002：Go主CF超时/5xx切直连、指纹错拒绝、缓存隔离/重启/过期、单nonce新签、不安全POST无重放、WS单会话、代理下载Range断点续传。
TEST-003：C相同场景以及四ABI运行库固定证书支持、控制流和代理升级；原有多槽保持。
TEST-004：主控test/vet、Probe全套、Android构建、C/ARM四ABI/Actions、正式SHA/资产和实机弱网备用路径。
验证结论按以下检查点和实机证据更新，不以设计代替证据。
2026-10-18 检查点：主控全套test/vet通过；Probe全套首次发现旧测试强制要求裸http.Transport，已适配新的包装层后全套通过；新增Go真实TLS错误指纹/nonce/POST/WS、缓存8并发单回源/TTL、12MiB双路Range逐字节对比均通过。C首次编译签名/头文件错误已修复，native与四ABI打包基线通过；新增C真实TLS发现/缓存/错误pin测试仍待执行。Android Kotlin升级原来绕开Go，现改为同一认证传输；APK构建尚待验证。
最终源码门禁：主控全套test及vet通过；Probe全套test通过，新增共享包vet通过；Android compileReleaseKotlin成功，Go mobilecore的12MiB双路下载比对通过；C真实HTTPS双路下载、断流续传、错误range拒绝通过，独立主控TLS发现/混合IPv6地址筛选/自签pin/错误pin/重启缓存测试通过；native和四套精确ASUS ABI多槽互操作共5轮全部通过；Linux router amd64/arm64编译通过。
已知基线限制：Probe全仓vet仍报告未修改Windows unsafe.Pointer与mobile_vrouter_test复制sync.Mutex，共8项；本次新增包无vet告警。发布技能旧preflight仍检查已退役Docker job及ARM64软路由附件，与v0.4.74已验证合同相同的误报；实际release.yml未被本次修改，手工复核mapledev/origin、自动版本/标签、12附件及ASUS/Android任务一致。

发布检查点：功能提交0f3e8de51f840a31ed381d6650cded946ed8cbc5已推送mapledev，Actions 39095798003全任务成功，自动版本提交894298b2/tag v0.4.75。正式12附件齐全，Android AAR/签名APK完成；ASUS native+四ABI多槽互通全部通过，两个正式包及manifest与GitHub SHA256一致。已通过产品接口发起生产主控v0.4.74→v0.4.75在线升级，尚在验收阶段。

v0.4.75实机检查点：主控已完成在线更新，公网IPv4/IPv6 15030为独立TLS，回环15030仍为HTTP，自签身份权限0600。经正常校验主域名HTTPS取得元数据后，实测固定公钥直连合法配置200/无签名401、Web/登录/管理/伪造CF头404、错误公钥拒绝；生产Go选择器模拟主通道失败后自动改走IP成功。两个并行认证Range读取同一94313字节正式包，合并SHA256与GitHub一致；缓存确实落在/opt/cloudhelper/probe_controller/temp/probe-download-cache。25/26插件和9本机均已通过产品在线升级；两插件配置/boot-id不变，4个正式payload逐字节回读一致，客户端及服务端4槽通过。

实机追加DEF-001 `已修复`：AX主域名TLS握手可持续20秒超时，首次发现接口原3秒预算导致缓存迟迟没有建立；本机也重现首次发现失败。Go/Android和C的首次发现改为后台单任务，挑战/元数据使用独立预算，原业务短超时与私有认证保持。新增真实TLS挑战延迟4秒回归，同时断言发现调用立即返回。Probe全套test、共享包vet、C原生与四ABI构建检查通过；可选race未运行（当前Windows工具链CGO关闭）。

最终发布：增量功能提交85530cd9cc341b53f0c5a5d588cb7260d279f4a1；Actions 39097148901全部成功，自动版本提交09c6ffd2/tag v0.4.76，正式12附件含安卓签名APK齐全。两款插件及manifest均核对GitHub SHA256；主控、节点9/25/26均通过在线升级，主控最终状态全部在线v0.4.76。

最终实机证据：AX release_id=19b8f35b2fae56d4、BE=27070225cdfc9842，两个设备4个发布payload回读逐字节一致，原config及boot-id不变，Koolcenter自启动仍启用。原有在线对等端的客户端/服务端四槽恢复；100秒三轮观察全部保持，新增carrier断开/超时及控制流异常事件均为空。证据保存在仓库外RouterFirmware/router-backups/plugin-multislot-20261018/verified-v0.4.76.json（分设备目录）及stability-v0.4.76.json。

最终代理升级：本机通过主控代理下载23377408字节Windows正式升级包，观察到两个并发独立Range片段，完成合并及在线重启，exe和主控缓存完整文件的SHA256均等于正式发布digest。身份与密钥未变（node_identity仅正常启动updated_at变化）、本地登录与WinSW配置保持。AX直连GitHub下载失败后，通过管理接口临时选择代理下发本次升级并立即恢复全部原节点字段/下载选择/密钥，代理升级成功。主控重启期间提前启动的一次本机升级遇502失败；主控恢复后重试成功，不将该中断记为通过。

最终入口复验：主域名已验证HTTPS发现双栈IP，直连无签名401、管理/登录/伪造CF头404、错误pin拒绝；正常配置及anthropic.com权威Fake IP查询200，生产Go选择器故障切换及两并发缓存Range合并SHA再次通过。AX日志出现Control stream established via pinned origin IP，实测路由器和本机到94.16.111.150:15030已建立连接，三端均有本地发现缓存。生产缓存路径/opt/cloudhelper/probe_controller/temp/probe-download-cache，30分钟闲置TTL由单测验证；未等待实机30分钟清理。

## 六、端到端追踪

REQ-001→UNIT-001/IF-001/002→TASK-001/002/003→TEST-001/004；REQ-002→UNIT-002/IF-003→TASK-001/002/003→TEST-002/004；REQ-003→UNIT-003/IF-003→TASK-001/002/003→TEST-003/004；REQ-004→全部单元→TASK-002/003→全部测试。

## 七、决策与冲突

DEC-001：IP不校验域名采用SPKI固定+有效期，不使用无条件InsecureSkipVerify；指纹只能从正常校验的主域名发现。
DEC-002：公网TLS与原回环HTTP分别绑定同端口，避免破坏cloudflared且禁止公网明文/管理访问。仅公布源站实际公网接口IP，可显式配置NAT对外地址。
DEC-003：HTTP通道可并行存活，WS不同时注册两个相同节点；失败认证GET切换须新challenge，POST不自动重放；升级下载由原有续传循环续接。
DEC-004：缓存准备返回短503+preparing标识，探针等待并重试，不将其视为网络故障；单文件512MiB、最多8项，避免磁盘无界增长。旧探针不带缓存能力标识时保留兼容转发；新Probe/Android/C插件主动启用缓存。Go大文件双路4MiB块、C大于256KiB双路临时片段，最终沿用升级文件校验。
DEC-005：缓存完整文件及摘要收据支持主控重启恢复；闲置TTL或容量LRU只清理无活跃读取的完成项。NAT可分别配置CLOUDHELPER_PROBE_DIRECT_LISTEN_IPS与CLOUDHELPER_PROBE_DIRECT_IPS；生产实测双栈源站，仅公布真实公网地址，C保留IPv4项。
DEC-006：首次地址发现与正常业务请求分开预算。Go/Android始终后台发现，最多60秒任务预算、挑战30秒；C复制配置到有界栈后台任务，挑战与元数据各最多30秒，无缓存时也不阻塞调用者。缓存仍只能由正常验证主域名HTTPS引入，不接受未认证直连自报指纹。
冲突：无；用户补充Web隔离与弱网升级目标已纳入全部单元和测试。

## 八、缺陷与风险

已验证外部主机及路由器15030可达。AX既有Cloudflare超时由备用入口接续，不通过换连接方向掩盖。首次无缓存且主域名完全不可达时不能凭空发现IP；缓存保证已成功发现后的重启恢复。安卓未做真机安装/联网验收，长期跨运营商稳定性超出100秒实机观察窗口；源站完全停止期间任何通道都不能提供服务。

## 九、回滚与门禁

回滚：产品在线恢复前版，保留原配置与证书身份文件（旧版忽略）；公网TLS监听随新版退出关闭，回环HTTP原路径不变。部署前保存外部私有基线；不写密码到Git。准备门禁和完成门禁通过，真机安卓与长期观察限制如上。
