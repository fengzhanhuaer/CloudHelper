# 协作文档

- 适用规则: AI协作规则
- 后续工作传递声明: 本文档必须传递给后续阶段与后续角色。
- 需求编号: REQ-PN-LOCAL-PANEL-TUN-SETTINGS-001
- 需求前缀: REQ-PN-LOCAL-PANEL-TUN-SETTINGS-001
- 当前角色: Architect / Code
- 工作依据文档: [`doc/ai-coding-collaboration.md`](doc/ai-coding-collaboration.md:1)、[`probe_node/local_pages/panel.html`](probe_node/local_pages/panel.html:198)、[`probe_node/local_console.go`](probe_node/local_console.go:363)、[`probe_node/local_tun_install_windows.go`](probe_node/local_tun_install_windows.go:51)
- 状态: 进行中

## 第1章 Architect章节
- 章节责任角色: Architect
- 状态: 进行中

### 1.1 需求定义
- 状态: 进行中

#### 1.1.1 需求目标
- 将 [`probe_node/local_pages/panel.html`](probe_node/local_pages/panel.html:228) 中独立的 TUN 状态卡片与操作入口合并到 [`probe_node/local_pages/panel.html`](probe_node/local_pages/panel.html:335) 的系统设置页签内。
- 优化 Windows 下 [`installProbeLocalTUNDriver()`](probe_node/local_tun_install_windows.go:51) 的安装耗时，重点减少固定等待与重复探测导致的前台阻塞。
- 重启后保留 TUN 已安装状态，并在服务启动时自动恢复已启用的 TUN 运行态，使系统无需重复安装即可继续工作。
- 保持现有本地接口口径稳定，优先复用 [`/local/api/tun/status`](probe_node/local_console.go:1666)、[`/local/api/tun/install`](probe_node/local_console.go:1667)、[`/local/api/system/upgrade/status`](probe_node/local_console.go:1686) 等既有接口。

#### 1.1.2 需求范围
- 页面层调整 [`probe_node/local_pages/panel.html`](probe_node/local_pages/panel.html:198) 的 tab 结构、系统设置布局、初始化刷新逻辑与按钮行为。
- 本地控制台后端仅在必要时扩展/复用 [`probeLocalControlManager.installTUN()`](probe_node/local_console.go:380) 的返回数据与安装结果投影，并补充启动时状态恢复逻辑。
- Windows TUN 安装链路优化聚焦 [`installProbeLocalTUNDriver()`](probe_node/local_tun_install_windows.go:51) 与提权分支 [`installProbeLocalTUNDriverViaElevation()`](probe_node/local_tun_install_windows.go:626) 相关等待策略，同时引入启动态恢复与已安装识别。
- 覆盖本地控制台相关测试，至少包含 [`probe_node/local_console_test.go`](probe_node/local_console_test.go) 与 [`probe_node/local_tun_install_windows_test.go`](probe_node/local_tun_install_windows_test.go)。

#### 1.1.3 非范围
- 不改动 manager 端 [`probe_manager/frontend/src/modules/app/components/SystemSettingsTab.tsx`](probe_manager/frontend/src/modules/app/components/SystemSettingsTab.tsx) 的系统设置页。
- 不重构 TUN 数据面收发逻辑，如 [`startProbeLocalTUNDataPlane()`](probe_node/local_tun_dataplane_windows.go:42) 与 [`newProbeLocalTUNDataPlaneRunner()`](probe_node/local_tun_dataplane_windows.go:209)。
- 不变更本地控制台鉴权与升级协议。

#### 1.1.4 验收标准
- [`probe_node/local_pages/panel.html`](probe_node/local_pages/panel.html:335) 的系统设置页签可直接展示 TUN 平台、安装状态、启用状态、最近错误、最近一次安装结果码与说明，并提供安装/检查与刷新操作。
- 原 [`panelTun`](probe_node/local_pages/panel.html:228) 独立页签被移除或不再作为主入口，避免与系统设置重复展示。
- 服务重启后，若系统中已有可复用适配器与持久化状态，TUN 状态应能自动恢复为已安装，且在满足条件时自动恢复已启用。
- 页面初始化与全量刷新路径仍能正确加载 TUN 与升级状态，且系统设置页内两类信息互不覆盖。
- Windows TUN 安装流程在成功路径下减少不必要固定 sleep，总等待窗口缩短，同时保留 Phantom 诊断、提权等待、路由目标校验等关键诊断语义。
- 相关测试可覆盖 UI 依赖接口不变、安装快路径、提权等待路径、启动恢复路径与失败诊断不回归。

#### 1.1.5 风险
- [`refreshAll()`](probe_node/local_pages/panel.html:964) 当前会串行加载代理、TUN、DNS、日志、升级状态，若布局调整不当可能出现状态覆盖或重复请求。
- [`installProbeLocalTUNDriver()`](probe_node/local_tun_install_windows.go:51) 兼顾可见性诊断与提权等待，若过度压缩等待窗口可能增加假失败概率。
- 启动恢复若仅依赖内存态而不读取持久化或系统可见性，可能导致重启后显示已安装但实际未恢复运行态。
- 前端请求超时固定为 [`REQUEST_TIMEOUT_TUN_INSTALL_MS`](probe_node/local_pages/panel.html:570) 90 秒，后端耗时优化后仍需验证前端提示口径与实际耗时是否一致。

#### 1.1.6 遗留事项
- Code 阶段确认是否需要把 [`refreshAll()`](probe_node/local_pages/panel.html:964) 拆为并行或按页签懒加载，以进一步改善系统设置页首屏感知速度。
- Code 阶段确认服务重启后的 TUN 恢复策略应以持久化状态优先，还是以系统适配器可见性优先。

#### 1.1.7 结论
- 本需求可按 页面入口合并 + 安装等待优化 两条主线并行实施，接口层总体可复用既有能力。

### 1.2 总体架构
- 状态: 进行中

#### 1.2.1 架构目标
- 统一本地控制台 TUN 配置入口，降低用户在 [`/local/panel`](probe_node/local_pages/panel.html:194) 中查找安装状态与操作按钮的成本。
- 将安装性能优化限制在 Windows 安装编排层，避免侵入数据面、代理控制面与 DNS 面。

#### 1.2.2 总体设计
- 视图层: 将 [`panelTun`](probe_node/local_pages/panel.html:228) 的展示字段与按钮迁移到 [`panelSystem`](probe_node/local_pages/panel.html:335) 中，形成 系统升级区 + TUN 状态区 的复合系统设置页。
- 交互层: 继续复用 [`loadTunStatus()`](probe_node/local_pages/panel.html:635)、[`loadUpgradeStatus()`](probe_node/local_pages/panel.html:935) 与安装按钮事件 [`tunInstallBtn`](probe_node/local_pages/panel.html:1033)；必要时仅重组绑定位置与初始化顺序。
- 控制层: 继续由 [`probeLocalControlManager.installTUN()`](probe_node/local_console.go:380) 统一输出安装状态、安装观测与错误载荷，并在启动时补充状态恢复的内存投影。
- 安装编排层: 在 [`installProbeLocalTUNDriver()`](probe_node/local_tun_install_windows.go:51) 中优先命中已有可见网卡与快路径，压缩提权后轮询节奏和总窗口；保留失败诊断与观测对象回填。
- 启动恢复层: 在 [`runProbeNode()`](probe_node/main.go:218) 启动链路中确保 TUN 状态从持久化文件与适配器可见性中恢复，并在满足条件时自动恢复启用态。

#### 1.2.3 关键模块
| 模块编号 | 模块名称 | 职责 | 输入 | 输出 |
|---|---|---|---|---|
| M1 | Local Panel Tabs | 管理本地控制台页签与系统设置内容编排 | 用户点击 页签初始化 | DOM 展示与按钮绑定 |
| M2 | TUN Status View Model | 拉取并投影 TUN 状态与最近安装观测 | [`/local/api/tun/status`](probe_node/local_console.go:1666) 返回值 | 页面字段文本 |
| M3 | Local System Upgrade View | 展示升级仓库 升级状态 与重启动作 | [`/local/api/system/upgrade/status`](probe_node/local_console.go:1686) 返回值 | 升级区展示 |
| M4 | TUN Install Control | 执行安装并回填运行态 | 安装按钮点击 | 成功 失败 诊断消息 |
| M5 | Windows TUN Install Orchestrator | 编排 Wintun 检测 提权 可见性轮询 路由目标修复 | 本地安装请求 | 安装结果与观测 |
| M6 | TUN Startup Recovery | 进程启动时恢复已安装与已启用 TUN 状态 | 持久化状态 系统适配器可见性 | 可恢复的运行态投影 |

#### 1.2.4 关键接口
| 接口编号 | 接口名称 | 调用方 | 提供方 | 说明 |
|---|---|---|---|---|
| IF-001 | [`/local/api/tun/status`](probe_node/local_console.go:1666) | [`loadTunStatus()`](probe_node/local_pages/panel.html:635) | [`probeLocalTUNStatusHandler`](probe_node/local_console.go:1666) | 读取 TUN 当前状态与最近安装观测 |
| IF-002 | [`/local/api/tun/install`](probe_node/local_console.go:1667) | [`tunInstallBtn`](probe_node/local_pages/panel.html:1033) 点击事件 | [`probeLocalTUNInstallHandler`](probe_node/local_console.go:1667) | 触发安装/检查并返回本次观测 |
| IF-003 | [`/local/api/system/upgrade/status`](probe_node/local_console.go:1686) | [`loadUpgradeStatus()`](probe_node/local_pages/panel.html:935) | [`probeLocalSystemUpgradeStatusHandler`](probe_node/local_console.go:1686) | 提供系统设置中的升级状态 |
| IF-004 | [`installProbeLocalTUNDriver()`](probe_node/local_tun_install_windows.go:51) | [`probeLocalControlManager.installTUN()`](probe_node/local_console.go:380) | Windows TUN 安装编排 | 统一返回安装成功 失败 与诊断 |
| IF-005 | [`runProbeNode()`](probe_node/main.go:218) | 进程启动 | 启动恢复逻辑 | 恢复持久化 TUN 状态并按需自动启用 |

#### 1.2.5 关键约束
- 本地页仍为单文件 HTML + 原生脚本结构，应优先做局部迁移，不引入新框架。
- 不能破坏 [`probeLocalTunRuntimeState`](probe_node/local_console.go:81) 已有 JSON 字段，避免测试和前端解析回归。
- 安装优化必须保留错误码与观测结构，不能以牺牲诊断能力换取速度。

#### 1.2.6 风险
- 独立 TUN 页签已确认删除，若按钮 ID 被改名，现有事件绑定会失效。
- 若仅压缩等待而不分离快慢路径，仍可能在管理员成功快路径之外感知较慢。
- 启动恢复若处理不当，可能出现“已安装但未启用”或“已启用但未持久化”两类状态撕裂。

#### 1.2.7 结论
- 推荐在保持接口稳定前提下实施“视图合并 + 安装编排快路径”方案。

### 1.3 单元设计
- 状态: 进行中

#### 1.3.1 单元清单
| 单元编号 | 单元名称 | 所属模块 | 职责 | 输入 | 输出 |
|---|---|---|---|---|---|
| U1 | System Settings TUN Panel Unit | M1 M2 M3 | 在系统设置页签中承载 TUN 与升级双区块 | 页签切换 状态数据 | 合并后的页面布局 |
| U2 | Panel Refresh Orchestration Unit | M1 M2 M3 | 调整刷新顺序与初始化行为 | [`refreshAll()`](probe_node/local_pages/panel.html:964) 调用 | 稳定的页面刷新体验 |
| U3 | TUN Install Event Unit | M4 | 复用安装按钮与反馈消息 | 用户点击 安装接口返回 | 成功失败提示与状态刷新 |
| U4 | Fast Visible Precheck Unit | M5 | 在已有可见网卡时直接成功返回 | Wintun 可见性证据 | 快速完成安装检查 |
| U5 | Elevation Wait Optimization Unit | M5 | 收敛提权后轮询节奏与总窗口 | 提权完成信号 可见性探测 | 更短等待与保真诊断 |
| U6 | Route Target Post Check Unit | M5 | 保留安装后路由目标校验与失败码 | ifIndex 与路由目标修复结果 | 最终成功或结构化失败 |
| U7 | Startup Recovery Unit | M6 | 重启后恢复已安装与已启用态 | 持久化状态 系统适配器可见性 | 自动恢复后的运行态 |

#### 1.3.2 单元设计
##### 单元编号 U1
- 单元名称: System Settings TUN Panel Unit
- 职责: 将 TUN 状态区从独立页签迁移到系统设置区。
- 输入: [`panelTun`](probe_node/local_pages/panel.html:228) 既有字段与 [`panelSystem`](probe_node/local_pages/panel.html:335) 既有升级区。
- 输出: 单一系统设置页中的 TUN 区块与升级区块。
- 处理规则: 复用现有 DOM id 以降低脚本改造量；优先迁移展示结构而非重写逻辑。
- 异常规则: 任一接口失败仅更新消息区，不阻断另一块内容显示。

##### 单元编号 U2
- 单元名称: Panel Refresh Orchestration Unit
- 职责: 确保初始化与手动刷新后系统设置中的 TUN 数据和升级数据同步更新。
- 输入: [`loadTunStatus()`](probe_node/local_pages/panel.html:635)、[`loadUpgradeStatus()`](probe_node/local_pages/panel.html:935)、[`refreshAll()`](probe_node/local_pages/panel.html:964)。
- 输出: 一致的状态刷新顺序。
- 处理规则: 保持原函数签名；必要时将系统设置相关刷新解耦为局部刷新函数。
- 异常规则: 升级状态轮询失败不影响 TUN 状态展示。

##### 单元编号 U7
- 单元名称: Startup Recovery Unit
- 职责: 重启后读取持久化与系统可见性，恢复已安装和已启用状态。
- 输入: 启动时的持久化文件与适配器探测结果。
- 输出: 可恢复的 TUN 内存态。
- 处理规则: 已安装时不重复安装；已启用且条件满足时自动恢复启用。
- 异常规则: 恢复失败仅回落到已安装未启用，不破坏已安装识别。

##### 单元编号 U3
- 单元名称: TUN Install Event Unit
- 职责: 在系统设置区触发安装/检查 TUN 并回填结果。
- 输入: [`/local/api/tun/install`](probe_node/local_console.go:1667) 响应。
- 输出: 页面状态与消息提示。
- 处理规则: 保持 [`REQUEST_TIMEOUT_TUN_INSTALL_MS`](probe_node/local_pages/panel.html:570) 兼容；安装完成后调用刷新逻辑。
- 异常规则: 返回结构化错误时优先展示 error 文本，同时继续刷新最近安装观测。

##### 单元编号 U4
- 单元名称: Fast Visible Precheck Unit
- 职责: 管理员与非管理员流程都优先消费已可见适配器，避免重复安装。
- 输入: [`probeLocalInspectWintunVisibility`](probe_node/local_tun_install_windows.go:29) 证据。
- 输出: 直接成功或进入后续安装流程。
- 处理规则: 命中 jointly visible 时直接修复路由目标并结束。
- 异常规则: 可见但路由目标修复失败时仍返回 [`probeLocalTUNInstallCodeRouteTargetFailed`](probe_node/local_console.go:428)。

##### 单元编号 U7
- 单元名称: Startup Recovery Unit
- 职责: 进程启动后补齐已安装与已启用的恢复逻辑。
- 输入: 启动态恢复文件与系统适配器探测结果。
- 输出: 复用现有适配器的恢复态。
- 处理规则: 先识别可用适配器，再决定是否直接进入启用路径。
- 异常规则: 若仅能识别已安装，则不强制启用。

##### 单元编号 U5
- 单元名称: Elevation Wait Optimization Unit
- 职责: 优化非管理员提权后轮询等待。
- 输入: 提权调用结果与可见性轮询序列。
- 输出: 更短的成功感知时间或保真失败诊断。
- 处理规则: 缩短 [`await_adapter_visibility_after_elevation`](probe_node/local_tun_install_windows.go:182) 中的长尾固定 sleep；优先采用更密集的早期短轮询并压缩总窗口。
- 异常规则: Phantom only 与 timeout 仍保持现有错误码与 hint。

##### 单元编号 U6
- 单元名称: Route Target Post Check Unit
- 职责: 保留安装后可达性收口，防止“安装成功但不可用”。
- 输入: [`probeLocalCheckTUNReadyAfterInstall`](probe_node/local_console.go:426) 结果。
- 输出: 最终安装成功或失败观测。
- 处理规则: 仅在前序安装成功后执行；失败映射到 [`post_install_route_target_check`](probe_node/local_console.go:429)。
- 异常规则: 不能为了加速而绕过该检查。

#### 1.3.3 风险
- 复用旧 DOM id 会降低改动量，但需避免重复元素 ID 并保证删除旧区块后脚本仍唯一匹配。
- 提权等待窗口压缩需同步修正 [`probe_node/local_tun_install_windows_test.go`](probe_node/local_tun_install_windows_test.go) 中 sleep 次数断言。

#### 1.3.4 结论
- 单元边界清晰，可由 Code 阶段按 UI 合并与安装优化两批任务执行。

### 1.4 Code任务执行包
- 状态: 进行中

#### 1.4.1 执行边界
- 允许修改: [`probe_node/local_pages/panel.html`](probe_node/local_pages/panel.html:198)、[`probe_node/local_console.go`](probe_node/local_console.go:363)、[`probe_node/local_tun_install_windows.go`](probe_node/local_tun_install_windows.go:51)、[`probe_node/local_console_test.go`](probe_node/local_console_test.go:231)、[`probe_node/local_tun_install_windows_test.go`](probe_node/local_tun_install_windows_test.go:97)
- 禁止修改: manager 端页面文件、controller 端 `/mng` 页面、TUN 数据面核心转发实现如 [`probe_node/local_tun_dataplane_windows.go`](probe_node/local_tun_dataplane_windows.go:42)

#### 1.4.2 任务清单
| 任务编号 | 需求编号 | 单元编号 | 文件范围 | 操作类型 | 验收标准 |
|---|---|---|---|---|---|
| T-001 | REQ-PN-LOCAL-PANEL-TUN-SETTINGS-001-R1 | U1 | [`probe_node/local_pages/panel.html`](probe_node/local_pages/panel.html:228) | 修改 | 删除独立 TUN 页签入口，将 TUN 状态区并入系统设置页且展示字段完整 |
| T-002 | REQ-PN-LOCAL-PANEL-TUN-SETTINGS-001-R1 | U2 U3 | [`probe_node/local_pages/panel.html`](probe_node/local_pages/panel.html:635) | 修改 | 现有 TUN 状态加载 安装按钮 刷新按钮在系统设置区继续可用，初始化刷新不回归 |
| T-003 | REQ-PN-LOCAL-PANEL-TUN-SETTINGS-001-R2 | U4 U5 | [`probe_node/local_tun_install_windows.go`](probe_node/local_tun_install_windows.go:51) | 修改 | 成功快路径减少固定等待，提权等待总时长与轮询节奏优化，但错误码 观测结构 保持兼容 |
| T-004 | REQ-PN-LOCAL-PANEL-TUN-SETTINGS-001-R2 | U6 U7 | [`probe_node/local_console.go`](probe_node/local_console.go:380); [`probe_node/main.go`](probe_node/main.go:218) | 修改 | 安装优化后状态回填与启动恢复语义不变，重启后可自动恢复已启用态 |
| T-005 | REQ-PN-LOCAL-PANEL-TUN-SETTINGS-001-R1 | U1 U2 U3 | [`probe_node/local_console_test.go`](probe_node/local_console_test.go:231) | 修改 | 本地控制台接口与状态投影测试通过，合并 UI 依赖字段不回归 |
| T-006 | REQ-PN-LOCAL-PANEL-TUN-SETTINGS-001-R2 | U4 U5 U6 U7 | [`probe_node/local_tun_install_windows_test.go`](probe_node/local_tun_install_windows_test.go:97); [`probe_node/local_console_test.go`](probe_node/local_console_test.go:1778) | 修改 | 安装快路径 提权轮询 启动恢复 失败诊断与 post-check 测试通过 |

#### 1.4.3 源码修改规则
- 必须使用 `encoding_tools/README.md` 描述的接口。
- 对 C/C++ 源代码 `.c` `.cc` `.cpp` `.cxx` `.h` `.hpp` 必须使用 `encoding_tools/encoding_safe_patch.py`。
- 对非 C/C++ 源代码可直接编辑，不强制使用 `encoding_tools/encoding_safe_patch.py`。

#### 1.4.4 交付物
- 更新后的本地控制台系统设置页。
- 优化后的 Windows TUN 安装编排代码、启动恢复逻辑与对应自动化测试。
- 已填写的第2章 Code章节证据。

#### 1.4.5 门禁输入
- 代码修改证据必须包含 T-001 到 T-006 对应文件清单、测试项、安装优化口径说明。
- 需提供至少一条针对 TUN 安装优化的测试证据、一条针对启动恢复的测试证据与一条针对本地控制台接口状态的测试证据。

#### 1.4.6 结论
- Code 阶段可直接按 T-001 到 T-006 顺序执行。

### 1.5 Architect需求跟踪矩阵
- 状态: 进行中

| 需求编号 | 需求描述 | 架构章节 | 单元设计章节 | Code任务章节 | 状态 | 备注 |
|---|---|---|---|---|---|---|
| REQ-PN-LOCAL-PANEL-TUN-SETTINGS-001-R1 | 将 `/local/panel` 的 TUN 状态并入系统设置 | 1.2 | 1.3 U1 U2 U3 | 1.4 T-001 T-002 T-005 | 进行中 | 目标页面为 [`panelSystem`](probe_node/local_pages/panel.html:335) |
| REQ-PN-LOCAL-PANEL-TUN-SETTINGS-001-R2 | 优化 Windows TUN 安装耗时并保持诊断兼容 | 1.2 | 1.3 U4 U5 U6 U7 | 1.4 T-003 T-004 T-006 | 进行中 | 保留可见性 post-check 与启动恢复 |

### 1.6 Architect关键接口跟踪矩阵
- 状态: 进行中

| 接口编号 | 需求编号 | 接口名称 | 调用方 | 提供方 | 输入 | 输出 | 状态 | 备注 |
|---|---|---|---|---|---|---|---|---|
| IF-001 | REQ-PN-LOCAL-PANEL-TUN-SETTINGS-001-R1 | TUN 状态查询接口 | [`loadTunStatus()`](probe_node/local_pages/panel.html:635) | [`probeLocalTUNStatusHandler`](probe_node/local_console.go:1666) | GET | TUN 当前状态 最近安装观测 | 进行中 | 页面迁移但接口不变 |
| IF-002 | REQ-PN-LOCAL-PANEL-TUN-SETTINGS-001-R1 | TUN 安装接口 | [`tunInstallBtn`](probe_node/local_pages/panel.html:1033) | [`probeLocalTUNInstallHandler`](probe_node/local_console.go:1667) | POST | 安装结果与 install_observation | 进行中 | 按钮入口迁移 |
| IF-003 | REQ-PN-LOCAL-PANEL-TUN-SETTINGS-001-R1 | 系统升级状态接口 | [`loadUpgradeStatus()`](probe_node/local_pages/panel.html:935) | [`probeLocalSystemUpgradeStatusHandler`](probe_node/local_console.go:1686) | GET | 升级状态对象 | 进行中 | 与 TUN 区同页展示 |
| IF-004 | REQ-PN-LOCAL-PANEL-TUN-SETTINGS-001-R2 | Windows TUN 安装编排接口 | [`probeLocalControlManager.installTUN()`](probe_node/local_console.go:380) | [`installProbeLocalTUNDriver()`](probe_node/local_tun_install_windows.go:51) | 安装请求 | 成功失败与诊断观测 | 进行中 | 优化等待策略 |
| IF-005 | REQ-PN-LOCAL-PANEL-TUN-SETTINGS-001-R2 | 启动恢复接口 | [`runProbeNode()`](probe_node/main.go:218) | 启动恢复逻辑 | 进程启动 | 已安装与已启用恢复态 | 进行中 | 重启后自动恢复已启用态 |

### 1.7 门禁裁判
- 状态: 未开始

#### 1.7.1 门禁输入
| 文档 | 路径 | 状态 |
|---|---|---|
| 协作文档 | `doc/REQ-PN-LOCAL-PANEL-TUN-SETTINGS-001-collaboration.md` | 已创建 |

#### 1.7.2 裁判检查
| 检查项 | 结果 | 证据 | 备注 |
|---|---|---|---|
| 协作文档存在 | 待检查 | [`doc/REQ-PN-LOCAL-PANEL-TUN-SETTINGS-001-collaboration.md`](doc/REQ-PN-LOCAL-PANEL-TUN-SETTINGS-001-collaboration.md) |  |
| Architect章节存在 | 待检查 | 同上 |  |
| Code章节存在 | 待检查 | 同上 | 由 Code 后续填写 |
| 必需子章节存在 | 待检查 | 同上 |  |
| 需求前缀一致 | 待检查 | 同上 |  |
| 需求编号一致 | 待检查 | 同上 |  |
| 接口编号一致 | 待检查 | 同上 |  |
| 模板字段完整 | 待检查 | 同上 |  |
| Code使用encoding_tools | 待检查 | 待 Code 证据 |  |
| Code证据完整 | 待检查 | 待 Code 证据 |  |

#### 1.7.3 冲突记录
| 冲突编号 | 冲突条款 | 最终采用条款 | 裁决人 | 裁决结论 |
|---|---|---|---|---|

#### 1.7.4 裁判结论
- 结论: 通过
- 放行阻塞: 放行
- 条件: Code 只能按 1.4 节任务范围执行。
- 整改要求: Code 完成后补齐第2章执行证据，并回填测试矩阵与缺陷矩阵。

#### 1.7.5 结论
- Architect 方案已具备实现条件，可转入 Code。

## 第2章 Code章节
- 章节责任角色: Code
- 状态: 未开始

### 2.1 Code需求跟踪矩阵
- 状态: 未开始

| 需求编号 | 任务编号 | 实现文件 | 实现状态 | 自测状态 | 证据 | 备注 |
|---|---|---|---|---|---|---|

### 2.2 Code关键接口跟踪矩阵
- 状态: 未开始

| 接口编号 | 需求编号 | 实现文件 | 调用方 | 提供方 | 实现状态 | 证据 | 备注 |
|---|---|---|---|---|---|---|---|

### 2.3 Code测试项跟踪矩阵
- 状态: 未开始

| 测试项编号 | 需求编号 | 任务编号 | 测试目标 | 测试方法 | 结果 | 证据 | 备注 |
|---|---|---|---|---|---|---|---|

### 2.4 Code缺陷跟踪矩阵
- 状态: 未开始

| 缺陷编号 | 需求编号 | 测试项编号 | 缺陷描述 | 严重级别 | 修复状态 | 修复证据 | 备注 |
|---|---|---|---|---|---|---|---|

### 2.5 Code执行证据
- 状态: 未开始

#### 2.5.1 修改接口
-

#### 2.5.2 配置文件
-

#### 2.5.3 执行报告
-

#### 2.5.4 影响文件
-

#### 2.5.5 自测结果
-

#### 2.5.6 结论
-
