# CloudHelper 架构设计文档

本项目采用 **C/S 架构**（Client-Server 模式），结合现代化的轻量级桌面开发框架构建。

## 1. 技术栈选型

- **桌面客户端 (Desktop Client)**: 采用 **Wails** (https://wails.io/)
  - **前端视图层**: React 或 Vue3 + TypeScript + TailwindCSS。负责构建现代化且高响应速度的桌面 UI。
  - **本地逻辑层**: **Go**。作为 Wails 的后端处理本地文件系统读取、SSH 隧道建立、以及本地配置加密存储等重性能与系统级操作。
  - *优势*：相较于 Electron，Wails 利用系统原生 WebView 渲染，打包体积极小，内存占用低，且能发挥 Go 语言的高并发和跨平台编译优势。

- **探针主控中心服务 (Probe Controller)**: 采用 **Go** 生态构建
  - **Web 框架**: Gin 或 Fiber，提供 RESTful API 及 WebSocket 推送。
  - **数据存储**: SQLite (业务数据，轻量、无需额外维护) + 内存缓存 (替代 Redis 简化部署)。

## 2. 系统架构图设计

```mermaid
graph TD
    subgraph "Desktop Client (Wails)"
        UI[Frontend UI: React/Vue3]
        LocalGo[Wails Go Backend]
        LocalDB[(SQLite/Keyring\nLocal Config & Keys)]
        
        UI <-->|JS Bridge\nEvents & Methods| LocalGo
        LocalGo <--> LocalDB
    end

    subgraph "Probe Controller Service (Go)"
        Gateway[API Gateway / Load Balancer]
        AuthService[Auth & User Service]
        AssetService[Cloud Asset Service]
        TermProxy[SSH & WebSocket Proxy]
        DB[(SQLite)]
        Cache[(Memory/Local Cache)]

        Gateway --> AuthService
        Gateway --> AssetService
        Gateway --> TermProxy
        AuthService --> DB
        AssetService --> DB
        AssetService --> Cache
    end

    subgraph "External Cloud Providers"
        AWS[AWS API]
        Aliyun[Aliyun API]
        Tencent[Tencent Cloud API]
    end

    subgraph "Target Remote Servers"
        VPS[Linux VPS / EC2]
    end

    %% Connections
    LocalGo <-->|HTTP/REST| Gateway
    LocalGo <-->|WebSocket| TermProxy
    AssetService <-->|API Calls| AWS
    AssetService <-->|API Calls| Aliyun
    AssetService <-->|API Calls| Tencent
    TermProxy <-->|SSH Protocol| VPS
    LocalGo <-->|Direct SSH (Optional)| VPS
```

## 3. 核心交互流程

1. **安全与认证**:
   - 用户在 Wails 客户端登录。
   - 登录获取 JWT Token 并在本地操作系统自带的安全凭证管理器 (Keyring/Keychain) 中加密保存。
2. **直连 vs 代理**:
   - 对于普通的 API 请求与资产查询，请求发送至云端 Probe Controller 中心。
   - 对于 SSH 终端：可根据配置选择客户端 Go 进程**直接连接**远程主机，或者通过 Probe Controller 作为**跳板机/堡垒机代理**访问（适合闭环网络）。
3. **性能与监控**:
   - 服务器状态监控数据通过 WebSocket 从云端主动推送到 Wails 客户端前端视图进行动态渲染。
