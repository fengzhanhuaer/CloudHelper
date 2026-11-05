# 任务：修复华硕插件 SNI 连接关闭后 CPU 空转

- 任务标识：`2026-10-20-router-sni-close-cpu`
- 状态：`进行中（2026-11-05 追加容量与 IP 缓存）`
- 用户最新指令：256 个会话，状态和缓冲随连接按需创建、逐步增长、关闭释放、闲置 600 秒、4096 条 IP 选路缓存 72 小时；只存 IP 和策略序号，特殊值用负数。IPv6 只做 WAN 双向承载，入站受公网开关控制；LAN 不允许 IPv6 入站或出站，不增加 IPv6 SNI/业务转发。
- 启用方式：明确长任务条件；涉及故障复现、TCP 生命周期实现、回归、发布及现场在线升级。
- 基线：`468aa383`，`mapledev`，工作区干净；BE86U / AX56U 插件均 v0.4.80。

## 一、需求定义

REQ-001：关闭或发送失败的 SNI/TCP 会话及时清理，不因残余缓存反复触发 POLLHUP/POLLOUT。
REQ-002：保留健康连接、背压排空、直连 TCP 半关闭响应；保持现有 main=8 认证、消息和关闭语义兼容。
REQ-003：完成原生/两款厂商 ABI、发布基线验证，提交发布；只通过在线升级接口更新 BE86U，并比较升级后 CPU 和业务连接。

验收：挂断回归无空转；正常/半关闭响应逐字节完整；既有直连/远程/拒绝测试通过；正式发布与功能 SHA 对应，BE86U 版本正确且单核不再持续 100%。不修改无线设置、固件或生产认证配置，不手工替换二进制。

## 二、总体架构

引擎主线程调用 tcp_proxy 的 pollfds/poll。原逻辑写失败仅标记 closing，而 drop 要求缓存为空；对端已关闭的缓存永远发不完，poll 不睡眠。无事件的 fd 也会不断返回 HUP。

目标：永久写失败立即回收；不订阅无需求的 fd；直连分别记录两个读取方向的 EOF，缓冲排空后才传播 SHUT_WR，正常响应继续接收；远程沿用完整 close 帧，不扩展协议。

接口记录：不适用。仅修改内部 TCP 会话生命周期，无持久化、API 或帧格式变化。

## 三、单元设计

| 单元 | 文件 | 处理规则 | 验证 |
|---|---|---|---|
| UNIT-001 | router_plugin/src/tcp_proxy.c | 区分 EOF/永久错误；发送失败 drop；无事件 fd=-1；直连独立半关闭和排空 | TEST-001/002 |
| UNIT-002 | router_plugin/tests/test_tls_proxy.c、Makefile | 真 socket 复现关闭、背压、半关闭，不依赖 CPU 计时阈值 | TEST-001/002 |

## 四、执行任务

| 任务 | 内容 | 状态 |
|---|---|---|
| TASK-001 | 生命周期修复和确定性回归 | 已完成 |
| TASK-002 | C/ABI、Go 发布基线 | 已完成 |
| TASK-003 | 提交发布、正式附件核验 | 已完成 |
| TASK-004 | BE86U 在线升级及 CPU/业务现场复测 | 已完成 |

当前交接：已完成实现、测试、发布、BE86U 产品在线升级及现场验收；完成门禁通过；下一步唯一动作：无。

## 五、测试与验证

| 测试 | 需求 | 方法与预期 | 结果 |
|---|---|---|---|
| TEST-001 | REQ-001 | 客户端断开且响应未发完、上游关闭且下游背压；回收或只等待健康端，不反复 HUP | native、四套 ARM、ASan/UBSan 通过 |
| TEST-002 | REQ-002 | 正常 direct/proxy/reject、直连 SHUT_WR 后完整响应、背压排空、旧远程关闭顺序 | 真 socket、旧透明 TCP 回归、15 项 Go/C 互操作通过 |
| TEST-003 | REQ-001/002/003 | build-ci.sh native/四套 rootfs/两款包，controller/node 全套与 vet | 全部 exit 0 |
| TEST-004 | REQ-003 | Actions 精确 SHA、资产/版本/hash，BE86U 在线升级后 CPU 与链接 | 9个job成功、12资产、四文件digest/成员hash通过；运行文件与正式包一致；两组CPU/连接/SNI活动核验通过 |

## 六、端到端追踪

| 需求 | 单元/任务 | 测试 | 状态 |
|---|---|---|---|
| REQ-001 | UNIT-001/002、TASK-001/002/004 | TEST-001/003/004 | 已完成 |
| REQ-002 | UNIT-001/002、TASK-001/002/004 | TEST-002/003/004 | 已完成 |
| REQ-003 | TASK-002/003/004 | TEST-003/004 | 已完成 |

## 七、决策与冲突记录

DEC-001：永久发送错误不能等待不可达缓存排空，立即回收。正常 EOF 仍须保留健康方向和已有数据。远程关闭沿用现有协议，无新帧。
CON-001：发布 skill 旧预检仍要求已退休 Docker/ARM64 路由产物；实际 release.yml 与前次已验证合同一致。验证实际工作流，临时预检副本只删除这三个过期标记，不恢复旧产物。其余预检和正式附件门禁全部保留。

## 八、缺陷记录

DEF-001：高。BE86U PID 14053 / TID 14083 持续单核 100%。连续六次 poll 元数据快照：fd 53/17/23 请求 POLLOUT，返回 POLLOUT|POLLHUP。对应会话 closing=1，残留 34/6/231 字节。原代码本地 10 万次 poll 全立即返回，约 228ms 消耗同量 CPU。已修复。永久写失败及时回收、正常 EOF 分方向排空；确定性回归与现场新版本验证通过。

## 九、回滚方案

若新版本导致健康传输中断或高 CPU 未消除，撤销本次功能提交并重新验证、发布更高补丁版本，再走产品在线升级接口恢复旧生命周期实现；现有升级接口只接受 latest 且要求版本更新，不能声称可直接在线降至 v0.4.80。配置与帧格式不迁移。回滚后核对版本、主控和节点连接，保留问题诊断。禁止 SSH/SCP 替换生产二进制。

## 十、已验证事实

FACT-001：同窗口 AX56U 插件单核 3.4%～3.8%，BE86U 单核约 100%；两台 DNS/SNI/网关均开启。BE86U 低流量且链路无持续发送积压，主线程异常集中在上述会话。
FACT-002：诊断只读取 poll/session 元数据，不打印、保存原始内存或凭据；复现文件在用户 Temp，未改生产。发布前基线工作区干净。

## 十一、风险与阻塞

风险：不能将正常 TCP FIN 当成永久写错误，否则半关闭后的响应可能丢失；TEST-002 必须覆盖。无当前阻塞。现场观察为有界窗口，不能声称已覆盖所有流量和未来网络条件；故障关闭场景有确定性 socket 回归。

## 十二、质量门禁

准备门禁：通过。范围、证据、设计、测试、基线及回滚已明确。
完成门禁：通过。REQ-001～003 已由实现、原生/ARM/内存/真实协议测试、正式资产和现场在线升级验收闭合；无失败测试、无范围漂移、无凭据/调试残留；回滚方式已按真实 latest-only 升级接口记录。

## 十三、检查点

2026-10-20：用户授权修复；创建独立账本，承接已验证 CPU 根因。旧热更新/八标签账本已完成，不重新开启。当前仅 TASK-001 进行中。

## 十四、完成摘要

### 2026-11-05 追加需求、设计与当前交接

历史 REQ-001～003 和已发布验收继续保留；本次不升级生产设备，不恢复 BE86U/Docker 发布或已删除的长互通组。基线 6dc38c087，mapledev；既有未提交 `2026-10-28-upgrade-source-resume.md` 不纳入本任务。

需求定义：REQ-004 会话上限 256、已建立会话闲置 600 秒，握手预算仍 12 秒；REQ-005 按真实目标 IPv4 缓存成功识别后的选路结果，最多 4096 条、固定 72 小时、不因命中续期、仅 RAM；每条 16 字节，只存二进制 IP、规则序号、过期时间和内核标志，负值表示直连/拒绝。缓存命中新连接立即选路并连接原目标 IP，TLS 原样转发。REQ-006 有效直连缓存通过内核 NAT 放行，绕过 1054 TCP 代理；过期/淘汰/规则或本地出口变更时撤销，保留已有连接。REQ-007 双向缓冲按实际数据分配，单方向上限仍 64 KiB，空槽只留指针，状态也按连接创建并在关闭时释放。REQ-008 IPv6 仅物理 WAN 承载收发：主动连接和监听 socket 均绑定 WAN，IPv6 入站 LAN 拒绝、WAN 由 allow_wan 控制；不改变原厂全局 IPv6/转发/NAT 设置，不扩展 IPv6 SNI/Fake IP。

总体架构：tcp_proxy 持有有界会话指针表、动态会话状态和缓冲和 64 KiB IP→选路缓存；平台增加插件自有 CHVR_TLS_IP NAT 子链，用 ACCEPT 终止 SNI DNAT（不改变 filter 防火墙策略）；平台缓存代数在清空/修复/开关时使 RAM 缓存失效。大量直连条目到期仅清空子链一次，保留未过期选路记录，下次命中恢复放行，不逐条启动数千次命令。仅真实公网 IPv4 TCP/443 参与缓存；Fake IP、私网、主控自身 OUTPUT 不进入 SNI 捕获。IPv6 承载仅 WAN 绑定 socket；CHVR6_IN 拒绝 LAN、按公网开关放行指定 WAN、其余接口拒绝；保留认证/票据/证书验证。

单元设计与接口：UNIT-003 tcp_proxy.c/engine.h 定义统一 256 会话和 513 poll 槽，数据缓冲按 4 KiB 起步增长；UNIT-004 platform.c/platform_address.h/platform.h 提供 IF-001 缓存增删、清空及代数（失败不发布新的内核放行）；UNIT-005 engine.c/policy_update.c 在规则或本地覆盖发布前清空缓存，失败保留旧策略；UNIT-006 现有 TCP/平台回归和 README。所有接口为进程内，不改配置/网络帧、不写 JFFS。

决策 DEC-002：按用户要求采用目标 IP 缓存；共享同一 IP 的多个域名在缓存有效期内共用已缓存选路，不能声称仍按每次真实 SNI 区分。缓存不永久保留，策略修改先清空。域名缓存方案已被用户要求的序号方案替代。DEC-003：不预留 128×128 KiB，缓冲只在数据到达时增长并在会话关闭时释放。用户最新 128 替代最初 256，不改变单方向 64 KiB 上限。DEC-004：IP 本身命中非直连 CIDR 时保留透明直连 socket，避免 NAT 放行后被 CIDR 路由再次捕获。IF-002 policy_rule_decision 提供统一的规则序号解析，正常匹配与缓存命中共用，失效规则拒绝。

| 任务 | 需求/单元 | 状态 | 完成条件 |
|---|---|---|---|
| TASK-005 | REQ-004～008 / UNIT-003～007 | 已完成 | 容量、时限、缓存、内核旁路和失效、IPv6 WAN 约束已实现 |
| TASK-006 | REQ-004～008 | 已完成 | TEST-005 容量/超时/懒分配；TEST-006 命中/TTL/淘汰/策略失效；TEST-007 真内核直连旁路与既有 TCP 生命周期；TEST-008 IPv6 WAN 绑定、LAN 拒接和公网开关；Go 发布基线通过 |
| TASK-007 | REQ-004～008 | 进行中 | 正式提交发布，精确 SHA 的 Actions 及 AX56U 资产验收 |

追踪：REQ-004→UNIT-003→TASK-005/006→TEST-005；REQ-005→UNIT-003/005→TEST-006；REQ-006→UNIT-004/005→TEST-006/007；REQ-007→UNIT-003→TEST-005/007；REQ-008→UNIT-007（tls/platform/engine/controller/control/upgrade）→IF-003（vr_bind6_carrier 的 WAN 参数及双栈出站绑定）→TASK-005/006→TEST-008。源代码/ABI 测试保持同一接口，认证与帧格式不变。

回滚：撤销本次代码并发布更高补丁版本；配置/身份无迁移，停止时清理新增自有链。准备门禁通过；追加完成门禁待测试和发布。当前唯一进行中 TASK-007；下一步唯一动作：提交已验证代码并跟踪正式 CI 的真实内核和厂商 ABI 验证。

2026-11-05 本地检查点：TEST-005/006 的原生及 ASan/UBSan/leak 回归通过，实际 sizeof(cache entry)=16，4096 条 64 KiB；128 会话容量及第129个拒绝、600秒边界、72小时到期、当前规则索引、拒绝、无出口不回退、淘汰和代数失效均已覆盖；直连批量到期一次清空内核子链，未到期记录仍保留且可重装。TEST-007 本地真 socket 的关闭/背压/半关闭与 TLS 字节完整性通过。Go controller/node 全套与 vet、发布合同通过（controller core 7.765s、tests 3.447s；node 45.720s；workflow 1.836s）。本地新版 libcurl 的既有 deprecated 警告仅降级为 warning；时钟偏移有 make clock-skew 提示，实际目标均重新构建。测试曾暴露旧 transport fixture 假定 IPv6 忽略无效 IPv4 WAN，现按新需求用 lo 作为隔离 WAN，并增加无效 WAN 拒绝和 accepted socket 继承设备绑定验证。一次 fixture if 缩进警告已修正。WSL 用户态缺乏 root 导致 CA 写入失败后改用本地 WSL root；需容器隔离的升级/control/真实内核脚本由 guard 拒绝，未绕过，留待正式 CI。Docker Desktop 当前未运行；不启动生产主机或测试容器。

CON-002：发布 preflight 仍要求已退休 Docker/路由 arm64 三个标记。依据用户现有发布范围、当前 release.yml（9个 job/10个资产）与通过的 Go workflow 合同，只在临时预检副本删除这3个过期要求，其余检查通过：branch/remote、无 git 操作、GitHub Actions/Release 读取及认证；工作流 hash=52ffa5ca0164236962517061abb1d44c6d675042cb250e6f512e9b6d5b0f7f73。未修改用户技能或发布工作流，未恢复 BE86U/Docker/arm64 路由产物。

已提交功能 b4573f37，正式发布 v0.4.81，并在线升级 BE86U 192.168.50.1。native 全套、四套 ARM、ASan/UBSan、15项 Go/C 互操作与 Go 发布基线均通过。现场两组共6个5秒 CPU 窗口，插件单核合计0.8%～1.6%，线程23096处于do_sys_poll；新SNI活动、主控和原5条四槽位链路正常，配置hash一致。旧版升级前已短暂自发恢复，本次不把升级后的低CPU作为单独因果证据。AX56U仅验证兼容、未升级。完成门禁通过；下一步唯一动作：无。

2026-10-20 实现检查点：永久写错误立即 drop；无事件 fd 从 poll 排除；直连读 EOF 分方向并在已缓存数据排空后 SHUT_WR。main=8 仍沿用完整关闭协议。新增真 socket 回归覆盖双向永久写错误、关闭/背压下无持续 readiness、65536 字节请求半关闭后完整响应、远程 close 的数据排空/顺序。首次 test-tcp-close 和现有 test-tls-proxy 均通过；当前 TASK-002 进行中，下一步完整构建与发布基线。

2026-10-20 验证检查点：full-ci.log 的完整 build-ci.sh exit 0，native 全套、四套厂商 rootfs 及两款包通过；ASan/UBSan + leak 检查的 test-tcp-close 通过。Go controller/node 全套、controller vet 和发布工作流专项 exit 0。四套 ARM 生命周期回归与重新编译的 Go/C 原生/厂商 ABI 互操作正在运行；新增 ARM 回归已加入 build-ci.sh。准备门禁仍通过，完成门禁等待互操作、正式发布及现场。

2026-10-20 发布准备：arm-interop.log exit 0，4/4 ARM 生命周期测试和 5轮/15项真实 Go/C 测试通过，覆盖客户端/服务端/旧单槽位、热更新、main=8、ping、上传下载。full-ci.log 的负例压缩包/非法映射日志属预期测试，整套 exit 0；互操作环境存在既有小 socket 缓冲/CAP_NET_ADMIN 提示，协议断言全部通过。最终仅5个任务文件，无密码、生产配置或构建产物。

2026-10-20 发布等待：功能提交 b4573f37d739aea3ada832a875e987c28e2fad49 已推送 origin/mapledev。提交前后仅本次5文件，无其他工作区改动；fetch 确认远端未分叉，无需 rebase。Git 自动整理报告既有不可达对象提示，未执行 prune。下一步唯一动作：跟踪精确功能 SHA 的 Actions 与正式附件。

2026-10-20 Actions 已定位：https://github.com/fengzhanhuaer/CloudHelper/actions/runs/39359588671，headSha 精确 b4573f37d739aea3ada832a875e987c28e2fad49，发布构建进行中。生产 BE86U 仍 v0.4.80，未提前部署测试构建。

2026-10-20 升级前追加事实：before-upgrade.json 显示同一个旧版 PID 14053 / v0.4.80 已自发恢复到插件单核合计 1.4%、1.4%、3.01%，SNI/网关仍启用，5条健康链路各4槽位。异常会话可能因既有120秒空闲超时或客户端状态变化释放，具体释放原因未采到；不得将随后升级后的低CPU简单声称为100%→低值的实时因果证明。修复根据先前六组挂断元数据、会话残余缓存与确定性回归确认，并现场验证新版本连接及有界观察。

2026-10-20 正式发布核验：Actions 39359588671 的9个 job全部 success，headSha 精确 b4573f37d739aea3ada832a875e987c28e2fad49。v0.4.81 指向 ea0ab6e2c9529d819ecda12c8bc2baf818e9e39e，父提交为功能 SHA。12个发布资产非空，非草稿/预发布；两款 tar.gz 和 JSON 均下载，4文件 digest、内部清单、版本、profile 及全部成员 hash逐一校验通过。两包二进制 SHA256=f0851c1791d5529a0d106d556a392a1ea2d4a682bf1daac017b12d66fb8f571d。准备开始 BE86U 在线升级；AX56U 未变更。

2026-10-20 BE86U 在线升级完成：通过 admin.sock 的产品 upgrade action，携带当前 CSRF，mode=proxy，下载走已认证主控代理；未通过 SSH/SCP 写二进制。期间管理接口短暂断开，监控脚本第一次导入时误触发一次未捕获查询，修正本地脚本入口后已确认 version=v0.4.81、phase=complete、controller_connected=true、5条链路连接、SNI/网关均true。读取运行文件 SHA256 与正式包完全一致。正在执行 after-upgrade-1.json 的连续 CPU/SNI 状态采样。

2026-10-20 最终现场检查点：BE86U PID23066 / 网络线程23096，after-upgrade-1/2.json 共6个5秒窗口单核合计1.6/1.0/0.8/0.8/1.0/1.4%；线程实际阻塞do_sys_poll。两个窗口之间有新SNI日志（last_tls_at递增），SNI/网关继续启用；主控在线、原5条健康链路均4槽位，原有离线peer15/16未新增回归。config_sha256与升级前一致。全部需求闭合，TASK-004完成，下一步唯一动作无。最终仅归档本账本，不触发另一功能版本。

2026-11-05 最新修正检查点：用户将会话上限由128改回256，明确动态内存逐步增长、关闭释放。DEC-005 替代 DEC-003 的128上限：256槽仅保留指针，会话状态calloc按连接创建，关闭free状态和全部方向缓冲；数据从4KiB逐步增长到每方向最多64KiB。此前128容量测试保留为历史证据，256容量及动态状态释放的ASan/UBSan回归正在复测；缓存仍4096条/72小时/64KiB、闲置600秒。其他模块验证无需无理由重跑，新增范围只重测受影响TCP/内存和最终原生构建。

2026-11-05 最终本地验证：256 会话容量及第257个拒绝通过；会话状态及双向缓冲关闭释放、4KiB→8KiB→12KiB线性增长通过。最终 native 构建、test-tcp-close/test-tls-proxy 和同两项 ASan/UBSan/leak 检查全部成功（chvr-sni-256-final.log、chvr-sni-256-growth.log）。其余已完成 Go/网络/平台验证保持有效；TEST-007/008 真内核、厂商 ABI 由正式 CI 隔离执行，完成门禁待该结果和正式资产。仅本次28个路径提交，既有升级续传账本保持未暂存。

2026-11-05 发布等待：功能提交2543c2062420637d72f0580e14449ebdd923f68e已推送origin/mapledev；Actions 42022644541精确匹配，https://github.com/fengzhanhuaer/CloudHelper/actions/runs/42022644541。当前TASK-007进行中，下一步唯一动作跟踪该运行并验收AX56U包；生产设备未升级。

2026-11-05 DEF-002：首次正式运行42022644541在Ubuntu18.04旧json-c编译时失败：tcp_proxy.c:84 size_t与int比较触发-Werror=sign-compare；本地新版json-c返回size_t未暴露。改为显式转换非负数组长度为size_t，不改变范围或放宽编译规则。该次v0.4.108未创建正式Release；仅工作流版本提交8800bad9e已产生，本地快进到该版本以继续发布修复。

2026-11-05 DEF-002本地复测：sni-json-c-compat-build.log构建成功，test-tcp-close/test-tls-proxy exit0。下一步唯一动作提交tcp_proxy.c及本账本，提交信息fix: support legacy json-c array length in router cache，再跟踪v0.4.109正式CI；升级续传账本不暂存。

2026-11-05 修复发布等待：d38c36ca71abcec229e26df10cff381a06e9b6c7已推送；精确Actions https://github.com/fengzhanhuaer/CloudHelper/actions/runs/42023077498 对应v0.4.109，旧json-c编译已越过原失败点，插件验证继续。

2026-11-05 DEF-003：v0.4.109运行42023077498已通过完整原生、四套ARM ABI与打包，在新增真实内核夹具test_platform.c:75的合并IPv6监听/防火墙断言失败，尚未测试连接保持。产品command_code将stderr重定向/dev/null，原失败未暴露系统原因；只在测试夹具保留ip6tables stderr并拆分监听/防火墙断言，未放宽断言、不推测修复产品。新增夹具已本地严格编译；下一步唯一动作通过诊断CI取精确失败命令。GitHub API本机TLS访问间歇失败，已用授权localrouter的只读SSH本地转发16443读取日志；未写路由器配置、未部署。
