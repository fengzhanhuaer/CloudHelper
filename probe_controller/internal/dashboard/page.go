package dashboard

import "net/http"

const pageHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>CloudHelper Probe Controller</title>
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
    </style>
</head>
<body>
    <div class="container">
        <div class="glass-panel">
            <h1>
                <div id="connection-status" class="status-indicator"></div>
                CloudHelper Probe Controller
            </h1>
            <div class="info-grid">
                <div class="info-card">
                    <div class="info-label">Service Status</div>
                    <div class="info-value" id="service-status" style="color: var(--error);">Checking</div>
                </div>
                <div class="info-card">
                    <div class="info-label">Uptime</div>
                    <div class="info-value" id="uptime">--:--:--</div>
                </div>
                <div class="info-card">
                    <div class="info-label">Latency</div>
                    <div class="info-value" id="ping-latency">-- ms</div>
                </div>
            </div>
        </div>
    </div>
    <script>
        const statusIndicator = document.getElementById('connection-status');
        const serviceStatus = document.getElementById('service-status');
        const pingLatency = document.getElementById('ping-latency');
        const uptimeEl = document.getElementById('uptime');

        function formatUptime(seconds) {
            const h = Math.floor(seconds / 3600).toString().padStart(2, '0');
            const m = Math.floor((seconds % 3600) / 60).toString().padStart(2, '0');
            const s = Math.floor(seconds % 60).toString().padStart(2, '0');
            return h + ":" + m + ":" + s;
        }

        async function checkStatus() {
            const startPing = performance.now();
            try {
                const res = await fetch('/dashboard/status');
                if (!res.ok) throw new Error('Not OK');
                const data = await res.json();
                const latency = Math.round(performance.now() - startPing);
                statusIndicator.className = 'status-indicator online';
                serviceStatus.innerText = 'Online';
                serviceStatus.style.color = 'var(--success)';
                pingLatency.innerText = latency + " ms";
                uptimeEl.innerText = formatUptime(data.uptime);
            } catch (err) {
                statusIndicator.className = 'status-indicator';
                serviceStatus.innerText = 'Offline';
                serviceStatus.style.color = 'var(--error)';
            }
        }

        checkStatus();
        setInterval(checkStatus, 2000);
    </script>
</body>
</html>`

func Handler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(pageHTML))
}
