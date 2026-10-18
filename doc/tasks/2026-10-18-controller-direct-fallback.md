# 任务：主控探针专用 IP/TLS 备用连接

- 状态：`进行中`
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
- TASK-003 `进行中`：提交并发布，之后在线部署主控、两台插件及授权本机Probe；不擅自批量更新所有节点。
- 下一步唯一动作：完成新增缓存/分片与三端切换回归，之后执行正式发布及在线升级。

## 五、测试与验证

TEST-001：主控回环HTTP、独立TLS、管理/登录/伪造CF头拒绝、合法探针认证、持久证书、IP过滤。
TEST-002：Go主CF超时/5xx切直连、指纹错拒绝、缓存隔离/重启/过期、单nonce新签、不安全POST无重放、WS单会话、代理下载Range断点续传。
TEST-003：C相同场景以及四ABI运行库固定证书支持、控制流和代理升级；原有多槽保持。
TEST-004：主控test/vet、Probe全套、Android构建、C/ARM四ABI/Actions、正式SHA/资产和实机弱网备用路径。
当前均待执行，不以设计代替证据。
2026-10-18 检查点：主控全套test/vet通过；Probe全套首次发现旧测试强制要求裸http.Transport，已适配新的包装层后全套通过；新增Go真实TLS错误指纹/nonce/POST/WS、缓存8并发单回源/TTL、12MiB双路Range逐字节对比均通过。C首次编译签名/头文件错误已修复，native与四ABI打包基线通过；新增C真实TLS发现/缓存/错误pin测试仍待执行。Android Kotlin升级原来绕开Go，现改为同一认证传输；APK构建尚待验证。
最终源码门禁：主控全套test及vet通过；Probe全套test通过，新增共享包vet通过；Android compileReleaseKotlin成功，Go mobilecore的12MiB双路下载比对通过；C真实HTTPS双路下载、断流续传、错误range拒绝通过，独立主控TLS发现/混合IPv6地址筛选/自签pin/错误pin/重启缓存测试通过；native和四套精确ASUS ABI多槽互操作共5轮全部通过；Linux router amd64/arm64编译通过。
已知基线限制：Probe全仓vet仍报告未修改Windows unsafe.Pointer与mobile_vrouter_test复制sync.Mutex，共8项；本次新增包无vet告警。发布技能旧preflight仍检查已退役Docker job及ARM64软路由附件，与v0.4.74已验证合同相同的误报；实际release.yml未被本次修改，手工复核mapledev/origin、自动版本/标签、12附件及ASUS/Android任务一致。

## 六、端到端追踪

REQ-001→UNIT-001/IF-001/002→TASK-001/002/003→TEST-001/004；REQ-002→UNIT-002/IF-003→TASK-001/002/003→TEST-002/004；REQ-003→UNIT-003/IF-003→TASK-001/002/003→TEST-003/004；REQ-004→全部单元→TASK-002/003→全部测试。

## 七、决策与冲突

DEC-001：IP不校验域名采用SPKI固定+有效期，不使用无条件InsecureSkipVerify；指纹只能从正常校验的主域名发现。
DEC-002：公网TLS与原回环HTTP分别绑定同端口，避免破坏cloudflared且禁止公网明文/管理访问。仅公布源站实际公网接口IP，可显式配置NAT对外地址。
DEC-003：HTTP通道可并行存活，WS不同时注册两个相同节点；失败认证GET切换须新challenge，POST不自动重放；升级下载由原有续传循环续接。
DEC-004：缓存准备返回短503+preparing标识，探针等待并重试，不将其视为网络故障；单文件512MiB、最多8项，避免磁盘无界增长。旧探针不带缓存能力标识时保留兼容转发；新Probe/Android/C插件主动启用缓存。Go大文件双路4MiB块、C大于256KiB双路临时片段，最终沿用升级文件校验。
DEC-005：缓存完整文件及摘要收据支持主控重启恢复；闲置TTL或容量LRU只清理无活跃读取的完成项。NAT可分别配置CLOUDHELPER_PROBE_DIRECT_LISTEN_IPS与CLOUDHELPER_PROBE_DIRECT_IPS；生产实测双栈源站，仅公布真实公网地址，C保留IPv4项。
冲突：无；用户补充Web隔离与弱网升级目标已纳入全部单元和测试。

## 八、缺陷与风险

尚未验证外部主机15030防火墙可达性。AX既有Cloudflare超时是本需求触发，不掩盖或用换方向替代。首次无缓存且主域名完全不可达时不能凭空发现IP；缓存保证已成功发现后的重启恢复。

## 九、回滚与门禁

回滚：产品在线恢复前版，保留原配置与证书身份文件（旧版忽略）；公网TLS监听随新版退出关闭，回环HTTP原路径不变。部署前保存外部私有基线；不写密码到Git。准备门禁通过，完成门禁不通过（未实现/测试/发布）。
