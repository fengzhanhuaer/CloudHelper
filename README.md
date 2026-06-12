# CloudHelper


CloudHelper 是一个探针主控与节点项目，当前版本：`0.0.7`。

## 项目结构

- `probe_controller`：探针主控服务（Go）
- `probe_node`：探针节点服务（Go）
- `scripts/install_probe_controller_service.sh`：Linux 主控一键安装脚本（systemd，公网安装入口）
- `probe_controller/internal/core/install_scripts/install_probe_controller_service.sh`：主控安装脚本的内置副本（用于系统设置里生成自包含迁移脚本）
- `probe_controller/internal/core/install_scripts/install_probe_node_service.sh`：Linux 探针节点安装脚本（由主控探针页分发，支持 systemd / 非 systemd）
- `probe_controller/internal/core/install_scripts/install_probe_node_service_windows.ps1`：Windows 探针节点安装脚本（由主控探针页分发，WinSW 服务）
- `scripts/restart_probe_node_service_windows.ps1`：Windows 探针节点服务一键重启脚本
- `doc/`：项目文档

探针节点安装脚本支持变量：
- `RUNTIME_MODE=auto|systemd|manual`（默认 `auto`）
- `MANUAL_ENABLE_BOOT=true|false`（仅 `manual` 模式，默认 `true`，通过 `rc.local` 配置开机启动）
- `PROBE_NODE_ID`、`PROBE_NODE_SECRET`（安装时写入 `/etc/default/probe_node`，探针启动后自动落盘到自身 `data/node_identity.json`）

## Linux 一键安装（主控）

```bash
curl -fsSL https://raw.githubusercontent.com/fengzhanhuaer/CloudHelper/mapledev/scripts/install_probe_controller_service.sh | sudo bash
```

安装完成后会：
- 从 GitHub Releases 拉取最新 `probe_controller` 可执行程序
- 安装到 `/opt/cloudhelper/probe_controller`
- 注册并启动 `probe_controller` systemd 服务

## 一键升级（主控）

重复执行同一条安装命令即可升级。脚本会自动备份旧二进制并重启服务。

## Linux 一键安装（探针节点）

在主控后台的“探针”页面为对应节点点击“安装”，复制生成的 Linux 安装命令执行。

安装完成后会：
- 从 GitHub Releases 拉取最新 `probe_node` 可执行程序
- 安装到 `/opt/cloudhelper/probe_node`
- 自动检测运行环境：有 systemd 则注册服务，无 systemd 则使用 `probe_node-ctl` 管理

可选：

复制命令后可在命令前追加环境变量，例如 `RUNTIME_MODE=systemd` 或 `RUNTIME_MODE=manual MANUAL_ENABLE_BOOT=true`。

## 一键升级（探针节点）

重复执行同一条探针节点安装命令即可升级。脚本会自动备份旧二进制并重启进程/服务。

## Docker 运行（探针节点）

发布流程会同步推送 x86 镜像：

```bash
ghcr.io/fengzhanhuaer/cloudhelper-probe-node:latest
ghcr.io/fengzhanhuaer/cloudhelper-probe-node:v0.0.7
```

使用 Compose 启动：

```bash
cd docker/probe_node
vi compose.yaml
docker compose up -d
```

将探针的 `target_system` 设置为 `docker` 后，主控“探针”页面的安装信息会直接生成完整 `compose.yaml`，其中会分别写入 `PROBE_NODE_ID`、`PROBE_NODE_SECRET`、`PROBE_CONTROLLER_URL` 和 `PROBE_PROXY_BASE_URL`。

Docker Compose 默认使用 host 网络，链路中继或本地代理端口由主控配置决定，容器会直接使用宿主机网络栈监听对应端口。如需在 Linux 容器内使用 TUN/VNet 能力，在 `docker/probe_node/compose.yaml` 的服务下追加：

```yaml
    cap_add:
      - NET_ADMIN
    devices:
      - /dev/net/tun:/dev/net/tun
```

默认数据文件会保存在 `docker/probe_node/compose.yaml` 所在目录下：
- `docker/probe_node/data/`：探针身份、链路缓存、同步配置等持久数据
- `docker/probe_node/logs/`：探针运行日志

Compose 会将 `docker/probe_node/` 目录整体挂载到容器的 `/opt/cloudhelper/probe_node/`。Docker 镜像只作为启动壳，不包含探针二进制；容器首次启动时若同目录下的 `probe_node` 不存在，会优先通过主控 `/api/probe/proxy/download` 拉取 `cloudhelper-probe-node-linux-amd64`，下载地址通过请求头传给主控以避免 URL 编码问题。主控代理不可用时再回退 GitHub Release 直连，并保存到该目录。后续可独立替换或通过探针自身升级该文件，不需要重打 Docker 镜像。

升级与重启约定：
- 正常启动：若 `probe_node/probe_node` 已存在且可执行，启动壳只拉起该文件，不会覆盖。
- 首次安装：若 `probe_node/probe_node` 不存在，启动壳会下载并安装。
- 探针自身升级：升级逻辑会替换 `probe_node/probe_node`，并在容器内原地重启新进程。
- 手动替换二进制：替换 `probe_node/probe_node` 后执行 `docker compose restart`。
- 重新安装：删除 `probe_node/probe_node` 后 `docker compose up -d`，或临时设置 `PROBE_NODE_FORCE_INSTALL=true`。
- 不建议长期保持 `PROBE_NODE_FORCE_INSTALL=true`，否则每次容器重启都会重新下载，可能覆盖探针自身升级后的版本。

可选变量：
- `RELEASE_TAG`：默认 `latest`，可指定 `v0.0.7`
- `PROBE_NODE_DOWNLOAD_URL`：指定自定义探针二进制下载地址
- `PROBE_PROXY_BASE_URL`：指定主控代理基础路径，默认由 `PROBE_CONTROLLER_URL` 推导为 `<controller>/api/probe/proxy`
- `PROBE_NODE_AUTO_INSTALL=false`：关闭缺失时自动安装，缺少二进制会直接退出
- `PROBE_NODE_FORCE_INSTALL=true`：启动时强制重新下载探针二进制
- `PROBE_LOCAL_CONSOLE_ENABLED=true`、`PROBE_LOCAL_LISTEN=0.0.0.0:16032`：需要开放本地控制台时再追加

## Windows 一键安装（探针节点）

在主控后台的“探针”页面为对应节点点击“安装”，复制生成的 Windows PowerShell 命令，并使用管理员权限执行。

默认安装根目录是 `C:\Tools`，实际运行目录统一为 `C:\Tools\probe_node`，并注册 `probe_node` Windows 服务（自动启动）。

执行新脚本升级旧版本时，会自动将旧平铺目录资产迁移到 `INSTALL_DIR\probe_node`（含 `probe_node.exe`、`data/`、`logs/`、WinSW 文件与 `.bak*` 备份）。

可选环境变量：
- `PROBE_NODE_ID`
- `PROBE_NODE_SECRET`
- `PROBE_CONTROLLER_URL`
- `INSTALL_DIR`（默认 `C:\Tools`，运行目录为 `INSTALL_DIR\probe_node`）

## Windows 一键重启（探针节点）

使用管理员权限 PowerShell 执行：

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\restart_probe_node_service_windows.ps1
```

默认重启 `probe_node` Windows 服务。服务名不同时可指定：

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\restart_probe_node_service_windows.ps1 -ServiceName probe_node
```

## 运行验证

```bash
sudo systemctl status probe_controller --no-pager
curl -I http://127.0.0.1:15030/dashboard
curl -s -H "X-Forwarded-Proto: https" http://127.0.0.1:15030/dashboard/status
```

## 常用服务命令

```bash
sudo systemctl status probe_controller --no-pager
sudo systemctl restart probe_controller
sudo systemctl stop probe_controller
sudo journalctl -u probe_controller -f
```

## 认证机制（当前实现）

- 认证方式：Challenge-Response（Ed25519 私钥签名 / 公钥验签）
- 服务端启动自动生成文件：`root_ca.crt.pem`、`root_ca.key.pem`、`admin_public_key.pem`、`admin_key.crt.pem`、`initial_admin_private_key.pem`
- 公开路由：`GET /dashboard`、`GET /dashboard/status`、`GET /dashboard/probes`
- 受保护路由示例：`GET /api/ping`、`GET /api/admin/status`
- 重点：`/dashboard/*` 仅允许公开脱敏指标；默认禁止公开节点号、IP、版本、密钥及其他服务端状态，除非有明确需求评审。

## 本地构建（Windows）

主控：

```powershell
cd probe_controller
.\build.bat
```

探针节点（Linux amd64）：

```powershell
cd probe_node
$env:GOOS="linux"
$env:GOARCH="amd64"
go build -o cloudhelper-probe-node-linux-amd64 .
```

探针节点（Windows amd64）：

```powershell
cd probe_node
$env:GOOS="windows"
$env:GOARCH="amd64"
go build -o cloudhelper-probe-node-windows-amd64.exe .
```

## 文档

- 安装与升级：`doc/install_upgrade.md`
- 认证与安全：`doc/login_requirements.md`
- 模块拆分：`doc/module_split.md`
- 前端拆分：`doc/frontend_split.md`

## 探针节点数据文件

- 探针节点身份：`probe_node/data/node_identity.json`（探针节点运行目录下 `data/`）

## 探针上报链路（WSS）

- 探针启动后会主动连接主控 `wss://<controller>/api/probe`
- 建链前先请求一次性 nonce：`GET /api/probe/nonce`
- 使用 `secret` 对 nonce 做 `HMAC-SHA256`，通过 Header 鉴权：
  - `X-Probe-Node-Id`
  - `X-Probe-Nonce`
  - `X-Probe-Signature`
- 安全约束（强制）：探针 WSS 会话仅允许访问 `/api/probe/*` 探针接口；严禁访问 `/api/admin/*` 私有管理接口，除非经过明确评审和需求变更。
- 主控主动限制：若请求携带 `X-Probe-Node-Id / X-Probe-Nonce / X-Probe-Signature` 任一探针鉴权头，且目标不是 `/api/probe/*`，主控直接拒绝（`403`）。
- 不再使用共享密钥；探针密钥由主控侧配置并用于探针鉴权。
- 探针周期上报：IPv4/IPv6、CPU、内存、磁盘、Swap

