# 任务：修复 v0.4.119 插件崩溃及状态页误报

- 状态：已完成
- 创建时间：2026-11-13 Asia/Singapore
- 启用方式：明确长任务条件，跨现场诊断、兼容修复、页面验证与发布。
- 用户最新指令：192.168.52.1 升级后前端显示不正常；在线升级已不可用，发布后直接替换插件。

## 一、需求定义

截图为 v0.4.119，“Virtual router service unavailable”，地址为空却仍显示连接中。SSH 实测插件反复 SIGSEGV，管理 httpd 保留。保留用户配置；本次修复空探测任务兼容及真实不可用显示，不改变选路协议。

| 需求 | 验收标准 | 状态 |
|---|---|---|
| REQ-001 | 主控未下发任务或空任务清单不崩溃，兼容固件旧 json-c | 已完成 |
| REQ-002 | 页面明确显示管理服务不可用，不继续展示未连接/空地址为当前状态；恢复后正常显示 | 已完成 |
| REQ-003 | 定向真实库回归与页面桌面/窄屏验证通过；发布修复版并 SSH 手动恢复 52.1 | 已完成 |

## 二、总体架构

主控控制流 → vr_monitor_submit → json_object_array_length；主控空清单可省略 tasks 字段，固件 json-c 不支持此函数传 NULL。界面 api 失败只更新 notice，旧 status 保留导致误报。沿既有管理接口恢复轮询与错误状态。

### 接口记录

- IF-001：network_monitor_tasks，省略/null/[] 的 tasks 均为空清单；其他类型不接受。沿主控既有协议，无新字段。
- IF-002：页面 status 请求失败应呈现不可用，成功响应恢复当前运行状态；保留编辑框输入。

## 三、单元设计

| 单元 | 文件 | 处理 | 验证 |
|---|---|---|---|
| UNIT-001 | src/network_monitor.c | 空字段先判断，不向旧 json-c 传 NULL；空清单清除任务 | 原生及固件 ARM 库回归 |
| UNIT-002 | web/VirtualRouter.asp | 状态请求失败独立呈现未知/不可用，禁用依赖状态操作，下一次成功恢复 | 浏览器模拟失败/恢复，桌面/窄屏 |
| UNIT-003 | tests/test_network_monitor.c、tools/build-ci.sh | 空任务回归在四种固件库执行 | QEMU 输出和 Actions |

## 四、执行任务

| 任务 | 内容 | 状态 | 需求 |
|---|---|---|---|
| TASK-001 | 现场诊断和定点复现 | 已完成 | REQ-001 |
| TASK-002 | 兼容及页面修复、相关验证 | 已完成 | REQ-001,002 |
| TASK-003 | 提交发布并核验 | 已完成 | REQ-003 |
| TASK-004 | 使用正式包手动恢复 52.1 并核对状态 | 已完成 | REQ-003 |

下一步唯一动作：无。
基线 f5ecf9999；无关 doc/tasks/2026-10-28-upgrade-source-resume.md 不改不提交。

## 五、测试与验证

| 测试 | 方法 | 状态 |
|---|---|---|
| TEST-001 | 原生 monitor 测试及 QEMU/四固件库空清单回归 | 已完成 |
| TEST-002 | 管理控制流省略 tasks 集成及既有双栈任务结果 | 已完成 |
| TEST-003 | 浏览器状态正常→服务不可用→恢复，桌面/窄屏，无异常 | 已完成 |
| TEST-004 | 本地 ARM 构建、必要组件回归与 Actions/资产核验 | 已完成 |
| TEST-005 | 真机正式包安装、配置未改、主控及链路恢复，跨多个汇报周期进程不重启 | 已完成 |

## 六、端到端追踪

| 需求 | 单元/接口 | 任务 | 测试 |
|---|---|---|---|
| REQ-001 | UNIT-001,003 / IF-001 | TASK-001,002 | TEST-001,002 |
| REQ-002 | UNIT-002 / IF-002 | TASK-002 | TEST-003 |
| REQ-003 | UNIT-001..003 | TASK-003,004 | TEST-004,005 |

## 七、决策与冲突

- DEC-001：根因修复与状态显示一并处理，不以 CSS 掩盖真实服务崩溃。
- DEC-002（已被 DEC-003 替代）：默认通过产品在线升级部署修复。
- DEC-003：用户明确授权“发布后，直接替换吧”“已经不能在线升级了”，发布验证后使用正式资产手动安装 192.168.52.1，保留配置并核对运行状态。
- 无冲突。

## 八、缺陷记录

- DEF-001（已修复）：高，v0.4.119 空任务消息导致 SIGSEGV。现场 LR=0x3a874；本地同源 ARM 反汇编 0x3a870 为 json_object_array_length 调用，r0/r9 均为 NULL，和 network_monitor.c 176 行吻合。原厂库回归与生产替换后稳定观察通过。
- DEF-002（已修复）：中，status 请求失败仅显示英文错误，页面继续保留旧连接中状态；受控浏览器失败/恢复回归通过，生产页面文件摘要已确认。

## 九、回滚方案

修复版异常通过在线升级回退已知稳定版；任务状态仅 RAM，无持久化迁移。配置保持，核验管理状态、主控和承载。

## 十、已验证事实

- SSH localrouter 当前指向 192.168.52.1:10022；httpd 存活，插件进程在采样时不存活，内核持续同位置 SIGSEGV。
- 原厂 json-c 为 libjson-c.so.2.0.2；已确认主程序调用空对象 array_length 的 ARM 指令，与崩溃 LR 完全一致。

## 十一、风险与阻塞

- 真机后台登录凭据不写日志；先以 SSH 只读证据和浏览器受控响应验证。
- Browser plugin not available，页面验证使用现有 Playwright。
- 已按用户后续明确授权安装正式 v0.4.120，原 config.json 未改变。
- 已获发布后手动替换授权；不得在发布验证前部署开发包。

## 检查点：实现与针对性验证

- NULL 调用已修复；省略/null 清单统一按 0 条处理。原生 monitor（含双栈真实 ICMP）通过；ARM 原厂 ax56uv2 json-c 空清单测试 PASS。
- 修复前 QEMU 程序 SIGSEGV，产生 core 后信号处理等待，已停止该验证容器；core 移至 `%TEMP%/cloudhelper-monitor-before-fix.core`，未纳入仓库。现场 LR 对应 NULL array_length 调用，和此复现一致。
- Playwright 受控接口场景通过（真实 ASUS 公共样式、当前页面代码）：1280×900 与 390×844，正常→英文服务失败→网络失败→恢复；保留编辑内容，旧状态不再伪装当前状态，依赖服务的按钮禁用/恢复；6 次 status 请求，0 pageerror。脚本与截图在 `%TEMP%/cloudhelper-router-status-qa*`，未写入仓库。此测试不代表生产后台已恢复。
- 正在执行固定 SDK 的完整 C 验证与 ARM/ABI 打包；没有修改 Go、认证、防火墙或现有用户配置。
- 发布预检 ready=true，沿 live workflow 十资产集合；原 skill 预检的退役 Docker/ARM64 router 标记沿用已核验的临时兼容预检，无 workflow 变更。

## 检查点：本地验证完成

- 固定 SDK `router_plugin/tools/build-ci.sh` 退出 0，C 原生全回归、控制流 IPv4/IPv6、升级回归、ARM 编译、四固件库空清单测试和 ABI 检查及 JFFS 打包全部通过。原生升级负例中的损坏 gzip 和断连日志为预期，流程最终通过。
- `go -C probe_node test . -run 'TestReleaseWorkflow(DefinesLinuxRouterArtifacts|DoesNotPublishStandaloneMihomoExitArtifacts)' -count=1` 通过；本次未改 Go 文件，不重复执行其全量测试。
- diff --check 通过，生产尚未修改。正式发布完成后按用户授权恢复 52.1。

## 检查点：发布运行中

- 提交 `fb636cf6bbddf184a7fe76c7ac089f9fbbdf6098` 已推送 origin/mapledev；对应 Actions： https://github.com/fengzhanhuaer/CloudHelper/actions/runs/43257904460 。
- 本机直连 GitHub 解析/连接失败，使用临时 SSH SOCKS（netcup2o）恢复 Git/gh 访问，只有单次命令代理，无持久网络设置变更；任务完成关闭转发。
- 安装前配置 SHA256 为 `47a0571a856fbc89d27748d3acc6153e0894d56531acc3f545f400128768bbf8`；旧 active 为 `745c6fcd49185c3c`。设备 RT-AX55/AX56U V2 兼容用户态，49559_koolshare；保留已有 config.json。
- 生产 Unix 管理 socket 请求当前 curl exit 7，与插件崩溃吻合；正式替换后通过同一接口核对主控及链路。

## 检查点：正式包已验证，开始手动恢复

- Actions 43257904460 completed/success，9 作业成功；Release v0.4.120 十资产非空且精确匹配 workflow。
- 版本提交 `7a35caa35244c9825758057d29fc78017ebf9b01` 的父提交为修复提交，v0.4.120 标签指向该版本提交。
- 正式 JFFS 包 136862 字节，SHA256 `180c821947317d200e719e12a49aa68b1c7f8f371cd127d3282e290001cd3505`，release_id `1299f413790a3563`；manifest 版本与压缩包摘要、内部 11 项文件大小和摘要全部通过。
- 按用户明确手动替换授权，仅使用正式包安装器更新 192.168.52.1，保留配置；尚未执行安装。

## 检查点：真机已恢复，稳定性观察中

- SCP 上传到 `/tmp/chvr-recover-v0.4.120/package.tar.gz`，远端 openssl 摘要匹配正式包；包内安装器退出 0，保留配置并注册已有 KoolCenter 启动钩子。
- active=`1299f413790a3563`；服务 PID=7877；Unix 管理接口 ok=true、v0.4.120、ready=true、controller_connected=true、控制地址 94.16.111.150:15030、LAN 192.168.52.1、虚拟 IP 198.18.0.16、端口 12040。
- 已有 peer 17/1 四槽位及 peer 24 链路恢复，其他链路仍在建立；不把初始快照等同所有节点均可达。
- config.json SHA256 与安装前相同。部署后的 dmesg 崩溃计数 30，先前采样 24；期间仍运行旧版重启任务，不能把差值归于新版，后续以 PID 稳定及计数不增加验证。
- 真实 ASUS 浏览器自动登录尝试一次失败（未记录凭据），未重试；生产前端视觉需用户现有登录页刷新确认。已通过的浏览器故障/恢复测试为受控接口，真实服务恢复由 Unix API 直接验证。

## 检查点：链路已全部恢复

- 后续状态采样：ready=true、controller_connected=true、error 为空；9 个对端（14、9、17、1、24、26、20、4、27）全部 connected、各 4 槽位。
- 同 PID 7877 持续运行，dmesg fatal-signal 计数保持 30，无新增崩溃。
- RAM 运行程序 SHA256 `b978512f7499519e2aec45d8959ccc5ecd0dd99e46d08d107fd99deb17fdf543`；已挂载页面 SHA256 `5a2d9fcee5d5225dcfa90645e96ad1d78c287d62abd80c0853d726cf4e5ea5ce`；均与正式包 manifest 一致。
- 最后跨周期复核后，清理本次 /tmp 安装暂存并关闭本机临时 SSH 转发；JFFS 正式版本、原配置与安装器保留的上一版本继续保留。

## 十二、质量门禁

- 准备门禁：通过；根因、范围、兼容测试与回滚已记录。
- 完成门禁：通过。REQ-001..003 已覆盖，定向四固件库、浏览器故障恢复、Actions 和真机稳定性证据完整；生产网页登录未通过的验证边界已记录，不宣称生产浏览器已验收。

## 完成记录

- 连续三次生产采样跨多个 20 秒汇报周期：PID 始终 7877、主控连接正常、9 个对端均 4 槽位、崩溃计数保持 30；配置摘要始终一致。
- `/tmp/chvr-recover-v0.4.120` 本次安装暂存已清理；仅保留正常 JFFS 正式/上一版本和用户配置。
- 代码修复及发布、生产恢复均完成；用户现有网页可 Ctrl+F5 刷新以载入新版状态逻辑。
- 本账本完成记录独立提交，skip ci/skip release，不触发额外版本。随后关闭本机临时 SSH 转发。
