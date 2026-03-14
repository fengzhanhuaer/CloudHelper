# CloudHelper

CloudHelper 是一个探针主控与管理端项目，当前版本：`0.0.7`。

## 项目结构

- `probe_controller`：探针主控服务（Go）
- `probe_manager`：管理端（Wails）
- `scripts/install_probe_controller_service.sh`：Linux 主控一键安装脚本（systemd）
- `doc/`：项目文档

## Linux 一键安装（主控）

```bash
curl -fsSL https://raw.githubusercontent.com/fengzhanhuaer/CloudHelper/main/scripts/install_probe_controller_service.sh | sudo bash
```

安装完成后会：
- 从 GitHub Releases 拉取最新 `probe_controller` 可执行程序
- 安装到 `/opt/cloudhelper/probe_controller`
- 注册并启动 `probe_controller` systemd 服务

## 一键升级（主控）

重复执行同一条安装命令即可升级。脚本会自动备份旧二进制并重启服务。

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
- 公开路由：`GET /dashboard`、`GET /dashboard/status`
- 受保护路由示例：`GET /api/ping`、`GET /api/admin/status`

## 管理端升级（新增）

管理端在「系统设置」页支持两种自身升级方式：

- `直连升级`：管理端直接请求 GitHub Release，下载并升级自身。
- `代理升级`：管理端将项目地址发送给主控，主控通过已鉴权接口代查 Release 与代下载文件并转发给管理端，管理端自行决定是否执行升级。

代理相关接口（主控）：

- `POST /api/admin/proxy/github/latest`
- `GET /api/admin/proxy/download?url=...`

注意：

- 代理接口必须在已登录会话（Bearer Token）下访问。
- 主控代理下载默认允许任意 HTTPS 下载地址。

## 本地构建（Windows）

主控：

```powershell
cd probe_controller
.\build.bat
```

管理端：

```powershell
cd probe_manager
wails build -clean -platform windows/amd64 -o probe_manager -nopackage
```

## 文档

- 安装与升级：`doc/install_upgrade.md`
- 认证与安全：`doc/login_requirements.md`
- 模块拆分：`doc/module_split.md`
- 前端拆分：`doc/frontend_split.md`

