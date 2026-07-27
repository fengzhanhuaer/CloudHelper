# 协作文档

- 适用规则: AI协作规则
- 后续工作传递声明: 本文档必须传递给后续阶段与后续角色。
- 需求编号: REQ-PC-DOH-GATEWAY-001
- 需求前缀: REQ-PC-DOH-GATEWAY-001
- 当前阶段: Architect最终门禁完成
- 最近更新角色: Architect
- 最近更新时间: 2026-07-27T15:30:00+08:00
- 工作依据文档: doc/ai-coding-collaboration.md; 用户在 2026-07-27 提出的主控 DoH 界面、DoH 查询记录与 TUN 网关技术方案需求; doc/REQ-PN-VROUTER-DNS-001-collaboration.md
- 状态: 已完成

## 第1章 Architect章节
- 章节责任角色: Architect
- 状态: 已完成

### 1.1 需求定义
- 状态: 已完成

#### 1.1.1 需求目标
- REQ-PC-DOH-GATEWAY-001-R01: 主控提供符合 RFC 8484 报文格式的 DoH GET/POST 查询入口。
- REQ-PC-DOH-GATEWAY-001-R02: DoH 按主控现有虚拟路由域名规则分流；命中 `probe_exit` 的 A 查询返回主控分配的 Fake IP，命中 `reject` 返回拒绝，未命中或命中 `direct` 返回真实上游 DNS 响应。
- REQ-PC-DOH-GATEWAY-001-R03: `/mng/route` 增加 DoH 页签，管理启用状态、上游与访问令牌，并展示、筛选、刷新和清空查询记录。
- REQ-PC-DOH-GATEWAY-001-R04: DoH 查询记录只保存在主控内存，不写运行日志、不落盘，避免域名历史长期泄露。
- REQ-PC-DOH-GATEWAY-001-R05: 形成可复用的 DoH + TUN 局域网网关技术方案，明确转发、回程、安全边界和分阶段实施条件。

#### 1.1.2 需求范围
- 本次 Code 范围包括主控 DoH 服务、主控管理 API、`/mng/route` DoH 页签、内存查询记录和自动化测试。
- 本文档负责定义后续 Windows、Linux、Docker TUN 网关模式的数据面方案。
- DoH 访问使用随机高熵路径令牌；管理接口继续使用主控管理会话认证。

#### 1.1.3 非范围
- 本次不启用 Windows/Linux IP forwarding，不修改防火墙、NAT、客户端静态路由或 Docker 网络。
- 本次不把 DoH 查询记录写入 `data/`、`temp/`、主控日志或外部数据库。
- 本次不向 DNS 客户端返回 CloudHelper 私有映射结构；标准 DNS 响应只包含 DNS 资源记录。
- 本次不改变探针现有本地 DNS、Android VpnService 或 TUN 全局接管开关。

#### 1.1.4 验收标准
- AC-01: 未启用、非 HTTPS、令牌错误、方法错误或畸形报文均被拒绝，且不进入上游解析。
- AC-02: DoH POST `application/dns-message` 与 GET `dns=` 均能处理单问题 DNS 报文。
- AC-03: `probe_exit` A 查询返回 `198.18.0.0/15` 内的主控 Fake IP；该 DNS 响应不包含私有映射 JSON。
- AC-04: `probe_exit` 的非 A 查询返回空成功响应，不能通过 AAAA/HTTPS/SVCB 获得真实地址绕过代理。
- AC-05: `direct` 和未命中查询通过配置的 HTTPS DoH 上游返回，所有非 OPT TTL 归一为 600 秒。
- AC-06: 查询记录包含时间、客户端、域名、类型、动作、应答、出口、状态与耗时，最多保留 500 条且可清空。
- AC-07: `/mng/route` DoH 页签可保存配置、轮换令牌、复制端点、筛选/刷新/清空记录，并在桌面和移动宽度无重叠。
- AC-08: 主控 Go 测试通过；页面无脚本错误，目标交互有渲染证据。
- AC-09: 本文档明确 TUN 网关后续实施前置条件、状态表、回程策略、授权边界和回滚方案。

#### 1.1.5 风险
- 公开递归 DoH 可被滥用；必须同时使用 HTTPS、路径令牌、请求体限制、并发限制和按客户端限速。
- Fake IP 查询与数据首包之间存在映射预热竞态；本次保留探针按 Fake IP 回查能力，定向预热作为网关实施阶段接口。
- DoH 路径令牌会出现在客户端配置中；管理页面必须只对已认证管理员返回，轮换后旧端点立即失效。
- 上游 DoH 能看到未命中域名；该风险不能被“客户端到主控加密”消除。

#### 1.1.6 遗留事项
- TUN 网关 Code 实施需单独进入后续 Code 任务包，覆盖 Windows、Linux、Docker 三类平台。
- 后续评估为每个网关探针分配独立 DoH 令牌，以支持定向预热、撤销和审计归属。

#### 1.1.7 结论
- 主控 DoH 与查询记录可在不改动探针数据面的前提下先行交付；TUN 网关仅形成技术方案，不伪装为已完成能力。

### 1.2 总体架构
- 状态: 已完成

#### 1.2.1 架构目标
- 让普通真实地址流量保持客户端直连，只有命中路由规则的域名获得 Fake IP 并进入 `198.18.0.0/15` 专用数据面。
- 保持主控为路由规则和 Fake IP 映射唯一权威来源。

#### 1.2.2 总体设计
- 当前交付链路: DNS 客户端 -> 主控 HTTPS DoH -> 路由规则匹配 -> Fake IP 响应或上游 DoH 真实响应 -> 主控内存查询记录 -> `/mng/route` DoH 页签。
- 后续网关链路: 局域网客户端把 `198.18.0.0/15` 静态路由指向网关探针；真实 IP 继续走客户端原默认网关；网关探针只接管 Fake IP 流量。
- 网关回程必须采用状态化转换：记录客户端五元组，将进入 CloudHelper 虚拟路由的数据源转换为本探针虚拟身份，回包到达源探针后再恢复客户端地址并写回 LAN。禁止仅以“路径终点”作为任意 LAN 投递授权。
- 网关必须配置允许的 LAN CIDR、最大并发、空闲超时和防火墙规则；默认关闭，不能由升级自动开启。

#### 1.2.3 关键模块
| 模块编号 | 模块名称 | 职责 | 输入 | 输出 |
|---|---|---|---|---|
| MOD-DOH-01 | DoH配置与认证 | 持久化启用状态、上游、访问令牌并校验公开请求 | 管理配置、HTTP请求 | 有效DoH上下文或拒绝 |
| MOD-DOH-02 | DNS分流解析器 | 解析DNS问题、匹配路由规则、分配Fake IP或查询上游 | DNS wire报文 | DNS wire响应 |
| MOD-DOH-03 | 查询记录仓库 | 内存环形保留查询摘要和统计 | 查询结果 | 查询列表与摘要 |
| MOD-DOH-04 | 管理界面 | 配置DoH并展示查询记录 | 管理API | 可操作的DoH页签 |
| MOD-GW-01 | TUN网关入口（后续） | 接收LAN转发到Fake IP网段的报文 | LAN IPv4报文 | 受控网关流 |
| MOD-GW-02 | 网关状态表（后续） | 维护五元组、源地址转换与回程恢复 | 正向/反向报文 | 可验证回程映射 |

#### 1.2.4 关键接口
| 接口编号 | 接口名称 | 调用方 | 提供方 | 说明 |
|---|---|---|---|---|
| IF-DOH-001 | `/dns-query/{token}` | 系统/浏览器DoH客户端 | 主控 | RFC 8484 GET/POST，公开但令牌鉴权且仅HTTPS |
| IF-DOH-002 | `/mng/api/route/doh` | 路由管理页 | 主控 | GET读取配置，POST保存或轮换令牌 |
| IF-DOH-003 | `/mng/api/route/doh/queries` | 路由管理页 | 主控 | GET读取内存记录，DELETE清空 |
| IF-GW-001 | Fake IP映射预热命令（后续） | 主控 | 网关探针 | 按网关身份定向发送域名、Fake IP和出口摘要 |
| IF-GW-002 | 网关状态接口（后续） | 探针本地控制台 | 网关数据面 | 展示流量、状态表、丢弃与回程统计 |

#### 1.2.5 关键约束
- Fake IP DNS TTL 固定为 600 秒；直连响应非 OPT 记录 TTL 同样归一为 600 秒。
- 每个 DNS 报文只允许一个问题；请求体和响应体均有上限。
- DoH 配置持久化，查询记录不持久化。
- 公开 DoH 不接受管理 Cookie 作为唯一授权，避免跨站请求和系统客户端不支持 Cookie 的问题。
- 后续网关不得开放默认路由接管；客户端只下发 `198.18.0.0/15` 路由。

#### 1.2.6 风险
- Windows 与 Linux 的内核转发语义不同，Docker 还涉及 capability 和 namespace；后续必须分平台测试。
- UDP/QUIC 无 TCP 重传兜底，映射预热和网关状态表必须在首包前可用或提供有界同步补查。

#### 1.2.7 结论
- 架构允许 DoH 控制面先交付，并为后续 TUN 网关保留清晰、安全的实现边界。

### 1.3 单元设计
- 状态: 已完成

#### 1.3.1 单元清单
| 单元编号 | 单元名称 | 所属模块 | 职责 | 输入 | 输出 |
|---|---|---|---|---|---|
| UNIT-DOH-01 | DoH配置单元 | MOD-DOH-01 | 规范化、保存、轮换令牌 | 管理请求 | DoH配置 |
| UNIT-DOH-02 | DoH请求单元 | MOD-DOH-01 | HTTPS、令牌、方法、大小、限流校验 | HTTP请求 | DNS请求报文 |
| UNIT-DOH-03 | DNS决策单元 | MOD-DOH-02 | 匹配direct/reject/probe_exit | 域名和类型 | 决策与规则 |
| UNIT-DOH-04 | DNS响应单元 | MOD-DOH-02 | 构造Fake/NODATA/拒绝或规范化上游响应 | DNS请求与决策 | DNS响应 |
| UNIT-DOH-05 | 查询记录单元 | MOD-DOH-03 | 聚合内存记录并提供快照/清空 | 查询摘要 | 记录列表 |
| UNIT-DOH-06 | DoH页面单元 | MOD-DOH-04 | 配置与记录交互 | 管理API | DoH页签 |
| UNIT-GW-01 | 网关入口单元（后续） | MOD-GW-01 | 校验LAN来源和Fake网段 | 转发报文 | 允许或丢弃 |
| UNIT-GW-02 | 网关NAT单元（后续） | MOD-GW-02 | 建立状态、改写源、恢复回程 | 五元组报文 | 双向报文 |

#### 1.3.2 单元设计
##### UNIT-DOH-01
- 单元名称: DoH配置单元
- 职责: 保存启用状态、单一HTTPS上游和随机路径令牌。
- 输入: 管理会话认证后的JSON。
- 输出: 规范化配置与端点路径。
- 处理规则: 空令牌生成32字节随机值；轮换立即替换；上游只允许HTTPS且禁止userinfo和fragment。
- 异常规则: 配置不合法返回400，保存失败返回500。

##### UNIT-DOH-02
- 单元名称: DoH请求单元
- 职责: 处理RFC 8484 GET/POST并实施入口防护。
- 输入: HTTP请求。
- 输出: 单问题DNS wire报文。
- 处理规则: 常量时间比较令牌；只信任既有可信代理链提供的客户端IP；限制并发和每IP速率。
- 异常规则: 禁用或令牌错误返回404，非HTTPS返回426，格式错误返回400/415/413/429。

##### UNIT-DOH-03
- 单元名称: DNS决策单元
- 职责: 使用现有域名规则匹配器形成动作。
- 输入: 规范域名、查询类型、当前路由规则。
- 输出: direct、reject或fake_ip。
- 处理规则: 按现有规则顺序首个命中生效；CIDR规则不参与域名查询。
- 异常规则: `probe_exit` 出口失效时返回SERVFAIL并记录失败。

##### UNIT-DOH-04
- 单元名称: DNS响应单元
- 职责: 构造或代理DNS响应。
- 输入: DNS请求和决策。
- 输出: TTL为600秒的DNS响应。
- 处理规则: fake_ip仅对A返回地址；其他类型返回NOERROR空答案；direct使用上游DoH。
- 异常规则: 上游超时、非2xx、畸形响应或问题不匹配均返回SERVFAIL。

##### UNIT-DOH-05
- 单元名称: 查询记录单元
- 职责: 保存最近500条摘要。
- 输入: 查询时间、客户端、问题、动作、答案、状态和耗时。
- 输出: 倒序记录和汇总计数。
- 处理规则: 只在内存保存；清空增加修订号；不写标准日志。
- 异常规则: 无。

##### UNIT-DOH-06
- 单元名称: DoH页面单元
- 职责: 管理配置并展示记录。
- 输入: IF-DOH-002、IF-DOH-003。
- 输出: DoH状态、端点、记录表。
- 处理规则: 页签激活时加载并每3秒刷新记录；端点点击或按钮复制；清空需确认。
- 异常规则: API失败显示页面状态，不清除最后一次成功数据。

##### UNIT-GW-01
- 单元名称: 网关入口单元（后续）
- 职责: 仅允许配置LAN CIDR到Fake IP CIDR的转发流量。
- 输入: 物理网卡转发报文。
- 输出: 允许进入TUN或拒绝。
- 处理规则: 默认拒绝；明确启用、源CIDR允许、目的地址属于Fake池且映射有效时才允许。
- 异常规则: 未授权、映射缺失、队列饱和按原因计数并限频记录。

##### UNIT-GW-02
- 单元名称: 网关NAT单元（后续）
- 职责: 保证LAN客户端流量能按源探针身份进入虚拟路由并正确回程。
- 输入: 正向和反向IPv4 TCP/UDP/ICMP报文。
- 输出: 校验和正确的改写报文。
- 处理规则: 使用协议+客户端地址端口+Fake目标地址端口作为状态键；端口冲突时分配网关侧端口；TCP关闭或空闲超时回收，UDP短超时回收。
- 异常规则: 状态缺失的回包不得投递到LAN；禁止远端节点借路径终点向任意LAN地址注入报文。

#### 1.3.3 风险
- 直接复用探针现有异步 Fake IP 补查会丢首个 UDP 报文；后续网关实施必须关闭该竞态。
- 查询记录含域名和客户端IP，虽然不落盘，管理页面仍属于敏感数据面。

#### 1.3.4 结论
- 单元边界满足本次主控交付，并将网关关键安全约束固化为后续实现输入。

### 1.4 Code任务执行包
- 状态: 已放行

#### 1.4.1 执行边界
- 允许修改: `doc/REQ-PC-DOH-GATEWAY-001-collaboration.md`; `probe_controller/go.mod`; `probe_controller/go.sum`; `probe_controller/internal/core/probe_route_config_store.go`; `probe_controller/internal/core/probe_doh.go`; `probe_controller/internal/core/probe_doh_test.go`; `probe_controller/internal/core/server.go`; `probe_controller/internal/core/mng_pages/route.html`。
- 禁止修改: 探针、Android、TUN数据面、系统路由、防火墙、安装脚本、部署脚本及其他主控页面。

#### 1.4.2 任务清单
| 任务编号 | 需求编号 | 单元编号 | 文件范围 | 操作类型 | 验收标准 |
|---|---|---|---|---|---|
| TASK-DOH-01 | REQ-PC-DOH-GATEWAY-001-R01,R02,R04 | UNIT-DOH-01..05 | `probe_route_config_store.go`; `probe_doh.go`; `server.go`; `go.mod`; `go.sum` | 新增、修改 | AC-01..06 |
| TASK-DOH-02 | REQ-PC-DOH-GATEWAY-001-R03 | UNIT-DOH-06 | `mng_pages/route.html` | 修改 | AC-07 |
| TASK-DOH-03 | REQ-PC-DOH-GATEWAY-001-R01..R04 | UNIT-DOH-01..06 | `probe_doh_test.go` | 新增 | AC-01..08 |
| TASK-DOH-04 | REQ-PC-DOH-GATEWAY-001-R05 | UNIT-GW-01,UNIT-GW-02 | `doc/REQ-PC-DOH-GATEWAY-001-collaboration.md` | 新增、修改 | AC-09 |

#### 1.4.3 源码修改规则
- 修改源代码时必须注意可能存在的 GBK 编码并保持原文件编码，避免乱码或误转码。

#### 1.4.4 交付物
- 主控 DoH 服务与管理 API。
- `/mng/route` DoH 页签。
- DoH 单元/处理器测试与页面渲染证据。
- 本协作文档中的 DoH + TUN 网关技术方案和追踪矩阵。

#### 1.4.5 门禁输入
- Code 只能修改允许范围；必须执行主控目标测试、全量测试和页面渲染验证。
- 任一测试失败必须记录缺陷或说明与本需求无关的既有失败。

#### 1.4.6 结论
- Code任务包完整，允许进入实现。

### 1.5 Architect需求跟踪矩阵
- 状态: 已完成

| 需求编号 | 需求描述 | 架构章节 | 单元设计章节 | Code任务章节 | 状态 | 备注 |
|---|---|---|---|---|---|---|
| REQ-PC-DOH-GATEWAY-001-R01 | 主控DoH入口 | 1.2.2 | UNIT-DOH-01,02,04 | TASK-DOH-01,03 | 已完成 | 无 |
| REQ-PC-DOH-GATEWAY-001-R02 | 路由分流与Fake IP | 1.2.2 | UNIT-DOH-03,04 | TASK-DOH-01,03 | 已完成 | 无 |
| REQ-PC-DOH-GATEWAY-001-R03 | DoH管理页和记录 | 1.2.2 | UNIT-DOH-05,06 | TASK-DOH-02,03 | 已完成 | 无 |
| REQ-PC-DOH-GATEWAY-001-R04 | 查询记录仅内存 | 1.2.2 | UNIT-DOH-05 | TASK-DOH-01,03 | 已完成 | 无 |
| REQ-PC-DOH-GATEWAY-001-R05 | TUN网关技术方案 | 1.2.2 | UNIT-GW-01,02 | TASK-DOH-04 | 已完成 | 本次不实现网关代码 |

### 1.6 Architect关键接口跟踪矩阵
- 状态: 已完成

| 接口编号 | 需求编号 | 接口名称 | 调用方 | 提供方 | 输入 | 输出 | 状态 | 备注 |
|---|---|---|---|---|---|---|---|---|
| IF-DOH-001 | REQ-PC-DOH-GATEWAY-001-R01,R02 | `/dns-query/{token}` | DoH客户端 | 主控 | DNS wire | DNS wire | 已完成 | GET/POST |
| IF-DOH-002 | REQ-PC-DOH-GATEWAY-001-R03 | `/mng/api/route/doh` | 路由页 | 主控 | 管理JSON | 配置JSON | 已完成 | 管理会话鉴权 |
| IF-DOH-003 | REQ-PC-DOH-GATEWAY-001-R03,R04 | `/mng/api/route/doh/queries` | 路由页 | 主控 | GET/DELETE | 记录JSON | 已完成 | 内存数据 |
| IF-GW-001 | REQ-PC-DOH-GATEWAY-001-R05 | Fake IP预热命令 | 主控 | 网关探针 | 映射摘要 | 应用结果 | 未开始 | 后续任务 |
| IF-GW-002 | REQ-PC-DOH-GATEWAY-001-R05 | 网关状态接口 | 本地控制台 | 网关数据面 | 查询 | 状态JSON | 未开始 | 后续任务 |

### 1.7 门禁裁判
- 状态: 已完成

#### 1.7.1 门禁输入
| 文档 | 路径 | 状态 |
|---|---|---|
| 协作文档 | doc/REQ-PC-DOH-GATEWAY-001-collaboration.md | 已完成 |

#### 1.7.2 裁判检查
| 检查项 | 结果 | 证据 | 备注 |
|---|---|---|---|
| 协作文档存在 | 通过 | 本文件 | 无 |
| Architect章节存在 | 通过 | 第1章 | 无 |
| Code章节存在 | 通过 | 第2章 | 证据已填写 |
| 必需子章节存在 | 通过 | 1.1..1.7、2.1..2.6 | 无 |
| 需求前缀一致 | 通过 | 全文REQ-PC-DOH-GATEWAY-001 | 无 |
| 需求编号一致 | 通过 | 1.5与任务包 | 无 |
| 接口编号一致 | 通过 | 1.2.4与1.6 | 无 |
| 模板字段完整 | 通过 | 文档头及固定章节 | 无 |
| GBK编码文件无乱码或误转码 | 通过 | 修改文件严格UTF-8解码成功且无U+FFFD | 本次未修改GBK文件 |
| Code证据完整 | 通过 | 第2.5节 | 无 |
| Code任务反馈已处理 | 通过 | 当前无反馈 | 无 |
| 验收标准可测试 | 通过 | AC-01..09 | 无 |
| 需求任务覆盖完整 | 通过 | 1.5 | 无 |
| 任务自测覆盖完整 | 通过 | 第2.3节 | 无 |
| 修改文件在允许范围内 | 通过 | git status与第1.4.1节比对 | 无 |
| 测试失败已记录缺陷 | 通过 | DEF-DOH-01 | 既有setup token测试环境问题 |
| 未执行测试原因完整 | 通过 | 第2.5.7节 | race受CGO环境限制 |
| 遗留风险可接受 | 通过 | 第2.5.8节 | 网关代码明确未实现 |

#### 1.7.3 冲突记录
| 冲突编号 | 冲突条款 | 最终采用条款 | 裁决人 | 裁决结论 |
|---|---|---|---|---|
| 无 | 无 | 无 | Architect | 无 |

#### 1.7.4 裁判结论
- 结论: 通过
- 放行阻塞: 放行
- 条件: 无
- 责任方: 无
- 关闭要求: 无
- 整改要求: 无

#### 1.7.5 结论
- 主控DoH、查询记录界面和技术方案具备实现与验证证据，最终门禁通过；TUN网关代码仍明确属于后续需求。

## 第2章 Code章节
- 章节责任角色: Code
- 状态: 已完成

### 2.1 Code需求跟踪矩阵
- 状态: 已完成

| 需求编号 | 任务编号 | 实现文件 | 实现状态 | 自测状态 | 证据 | 备注 |
|---|---|---|---|---|---|---|
| REQ-PC-DOH-GATEWAY-001-R01 | TASK-DOH-01,03 | `probe_doh.go`; `server.go` | 已完成 | 已完成 | TEST-DOH-01 | 无 |
| REQ-PC-DOH-GATEWAY-001-R02 | TASK-DOH-01,03 | `probe_doh.go`; `probe_route_config_store.go` | 已完成 | 已完成 | TEST-DOH-01 | 无 |
| REQ-PC-DOH-GATEWAY-001-R03 | TASK-DOH-02,03 | `mng_pages/route.html`; `probe_doh.go` | 已完成 | 已完成 | TEST-DOH-02,03 | 无 |
| REQ-PC-DOH-GATEWAY-001-R04 | TASK-DOH-01,03 | `probe_doh.go` | 已完成 | 已完成 | TEST-DOH-02 | 记录仅内存 |
| REQ-PC-DOH-GATEWAY-001-R05 | TASK-DOH-04 | 本文档 | 已完成 | 已完成 | TEST-DOH-04 | 仅技术方案 |

### 2.2 Code关键接口跟踪矩阵
- 状态: 已完成

| 接口编号 | 需求编号 | 实现文件 | 调用方 | 提供方 | 实现状态 | 证据 | 备注 |
|---|---|---|---|---|---|---|---|
| IF-DOH-001 | REQ-PC-DOH-GATEWAY-001-R01,R02 | `probe_doh.go`; `server.go` | DoH客户端 | 主控 | 已完成 | `TestProbeControllerDoH*` | 无 |
| IF-DOH-002 | REQ-PC-DOH-GATEWAY-001-R03 | `probe_doh.go`; `mng_pages/route.html` | 路由页 | 主控 | 已完成 | `TestProbeControllerDoHRejectAndManagementAPIs` | 无 |
| IF-DOH-003 | REQ-PC-DOH-GATEWAY-001-R03,R04 | `probe_doh.go`; `mng_pages/route.html` | 路由页 | 主控 | 已完成 | 管理API单测与Playwright | 无 |
| IF-GW-001 | REQ-PC-DOH-GATEWAY-001-R05 | 无 | 主控 | 网关探针 | 未开始 | 方案见1.2.4 | 后续任务 |
| IF-GW-002 | REQ-PC-DOH-GATEWAY-001-R05 | 无 | 本地控制台 | 网关数据面 | 未开始 | 方案见1.2.4 | 后续任务 |

### 2.3 Code测试项跟踪矩阵
- 状态: 已完成

| 测试项编号 | 需求编号 | 任务编号 | 测试目标 | 测试方法 | 结果 | 证据 | 未执行原因 | 备注 |
|---|---|---|---|---|---|---|---|---|
| TEST-DOH-01 | REQ-PC-DOH-GATEWAY-001-R01,R02 | TASK-DOH-01,03 | DoH鉴权、GET/POST和分流 | `go test ./internal/core -count=1` | 通过 | 核心包通过 | 无 | 覆盖Fake A、AAAA防绕过、直连TTL、拒绝、错误令牌 |
| TEST-DOH-02 | REQ-PC-DOH-GATEWAY-001-R03,R04 | TASK-DOH-02,03 | 管理API、记录与清空 | `go test ./internal/core -count=1` | 通过 | 核心包通过 | 无 | 覆盖令牌轮换与查询清空 |
| TEST-DOH-03 | REQ-PC-DOH-GATEWAY-001-R03 | TASK-DOH-02 | 页面交互与响应式布局 | Playwright + 本机Chrome | 通过 | desktop/filter/mobile截图；控制台0错误 | 无 | Browser plugin不可用，Playwright自带Chromium缺失后使用系统Chrome |
| TEST-DOH-04 | REQ-PC-DOH-GATEWAY-001-R05 | TASK-DOH-04 | 网关方案完整性 | 固定章节和字段检查 | 通过 | 第1章及最终门禁 | 无 | 网关代码仍为非范围 |

### 2.4 Code缺陷跟踪矩阵
- 状态: 已完成

| 缺陷编号 | 需求编号 | 测试项编号 | 缺陷描述 | 严重级别 | 修复状态 | 修复证据 | 备注 |
|---|---|---|---|---|---|---|---|
| DEF-DOH-01 | REQ-PC-DOH-GATEWAY-001-R01 | TEST-DOH-01 | `go test ./...` 中4个既有管理认证用例因环境setup token不一致返回 `invalid setup token` | 低 | 不在本需求修复 | `internal/core`通过，失败均位于既有`probe_controller/tests` | 与本次DoH代码无调用关系 |

### 2.5 Code执行证据
- 状态: 已完成

#### 2.5.1 修改接口
- 新增 IF-DOH-001 `/dns-query/{token}`。
- 新增 IF-DOH-002 `/mng/api/route/doh`。
- 新增 IF-DOH-003 `/mng/api/route/doh/queries`。

#### 2.5.2 配置文件
- `probe_route_config.json` 增加可选 `doh` 节点，保存启用状态、HTTPS上游、高熵访问令牌与更新时间。
- 查询记录不进入任何配置文件。

#### 2.5.3 执行报告
- DoH支持RFC 8484 GET/POST、HTTPS和令牌校验、每IP每分钟1200次限制、128并发限制及64KiB报文上限。
- `probe_exit` A 返回Fake IP；代理规则非A返回空成功，阻止AAAA/HTTPS等真实地址绕过；direct使用HTTPS上游；TTL统一600秒。
- 查询记录最多500条，仅内存保存，不打印正常查询日志。
- 页面DoH页签激活后每3秒刷新记录，离开页签停止；支持保存、轮换、复制、筛选和清空。

#### 2.5.4 影响文件
- `doc/REQ-PC-DOH-GATEWAY-001-collaboration.md`
- `probe_controller/go.mod`
- `probe_controller/internal/core/probe_route_config_store.go`
- `probe_controller/internal/core/probe_doh.go`
- `probe_controller/internal/core/probe_doh_test.go`
- `probe_controller/internal/core/server.go`
- `probe_controller/internal/core/mng_pages/route.html`

#### 2.5.5 测试命令
- `go test ./internal/core -count=1`
- `go vet ./internal/core`
- `go test ./...`
- `go test -race ./internal/core -run 'TestProbeControllerDoH|TestMngRoutePageIncludesDoH' -count=1`
- Playwright使用本机Chrome访问本地静态路由页并拦截管理API。
- `git diff --check`
- 严格UTF-8解码与U+FFFD检查。

#### 2.5.6 自测结果
- `go test ./internal/core -count=1`: 通过。
- `go vet ./internal/core`: 通过。
- `go test ./...`: `internal/core`通过；仅4个既有setup token环境用例失败，见DEF-DOH-01。
- Playwright: 页面标题正确、DoH页签非空、4条记录渲染、失败筛选为1条、保存/复制/清空成功、桌面1440x900与移动390x844无重叠、控制台0错误。
- 截图: `C:/Users/fengz/AppData/Local/Temp/cloudhelper-doh-desktop.png`; `cloudhelper-doh-filtered.png`; `cloudhelper-doh-mobile.png`。
- `git diff --check`: 通过；修改文件严格UTF-8解码成功且无替换字符。

#### 2.5.7 未执行测试原因
- Race测试未执行：当前Go环境提示 `-race requires cgo; enable cgo by setting CGO_ENABLED=1`。
- 未连接或部署线上主控，遵守工程仅在线更新、不得手工部署的约束。

#### 2.5.8 遗留风险
- TUN网关代码不在本次范围，当前不能宣称探针已可作为LAN网关。
- 当前为单一共享DoH令牌；多网关定向撤销和Fake IP预热需按第1章后续方案实现每网关身份。
- DoH GET的DNS参数以及路径令牌可能进入反向代理访问日志，生产使用应优先POST并限制代理日志字段。

#### 2.5.9 回滚方案
- 删除新增DoH路由和实现文件，恢复路由页面与路由配置存储结构；已有虚拟路由数据不应被删除。

#### 2.5.10 结论
- Code任务包已完成并提交Architect最终门禁。

### 2.6 Code任务反馈
- 状态: 已完成

| 反馈编号 | 任务编号 | 反馈类型 | 反馈描述 | 阻塞影响 | Code建议 | Architect处理状态 | Architect处理结论 |
|---|---|---|---|---|---|---|---|
| 无 | 无 | 无 | 无 | 无 | 无 | 已完成 | 无 |

#### 2.6.1 结论
- 当前任务包无缺口，允许继续执行。
