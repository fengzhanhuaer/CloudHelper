# UDP 三端复用 QUIC TTL 详细实施计划

## 1. 目标与边界

- 在 manager controller probe 三端统一 `association_v2` 语义
- 引入分档 TTL 策略，重点提升 QUIC 会话稳定性并降低 UDP 端口冲突
- 保持现有行为兼容，支持开关灰度和快速回滚
- 本计划仅定义实施路径与验收标准，不包含工时估算

---

## 2. 当前状态快照

### 2.1 已完成的基础改动

- manager 已扩展 `association_v2` 字段骨架
- manager TUN UDP relay 已增加 `nat_mode ttl_profile idle_timeout gc_interval`
- controller 已引入动态 TTL 策略框架与字段承载
- manager controller probe 全量测试曾通过一次

### 2.2 待收敛问题

- probe 侧 `association_v2` 扩展和 UDP pool 字段需最终对齐
- probe debug 与 controller debug 输出字段需完全一致
- 三端测试需要在最终代码形态下重新执行并归档

---

## 3. QUIC TTL 档位细化规则

### 3.1 档位定义

| 档位 | idle_timeout_ms | gc_interval_ms | 适用流量 |
|---|---:|---:|---|
| profile_dns_fast | 30000 | 10000 | DNS 短查询 |
| profile_udp_default | 90000 | 15000 | 普通 UDP |
| profile_quic_warm | 180000 | 30000 | QUIC 新建会话 |
| profile_quic_stable | 420000 | 60000 | QUIC 稳定会话 |
| profile_quic_long | 900000 | 120000 | QUIC 长稳态会话 |

### 3.2 晋升与降级

- 初始 QUIC 命中后进入 `profile_quic_warm`
- 连续活跃且无重建抖动时晋升 `profile_quic_stable`
- 持续稳定后晋升 `profile_quic_long`
- 失败重试频繁或长空闲时逐级降档，最低回落 `profile_udp_default`

### 3.3 三端统一约束

- `idle_timeout_ms` 下限 `30000`，上限 `900000`
- `gc_interval_ms` 下限 `10000`，上限 `120000`
- `gc_interval_ms` 始终满足 `<= idle_timeout_ms / 2`
- manager 决策优先，下发缺失时 controller probe 统一默认 `profile_udp_default`

---

## 4. 分阶段实施清单

## 阶段 A manager 收敛

### A1 字段与策略函数

- 校验并完善以下文件内字段含义与赋值一致性
  - [probe_manager/backend/network_assistant_mux.go](probe_manager/backend/network_assistant_mux.go)
  - [probe_manager/backend/network_assistant_tun_udp.go](probe_manager/backend/network_assistant_tun_udp.go)
- 确认 `association_v2` 输出字段包含
  - `transport ip_family nat_mode ttl_profile idle_timeout_ms gc_interval_ms created_at_unix_ms`

### A2 本地 TTL 使用策略

- relay idle 回收使用 `startIdleGCWithInterval idle gc`
- QUIC 端口默认命中 `profile_quic_warm`
- DNS 53 默认命中 `profile_dns_fast`
- 其他流量默认 `profile_udp_default`

### A3 manager debug 对齐

- 扩展以下输出字段
  - `nat_mode ttl_profile idle_timeout_ms gc_interval_ms`
- 目标文件
  - [probe_manager/backend/ai_debug_udp_assoc.go](probe_manager/backend/ai_debug_udp_assoc.go)

验收点

- manager debug 能看到新增字段且值非空
- 本地 UDP relay 空闲回收遵循分档 TTL

---

## 阶段 B controller 对齐

### B1 结构体对齐

- 在 controller `association_v2` 结构加入同名字段
- 目标文件
  - [probe_controller/internal/core/ws_tunnel.go](probe_controller/internal/core/ws_tunnel.go)

### B2 UDP pool 动态 TTL

- 在 pool 侧实现策略解析与钳制
- association 对象存储
  - `natMode ttlProfile idleTimeout gcInterval createdAtUnixMS`
- collectIdle 使用对象级 `idleTimeout`
- 目标文件
  - [probe_controller/internal/core/ws_tunnel_udp_assoc.go](probe_controller/internal/core/ws_tunnel_udp_assoc.go)

### B3 controller debug 字段

- 补充输出
  - `nat_mode ttl_profile idle_timeout_ms gc_interval_ms created_at_unix_ms`
- 目标文件
  - [probe_controller/internal/core/ws_tunnel_udp_debug.go](probe_controller/internal/core/ws_tunnel_udp_debug.go)

验收点

- controller debug 字段与 manager 命名一致
- controller 回收行为符合下发 TTL

---

## 阶段 C probe 对齐

### C1 结构体对齐

- 扩展 probe `association_v2` 字段
- 目标文件
  - [probe_node/link_chain_runtime.go](probe_node/link_chain_runtime.go)

### C2 UDP association pool 动态 TTL

- 新增策略常量与策略解析函数
- association 对象新增
  - `natMode ttlProfile idleTimeout gcInterval createdAtUnixMS`
- collectIdle 使用对象级 `idleTimeout`
- 目标文件
  - [probe_node/link_chain_udp_assoc.go](probe_node/link_chain_udp_assoc.go)

### C3 probe debug 字段

- 补充输出
  - `nat_mode ttl_profile idle_timeout_ms gc_interval_ms created_at_unix_ms`
- 目标文件
  - [probe_node/udp_assoc_debug.go](probe_node/udp_assoc_debug.go)

验收点

- probe debug 字段与 manager controller 一致
- probe 侧 UDP association 回收按下发 TTL 进行

---

## 阶段 D 测试与回归

### D1 单元测试扩展

- manager
  - [probe_manager/backend/network_assistant_tun_udp_test.go](probe_manager/backend/network_assistant_tun_udp_test.go)
- controller
  - [probe_controller/internal/core/ws_tunnel_udp_assoc_test.go](probe_controller/internal/core/ws_tunnel_udp_assoc_test.go)
- probe
  - [probe_node/link_chain_udp_assoc_test.go](probe_node/link_chain_udp_assoc_test.go)

覆盖点

- TTL 钳制与默认回退
- GC 间隔约束
- 结构字段透传

### D2 联调验证

- manager debug controller debug probe debug 同一 flow 对账
- QUIC 场景验证
  - 新建
  - 空闲后恢复
  - 端口冲突后的稳定性

### D3 命令回归

- manager
  - `go test ./backend`
- controller
  - `go test ./internal/core`
- probe
  - `go test ./...`

---

## 5. 灰度与回滚

### 5.1 开关顺序

1. 仅开启观测字段
2. 开启 manager TTL 下发
3. 开启 controller 动态 TTL
4. 开启 probe 动态 TTL
5. 开启 QUIC 长档位晋升

### 5.2 回滚顺序

- 按启用逆序关闭
- 保留观测字段，便于故障复盘
- 必要时回退到固定 90s 旧行为

---

## 6. 验收门槛

- 三端 debug 字段完全对齐
- QUIC 流量在 warm stable long 档位可观测
- UDP 冲突事件率下降
- 无新增编译错误，无关键测试回归

---

## 7. 执行检查清单

- [ ] manager 结构字段与下发逻辑完成
- [ ] controller 结构字段 动态 TTL debug 完成
- [ ] probe 结构字段 动态 TTL debug 完成
- [ ] 三端测试通过并记录结果
- [ ] 灰度开关文档更新
