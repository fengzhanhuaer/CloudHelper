# 任务：51.1 到 AWS SG 的真实 TCP 吞吐调查

- 状态：进行中；用户最新指令：也可以加更多观测点，改代码。
- 启用方式：跨源码、两端运行态与多轮有界诊断的明确长任务。
- 范围：诊断、添加按需观测点及验证发布；不改生产网络参数。设备在线升级另行确认。

## 一、需求定义

REQ-001：解释节点间测速 70–80 Mbps，而 51.1 经 AWS SG 的外部 TCP 下载约 5–9 Mbps 的差异；结论须区分已证实事实和假设。
REQ-002：控制槽位、任务路径与负载，检查单线程 CPU、内外层 TCP 和队列，避免把合成测速与外部下载混为同一机制。

## 二、总体架构

51.1（节点26 HM_A_R，v0.4.138）经 IPv6 TLS/WebSocket 四槽位连接 AWS SG（节点27，v0.4.138）。main=4 合成测速选择首个在线槽位；main=1 真实 IP 流按端点哈希固定槽位。真实 TCP 多出入口内核 TCP、TUN/NAT、出口 gVisor TCP 转发与外部 HTTPS 下载。

IF-001：插件 Unix socket status/measure/measure_stop，沿用 CSRF，仅测试任务变更。
IF-002：主控已有节点 telemetry、Probe console、shell session，只用于26/27诊断，凭据与会话不写账本。

## 三、单元设计

UNIT-001：复用 SSH 读取26的每线程 /proc stat、接口与 measurement；取样开始/结束的槽位字节差确认实际槽位。
UNIT-002：27通过主控既有认证 shell 读取 ss TCP 状态；对齐同一次测试。
UNIT-003：本地读取 carrier/measure/dataplane/exit_netstack，定位路径差异与可能的协议开销。

## 四、执行任务

| 任务 | 状态 | 完成条件 |
|---|---|---|
| TASK-001 源码与已有现场对账 | 已完成 | 明确两类测速和槽位选择差异 |
| TASK-002 有界复测与线程、槽位、TCP采样 | 进行中 | 对齐真实任务及资源变化 |
| TASK-003 汇总结论 | 待开始 | 有证据地指出瓶颈或最小剩余不确定项 |

下一步唯一动作：执行一组有界节点测速与真实下载，对齐槽位增量和每线程 CPU。

## 五、测试与验证

TEST-001：既有现场：AWS直连64MiB/257ms；26经27 11.29MiB/9997ms=9.47Mbps，复测5.45MiB/9989ms=4.58Mbps。
TEST-002：待执行：每线程CPU与槽位增量、TCP样本；避免仅看总CPU空闲得出主线程未饱和。
TEST-003：源码核对：C/Go均在main=4选active[0]，main=1按流亲和；已验证。上次真实下载走slot1。

## 六、追踪与决策

REQ-001→UNIT-001/002/003→TASK-001/002/003→TEST-001/002/003。
REQ-002→UNIT-001/002→TASK-002→TEST-002。
DEC-001：不依据“有重传”单独归因于跨境线路；当前未证明是线路容量、单槽差异、单线程或内层TCP约束。
冲突：先前将70–80Mbps误解为AWS外网速度，已按用户纠正修正。

## 七、缺陷、回滚、事实与风险

尚无已确认产品缺陷；无生产配置变更可回滚。测试仅影响本次有界测速流量。
已知：26总CPU空闲不低，但未获取每线程负载；27外层TCP上次出现cwnd96→47和7次重传，尚不足以证明根因。
风险：非同时测试的线路波动、不同槽位和测速源短时突发可混淆比较。

## 八、门禁与检查点

准备门禁：通过。完成门禁：待调查证据。


## 九、2026-11-27 观测点实现范围更新

REQ-003：增加认证管理接口可读的内层 TCP 拥塞窗口/RTT/重传/缓冲；仅按需读取，禁止为诊断启用逐包 gVisor TCPProbe 状态复制。
REQ-004：路由器采样记录真实收发、NAT反查、DNS黑名单扫描、TUN写入耗时；记录实际TCP_INFO与槽位。默认关闭，最多60秒、每64次采样一次、不写JFFS。
UNIT-004：Probe 活跃出口连接有界注册（64条、开关一次/关一次锁），GET已认证debug?tcp=1时读取原始 endpoint.GetSockOpt TCPInfoOption；连接结束注销。无逐包回调或改变 io.Copy。
UNIT-005：C RAM观测计数与TTL；status.observation、CSRF observe action，外部测速自动启动一次有界采样；disabled时不增加逐包计时调用。
TEST-004：Go registry容量/清理、有效TCP_INFO快照、认证；C关闭不采样、开启采样/到期、TCP_INFO/路径不改变。
TASK-002现场采样已完成；TASK-003改为观测实现（进行中），TASK-004验证发布（待开始）。
当前事实：节点测速51.86Mbps，external不同槽位8.27/9.34/9.37/8.52Mbps；内层捕获无数据重传/SACK、窗口增长至742144字节；26engine线程峰值累计占单核78.8%；27进程真实负载约0.32核，systemd CPUQuota无限，CF固定IP8MiB下载分别119MB/s与83MB/s。AWS goroutine捕获正在gonet.TCPConn.Write等待可写，CF socket接收队列267–312KiB；输出队列协程空闲，指向内层TCP/入口消费需要进一步精确观测，未确诊具体函数。
SSH：用户新增awssg并更新awssg.pem后已认证连接成功（admin可sudo）。仅记录入口名，不保存密钥。
接口：IF-003 Probe GET /local/api/virtual_router/debug?tcp=1（新增字段兼容）；IF-004 插件observe动作duration_sec 0–60及status.observation。
决策DEC-002：先观测再调参，不调整TCP算法、窗口、队列或选路；不改TLS与认证。
准备门禁：通过。下一步唯一动作：先写并运行观测回归的失败测试，再实现。


### 实现检查点

- Go RED已确认（缺失registry），已实现64条有界活跃端点注册，完成/失败释放；明确tcp=1才采样真实TCPInfo/GetSockOptInt，未安装TCPProbe回调。继承现有异步forwarder的goroutine，去掉relay中的冗余二次go启动，保证观测租约活到实际转发结束；不包装net.Conn，不改变io.Copy分发。
- C RED已确认（缺失观测结构/函数）；已实现<1KiB RAM计数、CSRF observe动作0–60秒、每64次一次时钟采样、自动测速30秒TTL，纳入CORE避免旧测试链接遗漏。
- C 原有dataplane/measure/external-speed、管理API生命周期及新观测test通过；普通包校验/NAT/黑名单仍在原顺序。附加TCP_INFO使用有长度检查的内核ABI解析，保留最终有效下载套接字样本。
- 首个Go认证测试误传nil cookie可变参数导致测试工具AddCookie panic，已修正测试调用；生产路径未出现该问题，待重测。
- 下一步唯一动作：Go定向和全量测试、C构建验证后发布观测版。生产尚未改动/升级。

- Go真实gVisor端点选项/缓冲读取、64条容量与幂等清理、认证和tcp=1可选读取均已通过。容量满时仅淘汰最旧的观测记录（不关闭实际连接），保证新测速能被观察。
- C定向回归通过：关闭无时钟采样、TTL过期、旧内核TCP_INFO部分字段为null、NAT路径/防伪/黑名单不变、观察开关不写配置；状态保留最后TCP_INFO并注明样本年龄。
- 当前下一步：完整Go回归、C所有相关目标链接检查，再按现有工作流发布。已请求代码观测，尚未获本次设备升级指令，生产版本仍v0.4.138。

- 完整回归：Probe全部包（主包67.7秒）、mobilecore、主控全部测试及vet通过；Linux Router amd64/arm64构建通过；C相关目标（含carrier/platform/path-selection）编译、observe/external/dataplane/measure/path/API测试通过。
- 源码核实gVisor MaxSegOption仅返回默认占位值，移除伪实际MSS字段，入口内核TCP_INFO提供真实snd_mss/rcv_mss。阶段为采样墙钟时间且有嵌套，不能相加当CPU。
- 发布预检ready=true；工作流SHA256 12e06f440e788030ad88649de2dd86e05b3ba8b5bbe0c09049777161ab699912未变。下一步唯一动作：提交并发布观测版。

- 用户最新约束：不要再弹出测试EXE授权。后续不再执行Windows原生测试EXE；必要补测交叉编译Linux测试程序，在本机WSL中执行，不使用HomePc。已有Windows回归已全部结束。

- 最终补测在本机WSL执行Linux ELF：Go观测定向测试PASS；C完整构建和test-observe通过，未执行Windows测试EXE。TASK-003完成，TASK-004进入提交/Actions发布；性能根因仍需新版本现场采样。
