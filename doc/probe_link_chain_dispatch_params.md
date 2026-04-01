# 探针链路下发参数说明（单探针 / 双探针 / 多探针）

本文说明主控向探针下发 `chain_link_control`（`action=apply`）时，不同拓扑下关键参数的取值差异。

---

## 1. 下发参数字段（控制器 -> 探针）

控制器下发结构体定义见：
- `probeChainLinkControlCommand`：[`probe_controller/internal/core/probe_command.go`](../probe_controller/internal/core/probe_command.go#L91)

探针接收结构体定义见：
- `probeControlMessage`：[`probe_node/main.go`](../probe_node/main.go#L95)

链路应用逻辑见：
- 组装并下发参数：[`applyProbeLinkChainRecord()`](../probe_controller/internal/core/probe_link_chain_dispatch.go#L12)
- 探针侧构建运行配置：[`buildProbeChainRuntimeConfigFromControl()`](../probe_node/link_chain_runtime.go#L514)

本次重点字段：
- `role`
- `listen_host` / `listen_port`
- `link_layer`
- `next_auth_mode`
- `next_host` / `next_port`
- `next_link_layer` / `next_dial_mode`
- `prev_host` / `prev_port`
- `prev_link_layer` / `prev_dial_mode`
- `require_user_auth`

---

## 2. 通用赋值规则（与拓扑无关）

来源：[`applyProbeLinkChainRecord()`](../probe_controller/internal/core/probe_link_chain_dispatch.go#L12)

- `listen_host`：来自节点 hop 配置；无则使用链级默认。
- `listen_port`：优先节点 hop 的 `listen_port`，否则回退链级 `listen_port`。
- `link_layer`：来自节点 hop 的 `link_layer`。
- `port_forwards`：整条链配置原样下发（是否实际监听由节点角色决定）。
- `controller_base_url`：下发主控地址。
- `require_user_auth`：仅路由第一个节点为 `true`（`i == 0`）。

---

## 3. 单探针（路由长度=1）

角色判定：[`len(route)==1 -> role=entry_exit`](../probe_controller/internal/core/probe_link_chain_dispatch.go#L22)

| 字段 | 下发值 |
|---|---|
| `role` | `entry_exit` |
| `next_auth_mode` | `proxy`（默认） |
| `next_host` / `next_port` | `egress_host` / `egress_port` |
| `next_dial_mode` | `none` |
| `next_link_layer` | 空字符串 |
| `prev_host` / `prev_port` | 空 / `0` |
| `prev_dial_mode` | `none` |
| `prev_link_layer` | 空字符串 |
| `require_user_auth` | `true`（因为 `i==0`） |

说明：
- 单探针不会出现“探针到探针”的下一跳中继连接，直接以 `proxy` 模式访问出口目标。

---

## 4. 双探针（路由长度=2）

路由形态：`entry -> exit`

来源：
- 下一跳计算：[`i < len(route)-1` 分支](../probe_controller/internal/core/probe_link_chain_dispatch.go#L35)
- 上一跳计算：[`i > 0` 分支](../probe_controller/internal/core/probe_link_chain_dispatch.go#L59)

### 4.1 第一个节点（Entry）

| 字段 | 下发值 |
|---|---|
| `role` | `entry` |
| `next_auth_mode` | `secret` |
| `next_host` / `next_port` | 下一个节点（Exit）的可拨号地址 / `external_port` |
| `next_link_layer` | 下一个节点（Exit）的 `link_layer` |
| `next_dial_mode` | 当前节点（Entry）hop 的 `dial_mode` |
| `prev_*` | 空 / `0` / `none` |
| `require_user_auth` | `true` |

### 4.2 第二个节点（Exit）

| 字段 | 下发值 |
|---|---|
| `role` | `exit` |
| `next_auth_mode` | `proxy` |
| `next_host` / `next_port` | `egress_host` / `egress_port` |
| `next_dial_mode` | `none` |
| `next_link_layer` | 空字符串 |
| `prev_host` / `prev_port` | 上一个节点（Entry）的可拨号地址 / `external_port` |
| `prev_link_layer` | 上一个节点（Entry）的 `link_layer` |
| `prev_dial_mode` | 上一个节点（Entry）的 `dial_mode` |
| `require_user_auth` | `false` |

---

## 5. 多探针（路由长度>=3）

路由形态：`entry -> relay(0..n) -> exit`

### 5.1 Entry 节点

与双探针 Entry 相同：
- `next_auth_mode=secret`
- `next_*` 指向第一个 Relay（若无 Relay 则指向 Exit）
- `prev_*` 为空
- `require_user_auth=true`

### 5.2 Relay 节点（中间每一跳）

| 字段 | 下发值 |
|---|---|
| `role` | `relay` |
| `next_auth_mode` | `secret` |
| `next_host` / `next_port` | 下一个节点的可拨号地址 / `external_port` |
| `next_link_layer` | 下一个节点 `link_layer` |
| `next_dial_mode` | 当前 Relay 自身 hop 的 `dial_mode` |
| `prev_host` / `prev_port` | 上一个节点可拨号地址 / `external_port` |
| `prev_link_layer` | 上一个节点 `link_layer` |
| `prev_dial_mode` | 上一个节点 `dial_mode` |
| `require_user_auth` | `false` |

### 5.3 Exit 节点

与双探针 Exit 相同：
- `next_auth_mode=proxy`
- `next_host/next_port=egress_host/egress_port`
- `prev_*` 来自最后一个 Relay（无 Relay 时来自 Entry）
- `require_user_auth=false`

---

## 6. 关键约束（常见失败点）

- 非 `proxy` 场景必须有 `next_host + next_port`，否则探针侧拒绝应用：[`buildProbeChainRuntimeConfigFromControl()`](../probe_node/link_chain_runtime.go#L547)
- `prev_dial_mode=reverse` 时必须有 `prev_host + prev_port`：[`buildProbeChainRuntimeConfigFromControl()`](../probe_node/link_chain_runtime.go#L560)
- `next_auth_mode=secret` 时 `link_secret` 必填：[`buildProbeChainRuntimeConfigFromControl()`](../probe_node/link_chain_runtime.go#L599)
- 多跳时下一跳端口来自“下一节点 external_port”，若缺失会在主控侧报错并跳过该节点下发：[`applyProbeLinkChainRecord()`](../probe_controller/internal/core/probe_link_chain_dispatch.go#L44)

---

## 7. 口径提醒

- `egress_port` 是 **出口节点向目标发起连接** 的端口，不是出口节点本机监听端口。
- 探针本机监听应看 `listen_port`（和对应协议层）。
