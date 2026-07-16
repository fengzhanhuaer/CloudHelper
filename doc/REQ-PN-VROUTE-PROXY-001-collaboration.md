# 协作文档

- 适用规则: AI协作规则
- 后续工作传递声明: 本文档必须传递给后续阶段与后续角色。
- 需求编号: REQ-PN-VROUTE-PROXY-001
- 需求前缀: REQ-PN-VROUTE-PROXY-001
- 当前阶段: Architect
- 最近更新角色: Architect
- 最近更新时间: 2026-07-16 22:21:00 +08:00
- 工作依据文档: `doc/ai-coding-collaboration.md`、用户需求、不依赖 TUN 的 HTTP/SOCKS5 VRoute 代理、UDP 支持、Fake-IP 复用要求
- 状态: 已完成

## 第1章 Architect章节
- 章节责任角色: Architect
- 状态: 已完成

### 1.1 需求定义
- 状态: 已完成

#### 1.1.1 需求目标
- 在 `probe_node` 新增不依赖 TUN 网卡的本地 HTTP 与 SOCKS5 代理入口。
- 代理复用现有 VRoute 的路由规则、出口节点、路径选择、物理 carrier、失败清理和 Fake-IP 语义。
- HTTP 代理支持普通 HTTP 请求和 TCP CONNECT。
- SOCKS5 支持 TCP CONNECT 与 RFC 1928 UDP ASSOCIATE。
- 允许新增 VRoute 帧主类型和子类型，为 TCP 流与 UDP 数据报提供连接级传输。

#### 1.1.2 需求范围
- REQ-PN-VROUTE-PROXY-001-R1: HTTP 与 SOCKS5 监听器可独立于本地 TUN 开关启动、停止和运行。
- REQ-PN-VROUTE-PROXY-001-R2: 域名、IPv4、IPv6 和 Fake-IP 目标统一执行现有 `direct`、`reject`、`probe_exit` 路由语义。
- REQ-PN-VROUTE-PROXY-001-R3: 新增 VRoute TCP open/result/data/close 帧，保持单连接数据有序并具备背压与关闭语义。
- REQ-PN-VROUTE-PROXY-001-R4: 新增 VRoute UDP request/response/close 帧，SOCKS5 UDP 关联复用出口 UDP socket 并支持响应回传。
- REQ-PN-VROUTE-PROXY-001-R5: Fake-IP 可作为代理目标标识；源节点反查域名与出口，映射缺失时按 Fake-IP 向控制器补取，出口按域名解析真实地址。
- REQ-PN-VROUTE-PROXY-001-R6: 本地设置、API、状态和虚拟路由页面提供启停、HTTP/SOCKS5 监听地址及可选用户名密码配置。
- REQ-PN-VROUTE-PROXY-001-R7: 中间节点只转发代理帧；出口节点始终可服务远端代理请求，不受本机代理入口开关影响。
- REQ-PN-VROUTE-PROXY-001-R8: 提供协议、路由、HTTP、SOCKS5 TCP、SOCKS5 UDP、Fake-IP、关闭和错误路径测试。
- REQ-PN-VROUTE-PROXY-001-R9: Windows 上代理监听成功后启用系统 HTTP/HTTPS/SOCKS 代理，分别指向 HTTP 与 SOCKS5 监听地址；代理关闭或进程退出时恢复启动前设置，监听启动失败不得修改系统代理。

#### 1.1.3 非范围
- 不实现 SOCKS5 BIND。
- 不实现 HTTP CONNECT-UDP、MASQUE 或 HTTP/3 代理入口。
- 不替换现有 TUN 数据面，不改变现有 IP 帧头格式、字段顺序、字段宽度或 checksum 范围。
- 不新增控制器管理页，不修改 Android/mobilecore 代理入口。
- 不在本需求中引入多 writer 并发重排或新的物理 carrier 协议。

#### 1.1.4 验收标准
- AS-01: 关闭或未安装 TUN 时，启用代理后 HTTP CONNECT 与 SOCKS5 CONNECT 可通过本地 direct 或远端 `probe_exit` 完成 TCP 往返。
- AS-02: SOCKS5 UDP ASSOCIATE 可完成至少 DNS/UDP echo 类型的数据报往返，且关联关闭后出口 socket 被释放。
- AS-03: 域名和 Fake-IP 命中同一 VRoute 规则时选择相同出口节点；Fake-IP 映射缺失时有明确补取或失败结果。
- AS-04: 新帧在多跳路径中保持路径方向，源到出口与出口到源使用互为反向的 path。
- AS-05: 单 TCP stream 的 data 帧在 dispatch 分片中保持同一 hash，连接关闭或 carrier 失败后不遗留会话。
- AS-06: 设置保存后监听器按配置重建；绑定失败返回可观测错误，不影响远端出口处理能力。
- AS-07: HTTP/SOCKS5 非回环监听必须配置认证；默认仅监听回环地址。
- AS-08: `go test ./...` 与 `go test -race ./...` 通过，新增定向测试覆盖 R1-R8。
- AS-09: Windows 系统代理应用与恢复具备幂等测试，且默认关闭时不修改系统代理。

#### 1.1.5 风险
- VRoute 单帧数据上限为 65535 字节，TCP/UDP 负载必须分块并预留 stream/datagram 头。
- TCP 数据与关闭帧必须按 stream ID 固定 dispatch shard，避免同连接重排。
- UDP 关联与出口 socket 生命周期不一致可能造成泄漏，必须有控制连接关闭与空闲超时双重清理。
- HTTP Transport 连接复用可能延长 VRoute stream 生命周期，必须正确实现 `net.Conn` deadline 与关闭行为。
- Fake-IP 映射补取依赖控制器，控制器不可用时必须快速失败而不是回落为 Fake-IP 直连。

#### 1.1.6 遗留事项
- 无。

#### 1.1.7 结论
- 需求边界完整，可进入架构和单元设计。

### 1.2 总体架构
- 状态: 已完成

#### 1.2.1 架构目标
- 将应用层代理入口与 TUN/IP 数据面解耦，同时复用 VRoute 控制面与 carrier 数据面。
- 以连接级 TCP stream 和关联级 UDP datagram 协议承载应用流量，不生成虚拟 IP 包。
- 保持 direct、reject、probe_exit 和 Fake-IP 的现有所有权语义。

#### 1.2.2 总体设计
- 本地入口层包含 HTTP Server 与 SOCKS5 Server；两者调用统一 `DialTCP` 与 `RelayUDP` 接口。
- 路由决策层规范化目标地址。普通域名/IP读取现有 route rule；Fake-IP 先反查或补取映射，再用域名和有效出口决策。
- `direct` 在本机使用现有 egress/direct-bypass 拨号；`reject` 直接返回协议错误；`probe_exit` 计算现有 VRoute 最佳路径。
- TCP 远端出口建立 source session 与 exit session，使用新 VRoute proxy 主类型的 open/result/data/close 子类型双向传输。
- UDP 使用 SOCKS5 TCP 控制连接建立 association；数据报按目标单独决策路径，出口按 association+target 复用 UDP socket，响应沿反向路径返回。
- 中间节点根据 frame control path 转发，不解析业务目标；最终节点负责拨号或交付本地入口。
- 代理入口运行时只受本地代理设置控制；VRoute proxy 帧处理器始终注册，保证节点可作为远端出口。

#### 1.2.3 关键模块
| 模块编号 | 模块名称 | 职责 | 输入 | 输出 |
|---|---|---|---|---|
| M-01 | Proxy Settings | 持久化、校验和发布代理入口配置 | 本地 API 配置 | 标准化设置 |
| M-02 | Proxy Listener Runtime | 管理 HTTP/SOCKS5 TCP 与 SOCKS5 UDP listener | 标准化设置 | listener 状态、接入连接 |
| M-03 | Proxy Route Decision | 复用 route rule、path 与 Fake-IP 所有权 | target host:port | direct/reject/probe_exit 决策 |
| M-04 | VRoute TCP Stream | 管理 stream open/result/data/close 与 `net.Conn` 适配 | TCP 字节流 | VRoute 帧、出口 TCP conn |
| M-05 | VRoute UDP Datagram | 管理 association、出口 UDP socket 与 request/response/close | SOCKS5 UDP datagram | VRoute 帧、UDP 响应 |
| M-06 | Frame Integration | 注册新主类型、分片 hash、路径转发和最终节点处理 | proxy frame | 转发或本地处理 |
| M-07 | Local Console Projection | 设置 API、状态 API 与页面控件 | settings/runtime snapshot | JSON 与 UI |
| M-08 | Windows System Proxy | 保存、应用和恢复 WinINet HTTP/HTTPS/SOCKS 系统代理 | proxy runtime state | Windows proxy state |

#### 1.2.4 关键接口
| 接口编号 | 接口名称 | 调用方 | 提供方 | 说明 |
|---|---|---|---|---|
| IF-01 | DecideProxyTarget | HTTP/SOCKS5 | Proxy Route Decision | 规范化域名/IP/Fake-IP并返回动作、出口和 path |
| IF-02 | DialProxyTCP | HTTP/SOCKS5 | VRoute TCP Stream | 返回不依赖 TUN 的 `net.Conn` |
| IF-03 | SendProxyFrame | TCP/UDP session | Frame Integration | 沿指定 path 发送新代理帧 |
| IF-04 | HandleProxyFrame | VRoute frame handler | TCP/UDP session | 中转或最终处理代理帧 |
| IF-05 | RelayProxyUDP | SOCKS5 UDP handler | VRoute UDP Datagram | 发送目标数据报并异步回传响应 |
| IF-06 | ReconcileProxyRuntime | startup/settings API | Proxy Listener Runtime | 启停或重建本地监听器 |
| IF-07 | SnapshotProxyRuntime | local status API/UI | Proxy Listener Runtime | 输出监听、会话、流量和错误状态 |
| IF-08 | ResolveProxyFakeIP | Proxy Route Decision | Existing Fake-IP store/controller client | 反查或同步补取 Fake-IP 条目 |
| IF-09 | ReconcileSystemProxy | Proxy Listener Runtime | Windows System Proxy | 监听成功状态与 HTTP/SOCKS5 地址驱动系统代理应用/恢复 |

#### 1.2.5 关键约束
- 不修改现有 VRoute frame envelope；只增加新的 `MainType` 与独立 payload 编码。
- TCP data payload 使用固定 stream ID 头，最大 chunk 小于 frame data 上限。
- UDP payload携带 association ID、目标地址和 data；禁止 SOCKS5 分片 `FRAG != 0`。
- source 与 exit 会话必须有唯一 ID、并发安全关闭、超时和状态计数。
- 非回环监听必须启用用户名密码认证，避免开放代理。
- 所有非 C/C++ 文件可按 `encoding_tools/README.md` 直接编辑；本需求禁止修改 C/C++ 文件。

#### 1.2.6 风险
- 外部 SOCKS5 库的 listener 生命周期不满足本项目运行时重建时，应只复用其 RFC 报文解析与响应结构，由本项目管理 socket。
- HTTP 普通请求与 CONNECT 的连接复用语义不同，需要分别测试。
- carrier 中断时主动 stream 可能无新写入，必须由 carrier 断开钩子清理受影响 path 会话。

#### 1.2.7 结论
- 架构不依赖 TUN，且对现有 VRoute envelope 与 IP 数据面保持兼容。

### 1.3 单元设计
- 状态: 已完成

#### 1.3.1 单元清单
| 单元编号 | 单元名称 | 所属模块 | 职责 | 输入 | 输出 |
|---|---|---|---|---|---|
| U-01 | ProxySettingsStore | M-01 | 加载、校验、保存代理设置 | JSON/settings | settings |
| U-02 | ProxyRuntimeManager | M-02 | 原子重建 listener 和维护状态 | settings | runtime |
| U-03 | HTTPProxyHandler | M-02 | 普通 HTTP 与 CONNECT 接入 | HTTP request | TCP proxy flow |
| U-04 | SOCKS5ProxyHandler | M-02 | CONNECT 与 UDP ASSOCIATE 接入 | SOCKS5 request/datagram | TCP/UDP proxy flow |
| U-05 | ProxyTargetResolver | M-03 | route rule、Fake-IP、path 决策 | host:port | target decision |
| U-06 | ProxyFrameCodec | M-06 | open/result/data/close/UDP payload 编解码与 hash | typed payload | bytes/session ID |
| U-07 | ProxyTCPRegistry | M-04 | source/exit stream 注册、拨号、pipe、关闭 | frame/conn | stream state |
| U-08 | ProxyUDPRegistry | M-05 | source association、exit socket、超时与回传 | datagram/path | UDP state |
| U-09 | ProxyStatusProjection | M-07 | 设置与运行状态投影 | runtime snapshot | API/UI payload |
| U-10 | WindowsSystemProxyManager | M-08 | 保存、应用、广播和恢复系统代理设置 | enabled/http/socks5 listen | system proxy state/error |

#### 1.3.2 单元设计
##### U-01
- 单元名称: ProxySettingsStore
- 职责: 在现有虚拟路由本地设置中增加 proxy 字段并保持旧文件兼容。
- 输入: enable、HTTP/SOCKS5 listen、username、password。
- 输出: 标准化设置或校验错误。
- 处理规则: 默认关闭；默认监听 `127.0.0.1:18080` 与 `127.0.0.1:18081`。
- 异常规则: 非回环无认证、地址无效、HTTP/SOCKS5 地址冲突时拒绝保存。

##### U-02/U-03/U-04
- 单元名称: ProxyListenerUnits
- 职责: 项目自主管理 listener，复用成熟 SOCKS5 库的 RFC 1928 解析结构。
- 输入: 标准化设置与客户端连接。
- 输出: HTTP/SOCKS5 响应、TCP conn 或 UDP association。
- 处理规则: 设置变更先成功创建新 listener 再替换旧 runtime；入口关闭不影响出口帧处理。
- 异常规则: 绑定失败保留旧 runtime 并返回错误；协议错误返回标准状态码。

##### U-05
- 单元名称: ProxyTargetResolver
- 职责: 保持 VRoute 规则优先级和 Fake-IP 所有权。
- 输入: host:port。
- 输出: direct/reject/probe_exit、规范化 target、exit node、path。
- 处理规则: Fake-IP 不允许直接拨号；映射补取后按域名规则与条目出口决策。
- 异常规则: 规则拒绝、映射缺失、出口缺失、path 不可用均返回明确错误。

##### U-06/U-07
- 单元名称: ProxyTCPProtocolUnits
- 职责: 编码帧并维护有序全双工 TCP stream。
- 输入: open metadata、stream bytes、close reason。
- 输出: VRoute frame 与 `net.Conn`。
- 处理规则: source 等待 open result 后才交付连接；data 固定按 stream ID hash；每帧数据不超过 32 KiB。
- 异常规则: 拨号失败发送 result error；帧发送失败、carrier 断开或任一端关闭时幂等清理。

##### U-08
- 单元名称: ProxyUDPRegistry
- 职责: SOCKS5 association 与出口 UDP socket 生命周期管理。
- 输入: association ID、target、payload、path。
- 输出: UDP request/response/close frame。
- 处理规则: association+target 复用 socket；响应记录真实 remote addr；空闲 2 分钟清理。
- 异常规则: 不支持分片；无 association 的响应丢弃并计数；关闭帧清理同 association 全部出口 socket。

##### U-09
- 单元名称: ProxyStatusProjection
- 职责: 输出运行状态与流量计数并提供页面配置。
- 输入: runtime/session registries。
- 输出: settings/status JSON 与页面状态。
- 处理规则: 明确区分入口启用、HTTP/SOCKS5 listener、source sessions、exit sessions、UDP associations。
- 异常规则: 最近错误保留时间与文本，避免仅显示未运行。

##### U-10
- 单元名称: WindowsSystemProxyManager
- 职责: 在监听成功后设置当前用户 WinINet HTTP/HTTPS/SOCKS 代理，并在关闭时恢复首次接管前状态。
- 输入: proxy enabled、HTTP 与 SOCKS5 listener address。
- 输出: applied/restored 状态或错误。
- 处理规则: 默认关闭不写注册表；重复应用幂等；只在 listener 成功后接管；普通进程写当前用户，LocalSystem 服务写活动控制台用户 SID 对应的 `HKEY_USERS`；进程正常退出恢复原设置。
- 异常规则: 设置失败时关闭新代理 runtime 并恢复旧 runtime/系统代理状态；测试通过 hook 隔离真实注册表。

#### 1.3.3 风险
- SOCKS5 UDP 客户端可能不使用 TCP 源端口作为 UDP 源端口，association 必须支持同源 IP 首包绑定。
- UDP 目标可能返回多个 remote endpoint，响应帧必须携带实际来源地址。

#### 1.3.4 结论
- 单元边界覆盖 TCP、UDP、Fake-IP、配置、状态与生命周期。

### 1.4 Code任务执行包
- 状态: 已完成

#### 1.4.1 执行边界
- 允许修改: `doc/REQ-PN-VROUTE-PROXY-001-collaboration.md`、`probe_node/go.mod`、`probe_node/go.sum`、`probe_node/main.go`、`probe_node/probe_virtual_router.go`、`probe_node/probe_virtual_router_settings.go`、`probe_node/local_console.go`、`probe_node/local_pages/virtual_router.html`、`probe_node/probe_vroute_proxy*.go`、`probe_node/*proxy*_test.go`、`probe_node/probe_virtual_router_test.go`、`probe_node/local_console_test.go`。
- 禁止修改: VRoute envelope 现有字段及二进制布局、TUN 数据面、controller、Android/mobilecore、C/C++ 文件、与本需求无关的模块。

#### 1.4.2 任务清单
| 任务编号 | 需求编号 | 单元编号 | 文件范围 | 操作类型 | 验收标准 |
|---|---|---|---|---|---|
| T-01 | REQ-PN-VROUTE-PROXY-001-R3,R4,R7 | U-06 | `probe_node/probe_virtual_router.go`、`probe_node/probe_vroute_proxy_protocol.go` | 修改、新增 | 新主类型、子类型、codec、hash、转发和最终处理测试通过 |
| T-02 | REQ-PN-VROUTE-PROXY-001-R2,R5 | U-05 | `probe_node/probe_vroute_proxy_route.go` | 新增 | domain/IP/Fake-IP direct/reject/probe_exit 决策测试通过 |
| T-03 | REQ-PN-VROUTE-PROXY-001-R3,R7 | U-07 | `probe_node/probe_vroute_proxy_tcp.go`、`probe_node/probe_virtual_router.go` | 新增、修改 | TCP source/exit 往返、失败和清理测试通过 |
| T-04 | REQ-PN-VROUTE-PROXY-001-R4,R7 | U-08 | `probe_node/probe_vroute_proxy_udp.go` | 新增 | UDP request/response、复用、关闭与超时测试通过 |
| T-05 | REQ-PN-VROUTE-PROXY-001-R1,R6 | U-01,U-02,U-03,U-04 | `probe_node/probe_vroute_proxy.go`、`probe_node/probe_virtual_router_settings.go`、`probe_node/main.go`、`probe_node/go.mod`、`probe_node/go.sum` | 新增、修改 | 无 TUN HTTP/SOCKS5 TCP/UDP listener 可启动并完成协议测试 |
| T-06 | REQ-PN-VROUTE-PROXY-001-R6 | U-09 | `probe_node/local_console.go`、`probe_node/local_pages/virtual_router.html`、`probe_node/local_console_test.go` | 修改 | 设置、状态 API 与页面配置可用，旧设置兼容 |
| T-07 | REQ-PN-VROUTE-PROXY-001-R1-R8 | U-01-U-09 | `probe_node/*proxy*_test.go`、`probe_node/probe_virtual_router_test.go`、`doc/REQ-PN-VROUTE-PROXY-001-collaboration.md` | 新增、修改 | 普通测试、race 测试和需求矩阵证据完整 |
| T-08 | REQ-PN-VROUTE-PROXY-001-R9 | U-02,U-10 | `probe_node/probe_vroute_proxy.go`、`probe_node/probe_vroute_proxy_system_windows.go`、`probe_node/probe_vroute_proxy_system_other.go`、`probe_node/probe_vroute_proxy_test.go` | 新增、修改 | listener 成功后应用系统代理，关闭/退出恢复，失败回滚测试通过 |

#### 1.4.3 源码修改规则
- 必须使用 encoding_tools/README.md 描述的接口。
- 对 C/C++ 源代码（`.c`、`.cc`、`.cpp`、`.cxx`、`.h`、`.hpp`）必须使用 encoding_tools/encoding_safe_patch.py。
- 对非 C/C++ 源代码可直接编辑，不强制使用 encoding_tools/encoding_safe_patch.py。
- encoding_tools/ 不可用或执行失败时，Code 必须记录失败命令、错误摘要、影响文件与阻塞影响，并提交第2.6节 `Code任务反馈`。
- 替代 encoding_tools/ 修改受控 C/C++ 源代码前，必须取得 Architect 明确允许。
- 本任务仅修改 Go、HTML、Markdown 与 Go module 文件，按 `encoding_tools/README.md` 允许直接编辑。

#### 1.4.4 交付物
- 一份持续更新的本协作文档。
- HTTP/SOCKS5 TCP+UDP 无 TUN 代理实现。
- VRoute proxy 帧协议、路由与会话实现。
- 设置、状态、页面与测试。

#### 1.4.5 门禁输入
- `encoding_tools/README.md` 已读取，允许非 C/C++ 文件直接编辑。
- 文件范围、操作类型、接口和可测试验收标准已定义。
- Code 必须记录依赖版本、测试命令、失败项、风险和回滚方案。

#### 1.4.6 结论
- Code任务执行包完整，可以放行 Code 阶段。

### 1.5 Architect需求跟踪矩阵
- 状态: 已完成

| 需求编号 | 需求描述 | 架构章节 | 单元设计章节 | Code任务章节 | 状态 | 备注 |
|---|---|---|---|---|---|---|
| REQ-PN-VROUTE-PROXY-001-R1 | 无 TUN HTTP/SOCKS5 入口 | 1.2 | U-01-U-04 | T-05,T-07 | 已完成 | 普通与race测试通过 |
| REQ-PN-VROUTE-PROXY-001-R2 | 复用 VRoute route rule/path | 1.2 | U-05 | T-02,T-07 | 已完成 | 路由决策测试通过 |
| REQ-PN-VROUTE-PROXY-001-R3 | TCP stream 帧 | 1.2 | U-06,U-07 | T-01,T-03,T-07 | 已完成 | codec、流与关闭测试通过 |
| REQ-PN-VROUTE-PROXY-001-R4 | UDP ASSOCIATE 与 datagram 帧 | 1.2 | U-06,U-08 | T-01,T-04,T-05,T-07 | 已完成 | SOCKS5 UDP往返通过 |
| REQ-PN-VROUTE-PROXY-001-R5 | Fake-IP 复用和补取 | 1.2 | U-05 | T-02,T-07 | 已完成 | Fake-IP命中与补取测试通过 |
| REQ-PN-VROUTE-PROXY-001-R6 | 设置、状态和页面 | 1.2 | U-01,U-02,U-09 | T-05,T-06,T-07 | 已完成 | API与页面测试通过 |
| REQ-PN-VROUTE-PROXY-001-R7 | 中间转发与出口独立性 | 1.2 | U-06-U-08 | T-01,T-03,T-04,T-07 | 已完成 | frame路径与断链清理覆盖 |
| REQ-PN-VROUTE-PROXY-001-R8 | 完整测试 | 1.2 | U-01-U-09 | T-07 | 已完成 | 普通全套与完整race通过 |
| REQ-PN-VROUTE-PROXY-001-R9 | Windows 系统代理接管与恢复 | 1.2 | U-02,U-10 | T-08 | 已完成 | 活动用户SID与登录后恢复已覆盖 |

### 1.6 Architect关键接口跟踪矩阵
- 状态: 已完成

| 接口编号 | 需求编号 | 接口名称 | 调用方 | 提供方 | 输入 | 输出 | 状态 | 备注 |
|---|---|---|---|---|---|---|---|---|
| IF-01 | REQ-PN-VROUTE-PROXY-001-R2,R5 | DecideProxyTarget | HTTP/SOCKS5 | U-05 | target | decision | 已完成 | 测试通过 |
| IF-02 | REQ-PN-VROUTE-PROXY-001-R1,R3 | DialProxyTCP | U-03,U-04 | U-07 | target | net.Conn | 已完成 | 测试通过 |
| IF-03 | REQ-PN-VROUTE-PROXY-001-R3,R4 | SendProxyFrame | U-07,U-08 | U-06 | frame/path | error | 已完成 | 测试通过 |
| IF-04 | REQ-PN-VROUTE-PROXY-001-R3,R4,R7 | HandleProxyFrame | VRoute handler | U-06-U-08 | frame | error | 已完成 | 测试通过 |
| IF-05 | REQ-PN-VROUTE-PROXY-001-R4 | RelayProxyUDP | U-04 | U-08 | datagram | async response | 已完成 | 测试通过 |
| IF-06 | REQ-PN-VROUTE-PROXY-001-R1,R6 | ReconcileProxyRuntime | startup/API | U-02 | settings | runtime/error | 已完成 | 启停、回滚、恢复测试通过 |
| IF-07 | REQ-PN-VROUTE-PROXY-001-R6 | SnapshotProxyRuntime | API/UI | U-02,U-09 | none | snapshot | 已完成 | API页面测试通过 |
| IF-08 | REQ-PN-VROUTE-PROXY-001-R5 | ResolveProxyFakeIP | U-05 | existing controller client | fake IP | entry/error | 已完成 | 测试通过 |
| IF-09 | REQ-PN-VROUTE-PROXY-001-R9 | ReconcileSystemProxy | U-02 | U-10 | runtime/http listen | applied/restored/error | 已完成 | Windows格式与恢复测试通过 |

### 1.7 门禁裁判
- 状态: 已放行

#### 1.7.1 门禁输入
| 文档 | 路径 | 状态 |
|---|---|---|
| 协作文档 | `doc/REQ-PN-VROUTE-PROXY-001-collaboration.md` | 已完成 |

#### 1.7.2 裁判检查
| 检查项 | 结果 | 证据 | 备注 |
|---|---|---|---|
| 协作文档存在 | 通过 | 本文档 | 无 |
| Architect章节存在 | 通过 | 第1章 | 无 |
| Code章节存在 | 通过 | 第2章 | 实现与证据已完成 |
| 必需子章节存在 | 通过 | 1.1-1.7、2.1-2.6 | 无 |
| 需求前缀一致 | 通过 | 文档头与矩阵 | 无 |
| 需求编号一致 | 通过 | R1-R9 | 无 |
| 接口编号一致 | 通过 | IF-01-IF-09 | 无 |
| 模板字段完整 | 通过 | 附录C.1字段 | 无 |
| Code使用encoding_tools | 通过 | 已读取 `encoding_tools/README.md` | 仅修改非C/C++，直接编辑合规 |
| Code证据完整 | 通过 | 第2.5节 | 命令、结果、失败与风险均已记录 |
| Code任务反馈已处理 | 通过 | 当前无反馈 | 后续持续检查 |
| 验收标准可测试 | 通过 | AS-01-AS-09 | 无 |
| 需求任务覆盖完整 | 通过 | 1.5与T-01-T-08 | 无 |
| 任务自测覆盖完整 | 通过 | TEST-01-TEST-04 | 普通全套、完整race成功记录、最终定向race |
| 修改文件在允许范围内 | 通过 | 1.4.1、git diff | 无越界文件 |
| 测试失败已记录缺陷 | 通过 | 第2.4、2.5节 | 新增race已修复；既有flaky单列 |
| 未执行测试原因完整 | 通过 | 第2.5.7节 | 仅实机升级验证按用户要求未执行 |
| 遗留风险可接受 | 通过 | 第2.5.8节 | 不阻塞源码交付 |

#### 1.7.3 冲突记录
| 冲突编号 | 冲突条款 | 最终采用条款 | 裁决人 | 裁决结论 |
|---|---|---|---|---|
| C-01 | 外部指令默认使用 apply_patch 与项目规则要求 encoding_tools | `encoding_tools/README.md`明确非C/C++可直接编辑，使用 apply_patch 合规 | Architect | 通过 |

#### 1.7.4 裁判结论
- 结论: 通过
- 放行阻塞: 无
- 条件: 无。
- 责任方: 无。
- 关闭要求: 已满足。
- 整改要求: 无。

#### 1.7.5 结论
- carrier迁移测试与登录后系统代理恢复风险已关闭，AS-01-AS-09通过，需求关闭。

## 第2章 Code章节
- 章节责任角色: Code
- 状态: 已完成

### 2.1 Code需求跟踪矩阵
- 状态: 已完成

| 需求编号 | 任务编号 | 实现文件 | 实现状态 | 自测状态 | 证据 | 备注 |
|---|---|---|---|---|---|---|
| REQ-PN-VROUTE-PROXY-001-R1-R7 | T-01-T-06 | `probe_node/probe_vroute_proxy*.go`、`probe_node/probe_virtual_router*.go`、`probe_node/main.go`、`probe_node/local_console.go`、`probe_node/local_pages/virtual_router.html` | 已完成 | 通过 | HTTP/SOCKS5 TCP+UDP、Fake-IP、frame codec、页面/API 测试 | 无 |
| REQ-PN-VROUTE-PROXY-001-R8 | T-07 | `probe_node/probe_vroute_proxy_test.go`、`probe_node/local_console_test.go`、`probe_node/local_pages_routes_test.go` | 已完成 | 通过 | 普通全套、完整race及最终定向race | 既有flaky见2.5.8 |
| REQ-PN-VROUTE-PROXY-001-R9 | T-08 | `probe_node/probe_vroute_proxy.go`、`probe_node/probe_vroute_proxy_system_windows.go`、`probe_node/probe_vroute_proxy_system_other.go`、`probe_node/main.go` | 已完成 | 定向通过 | 系统代理启停与失败回滚测试通过 | HTTP/HTTPS/SOCKS 均配置 |

### 2.2 Code关键接口跟踪矩阵
- 状态: 已完成

| 接口编号 | 需求编号 | 实现文件 | 调用方 | 提供方 | 实现状态 | 证据 | 备注 |
|---|---|---|---|---|---|---|---|
| IF-01-IF-08 | REQ-PN-VROUTE-PROXY-001-R1-R8 | `probe_node/probe_vroute_proxy*.go`、`probe_node/probe_virtual_router.go` | HTTP/SOCKS5/VRoute handler | route decision、TCP/UDP registry、frame integration | 已完成 | 定向与race测试通过 | 无 |
| IF-09 | REQ-PN-VROUTE-PROXY-001-R9 | `probe_node/probe_vroute_proxy.go`、`probe_node/probe_vroute_proxy_system_windows.go` | ProxyRuntimeManager | WindowsSystemProxyManager | 已完成 | 启停、失败回滚、SOCKS5格式测试通过 | 实机由用户升级后验证 |

### 2.3 Code测试项跟踪矩阵
- 状态: 已完成

| 测试项编号 | 需求编号 | 任务编号 | 测试目标 | 测试方法 | 结果 | 证据 | 未执行原因 | 备注 |
|---|---|---|---|---|---|---|---|---|
| TEST-01 | R2-R5 | T-01-T-04 | codec、hash、Fake-IP、TCP frame | Go 定向单测 | 通过 | `TestProbeVRouteProxyFrameCodecsAndDispatchHash` 等 | 无 | 无 |
| TEST-02 | R1,R4,R6 | T-05,T-06 | 无 TUN HTTP/SOCKS5 TCP/UDP 与页面/API | 本地 listener 端到端测试 | 通过 | `TestProbeVRouteProxyListenersWorkWithoutTUN` 等 | 无 | 无 |
| TEST-03 | R9 | T-08 | Windows 系统代理启停与失败回滚 | hook 隔离单测 | 通过 | `TestReconcileProbeVRouteProxyRuntime*` | 无 | 无 |
| TEST-04 | R1-R9 | T-07 | 全包与 race 回归 | `go test` / `go test -race` | 通过 | `go test ./...` 通过；`go test -race ./...` 于21:59通过 | 无 | 后续短超时复跑暴露既有flaky，见2.5.8 |

### 2.4 Code缺陷跟踪矩阵
- 状态: 已完成

| 缺陷编号 | 需求编号 | 测试项编号 | 缺陷描述 | 严重级别 | 修复状态 | 修复证据 | 备注 |
|---|---|---|---|---|---|---|---|
| DEF-01 | R3,R7 | TEST-01 | frame sender 直接函数变量形成 Go 初始化环 | 高 | 已修复 | 默认 sender 改为运行时回退，定向测试通过 | 无行为变化 |
| DEF-02 | R2,R5 | TEST-01 | 路由/Fake-IP 测试夹具未满足现有 Name 与 controller state 约束 | 低 | 已修复 | 修正夹具后通过 | 生产逻辑未改 |
| DEF-03 | R4 | TEST-02 | Windows UDP 小读取缓冲无法容纳 SOCKS5 datagram 头 | 低 | 已修复 | UDP 测试使用完整 datagram 缓冲后通过 | 生产协议正常 |
| DEF-04 | R3 | TEST-04 | 测试恢复 sender hook 时后台 close 仍可能读取，触发race | 中 | 已修复 | 显式等待 TCP Close 帧；连续20次及完整race通过 | 仅测试同步问题 |
| DEF-05 | R7 | TEST-04 | carrier迁移测试在清缓冲契约下依赖调度且读取无deadline | 中 | 已修复 | 先等待旧carrier脱离、attach后入队、2秒deadline；race连续100次通过 | 未改生产清缓冲语义 |
| DEF-06 | R9 | TEST-03 | LocalSystem服务早于用户登录时系统代理首次设置失败且不会恢复 | 高 | 已修复 | 15秒恢复循环与失败后恢复测试普通/race各20次通过 | 登录后无需重启服务 |

### 2.5 Code执行证据
- 状态: 已完成

#### 2.5.1 修改接口
- 新增 VRoute proxy MainType 8 及 TCP/UDP 子类型；新增统一 TCP dial、UDP relay、Fake-IP resolve、runtime reconcile 与 system proxy 接口。

#### 2.5.2 配置文件
- `probe_virtual_router_settings.json` 增加默认关闭的 proxy enable、HTTP/SOCKS5 listen 与可选认证字段，旧文件缺字段保持关闭。

#### 2.5.3 执行报告
- T-01-T-08 已实现并完成验证；未修改或部署 `C:\Tools\probe_node` 运行库。

#### 2.5.4 影响文件
- 影响范围限定在1.4.1；未修改 C/C++、controller、Android/mobilecore 或既有 frame envelope。

#### 2.5.5 测试命令
- `go test . -run 'Test(ReconcileProbeVRouteProxy|ProbeVRouteProxy|DecideProbeVRouteProxy|ResolveProbeVRouteProxy|ProbeLocalVirtualRouterSettingsHandlerConfiguresProxy|ProbeLocalStandalonePagesServedAfterLogin)' -count=1 -timeout 180s`。
- `go test ./... -count=1 -timeout 60s`。
- `$env:PATH='C:\msys64\ucrt64\bin;' + $env:PATH; go test -race ./... -count=1 -timeout 5m`。
- `$env:PATH='C:\msys64\ucrt64\bin;' + $env:PATH; go test -race . -run 'Test(ProbeVRoute|ReconcileProbeVRoute|DecideProbeVRoute|ResolveProbeVRoute|ProbeLocalVirtualRouterSettingsHandlerConfiguresProxy|ProbeLocalStandalonePagesServedAfterLogin)' -count=1 -timeout 3m`。
- `go test . -run 'Test(RecoverProbeVRouteProxy|ReconcileProbeVRouteProxy|ProbeVirtualRouterFrameLinkTXWorkerSurvivesCarrierMigration)' -count=20 -timeout 3m`。
- `$env:PATH='C:\msys64\ucrt64\bin;' + $env:PATH; go test -race . -run 'Test(RecoverProbeVRouteProxy|ReconcileProbeVRouteProxy|ProbeVirtualRouterFrameLinkTXWorkerSurvivesCarrierMigration)' -count=20 -timeout 3m`。
- 最终：`go test ./... -count=1 -timeout 60s` 与 `go test -race ./... -count=1 -timeout 60s`。

#### 2.5.6 自测结果
- 上述定向测试通过；HTTP 普通代理、HTTP CONNECT、SOCKS5 TCP、SOCKS5 UDP 均完成本机 echo 往返且 TUN 未运行。
- `go test ./...` 最终通过；完整 `go test -race ./...` 曾通过（主包17.344s、mobilecore 2.144s）；最终改动后的代理相关race通过（2.638s）。
- 风险修复后最终普通全套通过（主包16.970s、mobilecore 1.064s），完整race通过（主包17.891s、mobilecore 2.294s）。

#### 2.5.7 未执行测试原因
- 按用户要求未替换、停止或重启 `C:\Tools\probe_node`，因此未执行真实服务账号注册表与 Chrome 访问验证。

#### 2.5.8 遗留风险
- 无已知源码或测试阻塞风险。真实注册表与 Chrome 集成按用户要求留待自行升级后验证。

#### 2.5.9 回滚方案
- 关闭代理开关会停止入口并恢复首次接管前 Windows 系统代理；代码回滚可删除新增 proxy 文件与 frame type，并移除 settings/API/UI 字段。

#### 2.5.10 结论
- Code任务与风险修复完成，Architect最终门禁通过。

### 2.6 Code任务反馈
- 状态: 已完成

| 反馈编号 | 任务编号 | 反馈类型 | 反馈描述 | 阻塞影响 | Code建议 | Architect处理状态 | Architect处理结论 |
|---|---|---|---|---|---|---|---|
| 无 | T-01-T-08 | 无 | 无 | 无 | 无 | 无需处理 | 无 |

#### 2.6.1 结论
- 当前无反馈。
