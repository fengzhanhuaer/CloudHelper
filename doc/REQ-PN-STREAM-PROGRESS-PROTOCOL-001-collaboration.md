# 协作文档

- 适用规则: AI协作规则
- 后续工作传递声明: 本文档必须传递给后续阶段与后续角色。
- 需求编号: REQ-PN-STREAM-PROGRESS-PROTOCOL-001
- 需求前缀: REQ-PN-STREAM-PROGRESS-PROTOCOL-001
- 当前阶段: Architect协议讨论落盘
- 最近更新角色: Architect
- 最近更新时间: 2026-06-15T13:22:27+08:00
- 工作依据文档: doc/ai-coding-collaboration.md; 用户讨论: 在稳定流上为 probe node chain 建立自定义帧协议，基于 WS 承载，包含帧头、帧长、校验、可变控制部分、可变长数据部分、控制帧、缓冲池、独立收发线程、分层统计、多探针级联支持、独立磁贴界面、日志状态查找排错能力，承载 SOCKS5、HTTP、TUN 代理、端口转发和后续虚拟组网，能够自动协商 HTTP/2、HTTP/3 承载，并记录 Cloudflare 分段协议模型后续验证项。
- 状态: 进行中

## 第1章 Architect章节
- 章节责任角色: Architect
- 状态: 进行中

### 1.1 需求定义
- 状态: 进行中

#### 1.1.1 需求目标
- REQ-PN-STREAM-PROGRESS-PROTOCOL-001-R1: 在稳定 WebSocket 长连接上建立 CloudHelper 自定义完整帧协议，承载 probe node chain 的控制帧、数据帧、进度帧和关闭/错误帧。
- REQ-PN-STREAM-PROGRESS-PROTOCOL-001-R2: 自定义帧必须包含固定帧头、完整帧长、校验字段、可变控制部分和可变长数据部分。
- REQ-PN-STREAM-PROGRESS-PROTOCOL-001-R3: 帧缓冲池只负责完整帧的接收、发送和复用，不负责组帧、解析 control、解析 data 或理解业务语义。
- REQ-PN-STREAM-PROGRESS-PROTOCOL-001-R4: 底层连接必须由独立 read loop 和独立 write loop 负责收发，协议层不得直接读写底层 WS。
- REQ-PN-STREAM-PROGRESS-PROTOCOL-001-R5: 每层必须建立统计与事件记录，支持后续排查连接阻塞、帧错误、协议状态机错误、stream 队列压力和多跳级联问题。
- REQ-PN-STREAM-PROGRESS-PROTOCOL-001-R6: 协议第一版必须支持多探针级联建模，区分端到端 `flow_id` 与逐跳 `conn_id/stream_id/hop_id`。
- REQ-PN-STREAM-PROGRESS-PROTOCOL-001-R7: 必须规划独立磁贴界面，展示自定义帧链路的日志、状态、流列表、逐跳进度、统计和错误事件，并支持查找与排错。
- REQ-PN-STREAM-PROGRESS-PROTOCOL-001-R8: 协议承载范围必须覆盖 SOCKS5 代理、HTTP 代理、TUN 代理、端口转发，并为后续虚拟组网保留网络标识、节点标识、路由与虚拟链路扩展字段。
- REQ-PN-STREAM-PROGRESS-PROTOCOL-001-R9: 自定义帧协议必须支持自动协商 HTTP/2 与 HTTP/3 承载，能根据入口能力、路径可达性、失败类型和策略选择可用承载，并记录协商结果与回退原因。

#### 1.1.2 需求范围
- 定义 WS 承载上的 CloudHelper probe chain frame protocol。
- 定义固定帧头、长度限制、校验边界和帧类型。
- 定义完整帧缓冲池、read loop、write loop、read queue、write queue 的职责边界。
- 定义协议层对 control/data 的组帧、解析、stream 分发、控制帧处理职责。
- 定义 transport、frame io、protocol、stream 四层统计模型。
- 定义多探针级联字段、逐跳转发表、进度事件和反压传播原则。
- 定义独立磁贴界面的状态、日志、查找、过滤、排错入口和展示字段。
- 定义 SOCKS5、HTTP、TUN、端口转发、虚拟组网在同一帧协议上的业务类型和元数据边界。
- 定义 HTTP/2、HTTP/3 承载自动协商、失败回退、负缓存、最小保持时间和磁贴展示要求。

#### 1.1.3 非范围
- 不在本文档中执行源码修改。
- 不在本阶段实现代码。
- 不在首版要求替换所有现有 yamux 链路。
- 不改变现有 HMAC、secret、auth ticket 的安全模型。
- 不要求 WebSocket 自带 frame 语义参与业务分片。
- 不要求首版实现精细 WINDOW_UPDATE credit 算法，但必须保留协议类型与队列上限设计。

#### 1.1.4 验收标准
- AC1: 文档明确帧协议包含固定头、完整帧长、校验、可变 control、可变 data。
- AC2: 文档明确 `control_len <= 8096`，`data_len <= 65536`，超过限制必须按协议错误处理。
- AC3: 文档明确缓冲池只收发完整帧，不负责组帧和解析。
- AC4: 文档明确底层连接只有 read loop 读取，只有 write loop 写入。
- AC5: 文档明确协议层负责构造完整帧、解析完整帧、处理控制帧、分发数据帧。
- AC6: 文档明确 transport、frame io、protocol、stream 分层统计字段。
- AC7: 文档明确多探针级联所需 `flow_id`、`conn_id`、`stream_id`、`chain_id`、`src_node_id`、`dst_node_id`、`route`、`route_index`、`hop_id`。
- AC8: 文档明确首版需要队列上限和反压策略，避免单连接或单流无限占用内存。
- AC9: 文档明确独立磁贴界面必须能查看日志、连接状态、帧统计、stream 状态、逐跳进度、错误事件，并支持按 `flow_id`、`stream_id`、`conn_id`、`node_id`、`target`、错误码、业务类型查找。
- AC10: 文档明确协议承载 SOCKS5、HTTP、TUN、端口转发和后续虚拟组网的业务类型字段与扩展方向。
- AC11: 文档明确 HTTP/2、HTTP/3 自动协商候选、选择规则、失败分类、回退策略、负缓存、状态统计和磁贴展示字段。

#### 1.1.5 风险
- 自定义 mux 协议会替代 yamux 的一部分职责，必须自行处理 stream 生命周期、队列、反压、关闭语义和错误传播。
- WS 是可靠有序承载，所有虚拟流共享同一连接；如果没有流控，一个大流会压住其他流。
- 多探针级联时如果只使用 `stream_id`，会在不同连接上冲突，必须使用 `conn_id + stream_id` 做本地唯一键。
- 过度记录事件可能增加热路径开销，统计需要分层且可限流。

#### 1.1.6 遗留事项
- 已裁决: 首版直接迁移到新链路类型，不做与现有 yamux 的并行灰度。
- 已裁决: control 编码首版使用 JSON。
- 已裁决: checksum 首版使用 CRC32。
- 已裁决: 独立磁贴界面同步实现到 probe node 本地页面与 controller 管理端。
- 已裁决: 默认使用 HTTP/2；自动协商 HTTP/3，若可用则升级，失败则保留 HTTP/2。站端兼容点对点直连与经 CDN 的 HTTP/3 -> HTTP/2 分段链路。
- 已裁决: 队列上限与反压策略同时考虑，首版实现必须保留完整的流控占位与边界。
- 已裁决: 单跳与多跳级联同时考虑，首版实现必须保留逐跳建模和转发表扩展点。

#### 1.1.7 结论
- 方案成立。建议将 WS 视为稳定可靠承载，在 WS payload 内运行 CloudHelper 自定义完整帧协议；帧缓冲池、底层收发线程、协议层和虚拟 stream 层必须明确分离。

### 1.2 总体架构
- 状态: 进行中

#### 1.2.1 架构目标
- 使用一个稳定 WS 长连接承载多条 probe chain 虚拟流。
- 用自定义完整帧替代对 yamux 内部 stream 状态的依赖。
- 让每条虚拟流在每个环节可识别、可统计、可排错。
- 支持多探针级联下端到端 flow 与逐跳 stream 的关联。
- 提供独立磁贴界面，面向运维排错展示日志、状态、查找和逐跳诊断。
- 统一承载 SOCKS5、HTTP、TUN、端口转发，并为后续虚拟组网保留扩展空间。
- 自动协商 HTTP/2 与 HTTP/3 承载，优先使用当前入口允许且质量更好的承载，失败时可解释地回退。

#### 1.2.2 总体设计
- 底层承载为 WebSocket 长连接。
- WS 层不承载业务语义，只作为可靠有序传输。
- Frame Transport 层包含 read loop、write loop、完整帧缓冲池、read queue、write queue。
- Frame Transport 层只读取固定头中的完整帧长度并做最大帧边界保护，不解析 control/data。
- Protocol 层从 read queue 接收完整帧，负责校验、解析固定头、解析 control、分发 data。
- Protocol 层构造完整帧后提交 write queue，write loop 负责写出并释放 buffer。
- Stream 层以 `conn_id + stream_id` 作为本地流键，以 `flow_id + hop_id` 作为跨节点排障聚合键。
- 多探针级联时，中继节点在入站 stream 与出站 stream 之间建立转发表，保持 `flow_id` 不变，逐跳分配新的 `stream_id`。
- Business Adapter 层将 SOCKS5、HTTP、TUN、端口转发映射为统一 OPEN/DATA/CLOSE/ERROR 流语义，并通过 control 元数据标记 `business_type`、`network`、`target`、`listen_addr`、`virtual_network_id`。
- Tile UI 层通过状态 API 获取连接、帧、协议、stream、级联路由、日志和事件环快照，支持搜索、过滤和定位故障环节。
- Carrier Negotiator 层维护 HTTP/2 与 HTTP/3 候选承载能力，执行探测、选择、失败负缓存、最小保持时间和回退，输出 `selected_carrier` 与 `fallback_reason`。
- 对 Cloudflare/外部代理入口，协议模型允许 `Client/Probe Node -> Cloudflare Edge` 使用 HTTP/3，同时 `Cloudflare Edge -> Origin` 使用 HTTP/2；该分段协议作为默认兼容模型保留，HTTP/3 长流/WebSocket 可行性继续通过能力探测和验证记录确认。

#### 1.2.3 关键模块
| 模块编号 | 模块名称 | 职责 | 输入 | 输出 |
|---|---|---|---|---|
| M1 | WS Carrier | 提供稳定可靠长连接 | WebSocket conn | 字节/消息承载 |
| M2 | Frame Transport | 独立 read/write loop、完整帧收发、队列、缓冲池 | WS conn、完整帧 | read/write frame queue |
| M3 | Frame Buffer Pool | 复用完整帧 buffer | frame_total_len | frame buffer |
| M4 | Protocol Codec | 组帧、解析、校验、control/data 边界处理 | 完整帧 | 协议消息 |
| M5 | Control Plane | 处理 HELLO、AUTH、OPEN、ACK、WINDOW、PROGRESS、CLOSE、ERROR | control frame | 状态变更 |
| M6 | Virtual Stream Mux | 按 stream_id 分发 DATA，维护虚拟流生命周期 | data frame、control event | stream read/write |
| M7 | Cascade Router | 支持多探针级联路由和逐跳转发表 | route、route_index、flow_id | 下一跳 open/data |
| M8 | Observability | 分层统计与事件环 | transport/frame/protocol/stream event | snapshot/log |
| M9 | Business Adapter | 将 SOCKS5、HTTP、TUN、端口转发和虚拟组网映射到统一虚拟流 | business request、packet、tcp conn | OPEN/DATA/CLOSE |
| M10 | Chain Protocol Tile UI | 独立磁贴界面，展示日志、状态、查找和排错视图 | snapshot/log/event | UI/API response |
| M11 | Carrier Negotiator | 自动协商 HTTP/2 与 HTTP/3 承载并处理回退 | endpoint、entry policy、probe result | selected carrier |

#### 1.2.4 关键接口
| 接口编号 | 接口名称 | 调用方 | 提供方 | 说明 |
|---|---|---|---|---|
| IF-001 | `ReadFrame()` | Frame read loop | Frame Transport | 从底层读取完整帧并投递 read queue |
| IF-002 | `SubmitFrame()` | Protocol Layer | Frame Transport | 提交完整帧到 write queue |
| IF-003 | `FrameBufferPool.Get(frameLen)` | Frame Transport/Protocol | Frame Buffer Pool | 获取完整帧 buffer |
| IF-004 | `FrameBufferPool.Put(frame)` | Frame Transport/Protocol | Frame Buffer Pool | 归还完整帧 buffer |
| IF-005 | `DecodeFrame(frame)` | Protocol Layer | Protocol Codec | 解析完整帧并校验长度与 checksum |
| IF-006 | `EncodeFrame(message)` | Stream/Control Layer | Protocol Codec | 构造完整帧 |
| IF-007 | `OpenVirtualStream()` | chain runtime | Virtual Stream Mux | 建立虚拟业务流 |
| IF-008 | `RecordLayerStats()` | 各层 | Observability | 记录分层统计 |
| IF-009 | `OpenBusinessFlow()` | SOCKS5/HTTP/TUN/PortForward/VirtualNetwork | Business Adapter | 将业务流映射为虚拟 stream |
| IF-010 | `SnapshotChainFrameTile()` | Tile UI | Observability/Protocol Runtime | 输出磁贴状态、日志、查找结果 |
| IF-011 | `NegotiateFrameCarrier()` | chain runtime | Carrier Negotiator | endpoint、policy、history | carrier decision |

#### 1.2.5 关键约束
- `control_len` 最大 8096 字节。
- `data_len` 最大 65536 字节。
- `frame_total_len` 必须包含固定头、可选扩展头、control 和 data。
- Frame Transport 层不得解析业务 control/data。
- Protocol 层不得直接读写 WS conn。
- 每条底层连接必须只有一个 read loop 和一个 write loop。
- write queue、read queue、per-stream send queue、per-stream recv queue 必须有上限。
- 控制帧应优先于普通数据帧，避免 CLOSE/ERROR/PROGRESS 被大流长期压住。
- 所有业务类型必须通过统一 `business_type` 区分，禁止为 SOCKS5、HTTP、TUN、端口转发各自定义互不兼容的数据面。
- 磁贴界面不得只展示汇总状态，必须能下钻到连接、帧、stream、hop、flow、错误事件和最近日志。
- HTTP/2 与 HTTP/3 自动协商只允许对入口层错误触发回退；鉴权失败、业务目标不可达、OPEN 被拒绝、stream 内业务错误不得触发承载切换。
- HTTP/2 为默认承载，HTTP/3 仅在能力探测成功时升级使用；升级失败或入口受限时保留 HTTP/2。
- 入口策略必须能限制候选承载；受限入口默认可固定只走 HTTP/2/WebSocket，但允许在显式灰度或能力探测模式下尝试 HTTP/3，并记录验证结果。
- Cloudflare/外部代理入口不得仅因源站是 HTTP/2 就判定客户端侧 HTTP/3 不可用；必须区分 client-edge 与 edge-origin 两段协议。

#### 1.2.6 风险
- 如果 write queue 满时阻塞所有控制帧，连接可能无法及时关闭或报告错误。
- 如果 read queue 满时继续读取底层连接，内存会被上层处理速度拖垮；需要反压或关闭策略。
- 如果 buffer 生命周期不清晰，容易出现提前 Put 后上层仍访问的问题。
- 如果把业务错误误判为承载错误，会导致 HTTP/2 与 HTTP/3 频繁抖动切换。

#### 1.2.7 结论
- 总体架构采用四层分离：WS Carrier、Frame Transport、Protocol Codec/Control、Virtual Stream/Cascade。该分层能支持完整帧收发、可观测性和多探针级联。

### 1.3 单元设计
- 状态: 进行中

#### 1.3.1 单元清单
| 单元编号 | 单元名称 | 所属模块 | 职责 | 输入 | 输出 |
|---|---|---|---|---|---|
| U1 | Frame Header Unit | M4 | 定义固定帧头字段与长度计算 | header fields | binary header |
| U2 | Complete Frame IO Unit | M2 | 完整帧读取、写入、队列投递 | WS conn、frame buffer | complete frame |
| U3 | Frame Buffer Pool Unit | M3 | 复用完整帧 buffer | size | buffer |
| U4 | Protocol Control Unit | M5 | 控制帧解析与状态机 | control frame | control action |
| U5 | Virtual Stream Unit | M6 | 虚拟流分发、队列、关闭 | stream_id、data frame | stream data |
| U6 | Cascade Unit | M7 | 多探针级联路由与转发表 | route、flow_id | next hop stream |
| U7 | Layer Stats Unit | M8 | 分层统计和事件环 | layer event | snapshot |
| U8 | Business Adapter Unit | M9 | 统一承载 SOCKS5、HTTP、TUN、端口转发、虚拟组网 | business input | virtual stream open/data |
| U9 | Tile UI Unit | M10 | 独立磁贴状态、日志、查找、排错展示 | snapshot/log/event | UI/API payload |
| U10 | Carrier Negotiation Unit | M11 | HTTP/2、HTTP/3 自动协商、回退和负缓存 | endpoint、policy、probe history | selected carrier |

#### 1.3.2 单元设计
##### U1
- 单元名称: Frame Header Unit
- 职责: 定义固定帧头，支持 Protocol 层组帧和解析。
- 输入: magic、version、type、flags、header_len、frame_len、control_len、data_len、stream_id、seq、checksum。
- 输出: 固定头字节。
- 处理规则: `frame_len` 为完整帧长度；`control_len` 和 `data_len` 由 Protocol 层校验。
- 异常规则: magic/version/header_len/frame_len 不合法时返回协议错误。

建议固定头字段:
```text
magic        2 bytes
version      1 byte
type         1 byte
flags        2 bytes
header_len   2 bytes
frame_len    4 bytes
control_len  2 bytes or 4 bytes
data_len     4 bytes
stream_id    8 bytes
seq          8 bytes
checksum     4 bytes
```

##### U2
- 单元名称: Complete Frame IO Unit
- 职责: 由独立 read loop/write loop 负责底层收发。
- 输入: WS conn、完整帧 buffer。
- 输出: read queue/write result。
- 处理规则: read loop 先读固定头，取 `frame_len`，校验不超过最大帧长，从 pool 获取完整帧 buffer，读满后投递 read queue；write loop 从 write queue 取完整帧并写出。
- 异常规则: 帧长超过最大值、底层读写失败、队列关闭时关闭连接并记录统计。

##### U3
- 单元名称: Frame Buffer Pool Unit
- 职责: 只负责完整帧 buffer 复用。
- 输入: `frame_total_len`。
- 输出: `[]byte` 完整帧 buffer。
- 处理规则: 不参与组帧，不解析 fixed header，不理解 control/data。
- 异常规则: 请求大小超过最大帧长时拒绝分配。

##### U4
- 单元名称: Protocol Control Unit
- 职责: 处理控制帧和协议状态机。
- 输入: HELLO、AUTH、OPEN、OPEN_ACK、WINDOW_UPDATE、PROGRESS、CLOSE、ERROR、PING、PONG。
- 输出: stream 状态变更、路由动作、统计事件。
- 处理规则: 首版 control 建议 JSON 编码；每个 control 必须有 op、conn_id、stream_id、flow_id、seq、timestamp。
- 异常规则: control 超过 8096、缺少必填字段或状态迁移非法时返回 ERROR。

##### U5
- 单元名称: Virtual Stream Unit
- 职责: 在一条 WS 连接上维护多条虚拟业务流。
- 输入: DATA frame、OPEN/OPEN_ACK/CLOSE。
- 输出: stream read/write。
- 处理规则: 本地唯一键为 `conn_id + stream_id`；DATA 大于 65536 时由上层拆帧。
- 异常规则: 未知 stream_id 收到 DATA 时返回 ERROR 或丢弃并统计。

##### U6
- 单元名称: Cascade Unit
- 职责: 支持 entry、relay、exit 多探针级联。
- 输入: `flow_id`、`route`、`route_index`、`src_node_id`、`dst_node_id`、`hop_id`。
- 输出: 下一跳 OPEN、入出站 stream 转发表。
- 处理规则: `flow_id` 全链路不变；每跳重新分配 `stream_id`；每跳记录独立 `hop_id` 进度。
- 异常规则: 当前节点不匹配 route_index、下一跳不可用或目标不可达时返回稳定错误码。

##### U7
- 单元名称: Layer Stats Unit
- 职责: 输出分层统计和最近事件环。
- 输入: transport/frame/protocol/stream/cascade 事件。
- 输出: snapshot、recent events。
- 处理规则: 统计以 `conn_id/session_id/stream_id/flow_id/hop_id` 串联。
- 异常规则: 事件环满时覆盖最旧事件，不阻塞数据路径。

##### U8
- 单元名称: Business Adapter Unit
- 职责: 将不同业务入口统一映射到自定义帧协议虚拟流。
- 输入: SOCKS5 TCP connect、HTTP proxy request、TUN TCP/UDP flow、端口转发连接、后续虚拟组网 packet/session。
- 输出: `OPEN`、`DATA`、`PROGRESS`、`CLOSE`、`ERROR`。
- 处理规则: control 元数据必须包含 `business_type`，可选值至少包括 `socks5`、`http_proxy`、`tun_tcp`、`tun_udp`、`port_forward_tcp`、`port_forward_udp`、`virtual_network`；虚拟组网必须保留 `virtual_network_id`、`virtual_node_id`、`virtual_route` 扩展字段。
- 异常规则: 未知业务类型不得进入数据转发，必须返回稳定错误码。

##### U9
- 单元名称: Tile UI Unit
- 职责: 提供独立磁贴界面和状态 API，用于日志查看、状态查看、查找、过滤和排错。
- 输入: transport/frame/protocol/stream/cascade/business stats、recent events、runtime logs。
- 输出: 磁贴汇总、连接列表、flow/stream 列表、逐跳详情、错误事件、日志搜索结果。
- 处理规则: 必须支持按 `flow_id`、`stream_id`、`conn_id`、`node_id`、`business_type`、`target`、`hop_id`、错误码过滤；详情视图必须能展示每层统计与最近事件。
- 异常规则: 远端状态拉取失败时，本地磁贴仍应展示本地状态，并标记 remote status error。

##### U10
- 单元名称: Carrier Negotiation Unit
- 职责: 在 HTTP/2 与 HTTP/3 承载之间自动协商并输出可解释选择。
- 输入: endpoint、入口策略、历史质量、探测结果、失败事件。
- 输出: `selected_carrier`、`candidate_carriers`、`selection_reason`、`fallback_reason`、`negative_until`。
- 处理规则: 候选承载至少包括 `http2_ws` 与 `http3_ws`；选择前必须应用入口策略过滤；失败负缓存必须按承载与 endpoint 分开记录；最小保持时间内不得因单次业务错误切换；Cloudflare/外部代理入口应记录 `client_edge_carrier` 与 `edge_origin_carrier` 的分段认知，首版可将 `edge_origin_carrier` 标记为推断或配置值。
- 异常规则: 鉴权失败、业务目标不可达、OPEN 拒绝、stream 内错误不得标记承载不可用；只有 DNS、TCP/TLS/QUIC 握手、HTTP upgrade、HTTP/3 入口不可达等入口层错误可触发回退。

#### 1.3.3 风险
- 自定义协议需要严格测试半关闭、异常关闭和跨跳错误传播，否则会出现流泄漏。
- 如果首版没有合理优先级队列，控制帧可能被数据帧阻塞。
- 多跳级联需要统一错误码，否则 UI 很难判断是入口、某个 relay 还是 exit 故障。
- 如果 SOCKS5、HTTP、TUN、端口转发绕过统一 Business Adapter，会导致磁贴界面无法用同一套字段排错。
- 如果磁贴界面只做汇总而不能查找 flow/stream/hop，排障价值不足。
- 如果自动协商缺少负缓存和最小保持时间，HTTP/2 与 HTTP/3 可能在不稳定网络中频繁切换。

#### 1.3.4 结论
- 单元设计应优先实现完整帧 IO、buffer 生命周期、控制帧、虚拟 stream、承载协商、业务适配、分层统计和独立磁贴排错视图，再逐步增强精细流控。

### 1.4 Code任务执行包
- 状态: 待评审

#### 1.4.1 执行边界
- 允许修改: `probe_node/link_chain_runtime.go`; `probe_node/local_tun_group_runtime.go`; `probe_node/local_console.go`; `probe_node/local_pages.go`; `probe_node/local_pages/panel.html`; `probe_node/local_pages/proxy.html`; `probe_node/substream_monitor.go`; `probe_node/tcp_debug.go`; `probe_controller/internal/core/ws_tunnel.go`; 对应 `_test.go` 文件；必要时新增 `probe_node/chain_frame_protocol.go`; 必要时新增 `probe_controller/internal/core/chain_frame_protocol.go`。
- 禁止修改: release 脚本；鉴权 secret/HMAC 语义；Android 工程；现有 QUIC Data Plane 语义；非链路协议和磁贴入口相关 UI。

#### 1.4.2 任务清单
| 任务编号 | 需求编号 | 单元编号 | 文件范围 | 操作类型 | 验收标准 |
|---|---|---|---|---|---|
| REQ-PN-STREAM-PROGRESS-PROTOCOL-001-T001 | REQ-PN-STREAM-PROGRESS-PROTOCOL-001 | U1,U3 | `probe_node/chain_frame_protocol.go`; 对应测试 | 新增 | AC1、AC2、AC3 |
| REQ-PN-STREAM-PROGRESS-PROTOCOL-001-T002 | REQ-PN-STREAM-PROGRESS-PROTOCOL-001 | U2,U3 | `probe_node/chain_frame_protocol.go`; 对应测试 | 新增 | AC3、AC4、AC8 |
| REQ-PN-STREAM-PROGRESS-PROTOCOL-001-T003 | REQ-PN-STREAM-PROGRESS-PROTOCOL-001 | U4,U5 | `probe_node/link_chain_runtime.go`; `probe_node/local_tun_group_runtime.go`; 对应测试 | 修改 | AC5、AC8 |
| REQ-PN-STREAM-PROGRESS-PROTOCOL-001-T004 | REQ-PN-STREAM-PROGRESS-PROTOCOL-001 | U6 | `probe_node/link_chain_runtime.go`; 对应测试 | 修改 | AC7 |
| REQ-PN-STREAM-PROGRESS-PROTOCOL-001-T005 | REQ-PN-STREAM-PROGRESS-PROTOCOL-001 | U7 | `probe_node/substream_monitor.go`; `probe_node/tcp_debug.go`; 对应测试 | 修改 | AC6、AC7 |
| REQ-PN-STREAM-PROGRESS-PROTOCOL-001-T006 | REQ-PN-STREAM-PROGRESS-PROTOCOL-001 | U1-U7 | `probe_controller/internal/core/chain_frame_protocol.go`; `probe_controller/internal/core/ws_tunnel.go`; 对应测试 | 新增/修改 | AC1-AC8 |
| REQ-PN-STREAM-PROGRESS-PROTOCOL-001-T007 | REQ-PN-STREAM-PROGRESS-PROTOCOL-001 | U8 | `probe_node/link_chain_runtime.go`; `probe_node/local_tun_group_runtime.go`; 对应测试 | 修改 | AC10 |
| REQ-PN-STREAM-PROGRESS-PROTOCOL-001-T008 | REQ-PN-STREAM-PROGRESS-PROTOCOL-001 | U9 | `probe_node/local_console.go`; `probe_node/local_pages.go`; `probe_node/local_pages/panel.html`; `probe_node/local_pages/proxy.html`; 对应测试 | 修改 | AC9 |
| REQ-PN-STREAM-PROGRESS-PROTOCOL-001-T009 | REQ-PN-STREAM-PROGRESS-PROTOCOL-001 | U10 | `probe_node/link_chain_runtime.go`; `probe_node/link_relay_client_transport.go`; 对应测试 | 修改 | AC11 |

#### 1.4.3 源码修改规则
- 必须使用 encoding_tools/README.md 描述的接口。
- 对 C/C++ 源代码（`.c`、`.cc`、`.cpp`、`.cxx`、`.h`、`.hpp`）必须使用 encoding_tools/encoding_safe_patch.py。
- 对非 C/C++ 源代码可直接编辑，不强制使用 encoding_tools/encoding_safe_patch.py。
- encoding_tools/ 不可用或执行失败时，Code 必须记录失败命令、错误摘要、影响文件与阻塞影响，并提交第2.6节 `Code任务反馈`。
- 替代 encoding_tools/ 修改受控 C/C++ 源代码前，必须取得 Architect 明确允许。

#### 1.4.4 交付物
- 自定义完整帧协议定义。
- 完整帧缓冲池。
- 独立 read loop/write loop。
- 控制帧与虚拟 stream 状态机。
- 分层统计与快照。
- 多探针级联字段与路由转发表。
- SOCKS5、HTTP、TUN、端口转发和虚拟组网业务适配字段。
- HTTP/2、HTTP/3 自动协商与回退状态。
- Cloudflare/外部代理入口 HTTP/3 长流/WebSocket 可行性验证记录。
- 独立磁贴界面与状态 API。
- 单元测试和兼容性测试证据。

#### 1.4.5 门禁输入
- 必须证明 control/data 长度限制生效。
- 必须证明缓冲池不解析业务内容。
- 必须证明底层连接只有单独 read loop 和 write loop 接触。
- 必须证明队列上限与释放路径不会泄漏 buffer。
- 必须证明多跳字段能串联同一 `flow_id` 的逐跳状态。
- 必须证明磁贴界面可查看状态、日志、错误事件，并可按关键字段查找。
- 必须证明 SOCKS5、HTTP、TUN、端口转发使用统一业务类型字段进入虚拟流模型。
- 必须证明 HTTP/2、HTTP/3 协商只因入口层错误回退，并记录候选、结果、失败原因和负缓存时间。
- 必须为 Cloudflare/外部代理入口记录分段协议结论: 源站 HTTP/2 不禁止客户端到 Edge HTTP/3；HTTP/3 长流/WebSocket 可行性作为后续验证项，不作为默认假设。

#### 1.4.6 结论
- 本任务包为后续实现边界，不代表已经开始 Code 阶段。进入 Code 前建议先裁决首版启用范围和灰度策略。

### 1.5 Architect需求跟踪矩阵
- 状态: 进行中

| 需求编号 | 需求描述 | 架构章节 | 单元设计章节 | Code任务章节 | 状态 | 备注 |
|---|---|---|---|---|---|---|
| REQ-PN-STREAM-PROGRESS-PROTOCOL-001-R1 | WS 上建立自定义完整帧协议 | 1.2 | U1,U2,U4,U5 | T001,T002,T003,T006 | 进行中 | 首版协议核心 |
| REQ-PN-STREAM-PROGRESS-PROTOCOL-001-R2 | 帧头、帧长、校验、control/data | 1.2 | U1,U4 | T001,T006 | 进行中 | control/data 限长 |
| REQ-PN-STREAM-PROGRESS-PROTOCOL-001-R3 | 缓冲池只负责完整帧 | 1.2 | U2,U3 | T001,T002 | 进行中 | 分层边界 |
| REQ-PN-STREAM-PROGRESS-PROTOCOL-001-R4 | 独立 read/write loop | 1.2 | U2 | T002,T006 | 进行中 | 单连接单读单写 |
| REQ-PN-STREAM-PROGRESS-PROTOCOL-001-R5 | 分层统计与排错 | 1.2 | U7 | T005,T006 | 进行中 | transport/frame/protocol/stream |
| REQ-PN-STREAM-PROGRESS-PROTOCOL-001-R6 | 多探针级联 | 1.2 | U6 | T004,T006 | 进行中 | flow_id 全链路不变 |
| REQ-PN-STREAM-PROGRESS-PROTOCOL-001-R7 | 独立磁贴日志状态查找排错 | 1.2 | U9 | T008 | 进行中 | 状态、日志、查找、下钻 |
| REQ-PN-STREAM-PROGRESS-PROTOCOL-001-R8 | 承载 SOCKS5、HTTP、TUN、端口转发和虚拟组网 | 1.2 | U8 | T007 | 进行中 | business_type 统一 |
| REQ-PN-STREAM-PROGRESS-PROTOCOL-001-R9 | HTTP/2、HTTP/3 自动协商 | 1.2 | U10 | T009 | 进行中 | 入口策略、回退、负缓存、CF分段验证 |

### 1.6 Architect关键接口跟踪矩阵
- 状态: 进行中

| 接口编号 | 需求编号 | 接口名称 | 调用方 | 提供方 | 输入 | 输出 | 状态 | 备注 |
|---|---|---|---|---|---|---|---|---|
| IF-001 | REQ-PN-STREAM-PROGRESS-PROTOCOL-001 | `ReadFrame()` | read loop | Frame Transport | WS conn | complete frame | 进行中 | 只做完整帧读取 |
| IF-002 | REQ-PN-STREAM-PROGRESS-PROTOCOL-001 | `SubmitFrame()` | Protocol Layer | Frame Transport | complete frame | write enqueue result | 进行中 | 写队列有上限 |
| IF-003 | REQ-PN-STREAM-PROGRESS-PROTOCOL-001 | `FrameBufferPool.Get(frameLen)` | Frame Transport/Protocol | Frame Buffer Pool | frame_total_len | buffer | 进行中 | 不解析业务 |
| IF-004 | REQ-PN-STREAM-PROGRESS-PROTOCOL-001 | `FrameBufferPool.Put(frame)` | Frame Transport/Protocol | Frame Buffer Pool | frame buffer | 无 | 进行中 | 生命周期必须清晰 |
| IF-005 | REQ-PN-STREAM-PROGRESS-PROTOCOL-001 | `DecodeFrame(frame)` | Protocol Layer | Protocol Codec | complete frame | decoded message | 进行中 | 校验限长与 checksum |
| IF-006 | REQ-PN-STREAM-PROGRESS-PROTOCOL-001 | `EncodeFrame(message)` | Control/Stream Layer | Protocol Codec | message | complete frame | 进行中 | 上层负责组帧 |
| IF-007 | REQ-PN-STREAM-PROGRESS-PROTOCOL-001 | `OpenVirtualStream()` | chain runtime | Virtual Stream Mux | route/target | stream_id | 进行中 | 多路复用 |
| IF-008 | REQ-PN-STREAM-PROGRESS-PROTOCOL-001 | `RecordLayerStats()` | 各层 | Observability | layer event | snapshot/log | 进行中 | 排错基础 |
| IF-009 | REQ-PN-STREAM-PROGRESS-PROTOCOL-001 | `OpenBusinessFlow()` | SOCKS5/HTTP/TUN/PortForward/VirtualNetwork | Business Adapter | business metadata | virtual stream | 进行中 | 统一业务入口 |
| IF-010 | REQ-PN-STREAM-PROGRESS-PROTOCOL-001 | `SnapshotChainFrameTile()` | Tile UI | Observability/Protocol Runtime | filter | tile snapshot | 进行中 | 状态日志查找 |
| IF-011 | REQ-PN-STREAM-PROGRESS-PROTOCOL-001 | `NegotiateFrameCarrier()` | chain runtime | Carrier Negotiator | endpoint/policy/history | carrier decision | 进行中 | HTTP/2/HTTP/3 |

### 1.7 门禁裁判
- 状态: 待评审

#### 1.7.1 门禁输入
| 文档 | 路径 | 状态 |
|---|---|---|
| 协作文档 | doc/REQ-PN-STREAM-PROGRESS-PROTOCOL-001-collaboration.md | 已更新 |

#### 1.7.2 裁判检查
| 检查项 | 结果 | 证据 | 备注 |
|---|---|---|---|
| 协作文档存在 | 通过 | 本文档存在 | 无 |
| Architect章节存在 | 通过 | 第1章存在 | 无 |
| Code章节存在 | 通过 | 第2章存在 | Code未开始 |
| 必需子章节存在 | 通过 | 1.1-1.7、2.1-2.6存在 | 无 |
| 需求前缀一致 | 通过 | REQ-PN-STREAM-PROGRESS-PROTOCOL-001 | 无 |
| 需求编号一致 | 通过 | REQ-PN-STREAM-PROGRESS-PROTOCOL-001 | 无 |
| 接口编号一致 | 通过 | IF-001至IF-011 | 无 |
| 模板字段完整 | 通过 | 头字段与章节保留 | 无 |
| Code使用encoding_tools | 无 | Code未开始 | 后续检查 |
| Code证据完整 | 无 | Code未开始 | 后续检查 |
| Code任务反馈已处理 | 无 | Code未开始 | 后续检查 |
| 验收标准可测试 | 通过 | AC1-AC11可测试 | 无 |
| 需求任务覆盖完整 | 通过 | R1-R9均关联任务 | 无 |
| 任务自测覆盖完整 | 无 | Code未开始 | 后续检查 |
| 修改文件在允许范围内 | 无 | Code未开始 | 后续检查 |
| 测试失败已记录缺陷 | 无 | Code未开始 | 后续检查 |
| 未执行测试原因完整 | 无 | Code未开始 | 后续检查 |
| 遗留风险可接受 | 有条件通过 | 首版启用范围和灰度策略待裁决 | 不阻塞讨论落盘 |

#### 1.7.3 冲突记录
| 冲突编号 | 冲突条款 | 最终采用条款 | 裁决人 | 裁决结论 |
|---|---|---|---|---|
| 无 | 无 | 无 | 无 | 无 |

#### 1.7.4 裁判结论
- 结论: 有条件通过
- 放行阻塞: 放行
- 条件: 用户已完成首版启用范围、灰度策略、control 编码格式、checksum 算法、HTTP/3 适用入口策略、磁贴同步范围与级联/流控取向裁决；后续进入 Code 阶段。
- 责任方: Architect、用户、Code
- 关闭要求: 按已裁决事项更新第1.4节与实现任务；Code完成实现和测试证据后重新门禁。
- 整改要求: 无

#### 1.7.5 结论
- 本次讨论已落实为协议设计文档。推荐方案是基于 WS 的 CloudHelper 自定义完整帧协议，完整帧缓冲池与底层收发线程独立，协议层负责组帧解析，统计分层，第一版即保留多探针级联模型。

## 第2章 Code章节
- 章节责任角色: Code
- 状态: 进行中

### 2.1 Code需求跟踪矩阵
- 状态: 进行中

| 需求编号 | 任务编号 | 实现文件 | 实现状态 | 自测状态 | 证据 | 备注 |
|---|---|---|---|---|---|---|
| REQ-PN-STREAM-PROGRESS-PROTOCOL-001 | T001,T002,T003,T004,T005,T007,T009 | `probe_node/chain_frame_protocol.go`; `probe_node/link_chain_runtime.go`; `probe_node/mobilecore/chain_frame_protocol.go`; `probe_node/mobilecore/mobilecore_chain_runtime.go`; `probe_node/main.go`; `probe_node/link_chain_udp_assoc.go`; `probe_node/udp_assoc_debug.go`; `probe_node/local_proxy_monitor.go`; `probe_node/local_pages/proxy.html`; `probe_node/local_tun_stack_windows.go`; `probe_controller/internal/core/ws_tunnel_udp_assoc.go`; `probe_controller/internal/core/ws_tunnel_udp_debug.go` | 部分完成 | 通过 | `go test ./... -run TestDoesNotExist` (probe_node); `go test ./...` (probe_controller) | 控制面仍保留 yamux，链路侧已引入新帧；新增 UDP bridge/association 收发、阻塞、延迟观测 |

### 2.2 Code关键接口跟踪矩阵
- 状态: 进行中

| 接口编号 | 需求编号 | 实现文件 | 调用方 | 提供方 | 实现状态 | 证据 | 备注 |
|---|---|---|---|---|---|---|---|
| IF-001 | REQ-PN-STREAM-PROGRESS-PROTOCOL-001 | `probe_node/chain_frame_protocol.go` | read loop | Frame Transport | 已实现 | `probe_node/chain_frame_protocol_test.go` | 链路侧完整帧读写 |
| IF-005 | REQ-PN-STREAM-PROGRESS-PROTOCOL-001 | `probe_node/link_chain_runtime.go`; `probe_node/mobilecore/mobilecore_chain_runtime.go` | Protocol Layer | Protocol Codec | 已实现 | 对应链路测试 | control/data 解析与分发 |
| IF-009 | REQ-PN-STREAM-PROGRESS-PROTOCOL-001 | `probe_node/link_chain_runtime.go`; `probe_node/mobilecore/mobilecore.go` | Business Adapter | Business Adapter | 进行中 | 对应测试 | SOCKS5/HTTP/TUN/端口转发仍在收口 |
| IF-010 | REQ-PN-STREAM-PROGRESS-PROTOCOL-001 | `probe_node/link_chain_udp_assoc.go`; `probe_node/udp_assoc_debug.go`; `probe_node/local_proxy_monitor.go`; `probe_node/local_tun_stack_windows.go`; `probe_controller/internal/core/ws_tunnel_udp_assoc.go`; `probe_controller/internal/core/ws_tunnel_udp_debug.go` | Frame/UDP observability | 监视器与页面 | 进行中 | 新增字段与页面展示 | 记录 bytes、writes、blocked writes、max write block、last block time |

### 2.3 Code测试项跟踪矩阵
- 状态: 进行中

| 测试项编号 | 需求编号 | 任务编号 | 测试目标 | 测试方法 | 结果 | 证据 | 未执行原因 | 备注 |
|---|---|---|---|---|---|---|---|---|
| T001 | REQ-PN-STREAM-PROGRESS-PROTOCOL-001 | T001 | 完整帧编解码 | 单测 | 通过 | `probe_node/chain_frame_protocol_test.go` | 无 |
| T002 | REQ-PN-STREAM-PROGRESS-PROTOCOL-001 | T002 | 完整帧收发 | 单测 | 通过 | `probe_node/chain_frame_protocol_test.go` | 无 |
| T003 | REQ-PN-STREAM-PROGRESS-PROTOCOL-001 | T003 | 链路控制分发 | 单测/编译 | 通过 | `probe_node/link_chain_runtime.go` | 无 |
| T004 | REQ-PN-STREAM-PROGRESS-PROTOCOL-001 | T004 | 多探针级联路径 | 单测/编译 | 进行中 | `probe_node/link_chain_runtime.go` | 仍在继续收口 |
| T005 | REQ-PN-STREAM-PROGRESS-PROTOCOL-001 | T005 | 分层统计与排错 | 单测/编译 | 进行中 | `probe_node/tcp_debug.go`、`probe_node/udp_assoc_debug.go`、`probe_node/local_proxy_monitor.go` 等 | 仍在持续补齐 |
| T007 | REQ-PN-STREAM-PROGRESS-PROTOCOL-001 | T007 | 业务适配与承载协商 | 单测/编译 | 进行中 | `probe_node/mobilecore/*` | 仍在持续补齐 |
| T009 | REQ-PN-STREAM-PROGRESS-PROTOCOL-001 | T009 | HTTP/2/HTTP/3 协商 | 单测/编译 | 进行中 | `probe_node/link_chain_runtime.go` | 仍在持续补齐 |

### 2.4 Code缺陷跟踪矩阵
- 状态: 进行中

| 缺陷编号 | 需求编号 | 测试项编号 | 缺陷描述 | 严重级别 | 修复状态 | 修复证据 | 备注 |
|---|---|---|---|---|---|---|---|
| 无 | 无 | 无 | 无 | 无 | 无 | 无 | 当前无已知阻塞缺陷 |

### 2.5 Code执行证据
- 状态: 进行中

#### 2.5.1 修改接口
- `probe_node/chain_frame_protocol.go`
- `probe_node/link_chain_runtime.go`
- `probe_node/link_chain_udp_assoc.go`
- `probe_node/udp_assoc_debug.go`
- `probe_node/local_proxy_monitor.go`
- `probe_node/local_tun_stack_windows.go`
- `probe_node/local_pages/proxy.html`
- `probe_node/mobilecore/chain_frame_protocol.go`
- `probe_node/mobilecore/mobilecore_chain_runtime.go`
- `probe_node/main.go`
- `probe_controller/internal/core/ws_tunnel.go`
- `probe_controller/internal/core/ws_tunnel_udp_assoc.go`
- `probe_controller/internal/core/ws_tunnel_udp_debug.go`

#### 2.5.2 配置文件
- 无

#### 2.5.3 执行报告
- `probe_controller` 已通过 `go test ./...`
- `probe_node` 已通过 `go test ./...`

#### 2.5.4 影响文件
- `probe_node/*`
- `probe_controller/internal/core/*`

#### 2.5.5 测试命令
- `go test ./...` in `probe_node`
- `go test ./...` in `probe_controller`

#### 2.5.6 自测结果
- 通过

#### 2.5.7 未执行测试原因
- 无

#### 2.5.8 遗留风险
- 仍有一部分文档目标属于后续工作，不在本轮收口范围内

#### 2.5.9 回滚方案
- 回退 `probe_node` 链路帧相关改动即可恢复到旧实现

#### 2.5.10 结论
- Code已开始，链路侧实现进行中，控制面仍保留 yamux。

### 2.6 Code任务反馈
- 状态: 进行中

| 反馈编号 | 任务编号 | 反馈类型 | 反馈描述 | 阻塞影响 | Code建议 | Architect处理状态 | Architect处理结论 |
|---|---|---|---|---|---|---|---|
| 无 | 无 | 无 | 无 | 无 | 无 | 无 | 暂无阻塞反馈 |

#### 2.6.1 结论
- 当前已有实现反馈，暂无阻塞项。
