# 协作文档

- 适用规则: AI协作规则
- 后续工作传递声明: 本文档必须传递给后续阶段与后续角色。
- 需求编号: REQ-PN-DDNS-001
- 需求前缀: REQ-PN-DDNS-001
- 当前阶段: Architect最终门禁
- 最近更新角色: Architect
- 最近更新时间: 2026-07-29T09:15:00+08:00
- 工作依据文档: `doc/ai-coding-collaboration.md`、用户提出的独立新需求“探针DDNS功能”及后续裁定、`probe_node/local_pages/system.html`、`probe_node/local_console.go`、`probe_node/ip_report_settings.go`、`probe_node/public_ip.go`、`probe_node/main.go`、`probe_controller/internal/core/cloudflare_assistant.go`、`probe_controller/internal/core/probe_certificate.go`
- 状态: 已完成

## 第1章 Architect章节
- 章节责任角色: Architect
- 状态: 已完成

### 1.1 需求定义
- 状态: 已完成

#### 1.1.1 需求目标
- 开启“探针DDNS功能”独立新需求的需求跟踪。
- 在探针侧系统设置中新增独立 DDNS 功能。
- 支持特定网卡 IP 和公网出口 IP 两类 DDNS，并通过 Cloudflare 更新用户配置的完整域名。
- 使用 Let's Encrypt 为配置域名申请证书。
- 将上述配置统一存储到 `./data/ddns`。

#### 1.1.2 需求范围
- 已确认: 这是一个独立的新需求。
- 已确认: 功能入口位于探针侧“系统设置”。
- 已确认: 第一类 DDNS 允许用户选择特定网卡，并为该网卡的 IP 配置 DDNS 完整域名；完整域名可以配置多个。
- 已确认: 第二类 DDNS使用探针公网出口 IP，并允许配置多个 DDNS 完整域名。
- 已确认: 用户提供 Cloudflare API Key，探针使用该凭据执行 DNS 操作。
- 已确认: 使用 Let's Encrypt 为域名申请证书。
- 已确认: 上述配置存储到 `./data/ddns`。
- 已确认: Cloudflare Key 种类与主控侧一致；经代码核对为 Bearer API Token，不支持 Global API Key。
- 已确认: 第一类 DDNS 仅选择一个网卡。
- 已确认: 每个完整域名绑定所选来源的全部 IP；IPv4 使用 A 记录，IPv6 使用 AAAA 记录。
- 已确认: 所有已配置域名签发为一张多域名 SAN 证书。
- 已确认: 证书自动续期，域名定期自动更新 IP。
- 已确认: 删除域名或停用配置时不主动删除 Cloudflare DNS 记录。
- 已确认: DDNS 配置、运行状态、ACME 账户密钥、域名证书与私钥均存储到 `./data/ddns`。
- 已确认的现状依据: 探针系统设置页面为 `probe_node/local_pages/system.html`；系统设置 API 注册位于 `probe_node/local_console.go`；稳定网卡身份和 IP 枚举可参考 `probe_node/ip_report_settings.go`；公网出口 IP 采集现有入口为 `probe_node/public_ip.go`。

#### 1.1.3 非范围
- 不继承此前其他需求的设计、实现或结论。
- 不修改虚拟路由、移动端或主控下发证书流程。
- 当前阶段不执行生产部署。

#### 1.1.4 验收标准
- AC-001: 探针侧系统设置可配置特定网卡 DDNS，能够选择网卡并保存多个完整域名。
- AC-002: 探针侧系统设置可配置公网出口 DDNS，并保存多个完整域名。
- AC-003: Cloudflare API Key 可由用户保存，读取接口和页面不得回显完整密钥。
- AC-004: 特定网卡 IP 变化后，其所有已配置完整域名均更新为当前地址。
- AC-005: 公网出口 IP 变化后，其所有已配置完整域名均更新为当前出口地址。
- AC-006: 探针能为配置域名通过 Let's Encrypt 申请证书，并记录申请结果与有效期。
- AC-007: 本需求配置位于 `resolveDataDir()/ddns`；默认运行目录下对应 `./data/ddns`。
- AC-008: 配置加载、保存、DDNS 更新、证书申请和错误状态具备自动化测试证据。
- AC-009: 所有配置域名写入同一张 SAN 证书；证书在剩余有效期不超过 30 天时自动续期，检查周期为 6 小时。
- AC-010: DDNS 启动后及每 10 分钟执行一次收敛；保存配置后触发一次异步收敛。
- AC-011: 删除域名或停用配置不删除远端记录；正常地址集合减少时必须移除本功能已登记的多余记录，避免旧 IP 继续解析。
- AC-012: 本地 API 不返回 API Token、ACME 账户私钥或证书私钥正文。

#### 1.1.5 风险
- DNS-01 申请需要创建和清理 `_acme-challenge` TXT 记录，Cloudflare 凭据必须具备对应 Zone 的 DNS 编辑权限。
- 多个完整域名可能属于不同 Cloudflare Zone，必须按最长域名后缀解析每个 Zone，并要求同一 API Token 对所有 Zone 有权限。
- Let’s Encrypt 对单张证书的域名数量存在限制；第一版配置最多接受 100 个去重域名。
- 网卡临时消失或公网 IP 探测失败时不得把空结果当作有效变更，以免误清理仍可用记录。

#### 1.1.6 遗留事项
- 无。

#### 1.1.7 结论
- 用户已完成关键产品规则裁定，可以进入总体架构、单元设计和任务包评审。

### 1.2 总体架构
- 状态: 已完成

#### 1.2.1 架构目标
- 在探针进程内建立独立 DDNS 管理器，不依赖主控运行时同步。
- 复用探针现有稳定网卡身份与公网出口探测能力，统一驱动 Cloudflare DNS 和 ACME DNS-01。
- 保证凭据、证书私钥和运行状态均在 `resolveDataDir()/ddns` 内持久化并脱敏展示。

#### 1.2.2 总体设计
- 配置层保存一个选定网卡、网卡完整域名列表、公网出口完整域名列表和 Cloudflare API Token。
- 地址层按稳定网卡 ID 找到单个网卡，收集其全部有效 IPv4/IPv6；公网出口复用 `collectPublicIPs()`。
- DNS 收敛层按每个域名生成全部 A/AAAA 期望记录，通过 Cloudflare Zone 查询按最长后缀匹配 Zone；同名同类型允许多个记录值。
- DNS 收敛层每 10 分钟运行一次，进程启动和配置保存后也触发；同一时刻只运行一个任务，连续触发合并为下一轮。
- 对配置中仍存在的域名，地址减少时删除本功能状态文件登记的多余记录；用户删除域名或停用功能时不主动删除远端记录。
- 证书层将网卡域名和公网出口域名合并、规范化、去重后签发一张 SAN 证书；使用 Cloudflare DNS-01 完成验证。
- 证书层每 6 小时检查，证书不存在、SAN 集合变化或剩余有效期不超过 30 天时申请；续期失败且旧证书仍有效时继续保留旧证书。
- API 层提供同一系统设置资源的读取和保存；读取只返回 `api_token_configured`，不得返回 Token 正文。
- 页面层在探针“系统设置”新增 DDNS 区域，展示配置、网卡、当前地址、同步状态和证书有效期。

#### 1.2.3 关键模块
| 模块编号 | 模块名称 | 职责 | 输入 | 输出 |
|---|---|---|---|---|
| M-001 | DDNS配置与状态存储 | 保存配置、受管记录状态、证书元数据 | 系统设置请求、运行结果 | 规范化配置与状态 |
| M-002 | DDNS地址源 | 获取单网卡全部IP与公网出口全部IP | 稳定网卡ID、公网探测 | IPv4/IPv6集合 |
| M-003 | Cloudflare DNS收敛器 | 解析Zone并收敛A/AAAA记录 | 域名、地址、API Token、受管状态 | DNS执行结果 |
| M-004 | Let's Encrypt证书管理器 | 使用DNS-01签发并自动续期SAN证书 | 全部域名、API Token | 证书、私钥、有效期 |
| M-005 | DDNS调度与观测 | 合并周期/保存触发并输出脱敏状态 | 配置变化、定时事件 | 最近同步与证书状态 |
| M-006 | 系统设置DDNS界面 | 配置并展示探针DDNS | 用户输入、DDNS API | 保存结果与运行状态 |

#### 1.2.4 关键接口
| 接口编号 | 接口名称 | 调用方 | 提供方 | 说明 |
|---|---|---|---|---|
| IF-001 | `GET /local/api/system/ddns` | 系统设置页面 | DDNS本地API | 返回脱敏配置、网卡列表、地址与状态 |
| IF-002 | `POST /local/api/system/ddns` | 系统设置页面 | DDNS本地API | 校验并保存配置，异步触发同步与证书检查 |
| IF-003 | `triggerProbeDDNSSync()` | 启动、配置保存、周期调度 | DDNS调度器 | 合并触发单实例收敛 |
| IF-004 | `reconcileProbeDDNS()` | DDNS调度器 | Cloudflare DNS收敛器 | 收敛全部配置域名与IP |
| IF-005 | `ensureProbeDDNSCertificate()` | 启动、配置保存、续期调度 | Let's Encrypt证书管理器 | 确保SAN证书存在且可用 |

#### 1.2.5 关键约束
- Cloudflare 鉴权严格与主控一致，使用 `Authorization: Bearer <API Token>`。
- 配置最多一个网卡，但网卡域名和公网出口域名各自允许多个，合计去重后最多 100 个。
- 完整域名必须规范化为小写、去尾点并通过 DNS 名称校验；不得接受 URL、端口、通配符或 IP 字面量。
- 空 Token 的保存请求表示保留现有 Token；读取响应永不包含 Token 正文。
- 配置和密钥文件使用 `0600`，目录使用 `0700`，并沿用探针本地 JSON 持久化模式。
- 地址源获取失败或返回空集合时记录错误并保留上次远端记录，不执行空集合清理。
- 本功能创建的 Cloudflare 记录写入固定 `comment` 所有权标记；不接管无该标记的人工记录，不按域名前缀批量删除。
- ACME TXT 临时记录无论成功失败均尝试清理；该临时记录清理不受“不删除DDNS记录”约束。

#### 1.2.6 风险
- Cloudflare API 或 DNS 传播延迟可能导致签发超时，需保留状态并在下一周期重试。
- 单张 SAN 证书中任一域名授权失败会导致整张证书签发失败。
- 多地址同名记录的 Record ID 必须持久化，否则无法精确更新或清理减少的地址。

#### 1.2.7 结论
- 架构满足用户已确认的单网卡、全部IP、多域名SAN证书、自动更新续期、不因配置删除而清理远端记录及本地存储要求。

### 1.3 单元设计
- 状态: 已完成

#### 1.3.1 单元清单
| 单元编号 | 单元名称 | 所属模块 | 职责 | 输入 | 输出 |
|---|---|---|---|---|---|
| U-001 | 配置规范化与持久化 | M-001 | 校验并原子保存配置与Token | API请求 | 配置/错误 |
| U-002 | 单网卡地址解析 | M-002 | 按稳定ID获取全部有效地址 | 网卡ID | A/AAAA地址集合 |
| U-003 | 公网出口地址解析 | M-002 | 复用公网IP采集器 | 无 | A/AAAA地址集合 |
| U-004 | Cloudflare客户端 | M-003 | Zone匹配及DNS CRUD | Token、记录 | 记录ID/错误 |
| U-005 | DNS差异收敛 | M-003 | 比较期望值与受管状态并执行变更 | 配置、地址、状态 | 新状态 |
| U-006 | ACME SAN证书管理 | M-004 | DNS-01签发、存储与自动续期 | 域名集合、Token | PEM与元数据 |
| U-007 | 合并调度器 | M-005 | 串行化并合并触发 | 定时/保存/启动事件 | 执行状态 |
| U-008 | DDNS本地API与页面 | M-006 | 配置、脱敏状态和界面交互 | HTTP请求 | JSON/HTML |

#### 1.3.2 单元设计
##### U-001至U-008
- 单元名称: 探针DDNS独立单元组
- 职责: 完成配置、地址采集、DNS收敛、证书续期、调度和系统设置交互。
- 输入: 单网卡ID、两组完整域名、Cloudflare API Token、周期事件。
- 输出: Cloudflare A/AAAA记录、SAN证书与私钥、脱敏运行状态。
- 处理规则: 配置保存成功后立即刷新内存配置并异步触发；DNS与证书任务分别串行；状态文件只在远端操作成功后推进；证书SAN集合按字典序稳定化。
- 异常规则: 配置校验错误返回400；未登录返回401；Cloudflare/ACME失败不回滚合法配置，不覆盖仍可用证书，并记录最近错误供页面展示。

#### 1.3.3 风险
- 测试必须通过可替换HTTP客户端和ACME签发接口隔离外网，避免单元测试访问真实Cloudflare或Let's Encrypt。

#### 1.3.4 结论
- 单元边界和异常策略完整，可形成文件级Code任务包。

### 1.4 Code任务执行包
- 状态: 已完成

#### 1.4.1 执行边界
- 允许修改: `probe_node/probe_ddns.go`、`probe_node/probe_ddns_cloudflare.go`、`probe_node/probe_ddns_certificate.go`、`probe_node/probe_ddns_test.go`、`probe_node/local_console.go`、`probe_node/local_pages/system.html`、`probe_node/main.go`、`probe_node/go.mod`、`probe_node/go.sum`、本文档。
- 禁止修改: `probe_controller/`、`probe_node/mobilecore/`、`probe_node_android/`、虚拟路由与既有主控证书实现。

#### 1.4.2 任务清单
| 任务编号 | 需求编号 | 单元编号 | 文件范围 | 操作类型 | 验收标准 |
|---|---|---|---|---|---|
| T-001 | REQ-PN-DDNS-001 | U-001,U-002,U-003 | `probe_node/probe_ddns.go` | 新增 | 配置/状态存入data/ddns，单网卡和公网地址解析测试通过 |
| T-002 | REQ-PN-DDNS-001 | U-004,U-005 | `probe_node/probe_ddns_cloudflare.go`、`probe_node/probe_ddns_test.go` | 新增 | Bearer Token、跨Zone、多域名全IP收敛与不接管记录测试通过 |
| T-003 | REQ-PN-DDNS-001 | U-006 | `probe_node/probe_ddns_certificate.go`、`probe_node/probe_ddns_test.go`、`probe_node/go.mod`、`probe_node/go.sum` | 新增/修改 | 多域名SAN、DNS-01、30天续期、失败保留旧证书测试通过 |
| T-004 | REQ-PN-DDNS-001 | U-007 | `probe_node/probe_ddns.go`、`probe_node/main.go` | 新增/修改 | 启动、10分钟周期、6小时续期与触发合并测试通过 |
| T-005 | REQ-PN-DDNS-001 | U-008 | `probe_node/local_console.go`、`probe_node/local_pages/system.html`、`probe_node/probe_ddns_test.go` | 修改 | 登录保护、GET/POST、Token不回显和系统设置交互测试通过 |
| T-006 | REQ-PN-DDNS-001 | U-001至U-008 | 本任务包全部允许文件、本文档 | 修改/验证 | gofmt、定向测试、probe_node全量测试和diff检查通过，证据回填本文档 |

#### 1.4.3 源码修改规则
- 修改源代码时必须注意可能存在的 GBK 编码并保持原文件编码，避免乱码或误转码。

#### 1.4.4 交付物
- 探针DDNS独立后端、系统设置界面、自动化测试和更新后的本文档。

#### 1.4.5 门禁输入
- 用户确认的功能与产品规则。
- 1.1验收标准、1.2架构、1.3单元设计和T-001至T-006。

#### 1.4.6 结论
- 任务包文件范围、操作类型和可测试验收标准完整，可进入Code阶段。

### 1.5 Architect需求跟踪矩阵
- 状态: 已完成

| 需求编号 | 需求描述 | 架构章节 | 单元设计章节 | Code任务章节 | 状态 | 备注 |
|---|---|---|---|---|---|---|
| REQ-PN-DDNS-001 | 探针侧两类DDNS与Let's Encrypt证书 | 1.2 | 1.3 | 1.4 | 已完成 | T-001至T-006覆盖 |

### 1.6 Architect关键接口跟踪矩阵
- 状态: 已完成

| 接口编号 | 需求编号 | 接口名称 | 调用方 | 提供方 | 输入 | 输出 | 状态 | 备注 |
|---|---|---|---|---|---|---|---|---|
| IF-001 | REQ-PN-DDNS-001 | GET DDNS设置 | 系统设置页面 | DDNS本地API | 登录会话 | 脱敏配置与状态 | 已完成 | T-005 |
| IF-002 | REQ-PN-DDNS-001 | POST DDNS设置 | 系统设置页面 | DDNS本地API | 配置与可选Token | 保存结果 | 已完成 | T-005 |
| IF-003 | REQ-PN-DDNS-001 | DDNS同步触发 | 启动/保存/周期 | DDNS调度器 | 触发原因 | 合并任务 | 已完成 | T-004 |
| IF-004 | REQ-PN-DDNS-001 | DNS收敛 | DDNS调度器 | Cloudflare收敛器 | 配置与地址 | DNS状态 | 已完成 | T-002 |
| IF-005 | REQ-PN-DDNS-001 | SAN证书确保 | DDNS调度器 | 证书管理器 | 域名与Token | 证书状态 | 已完成 | T-003 |

### 1.7 门禁裁判
- 状态: 已放行

#### 1.7.1 门禁输入
| 文档 | 路径 | 状态 |
|---|---|---|
| 协作文档 | `doc/REQ-PN-DDNS-001-collaboration.md` | 已完成 |

#### 1.7.2 裁判检查
| 检查项 | 结果 | 证据 | 备注 |
|---|---|---|---|
| 协作文档存在 | 通过 | 本文档 | 无 |
| Architect章节存在 | 通过 | 第1章 | 无 |
| Code章节存在 | 通过 | 第2章 | 无 |
| 必需子章节存在 | 通过 | 本文档 | 无 |
| 需求前缀一致 | 通过 | REQ-PN-DDNS-001 | 无 |
| 需求编号一致 | 通过 | REQ-PN-DDNS-001 | 无 |
| 接口编号一致 | 通过 | IF-001至IF-005 | 无 |
| 模板字段完整 | 通过 | 本文档 | 无 |
| GBK编码文件无乱码或误转码 | 通过 | 修改文件保持原编码，Go源码gofmt通过，页面渲染通过 | 无 |
| Code证据完整 | 通过 | 第2.5节 | 修改接口、配置、报告、文件、测试、风险与回滚均完整 |
| Code任务反馈已处理 | 通过 | 第2.6节 | 无未处理反馈 |
| 验收标准可测试 | 通过 | AC-001至AC-012 | 无 |
| 需求任务覆盖完整 | 通过 | T-001至T-006 | 无 |
| 任务自测覆盖完整 | 通过 | TEST-001至TEST-009 | 定向、重复、全量与UI验证完整 |
| 修改文件在允许范围内 | 通过 | Git状态与第1.4.1节逐项核对 | 无越界文件 |
| 测试失败已记录缺陷 | 通过 | DEF-001至DEF-005 | 本需求缺陷均修复，既有vet告警单独记录 |
| 未执行测试原因完整 | 通过 | 第2.5.7节 | 真实外部写入、race与第二实例原因完整 |
| 遗留风险可接受 | 通过 | 第2.5.8节 | 仅真实凭据权限/网络与既有vet告警 |

#### 1.7.3 冲突记录
| 冲突编号 | 冲突条款 | 最终采用条款 | 裁决人 | 裁决结论 |
|---|---|---|---|---|
| 无 | 无 | 无 | Architect | 无 |

#### 1.7.4 裁判结论
- 结论: 通过
- 放行阻塞: 放行
- 条件: 无。
- 责任方: 无。
- 关闭要求: 已关闭。
- 整改要求: 无。

#### 1.7.5 结论
- REQ-PN-DDNS-001需求、实现与验证证据完整，最终门禁通过并放行。

## 第2章 Code章节
- 章节责任角色: Code
- 状态: 已完成

### 2.1 Code需求跟踪矩阵
- 状态: 已完成

| 需求编号 | 任务编号 | 实现文件 | 实现状态 | 自测状态 | 证据 | 备注 |
|---|---|---|---|---|---|---|
| REQ-PN-DDNS-001 | T-001 | `probe_node/probe_ddns.go` | 已完成 | 已完成 | 配置、状态、地址源与API定向测试通过 | 无 |
| REQ-PN-DDNS-001 | T-002 | `probe_node/probe_ddns_cloudflare.go` | 已完成 | 已完成 | Zone匹配与多地址收敛定向测试通过 | 无 |
| REQ-PN-DDNS-001 | T-003 | `probe_node/probe_ddns_certificate.go` | 已完成 | 已完成 | SAN落盘与提前续期抑制定向测试通过 | 无 |
| REQ-PN-DDNS-001 | T-004 | `probe_node/probe_ddns.go`、`probe_node/main.go` | 已完成 | 已完成 | 合并调度测试与全量测试通过 | 无 |
| REQ-PN-DDNS-001 | T-005 | `probe_node/local_console.go`、`probe_node/local_pages/system.html` | 已完成 | 已完成 | API测试及Playwright桌面/手机测试通过 | 无 |
| REQ-PN-DDNS-001 | T-006 | 本需求全部允许文件 | 已完成 | 已完成 | 定向重复测试、全量测试、diff检查通过 | 无 |

### 2.2 Code关键接口跟踪矩阵
- 状态: 已完成

| 接口编号 | 需求编号 | 实现文件 | 调用方 | 提供方 | 实现状态 | 证据 | 备注 |
|---|---|---|---|---|---|---|---|
| IF-001 | REQ-PN-DDNS-001 | `probe_node/probe_ddns.go` | 系统设置页面 | DDNS本地API | 已完成 | GET API及脱敏响应测试 | 无 |
| IF-002 | REQ-PN-DDNS-001 | `probe_node/probe_ddns.go` | 系统设置页面 | DDNS本地API | 已完成 | POST API及持久化测试 | 无 |
| IF-003 | REQ-PN-DDNS-001 | `probe_node/probe_ddns.go` | 启动/保存/周期 | DDNS调度器 | 已完成 | `TestTriggerProbeDDNSSyncCoalescesPendingEvents`及全量测试 | 无 |
| IF-004 | REQ-PN-DDNS-001 | `probe_node/probe_ddns_cloudflare.go` | DDNS调度器 | Cloudflare收敛器 | 已完成 | 模拟Cloudflare定向测试 | 无 |
| IF-005 | REQ-PN-DDNS-001 | `probe_node/probe_ddns_certificate.go` | DDNS调度器 | 证书管理器 | 已完成 | 模拟签发定向测试 | 无 |

### 2.3 Code测试项跟踪矩阵
- 状态: 已完成

| 测试项编号 | 需求编号 | 任务编号 | 测试目标 | 测试方法 | 结果 | 证据 | 未执行原因 | 备注 |
|---|---|---|---|---|---|---|---|---|
| TEST-001 | REQ-PN-DDNS-001 | T-001 | 配置规范化、来源冲突与域名校验 | Go定向测试 | 已完成 | `TestNormalizeProbeDDNSConfig` | 无 | 无 |
| TEST-002 | REQ-PN-DDNS-001 | T-001,T-005 | Token存储且API不回显 | Go本地API测试 | 已完成 | `TestProbeLocalSystemDDNSAPITokenIsNotReturned` | 无 | 无 |
| TEST-003 | REQ-PN-DDNS-001 | T-002 | 最长Zone、多IP收敛和人工记录隔离 | 模拟Cloudflare HTTP测试 | 已完成 | `TestMatchProbeDDNSCloudflareZoneUsesLongestSuffix`、`TestReconcileProbeDDNSSourceCreatesAllIPsAndDeletesStaleAddress`、`TestEnsureProbeDDNSCloudflareRecordDoesNotAdoptManualRecord` | 无 | 无 |
| TEST-004 | REQ-PN-DDNS-001 | T-002 | 配置移除不远端删除 | 纯函数状态测试 | 已完成 | `TestDropProbeDDNSUnconfiguredSourceRecordsDoesNotCallRemote` | 无 | 无 |
| TEST-005 | REQ-PN-DDNS-001 | T-003 | 多域名证书存储、续期窗口和失败保留 | 模拟签发测试 | 已完成 | `TestEnsureProbeDDNSCertificatePersistsSANAndSkipsEarlyRenewal`、`TestEnsureProbeDDNSCertificateRenewsInsideWindowAndPreservesOnFailure` | 无 | 无 |
| TEST-006 | REQ-PN-DDNS-001 | T-004 | 并发触发合并与当前公网IP采集 | Go调度/注入测试 | 已完成 | `TestTriggerProbeDDNSSyncCoalescesPendingEvents`、`TestCollectProbeDDNSPublicAddressesUsesCurrentSniff` | 无 | 无 |
| TEST-007 | REQ-PN-DDNS-001 | T-005 | 系统设置页面渲染与交互 | Playwright桌面1440x1000、手机390x844 | 已完成 | 页面身份、非空、无错误、保存交互、Token清空、无横向溢出均通过 | 无 | Browser插件不可用，使用本机Chrome |
| TEST-008 | REQ-PN-DDNS-001 | T-006 | probe_node全量回归 | `go test ./...` | 已完成 | `probe_node`与`mobilecore`通过 | 无 | 无 |
| TEST-009 | REQ-PN-DDNS-001 | T-006 | 新增测试稳定性 | `go test -count=3 -run "Test.*ProbeDDNS" .` | 已完成 | 连续三次通过 | 无 | 无 |

### 2.4 Code缺陷跟踪矩阵
- 状态: 已完成

| 缺陷编号 | 需求编号 | 测试项编号 | 缺陷描述 | 严重级别 | 修复状态 | 修复证据 | 备注 |
|---|---|---|---|---|---|---|---|
| DEF-001 | REQ-PN-DDNS-001 | TEST-003 | Zone匹配曾依赖调用方排序 | 中 | 已完成 | 匹配函数内部始终选择最长后缀，测试通过 | 无 |
| DEF-002 | REQ-PN-DDNS-001 | TEST-005 | 证书时间测试未考虑DER秒级精度 | 低 | 已完成 | 改为Unix秒比较，测试通过 | 测试缺陷 |
| DEF-003 | REQ-PN-DDNS-001 | TEST-003 | 同名同值人工记录可能被误登记 | 高 | 已完成 | Cloudflare comment所有权标记及隔离测试通过 | 无 |
| DEF-004 | REQ-PN-DDNS-001 | TEST-008 | Windows临时替换回退存在旧文件丢失风险 | 中 | 已完成 | 改为仓库既有受限权限JSON写入模式 | 无 |
| DEF-005 | REQ-PN-DDNS-001 | TEST-008 | `go vet ./...`报告既有mobilecore锁复制与Windows unsafe.Pointer告警 | 低 | 待整改 | 告警文件均不在本任务修改范围 | 不阻塞本需求测试门禁 |

### 2.5 Code执行证据
- 状态: 已完成

#### 2.5.1 修改接口
- 新增 `GET/POST /local/api/system/ddns`。
- 新增内部 `triggerProbeDDNSSync()`、`reconcileProbeDDNS()`、`ensureProbeDDNSCertificate()`。

#### 2.5.2 配置文件
- `data/ddns/config.json`: 独立配置及Bearer API Token。
- `data/ddns/state.json`: 受管记录ID与最近运行状态。
- `data/ddns/acme_account.key.pem`、`tls.crt.pem`、`tls.key.pem`、`tls.meta.json`: ACME账户与SAN证书产物。

#### 2.5.3 执行报告
- 两类DDNS、全部IPv4/IPv6、多域名SAN证书、10分钟同步、30天续期窗口和6小时证书检查均已实现。
- 删除配置域名或停用功能不删除远端记录；当前配置域名的地址集合减少时，仅删除带本功能所有权标记且已登记的旧地址记录。
- 系统设置页面完成桌面与手机渲染、保存交互和Token脱敏验证。

#### 2.5.4 影响文件
- `probe_node/probe_ddns.go`
- `probe_node/probe_ddns_cloudflare.go`
- `probe_node/probe_ddns_certificate.go`
- `probe_node/probe_ddns_test.go`
- `probe_node/local_console.go`
- `probe_node/local_pages/system.html`
- `probe_node/main.go`
- `doc/REQ-PN-DDNS-001-collaboration.md`

#### 2.5.5 测试命令
- `go test -run "Test.*ProbeDDNS" .`
- `go test -count=3 -run "Test.*ProbeDDNS" .`
- `go test ./...`
- `npx playwright test --config=<临时目录>/playwright.config.js --reporter=line`
- `git diff --check`
- `go test -race -run "Test.*ProbeDDNS" .`（未执行成功，见2.5.7）。
- `go vet ./...`（执行后发现既有告警，见DEF-005）。

#### 2.5.6 自测结果
- 新增DDNS定向测试通过并连续三次稳定通过。
- `probe_node go test ./...`通过，`mobilecore`通过。
- Playwright系统Chrome验证通过，桌面和390px手机端无横向溢出、控制台错误或交互失败。
- `git diff --check`通过，仅有Git换行符提示。

#### 2.5.7 未执行测试原因
- 未调用真实Cloudflare或Let's Encrypt生产API，避免修改用户DNS和触发真实签发；通过模拟HTTP与可替换签发器验证协议和状态机。
- `go test -race`因当前Go环境禁用CGO而拒绝执行：`-race requires cgo`。
- 未直接启动第二个探针实例；现有Windows全局启动门禁被运行中的探针占用，未停止现有服务，页面改用Playwright拦截API验证。

#### 2.5.8 遗留风险
- 首次真实使用仍依赖用户API Token对所有域名所属Zone具备DNS Write权限，以及公网可访问Cloudflare与Let's Encrypt。
- `go vet`既有告警与本需求无文件交集，但仓库后续应单独整改。

#### 2.5.9 回滚方案
- 回滚本节2.5.4列出的源代码修改；保留或备份`data/ddns`后停止调度即可。由于配置移除本来就不删除远端记录，回滚不会额外删除Cloudflare记录。

#### 2.5.10 结论
- T-001至T-006已完成并通过可执行验收；提交Architect最终门禁。

### 2.6 Code任务反馈
- 状态: 已完成

| 反馈编号 | 任务编号 | 反馈类型 | 反馈描述 | 阻塞影响 | Code建议 | Architect处理状态 | Architect处理结论 |
|---|---|---|---|---|---|---|---|
| 无 | T-001至T-006 | 无 | 无 | 无 | 无 | 已完成 | 无 |

#### 2.6.1 结论
- 无未处理Code任务反馈。
