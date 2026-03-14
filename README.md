# CloudHelper

CloudHelper 是一个探针主控与管理端项目。

## 一键安装（Linux 主控，systemd 服务）

```bash
curl -fsSL https://raw.githubusercontent.com/fengzhanhuaer/CloudHelper/main/scripts/install_probe_controller_service.sh | sudo bash
```

该命令会：
- 从 GitHub Releases 拉取最新 `probe_controller` 可执行程序
- 安装到 `/opt/cloudhelper/probe_controller`
- 注册并启动 `probe_controller` systemd 服务

## 一键升级

重复执行同一条安装命令即可完成升级。脚本会自动备份旧二进制并重启服务。

## 常用服务命令

```bash
sudo systemctl status probe_controller --no-pager
sudo systemctl restart probe_controller
sudo journalctl -u probe_controller -f
```

## 文档

- 安装与升级：`doc/install_upgrade.md`
- 认证需求：`doc/login_requirements.md`

