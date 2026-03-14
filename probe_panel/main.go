package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DataStore represents our JSON storage
type DataStore struct {
	mu   sync.RWMutex
	path string
	Data map[string]interface{} `json:"data"`
}

var (
	Store      *DataStore
	serverStartTime time.Time
)

// ========= 纯静态前端内嵌代码 =========
const statusHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>CloudHelper 探针面板</title>
    <style>
        :root {
            --bg-color: #0d1117;
            --panel-bg: rgba(22, 27, 34, 0.6);
            --text-primary: #c9d1d9;
            --text-secondary: #8b949e;
            --accent: #58a6ff;
            --success: #2ea043;
            --error: #f85149;
        }
        body {
            margin: 0;
            padding: 0;
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
            background-color: var(--bg-color);
            color: var(--text-primary);
            display: flex;
            justify-content: center;
            align-items: center;
            min-height: 100vh;
            background-image: radial-gradient(circle at 50% 0%, #1f2937 0%, transparent 70%);
            overflow: hidden;
        }
        .container {
            width: 90%;
            max-width: 650px;
            z-index: 10;
        }
        .glass-panel {
            background: var(--panel-bg);
            backdrop-filter: blur(12px);
            -webkit-backdrop-filter: blur(12px);
            border: 1px solid rgba(255, 255, 255, 0.1);
            border-radius: 16px;
            padding: 32px;
            box-shadow: 0 16px 40px rgba(0, 0, 0, 0.4);
            transition: transform 0.3s ease, box-shadow 0.3s ease;
        }
        .glass-panel:hover {
            transform: translateY(-2px);
            box-shadow: 0 20px 50px rgba(0, 0, 0, 0.5);
        }
        h1 {
            margin-top: 0;
            font-size: 24px;
            font-weight: 600;
            display: flex;
            align-items: center;
            gap: 12px;
            border-bottom: 1px solid rgba(255, 255, 255, 0.1);
            padding-bottom: 20px;
            margin-bottom: 24px;
        }
        .status-indicator {
            width: 14px;
            height: 14px;
            border-radius: 50%;
            background-color: var(--error);
            box-shadow: 0 0 10px var(--error);
            transition: all 0.5s ease;
        }
        .status-indicator.online {
            background-color: var(--success);
            box-shadow: 0 0 12px var(--success);
        }
        .pulse {
            animation: pulse-animation 2s infinite;
        }
        @keyframes pulse-animation {
            0% { transform: scale(0.95); box-shadow: 0 0 0 0 rgba(46, 160, 67, 0.7); }
            70% { transform: scale(1); box-shadow: 0 0 0 10px rgba(46, 160, 67, 0); }
            100% { transform: scale(0.95); box-shadow: 0 0 0 0 rgba(46, 160, 67, 0); }
        }
        .info-grid {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
            gap: 20px;
        }
        .info-card {
            background: rgba(0, 0, 0, 0.25);
            border: 1px solid rgba(255, 255, 255, 0.05);
            border-radius: 12px;
            padding: 20px;
            position: relative;
            overflow: hidden;
        }
        .info-card::before {
            content: '';
            position: absolute;
            top: 0; left: 0; width: 4px; height: 100%;
            background: var(--accent);
            opacity: 0.5;
            border-radius: 4px 0 0 4px;
        }
        .info-label {
            font-size: 13px;
            color: var(--text-secondary);
            text-transform: uppercase;
            letter-spacing: 1px;
            margin-bottom: 10px;
        }
        .info-value {
            font-size: 22px;
            font-weight: 500;
            font-family: 'Courier New', Courier, monospace;
            color: #fff;
        }
        
        /* 装饰背景 */
        .decoration {
            position: absolute;
            width: 300px;
            height: 300px;
            background: var(--accent);
            border-radius: 50%;
            filter: blur(100px);
            opacity: 0.15;
            z-index: 1;
        }
        .decoration.top-right { top: -100px; right: -100px; background: #a371f7; }
        .decoration.bottom-left { bottom: -100px; left: -100px; }
    </style>
</head>
<body>
    <div class="decoration top-right"></div>
    <div class="decoration bottom-left"></div>
    
    <div class="container">
        <div class="glass-panel">
            <h1>
                <div id="connection-status" class="status-indicator"></div>
                CloudHelper 探针节点
                <span style="margin-left:auto; font-size:12px; color:var(--text-secondary); font-weight:normal; font-family:monospace;">v1.0.0-lite</span>
            </h1>
            <div class="info-grid">
                <div class="info-card" style="border-left-color: var(--success);">
                    <div class="info-label">服务状态</div>
                    <div class="info-value" id="service-status" style="color: var(--error);">等待连接</div>
                </div>
                <div class="info-card" style="border-left-color: #a371f7;">
                    <div class="info-label">内核运行时间</div>
                    <div class="info-value" id="uptime">--:--:--</div>
                </div>
                <div class="info-card" style="border-left-color: #f778ba;">
                    <div class="info-label">探针响应延迟</div>
                    <div class="info-value" id="ping-latency">-- ms</div>
                </div>
                <div class="info-card" style="border-left-color: var(--accent);">
                    <div class="info-label">最后心跳时间</div>
                    <div class="info-value" id="last-update">--:--:--</div>
                </div>
            </div>
        </div>
    </div>

    <script>
        const statusIndicator = document.getElementById('connection-status');
        const serviceStatus = document.getElementById('service-status');
        const pingLatency = document.getElementById('ping-latency');
        const uptimeEl = document.getElementById('uptime');
        const lastUpdate = document.getElementById('last-update');
        
        // 格式化秒数为 hh:mm:ss
        function formatUptime(seconds) {
            const h = Math.floor(seconds / 3600).toString().padStart(2, '0');
            const m = Math.floor((seconds % 3600) / 60).toString().padStart(2, '0');
            const s = Math.floor(seconds % 60).toString().padStart(2, '0');
            return h + ":" + m + ":" + s;
        }

        async function checkStatus() {
            const startPing = performance.now();
            try {
                const res = await fetch('/api/ping');
                if (res.ok) {
                    const data = await res.json();
                    const latency = Math.round(performance.now() - startPing);
                    
                    statusIndicator.className = 'status-indicator online pulse';
                    serviceStatus.innerText = '在线守护中';
                    serviceStatus.style.color = 'var(--success)';
                    pingLatency.innerText = latency + " ms";
                    uptimeEl.innerText = formatUptime(data.uptime);
                    
                    const now = new Date();
                    lastUpdate.innerText = now.toLocaleTimeString();
                } else {
                    throw new Error('Not OK');
                }
            } catch (err) {
                statusIndicator.className = 'status-indicator';
                serviceStatus.innerText = '节点离线';
                serviceStatus.style.color = 'var(--error)';
            }
        }

        // 初始检查，然后每 2 秒轮询一次
        checkStatus();
        setInterval(checkStatus, 2000);
    </script>
</body>
</html>`


func initStore() {
	// 确保数据目录存在
	dataDir := "./data"
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Fatalf("failed to create data directory: %v", err)
	}

	dbPath := filepath.Join(dataDir, "cloudhelper.json")
	Store = &DataStore{
		path: dbPath,
		Data: make(map[string]interface{}),
	}

	// Load existing data if file exists
	if _, err := os.Stat(dbPath); err == nil {
		content, err := os.ReadFile(dbPath)
		if err != nil {
			log.Fatalf("failed to read JSON data file: %v", err)
		}
		if len(content) > 0 {
			if err := json.Unmarshal(content, &Store.Data); err != nil {
				log.Fatalf("failed to parse JSON data file: %v", err)
			}
		}
	} else if os.IsNotExist(err) {
		// Create an empty file
		Store.Save()
	} else {
		log.Fatalf("failed to check JSON data file: %v", err)
	}

	log.Println("JSON Datastore initialized at", dbPath)
}

func (s *DataStore) Save() error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	content, err := json.MarshalIndent(s.Data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, content, 0644)
}

// corsMiddleware 简单的CORS中间件处理
func corsMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, accept, origin, Cache-Control, X-Requested-With")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, DELETE")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next(w, r)
	}
}

// pingHandler 处理探针基础健康检查，返回状态和运行时间
func pingHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message": "pong",
		"service": "CloudHelper Probe Panel",
		"uptime":  int(time.Since(serverStartTime).Seconds()),
	})
}

// statusPageHandler 返回内嵌的HTML状态面板
func statusPageHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(statusHTML))
}

func main() {
	// 记录服务启动时间
	serverStartTime = time.Now()

	// 初始化基于JSON的存储
	initStore()

	mux := http.NewServeMux()
	
	// 基础健康检查接口，应用跨域中间件
	mux.HandleFunc("/api/ping", corsMiddleware(pingHandler))
	
	// 提供网页版探针状态面板
	mux.HandleFunc("/", statusPageHandler)

	log.Println("CloudHelper Probe Panel is running at http://127.0.0.1:15030")
	if err := http.ListenAndServe("127.0.0.1:15030", mux); err != nil {
		log.Fatal(err)
	}
}
