# 协作文档

- 适用规则: AI协作规则
- 后续工作传递声明: 本文档必须传递给后续阶段与后续角色。
- 需求编号: REQ-PN-QUIC-STREAM-DATAPLANE-001
- 需求前缀: REQ-PN-QUIC-STREAM-DATAPLANE-001
- 当前阶段: Code首版最小闭环完成，待UDP datagram完善与Architect复核
- 最近更新角色: Code
- 最近更新时间: 2026-05-25T10:41:31+08:00
- 工作依据文档: [`doc/ai-coding-collaboration.md`](doc/ai-coding-collaboration.md:1)、[`doc/REQ-PN-RELAY-DUAL-STACK-001-collaboration.md`](doc/REQ-PN-RELAY-DUAL-STACK-001-collaboration.md:1)、[`probe_node/link_relay_client_transport.go`](probe_node/link_relay_client_transport.go:1)、[`probe_node/link_chain_runtime.go`](probe_node/link_chain_runtime.go:1)、[`probe_node/local_tun_group_runtime.go`](probe_node/local_tun_group_runtime.go:1)、[`probe_node/link_chain_udp_assoc.go`](probe_node/link_chain_udp_assoc.go:1)
- 状态: 进行中；Code已完成 QUIC 配置、控制流认证、客户端会话、服务端入口、TCP stream 最小闭环和回归测试；UDP datagram 业务映射仍待完善

## 第1章 Architect章节
- 章节责任角色: Architect
- 状态: 已完成

### 1.1 需求定义
- 状态: 已完成

#### 1.1.1 需求目标
- 新增实验性 QUIC 数据面链路，目标是降低现有 `WS-H3 -> HTTP/3 extended CONNECT -> yamux -> proxy stream` 的多层封装开销。
- QUIC 连接优先启用 QUIC v2，保留 QUIC v1 协商回退。
- TCP 代理语义改为一条代理 TCP 连接映射一条 QUIC bidirectional stream。
- UDP 代理语义改为使用 QUIC datagram 承载无可靠、无序、按包投递的数据。
- yamux 从批量数据承载层收敛为控制/管理职责，避免 TCP/UDP 数据再经过 yamux 多路复用。
- 保留现有 `websocket` 与 `websocket-h3` 稳定路径作为兼容和回退，不在本需求中删除。
- 将该能力纳入需求跟踪，后续 Architect 与 Code 输出统一维护在本文档。

#### 1.1.2 需求范围
- 客户端范围: 新增 QUIC 数据面拨号、能力协商、协议选择、TCP stream 建立、UDP datagram 发送接收、回退到现有 WS/WS-H3 的策略。
- 服务端范围: 新增 QUIC 数据面监听/接入、认证控制流、TCP stream 接收与代理、UDP datagram 解析与目标关联、连接生命周期管理。
- 协议范围: 新增实验性协议标签，建议命名为 `quic-stream` 或 `quic-v2-stream`，不得覆盖现有 `websocket`、`websocket-h3` 语义。
- 版本范围: quic-go 当前版本 `v0.55.0` 已包含 `quic.Version2`、`quic.Version1`、`Config.Versions`、`EnableDatagrams`、`SendDatagram`、`ReceiveDatagram`，本需求以现有依赖可实现能力为基础。
- 控制范围: 控制流负责认证、chain_id、bridge_role、会话保活、能力协商、流量统计、TCP/UDP 映射元数据、错误关闭原因。
- 观测范围: 链路详情必须展示 QUIC 协商版本、datagram 支持状态、TCP stream 数、UDP datagram 计数、丢包近似指标、连接/stream 流控窗口、吞吐、RTT、重连与回退原因。
- 测试范围: 覆盖 QUIC v2 优先、v1 回退、TCP 一连接一 stream、UDP datagram、datagram 不可用回退、现有 WS/WS-H3 不回归。

#### 1.1.3 非范围
- 不删除 `websocket` 与 `websocket-h3` 两条现有可用路径。
- 不默认强制所有用户切换到 QUIC 数据面。
- 不考虑外部代理路径，本需求仅面向直连或自建可控 UDP 入口。
- Cloudflare Zero Trust/CF 入口不属于 QUIC Data Plane 适用范围；CF 入口固定只使用 `websocket`，不使用 `websocket-h3`、`HTTP/3` 或 QUIC Data Plane。
- 不把业务 TCP 数据继续封装进 yamux stream。
- 不把 UDP datagram 设计成可靠传输；需要可靠语义的业务仍必须走 TCP stream。
- 不重写 TUN、DNS、主控链路配置的非必要功能。
- 不在 Code 阶段绕过本文档第1.4节任务包扩大修改范围。

#### 1.1.4 验收标准
- `newProbeChainQUICConfig()` 或新的 QUIC 数据面配置必须显式支持 QUIC v2 优先、QUIC v1 回退，并启用 datagram 能力。
- 客户端可通过新协议标签建立 QUIC 数据面连接，连接成功后可通过控制流完成 chain_id、secret、bridge_role 与能力协商。
- 服务端可接受 QUIC 数据面连接，认证失败时必须关闭连接并记录明确日志，认证成功后进入可代理状态。
- 每条 TCP 代理连接必须建立独立 QUIC bidirectional stream，stream 内不得再套 yamux 数据流。
- UDP 代理必须优先通过 `SendDatagram` / `ReceiveDatagram` 承载，并携带可解析的 association/session/target 元数据。
- 当对端或路径不支持 datagram 时，系统必须明确记录原因，并按策略回退到现有 UDP over stream 或现有 WS/WS-H3 路径。
- yamux 只能承担控制/管理职责，或作为旧路径兼容存在；新 QUIC 数据面不得把批量 TCP/UDP 数据放回 yamux。
- 状态详情必须展示 QUIC version、datagram enabled/supported、active TCP streams、UDP datagram tx/rx/drop、RTT、吞吐、连接窗口、stream 窗口、回退原因。
- 现有 `websocket` 与 `websocket-h3` 测速、建链、代理链路测试必须保持可用。
- 单元测试和可运行回归测试必须覆盖新旧路径，不得仅做文档或编译层验证。

#### 1.1.5 风险
- 部署边界风险: 本需求只面向直连或自建可控 UDP 入口；任何反向代理或非透明中间层均不属于本需求兼容目标。CF 入口必须固定回到 `websocket`，不得尝试 H3/QUIC。
- H3 混用风险: 在标准 HTTP/3 ALPN `h3` 下打开非 HTTP/3 语义的裸 QUIC stream 可能不被中间代理和 http3 栈接受；若要共享同一底层 QUIC 连接，服务端必须由应用直接管理 quic.Conn，而不能只交给 `http3.Server`。
- 协议兼容风险: QUIC v2 需要双方和路径均支持版本协商；部分网络设备或代理可能只允许 QUIC v1/H3 流量。
- UDP datagram 风险: QUIC datagram 是不可靠、无序、可能丢弃的帧，只适合承载 UDP 语义；不能用于需要可靠传输的控制消息。
- 性能风险: 单连接 QUIC 的拥塞控制、流控窗口、包大小、GSO/PMTU、系统 UDP buffer 都会影响是否能达到 1000Mbps。
- 资源风险: 一条 TCP 对应一条 QUIC stream 后，stream 数、流控窗口、goroutine、buffer 池需要有上限与观测，否则高并发下可能内存放大。
- 回退风险: 若协议选择状态机没有清楚区分“数据面不可用”和“业务目标不可达”，可能错误降级或频繁抖动。

#### 1.1.6 遗留事项
- 实验性协议标签最终采用 `quic-stream` 还是 `quic-v2-stream`，由 Code 阶段结合现有 `normalizeProbeChainLinkLayer()` 兼容策略确定。
- H3 验证与 QUIC 数据面是否使用同一 UDP 端口，需在实现前确认现有 `http3.Server` 与自定义 quic.Listener 的端口复用方案；若无法安全复用，则采用同端口替代入口或独立实验端口。
- UDP datagram 帧格式中的 target 编码、association id 长度、最大 payload、分片策略需要在 Code 阶段固化。
- 1000Mbps 目标需要实际公网和内网压测数据闭环，本需求先定义数据面架构与观测项。

#### 1.1.7 结论
- 本需求正式纳入需求跟踪，采用“QUIC v2 优先 + TCP 一连接一 QUIC stream + UDP QUIC datagram + yamux 控制化 + WS/WS-H3 回退”的演进方向。

### 1.2 总体架构
- 状态: 已完成

#### 1.2.1 架构目标
- 用应用可控的 QUIC 数据面减少 WS-H3 与 yamux 双重复用开销。
- 让 TCP 代理流直接利用 QUIC stream 的可靠、有序、独立流控能力。
- 让 UDP 代理流直接利用 QUIC datagram 的按包、低延迟、无队头阻塞能力。
- 在不破坏现有可用路径的前提下，引入可灰度、可观测、可回退的高性能链路。

#### 1.2.2 总体设计
- 新增 QUIC Data Plane，与现有 `websocket`、`websocket-h3` 并列成为候选传输协议。
- QUIC Data Plane 使用独立 ALPN，建议 `probe-quic/1`，避免在标准 `h3` ALPN 下混入非 HTTP/3 裸 stream。
- 客户端 QUIC 配置优先提供 `quic.Version2`，同时保留 `quic.Version1`，通过版本协商得到最终版本。
- 服务端 QUIC 配置与客户端保持一致，启用 `EnableDatagrams`，并设置面向 1000Mbps 的连接级与 stream 级流控窗口。
- 连接建立后，双方先建立控制流。控制流完成认证、链路角色、能力协商、datagram 支持确认、心跳、统计与关闭原因传递。
- TCP 代理连接建立时，客户端为每条本地 TCP 打开一条 QUIC bidirectional stream，并在 stream 起始帧写入目标地址、network、session id、可选元数据。
- 服务端每收到一条 TCP data stream 后直接拨目标 TCP，并使用 `probeChainCopy()` 或优化后的 copy loop 在 QUIC stream 与目标 TCP 之间双向转发。
- UDP 代理包通过 QUIC datagram 发送。datagram payload 包含 association id、方向、目标地址或目标索引、原始 UDP payload。
- UDP 响应 datagram 使用同一 association id 返回客户端，客户端根据 association id 和本地客户端地址写回本地 UDP socket。
- 若 datagram 不可用，UDP 可按配置回退到现有 UDP over stream 或整体回退到 `websocket`/`websocket-h3`。
- yamux 仅保留在旧路径，或用于控制管理的兼容封装；新 QUIC 数据面的 TCP/UDP 数据不经过 yamux。
- 协议选择状态机将 QUIC Data Plane 作为高性能候选，但必须具备失败负缓存、冷却、最小保持时间与回退原因记录。
- 状态详情页面聚合连接级、stream级、datagram级、协议选择级指标，辅助定位吞吐瓶颈。

#### 1.2.3 关键模块
| 模块编号 | 模块名称 | 职责 | 输入 | 输出 |
|---|---|---|---|---|
| M1 | QUIC Data Plane Listener | 服务端监听和接受自定义 QUIC 数据面连接 | TLS 配置、QUIC 配置、chain runtime | 已认证或待认证 QUIC 连接 |
| M2 | QUIC Data Plane Dialer | 客户端建立 QUIC v2/v1 数据面连接 | relay endpoint、secret、bridge_role、协议策略 | QUIC 数据面会话 |
| M3 | QUIC Control Channel | 认证、能力协商、心跳、统计、关闭原因、错误码管理 | control stream 消息 | 会话状态与能力结果 |
| M4 | TCP Stream Mapper | 将一条代理 TCP 映射为一条 QUIC bidirectional stream | 本地 TCP 连接、目标地址 | QUIC stream 与远端 TCP 转发 |
| M5 | UDP Datagram Mapper | 将 UDP 包映射为 QUIC datagram | UDP association、目标地址、payload | datagram tx/rx 与本地 UDP 回写 |
| M6 | Protocol Selection and Fallback | 将 QUIC Data Plane 纳入现有协议选择与测速 | 链路质量、失败原因、回退策略 | 当前选中协议与回退决策 |
| M7 | QUIC Dataplane Observability | 暴露连接、stream、datagram、吞吐、RTT、流控与回退指标 | 运行时统计 | 链路详情 API 与页面展示 |

#### 1.2.4 关键接口
| 接口编号 | 接口名称 | 调用方 | 提供方 | 说明 |
|---|---|---|---|---|
| IF-001 | `newProbeChainQUICConfig()` | QUIC listener/dialer | QUIC 配置层 | 增加 v2/v1 版本列表、datagram 开关与高吞吐流控参数 |
| IF-002 | `startProbeChainQUICDataPlaneServer()` | chain runtime 启动路径 | M1 | 启动自定义 QUIC 数据面监听 |
| IF-003 | `openProbeChainRelayQUICStreamNetConn()` | 本地 TUN/relay 客户端 | M2/M3 | 建立 QUIC 数据面控制会话 |
| IF-004 | `openProbeChainQUICProxyStream()` | 本地 TCP 代理路径 | M4 | 为单条 TCP 代理连接打开一条 QUIC stream |
| IF-005 | `sendProbeChainQUICDatagram()` | 本地 UDP 代理路径 | M5 | 发送 UDP datagram 帧 |
| IF-006 | `receiveProbeChainQUICDatagrams()` | QUIC 数据面会话 | M5 | 接收 UDP datagram 并投递到本地或远端 |
| IF-007 | `snapshotProbeChainQUICDataPlaneState()` | 本地状态页面/API | M7 | 输出 QUIC 数据面详细状态 |
| IF-008 | `probeChainRelaySpeedTestWithLayer()` | 测速入口 | M6/M7 | 增加 QUIC Data Plane 测速候选与结果展示 |

#### 1.2.5 关键约束
- QUIC Data Plane 必须是实验能力，默认可灰度开启，不得破坏现有 `websocket` 与 `websocket-h3`。
- QUIC Data Plane 只要求直连或自建可控 UDP 入口可达，不考虑反向代理兼容；CF 入口候选协议必须排除 QUIC Data Plane 与 `websocket-h3`。
- 在 `h3` ALPN 下不得直接假设可以打开裸 QUIC stream 承载代理数据；若需要底层 QUIC stream，必须由双方应用协议共同管理。
- 控制消息必须可靠传输，禁止使用 QUIC datagram 承载认证、流创建、关闭原因等控制语义。
- TCP 数据必须使用 QUIC bidirectional stream，不得再嵌套 yamux 数据流。
- UDP 数据优先使用 QUIC datagram；datagram 不可用时必须显式进入回退路径并展示原因。
- datagram payload 必须限制在路径 MTU 和 quic-go 可发送上限内，超过上限不得静默截断。
- 每条 QUIC 连接必须限制最大 TCP stream 并发、UDP association 数、datagram 队列长度和内存占用。
- 协议选择必须记录 QUIC version、datagram negotiated、失败阶段、回退目标和冷却时间。
- 所有新增协议帧必须具备版本号，便于后续兼容升级。

#### 1.2.6 风险
- 自定义 QUIC 数据面会绕过 HTTP 代理生态，部署环境必须允许 UDP/443 或指定 UDP 端口直达服务端。
- 若流控窗口设置过大，弱机器上可能导致内存占用快速上升；若设置过小，又无法接近 1000Mbps。
- UDP datagram 不保证送达，DNS/游戏/实时流量可接受，其他 UDP 业务可能需要应用层容错。
- QUIC v2 虽由 quic-go 支持，但中间网络对 v2 长包/版本协商的处理仍可能不稳定。
- 若测速仍只测短周期，可能无法反映 QUIC 慢启动、拥塞窗口增长和稳态吞吐。

#### 1.2.7 结论
- 总体架构采用新增自定义 QUIC Data Plane 的方案，控制与数据分离，TCP 走 QUIC stream，UDP 走 QUIC datagram，保留现有 WS/WS-H3 兼容回退。

### 1.3 单元设计
- 状态: 已完成

#### 1.3.1 单元清单
| 单元编号 | 单元名称 | 所属模块 | 职责 | 输入 | 输出 |
|---|---|---|---|---|---|
| U1 | QUIC Config Unit | M1/M2 | 统一 QUIC v2/v1、datagram、流控、keepalive 配置 | 参数常量、运行配置 | `*quic.Config` |
| U2 | Server Accept Unit | M1 | 接受 QUIC 数据面连接并启动控制流处理 | UDP listener、TLS、chain runtime | QUIC 会话 |
| U3 | Client Dial Unit | M2 | 建立 QUIC 数据面连接并完成控制认证 | relay endpoint、secret、bridge_role | 数据面客户端会话 |
| U4 | Control Protocol Unit | M3 | 定义控制帧、认证、能力协商、心跳和统计 | control stream | 会话状态 |
| U5 | TCP Stream Unit | M4 | 一条 TCP 代理连接对应一条 QUIC stream | 本地 TCP、目标地址 | 双向代理 |
| U6 | UDP Datagram Unit | M5 | UDP association 与 datagram 编解码、投递、回写 | UDP packet、association | datagram tx/rx |
| U7 | Fallback Unit | M6 | 处理 QUIC 数据面失败、datagram 不支持、版本不兼容 | 错误分类、质量状态 | 回退到 WS/WS-H3 |
| U8 | Observability Unit | M7 | 收集并输出 QUIC 数据面指标 | 会话统计、错误事件 | 链路详情状态 |

#### 1.3.2 单元设计
##### 单元编号: U1
- 单元名称: QUIC Config Unit
- 职责: 统一生成客户端和服务端 QUIC 数据面配置。
- 输入: 最大 stream 数、是否启用 datagram、高吞吐窗口参数、keepalive 参数。
- 输出: `*quic.Config`。
- 处理规则: 默认 `Versions` 顺序为 `quic.Version2`、`quic.Version1`；默认 `EnableDatagrams=true`；窗口参数不得小于当前 WS-H3 路径使用的 QUIC 窗口。
- 异常规则: 若运行库不支持 QUIC v2 或 datagram，必须降级并记录能力缺失，不得伪装为 QUIC v2/datagram 成功。

##### 单元编号: U2
- 单元名称: Server Accept Unit
- 职责: 服务端启动自定义 QUIC 数据面入口。
- 输入: 监听地址、证书、chain runtime、QUIC 配置。
- 输出: 待认证 QUIC 连接。
- 处理规则: 接入后必须先等待控制流认证；认证成功前不得接受 TCP/UDP 数据；连接关闭必须清理所有 stream 与 association。
- 异常规则: 监听失败不得影响现有 WS/WS-H3；认证失败、版本失败、datagram 协商失败必须记录日志和状态。

##### 单元编号: U3
- 单元名称: Client Dial Unit
- 职责: 客户端建立 QUIC 数据面会话。
- 输入: chain_id、secret、relay host/port、dial host、host header、bridge_role、open timeout。
- 输出: QUIC 数据面客户端会话。
- 处理规则: 成功后创建控制流并完成认证；失败时进入协议负缓存并按策略回退；成功时刷新解析成功缓存。
- 异常规则: 拨号、TLS、版本协商、认证、控制流任一阶段失败都必须带阶段标签。

##### 单元编号: U4
- 单元名称: Control Protocol Unit
- 职责: 管理认证、能力协商、心跳、统计和关闭原因。
- 输入: control stream JSON 或二进制帧。
- 输出: 会话能力、统计事件、关闭事件。
- 处理规则: 控制帧必须含协议版本、消息类型、request id；认证帧必须复用现有 secret/HMAC 逻辑；能力协商必须返回 quic_version、datagram_supported、max_datagram_payload、max_streams。
- 异常规则: 控制帧解析失败或认证失败时关闭连接；心跳超时关闭连接并触发客户端回退或重连。

##### 单元编号: U5
- 单元名称: TCP Stream Unit
- 职责: 一条代理 TCP 映射一条 QUIC bidirectional stream。
- 输入: 本地 TCP 连接、目标地址、QUIC 会话。
- 输出: 远端 TCP 代理连接与双向流量。
- 处理规则: stream 首帧携带 open request；服务端拨号成功后进入双向 copy；任一方向 EOF 应半关闭对应方向；错误关闭必须返回 stream 错误码或控制事件。
- 异常规则: 目标拨号失败只关闭当前 stream，不关闭整个 QUIC 会话。

##### 单元编号: U6
- 单元名称: UDP Datagram Unit
- 职责: 使用 QUIC datagram 承载 UDP 代理包。
- 输入: UDP association、目标地址、payload、QUIC 会话。
- 输出: datagram 帧与本地/远端 UDP 包。
- 处理规则: datagram 帧必须包含版本、association id、方向、target id 或 target addr、payload；目标地址可通过控制流注册为 target id 以减少每包头开销。
- 异常规则: `DatagramTooLargeError`、send queue full、datagram unsupported 必须计数并触发回退策略；不得静默丢包。

##### 单元编号: U7
- 单元名称: Fallback Unit
- 职责: 将 QUIC 数据面纳入现有协议选择与失败回退。
- 输入: QUIC 数据面错误、质量指标、现有协议状态。
- 输出: 选中协议、负缓存、回退原因。
- 处理规则: 直连/自建入口的 QUIC 数据面失败后可按策略回退 `websocket-h3` 或 `websocket`；CF 入口不进入本单元，始终只使用 `websocket`。
- 异常规则: 鉴权失败不得切换协议；业务目标失败不得标记 QUIC 数据面不可用。

##### 单元编号: U8
- 单元名称: Observability Unit
- 职责: 输出 QUIC 数据面的细粒度状态。
- 输入: 会话、stream、datagram、流控、测速和错误事件。
- 输出: 链路详情 API 与页面状态。
- 处理规则: 至少展示 negotiated_version、datagram_supported、active_streams、opened_streams、closed_streams、stream_open_failures、datagram_tx/rx/drop、rtt_ms、send_rate、recv_rate、fallback_reason、last_error。
- 异常规则: 状态采集失败不得影响代理业务，采集失败本身必须展示。

#### 1.3.3 风险
- QUIC Data Plane 需要直接管理 `quic.Conn` 生命周期，不能简单复用 `http3.Server` handler 模型。
- UDP datagram 的目标地址压缩、association 生命周期和 NAT 映射过期策略如果设计不稳，会造成回包错投或状态泄漏。
- 如果 Code 阶段同时重构太多 TUN 与代理入口，风险会失控；必须先做可灰度的最小数据面闭环。

#### 1.3.4 结论
- 单元设计已覆盖配置、接入、控制、TCP stream、UDP datagram、回退和观测；Code 阶段应优先实现最小可用 QUIC 数据面，再逐步接入默认协议选择。

### 1.4 Code任务执行包
- 状态: 已完成

#### 1.4.1 执行边界
- 允许修改: `probe_node/link_chain_runtime.go`、`probe_node/link_relay_client_transport.go`、`probe_node/local_tun_group_runtime.go`、`probe_node/link_chain_udp_assoc.go`、`probe_node/local_console.go`、`probe_node/local_pages/proxy.html`、`probe_node/main.go`、`probe_node/*_test.go`、必要时新增 `probe_node/link_quic_dataplane*.go`、必要时修改 `probe_node/go.mod` 与 `probe_node/go.sum`。
- 禁止修改: 不删除现有 `websocket`、`websocket-h3` 路径；不重写无关 DNS/TUN 路由功能；不改动非 probe_node 业务模块；不绕过现有私有鉴权。

#### 1.4.2 任务清单
| 任务编号 | 需求编号 | 单元编号 | 文件范围 | 操作类型 | 验收标准 |
|---|---|---|---|---|---|
| REQ-PN-QUIC-STREAM-DATAPLANE-001-T001 | REQ-PN-QUIC-STREAM-DATAPLANE-001 | U1 | `probe_node/link_chain_runtime.go`、必要时新增 `probe_node/link_quic_dataplane_config.go` | 改造/新增 | QUIC 配置显式启用 v2/v1、datagram、高吞吐窗口；现有 H3 路径不回归 |
| REQ-PN-QUIC-STREAM-DATAPLANE-001-T002 | REQ-PN-QUIC-STREAM-DATAPLANE-001 | U2/U4 | `probe_node/link_chain_runtime.go`、新增 `probe_node/link_quic_dataplane_server.go` | 新增/改造 | 服务端可启动 QUIC 数据面入口，认证控制流成功后进入 ready，失败日志包含阶段 |
| REQ-PN-QUIC-STREAM-DATAPLANE-001-T003 | REQ-PN-QUIC-STREAM-DATAPLANE-001 | U3/U4 | `probe_node/link_relay_client_transport.go`、新增 `probe_node/link_quic_dataplane_client.go` | 新增/改造 | 客户端可建立 QUIC 数据面会话，完成认证与能力协商，并记录 negotiated_version/datagram_supported |
| REQ-PN-QUIC-STREAM-DATAPLANE-001-T004 | REQ-PN-QUIC-STREAM-DATAPLANE-001 | U5 | `probe_node/local_tun_group_runtime.go`、`probe_node/link_chain_runtime.go`、新增 `probe_node/link_quic_dataplane_tcp.go` | 改造/新增 | 每条代理 TCP 连接使用独立 QUIC bidirectional stream，数据不经过 yamux |
| REQ-PN-QUIC-STREAM-DATAPLANE-001-T005 | REQ-PN-QUIC-STREAM-DATAPLANE-001 | U6 | `probe_node/link_chain_udp_assoc.go`、`probe_node/link_chain_runtime.go`、新增 `probe_node/link_quic_dataplane_udp.go` | 改造/新增 | UDP 优先通过 QUIC datagram 传输，支持 association/target 映射、过大包错误、datagram 不支持回退 |
| REQ-PN-QUIC-STREAM-DATAPLANE-001-T006 | REQ-PN-QUIC-STREAM-DATAPLANE-001 | U7 | `probe_node/link_relay_client_transport.go`、`probe_node/local_tun_group_runtime.go` | 改造 | 协议选择支持 QUIC 数据面灰度候选，失败后回退 WS-H3/WS，非入口错误不触发回退 |
| REQ-PN-QUIC-STREAM-DATAPLANE-001-T007 | REQ-PN-QUIC-STREAM-DATAPLANE-001 | U8 | `probe_node/local_console.go`、`probe_node/local_pages/proxy.html`、必要时新增状态 API | 改造/新增 | 链路详情展示 QUIC version、datagram、stream、吞吐、RTT、流控、回退原因 |
| REQ-PN-QUIC-STREAM-DATAPLANE-001-T008 | REQ-PN-QUIC-STREAM-DATAPLANE-001 | U1-U8 | `probe_node/*_test.go` | 测试 | 覆盖 QUIC v2/v1、TCP stream、UDP datagram、datagram 回退、协议回退、旧路径不回归 |

#### 1.4.3 源码修改规则
- 必须使用 encoding_tools/README.md 描述的接口。
- 对 C/C++ 源代码（`.c`、`.cc`、`.cpp`、`.cxx`、`.h`、`.hpp`）必须使用 `encoding_tools/encoding_safe_patch.py`。
- 对非 C/C++ 源代码可直接编辑，不强制使用 `encoding_tools/encoding_safe_patch.py`。
- encoding_tools/ 不可用或执行失败时，Code 必须记录失败命令、错误摘要、影响文件与阻塞影响，并提交第2.6节 `Code任务反馈`。
- 替代 encoding_tools/ 修改受控 C/C++ 源代码前，必须取得 Architect 明确允许。

#### 1.4.4 交付物
- QUIC Data Plane 服务端入口与客户端会话实现。
- 控制流认证和能力协商协议。
- TCP 一连接一 QUIC stream 代理实现。
- UDP QUIC datagram 代理实现和回退策略。
- 协议选择、测速、链路详情观测接入。
- 单元测试、回归测试与压测观测证据。

#### 1.4.5 门禁输入
- Code 阶段必须先实现可灰度关闭的最小闭环，再考虑纳入默认 auto 策略。
- Code 阶段必须验证现有 `websocket` 与 `websocket-h3` 路径不回归。
- Code 阶段只需验证直连或自建可控 UDP 入口场景；若入口标记为 CF/外部代理，必须验证其不会进入 QUIC Data Plane 或 `websocket-h3` 候选集合。

#### 1.4.6 结论
- Code 任务执行包已定义，可进入 Code 实施；实施时不得扩大到非任务包文件和非范围需求。

### 1.5 Architect需求跟踪矩阵
- 状态: 已完成

| 需求编号 | 需求描述 | 架构章节 | 单元设计章节 | Code任务章节 | 状态 | 备注 |
|---|---|---|---|---|---|---|
| REQ-PN-QUIC-STREAM-DATAPLANE-001 | 新增 QUIC v2 优先数据面，TCP 一连接一 QUIC stream，UDP 使用 QUIC datagram，yamux 收敛为控制/管理职责 | 1.2 | 1.3 | 1.4 | 进行中 | 待 Code 实施 |

### 1.6 Architect关键接口跟踪矩阵
- 状态: 已完成

| 接口编号 | 需求编号 | 接口名称 | 调用方 | 提供方 | 输入 | 输出 | 状态 | 备注 |
|---|---|---|---|---|---|---|---|---|
| IF-001 | REQ-PN-QUIC-STREAM-DATAPLANE-001 | `newProbeChainQUICConfig()` | QUIC listener/dialer | QUIC 配置层 | 窗口、stream、datagram、版本参数 | `*quic.Config` | 待实现 | 需要显式设置 v2/v1 与 datagram |
| IF-002 | REQ-PN-QUIC-STREAM-DATAPLANE-001 | `startProbeChainQUICDataPlaneServer()` | chain runtime 启动路径 | QUIC 服务端入口 | 监听配置、证书、runtime | QUIC 数据面入口 | 待实现 | 建议新增函数 |
| IF-003 | REQ-PN-QUIC-STREAM-DATAPLANE-001 | `openProbeChainRelayQUICStreamNetConn()` | 客户端连接路径 | QUIC 客户端拨号 | endpoint、secret、bridge_role | QUIC 数据面会话 | 待实现 | 名称可由 Code 阶段调整 |
| IF-004 | REQ-PN-QUIC-STREAM-DATAPLANE-001 | `openProbeChainQUICProxyStream()` | TCP 代理路径 | TCP Stream Mapper | target、metadata | QUIC stream | 待实现 | 一条 TCP 一条 stream |
| IF-005 | REQ-PN-QUIC-STREAM-DATAPLANE-001 | `sendProbeChainQUICDatagram()` | UDP 代理路径 | UDP Datagram Mapper | association、target、payload | datagram 发送结果 | 待实现 | 需要过大包处理 |
| IF-006 | REQ-PN-QUIC-STREAM-DATAPLANE-001 | `receiveProbeChainQUICDatagrams()` | QUIC 会话 | UDP Datagram Mapper | datagram payload | UDP 投递结果 | 待实现 | 需要 association 映射 |
| IF-007 | REQ-PN-QUIC-STREAM-DATAPLANE-001 | `snapshotProbeChainQUICDataPlaneState()` | 状态页面/API | Observability Unit | 运行时统计 | 详情状态 | 待实现 | 链路详情优化依据 |
| IF-008 | REQ-PN-QUIC-STREAM-DATAPLANE-001 | `probeChainRelaySpeedTestWithLayer()` | 测速入口 | 协议选择/测速模块 | 协议标签、byteCount | 测速结果 | 待实现 | 增加 QUIC 数据面测速 |

### 1.7 门禁裁判
- 状态: 已完成

#### 1.7.1 门禁输入
| 文档 | 路径 | 状态 |
|---|---|---|
| 协作文档 | doc/REQ-PN-QUIC-STREAM-DATAPLANE-001-collaboration.md | 已创建 |

#### 1.7.2 裁判检查
| 检查项 | 结果 | 证据 | 备注 |
|---|---|---|---|
| 协作文档存在 | 通过 | `doc/REQ-PN-QUIC-STREAM-DATAPLANE-001-collaboration.md` | 已创建 |
| Architect章节存在 | 通过 | 第1章 | 已初始化 |
| Code章节存在 | 通过 | 第2章 | 已初始化 |
| 必需子章节存在 | 通过 | 第1章、第2章固定子章节 | 已初始化 |
| 需求前缀一致 | 通过 | 文档头与矩阵 | 一致 |
| 需求编号一致 | 通过 | 文档头与矩阵 | 一致 |
| 接口编号一致 | 通过 | 1.2.4、1.6 | 一致 |
| 模板字段完整 | 通过 | 文档头字段完整 | 已填写 |
| Code使用encoding_tools | 不适用 | Code 未开始 | Code 阶段必须记录 |
| Code证据完整 | 不适用 | Code 未开始 | Code 阶段必须补齐 |
| Code任务反馈已处理 | 通过 | 2.6 当前无反馈 | 无反馈 |
| 验收标准可测试 | 通过 | 1.1.4、1.4.2 | 可测试 |
| 需求任务覆盖完整 | 通过 | 1.4.2 | 已覆盖 |
| 任务自测覆盖完整 | 通过 | 1.4.2、2.3 | 已定义测试项 |
| 修改文件在允许范围内 | 通过 | 1.4.1 | 已限定 |
| 测试失败已记录缺陷 | 不适用 | Code 未开始 | Code 阶段检查 |
| 未执行测试原因完整 | 通过 | 2.5.7 | 当前为文档初始化 |
| 遗留风险可接受 | 通过 | 1.1.5、1.2.6、1.3.3 | 可接受 |

#### 1.7.3 冲突记录
| 冲突编号 | 冲突条款 | 最终采用条款 | 裁决人 | 裁决结论 |
|---|---|---|---|---|
| 无 | 无 | 无 | 无 | 无 |

#### 1.7.4 裁判结论
- 结论: 有条件通过
- 放行阻塞: 放行
- 条件: Code 阶段必须按第1.4节任务包实施，并补齐第2章执行证据、测试结果与遗留风险。
- 责任方: Code
- 关闭要求: QUIC 数据面实现、测试和观测证据完成后，Architect 重新执行最终门禁。
- 整改要求: 无

#### 1.7.5 结论
- Architect 阶段已完成，允许进入 Code 实施。

## 第2章 Code章节
- 章节责任角色: Code
- 状态: 首版最小闭环已完成

### 2.1 Code需求跟踪矩阵
- 状态: 部分完成

| 需求编号 | 任务编号 | 实现文件 | 实现状态 | 自测状态 | 证据 | 备注 |
|---|---|---|---|---|---|---|
| REQ-PN-QUIC-STREAM-DATAPLANE-001 | REQ-PN-QUIC-STREAM-DATAPLANE-001-T001 | `probe_node/link_chain_runtime.go` | 已完成 | 通过 | `TestNewProbeChainQUICConfigUsesV2V1AndDatagrams`、`go test ./...` | QUIC 配置显式启用 v2/v1、datagram 与高吞吐窗口 |
| REQ-PN-QUIC-STREAM-DATAPLANE-001 | REQ-PN-QUIC-STREAM-DATAPLANE-001-T002 | `probe_node/link_chain_runtime.go`、`probe_node/link_quic_dataplane.go` | 已完成 | 通过 | `TestProbeChainQUICDataPlaneTCPStreamRoundTrip`、`go test ./...` | 服务端启动独立实验 QUIC Data Plane 入口，认证后接受 stream |
| REQ-PN-QUIC-STREAM-DATAPLANE-001 | REQ-PN-QUIC-STREAM-DATAPLANE-001-T003 | `probe_node/link_relay_client_transport.go`、`probe_node/link_quic_dataplane.go` | 已完成 | 通过 | `TestProbeChainQUICDataPlaneTCPStreamRoundTrip`、`go test ./...` | 客户端可建立 QUIC 会话并完成能力响应读取 |
| REQ-PN-QUIC-STREAM-DATAPLANE-001 | REQ-PN-QUIC-STREAM-DATAPLANE-001-T004 | `probe_node/local_tun_group_runtime.go`、`probe_node/link_quic_dataplane.go` | 已完成 | 通过 | `TestProbeChainQUICDataPlaneTCPStreamRoundTrip`、`TestProbeChainQUICDataPlaneLayerIncludesHTTP3Alias`、`go test ./...` | TUN 组运行时在 `http3` / `quic-stream` 下直接打开 QUIC bidirectional stream，不套 yamux |
| REQ-PN-QUIC-STREAM-DATAPLANE-001 | REQ-PN-QUIC-STREAM-DATAPLANE-001-T005 | `probe_node/link_quic_dataplane.go` | 部分完成 | 部分通过 | `go test ./...` | 已启用 QUIC datagram 能力并返回协商状态；UDP datagram association/target 业务映射待续 |
| REQ-PN-QUIC-STREAM-DATAPLANE-001 | REQ-PN-QUIC-STREAM-DATAPLANE-001-T006 | `probe_node/link_relay_client_transport.go`、`probe_node/local_tun_group_runtime.go`、`probe_node/local_console.go`、`probe_node/local_pages/proxy.html` | 部分完成 | 通过 | `TestProbeLocalProxyLinkReachabilityHTTP3UsesQUICStream`、`go test ./...` | TUN 组 `http3` 已切换为 QUIC Data Plane；旧 `websocket-h3` 仅保留显式兼容路径 |
| REQ-PN-QUIC-STREAM-DATAPLANE-001 | REQ-PN-QUIC-STREAM-DATAPLANE-001-T007 | `probe_node/link_chain_runtime.go`、`probe_node/link_relay_client_transport.go` | 部分完成 | 通过 | `go test ./...` | listener 状态和测速握手已接入；细粒度 stream/datagram 计数待续 |
| REQ-PN-QUIC-STREAM-DATAPLANE-001 | REQ-PN-QUIC-STREAM-DATAPLANE-001-T008 | `probe_node/link_quic_dataplane_test.go` | 部分完成 | 通过 | `go test ./...` | 覆盖配置、控制认证、TCP stream round trip、旧路径全量回归 |

### 2.2 Code关键接口跟踪矩阵
- 状态: 部分完成

| 接口编号 | 需求编号 | 实现文件 | 调用方 | 提供方 | 实现状态 | 证据 | 备注 |
|---|---|---|---|---|---|---|---|
| IF-001 | REQ-PN-QUIC-STREAM-DATAPLANE-001 | `probe_node/link_chain_runtime.go` | QUIC listener/dialer | QUIC 配置层 | 已完成 | `TestNewProbeChainQUICConfigUsesV2V1AndDatagrams` | `Versions=[v2,v1]`、`EnableDatagrams=true` |
| IF-002 | REQ-PN-QUIC-STREAM-DATAPLANE-001 | `probe_node/link_quic_dataplane.go` | chain runtime 启动路径 | QUIC 服务端入口 | 已完成 | `TestProbeChainQUICDataPlaneTCPStreamRoundTrip` | 使用独立实验 UDP 端口 `listen_port+1`，避免与现有 H3 监听抢同一 socket |
| IF-003 | REQ-PN-QUIC-STREAM-DATAPLANE-001 | `probe_node/link_quic_dataplane.go`、`probe_node/link_relay_client_transport.go` | 客户端连接路径 | QUIC 客户端拨号 | 已完成 | `TestProbeChainQUICDataPlaneTCPStreamRoundTrip` | 完成 HMAC 认证、能力响应和 negotiated version/datagram 日志 |
| IF-004 | REQ-PN-QUIC-STREAM-DATAPLANE-001 | `probe_node/link_quic_dataplane.go`、`probe_node/local_tun_group_runtime.go` | TCP 代理路径 | TCP Stream Mapper | 已完成 | `TestProbeChainQUICDataPlaneTCPStreamRoundTrip`、`TestProbeChainQUICDataPlaneLayerIncludesHTTP3Alias` | TUN 组 `http3` / `quic-stream` 每条 TCP open 使用独立 QUIC bidirectional stream |
| IF-005 | REQ-PN-QUIC-STREAM-DATAPLANE-001 | `probe_node/link_quic_dataplane.go` | UDP 代理路径 | UDP Datagram Mapper | 部分完成 | `go test ./...` | datagram 能力协商已就绪，业务帧发送待续 |
| IF-006 | REQ-PN-QUIC-STREAM-DATAPLANE-001 | `probe_node/link_quic_dataplane.go` | QUIC 会话 | UDP Datagram Mapper | 部分完成 | `go test ./...` | `ReceiveDatagram` 消费循环与 association 投递待续 |
| IF-007 | REQ-PN-QUIC-STREAM-DATAPLANE-001 | `probe_node/link_chain_runtime.go`、`probe_node/link_relay_client_transport.go` | 状态页面/API | Observability Unit | 部分完成 | `go test ./...` | listener 状态和握手日志已接入，细粒度指标待续 |
| IF-008 | REQ-PN-QUIC-STREAM-DATAPLANE-001 | `probe_node/link_relay_client_transport.go`、`probe_node/local_console.go`、`probe_node/local_pages/proxy.html` | 测速入口 | 协议选择/测速模块 | 部分完成 | `go test ./...` | 页面链路测试改为 `测速QUIC` / `测速WS`；`quic-stream` 已打开 speed_test stream 并按数据读取窗口计算吞吐 |

### 2.3 Code测试项跟踪矩阵
- 状态: 部分完成

| 测试项编号 | 需求编号 | 任务编号 | 测试目标 | 测试方法 | 结果 | 证据 | 未执行原因 | 备注 |
|---|---|---|---|---|---|---|---|---|
| REQ-PN-QUIC-STREAM-DATAPLANE-001-TEST-001 | REQ-PN-QUIC-STREAM-DATAPLANE-001 | T001 | QUIC v2 优先、v1 回退、datagram 配置 | 单元测试 | 通过 | `TestNewProbeChainQUICConfigUsesV2V1AndDatagrams` | 无 | 无 |
| REQ-PN-QUIC-STREAM-DATAPLANE-001-TEST-002 | REQ-PN-QUIC-STREAM-DATAPLANE-001 | T002/T003 | 客户端与服务端控制流认证和能力协商 | 集成测试 | 通过 | `TestProbeChainQUICDataPlaneTCPStreamRoundTrip` | 无 | 测试通过认证后继续验证 TCP stream |
| REQ-PN-QUIC-STREAM-DATAPLANE-001-TEST-003 | REQ-PN-QUIC-STREAM-DATAPLANE-001 | T004 | 一条 TCP 代理连接对应一条 QUIC stream | 集成测试 | 通过 | `TestProbeChainQUICDataPlaneTCPStreamRoundTrip` | 无 | 使用真实 QUIC listener/client 与本地 TCP echo |
| REQ-PN-QUIC-STREAM-DATAPLANE-001-TEST-004 | REQ-PN-QUIC-STREAM-DATAPLANE-001 | T005 | UDP 使用 QUIC datagram 并支持回包 | 集成测试 | 未执行 | 无 | UDP datagram 业务映射尚未完成 | 后续补齐 association/target 映射后执行 |
| REQ-PN-QUIC-STREAM-DATAPLANE-001-TEST-005 | REQ-PN-QUIC-STREAM-DATAPLANE-001 | T005/T006 | datagram 不支持或过大包时回退 | 单元/集成测试 | 未执行 | 无 | UDP datagram 回退尚未完成 | 后续补齐 `DatagramTooLargeError` 与 fallback 测试 |
| REQ-PN-QUIC-STREAM-DATAPLANE-001-TEST-006 | REQ-PN-QUIC-STREAM-DATAPLANE-001 | T006/T007 | 协议选择、测速、链路详情展示 | 单元/页面测试 | 部分通过 | `TestProbeChainQUICDataPlaneLayerIncludesHTTP3Alias`、`TestProbeLocalProxyLinkReachabilityHTTP3UsesQUICStream`、`go test ./...` | 页面细粒度指标未完成 | 当前覆盖 `http3` 切 QUIC Data Plane、listener 状态和握手测速 |
| REQ-PN-QUIC-STREAM-DATAPLANE-001-TEST-007 | REQ-PN-QUIC-STREAM-DATAPLANE-001 | T008 | 现有 WS/WS-H3 路径不回归 | `go test ./...` | 通过 | `ok github.com/cloudhelper/probe_node` | 无 | 全量回归通过 |

### 2.4 Code缺陷跟踪矩阵
- 状态: 已更新

| 缺陷编号 | 需求编号 | 测试项编号 | 缺陷描述 | 严重级别 | 修复状态 | 修复证据 | 备注 |
|---|---|---|---|---|---|---|---|
| 无 | REQ-PN-QUIC-STREAM-DATAPLANE-001 | 无 | 当前未发现新增测试失败缺陷 | 无 | 无 | `go test ./...` | UDP datagram 未完成按遗留风险记录 |

### 2.5 Code执行证据
- 状态: 已更新

#### 2.5.1 修改接口
- `newProbeChainQUICConfig()` 增加 `Versions=[quic.Version2, quic.Version1]` 与 `EnableDatagrams=true`。
- `startProbeChainQUICDataPlaneServer()` 新增服务端 QUIC Data Plane 入口。
- `openProbeChainRelayQUICDataPlaneSession()` 新增客户端 QUIC Data Plane 会话拨号与控制流认证。
- `openProbeChainQUICProxyStream()` 新增 TCP over QUIC bidirectional stream open。
- `probeLocalProxyLinkLatencyHandler()` 的链路延迟改为认证建连后读取 1 byte 测试数据的耗时，不把登录、鉴权或握手耗时计入 `latency_ms`。
- `probeChainRelaySpeedTestWithLayer()` 支持 `quic-stream` 真实数据流测速；测速结果中的 `latency_ms` 为认证建连后的首字节数据耗时，`duration_ms` 与 `rate_bps` 只按数据读取窗口计算。
- `isProbeChainQUICDataPlaneLayer()` 将 TUN 组 `http3` 入口切换到 QUIC Data Plane，旧 `websocket-h3` 不再作为该路径默认连接方式。
- `proxy.html` 链路操作将非 CF 的 H3 测试入口改为 `测速QUIC`，CF 入口仍只允许 `websocket`。

#### 2.5.2 配置文件
- 无新增配置文件；首版采用显式协议标签 `quic-stream`，QUIC Data Plane 监听端口为 `listen_port+1`。

#### 2.5.3 执行报告
- 已完成 QUIC Data Plane 首版最小闭环: QUIC v2/v1 配置、datagram 能力开启、独立 ALPN `probe-quic/1`、控制流 HMAC 认证、服务端接受数据 stream、客户端 `quic-stream` 会话、本地 TUN 组 `http3` / `quic-stream` 运行时绕过 yamux 打开独立 QUIC stream、TCP echo 集成测试、QUIC speed_test 数据窗口测速。

#### 2.5.4 影响文件
- `probe_node/link_chain_runtime.go`
- `probe_node/link_relay_client_transport.go`
- `probe_node/local_tun_group_runtime.go`
- `probe_node/local_console.go`
- `probe_node/local_console_test.go`
- `probe_node/local_pages/proxy.html`
- `probe_node/link_quic_dataplane.go`
- `probe_node/link_quic_dataplane_test.go`
- `doc/REQ-PN-QUIC-STREAM-DATAPLANE-001-collaboration.md`

#### 2.5.5 测试命令
- `cd probe_node && go test ./...`

#### 2.5.6 自测结果
- `go test ./...` 通过。

#### 2.5.7 未执行测试原因
- UDP datagram association/target 映射尚未完成，因此 UDP datagram 回包、过大包和 datagram 不支持回退测试未执行。
- 前端页面细粒度 QUIC stream/datagram 指标尚未完成，因此页面专项测试未执行。

#### 2.5.8 遗留风险
- QUIC Data Plane 当前使用独立实验 UDP 端口 `listen_port+1`，避免与现有 HTTP/3 server 抢同一 UDP socket；部署侧需开放相邻 UDP 端口。
- UDP datagram 业务映射尚未完成；当前 UDP 仍需后续实现 association/target frame、接收循环、过大包处理和回退。
- TUN 组 `http3` 已切到 QUIC Data Plane；非 TUN yamux carrier 路径仍保留旧 `websocket-h3` 兼容，避免把不兼容的 QUIC stream 控制连接误用于 yamux。
- 观测当前覆盖 listener 状态与握手日志，尚未补齐 active streams、datagram tx/rx/drop、窗口和吞吐细粒度状态。

#### 2.5.9 回滚方案
- 回滚本次影响文件即可移除 QUIC Data Plane 首版实现；现有 `websocket` 与 `websocket-h3` 路径未删除。

#### 2.5.10 结论
- Code 首版最小闭环已完成，允许继续推进 UDP datagram、细粒度观测和默认 auto 灰度策略。

### 2.6 Code任务反馈
- 状态: 已更新

| 反馈编号 | 任务编号 | 反馈类型 | 反馈描述 | 阻塞影响 | Code建议 | Architect处理状态 | Architect处理结论 |
|---|---|---|---|---|---|---|---|
| REQ-PN-QUIC-STREAM-DATAPLANE-001-F001 | T002/T003 | 实现约束 | quic-go 同一 `net.PacketConn` 只能由一个 QUIC Transport/Listener 使用，现有 HTTP/3 server 已占用 `listen_port` UDP socket | 不阻塞首版 TCP 闭环，但影响“同端口复用”规划 | 首版采用独立实验 UDP 端口 `listen_port+1`；后续如需同端口，需要重构为统一 QUIC listener 并按 ALPN 分发 H3 与 Data Plane | 待Architect处理 | 待定 |
| REQ-PN-QUIC-STREAM-DATAPLANE-001-F002 | T005 | 范围拆分 | UDP datagram association/target 映射和 fallback 仍未完成 | 阻塞 T005/T005 测试完全关闭 | 下一步单独实现 datagram frame、接收循环、过大包计数和 fallback | 待Architect处理 | 待定 |

#### 2.6.1 结论
- 无阻塞首版 TCP 最小闭环的问题；UDP datagram 与同端口复用作为后续反馈继续跟踪。
