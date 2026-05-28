# 协作文档

- 适用规则: AI协作规则
- 后续工作传递声明: 本文档必须传递给后续阶段与后续角色。
- 需求编号: REQ-PN-ANDROID-ARM64-CLIENT-001
- 需求前缀: REQ-PN-ANDROID-ARM64-CLIENT-001
- 当前阶段: Architect需求跟踪
- 最近更新角色: Architect
- 最近更新时间: 2026-05-28T00:00:00Z
- 工作依据文档: doc/ai-coding-collaboration.md
- 状态: 进行中

## 第1章 Architect章节
- 章节责任角色: Architect
- 状态: 进行中

### 1.1 需求定义
- 状态: 已完成

#### 1.1.1 需求目标
- 开发 Android arm64 版本的 probe node 客户端，可在 Android 设备上注册/连接 controller，并承载本地代理与链路能力。
- 第一阶段先打通 Android 客户端交付闭环: GitHub Actions 编译 APK、Release 上传 Android arm64 产物、主控识别 Android 类型节点、Android 客户端具备升级入口。
- Android 网络接管使用系统 `VpnService`，不依赖 root，不复用 Windows Wintun 或 Linux `/dev/net/tun` 安装逻辑。
- 复用现有 probe node 的控制面、链路层 WS/WS-H3、代理组选路、DNS/FakeIP 与 relay 传输核心能力。
- 输出 Android `arm64-v8a` 可安装包，具备前台服务、VPN 授权、运行状态展示、日志与基础配置能力。

#### 1.1.2 需求范围
- 新增 Android 客户端工程，建议路径为 `probe_node_android/`。
- 新增 GitHub Actions Android arm64 构建与 Release 产物上传流程。
- 主控新增或扩展 probe node 平台/类型识别，能区分 Android 节点并用于展示、筛选和升级分发。
- Android 客户端新增升级能力骨架，能发现 Android Release 产物并触发安装/更新流程。
- 抽取或封装 probe node Go 核心能力，供 Android 通过 `gomobile bind`、JNI 或 Android native library 调用。
- Android 侧实现 `VpnService`、前台服务通知、VPN 权限申请、启动/停止、状态页与基础配置页。
- Android 侧支持 controller 地址、node id/secret、登录态或配对信息配置。
- Android 侧支持 arm64 构建产物与最小冒烟测试。

#### 1.1.3 非范围
- 不开发 iOS 客户端。
- 不要求 Android root 模式、iptables 或系统全局代理设置。
- 不迁移 Windows Wintun 安装、Windows 路由接管、Linux 路由接管实现。
- 首阶段不要求完整复刻桌面本地 HTML 控制台。
- 首阶段不要求 Play Store 发布、应用商店内更新与多 ABI 全覆盖。

#### 1.1.4 验收标准
- GitHub Actions Release 中存在可下载的 Android arm64 APK 产物，资产命名可被升级逻辑稳定匹配。
- 主控可识别并展示 Android 类型 probe node，不与 Linux/Windows 节点混淆。
- Android 客户端升级逻辑可选择 Android APK 资产，并提供可执行的安装/更新入口；若受系统安装权限限制，必须给出明确状态。
- Android arm64 设备或模拟器可安装启动客户端。
- 用户授权 VPN 后，客户端能启动前台服务并创建 Android VPN 虚拟网卡。
- 客户端能连接 controller，完成 probe node 身份加载、心跳汇报与链路配置同步。
- 至少支持 `websocket` 与 `websocket-h3` 两种链路协议候选。
- DNS/FakeIP 与代理组选路在 Android VPN 数据面中有明确实现路径或首阶段降级策略。
- 停止 VPN 后必须释放前台服务、VPN fd、Go runtime 连接与本地资源。
- `arm64-v8a` Debug/Release 构建命令可复现。

#### 1.1.5 风险
- 现有 `probe_node` 是 `package main`，移动端复用前需要拆出可绑定核心包，避免直接绑定主程序。
- Android `VpnService` 只提供文件描述符，Go 侧 packet stack 需要适配 fd 读写与生命周期。
- `quic-go`、gVisor netstack、gomobile/NDK 兼容性需验证，可能需要拆分 build tag 或替代边界。
- Android 后台限制要求前台服务和通知配置正确，否则长连接和 VPN 容易被系统杀死。
- 电池优化、网络切换、Doze 模式会影响 WS/WS-H3 保活与吞吐。

#### 1.1.6 遗留事项
- 确认最小 Android SDK 与目标 SDK。
- 确认 UI 技术栈使用 Kotlin 原生页面、Compose，还是 WebView 承载轻量页面。
- 确认首阶段 Android APK 签名、升级包命名和安装权限提示策略。
- 确认首阶段是否只做 TUN VPN 模式，还是同时提供 HTTP/SOCKS 本地代理模式。
- 确认 Android 端 node 身份下发/配对流程是否沿用现有 controller API。

#### 1.1.7 结论
- 本需求进入架构设计与任务拆分阶段。首阶段目标不是先追全量 VPN 数据面，而是先完成 Android arm64 客户端的编译、发布、主控识别与升级闭环。

### 1.2 总体架构
- 状态: 已完成

#### 1.2.1 架构目标
- 将 probe node 核心能力拆成可嵌入 Android 的移动核心层。
- Android 原生层负责系统权限、VPN fd、前台服务、通知与 UI。
- Go 移动核心层负责 controller 通信、链路传输、代理组策略、DNS/FakeIP 与 packet routing。

#### 1.2.2 总体设计
- Android App 层: Kotlin/Java 工程，包含 Activity、VpnService、Foreground Service、配置存储和状态展示。
- Android VPN 层: 通过 `VpnService.Builder` 创建 TUN fd，配置地址、路由、DNS 与 MTU。
- Native Bridge 层: 将 TUN fd、配置、生命周期事件传给 Go core；Go core 输出状态、日志和错误。
- Go Mobile Core 层: 从现有 `probe_node` 拆出可复用包，例如 `probe_node/mobilecore` 或 `probe_node/internal/mobilecore`。
- Shared Core 层: 复用现有链路 runtime、relay client、DNS/FakeIP、路由决策与代理组选链逻辑；对 Windows/Linux 专有代码使用 build tag 隔离。

#### 1.2.3 关键模块
| 模块编号 | 模块名称 | 职责 | 输入 | 输出 |
|---|---|---|---|---|
| M-001 | Android App Shell | 管理 UI、权限、前台服务、配置 | 用户操作、系统回调 | 服务启动/停止、状态展示 |
| M-002 | Android VpnService | 创建和管理 Android VPN fd | VPN 授权、路由/DNS 配置 | TUN fd、生命周期事件 |
| M-003 | Mobile Bridge | Java/Kotlin 与 Go 之间的桥接 | fd、配置、命令 | Go core 调用结果、状态事件 |
| M-004 | Probe Mobile Core | controller 通信、节点身份、链路配置同步 | controller URL、node secret | 控制面连接、链路 runtime |
| M-005 | Android Packet Data Plane | 从 TUN fd 读取 IP 包并路由 | IP packets、组选路 | direct/tunnel/reject 转发 |
| M-006 | Build & Packaging | 构建 arm64-v8a AAR/APK | Gradle、gomobile/NDK | APK/AAB、测试产物 |
| M-007 | Controller Android Node Support | 识别、展示和分发 Android 节点能力 | probe report、platform、release assets | Android 节点状态、升级目标 |
| M-008 | Android Upgrade Channel | Android 客户端升级检查与安装入口 | 当前版本、release assets | APK 下载、安装触发、状态 |

#### 1.2.4 关键接口
| 接口编号 | 接口名称 | 调用方 | 提供方 | 说明 |
|---|---|---|---|---|
| IF-001 | `ProbeMobileCore.start(config)` | Android Service | Go Mobile Core | 启动 probe core 与 controller 通信 |
| IF-002 | `ProbeMobileCore.stop()` | Android Service | Go Mobile Core | 停止 probe core、链路和数据面 |
| IF-003 | `ProbeMobileCore.attachTunFd(fd, mtu)` | Android VpnService | Go Mobile Core | 绑定 Android VPN TUN fd |
| IF-004 | `ProbeMobileCore.updateConfig(config)` | Android UI/Service | Go Mobile Core | 更新 controller、身份与代理策略 |
| IF-005 | `ProbeMobileCore.snapshot()` | Android UI | Go Mobile Core | 获取连接、VPN、链路、DNS 与错误状态 |
| IF-006 | `ProbeMobileEventSink` | Go Mobile Core | Android Service/UI | 上报日志、状态变化和错误 |
| IF-007 | Android Release Asset | GitHub Actions | Release/Upgrade Channel | Android arm64 APK 产物命名和上传规则 |
| IF-008 | Probe Node Platform Type | Android/桌面 probe node | Controller | 汇报 node 平台/类型，用于展示、筛选和升级策略 |
| IF-009 | Android Upgrade Asset Selection | Android Client/Controller | Release API/Upgrade Logic | 为 Android arm64 选择 APK 升级资产 |

#### 1.2.5 关键约束
- Android 首阶段目标 ABI 为 `arm64-v8a`。
- Android VPN 必须使用 `VpnService` 合规授权流程。
- 移动端不得依赖 Windows Wintun DLL、Windows netapi 或 Linux root 路由命令。
- Go core 拆分必须尽量保持桌面 probe node 行为不回归。
- 首阶段优先 MVP 可运行，再逐步补齐桌面端本地控制台全部能力。

#### 1.2.6 风险
- `package main` 拆包会触及较多全局状态，需分阶段降低改动风险。
- Android fd 与 Go netstack/gVisor 集成可能需要专门适配层和性能测试。
- WS-H3 在 Android 网络切换下的 QUIC 连接恢复策略需单独验证。

#### 1.2.7 结论
- 推荐按“Android 工程壳 + GitHub Actions APK 产物 + 主控 Android 节点识别 + Android 升级通道 + mobilecore/VPN 数据面 + 策略能力补齐”六阶段推进。
- 第一批优先建立可迭代闭环: 先让每次提交都能产出 Android arm64 APK，主控能知道这是 Android 节点，升级逻辑能找到 Android APK；之后再迭代 VPN/TUN、WS/WS-H3 性能和代理策略。

### 1.3 单元设计
- 状态: 已完成

#### 1.3.1 单元清单
| 单元编号 | 单元名称 | 所属模块 | 职责 | 输入 | 输出 |
|---|---|---|---|---|---|
| U-001 | Android Project Scaffold | M-001 M-006 | 建立 Android arm64 工程与构建链路 | Gradle/SDK/NDK | 可编译 APK |
| U-002 | Mobile Core Extraction | M-004 | 从 probe_node 拆出可绑定核心 | 现有 Go main 逻辑 | mobilecore API |
| U-003 | VPN Permission & Service | M-001 M-002 | 管理 VPN 授权和前台服务 | 用户授权 | TUN fd |
| U-004 | TUN FD Bridge | M-002 M-003 M-005 | 将 Android fd 接入 Go packet loop | fd、mtu | packet reader/writer |
| U-005 | Controller Connectivity | M-004 | Android 端连接 controller 并汇报状态 | controller URL、身份 | 心跳/配置同步 |
| U-006 | Proxy Policy Runtime | M-004 M-005 | 复用代理组、DNS/FakeIP、路由决策 | DNS/IP packet | direct/tunnel/reject |
| U-007 | Lifecycle & Observability | M-001 M-003 | 状态、日志、异常和停止清理 | runtime events | UI/notification/log |
| U-008 | Android Build & Test | M-006 | 本地与 CI arm64 构建、Release 产物上传 | CI/本地命令 | APK 与测试报告 |
| U-009 | Controller Android Node Type | M-007 | 主控识别 Android 节点平台/类型并参与升级策略 | probe report、platform | Android 节点展示/筛选/升级目标 |
| U-010 | Android Upgrade Flow | M-008 | Android 客户端版本检查、资产选择、下载与安装触发 | version、release assets | APK 更新流程 |

#### 1.3.2 单元设计
##### 单元编号 U-001
- 单元名称: Android Project Scaffold
- 职责: 新增 Android 工程，配置 `arm64-v8a` ABI、VPN 权限、前台服务权限和基础 Activity。
- 输入: Android Gradle Plugin、Kotlin 或 Java 选择。
- 输出: 可安装 Debug APK。
- 处理规则: 工程与现有 Go 模块解耦，构建脚本明确 Android SDK/NDK 要求。
- 异常规则: SDK/NDK 不可用时记录环境缺口，不阻塞 Go core 拆分设计。

##### 单元编号 U-002
- 单元名称: Mobile Core Extraction
- 职责: 将 `probe_node` 中控制面和链路核心拆成可由 Android 调用的包。
- 输入: `probeLaunchOptions`、controller URL、node identity。
- 输出: `Start/Stop/Snapshot/UpdateConfig` 等移动端 API。
- 处理规则: 保持桌面 main 入口调用旧路径或通过同一 core 包启动，避免复制业务逻辑。
- 异常规则: Windows/Linux 专有实现必须通过 build tag 或接口隔离。

##### 单元编号 U-003
- 单元名称: VPN Permission & Service
- 职责: Android 侧申请 VPN 权限、启动前台服务、创建 `VpnService.Builder`。
- 输入: 用户点击启用、系统授权结果。
- 输出: `ParcelFileDescriptor`、通知状态。
- 处理规则: 未授权时不得启动 core 数据面；停止时必须关闭 fd。
- 异常规则: 用户拒绝授权时显示可恢复错误，不清除配置。

##### 单元编号 U-004
- 单元名称: TUN FD Bridge
- 职责: 将 Android TUN fd 与 Go packet loop 连接。
- 输入: fd、MTU、路由配置。
- 输出: IP packet read/write 能力。
- 处理规则: 需要明确 fd 所有权、关闭顺序和 goroutine 退出信号。
- 异常规则: fd read/write 错误必须触发 VPN 服务降级或停止。

##### 单元编号 U-005
- 单元名称: Controller Connectivity
- 职责: Android 端连接 controller，完成心跳汇报、命令接收、链路配置同步。
- 输入: controller URL、node id、node secret。
- 输出: 在线状态、链路 runtime 状态。
- 处理规则: 网络切换后应重连；配置变更应重启相关 runtime。
- 异常规则: controller 不可达时 VPN 可保持启动但状态显示离线。

##### 单元编号 U-006
- 单元名称: Proxy Policy Runtime
- 职责: Android VPN 数据面执行 DNS/FakeIP、代理组路由和 tunnel/direct/reject 策略。
- 输入: DNS 请求、TCP/UDP packet、proxy_state。
- 输出: 直连、链路转发或拒绝。
- 处理规则: 首阶段可先实现 TCP/DNS 主路径，再补齐 UDP 细节。
- 异常规则: 未选链组应按 fallback/direct 策略处理，不能静默断网。

##### 单元编号 U-007
- 单元名称: Lifecycle & Observability
- 职责: 管理 app/service/core 生命周期、日志、状态快照和错误展示。
- 输入: Android 生命周期、Go core events。
- 输出: UI 状态、通知文案、日志页面。
- 处理规则: 前台服务通知必须反映 VPN 运行/异常状态。
- 异常规则: 崩溃或停止时必须释放 VPN fd 与 Go core。

##### 单元编号 U-008
- 单元名称: Android Build & Test
- 职责: 提供本地构建、GitHub Actions 构建、Release 资产上传、单元测试、模拟器或真机冒烟命令。
- 输入: Gradle、gomobile、NDK。
- 输出: arm64-v8a APK/AAR 与测试报告。
- 处理规则: 第一阶段必须能在 GitHub Actions 产出 Android arm64 APK；Debug/unsigned 产物可先作为内部测试包，Release 签名策略单独确认。
- 异常规则: 无 Android SDK 环境时必须记录未执行原因。

##### 单元编号 U-009
- 单元名称: Controller Android Node Type
- 职责: 让主控识别 Android probe node 的平台/类型，并在状态展示、筛选、升级分发中使用该信息。
- 输入: probe report 中的平台/类型字段、当前版本、系统信息。
- 输出: Android 节点状态、升级目标平台、可展示类型。
- 处理规则: 不应通过 `System` 字符串模糊判断；应使用稳定字段，例如 `platform=node_android` 或 `os=android/arch=arm64`。
- 异常规则: 老版本节点未上报平台字段时保持兼容，按原有 Linux/Windows 逻辑处理。

##### 单元编号 U-010
- 单元名称: Android Upgrade Flow
- 职责: Android 客户端发现自身版本、匹配 Android APK 资产、下载并触发安装或提示授权。
- 输入: 当前版本、Release 元数据、APK 资产命名、Android 安装未知来源权限状态。
- 输出: 下载进度、安装 Intent、升级状态。
- 处理规则: Android APK 资产与桌面二进制资产分开命名，避免 `cloudhelper-probe-node-<goos>-<goarch>` 规则误匹配。
- 异常规则: 无安装权限、下载失败、签名不一致时必须进入可恢复错误状态。

#### 1.3.3 风险
- 首阶段若先追求 VPN 全功能，会扩大拆包风险；建议以 CI 产物、主控识别和升级闭环为第一里程碑。
- Android 端 UI 与 Go core 状态模型需要避免双向强耦合。

#### 1.3.4 结论
- 单元边界清晰，可进入 Code 任务拆分。

### 1.4 Code任务执行包
- 状态: 已完成

#### 1.4.1 执行边界
- 允许修改:
  - `doc/REQ-PN-ANDROID-ARM64-CLIENT-001-collaboration.md`
  - `probe_node/**`
  - `probe_node_android/**`
  - `.github/**`
  - `scripts/**`
- 禁止修改:
  - `Lib/**`
  - 第三方依赖源码
  - 与 Android 客户端无关的 controller 管理页面

#### 1.4.2 任务清单
| 任务编号 | 需求编号 | 单元编号 | 文件范围 | 操作类型 | 验收标准 |
|---|---|---|---|---|---|
| TASK-001 | REQ-PN-ANDROID-ARM64-CLIENT-001 | U-001 U-008 | `probe_node_android/**` `scripts/**` | 新增 | Android 工程可执行 Debug 构建，目标 ABI 包含 `arm64-v8a` |
| TASK-002 | REQ-PN-ANDROID-ARM64-CLIENT-001 | U-008 | `.github/**` `scripts/**` `probe_node_android/**` | 新增/修改 | GitHub Actions 可构建 Android arm64 APK，并作为 Release asset 上传 |
| TASK-003 | REQ-PN-ANDROID-ARM64-CLIENT-001 | U-009 | `probe_node/**` `probe_controller/**` | 修改/新增 | 主控可识别/展示 Android 类型节点，并保留旧节点兼容 |
| TASK-004 | REQ-PN-ANDROID-ARM64-CLIENT-001 | U-010 | `probe_node/**` `probe_node_android/**` `probe_controller/**` | 修改/新增 | Android 升级逻辑可匹配 APK 资产并触发下载/安装入口 |
| TASK-005 | REQ-PN-ANDROID-ARM64-CLIENT-001 | U-002 | `probe_node/**` | 修改/新增 | 提供可绑定 mobilecore API，桌面 `go test ./...` 不回归 |
| TASK-006 | REQ-PN-ANDROID-ARM64-CLIENT-001 | U-003 | `probe_node_android/**` | 新增 | 完成 VPN 授权、前台服务、通知和启动/停止入口 |
| TASK-007 | REQ-PN-ANDROID-ARM64-CLIENT-001 | U-004 U-005 | `probe_node/**` `probe_node_android/**` | 修改/新增 | Android TUN fd 可传入 Go core，并能启动 controller 连接 |
| TASK-008 | REQ-PN-ANDROID-ARM64-CLIENT-001 | U-006 | `probe_node/**` `probe_node_android/**` | 修改/新增 | Android 数据面具备 DNS/FakeIP 与基础 TCP 代理主路径 |
| TASK-009 | REQ-PN-ANDROID-ARM64-CLIENT-001 | U-007 | `probe_node_android/**` | 新增 | UI 可展示 controller、VPN、链路、日志与最近错误状态 |
| TASK-010 | REQ-PN-ANDROID-ARM64-CLIENT-001 | U-008 | `probe_node/**` `probe_node_android/**` | 新增/修改 | 提供 Go 单测、Android 单测或真机/模拟器冒烟测试记录 |

#### 1.4.3 源码修改规则
- 必须使用 `encoding_tools/README.md` 描述的接口。
- 对 C/C++ 源代码 `.c` `.cc` `.cpp` `.cxx` `.h` `.hpp` 必须使用 `encoding_tools/encoding_safe_patch.py`。
- 对非 C/C++ 源代码可直接编辑，不强制使用 `encoding_tools/encoding_safe_patch.py`。
- Android Gradle/Kotlin/XML 文件可直接编辑，但必须记录影响文件与测试命令。

#### 1.4.4 交付物
- Android arm64 工程与构建脚本。
- GitHub Actions Android arm64 APK 构建与 Release asset 上传流程。
- 主控 Android 类型 node 识别、展示和升级目标匹配。
- Android 客户端升级检查、APK 资产选择和安装入口。
- Go mobilecore 包与 Android bridge。
- Android VPN 前台服务与基础 UI。
- Android arm64 构建产物说明与测试证据。

#### 1.4.5 门禁输入
- `cd probe_node; go test ./...`
- Android 工程 Gradle 构建命令与结果。
- GitHub Actions Android arm64 APK 构建/上传结果，或本地等价验证。
- 主控 Android 类型 node 上报/展示/升级资产匹配验证。
- Android 升级资产选择与安装入口冒烟验证。
- gomobile/NDK 构建命令与结果，或未执行原因。
- 真机/模拟器冒烟测试记录，或未执行原因。

#### 1.4.6 结论
- 允许 Code 阶段按 TASK-001 到 TASK-010 分阶段实施；建议第一批执行 TASK-001 到 TASK-004，先完成 Android 编译发布、主控识别和升级闭环，再进入 mobilecore/VPN 数据面。

### 1.5 Architect需求跟踪矩阵
- 状态: 已完成

| 需求编号 | 需求描述 | 架构章节 | 单元设计章节 | Code任务章节 | 状态 | 备注 |
|---|---|---|---|---|---|---|
| REQ-PN-ANDROID-ARM64-CLIENT-001-R1 | Android arm64 工程与 GitHub Actions APK 产物 | 1.2 | U-001 U-008 | TASK-001 TASK-002 | 进行中 | 第一批优先 |
| REQ-PN-ANDROID-ARM64-CLIENT-001-R2 | 主控 Android 类型 node 识别与展示 | 1.2 | U-009 | TASK-003 | 进行中 | 第一批优先 |
| REQ-PN-ANDROID-ARM64-CLIENT-001-R3 | Android 客户端升级通道与 APK 资产匹配 | 1.2 | U-010 | TASK-004 | 进行中 | 第一批优先 |
| REQ-PN-ANDROID-ARM64-CLIENT-001-R4 | probe_node Go core 移动端可嵌入 | 1.2 | U-002 | TASK-005 | 进行中 | 第二批 |
| REQ-PN-ANDROID-ARM64-CLIENT-001-R5 | Android VPNService 数据面接入 | 1.2 | U-003 U-004 | TASK-006 TASK-007 | 进行中 | 依赖 Android 权限 |
| REQ-PN-ANDROID-ARM64-CLIENT-001-R6 | controller 联通、链路同步与代理策略主路径 | 1.2 | U-005 U-006 | TASK-007 TASK-008 | 进行中 | 可分阶段交付 |
| REQ-PN-ANDROID-ARM64-CLIENT-001-R7 | Android 状态 UI、日志与生命周期 | 1.2 | U-007 | TASK-009 | 进行中 | MVP 需要基础页 |

### 1.6 Architect关键接口跟踪矩阵
- 状态: 已完成

| 接口编号 | 需求编号 | 接口名称 | 调用方 | 提供方 | 输入 | 输出 | 状态 | 备注 |
|---|---|---|---|---|---|---|---|---|
| IF-001 | REQ-PN-ANDROID-ARM64-CLIENT-001 | `ProbeMobileCore.start(config)` | Android Service | Go Mobile Core | controller/node 配置 | core 运行态 | 进行中 | 新增 |
| IF-002 | REQ-PN-ANDROID-ARM64-CLIENT-001 | `ProbeMobileCore.stop()` | Android Service | Go Mobile Core | 停止命令 | 资源释放结果 | 进行中 | 新增 |
| IF-003 | REQ-PN-ANDROID-ARM64-CLIENT-001 | `ProbeMobileCore.attachTunFd(fd, mtu)` | Android VpnService | Go Mobile Core | TUN fd/MTU | packet loop | 进行中 | 新增 |
| IF-004 | REQ-PN-ANDROID-ARM64-CLIENT-001 | `ProbeMobileCore.updateConfig(config)` | Android UI/Service | Go Mobile Core | 配置变更 | 应用结果 | 进行中 | 新增 |
| IF-005 | REQ-PN-ANDROID-ARM64-CLIENT-001 | `ProbeMobileCore.snapshot()` | Android UI | Go Mobile Core | 查询请求 | 状态快照 | 进行中 | 新增 |
| IF-006 | REQ-PN-ANDROID-ARM64-CLIENT-001 | `ProbeMobileEventSink` | Go Mobile Core | Android UI/Service | 事件/日志/错误 | 展示与通知 | 进行中 | 新增 |
| IF-007 | REQ-PN-ANDROID-ARM64-CLIENT-001 | Android Release Asset | GitHub Actions | Release/Upgrade Channel | Android arm64 APK | 可下载 Release 资产 | 进行中 | 第一批 |
| IF-008 | REQ-PN-ANDROID-ARM64-CLIENT-001 | Probe Node Platform Type | probe node | Controller | platform/os/arch/type/version | 节点类型与升级目标 | 进行中 | 第一批 |
| IF-009 | REQ-PN-ANDROID-ARM64-CLIENT-001 | Android Upgrade Asset Selection | Android Client/Controller | Release API/Upgrade Logic | version、asset list | APK 下载/安装入口 | 进行中 | 第一批 |

### 1.7 门禁裁判
- 状态: 待评审

#### 1.7.1 门禁输入
| 文档 | 路径 | 状态 |
|---|---|---|
| 协作文档 | `doc/REQ-PN-ANDROID-ARM64-CLIENT-001-collaboration.md` | 已创建 |

#### 1.7.2 裁判检查
| 检查项 | 结果 | 证据 | 备注 |
|---|---|---|---|
| 协作文档存在 | 通过 | 本文件 | 无 |
| Architect章节存在 | 通过 | 第1章 | 无 |
| Code章节存在 | 待评审 | 第2章骨架 | Code阶段补充 |
| 必需子章节存在 | 通过 | 1.1-1.7 | 无 |
| 需求前缀一致 | 通过 | REQ-PN-ANDROID-ARM64-CLIENT-001 | 无 |
| 验收标准可测试 | 通过 | 1.1.4、1.4.5 | 无 |

#### 1.7.3 门禁结论
- 裁判结论: 有条件通过
- 放行阻塞: 放行
- 条件:
  - Code 阶段开始前需确认 Android SDK/NDK/gomobile 可用性，或先使用不依赖 gomobile 的 Android 壳工程完成 CI APK。
  - 第一批实施建议限制在 TASK-001 到 TASK-004，避免过早拆 VPN 数据面，先确保 Android 编译发布、主控识别和升级闭环成立。

## 第2章 Code章节
- 章节责任角色: Code
- 状态: 未开始

### 2.1 Code需求跟踪矩阵
- 状态: 未开始

| 需求编号 | Code任务 | 状态 | 备注 |
|---|---|---|---|
| REQ-PN-ANDROID-ARM64-CLIENT-001 | 待 Code 阶段填写 | 未开始 | 无 |

### 2.2 Code关键接口跟踪矩阵
- 状态: 未开始

| 接口编号 | 接口名称 | 实现状态 | 备注 |
|---|---|---|---|
| IF-001 | `ProbeMobileCore.start(config)` | 未开始 | 无 |
| IF-002 | `ProbeMobileCore.stop()` | 未开始 | 无 |
| IF-003 | `ProbeMobileCore.attachTunFd(fd, mtu)` | 未开始 | 无 |
| IF-004 | `ProbeMobileCore.updateConfig(config)` | 未开始 | 无 |
| IF-005 | `ProbeMobileCore.snapshot()` | 未开始 | 无 |
| IF-006 | `ProbeMobileEventSink` | 未开始 | 无 |
| IF-007 | Android Release Asset | 未开始 | 无 |
| IF-008 | Probe Node Platform Type | 未开始 | 无 |
| IF-009 | Android Upgrade Asset Selection | 未开始 | 无 |

### 2.3 Code测试项跟踪矩阵
- 状态: 未开始

| 测试项编号 | 需求编号 | 测试项 | 状态 | 备注 |
|---|---|---|---|---|
| TC-001 | REQ-PN-ANDROID-ARM64-CLIENT-001 | `cd probe_node; go test ./...` | 未开始 | Code阶段执行 |
| TC-002 | REQ-PN-ANDROID-ARM64-CLIENT-001 | Android Debug arm64 构建 | 未开始 | Code阶段执行 |
| TC-003 | REQ-PN-ANDROID-ARM64-CLIENT-001 | GitHub Actions Android APK Release asset 验证 | 未开始 | Code阶段执行 |
| TC-004 | REQ-PN-ANDROID-ARM64-CLIENT-001 | 主控 Android 类型 node 上报/展示验证 | 未开始 | Code阶段执行 |
| TC-005 | REQ-PN-ANDROID-ARM64-CLIENT-001 | Android 升级资产选择与安装入口冒烟 | 未开始 | Code阶段执行 |
| TC-006 | REQ-PN-ANDROID-ARM64-CLIENT-001 | Android VPN 授权与服务启动冒烟 | 未开始 | 后续 Code 阶段执行 |
| TC-007 | REQ-PN-ANDROID-ARM64-CLIENT-001 | controller 联通冒烟 | 未开始 | 后续 Code 阶段执行 |

### 2.4 Code缺陷跟踪矩阵
- 状态: 未开始

| 缺陷编号 | 需求编号 | 描述 | 状态 | 备注 |
|---|---|---|---|---|
| DEF-001 | REQ-PN-ANDROID-ARM64-CLIENT-001 | 暂无 | 未开始 | 无 |

### 2.5 Code执行证据
- 状态: 未开始

- 修改接口: 未开始
- 配置文件: 未开始
- 执行报告: 未开始
- 影响文件: 未开始
- 测试命令: 未开始
- 自测结果: 未开始
- 未执行测试原因: 未开始
- 遗留风险: 未开始
- 回滚方案: 未开始

### 2.6 Code任务反馈
- 状态: 未开始

| 反馈编号 | 类型 | 描述 | 状态 |
|---|---|---|---|
| FB-001 | 无 | 暂无 | 未开始 |
