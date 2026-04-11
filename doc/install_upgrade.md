# CloudHelper 主控安装与升级（Linux 服务版）

## 1. 一键安装命令（从 GitHub 复制）

```bash
curl -fsSL https://raw.githubusercontent.com/fengzhanhuaer/CloudHelper/main/scripts/install_probe_controller_service.sh | sudo bash
```

说明：
- 仅安装 `probe_controller`（主控）
- 从 **GitHub Releases** 拉取最新可执行程序（不再本地编译）
- 自动注册为 `systemd` 服务并启动
- 重复执行同一命令即可升级

## 2. 可选自定义参数

```bash
curl -fsSL https://raw.githubusercontent.com/fengzhanhuaer/CloudHelper/main/scripts/install_probe_controller_service.sh \
| sudo RELEASE_REPO=fengzhanhuaer/CloudHelper \
       RELEASE_TAG=latest \
       ASSET_NAME= \
       INSTALL_DIR=/opt/cloudhelper/probe_controller \
       SERVICE_NAME=probe_controller \
       SERVICE_USER=cloudhelper \
       SERVICE_GROUP=cloudhelper \
       bash
```

默认值：
- `RELEASE_REPO=fengzhanhuaer/CloudHelper`
- `RELEASE_TAG=latest`
- `ASSET_NAME=`（空表示自动匹配 Linux 资产）
- `INSTALL_DIR=/opt/cloudhelper/probe_controller`
- `SERVICE_NAME=probe_controller`
- `SERVICE_USER=cloudhelper`
- `SERVICE_GROUP=cloudhelper`

提示：
- 如果自动匹配不到 Linux 资产，请显式指定 `ASSET_NAME`。
- 私有仓库或 API 受限时，可设置 `GITHUB_TOKEN`。

## 3. 安装结果
脚本执行完成后：
- 二进制路径：`/opt/cloudhelper/probe_controller/probe_controller`
- 数据目录：`/opt/cloudhelper/probe_controller/data`
- 服务文件：`/etc/systemd/system/probe_controller.service`
- 环境变量文件：`/etc/default/probe_controller`

首次启动会自动生成：
- `cloudhelper.json`
- `blacklist.json`
- `root_ca.crt.pem`
- `root_ca.key.pem`
- `admin_public_key.pem`
- `admin_key.crt.pem`
- `initial_admin_private_key.pem`

## 4. 服务管理命令

```bash
sudo systemctl status probe_controller --no-pager
sudo systemctl restart probe_controller
sudo systemctl stop probe_controller
sudo journalctl -u probe_controller -f
```

## 5. 升级流程
主控升级直接重复执行一键安装命令即可。

脚本会：
- 拉取目标 Release（默认 latest）
- 备份旧二进制（`probe_controller.bak.<timestamp>`）
- 重启 systemd 服务
- 保留原有 `data/` 数据

## 6. Challenge-Response 注意事项
系统仅允许 Challenge-Response 登录，且使用“私钥签名 / 公钥验签”模型。

服务端行为：
- 启动时自动生成 Root CA（`root_ca.key.pem` + `root_ca.crt.pem`）
- 自动签发管理员密钥对与管理员证书
- 服务端只使用 `admin_public_key.pem` 验签

建议操作：
1. 首次启动后立刻备份 `initial_admin_private_key.pem` 到安全位置（管理员客户端侧）
2. 确认客户端可用私钥完成签名登录
3. 删除服务端本地 `initial_admin_private_key.pem`

## 7. HTTPS 反向代理要求
主控 `/api/*` 路由已强制 HTTPS。

若使用 Nginx 反向代理，请确保转发：

```nginx
location / {
    proxy_pass http://127.0.0.1:15030;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto https;
}
```

否则接口可能返回：`426 https is required`。

## 8. 常见问题

### 8.1 登录返回 503
错误：`challenge-response unavailable: admin public key is not loaded`

处理：
- 检查 `data/admin_public_key.pem` 与 `cloudhelper.json` 中 `admin_public_key`
- 若缺失，恢复备份或重新初始化生成

### 8.2 /api/ping 返回 401
这是预期行为：`/api/ping` 已要求 Bearer Token。

### 8.3 资产匹配失败
错误特征：找不到 `probe_controller` 的 Linux 可执行资产。

处理：
- 在 Release 中确认资产名称
- 通过 `ASSET_NAME=<实际文件名>` 显式指定

## 9. Windows 探针节点安装与迁移（WinSW）

使用管理员权限 PowerShell 执行：

```powershell
iwr -UseBasicParsing "https://raw.githubusercontent.com/fengzhanhuaer/CloudHelper/main/scripts/install_probe_node_service_windows.ps1" | iex
```

默认行为：

- 安装根目录：`C:\Tools`（可通过 `INSTALL_DIR` 覆盖）
- 运行目录：`INSTALL_DIR\probe_node`
- 服务名：`probe_node`
- WinSW 与探针二进制统一落在运行目录中

目录结构（示例）：

```text
C:\Tools\probe_node\
  probe_node.exe
  probe_node-service.exe
  probe_node-service.xml
  data\
  logs\
```

升级与迁移行为：

- 重复执行安装脚本即可升级
- 若存在旧平铺布局（如 `INSTALL_DIR\probe_node.exe`、`INSTALL_DIR\logs`、`INSTALL_DIR\data`），脚本会自动迁移到 `INSTALL_DIR\probe_node`
- 迁移过程遇到同名冲突时，旧文件会自动改名为 `*.legacy.<timestamp>.*` 保留

常用检查命令（cmd）：

```cmd
sc query probe_node
dir C:\Tools\probe_node
```
