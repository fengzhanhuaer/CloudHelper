# 协作文档

- 适用规则: AI协作规则
- 后续工作传递声明: 本文档必须传递给后续阶段与后续角色。
- 需求编号: REQ-PN-RELAY-DUAL-STACK-001
- 需求前缀: REQ-PN-RELAY-DUAL-STACK-001
- 当前阶段: Architect设计中
- 最近更新角色: Architect
- 最近更新时间: 2026-05-18T00:00:00+08:00
- 工作依据文档: [`doc/ai-coding-collaboration.md`](doc/ai-coding-collaboration.md:1)、[`probe_node/link_chain_runtime.go`](probe_node/link_chain_runtime.go:1)、[`probe_node/local_tun_group_runtime.go`](probe_node/local_tun_group_runtime.go:1)
- 状态: 进行中

## 第1章 Architect章节
- 章节责任角色: Architect
- 状态: 进行中

### 1.1 需求定义
- 状态: 已完成

#### 1.1.1 需求目标
- 将 `probe_node` 探针 relay 入口纳入正式需求跟踪，支持后续围绕 `HTTP/1.1`、`HTTP/2`、`HTTP/3` 双栈/三栈接入策略开展设计与实现。
- 保留现有直连 relay 能力，并为后续接入 Cloudflare 代理保留兼容路径。
- 约束 `HTTP/1.1` 在新方案中的职责边界，仅作为兼容或升级引导通道，不作为长期业务承载主路径。
- 明确 `HTTP/2` 作为默认稳定业务通道、`HTTP/3` 作为直连优先高性能通道的架构方向。
- 明确客户端连接策略、服务端入口能力、私有鉴权保留策略与后续 Cloudflare 兼容边界。

#### 1.1.2 需求范围
- 服务端入口范围: relay 公网入口的 `http` / `http2` / `http3` 接入策略、监听模型、协议能力宣告。
- 客户端连接范围: 客户端默认连接层选择、从 `http2` 切换到 `http3` 的策略、失败降级路径。
- 鉴权范围: 保留现有私有鉴权头、认证信封、链路认证逻辑，并明确经过 Cloudflare 后的真实来源标识获取方式。
- 云代理兼容范围: 明确直连入口与 Cloudflare 代理入口的并存方案，以及各自允许的链路层能力。
- 文档范围: 将该需求纳入单一协作文档，后续 Architect 与 Code 统一在本文件持续维护。

#### 1.1.3 非范围
- 本阶段不直接修改 `probe_node` 源码实现。
- 本阶段不直接落地 `cloudflared` 配置文件、部署脚本或生产环境接入。
- 不在当前阶段重构 `yamux` 复用模型或 UDP over stream 的业务承载方式。
- 不在当前阶段替换现有私有鉴权方案为 Cloudflare Access JWT 单一方案。
- 不在当前阶段决定最终公网域名、证书颁发、云防火墙与 CDN 策略细节。

#### 1.1.4 验收标准
- 协作文档中必须明确 relay 入口的目标协议集合、每种协议的职责边界与推荐路径。
- 协作文档中必须明确 `HTTP/1.1` 是否承载业务、是否允许升级、以及升级失败时的处理策略。
- 协作文档中必须明确 `HTTP/2` 与 `HTTP/3` 在客户端与服务端两侧的连接与降级策略。
- 协作文档中必须明确直连模式与 Cloudflare 代理模式的共存方式，以及各自允许的入口协议。
- 协作文档中必须明确现有私有鉴权逻辑的保留策略、Cloudflare 接入后的真实来源获取策略与兼容约束。
- 后续进入 Code 阶段前，第1.4节 `Code任务执行包` 必须给出明确文件范围、操作类型与可测试验收标准。

#### 1.1.5 风险
- 若将 `HTTP/1.1` 升级、`HTTP/2` 直连与 `HTTP/3` 切换的语义混淆，可能导致实现阶段误把“新建连接切换”理解为“同连接升级”。
- 若 Cloudflare 代理路径与直连路径共用同一组客户端策略但未做能力区分，可能导致客户端错误尝试 origin `http3`。
- 若继续依赖 `RemoteAddr` 作为来源标识，Cloudflare 接入后会破坏现有基于来源 IP 的失败计数与黑名单逻辑。
- 若 `HTTP/1.1` 继续承载业务流量，可能削弱新方案中 `HTTP/2` / `HTTP/3` 的职责边界，增加维护复杂度。

#### 1.1.6 遗留事项
- 是否保留 `HTTP/1.1` 真实业务兜底能力，当前未最终裁定，倾向仅保留为兼容或升级引导通道。
- 是否通过 `Alt-Svc` 对 `HTTP/2` 客户端宣告 `HTTP/3`，以及客户端是否主动持久化该能力缓存，待后续细化。
- Cloudflare 接入时 origin 是否固定为 `HTTPS + HTTP/2`，当前作为推荐路径，待后续 Architect 章节固化。

#### 1.1.7 结论
- 该需求正式纳入需求跟踪，后续按“直连优先 `HTTP/3`、稳定主通道 `HTTP/2`、`HTTP/1.1` 仅兼容或升级引导、保留 Cloudflare 兼容入口”的方向继续设计。

### 1.2 总体架构
- 状态: 已完成

#### 1.2.1 架构目标
- 建立可同时支持直连与 Cloudflare 代理的 relay 入口模型。
- 将 relay 入口协议职责分层，减少 `http` / `http2` / `http3` 混用造成的语义歧义。
- 在不推翻现有 `yamux` 复用与私有鉴权主模型的前提下，演进入口能力与客户端切换策略。

#### 1.2.2 总体设计
- 服务端入口采用双通道设计: `TCP` 侧提供 `HTTP/1.1 + HTTP/2`，`UDP` 侧提供 `HTTP/3`。
- `HTTP/2` 作为默认稳定业务承载入口，直连与 Cloudflare 代理均优先围绕该通道兼容。
- `HTTP/3` 作为直连优先高性能入口，通过能力宣告或客户端策略进行独立新连接切换，不定义为从 `HTTP/2` 同连接升级。
- `HTTP/1.1` 默认不作为长期业务主承载层，职责限定为兼容接入、升级引导或受控兜底。
- 保留现有私有鉴权头、Bearer/HMAC 认证信封与链路认证逻辑；Cloudflare 接入后对来源地址解析单独适配。
- 服务端内部业务 relay、`yamux` 会话管理与 chain runtime 逻辑继续复用现有模型，入口层负责协议接入、切换与兼容边界。

#### 1.2.3 关键模块
| 模块编号 | 模块名称 | 职责 | 输入 | 输出 |
|---|---|---|---|---|
| M1 | Relay Ingress Negotiator | 统一管理 `http` / `http2` / `http3` 入口能力与协议边界 | 监听配置、请求协议、客户端能力 | 实际业务入口协议 |
| M2 | Relay Auth Envelope | 复用现有私有鉴权、头字段与认证信封校验 | 请求头、chain 配置、认证 secret | 鉴权结果、来源标识 |
| M3 | Relay Session Bridge | 将入口请求桥接为内部 `yamux` 会话与 stream | 入口请求体/响应体、桥接角色 | `yamux` session、stream |
| M4 | Client Transport Strategy | 管理客户端默认入口协议、能力宣告、切换与降级 | 配置、连接结果、服务端宣告 | 实际连接协议、回退决策 |
| M5 | Cloudflare Compatibility Adapter | 约束 Cloudflare 代理入口允许的 origin 协议与来源解析 | Cloudflare 入口请求、代理头 | 兼容入口与真实来源标识 |

#### 1.2.4 关键接口
| 接口编号 | 接口名称 | 调用方 | 提供方 | 说明 |
|---|---|---|---|---|
| IF-001 | `startProbeChainPublicRelayServer()` | chain runtime 启动路径 | Relay Ingress Negotiator | 启动 relay 公网入口并按协议暴露 |
| IF-002 | `handleProbeChainRelayToRuntime()` | 外部 relay 请求入口 | Relay Auth Envelope / Relay Session Bridge | 对入口请求执行鉴权并桥接业务流 |
| IF-003 | `openProbeChainRelayNetConn()` | relay 客户端/下游连接方 | Client Transport Strategy | 按配置选择 `http` / `http2` / `http3` 建链 |
| IF-004 | `resolveProbeChainSourceIPFromRequest()` | 鉴权失败计数与来源解析逻辑 | Cloudflare Compatibility Adapter | 在直连或代理模式下统一得到来源标识 |
| IF-005 | `newProbeChainYamuxConfig()` | relay 桥接两端 | Relay Session Bridge | 提供统一复用会话参数 |

#### 1.2.5 关键约束
- `HTTP/3` 切换必须建新连接，不得在文档或实现中表述为从 `HTTP/2` 同连接升级。
- Cloudflare 代理模式下，origin 首选 `HTTPS + HTTP/2`，不要求 origin `HTTP/3` 透传。
- 现有私有鉴权逻辑必须保留，Cloudflare Access 如后续引入，仅作为叠加身份层而非替换层。
- 来源地址判断不得只依赖 `RemoteAddr`，必须为代理接入场景预留头字段解析路径。
- 在进入 Code 阶段前，必须明确 `HTTP/1.1` 的最终职责边界，避免实现范围扩散。

#### 1.2.6 风险
- 若服务端同时支持多入口但客户端策略未与入口能力对齐，可能造成错误重试风暴。
- 若 `HTTP/1.1` 被保留为完整业务兜底，后续 Cloudflare 与直连两条路径的调试复杂度会显著上升。
- 若 `Alt-Svc` 或类似能力宣告处理不当，客户端可能长期缓存错误的 `HTTP/3` 可用性状态。

#### 1.2.7 结论
- 总体架构采用“入口双栈/三栈分层 + `HTTP/2` 主通道 + `HTTP/3` 直连优先 + `HTTP/1.1` 兼容受限 + 鉴权保留 + Cloudflare 兼容适配”的方案。

### 1.3 单元设计
- 状态: 进行中

#### 1.3.1 单元清单
| 单元编号 | 单元名称 | 所属模块 | 职责 | 输入 | 输出 |
|---|---|---|---|---|---|
| U1 | Relay Listener Unit | M1 | 组织 `TCP` / `UDP` 监听与协议绑定 | 监听地址、link layer 配置 | `HTTP/1.1` / `HTTP/2` / `HTTP/3` 入口 |
| U2 | Relay Protocol Policy Unit | M1 | 约束 `HTTP/1.1`、`HTTP/2`、`HTTP/3` 的职责边界 | 请求协议、策略配置 | 放行/拒绝/升级/宣告策略 |
| U3 | Relay Auth Source Unit | M2 / M5 | 解析鉴权头与来源地址 | 请求头、代理头、RemoteAddr | 鉴权信封、真实来源标识 |
| U4 | Relay Session Bridge Unit | M3 | 将入口长流桥接为内部 `yamux` session | 请求体、响应体、桥接角色 | `yamux` session、stream |
| U5 | Client Protocol Strategy Unit | M4 | 管理默认连接、能力宣告、`HTTP/3` 切换与降级 | 客户端配置、连接结果、服务端能力 | 实际传输层选择 |

#### 1.3.2 单元设计
##### 单元编号
- 单元名称: 无
- 职责: 待后续细化。
- 输入: 无
- 输出: 无
- 处理规则: 无
- 异常规则: 无

#### 1.3.3 风险
- 单元边界尚未细化到字段与状态机级别，进入 Code 阶段前必须补齐。

#### 1.3.4 结论
- 单元设计已建立初始骨架，待后续继续细化。

### 1.4 Code任务执行包
- 状态: 未开始

#### 1.4.1 执行边界
- 允许修改: 无
- 禁止修改: 当前阶段未放行源码修改。

#### 1.4.2 任务清单
| 任务编号 | 需求编号 | 单元编号 | 文件范围 | 操作类型 | 验收标准 |
|---|---|---|---|---|---|

#### 1.4.3 源码修改规则
- 必须使用 encoding_tools/README.md 描述的接口。
- 对 C/C++ 源代码（`.c`、`.cc`、`.cpp`、`.cxx`、`.h`、`.hpp`）必须使用 `encoding_tools/encoding_safe_patch.py`。
- 对非 C/C++ 源代码可直接编辑，不强制使用 `encoding_tools/encoding_safe_patch.py`。
- encoding_tools/ 不可用或执行失败时，Code 必须记录失败命令、错误摘要、影响文件与阻塞影响，并提交第2.6节 `Code任务反馈`。
- 替代 encoding_tools/ 修改受控 C/C++ 源代码前，必须取得 Architect 明确允许。

#### 1.4.4 交付物
- relay 双栈/三栈需求设计文档。
- 后续待细化的客户端/服务端任务包。

#### 1.4.5 门禁输入
- 当前仅放行需求跟踪，不放行源码修改。

#### 1.4.6 结论
- 进入 Code 阶段前需由 Architect 补齐明确任务包。

### 1.5 Architect需求跟踪矩阵
- 状态: 进行中

| 需求编号 | 需求描述 | 架构章节 | 单元设计章节 | Code任务章节 | 状态 | 备注 |
|---|---|---|---|---|---|---|
| REQ-PN-RELAY-DUAL-STACK-001 | relay 入口双栈/三栈、Cloudflare 兼容与私有鉴权保留 | 1.2 | 1.3 | 1.4 | 进行中 | 当前仅完成需求纳管与初始架构 |

### 1.6 Architect关键接口跟踪矩阵
- 状态: 进行中

| 接口编号 | 需求编号 | 接口名称 | 调用方 | 提供方 | 输入 | 输出 | 状态 | 备注 |
|---|---|---|---|---|---|---|---|---|
| IF-001 | REQ-PN-RELAY-DUAL-STACK-001 | `startProbeChainPublicRelayServer()` | chain runtime 启动路径 | Relay Ingress Negotiator | 监听配置 | relay 入口 | 进行中 | 待细化同端口双栈策略 |
| IF-002 | REQ-PN-RELAY-DUAL-STACK-001 | `handleProbeChainRelayToRuntime()` | 外部请求入口 | Relay Auth Envelope / Relay Session Bridge | 请求头、请求体 | 鉴权结果、桥接流 | 进行中 | 待细化协议边界 |
| IF-003 | REQ-PN-RELAY-DUAL-STACK-001 | `openProbeChainRelayNetConn()` | 客户端连接路径 | Client Transport Strategy | chain 参数、link layer | 连接结果 | 进行中 | 待细化切换与降级 |
| IF-004 | REQ-PN-RELAY-DUAL-STACK-001 | `resolveProbeChainSourceIPFromRequest()` | 鉴权来源解析 | Cloudflare Compatibility Adapter | 请求上下文 | 来源标识 | 进行中 | 待明确 Cloudflare 头字段 |
| IF-005 | REQ-PN-RELAY-DUAL-STACK-001 | `newProbeChainYamuxConfig()` | relay 桥接两端 | Relay Session Bridge | 无 | yamux 配置 | 进行中 | 后续可能需要调优窗口 |

### 1.7 门禁裁判
- 状态: 进行中

#### 1.7.1 门禁输入
| 文档 | 路径 | 状态 |
|---|---|---|
| 协作文档 | doc/REQ-PN-RELAY-DUAL-STACK-001-collaboration.md | 已创建 |

#### 1.7.2 裁判检查
| 检查项 | 结果 | 证据 | 备注 |
|---|---|---|---|
| 协作文档存在 | 通过 | `doc/REQ-PN-RELAY-DUAL-STACK-001-collaboration.md` | 已创建 |
| Architect章节存在 | 通过 | 第1章 | 已初始化 |
| Code章节存在 | 通过 | 第2章 | 已初始化 |
| 必需子章节存在 | 通过 | 第1章、第2章固定子章节 | 已初始化 |
| 需求前缀一致 | 通过 | 文档头与章节矩阵 | 一致 |
| 需求编号一致 | 通过 | 文档头与矩阵 | 一致 |
| 接口编号一致 | 通过 | 1.2.4、1.6 | 当前一致 |
| 模板字段完整 | 通过 | 文档头字段完整 | 已填写 |
| Code使用encoding_tools | 无 | 当前未进入 Code 执行 | 无源码修改 |
| Code证据完整 | 无 | 当前未进入 Code 执行 | 无源码修改 |
| Code任务反馈已处理 | 通过 | 2.6 当前为空 | 无反馈 |
| 验收标准可测试 | 通过 | 1.1.4 | 需求层验收明确 |
| 需求任务覆盖完整 | 有条件通过 | 1.4.2 当前为空 | 待后续补齐任务包 |
| 任务自测覆盖完整 | 有条件通过 | 当前未进入 Code 阶段 | 待后续补齐 |
| 修改文件在允许范围内 | 通过 | 当前无源码修改 | 仅新增文档 |
| 测试失败已记录缺陷 | 通过 | 当前无测试执行 | 无 |
| 未执行测试原因完整 | 通过 | 2.5.7 | 当前无 Code 执行 |
| 遗留风险可接受 | 通过 | 1.1.5、1.2.6、1.3.3 | 当前可接受 |

#### 1.7.3 冲突记录
| 冲突编号 | 冲突条款 | 最终采用条款 | 裁决人 | 裁决结论 |
|---|---|---|---|---|

#### 1.7.4 裁判结论
- 结论: 有条件通过
- 放行阻塞: 放行
- 条件: 当前仅完成需求跟踪与初始架构，后续进入 Code 前必须补齐第1.4节任务包。
- 责任方: Architect
- 关闭要求: 在进入源码修改前完成任务清单、文件范围、操作类型与可测试验收标准。
- 整改要求: 无

#### 1.7.5 结论
- 本需求已纳入正式跟踪，允许继续在同一文档中深化 Architect 设计；当前不放行源码修改。

## 第2章 Code章节
- 章节责任角色: Code
- 状态: 未开始

### 2.1 Code需求跟踪矩阵
- 状态: 未开始

| 需求编号 | 任务编号 | 实现文件 | 实现状态 | 自测状态 | 证据 | 备注 |
|---|---|---|---|---|---|---|

### 2.2 Code关键接口跟踪矩阵
- 状态: 未开始

| 接口编号 | 需求编号 | 实现文件 | 调用方 | 提供方 | 实现状态 | 证据 | 备注 |
|---|---|---|---|---|---|---|---|

### 2.3 Code测试项跟踪矩阵
- 状态: 未开始

| 测试项编号 | 需求编号 | 任务编号 | 测试目标 | 测试方法 | 结果 | 证据 | 未执行原因 | 备注 |
|---|---|---|---|---|---|---|---|---|

### 2.4 Code缺陷跟踪矩阵
- 状态: 未开始

| 缺陷编号 | 需求编号 | 测试项编号 | 缺陷描述 | 严重级别 | 修复状态 | 修复证据 | 备注 |
|---|---|---|---|---|---|---|---|

### 2.5 Code执行证据
- 状态: 未开始

#### 2.5.1 修改接口
- 无

#### 2.5.2 配置文件
- 无

#### 2.5.3 执行报告
- 无

#### 2.5.4 影响文件
- 无

#### 2.5.5 测试命令
- 无

#### 2.5.6 自测结果
- 无

#### 2.5.7 未执行测试原因
- 当前阶段仅执行需求跟踪与 Architect 设计初始化，未进入 Code 实现与测试阶段。

#### 2.5.8 遗留风险
- 无

#### 2.5.9 回滚方案
- 删除 `doc/REQ-PN-RELAY-DUAL-STACK-001-collaboration.md` 即可回滚本次文档新增。

#### 2.5.10 结论
- 当前未进入 Code 执行阶段。

### 2.6 Code任务反馈
- 状态: 未开始

| 反馈编号 | 任务编号 | 反馈类型 | 反馈描述 | 阻塞影响 | Code建议 | Architect处理状态 | Architect处理结论 |
|---|---|---|---|---|---|---|---|

#### 2.6.1 结论
- 无
