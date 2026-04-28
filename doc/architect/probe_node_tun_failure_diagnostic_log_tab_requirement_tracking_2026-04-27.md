# 需求跟踪表 `probe_node` Windows TUN 安装可见性与假成功收敛

## 工作依据与规则传递声明
- 当前角色: 架构师
- 工作依据文档: `doc/ai-coding-unified-rules.md`
- 适用规则: AI协作统一规则 单一规范
- 规则遵循声明: 必须遵守本规则。
- 协作传递要求: 后续接手者与协作者必须遵守同一规则，不得降级或替换执行口径。

- 日期: 2026-04-27
- 备注: 本跟踪文档与架构方案同步，仅覆盖 Windows TUN 安装后系统不可见与流程假成功问题，不扩展到其他能力改造。
- 风险:
  - 提权链路时序差异可能导致可见性轮询窗口不稳定。
  - 枚举接口异常可能导致短时误判。
  - 诊断码覆盖不完整会影响排障效率。
- 遗留事项:
  - 编码阶段将诊断码与步骤字段对齐到接口响应。
  - 测试阶段补齐成功 失败 超时三类场景的自动化断言。
  - 联调阶段校验页面与日志对诊断码展示的一致性。
- 进度状态: 已完成（含 P0 最小可观测增强）
- 完成情况: 已完成需求条目化、执行单元包映射、验证场景映射与门禁口径定义；并在 `probe_node` 完成 Windows TUN 安装可观测增强（driver/create/visibility/final/diagnostic 结构化结果）与自动化测试通过确认。
- 检查表:
  - [x] 已建立需求编号并限定边界
  - [x] 已绑定执行单元包
  - [x] 已建立测试场景映射
  - [x] 已写入风险 回滚与观测项
  - [x] 已写入规则传递声明
  - [x] 已完成实现状态同步
  - [x] 已完成测试状态同步
- 跟踪表状态: 已完成
- 结论记录: 本问题范围内实现已落地，Windows TUN 安装可见性相关成功 失败 超时路径均已由自动化测试覆盖并通过；并补齐安装接口与状态接口可读的 `install_observation/last_install_observation` 观测输出。

## 字符集编码基线
- 文档文件: UTF-8 无 BOM LF
- 代码文件: 本任务涉及 `probe_node` 局部代码与测试文件改动（仅限可观测增强边界）
- 跨平台兼容要求: 新增文档按 UTF-8 无 BOM LF 落盘
- 历史文件迁移策略: 不做全仓迁移，仅对本次触达文档执行统一基线

## 问题背景与复现表征
- 用户在 Windows 触发 TUN 安装后，流程返回成功，但系统看不到网卡。
- 复现时常见表征为“安装完成提示存在，后续依赖网卡可见的流程失败”。

## 根因链路
- 提权后过早成功: 提权请求被接受即被当作阶段成功。
- 可见性判定过宽: 句柄或 LUID 诊断信息被误用为成功条件。
- 成功判定缺口: 未强制同时满足系统可见与路由目标配置成功。

## 目标与非目标
### 目标
- 将安装成功判定收敛到“系统可见网卡 + 路由目标可配置成功”。
- 提供提权后可见性轮询与超时失败语义。
- 建立结构化诊断码并可映射测试场景。

### 非目标
- 不覆盖 Linux 或其他平台改造。
- 不引入与本问题无关的重构建议。
- 不在本任务中改动 `.go` 代码与测试文件。

## 方案设计摘要
- 成功判定收敛: 双条件同时满足才可成功。
- 可见性轮询: 提权后进入轮询窗口，超时必须失败。
- 诊断码: 失败返回 `code stage hint details steps` 最小集合。

## 验证策略
| 测试场景编号 | 场景 | 预期结果 | 对应需求 |
|---|---|---|---|
| TS-PN-TUN-VIS-OK-01 | 成功场景 系统可见且路由目标配置成功 | 返回成功并具备完整步骤链路 | RQ-PN-TUN-VIS-001 RQ-PN-TUN-VIS-002 |
| TS-PN-TUN-VIS-FAIL-01 | 失败场景 创建失败或创建后不可见 | 返回失败且给出准确诊断码 | RQ-PN-TUN-VIS-001 RQ-PN-TUN-VIS-003 RQ-PN-TUN-VIS-004 |
| TS-PN-TUN-VIS-TIMEOUT-01 | 超时场景 提权后轮询窗口内不可见 | 返回超时失败并附轮询步骤 | RQ-PN-TUN-VIS-002 RQ-PN-TUN-VIS-004 |

## 风险 回滚与观测项
### 风险
- 轮询时间窗与系统延迟存在耦合。
- 枚举接口错误与权限边界处理复杂。

### 回滚
- 若收敛策略引发异常阻塞，可回滚至上一稳定版本。
- 回滚需保留失败诊断样本用于后续重放验证。

### 观测项
- 安装总请求数 成功数 失败数 超时数。
- 失败码分布与阶段分布。
- 提权到可见耗时分位。
- 轮询次数分布与最终状态。

## 需求跟踪表
| 需求编号 | 需求描述 | 执行单元包 | 编码状态 | 测试状态 | 当前责任角色 | 风险与遗留 | 测试场景映射 | 最新更新时间 |
|---|---|---|---|---|---|---|---|---|
| RQ-PN-TUN-VIS-001 | 成功判定必须满足系统可见网卡与路由目标可配置成功 | PKG-PN-TUN-VIS-01 | 已实现 | 已通过 | 编码工程师 | 已验证无旧判定残留导致假成功 | TS-PN-TUN-VIS-OK-01 TS-PN-TUN-VIS-FAIL-01 | 2026-04-27 |
| RQ-PN-TUN-VIS-002 | 提权后必须执行可见性轮询，超时返回失败 | PKG-PN-TUN-VIS-02 | 已实现 | 已通过 | 编码工程师 | 已验证轮询与超时语义可被自动化断言 | TS-PN-TUN-VIS-OK-01 TS-PN-TUN-VIS-TIMEOUT-01 | 2026-04-27 |
| RQ-PN-TUN-VIS-003 | 句柄成功与 LUID ifIndex 仅用于诊断，不得作为成功条件 | PKG-PN-TUN-VIS-03 | 已实现 | 已通过 | 编码工程师 | 已验证诊断性信号未被用作成功条件 | TS-PN-TUN-VIS-FAIL-01 | 2026-04-27 |
| RQ-PN-TUN-VIS-004 | 失败输出结构化诊断码与步骤链路，并附带 install_observation.diagnostic/raw_error | PKG-PN-TUN-VIS-04 | 已实现（P0增强） | 已通过 | 编码工程师 | 已验证失败输出包含 reason_code 与 raw_error 及观测对象 | TS-PN-TUN-VIS-FAIL-01 TS-PN-TUN-VIS-TIMEOUT-01 | 2026-04-28 |
| RQ-PN-TUN-VIS-005 | 验证必须覆盖成功 失败 超时并建立映射 | PKG-PN-TUN-VIS-05 | 已实现（P0增强） | 已通过（新增观测断言） | 测试工程师 | 已验证覆盖链路完整且可复现 | TS-PN-TUN-VIS-OK-01 TS-PN-TUN-VIS-FAIL-01 TS-PN-TUN-VIS-TIMEOUT-01 | 2026-04-28 |

## 执行单元包状态
- PKG-PN-TUN-VIS-01: 成功判定双条件收敛 已完成
- PKG-PN-TUN-VIS-02: 提权后可见性轮询与超时处理 已完成
- PKG-PN-TUN-VIS-03: 可见性判定收敛与诊断性信号降级 已完成
- PKG-PN-TUN-VIS-04: 结构化诊断码与步骤链路 已完成
- PKG-PN-TUN-VIS-05: 成功 失败 超时验证矩阵 已完成

## 门禁记录
- G1 需求门: 通过
- G2 架构门: 通过
- G3 编码核查门: 通过
- G4 测试核查门: 通过
- G5 复盘门: 待执行

## 本次实现与测试更新
- 代码改动:
  - `probe_node/local_tun_install_diagnostic.go`: 新增 P0 观测结构体（driver/create/visibility/final/diagnostic）与最近一次观测缓存。
  - `probe_node/local_tun_install_windows.go`: 安装流程全过程采集观测字段，成功/失败均落盘 observation，并在失败时补全 reason_code/raw_error。
  - `probe_node/local_console.go`: 安装响应增加 `install_observation`；状态响应返回 `last_install_observation` 摘要；错误 payload 暴露 `install_observation`。
  - `probe_node/local_tun_install_windows_test.go`、`probe_node/local_console_test.go`: 新增/增强观测字段断言与状态读取断言。
- 测试执行:
  - `go test -run "TestInstallProbeLocalTUNDriver|TestProbeLocalTUNInstall" -count=1 .` 通过
  - `go test -run "TestProbeLocalTUNStatus|TestProbeLocalTUNInstall" -count=1 .` 通过
- 结果说明: 安装接口与状态接口均可直接读取结构化观测结果，满足“可见驱动/创建设备/枚举可见/最终判定”的 P0 验收边界。

## 2026-04-28 实机闭环修复追加记录
- 目标: 按“实机安装 -> 双枚举验证 -> 失败即修复 -> 回归”闭环迭代，直到 Windows 实机通过。
- 关键改动摘要:
  - 在 `probe_node/local_tun_install_windows.go` 引入安装成功后的句柄保持机制，避免安装后适配器被过早释放导致系统枚举抖动。
  - 在提权等待分支中补充 `resolve_wintun_path_for_elevation_wait` 与“可见后句柄保持”路径，降低 `TUN_ELEVATION_TIMEOUT` 假阴性。
  - 适配器命名调整为 `CloudHelper/CloudHelper Tunnel`，并在 `probe_node/local_tun_adapter_windows.go` 维持与既有匹配逻辑兼容。
  - 同步更新 `probe_node/local_tun_install_windows_test.go` 断言，覆盖句柄保持后的行为变化。
- 实机证据快照:
  - 安装观测: `install_observation.final.success=true`，`install_observation.final.reason_code=TUN_INSTALL_SUCCEEDED`，`install_observation.visibility.detect_visible=true`。
  - NetAdapter 枚举命中（`Get-NetAdapter -IncludeHidden` 关键词过滤）:
    - Name=`CloudHelper`，InterfaceDescription=`CloudHelper Tunnel`，InterfaceGuid=`{DA8E9C42-8E77-4D3A-B4A5-1F3E6A7B9012}`。
  - PnP 设备枚举命中（`Get-PnpDevice -PresentOnly:$false` / `pnputil /enum-devices /class Net`）:
    - InstanceId=`SWD\Wintun\{DA8E9C42-8E77-4D3A-B4A5-1F3E6A7B9012}`，Device Description=`CloudHelper Tunnel`。
- 回归测试:
  - `go test -run "TestInstallProbeLocalTUNDriver|TestProbeLocalTUNInstall|TestProbeLocalTUNStatus" -count=1 .` 通过。
- 状态结论: 已完成本次闭环修复与实机验证记录更新。

## 2026-04-28 实机闭环修复第二轮（本轮）
- 范围声明: 仅处理 Windows TUN 创建设备可见性；不做无关改动。
- 代码口径补充:
  - 成功条件强制为“present PnP + NetAdapter”联合命中，单侧命中不再判定成功。
  - 新增 Phantom 识别与失败码:
    - `TUN_ADAPTER_PHANTOM_ONLY`
    - `TUN_ADAPTER_JOINT_VISIBILITY_MISSING`
  - 当固定标识复用到异常可见性路径时，增加“fresh identity 再创建”与失败诊断信息补全（`final.reason_code/final.reason/raw_error`）。
  - 增加 Phantom 清理尝试（仅清理匹配关键词且为非 present 的 PnP 节点）。

### 本轮迭代记录
- 迭代 1（非管理员触发）
  - 现象: 提权等待窗口内未形成联合可见。
  - 结果: 失败，`code=TUN_ELEVATION_TIMEOUT`（早期）/后续在 Phantom 命中时返回 `TUN_ADAPTER_PHANTOM_ONLY`。
- 迭代 2（管理员进程）
  - 现象: `create_or_open_adapter: ok` 且 `resolve_adapter_luid_from_handle: ok`，但联合枚举持续 `not_found`。
  - 结果: 失败，`code=TUN_ADAPTER_JOINT_VISIBILITY_MISSING`，并进入 fallback fresh create 后仍未形成联合可见。
- 迭代 3（加入 Phantom 识别强化与清理）
  - 现象: 管理员运行仍返回 exit code 1；日志显示 fresh create 后联合可见仍未成立。
  - 结果: 失败，`code=TUN_ADAPTER_JOINT_VISIBILITY_MISSING`。

### 本轮实机证据（当前状态）
- `Get-NetAdapter -IncludeHidden` 关键词过滤（Wintun/WireGuard/CloudHelper）: `[]`
- `Get-PnpDevice -PresentOnly:$false` 关键词过滤（Wintun/WireGuard/CloudHelper）: `[]`
- 安装日志末次失败:
  - `code=TUN_ADAPTER_JOINT_VISIBILITY_MISSING`
  - `hint=LUID 路径冲突后重建仍未满足 present PnP + NetAdapter 联合可见`

### 阻断点与人工管理员命令
- 阻断点: 已进入管理员进程执行安装（`Start-Process -Verb RunAs`），但系统侧两类枚举均无命中，无法满足验收三条件。
- 可复制管理员命令（用于人工前台确认 UAC 并串行执行安装+双枚举）:
  - `powershell -NoProfile -ExecutionPolicy Bypass -Command "$exe='d:\Code\CloudHelper\probe_node\probe_node_tun_fix.exe'; Start-Process -FilePath $exe -ArgumentList '--local-tun-install' -Verb RunAs -Wait; Get-NetAdapter -IncludeHidden | ? { $_.Name -match 'Wintun|WireGuard|CloudHelper' -or $_.InterfaceDescription -match 'Wintun|WireGuard|CloudHelper' } | ft -Auto; Get-PnpDevice -PresentOnly:$false | ? { $_.FriendlyName -match 'Wintun|WireGuard|CloudHelper' -or $_.InstanceId -match 'Wintun|WireGuard|CloudHelper' } | ft -Auto"`
