# 安装与升级

## Mihomo 特殊出口探针

特殊出口探针从普通 `probe_node` 同一源码以 `mihomo_exit` tag构建，发布物名为 `cloudhelper-probe-exit-node-linux-amd64`。安装前先在主控 `/mng/probe` 的探针列表选择“Mihomo 出口探针”并创建；创建后再到 `/mng/route` 的“二次分流”Tab添加Clash配置并提取节点，然后为每组域名选择一个具体节点。每个Clash配置源的请求头均为可选项；刷新只有在全部启用源成功下载、解析和合并后才替换现有节点快照。未匹配域名固定DIRECT，不支持旧的多动作、端口或网络条件模型。私有快照v2不兼容旧动作模型；升级主控和特殊探针后，需要重新保存二次分流配置并重新提取节点，两端应安排在同一维护窗口升级。

### 原生安装

在主控“探针管理”页创建Mihomo出口探针；创建完成后不会自动弹出安装方式。在节点“编辑”中将 `target_system` 选择为 `linux`或`docker`，再点击该节点行内独立的“安装”按钮；安装弹窗默认匹配所选版本，也可在Linux x64与Docker间切换。二次分流Tab不负责创建或安装探针。原生安装器会：

1. 拒绝非 Linux x86_64 环境。
2. 下载并自检 `build_kind=mihomo_exit` 的特殊探针程序。
3. 下载固定的官方 Mihomo Linux amd64 compatible资产并校验SHA-256。
4. 建立 `/opt/cloudhelper/probe_exit_node/data|log|temp`，持久目录不在升级时覆盖。
5. 安装 `probe_exit_node.service`，使用严格文件系统保护、1 GiB内存、512任务和262144文件句柄上限。

### Docker 壳

`docker/probe_exit_node`使用host网络，但不挂载TUN、不给`NET_ADMIN`。镜像仅包含启动环境，Compose分别挂载`program/`、`data/`、`log/`、`temp/`；删除容器或更新镜像不会删除程序、身份、快照、Mihomo或日志。

### 成对升级

Release必须同时包含特殊探针程序和 `cloudhelper-probe-exit-node-manifest.json`。清单固定 `build_kind=mihomo_exit`、`os=linux`、`arch=amd64`，并给出程序资产、Mihomo资产、兼容版本范围和两者SHA-256。升级器在替换前校验两个候选，替换/重启失败时恢复两个旧文件。Docker壳不参与日常程序升级。

`v0.3.316`及更早的特殊探针无法识别无扩展名的`cloudhelper-probe-exit-node-linux-amd64`候选文件。升级这些版本时必须先升级主控，再从新版主控重新下发探针升级。主控会临时通过认证下载代理提供旧解包器可识别的兼容文件名，下载内容、Release URL、配对manifest和SHA-256均保持不变；探针升级至`v0.3.317`及以后版本后，后续升级恢复使用节点自身的直连/主控代理设置。

### 状态与失败关闭

特殊探针向主控报告 desired/applied revision/hash、探针/Mihomo版本、`exit_ready`、健康、活动会话、上下行字节和最近应用错误。缺少私有快照、构建类型错配、快照哈希失败、Mihomo配置校验失败或健康检查失败时停止旧Mihomo并拒绝新出口连接。
