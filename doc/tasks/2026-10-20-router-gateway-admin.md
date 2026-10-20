# 任务：修复经授权网关访问华硕插件后台失败

- 任务标识：`2026-10-20-router-gateway-admin`
- 状态：`进行中`
- 用户最新指令：截图显示 Virtual router service unavailable；延续修复请求。
- 启用方式：明确长任务条件，涉及现场诊断、桥接访问策略、测试、发布和在线升级。
- 基线：`e05459bf`，mapledev，工作区干净。BE86U 插件 v0.4.81。

## 一、需求定义

REQ-001：已授权网关节点通过路由器 LAN 地址访问插件 API，应正常读取版本、状态及设置。
REQ-002：保留原厂登录、CSRF、Unix 对端 UID 校验和 LAN 目的地址限制；不得仅凭私网地址开放 API。
REQ-003：通过组件和发布验证后发布，并仅用产品在线升级更新 BE86U；验证运行状态及网页入口。

范围：httpd 桥接的来源访问判断与回归。非范围：无线、固件、网关认证规则、现有业务路由和源地址转换。
验收：LAN 及明确经 chvr0 回程的内部来源通过；其他来源/错误目的地址/缺少原厂登录/错误 CSRF 均拒绝；两款设备 ABI 兼容。

## 二、总体架构

现场：BE86U PID 23066、管理 Unix socket、主控和 5 条连接正常。网页请求来源 198.18.0.8，目的 192.168.50.1:80；桥接 lan_client 仅放行 LAN 同网段导致失败。
链路：原厂 httpd 登录 → bridge_input/output → Unix socket → 插件管理线程。
目标：仍要求目标为 LAN 地址；同网段沿用原判断；其他内部地址仅在内核主路由最长匹配为直接 chvr0 路由时允许。网关数据面在注入前验证节点身份、源虚拟 IP、目标 CIDR、允许节点，TCP 回程进一步经过此数据面。
接口：管理 API 格式不变；仅扩大已认证网关路径的可达性。

## 三、单元设计

UNIT-001：httpd_access.h 纯判断函数，读取内核 /proc/net/route，按最长匹配选择；只接受内部 IPv4、UP、无网关/拒绝标记、非默认、chvr0 路由；失败关闭。
UNIT-002：httpd_bridge.c 在确认本地目的 LAN 后调用；不改变登录/CSRF/Unix 认证。
UNIT-003：test_httpd_access.c 测试 LAN、网关、错误接口、缺失/默认/更具体拒绝路由及公网来源；既有真实 HTTP/HTTPS 桥接回归继续运行。

## 四、执行任务

| 任务 | 内容 | 状态 |
|---|---|---|
| TASK-001 | 判断实现及针对性回归 | 已完成 |
| TASK-002 | 组件/ABI/发布基线验证 | 已完成 |
| TASK-003 | 发布及 BE86U 在线升级/验收 | 进行中 |

下一步唯一动作：等待用户刷新已登录的原厂插件页面，确认版本与运行状态恢复；代码、发布和在线升级已完成。

## 五、测试与验证

TEST-001：来源/目的地址与最长匹配策略单元回归，native 通过；覆盖更具体/同前缀竞争路由、关闭/网关/拒绝标记、默认路由、错误地址及公网。
TEST-002：既有 test_httpd_bridge.py 实际 HTTP/HTTPS 请求通过：原厂未登录 401、错误 CSRF 拒绝、正常读写/stock 页面委托通过。
额外通过 Docker --cap-add NET_ADMIN 的隔离网络测试（test_httpd_gateway_network.py）：真实 chvr0 地址 198.18.0.8/15、另接口 198.20.0.8/24，无生产认证/路由 mock；导出/隐藏原厂符号、HTTP/HTTPS 均验证网关成功、非网关拒绝、未登录 401、错误 CSRF 拒绝。只在临时容器修改接口，未修改宿主/生产路由。
TEST-003：完整 build-ci.sh exit 0：native、ax56uv2/ax56uv2-koolshare/be86u/be86u-koolcenter 的新判断和 TCP 关闭回归通过，两款包生成通过。controller test ./...、vet ./...、node test ./... 和 workflow 聚焦回归均 exit 0；本机 WSL BuildServer/docker，日志在系统临时目录 router-gateway-admin。
TEST-004：精确功能 SHA 的 Actions/附件、BE86U 在线更新后管理状态和网页，待执行。
Actions 39370161638 全部 9 项 job 成功；v0.4.82 tag=fba601a16，父提交为精确功能 SHA 03dd26074。12 个正式资产齐全、两款 ASUS 包/JSON 的 GitHub digest、内部 manifest 全部文件大小/hash、版本与 profile 校验通过。两款运行二进制 SHA256 a46bb1253bbc572dadf7939e3e07a20d246dc859ae8da54fc88c039c1681f6f0；桥接库 77debe9b9b9ab92211bd4fb4bf9b8b389316fe8dafd89bc866ef3171a15786a5。BE86U 包 c4742b2ffa345900ac81f9bc27a55e6ff1aa3656cc2ff3d4bc9ca92fb5238d99。已经 Unix 管理 API 发起 mode=proxy 在线升级，未手工部署。
在线升级完成：BE86U PID 26331、httpd 26417/26418，version v0.4.82、phase complete、主控已连接、5 条链路 connected。运行二进制及桥接库 SHA256 均与正式包匹配；两个 httpd 的 maps 已映射当前桥接库，未保留 deleted 旧库；升级前后配置文件 SHA256 均为 6d5bbed87afd466c5d69f2f8282d6ed45f823028527754e9d8f9a0b3ab9f0d4a。升级过程短暂 socket 不可用属于重启窗口，10 秒后恢复。已请求用户刷新页面验收，等候回复；未读取路由器后台密码或绕过原厂登录。AX56U 未升级。
升级后 15 秒现场窗口：插件单核 CPU 0.6%/1.0%/0.6%，系统 CPU 约 1.8%–2.2%，SNI 有新记录。连接 25、17 两条 route、24、1 均为 4 槽位 connected；16/15 原有离线状态未变。本次未改变路由或其他设备。
发布检查点：功能提交 03dd26074b1fff6c70e0a41f8653cbcf9ae7741a 已推送 origin/mapledev；Actions https://github.com/fengzhanhuaer/CloudHelper/actions/runs/39370161638 运行中，自动版本 v0.4.82。Git 自动 repack 尚在运行，提交已单独安全推送，无手工清理对象。另将 BE86U 当前 /proc/net/route 读到本机临时目录，用正式源判断确认 198.18.0.8 → 192.168.50.1 返回 allowed=1。
测试工具补充：网络夹具增加 CHVR_DISPOSABLE_TEST=1 显式门禁，避免普通脚本/测试导入意外改动宿主接口；未设置时在任何网络改动前退出，设置后在隔离容器中真实网络回归再次通过。此补充仅影响手工测试入口，不影响 v0.4.82 生产构建产物，将与最终核验记录一并提交且跳过重复发布。

追踪：REQ-001 → UNIT-001/002 → TASK-001/003 → TEST-001/004；REQ-002 → UNIT-001/002/003 → TASK-001/002 → TEST-001/002；REQ-003 → TASK-002/003 → TEST-003/004。

## 六、决策、缺陷与回滚

DEC-001：通过已授权 TUN 回程识别网关访问；不直接放行全部私网或全部已登录公网来源。
CON-001：发布 Skill 的旧预检要求已退役的 probe Docker/arm64 router 项。当前 workflow SHA256 eabdc410d23951404c905fb3476c68cf8612b0683f5fe3a124529b89522c4ca6 与此前核验基线一致；沿用临时适配预检，仅排除此三项旧标记，分支/远端/GitHub 权限/工作区均通过。不修改 Skill 或发布工作流。
DEF-001：桥接过度限制 LAN 同网段，使正常网关访问显示后台不可用；已通过隔离真实 HTTP/HTTPS 网关验证并部署 v0.4.82，用户页面复查待回复。
TEST-001 首次失败来自测试追加路由时文件游标在起始位置，覆盖表头；已修复测试显式 seek 到结尾，完整断言通过；没有修改产品条件来放宽测试。
回滚：若出现管理访问或 httpd 回归，回退此次源修改并正常发布/在线升级；无配置格式/数据迁移；不手动替换生产二进制。
准备门禁：通过。完成门禁：有条件通过，唯一未关闭项为用户已登录原厂页面的刷新验收。代码/回归/正式发布/在线安装/运行哈希/服务及 CPU 验证已闭合，不将页面结果报告为已确认。
