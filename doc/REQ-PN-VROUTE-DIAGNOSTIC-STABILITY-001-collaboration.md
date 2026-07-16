# 协作文档

- 适用规则: AI协作规则
- 后续工作传递声明: 本文档必须传递给后续阶段与后续角色。
- 需求编号: REQ-PN-VROUTE-DIAGNOSTIC-STABILITY-001
- 需求前缀: REQ-PN-VROUTE-DIAGNOSTIC-STABILITY-001
- 当前阶段: 最终门禁
- 最近更新角色: Architect
- 最近更新时间: 2026-07-16
- 工作依据文档: `doc/ai-coding-collaboration.md`、本机 #9 控制台状态、经虚拟路由调试帧返回的远端 #17 日志
- 状态: 已完成

## 第1章 Architect章节
- 章节责任角色: Architect
- 状态: 已完成

### 1.1 需求定义
- 状态: 已完成

#### 1.1.1 需求目标
- 修复 Fake-IP 校验固定超时短于实际链路控制 RTT，导致成功出口被误报为校验超时的问题。
- 限制诊断校验并发，并对连续失败做退避，避免诊断流量反过来放大拥塞。
- 聚合 Fake-IP 校验、非直连路径守护和活跃 carrier 的 ping 延迟日志，保留状态变化证据且避免刷屏。

#### 1.1.2 需求范围
- 仅修改 probe node 虚拟路由诊断与日志行为。
- 增加自适应校验超时、并发上限、失败退避和日志节流。
- 增加针对上述行为的 Go 单元测试。

#### 1.1.3 非范围
- 不修改虚拟路由业务报文转发语义、Fake-IP 映射语义或拓扑配置。
- 不修改本轮尚未部署的 HTTP/SOCKS5 代理功能。
- 不部署、不重启、不替换 `C:\Tools\probe_node` 运行库。

#### 1.1.4 验收标准
- 已知 RTT 为 4.7 秒左右时，Fake-IP 校验等待时间大于该 RTT，且不超过硬上限。
- 同时运行的 Fake-IP 校验有固定上限。
- 同一目标连续失败后的重试间隔指数增长并有上限，成功后恢复基础间隔。
- 同一路径同类日志在节流周期内只输出一次，后续输出包含聚合的 suppressed 数量。
- `go test -count=1 ./...` 与目标 race 测试通过。

#### 1.1.5 风险
- 延长诊断等待时间会让单次诊断结果更晚出现，但不会阻塞业务转发。
- 日志节流会隐藏逐条重复信息，因此必须输出 suppressed 聚合计数。

#### 1.1.6 遗留事项
- 真实双机效果需由用户升级后观察新版本日志确认。

#### 1.1.7 结论
- 需求边界明确，可进入 Code 实现。

### 1.2 总体架构
- 状态: 已完成

#### 1.2.1 架构目标
- 将 Fake-IP 校验保持为低成本旁路诊断，不允许其成为链路负载放大器。

#### 1.2.2 总体设计
- 根据已知路径 RTT 计算校验超时，采用最小值、RTT 倍数与最大值夹取。
- 在校验状态内维护全局活动数和目标连续失败数；调度前执行并发与退避判断。
- 提供包内通用日志节流器，按稳定键聚合重复日志。

#### 1.2.3 关键模块
| 模块编号 | 模块名称 | 职责 | 输入 | 输出 |
|---|---|---|---|---|
| U-01 | Fake-IP 校验调度 | 自适应超时、并发限制、失败退避 | 报文、路径、校验状态 | 校验任务或跳过 |
| U-02 | 日志节流 | 聚合同一路径重复诊断 | 稳定键、周期 | 是否打印、抑制数量 |
| U-03 | 路径诊断日志 | 接入守护和 ping 日志 | 路径或 route 状态 | 低频聚合日志 |

#### 1.2.4 关键接口
| 接口编号 | 接口名称 | 调用方 | 提供方 | 说明 |
|---|---|---|---|---|
| IF-01 | `probeVirtualRouterFakeIPVerifyTimeoutForPath` | Fake-IP 校验 | 虚拟路由状态 | 按路径 RTT 返回有界超时 |
| IF-02 | `takeProbeVirtualRouterLogThrottle` | 诊断日志点 | 日志节流器 | 返回打印许可与 suppressed 数量 |

#### 1.2.5 关键约束
- 诊断任务必须异步，业务报文发送不等待校验结果。
- 节流键不得包含 request_id 等每次变化字段。

#### 1.2.6 风险
- RTT 状态缺失时使用保守默认值，可能无法完全覆盖极端高延迟链路。

#### 1.2.7 结论
- 设计可测试且不改变数据面语义。

### 1.3 单元设计
- 状态: 已完成

#### 1.3.1 单元清单
| 单元编号 | 单元名称 | 所属模块 | 职责 | 输入 | 输出 |
|---|---|---|---|---|---|
| U-01 | 校验调度状态 | Fake-IP 校验调度 | 控制活动数、退避和结果状态 | key、路径、结果 | 是否调度 |
| U-02 | 日志节流状态 | 日志节流 | 汇总周期内重复项 | key、时间 | allow、suppressed |
| U-03 | 日志接入点 | 路径诊断日志 | 生成稳定键并打印聚合结果 | path、route、error | 日志 |

#### 1.3.2 单元设计
##### U-01
- 单元名称: 校验调度状态
- 职责: 诊断并发不超过上限；失败后指数退避；根据 RTT 计算等待时间。
- 输入: Fake-IP 目标键、路径 RTT、上次结果。
- 输出: 调度许可和超时时间。
- 处理规则: 基础超时 5 秒，按 `2*RTT+2秒` 放大并夹取到 15 秒；基础冷却 15 秒，失败后指数退避到 5 分钟。
- 异常规则: RTT 不可用时使用基础超时；达到并发上限时直接跳过本次旁路诊断。

##### U-02
- 单元名称: 日志节流状态
- 职责: 周期内抑制同键重复日志并累计数量。
- 输入: 稳定键、周期、当前时间。
- 输出: 是否允许打印、此前抑制数量。
- 处理规则: 首次立即打印；周期内累计；周期后下一条打印聚合计数。
- 异常规则: 空键不节流，非正周期使用调用方默认周期。

#### 1.3.3 风险
- 全局日志键集合需按过期时间清理，避免长期增长。

#### 1.3.4 结论
- 单元职责和异常边界完整。

### 1.4 Code任务执行包
- 状态: 已完成

#### 1.4.1 执行边界
- 允许修改: `doc/REQ-PN-VROUTE-DIAGNOSTIC-STABILITY-001-collaboration.md`、`probe_node/probe_virtual_router_fake_ip_verify.go`、`probe_node/probe_virtual_router.go`、`probe_node/probe_virtual_router_log_throttle.go`、`probe_node/probe_virtual_router_test.go`。
- 禁止修改: `C:\Tools\probe_node`、控制器配置、代理功能实现、其他运行库。

#### 1.4.2 任务清单
| 任务编号 | 需求编号 | 单元编号 | 文件范围 | 操作类型 | 验收标准 |
|---|---|---|---|---|---|
| T-01 | REQ-PN-VROUTE-DIAGNOSTIC-STABILITY-001 | U-01 | `probe_node/probe_virtual_router_fake_ip_verify.go` | 修改 | 自适应超时、并发上限、失败退避生效 |
| T-02 | REQ-PN-VROUTE-DIAGNOSTIC-STABILITY-001 | U-02 | `probe_node/probe_virtual_router_log_throttle.go` | 新增 | 重复日志按稳定键聚合且状态过期清理 |
| T-03 | REQ-PN-VROUTE-DIAGNOSTIC-STABILITY-001 | U-03 | `probe_node/probe_virtual_router.go` | 修改 | guardian 与活跃 carrier ping 日志受控 |
| T-04 | REQ-PN-VROUTE-DIAGNOSTIC-STABILITY-001 | U-01,U-02,U-03 | `probe_node/probe_virtual_router_test.go` | 修改 | 单测覆盖超时、退避、并发和节流 |
| T-05 | REQ-PN-VROUTE-DIAGNOSTIC-STABILITY-001 | U-01,U-02,U-03 | 本协作文档 | 修改 | 回填 Code 证据与最终门禁 |

#### 1.4.3 源码修改规则
- Go 与 Markdown 文件可直接编辑；本次不涉及 C/C++ 文件。
- 修改保持局部，不改变业务帧协议与转发路径。

#### 1.4.4 交付物
- 源码、单元测试、测试结果和本协作文档。

#### 1.4.5 门禁输入
- 现场日志统计、#9 状态、#17 调试日志、目标测试结果、`git diff --check`。

#### 1.4.6 结论
- Code 任务已放行。

### 1.5 Architect需求跟踪矩阵
- 状态: 已完成

| 需求编号 | 需求描述 | 架构章节 | 单元设计章节 | Code任务章节 | 状态 | 备注 |
|---|---|---|---|---|---|---|
| REQ-PN-VROUTE-DIAGNOSTIC-STABILITY-001 | 稳定虚拟路由诊断并减少误报刷屏 | 1.2 | 1.3 | T-01 至 T-05 | 已完成 | Code 证据和测试已回填 |

### 1.6 Architect关键接口跟踪矩阵
- 状态: 已完成

| 接口编号 | 需求编号 | 接口名称 | 调用方 | 提供方 | 输入 | 输出 | 状态 | 备注 |
|---|---|---|---|---|---|---|---|---|
| IF-01 | REQ-PN-VROUTE-DIAGNOSTIC-STABILITY-001 | 自适应校验超时 | 校验调度 | VRoute RTT 状态 | path | duration | 已放行 | 有上下限 |
| IF-02 | REQ-PN-VROUTE-DIAGNOSTIC-STABILITY-001 | 日志节流 | 日志调用点 | 节流器 | key、period、now | allow、suppressed | 已放行 | 稳定键 |

### 1.7 门禁裁判
- 状态: 已完成

#### 1.7.1 门禁输入
| 文档 | 路径 | 状态 |
|---|---|---|
| 协作文档 | `doc/REQ-PN-VROUTE-DIAGNOSTIC-STABILITY-001-collaboration.md` | 已创建 |

#### 1.7.2 裁判检查
| 检查项 | 结果 | 证据 | 备注 |
|---|---|---|---|
| 协作文档与必需章节存在 | 通过 | 本文档 | 无 |
| 需求、接口、任务编号一致 | 通过 | 1.4 至 1.6 | 无 |
| 验收标准可测试 | 通过 | 1.1.4、1.4.2 | 无 |
| 修改文件范围明确 | 通过 | 1.4.1 | 无 |
| Code证据完整 | 通过 | 第2章 | 修改、测试和回滚证据完整 |
| 遗留风险可接受 | 通过 | 1.1.5、1.1.6、2.5.8 | 用户升级后实机确认，不阻塞源码交付 |

#### 1.7.3 冲突记录
| 冲突编号 | 冲突条款 | 最终采用条款 | 裁决人 | 裁决结论 |
|---|---|---|---|---|
| 无 | 无 | 无 | Architect | 无 |

#### 1.7.4 裁判结论
- 结论: 通过
- 放行阻塞: 放行
- 条件: 已关闭；Code 在 1.4.1 文件范围内完成实现和证据回填，未操作当前运行库。
- 责任方: Code
- 关闭要求: 已满足，目标测试、全量测试与全量 race 测试通过。
- 整改要求: 测试失败必须记录到第2.4节。

#### 1.7.5 结论
- T-01 至 T-05 已完成，最终门禁通过。

## 第2章 Code章节
- 章节责任角色: Code
- 状态: 已完成

### 2.1 Code需求跟踪矩阵
- 状态: 已完成

| 需求编号 | 任务编号 | 实现文件 | 实现状态 | 自测状态 | 证据 | 备注 |
|---|---|---|---|---|---|---|
| REQ-PN-VROUTE-DIAGNOSTIC-STABILITY-001 | T-01 至 T-05 | `probe_virtual_router_fake_ip_verify.go`、`probe_virtual_router_log_throttle.go`、`probe_virtual_router.go`、测试与本文档 | 已完成 | 通过 | 全量普通测试与全量 race 测试通过 | 未部署运行库 |

### 2.2 Code关键接口跟踪矩阵
- 状态: 已完成

| 接口编号 | 需求编号 | 实现位置 | 状态 | 证据 | 备注 |
|---|---|---|---|---|---|
| IF-01 | REQ-PN-VROUTE-DIAGNOSTIC-STABILITY-001 | `probeVirtualRouterFakeIPVerifyTimeoutForPath` | 已完成 | 4.7 秒 RTT 单测得到 11.4 秒等待 | 5 至 15 秒有界 |
| IF-02 | REQ-PN-VROUTE-DIAGNOSTIC-STABILITY-001 | `takeProbeVirtualRouterLogThrottle` | 已完成 | suppressed 聚合单测通过 | 状态自动过期 |

### 2.3 Code测试项跟踪矩阵
- 状态: 已完成

| 测试项编号 | 需求编号 | 任务编号 | 测试内容 | 测试方式 | 状态 | 证据 | 备注 |
|---|---|---|---|---|---|---|---|
| TC-01 | REQ-PN-VROUTE-DIAGNOSTIC-STABILITY-001 | T-01 | 自适应超时、退避、并发和成功复位 | Go 单测 | 已完成 | 目标测试通过 | 无 |
| TC-02 | REQ-PN-VROUTE-DIAGNOSTIC-STABILITY-001 | T-02,T-03 | 日志聚合 | Go 单测 | 已完成 | `TestProbeVirtualRouterLogThrottleAggregatesSuppressedEntries` 通过 | 无 |
| TC-03 | REQ-PN-VROUTE-DIAGNOSTIC-STABILITY-001 | T-01 至 T-04 | 回归与竞态 | Go test/race | 已完成 | `go test -count=1 ./...`、`go test -race -count=1 ./...` 通过 | race 临时使用 MSYS64 GCC |

### 2.4 Code缺陷跟踪矩阵
- 状态: 已完成

| 缺陷编号 | 需求编号 | 测试项编号 | 缺陷描述 | 严重级别 | 状态 | 修复证据 | 备注 |
|---|---|---|---|---|---|---|---|
| DEF-01 | REQ-PN-VROUTE-DIAGNOSTIC-STABILITY-001 | TC-01 | 固定 3 秒校验超时短于现场约 4.7 秒控制 RTT | 高 | 已完成 | 自适应超时测试通过 | 现场误报主因已修复 |
| DEF-02 | REQ-PN-VROUTE-DIAGNOSTIC-STABILITY-001 | TC-01,TC-02 | 每目标 5 秒重试且无全局并发和日志聚合 | 中 | 已完成 | 退避、并发、节流测试通过 | 负载和日志放大已限制 |

### 2.5 Code执行证据
- 状态: 已完成

#### 2.5.1 修改接口
- 新增 `probeVirtualRouterFakeIPVerifyTimeoutForPath`、`probeVirtualRouterFakeIPVerifyCooldownForFailures`、`completeProbeVirtualRouterFakeIPVerifySchedule` 和 `takeProbeVirtualRouterLogThrottle`。

#### 2.5.2 配置文件
- 无配置格式变更。

#### 2.5.3 执行报告
- Fake-IP 校验超时由固定 3 秒改为 `2*RTT+2秒`，夹取到 5 至 15 秒。
- 校验并发上限为 4；基础冷却 15 秒，连续失败指数退避至 5 分钟，成功后复位。
- Fake-IP 校验按路径、原因和结果聚合 30 秒日志；guardian 与活跃 carrier ping 延迟按路径或 route 聚合 5 分钟日志。
- 被节流日志通过 `suppressed` 保留数量，不再逐条占用日志和最近报文环。

#### 2.5.4 影响文件
- `probe_node/probe_virtual_router_fake_ip_verify.go`
- `probe_node/probe_virtual_router_log_throttle.go`
- `probe_node/probe_virtual_router.go`
- `probe_node/probe_virtual_router_test.go`
- `doc/REQ-PN-VROUTE-DIAGNOSTIC-STABILITY-001-collaboration.md`

#### 2.5.5 测试命令
- `go test -count=1 -run "TestProbeVirtualRouterFakeIPVerify|TestProbeVirtualRouterLogThrottle|TestProbeVirtualRouter.*PingError|TestProbeVirtualRouterFrameLinkRXCapacity" .`
- `go test -count=1 ./...`
- 临时设置 `CGO_ENABLED=1` 与进程 PATH 后执行 `go test -race -count=1 ./...`；GCC 为 `C:\msys64\ucrt64\bin\gcc.exe`。
- `git diff --check`

#### 2.5.6 自测结果
- 目标测试通过。
- 全量普通测试通过: 主包和 `mobilecore`。
- 全量 race 测试通过: 主包和 `mobilecore`。
- `git diff --check` 通过。

#### 2.5.7 未执行测试原因
- 按用户要求未升级、未重启当前 `C:\Tools\probe_node`，因此未执行新版本双机运行验证。

#### 2.5.8 遗留风险
- 用户升级前，当前 `v0.3.258` 运行实例仍会继续输出旧行为日志。

#### 2.5.9 回滚方案
- 回退本需求对应源码、测试和协作文档变更；无运行时配置迁移。

### 2.6 Code任务反馈
- 状态: 已完成
- 无任务缺失、范围缺失、接口缺失、验收阻塞或规则冲突。
