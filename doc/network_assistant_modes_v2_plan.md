# 网络助手规则模式流量引擎规划（V2）

## 1. 目标

流量引擎固定工作在 `rule` 模式。

- 流量引擎支持两种本机接入模式：系统代理模式（`HTTP`、`SOCKS4`、`SOCKS5`）与 `TUN` 模式
- 系统代理模式与 `TUN` 模式互斥，同一时刻仅允许一种模式启用
- 当前生效模式下的入站流量统一经过规则引擎
- 流量引擎支持 `TCP/UDP` 流量处理（UDP 含 DNS、QUIC）
- 规则动作支持：
  - `DIRECT`：直连
  - `REJECT`：拒绝
  - `GROUP(group_id)`：交由代理组选择代理出口
- 代理组由“链路管理模块”统一配置并可选（用于代理出口策略）

---

## 2. 工作模型

### 2.1 入站范围

- 系统代理模式入站：进入本机代理端口的流量（HTTP/SOCKS4/SOCKS5；其中 UDP 由 SOCKS5 UDP ASSOC 承载）。
- TUN 模式入站：由 TUN 网卡接管进入引擎的本机流量（TCP/UDP）。
- 两种模式互斥，运行时仅可激活一种入站模式。
- 无论当前激活哪种模式，均共用同一套规则与动作语义。

### 2.2 决策流程

统一决策函数：

`decide(flow, ruleSet, FINAL, ingress_type) -> DIRECT | REJECT | GROUP(group_id)`

决策优先级：

1. 命中 `direct_whitelist`：强制 `DIRECT`（最高优先级）
2. 按规则顺序匹配：
   - 命中返回对应动作
3. 未命中：
   - 返回 `FINAL`

说明：

- `ingress_type` 取值：`proxy_inbound | tun_inbound`
- 同一实例同一时刻仅有一种 `ingress_type` 生效（由当前接入模式决定）
- 两类入站使用同一规则集，不维护两套策略
- 当返回 `GROUP(group_id)` 时，再由代理组解析最终代理出口

---

## 3. 规则能力

### 3.1 匹配类型

- `cidr`：CIDR 网段匹配（如 `10.0.0.0/8`）
- `ip`：目标 IP 精确匹配（IPv4/IPv6）
- `domain_exact`：域名全匹配（如 `api.example.com`）
- `domain_keyword`：域名关键字匹配（如 `google`）
- `domain_suffix`：域名后缀匹配（如 `.example.com`）
- `domain_prefix`：域名前缀匹配（如 `api-`）

### 3.2 动作类型

- `direct`
- `reject`
- `group`（必须指定 `group_id`）

### 3.3 direct_whitelist 约束

- 在规则引擎中始终优先于普通规则。
- 建议沿用文件：`./data/direct_whitelist.txt`。

### 3.4 FINAL 兜底

- `FINAL` 是规则未命中时的最终动作。
- `FINAL` 可配置为：
  - `DIRECT`
  - `REJECT`
  - `GROUP(group_id)`
- 当 `FINAL=GROUP(group_id)` 时，`group_id` 必须存在于链路管理模块代理组目录。

---

## 4. 代理组与出口来源（链路管理模块）

规则引擎不维护远方出口详情，统一引用链路管理模块提供的“代理组目录”。

- 规则仅保存 `group_id`
- `group_id` 的成员（若干可选代理出口）由链路管理模块维护
- 规则中引用 `group_id`
- 代理组最终出口通过界面选择（样式参照 Clash Meta Rev 的代理组切换面板）
- 若代理组最终选择代理出口，则出口连接参数仍由链路管理模块维护

建议约束：

- 删除或禁用代理组时，引用该 `group_id` 的规则应标红并阻止发布
- 规则发布前校验所有 `group` 动作的 `group_id` 可用
- 代理组当前选择出口变更后应实时生效，并保留最近一次有效选择
- 代理组未手动选择最终出口时，自动回落到“上次选中节点”；若无历史记录则选择“第一个可用节点”

---

## 5. Proxy 出站语义

- 当命中规则/FINAL 返回 `GROUP(group_id)` 且组解析得到 `PROXY(exit_id)` 时，本地流量封装为代理协议流
- 本机接入协议支持：`SOCKS4(TCP)`、`SOCKS5(TCP/UDP ASSOC)`、`HTTP CONNECT/Forward(TCP)`
- 上述协议仅工作在本机侧，供其他应用程序接入流量引擎
- 与远端节点通信不直接使用上述裸代理协议，而是通过管理端到远端节点的隧道进行转发
- UDP 流量来源包含：`SOCKS5 UDP ASSOC` 与 `TUN` 入站 UDP
- 命中 `GROUP(group_id)` 的 UDP 流量通过隧道 UDP 转发通道送达远端
- 在远端使用 `exit_id` 对应出口进行最终外联出站（TCP/UDP，含 QUIC）
- 若解析到的 `exit_id` 不支持 UDP 出站，则 UDP 流量直接丢弃（不回退直连，不切换其他出口）
- 若解析到的 `exit_id` 对应远方出口不可用，则该流量直接丢弃（不回退直连，不切换其他出口）

## 5.1 TUN 模式语义

- 当接入模式为 `TUN` 时，本机 `TCP/UDP` 流量由 TUN 网卡接管。
- 进入 TUN 的流量执行与系统代理模式一致的规则决策：
  - `DIRECT`：本机直接出站
  - `REJECT`：拒绝
  - 命中 `GROUP(group_id)`：由代理组解析为 `PROXY(exit_id)` 后出站
- TUN 与系统代理模式在策略层完全一致，仅入站来源不同。

## 5.2 DNS 与 Fake-IP 语义（TUN 防泄露）

- 流量引擎提供本地 DNS 入口（建议默认：`127.0.0.1:53`）；`TUN` 模式下系统 DNS 指向该入口。
- 本机 DNS 默认工作在 `fake-ip` 模式：为域名分配虚拟地址（建议网段：`198.18.0.0/16`），并维护 `domain <-> fake_ip` 映射表。
- 当连接目标为 `fake_ip` 时，引擎先反查域名后再执行规则匹配，确保 `domain_*` 规则稳定生效。
- 命中 `DIRECT` 且目标为 `fake_ip` 时，执行固定还原流程：`fake_ip -> domain` 反查，再用直连 DNS 上游解析真实 `A/AAAA`，最后按 IP 策略选择真实地址直连。
- `DIRECT` 路径遇到 `fake_ip` 映射缺失时强制 `REJECT` 并记录错误码 `FAKEIP_MAP_MISS`。
- `DIRECT` 路径真实地址解析失败时强制 `REJECT` 并记录错误码 `DIRECT_DNS_FAIL`。
- `DIRECT` 还原失败不自动回退到 `GROUP`。
- `active_mode=tun` 时，强制劫持 `UDP/53`、`TCP/53`（可选 `TCP/853`）到流量引擎本地 DNS。
- `active_mode=proxy` 时，命中 `DIRECT` 的流量可本地解析并直连出站。
- `active_mode=proxy` 时，命中 `GROUP(group_id)` 的流量 DNS 由远方出口解析后再出站。
- `active_mode=proxy` 仍不对 DNS 泄露做强约束（非代理流量不纳入本版本防护范围）。

---

## 6. 配置模型（建议）

```json
{
  "network_assistant": {
    "engine_mode": "rule",
    "inbounds": {
      "active_mode": "proxy",
      "listen": "127.0.0.1",
      "http_port": 7890,
      "socks4_port": 7891,
      "socks5_port": 7892
    },
    "udp": {
      "enabled": true,
      "socks5_udp_assoc": true,
      "udp_idle_timeout_sec": 90
    },
    "dns": {
      "listen": "127.0.0.1:53",
      "enhanced_mode": "fake-ip",
      "fake_ip_range": "198.18.0.0/16",
      "fake_ip_filter": ["*.lan", "*.local", "localhost"],
      "mapping_ttl_sec": 600,
      "direct_dns_upstreams": ["223.5.5.5", "119.29.29.29"],
      "direct_resolve_timeout_ms": 1500,
      "direct_ip_strategy": "happy_eyeballs",
      "direct_on_fakeip_miss": "reject",
      "direct_on_dns_fail": "reject",
      "hijack": ["udp/53", "tcp/53", "tcp/853"],
      "proxy_group_remote_resolve": true,
      "ipv6_protect_in_tun": true
    },
    "FINAL": { "type": "group", "group_id": "g-default" },
    "rule_version": 1,
    "rules": [
      {
        "name": "local-net",
        "match": { "cidr": ["10.0.0.0/8", "192.168.0.0/16"] },
        "action": { "type": "direct" }
      },
      {
        "name": "deny-ads",
        "match": { "domain_keyword": ["ad", "tracker"] },
        "action": { "type": "reject" }
      },
      {
        "name": "corp-api",
        "match": { "domain_prefix": ["api-"], "domain_suffix": [".corp.example.com"] },
        "action": { "type": "group", "group_id": "g-corp" }
      }
    ]
  }
}
```

说明：

- `active_mode` 取值：`proxy | tun`，且互斥
- `active_mode` 为接入模式唯一开关
- `active_mode=proxy` 时启用本地代理端口；`active_mode=tun` 时启用 TUN 接管
- `inbounds.listen` 为本机代理入口监听地址（可配置）
- `udp.enabled=true` 启用 UDP 处理链路
- `udp.socks5_udp_assoc=true` 启用 SOCKS5 UDP ASSOC 入站能力
- `udp.udp_idle_timeout_sec` 为 UDP 会话空闲回收时间
- `dns.listen` 为本地 DNS 监听地址；TUN 模式下系统 DNS 建议固定指向该地址
- `dns.enhanced_mode=fake-ip` 启用本机 fake-ip 解析能力
- `dns.fake_ip_range` 为 fake-ip 地址池，`dns.fake_ip_filter` 为不走 fake-ip 的例外域名
- `dns.mapping_ttl_sec` 控制 `domain <-> fake_ip` 映射保活时间
- `dns.direct_dns_upstreams` 定义 `DIRECT` 还原路径的直连 DNS 上游
- `dns.direct_resolve_timeout_ms` 定义 `DIRECT` 解析超时
- `dns.direct_ip_strategy` 定义 `DIRECT` 的 IPv4/IPv6 选路策略（建议 `happy_eyeballs`）
- `dns.direct_on_fakeip_miss=reject` 与 `dns.direct_on_dns_fail=reject` 固化失败行为且不回退 `GROUP`
- `dns.hijack` 在 `active_mode=tun` 生效，用于接管 DNS 端口流量
- `dns.proxy_group_remote_resolve=true` 表示 proxy 模式下命中 `GROUP` 的流量使用远端解析
- `dns.ipv6_protect_in_tun=true` 用于 TUN 模式下 IPv6 DNS 路径防护
- `active_mode=proxy` 不纳入 DNS 泄露防护验收范围
- `g-default`、`g-corp` 由链路管理模块提供并管理
- 代理组成员示例：
  - `g-corp` -> `exit-corp-a`, `exit-corp-b`（可选代理集合）

---

## 7. 前后端改造范围

### 7.1 后端（manager backend）

- 规则引擎固定 `rule` 模式
- 支持本地 `HTTP/SOCKS4/SOCKS5` 入站与 TUN 入站，并按模式互斥启用
- 支持代理入口监听地址配置（`inbounds.listen`）
- 本版本暂不实现代理入口鉴权
- 支持 UDP 数据面（SOCKS5 UDP ASSOC 入站、TUN UDP 入站、隧道 UDP 转发）
- 实现 UDP 会话跟踪与超时回收（按 `udp_idle_timeout_sec`）
- 提供本地 DNS 服务（fake-ip 模式）并实现 TUN 模式防泄露策略（53/853 劫持、IPv6 防护）
- 实现 proxy 模式 DNS 规则：`DIRECT` 本地解析，`GROUP` 远端解析
- 维护 `domain <-> fake_ip` 映射表（分配、反查、过期回收）
- 实现 `DIRECT + fake-ip` 还原链路：反查域名 -> 直连 DNS 解析真实地址 -> 真实 IP 出站
- 落实 `DIRECT` 失败策略与错误码：`FAKEIP_MAP_MISS`、`DIRECT_DNS_FAIL`（不回退 `GROUP`）
- 维护 `domain -> real_ip` 解析缓存（遵循 TTL）与单连接 IP 粘滞
- 规则集加载、校验、热更新
- 决策执行：`DIRECT / REJECT / GROUP(group_id)`；其中 `GROUP` 再解析为 `PROXY(exit_id)`
- 支持 `FINAL` 兜底动作解析与执行
- 对接链路管理模块的代理组目录读取与发布校验

### 7.2 前端（network assistant tab）

- 接入模式切换：系统代理模式 / TUN 模式（互斥）
- 代理模式下配置监听地址与入口端口（HTTP/SOCKS4/SOCKS5）
- TUN 模式下展示网卡状态与启停
- UDP 能力配置与状态展示（UDP 开关、SOCKS5 UDP ASSOC、UDP 会话状态）
- DNS 配置与状态展示（fake-ip、地址池、例外域名、DIRECT 解析策略、TUN 劫持端口）
- 规则管理：列表、优先级、启停、导入导出
- 匹配器编辑：CIDR/IP/域名（全匹配、关键字、后缀、前缀）
- 动作编辑：直连 / 拒绝 / 代理组（`group_id`）
- `FINAL` 兜底动作编辑：直连 / 拒绝 / 代理组（`group_id`）
- 代理组面板：界面选择每个代理组当前使用的最终出口（样式参照 Clash Meta Rev）
- 规则编辑支持 `group_id` 自动补全与发布前校验
- 若当前组未选择出口，界面自动应用“上次选中节点”，否则使用“第一个可用节点”

### 7.3 链路管理模块

- 提供代理组清单查询接口（含 `group_id`、组成员、可用状态）
- 提供组与出口变更事件，供规则页做引用一致性检查

---

## 8. 验收标准

- 可在系统代理模式与 TUN 模式间切换，且同一时刻仅一种模式生效
- 系统代理模式下，本机 `HTTP/SOCKS4/SOCKS5` 入站可用，且监听地址配置生效
- TUN 模式下，本机流量可由 TUN 接管并按规则处理
- `SOCKS5 UDP ASSOC` 入站可用，UDP 会话可建立并转发
- TUN 入站 UDP 流量可稳定命中规则并执行 `DIRECT/REJECT/GROUP`
- 命中 `GROUP` 的 UDP 流量可通过隧道到远端出口（含 QUIC）
- 远端出口不支持 UDP 时流量被丢弃，并有清晰告警与失败日志
- 本地 DNS 可用；TUN 模式下系统 DNS 可稳定指向流量引擎
- fake-ip 分配与反查可用，`domain_*` 规则在 TUN 模式可稳定命中
- `fake_ip_filter` 命中域名可绕过 fake-ip 并保持可访问
- `DIRECT` 命中且目标为 `fake_ip` 时，可稳定完成 `fake_ip -> domain -> real_ip` 还原并直连
- `fake_ip` 映射缺失时返回 `REJECT`，并记录 `FAKEIP_MAP_MISS`
- `DIRECT` 真实地址解析失败时返回 `REJECT`，并记录 `DIRECT_DNS_FAIL`
- `DIRECT` 还原失败不自动回退 `GROUP`
- `active_mode=tun` 时，`53/853` 端口可按配置被劫持且不泄露到直连路径
- `active_mode=proxy` 时，命中 `GROUP` 的流量可在远端完成 DNS 解析；命中 `DIRECT` 的流量可本地解析
- `active_mode=proxy` 下 DNS 泄露不作为本版本验收失败项
- 规则未命中流量可按 `FINAL` 正确执行：直连 / 拒绝 / 代理组
- `direct_whitelist` 始终优先直连
- 规则中的 `group_id` 均可在链路管理模块中解析
- 代理组未显式选择出口时可正确回落到“上次选中节点或第一个可用节点”
- 出口失效时流量被丢弃，且有清晰告警与失败日志

---

## 9. 风险与约束

- 代理组解析到的出口不可用时，流量按设计直接丢弃（不做自动回退）
- 规则数量增大后，命中顺序与冲突处理复杂度上升；需冲突检测与可视化提示
- 链路管理模块与规则模块配置一致性需要原子校验（避免 `group_id` 或组成员引用悬空）
- TUN 模式需避免流量回环（代理进程自身流量、隧道控制流量需显式排除）
- TUN 模式下部分应用可能使用内置 DoH/DoQ 绕过 `53/853`，需结合进程/域名策略做补充拦截
- 少数应用对 fake-ip 兼容性差（需真实 IP），需通过 `fake_ip_filter` 维护例外清单
- 本版本暂不考虑代理入口鉴权，若监听非回环地址需由部署侧自行控制访问边界
- `DIRECT` 还原失败即拒绝会提升失败可见性，但会增加短时访问失败感知，需配套监控与告警
- UDP 无连接特性导致会话回收和端口复用复杂度高，需精细化超时与并发控制
- UDP/QUIC 对 MTU 更敏感，需做好分片与 PMTU 异常场景处理
