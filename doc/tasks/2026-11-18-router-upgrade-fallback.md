# 任务：路由器插件升级自动切换下载通道

- 状态：已完成；日期：2026-11-18 Asia/Singapore。
- 用户最新指令：优化。承接方案：主控 IP 优先、失败自动回退、同一包续传、区分下载设置、上报真实进度和失败原因。
- 启用方式：明确长任务条件，涉及 C 下载、控制流上报、Go 主控及界面和正式发布。

## 一、需求定义

现场：节点 26/192.168.51.1 收到升级指令，GitHub 直连下载失败，v0.4.120 保留运行；本次经产品在线升级入口改用主控通道，已升 v0.4.122，主控在线且通用 MSS 规则生效。节点 25 也已是 v0.4.122。旧日志没有具体错误，不能断言历史失败的底层网络原因。

| 需求 | 验收标准 | 状态 |
|---|---|---|
| REQ-001 | 已保存身份的插件升级优先缓存主控 IP，再主控域名，最后 GitHub；旧 direct/proxy 指令均兼容，不因 GitHub 直连设置跳过主控 | 已完成 |
| REQ-002 | 单路、有界重试、部分内容保留，跨源 ETag 不阻止同一 SHA256 包续传；最终 hash、包格式/设备校验不放宽 | 已完成 |
| REQ-003 | 显示来源、进度、速率、HTTP/curl 失败原因，主控接收升级状态；已下发不冒充已完成 | 已完成 |
| REQ-004 | 相关真实 TLS/断点/失败回退及固件 ABI 验证通过，按工作流发布；不替换生产二进制、不新增 BE86/Docker 产物 | 已完成 |

范围：router_plugin 升级下载、局部界面、主控升级报告和展示。非范围：Probe/Android 下载算法、承载控制连接策略、路由规则和认证变更。缓存与进度只写 /tmp/var/run，不增加 JFFS 写入。旧包 ABI 兼容。

## 二、总体架构

现状：upgrade.c 的 direct 模式完全绕开主控；fetch 仅在 IP 连接失败后切到主控域名，不会回退 GitHub；256KiB 以上启动两路。失败清空字节计数，控制流未上报升级结果。

目标：单路下载状态机，metadata 和包都采用主控 IP→域名→GitHub。每次主控请求重新领取 nonce 并私有认证，IP 保留 SPKI pin。部分响应交还外层，以新偏移续传；校验后才安装。可重试错误切源，缓存 preparing 等待原源，不当成失败。状态独立报告，上报和页面只展示非敏感来源与错误。

IF-001：升级 JSON 新增 source/endpoint/speed_bytes_per_sec/http_status/curl_code/error，保留旧字段；主控 report 增加 router_plugin_upgrade_status，可选以兼容旧端。
IF-002：原 direct/proxy 命令仍接受，已配置主控的插件统一自动选源；无主控身份的显式 direct 保持 GitHub 能力。主控页面说明 GitHub 下载设置与主控连接的区别。

## 三、单元设计

UNIT-001：upgrade.c：下载单路化、元数据与文件选源、低速切换、断点与错误保留、节流更新。
UNIT-002：control.c、probe_ws.go、probe_runtime.go：以复制 JSON/类型字段上报升级状态，避免旧 json-c 跨线程引用竞争。
UNIT-003：VirtualRouter.asp、mng_pages/probe.html：显示实际下载来源/速率/错误和升级阶段，澄清直连下载标签。

## 四、执行任务

TASK-001 调查设计：已完成。基线 9a0537984，干净工作区，mapledev；历史 10-28/10-29 升级账本已交付，本账本跟踪本轮插件新行为。
TASK-002 实现及定向 TLS 验证：已完成。缓存 preparing 响应体不写入升级包已随固定 SDK 复验通过。
TASK-003 固件构建、主控验证与发布：已完成。
下一步唯一动作：无。

## 五、测试与验证

TEST-001：真实 TLS 验证主控 IP 优先、IP→域名→GitHub 回退、每次新 nonce、错误 pin 拒绝。
TEST-002：中断跨源 Range 续传、不同 ETag 同包、错误 Range 不写入、低速/错误分类、最终 SHA256 拒绝错误内容。
TEST-003：状态保留字节数/错误，主控保存兼容旧端和展示，浏览器展示/提交状态。
TEST-004：固定 SDK ARM/QEMU ABI 与插件既有升级夹具；主控相关测试与完整 test/vet；正式工作流及包清单核验。
TEST-001/002：真实 TLS 主控 IP→域名→GitHub，IP 写入两字节后断开，GitHub 以不同 ETag 从偏移 2 续传，最终内容正确；新 nonce/Range、错误 pin、缓存 preparing 不触发切源均通过。升级 tar/CRC/机型/中断/忽略 Range 重下/错误 Range 拒绝夹具通过。最后补充 preparing 有响应体用例也已在固定 SDK 中通过。
TEST-003：主控报告解码、保留失败字节/错误、对象复制、错误长度边界、旧端清理状态通过；主控全套 test ./... 和 vet ./... 通过。真实 Chrome 隔离夹具在 1280px/390px 验证升级按钮实际请求 action/mode/csrf/check_only、两端来源/字节/速率/失败/完成展示及 HTML 注入转义通过。此为源码页面夹具，不冒称生产 UI 验收。
TEST-004：Probe 全套 test -p 1 ./... 通过（main 71.287s，含 mobilecore）；固定 SDK build-ci.sh 退出 0，IPv4/IPv6 升级夹具及新 SHA256 错误不启动安装器用例通过，元数据 IP 优先/跨源断点/有响应体 preparing 不落包均通过；ARM 交叉编译及四套固件 QEMU/ABI 检查通过。开发包 137494 字节，仅为验证产物未安装。预检通过（TEMP 预检副本仅移除已退役 Docker/ARM64 发布标记，其他检查保留；实际 workflow 未修改）。
不恢复此前删除的五组耗时互通测试。TLS 测试服务器出现的 ConnectionResetError 为无 HTTP 请求的预连接探测关闭，测试断言/退出码通过，未屏蔽其他异常。

## 六、端到端追踪

REQ001→UNIT001→TASK002→IF002→TEST001；REQ002→UNIT001→TASK002→TEST002；REQ003→UNIT001/002/003→TASK002→IF001→TEST003；REQ004→TASK003→TEST004。

## 七、决策与冲突

DEC-001：插件体积小，移除双路实现，统一单路与固定内存。既有双路测试由有意义的单路/跨源测试替换。
DEC-002：不修改主控为旧插件下发的 mode 枚举，避免旧端拒绝新命令；新插件内部选源自动化。
冲突：无。

## 八、缺陷与回滚

DEF-001：direct 模式失败没有通道回退；已定位，待修复。
回滚：通过产品在线升级恢复上一正式包；保存配置字段不变，无数据迁移，不通过 SSH/SCP 替换生产。校验失败保留旧版本。

## 九、事实与风险

现场 v0.4.122 已在线升级成功，不等于本轮新算法已上线。主控需升级后才能保存/展示新增可选状态；旧主控忽略新增字段。

## 十、质量门禁

准备门禁：通过。实现与验证门禁：通过。完成门禁：通过。实现、测试、兼容与正式发布证据均闭合；生产在线部署不属于本轮请求范围。

## 十一、检查点

已检查当前工作流和历史升级账本，确认用户要求轻量单路与生产仅在线更新。当前无生产变更。

实现检查点：新增可选 router_plugin_upgrade_status，旧主控/插件均保持兼容；没有凭据落盘。升级状态通过 RAM 文件及原控制流上报；后台 JSON 先在锁内复制，避免旧 json-c 跨线程引用计数问题。主控 GitHub 设置明确标注，插件始终自动选源。

验证检查点：首轮编译发现 C 同行 if/free 的 misleading-indentation 和测试 URL 缓冲区过小，均修正；一次 TLS 测试误用了此前编译失败留下的旧测试二进制，已改为先成功构建再串行运行，真实回退新断言全部通过。所有这些失败已记录，没有放宽断言。

发布前检查点：所有规定验证通过，工作区 14 个任务文件，无其他改动；workflow 不变、mapledev、origin 和 GitHub 认证符合契约。准备提交并推送，生产尚未安装本轮改动。

发布检查点：7481ad71d00d25bfc8ee634cd14a588a0f53e851 已推送 origin/mapledev，无远端并发提交需要重排。匹配运行 https://github.com/fengzhanhuaer/CloudHelper/actions/runs/44303266775 正在构建；TEMP/cloudhelper-upgrade-release-watch.log 记录 watch，待完成再核验正式附件。

最终交付检查点：44303266775 全部九个 job success，正式标签 v0.4.123，工作流版本提交 088630540。https://github.com/fengzhanhuaer/CloudHelper/releases/tag/v0.4.123 的十个附件与现有 workflow 一致，无 BE86U/Docker 新产物。正式插件包 137503 字节，SHA256=5026d4671ff286a81f32e8a7df68402e7afc8277014c0d4c06bc9fba3c073be0，与 GitHub digest 及外清单一致；内清单版本/机型与全部 11 个文件的字节数和 hash 均核验。REQ-001/002/003/004 全部完成。正式包仅下载至 TEMP，未安装生产；插件升级后使用新策略，主控升级后展示新增上报。CI 仅有既有 Ubuntu runner 迁移/Gradle 生命周期提示，无失败。
