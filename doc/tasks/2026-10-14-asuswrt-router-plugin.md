# 任务：华硕原厂固件 JFFS 虚拟路由插件

- 任务标识：`2026-10-14-asuswrt-router-plugin`
- 状态：`进行中`
- 创建时间：`2026-10-14 10:52 +08:00`
- 更新时间：`2026-10-16`
- 用户原始需求：开始需求跟踪；路由器插件核心功能为 DNS 分配 fake IP 并确定直连或代理、fake IP 代理、Probe Node 物理链路 12040 服务端及客户端。
- 历史指令：“继续开发”。B已按前一指令恢复为A备份的原厂52332；继续C实现的JFFS独立插件，完成原厂兼容、安装包、菜单/HTTPS管理、DNS/分流/12040及Probe Link测速验证，不再编译或刷写系统。最新补充：页面直接嵌入原厂后台菜单，沿用原厂登录和端口，不开独立管理端口或插件登录。主控地址、Node ID和密钥由用户后续填写。
- 用户最新指令：“刷入路由器”。将已离线验收的 a76398d1 最小自启固件写入物理B，配套新版JFFS插件并验证启动与自启；保留物理A和CFE及原配置。当前B运行52332，必须先切A再用原生inactive写入器，沿用本次升级授权完成必要重启与管理恢复；历史ONCE异常仍须调查并保留证据。
- 开发收尾必做：恢复`noconsole=1`并`nvram commit`，回读确认；若控制台已启动，关闭其运行态并验证后续启动行为。该恢复是用户已授权的收尾步骤，不能因固件切换A/B遗漏（共享配置）。
- 启用方式：用户明确“开始需求跟踪”，按用户级 AGENTS.md 强制启用 manage-long-tasks。
- 已提交：d179308固定上游+定制覆盖，9f8ff875基础构建验证，e8f85802镜像字段修正，7bbff0d1升级保留证据，3dffc397原JFFS2挂载兼容补丁。Git只保留自有构建/版本/补丁与账本。
- 历史SWRT交付（官方替换正在执行）：基础固件升级完成，含JFFS2兼容补丁的镜像运行在Second；两次启动自动挂载原数据、SSH与管理Web可用。15项网络配置、DNS/WAN/无线驱动和LAN HTTPS通过，CFE ROM及完整First原厂固件SHA256未变。未精简、未加入CloudHelper业务功能；下一阶段等待用户精简名单。

## 一、需求定义

### 1.1 背景与问题

用户希望在 ASUS RT-AX56U V2 上运行现有 CloudHelper fake IP 路由能力，并作为其他 Probe 可连接的物理链路节点。初始原厂固件已经通过 SSH 只读检查，后续基础固件升级及实机验证已完成，但现有发布未提供 ARMv7 程序，尚未验证模块加载、程序运行、启动持久化或业务流量。

### 1.2 目标

交付适用于RT-AX56U V2原厂52332固件的独立JFFS插件，采用C模块、原厂后台内嵌页面与同端口认证接口。DEC-034/039替代历史定制固件内置与独立HTTPS页面方向。保留DNS分流、fake IP代理、12040双角色及既有协议兼容，服务端始终绑定LAN地址，开启公网访问才额外绑定WAN地址（DEC-029）。

### 1.3 范围、非范围与约束

- 范围内：精确型号源码/工具链锁定、基础固件构建、ARMv7新模块、rootfs内置、DNS/dnsmasq、fake IP数据面、12040双角色、原生启动/WAN/DNS/防火墙事件、轻量Web/菜单、固件独立发布与恢复。
- 管理范围：轻量控制台及受认证LAN管理API；在固件Web源码直接增加CloudHelper菜单与页面资源，验证导航/登录、重启和停用业务后可管理；12040为LAN及可选WAN链路，不混用管理端口。
- 小Web最小能力：总开关/状态、节点身份和主控连接设置（机密不回显）、DNS上游与启停、规则/映射查询、12040服务端/客户端与对端状态、有界即时状态和诊断（日志仅shell）、安装版本/更新状态。业务规则与映射沿用主控权威来源，本地页面不得创造另一套冲突规则。停止服务须保留可恢复的管理入口或明确管理脚本恢复方法。
- 独立代码与产物：新router_plugin独立C模块及入口，参考既有协议/行为，不裁剪普通Probe冒充新实现。主发布资产改为精确型号定制固件、校验与来源/版本清单；模块独立构建，暂不承诺额外插件安装包。
- 路由器特点：控制缓存、并发、队列及日志的上限；减少Flash写入并合并必要持久化；优先小依赖集合；配置与映射采用一致快照；WAN/DNS/防火墙恢复幂等；生命周期可回滚。暂不以零依赖或绝对吞吐作未经验证的承诺。
- 服务启用时始终绑定活动LAN IPv4:配置端口（默认12040）；允许公网时额外绑定WAN IPv4并放行，关闭公网则撤销WAN监听和规则。无WAN仍保留LAN服务；不绑定0.0.0.0。客户端经物理WAN出站并避免TUN回路。
- 第一目标设备：SSH 别名 `localrouter`，地址 `192.168.52.1:22`；RT-AX56U V2；原厂 `3.0.0.4.386_52332-gfbf3b9c`。
- 固件主线：下载并锁定源码/工具链，保持候选上游模块配置编译基础镜像并验证可运行；用户给出精简名单后才做模块精简和回归，随后加入CloudHelper功能。SWRT 386固定基线已在本V2实机启动验证，其他硬件版本/任意同名镜像不能据此认定兼容；包括SOFTCENTER/SMARTDNS在内的上游模块当前不自行精简。
- 范围外：从零重写操作系统/无线驱动、完整软路由/Mihomo产品和无关控制器功能。保持原厂Wi-Fi/AiMesh/DHCP/NAT作为兼容验收；当前基础刷写及验证已有用户明确授权，不清空配置、不主动改CFE ROM。
- 初始数据入口采用 TUN，属于当前代码和实机能力支持的设计方案，用户尚未明确指定入口；如后续改用 TPROXY，更新本账本与决策。
- 默认仅接管 fake IP 网段，避免自动创建全局接管路由；普通流量使用原厂转发。虚拟节点地址等协议必需路由在设计中单独列明。
- DNS初始参考现有语义：代理规则A查询返回fake IP，直连返回真实IP，拒绝返回REFUSED；这是行为兼容方案，并非用户要求所有域名都返回fake IP。
- 继续使用控制器权威 fake IP 分配与身份/路由授权；不得在插件内自行分配与全网不一致的映射。
- 12040“服务端”指接受其他 Probe 的物理 Carrier 连接；是否还承担其他节点的互联网出口功能需单独设计，不把监听成功当成出口验收。
- 既有生产 Probe/Controller 二进制沿用在线更新交付；SSH 用于诊断和验证。固件发布/更新独立设计，不把只读SSH连接授权解释成手工替换现有生产程序授权。
- 不使用 HomePc 做构建或测试；必要的 Linux 隔离验证使用本地 WSL。
- 工作区状态已变化：当前HEAD08dc24bcc80003428f84667981fc37694832e440，本轮开始git status本轮开始仅本账本修改；执行期间备用出口账本发生并发修改，保留不触碰；此前备用出口代码改动为历史，不归因本任务。

### 1.4 需求与验收标准

| 编号 | 需求 | 可观察验收标准 | 优先级 | 状态 | 来源 |
|---|---|---|---|---|---|
| REQ-001 | DNS 分流和 fake IP 分配 | LAN TCP/UDP DNS 查询按同一规则产生代理 fake IP、直连真实 IP、拒绝响应；fake IP 映射与主控一致；保留局域网名称解析；IPv6 策略明确且不会绕过指定出口 | P0 | 待开始 | 用户核心功能 1 |
| REQ-002 | fake IP 代理 | LAN 无需设置显式代理即可访问 fake IP 对应目标；TCP、UDP 请求及回包经过指定节点；映射缺失和出口不可达时明确失败，不泄漏为直连 | P0 | 待开始 | 用户核心功能 2 |
| REQ-003 | 12040 物理链路服务端 | TCP/TLS-WebSocket 始终监听 LAN IPv4:配置端口（默认12040），允许公网时额外绑定 WAN IPv4；无 wildcard、无 QUIC/UDP Carrier；其他 Probe 合法连接后双向交换数据；错误认证拒绝；WAN 地址变化后重新绑定，未就绪不错误放宽地址 | P0 | 待开始 | 用户核心功能 3及“12040绑定到WAN口IP” |
| REQ-004 | 12040 物理链路客户端 | 路由器按主控配置连接其他兼容服务端；fake IP 请求及回包使用此链路；断开后按既有策略重连；与本机服务端同时工作 | P0 | 待开始 | 用户核心功能 3，连接其他服务器 |
| REQ-005 | 固件内置和生命周期 | ARMv7模块与必要内核支持内置rootfs；配置持久化，镜像和资源预算适配；开机与 WAN/防火墙重建后恢复；停用恢复受管配置，原厂Wi-Fi/AiMesh/DNS/DHCP/NAT正常 | P0 | 待开始 | 固件内置方向；原无需刷机约束由DEC-012替代 |
| REQ-006 | 兼容 Probe 配置与协议 | 新实现与现有节点身份、主控规则、fake IP映射、拓扑、认证和Carrier协议互通；跨实现测试证明兼容；状态能区分DNS、入口与链路故障 | P0 | 待开始 | “当前能力”和Probe双角色，新代码指令 |
| REQ-007 | 独立定制固件发布产物 | 专用V2固件镜像、型号/上游/工具链/模块版本清单与校验；更新配置兼容；错误型号拒绝；不误用普通Probe/linux_router产物；恢复步骤可复查 | P0 | 待开始 | 用户“这是一个独立的发布产物” |
| REQ-008 | 路由器专用新代码 | 独立router_plugin模块和入口，按DNS/映射规则/数据面/Carrier/平台/生命周期划分；不依赖普通Probe或linux_router产品运行时；缓存、并发、队列、日志与Flash写入有可测试边界；ARMv7编译与资源测量通过 | P0 | 待开始 | 用户要求“专门供路由器使用”“重新起一套代码” |
| REQ-009 | 小Web控制台 | LAN可访问经认证控制台，提供范围列出的最小控制/状态；配置校验错误可见，操作确实改变运行态；停止业务后仍能恢复；小型静态资源无重前端运行时依赖；管理API不向WAN开放 | P0 | 待开始 | 用户“需要一个小web做控制” |
| REQ-010 | 固件后台原生独立菜单 | 在Web源码加入CloudHelper菜单/页面；原厂导航登录正常，管理认证边界明确；重启后存在、停业务仍能管理；页与固件模块版本一致 | P0 | 待开始 | 菜单要求按最新固件内置方向调整，替代运行时注入 |
| REQ-011 | 精确型号基础固件与恢复 | 锁定源码commit/工具链/驱动/型号；基础固件可构建；镜像头/分区/大小匹配V2；有可复查原厂恢复路线，集成前基线有记录 | P0 | 已完成 | 固件实施前置 |
| REQ-012 | 基础可运行→指定精简→新功能的顺序 | 先下载并保留上游模块配置完成基础编译/运行证据；精简名单由用户提供；精简后回归通过再加入新功能，构建兼容修复单独记录且不冒充精简 | P0 | 进行中 | 用户最新明确顺序 |
| REQ-013 | 固定上游加定制覆盖与Git范围 | Git只跟踪修改源码/补丁/构建配置/版本锁定及账本；完整上游源树、源文档、布局清单和中间文件只本地保留；在固定上游应用覆盖可构建，不需要把完整上游纳入Git | P0 | 已完成 | 用户后续位置、无需Git历史与忽略指令 |

| REQ-014 | 升级尽量保留启动ROM | 无额外CFE ROM镜像通过原生校验；备份启动区/配置；核对写入分区并比较启动区；保留配置，不主动写CFE ROM | P0 | 已完成 | 用户“开始升级吧，尽量不动启动rom” |

REQ-015（P0，已完成）：形成用户级路由器升级调试技能，保留物理A/CFE ROM，只从A写B、B ONCE试启动、真实健康检查后长期B，并区分已验证A回退/B长期与未验证ONCE/故障回退。对应TASK-015、TEST-026、UNIT-007、DEC-020；技能不更改本轮固件或启动状态。

REQ-016（P0，进行中，新增TASK-017/019和TEST-028/030）：核实华硕官方RT-AX55 GPL源码对本机RT-AX56U_V2的支持，用户自行下载候选386_53297。验收区分官方来源、源码目标/硬件配置匹配、构建依赖完整、实际编译和整机运行；不能用型号字符串代替后两项。关联UNIT-007、TASK-016、TEST-027。

REQ-017（P0，已完成，按DEC-027普通B例外）：活动固件基线切为官方GPL 386_53297，原配置构建并升级验证。可复现来源锁/SDK、完整构建/CRC/容量/rootfs及原配置/JFFS/SSH密钥、Wi-Fi/Web/DNS/WAN/LAN HTTPS验证通过；CFE原hash与A全部逻辑载荷保持。用户明确正式B启动，替代本阶段原ONCE→健康→长期启动顺序；普通B一次重启及健康通过，不承诺ONCE和故障自动回退。关联UNIT-007、TASK-017~019、TEST-028~030/035。

## 二、总体架构

REQ-018（P0，已完成）：Python串口工具与现有技能集成；可枚举端口、被动抓日志、执行命令并核对真正返回和退出码、交互终端；默认COM4/115200/8N1，无自动重启/Flash写入。已有Linux shell时独立于SSH；无shell时仅显式SSH临时接入，不能误称持久启用。超时/回显/非shell控制台不冒充成功，日志本地忽略。关联UNIT-008、IF-008、TASK-020、TEST-031/032。

串口调试架构：PC Python/pyserial直接访问UART；捕获模式只收不发，命令模式用随机标记确认POSIX shell并读取退出码。可选SSH临时桥接只启动RAM shell，不写NVRAM或init；固定启用需核查真实rc入口。脚本主副本在firmware/asuswrt/tools，技能scripts保存相同副本，更新时校验SHA256。

### 2.1 当前现状

- DNS：`probe_virtual_router_dns_service.go` 在 `198.18.0.2:53` 监听 TCP/UDP；代理规则 A 查询请求主控映射；非 A 查询走真实上游。目前 AAAA 可能导致 IPv6 绕过。
- DNS 上游与缓存：`probe_virtual_router_dns_cache.go`；Linux 系统 DNS 适配：`probe_virtual_router_dns_system_linux.go`，默认处理 resolv.conf/systemd-resolved，不能未经适配直接套用到华硕。
- IP 包入口：`local_tun_install_linux.go`、`probe_virtual_router_tun_dataplane_linux.go`、`probe_virtual_router_linux.go`；`probe_virtual_router.go` 按映射/路径封装并转发原始 IP 包。
- Carrier参考实现：`probe_virtual_router_runtime.go`；默认12040，已有TCP/TLS WebSocket、UDP/HTTP3监听、客户端worker与重连。新实现必须按所支持的既有传输配置互通，逐项验证，不创造不兼容端口协议，也不默认承诺全部旧功能已搬入。
- 产品：普通 Probe 默认可能修改系统 DNS和全局路由；`linux_router` 限制 amd64/arm64 并带有原厂路由器不需要的完整软路由/Mihomo功能；两者均不等于现成华硕插件。
- 原厂 dnsmasq 使用 `bind-dynamic`、`no-resolv` 和 `servers-file=/tmp/resolv.dnsmasq`，监听 LAN/本机 53 并承担 DHCP。

### 2.2 目标架构

在 `router_plugin/` 建立新的路由器专用 C 实现，采用官方 ARM GCC 与 Makefile；规划 src/、include/、tests/ 和现有 web/。按DNS、策略、主控协议、数据面、Carrier、Asuswrt平台、生命周期和管理API划分职责。不使用Go服务或嵌入Go运行时；PC开发工具可继续Python，主控维持现有Go实现。此前未部署的Go草稿归档到本地忽略的 artifacts/go-prototype-20261015/，不属于活动源码或交付输入。

现有Probe源码仅为协议、映射、认证及路由行为的参考，协议契约通过跨实现测试固化。第一目标华硕ARMv7；平台接口为其他路由器保留适配边界，不把OpenWrt已支持或通用路由器兼容作为本次已完成成果。

新数据面首选TUN接收原始IP包并封装到兼容Carrier，在入口侧避免仅为转发加载完整用户态TCP/IP栈。是否需要本机承担其他节点的最终互联网出口，及相应协议栈依赖，须在设计中明确；12040服务端接入并不自动等于出口角色。链路仅 TLS/WebSocket，取消 QUIC/HTTP3；认证、HTTP及JSON复用固件现有OpenSSL/libcurl/json-c，ARMv7和旧内核支持用实际构建及运行证据证明。

固件集成层与业务模块分开：本Git仅保留firmware/asuswrt构建脚本/固定上游版本/定制补丁及我们新增源码。完整上游普通快照只本地保留并忽略，Linux从固定上游建立构建工作树；定制修改必须来自本Git补丁或定制源码，不能只改临时目录。先验证无CloudHelper修改的基础构建，再加入模块。程序/静态资源进入rootfs，配置在JFFS，日志只stdout/stderr，不写日志文件或syslog；原厂JFFS剩余空间不能代替镜像容量核算。

轻量Web保持LAN管理和认证边界；菜单在固件Web源码直接集成，rc启动/停止、WAN/DNS/防火墙重建处加入幂等调用，具体函数/顺序待源码检查。停止推进运行时绑定挂载菜单。固件内置仍采用内核TUN/转发加用户态业务进程，写入固件不等于将整个代理改成内核模块。

```text
LAN 客户端 DNS -> 原厂 dnsmasq -> 插件 DNS -> 规则 + 主控 fake IP 映射
                    |              |
                    |              +-> 真实上游 DNS（不得回指 dnsmasq）
                    +-> DHCP/局域网名称解析

LAN fake IP 数据 -> Linux 路由/转发 -> TUN -> 插件虚拟路由
                                               |
                                               +-> 12040 客户端 Carrier -> 其他节点/出口
其他 Probe -> WAN IPv4:12040 服务端 -> 认证后的 Carrier -> 既有虚拟路由处理
回包沿对应链路 -> TUN -> LAN 客户端
```

### 2.3 关键模块与职责

| 模块 | 目标职责 | 输入/输出 | 依赖 |
|---|---|---|---|
| 专用新运行时 | 独立模块启用三类核心能力，按路由器预算管理资源 | 生命周期/运行状态 | ARMv7构建、专用配置 |
| DNS 适配 | 原厂 dnsmasq 转发到插件，按规则返回 fake/real/refused | DNS 请求/应答与映射 | 主控、上游、局域网名称 |
| fake IP 数据面 | 接管fake网段，按兼容路径封装转发和回注 | IP包/回包 | TUN、iptables、权威映射 |
| Carrier | WAN地址绑定的12040服务端、物理WAN客户端、认证/心跳/重连 | 现有协议连接/帧 | 节点身份、证书、主控拓扑、WAN地址 |
| 平台生命周期 | 内置服务启停、事件恢复、配置持久化及日志限额 | rc/WAN/DNS/防火墙事件与管理操作 | 固件源码、JFFS配置 |
| 小Web与菜单适配 | LAN控制、认证与状态；原厂菜单入口挂接和撤销 | 浏览器/管理API/菜单链接 | LAN地址、专用端口、原厂菜单版本、生命周期 |

### 2.4 关键流程

| 流程 | 发起/处理 | 状态变化 | 失败处理 | 需求 |
|---|---|---|---|---|
| DNS 分流 | LAN/dnsmasq -> 插件 | 规则匹配，主控分配映射，返回响应 | 分配失败返回 SERVFAIL；不得错误返回直连 IP | REQ-001/006 |
| fake IP 连接 | LAN -> TUN -> Carrier | 查映射、选路径、封装数据与回注 | 映射刷新失败、无出口明确丢弃/报错，沿用既有恢复语义 | REQ-002/004 |
| 被动链路 | 其他 Probe -> WAN地址服务端 | 校验认证并接入 Carrier；WAN改变重建监听 | 非法身份/路由拒绝；端口冲突可观测；无WAN不回退全接口 | REQ-003/006 |
| 主动链路 | 本机 -> 配置服务端 | 建链、心跳、数据交换、重连 | 按既有退避重试；底层连接不得送入自身 TUN 回路 | REQ-004/006 |
| 重启/停用 | 固件生命周期 -> 内置服务管理 | 幂等恢复或移除业务拥有配置 | DNS/路由配置只在数据面就绪后切换；失败恢复原厂配置 | REQ-005 |
| 管理操作 | LAN浏览器 -> 插件Web/API | 认证、校验配置、应用业务状态、限量返回诊断 | 配置错误保留旧运行态；机密不返回；停用保留恢复路径 | REQ-009 |
| 菜单进入 | 原厂后台 -> CloudHelper入口 -> 插件控制台 | 仅导航到管理入口；独立认证 | 集成失败可撤销，不阻断原厂导航和直接控制台 | REQ-010 |

### 2.5 接口记录

| 编号 | 名称/提供方 | 调用方 | 输入、输出与错误契约 | 位置/兼容 | 关联 | 状态 |
|---|---|---|---|---|---|---|
| IF-001 | 插件 DNS | dnsmasq/LAN | TCP/UDP DNS，fake/real/refused/SERVFAIL；AAAA 策略待设计 | 新internal/dns + 华硕适配；旧DNS service仅作行为参考；监听地址待设计确认 | REQ-001；TASK-003；TEST-003/004 | 待设计 |
| IF-002 | 主控身份、规则和映射 | 插件 | 原有认证、规则同步、映射分配/查询；不可跨节点自行分配 | 新 `internal/controller`、`internal/policy`；参考 `probe_route_config_sync.go`、`probe_virtual_router.go` | REQ-001/006/008；TASK-002/003；TEST-003/008 | 兼容新实现待设计 |
| IF-003 | 12040 Carrier 双角色 | 插件与其他 Probe | 兼容协议/认证/帧格式；配置传输绑定活动WAN IPv4；地址变更重新监听，无WAN等待；客户端物理WAN出站 | 新 `internal/carrier` 和平台适配；参考 `probe_virtual_router_runtime.go`、`route_relay_client_transport.go` | REQ-003/004/006/008；TASK-005；TEST-006/007/008/012 | 新实现待设计与验证 |
| IF-004 | 内置服务生命周期 | 固件rc与小Web | start/stop/status/reconcile；开机/WAN/DNS/防火墙重建幂等；业务就绪后切DNS | 新生命周期模块与固件rc补丁 | REQ-005/011；TASK-013；TEST-009/012/019 | 待设计 |
| IF-005 | 定制固件发布清单 | 构建/固件更新 | 精确型号/上游commit/工具链/模块版本/资产/SHA256，错误型号拒绝；原厂备份/First回退已验证，完整发布恢复契约待TASK-008 | 新firmware/asuswrt构建发布适配 | REQ-007/011；TASK-008/012；TEST-011/018/020 | 基础来源/环境/补丁/镜像SHA与运行证据已在baseline.lock.json；完整产品发布及错误型号验收待TASK-008 |
| IF-006 | 插件管理Web/API | LAN浏览器 | 专用LAN HTTP(S)入口和认证；状态/配置/启停/日志/更新；写操作须保护会话和CSRF；不返回身份密钥；操作错误码与状态一致；路由/端口待设计 | 新internal/web、web静态资源 | REQ-009；TASK-010；TEST-014/015 | 待设计 |
| IF-007 | 固件菜单与页面 | 华硕后台浏览器 | 原生菜单到管理页/API；认证边界明确；停业务仍可管理；页与镜像版本一致 | 固件menuTree/Web补丁 | REQ-010；TASK-010；TEST-016/019 | 待实现，替代运行时挂接 |

### 2.6 架构决策引用

DEC-001已被DEC-012替代；DEC-002 TUN入口；DEC-003权威映射与协议兼容；DEC-004 dnsmasq前置；DEC-005/008已被DEC-009替代；DEC-006交付边界；DEC-007 WAN绑定；DEC-009新代码；DEC-010运行时菜单方案已替代，DEC-012固件内置主线。

## 三、单元设计

### 3.1 受影响单元

| 编号 | 文件/位置 | 职责和输入输出 | 依赖 | 需求 |
|---|---|---|---|---|
| UNIT-001 | 新 `router_plugin/Makefile`、`src/`、`include/`、构建发布配置 | 独立入口/依赖/产物、资源配置和升级身份 | 必要依赖与ARMv7支持 | REQ-005/006/007/008 |
| UNIT-002 | 新 `internal/dns`、`internal/policy`、`internal/controller` | DNS查询到规则/权威映射，受限缓存及配置快照；dnsmasq适配 | 主控、独立上游、平台 | REQ-001/006/008 |
| UNIT-003 | 新 `internal/dataplane`、`internal/platform/asuswrt` | LAN fake包到虚拟路径/回包；TUN/托管路由、防火墙规则 | 内核转发、统一映射与Carrier | REQ-002/008 |
| UNIT-004 | 新 `internal/carrier`、协议编解码/认证、平台WAN接口 | WAN监听与恢复、物理拨号、Carrier双向处理和有界队列 | 身份、证书、拓扑、活动WAN | REQ-003/004/006/008 |
| UNIT-005 | 新internal/lifecycle、internal/status及固件rc补丁 | 资源预算、服务启停、恢复停用；启动/WAN/DNS/防火墙事件 | 固件事件、JFFS配置 | REQ-005/008/011 |
| UNIT-006 | 新internal/web、web及固件Web补丁 | LAN认证/API与原生菜单；停业务仍可管理 | 生命周期、固件Web资源 | REQ-009/010 |
| UNIT-007 | 新firmware/asuswrt构建锁定/补丁/清单；WSL上游源码 | 型号/工具链、基础构建、rootfs集成、镜像发布恢复 | 匹配型号源码/闭源组件、隔离Linux环境 | REQ-005/007/011 |

UNIT-007历史SWRT启动兼容补充（官方53297原生JFFS2路径无需此补丁）：patches/0001-rtax55-preserve-stock-jffs2.patch修改锁定上游rc/ubifs.c，仅RTAX55目标且productid=RT-AX55生效；已挂载JFFS2直接返回，misc2只读挂载成功后remount为可写并进入公共用户目录初始化。原始文件系统数据不迁移、不擦除；remount失败保留只读挂载并报告错误，禁止落入UBIFS格式化路径。未识别到JFFS2时保留上游UBIFS行为。验证器检查生成ARM rc包含补丁标记；两次实机启动检查自动JFFS2、三把原密钥hash及ROM通过。日志服务晚于挂载启动，syslog缺挂载标记不作为失败；运行rc字符串只证明补丁存在，功能证据由自动挂载/原文件hash提供。

UNIT-007官方替换设计：基于已校验的官方TGZ，在本地WSL保留Linux大小写/符号链接；活动锁由官方版本、归档hash和sdk归档身份决定，无需完整Git历史。原生target/profile不改；仅外部SDK路径/宿主库映射和实际必要构建修复。原生misc2 JFFS2挂载取代SWRT UBIFS兼容补丁，运行rc完整hash取代旧标记断言。Upgrade分阶段脚本分别备份、选择A、上传/校验、原生写B、写后保留核验、B ONCE、健康、显式长期B、持久重启；每步回读及失败即停，不能从当前B调用inactive写入。旧B完整镜像已另存，新配置备份/原A/ROM保留结果和每次boot_id只本地忽略；测试必须绑定实际Wi-Fi源接口以避免有线外网造成假通过。

### 3.2 处理与异常规则

| 单元 | 正常规则 | 异常规则 | 兼容要求 | 验证 |
|---|---|---|---|---|
| UNIT-001 | 独立C模块使用官方ARM GCC交叉编译，在本机ARMv7/Linux4.1 ABI运行，依赖按契约引入 | 不支持时记录确切错误再调整；不靠改名旧产物绕过 | 不引入普通Probe/linux_router运行时；发布兼容 | TEST-002/008/010/011/013 |
| UNIT-002 | LAN 本地名称原厂处理，外部查询进入插件 | 禁止 DNS 上游循环；IPv6 行为必须收敛；缓存不得在映射失效后误用 | TCP/UDP、DNS TTL与全网映射一致 | TEST-003/004 |
| UNIT-003 | fake IP 和必要虚拟地址路由进入 TUN | 模块/接口未就绪不得切换 DNS；普通上网维持原厂路径 | 避免影响硬件加速和现有业务；保留现有 ICMP 能力作为回归观察 | TEST-001/005/009/010 |
| UNIT-004 | 同一节点同时listen/dial，服务端绑定活动WAN；变更地址精确重建 | 无WAN等待而非全接口监听；未认证拒绝；断线重连；拨号绕过接管；UDP监听失败报告 | 现有Probe可互联，不能只以端口监听判成功 | TEST-006/007/008/012 |
| UNIT-005 | 有归属的幂等配置，日志和缓存受限 | WAN/DNS/防火墙重建恢复；固件配置持久化；停用精确恢复 | 不修改 Wi-Fi/DHCP 或批量清空防火墙 | TEST-009/010 |
| UNIT-006 | LAN控制与认证，静态资源小型化；菜单仅导航入口 | 写操作校验与CSRF保护；秘密脱敏；停业务仍可恢复；菜单适配失败精确撤销 | 原厂菜单/登录正常；新API不默认信任原厂cookie；WAN无管理监听 | TEST-014/015/016 |

## 四、执行任务

UNIT-008 / IF-008：tools/router_serial.py及技能同名副本提供ports、capture、exec、terminal CLI，输出本地rx.bin/events.jsonl/result.json；exec返回远端退出码，工具超时/端口/协议错误单独报错；输入受长度限制、命令回显不能当标记、默认不记录TX机密，交互登录由用户输入。依赖pyserial==3.5，Windows为实测平台；非交互协议隔离测试和COM4实机只读验证闭环。

### 4.1 当前交接

- 当前阶段：TASK-024原厂恢复完成；按“继续开发”恢复TASK-023进行中，先完成JFFS安装包/原厂运行验证，继而业务互通与收尾。整机重编译已停止，历史固件业务补丁仅参考。
- 当前设备：B已由原始A备份恢复为原厂52332-gfbf3b9c，boot_id f9b282a3-b242-43d1-89dc-dac58ed3a30f；实际/下次均Second，Aseq16/commit1、Bseq17/commit1；A269个逻辑载荷和CFE保留，完整基础健康通过；noconsole0按开发期授权保持，已安装JFFS插件83dcc8ed5eb72c3f（默认关闭业务），同原厂后台端口。本次B早期CFE日志未捕获，证据边界见第十三章。
- 工具：仓库tools/router_serial.py与用户技能scripts副本SHA256一致，隔离venv在artifacts/serial-venv；8项协议测试在WSL通过，Windows7项通过/1项平台跳过；COM4执行、退出7、超时124及清理、零发送抓日志、交互uname与Ctrl+]退出实测通过。
- 原生常驻控制台：官方源码已有noconsole控制的login流程；开关0跨重启和原生登录验证通过，不使用SSH bootstrap；开发收尾恢复1并commit。
- 工作区：新增router_plugin C服务、原厂菜单/管理桥/rc与rootfs安装补丁、专用构建入口及主控asus_56uv2类型/传输能力适配；既有发布工作流、流量UI、备用出口等改动保留，不混入固件任务。
- 门禁：官方基础构建/普通启动/管理与基本网络完成门禁通过；ONCE及故障自动回退未验证，整体业务产品门禁仍不通过。
- 下一步唯一动作：实现共享网关代理配置、授权及节点持久化路由，随后移除旁路由/单臂路由重复入口并验证。REQ-025源码成果保留，新Android AAR/APK仍待完整NDK；未发布。

### 4.2 任务计划

| 编号 | 工作 | 状态 | 需求 | 文件/接口 | 完成条件 |
|---|---|---|---|---|---|
| TASK-001 | 建立需求账本并补录实机/代码事实 | 已完成 | 全部 | 本账本 | 五类产物、追踪和最新固件问题落盘 |
| TASK-002 | 提取兼容契约与新模块设计 | 待开始 | REQ-005/006/008 | UNIT-001~005、IF-001~005 | 协议、最小依赖、平台及资源预算明确，已有调查保留 |
| TASK-003 | DNS 与 dnsmasq 集成及 IPv6 策略 | 待开始 | REQ-001/006 | UNIT-002、IF-001/002 | fake/real/reject、无循环、IPv6和缓存策略验证 |
| TASK-004 | LAN fake IP 数据接入和回包 | 待开始 | REQ-002 | UNIT-003 | 指定出口 TCP/UDP、失败不直连、普通上网正常 |
| TASK-005 | 12040 服务端与客户端互联 | 待开始 | REQ-003/004/006 | UNIT-004、IF-003 | 双角色、认证、真实数据和重连均验证 |
| TASK-006 | 原厂独立插件安装/持久hook/卸载 | 已取消 | 历史REQ-005 | 历史UNIT-005/IF-004 | 固件内置方向由TASK-013替代，不继续原厂插件持久hook调查 |
| TASK-007 | 隔离回归、授权实机验证和完成门禁 | 待开始 | 全部 | TEST-001~022 | 每项需求有通过证据和可复查产物 |
| TASK-008 | 独立定制固件构建与发布清单 | 待开始 | REQ-005/007/011 | UNIT-001/007、IF-005 | V2镜像/型号/校验/来源清单、升级配置与恢复验证 |
| TASK-009 | 建立路由器新模块、资源治理与测试基础 | 待开始 | REQ-005/006/008 | 新router_plugin目录、UNIT-001/005 | 独立C模块/入口、平台边界、配置快照和资源限制；ARMv7编译；后续DNS/数据面/Carrier任务在新模块实施 |
| TASK-010 | 轻量Web与固件原生菜单 | 待开始 | REQ-009/010 | UNIT-006、IF-006/007 | 页面入镜像、菜单源码集成、LAN认证和浏览器验证 |
| TASK-011 | 固件备选源码可得性调查 | 已完成 | REQ-005/010相关可行性调查，未替代验收 | 本账本FACT-011、DEC-011；公开源码/工具链说明 | 提供可复查来源并区分公开目标、V2兼容与实际构建；不把文档命令当构建成功 |
| TASK-012 | 精确型号源码/工具链及基础固件构建 | 已完成 | REQ-005/007/011 | UNIT-007、IF-005 | 固定上游原配置构建、JFFS2兼容补丁重建、Second两次启动与网络回归、原First回退实际启动、ROM/完整First保留校验通过 |
| TASK-013 | 内置模块与原生生命周期 | 待开始 | REQ-005/011 | UNIT-005/007、IF-004 | rootfs/内核模块就绪；rc事件幂等；停用恢复和重启/WAN验证 |
| TASK-014 | 按用户名单精简并验证 | 待开始 | REQ-012/005/007 | UNIT-007及上游功能配置 | 基础可运行证据成立且收到精简名单后执行；配置/产物/资源差异与回归有证据；未收到名单不自行选择模块 |
| TASK-024 | 用原始A备份恢复物理B并启动验证 | 已完成 | REQ-024 | UNIT-008/IF-008、原生升级器、外部恢复报告 | B全部269逻辑块核验，两个metadata仅序号/CRC调整；B原厂52332启动和健康通过，A/CFE保留；TEST-042 |

TASK-015（已完成，REQ-015/014、UNIT-007）：在C:/Users/fengz/.codex/skills/asuswrt-router-upgrade-debug创建SKILL.md、agents/openai.yaml和references/rt-ax56u-v2.md；说明授权、A/B映射、写入/ONCE/健康提升与失败恢复。只创建技能，不执行live升级或重启。

TASK-016（已完成，仅源码检查阶段，REQ-016、UNIT-007）：用户已提供D:/迅雷下载/GPL_RT_AX55_300438653297.zip，核验官网SHA256、ZIP内TGZ及源码目标/板级配置/工具链和闭源预编译输入；其他两版仍为.xltd，不把预分配长度当下载完成。源码和报告保存在本仓库忽略的upstream/official及artifacts目录；官网哈希/ZIP与TGZ校验、实机基础型号/板级配置核对、预编译输入存在性检查及随包工具链ARM编译/QEMU运行通过。完整固件构建和整机运行未执行，不覆盖SWRT基线或进行实机升级。

TASK-017（已完成，REQ-016/017、UNIT-007）：切换baseline.lock.json和源码/SDK准备、构建/覆盖/下载脚本至官方GPL，旧SWRT锁/产物只作本地历史；按实际源码判断旧JFFS补丁，不跨版本盲套。新工作树来自已校验官方TGZ，使用随包ARM GCC及所需锁定辅助SDK，宿主库/路径适配与模块精简区分。

TASK-018（已完成，REQ-017、UNIT-007）：完整固件离线CRC/UBI/容量/rootfs/ARM/TUN/运行程序hash与QEMU验证；产物清单和升级前新版备份，门禁通过后再动设备。

TASK-019（已完成：按DEC-027明确普通B例外，REQ-014/015/017、UNIT-007）：官方B写入及回读通过；普通7选择后一次重启，完整UART/SSH证明官方B运行，PID1与构建哈希一致，基础健康和A/CFE保留通过。原ONCE门禁未通过，不宣称自动回退有效。详见TEST-035。

TASK-020（已完成，REQ-018、UNIT-008、IF-008）：实现串口工具、更新技能、隔离协议回归和实机只读往返。TEST-031覆盖回显假阳性、非零退出、分片和超时；TEST-032验证COM4命令、日志、临时shell清理及技能格式。准备门禁通过：当前范围/依赖/接口/测试已明确；仅临时RAM桥接，回滚为关闭本工具及临时shell。REQ-018→UNIT-008/IF-008→TASK-020→tools/router_serial.py及技能→TEST-031/032；整体固件门禁仍不通过。

### 4.3 变更记录

本轮新增firmware/asuswrt的构建环境、下载工具、固定版本清单、Linux工作树准备/构建脚本及JFFS2兼容补丁；修改本账本。源码/工具链缓存及敏感备份均在D:/Code/RouterFirmware，Linux构建仅本地BuildServer。已升级Second基础镜像并实机验证，DEF-003补丁重建及两次启动验证均完成；临时LAN Telnet使用后关闭，原厂配置未重置，原启动ROM和First镜像保留。不修改业务模块，未精简；固件回滚见第九章。

## 五、测试与验证

### 5.1 测试计划与结果

| 编号 | 目标 | 需求/任务 | 方法 | 预期 | 实际/状态 |
|---|---|---|---|---|---|
| TEST-001 | 实机基础能力 | REQ-005 / TASK-001/002 | `ssh localrouter` 读取 uname、nvram、`zcat /proc/config.gz`、模块目录、netstat、dnsmasq、df/meminfo | 精确设备与可用模块/资源 | 只读检查已完成，模块加载和接口创建未执行 |
| TEST-002 | 新模块ARMv7构建 | REQ-005/006/008 / TASK-009 | 在本地WSL使用锁定官方ARM GCC构建router_plugin C源码；readelf核对ARM/EABI/动态依赖，QEMU验证基本入口 | 必要依赖支持、ELF架构/体积有证据；实机启动单独验证 | 待开始；新模块尚未创建 |
| TEST-003 | DNS 规则和映射 | REQ-001/006 / TASK-003 | 隔离 TCP/UDP 查询 direct/proxy/reject、映射缺失/控制器失败、缓存TTL与重启映射一致性 | 真实/fake/refused正确，失败SERVFAIL且映射权威一致 | 待开始 |
| TEST-004 | DNS 原厂集成/IPv6 | REQ-001 / TASK-003 | LAN 查询内部名/外部名、AAAA、缓存旧应答、指定其他普通53服务器；DoH/DoT单独说明边界 | 无上游循环，IPv6策略不绕出口，原厂DHCP正常 | 待开始 |
| TEST-005 | fake IP 端到端 | REQ-002 / TASK-004 | 本地隔离入口/出口与 LAN 客户端请求 TCP/UDP，观察出口来源、回包、缺映射和断链 | 指定出口处理，失败不变直连；ICMP已有能力回归观察 | 待开始 |
| TEST-006 | 服务端互联 | REQ-003/006 / TASK-005 | 兼容 Probe 连接插件 12040，合法/非法认证、真实帧双向交换、配置传输层 | 合法 Carrier 可用，非法拒绝；按配置验证TCP/UDP | 待开始 |
| TEST-007 | 客户端与双角色 | REQ-004/006 / TASK-005 | 插件连隔离服务端，承载fake请求；同时接受其他Probe；模拟对端断开重连 | 双角色不冲突，端到端和重连有效，无TUN回路 | 待开始 |
| TEST-008 | 跨实现契约/必要旧产品回归 | REQ-006/008 / TASK-005/007/009 | 新模块测试采用现有编码器/解码器产生的协议向量、主控API与现有Probe对端；修改旧产品才运行其相关回归 | 新旧协议互通；不依赖普通Probe运行时；旧产品修改不回退 | 待开始；并发备用出口变化须对账 |
| TEST-009 | 内置服务生命周期 | REQ-005 / TASK-013 | 隔离事件模拟；后续固件重启/WAN/DNS/防火墙重建与停用测试 | 幂等恢复，普通上网/DNS/DHCP正常 | 待开始；替代独立插件安装/卸载场景 |
| TEST-010 | 资源与更新 | REQ-005 / TASK-002/013/007 | 测量构建/安装/更新临时空间、RSS峰值、CPU、吞吐；更新失败回滚 | 资源预算适配实机，更新失败可恢复；不承诺未测吞吐 | 待开始 |
| TEST-011 | 独立固件发布契约 | REQ-007 / TASK-008 | 检查镜像型号/格式/清单/校验、更新配置与错误型号拒绝；旧产品发布回归 | V2固件资产独立，不误装普通Probe程序 | 待开始 |
| TEST-012 | WAN精确绑定和变化 | REQ-003/004/005 / TASK-005/013 | 平台模拟WAN地址发现/变更/无地址；隔离网络检查TCP/UDP监听和客户端出站；实机验证须安排避免管理断联 | 服务端只有WAN IP:12040；无WAN不监听其他地址；变更后旧监听关闭，新地址可互联；客户端不走TUN | 只读确认当前WAN eth0/192.168.205.3，运行行为待开始 |
| TEST-013 | 路由器新模块资源边界 | REQ-008 / TASK-009/007 | 独立C构建/依赖图与入口检查；DNS并发、缓存、帧队列满载/慢对端、stdout/stderr及无日志文件写入测试；资源压力测量 | 无旧产品运行时依赖；配置上限有效；超限有明确行为；Flash写入可控；ARMv7资源结果有证据 | 待开始 |
| TEST-014 | 小Web控制功能和UI | REQ-009 / TASK-010/007 | 管理API配置校验、启停、状态/映射/链路/日志/更新测试；浏览器验证主要操作和窄屏 | 操作改变真实状态；错误清晰；小页面可用；停止业务后可恢复 | 待开始 |
| TEST-015 | Web访问与机密边界 | REQ-009 / TASK-010/007 | 检查LAN监听、WAN不可访问、未认证访问/越权写操作/CSRF、日志和响应秘密脱敏 | 管理不暴露WAN；认证/写入保护有效；机密不回显 | 待开始 |
| TEST-016 | 固件原生菜单 | REQ-010 / TASK-010/013 | 镜像菜单/页面检查，后续浏览器验证导航/认证/重启/停业务管理 | 菜单常驻且原厂导航正常 | 原厂入口只读确认，固件修改未执行 |
| TEST-017 | 固件候选源码与说明核查 | TASK-011 / REQ-005/010相关调查 | 2026-10-14浏览SWRT源码README、386分支Makefile和工具链仓库、Merlin作者Asuswrt说明 | 确认源码/工具链和明确构建目标；分开记录V2兼容、闭源组件与未运行构建 | 已完成只读来源核查：有RT-AX55目标；后续TEST-018/024/025已证明固定基线依赖完整和本V2实机运行；原厂52332精确GPL下载尚未核实 |
| TEST-018 | 基础固件构建/型号 | REQ-011 / TASK-012 | 固定上游/SDK rt-ax55构建；collect-baseline；verify-baseline.py；生成rootfs BusyBox/dnsmasq在QEMU执行 | 精确目标可构建，CRC/格式/大小/平台可复查 | 上游make完成；镜像35258388与36306964字节均小于51MiB，CRC/UBI头/ARM ELF/SquashFS v4/tun.ko检查通过；BusyBox echo与dnsmasq 2.90版本命令退出0；16:20修正WFI字段后重跑验证通过，两个镜像系统载荷一致，CFE ROM前缀差1048576字节；兼容镜像重建退出0，Second两次启动及基本网络已通过TEST-025 |
| TEST-019 | 内置镜像与平台事件 | REQ-005/010/011 / TASK-010/013 | 对比rootfs/模块/页面/rc入口；事件模拟及后续实机 | 内置正确、时序幂等、管理可用、容量合规 | 待开始 |
| TEST-020 | 固件更新与恢复 | REQ-007/011 / TASK-007/008/012 | 原厂镜像/救援入口核查；授权升级及旧分区启动 | 错误型号拒绝、配置兼容且可恢复 | 原厂恢复ZIP CRC通过；原生升级Second成功；First原厂52332实际回退启动通过（boot_id ecfb4a65-6b61-4532-bddf-53c4ee1ebcb0），再更新Second并两次启动通过。TFTP/ASUS救援刷写和错误型号负向试验未执行，有官方V2恢复步骤与本机备份 |
| TEST-021 | 原配置基线与阶段顺序 | REQ-012 / TASK-012/014 | 校验锁定commit与上游工作树diff，记录环境/构建修复；基础运行结果、用户精简名单与精简回归；检查新功能开始条件 | 基础阶段无自行删模块，精简有用户名单，新功能在精简回归后 | 固定上游下载/原配置编译及镜像验证完成；无功能模块修改，构建环境修正及JFFS2基础兼容补丁单独记录；实机基础运行通过，精简名单待用户提供 |

| TEST-022 | 下载/环境与交叉工具链 | REQ-011/012 / TASK-012 | Git对象齐全后导出tar；官方Ubuntu镜像分层hash；docker build及普通UID，工具链静态ARMv7程序与readelf；QEMU运行 | 源码/工具链锁定、编译器可执行并输出正确ARM ABI | 工具链17119文件/9824 unique blobs导出并解包；Ubuntu18构建环境完成；静态ELF ARM/EABI5软浮点编译通过，QEMU输出Asuswrt ARMv7 toolchain smoke test且退出0；源码177014 unique blobs/212350文件齐全，仓库快照逐blob校验并恢复Linux工作树，两仓库HEAD通过；不是完整固件运行验证 |
| TEST-023 | Git仅定制覆盖范围 | REQ-013 / TASK-012 | git check-ignore --no-index --stdin验证完整upstream/源码文档/临时manifest/产物被忽略，账本/自有README/版本/构建/补丁不忽略；暂存区检查无完整源树、SDK及无关备用出口账本 | 本Git只保留我们的修改与构建输入描述，固定上游加覆盖可复现 | 忽略边界验证已通过；整树git add已中止且没有源树进入索引；新下载脚本实际成功复用source/SDK归档且SHA256一致；构建前apply-overlays在固定HEAD工作树执行通过 |

TEST-024（REQ-014、UNIT-007、TASK-012）：首次无CFE ROM镜像35258388字节上传回读SHA256正确，设备firmware_check=1，hnd-write=99成功；Second序列15更新为17并实际启动。启动区mtd3/整Flash前1MiB及完整First原厂分区写入前后SHA256均不变，重启后前1MiB再次等于d3be3cb5ba9251eb5eaa1f4d8aacd9a4b3dfa2bcd500121e0d9ec130b2eba91f。版本、boot_id、15项LAN/WAN/DHCP/Wi-Fi/SSH配置保留检查通过；DNS、路由器WAN ping、LAN客户端经Wi-Fi直连example.com HTTPS得到200通过。兼容镜像重新通过原生写入99及保留校验，DEF-003与重启持久化已由TEST-025关闭；AiMesh/吞吐未测试。

TEST-025（REQ-011/014、TASK-012）：通过。兼容镜像两次实际启动（bbd3f846-0d02-4612-97c7-904abc61c17b、0ef52ae6-571d-4bab-9ac1-a1249ffd8545）均原misc2自动挂载/jffs为jffs2 rw,noatime，三把SSH host_key SHA256与原jffs.tar.gz一致，SSH/Web恢复；15项LAN/WAN/DHCP/Wi-Fi/SSH配置保持。第二次重启后ROM/mtd3与完整First回读SHA256仍等于备份，LAN TCP/UDP DNS都解析172.66.147.243，经Wi-Fi 192.168.52.170/ifindex13请求example.com TLS/HTTPS返回HTTP/1.1 200。Telnet为0且未监听23。证据位于外部备份目录compat-first-boot.json、compat-second-boot.json、compat-runtime-verification.json和compat-final-verification.json；没有再使用Telnet或手工挂载。

兼容镜像复建验证：build-baseline.sh执行原配置make rt-ax55退出0，verify-baseline.py退出0；45行补丁SHA256 f9bdda0dc39b7ef0ec4c64864292c2b95edca0f36b4fc4a08835b9021191881a，fs/sbin/rc为ARM且包含标记。无额外CFE ROM镜像仍35258388字节，SHA256 0f9a2d14b994119d5523e8edad78e563283b80f1d5b7e783aa439aa1b122ea8a；大封装36306964字节SHA256 2331c08cd834fc75cb1fce6f66eee40ee8d65254d23f1cd30121990ea09872eb。系统载荷一致，SquashFS30240768字节、tun.ko存在；报告与两镜像另存本仓库忽略的artifacts/jffs-compat，原始镜像未覆盖。锁文件保留首次已实机验证的原镜像信息，当前兼容镜像已刷写并通过两次启动，on_device_boot_verified=true，保留首次镜像信息便于追溯。Linux实际ubifs.c修改的git diff --check通过；补丁文件本身的上下文空格/制表符属于统一diff格式，由.patch专属whitespace属性处理，未改变C源码检查。

TEST-026（REQ-015、TASK-015）：skill-creator/quick_validate.py退出0；UI YAML、short_description长度、默认提示技能名、默认自动发现及引用文件存在检查通过。流程人工检查覆盖“当前B时先回A才能写B”“原生写入后覆盖长期B为ONCE”“健康前不得主动提升”“自动提升视为策略不符”“管理消失先检查Wi-Fi而非重刷”。没有执行一次试启动或故障模拟，不用静态技能校验当实机回退验收。

TEST-027历史初次检查（REQ-016、TASK-016，后续结果见22:40检查点）：本轮ssh只读nvram/uname/dmesg/proc/mtd退出0，确认productid RT-AX55、odmpid RT-AX56U_V2、armv7l、Broadcom BCM947622及两固件区各51MiB；当前版本RTAX55_R5.2.9-gbd30beb。git check-ignore确认建议源码包路径由/upstream/规则忽略，Test-Path为False。官方包未提供，源码目标/profile/依赖未检查，官方源码未编译或刷写。

TEST-027最新源码核查：见22:40检查点及忽略的firmware/asuswrt/artifacts/official-53297-inspection/source-usability.json；完整性、基础板型、预编译输入存在性及随包ARM编译/QEMU烟测通过；完整固件构建/运行未执行。

官方成品固件封装核查（TEST-027历史补充，非GPL源码验收）：本地已下载FW_RT-AX55_300438653370.zip为69276036字节，SHA256 e8643059c6bc33de621ec1ec04e20db970f9e6aec4d4751db3939c54c4ded997，与用户截图/官网一致。先前保存的V2官方页面标题RT-AX56U_V2、固件列表Model=RT-AX55且包含53370，官网来源为https://www.asus.com.cn/networking-iot-servers/wifi-routers/all-series/rt-ax56u-v2/helpdesk_bios/?model2Name=RT-AX56U-V2。ZIP中的74659500字节.w为COMFW_MAGIC=0x20210816的双载荷容器：100字节头、model_id13(RTAX55)的35389460字节UBI/WFI镜像（33.75MiB、chip47622、NAND3、flags2）、model_id14的39269940字节载荷；总长度精确匹配。RTAX55载荷小于本机51MiB分区。型号ID/结构来自现有comfw.h定义，华硕官方GPL包内容仍待用户提供；未把成品固件包当源码包、没有执行刷写。

社区调查（TEST-027补充）：2026-10-14浏览Merlin作者一手说明，https://www.snbforums.com/threads/netgear-to-asux-ax-merlin-please-suggest-model.94400/ （2025/04/14 #3）说明华硕GPL按型号提供专有组件预编译对象；https://www.snbforums.com/threads/rt-axe7800-bcm6756-gpl-build-research-missing-model-specific-prebuilts.97755/ （2026/08/17 #5）说明原厂GPL的prebuild文件可直接在prebuild/，Merlin多型号集成才用子目录，不因缺prebuild/RT-AX55/直接判包缺失。https://www.snbforums.com/threads/asus-merlin-support-ax56u-model-can-support-ax56u-v2-same-firmware.83298/ 中作者2023/01/31 #4明确当时无V2支持计划；该历史声明不替代当前实际包检查。SWRT原始支持列表明确RT-AX55/RT-AX56U_V2的BCM6755 A支持、BCM6756 B不支持，现有386 README有make rt-ax55。未找到直接解释官方52294包1.97GB为何大于另外两包或证明官方53297包在本V2成功编译运行的记录；包大小不作为完整性证据，仍待用户提供官方包检查。

### 5.2 未执行测试

TEST-002~016及019业务运行验收未执行，因为阶段顺序要求先基础运行、后用户指定精简、再新功能。TEST-018基础编译/离线/用户态QEMU及整机启动、TEST-020旧First回退和TEST-024/025升级持久性已通过；AiMesh/吞吐、错误型号负向试验及实际救援刷写未执行。不能用基础固件联网通过替代DNS fake IP/12040/Web新模块验收。

## 六、端到端追踪

| 需求 | 验收 | 单元 | 任务 | 文件/接口 | 测试 | 当前结果 |
|---|---|---|---|---|---|---|
| REQ-001 | DNS分类/权威映射/IPv6 | UNIT-002 | TASK-003/007/009 | 新dns/policy/controller/platform；IF-001/002 | TEST-003/004 | 新实现待开始 |
| REQ-002 | fake IP TCP/UDP及失败语义 | UNIT-003/004 | TASK-004/005/007/009 | 新dataplane/carrier；IF-003 | TEST-005/007 | 旧实现可参考，新插件未验证 |
| REQ-003 | WAN精确12040监听、变化恢复、被连接、认证和双向帧 | UNIT-004 | TASK-005/013/007/009 | 新carrier/platform；IF-003 | TEST-006/007/012 | 只读WAN确认，新实现待开始 |
| REQ-004 | 主动连接、承载请求、重连 | UNIT-004 | TASK-005/007/009 | 新carrier/controller；IF-003 | TEST-005/007 | 旧实现可参考，新插件未验证 |
| REQ-005 | 内置/持久化/事件恢复/停用 | UNIT-001/005/007 | TASK-002/013/007/008/012 | rootfs/rc；IF-004/005 | TEST-001/002/009/010/012/019 | 固件内置尚未实现 |
| REQ-006 | 协议/主控/既有产品兼容 | UNIT-001/002/004 | TASK-002/003/005/007/009 | 新controller/policy/carrier；IF-002/003 | TEST-003/006/007/008 | 契约待提取、新实现待验证 |
| REQ-007 | 专用固件资产/清单/更新 | UNIT-007 | TASK-008/012/007 | 固件构建发布；IF-005 | TEST-011/018/020 | 已调整形态，未实现 |
| REQ-008 | 专供路由器独立新代码/资源边界 | UNIT-001~005 | TASK-002/009/003/004/005/013/007 | 新router_plugin模块及IF-001~005 | TEST-002/008/010/013 | 新架构已记录，模块未创建 |
| REQ-009 | 小Web控制/状态/认证与恢复 | UNIT-006 | TASK-010/007 | 新internal/web、web/；IF-006 | TEST-014/015 | 已记录，待实现 |
| REQ-010 | 原生菜单/页面/重启与停用 | UNIT-006/007 | TASK-010/013/007 | 固件Web；IF-007 | TEST-014/016/019 | 取消运行时注入路线；未实现 |
| REQ-011 | 精确型号基础构建/镜像/恢复 | UNIT-007 | TASK-012/013/008/007 | 来源锁定/补丁/IF-005 | TEST-018/019/020/022 | 固定源码/工具链与覆盖构建、离线镜像/ARM验证、V2两次启动、First原厂回退及原ROM保留已通过；救援步骤与备份可复查 |
| REQ-012 | 原配置基础→用户精简→新功能 | UNIT-007及UNIT-001~006 | TASK-012/014/002~005/009/010/013 | 上游版本/配置diff及构建记录 | TEST-018/020/021/022 | 基础原配置构建和实机运行通过，兼容补丁单独记录；精简名单待用户，业务仍待开始 |
| REQ-013 | 固定上游+仅修改覆盖的Git范围 | UNIT-007 | TASK-012 | firmware/asuswrt脚本/锁定/patches及本账本，完整upstream/manifest本地忽略 | TEST-022/023 | Git忽略/覆盖入口及实际补丁重建已通过并提交3dffc397；完整上游/SDK/产物不入Git |

| REQ-014 | 保留启动ROM且基础固件启动 | UNIT-007 | TASK-012 | 原厂firmware_check、hnd-write及受控升级脚本 | TEST-018/020/024 | 原生无CFE ROM镜像升级Second与两次启动通过；原ROM和完整First写入前后/最终回读SHA256不变 |

| REQ-015 | A保留/B试启动/健康后长期的可复用技能 | UNIT-007 | TASK-015 | 用户级skills/asuswrt-router-upgrade-debug | TEST-026 | 技能创建与静态校验通过；ONCE及故障回退实机仍未测试 |
| REQ-016 | 官方GPL源码支持核实 | UNIT-007 | TASK-016 | 忽略的upstream/official包与本账本 | TEST-027 | 官方53297包完整性、基础型号/profile匹配、随包ARM工具链烟测通过；完整依赖、固件构建/启动未证明，REQ-016继续 |

| REQ-017 | 官方基线替换、编译和升级验证 | UNIT-007 | TASK-017~019 | 来源锁、构建/patches、忽略的产物/备份 | TEST-028~030/035 | 已完成，DEC-027普通B例外；ONCE仍未通过 |

## 七、决策与冲突记录

### 7.1 决策

| 编号 | 采用方案 | 依据/替代方案 | 影响/状态 |
|---|---|---|---|
| DEC-001 | 优先在当前原厂固件安装插件，不以刷机为前提 | 实机带TUN/TPROXY模块；未发现必须刷机的证据；替代为确有缺口后再评估固件 | 全任务；已替代，见DEC-012 |
| DEC-002 | 初始采用TUN接收fake IP包 | 可参考既有包转发和当前模块；新实现按路由器设计；TPROXY为可替代入口 | UNIT-003；设计有效，实机待验证 |
| DEC-003 | 保持主控权威映射、节点身份和Carrier协议兼容 | 复用外部协议契约而非旧产品代码结构；避免地址冲突；12040不是通用SOCKS端口 | UNIT-002/004；有效 |
| DEC-004 | 原厂dnsmasq前置，插件作为外部查询上游 | 保留LAN名称/DHCP；替代为直接DNS接入；前置会使插件通常看不到原始客户端身份，按客户端DNS规则暂不承诺 | UNIT-002；设计有效，生命周期待查 |
| DEC-005 | 精简插件适配，不直接安装现有完整linux_router | 原先产品形态待定；用户明确独立发布产物后由DEC-008收敛 | UNIT-001/005；已替代，见DEC-008 |
| DEC-006 | 保持生产在线更新路径，设计首装引导与资源预算 | 历史交付约束；本次只读SSH未修改部署 | UNIT-005；有效 |
| DEC-007 | 12040服务端精确绑定活动WAN IPv4，未就绪不回退wildcard/LAN | 用户明确要求；当前WAN由nvram与ip addr共同确认；不把WAN私有地址当公网地址 | UNIT-004/005、IF-003；有效 |
| DEC-008 | 独立插件构建/资产/安装与更新身份，共享已有核心源码 | 历史方案；用户进一步要求路由器专用新代码；独立资产原则保留，共享实现方案取消 | 已替代，见DEC-009；曾替代DEC-005 |
| DEC-009 | 新router_plugin模块、独立入口/依赖/实现/测试，参考旧协议行为并跨实现验证 | 用户最新方向优先；按内存/Flash/WAN/原厂生命周期设计；不用普通Probe产品裁剪或新tag充当实现 | 全部单元、REQ-008；有效，替代DEC-008共享实现部分 |
| DEC-010 | 小Web独立LAN服务，原厂增加可撤销菜单导航；不修改只读固件镜像 | 用户要求控制Web/菜单；当前menuTree入口可读但/www只读；候选运行时绑定挂载未验证；不默认继承原厂登录会话 | 运行时菜单方案已替代，见DEC-012；LAN管理/认证边界保留 |
| DEC-011 | 固件备选优先评估基于Asuswrt的定制，并将独立路由器模块内置 | 用户询问自做固件与源码；SWRT提供386/RT-AX55构建说明，实机productid为RT-AX55；这是候选关联，不能替代V2型号/驱动/分区/镜像校验 | 既有调查保留；备选地位由DEC-012改为主线 |
| DEC-012 | 基于精确型号Asuswrt做定制固件，内置独立新模块及原生Web/生命周期 | 用户最新倾向固件内置；先基础构建再集成；内核转发/TUN与用户态业务边界保留 | 有效，替代DEC-001、DEC-010运行时菜单及DEC-011备选地位；REQ-005/007/010调整，新增REQ-011；TASK-006取消由TASK-013替代 |
| DEC-013 | 先原配置基础编译/运行，后用户指定精简，再新功能 | 用户明确指定顺序和精简选择；不得先删除SWRT附带模块；必要构建修复保存补丁和原因 | 有效；补充DEC-012，REQ-012/TASK-014/TEST-021；新功能任务暂不启动 |
| DEC-014 | 固件源码放本仓库firmware/asuswrt，WSL只作Linux构建工作树/临时产物 | 用户明确代码放本仓库 | 位置原则有效，Git/子模块存储方式由DEC-015替代；UNIT-007/TASK-012及准备脚本调整；阶段顺序不变 |
| DEC-015 | 本仓库保留普通源码基线快照及版本/Linux布局清单，无嵌套Git或子模块 | 用户明确不需完整Git；网络下载Git缓存只作仓库外临时下载工具 | 覆盖DEC-014的存储方式；全体目标文件仍保留，大小写冲突文件采用可恢复布局；阶段顺序和模块配置不变 |

| DEC-016 | 上游参考文档/编译过程文件忽略，本账本和自有构建说明正常入Git | 用户澄清其文档指ASUS/源码文档，不是Codex账本；保留文档目录内构建配方/源码/许可 | 有效，修正此前对文档范围的解释；REQ-013/TEST-023；用户授权本次git commit，构建尚未完成不推送/发布 |

| DEC-017 | 固定上游版本加本仓库定制覆盖，完整源树仅本地忽略 | 用户最新明确只保留需修改部分，不把源仓库搬入Git；源码已下载但不是必须全部跟踪 | 有效，替代DEC-015的完整基线跟踪方式并收紧DEC-016；REQ-013/TEST-023；停止整树暂存，Git仅留脚本/锁定/补丁/定制代码和账本 |

DEC-020：后续路由器升级默认策略改为保留物理A原厂/CFE ROM，从A仅写B，写入核验后明确B ONCE，实机健康通过后再提升长期B并重启验证。技能已固化流程，但本轮实际基础升级曾使用长期B，不能倒写成已试启动；设备帮助和源码提供6/8，CFE消费/失败回退/固件自动commit尚未实测。共享JFFS/配置回退独立验证。关联REQ-015/014、TASK-015、TEST-026；下一次实际升级前完成ONCE语义核对，不修改启动ROM。

DEC-021：官方GPL作为独立候选核查，不能将先前SWRT构建/实机证据归因华硕官方源码。前一轮官方Source Code接口列出386_53297（2026/01/30，1.14GB）、52294和49559，未列原厂52332对应GPL；候选包内容未验证。用户接管下载，源码包就位前不切换现有锁文件/构建来源。

### 7.2 冲突

DEC-018：用户明确授权升级，覆盖此前“刷写尚未授权”的操作边界；遵守尽量保留启动ROM，选用无额外CFE ROM前缀镜像，并依据原生校验/写入机制执行。关联REQ-011/012、UNIT-007、TASK-012、TEST-018/020；当前先备份与核查，没有立即绕过设备校验。

DEC-019：基础实机启动暴露原厂JFFS2与上游UBIFS默认路径不兼容，采用针对RT-AX55的只读探测与保留挂载补丁，不通过格式化/迁移分区恢复SSH。不改变上游功能配置，不属于用户尚未指定的模块精简。更新第二分区前暂时启动保留的First原厂镜像，避免原生inactive写入接口覆盖唯一原厂回退。关联DEF-003、UNIT-007、TEST-025；构建与两次实机复测通过。

| 编号 | 输入 | 处理结论 | 影响/状态 |
|---|---|---|---|
| CON-001 | 历史评估称实机未知；当前SSH已有明确事实 | 当前实机证据优先，保留未验证模块加载/启动事项，不把旧未知当阻塞 | 架构/FACT/TEST；已解决 |
| CON-002 | 原先假设可直接运行已有Linux产品；代码限制amd64/arm64且有额外功能 | 精简适配及ARMv7构建单独验收，不直接套用已有产物 | UNIT-001/TASK-002；已解决 |
| CON-003 | 本任务与备用出口任务修改同一共享数据面 | 既有改动保留；实现前复核状态和差异，必要时隔离checkout；不覆盖备用出口账本 | 工作区/回归；已解决，持续监测 |
| CON-004 | 账本先前拟复用旧产品核心源码；用户希望专供路由器的新代码 | 用户最新指令优先；新模块重设计，旧实现只作为协议/行为参考；协议兼容与发布独立要求仍有效 | REQ-006/008、架构、单元、TASK-002/009、TEST-013、DEC-009；已解决 |
| CON-005 | 历史无需刷机插件/运行时菜单与固件内置方向冲突 | 最新方向优先；保留核心协议/新代码/12040，调整交付/菜单/生命周期；方向改变不产生刷写授权，后续DEC-018已收到明确升级授权 | DEC-012、REQ-005/007/010/011、TASK-012/013、TEST-018~020；已解决 |
| CON-006 | 之前不默认纳入SWRT额外组件，用户明确先编译再指定精简 | 保持本轮上游target的模块配置；用户决定后续精简。构建依赖选择不算业务模块精简 | DEC-013、REQ-012；已解决 |

| CON-007 | 先前错误地把用户文档忽略要求用于本账本；用户明确指ASUS/源码文档 | 最新澄清优先；撤销账本忽略/取消索引删除，恢复自有README跟踪，改为上游参考文档忽略并复测 | DEC-016、REQ-013/TEST-023；已解决 |

| CON-008 | 之前开始暂存完整普通源码快照；用户进一步明确Git只保存修改部分 | 用户最新指令优先；Ctrl-C结束本轮Git add，确认Git进程已退出且残留index.lock为本轮零字节锁后清理；没有完整源树进入索引；保留本地下载与运行中的编译 | DEC-017、REQ-013/TEST-023、Git暂存与构建说明；已解决 |

## 八、缺陷记录

- DEF-003（启动兼容，已修复）：原厂misc2是JFFS2，上游UBIFS路径未挂载导致Dropbear缺主机密钥。45行patches/0001-rtax55-preserve-stock-jffs2.patch先只读识别原数据，再remount可写，保留原分区和密钥；固定上游+补丁镜像重建通过、两次启动自动挂载且三把原host_key一致。临时LAN Telnet已关闭，升级后没有再手工挂载。REQ-011/014、UNIT-007、TASK-012、TEST-025闭合，不属于模块精简。

- DEF-002（升级流程，已修复流程并复测）：原厂stop_upgrade/stop_misc关闭Dropbear导致SSH连接丢失；脚本未自动重启。使用备份内管理凭据认证登录（未回显）并发送apply.cgi的rc_service=restart_sshd，SSH恢复。读取原始日志证明第一次写入成功，未重复刷写；已核对启动区及原厂分区保持一致。关联REQ-011/014、TASK-012、TEST-020/024。本次兼容升级脚本在hnd-write后自动restart_sshd，恢复连接并先核对写入/分区/ROM，随后明确重启，复测通过。

- DEF-001（低，验证工具，已修复）：verify-baseline.py将WFI_TAG第4项误称token_version、第5项误称flash_type；上游bcmTag.h实际定义为wfiFlashType和wfiFlags。修正字段命名与报告，保留CRC/版本/芯片校验并显式检查128 KiB NAND类型3；重跑TEST-018通过，两镜像flags为6/2。加入系统载荷一致性验证，结果通过。关联REQ-011、UNIT-007、TASK-012；固件字节与SHA256不变，不需要实机回滚。

基础兼容缺陷DEF-003已关闭，新业务模块尚未实现。构建收集问题ENV-002：编译运行期间修改build-baseline.sh使Bash后续读取偏移发生unexpected EOF；上游make已完整结束且镜像齐全，当前脚本bash -n通过。增加独立collect-baseline.sh收集既有产物并实际通过；后续不修改运行中的脚本。首轮环境问题ENV-001：prebuild_checks缺xxd退出2；附带上游自动Git标签无身份报错。修复仅Dockerfile补xxd及构建容器内临时Git作者/提交者身份，不改模块配置；复测已越过原失败环境检查，进入宿主工具构建；最终原配置镜像及兼容镜像构建都通过。现有DNS对proxy域名的非A查询返回真实解析，是IPv4入口插件设计中的IPv6绕行风险，关联TEST-004，尚未修改或声称修复。

- DEF-004（验证假设，已修正）：TEST-025首次要求挂载成功标记进入syslog，实际无该日志而功能成功。上游rc.h:1745将start_jffs2映射start_ubifs，init.c:20137/20622调用挂载早于20861的start_logger。外部wait-router-compat-boot.py改为核对运行rc补丁标记、自动挂载及三把备份host_key SHA256；保留初次失败输出。两次重启功能验证通过，无固件代码变化，也未隐藏运行故障。

## 九、回滚方案

用户已授权升级。备份位于D:/Code/RouterFirmware/router-backups/20261014-localrouter，含settings.cfg、nvram文本（机密不入日志/Git）、JFFS、启动区mtd3与整Flash前1 MiB、当前原厂image-mtd4及SHA256清单。同版本原厂386_52332完整ZIP校验无错误，已提取原厂恢复镜像；优先保留当前First原厂分区，由原厂写入接口处理Second。写入失败不自动重启；新固件启动失败按旧分区/V2手册救援恢复，不用dd直接写整Flash。构建Shell变更仅容器内，宿主机不变。

| 风险 | 触发条件 | 计划回滚 | 验证 |
|---|---|---|---|
| DNS切换 | 插件启动/上游/映射失败 | 恢复安装前dnsmasq上游，清理仅插件创建的DNS配置，按固件机制重载 | LAN真实DNS与本地名称、DHCP正常 |
| 路由/防火墙 | 普通上网异常或fake回包失败 | 移除插件所属规则/路由/TUN，避免flush全表；卸载模块前确认未被其他服务使用 | 默认路由、原厂转发、管理连接正常 |
| 安装/升级 | 体积不足、校验/启动失败 | 原子切换前保留旧程序/配置；失败回退；不先删除原部署 | 旧版本健康、DNS/链路恢复 |
| 菜单/Web | 导航/认证异常 | 回退固件Web补丁并重建镜像；运行中停止业务仍留管理入口 | 原厂登录/导航及LAN管理正常 |
| 固件 | 启动/联网失败 | 用已验证原厂/基础镜像及救援步骤恢复，刷写前保留配置与恢复文件 | 原厂启动/LAN/Wi-Fi/DNS/DHCP/NAT正常 |

## 十、已验证事实

| 编号 | 事实 | 证据 | 影响 |
|---|---|---|---|
| FACT-001 | localrouter 指向192.168.52.1；SSH连接成功，UID0 | 本聊天 `ssh -G localrouter` 和远端/proc/self/status | 诊断目标准确；连接授权不等于安装完成 |
| FACT-002 | RT-AX56U_V2；productid RT-AX55；4.1.52；armv7l四核；原厂386_52332 | 本聊天uname/nvram/cpuinfo只读结果 | 精确平台，需要32位ARM支持 |
| FACT-003 | CONFIG_TUN=m，tun.ko存在；TPROXY/socket模块与iptables扩展存在；策略路由开启 | config.gz/模块目录检查；当前/dev/net/tun缺失 | 有基础模块，未证明可加载或完整运行 |
| FACT-004 | 总内存250620kB，当时MemAvailable127492kB；JFFS15MiB、可用约14.1MiB；根文件系统只读 | meminfo/df/mount | 必须测体积、运行峰值和升级空间；值会变化 |
| FACT-005 | 原厂dnsmasq监听LAN/loopback53并提供DHCP，使用动态绑定和servers-file | 本聊天ps/netstat、/etc/dnsmasq.conf | 可设计前置DNS集成；持久hook仍未知 |
| FACT-006 | 当前发布无ARMv7；linux_router产品限制amd64/arm64 | release.yml、product_profile_linux_router.go | 不能直接下载已有release安装 |
| FACT-007 | 旧Probe中DNS/TUN与12040服务端/拨号/重连实现存在 | DNS service、virtual_router runtime及dataplane源代码 | 作为新模块协议/行为参考，不证明新模块已实现 |
| FACT-008 | 初始备用出口未提交修改为历史，本轮工作区仅本账本改动 | 本轮git status、HEAD08dc24b | 不将历史并发状态当当前阻塞或固件成果 |
| FACT-009 | 2026-10-14 10:57选定WAN unit0、接口eth0、IPv4 192.168.205.3/24，协议static；LAN br0为192.168.52.1/24 | `ssh localrouter`读取wan_unit/wan0_ifname/wan0_ipaddr，`ip -4 addr show`交叉验证 | 当前12040目标为192.168.205.3:12040；WAN为私有地址，公网可达性需上级网络验证，地址不得永久硬编码 |
| FACT-010 | 原厂state.js的show_menu加载/require/modules/menuTree.js；菜单点击使用location.href；/www属于只读SquashFS；无user*.asp与overlayfs支持 | 11:02本聊天SSH只读ls/find/head/sed/config.gz/mount结果 | 有菜单适配候选入口，未证明可运行扩展；需文件级可撤销方案和生命周期验证 |
| FACT-011 | 公开SWRT Asuswrt源码README列出386分支make rt-ax55，工具链仓库含ARM/Linux4.1版本；Asuswrt含二进制闭源驱动，不是全部组件开源 | https://github.com/SWRT-dev/asuswrt-bcm 、https://github.com/SWRT-dev/bcmhnd-toolchains 、https://github.com/RMerl/asuswrt-merlin.ng/wiki/About-Asuswrt ；386/release/src-rt/Makefile含RT-AX55条件 | 所需平台源码已完整下载并执行基础编译；本V2固定基线升级与两次启动证据已在TEST-024/025形成；原厂386_52332精确GPL包本次未核实 |
| FACT-012 | 候选target定义RT-AX55、PROFILE=947622GW、ODMPID=y、LINUX_MTD=64，并带SOFTCENTER/SMARTDNS等扩展 | https://raw.githubusercontent.com/SWRT-dev/asuswrt-bcm/386/release/src-rt/target.mak | 有平台/ODM线索，后续固定基线实机已验证，不能推广任意同名固件；按用户指令保留候选上游全部目标模块配置，包括额外组件 |
| FACT-013 | 本地WSL BuildServer为Debian13.5/UID1000，当时磁盘可用949G；gcc/make/python2未找到 | 本轮wsl只读检查，command -v退出1 | 初始环境事实；当前隔离Ubuntu18镜像与源码/SDK构建已完成，不全局改shell |
| FACT-014 | 源码下载候选386远端commit bd30beb222c9d4aa2386c2b5d8becd4cd787b534；工具链master commit7710a1e09d994598ac6c2db8ab16dc54ca5aed3d | git ls-remote源码分支、Windows curl工具链branch API；完整下载后再核对实际HEAD | 下载已完成，Linux实际HEAD与两个锁定commit一致 |
| FACT-015 | 官方Ubuntu18 amd64配置digest f9a80a55f492e823bf5d51f1bd5f87ea3eed1cb31788686aa99a2fb61a27af6a；归档sha256 fe17469c6068c8713e472af9ad49ef4d97299e0465b4f77625034e22abe27585；docker load成功 | tools/download_ubuntu_image.py实际执行与分层hash检查；本地BuildServer Docker name=dev-pc | WSL registry fakeIP访问失败，使用Windows下载+本地load；构建环境已生成，首轮编译发现xxd需补齐 |

| FACT-016 | 工具链所选目录9824 unique blobs齐全，tar584765440字节；Linux提取完成，ARM GCC生成ELF32/ARM/EABI5 soft-float静态程序，要求Linux4.1 | toolchain-batches.log、工具链tar、WSL交叉编译与readelf输出 | 工具链构建基础通过；不是完整固件运行证据 |
| FACT-017 | 实机设备树Broadcom BCM947622；productid RT-AX55/odmpid RT-AX56U_V2；image与image_update分区为0x03300000（51MiB） | 本轮ssh localrouter只读nvram、/proc/mtd、设备树 | 芯片与候选profile一致，镜像仍须格式/容量校验；不得把重复逻辑分区简单相加作为Flash可用量 |

| FACT-018 | 交叉编译静态ARM程序在本地QEMU实际运行通过；扩展构建环境镜像ID5c4abe8b7811e77058c0a9f2fed784b6de8b9b0ddc97e417b3f629fec76beb27 | docker run qemu-arm /workspace/artifacts/toolchain-smoke-armv7输出固定测试字符串并退出0 | 证明工具链/目标程序验证环境可用；不证明Broadcom固件整机启动 |

| FACT-019 | SWRT发布者支持列表明确列出RT-AX55/RT-AX56U_V2，支持bcm6755(hwver:A)，不支持bcm6756(hwver:B) | 2026-10-14 13:05浏览 https://github.com/SWRT-dev/Supported_Devices/blob/master/Supported_Devices.md | 精确V2存在公开支持依据；实机平台/镜像兼容仍需独立核对，不能将任意RT-AX55版本刷入 |

| FACT-020 | 固件源码基线普通快照已生成在本仓库firmware/asuswrt/upstream/asuswrt-bcm，无嵌套Git/子模块；212350文件（168450普通文件、41469可执行文件、2431链接），259路径便携存储 | baseline.manifest.json、materialize_repo_source.py实际逐blob hash与内容验证；源码commit bd30beb222c9d4aa2386c2b5d8becd4cd787b534 | 物理快照仍本地保留；按后续DEC-017不纳入Git，构建输入改为固定上游加Git内定制覆盖；不是启动通过证据 |

| FACT-021 | 两个镜像来自bcmImageMaker的--image/--fsonly，系统载荷完全一致；cferom镜像额外带1048576字节CFE ROM前缀及不同WFI尾 | 上游build/Makefile:883、hostTools/scripts/bcmImageMaker、shared/opensource/include/bcm963xx/bcmTag.h；实际镜像逐字节比较和修正后verify-baseline.py退出0 | 属同次构建两种封装，非两个系统版本；无额外ROM封装已经firmware_check/hnd-write实际验证，ROM回读不变；不推荐含ROM封装用于本升级 |

FACT-022：兼容镜像已写入Second并两次实际启动；原厂First已实际回退启动。boot_id及SHA256详见baseline.lock.json/on_device_verification与外部compat-*.json，报告均不入Git，未加入任何业务能力。

FACT-023：第二次重启后原ROM前1MiB/mtd3 SHA256 d3be3cb5ba9251eb5eaa1f4d8aacd9a4b3dfa2bcd500121e0d9ec130b2eba91f，完整First 51MiB SHA256 5877cf14088f149b5195820c15b0a45d1467e7b51ef5b7ead0ce7925dd50257a，均与原备份一致。

## 十一、风险与阻塞

历史SWRT阶段连接阻塞已解除且升级完成（官方迁移当前见下文/最新检查点）：用户网口保留外网，沿用Wi-Fi；Windows自动重连未及时触发，netsh connect因位置权限拒绝；不改变隐私权限，使用WlanConnect直接请求已保存配置成功，两次重启后SSH可用。当前Second运行兼容基础镜像，没有实际连接阻塞。后续只等待用户精简名单。

| 编号 | 类型 | 风险/证据 | 缓解/下一动作 | 状态 |
|---|---|---|---|---|
| RISK-001 | 兼容 | 新模块必要依赖尤其QUIC可能不支持ARMv7/旧内核，尚未选择和编译证明；旧Probe依赖不能自动当成新模块必需 | 按协议契约选择最小依赖并隔离构建，记录确切错误后调整 | 未关闭 |
| RISK-002 | 存储/内存 | 14MiB可写空间且256MiB内存，无swap | 体积/RSS/升级峰值预算、缓存日志限额；不假设压缩即可运行 | 未关闭 |
| RISK-003 | 固件事件 | 原厂脚本hook路线取消；固件rc/WAN/DNS/防火墙函数/时序待查 | 源码锁定入口，幂等和异常顺序测试 | 未关闭 |
| RISK-004 | DNS | IPv6绕行、缓存映射失效、上游循环；DoH/DoT不等同普通53查询 | 明确AAAA策略、映射TTL；独立上游；DNS接入边界记录 | 未关闭 |
| RISK-005 | 转发 | 固件硬件加速、LAN/TUN转发策略、底层Carrier路由影响未知 | 限定fake网段，实际验证请求/回包，不盲目关闭全局加速 | 未关闭 |
| RISK-006 | 网络 | WAN当前为私有192.168.205.3；监听12040不代表互联网可达，NAT/上级条件未查；WAN切换须恢复 | 精确WAN绑定，配置所需TCP/UDP规则并从授权对端验证；公网场景检查上级转发 | 未关闭 |
| RISK-007 | 并发 | 备用出口改动涉及共享路由和映射 | 实现前对账，必要时隔离；不覆盖既有修改 | 持续监测 |
| RISK-008 | 新实现兼容 | 重设计可能遗漏旧认证、帧格式、拓扑或映射语义 | 契约表/旧实现协议向量/现有Probe双向互联；不以自测双方相同错误判兼容 | 未关闭 |
| RISK-009 | 菜单/管理 | 原运行时注入不再主线；固件Web认证/API/停业务管理待实现 | 原生菜单及版本一致、浏览器/认证验证 | 未关闭 |
| RISK-010 | 固件主线 | 本V2 BCM6755型号/驱动/分区已核对，固定基线构建和两次启动/网络通过，原First回退验证及ROM保留通过；AiMesh/吞吐仍未验证 | 后续精简/集成各阶段继续做同项回归；不可将此基线结论推广到其他硬件版本或镜像 | 基础启动风险已关闭，功能兼容持续验证 |

历史SWRT基础运行通过；本轮官方53297完整构建/离线检查/原生写B完成，但ONCE实际两次回A，普通B启动例外等待用户批准。当前运行原厂A，原ROM保持、原A全部269个UBI逻辑载荷保持，raw有已解释的正常UBI元数据搬迁。新功能仍在精简回归之后。

## 十二、质量门禁

### 12.1 准备门禁

| 检查项 | 结论 | 条件/证据 |
|---|---|---|
| 最新目标、范围和约束 | 通过 | 三类核心能力与当前固件方向记录 |
| 验收可观察 | 通过 | REQ-001~012，TEST-001~022 |
| 架构/单元达到可实现程度 | 有条件通过 | 新模块协议契约、最小依赖、资源预算、固件构建目标/工具链、rc入口及AAAA策略须在相关实现前关闭 |
| 每项需求已有任务与测试 | 通过 | 追踪矩阵 |
| 工作区基线与已有改动 | 通过 | HEAD及FACT-008 |
| 高风险回滚思路 | 有条件通过 | 精确型号/基础构建/镜像校验与恢复步骤须在刷写前确定 |
| 无未解决方向冲突 | 通过 | CON-001~006处理结论 |

- 门禁结论：有条件通过。
- 允许继续兼容调查、隔离构建和设计；不以门禁条件自行增加用户确认要求。涉及实机业务配置时依据当时用户授权与准备结果执行。

### 12.2 完成门禁

- 门禁结论：不通过（整体产品）。基础构建与升级子阶段通过，DEF-003及TEST-025已关闭；新业务模块未实现，TEST-002~016及019业务运行验收未执行。精简名单待用户，不将基础联网通过标成全部需求完成。
- 需求跟踪已启动，但整体固件任务不得标为已完成。

## 十三、检查点

### 2026-10-15 原厂B恢复后继续插件

DEC-037：用户明确继续开发，恢复TASK-023为唯一进行中。当前原厂B52332和boot_id f9b282a3-b242-43d1-89dc-dac58ed3a30f已SSH复核，JFFS空余13332KiB；不再改启动选择、重启或Flash系统。ARM91516字节、原厂52332库/QEMU核心和原厂BusyBox sh -n门禁本轮通过，确切二进制SHA256f470aa42ca4ce2916569d8b8db6aa4ecf26c7e87c15d559f73d40e832943a850。准备门禁有条件通过：先验证原厂dnsmasq独立端口SIGHUP；安装默认禁用业务，只启用LAN管理和菜单，保留停止/卸载恢复路径。不等待真实主控凭据，主控/Carrier互操作使用隔离fixture，实机注册待用户填入。

DEF-007 / DEC-038：首次JFFS安装时程序初始化直接退出0（没有管理就绪输出），未接管DNS。QEMU syscall定位至SSL初始化读取/proc/self/exe后exit0；官方openssl-1.1/ssl/ssl_init.c的invalid_program明确只允许/usr、/bin、/sbin等路径，非libcurl ABI错误。既有--version/核心门禁未覆盖SSL初始化，此处不能视为原厂运行通过。适配采用JFFS持久化＋受管512KiB tmpfs的/usr/local运行路径，RAM仅复制约90KiB程序，保留原share→/tmp/share；仅在原目录结构完全匹配且无他方挂载时启用，不设置/tmp/asusdebug、不修改库。停止/卸载解除自有挂载。新增确切运行路径下--check-config必须输出configuration valid的门禁，并扩展安装升级回滚卸载测试；首次失败安装保留配置供修复，cron已停止，DNS未变。

### 2026-10-15 用户要求回到原厂A（当前动作）

DEC-036 / CON-003：用户进一步明确恢复B内容，替代DEC-035仅切A即完成的解释。REQ-024：物理B恢复为A备份同一原厂52332系统，A与CFE保留；UNIT-008/IF-008复用原生inactive升级与串口验证。TASK-024进行中，TASK-023暂停本轮业务开发；TEST-042为恢复前后CFE原始hash/A逻辑载荷、原生镜像校验/写B回读、原厂启动与网络健康。先验证A备份及安全升级封装，切A后才允许写inactive B；如无需重编译即可使用备份封装，不构建新系统。当前下一步唯一动作：新建外部恢复证据并核验A备份/CFE与原生升级封装。

恢复准备检查点：外部证据D:/Code/RouterFirmware/router-backups/20261015-restore-b-from-a，当前B完整51MiB备份before-second.bin已保存；CFE哈希与原备份一致，原厂A269个逻辑块/EC/VID CRC全部一致。由原始A备份去除139个空闲EC块、保留全部269个已分配块，附SDK定义WFI尾（0x5732/0x47622/NAND128/flags2），生成35258388字节stock-A-52332-no-cfe.w，SHA25640681a74f198a56058f20c8db65b38ba5bc62d30544b281428352c040b0e85de；系统逻辑载荷逐块与A原备份一致，无额外CFE前缀。官方保存升级包的嵌入UBI/WFI结构核对相符；版本相同的厂商包与运行A的差异仅卷1/2启动元数据及布局卷，系统卷0/10一致。上传RAM回读SHA256一致，原厂firmware_check=1，restore_defaults=0。

COM4被动捕获一次切A启动，当前boot_id4448a878-ee75-44cb-bf37-f887d805f99b，BootedFirst/RebootFirst、52332-gfbf3b9c、JFFS2自动挂载；尚未写B。准备门禁通过，下一步唯一动作：确认A健康/刷前A逻辑载荷后，原生hnd-write只写inactive B，写后不自动重启，先回读核验B内容及A/CFE保留。

写B检查点：A健康验证通过（原厂PID1 hash c958494dfca7126a3c92b3e8292687e9607aa07dcd72db28a7bd684efa49c606、JFFS2、19项配置、3把SSH key、两路无线isup/ap均1、Web认证页面、WAN及Wi-Fi绑定HTTPS200、TCP/UDP DNS）。从A执行一次原生hnd-write，退出99/HNDWR99且WRITE_FINISHED_REBOOT_PENDING，无自动重启；日志明确将B两份metadata sequence改17。写后完整回读B和A51MiB、ROM1MiB：267个系统/布局块与原A逐字节一致，卷1/2仅016→017及记录CRC变更（原/新记录及EC/VID CRC均验证），全部269逻辑块核验通过。CFE原始hash不变、A269个逻辑载荷不变，写后A raw也与刷前raw一致。证据native-write.log、after-preservation.json、restored-b-content-verification.json。原生升级已选RebootSecond且两份commit1；按当前恢复授权启动B验证，无业务集成，不额外试验已知未验证的ONCE机制。下一步唯一动作：已有COM4捕获中执行一次重启，核对实际B/原厂52332及完整健康。

恢复完成检查点2026-10-15 18:32 +08:00：B恢复后唯一一次重启成功，boot_id f9b282a3-b242-43d1-89dc-dac58ed3a30f，实际/下次均Second、原厂52332-gfbf3b9c，Aseq16commit1/Bseq17commit1。运行PID1完整SHA256与厂商原版52332相同；health-stock-second.json记录JFFS自动挂载、19项配置/3把原SSH密钥保持、两路无线isup/ap、SSH/后台认证页面、WAN、LAN TCP/UDP DNS和绑定Wi-Fi接口的HTTPS200均通过。启动后再次回读A和CFE：CFE原hash不变、A269个逻辑载荷和EC/VID CRC不变；A raw与本轮刷前一致，历史相对最初raw的UBI搬迁差异未被掩盖。恢复内容与启动验收通过，REQ-024/TASK-024/TEST-042完成；插件整体TASK-023待继续，未安装插件，未提交/发布。

串口证据边界纠正：切A有完整捕获，写B有串口记录；写B捕获设置300秒，在B重启前已到期，先前“已有捕获中”的状态判断不准确。补采仅覆盖B启动后的串口活动，未获得本次B早期CFE日志，不将其标为完整启动捕获，也不为补日志重复重启。实际B启动由新boot_id、原生BootedSecond/版本、运行PID1原厂hash及健康实测证明；dmesg/syslog已本地保存。COM4捕获结束后关闭。noconsole0继续仅开发期保留，TASK-022的最终恢复1尚待开发收尾。此恢复子任务完成门禁通过，整体插件完成门禁仍不通过。下一步唯一动作：继续已授权JFFS插件的原厂ABI/安装包门禁及实机验证，尚未执行。

插件实现检查点：新增独立LAN HTTPS8443（SO_BINDTODEVICE、256位管理口令、CSRF、Host/Origin校验）、自有静态页面、JFFS安装/更新/回滚/卸载、RAM菜单bind覆盖/撤销和当前启动周期cru守护；DNS切换改为原厂servers-file的RAM备份/原子替换/SIGHUP，不依赖固件补丁。原生核心、真实HTTPS认证边界、DNS文件幂等/厂商重建恢复、真实菜单bind、安装升级回滚卸载、隔离内核TCP/UDP NAT与LAN/WAN监听、主控internal/core全包、桌面/390px手机登录保存退出均通过。ARM91516字节，原厂52332从已保存官方镜像提取的库/QEMU核心通过；最新打包门禁工具及原厂BusyBox语法验证尚待运行，最终安装包未生成。原厂dnsmasq的QEMU网卡枚举失败（环境边界）；独立实机RAM回环15353诊断无法获得初始上游解析，已退出并清除自有进程/临时文件，未接管系统DNS/JFFS。不得将此项或开机自启标通过；原厂每次重启仍需SSH启动。页面截图在用户可视化目录router-jffs-desktop/mobile.png，不入Git。

DEC-035：用户“将系统刷回A区备份吧”授权恢复原厂运行环境。本机A原厂备份仍保留；先核验A原厂逻辑载荷与CFE，完整则通过原生bcm_bootstate5选择A并一次重启，不为“刷回”重复擦写A。保存串口启动与恢复前后状态，验证实际First/52332及原网络配置/JFFS/SSH/Web/DNS/WAN。不写启动ROM、不安装本轮插件；开发尚未结束，noconsole0保留，最终开发收尾恢复1仍有效。TASK-023唯一进行中，当前下一步唯一动作临时改为核对A/CFE保留后恢复原厂A启动并验证。

### 2026-10-15 原厂系统插件实施准备

DEC-034（有效，替代DEC-012的活动产品形态）：用户明确“按这个方向实现”“使用系统原版本的固件”。交付改为JFFS独立包，SDK仅交叉编译，原固件集成补丁留作历史参考。REQ-005改为原厂系统独立安装/停用/卸载/事件恢复；REQ-007为精确型号插件包/清单/原厂ABI门禁；REQ-010为运行时菜单挂接。UNIT-005负责脚本及运行态DNS，UNIT-006为独立LAN HTTPS/口令认证/CSRF和菜单覆盖，UNIT-007为模块构建/原厂依赖检查/插件打包；IF-004/005/006/007按此替代固件契约。TASK-023继续唯一进行中，整机编译/刷写不属于本轮实现。

准备门禁有条件通过：实现原厂servers-file=/tmp/resolv.dnsmasq的RAM备份、原子切换及SIGHUP恢复；限定LAN设备的HTTPS管理默认8443，独立256位随机管理口令/0600文件，不复用未经验证的原厂cookie；菜单只覆盖menuTree，缺失或版本结构不符拒绝挂接。测试覆盖未认证/CSRF/Host/Origin边界、配置脱敏、DNS恢复和原厂配置被重建、菜单重复操作与卸载。启动限制明确：原厂常见JFFS钩子禁用、cru写/var/spool/cron，均不能提供已验证跨重启启动；先提供显式SSH启动入口和可选主机启动工具，不伪造原厂自启能力。原厂52332的实际ABI尚需独立验证；不将当前53297自编译B称作原版固件。本轮不主动切分区或刷写。回滚为停止进程、恢复DNS、解除自有菜单挂载、删除自有管理防火墙；保留用户配置供重装，卸载不得清空JFFS。

### 2026-10-15 JFFS空间核算（用户要求统计）

仅统计与SSH只读查询，未安装、挂载、修改路由器或重编译系统。当前ARM程序87420字节（SHA256 83367bee3825f5393a2762ef21fd1e55f1e7b3992a78518590dd1811e1acf4c6）、页面8040字节、服务脚本1338字节，共96798字节；本地已集成菜单候选25474字节（实机原菜单20313字节），按候选计合计122272字节/119.41KiB。这不是已完成JFFS安装包：独立LAN管理/认证、DNS及启动适配仍未完成，新增代码与安装脚本体积未实测。

配置读取上限16KiB；防重放安全状态最多1024条，预留128KiB，原子替换时另需临时空间；业务缓存、连接状态与日志不计入JFFS。运行库及CA证书实机已存在，无需随包复制。将后续适配、配置/安全状态、元数据及临时写入纳入后，初步安装预算0.3~0.5MiB，建议为插件预留1MiB、保留旧版供升级回滚时预留2MiB；这是预算，不是文件系统实测占用或最终大小承诺。SSH df -k /jffs复核总15360KiB、已用2028KiB、可用13332KiB（13.02MiB），容量充足。TASK-023及唯一下一步保持：实现JFFS独立平台适配；最终包生成后再以文件清单和实机占用关闭TEST-010空间子项。

### 2026-10-15 用户改为JFFS独立部署（最新方向）

DEC-033 / CON-002：用户“这个大小是不是可以直接放在jtfs”“可以不编译系统”，按JFFS理解并停止整机重编译。2026-10-15实机只读确认RT-AX55/RT-AX56U_V2、53297-gee411da、原boot_id1afc7abc-8bd3-41f7-8ad5-c1d081e386d2；/jffs总15360KiB、已用1960KiB、可用13400KiB。四个运行库libcurl.so.4/libssl.so.1.1/libcrypto.so.1.1/libjson-c.so.2均存在。当前ARM程序87420字节，已通过QEMU与依赖门禁，体积适合JFFS。未上传程序、未改设备或分区；只停止本地构建容器cloudhelper-asuswrt-official-baseline，未把中断构建标为通过。

源码核查：官方rc/jffs2.c中jffs2_exec及/jffs/.asusrouter调用位于`#if 0 /* disable legacy & asus autoexec */`内。实机存在/jffs/scripts目录不证明有开机执行能力；.asusrouter不存在。/etc/dnsmasq.conf与/tmp/resolv.dnsmasq可写，/www仍来自只读SquashFS。当前C平台接入此前依赖自有固件的httpd桥、rc钩子和dnsmasq marker补丁；必须改为运行时适配后才可称独立插件，不能承诺简单复制二进制后全功能、自启和原菜单即用。

REQ-005/007/009/010、UNIT-005/006/007、IF-004~007按新部署方向重新设计；已有C业务协议/缓存限制/测试保持有效。TASK-023仍是唯一进行中步骤，固件重编译子步骤已取消，源码/SDK仅保留为交叉编译输入；后续不为每次业务更新编译整个系统。独立插件完成门禁仍不通过，尚未形成安装/卸载/重启验收。原固件候选2d44b40d9d7a含旧业务且有宿主core转储，明确不可部署；最终308bbdefed90构建中断，不能使用task/artifacts中的旧镜像代替。

本轮测试补齐：主控go test ./...及go vet ./...退出0；ASAN/UBSAN非PIE电脑测试核心/异步映射/DNS/控制流/双角色Carrier全部通过，旧PIE sanitizer曾在无工作线程的启动阶段挂起，已停止该测试进程并记录环境差异。桌面1440x1000模拟API页面保存、CSRF字段、密钥清空、测速结果通过，无页面脚本错误；原厂state/general等服务端ASP脚本在本地替身中替换，故不计原厂登录/菜单集成通过。390x844仍998px固定宽度，横向滚动；截图在用户可视化目录router-ui-desktop.png与router-ui-mobile.png，不入Git。

### 2026-10-15 C业务互通与内核验证（当前有效，覆盖旧未实现描述）

TASK-023继续进行中；整体完成门禁不通过，未刷写本轮业务镜像。TEST-038真实普通Probe双角色互通已通过：C主动连接、C接受连接均完成Ping和有界上传/下载；主控TLS/WebSocket/yamux上报与命令测试通过。TEST-037直接/拒绝/fake IP、AAAA/HTTPS防绕行、IP规则优先级及真实TCP/UDP DNS工作线程通过。TEST-039本地进程配置/CSRF/密钥脱敏/0600/单实例通过；隔离WSL Docker网络命名空间的真实Linux内核TUN+NAT验证TCP/UDP往返、LAN始终监听/可选WAN监听及停用仅删除自有规则通过。该证据不能替代路由器4.1内核、无线加速、真实主控配置实机验收。

DEF-007（已修复/复测通过）：现有Probe客户端URL携带route_id，C服务端原来只接受无查询参数路径；carrier.c兼容且核对URL与认证路由一致。Ping响应保留协议消息原始Path、仅反转帧路径，避免多跳语义错误。

DEF-008（已修复/复测通过）：测速tick早于send_start时间戳，无符号减法跨毫秒下溢会立即发送Finish，造成成功但0字节；measure.c在计算发送时限前重新取时，双角色上传/下载断言收到大于0且不超16MiB通过。

DEF-009（已修复/复测通过）：把节点虚拟IP赋给TUN会使WAN NAT回包被local表截获。TUN改为不分配本机地址，仅对从chvr0注入的包添加精确local策略路由；实测TCP/UDP自身出口往返通过。停用撤销自有iif规则/路由，不改系统local规则；测试保留无关防火墙规则。

DEF-010（已修复/镜像复测通过）：上游reinstall包展开顺序将独立模块/www页面移到根目录，已改为跟随www-install安装；构建校验核对最终/www/VirtualRouter.asp及/rom/etc清单。第二轮完整镜像已生成，但不作为当前最终候选：尚未包含后续内核回包和异步映射修复。

DEF-011（待最终镜像复测）：发现原厂宿主LnxHtmlEnumDict处理两个Captive_Portal模板时崩溃，core转储遗留在源码www并被随后打包。本轮未修改该上游功能，新增构建ulimit -c 0、仅识别ELF ET_CORE的清理脚本及最终rootfs拒绝core门禁；不把清理构建转储称作功能精简。用户要求不纳入中间文件，router_plugin测试core已加入忽略。

UNIT-003新增单个256KiB映射解析线程、16个固定包槽：主循环不再等待主控HTTP/DNS，缓存缺失交给有界队列，满队列丢包且不直连；回调仍由网络线程处理NAT/Carrier。模拟200ms主控延迟，连续64包立即返回且只保留16槽，映射成功后转发通过。自身出口缓存真实目的IPv4，不再逐包查DNS。TLS沿用固件既有/etc/ssl/certs/ca-certificates.crt；停止先恢复dnsmasq，再等待工作线程网络期限。

ASAN/UBSAN：核心报文/配置、NAT、DNS线程及主控互通已通过首轮；Carrier首轮CClient超时、单独复跑通过，未据此宣称稳定性完结，后续与最终变更一起复测。当前无新增固件运行库；测试容器的iproute2/iptables/开发包仅电脑端。

| 时间 | 已完成 | 变化/发现 | 影响 | 下一步唯一动作 |
|---|---|---|---|---|
| 2026-10-14 10:52 +08:00 | 用户强制启用跟踪；实机事实与源码入口补录；唯一插件账本建立 | 用户追问刷机；共享Carrier双角色已存在，ARMv7/产品形态尚待适配；并发备用出口工作存在 | 原厂固件优先，隔离新增插件工作 | 本地隔离ARMv7基线交叉构建并记录错误/体积 |
| 2026-10-14 10:57 +08:00 | 用户最新约束更新到需求/架构/单元/接口/测试；只读核实WAN地址 | 12040绑定WAN；独立发布产物；当前WAN为eth0/192.168.205.3 | REQ-003收紧，新增REQ-007/IF-005/TASK-008/TEST-011/012，DEC-008替代产品形态未定方案 | 本地隔离ARMv7共享代码基线构建，作为独立插件设计证据 |
| 2026-10-14 10:59 +08:00 | 最新要求更新到全部设计与追踪 | 专供路由器的新代码；旧共享产品形态被替代 | 新REQ-008/TASK-009/TEST-013、DEC-009；取消旧Probe基线构建为当前下一步 | 提取兼容契约，确定新模块最小接口/依赖/资源预算 |
| 2026-10-14 11:02 +08:00 | 小Web和菜单目标加入账本；只读确认原厂menuTree与只读页面 | 原厂无已验证菜单插件机制；研究运行时文件挂接，不改固件镜像 | REQ-009/010、UNIT-006、IF-006/007、TASK-010、TEST-014~016和DEC-010；原厂优先方向继续 | 提取兼容契约，确定新模块最小接口/依赖/资源预算 |
| 2026-10-14 11:12 +08:00 | 固件备选源码可得性调查完成；公开链接和具体RT-AX55构建目标入账 | 有Asuswrt源码/工具链；V2精确镜像兼容和原厂52332GPL仍未知 | TASK-011/TEST-017、FACT-011、DEC-011/RISK-010；插件范围保留；并发修改增至13个已修改文件，继续不触碰 | 提取兼容契约，确定新模块最小接口/依赖/资源预算 |

| 2026-10-14 11:32 +08:00 | 固件内置方向落实至需求/架构/任务/验证与追踪；候选target及WSL核查 | 原无需刷机/插件包/运行时菜单方案替代；工作区仅本账本修改 | DEC-012/CON-005；REQ-011、UNIT-007；TASK-012进行中/006取消/013待开始；TEST-018~020 | 锁定精确型号源码/工具链与隔离基础构建清单 |

| 2026-10-14 11:53 +08:00 | 用户阶段顺序入账；开始源码/工具链下载；官方Ubuntu18镜像校验和导入；隔离依赖安装启动 | GitHub/Registry在WSL访问超时，Windows下载可用；整仓下载因early EOF失败后转分批对象；重复archive下载已停止 | REQ-012/TASK-014/TEST-021、DEC-013；当前TASK-012；未精简/未加新功能/未刷机 | 完成源码/工具链下载及校验，执行原配置基础构建 |

| 2026-10-14 12:49 +08:00 | 固定工具链快照下载/提取完成；ARM静态程序编译和QEMU运行通过；环境与工具链版本记录 | 整仓Git早期EOF失败；分批下载可恢复；大批次受Windows命令长度/连接限制，四组独立缓存下载剩余源码 | firmware/asuswrt新增构建/下载工具，TEST-022部分通过；固件未构建，无精简/新功能/刷机；备用出口账本有并发修改保留 | 完成四组源码下载/合并校验，生成Linux工作树并运行原配置基础构建 |

| 2026-10-14 13:09 +08:00 | 所需源码135305/177014对象约76%已缓存；核实发布者V2支持列表；启动本地串行后续流程 | downloads/continue-baseline.py等待四组完成标记，之后合并完成pack、导出/校验归档、准备Linux工作树，再调用基础构建；任一步失败即停止后续 | TASK-012继续；TEST-022源码仍未通过完整校验；支持列表有硬件版本限制；没有功能精简或新功能 | 等待四组完成并检查串行流程的源码校验/基础构建结果 |

| 2026-10-14 13:40 +08:00 | 四组所需177014 unique blobs下载齐全；根据用户指令将普通源码快照落入本仓库，移出嵌套Git；构建依赖与ARM验证继续有效 | 源码含78组大小写同名路径、2431符号链接及Windows保留名；源码全部内容与原始Linux布局由清单保留，不属于功能精简 | DEC-015覆盖存储方案，baseline.lock.json下载状态更新；仓库快照逐文件校验/生成中；没有基础固件编译/刷写证据 | 完成仓库基线校验并生成Linux构建输入，执行原配置rt-ax55构建 |

| 2026-10-14 13:52 +08:00 | 本仓库普通源码基线212350文件生成完成；SHA1逐blob检查，原始Linux权限/链接/路径清单保存；仓库内无Git/子模块 | 78组大小写冲突及其他Windows保留名/非法字符路径采用259条重定位映射，全部内容保留；此前Windowscolon文件错误已通过布局恢复解决 | DEC-015落实；TASK-012继续；没有功能精简/新功能；从仓库源文件打包Linux输入校验进行中 | 完成Linux输入归档校验和工作树准备，执行原配置rt-ax55基础构建 |

| 2026-10-14 14:07 +08:00 | 仓库源码快照校验/打包完成，tar3782348800字节；Linux源码/工具链工作树校验输出固定HEAD；Git忽略与过程文档本地保留已落实 | 26个上游.gitignore以SHA1校验后可还原收纳，避免漏掉原本tracked的预编译依赖；此前误解导致本账本git rm --cached，已按用户澄清恢复跟踪；原始源码内容/模块配置不变 | REQ-013/TEST-023；外部Git缓存仅临时，仓库普通基线；备用出口账本继续不触碰 | 检查原配置rt-ax55基础构建日志并处理实际构建问题 |

| 2026-10-14 14:14 +08:00 | 完整仓库源树打包hash ec7fc98e1f6aea1233a7d9dc44eca8cb15356f2982f63bf7d95ec447e54f134f；Linux源码和SDK固定HEAD通过；首轮原配置编译执行 | 环境检查缺xxd，make退出2；上游自动Git标签需要容器内身份；缺busybox autoconf发生初始clean依赖探测，不据此删除功能 | TASK-012继续；Dockerfile补xxd，build脚本仅容器Git身份；过程文档/账本本地忽略规则已通过 | 补齐容器依赖并重试原配置基础编译 |

| 2026-10-14 14:16 +08:00 | 容器补xxd重新构建成功，镜像ID8ce9bb0a5fd6ba134fb810f3f6aa7bc0d9921349449fabcd8d5a176af2bd3db0；Python构建工具AST与build脚本bash -n通过 | 第二轮原配置rt-ax55编译已运行，越过xxd检查进入宿主工具；Git身份仅容器环境变量，不改宿主全局配置；逐轮日志保留 | TASK-012继续，ENV-001原检查已通过；没有修改上游模块配置 | 检查重试编译日志与产物，处理实际失败点 |

| 2026-10-14 14:27 +08:00 | 原配置编译已进入Broadcom用户态组件；Git规则正在按用户澄清修正 | 用户所指文档为ASUS/上游参考文档，不是本任务账本；用户要求git commit；恢复账本和自有构建说明跟踪 | REQ-013/TEST-023重新复测；仅提交本任务源码/构建/账本；并发备用出口账本保留；无固件发布 | 完成上游文档忽略验证并提交本任务基线，继续基础编译 |

| 2026-10-14 14:33 +08:00 | 按最新规则逐项检查212350源码路径：18087参考文档忽略，194263源码基线路径可追踪；源码/预编译输入、许可、自有README/账本/配置正常保留 | 修复宽泛readme规则误匹配readmem.c；官方V2支持页API明确返回RT-AX55，新版原厂恢复归档hash与官网一致，但其镜像74659500字节超过当前51MiB逻辑分区，尚不作为可刷证明；基础编译正在编译libcurl | TEST-023最新策略通过；本次本地提交基线/构建/账本，基础固件仍在编译，未刷写/未发布 | 已完成轻量提交；继续基础编译与镜像验证 |

| 2026-10-14 14:44 +08:00 | 按用户最新范围停止完整源树Git add，已有源码/SDK/编译仍保留本地 | Git仅保留我们修改的部分和构建/版本/账本；完整上游和布局清单忽略；通过固定上游+覆盖复现 | DEC-017覆盖旧Git方案，TASK-012继续；当前没有功能精简/业务修改 | 完成覆盖构建脚本与Git范围验证，然后提交本任务 |

| 2026-10-14 14:49 +08:00 | 固定上游+定制覆盖方案落地；新下载脚本PowerShell解析通过，3个构建脚本bash -n通过；apply-overlays在实际固定HEAD上通过；Git范围验证通过 | upstream及完整manifest忽略，迁移时期的一次性快照工具也本地忽略；本账本/README/版本/构建/未来patches可追踪；原配置编译持续运行 | REQ-013/TEST-023按DEC-017更新；准备仅本任务轻量提交，不推送或发布未完成固件 | 继续基础编译与镜像验证 |

| 2026-10-14 14:55 +08:00 | 新download-baseline.ps1实际执行通过，复用source-baseline.tar与toolchains-baseline.tar且SHA256匹配固定值 | Git所需输入完整：仅脚本/配置/版本/本账本，不纳入上游完整源树；基础编译仍在进行 | TEST-023通过；本轮用户要求的Git提交可执行，固件完成门禁仍不通过 | 已完成轻量提交；继续基础编译与镜像验证 |

| 2026-10-14 15:44 +08:00 | 上游make完成，两个puresqubi镜像生成；独立collect-baseline退出0；verify-baseline检查CRC/UBI头/容量/SquashFS/ARM ELF/tun.ko通过；生成BusyBox echo与dnsmasq版本命令QEMU退出0；再次SSH只读核实RT-AX55/RT-AX56U_V2及51MiB分区 | 编译中原位修改启动脚本导致后续读取偏移错误，未影响已完成make；独立收集已修正。镜像header含47622，无实机刷写/整机启动证据 | TEST-018离线/用户态通过，完整启动未通过；ENV-002记录；阶段仍为基础运行验证，不精简/加功能 | 核对V2恢复手册与当前原厂镜像，完成基线结果提交 |

| 2026-10-14 15:47 +08:00 | V2官方手册73~74页读取确认救援步骤，原厂当前386_52332镜像下载成功响应但首次90s超时，已断点续传；全局配置未改变 | 恢复路线有精确V2手册依据；未把新版74MB原厂镜像简单判定为可写入51MiB，其可能为多平台容器须单独解析 | TASK-012仅剩实机启动/恢复动作准备；具体刷写会中断当前路由，不能以只读SSH授权代替 | 校验当前原厂恢复归档，提交基础镜像验证结果 |

| 2026-10-14 16:20 +08:00 | 回答两个镜像区别；实际确认系统载荷一致，大镜像额外1 MiB CFE ROM前缀；WFI尾字段修正后重跑验证通过 | DEF-001为校验报告字段解释错误，镜像内容/校验值不变；README与验证工具同步；其他任务的并发改动保留 | TEST-018补充两封装载荷比较；TASK-012仍进行中，未刷写/精简/加功能 | 校验当前原厂恢复归档，核对具体刷写入口与镜像封装 |

| 2026-10-14 16:46 +08:00 | 用户已授权升级；dmesg License chip name为6755；原厂ZIP完整CRC通过；原厂配置/JFFS/启动区/当前镜像已本机备份；无CFE ROM镜像上传回读SHA256一致，设备firmware_check=1 | stock启用pipefw，真实写入由GUI同用的hnd-write处理；受控脚本语法通过，不清空配置、不绕过校验 | REQ-014、DEC-018、TEST-024；TASK-012持续 | 原厂hnd-write写入，核对分区和启动区，随后重启验证 |

检查点2026-10-14 18:48 +08:00：原厂First回退实际启动已通过；修复版35258388字节、SHA256 0f9a2d14b994119d5523e8edad78e563283b80f1d5b7e783aa439aa1b122ea8a，经原生hnd-write退出99成功替换Second，SSH自动恢复且没有重复刷写。写入后ROM/mtd3/完整First回读SHA256不变。修复版首次启动自动挂载原JFFS2可写，三把host_key一致，15项配置及DNS/WAN/Web通过，第二次重启验证正在执行。TEST-025原断言启动标记必须进syslog失败；源码init.c挂载先于start_logger，已改为运行rc标记+自动挂载+原文件hash，保留初次失败记录，不能将二进制字符串当运行日志。

历史检查点（现状态已由后续检查点覆盖）：

检查点2026-10-14 17:55 +08:00：原配置兼容镜像重建/收集及验证均退出0；补丁可从本Git覆盖固定上游，产物/源码文档仍忽略。首次Second新固件启动、15项配置保留、DNS/WAN/两路无线驱动、LAN客户端HTTPS和原启动ROM校验已通过。请求从First启动后仍Wi-Fi Disconnected，用户重连请求待答；未核实First运行、未写兼容镜像、未验证自动挂载。TASK-012已阻塞于实际网络连接，下一步先核对First及原始分区，再更新Second；无功能精简或新业务实现。

检查点2026-10-14 16:58 +08:00：hnd-write退出99/hndwr99，Second序列17/下次从Second启动；First序列16与完整51MiB原厂镜像SHA256未变，启动区mtd3及整Flash前1MiB SHA256未变。管理SSH恢复，原始写入日志及核验报告保存在本机备份目录。下一步唯一动作：重启验证新固件。

检查点2026-10-14 18:54 +08:00：基础升级子阶段完成，修复版Second两次启动通过；15项配置与三把SSH密钥保留，自动JFFS2/Web/无线/WAN正常，LAN TCP/UDP DNS及Wi-Fi绑定HTTPS 200通过。最终回读ROM和完整First与备份一致。版本锁on_device_boot_verified更新true；TASK-012、REQ-011/013/014完成，DEF-003关闭。完整产品未完成，TASK-014等待用户名单，不启动精简/业务。

检查点2026-10-14 21:00 +08:00：用户级asuswrt-router-upgrade-debug技能创建，3个文件，格式/UI/引用验证通过。REQ-015/TASK-015完成，DEC-020为后续升级策略，TEST-026仅静态验证。当前路由器仍长期B，未执行ONCE或故障模拟。当前JFFS总15MiB、可用13808KiB≈13.48MiB；专用插件体积未构建测量，不能断言一定装得下或装不下。固件内置程序/Web、JFFS只存配置仍为主线，精简待用户名单。技能补充默认保持分区布局，重划分区涉及板级NVRAM/启动器和数据迁移，另行验证；本轮不改变分区。

检查点2026-10-14 21:19 +08:00：用户接管官方GPL源码下载，Codex没有开始下载。只读SSH确认本机型号、ARMv7、BCM947622和51MiB两固件区；建议源码包路径被Git忽略且尚不存在。REQ-016/TASK-016/TEST-027/DEC-021记录支持核查，待用户包就位后继续；本轮仅修改账本，未改变设备。

检查点2026-10-14 22:40 +08:00：用户提供D:/迅雷下载/GPL_RT_AX55_300438653297.zip，SHA256 54769b983b0d33f631408cfdae6ecda6441221bb9d580b5252266342bff03e98与官网匹配；ZIP内单TGZ CRC及gzip EOF/CRC通过，共263406条归档项、内容3827216254字节。源码输入保存在firmware/asuswrt/upstream/official/GPL_RT-AX55_3.0.0.4.386.53297-gee411da.tgz，并在本地WSL BuildServer独立目录/home/maple/cloudhelper-firmware-official-53297/source/asuswrt解包成功；52294/49559仍是迅雷xltd，未解析或改动。实际SSH只读再次确认productid RT-AX55、odmpid RT-AX56U_V2、armv7l、Machine BCM947622、License chip6755、boardrev0x1134，与target.mak RT-AX55/PROFILE947622GW/ARM/ODMPID及targets/947622GW/947622GW.RT-AX55中的chip47622/boardRT-AX55/ARM/NAND匹配。包有133个prebuild输入、52个47622-arm saved输入和五套Broadcom工具链。随包GCC5.5.0首次编译因容器libmpc.so.3搜索路径失败，发现SDK自带库后设置LD_LIBRARY_PATH解决；最终脚本退出0，ARM EABI5静态程序编译成功、qemu-arm输出official-53297-arm-toolchain-ok。过程脚本首次CRLF尾行导致退出1但编译/运行通过，转LF复跑得到最终退出0；保留初次失败边界。TEST-027源码检查/工具链验证通过，完整依赖图、固件链接/镜像、容量和整机启动均未验证；TASK-016源码核查子阶段完成，REQ-016整体仍进行中。Git check-ignore确认源码及artifacts报告被忽略，未出现新增完整源树；当前SWRT版本锁、程序/分区/启动状态均未改变。下一步为独立官方原配置构建，不启动精简或新增业务。

## 十四、完成摘要

- 当前官方基线：53297-gee411da原配置完整编译、离线镜像/ARM/CRC/容量/JFFS2和生成BusyBox/DNS程序QEMU验证通过；构建工具/版本锁已替换，完整上游及产物仍忽略。
- 官方无CFE镜像36,044,820字节已写物理B且系统载荷完整回读一致；标准reboot和原生mailbox/board ioctl重启两次ONCE均实际进入A，当前不能声明官方B运行或ONCE/自动失败回退成功。
- 当前实际运行原厂A 52332，CFE ROM原hash保持；A raw hash因UBI元数据搬迁变化，全部269个逻辑载荷包括原系统/启动元数据均与原备份一致。保存差异报告，原参照hash不修改。
- 下一步唯一动作：等待用户批准临时普通B启动例外；批准后SSH恢复即选择下次A，健康通过才长期B再重启；没有答复前不执行7。
- 备份包括原A、旧SWRT B、CFE、配置/JFFS，目录D:/Code/RouterFirmware/router-backups/20261014-official53297；官方镜像在firmware/asuswrt/artifacts/official-53297，均本地不入Git。
- 未精简、未加入DNS fake IP/代理/12040/新Web；AiMesh与吞吐未测，整体产品门禁仍不通过。旧SWRT两次启动结果保留为历史，不作为本轮官方验证。

DEC-022（替代DEC-021候选不切换限制）：用户明确授权替换并编译升级验证；以已校验官方53297 TGZ为活动基线及随包SDK优先，保留旧SWRT锁和产物作本地历史。需要的构建修复保存为Git内补丁/工具，不关闭模块。保留A/CFE策略及恢复授权持续，B ONCE后再主动长期启动。

检查点2026-10-14 22:43 +08:00：接管用户官方基线替换/构建/升级授权，读取升级技能和现有账本；SSH仍原SWRT Second seq17/commit1、First seq16/commit1，原JFFS2已挂载；设备尚未更改。本任务只改firmware/asuswrt和原账本，其余五个并发文件保留。TASK-017唯一进行中。

检查点2026-10-14 23:05 +08:00：官方来源锁已切换为53297-gee411da TGZ SHA25665aa57c4e40c7fc3a56953e9e68146005077980fdd91a3da6df910d015358110；旧SWRT来源锁/README/脚本及JFFS兼容补丁移到忽略的artifacts/history/swrt-bd30beb，官方目标BRCM_NAND_JFFS2原生misc2路径，旧补丁不用于官方构建。prepare-baseline退出0，随包SDK usr/bin兼容映射/LD_LIBRARY_PATH在源码之外，旧HND2013辅助SDK commit7710a1e已核对，仅作为构建工具，不取SWRT源码或router闭源对象。第一次过早调用build因准备尚未完成缺归档标记而退出1；准备完成后第二次真实make正在构建ARM Linux4.1内核（文件系统阶段），未修改正在运行的构建脚本。当前B完整51MiB、A完整51MiB、CFE1MiB、JFFS及配置已另存D:/Code/RouterFirmware/router-backups/20261014-official53297，A/CFE与原记录一致。尚未切换A/刷写。Python AST、下载PS解析及git diff --check通过。TASK-017继续，下一步查看完整编译结果并处理实际失败点。

检查点2026-10-14 23:19 +08:00：download-baseline.ps1实际退出0，复用官方TGZ及锁定辅助SDK归档哈希通过；SDK仅工具不混用旧SWRT固件输入。旧Windows完整源树已安全重命名到忽略的upstream/history/swrt-bd30beb，活动输入只有upstream/official。所有固件Shell bash -n、Python AST及PS解析通过。源commit字段标为厂商短revision ee411da，不虚构完整Git SHA。首轮完整构建仍正在ARM Linux4.1网络协议/过滤模块，容器约115%CPU、324MiB内存，内核主要串行；不打断正常编译或改正在读取的脚本。源模块配置未精简；设备仍SWRT B不变。下一步唯一动作：检查首轮完整make结果并处理实际失败。

检查点2026-10-14 23:36 +08:00：本轮真实make已通过ARM内核/无线wl.ko及其他模块、BusyBox和OpenSSL阶段，正在shared用户态公共库；执行会话97188/容器cloudhelper-asuswrt-official-baseline仍运行，日志/home/maple/cloudhelper-firmware-official-53297/logs/baseline-build.log。没有完整镜像或退出0证据，TASK-017继续。新增prepare-auxiliary-sdk.py已实际校验锁定SDK tar并仅解包HND2013/fwtag.ini到当前官方task_root/auxiliary-toolchains，无Git元数据；本轮容器已在启动时挂载旧SDK目录的相同锁定内容，未来构建从新的独立辅助目录映射，未更换运行中容器的工具文件。尚未重启/刷写，设备仍旧SWRT B。升级阶段分离助手在忽略的artifacts/official-53297-inspection/official-upgrade.py，snapshot已退出0，select-a/upload/flash/pretrial/trial/health-trial/promote/health-final均未执行；先等完整编译/离线门禁。下一步唯一动作：完成当前构建并处理真实失败点。

检查点2026-10-15 00:00 +08:00：跨午夜继续同一任务；用户当前日期更新，不取消升级授权。官方构建已通过kernel/无线模块、BusyBox、OpenSSL、shared、SQLite、conntrack、lldpd/libnl及httpd阶段，仍在其他系统服务配置/编译；无完整make退出0/镜像校验证据。状态仍TASK-017进行中，TASK-018/019待开始；设备未重启/刷写。升级后健康检查增加真实Web认证和appGet版本验证，凭据仅由原本地settings.cfg在内存解码，失败可用已授权Web重新启动SSH，不回显或写入源码。下一步唯一动作：完成本轮完整make并处理真实失败。

检查点2026-10-15 00:35 +08:00：官方make/collect退出0，verify-baseline退出0；无CFE镜像36044820字节、SHA256 b7ec46c022c8981e9c79820a609c04d2f9b0089f2642bab2257e4f82d028ab12，含CFE镜像37093396字节；两者系统载荷一致、均适配51MiB，WFI CRC/UBI/SquashFS/ARM/TUN及原生JFFS2配置通过。源码模块未改，活动补丁为空。产物复制到忽略的artifacts/official-53297且hash一致，当前设备尚未改动；TASK-017完成，TASK-018离线用户态验证继续。

TEST-028通过：完整官方rt-ax55原配置make/collect退出0。TEST-029通过：离线CRC/UBI/容量/ARM/SquashFS/TUN/原生JFFS2及生成BusyBox echo、dnsmasq 2.90 QEMU执行退出0；rstats位于bin/rstats且为ELF，未写权限导致strip警告，未丢文件；ceventd实际依赖不含可选缺失libceshared。前置临时检查因宿主没有file、CRLF以及容器默认旧工作目录只读而停止，修正检查脚本后QEMU通过，未修改源模块配置。原A、旧B、CFE/JFFS/配置完整备份完成，TASK-018完成，TASK-019当前唯一进行中，开始从B回原厂A。

检查点2026-10-15 00:45 +08:00：原厂A实机启动boot_id c5d074c9-9b75-4d54-97a6-bf9196fe69ce，版本52332-gfbf3b9c，A/CFE完整hash保持。官方36044820字节系统镜像上传回读hash及原生firmware_check=1通过；原生hnd-write退出99/hndwr99并明确写inactive B sequence17，SSH自动恢复，无重复写入。写后A/CFE回读一致，bcm_bootstate8回读BOOT_SET_PART2_IMAGE_ONCE、RebootSecond、Bcommit0。开始首次官方B ONCE，尚未确认消费/健康/长期启动。TASK-019继续。

检查点2026-10-15 01:04 +08:00：B ONCE首次标准reboot实际仍A，boot_id ae5d1972-4925-4f67-8ad6-ebb2aefb568c，52332版本，ONCE未消费/Bcommit0，不能作为B启动成功。NEW_IMAGE_ONCE3与8映射相同，未重复刷写。原A raw hash变为cbb74a77af3840cf2e4d2eefa032af80cb7706ac8ae37103e75f4e478269f73e，8590字节仅物理块0/1/300/301：UBI将METADATA/METADATACOPY从0/1迁移到300/301、更新EC/VID序号，全部269个有效逻辑LEB载荷（含根fs、cferam/内核、两份启动元数据和UBI卷表）SHA256完全一致，EC/VID CRC验证。没有改写原厂程序/版本；保留原raw备份和差异报告，不改原参照hash、不以raw全分区不变宣称后续成功。后续A保留门禁为原raw校验记录+全部逻辑载荷相同，CFE1MiB仍要求raw严格一致。平台原生mailbox flag1已核对，原生软重启助手仅在RAM、ARM ABI24byte、调用board_ioctl现有62/4接口，不提交B；正在尝试原生ONCE重启。TASK-019仍进行中。

检查点2026-10-15 01:06 +08:00：board_getset_boot_inactive ioctl62读取为1；原生board restart ioctl4真实重启仍A，boot_id20d37e88-28a6-4c5e-b138-ab720c0ceaf6、52332，未提交B/未再次刷写，不能宣称ONCE成功或失败回退机制工作。对应TEST-030当前失败在B试启动，DEF-004新增：当前CFE与启动接口组合ONCE不生效，原因尚未确认；不依据mailbox标记承诺启动结果。因技能要求健康前不得设置7及用户先试启动策略，已async申请临时普通B启动例外：SSH恢复后先选下次A、健康通过后再长期B。尚无用户回答，不能执行依赖的7/重启；TASK-019等待该例外，独立继续B写入内容读回验证。当前路由器原厂A管理正常。

DEC-023：原生B ONCE实机未生效，不得承诺自动回退；普通B试启动为需要用户批准的策略例外，现待回答。DEC-024：A保留校验保留原raw参照并报告搬迁后的raw hash，同时以EC/VID CRC及全部269个UBI逻辑载荷完全一致核对原系统，CFE仍raw严格一致。

检查点2026-10-15 07:33 +08:00：用户追问是否B曾启动但未被SSH探测；本轮只读诊断并恢复电脑已有Wi-Fi连接，没有重启/刷写/设置bootstate。当前boot_id仍20d37e88-28a6-4c5e-b138-ab720c0ceaf6，uptime6小时26分，BootedFirst/extendno52332，dmesg为gitserv_asus@tpbuildsvrvu01原厂2024-11-06内核；新官方构建内核为2026-10-14 16:24:22UTC。/tmp/syslog.log和/jffs/syslog.log保留历史（有重复）：两次旧SWRT启动及本轮三次原厂52332启动，完全没有53297记录；/jffs/asd.log和webs_upgrade.log无相关启动失败证据。CONFIG_PSTORE未设置，设备没有/sys/fs/pstore或/proc/last_kmsg可读崩溃日志，未抓取ttyAMA0/CFE串口。纠正DEF-004与DEC-023的因果表述：只能确认探测恢复时最终运行A、官方B没有持久用户态启动记录；不能由此断言ONCE没有尝试B，也不能排除B在CFE/解压/内核早期失败后回A。无失败串口证据，不能宣称失败自动回退已证实。完整审计日志在外部backup/boot-log-audit-20261015，公开摘要boot-markers-sanitized.txt、conclusion.json；原始日志仅本地。TASK-019保持进行中，普通B例外尚未获批，下一步在用户指定策略下继续，串口完整开机记录可消除早期启动歧义。

检查点2026-10-15 10:08 +08:00：用户指定COM4并明确允许串口发送，仅要求查询命令有往返即可。COM4 115200/8N1/无硬件流控实际验证双向通过：先由SSH向ttyAMA0输出标记，PC收到39字节；随后通过SSH在RAM临时接入串口shell，PC从COM4发送printf唯一标记、uname -a及exit，串口收到独立执行结果UART_QUERY_T100753_OK及Linux RT-AX56U_V2-5420 4.1.52、2024-11-06、armv7l，区别于命令回显；临时shell退出，COM4已关闭。外层SSH包装脚本出现BOM/CRLF解析警告，保留原始错误，不把包装退出码作为成功依据；成功依据为串口独立标记和真实uname输出。临时脚本已清理，结果在外部备份serial-query-COM4-20261015-100752/capture.txt、result.json，本轮无重启/刷写/启动选择改动。09:48只读实际boot_id已为1e24c324-caa5-4fef-a401-1085de7144ef、原厂52332/BootedFirst/BOOT_SET_PART1_IMAGE，与07:33不同，该变化不是本轮测试引发；本轮仅证明UART收发，不证明官方B启动。


检查点2026-10-15 10:44 +08:00（REQ-018完成）：Python脚本提供ports/capture/exec/terminal、COM4默认115200/8N1、无流控/DTR/RTS、随机shell握手/独立BEGIN-END/远端退出码、原始RX及有界日志、UTF-8文本命令文件；仅显式参数启用SSH临时RAM shell。脚本在firmware/asuswrt/tools/router_serial.py，同步技能scripts同名文件，SHA256 2d575fc8c005230cef5da4a54aebb05848e2e9fe5100314adffb25b13f3288bb。技能新增serial-debug.md并修正旧SWRT与官方阶段/ONCE因果边界。完整上游、venv、过程报告均忽略，未提交秘密。

TEST-031通过：Windows unittest 7通过/1真实POSIX平台跳过；本地WSL python3 unittest全部8通过，实际/bin/sh验证引号、中文、多行、退出9及下次命令可继续；其余覆盖分片、回显假阳性、超时不重试、缺开始标记、超长/控制字符零发送、被动捕获零发送、原始字节与磁盘限额。

TEST-032通过：COM4实际exec返回原厂内核/boot_id；Python subprocess核对远端exit7返回7；超时返回124且临时shell/锁清理；capture收到SSH注入标记且events无tx；Windows真实PTY terminal发送uname -a收到结果、Ctrl+]退出0。最新证据在忽略的artifacts/serial-validation/20261015-103317-c3ee7508、103319-a2158382、103325-170fcbfb、103924-bc6baf84；技能quick_validate通过，主/副脚本hash相同。所有COM4会话结束，实机noconsole仍1，boot_id不变。固定启用与真实重启日志未测，未将其作为本次已完成项。

DEF-005（串口工具清理，已修复）：初轮超时测试发现BusyBox交互sh忽略TERM，导致临时shell及锁残留，保留失败报告102739-4ab1ea7d。精确HUP清理本次进程后，改为HUP及SSH stdin生命周期通道，STOP/EOF触发自有资源清理，远端watchdog负责期限兜底；复测超时/正常/交互退出均完成清理。未killall、未改固件。命令子进程是否结束仍按实际状态判断，不保证关闭串口等于取消远端操作。

DEC-025：串口工具作为独立诊断入口，不包含自动刷写/重启/永久控制台开关。已有认证shell可脱离SSH使用；无shell时明确临时桥接依赖。noconsole持久路径优先复用原生login，后续按实际配置/重启授权验证；用户咨询“能否固定启用”未被解释为立即改NVRAM或重启。REQ-018→UNIT-008/IF-008→TASK-020→工具/技能→TEST-031/032闭合；工具子阶段门禁通过，整体任务保持未完成。


检查点2026-10-15 10:49 +08:00：用户明确授权开发期间固定noconsole=0，开发结束后恢复。前置实机核对RT-AX55/RT-AX56U_V2、原值1、当前/下次均First；nvram set noconsole=0及nvram commit退出0，新的SSH连接回读0，boot_id保持1e24c324-caa5-4fef-a401-1085de7144ef。证据为外部备份console-enable-20261015-104901.json；本轮未重启、未改变启动选择，跨重启原生登录未实测。

REQ-019：开发期间持久启用原生串口控制台，开发完成后恢复原值1。关联UNIT-008/IF-008、TASK-021（已完成：设置/commit/独立回读）、TASK-022（待开始：开发收尾恢复1/commit及运行态与启动验证）、TEST-033（设置退出0和新会话回读0通过，重启行为未测）。本次设置子步骤准备/完成门禁通过；恢复作为开发交付必要收尾，不在当前立即撤销。DEC-026覆盖DEC-025的未授权持久设置状态，既有不改启动ROM/分区策略保持。恢复授权已明确，无需届时再次询问同一恢复动作。


检查点2026-10-15 11:23 +08:00（本轮验证及资料核对完成）：COM4被动捕获覆盖B ONCE设置前到唯一一次标准重启后，72215字节完整RX。早期日志PAR1/U016/COM1、PAR2/U017/COM0，只有一次TRY1、一次CFE及一次Linux启动；明确Booting from previous image，address/offset 0x00100000，随后原厂2024-11-06内核和52332版本。当前boot_id730a47d6-ae22-452f-87a1-50640a87922f。本次直接选择A，未观察到B内核启动再失败回A；此前两次无串口记录的试验仍不能追溯定因。CFE为1.0.38-164.255/BCM947622、Base5.2_07p2；ONCE未消费的具体原因未确认，不能宣布B镜像损坏或自动故障回退有效。

TEST-034：本次完整串口启动取证通过，B试启动目标未通过；重启前后CFE ROM严格hash一致，A全部269个UBI逻辑载荷及EC/VID CRC一致，raw搬迁差异保留原参照。noconsole=0跨重启保持，原生console出现login，经已授权账户认证后直接UART运行uname/nvram/boot_id/bcm_bootstate，独立随机标记与退出0通过，未使用SSH bootstrap，认证过程不落盘。TEST-033跨重启原生控制台验证补齐；开发完成恢复1的TASK-022仍待执行。

DEF-006（试验标记清理已完成）：在本次状态执行bcm_bootstate5仍回读B ONCE。官方GPL board_ioctl.c的BOARD_IOCTL_GETSET_BOOT_INACTIVE_IMAGE明确offset=-1读取、1设置、0清除硬件mailbox；公开SWRT同路径源码相符。采用独立ARM32静态RAM助手，只含该ioctl的读取/清除，无重启或Flash写入接口；上传回读SHA256一致，清除前1、清除后0。再次bcm_bootstate确认BOOT_SET_PART1_IMAGE、BootedFirst、RebootFirst，Acommit1/Bcommit0不变，boot_id不变；助手已移除。此实测说明5不足以撤销当前ONCE标记，不能只凭退出0宣布已恢复A。

资料核对：https://raw.githubusercontent.com/SWRT-dev/asuswrt-bcm/386/release/src-rt-5.02axhnd.675x/bcmdrivers/opensource/char/board/bcm963xx/impl1/board_ioctl.c 。本地官方GPL同接口855-879行一致。未找到同型号同CFE版本可验证的公开ONCE案例，不能将其他Broadcom机型的启动日志当兼容性证明。原厂A能正常启动与本轮B ONCE未达预期分别报告；不设置7/+2来绕过待验证的试启动策略。

证据目录：D:/Code/RouterFirmware/router-backups/20261014-official53297/b-once-uart-20261015-105733，含boot-selection-markers.txt、conclusion.json、native-console-verification.json、trial-marker-cleanup.json及原始RX。保留报告在其父目录uart-pretrial-20261015-105733-preservation.json、uart-posttrial-20261015-105733-preservation.json。未再刷写、未增加第二次重启；当前A稳定、原生控制台开发期间保持0，COM4已关闭。TASK-019官方B运行仍未完成，下一步仅继续分析CFE选择与ONCE机制，不自动普通B启动/提交B；产品精简和业务集成未开始。


DEC-027（用户最新授权）：用户“正式启动B试试”明确批准B ONCE未生效后的普通B启动例外，替代DEC-023的待授权状态。执行本机7、回读、一次重启及完整串口捕获；保留A/CFE，不重复刷写，不以正式选择成功代替B运行/健康通过。失败按既有恢复授权处理。准备门禁通过：当前A52332、boot_id730a47d6-ae22-452f-87a1-50640a87922f、Acommit1/Bcommit0、当前/下次A、noconsole0，已有B镜像写入回读和原A/CFE备份，串口已实测。TASK-019继续进行中。


检查点2026-10-15 11:36 +08:00：按用户“正式启动B试试”执行DEC-027。重启前A/CFE保留检查通过，COM4捕获就绪且真实RX标记通过后，bcm_bootstate7回读BOOT_SET_PART2_IMAGE/RebootSecond，再执行唯一一次reboot。UART PAR1/COM1、PAR2/COM1、TRY2，CFE从latest image地址/偏移0x03400000加载B，内核为builder@a51fbda31b90、2026-10-14 16:24:22 UTC，固件53297-gee411da；SSH自动恢复，boot_id1afc7abc-8bd3-41f7-8ad5-c1d081e386d2、BootedSecond/RebootSecond、Aseq16commit1/Bseq17commit1。本轮不再刷写，未增加第二次重启。

TEST-035通过（普通B启动与基础健康，明确非ONCE）：运行/proc/1/exe长1755988字节、SHA2563376e6960a97a8d30bfe88c463b8ce1888bee5597f92b3c8842240b914930fd4，与构建sbin/rc一致；/dev/mtdblock9自动挂载/jffs为jffs2；19项网络/Wi-Fi/SSH相关配置与备份一致，三把SSH host_key一致；SSH/Web登录及管理API版本53297通过，两路无线驱动、WAN网关/外网连通通过，LAN TCP/UDP DNS及显式绑定Wi-Fi192.168.52.170/ifindex10的HTTPS返回200。重启后CFE1MiB原始hash不变、A全部269个UBI逻辑载荷和CRC核对通过，原物理搬迁差异仍保留原始参照。B原生串口登录认证和查询独立通过，无SSH bootstrap，noconsole保持0；COM4已关闭。

证据：D:/Code/RouterFirmware/router-backups/20261014-official53297/b-normal-uart-20261015-113047，含完整71880字节RX、boot-selection-markers.txt、conclusion.json、native-console-verification.json；父目录normal-b-20261015-113047-health.json及-preservation.json。官方基线普通启动阶段完成门禁通过，REQ-017/TASK-019完成；ONCE/故障自动回退依然未通过，AiMesh/吞吐未测，不宣称整套业务产品已完成。下阶段等待用户精简名单，不自选模块；开发结束恢复noconsole1的TASK-022继续保留。


DEC-028 / CON-001（2026-10-15 用户新范围）：基础官方B已运行后，用户直接要求开发虚拟路由菜单、可配置监听端口/WAN访问、主控地址和Node密钥、DNS/虚拟路由服务端客户端/分流，并补充Probe Link状态和节点测速。最新明确开发指令替代“等待精简名单才开始业务”；本轮不自行精简模块。菜单位于网络工具下方，名称“虚拟路由”，管理复用原厂会话/CSRF边界，12040默认且可修改，WAN入站默认关闭，监听仍只绑定活动WAN。新增Node ID字段与原控制器挑战HMAC身份匹配；密钥只保存不回显。

REQ-020：虚拟路由页面支持启停、主控地址、Node ID/密钥、监听端口默认12040及WAN入站开关；配置校验/持久化/生命周期完整。REQ-021：展示Probe Link真实连接/对端/延迟/收发计数及速率；提供与现有Probe兼容的双向有界测速，显示结果/错误，不伪造测量值。

UNIT-009/IF-009（按DEC-031修订）：独立router_plugin C模块，复用现有API/wire契约而非普通Probe运行时；原厂httpd认证入口代理本地Unix socket，不新增WAN管理HTTP。路由策略/拓扑/权威fake IP由主控同步；首选内核TUN及托管iptables/路由，只接管fake IP与明确虚拟节点地址；链路协议保持12字节VR帧、HMAC及Ed25519 ticket，测速沿用类型4/VRS1。WAN变化重建绑定，停止恢复原dnsmasq与托管规则；缓存/并发/队列有界。

TASK-023（进行中，REQ-001~010/020/021）：独立实现协议/主控/策略/DNS/数据面/链路/测速、认证页面及固件集成，完成单元/互操作/ARMv7/固件构建验证。TEST-036协议和认证、TEST-037 DNS/分流/权威映射、TEST-038链路/测速、TEST-039平台启停/安全配置、TEST-040页面/ARMv7/镜像集成。固件升级沿用保留A/CFE策略，但先完成可审查构建和测试，不以页面或监听成功代替业务互通。


DEC-029（覆盖DEC-007/028仅WAN监听）：用户明确“开启公网连接时绑定wan地址，本地Lan地址始终绑定”。服务启用时Carrier始终绑定活动LAN IPv4:配置端口；AllowWAN开启才额外监听活动WAN IPv4并放行该端口，关闭撤销WAN监听和入站规则，不影响LAN服务端或主动客户端。两侧均验证现有HMAC/ticket，不绑定0.0.0.0；地址变化独立重建，缺失WAN不影响LAN。本轮管理API仍仅Unix socket经原厂认证页面访问，Carrier监听与管理入口分离。REQ-003/IF-003/UNIT-004按此更新。


DEC-030（资源/日志）：用户要求按256MiB设备设计，不引入数据库/复杂日志/高文件数；日志只stdout/stderr，不落文件、不接syslog、不保存测速历史。原Go草稿的调度线程/GC软内存预算已由DEC-031取消；C实现须明确缓冲区、连接与内存分配上限。原待验证参考为4096映射/2048 NAT流、单Carrier每拓扑、单测速3秒/16MiB、32并发DNS。配置及必要防重放安全状态少量持久化，数据包/连接统计均内存；不引入数据库。以上预算须通过测试及ARM实机RSS核对。


### 2026-10-15 语言选型修正检查点

DEC-031（有效）：用户明确“那就不用go”，路由器业务服务改用C；替代UNIT-001/009及TEST-002/013中的Go构建方案，保留原生Web、内核转发/TUN、DNS和Probe协议兼容目标。主控和普通Probe原有Go实现不受该决定影响。旧Go代码与构建产物只在本地忽略目录router_plugin/artifacts/go-prototype-20261015保存，不作为发布输入、不继续开发。没有把C重写或业务互操作标为完成。

REQ-022（P0，待开始）：主控新增“华硕56UV2”探针类型；注册、配置、展示、运行状态能够区分，普通Probe/linux_router安装更新产物不可误用于该型号。地址/Node ID/密钥由用户后续填写，开发和本地验证不等待真实凭据。关联UNIT-009、TASK-023、TEST-041（类型与升级隔离验证，待执行）。

REQ-023（P0，进行中）：路由器新服务使用C，官方ARMv7工具链构建；新服务不含Go运行时，不依赖Go工具链。使用系统内核与经核实的C库，认证/密码算法不自行发明；所有缓存/队列/连接有上限。关联UNIT-001/009、TASK-023、TEST-002/008/010/013/036~040。

准备门禁：有条件通过。语言/边界/协议目标已确定；C依赖与旧SDK兼容性尚须核实后才能承诺完整传输支持。TLS/WebSocket必须逐项验证；QUIC按用户后续DEC-032明确取消，不能宣称完整业务互通。原Go无测试文件的go test成功不计入C验证证据。完成门禁不通过，C服务尚未实现/构建/部署。

本轮操作：只调整本地开发路线、归档未部署Go草稿、更新设计；保留Web与固件集成草稿供C后端接入。未改设备、未刷写、未部署主控。归档可逆，不能擅自恢复Go为交付方案。下一步按4.1唯一动作执行。


DEC-032（2026-10-15用户收敛范围）：取消QUIC/HTTP3，物理链路采用TLS/WebSocket；尽量不引入新库。已核实官方构建树及rootfs含OpenSSL1.1.1t、json-c、libcurl，C服务优先动态链接现有库，不打包新库或升级系统库。电脑测试容器的开发包仅用于离线验证。强制QUIC的拓扑应给出明确不支持错误，不能伪报已建链。TEST-036/038及REQ-006按此收敛。TASK-023继续进行中。


检查点2026-10-15（C核心首轮）：已实现C配置/JSON限制、原子0600配置保存、VR12字节帧/流式解码、DNS报文/上游TCP-UDP、规则匹配、HMAC/Ed25519、主控挑战调用；主控asus_56uv2类型/原生UI选项/普通安装与升级拒绝已实现。主控internal/core全包测试通过；原生C单元测试通过；官方ARM GCC5.5交叉编译并在原rootfs库环境QEMU运行相同核心测试通过，依赖libcurl.so.4/libssl.so.1.1/libcrypto.so.1.1/libjson-c.so.2/libpthread/libc均来自固件。协议跨进程互通与实机业务未验收。后续C服务/Carrier/数据面/测速编写中，不能把核心测试计为完整功能通过。

检查点2026-10-15（轻依赖约束落实）：用户再次要求尽量不引入新库，保持DEC-032。新增ARM ELF依赖门禁，仅允许固件现有OpenSSL/libcurl/json-c及系统库，检查rootfs存在与无自定义RPATH。当前C运行时已实现管理Unix socket、DNS工作队列、TUN/NAT、TLS-WebSocket双角色和有界测速；两条并行握手任务移出转发循环，原生-Werror构建与核心测试通过。管理进程测试覆盖CSRF、密钥不回显、0600持久化、重启恢复和单实例。主控适配将涉及asus_56uv2的拓扑两侧下发TLS-WebSocket/单Carrier，保留存储拓扑原值。该适配与新增依赖门禁待本轮复测；主控持续上报、规则刷新、全链路互操作、生命周期和完整固件集成仍未完成，不计为业务已验收。TASK-023唯一进行中；下一步唯一动作：运行更新后的主控回归和官方ARM构建依赖核查。

检查点2026-10-15（主控连接与构建准备）：TEST-041主控internal/core全包回归通过，ASUS链路两端下发http2/单Carrier且不改存储拓扑。TEST-036扩展WebSocket头校验、嵌入NUL拒绝、DNS根查询/规则顺序及TCP/UDP/ICMP NAT已知报文校验通过。TEST-038主控控制流子项通过：C连接真实TLS/gorilla WebSocket/hashicorp yamux测试对端，挑战HMAC、两次上报、yamux Ping、report_once与route_config_sync/配置刷新通过；Go fixture仅电脑测试，不进固件。Carrier物理链路与真实业务仍未验收。TEST-039管理接口进程复测通过；进程PID由持锁C实例原子写入，启动清理自有残留，服务脚本准备JFFS/运行目录并校验PID归属；rc原生check_services恢复管理服务，防火墙重建检测与停止测速清理已实现待平台验证。新增专用build-router-firmware.sh、stage-router.py及rootfs Makefile集成；基础构建默认不应用业务补丁，已应用工作树要求使用专用构建入口。未刷写/未部署主控/未修改启动选择。下一步执行完整固件构建；整体完成门禁仍不通过。

### 2026-10-15 原厂同端口管理修正

DEC-039 / CON-004：用户明确页面直接在原厂菜单内、不需要单独管理端口，替代独立HTTPS8443与管理口令方案。刚完成的JFFS d577a041e68c7c0f安装/HTTPS验证只作为历史，不作为当前UI验收；已停止旧服务及8443监听，恢复原菜单。REQ-009/010、UNIT-006、IF-006/007更新为原厂认证后同源ASP与Unix socket桥接，不新增TCP管理监听。原厂httpd的mime_handlers、do_ssl、ssl_stream_fd动态导出已经核实，可设计极小预加载扩展接入认证后的ASP处理；不改Flash或重编译系统。先在隔离环境验证继承认证和接口边界，再受控重启原厂Web进程，失败撤销扩展恢复原始进程。TASK-023仍唯一进行中。下一步唯一动作：实现并隔离验证同端口httpd桥接与原厂外框页面。

### 2026-10-15 同端口管理与界面漂移修复检查点

TEST-043（通过，REQ-005/007/009/010/023，UNIT-001/005/006，IF-004/006/007，TASK-023）：移除独立HTTPS监听/管理口令实现和旧测试，由Unix管理、原厂httpd桥接与其认证/生命周期测试替代。原厂httpd导出mime_handlers/do_ssl/ssl_stream_fd；扩展仅包装已有受认证ASP处理器的输入/输出，保留auth回调，其余路径原样委托。固定stock httpd hash144f9083c6ae57632cf59b4012d5ad46aff6f4f64012ce3fed50e0f1b8ebf6d1，未知版本/已有第三方挂载拒绝覆盖。原可执行文件复制到RAM，长度/hash不变；可撤销RAM启动入口挂载加载9,440字节扩展。原厂SSL初始化、ARM扩展构造器、导出契约/原厂库、BusyBox语法、native核心/Unix API/CSRF/机密脱敏、继承认证与非插件路径委托通过；临时特权Docker验证安装、升级、回滚、卸载与原目录/菜单还原。未认证API拒绝，未新增管理TCP监听。服务87,420字节，系统库不打包；原厂独立dnsmasq切换/SIGHUP恢复亦实机通过。

实机安装原厂B成功（无系统重启，boot_id仍f9b282a3-b242-43d1-89dc-dac58ed3a30f），配置仍enabled=false，主控/节点密钥未设置；配置保存/刷新测试只临时改端口，已恢复12040。原厂上游文件仍两行server=192.168.205.1，未接管DNS。活动1a0dcc37ef028a2d，压缩包58015字节，SHA256044fd74bde5dbdc9ad0b40146cb10d14823a388e817f6cee4fcf7788e7a74dd7；发布四个载荷113008字节，含上一版本JFFS目录229KiB，JFFS空余13160KiB（文件系统按实际压缩/GC变化）。RAM文件680KiB、空闲服务RSS3584KiB；业务峰值未测。运行httpd hash严格等于原版；TCP仅沿用原厂80，无8443/12040监听（业务尚未启用）。更新过程停止旧版时实际撤销原Web入口并重新接入；无Flash写入。证据位于忽略的router_plugin/artifacts/stock-runtime-validation.json、jffs/device-stock-web.json。

DEF-008（已修复，REQ-009/010）：用户截图显示页头居中但侧栏/内容左漂。原因是新ASP缺少原厂content的align=center及FormTitle嵌套容器，且原厂current_url只取basename，无法选中子目录菜单，触发tab未定义。复用Main_Netstat_Content.asp的外框/760px FormTitle结构、base根资源路径，并为本页面指定完整菜单URL。TEST-044（通过）：真实Chromium从原厂登录表单认证，点击网络工具下方菜单，保存/刷新配置并恢复，原厂网络工具与插件banner/menu/form的水平位置和宽度误差小于2px；1280及1722宽度对齐、390手机截图；零pageerror、零HTTP失败，无插件登录/额外管理端口。证据router_plugin/artifacts/browser/verification.json及stock-menu-desktop/wide/mobile.png。初期用Python生成的cookie直接注入浏览器被原厂拒绝，已改为正常浏览器表单登录；该测试问题不误报为菜单挂载失败。

本轮准备门禁通过，JFFS安装/同源原厂页面子阶段完成门禁通过；整体产品仍不通过：真实主控/业务实机未配置，WAN重建/业务峰值与跨重启自启仍待验收，noconsole最终恢复1仍待开发收尾。TASK-023唯一进行中；下一步唯一动作见4.1。README已改为当前JFFS方向，历史固件集成源仅参考。未提交/推送/发布，保留其他任务脏改动。

### 2026-10-15 统一节点DNS配置（当前有效）

REQ-025（P0，进行中）：普通Probe、Android和ASUS统一一项DoH、一项DoT、一项普通DNS及操作系统DNS回退；默认三项为用户给定阿里地址。主控/mng/route的DoH标签新增独立“节点DNS上游”配置，不改变既有主控DoH服务上游；保存后通知节点同步，离线节点重连获取。节点首次使用默认JSON，主控下发后原子保存，重复相同值不重复写Flash；旧主控省略该字段时保留本地值。系统DNS取真实物理网络的原上游，排除自身拦截地址。

总体架构/DEC-040：/api/probe/route/config顶层新增dns_upstreams={doh,dot,dns}；主控配置存入既有route JSON，节点单独小型dns_upstreams.json（ASUS沿用config.json嵌套对象）。错误配置整体拒绝，保留旧值；TLS验证证书/主机名，DoH使用DNS wire POST；失败逐级回退，普通UDP截断转TCP；不引入外部库、不增加日志文件。系统DNS是一类，允许系统提供多个有效物理DNS地址；前三类各且仅一个。DoH/DoT域名引导解析用普通DNS/系统DNS，默认阿里域名可用静态引导IP，禁止递归调用自身DNS服务。

UNIT-010：主控probe_dns_upstreams.go、probe_route_config_store.go、probe_route_handlers.go、mng_pages/route.html负责校验、保存、下发与页面；probe_node/internal/dnsupstream共享本地配置与Android查询传输；普通Probe复用原查询器及物理出站绕行；Android通过ConnectivityManager传入非VPN网络DNS；ASUS用既有curl/OpenSSL及受限缓冲实现同等机制，保存配置保留身份和管理外框。
IF-010：dns_upstreams对象三个必填字符串doh/dot/dns，节点配置响应可省略以兼容旧控制器；管理保存非法值返回400且不改变任何已保存值；控制器保存成功后触发既有route_config_sync。关联REQ-025/TASK-023/TEST-045~048。

| 追踪 | 单元/任务/接口 | 验证计划 | 当前结果 |
|---|---|---|---|
| REQ-025 | UNIT-010 / TASK-023 / IF-010 | TEST-045主控API保存/校验/通知/节点响应；TEST-046普通Probe/Android默认JSON、覆盖/离线重启、四级顺序/证书/截断；TEST-047 C同等协议与原厂ARM ABI；TEST-048浏览器真实填写保存/刷新与窄屏布局 | 已实现，API/四级DNS/华硕实机菜单通过；Android新AAR/APK打包待完整NDK |

准备门禁：通过。现有脏改动已经识别并保留；不刷写固件、不重启系统、不修改CFE/A。回滚：恢复上一插件及节点程序版本，保留JSON；旧端忽略新增顶层字段。完成门禁仍不通过，当前唯一进行中TASK-023。


### 2026-10-15 统一DNS实现与华硕更新检查点

实现：UNIT-010/IF-010已落地。主控独立DNS管理API复用原后台认证，写既有route JSON，成功后通知route_config_sync；所有节点配置响应均带三项值。普通Probe复用物理出站绕行和查询器，清除doh.pub/dot.pub/119.29.29.29的额外候选（历史静态引导兼容表保留，不作为回退候选）。Probe和Android共享dnsupstream配置/传输模块，无新外部库；本地文件支持默认初始化、合法主控原子覆盖、相同值免写、旧主控省略保留、合法主控修复损坏本地JSON。Android物理DNS通过非VPN的ConnectivityManager/LinkProperties传入，网络变化刷新。ASUS新增既有curl/OpenSSL DoH/DoT和原厂系统DNS兜底，配置嵌套dns_upstreams持久化，旧普通DNS字段可读迁移；本机/fake DNS过滤，保留身份与原菜单布局。

TEST-045通过：主控全包`go test ./... -count=1`及`go vet ./...`；新增测试覆盖默认值、整体校验/非法保存保持、存储回读、同步通知、普通/Android兼容类型/ASUS响应。TEST-046（DNS模块通过，完整新Android产物待验）：Probe全包曾有完整通过；最终Windows回归出现既有代理测试端口预留波动，两项受影响代理测试单独复测通过。共享DNS真实HTTPS/TLS、证书拒绝、回退到系统DNS、UDP截断转TCP、默认/覆盖/旧主控/离线重启/损坏JSON修复通过。Android Go arm64交叉编译通过，Kotlin编译/lint通过；系统DNS桥已编译。`go vet ./...`在Probe侧存在未改动的Windows unsafe.Pointer和mobile_vrouter_test旧复制mutex告警，不报全包vet通过；新增dnsupstream单包可独立vet。Windows全包曾因既有TCP/UDP测试端口预留失败，原测试单独重跑通过，未改测试逻辑；此前额外119.29默认断言按新需求修订。额外race检查因本地CGO禁用未运行，不作为通过证据。

TEST-047通过（本地功能/ARM ABI/华硕配置）：真实C HTTPS→TLS DNS→普通UDP/TCP传输、证书信任拒绝、系统原DNS回退/排除自身/厂商新上游优先均通过；原生核心、DNS分流TCP/UDP工作线程、Unix管理/CSRF/持久化/重启、httpd桥、特权临时容器安装/更新/回滚/卸载与原厂DNS/menu恢复通过。ARM编译/原厂52332库与SSL初始化/QEMU门禁通过，二进制约90KiB，未增库。中间测试曾因Ubuntu Python3.6不支持capture_output失败，改为兼容标准参数后通过；test_jffs首次漏helper参数属命令错误，正确传入test-platform后通过，未放宽断言。

TEST-048通过：Browser plugin not available，使用现有Playwright。真实本地Go管理handler/store的/mng/route→DoH→三字段填写→保存POST载荷→刷新回读→非法值400保持原值，1440/390px检查新控件无越界、截图并视觉检查、零pageerror。截图在用户visualizations/dns-controller-desktop.png及dns-controller-mobile.png。华硕83dcc8ed5eb72c3f通过原厂登录实际页面加载三DNS字段、保存/刷新保持、菜单/原厂外框1280/1722及390截图、零脚本/HTTP失败；未新增管理端口。

实机：只更新JFFS插件，压缩61141字节，SHA25630cd042c4ac3fef9e431ff666a371042ad02bb14be8e59c229a3ee8ca7a6976d，四个运行载荷117582字节；包与实机载荷回读一致。业务仍关闭，无主控/密钥；本机JSON三项已保存用户给定默认值。重复相同配置前后inode一致证明没有原子替换重写。初次实测误用不存在的stat命令，改为ls -i验证，配置本身已成功保存；未误报失败脚本为通过。原厂52332/Second/boot_id仍f9b282a3-b242-43d1-89dc-dac58ed3a30f，无系统重启/刷写，JFFS空余13140KiB。安装和原厂浏览器证据在忽略的artifacts/jffs/device-dns-web.json与artifacts/browser/verification.json。

剩余环境门禁：本机SDK的NDK27.2.12479018和30.0.14904198仅.installer残留，gomobile bind均报缺meta/platforms.json；尝试Google官方SDK元数据下载25秒连接超时。Go Android arm64编译和壳assembleDebug/lint通过，但后者包含旧AAR，明确不能作为新DNS APK交付或发布依据。未发布/推送、未在线升级主控或现有Probe；依cloudhelper-release-push的验证门禁保留当前代码，待完整新AAR/APK验证后再发布。源码DNS子功能本地门禁通过，REQ-025交付完成门禁不通过（Android新产物尚未关闭），TASK-023仍进行中，整体插件完成门禁不通过。

补充验证：gobind Java/JNI生成通过，新setSystemDNS(String)桥存在；新增dnsupstream单包vet通过。最终Windows全包另一次失败为TestProbeVRouteProxyListenersWorkWithoutTUN的既有TCP/UDP端口预留，与本轮DNS测试无关，单独复测并补Linux全包验证，不篡改旧测试或忽略失败。未能满足完整发布门禁，保持未推送。


补充回归结论：Windows TestProbeVRouteProxyListenersWorkWithoutTUN 单独复测通过。以Windows交叉编译的Linux测试可执行文件在本机WSL运行完整main包，唯一失败为TestReleaseWorkflowDefinesLinuxRouterArtifacts：runtime.Caller保留Windows源码绝对路径D:/Code/CloudHelper，WSL无法按该路径读取release.yml；其余执行的main包测试通过，C互操作测试未设环境变量而跳过。不能将这次跨平台运行记为Linux全包通过。官方NDK下载额外验证：系统解析超时后用223.5.5.5获得dl.google.com IPv4并以curl --resolve重试，TCP连接仍超时；未安装完整NDK，未发布。

### 2026-10-15 临时优先：Probe #9访问#23局域网诊断

用户要求只读排查192.168.50.1不可访问，并询问中转节点丢包、网关SNAT及新协议必要性。本轮通过现有主控认证控制台桥分别读取#9/#22/#23，不将本地状态冒充远端；所有查询产物保留在忽略的router_plugin/artifacts/lan-*.jsonl，凭据不入日志，临时cookie退出即删除。没有改远端路由、NAT、hosts、进程或业务配置，没有升级生产探针。

实证：#23 HomeAlpine为192.168.50.94/24，已发布192.168.50.0/24、允许#9/#14；从#23请求192.168.50.1:80返回HTTP200，ARP可达。#9→#22→#23路径RTT约26ms，已有链路无当前队列积压；这些不能单独证明目标业务转发正确。#9本机route_test在计划阶段失败：目标IP未命中虚拟路由规则；缓存更新时间2026-10-15T12:45:37Z，没有linux-router-23规则，OS实际选192.168.0.0/16经Wi-Fi192.168.52.1。由此本次请求没有按预期进入虚拟链路，未发现中间节点丢弃该请求的证据。

配置同步根因：#9多次收到route_config_sync但拉取/api/probe/auth/challenge或/api/probe/route/config超时。Windows hosts含cloudhelper.fengz.de→104.29.111.127，curl/PowerShell使用该记录HTTP200；强制同域名连接DNS得到的172.67.203.212与104.21.90.186均连接超时。现有Probe的resolveProbeLocalDNSIPv4s仅查自身dns_host.txt/引导DNS，不读取系统hosts；该节点dns_host.txt无主控条目。使用节点现有身份在节点内部签名，并通过系统解析只读请求主控最新route/config成功，确实收到linux-router-23-cfa59d7da17d、cidr192.168.50.0/24、exit23。因此为新规则因解析路径不同未成功同步，无需另设网关代理协议。后续修复可统一控制链路对系统hosts的支持，需测试后在线更新；本次诊断未擅自改节点配置。

SNAT实机：#23 nft cloudhelper_router postrouting存在oif cloudhelper0/ip saddr@lan4/ip daddr@routed4 snat198.18.0.17，eth0/lan4 masquerade，以及iif cloudhelper0/oif eth0/ip daddr@published4 masquerade；ipv4.ip_forward=1。最后一条负责远端访问本地LAN转换为192.168.50.94，conntrack执行回包反向转换。规则未启用counter，未修改规则加计数，不能声称本次失败请求触发过SNAT。该诊断完成，当前继续保留DNS新Android产物的未完成门禁。


### 2026-10-15 共享网关代理（最新有效需求）

REQ-026（P0，进行中）：用户确认“网关代理”指远程访问本节点LAN并做SNAT；从旁路由/单臂路由抽出为虚拟路由独立标签，普通Probe（Windows/Linux）同等支持。可配置最多32个私有IPv4代理网段、允许访问节点列表；配置持久化，停用/撤销授权后拒绝访问。已有LAN客户端上网/DNS接管保留各路由模式，不放进新功能。REQ-027（P0，进行中）：主控只向获准源节点下发网段路由，源节点原子保存并在离线重启恢复，成功收到撤销配置后替换缓存与OS路由，防止永久残留。

现状/DEF-009：普通Probe配置缓存persist/load主动stripProbeLinuxRouterPublishedRouteRules，导致即使下发成功，重启或同步失败时丢失远程LAN授权路由；本轮按用户要求取消剔除并增加重启/撤销回归。之前“缓存缺少路由”不能单独证明没收到，实机未命中与持续HTTP同步超时为另外独立证据。

总体架构/DEC-041：新增共享gateway_proxy.json及共同Web API/UI，不再依赖linux_router构建标签。复用既有认证IP帧/路径和gVisor TCP/UDP转发，不新增线协议或库；目标节点按原始源虚拟IP及路径校验允许列表，物理LAN连接由本机socket发起实现源地址转换，回包仍经既有链路。Linux路由产品保留内核原模式，自身远程LAN设置迁移到共享配置；旧配置导入一次，之后独立修改不会被旧值覆盖。内核旧本地LAN入口不用于绕过新ACL。ICMP复用既有物理回显实现，业务范围为TCP/UDP与echo，不承诺广播或任意L2协议。主控运行上报新增gateway_proxy配置，动态聚合到获准节点RouteRules，保留旧linux-router字段兼容旧节点。控制面系统hosts修复另作最小回归，先不改生产配置。

UNIT-011：probe_node/probe_gateway_proxy.go及Web负责校验、本地JSON、迁移、授权和转发；main启动/报告关联；既有exit netstack复用；路由缓存与Windows/Linux发布网段路由器支持新规则ID。主控probe_gateway_proxy.go、probe_ws/probe_runtime/probe_virtual_router.go负责规范化上报、变化通知与按源下发。原旁路由/单臂HTML移除远程访问重复设置，新增统一网关标签。
IF-011：本机认证GET/POST /local/api/virtual_router/gateway；JSON {enabled,proxy_cidrs,allowed_node_ids}，三字段整体保存，非法返回400且保留旧值；GET返回配置、节点候选及状态。报告gateway_proxy同结构；派生gateway-proxy-<node>-<hash>路由只下发允许源。旧节点/控制器省略字段保持兼容。关联REQ026/027、TASK023、TEST049~052。
TEST-049：配置多网段/非法原子拒绝/重启保存/旧路由迁移；TEST-050：控制器源ACL与上下线/撤销、普通与路由产品；TEST-051：真实TCP/UDP往返与源地址转换、拒绝未经允许源、中间节点不提前消费、离线缓存恢复与撤销清理；TEST-052：真实浏览器普通/旁路/单臂独立标签保存/刷新与窄屏，Go Windows/Linux编译测试。
准备门禁：通过（用户已确认语义、验收/接口/任务和验证计划齐备，已有脏改动保留）。回滚：恢复旧程序与备份JSON；旧节点忽略新字段，新网关默认关闭，迁移前保留原配置文件。发布与实机升级仍遵循原在线更新门禁，尚未执行。TASK023继续唯一进行中。

REQ-028（P0，进行中）：华硕原厂JFFS插件也提供独立网关代理设置、多私有IPv4网段、允许节点列表、出口鉴权、LAN SNAT和获准远端路由持久化；复用现有IPv4帧，主控上报gateway_proxy同契约。不刷机、不新增库/管理端口、不写日志文件。关联UNIT-012、TASK-023、TEST-053。
UNIT-012/DEC-042：华硕保留既有有界C数据面和内核iptables/conntrack，出口确认路径终点、源虚拟IP对应路径首节点且在允许列表，目标命中配置网段后注入TUN；只给这些网段添加LAN出站SNAT。来源端按下发CIDR经现有链路出站，策略快照本地原子保存并随服务恢复，撤销替换缓存和受管路由。菜单沿原厂VirtualRouter.asp内增加网关代理标签。TEST-053覆盖配置边界、未授权拒绝、SNAT命令、重启缓存与撤销、ARMv7构建与原厂页面。准备门禁：通过（共享契约兼容、省略字段旧端不受影响）。

REQ-029/IF-012（P0，进行中）：华硕上报现有Probe ipv4物理地址数组须含有效LAN和WAN且去重，WAN断开不保留旧地址；追加lan_ip/wan_ip字段标识来源，主控运行态可读，地址变化重建后更新；虚拟IP不混入物理IP。关联TASK-023、UNIT-012、TEST-054（有效双地址、相同去重、WAN断开及主控解码）。现状control.c已有ipv4双地址代码，需增强来源字段/去重与测试，不能仅凭代码宣称实机主控可见。

REQ-030（P0，进行中）：华硕活动WAN口RX/TX累计字节以现有system.network_rx_bytes/network_tx_bytes/network_traffic_available上送，复用主控增量累计统计；仅WAN，不与LAN或虚拟口相加。失联/接口计数不可读时available=false，接口切换后新计数不冒充历史值。TEST-054补充临时sysfs fixture双方向、缺失与主控现有计数链路。

用户交付指令（2026-10-15）：这几个功能开发测试完成后提交GitHub，按cloudhelper-release-push验证、提交、推送并监视发布。未通过验证不推送；不把尚未构建的新Android DNS产物冒充完成；需要区分本轮可独立发布的功能与旧门禁。

检查点（2026-10-15，共享网关实现）：普通Probe/路由产品新增gateway_proxy.json、认证API及独立标签，出口按路径/源虚拟IP/允许列表和CIDR逐包校验；配置改变关闭独立转发栈与连接。主控上报gateway_proxy聚合gateway-proxy-*路由，旧linux-router报告仅在未上报新字段时兼容。源节点缓存不再剔除授权发布路由，使用原子替换；Windows/Linux受管路由识别新前缀。华硕32网段/64节点（硬件限额，普通Probe256节点），内核LAN SNAT与来源鉴权，策略/证书缓存。WAN/LAN来源字段+去重IPv4、WAN-only uint64计数接入既有主控流量累计。
TEST-049/050通过：普通Probe持久化/非法保留/ACL/真实TCP UDP物理连接和撤销、主控跨普通/华硕多网段按源授权、撤销、地址及统计解码；Windows全包go test ./...通过，Controller全包与vet通过。TEST-051/053部分通过：真实隔离Linux内核TCP/UDP远端帧→LAN SNAT，目标实际看到192.168.52.1并回包，未授权丢弃，停止清理；C核心/数据面/网关与上报单测通过。ARMv7正式编译99708字节、原厂52332库/SSL/桥接初始化通过，无新库。
TEST-052部分通过：普通Probe真实浏览器1440/390多网段、多节点保存回读、非法提交保留且无溢出；路由产品导航测试初轮定位器将role=tab当link，属测试错误待改，不宣称通过。内核首轮缺iproute2镜像、次轮测试载体vr_path无回程导致超时，已改为platform测试镜像与完整双向载体，产品未因测试错误放宽。
发布预检发现当前工作区已有独立发布裁剪（移除router-arm64及Docker）与技能旧契约不一致；保留该改动，不混入本轮提交。准备使用隔离发布树核对实际拟提交工作流（HEAD原版）与原版配套测试。Android DNS源码/新APK门禁仍未完成，不纳入本轮独立网关提交，已有成果保留。TASK-023继续，下一步唯一动作：完成路由/原厂页面和离线缓存回归，再核对提交树与发布。


### 2026-10-16 网关代理交付候选检查点

TEST-049~054本地功能门禁通过：普通Probe及主控在隔离发布树全包测试通过，主控vet通过；网关配置多网段/节点持久化、出口ACL、TCP/UDP物理往返与来源地址、配置撤销关闭连接通过。原厂C核心/数据面/上报单测、服务API及进程重启通过，新增缓存回归证明在线保存、离线恢复、不重复写、成功撤销覆盖、错误身份/损坏缓存/非法在线响应拒绝。普通Probe和华硕均复用现有IP帧，不增加协议类型或运行库。ICMP异步响应前也复核当前授权。

TEST-052：普通页面1440/390真实保存载荷及回读、路由产品旁路/单臂导航、共享入口和旧控件移除均已通过浏览器验收。Linux专用旧页面测试仍要求原控件，按最新需求改为断言共享入口存在且旧控件不存在，保留其他配置/连接/升级验证；新增旧路由配置只迁移一次、独立撤销后不复活回归。完整Linux专用回归见后续最终结果。

TEST-053：ARMv7最终程序99708字节，无新增库；原厂52332库/SSL实际初始化、BusyBox及httpd扩展验证通过。JFFS发布89529b40cd325596，包65088字节，SHA256 ebd2e4cb208cd8eb834a6b0b3849beb04f06f752839ec09c80040080c5c2a263，四个载荷128965字节；实机回读哈希与包一致。原厂页面真实网关标签、多网段/节点部分保存请求、刷新回读、恢复原配置及原菜单对齐通过，零脚本/HTTP错误。原厂52332与boot_id f9b282a3-b242-43d1-89dc-dac58ed3a30f保持，未刷写/重启；WAN192.168.205.3、LAN192.168.52.1。业务仍关闭，无主控/身份密钥；上报字段及WAN-only64位累计计数通过真实代码/临时sysfs/主控解码测试，尚不能声称真实主控已收到。原厂跨重启自启仍需手工SSH启动，不将配置重启持久化等同自启。

CON-012/DEC-043：fetch后确认origin/mapledev已前进至94422f3b，018210eb明确移除router-arm64及Docker发布，且远端工作流/对应测试与原工作区改动相同。上一检查点拟用旧HEAD工作流的方案已被远端实证替代；不恢复已退役产物。当前技能预检固定旧标记而失败，按技能要求的“读取实际工作流”和“unless live workflow intentionally changed”识别这项有意变更，保留当前已发布契约，执行等价的分支目标/remote/无冲突操作/GitHub权限/版本触发/有效产物与配套契约测试。隔离工作区D:/Code/CloudHelper-gateway-release基于最新远端，分支codex/gateway-release，最终明确推送HEAD:mapledev；原工作区及无关改动保留。不恢复上游已删除的firmware/asuswrt历史构建目录，插件构建改为CHVR_VENDOR_ROOT指定外部官方SDK目录。仅必要源码、测试及说明入Git，bin/artifacts/__pycache__忽略。

本轮提交范围：共享网关、ASUS C JFFS插件及必要的Probe/主控接入和普通Probe/ASUS DNS；Android DNS mobilecore/Kotlin变更保留原工作区，不纳入发布。候选树使用远端现有Android代码，并完成Go回归，发布时CI仍构建原有Android产品，不冒充新版Android DNS交付。REQ026~030源码及本地功能完成；真实跨节点业务需要用户后续填写身份联调。TASK023仍唯一进行中，下一步唯一动作：完成Linux专用最终回归，提交候选树并推送GitHub，核对对应Actions及产物。

最终发布前回归：Linux专用TestProbeGateway（含一次迁移）、TestProbeLinuxRouter、TestBuildProbeVirtualRouterLinuxPublished、TestEnsureProbeVirtualRouterLinuxPublished全部通过；新断言发现单臂旧选择框实际残留，现已清理并以真实浏览器双模式导航复测通过，非仅调整测试。正式候选树Controller/Probe全包、Controller vet、发布工作流契约测试通过；linux_router amd64测试可执行文件及arm64程序构建通过。112个明确文件入暂存，凭据扫描与diff --cached --check通过；未暂存Android/固件基线/生成文件或其他任务。提交门禁通过，真实业务与自启动门禁继续保留。


### 2026-10-16 GitHub提交与发布验收

本轮独立交付已完成：功能提交66aeb79dfcb5203b3e21bafddfeb3e95d6e2514f，已推送origin/mapledev；Actions https://github.com/fengzhanhuaer/CloudHelper/actions/runs/38768096220 全部success，自动版本提交8b1df2d9与标签v0.4.54仅修改version。发布 https://github.com/fengzhanhuaer/CloudHelper/releases/tag/v0.4.54 为正式非草稿，当前工作流规定的8个产物及额外ASUS JFFS包/manifest共10项均非空。已下载ASUS发布包核对SHA256 ebd2e4cb208cd8eb834a6b0b3849beb04f06f752839ec09c80040080c5c2a263，manifest明确绑定上述功能提交；未把本地生成文件提交Git。Docker/router-arm64依远端018210eb的既有裁剪不发布，未擅自恢复。

本轮REQ026~030源码、独立产物及本地/隔离环境验收完成；整体产品TASK023继续进行中（真实用户身份联调、跨重启自启、WAN重建/业务峰值及Android新DNS产物仍待验证）。普通Probe/生产主控未执行在线升级；华硕只做已授权的JFFS插件更新及同源Web验收，业务保持关闭，固件/启动ROM/系统分区与boot_id未改变。noconsole保持开发期0，整体开发结束后再恢复。后续唯一动作：用户在原厂“虚拟路由”页面填写主控/Node ID/密钥后，按在线升级流程联调实际跨节点LAN访问与上报；不将测试fixture或当前PC状态当作远端实机状态。

为保留原工作区其他任务和Android未完成改动，代码从D:/Code/CloudHelper-gateway-release隔离工作区提交；原D:/Code/CloudHelper未被reset/checkout/stash。验收结果只更新本账本，以[skip ci]提交，不触发新的业务版本。


### 2026-10-16 补齐所有自有源码提交（用户最新指令）

用户明确要求剩余源码均入Git，包括Android APK源码；并明确“不要提交密码”。REQ-031：提交Android DNS/mobilecore/Kotlin、相关测试与说明、历史固件自有构建/补丁/串口工具及剩余任务记录，排除上游源码、SDK、镜像、设备备份、生成文件和任何真实密码/Token/私钥/签名密钥。此前将源码与未完成APK产物门禁一起留在未提交状态的处理已替代；源码交付与CI打包结果分别记录，不把旧AAR打包当作新功能验证。

UNIT-013：保留firmware/asuswrt历史来源锁与自有构建/集成代码，README明确当前产品仍是原厂JFFS插件，历史补丁未针对最新插件做整机编译；串口工具Windows及本机WSL测试验证。Android复用已提交的dnsupstream配置/协议模块，Go测试覆盖主控下发和重启保存，Kotlin传递非VPN物理DNS并跟随网络变化；新版AAR/APK由现有CI重建验收，不使用HomePc。IF-010保持不变。

TEST-055：检查所有明确文件清单及其秘密字段；实际路由器密码从私有备份仅在内存比较，不输出；源目录禁止设备cfg、密钥容器和私有证书，新忽略规则补齐。TEST-056：Probe全包、共享DNS测试、Kotlin/lint、串口8项（WSL实际shell往返）与构建工具语法；CI新版AAR/APK最终打包签名验证。提交在当前D:/Code/CloudHelper的mapledev完成，同步自动版本提交并确认工作区不再遗留本轮源码。TASK023继续唯一进行中；下一步唯一动作：完成本地回归及秘密扫描，明确暂存清单，提交推送并验证CI。自启动咨询仍待后续完成，未持久更改路由器启动或重启。

本地提交门禁：TEST-055通过，37个明确文件与实际私有路由器密码、常见Token和私钥头逐项检查无匹配；签名密码仅内存比较，秘密/备份/SDK/输出路径忽略验证通过。TEST-056本地通过：Probe全包、Controller全包及vet、发布契约、Android compileDebugKotlin/lintDebug、Windows串口7项+POSIX项跳过及本机WSL串口8项全部通过，所有新增Python AST与9个shell入口语法通过。本机没有完整NDK，Kotlin检查使用已有AAR；新Go实现的原生AAR与签名APK仍须由本次CI重建，尚不宣称通过。历史固件补丁仅归档自有源码，没有重新编译或刷写整机。下一步唯一动作：明确暂存37个文件，提交推送并核对功能SHA对应CI与APK。

### 2026-10-16 源码提交完成与原厂自启动复核

REQ031源码提交558dca945cba71c439b24accab9d0fb243b1fb63已在origin/mapledev，37个文件；自动版本提交16d645b2及v0.4.55，本工作区已快进同步。Actions https://github.com/fengzhanhuaer/CloudHelper/actions/runs/38852875864 全部success；新mobilecore AAR、Android release、固定密钥签名及apksigner校验、上传步骤成功，正式发布8个当前工作流资产非空。APK18597185字节、GitHub资产SHA256 f3f5c116714210dbe5635f61858d75f216f818c9ac77e0b86e95a1388ea19f4b。未部署生产Controller/Probe，也未升级或重启路由器。源码门禁和CI产物门禁闭合，Android真机多网络切换仍未验收。

用户最新追问：能否让插件在路由器重启后自动启动。只读复核localrouter仍为原厂52332-gfbf3b9c、boot_id f9b282a3-b242-43d1-89dc-dac58ed3a30f；/jffs为原JFFS2，/var/spool/cron在运行时目录，crond存在。实际/sbin/rc1755936字节，SHA256 c958494dfca7126a3c92b3e8292687e9607aa07dcd72db28a7bd684efa49c606，不含/jffs/scripts、services-start、init-start或jffs2_exec等入口字符串；字符串缺失只作为辅助证据，不单独证明所有可能机制都不存在。对应官方53297源码rc/jffs2.c459~466、ubifs.c287~294明确禁用legacy/asus autoexec；shared/scripts.c虽保留run_custom_script定义，rc没有启动调用，不能因函数/目录存在就承诺可用。

现有service start只注册当前启动周期cru任务；重启后RAM挂载菜单/httpd扩展和任务均需恢复。保持原厂不刷写的独立JFFS开机入口尚未找到，不能把写services-start或nvram jffs2_scripts=1冒充解决。CIFS挂载后执行机制在源码存在，但依赖外部SMB且并未实机验证，不为启动小插件引入额外重型服务或劫持VPN/网络配置。可选后续为外部常开设备SSH唤起，或在固件rc添加最小启动钩子后做B试启动；后者改变当前纯原厂约束，需要用户明确选择后再执行。未更改持久启动设置、NVRAM、A/B或CFE。

TASK023继续唯一进行中；源码提交子阶段完成门禁通过，整体功能/跨重启自启仍未完成。下一步唯一动作：根据用户对保持原厂与增加启动钩子的选择，实施对应持久启动方案并做真实重启验收；当前只读诊断不授权刷写或重启。此账本以[skip ci]提交记录本轮验收。

发布APK下载因网络中断首次失败，经断点重传成功；本地完整18597185字节及SHA256与GitHub一致，DEX存在setSystemDNS、新libgojni.so含SetSystemDNS实现，确认不是旧AAR产物。Actions逐步日志下载超时，不据此声称查看了签名日志；签名验证成功由具体步骤状态及工作流命令证实。TEST056新版原生产物构建验证通过，真机运行另行验收。

### 2026-10-16 网关代理保存异步化（用户最新指令）

REQ-032：网关代理配置在本机校验、落盘并应用后立即返回，不等待主控或获准节点在线/确认；后台上报主控，获准节点上线/重连后获取最新授权并持久化。截图来自普通Probe共享网关页，不改华硕启动或固件。UNIT-014/IF-013：probe_gateway_web.go成功POST仅排队异步报告；并发保存最多一个报告worker和一次合并补报，报告从最新本地状态生成，无常驻新线程/磁盘队列或新依赖，断线由既有周期/重连报告恢复。页面取消永久等待话术，只确认本地保存和后台自动同步，不虚构对端已应用。准备门禁通过：主控registerProbeSession已触发配置刷新、源节点已有初始/重连通知拉取和原子缓存；TEST-057覆盖阻塞报告时保存仍返回、连续保存合并保留最新、报告失败不撤销本地保存与重启恢复、未授权请求不排队；TEST-058覆盖真实桌面/窄屏浏览器保存/读取与离线文案。Browser plugin not available，沿frontend-testing-debugging使用已安装Playwright，无新增依赖。回滚为恢复旧版本，gateway_proxy.json契约不变。TASK023继续唯一进行中，下一步唯一动作：实现异步上报并完成离线/并发与浏览器回归。此前固件自启咨询暂不推进。

REQ032实现与本地门禁通过：POST成功落盘后仅启动有界、可合并的异步报告，不调用对端或等待确认；报告失败保留配置，复用周期/重连报告。进一步源码复核纠正上一检查点的“已具备重连刷新”：旧registerProbeSession仅路由产品状态变化才广播，普通Probe缺少明确重连拉取；本轮新增每次注册后异步向该会话发route_config_sync，并将启用共享网关的普通/华硕在线边沿纳入网段路由刷新。原通知/配置协议及本地缓存兼容。TEST057通过：阻塞且失败的上报不阻塞HTTP，20次保存合并为一次补报且使用最新授权；主控离线成功保存/重启恢复、非法保留；真实net.Pipe首连/重连均收到刷新命令，普通网关断线/重连恢复最新授权、撤销不复活。TEST058通过：生产handler本地浏览器1440/390，实际POST载荷和回读正确，成功显示后台同步/离线节点上线获取且按钮恢复；非法CIDR返回错误而旧配置保留，零JS错误、无横向溢出。截图仅在用户可视化目录，测试脚本在系统临时目录。Probe/Controller全包、Controller vet与实际发布契约测试通过；旧技能preflight仍因远端018210eb已退役Docker/router-arm64标记失败，沿CON012/DEC043以当前工作流及等价预检执行，无未解决功能测试失败。准备提交7个明确文件，未改固件/路由器状态/生产程序；下一步唯一动作：扫描暂存源文件、提交发布并核对功能SHA的CI。

发布检查点：功能提交212bc637022d6e159090202fe6261006c8c5bce5已推送origin/mapledev，工作区源码无遗留；Actions https://github.com/fengzhanhuaer/CloudHelper/actions/runs/38855979200 与该SHA对应，正在构建。当前没有升级生产Controller/Probe；完整行为需新版主控与相关Probe在线更新后生效。下一步唯一动作：核对该CI及自动发布资产，记录最终验收并提交账本。

REQ032发布验收通过：Actions38855979200全部success并精确对应212bc637022d6e159090202fe6261006c8c5bce5；自动版本提交d1058f75、正式v0.4.56及当前工作流8个非空资产已核对。本工作区快进同步version且源码全部已提交。此次子需求实现/本地测试/浏览器和发布门禁通过，主控及生产Probe未在线升级，实际跨节点离线→上线业务尚需升级后验证；不得把本地net.Pipe/浏览器fixture当生产运行结果。整体TASK023仍进行中，原厂自启及真实用户身份联调继续待验收。下一步唯一动作：按用户后续部署指令通过产品在线更新主控/相关Probe，验证真实离线节点重连获得最新授权；不通过SSH替换生产程序。无新增依赖、协议字段、密码或签名材料提交。

### 2026-10-16 原厂自启补充调研与梅林来源澄清

用户最新范围：继续查资料寻找不刷机的自启方式；追问梅林是否为原厂改版。本轮仅查询资料、官方GPL源码及SSH只读核对，没有安装启动方案、修改NVRAM、重启或刷写。TASK023仍为唯一整体进行中任务；本次调研完成不表示自启验收完成。

实机仍为原厂386_52332-gfbf3b9c、boot_id f9b282a3-b242-43d1-89dc-dac58ed3a30f。没有/sbin/mount-cifs、/usr/sbin/mount-cifs、/usr/bin/mount-cifs、CIFS内核模块或已注册CIFS文件系统；没有/usr/sbin/app_init_run.sh及app_base_*应用启动脚本。因此其他机型通过CIFS挂载或USB应用启动的教程不能直接套用本机。官方GPL53297（与实机52332版本不同）rc/jffs2.c、ubifs.c将旧exec/.asusrouter入口置于#if 0；rc中未找到run_custom_script启动调用，不能通过设置同名NVRAM就宣称启用梅林钩子。

外部一手资料：jacklul/asuswrt-scripts README明确原厂需要USB接口与Download Master支持，较新固件屏蔽script_usbmount，改用asusware USB应用机制；Entware官方Wiki也记录USB asusware.arm替代启动法。JACK-THINK/SCRIPTS-BOOTLOADER-FOR-ASUS-ROUTER同样依赖U盘。以上项目并非无USB/JFFS独立启动证据。参考：https://github.com/jacklul/asuswrt-scripts 、https://github.com/Entware/Entware/wiki/Install-on-Asus-stock-firmware 、https://github.com/JACK-THINK/SCRIPTS-BOOTLOADER-FOR-ASUS-ROUTER 。

VPN为未验证候选：实机存在/usr/sbin/openvpn与/usr/lib/libvpn.so，后者包含_filter_custom_config、script-security与配置生成字符串；GPL libvpn/openvpn_options.c有导入参数白名单，WAN启动调用start_ovpn_eas。OpenVPN上游文档支持config嵌套及up脚本，但这只证明上游能力，不证明本机ASUS过滤、配置跨重启保存与启动时序允许该组合。论坛提到config包含JFFS配置的绕行仅作为线索，未作为可用方案结论，未提供或执行绕过配置。后续若继续该方向，应先核实过滤函数与持久化，再设计隔离的最小标记验证，不能拿手工OpenVPN执行成功当系统冷启动自启通过。

梅林项目README确认其基于原厂Asuswrt修改，目的包括修复和少量增强；其自定义脚本钩子属于固件修改。当前官方Supported-Devices未列RT-AX55或RT-AX56U V2，旧RT-AX56U列于停止支持，不据此推断V2兼容。参考：https://github.com/RMerl/asuswrt-merlin.ng 、https://github.com/RMerl/asuswrt-merlin.ng/wiki/Supported-Devices 。若借鉴梅林只移植JFFS启动钩子，业务仍可留在JFFS，但需编译/刷入修改固件；不属于纯SSH设置。既有“必须改固件”说法应限缩为“尚未找到并验证本机原厂纯SSH、无外部设备的独立自启入口”，保留VPN候选的不确定性。无源码/API变更，无需代码测试；本轮只读核验通过，跨重启自启未测。下一步唯一动作：如继续不刷机路线，先核查本机libvpn过滤和配置持久化，未获得实证前不宣布可行或持久部署。


### 2026-10-16 最小自启动固件（用户最新选择）

REQ-033：官方GPL53297基础固件只增加JFFS插件开机启动入口，插件/页面/配置继续独立放在JFFS，保留原厂模块及分区，不带CFE前缀、不内置业务二进制。不迁移整个梅林，不自行精简。当前授权完成固件制作；刷写具体镜像及尚未验证的B ONCE另按升级门禁，不在构建中修改设备。
UNIT-015/IF-014：rc在原厂全部服务启动后异步调用ROM内固定启动脚本；脚本确认本机型号、JFFS2挂载、插件入口及禁用标记，调用/jffs/cloudhelper-router/service start；RAM锁防重入，无插件无副作用，输出console。插件既有当前启动周期cru负责后续恢复，固件不增加轮询/库/日志文件。构建生成只读httpd哈希声明供插件严格验证此定制固件的Web兼容；禁止简单取消原有httpd哈希门禁。业务服务、端口及协议不变。
DEC-044：从旧完整业务集成构建切换为最小自启变体，旧0001集成补丁保持历史用途并明确互斥；构建先恢复官方受影响文件、排除旧集成残留，再应用新自启补丁。来源仍为锁定官方归档及工具链；源码工作树/SDK/镜像/备份不入Git。
TEST-059：异步调用位置、无插件/禁用/错误型号或挂载跳过、重复启动锁与失败恢复、真实shell执行及console输出、无磁盘日志；TEST-060：官方ARM整机编译、最终SquashFS取出启动脚本及rc核对、无旧内置业务残留、镜像WFI/UBI/容量与无CFE前缀、插件ARM加载及Web桥接兼容、产物manifest和SHA。实机跨重启自启为待后续刷写验收，不能用离线测试冒充。
追踪：REQ033→UNIT015/IF014/DEC044→TASK023→TEST059/060→firmware/asuswrt新自启构建/补丁与router_plugin兼容启动。准备门禁通过；回滚保留原厂A/现有B及JFFS备份，构建阶段无需设备回滚。当前唯一下一步：制作最小补丁及构建入口，完成测试后生成可审阅镜像。

### 2026-10-16 最小自启固件构建检查点

- 已撤销旧 `0001-virtual-router-integration.patch`，恢复官方 HTTPD 与菜单源码；本轮仅应用 `0002-jffs-autostart.patch`。源码校验拒绝未知覆盖，业务仍留 JFFS。
- 实际 `make rt-ax55 PARALLEL_BUILD=-j6` 成功；无 CFE 镜像 35,258,388 字节，SHA256 `dcb1a7f5486bc373fe6ce4ff8b7f65aa70ba94508164255bef17feab3cf3d5dd`。WFI CRC/芯片/NAND、UBI 头、容量、原生 JFFS2、ARM ELF 检查通过；补丁报告确认仅 0002。
- 启动脚本与 HTTPD 白名单 5 项 POSIX 测试通过；一次性特权容器中的插件安装、更新、回滚、卸载与原厂菜单挂载测试通过。测试镜像缺少 iproute2，补齐该临时容器依赖后通过。
- 构建后台正常运行期间重复启动曾因容器同名拒绝，未终止原构建。构建结束时运行中的 shell 脚本因被编辑发生读取偏移，后置校验中断；脚本静态语法通过，单独重跑实际产物校验通过。后续不编辑正在执行的包装脚本。
- TEST059/060 离线门禁已通过：5 项真实 POSIX shell 测试、隔离安装/升级/回滚/卸载；ARM 插件在原厂 52332 与当前 53297 的依赖、核心、HTTPD 桥接、SSL 实际初始化及 BusyBox 语法全部通过，无新增运行库。直接从本轮无 CFE 镜像提取 SquashFS 验证启动脚本、rc 调用、HTTPD 精确哈希声明、原厂 Web 源码与无旧内置业务残留通过。没有刷写、重启或改设备配置。
- 发布技能旧 preflight 因已退役产物标记失败；重新查账本确认既有 CON012/DEC043 已记录该有意变更，沿已确认的当前契约完成等价预检及全部 TestReleaseWorkflow 测试，不恢复 router-arm64/Docker。待源码提交后核对对应 Actions，并将独立固件与新版 JFFS 包作为发布附件，构建输入与产物不入 Git。

本轮产物：`firmware/asuswrt/artifacts/autostart/CloudHelper_RT-AX56U_V2_53297_jffs-autostart_dcb1a7f5486b_puresqubi.w`，35,258,388 字节；同名 JSON/SHA256。JFFS 包 `router_plugin/artifacts/jffs/cloudhelper-asus56uv2-jffs-df505ecea68822cf.tar.gz`，65,376 字节、SHA256 `53b92fac473ec6d9d531905c753a73ad95182648f74aa6883439873871d94055`。插件二进制99,708字节、Web扩展9,440字节，无新增库。TEST060的实机跨重启自启部分待单独刷写验收；TASK023整体继续进行中。下一步唯一动作：提交本轮明确源文件并核对发布，随后等待具体镜像升级授权与B试启动方案；不在发布操作中刷写或重启设备。

发布网络检查点：当前 API 请求将 api.github.com 解析为 198.18.4.58，gh Actions/Release 请求超时；显式查询 223.5.5.5 返回 20.205.243.168。未修改系统 DNS、hosts 或路由。先完成本地源码提交；仅在 GitHub 实际可达并核对远端后继续推送，不将本地提交称作已推送/发布。

### 2026-10-16 最小自启固件实机升级启动

用户明确授权刷入已交付镜像。当前实机：RT-AX55/RT-AX56U_V2、原厂52332、boot_id f9b282a3-b242-43d1-89dc-dac58ed3a30f，当前/下次B，A序号16/B17且均commit1，原JFFS2可用、noconsole0，活动插件89529b40cd325596。COM4存在、原生登录提示可读，尚未认证shell。REQ033→TASK023→TEST060实机阶段进行中。先新建独立备份目录保存当前A/B/ROM/JFFS/配置/密钥哈希，再从B选择A并在已开始串口捕获时重启。升级期间不改电脑默认路由、密码或分区，不覆盖历史原厂备份。准备门禁有条件通过：写入前须确认A实际运行，B启动前须解决或明确处理历史ONCE选择异常。下一步唯一动作：完成当次备份与A切换。

写入完成检查点：新备份目录 `D:/Code/RouterFirmware/router-backups/20261016-jffs-autostart` 含当前A/B完整51MiB原始分区、ROM、JFFS、导出配置及三把SSH主机密钥哈希。已在完整UART捕获下从原厂B切回原厂A，boot_id 6ff87a34-497a-40cf-939a-462ea7753758，明确当前First；原生firmware_check=1、restore_defaults=0且RAM上传回读SHA相符。原生 hnd-write 仅写inactive物理B，退出99/hndwr99及完整日志已保存，无自动重启。写后A原始SHA仍4479aa9dd035f551d31143a34082a369189fac3d050bcf933e2b933311e42a87，ROM原始SHA与20261014完全相同；A全部269逻辑块一致。B全部269逻辑块核验通过，267个系统/布局载荷逐字节相同，仅两份metadata被平台改commit1/sequence017及相应CRC；无额外CFE写入。当前仍运行原厂A，写入器已选下次B；首次重启前必须调整并核实试启动选择。下一步唯一动作：配套JFFS升级并调查/执行有完整串口证据的B试启动。

启动前检查点：已在A安装配套JFFS发布df505ecea68822cf，活动版本及运行状态回读通过，autostart.disabled不存在，配置身份未改。原生B ONCE8已回读为当前First/下次Second且Bcommit0；为避免历史mailbox失效直接回A而无法验证，开始完整串口捕获并在CFE自动运行提示处请求暂停，仅查询help及help r，寻找不预先长期commit B的临时启动方式。未向CFE发送擦写、配置修改或Flash命令；不能把CFE暂停当作新内核已启动。下一步唯一动作：根据实际CFE帮助确定一次B启动方法；若没有可靠接口，保持证据并恢复A后按门禁处理，不猜测写命令。

ONCE核对结论：UART捕获在CFE自动运行窗口暂停，仅执行help/help r/p读取；实际help r只支持文档化r运行（f/h/c由bootline控制），未发现安全的单次物理B选择参数。文档化r后明确从previous image 0x00100000启动原厂A，boot_id1d128574-3960-4ba8-9688-b621fca69b96，B ONCE仍未消费；没有B内核启动证据。未更改CFE配置、RAM地址或Flash。DEF004仍有效，不能声称ONCE或故障自动回A有效。
DEC045/CON013：用户会话已明确“正式启动B试试”，本轮又对已交付具体镜像明确“刷入路由器”；按持续授权不重复询问，将普通B启动例外用于本轮已回读验证的最小变体。此为保留A和完整UART监护的普通B启动，不冒充健康后提升/有效ONCE流程；先用已核对本机ARM32 ioctl62助手清除未消费mailbox，确认A安全回退选项，再选择普通B并一次重启。若新B不健康而管理可达，立即按已有恢复授权回原厂A；不可达则保留串口证据定位，不盲目重刷。下一步唯一动作：普通B启动和新固件/插件自启健康验收。

首轮B启动健康通过：UART TRY2、latest image 0x03400000、本轮builder内核与“CloudHelper JFFS startup completed”构成完整启动证据；实际/下次Second、53297-gee411da、boot_id7911d888-0e2e-4a7d-a0b2-7b2bbe847c88。运行/proc/1/exe完整SHA b1c81f968330dcd498ce4c7a6c3c47dad255441503a91ab679e70333e342b724与镜像rc一致，启动RAM成功标记存在，插件PID1721/活动df505ecea68822cf，未手工service start。原JFFS2自动挂载、20项含密码在内的配置布尔比对及3把SSH主机密钥、双无线UP/AP、WAN网关/外网ping、LAN TCP/UDP DNS与Wi-Fi ifindex10绑定出站HTTPS200通过；原厂Web认证/设置页正常。虚拟路由页面、实际菜单URL及同端口认证API成功，匿名API拒绝。插件业务仍关闭，身份/主控/密钥未填，不能把插件管理自启当成主控/跨节点业务联调通过。
A在重启后有UBI元数据物理搬迁，raw变化为bf9699a332e90fa00a6dfbf1123a95ec1aecb8d9fe193d5892a8bbbf9327c0ed；保留所有原raw参照，全部269逻辑载荷与原始A仍逐字节一致。CFE原始SHA d3be3cb5ba9251eb5eaa1f4d8aacd9a4b3dfa2bcd500121e0d9ec130b2eba91f不变。健康通过后显式选择长期B并执行一次持久启动/自启重启验收，已先准备UART捕获。下一步唯一动作：核对第二次B新boot_id、自动插件和网络，再形成交付记录；ONCE/故障自动回退仍标未通过。

### 2026-10-16 最小自启固件升级验收完成

TEST060实机子阶段通过（普通B启动例外，ONCE/故障自动回退仍未通过）：第二次B启动boot_id e53728f3-4a36-4ba8-8b60-c5dfeb30e59a，与首轮不同，UART再次TRY2/0x03400000和CloudHelper JFFS startup completed。未手工拉起插件，活动df505ecea68822cf及实际运行二进制/ROM声明HTTPD/四个JFFS载荷哈希与交付产物一致，自动启动RAM标记与原厂菜单挂载存在。二次完整健康、原厂Web与匿名API拒绝、LAN TCP/UDP DNS、Wi-Fi绑定HTTPS200均通过；仅业务关闭/身份未填，保留用户原配置。原生UART登录认证及独立查询成功，无SSH桥接，凭据不落日志。插件目录无文件日志；本机BusyBox缺少find，改以只读tar枚举实际目录核验，并比对用户config.json与升级前备份完全一致。

最终分区校验：A raw bf9699a332e90fa00a6dfbf1123a95ec1aecb8d9fe193d5892a8bbbf9327c0ed，仅物理UBI搬迁，全部269逻辑载荷与原始备份相同；CFE原始SHA完全不变。B启动前后首次严格逻辑比对发现两份内部layout卷表变化，定位为rootfs_ubifs原生autoresize：239→368 reserved PEBs、autoresize位1→0和CRC重算；逐字节白名单重建两份卷表相符，其他所有逻辑载荷（内核/rootfs/启动metadata）完全一致，不修改原始预期或将差异忽略。具体报告final-b-postboot-content.json；全部备份与串口日志附最终SHA清单。

### 2026-10-17 华硕到节点23的自定义端口兼容修复

REQ-034：真实华硕25按主控节点表service_port连接节点23，修复“TLS connection failed”，保持C/JFFS独立产物、不刷固件、不改业务身份。用户说明同局域网Ies_Sub_Route能连接23；SSH实际证明172.18.52.205的四条Carrier连接目标均为123.171.243.171:12041，华硕拨12040被拒。华硕缓存probe_ips节点23已下发12041，拓扑端口省略；C只读拓扑并默认12040。华硕WAN绑定curl测试12041的TCP/TLS1.3/HTTP200通过，不能归因23整体不可用。DEF-005：C与Go节点端口选择不一致（已定位）。

总体架构/UNIT-016：policy.c提供与Go probeVirtualRouterServicePortForNode一致的节点端口选择，probe_ips匹配节点优先，缺省节点端口为12040；没有节点记录才用拓扑端口/12040。carrier.c拨号复用该函数，认证/SPKI/主控配置均保持。IF-015：已有probe_ips[].service_port契约，未新增协议或依赖。回滚使用previous JFFS发布，保留当前0945c1b32149e62d和配置私有备份；不触碰A/B/CFE。

TASK-024进行中：修复、C核心回归、真实C/Go握手与ping/双向测速、自有ARM构建/现有系统依赖验证、独立JFFS包；沿用用户已授权的华硕更新与可逆修复范围安装后核验配置不变、boot_id不变、#25→#23 connected与测速往返。TEST-061：节点自定义/覆盖拓扑/默认/缺节点回退及C客户端仅节点表携带随机监听端口的真实互通回归；TEST-062：实机连接状态及测速、打包/安装回读。REQ034→UNIT016/IF015→TASK024→TEST061/062。准备门禁通过，整体历史TASK023其余业务仍待验收；当前唯一下一步：执行C/Go互通回归及ARM构建。

用户继续授权“调试升级路由器插件，测试迭代dns虚拟路由功能”；并明确本机监听端口不在华硕配置、只由主控配置，增加日志显示且不写文件。REQ-035：主控节点表为本机监听及对端拨号端口唯一正常配置来源，旧本地port兼容读取但不决定监听，删除UI输入/保存字段，状态显示实际端口。REQ-036：增加日志标签，最近64条固定内存环、连续相同错误合并、只在查看时读取，stderr沿原有console输出，不写Flash/磁盘、不保存请求凭据或报文。UNIT-017/IF-016：service.h与log.c有界线程安全环，原厂认证下的logs只读动作，记录主控和链路建立/失败/断开、实际对端地址/端口；无新端口/依赖。TEST-063：64条覆盖/顺序/重复合并与logs动作不产生文件、真实页面标签/端口移除/只查看时请求日志。TEST-064：真实路由器DNS直连、fake IP分流及一次实际访问；分别记录链路、DNS、数据面结果。REQ035/036及REQ001/002实机子目标→TASK024继续唯一进行中→TEST061—064。后续未知业务问题按实证迭代，不修改无关节点或主控设置、不凭缓存ready宣称链路成功。

交付记录：upgrade-conclusion.json与final-SHA256-manifest.json在独立备份目录；仓库baseline.lock.json追加minimal_jffs_autostart_upgrade保留旧历史，ignored artifacts/autostart/delivery.json反映实机已刷入/自启验收。REQ033本次“刷入与跨重启自启”子目标完成门禁通过；整体TASK023继续进行中（用户身份及实际跨节点DNS/fakeIP/网关代理联调尚未完成，ONCE缺陷独立保留）。noconsole仍按开发期授权0，不提前关闭调试控制台。当前子任务无待执行升级动作；整体下一步唯一动作：用户填写主控/Node ID/密钥后验证真实业务，不自行启用或填写身份。最终仅提交本轮账本、README、baseline.lock.json，秘密/备份/日志/镜像不入Git。


### 2026-10-17 DNS/虚拟路由联调迭代与JFFS升级

REQ-037/UNIT-018：主控和链路的IPv4解析最多返回4个去重地址，在既有HTTP/TLS连接预算内顺序回退；保留证书、主机名与SPKI校验，不重放已完成TLS的POST。配置HTTP临时失败保留已认证控制流和当前策略，周期重试；真实变化仍按原流程重建。IF-017复用原HTTP/WS/yamux协议，不新增帧、管理端口或依赖。TEST-065：第一地址不可用后真实TLS认证/配置/控制流走第二地址，首次配置503后同一会话仍报告/ACK，后续同步成功。原厂旧libcurl永久RESOLVE项必须显式删除再替换，首轮回归失败后已修正并通过。

REQ-038/UNIT-019：清理每次OpenSSL读写/握手前的线程错误队列，防止证书PEM读取遗留错误被误当作非阻塞发送失败；新增有界TLS错误原因日志。每轮最多读16条TLS记录，已有解密数据不额外睡眠。接收测速按首帧起3秒加500毫秒宽限结算，不等待积压的结束帧；无数据失败，迟到结束帧不覆盖结果。TEST-066覆盖遗留PEM错误/4096字节发送缓冲的C与Go双角色真实互通、假时钟结算及迟到帧；最终实机23上传2.370Mbps/1024000字节/3456ms，下载4.625Mbps/2010112字节/3477ms；25→23 Ping24ms、25→23→24 Ping202ms，均收到目标响应。测速数字仅为当次3秒窗口，不代表设备极限。

DEF-006/REQ-039/UNIT-020：追加AAAA检查发现LAN仍返回上游IPv6，而插件1053直接查询已为空。真实原厂dnsmasq为nobody；通用原子写0600令它在HUP重载时读不到servers-file，保留旧上游。实机只改公开/tmp/resolv.dnsmasq为0644并HUP后A变为30秒fake IP、AAAA为空，证据定位权限问题。platform.c接管/恢复均设置0644，已接管旧0600文件主动修复并HUP；RAM私有备份和身份文件保持0600。TEST-067要求原厂ARM dnsmasq实际降权为65534进行UDP/TCP接管/恢复，并覆盖旧权限自愈、幂等及WAN文件更新。旧测试以root运行掩盖权限缺陷，测试上游也缺TCP监听；改用files NSS与双协议上游。旧QEMU2.11不支持当前WSL内核IFA11导致初始化失败，隔离容器改用本机QEMU10运行原厂ARM ELF，不改产品或原厂二进制。

TEST-061/063/065/066已通过：C核心/64条内存日志/API认证CSRF和密钥脱敏/重启配置/异步映射NAT，真实C-Go Carrier与TLS/WebSocket/yamux控制面；probe_node全包和发布契约测试、probe_controller全包与vet通过。容器结束时的EOF/close1006、无CAP_NET_ADMIN导致套接字缓冲低于请求是明确测试环境输出，非隐藏失败。ARM正式工具链、QEMU核心、原厂动态依赖/真实SSL初始化/BusyBox脚本语法通过，无新增库。原厂页面真实Chrome桌面1366及390视口检查通过：原厂登录、删除本地端口输入和保存载荷、日志仅显示时取数、真实12041事件和网关标签，零脚本错误；手机仍沿用原厂固定宽度横向滚动。

TEST-062/064最终实机验收：JFFS发布9b428dd04774a2c8，包69081字节，SHA256 85c300e53e61b13744040941944e68330a7ffb9faef00950dc3ce88e6bfe38d9，程序103800字节，SHA256 0e304cef18cc105ea171dad429483d26d1a46cd2974fa09a92ad54ec8b0d6388，四载荷136317字节。安装回读、原厂Web认证/菜单、服务PID4174通过；JFFS剩余12872KiB。config.json与升级前私有备份逐字节一致，boot_id仍e53728f3-4a36-4ba8-8b60-c5dfeb30e59a，本轮未重启/刷写系统或改A/B/CFE。原始JFFS恢复包、配置、boot_id与哈希保留在D:/Code/RouterFirmware/router-backups/20261017-005251-jffs-port-logs（不入Git），当前与previous版本可回退。

最终设备DNS实测：电脑Wi-Fi192.168.52.170/ifindex10，example.com的LAN UDP125ms/TCP17ms返回真实A；www.anthropic.com返回198.18.7.31/TTL30，首查4.225秒，AAAA成功空答案，TCP fake IP一致。强制Wi-Fi源接口访问该fake IP，TLS1.3正常校验证书并在1.165秒收到HTTP200。独立主控远程控制台和AWS只读诊断此前确认路径25→23→24、域名和TCP校验均正确、delivered且无错误，未将本机节点9的状态当作华硕。临时抓包/测试程序已随安装移除，插件无文件日志。限制：主控公网某个Cloudflare地址不可达，地址回退可恢复但仍偶发HTTP超时/404及控制流重连；此前实际TCP首连曾等待约12秒，长期稳定性/其他出口未验收，不声称弱网完全解决。真实外网访问已通过，不能因ready或缓存单独宣称成功。

本轮REQ034—039→UNIT016—020/IF015—017→TASK024→TEST061—067。TASK024进入提交发布阶段，唯一下一步：完成权限回归核对、秘密扫描后提交本轮明确源码/测试/文档并推送mapledev，核对功能SHA对应Actions和独立JFFS资产。历史TASK023整体其余业务不因本次DNS成功自动关闭；生产Go Probe/主控未部署或重启。

TEST-067最终通过：QEMU10运行未修改原厂ARM dnsmasq，/proc确认四个UID均65534；真实UDP/TCP接管和恢复成功。JFFS测试的旧0600文件自愈/0644可读/0600私有备份/幂等/WAN更新/恢复及真实菜单挂载回滚均通过。最终设备回读公开文件0644、备份0600，未留下测试程序。秘密扫描30个源码文件通过，git diff --check通过。技能旧预检仍仅因已退役Docker及router-arm64标记失败，沿用CON012/DEC043的当前8产物契约；实际mapledev/origin/GitHub Actions权限和配套发布测试均核实，本轮不恢复旧产物。
