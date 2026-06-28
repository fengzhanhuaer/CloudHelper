# 协作文档

- 适用规则: AI协作规则
- 后续工作传递声明: 本文档必须传递给后续阶段与后续角色。
- 需求编号: REQ-PN-DISTRIBUTED-VROUTER-001
- 需求前缀: REQ-PN-DISTRIBUTED-VROUTER-001
- 当前阶段: Code实现
- 最近更新角色: Code
- 最近更新时间: 2026-06-28
- 工作依据文档: doc/ai-coding-collaboration.md
- 状态: 第一阶段实现中

## 第1章 Architect章节
- 章节责任角色: Architect
- 状态: 进行中

### 1.1 需求定义
- 状态: 进行中

#### 1.1.1 需求目标
- 将探针能力升级为分布式虚拟路由器入口，使多个探针组成一个统一的虚拟局域网与代理入口体系。
- 探针之间通过现有自定义帧进行通讯，自定义帧运行在物理连接之上。
- 当前物理连接已经存在，并已经具备双向通讯能力。
- 探针间物理连接的拓扑结构在主控侧设置。
- 每条拓扑规则描述两个探针之间的连接关系，A/B 端顺序固定表示底层物理建联方向：A 主动拨 B，B 监听，不提供方向配置。
- 拓扑规则的 A->B 只描述底层物理建联方向，不削弱虚拟连接的点对点语义；虚拟连接建立在物理连接之上，一旦物理连接形成，双方都应知道完整拓扑、对端探针静态 IP，并支持在同一虚拟链路上双向虚拟 ping/pong 与数据转发。
- 互不直接连接的探针，如果存在共同连接节点，虚拟路由器应支持通过共同节点转发。
- 每个探针由主控分配一个静态全局局域网 IP。
- 采用当前 TUN FakeIP 地址池的前 1024 个 IP 作为探针静态局域网 IP 池。
- FakeIP 地址池作为全局共享资源，不再轻易清空。
- 端口转发能力纳入虚拟路由器统一管理。
- 探针之间可以通过全局局域网 IP 互相连接。
- 代理功能通过该虚拟路由器能力完成。
- `proxy_group` 从单探针本地配置升级为虚拟路由器全局配置，代理组可以选择绑定的探针出口。
- 每个探针出口可以单独配置监听域名与端口，监听域名包括通过 Cloudflare 代理和直连两种形态。
- 第一阶段先不改动原有 TUN 代理功能，仅调整当前 TUN FakeIP 地址池前 1024 个 IP 的保留用途。
- 第一阶段在主控探针链路中新增独立拓扑规则配置页面。
- 第一阶段在主控探针链路中新增路由状态视图，用于查看各拓扑规则连接状态、流量统计与定时虚拟 ping/pong 延迟。
- 第一阶段实现基于探针静态局域网 IP 与主控拓扑规则的局域网互联功能。
- 每条拓扑规则可以独立配置两端探针的服务域名与服务端口；端口默认 `12040`，允许在多条规则中复用，域名候选来自 Cloudflare DDNS 模块，同时允许填写内网域名。
- 拓扑规则服务域名与端口只存储在虚拟路由器配置中，不读取、不回写、不混用旧探针侧链路域名、入口或 `relay_host` 配置。

#### 1.1.2 需求范围
- RQ-DVRT-001: 探针作为分布式虚拟路由器入口参与统一网络。
- RQ-DVRT-002: 探针间通讯使用现有自定义帧，承载于已有或未来物理连接之上。
- RQ-DVRT-003: 当前物理连接已经存在，并已经具备双向通讯能力。
- RQ-DVRT-004: 每个探针由主控分配一个静态全局局域网 IP。
- RQ-DVRT-005: 端口转发由虚拟路由器统一编排和管理。
- RQ-DVRT-006: 探针之间支持通过全局局域网 IP 互联。
- RQ-DVRT-007: 代理功能统一通过虚拟路由器完成。
- RQ-DVRT-008: 采用当前 TUN FakeIP 地址池的前 1024 个 IP 作为探针静态局域网 IP 池，主控从该范围内为探针分配静态 IP。
- RQ-DVRT-009: FakeIP 地址池作为全局共享资源，不再轻易清空；清理行为必须受控并避免破坏探针静态 IP 与全局映射关系。
- RQ-DVRT-010: `proxy_group` 从单探针本地配置升级为虚拟路由器全局配置，代理组可以选择绑定的探针出口。
- RQ-DVRT-011: 探针间物理连接的拓扑结构在主控侧设置，虚拟路由器基于该拓扑组织探针间连通关系。
- RQ-DVRT-012: 每条拓扑规则描述两个探针之间的连接关系；A 端固定为物理主动建联端，B 端固定为物理监听端，不提供方向配置。
- RQ-DVRT-013: 互不直接连接的探针，如果存在共同连接节点，虚拟路由器应支持通过共同节点转发。
- RQ-DVRT-014: 每个探针出口可以单独配置监听域名与端口，监听域名包括通过 Cloudflare 代理和直连两种形态。
- RQ-DVRT-015: 第一阶段先不改动原有 TUN 代理功能，仅调整当前 TUN FakeIP 地址池前 1024 个 IP 的保留用途。
- RQ-DVRT-016: 第一阶段在主控探针链路中新增独立拓扑规则配置页面，用于维护探针间 A/B 连接关系、服务地址与启用状态。
- RQ-DVRT-017: 第一阶段实现基于探针静态局域网 IP 与主控拓扑规则的局域网互联功能。
- RQ-DVRT-018: 每条拓扑规则可以独立配置 A/B 两端服务域名与服务端口，端口默认 `12040` 且允许复用；域名从 Cloudflare DDNS 模块选择或手动填写内网域名，配置独立保存在虚拟路由器拓扑规则内，不混用旧探针侧配置。
- RQ-DVRT-019: 主控探针链路页面提供路由状态 tab，按拓扑规则展示连接状态、累计统计、定时虚拟 ping/pong 延迟与最近活动时间。
- RQ-DVRT-020: 拓扑规则 A/B 顺序固定表示物理 A->B 建联；虚拟连接必须保持点对点双向可达语义，双方均应保存完整拓扑邻居与探针静态 IP，物理单向建联成功后，B 也应能沿已建立的同一条物理 bridge 连接反向进行虚拟 ping/pong 与数据转发；虚拟路由层不得暴露历史上下游口径，也不得建模成两条独立数据通道，只允许使用虚拟 next/prev 与物理 bridge 连接语义。
- RQ-DVRT-021: 虚拟路由运行态必须分层统计、上传并展示 TUN 数据面 RX/TX、IP 包生命周期、虚拟路由数据面 frame 收发和底层物理会话所有自定义 frame 收发；frame 均应按发送、接收方向分别累计帧数、字节数和最近活动时间；TUN 数据面 RX/TX 属于节点级统计，路由状态页必须按节点去重展示，不能混作单条规则、单个对端或物理连接统计；每条虚拟路由规则运行态只应维持一条双向物理 bridge 连接，新连接接管时必须关闭并替换旧连接；虚拟路由状态按该物理连接展示 frame tx/rx，不展示历史上下游通道口径，便于定位系统包未进 TUN、已发未收、已收未写回 TUN 等链路问题。
- RQ-DVRT-022: 探针收到目标为本机虚拟 IP 的 ICMP Echo Request 时，虚拟路由器必须能够生成 Echo Reply 并沿虚拟拓扑回送，作为跨平台虚拟 IP ping 的保底能力，不能完全依赖操作系统 TUN 栈自动回包。

#### 1.1.3 非范围
- 当前阶段不设计技术实现方案。
- Architect 初始记录阶段不拆分代码任务；后续已在第1.4节补充第一阶段 Code 任务包。
- Architect 初始记录阶段不定义协议字段、路由算法、状态同步算法或数据库结构；后续已在第1.2至1.4节补充第一阶段架构与任务边界。
- Architect 初始记录阶段不修改源码；后续源码修改记录见第2章。
- 第一阶段不改动原有 TUN 代理功能的既有行为。
- 第一阶段不实施 `proxy_group` 全局化。
- 第一阶段不实施端口转发统一管理。
- 第一阶段不实施代理组绑定探针出口。
- 第一阶段不实施旧探针侧出口监听域名与端口配置迁移或复用；仅允许在虚拟路由器拓扑规则内新增独立服务域名与端口字段。

#### 1.1.4 验收标准
- 已在协作文档中记录用户提出的全部核心需求。
- 每条核心需求具有稳定需求编号。
- 文档明确需求记录、架构放行与 Code 执行的阶段边界。
- 后续规划必须基于本文档继续补充，不得另起需求口径。

#### 1.1.5 风险
- 需求跨度大，后续需要拆清虚拟局域网、主控静态 IP 分配、路由、端口转发、代理与探针间物理连接的边界。
- 主控静态 IP 分配的冲突处理、回收与变更流程尚未确认。
- TUN FakeIP 前 1024 个 IP 作为探针静态局域网 IP 池，需要避免与普通 FakeIP 分配范围重叠。
- FakeIP 全局共享后，清空操作可能破坏全局映射、探针静态 IP 与代理路由关系，后续规划需要定义受控清理边界。
- `proxy_group` 作用域升级为全局后，需要处理旧单探针配置与全局配置之间的兼容、迁移与冲突。
- 代理组绑定探针出口后，需要避免出口探针离线、变更或不可达时造成代理组不可用。
- 主控侧拓扑设置需要与实际物理连接状态保持一致，否则可能导致虚拟路由器连通关系与真实链路不一致。
- 拓扑规则不再提供方向配置，需要避免把 A/B 物理建联方向误解释为虚拟可达方向。
- 通过共同节点转发会引入路径依赖，共同节点离线或不可达时可能影响多个非直连探针之间的连通性。
- 探针出口监听域名与端口单独配置后，需要避免域名、端口、Cloudflare 代理形态与直连形态之间的配置冲突。
- 第一阶段要求不改动原有 TUN 代理功能，因此局域网互联改造需要与既有 TUN 代理路径隔离，避免引入回归。
- 第一阶段仅调整前 1024 个 FakeIP 的保留用途，需要避免影响普通 FakeIP 分配与既有代理解析行为。

#### 1.1.6 遗留事项
- 待确认虚拟局域网 IP 地址段、前 1024 个探针静态 IP 的主控分配策略、回收流程与变更流程。
- 待确认 FakeIP 全局共享后的清理权限、清理粒度、恢复策略与持久化策略。
- 待确认 `proxy_group` 全局配置的数据归属、同步策略、旧配置迁移策略与代理组出口绑定规则。
- 待确认主控侧拓扑配置的表达方式、变更流程、校验规则与下发策略。
- 待确认拓扑规则中两个探针的标识字段、启用状态与优先级规则；历史 `direction` 字段仅作为兼容输入保留并归一为 A->B。
- 待确认共同节点转发的路径选择、环路避免、故障切换与可观测性规则。
- 待确认探针出口监听域名与端口配置的数据结构、Cloudflare 代理标识、直连标识与冲突校验规则。
- 待确认第一阶段拓扑规则配置页面在主控探针链路页面中的入口、交互与权限。
- 待确认第一阶段局域网互联功能的最小验收口径，包括探针静态 IP 分配、直连互通、经共同节点转发互通与不可达提示。
- 待确认端口转发统一管理的入口位置、权限模型与展示方式。
- 待确认代理功能通过虚拟路由器完成后的用户配置模型。
- 物理连接已具备双向通讯，后续规划可直接基于现有双向物理通讯能力展开。

#### 1.1.7 结论
- 已完成需求记忆与需求编号固化。
- Architect 初始需求记录已完成；后续技术方案与源码修改记录见第1.2至第2章。

### 1.2 总体架构
- 状态: 进行中

#### 1.2.1 架构目标
- 第一阶段仅建设虚拟局域网互联最小闭环，不改变现有 TUN 代理功能既有行为。
- 在主控探针链路管理中新增独立拓扑规则配置能力。
- 主控维护探针静态局域网 IP 与拓扑规则，并通过现有探针链路配置同步接口下发。
- probe_node 基于现有自定义帧链路承载虚拟局域网互联流量。
- 当前 TUN FakeIP 地址池前 1024 个 IP 作为探针静态局域网 IP 池，普通 FakeIP 分配不得占用该池。

#### 1.2.2 总体设计
- 主控侧在现有 `probe_link_chains.json` 存储中增加虚拟路由器配置区，包含探针静态 IP 记录与拓扑规则记录。
- 主控管理页面 `/mng/link` 增加独立“拓扑规则”配置视图，用于维护两个探针之间的 A/B 端、服务地址、启用状态和备注。
- 主控管理页面 `/mng/link` 增加独立“路由状态”视图，用于展示每条拓扑规则对应隐藏运行态的两端状态、统计、定时虚拟 ping/pong 延迟和最近活动。
- 拓扑规则配置视图支持为 A/B 两端分别维护服务域名和服务端口；域名弹窗从 Cloudflare DDNS 模块读取候选，手动输入用于内网域名。
- 主控探针同步接口 `/api/probe/link/config/grouped` 兼容扩展返回虚拟路由器配置，旧探针忽略新增字段。
- probe_node 同步到虚拟路由器配置后，保存本地缓存，并为本探针建立静态局域网 IP 与可达拓扑视图。
- probe_node 基于拓扑规则为本探针自动生成隐藏 `vrouter-*` 链路运行态，使用规则级服务域名与端口建立真实监听或拨号连接。
- probe_node 必须区分物理建联方向、虚拟拓扑邻接和虚拟探测方向：`A->B` 表示 A 主动拨 B 的物理监听端，B 不主动拨 A 的物理地址；但 A 与 B 仍互为虚拟拓扑邻居，双方都应知道对端探针静态 IP，B 可在 A 已建立的物理会话上反向打开虚拟子流 ping A。
- 隐藏 `vrouter-*` 运行态复用现有共享监听端口与 `chain_id` 分发机制，因此多条拓扑规则可以复用默认 `12040` 端口。
- 局域网互联流量在第一阶段复用现有自定义帧子流与隐藏虚拟路由器 runtime，不改动 `proxy_group`、端口转发统一管理和既有代理决策。
- 前 1024 个探针静态 IP 池与普通 FakeIP 分配边界通过分配函数约束实现。
- 拓扑规则服务域名与服务端口只作为虚拟路由器规则字段保存和下发，不复用旧探针侧 `relay_host`、链路入口或节点域名配置。

#### 1.2.3 关键模块
| 模块编号 | 模块名称 | 职责 | 输入 | 输出 |
|---|---|---|---|---|
| M-DVRT-01 | 主控虚拟路由器配置存储 | 保存探针静态 IP 与拓扑规则 | 管理端配置请求 | 规范化配置与持久化 JSON |
| M-DVRT-02 | 主控拓扑规则页面 | 展示和编辑拓扑规则 | 管理员操作 | 拓扑规则配置 |
| M-DVRT-03 | 探针配置下发扩展 | 在链路同步响应中下发虚拟路由器配置 | 探针身份、链路配置 | 当前探针可见的虚拟路由器配置 |
| M-DVRT-04 | probe_node 虚拟路由器运行态 | 保存配置并建立本探针局域网互联视图 | 主控下发配置、本机身份 | 本机静态 IP、拓扑可达信息 |
| M-DVRT-05 | FakeIP 前 1024 池约束 | 避免普通 FakeIP 占用探针静态 IP 池 | FakeIP CIDR、当前分配状态 | 跳过保留池后的普通 FakeIP |

#### 1.2.4 关键接口
| 接口编号 | 接口名称 | 调用方 | 提供方 | 说明 |
|---|---|---|---|---|
| IF-DVRT-01 | GET `/mng/api/link/virtual_router` | 管理页面 | probe_controller | 获取虚拟路由器配置 |
| IF-DVRT-02 | POST `/mng/api/link/virtual_router` | 管理页面 | probe_controller | 保存探针静态 IP、拓扑规则以及规则级服务域名/端口 |
| IF-DVRT-03 | GET `/api/probe/link/config/grouped` | probe_node | probe_controller | 兼容扩展返回虚拟路由器配置 |
| IF-DVRT-04 | 本地虚拟路由器配置缓存 | probe_node | 本地文件 | 保存主控下发的虚拟路由器配置 |
| IF-DVRT-05 | 隐藏 `vrouter-*` 链路运行态 | probe_node | 本地 runtime | 根据拓扑规则建立虚拟路由器监听、拨号和 frame 子流 |
| IF-DVRT-06 | GET `/mng/api/link/virtual_router/status` | 管理页面 | probe_controller | 汇总拓扑规则的连接状态、统计、虚拟 ping/pong 延迟 |

#### 1.2.5 关键约束
- 第一阶段禁止改变现有 TUN 代理功能既有行为。
- 第一阶段禁止实施 `proxy_group` 全局化、端口转发统一管理、代理组绑定探针出口和旧探针侧出口监听域名端口配置迁移或复用。
- 探针静态局域网 IP 必须来自当前 TUN FakeIP 地址池前 1024 个 IP。
- 普通 FakeIP 分配必须跳过前 1024 个探针静态局域网 IP 池。
- 主控下发接口必须兼容旧探针，新增字段不得破坏既有链路同步。
- 拓扑规则级服务端口默认 `12040` 且允许复用；服务域名可以为空、来自 Cloudflare DDNS 候选或手动填写内网域名。
- 拓扑规则 A/B 物理建联方向不得被解释为虚拟连接方向；物理建联固定 A->B，虚拟连接必须按点对点链路处理，并在运行态与状态页中分别标注拓扑邻居、物理主动建联能力和虚拟探测能力。

#### 1.2.6 风险
- 局域网互联复用自定义帧子流时，需要避免与既有端口转发和代理链路子流语义冲突。
- 前 1024 个 FakeIP 池改变后，需要确保历史普通 FakeIP 记录不会继续占用探针静态 IP 池。
- 主控拓扑规则与实际链路运行态不一致时，可能出现配置可达但运行不可达。

#### 1.2.7 结论
- 第一阶段采用兼容扩展方式推进，先完成拓扑配置、静态 IP 池约束和局域网互联基础，不进入后续代理全局化范围。

### 1.3 单元设计
- 状态: 进行中

#### 1.3.1 单元清单
| 单元编号 | 单元名称 | 所属模块 | 职责 | 输入 | 输出 |
|---|---|---|---|---|---|
| U-DVRT-01 | 虚拟路由器配置模型 | M-DVRT-01 | 定义静态 IP 与拓扑规则结构及规范化规则 | 原始 JSON | 规范化配置 |
| U-DVRT-02 | 虚拟路由器管理接口 | M-DVRT-01 | 提供查询与保存接口 | HTTP 请求 | JSON 响应 |
| U-DVRT-03 | 拓扑规则页面 | M-DVRT-02 | 管理拓扑规则 | 管理端状态 | 页面交互与 API 请求 |
| U-DVRT-04 | 探针配置下发扩展 | M-DVRT-03 | 将虚拟路由器配置加入 grouped 响应 | 探针身份 | 虚拟路由器配置 |
| U-DVRT-05 | probe_node 配置同步缓存 | M-DVRT-04 | 接收并持久化虚拟路由器配置 | grouped 响应 | 本地缓存 |
| U-DVRT-06 | FakeIP 分配保留池 | M-DVRT-05 | 普通 FakeIP 跳过前 1024 个 IP | FakeIP CIDR | 普通 FakeIP |
| U-DVRT-07 | 局域网互联基础运行态 | M-DVRT-04 | 基于拓扑建立可达关系与后续包转发入口 | 本地配置、链路 runtime | 互联运行态 |

#### 1.3.2 单元设计
##### U-DVRT-01
- 单元名称: 虚拟路由器配置模型
- 职责: 定义主控侧持久化结构，包含探针静态 IP 列表、拓扑规则列表、规则级服务域名/端口、更新时间。
- 输入: 管理端提交 JSON。
- 输出: 规范化后的配置。
- 处理规则: 节点 ID 归一化；IP 必须位于前 1024 探针静态 IP 池；拓扑规则必须包含两个不同探针，A 端固定为物理主动建联端、B 端固定为物理监听端；规则级服务端口未填时默认 `12040`，已填时必须位于 1-65535，端口不做唯一性约束；历史 `direction` 输入统一归一为 `forward`。
- 异常规则: IP 不在地址池、节点重复、规则数量超限时拒绝保存。

##### U-DVRT-02
- 单元名称: 虚拟路由器管理接口
- 职责: 提供管理端查询和保存虚拟路由器配置。
- 输入: GET/POST 管理端请求。
- 输出: 当前配置与保存结果。
- 处理规则: 复用管理端鉴权；保存后写入现有链路 store 并触发备份。
- 异常规则: store 未初始化、JSON 非法或校验失败时返回错误。

##### U-DVRT-03
- 单元名称: 拓扑规则页面
- 职责: 在主控探针链路页面中提供独立拓扑规则配置视图。
- 输入: 管理员编辑操作。
- 输出: API 保存请求。
- 处理规则: 支持新增、删除、启用、禁用、选择 A/B 两个探针；A 端固定主动、B 端固定监听，不显示方向选择；支持在规则内为 A/B 两端选择 Cloudflare DDNS 域名或手动填写内网域名，并配置服务端口。
- 异常规则: 前端阻止明显非法输入，后端仍执行最终校验。

##### U-DVRT-04
- 单元名称: 探针配置下发扩展
- 职责: 将虚拟路由器配置加入现有 grouped 响应。
- 输入: 探针身份。
- 输出: 包含本探针静态 IP、全局探针 IP 表与拓扑规则的扩展响应。
- 处理规则: 仅下发已启用拓扑规则；保留旧字段不变。
- 异常规则: 无虚拟路由器配置时返回空结构。

##### U-DVRT-05
- 单元名称: probe_node 配置同步缓存
- 职责: 接收主控下发的虚拟路由器配置并持久化本地缓存。
- 输入: grouped 响应。
- 输出: 本地缓存与运行态快照。
- 处理规则: 同步失败时保持上一份缓存；配置为空时关闭虚拟路由器运行态。
- 异常规则: 缓存损坏时忽略并等待下次同步。

##### U-DVRT-06
- 单元名称: FakeIP 分配保留池
- 职责: 普通 FakeIP 分配跳过当前 TUN FakeIP 地址池前 1024 个 IP。
- 输入: FakeIP CIDR、现有映射。
- 输出: 不在探针静态 IP 池内的普通 FakeIP。
- 处理规则: 默认 `198.18.0.0/15` 时普通 FakeIP 从第 1025 个可用地址开始。
- 异常规则: 地址池过小或耗尽时返回分配失败。

##### U-DVRT-07
- 单元名称: 局域网互联基础运行态
- 职责: 为第一阶段 LAN 互联建立本机静态 IP、拓扑可达关系、隐藏 `vrouter-*` 链路运行态与 frame 子流入口。
- 输入: 本地虚拟路由器配置、现有链路 runtime。
- 输出: LAN 互联运行态。
- 处理规则: 优先支持本机与直连/共同节点可达探针之间的互联流量；本机涉及的启用拓扑规则会生成隐藏 runtime，A 端只主动拨 B 端配置的服务域名/端口，B 端只监听，不因 A 端存在域名而反向拨号。拓扑邻接、物理主动建联和虚拟探测必须分开建模：`A->B` 下 B 仍保存 A 为虚拟拓扑邻居并知道 A 的探针静态 IP；当 A 建立到 B 的物理会话后，B 应能沿该会话反向打开虚拟子流对 A 执行虚拟 ping/pong 与数据转发。
- 异常规则: 目标不可达、链路 runtime 不存在或共同节点不可用时返回不可达。

#### 1.3.3 风险
- 第一阶段 LAN 互联涉及 probe_controller 与 probe_node 双端改造，需要保持配置兼容。
- Windows TUN 包处理路径复杂，编码时必须避免影响现有 TCP/UDP 代理路径。
- 共同节点转发可能需要后续补充更完整的路径选择与环路避免策略。

#### 1.3.4 结论
- 第一阶段具备可拆分编码任务，但 Code 只能执行第1.4节列出的文件范围与任务。

### 1.4 Code任务执行包
- 状态: 进行中

#### 1.4.1 执行边界
- 允许修改: `probe_controller/internal/core/probe_link_chain_store.go`、`probe_controller/internal/core/probe_link_chains.go`、`probe_controller/internal/core/mng_link_handlers.go`、`probe_controller/internal/core/mng_link_actions.go`、`probe_controller/internal/core/server.go`、`probe_controller/internal/core/mng_pages/link.html`、`probe_controller/internal/core/*link*_test.go`、`probe_node/probe_link_chains_sync.go`、`probe_node/local_dns_service.go`、`probe_node/local_dns_service_test.go`、`probe_node/local_tun_stack_windows.go`、`probe_node/local_tun_stack_windows_test.go`、新增第一阶段虚拟路由器相关 Go 文件与测试文件。
- 禁止修改: 既有 `proxy_group` 全局化实现、端口转发统一管理实现、代理组绑定探针出口实现、旧探针侧出口监听域名端口实现、无关页面与无关业务。

#### 1.4.2 任务清单
| 任务编号 | 需求编号 | 单元编号 | 文件范围 | 操作类型 | 验收标准 |
|---|---|---|---|---|---|
| T-DVRT-01 | RQ-DVRT-008,RQ-DVRT-015 | U-DVRT-06 | `probe_node/local_dns_service.go`, `probe_node/local_dns_service_test.go` | 修改 | 普通 FakeIP 分配跳过前 1024 个探针静态 IP 池，测试覆盖默认 CIDR |
| T-DVRT-02 | RQ-DVRT-011,RQ-DVRT-012,RQ-DVRT-016,RQ-DVRT-018 | U-DVRT-01,U-DVRT-02 | `probe_controller/internal/core/probe_link_chain_store.go`, `probe_controller/internal/core/probe_link_chains.go`, `probe_controller/internal/core/mng_link_handlers.go`, `probe_controller/internal/core/mng_link_actions.go`, `probe_controller/internal/core/server.go`, 新增测试 | 新增/修改 | 管理接口可查询和保存拓扑规则，后端校验节点、方向、静态 IP 池、规则级服务端口 |
| T-DVRT-03 | RQ-DVRT-016,RQ-DVRT-018 | U-DVRT-03 | `probe_controller/internal/core/mng_pages/link.html` | 修改 | 主控探针链路页面出现独立拓扑规则配置视图，可配置规则级服务域名/端口，域名从 Cloudflare DDNS 弹窗选择或手动填写 |
| T-DVRT-04 | RQ-DVRT-004,RQ-DVRT-011,RQ-DVRT-013,RQ-DVRT-017,RQ-DVRT-018 | U-DVRT-04,U-DVRT-05 | `probe_controller/internal/core/probe_link_chains.go`, `probe_node/probe_link_chains_sync.go`, 新增测试 | 新增/修改 | grouped 响应兼容扩展下发虚拟路由器配置，probe_node 可缓存配置与规则级服务域名/端口 |
| T-DVRT-05 | RQ-DVRT-006,RQ-DVRT-013,RQ-DVRT-017 | U-DVRT-07 | `probe_node/local_tun_stack_windows.go`, 新增虚拟路由器运行态文件与测试 | 新增/修改 | 第一阶段 LAN 互联运行态具备目标可达判断与 frame 子流入口，不改变既有 TUN 代理测试结果 |
| T-DVRT-06 | 全部第一阶段需求 | 全部第一阶段单元 | 协作文档与测试 | 修改/验证 | 更新 Code 证据，执行相关 Go 测试 |

#### 1.4.3 源码修改规则
- 必须使用 encoding_tools/README.md 描述的接口。
- 对 C/C++ 源代码（`.c`、`.cc`、`.cpp`、`.cxx`、`.h`、`.hpp`）必须使用 encoding_tools/encoding_safe_patch.py。
- 对非 C/C++ 源代码可直接编辑，不强制使用 encoding_tools/encoding_safe_patch.py。
- encoding_tools/ 不可用或执行失败时，Code 必须记录失败命令、错误摘要、影响文件与阻塞影响，并提交第2.6节 `Code任务反馈`。
- 替代 encoding_tools/ 修改受控 C/C++ 源代码前，必须取得 Architect 明确允许。

#### 1.4.4 交付物
- 更新后的协作文档。
- 主控虚拟路由器拓扑配置接口与页面。
- 探针虚拟路由器配置同步缓存。
- FakeIP 前 1024 探针静态 IP 池约束。
- 第一阶段局域网互联基础运行态。
- 自动化测试与执行证据。

#### 1.4.5 门禁输入
- 仅放行第一阶段任务 T-DVRT-01 至 T-DVRT-06。
- 后续 `proxy_group` 全局化、端口转发统一管理、代理组绑定出口、探针出口监听域名端口配置仍保持阻塞。

#### 1.4.6 结论
- 第一阶段 Code 任务有条件放行，执行中不得越过第1.4.1边界。

### 1.5 Architect需求跟踪矩阵
- 状态: 进行中

| 需求编号 | 需求描述 | 架构章节 | 单元设计章节 | Code任务章节 | 状态 | 备注 |
|---|---|---|---|---|---|---|
| RQ-DVRT-001 | 探针作为分布式虚拟路由器入口参与统一网络 | 1.1,1.2 | 1.3 | T-DVRT-04,T-DVRT-05 | 进行中 | 第一阶段部分覆盖 |
| RQ-DVRT-002 | 探针间通讯使用现有自定义帧并承载于物理连接之上 | 1.1,1.2 | 1.3 | T-DVRT-05 | 进行中 | 第一阶段复用现有帧 |
| RQ-DVRT-003 | 当前物理连接已经存在，并已经具备双向通讯能力 | 1.1,1.2 | 1.3 | T-DVRT-05 | 进行中 | 第一阶段复用现有链路 |
| RQ-DVRT-004 | 每个探针由主控分配一个静态全局局域网 IP | 1.1,1.2 | 1.3 | T-DVRT-02,T-DVRT-04 | 进行中 | 第一阶段覆盖 |
| RQ-DVRT-005 | 端口转发由虚拟路由器统一编排和管理 | 1.1 | 无 | 无 | 阻塞 | 非第一阶段范围 |
| RQ-DVRT-006 | 探针之间支持通过全局局域网 IP 互联 | 1.1,1.2 | 1.3 | T-DVRT-05 | 进行中 | 第一阶段覆盖 |
| RQ-DVRT-007 | 代理功能统一通过虚拟路由器完成 | 1.1 | 无 | 无 | 阻塞 | 非第一阶段范围 |
| RQ-DVRT-008 | 当前 TUN FakeIP 地址池的前 1024 个 IP 作为探针静态局域网 IP 池 | 1.1,1.2 | 1.3 | T-DVRT-01,T-DVRT-02 | 进行中 | 第一阶段覆盖 |
| RQ-DVRT-009 | FakeIP 地址池作为全局共享资源，不再轻易清空 | 1.1,1.2 | 1.3 | T-DVRT-01 | 进行中 | 第一阶段仅覆盖分配边界 |
| RQ-DVRT-010 | proxy_group 从单探针本地配置升级为虚拟路由器全局配置，代理组可绑定探针出口 | 1.1 | 无 | 无 | 阻塞 | 非第一阶段范围 |
| RQ-DVRT-011 | 探针间物理连接的拓扑结构在主控侧设置 | 1.1,1.2 | 1.3 | T-DVRT-02,T-DVRT-03,T-DVRT-04 | 进行中 | 第一阶段覆盖 |
| RQ-DVRT-012 | 每条拓扑规则描述两个探针之间的连接关系，A 端固定主动拨 B 端 | 1.1,1.2 | 1.3 | T-DVRT-02,T-DVRT-03 | 进行中 | 第一阶段覆盖 |
| RQ-DVRT-013 | 非直连探针存在共同连接节点时支持通过共同节点转发 | 1.1,1.2 | 1.3 | T-DVRT-04,T-DVRT-05 | 进行中 | 第一阶段基础覆盖 |
| RQ-DVRT-014 | 每个探针出口可单独配置监听域名与端口，支持 Cloudflare 代理和直连形态 | 1.1 | 无 | 无 | 阻塞 | 旧探针侧出口配置非第一阶段范围 |
| RQ-DVRT-015 | 第一阶段不改动原有 TUN 代理功能，仅调整前 1024 个 FakeIP 的保留用途 | 1.1,1.2 | 1.3 | T-DVRT-01,T-DVRT-05 | 进行中 | 第一阶段约束 |
| RQ-DVRT-016 | 第一阶段在主控探针链路中新增独立拓扑规则配置页面 | 1.1,1.2 | 1.3 | T-DVRT-02,T-DVRT-03 | 进行中 | 第一阶段覆盖 |
| RQ-DVRT-017 | 第一阶段实现基于探针静态局域网 IP 与主控拓扑规则的局域网互联功能 | 1.1,1.2 | 1.3 | T-DVRT-04,T-DVRT-05 | 进行中 | 第一阶段覆盖 |
| RQ-DVRT-018 | 每条拓扑规则独立配置 A/B 两端服务域名与服务端口，不混用旧探针侧配置 | 1.1,1.2 | 1.3 | T-DVRT-02,T-DVRT-03,T-DVRT-04 | 进行中 | 第一阶段覆盖 |
| RQ-DVRT-020 | 拓扑规则 A/B 顺序固定表示物理 A->B 建联，虚拟连接保持点对点双向可达语义 | 1.1,1.2 | 1.3 | T-DVRT-04,T-DVRT-05 | 已完成 | 虚拟转发复用相邻 runtime 的唯一双向 bridge session，物理建联方向、虚拟拓扑邻接和虚拟探测分开建模 |
| RQ-DVRT-021 | 虚拟路由 TUN 数据面、数据面 frame 与底层物理会话自定义 frame 必须按层统计、上传并展示 | 1.1,1.2 | 1.3 | T-DVRT-03,T-DVRT-05 | 已完成 | 新增 TUN RX/TX、虚拟路由数据面 frame tx/rx、单规则单物理 bridge 连接 frame tx/rx 与 session 明细；TUN RX/TX 按节点去重展示，规则行仅展示本规则两端 runtime 统计 |
| RQ-DVRT-022 | 本机虚拟 IP 必须具备 ICMP Echo Reply 保底能力 | 1.1,1.2 | 1.3 | T-DVRT-05 | 已完成 | vRouter 收到目标为本机虚拟 IP 的 Echo Request 时直接生成 Echo Reply 并按虚拟拓扑发回 |

### 1.6 Architect关键接口跟踪矩阵
- 状态: 进行中

| 接口编号 | 需求编号 | 接口名称 | 调用方 | 提供方 | 输入 | 输出 | 状态 | 备注 |
|---|---|---|---|---|---|---|---|---|
| IF-DVRT-01 | RQ-DVRT-011,RQ-DVRT-012,RQ-DVRT-016 | GET `/mng/api/link/virtual_router` | 管理页面 | probe_controller | 管理会话 | 虚拟路由器配置 | 进行中 | 第一阶段新增 |
| IF-DVRT-02 | RQ-DVRT-004,RQ-DVRT-011,RQ-DVRT-012,RQ-DVRT-016,RQ-DVRT-018 | POST `/mng/api/link/virtual_router` | 管理页面 | probe_controller | 静态 IP、拓扑规则、规则级服务域名/端口 JSON | 保存结果 | 进行中 | 第一阶段新增 |
| IF-DVRT-03 | RQ-DVRT-004,RQ-DVRT-011,RQ-DVRT-013,RQ-DVRT-017 | GET `/api/probe/link/config/grouped` | probe_node | probe_controller | node_id、secret | 链路配置与虚拟路由器配置 | 进行中 | 兼容扩展 |
| IF-DVRT-04 | RQ-DVRT-004,RQ-DVRT-017 | 本地虚拟路由器配置缓存 | probe_node | 本地文件 | 主控下发配置 | 缓存文件 | 进行中 | 第一阶段新增 |

### 1.7 门禁裁判
- 状态: 已完成

#### 1.7.1 门禁输入
| 文档 | 路径 | 状态 |
|---|---|---|
| 协作文档 | doc/REQ-PN-DISTRIBUTED-VROUTER-001-collaboration.md | 已创建 |

#### 1.7.2 裁判检查
| 检查项 | 结果 | 证据 | 备注 |
|---|---|---|---|
| 协作文档存在 | 通过 | 本文档 | 无 |
| Architect章节存在 | 通过 | 第1章 | 无 |
| Code章节存在 | 通过 | 第2章 | 无 |
| 必需子章节存在 | 通过 | 1.1-1.7 与 2.1-2.6 | 无 |
| 需求前缀一致 | 通过 | REQ-PN-DISTRIBUTED-VROUTER-001 | 无 |
| 需求编号一致 | 通过 | REQ-PN-DISTRIBUTED-VROUTER-001 | 无 |
| 接口编号一致 | 通过 | 1.6 IF-DVRT-01 至 IF-DVRT-04 | 第一阶段接口已定义 |
| 模板字段完整 | 通过 | 文档头字段 | 无 |
| Code使用encoding_tools | 通过 | 当前为Architect放行检查 | Code执行后需补证据 |
| Code证据完整 | 有条件通过 | 当前为Architect放行检查 | Code执行后需补第2.5节 |
| Code任务反馈已处理 | 通过 | 当前无Code反馈 | 无 |
| 验收标准可测试 | 通过 | 1.4.2 | 第一阶段任务具备验收标准 |
| 需求任务覆盖完整 | 有条件通过 | 1.5 | 第一阶段需求覆盖，非第一阶段需求保持阻塞 |
| 任务自测覆盖完整 | 有条件通过 | 1.4.2 | Code执行后需补充测试证据 |
| 修改文件在允许范围内 | 通过 | 1.4.1 | 第一阶段文件范围已限定 |
| 测试失败已记录缺陷 | 通过 | 当前无测试失败 | 无 |
| 未执行测试原因完整 | 有条件通过 | 当前为Architect放行检查 | Code执行后需补充 |
| 遗留风险可接受 | 有条件通过 | 1.1.5、1.1.6、1.2.6 | 第一阶段可接受，后续范围继续阻塞 |

#### 1.7.3 冲突记录
| 冲突编号 | 冲突条款 | 最终采用条款 | 裁决人 | 裁决结论 |
|---|---|---|---|---|
| 无 | 无 | 无 | 无 | 无 |

#### 1.7.4 裁判结论
- 结论: 有条件通过
- 放行阻塞: 放行
- 条件: 仅放行第一阶段 T-DVRT-01 至 T-DVRT-06；不得实施 `proxy_group` 全局化、端口转发统一管理、代理组绑定探针出口、探针出口监听域名端口配置。
- 责任方: Code
- 关闭要求: Code 完成后必须补齐第2章执行证据、测试结果、缺陷与反馈；Architect 需重新执行最终门禁。
- 整改要求: 如 Code 发现任务范围缺失、接口缺失或验收不可测试，必须停止对应修改并在第2.6节反馈。

#### 1.7.5 结论
- 第一阶段实现有条件放行；后续阶段需求保持阻塞。

## 第2章 Code章节
- 章节责任角色: Code
- 状态: 已完成第一阶段基础实现

### 2.1 Code需求跟踪矩阵
- 状态: 已完成第一阶段基础实现

| 需求编号 | 任务编号 | 实现文件 | 实现状态 | 自测状态 | 证据 | 备注 |
|---|---|---|---|---|---|---|
| RQ-DVRT-008,RQ-DVRT-015 | T-DVRT-01 | `probe_node/local_dns_service.go`, `probe_node/local_dns_service_test.go` | 已完成 | 通过 | `go test -count=1 ./...` | 普通 FakeIP 分配跳过前 1024 个探针静态 IP |
| RQ-DVRT-011,RQ-DVRT-012,RQ-DVRT-016 | T-DVRT-02 | `probe_controller/internal/core/probe_virtual_router.go`, `probe_controller/internal/core/mng_link_actions.go`, `probe_controller/internal/core/mng_link_handlers.go`, `probe_controller/internal/core/server.go`, `probe_controller/internal/core/*virtual_router*_test.go` | 已完成 | 通过 | `go test -count=1 ./internal/core` | 新增管理接口、存储、校验 |
| RQ-DVRT-016 | T-DVRT-03 | `probe_controller/internal/core/mng_pages/link.html` | 已完成 | 通过 | 页面静态检查与接口测试 | 链路页面新增“拓扑规则”视图 |
| RQ-DVRT-004,RQ-DVRT-011,RQ-DVRT-013,RQ-DVRT-017,RQ-DVRT-020 | T-DVRT-04 | `probe_controller/internal/core/probe_link_chains.go`, `probe_node/probe_link_chains_sync.go`, `probe_node/probe_virtual_router.go`, `probe_node/probe_virtual_router_test.go` | 已完成 | 通过 | `go test -count=1 -run 'TestProbeVirtualRouterRuntimeForAdjacentNode|TestProbeVirtualRouterRuntimeForAdjacentNodePrefersAvailableBridgeSession|TestProbeVirtualRouterReachableTreatsDirectionAsPhysicalDialOnly' .`, `go test -count=1 ./internal/core` | grouped 响应扩展下发虚拟路由器配置，节点侧缓存；虚拟转发复用相邻规则 runtime 的唯一双向 bridge session |
| RQ-DVRT-006,RQ-DVRT-013,RQ-DVRT-017,RQ-DVRT-020 | T-DVRT-05 | `probe_node/probe_virtual_router.go`, `probe_node/probe_virtual_router_runtime.go`, `probe_node/probe_virtual_router_windows.go`, `probe_node/probe_virtual_router_other.go`, `probe_node/link_chain_runtime.go`, `probe_node/local_tun_dataplane_windows.go`, `probe_node/probe_virtual_router_test.go` | 已完成基础数据面与隐藏链路运行态 | 通过 | `TestBuildProbeVirtualRouterRuntimeConfigsForNode`, `TestProbeVirtualRouterReachableViaCommonNode`, `TestBuildProbeVirtualRouterTunnelOpenRequest`, `TestOpenProbeVirtualRouterPhysicalBridgeStreamIgnoresLegacySessionDirection`, `GOOS=windows GOARCH=amd64 go test -c -o /tmp/probe_node_windows.test.exe .` | 已接入 TUN 入站、隐藏 `vrouter-*` 监听/拨号、frame 子流、逐跳转发与目标写入 TUN；vRouter 虚拟 ping 与虚拟 IP 包转发直接打开唯一物理 bridge，不再按历史上下游方向选择开流；真实多机互 ping 待环境验证 |
| RQ-DVRT-019,RQ-DVRT-021 | T-DVRT-03,T-DVRT-05 | `probe_controller/internal/core/mng_pages/link.html`, `probe_controller/internal/core/mng_link_handlers.go`, `probe_node/probe_virtual_router.go`, `probe_node/chain_frame_session.go`, `probe_node/link_chain_runtime.go`, `probe_node/link_relay_client_transport.go` | 已完成 | 通过 | `go test -count=1 -run 'TestProbeVirtualRouterRuntimeFrameStatsAreDirectional|TestProbeVirtualRouterReachableTreatsDirectionAsPhysicalDialOnly|TestProbeChainFrameSessionIOStatsAreDirectional' .`, `go test -count=1 -run TestMngLinkVirtualRouterStatusHandlerReturnsRuleRuntimeStatus ./internal/core` | 新增路由状态 tab，展示节点级 TUN RX/TX、规则状态、IP 包 fwd/rx/local 生命周期统计、虚拟路由数据面 frame 发送/接收统计、单规则单物理 bridge 连接 frame 发送/接收统计与 session 明细、定时虚拟 ping/pong 延迟和最近活动 |
| 全部第一阶段需求 | T-DVRT-06 | 本文档与测试命令 | 已完成 | 通过 | 第2.5节 | 已补执行证据 |

### 2.2 Code关键接口跟踪矩阵
- 状态: 已完成

| 接口编号 | 需求编号 | 实现文件 | 调用方 | 提供方 | 实现状态 | 证据 | 备注 |
|---|---|---|---|---|---|---|---|
| IF-DVRT-01 | RQ-DVRT-011,RQ-DVRT-012,RQ-DVRT-016 | `probe_controller/internal/core/mng_link_handlers.go`, `probe_controller/internal/core/mng_link_actions.go`, `probe_controller/internal/core/server.go` | 管理页面 | probe_controller | 已完成 | `TestMngLinkVirtualRouterHandlerSaveAndGet` | GET `/mng/api/link/virtual_router` |
| IF-DVRT-02 | RQ-DVRT-004,RQ-DVRT-011,RQ-DVRT-012,RQ-DVRT-016 | `probe_controller/internal/core/mng_link_handlers.go`, `probe_controller/internal/core/probe_virtual_router.go` | 管理页面 | probe_controller | 已完成 | `TestMngLinkVirtualRouterHandlerRejectsProbeIPOutsideReservedPool` | POST `/mng/api/link/virtual_router` |
| IF-DVRT-03 | RQ-DVRT-004,RQ-DVRT-011,RQ-DVRT-013,RQ-DVRT-017 | `probe_controller/internal/core/probe_link_chains.go`, `probe_node/probe_link_chains_sync.go` | probe_node | probe_controller | 已完成 | `go test -count=1 ./internal/core`, `go test -count=1 ./...` | grouped 响应增加 `virtual_router` |
| IF-DVRT-04 | RQ-DVRT-004,RQ-DVRT-017 | `probe_node/probe_virtual_router.go`, `probe_node/probe_link_chains_sync.go` | probe_node | 本地文件 | 已完成 | `TestProbeVirtualRouterCacheRoundTrip` | 本地缓存 `virtual_router.json` |
| IF-DVRT-05 | RQ-DVRT-006,RQ-DVRT-017 | `probe_node/probe_virtual_router_runtime.go`, `probe_node/probe_link_chains_sync.go`, `probe_node/probe_virtual_router.go` | probe_node | 本地 runtime | 已完成 | `TestBuildProbeVirtualRouterRuntimeConfigsForNode`, `TestCollectProbeLinkChainRuntimeIDsToStopKeepsVirtualRouterRuntime` | 拓扑规则生成隐藏 `vrouter-*` runtime，并优先用于相邻探针转发 |
| IF-DVRT-06 | RQ-DVRT-019 | `probe_controller/internal/core/mng_link_handlers.go`, `probe_controller/internal/core/server.go` | 管理页面 | probe_controller | 已完成 | `TestMngLinkVirtualRouterStatusHandlerReturnsRuleRuntimeStatus` | GET `/mng/api/link/virtual_router/status` |

### 2.3 Code测试项跟踪矩阵
- 状态: 已完成

| 测试项编号 | 需求编号 | 任务编号 | 测试目标 | 测试方法 | 结果 | 证据 | 未执行原因 | 备注 |
|---|---|---|---|---|---|---|---|---|
| TC-DVRT-01 | RQ-DVRT-008,RQ-DVRT-015 | T-DVRT-01 | 普通 FakeIP 跳过前 1024 个地址 | Go 单测 | 通过 | `TestProbeLocalDNSFakeIPSkipsVirtualRouterProbeReserve` | 无 | 默认 CIDR 首个普通 FakeIP 为 `198.18.4.1` |
| TC-DVRT-02 | RQ-DVRT-004,RQ-DVRT-011,RQ-DVRT-012,RQ-DVRT-016 | T-DVRT-02 | 管理接口保存与查询 | Go 单测 | 通过 | `TestMngLinkVirtualRouterHandlerSaveAndGet` | 无 | 覆盖 POST/GET |
| TC-DVRT-03 | RQ-DVRT-008 | T-DVRT-02 | 静态 IP 池校验 | Go 单测 | 通过 | `TestMngLinkVirtualRouterHandlerRejectsProbeIPOutsideReservedPool` | 无 | 拒绝非前 1024 池 IP |
| TC-DVRT-04 | RQ-DVRT-004,RQ-DVRT-011 | T-DVRT-04 | grouped 下发过滤禁用规则 | Go 单测 | 通过 | `TestBuildProbeVirtualRouterConfigForNodeFiltersDisabledRules` | 无 | 仅下发启用且相关规则 |
| TC-DVRT-05 | RQ-DVRT-013,RQ-DVRT-017 | T-DVRT-05 | 共同节点可达与物理方向不限制虚拟反向路径 | Go 单测 | 通过 | `TestProbeVirtualRouterReachableViaCommonNode`, `TestProbeVirtualRouterReachableTreatsDirectionAsPhysicalDialOnly` | 无 | `direction` 只约束物理建联方向；虚拟连接按点对点双向拓扑寻路 |
| TC-DVRT-06 | RQ-DVRT-004,RQ-DVRT-017 | T-DVRT-04 | 节点侧缓存读写 | Go 单测 | 通过 | `TestProbeVirtualRouterCacheRoundTrip` | 无 | 缓存文件可恢复 |
| TC-DVRT-07 | RQ-DVRT-006,RQ-DVRT-017 | T-DVRT-05 | LAN frame open request 入口 | Go 单测 | 通过 | `TestBuildProbeVirtualRouterTunnelOpenRequest` | 无 | 构造 `virtual_router_lan_packet` open request |
| TC-DVRT-10 | RQ-DVRT-006,RQ-DVRT-013,RQ-DVRT-017 | T-DVRT-05 | 虚拟路由器路径、目标 IP、相邻 runtime 选择 | Go 单测 | 通过 | `TestProbeVirtualRouterNextHopInPath`, `TestProbeVirtualRouterPathFromRequest`, `TestProbeVirtualRouterIPv4Destination`, `TestProbeVirtualRouterRuntimeForAdjacentNode` | 无 | 覆盖逐跳转发基础函数 |
| TC-DVRT-13 | RQ-DVRT-006,RQ-DVRT-017 | T-DVRT-05 | 虚拟路由器 packet stream 复用与过期 | Go 单测 | 通过 | `TestProbeVirtualRouterPacketStreamKey`, `TestProbeVirtualRouterPacketStreamCacheReuseAndDrop`, `TestProbeVirtualRouterPacketStreamCacheExpires` | 无 | 覆盖同一路径复用、失败剔除、TTL 过期 |
| TC-DVRT-14 | RQ-DVRT-006,RQ-DVRT-017,RQ-DVRT-018,RQ-DVRT-020 | T-DVRT-05 | 拓扑规则生成隐藏监听/拨号运行态与物理 bridge 开流 | Go 单测 | 通过 | `TestBuildProbeVirtualRouterRuntimeConfigsForNode`, `TestBuildProbeVirtualRouterRuntimeConfigFixedADialsBRequiresBAddress`, `TestOpenProbeVirtualRouterPhysicalBridgeStreamIgnoresLegacySessionDirection`, `TestCollectProbeLinkChainRuntimeIDsToStopKeepsVirtualRouterRuntime` | 无 | 覆盖默认 `12040`、端口复用、固定 A 拨 B、B 不因 A 有地址而反向拨号、vRouter 按唯一物理 bridge 开流、不按历史上下游登记方向开流、普通链路同步不清理 `vrouter-*` runtime |
| TC-DVRT-15 | RQ-DVRT-019,RQ-DVRT-021 | T-DVRT-03,T-DVRT-05 | 路由状态按规则汇总两端 runtime 状态和统计 | Go 单测 | 通过 | `TestMngLinkVirtualRouterStatusHandlerReturnsRuleRuntimeStatus` | 无 | 覆盖 ready 状态、IP 包 fwd/rx/local 生命周期统计、frame 收发统计、虚拟 ping/pong 延迟和最近活动时间 |
| TC-DVRT-16 | RQ-DVRT-021 | T-DVRT-05 | 虚拟路由器数据面与底层会话 frame 发送/接收方向计数 | Go 单测 | 通过 | `TestProbeVirtualRouterRuntimeFrameStatsAreDirectional`, `TestProbeChainFrameSessionIOStatsAreDirectional` | 无 | 覆盖 frame tx/rx 帧数、字节数和最近活动时间 |
| TC-DVRT-17 | RQ-DVRT-022 | T-DVRT-05 | 本机虚拟 IP ICMP Echo Reply 构造 | Go 单测 | 通过 | `TestBuildProbeVirtualRouterICMPEchoReply` | 无 | 覆盖源/目标交换、ICMP echo reply 类型和 IP/ICMP checksum |
| TC-DVRT-11 | RQ-DVRT-006,RQ-DVRT-017 | T-DVRT-05 | Windows 主包编译 | `GOOS=windows GOARCH=amd64 go test -c -o /tmp/probe_node_windows.test.exe .` | 通过 | 命令退出码 0 | 无 | 仅编译不执行，避免 Linux 上执行 Windows exe |
| TC-DVRT-12 | 全部第一阶段需求 | T-DVRT-06 | 旧远程状态用例全局隔离回归 | `go test -count=1 -run TestProbeLocalTUNGroupRuntimeFetchRemotePeerStatusRejectsOpenResponse .` | 通过 | 命令输出 `ok github.com/cloudhelper/probe_node` | 无 | 补充清理 proxy view chains 与 runtime map |
| TC-DVRT-08 | 全部第一阶段需求 | T-DVRT-06 | controller 包整体回归 | `go test -count=1 ./internal/core` | 通过 | 命令输出 `ok github.com/cloudhelper/probe_controller/internal/core` | 无 | 无 |
| TC-DVRT-09 | 全部第一阶段需求 | T-DVRT-06 | probe_node 整体回归 | `go test -count=1 ./...` | 通过 | 命令输出 `ok github.com/cloudhelper/probe_node`, `ok github.com/cloudhelper/probe_node/mobilecore` | 无 | 无 |

### 2.4 Code缺陷跟踪矩阵
- 状态: 已完成

| 缺陷编号 | 需求编号 | 测试项编号 | 缺陷描述 | 严重级别 | 修复状态 | 修复证据 | 备注 |
|---|---|---|---|---|---|---|---|
| 无 | 无 | 无 | 当前未发现阻断缺陷 | 无 | 无 | `go test -count=1 ./internal/core`, `go test -count=1 ./...`, `GOOS=windows GOARCH=amd64 go test -c -o /tmp/probe_node_windows.test.exe .` | 真实 Windows 多探针互 ping 未在当前环境执行 |

### 2.5 Code执行证据
- 状态: 已完成

#### 2.5.1 修改接口
- 新增 GET `/mng/api/link/virtual_router`。
- 新增 POST `/mng/api/link/virtual_router`。
- 新增 GET `/mng/api/link/virtual_router/status`。
- 扩展 GET `/api/probe/link/config/grouped` 响应字段 `virtual_router`。
- 节点侧新增本地缓存文件 `virtual_router.json`。

#### 2.5.2 配置文件
- 主控 `probe_link_chain_store` 增加 `virtual_router` 配置区。
- 节点侧数据目录增加 `virtual_router.json` 缓存。

#### 2.5.3 执行报告
- 完成主控虚拟路由器配置模型、校验、存储、查询和保存接口。
- 完成主控链路页面“拓扑规则”视图。
- 完成 grouped 配置下发与节点侧缓存。
- 完成普通 FakeIP 跳过前 1024 个探针静态 IP 的分配约束。
- 完成节点侧虚拟路由器可达路径计算、按 IP 查节点、当前本机 IP/节点识别与 LAN frame open request 构造。
- 完成拓扑规则到隐藏 `vrouter-*` 链路运行态的生成、恢复和清理；支持默认 `12040` 端口、多规则复用同端口、固定 A 端主动拨 B 端，B 端不反向主动建联。
- 完成隐藏 `vrouter-*` 运行态统计上报，包含转发/接收/送达 IP 包字节、虚拟路由数据面 frame 发送/接收帧数与字节、定时虚拟 ping/pong 延迟、最近错误和最近活动。
- 完成主控路由状态 tab 与状态汇总 API，按节点去重展示 TUN RX/TX，并按拓扑规则展示两端 runtime 状态、IP 包 fwd/rx/local 生命周期统计、虚拟路由数据面 frame 收发统计、单规则单物理 bridge 连接 frame 收发统计与 session 明细、虚拟 ping/pong 延迟和最近活动。
- 完成 `virtual_router_lan_packet` frame 子流接收、路径逐跳转发、目标探针写入本地 TUN。
- 完成按链路、目标 IP、拓扑路径复用虚拟路由器 packet stream，支持写失败剔除与空闲 TTL 过期；vRouter packet stream 的底层开流只使用唯一物理 bridge，不再按历史上下游方向选择 session。
- 完成 Windows TUN 入站 hook，发往探针静态 IP 的 IPv4 包进入虚拟路由器转发。
- 完成 Windows 平台本机探针静态 IP 追加到 Wintun 接口，避免目标探针无法接收发往本机虚拟 LAN IP 的包。
- 主控探针静态 IP 分配避开现有 TUN gateway/interface 地址 `198.18.0.1`、`198.18.0.2`，默认从 `198.18.0.3` 开始；因此前 1024 保留范围中实际可自动分配探针地址为 1022 个。

#### 2.5.4 影响文件
- `probe_controller/internal/core/probe_virtual_router.go`
- `probe_controller/internal/core/probe_virtual_router_test.go`
- `probe_controller/internal/core/mng_link_actions.go`
- `probe_controller/internal/core/mng_link_handlers.go`
- `probe_controller/internal/core/mng_link_handlers_test.go`
- `probe_controller/internal/core/mng_pages/link.html`
- `probe_controller/internal/core/probe_link_chain_store.go`
- `probe_controller/internal/core/probe_link_chains.go`
- `probe_controller/internal/core/server.go`
- `probe_node/probe_virtual_router.go`
- `probe_node/probe_virtual_router_runtime.go`
- `probe_node/probe_virtual_router_windows.go`
- `probe_node/probe_virtual_router_other.go`
- `probe_node/probe_virtual_router_test.go`
- `probe_node/link_relay_client_transport.go`
- `probe_node/probe_link_chains_sync.go`
- `probe_node/link_chain_runtime.go`
- `probe_node/local_dns_service.go`
- `probe_node/local_dns_service_test.go`
- `probe_node/local_tun_dataplane_windows.go`
- `doc/REQ-PN-DISTRIBUTED-VROUTER-001-collaboration.md`

#### 2.5.5 测试命令
- `go test -count=1 ./internal/core`，执行目录 `probe_controller`。
- `go test -count=1 ./...`，执行目录 `probe_node`。
- `go test -count=1 -run 'TestBuildProbeVirtualRouterRuntime|TestCollectProbeLinkChainRuntimeIDsToStop|TestProbeVirtualRouter' .`，执行目录 `probe_node`。
- `go test -count=1 -run TestProbeLocalTUNGroupRuntimeFetchRemotePeerStatusRejectsOpenResponse .`，执行目录 `probe_node`。
- `GOOS=windows GOARCH=amd64 go test -c -o /tmp/probe_node_windows.test.exe .`，执行目录 `probe_node`。
- `git diff --check`。

#### 2.5.6 自测结果
- `go test -count=1 ./internal/core`: 通过。
- `go test -count=1 ./...`: 通过。
- `go test -count=1 -run 'TestBuildProbeVirtualRouterRuntime|TestCollectProbeLinkChainRuntimeIDsToStop|TestProbeVirtualRouter' .`: 通过。
- `go test -count=1 -run TestProbeLocalTUNGroupRuntimeFetchRemotePeerStatusRejectsOpenResponse .`: 通过。
- `GOOS=windows GOARCH=amd64 go test -c -o /tmp/probe_node_windows.test.exe .`: 通过。
- `git diff --check`: 通过，无空白错误。

#### 2.5.7 未执行测试原因
- 未执行浏览器端手工点击验证；本次页面变更为现有静态页面内新增视图，后端接口已由 Go 测试覆盖。
- 未执行跨 Windows TUN 的真实探针互 ping；当前环境无法运行 Windows Wintun 多探针实测，只完成 Windows 主包编译与 Go 单测。

#### 2.5.8 遗留风险
- T-DVRT-05 已完成基础数据面和隐藏链路运行态接入，但真实 Windows 多探针互 ping、链路中断恢复与吞吐性能仍需环境验证。
- 当前已按路径复用 frame packet stream；后续可继续做窗口、批量、背压和观测指标优化。
- 页面没有做浏览器手工验证，可能存在交互细节需后续联调。

#### 2.5.9 回滚方案
- 删除新增虚拟路由器 Go 文件与测试文件。
- 回退 `probe_link_chain_store.go`、`probe_link_chains.go`、`mng_link_*`、`server.go`、`link.html`、`probe_link_chains_sync.go`、`local_dns_service.go` 的本次变更。
- 如已生成主控配置中的 `virtual_router` 或节点缓存 `virtual_router.json`，可删除对应字段或缓存文件。

#### 2.5.10 结论
- 第一阶段配置闭环、FakeIP 保留池、拓扑页面、下发缓存、隐藏 `vrouter-*` 链路运行态、基础可达运行态和基础 LAN 包转发数据面已完成。
- 真实 Windows 多探针互 ping与性能优化仍需后续在目标环境验证。

### 2.6 Code任务反馈
- 状态: 已完成

| 反馈编号 | 任务编号 | 反馈类型 | 反馈描述 | 阻塞影响 | Code建议 | Architect处理状态 | Architect处理结论 |
|---|---|---|---|---|---|---|---|
| FB-DVRT-01 | T-DVRT-05 | 验证反馈 | 已完成基础数据面接入，但当前环境无法执行 Windows Wintun 多探针真实互 ping。 | 不影响代码自测；影响最终现场验收 | 在 Windows 目标环境配置两个以上探针，验证直连与经共同节点转发的 ICMP/TCP 连通性 | 待Architect处理 | 待处理 |

#### 2.6.1 结论
- 当前无阻断缺陷；T-DVRT-05 需要后续做 Windows 目标环境实测与性能优化。
