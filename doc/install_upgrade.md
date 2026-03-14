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
- `initial_key.log`

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
系统仅允许 Challenge-Response 登录。

请确保主控能够读取明文密钥（任一方式）：
- `data/initial_key.log` 存在且内容与 `admin_key_hash` 匹配
- 在 `/etc/default/probe_controller` 中设置：

```bash
CLOUDHELPER_ADMIN_KEY=你的明文密钥
```

修改后执行：

```bash
sudo systemctl restart probe_controller
```

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
错误：`challenge-response unavailable: admin secret is not loaded`

处理：
- 检查 `data/initial_key.log`
- 或设置 `CLOUDHELPER_ADMIN_KEY` 后重启服务

### 8.2 /api/ping 返回 401
这是预期行为：`/api/ping` 已要求 Bearer Token。

### 8.3 资产匹配失败
错误特征：找不到 `probe_controller` 的 Linux 可执行资产。

处理：
- 在 Release 中确认资产名称
- 通过 `ASSET_NAME=<实际文件名>` 显式指定

