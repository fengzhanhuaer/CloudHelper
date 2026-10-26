# 任务：第一阶段 IPv6 承载层双栈直连

- 任务标识：`2026-10-26-ipv6-carrier`
- 状态：`已完成`
- 创建/更新时间：2026-10-26 Asia/Singapore
- 用户原始及最新指令：先做主控连接、节点链路等承载层直连，不需要 Fake IP；虚拟路由留第二阶段。
- 启用方式：明确长任务条件；跨主控、Probe、安卓、C 插件调查、实现、验证和发布。

## 一、需求定义

范围：主控 HTTP/控制流/升级下载、节点 TLS/WebSocket 承载的域名 A/AAAA 与 IPv6 literal、双栈监听、失败回退。非范围：虚拟路由内层 IPv6 报文、Fake IPv6、AAAA 业务策略、IPv6 LAN 网关、SNI 嗅探、证书身份策略变更。

| 编号 | 需求与验收 | 优先级 | 状态 |
|---|---|---|---|
| REQ-001 | 主控域名和发现的 IPv6:15030 可连，保持私有认证、SPKI pin 和 Web 隔离；HTTP/控制流/下载均覆盖 | 高 | 已实现并验证 |
| REQ-002 | Probe/安卓/插件节点链路客户端及服务端支持 IPv6，保留多槽位和 IPv4 兼容 | 高 | 已实现并验证 |
| REQ-003 | IPv6 不通可回退 IPv4；不把 IPv6 交给 IPv4 bypass；地址/候选更新不关闭既有连接 | 高 | 已实现 |
| REQ-004 | 源码、针对性实连验证、正式 Release 校验；不发布 BE86U，不用 SSH/SCP 替换生产二进制 | 高 | 已完成 |

## 二、总体架构

调查基线：主控 controller_direct.go 已监听并发布 IPv4/IPv6；Go controllerlink 有 endpoint health。Probe 主控域名/relay 默认查询 A；安卓主控使用系统双栈。插件 tls.c socket(AF_INET)、config.c 不接受 IPv6 origin、controller_link.c 丢弃 IPv6、engine.c 仅 IPv4 监听。以上承载限制已由本次修改补齐。

目标：承载专用 A/AAAA 解析与候选选择独立于业务 Fake IP；有界尝试与正确括号格式；现有 pin/HMAC/ticket 不变；IPv6 监听与规则仅作用于节点服务端端口，尊重 allow_wan。旧内层报文协议不变。

| 接口 | 契约 | 位置 | 关联 |
|---|---|---|---|
| IF-001 | 主控发现 JSON version=1 不变，ip 可为 v4/v6，port=15030，pin 必填 | controller_direct / controllerlink / controller_link.c | REQ-001 |
| IF-002 | 承载解析返回有界、去重的真实 v4/v6 候选；literal 不经 DNS | Probe 解析、插件 transport | REQ-001/002/003 |
| IF-003 | 节点 TLS/WS 与 ticket/slot 不变；IPv6 HTTP Host 使用 [ip]:port | relay / carrier / control | REQ-002 |

## 三、单元设计

| 单元 | 文件范围 | 规则与异常处理 | 验证 |
|---|---|---|---|
| UNIT-001 | probe_node resolved_dial、route_relay_client_transport、承载 DNS | 专用 A/AAAA，IPv4 fallback，保留缓存成功逻辑；IPv6 不安装 IPv4 host route | Go 双栈/黑洞/回退测试 |
| UNIT-002 | router_plugin config/dns/tls/controller/controller_link/upgrade | 双栈 origin 和真实地址解析，括号正确，认证不变，有界连接与回退 | C/本地 TLS/HTTP fixtures |
| UNIT-003 | router_plugin engine/platform/transport | IPv6 节点监听、最小 INPUT 规则，缺少 IPv6 不影响 IPv4，热更新不重建已连槽位 | Linux loopback/规则测试 |
| UNIT-004 | 主控与安卓既有承载 | 审计现有双栈；补齐实际缺口，避免无关改动 | 既有 controllerlink/mobile 测试 |

## 四、执行任务

### 当前交接
- 当前阶段：已完成
- 当前计划步骤：TASK-004 已完成
- 当前门禁：准备与完成门禁通过；保留既有静态告警和纯IPv6 DNS/外网验证边界。
- 基线：mapledev，v0.4.91，工作区原本干净。
- 下一步唯一动作：交付 v0.4.92；生产在线升级/IPv6外网验收尚未执行，需另行部署范围。虚拟路由内层IPv6在第二阶段。
- 恢复先读：本账本、git status/diff、transport.h、resolved_dial.go、route_relay_client_transport.go。

| 任务 | 内容 | 状态 | 需求 | 完成条件 |
|---|---|---|---|---|
| TASK-001 | 调查、确认边界、准备门禁 | 已完成 | 全部 | 入口和设计达到可实现 |
| TASK-002 | Go 承载 DNS/选址/回退 | 已完成 | 001/002/003 | 专用 A/AAAA、候选上限4、DNS工作上限16；TCP/TLS/QUIC竞速，真实 IPv6 WS/H3 与多槽位认证通过 |
| TASK-003 | C 插件双栈客户端与服务端 | 已完成 | 001/002/003 | TLS、HTTP、V6ONLY监听、权限测试通过；IPv6 避开 IPv4 WAN 绑定；缺少 IPv6 不重置 IPv4 |
| TASK-004 | 回归、提交、发布与产物校验 | 已完成 | 004 | v0.4.92；Actions 9/9，10个asset与插件全部文件校验通过 |

变更记录：Go 新增 transport_dns.go 并接入主控/relay；默认节点监听改双栈通配。插件新增 transport_dns.c，TLS 有界非阻塞握手竞速；主控 origin/discovery/HTTP/下载支持 v6；carrier6 独立监听和最小 CHVR6_IN 规则。认证、业务映射与内层报文未变。

## 五、测试与验证

| 测试 | 目标/需求 | 方法 | 状态/证据 |
|---|---|---|---|
| TEST-001 | Go 主控与节点双栈、回退；001/002/003 | Windows/Linux ::1 实际 TLS/WS/H3、4槽位 HMAC+签名ticket、错误密钥401、QUIC黑洞地址回退 | 通过；TestProbeTransport*、Linux容器针对性套件 |
| TEST-002 | C 客户端/服务端；001/002/003 | WSL严格编译、本地隔离容器TLS/HTTP/控制流/断点下载；IPv6身份与pin拒绝、IPv4阻塞TLS回退、最小INPUT规则 | 通过；真实A/AAAA、IPv6主控认证/监听、IPv4与IPv6 WS/yamux配置同步、IPv4与IPv6双分片/Range/校验、ASAN |
| TEST-003 | Go/安卓/主控兼容；001/002/003 | 完整 go test ./...、controller vet、Linux router构建、安卓核心IPv6四槽位回显 | 通过；Probe 38s，主控7s；完整Probe vet有既有告警，见下 |
| TEST-004 | 正式发布；004 | preflight、git diff --check、Actions、tag/asset/hash | 通过；运行40261790158，tag父提交ccceedeeb，精确10项产物 |

验证边界：生产外网 IPv6 需要具备可达网络，不能用本机监听代替外网证明。未恢复用户已删除的五组耗时互通测试。

已知静态告警：完整 Probe vet 报 mobile_vrouter_test.go:71/114 复制锁以及 local_tun_egress_windows.go:185/187、local_windows_netapi.go:695/697/728/730/825 的 unsafe.Pointer；这些文件未修改。保留原始结果，排除这两类检查的 vet 已通过，不能称完整 vet 无告警。最终完整 Probe 测试37.5s通过；主控完整测试与vet通过，Linux router amd64构建通过。

## 六、端到端追踪

| 需求 | 单元/接口 | 任务 | 测试 | 结果 |
|---|---|---|---|---|
| REQ-001 | UNIT-001/002/004，IF-001/002 | TASK-002/003 | TEST-001/002/003 | 通过 |
| REQ-002 | UNIT-001/002/003/004，IF-002/003 | TASK-002/003 | TEST-001/002/003 | 通过 |
| REQ-003 | UNIT-001/002/003，IF-002 | TASK-002/003 | TEST-001/002 | 通过 |
| REQ-004 | 发布工作流 | TASK-004 | TEST-004 | 通过 |

## 七、决策与冲突

| 决策 | 采用方案 | 原因/范围 |
|---|---|---|
| DEC-001 | 仅承载层双栈，不改 Fake IP 或内层 IPv4 | 用户明确阶段边界 |
| DEC-002 | 保留 pin、HMAC、ticket、普通 relay SNI 策略 | 地址族不改变认证语义 |
| DEC-003 | 保留旧连接，只有新连接选址和缺失监听修复 | 用户既有稳定性要求 |

CON-001：release skill 的 preflight 仍要求已退休的 build-probe-node-docker、Linux router arm64 二进制/manifest 三项，原始脚本因此失败。当前 workflow 不生成这些产物；沿用仅移除这三条旧断言的临时 preflight，其余 Git/认证/分支/正式产物校验通过。实际清单为9个job、10个asset，只有AX56U V2插件，无BE86U。

## 八、缺陷记录

- DEF-001 已修复：插件固定 AF_INET、丢弃主控IPv6发现、IPv6缺少端口括号。
- DEF-002 已修复：IPv6连接错误使用IPv4 bypass/WAN接口；A列表占满发现候选；单地址TLS/QUIC黑洞阻塞可用地址。
- DEF-003 已修复：异常退出遗留 IPv6 WAN 相反权限规则时，先建目标规则再删相反规则，不重置连接。
- TEST-DEF-001 已修复：新增C fixture未在并发请求前 curl_global_init，导致一次崩溃；生产main已初始化，fixture补齐并在ASAN/普通隔离容器复验。数字IPv6不再生成无效RESOLVE移除项。控制流的IPv4公网IP mock允许被转发至IPv6 fixture，同时保留首IPv4失效/第二IPv4成功回归。

## 九、回滚方案

若 IPv4 连接/认证回归：停止推广，经正式发布回退承载改动并用产品在线更新；不改变缓存 schema/version、证书或业务 Fake IP 数据。IPv6 listener/INPUT 规则关闭时仅移除自有规则，保留固件规则和现有 IPv4。

## 十、已验证事实

- FACT-001：controller_direct.go net.Listen("tcp", JoinHostPort(ip,"15030")) 可处理 IPv6；发现上限 4。
- FACT-002：插件 tls.c 固定 AF_INET；engine.c 仅 lan/wan 两个 IPv4 listener；Probe relay 默认真实 A 查询。
- FACT-003：原工作区干净；无仓库额外 AGENTS.md；用户级入口已读。
- FACT-004：Ubuntu18.04容器镜像下载两次发生 tls: bad record MAC；未修改发布镜像，以已缓存的本地Go/Debian隔离容器完成真实功能回归。正式CI仍校验Ubuntu18.04和4款ABI。
- FACT-005：WSL单独运行新增HTTP6 fixture出现本机TLS超时；最终验收使用隔离容器真实回环，不将WSL环境超时称为生产验证或外网故障。临时诊断verbose已移除。

## 十一、风险与阻塞

- 老固件 libcurl 行为不同，不能依赖仅新版本支持的多地址 RESOLVE 语法；需要 ABI 构建与实际 fixtures。
- IPv6 默认防火墙必须保留 LAN/WAN 权限边界；不改系统 IPv6 默认策略。
- 资源有限，候选、线程/FD、DNS 等待均需有界。
- 本次承载 A/AAAA 独立查询真实上游；Go/C自举DNS的纯IPv6上游尚未完成，因此不宣称纯IPv6且没有可用IPv4 DNS的冷启动。IPv6 literal和已发现/已缓存直连地址不依赖该自举。

## 十二、质量门禁

准备门禁：通过；需求/范围/追踪/回滚与具体入口已确认。完成门禁：通过；本机与隔离容器真实IPv6认证/控制流/监听/多槽位/下载以及正式4款ABI验证完成。完整Probe vet既有告警、纯IPv6 DNS自举、生产升级和外网验收边界均已明确。

## 十三、检查点

2026-10-26：已发布 v0.4.92，以下清单提交与正式产物均验证完毕；生产设备未在线升级。

已提交：`feat: enable dual-stack IPv6 controller and node carriers`。精确文件清单（仅本任务）：

```text
doc/tasks/2026-10-26-ipv6-carrier.md
probe_controller/internal/core/controller_direct.go
probe_controller/internal/core/controller_direct_test.go
probe_node/mobilecore/transport_ipv6_test.go
probe_node/probe_virtual_router_dns_cache_test.go
probe_node/probe_virtual_router_runtime.go
probe_node/resolved_dial.go
probe_node/route_relay_client_transport.go
probe_node/transport_dns.go
probe_node/transport_dns_test.go
router_plugin/Makefile
router_plugin/include/chvr.h
router_plugin/include/engine.h
router_plugin/include/platform.h
router_plugin/include/transport.h
router_plugin/src/carrier.c
router_plugin/src/config.c
router_plugin/src/control.c
router_plugin/src/controller.c
router_plugin/src/controller_link.c
router_plugin/src/dns.c
router_plugin/src/engine.c
router_plugin/src/platform.c
router_plugin/src/platform_address.h
router_plugin/src/tls.c
router_plugin/src/transport_dns.c
router_plugin/src/upgrade.c
router_plugin/tests/test_control.c
router_plugin/tests/test_control.py
router_plugin/tests/test_controller_link.c
router_plugin/tests/test_platform_command.c
router_plugin/tests/test_tls_connect.c
router_plugin/tests/test_tls_connect.py
router_plugin/tests/test_transport.c
router_plugin/tests/test_transport.py
router_plugin/tests/test_upgrade.py
router_plugin/tools/build-ci.sh
```

## 十四、完成摘要

第一阶段已完成。功能提交 `ccceedeeb45b679c090c33b61236c1d611d17c6b`；自动版本提交/tag `2b5e2b61163672fce53d0d6df54ee3b7c73dfad0` / `v0.4.92`，父提交已核对。

- Actions：https://github.com/fengzhanhuaer/CloudHelper/actions/runs/40261790158 ，9/9成功；安卓签名APK、各平台Probe、主控、Linux router amd64和AX56U V2插件共10个发布asset，严格清单通过；没有Docker/路由arm64/BE86U发布产物。
- Release：https://github.com/fengzhanhuaer/CloudHelper/releases/tag/v0.4.92 ，正式非草稿非预发布。
- 插件包SHA256：`1c3f579110005252f00fd79dcf3127c2a442bb1fd9d75825a8f52456b451835c`；外部manifest SHA256：`55327937c178e4f5f8f45d8b371c33352cb66b2ea8f8a6e0c6d243ef6acc75e5`；包内二进制SHA256：`35771b7608cc61db1f4c3fd79c21764fd927bb895644afd01ed07bbcc5c47b5f`。包内checksums逐项通过，二进制版本与IPv6 listener标识存在。
- CI四款ABI通过；发布manifest只列AX56U V2原厂/官改两种，BE86U仅作兼容夹具。
- 本次未执行生产升级；不宣称生产外网IPv6已经可达。虚拟路由内层IPv6/Fake IPv6按用户要求留第二阶段。
