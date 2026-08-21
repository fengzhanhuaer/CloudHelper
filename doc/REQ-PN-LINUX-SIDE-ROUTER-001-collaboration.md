# 协作文档

- 适用规则: AI协作规则
- 后续工作传递声明: 本文档必须传递给后续阶段与后续角色。
- 需求编号: REQ-PN-LINUX-SIDE-ROUTER-001
- 需求前缀: REQ-PN-LINUX-SIDE-ROUTER-001
- 当前阶段: Code实施
- 最近更新角色: Code
- 最近更新时间: 2026-08-16T16:41:23+08:00
- 工作依据文档: `doc/ai-coding-collaboration.md`; 用户在2026-08-16确认的独立精简Linux旁路由、现有TUN代理、局域网网段发布、独立开关、默认192.168.1网段及增加ARM64版本需求; `doc/REQ-PC-DOH-GATEWAY-001-collaboration.md`; 现有普通探针、Mihomo出口探针、虚拟路由协议、Linux TUN与在线升级实现
- 状态: Code实现完成，TASK-RTR-08受实机环境阻塞

## 第1章 Architect章节
- 章节责任角色: Architect
- 状态: 已完成

### 1.1 需求定义
- 状态: 已完成

#### 1.1.1 需求目标
- REQ-PN-LINUX-SIDE-ROUTER-001-R01: 提供独立的Linux amd64和arm64旁路由探针产品，复用现有普通探针的主控认证、虚拟路由协议、TUN数据面与在线升级能力，但使用独立二进制、安装脚本、服务、目录、节点类型和发布资产。
- REQ-PN-LINUX-SIDE-ROUTER-001-R02: 旁路由使用Alpine Linux 3.24.x x86_64或aarch64最小系统原生运行，由OpenRC托管，不依赖Docker、Mihomo、图形桌面、Python、Ruby或Node.js运行时。
- REQ-PN-LINUX-SIDE-ROUTER-001-R03: 主控提供可配置的网关代理，支持独立启停；默认只预填`192.168.1.0/24`、网关IP`192.168.1.150/24`和上游网关`192.168.1.1`，安装后默认关闭，不得自动接管网络。
- REQ-PN-LINUX-SIDE-ROUTER-001-R04: 网关代理仅接管来自已配置LAN源网段的转发流量，通过nftables标记和独立策略路由表进入`probe0`；不得通过主路由表两个`/1`路由接管旁路由宿主机自身流量。
- REQ-PN-LINUX-SIDE-ROUTER-001-R05: 网关代理开启时，局域网客户端能够使用现有域名/CIDR路由规则选择直出、拒绝或现有虚拟出口；直出流量通过上游网关SNAT，虚拟出口流量继续使用现有虚拟路由协议，不引入Mihomo二次分流。
- REQ-PN-LINUX-SIDE-ROUTER-001-R06: 旁路由在已配置网关IP提供LAN DNS入口，使局域网客户端能够使用现有域名规则和Fake IP能力；DNS入口随网关代理单独启停，不修改旁路由宿主机自身DNS。
- REQ-PN-LINUX-SIDE-ROUTER-001-R07: 主控提供可配置的本地IP代理，支持与网关代理相互独立启停；默认只预填发布网段`192.168.1.0/24`且默认关闭，开启并经管理员批准后，远端探针能够访问该旁路由后的真实局域网IP。
- REQ-PN-LINUX-SIDE-ROUTER-001-R08: 本地IP代理第一版使用三层子网路由和旁路由侧SNAT，不要求修改主路由或LAN终端回程路由；必须支持发布多个IPv4 CIDR、访问授权、网段冲突检测、离线撤销和状态上报。
- REQ-PN-LINUX-SIDE-ROUTER-001-R09: 旁路由必须提供事务化启停和fail-open；配置错误、探针退出、升级、重启或健康检查失败时，不得遗留导致宿主机或LAN持续断网的路由、地址、DNS重定向及nftables规则。
- REQ-PN-LINUX-SIDE-ROUTER-001-R10: 旁路由节点创建方式与普通探针一致，安装方式不在创建时弹出；节点创建后提供Linux专用安装按钮，脚本自动识别amd64或arm64，并提供配置入口、运行状态磁贴、网关代理状态、本地IP代理状态、流量计数、延迟和最近错误。
- REQ-PN-LINUX-SIDE-ROUTER-001-R11: 旁路由程序通过独立资产`cloudhelper-probe-router-linux-amd64`或`cloudhelper-probe-router-linux-arm64`接收主控自动升级，数据、日志、临时文件分别保存在`./data`、`./log`、`./temp`，升级必须支持按本机架构选择、校验、替换、重启与失败回滚。
- REQ-PN-LINUX-SIDE-ROUTER-001-R12: 第一版仅支持Linux amd64和arm64、IPv4、单网卡同网段旁路由和已有TUN协议；必须在网络命名空间集成测试及两种架构的Alpine实机上验证宿主机不受接管、LAN直出、虚拟出口、远程子网访问、故障回退和在线升级。

#### 1.1.2 需求范围
- 节点产品仅保留`normal`和`linux_router`；旁路由内置Mihomo代理出口能力，不再提供独立出口产品。
- 新增Linux amd64和arm64旁路由二进制、自动识别架构的OpenRC安装脚本、工作目录和GitHub Release资产。
- 新增主控旁路由配置、状态、安装、网关代理和本地IP代理界面。
- 新增Linux单网卡IPv4地址管理、转发、策略路由、nftables、SNAT、DNS入口和fail-open管理。
- 扩展现有路由配置同步，使旁路由获取专属配置，普通探针获取已授权的发布网段路由。
- 复用现有虚拟路由帧协议传送IPv4报文，不另建Overlay协议。
- 支持远端已启用虚拟路由的普通探针访问经批准的发布网段。
- 支持TCP、UDP和ICMP三层转发；状态页分别统计LAN入口、直出、虚拟出口、远端入站和回程。

#### 1.1.3 非范围
- Windows、Linux 32位armv7/armhf、Android或iOS旁路由产品。
- Docker、OpenWrt、RouterOS、Buildroot安装包。
- Mihomo、Clash订阅或二次代理节点选择。
- IPv6接管、NAT66或IPv6发布网段。
- 二层桥接、ARP跨站传播、DHCP中继、mDNS、SSDP、NetBIOS及任意UDP广播中继。
- 第一版不提供重叠网段的虚拟地址映射；检测到冲突时必须拒绝下发或保持未启用。
- 第一版不自动修改主路由DHCP；管理员可在验证后自行将客户端网关和DNS指向旁路由。
- 第一版不要求双网卡WAN/LAN隔离，后续可在不改变控制面语义的情况下扩展。
- 不修改现有Mihomo出口探针行为和资产。

#### 1.1.4 验收标准
- AC-01: 主控可创建`linux_router`节点；创建流程不弹安装方式，创建后专用安装按钮提供Linux原生安装脚本并自动识别x86_64/aarch64，普通和Mihomo节点安装入口不受影响。
- AC-02: Release工作流同时产出`cloudhelper-probe-router-linux-amd64`和`cloudhelper-probe-router-linux-arm64`；程序拒绝其他平台、32位ARM和非`linux_router`预期节点，升级只选择与本机架构一致的专用资产。
- AC-03: 新安装形成`/opt/cloudhelper/probe_router/{data,log,temp}`和OpenRC服务；运行环境不依赖Docker、Mihomo、glibc和解释器运行时。
- AC-04: 新节点默认配置显示`192.168.1.0/24`、`192.168.1.150/24`、`192.168.1.1`，网关代理与本地IP代理均默认关闭，保存配置不会在未启用时写入系统网络状态。
- AC-05: 网关代理开启后，主路由表默认路由保持指向物理上游，不出现`0.0.0.0/1`或`128.0.0.0/1`的宿主机全局接管；旁路由自身访问主控、SSH、DNS和公网正常。
- AC-06: 测试客户端将网关和DNS设为旁路由后，域名规则的direct、reject和probe_exit行为与普通探针一致；直出回包经SNAT正确返回，TCP、UDP、ICMP均有测试证据。
- AC-07: 仅开启网关代理时不得发布本地网段；仅开启本地IP代理时不得接管LAN客户端默认出口；两个开关同时开启时功能互不覆盖。
- AC-08: 本地IP代理获批后，已授权远端探针能够访问发布网段内的HTTP、TCP、UDP和ICMP目标；LAN目标看到的来源为旁路由网关IP，主路由和目标终端无需增加回程路由。
- AC-09: 发布默认路由、回环、链路本地、组播、Fake IP池、覆盖控制面端点的网段或与远端本地网段冲突时，主控或探针明确拒绝并显示原因。
- AC-10: 停止服务、`SIGTERM`、升级重启、无效配置回滚和模拟进程异常后，专用nftables链、策略规则、路由和附加IP能够清理；健康监护触发后LAN恢复经物理上游直出。
- AC-11: 主控状态磁贴展示在线、配置修订、应用状态、网关代理、本地IP代理、LAN地址、发布网段、流量计数、延迟、最近错误和检查时间；页面在桌面和移动宽度无重叠且控制台无错误。
- AC-12: `go test ./...`、amd64/arm64 Linux交叉构建与构建标签测试、控制器测试、`git diff --check`、Playwright页面测试、Linux网络命名空间集成测试，以及Alpine 3.24.x amd64和arm64实机验收均通过；任何环境限制或失败必须记录在第2章。
- AC-13: amd64和arm64 Alpine最小系统均在1GB内存环境中连续转发24小时无OOM或服务重启；分别记录空闲RSS、峰值RSS、连接数、吞吐、丢包和CPU，不以未实测估算值作为通过证据。

#### 1.1.5 风险
- 单网卡同网段直出若未SNAT，会因主路由直接向LAN终端回包形成非对称路径。
- 旁路由拥有`NET_ADMIN`等价能力，错误规则可能中断网络；所有对象必须使用专用表、专用链和可重放事务管理。
- 现有Linux TUN通过主路由表两个`/1`路由接管本机，不能直接用于网关产品。
- 现有普通探针对RFC1918私网默认旁路；发布网段需要安装更精确路由，并在本地网段重叠时拒绝启用。
- 进程被`SIGKILL`或主机掉电时无法执行退出清理，启动自愈和独立健康监护必须能够识别并清除遗留状态。
- DNS和Fake IP首次解析存在异步映射竞态风险，网关模式不得依赖丢弃首包后等待客户端重试。
- 远端访问真实LAN扩大了攻击面，默认关闭、管理员审批、最小授权和审计计数属于发布前强制条件。

#### 1.1.6 遗留事项
- 重叠网段的虚拟CIDR映射方案留待后续独立需求。
- 双网卡WAN/LAN模式、IPv6和广播发现中继留待后续独立需求。
- Alpine专用可写盘镜像制作可在原生安装稳定后追加，不阻塞第一版脚本安装。

#### 1.1.7 结论
- 需求边界明确：独立Alpine旁路由产品同时提供可独立启停的网关代理和本地IP代理，复用现有虚拟路由协议，但必须新增隔离的Linux网关控制面和故障恢复机制。

### 1.2 总体架构
- 状态: 已完成

#### 1.2.1 架构目标
- 保持普通探针、Mihomo出口探针和旁路由探针三个产品运行边界清晰，共享协议和基础能力而不复制整套代码。
- 让LAN转发流量进入TUN，同时确保旁路由宿主机自身通信始终保留物理默认出口。
- 以关闭为默认值，以管理员批准为发布网段的生效前提，以fail-open保证旁路由故障不持续阻断LAN。

#### 1.2.2 总体设计
- 产品层使用Go构建标签`linux_router`生成`cloudhelper-probe-router-linux-amd64`和`cloudhelper-probe-router-linux-arm64`；共享包继续位于`probe_node`，专用profile只启用身份、主控、虚拟路由、网关管理、DNS入口、状态和升级所需能力。
- 系统层使用Alpine OpenRC启动`/opt/cloudhelper/probe_router/probe-router`，数据、日志、临时升级目录分别为`data`、`log`、`temp`。
- 控制面在探针管理页创建`linux_router`节点并提供专用安装按钮；路由管理页新增“旁路由”子页，配置节点网卡、网关地址、上游网关、LAN源CIDR、网关代理开关、DNS开关、本地IP代理开关、发布CIDR和访问授权。
- 配置默认值为`gateway_proxy.enabled=false`、`local_ip_proxy.enabled=false`、`lan_cidrs=[192.168.1.0/24]`、`gateway_address=192.168.1.150/24`、`upstream_gateway=192.168.1.1`、`advertised_cidrs=[192.168.1.0/24]`。
- 网关代理数据面在nftables `prerouting`仅标记来自配置LAN源CIDR、目标非本地管理地址的转发流量；`ip rule`按fwmark查询专用路由表，默认进入`probe0`。本机`output`不打标，主路由表不安装两个`/1`路由。
- TUN按现有域名/CIDR规则执行direct、reject和probe_exit。direct从物理出口发送并SNAT为网关IP；probe_exit经现有虚拟路由协议转发；回包由内核转发回LAN客户端。
- LAN DNS仅绑定配置网关IP的TCP/UDP 53，复用现有路由规则和Fake IP状态，不修改宿主机`resolv.conf`；网关代理关闭时DNS入口同时关闭。
- 本地IP代理将获批发布CIDR聚合到其他已授权探针配置。远端探针为无冲突CIDR安装比本地私网旁路更精确的TUN路由；报文经现有虚拟路由到达旁路由后转发至LAN并SNAT为网关IP。
- 本地IP代理回程使用conntrack标记恢复和专用策略路由，确保LAN回包重新进入`probe0`而不是从上游网关逸出。
- 规则管理采用期望状态事务：先校验、在临时对象应用并探测，再原子切换；失败保留上一已知可用状态。所有规则使用固定CloudHelper前缀、独立nftables table/chain、独立fwmark和路由表编号。
- fail-open由进程退出清理、启动遗留清理、OpenRC重启策略和独立健康监护共同实现。探针不可用时移除LAN接管策略，保留物理默认出口和直出SNAT。

#### 1.2.3 关键模块
| 模块编号 | 模块名称 | 职责 | 输入 | 输出 |
|---|---|---|---|---|
| MOD-RTR-CTRL | 旁路由控制面 | 节点类型、配置、审批、授权、状态聚合 | 管理操作、探针报告 | 节点配置、运行状态 |
| MOD-RTR-PRODUCT | 旁路由产品层 | 构建profile、平台校验、目录、服务和升级资产隔离 | build tag、身份、Release清单 | 专用二进制和运行profile |
| MOD-RTR-INSTALL | Alpine安装层 | 安装依赖、目录、OpenRC服务、卸载和升级准备 | 节点ID、密钥、主控URL | 可运行服务 |
| MOD-RTR-GATEWAY | 网关规则管理器 | 地址、转发、nftables、fwmark、策略路由、SNAT和fail-open | 网关代理配置 | 生效网络状态和应用结果 |
| MOD-RTR-DNS | LAN DNS入口 | 将LAN DNS接入现有域名规则与Fake IP | TCP/UDP DNS请求 | DNS响应和计数 |
| MOD-RTR-VROUTE | 虚拟路由适配 | 复用现有帧协议完成LAN出站和远端入站 | IPv4报文、路由决策 | direct/reject/probe_exit/发布网段转发 |
| MOD-RTR-SUBNET | 本地IP代理 | 发布CIDR、审批、冲突检测、授权、远端路由和回程 | 发布配置、远端请求 | 可撤销子网路由 |
| MOD-RTR-STATUS | 运行状态 | 上报配置修订、规则、计数、延迟、错误和健康状态 | 数据面与系统快照 | 主控磁贴和诊断信息 |
| MOD-RTR-TEST | 验证与发布 | 单元、命名空间、页面、Alpine实机、升级和长稳测试 | 构建与测试环境 | 验收证据 |

#### 1.2.4 关键接口
| 接口编号 | 接口名称 | 调用方 | 提供方 | 说明 |
|---|---|---|---|---|
| IF-RTR-001 | `node_kind=linux_router`节点管理 | 探针管理页 | 主控节点注册 | 创建、保存和校验旁路由节点 |
| IF-RTR-002 | `/api/probe/proxy/probe-router/install-script` | 安装按钮/管理员 | 主控 | 返回自动识别amd64/arm64的Alpine OpenRC安装脚本 |
| IF-RTR-003 | `/mng/api/route/router_probes` | 路由管理页 | 主控 | 查询全部旁路由配置和聚合状态 |
| IF-RTR-004 | `/mng/api/route/router_probes/{node_id}` | 路由管理页 | 主控 | 保存单节点网关代理、本地IP代理和授权配置 |
| IF-RTR-005 | `/api/probe/route/config`旁路由扩展 | 旁路由/普通探针 | 主控 | 下发专属网关配置、发布网段、授权与修订号 |
| IF-RTR-006 | `/api/probe/route/router/report` | 旁路由探针 | 主控 | 上报应用状态、计数、延迟、错误和健康状态 |
| IF-RTR-007 | Linux网关事务接口 | 配置同步单元 | 网关规则管理器 | validate/apply/rollback/disable/cleanup期望状态 |
| IF-RTR-008 | LAN DNS入口 | LAN客户端 | 旁路由DNS单元 | 配置网关IP上的TCP/UDP 53 |
| IF-RTR-009 | 现有虚拟路由帧协议 | 普通探针/旁路由 | 现有虚拟路由数据面 | 不改变帧格式，增加发布网段路由用途 |
| IF-RTR-010 | 旁路由升级资产选择 | 主控升级命令 | 旁路由升级器 | 只接受与本机amd64/arm64架构一致的旁路由资产 |

#### 1.2.5 关键约束
- 仅在`runtime.GOOS=linux`且`runtime.GOARCH`为`amd64`或`arm64`时运行旁路由profile。
- 两个功能开关必须独立；默认值只用于表单预填，禁止默认启用。
- 网关IP启用前必须完成格式、接口归属、上游可达和地址冲突探测；失败不得部分应用。
- 主路由表、主默认路由和宿主机DNS必须保持不变。
- IPv6在第一版必须显式禁用转发或阻止LAN绕过，不能处于未管理状态。
- 发布CIDR必须使用管理员批准和最小远端节点授权，禁止隐式全网开放。
- 正常成功探测保持静默；持续错误按失败阶段限频，恢复后允许新一轮错误事件。

#### 1.2.6 风险
- nftables、ip rule、路由和地址跨多个内核子系统，必须通过快照和幂等对象名解决半应用状态。
- Alpine使用OpenRC而非systemd，现有安装、重启和升级脚本不能只替换服务名，必须实现并测试OpenRC生命周期。
- 发布网段路由会覆盖普通探针现有私网旁路，需要严格的前缀和本地接口冲突判定。

#### 1.2.7 结论
- 架构可在不修改现有虚拟路由帧格式的前提下落地；新增工作集中在产品profile、控制面、Linux网关规则管理和发布网段路由生命周期。

### 1.3 单元设计
- 状态: 已完成

#### 1.3.1 单元清单
| 单元编号 | 单元名称 | 所属模块 | 职责 | 输入 | 输出 |
|---|---|---|---|---|---|
| UNIT-RTR-01 | 节点类型单元 | MOD-RTR-CTRL | 管理`linux_router`身份和平台约束 | 节点请求 | 节点记录 |
| UNIT-RTR-02 | 配置存储与API单元 | MOD-RTR-CTRL | 保存、校验和返回两个开关及网络配置 | 管理JSON | 规范配置 |
| UNIT-RTR-03 | 管理页面单元 | MOD-RTR-CTRL | 创建、安装、配置和展示状态 | 管理API | 页面交互 |
| UNIT-RTR-04 | 产品profile单元 | MOD-RTR-PRODUCT | 隔离能力、目录、平台和升级前缀 | build tag | active profile |
| UNIT-RTR-05 | Alpine安装单元 | MOD-RTR-INSTALL | 安装OpenRC服务与目录 | 安装参数 | 服务实例 |
| UNIT-RTR-06 | 网关配置校验单元 | MOD-RTR-GATEWAY | 校验接口、CIDR、IP、上游、冲突和危险网段 | 期望配置 | 校验结果 |
| UNIT-RTR-07 | 网关事务单元 | MOD-RTR-GATEWAY | 幂等应用/回滚地址、nftables、ip rule和路由 | 已校验配置 | 内核状态 |
| UNIT-RTR-08 | LAN DNS单元 | MOD-RTR-DNS | 在网关IP接收DNS并复用路由决策 | DNS请求 | DNS响应 |
| UNIT-RTR-09 | LAN出站单元 | MOD-RTR-VROUTE | 将LAN转发流量送入现有TUN路由决策 | IPv4帧 | direct/reject/probe_exit |
| UNIT-RTR-10 | 发布网段控制单元 | MOD-RTR-SUBNET | 审批、授权、冲突检测和路由聚合 | 发布CIDR | 授权路由 |
| UNIT-RTR-11 | 发布网段数据单元 | MOD-RTR-SUBNET | 远端入站、LAN SNAT、connmark和TUN回程 | 远端IPv4帧 | LAN请求与回包 |
| UNIT-RTR-12 | 状态与健康单元 | MOD-RTR-STATUS | 计数、延迟、错误、修订和fail-open监护 | 运行快照 | 状态报告/清理动作 |
| UNIT-RTR-13 | 构建升级单元 | MOD-RTR-PRODUCT | 发布资产、选择、校验、替换和回滚 | Release/升级命令 | 新版本进程 |
| UNIT-RTR-14 | 验证单元 | MOD-RTR-TEST | 自动化和实机验收 | 构建产物 | 测试证据 |

#### 1.3.2 单元设计
##### UNIT-RTR-01
- 单元名称: 节点类型单元
- 职责: 将旁路由作为独立产品身份管理。
- 输入: `node_name`、`node_kind=linux_router`。
- 输出: 节点ID、密钥、类型和在线状态。
- 处理规则: 创建方式与普通探针一致；类型创建后不可静默改为其他产品；只允许Linux amd64或arm64安装。
- 异常规则: 类型不匹配的注册、配置同步和安装请求返回明确错误。

##### UNIT-RTR-02
- 单元名称: 配置存储与API单元
- 职责: 管理旁路由期望配置和修订号。
- 输入: 网关代理、本地IP代理、接口、地址、CIDR、上游和授权列表。
- 输出: 规范化配置、修订号和保存结果。
- 处理规则: 两个开关独立；新配置只预填默认192.168.1网段但保持关闭；CIDR去重并稳定排序。
- 异常规则: 非法地址、危险网段、未知节点、未授权节点或空关键字段拒绝保存。

##### UNIT-RTR-03
- 单元名称: 管理页面单元
- 职责: 提供节点创建后的专用安装按钮及路由页旁路由配置和状态。
- 输入: IF-RTR-001至IF-RTR-006。
- 输出: 配置表单、独立开关、状态磁贴和错误提示。
- 处理规则: 先选旁路由节点再显示其状态和配置；不得把单节点状态或聚合规则固定放在页面顶部。
- 异常规则: 保存或下发失败保留用户输入和上一成功状态，不显示假成功。

##### UNIT-RTR-04
- 单元名称: 产品profile单元
- 职责: 定义旁路由独立运行能力。
- 输入: `linux_router` build tag和预期节点类型。
- 输出: 服务名、目录、能力开关和升级前缀。
- 处理规则: 复用普通探针核心；关闭同步、DDNS、Mihomo等非必需能力；启用虚拟路由、网关、DNS和报告。
- 异常规则: 非Linux amd64/arm64、32位ARM或类型不匹配时在写系统网络前退出。

##### UNIT-RTR-05
- 单元名称: Alpine安装单元
- 职责: 完成最小依赖、下载、目录、权限和OpenRC服务配置。
- 输入: 主控URL、节点ID、节点密钥。
- 输出: `/opt/cloudhelper/probe_router`和`probe_router`服务。
- 处理规则: 使用`apk`安装`nftables iproute2 ca-certificates tzdata`和必要管理组件；创建data/log/temp；服务异常自动重启。
- 异常规则: 下载、校验或服务启动失败时退出非零并保留可诊断日志，不修改网络接管状态。

##### UNIT-RTR-06
- 单元名称: 网关配置校验单元
- 职责: 在任何内核写操作前校验完整配置。
- 输入: 网卡、网关地址、上游网关、LAN CIDR、发布CIDR和开关。
- 输出: 规范配置或字段级错误。
- 处理规则: 确认网卡存在、前缀一致、上游同链路、地址未占用、控制面端点可旁路、网段不危险且不冲突。
- 异常规则: 任一校验失败整体拒绝，保持上一已应用修订。

##### UNIT-RTR-07
- 单元名称: 网关事务单元
- 职责: 管理CloudHelper专用内核对象。
- 输入: 已校验期望配置。
- 输出: 应用修订、快照和回滚结果。
- 处理规则: 使用独立nftables表、fwmark、ip rule和路由表；只标记转发流量；保存前态；幂等应用和清理。
- 异常规则: 任一步失败逆序回滚；启动时清理无所有者遗留对象；清理失败触发fail-open告警。

##### UNIT-RTR-08
- 单元名称: LAN DNS单元
- 职责: 将LAN DNS请求接入现有域名规则和Fake IP映射。
- 输入: 网关IP TCP/UDP 53请求。
- 输出: direct、reject或Fake IP响应。
- 处理规则: 仅网关代理和DNS入口均启用时监听；不得更改宿主机DNS；映射在返回首个响应前准备完成。
- 异常规则: 上游或映射失败返回明确DNS失败，不将真实地址作为代理规则回退。

##### UNIT-RTR-09
- 单元名称: LAN出站单元
- 职责: 执行LAN客户端TUN分流。
- 输入: 来自已授权LAN源CIDR的IPv4报文。
- 输出: direct、reject或现有虚拟出口帧。
- 处理规则: direct经物理上游SNAT；reject丢弃并计数；probe_exit复用现有路由协议和回包路径。
- 异常规则: 未命中有效路径时按配置fail-open或fail-closed，并在状态中明确动作；默认网关故障恢复采用fail-open。

##### UNIT-RTR-10
- 单元名称: 发布网段控制单元
- 职责: 将获批本地CIDR提供给指定远端探针。
- 输入: 发布开关、CIDR、授权节点和在线状态。
- 输出: 每远端探针的路由配置。
- 处理规则: 默认关闭；管理员批准；最长前缀优先；远端本地路由冲突时不安装。
- 异常规则: 发布者离线、关闭、撤销批准或冲突后下发撤销并清理远端路由。

##### UNIT-RTR-11
- 单元名称: 发布网段数据单元
- 职责: 在旁路由与LAN之间转发远端访问。
- 输入: 目标属于获批发布CIDR的远端IPv4帧。
- 输出: SNAT后的LAN请求和经TUN返回的响应。
- 处理规则: 只接受控制面授权路径；使用conntrack标记和策略回程；LAN目标看到来源为网关IP。
- 异常规则: 未授权目标、状态缺失回包、冲突网段和非法源不得进入LAN。

##### UNIT-RTR-12
- 单元名称: 状态与健康单元
- 职责: 提供运行可观测性和故障恢复。
- 输入: 配置修订、内核快照、TUN状态、计数和探测结果。
- 输出: IF-RTR-006报告、磁贴状态和fail-open动作。
- 处理规则: 成功保持低噪声；失败按阶段限频；恢复重置失败事件；报告不得包含密钥。
- 异常规则: TUN、主控或规则管理连续失败达到阈值时撤销LAN接管并保留物理直出。

##### UNIT-RTR-13
- 单元名称: 构建升级单元
- 职责: 生成和升级旁路由独立资产。
- 输入: Release工作流和主控升级命令。
- 输出: 校验后的新二进制或旧版本回滚。
- 处理规则: `CGO_ENABLED=0`，分别以linux/amd64和linux/arm64及`linux_router` tag构建；资产按专用前缀和本机架构选择；升级工作区使用temp。
- 异常规则: 下载、校验、替换或启动失败回滚，不选择普通或Mihomo资产。

##### UNIT-RTR-14
- 单元名称: 验证单元
- 职责: 建立从配置到数据面和升级的可重复验证。
- 输入: 自动化环境和Alpine实机。
- 输出: 测试结果、截图、性能和长稳报告。
- 处理规则: 覆盖开关组合、宿主机隔离、direct/reject/probe_exit、子网访问、冲突、异常回滚和升级。
- 异常规则: 无法执行的测试必须记录环境原因和残余风险，关键实机项未执行不得关闭需求。

#### 1.3.3 风险
- 普通profile使用`!linux_router`构建约束；旁路由专属能力必须保持在`linux_router`构建内，避免普通探针和旁路由能力相互泄漏。
- 远端发布网段路由涉及普通探针Windows/Linux路由安装，平台差异必须通过各自单元测试和命名空间/实机验证覆盖。

#### 1.3.4 结论
- 单元设计覆盖控制面、产品、安装、网关、DNS、子网发布、状态、升级和验证完整生命周期，具备Code拆分条件。

### 1.4 Code任务执行包
- 状态: 已放行

#### 1.4.1 执行边界
- 允许修改: `doc/REQ-PN-LINUX-SIDE-ROUTER-001-collaboration.md`; `.github/workflows/release.yml`; `probe_controller/internal/core/server.go`; `probe_controller/internal/core/probe_registry.go`; `probe_controller/internal/core/probe_special_exit.go`; `probe_controller/internal/core/probe_runtime.go`; `probe_controller/internal/core/probe_ws.go`; `probe_controller/internal/core/probe_route_config_store.go`; `probe_controller/internal/core/probe_route_handlers.go`; `probe_controller/internal/core/probe_virtual_router.go`; `probe_controller/internal/core/probe_command.go`; `probe_controller/internal/core/install_scripts.go`; `probe_controller/internal/core/install_scripts/install_probe_router_service.sh`; `probe_controller/internal/core/mng_probe_handlers.go`; `probe_controller/internal/core/mng_pages/probe.html`; `probe_controller/internal/core/mng_pages/route.html`; 上述控制器文件对应的`*_test.go`; 新增`probe_controller/internal/core/probe_linux_router.go`与`probe_linux_router_test.go`; `probe_node/main.go`; `probe_node/product_profile.go`; `probe_node/product_profile_normal.go`; `probe_node/product_profile_normal_test.go`; `probe_node/service_entry.go`; `probe_node/probe_special_exit_product_normal.go`; `probe_node/probe_route_config_sync.go`; `probe_node/probe_route_config_sync_test.go`; `probe_node/probe_virtual_router.go`; `probe_node/probe_virtual_router_test.go`; `probe_node/probe_virtual_router_settings.go`; `probe_node/probe_virtual_router_linux.go`; `probe_node/probe_virtual_router_linux_test.go`; `probe_node/probe_virtual_router_windows.go`; `probe_node/probe_virtual_router_windows_test.go`; `probe_node/local_tun_egress_linux.go`; `probe_node/probe_virtual_router_dns_service.go`; `probe_node/probe_virtual_router_tun_dataplane_linux.go`; `probe_node/probe_virtual_router_tun_dataplane_linux_test.go`; `probe_node/upgrade.go`; `probe_node/upgrade_test.go`; 新增`probe_node/product_profile_linux_router.go`; 新增`probe_node/product_profile_linux_router_test.go`; 新增`probe_node/service_entry_linux_router.go`; 新增`probe_node/probe_linux_router.go`; 新增`probe_node/probe_linux_router_linux.go`; 新增`probe_node/probe_linux_router_test.go`; 新增`probe_node/probe_linux_router_linux_test.go`; 新增`probe_node/release_workflow_linux_router_test.go`。
- 禁止修改: `probe_node/mobilecore/`; Android工程; Mihomo实现、Mihomo构建profile和Mihomo安装脚本; Docker目录; 控制器迁移与部署脚本; 与旁路由无关的主控页面; 现有虚拟路由帧格式。若实现发现必需文件不在允许范围，Code必须先在2.6节提交“文件范围缺失”，不得自行扩展。

#### 1.4.2 任务清单
| 任务编号 | 需求编号 | 单元编号 | 文件范围 | 操作类型 | 验收标准 |
|---|---|---|---|---|---|
| TASK-RTR-01 | REQ-PN-LINUX-SIDE-ROUTER-001-R01,R02,R11 | UNIT-RTR-04,05,13 | product profile、service entry、安装脚本、upgrade、release workflow及其测试 | 新增、修改 | AC-02,03,10,12 |
| TASK-RTR-02 | REQ-PN-LINUX-SIDE-ROUTER-001-R03,R07,R10 | UNIT-RTR-01,02 | probe registry、route config store/handler、新控制面文件及测试 | 新增、修改 | AC-01,04,07,09 |
| TASK-RTR-03 | REQ-PN-LINUX-SIDE-ROUTER-001-R03,R07,R10 | UNIT-RTR-03 | `probe.html`、`route.html`及页面/API测试 | 修改 | AC-01,04,07,11,12 |
| TASK-RTR-04 | REQ-PN-LINUX-SIDE-ROUTER-001-R03,R04,R09 | UNIT-RTR-06,07,12 | 新Linux router管理文件、main/profile接入及测试 | 新增、修改 | AC-04,05,07,10,12 |
| TASK-RTR-05 | REQ-PN-LINUX-SIDE-ROUTER-001-R05,R06 | UNIT-RTR-08,09 | Linux TUN、DNS、路由决策和配置同步文件及测试 | 修改 | AC-05,06,07,12 |
| TASK-RTR-06 | REQ-PN-LINUX-SIDE-ROUTER-001-R07,R08 | UNIT-RTR-10,11 | 主控路由聚合、普通探针平台路由、旁路由入站/回程及测试 | 新增、修改 | AC-07,08,09,12 |
| TASK-RTR-07 | REQ-PN-LINUX-SIDE-ROUTER-001-R09,R10 | UNIT-RTR-12 | 状态报告API、健康监护、磁贴和错误限频测试 | 新增、修改 | AC-10,11,12 |
| TASK-RTR-08 | REQ-PN-LINUX-SIDE-ROUTER-001-R01至R12 | UNIT-RTR-14 | 自动化、Playwright、网络命名空间、Alpine实机、升级和24小时长稳验证；同步本文第2章 | 新增、修改、执行 | AC-01..13 |

#### 1.4.3 源码修改规则
- 修改源代码时必须注意可能存在的GBK编码并保持原文件编码，避免乱码或误转码。
- 所有Linux网络对象必须使用常量化专用名称并提供幂等查询、应用和删除方法，测试不得直接污染开发机真实路由表。
- 不得通过shell拼接未经校验的网卡名、CIDR、IP、fwmark或路由表编号；优先使用结构化参数，必须调用命令时逐参数传递并限制允许字符。

#### 1.4.4 交付物
- `REQ-PN-LINUX-SIDE-ROUTER-001`完整Code跟踪证据。
- 旁路由独立二进制、GitHub Release资产和Alpine OpenRC安装脚本。
- 主控节点创建、专用安装、旁路由配置和状态界面。
- 网关代理、本地IP代理、LAN DNS、fail-open和发布网段数据面。
- 单元测试、命名空间集成测试、Playwright截图、Alpine实机、升级和长稳报告。

#### 1.4.5 门禁输入
- 每个任务必须关联第2.3节至少一个测试项；网络命名空间测试必须在隔离命名空间执行。
- AC-05、AC-06、AC-08、AC-10和AC-13必须有运行证据，不能只用代码审查或mock通过替代。
- 页面改动必须使用Playwright验证桌面和移动视口、交互、错误态、无重叠和控制台零错误。
- 发布前必须验证在线升级选择旁路由资产且失败可回滚；不得手工替换线上运行二进制作为交付手段。

#### 1.4.6 结论
- Code任务包覆盖全部需求和可测试验收标准，允许按TASK-RTR-01至TASK-RTR-08进入实现。

### 1.5 Architect需求跟踪矩阵
- 状态: 已完成

| 需求编号 | 需求描述 | 架构章节 | 单元设计章节 | Code任务章节 | 状态 | 备注 |
|---|---|---|---|---|---|---|
| REQ-PN-LINUX-SIDE-ROUTER-001-R01 | 独立旁路由产品 | 1.2.2 | UNIT-RTR-01,04,13 | TASK-RTR-01,02,08 | 未开始 | 复用核心、隔离交付 |
| REQ-PN-LINUX-SIDE-ROUTER-001-R02 | Alpine最小系统 | 1.2.2 | UNIT-RTR-04,05 | TASK-RTR-01,08 | 未开始 | Linux amd64/arm64、OpenRC |
| REQ-PN-LINUX-SIDE-ROUTER-001-R03 | 可配置网关代理及默认值 | 1.2.2 | UNIT-RTR-02,03,06,07 | TASK-RTR-02,03,04,08 | 未开始 | 默认关闭 |
| REQ-PN-LINUX-SIDE-ROUTER-001-R04 | 仅接管转发流量 | 1.2.2 | UNIT-RTR-07,09 | TASK-RTR-04,05,08 | 未开始 | 宿主机主路由不变 |
| REQ-PN-LINUX-SIDE-ROUTER-001-R05 | 现有TUN分流 | 1.2.2 | UNIT-RTR-07,09 | TASK-RTR-05,08 | 未开始 | 无Mihomo |
| REQ-PN-LINUX-SIDE-ROUTER-001-R06 | LAN DNS入口 | 1.2.2 | UNIT-RTR-08 | TASK-RTR-05,08 | 未开始 | 不改宿主机DNS |
| REQ-PN-LINUX-SIDE-ROUTER-001-R07 | 独立本地IP代理开关 | 1.2.2 | UNIT-RTR-02,03,10 | TASK-RTR-02,03,06,08 | 未开始 | 默认关闭 |
| REQ-PN-LINUX-SIDE-ROUTER-001-R08 | 多CIDR发布与远程访问 | 1.2.2 | UNIT-RTR-10,11 | TASK-RTR-06,08 | 未开始 | SNAT免回程配置 |
| REQ-PN-LINUX-SIDE-ROUTER-001-R09 | 事务与fail-open | 1.2.2 | UNIT-RTR-06,07,12 | TASK-RTR-04,07,08 | 未开始 | 强制实测 |
| REQ-PN-LINUX-SIDE-ROUTER-001-R10 | 创建、安装、配置和状态UI | 1.2.2 | UNIT-RTR-01,03,12 | TASK-RTR-02,03,07,08 | 未开始 | 选择节点后显示状态 |
| REQ-PN-LINUX-SIDE-ROUTER-001-R11 | 独立升级与目录 | 1.2.2 | UNIT-RTR-04,05,13 | TASK-RTR-01,08 | 未开始 | data/log/temp分离 |
| REQ-PN-LINUX-SIDE-ROUTER-001-R12 | 平台边界和验证 | 1.2.2 | UNIT-RTR-14 | TASK-RTR-08 | 未开始 | 关键实机门禁 |

### 1.6 Architect关键接口跟踪矩阵
- 状态: 已完成

| 接口编号 | 需求编号 | 接口名称 | 调用方 | 提供方 | 输入 | 输出 | 状态 | 备注 |
|---|---|---|---|---|---|---|---|---|
| IF-RTR-001 | REQ-PN-LINUX-SIDE-ROUTER-001-R01,R10 | `node_kind=linux_router` | 探针页 | 节点注册 | 节点名称、类型 | 节点身份 | 未开始 | 与普通节点同一创建动作 |
| IF-RTR-002 | REQ-PN-LINUX-SIDE-ROUTER-001-R01,R02,R10,R11 | 旁路由安装脚本 | 管理员 | 主控 | 节点身份、主控URL | OpenRC脚本 | 未开始 | 自动识别amd64/arm64 |
| IF-RTR-003 | REQ-PN-LINUX-SIDE-ROUTER-001-R03,R07,R10 | 旁路由列表与状态 | 路由页 | 主控 | 管理会话 | 配置和状态 | 未开始 | 选择节点后展示 |
| IF-RTR-004 | REQ-PN-LINUX-SIDE-ROUTER-001-R03,R07,R08 | 单节点配置保存 | 路由页 | 主控 | 两个开关、网络与授权 | 修订配置 | 未开始 | 字段级校验 |
| IF-RTR-005 | REQ-PN-LINUX-SIDE-ROUTER-001-R03至R08 | 路由配置同步扩展 | 普通/旁路由探针 | 主控 | 节点身份 | 专属配置 | 未开始 | 复用现有端点 |
| IF-RTR-006 | REQ-PN-LINUX-SIDE-ROUTER-001-R09,R10 | 运行状态报告 | 旁路由 | 主控 | 修订、健康、计数、错误 | 接收结果 | 未开始 | 不含密钥 |
| IF-RTR-007 | REQ-PN-LINUX-SIDE-ROUTER-001-R03,R04,R09 | Linux网关事务 | 配置同步 | 网关管理器 | 期望配置 | 应用/回滚结果 | 未开始 | 幂等、fail-open |
| IF-RTR-008 | REQ-PN-LINUX-SIDE-ROUTER-001-R05,R06 | LAN DNS | LAN客户端 | 旁路由 | DNS wire | DNS wire | 未开始 | TCP/UDP 53 |
| IF-RTR-009 | REQ-PN-LINUX-SIDE-ROUTER-001-R05,R08 | 虚拟路由帧协议 | 普通/旁路由探针 | 虚拟路由数据面 | IPv4帧 | IPv4帧 | 未开始 | 帧格式不变 |
| IF-RTR-010 | REQ-PN-LINUX-SIDE-ROUTER-001-R11 | 升级资产选择 | 升级器 | GitHub Release | 版本、平台、前缀 | 候选二进制 | 未开始 | 独立前缀 |

### 1.7 门禁裁判
- 状态: 已完成

#### 1.7.1 门禁输入
| 文档 | 路径 | 状态 |
|---|---|---|
| 协作文档 | `doc/REQ-PN-LINUX-SIDE-ROUTER-001-collaboration.md` | 已完成 |

#### 1.7.2 裁判检查
| 检查项 | 结果 | 证据 | 备注 |
|---|---|---|---|
| 协作文档存在 | 通过 | 本文件 | 无 |
| Architect章节存在 | 通过 | 第1章 | 无 |
| Code章节存在 | 通过 | 第2章 | 已初始化，待Code填写 |
| 必需子章节存在 | 通过 | 1.1至1.7、2.1至2.6 | 无 |
| 需求前缀一致 | 通过 | 全文`REQ-PN-LINUX-SIDE-ROUTER-001` | 无 |
| 需求编号一致 | 通过 | 1.1、1.5和1.6 | 矩阵中Rxx均指本需求前缀 |
| 接口编号一致 | 通过 | 1.2.4和1.6 | IF-RTR-001至010 |
| 模板字段完整 | 通过 | 文档头和固定章节 | 无 |
| GBK编码文件无乱码或误转码 | 通过 | 本轮只新增UTF-8 Markdown | Code阶段重新检查 |
| Code证据完整 | 待Code | 第2章已初始化 | 最终门禁前必须补齐 |
| Code任务反馈已处理 | 通过 | 当前无反馈 | 后续持续检查 |
| 验收标准可测试 | 通过 | AC-01至AC-13 | 包含自动化、命名空间和实机标准 |
| 需求任务覆盖完整 | 通过 | 1.5与TASK-RTR-01至08 | 无 |
| 任务自测覆盖完整 | 待Code | TASK-RTR-08和1.4.5 | Code阶段细化到2.3并执行 |
| 修改文件在允许范围内 | 通过 | 1.4.1 | 本轮仅新增本文档 |
| 测试失败已记录缺陷 | 待Code | 本轮未执行代码测试 | Code阶段强制记录 |
| 未执行测试原因完整 | 通过 | 第2.5.7节 | 本轮无业务源码变更 |
| 遗留风险可接受 | 通过 | 1.1.5、1.1.6、1.2.6 | 关键风险均有门禁 |

#### 1.7.3 冲突记录
| 冲突编号 | 冲突条款 | 最终采用条款 | 裁决人 | 裁决结论 |
|---|---|---|---|---|
| 无 | 无 | 无 | Architect | 无 |

#### 1.7.4 裁判结论
- 结论: 有条件通过
- 放行阻塞: 放行
- 条件: Code必须严格按1.4节执行；AC-05、AC-06、AC-08、AC-10和AC-13缺少运行或实机证据时不得关闭需求。
- 责任方: Code负责实现与证据；Architect负责最终门禁复核。
- 关闭要求: 第2章全部矩阵和执行证据完成，所有关键验收通过，未处理反馈和阻塞缺陷为零。
- 整改要求: Code发现文件范围、接口、验收或规则缺口时必须在2.6节反馈，等待Architect更新本文档后继续。

#### 1.7.5 结论
- Architect方案、任务包和跟踪矩阵完成，阶段门禁有条件通过并允许进入Code实施；当前不代表功能已经开发完成。

## 第2章 Code章节
- 章节责任角色: Code
- 状态: 有条件完成，等待实机验收

### 2.1 Code需求跟踪矩阵
- 状态: Code实现已完成

| 需求编号 | 任务编号 | 实现文件 | 实现状态 | 自测状态 | 证据 | 备注 |
|---|---|---|---|---|---|---|
| REQ-PN-LINUX-SIDE-ROUTER-001-R01,R02,R11 | TASK-RTR-01 | 产品profile、双架构Release、OpenRC安装及升级资产 | 已完成 | 自动化通过，实机升级待验收 | `product_profile_linux_router.go`; `release.yml`; `install_probe_router_service.sh`; 双架构构建与build-kind自检通过 | 不依赖Docker、Mihomo或解释器运行时 |
| REQ-PN-LINUX-SIDE-ROUTER-001-R03,R07,R10 | TASK-RTR-02,TASK-RTR-03 | `probe_linux_router.go`; 探针管理页与路由页旁路由Tab | 已完成 | Go与Playwright通过 | 默认值、独立开关、先选节点再显示、保存立即同步、桌面/移动截图 | 本地Web不在当前范围，主控是唯一配置源 |
| REQ-PN-LINUX-SIDE-ROUTER-001-R03,R04,R09 | TASK-RTR-04 | `probe_linux_router_linux.go`; 产品接管能力隔离 | 已完成 | 单元通过，命名空间实测待验收 | fwmark表208、TUN回注表209、无`/1`、sysctl原值恢复及fail-open测试 | WSL nft/netns环境不可用，未替代实机证据 |
| REQ-PN-LINUX-SIDE-ROUTER-001-R05,R06 | TASK-RTR-05 | TUN包钩子、direct回注、reject、probe_exit复用及LAN DNS DNAT | 已完成 | 单元通过，端到端待验收 | `probe_linux_router.go`; `probe_virtual_router.go`; nft脚本断言 | TCP/UDP/ICMP/DNS实流量仍需Alpine环境 |
| REQ-PN-LINUX-SIDE-ROUTER-001-R07,R08 | TASK-RTR-06 | 发布规则聚合、ACL、离线撤销、普通探针精确路由、conntrack回程 | 已完成 | 控制面和回程单元通过，端到端待验收 | `appendProbeLinuxRouterPublishedRouteRules`; Linux/Windows冲突检查; connmark`0x4349` | 多节点远程访问实测待执行 |
| REQ-PN-LINUX-SIDE-ROUTER-001-R09,R10 | TASK-RTR-07 | 运行报告、健康检查、fail-open、配置修订、TUN流量和邻接延迟磁贴 | 已完成 | Go与Playwright通过 | desired/applied SHA与修订、健康/错误、RX/TX统计、最优健康邻接RTT | 实机故障注入待执行 |
| REQ-PN-LINUX-SIDE-ROUTER-001-R01至R12 | TASK-RTR-08 | 自动化、交叉构建和页面验证 | 部分完成 | 自动化通过，关键实机门禁未执行 | 见2.3与2.5 | AC-05、06、08、10、13不得据此关闭 |

### 2.2 Code关键接口跟踪矩阵
- 状态: 已完成

| 接口编号 | 需求编号 | 实现文件 | 调用方 | 提供方 | 实现状态 | 证据 | 备注 |
|---|---|---|---|---|---|---|---|
| IF-RTR-001,IF-RTR-002 | R01,R02,R10,R11 | `probe_registry.go`; `probe_command.go`; `mng_probe_handlers.go`; `probe.html`; OpenRC脚本 | 管理员/安装按钮 | 主控 | 已完成 | 控制器测试和脚本语法通过 | 原生安装自动识别amd64/arm64 |
| IF-RTR-003,IF-RTR-004 | R03,R07,R10 | `probe_linux_router.go`; `route.html`; `server.go` | 路由管理页 | 主控 | 已完成 | Playwright保存与立即同步断言通过 | 实际使用单一`/mng/api/route/linux_router` GET/POST接口 |
| IF-RTR-005 | R03至R09 | `probe_route_handlers.go`; `probe_route_config_sync.go` | 探针 | 主控 | 已完成 | SHA、节点类型、修订与同步测试通过 | 专属快照和聚合路由同次返回 |
| IF-RTR-006 | R09,R10 | `main.go`; `probe_ws.go`; `probe_runtime.go` | 旁路由 | 主控 | 已完成 | 状态序列化与页面渲染通过 | 复用周期报告，不新增独立HTTP上报端点 |
| IF-RTR-007,IF-RTR-008 | R03至R06,R09 | `probe_linux_router_linux.go`及现有DNS服务 | 配置同步/TUN | Linux内核和DNS单元 | 已完成 | 单元通过，实机待验收 | 表208/209、nft专用表、DNS DNAT和fail-open |
| IF-RTR-009 | R05,R08 | `probe_virtual_router.go`; Linux/Windows平台路由 | 普通探针/旁路由 | 现有虚拟路由 | 已完成 | 全量Go回归通过 | 帧格式未改变 |
| IF-RTR-010 | R11 | 产品profile、`upgrade.go`、Release工作流 | 主控升级命令 | 旁路由升级器 | 已完成 | 双架构构建、amd64 build-kind自检通过 | arm64实机替换/回滚待验收 |

### 2.3 Code测试项跟踪矩阵
- 状态: 部分完成

| 测试项编号 | 需求编号 | 任务编号 | 测试目标 | 测试方法 | 结果 | 证据 | 未执行原因 | 备注 |
|---|---|---|---|---|---|---|---|---|
| TEST-RTR-001 | R01,R02,R11 | TASK-RTR-01 | 双架构产品profile、安装、资产和升级隔离 | Go测试、交叉构建、脚本语法、自检、实机升级 | 部分通过 | amd64/arm64构建通过；Alpine `sh -n`通过；amd64 build-kind自检通过 | 无两种架构Alpine实机 | 安装与在线升级仍待实机 |
| TEST-RTR-002 | R03,R07,R10 | TASK-RTR-02,TASK-RTR-03 | 默认值、独立开关、API和页面 | Go测试、Playwright桌面/390px移动 | 通过 | 2个Playwright用例通过；截图见2.5.6 | 无 | 控制台/pageerror为零，无横向溢出 |
| TEST-RTR-003 | R03,R04,R09 | TASK-RTR-04 | 仅接管转发流量及事务回滚 | 单元、Linux命名空间 | 部分通过 | `/1`禁用、表208/209、nft生成、fail-open、sysctl恢复单元通过 | WSL2及特权Alpine容器的`nft --check`与`ip link add dummy`均进入D状态；当前共享内核netlink不可用 | 不将环境失败记为功能通过 |
| TEST-RTR-004 | R05,R06 | TASK-RTR-05 | direct/reject/probe_exit和LAN DNS | 命名空间端到端TCP/UDP/ICMP/DNS | 未执行 | 代码单元与普通虚拟路由回归通过 | 缺少可用nft网络命名空间/Alpine实机 | 发布前强制执行 |
| TEST-RTR-005 | R07,R08 | TASK-RTR-06 | 发布、授权、冲突、远端访问和回程 | 控制面单元、多命名空间端到端 | 部分通过 | ACL、离线撤销、路由冲突、conntrack回程单元通过 | 缺少多节点Linux环境 | HTTP/TCP/UDP/ICMP远程访问待验收 |
| TEST-RTR-006 | R09,R10 | TASK-RTR-07 | 状态、退出清理和fail-open | 故障单元、API及页面测试、实机注入 | 部分通过 | 状态、流量、fail-open和清理单元/页面通过 | 未执行实机进程异常和掉电 | SIGKILL遗留自愈需实机确认 |
| TEST-RTR-007 | R01至R12 | TASK-RTR-08 | 全量回归和双架构实机验收 | Go、Playwright、构建、Alpine实机 | 部分通过 | 控制器、普通探针、linux_router标签测试通过；双架构构建通过 | 无amd64/arm64 Alpine实机 | 自动化已完成，实机未完成 |
| TEST-RTR-008 | R12 | TASK-RTR-08 | 双架构1GB内存24小时长稳 | 流量压测和资源采样 | 未执行 | 无 | 无两种架构1GB Alpine实机和24小时窗口 | AC-13未通过 |

### 2.4 Code缺陷跟踪矩阵
- 状态: 已关闭已发现代码缺陷

| 缺陷编号 | 需求编号 | 测试项编号 | 缺陷描述 | 严重级别 | 修复状态 | 修复证据 | 备注 |
|---|---|---|---|---|---|---|---|
| DEF-RTR-001 | R10 | TEST-RTR-002 | CSS grid覆盖`hidden`且移动顶栏横向溢出 | 中 | 已修复 | Playwright桌面/移动2用例通过 | 添加`[hidden]`优先级和移动顶栏布局 |
| DEF-RTR-002 | R07,R08 | TEST-RTR-005 | 仅开启本地IP代理时LAN回包未重新进入TUN | 高 | 已修复 | connmark`0x4349`、表208回程及发布网段源包测试 | 同时保留独立开关语义 |
| DEF-RTR-003 | R04,R09 | TEST-RTR-003 | 旁路由可能沿用普通探针本地设置安装两个`/1` | 高 | 已修复 | 产品能力禁用接管；Linux/Windows平台和出口刷新均加门禁 | 普通探针原行为回归通过 |
| DEF-RTR-004 | R09 | TEST-RTR-003,TEST-RTR-006 | 完全关闭后未恢复`ip_forward/rp_filter`原值 | 高 | 已修复 | `TestProbeLinuxRouterSysctlsRestoreOriginalValues`通过 | fail-open期间按设计保留转发 |

### 2.5 Code执行证据
- 状态: 自动化证据完成，实机证据待补

#### 2.5.1 修改接口
- 新增旁路由节点类型`linux_router`、原生安装信息和专用安装脚本端点。
- 新增主控`GET/POST /mng/api/route/linux_router`；保存后立即向在线已知探针发送`route_config_sync`。
- 扩展`/api/probe/route/config`响应和探针周期报告，承载专属快照、修订/SHA、健康、fail-open、错误、TUN RX/TX统计及最优健康邻接RTT。
- 现有虚拟路由帧协议未改；普通探针仅为授权的`linux-router-*`发布规则安装精确CIDR路由。

#### 2.5.2 配置文件
- 主控路由配置增加`linux_routers`数组。
- 旁路由期望快照保存为`./data/probe_linux_router_config.json`；运行日志为`./log/probe_router.runtime.log`；升级临时目录为`./temp`。
- OpenRC环境配置保存于`/etc/conf.d/probe_router`，服务文件为`/etc/init.d/probe_router`。

#### 2.5.3 执行报告
- 新节点默认两个开关均关闭，仅预填`192.168.1.0/24`、`192.168.1.150/24`和`192.168.1.1`。
- 网关流量使用fwmark`0x4348`和表`208`进入TUN；TUN回注使用`iif`规则和表`209`按已配置上游/物理CIDR送出，主路由表不写两个`/1`。
- 本地IP代理使用conntrack标记`0x4349`恢复回程；ACL、离线停止聚合和Linux/Windows本地路由冲突检查已实现。
- 配置失败时网关进入直通；本地代理单独失败时清理策略；完全关闭时恢复首次读取的sysctl原值。
- 状态延迟复用现有虚拟路由ping-pong统计，取无错误且大于零的最小邻接RTT；没有有效邻接时显示`-`，不将未知值显示为`0 ms`。
- 旁路由本地Web未实现：当前产品profile关闭本地控制台，主控是唯一配置源；如后续增加，建议独立需求限定为只读状态、诊断和紧急直通。

#### 2.5.4 影响文件
- 发布和安装: `.github/workflows/release.yml`; `install_scripts.go`; `install_probe_router_service.sh`。
- 主控: 节点/安装/路由配置/运行状态/WebSocket/管理页面相关文件及`probe_linux_router.go`测试。
- 探针: 产品profile、服务入口、升级目录、配置同步、虚拟路由平台精确路由、旁路由控制/数据面及测试。
- 未修改Mihomo核心、Android/mobilecore和Docker目录。

#### 2.5.5 测试命令
- `go test ./internal/core`（`probe_controller`）。
- `go test ./...`与`go test -tags linux_router ./...`（`probe_node`，最终串行执行）。
- `GOOS=linux GOARCH=amd64/arm64 CGO_ENABLED=0 go build -tags linux_router ...`。
- `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c ...`。
- `docker run --rm -v D:\Code\CloudHelper:/workspace alpine:3.22 sh -n /workspace/probe_controller/internal/core/install_scripts/install_probe_router_service.sh`。
- WSL运行amd64产物`--upgrade-verify --upgrade-verify-build-kind=linux_router`。
- Playwright: `playwright test linux-router-route.spec.js --reporter=line --workers=1`。
- `git diff --check`。

#### 2.5.6 自测结果
- 主控完整测试通过；普通探针完整测试串行通过；`linux_router`标签完整测试通过。
- Linux amd64、Linux arm64旁路由构建通过；Windows amd64普通探针测试交叉编译通过。
- Alpine OpenRC脚本`sh -n`通过；Linux amd64专用产物build-kind自检通过。
- Playwright 2/2通过：未选节点详情隐藏、选择后显示、独立开关、ACL、保存立即下发提示、desired/applied差异、TUN流量、`17 ms`邻接延迟、390px无横向溢出、无console/pageerror。
- 页面截图: `C:\Users\fengz\.codex\visualizations\2026\08\13\019ff891-e1ff-7363-959b-b154ff9fae7d\linux-router-desktop.png`; `C:\Users\fengz\.codex\visualizations\2026\08\13\019ff891-e1ff-7363-959b-b154ff9fae7d\linux-router-mobile.png`。
- WSL执行新增Linux纯规则测试通过；`git diff --check`通过。

#### 2.5.7 未执行测试原因
- 当前没有可用的Alpine amd64和arm64实机，因此原生安装、OpenRC生命周期、在线升级替换/回滚、1GB资源和24小时长稳未执行。
- WSL `unshare`内创建dummy接口以及`nft --check`均进入内核等待。后续Alpine软件源已恢复，在`--privileged`的`alpine:3.22`临时容器成功安装`nftables 1.1.3`与`iproute2 6.15.0`，但`nft --check`仍进入D状态；删除该容器后，新建的干净特权容器执行首条`ip link add dummy`仍进入D状态，确认是Docker Desktop与各发行版共享的WSL2内核netlink异常，而非缺包或测试规则导致。
- 所有本轮临时测试容器均已删除；`autosurf`和`moviepilot-v2`业务容器保持健康。当前没有独立Hyper-V/Linux VM；未经用户允许不重启Docker Desktop/WSL2，避免中断业务容器。
- TCP/UDP/ICMP/DNS端到端、远程发布网段访问、进程异常/掉电清理仍需真实或可用的Linux网络命名空间环境。

#### 2.5.8 遗留风险
- AC-05、AC-06、AC-08、AC-10和AC-13仍缺实机运行证据，需求不得关闭或发布为生产稳定版。
- `SIGKILL`和掉电无法执行进程内清理，虽有启动重放/fail-open代码，仍必须验证遗留nft/rule/address自愈。
- nftables规则已通过字符串和行为单元检查，但当前环境不能完成内核解析/实际转发验收。

#### 2.5.9 回滚方案
- 未发布前可整体回退本需求文件变更。
- 已安装节点可先在主控关闭两个开关，确认表208/209、优先级10080/10081和`cloudhelper_router`表清理，再停止`probe_router`服务并恢复原二进制。
- Release工作流新增资产独立于普通/Mihomo资产；回滚不需要替换普通探针。

#### 2.5.10 结论
- Code实现和可在当前环境完成的自动化验证已完成；因关键Alpine双架构实机、端到端网络和24小时长稳证据缺失，保持“有条件完成，等待实机验收”，不满足需求关闭条件。

#### 2.5.11 当前检查点
- 启用方式: 用户明确要求继续推进已有长任务，接管现有唯一协作文档，不创建竞争账本。
- 用户最新指令: 已启动无密码root的Hyper-V Alpine虚拟机，要求形成可复用Alpine调试技能并开始调试；随后追加更高优先级，先只读排查现有TUN代理不稳定及约38 MB PyPI文件无法下载的问题。
- 当前任务: TUN大文件下载只读诊断已完成，`TASK-RTR-08`恢复为待继续；代码实现和现有自动化证据保持有效，完成门禁仍为有条件通过。
- TUN诊断结论: `files.pythonhosted.org`未配置规则时的失败主因是本机物理网络到国际CDN的直连出口质量异常，不是虚拟路由载波断开、业务队列满、TUN丢包或MTU硬中断。添加域名规则后实际命中`rr-3 / Github`，路径为`9 -> 18 (vipcloud.hk)`，分阶段诊断成功，路径RTT约85 ms。
- TUN诊断证据: 47.7 MB Playwright wheel经节点18完整下载，73.07秒、约653 KB/s；连接累计约51.5 MB，`dropped=0`、`errors=0`并正常FIN关闭。同期TUN队列未满，`tx_dropped=0`、`tx_errors=0`，但历史最大Wintun回写646 ms、排队642 ms，属于可观测背压而非本次失败根因。
- 物理出口对照: 固定相同Fastly真实IP并由探针创建`/32 -> Ethernet -> 172.18.55.254`旁路后，47.7 MB请求180秒仅收到1.31 MB、约7.3 KB/s并超时；四个Fastly地址的2 MB分段为约4-191 KB/s且一条超时，Cloudflare固定物理旁路无法建连。国内清华PyPI镜像同一wheel同样走物理`/32`旁路，2 MB用时1.58秒、约1.33 MB/s，排除本机物理网卡、TCP栈和TUN旁路机制整体故障。
- 已确认环境: Docker及WSL2可启动，但共享内核`6.6.87.2-microsoft-standard-WSL2`中的网络netlink修改路径不返回；特权容器、已安装的真实`nft/iproute2`和干净网络命名空间均已复现。
- 阻塞影响: 无法在本机完成`CLOUDHELPER_ROUTER_NFT_CHECK=1`、`CLOUDHELPER_ROUTER_NETNS_TEST=1`以及TCP/UDP/ICMP/DNS端到端验收；AC-05、AC-06、AC-08、AC-10和AC-13继续保持未通过。
- 已失败尝试: BuildServer WSL2直接执行、Ubuntu/WSL能力核对、特权Alpine安装真实工具、删除旧容器后新建干净容器、Hyper-V及常见本地VM工具检查。
- 所需最小外部动作: 在不承载现有业务容器的独立Linux/Alpine主机或VM中运行现有集成测试；若只能使用本机，需先获得用户明确许可，在维护窗口重启Docker Desktop/WSL2并接受业务容器短时中断。
- 下一步唯一动作: 继续完成并验证`cloudhelper-alpine-router-debug`技能，然后连接无密码root的Hyper-V Alpine虚拟机执行`TASK-RTR-08`实机验收。

### 2.6 Code任务反馈
- 状态: 已处理

| 反馈编号 | 任务编号 | 反馈类型 | 反馈描述 | 阻塞影响 | Code建议 | Architect处理状态 | Architect处理结论 |
|---|---|---|---|---|---|---|---|
| FEEDBACK-RTR-001 | TASK-RTR-01,TASK-RTR-07 | 文件范围缺失 | 第三产品build-tag互斥需要修改`probe_special_exit_product_normal.go`，节点类型归一化和运行状态回传需要修改`probe_special_exit.go`、`probe_runtime.go`、`probe_ws.go`，原允许范围未包含这些文件 | 未补充前会导致linux_router构建符号冲突或状态无法落库 | 将四个已证明必需的文件及对应测试加入1.4.1，继续保持Mihomo、Android和部署目录禁改 | 已处理 | Architect已将四个文件加入1.4.1；范围扩充仅覆盖第三产品编译和状态链路 |
| FEEDBACK-RTR-002 | TASK-RTR-04 | 文件范围缺失 | 仅在旁路由初次TUN创建处禁用`/1`不足，现有本地设置和Linux出口刷新路径仍可能重新安装接管路由；原范围缺少`probe_virtual_router_settings.go`和`local_tun_egress_linux.go` | 不补充会违反R04并影响宿主机通信 | 将两个文件加入1.4.1，仅增加产品接管能力门禁，不改变普通探针行为 | 已处理 | Architect已补充最小文件范围；普通探针全量回归通过 |

#### 2.6.1 结论
- FEEDBACK-RTR-001和FEEDBACK-RTR-002均已完成最小范围补充；当前无未处理Code反馈。
