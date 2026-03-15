package dashboard

import "net/http"

const pageHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <link rel="icon" type="image/svg+xml" href="data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 64 64'%3E%3Cdefs%3E%3ClinearGradient id='g' x1='0' y1='0' x2='1' y2='1'%3E%3Cstop offset='0%25' stop-color='%2358a6ff'/%3E%3Cstop offset='100%25' stop-color='%232ea043'/%3E%3C/linearGradient%3E%3C/defs%3E%3Crect x='6' y='6' width='52' height='52' rx='12' fill='%230b1628'/%3E%3Cpath d='M22 21L14 32L22 43' stroke='url(%23g)' stroke-width='5' stroke-linecap='round' stroke-linejoin='round' fill='none'/%3E%3Cpath d='M42 21L50 32L42 43' stroke='url(%23g)' stroke-width='5' stroke-linecap='round' stroke-linejoin='round' fill='none'/%3E%3Cpath d='M30 17L34 47' stroke='%239cd0ff' stroke-width='5' stroke-linecap='round'/%3E%3C/svg%3E">
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
        .brand-icon {
            width: 28px;
            height: 28px;
            border-radius: 8px;
            display: inline-flex;
            align-items: center;
            justify-content: center;
            background: linear-gradient(135deg, rgba(88, 166, 255, 0.28), rgba(46, 160, 67, 0.28));
            border: 1px solid rgba(255, 255, 255, 0.2);
            box-shadow: 0 0 14px rgba(88, 166, 255, 0.3);
        }
        .brand-icon svg {
            width: 18px;
            height: 18px;
        }
        .info-grid {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
            gap: 20px;
            margin-bottom: 20px;
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
        .probe-panel-title {
            font-size: 14px;
            color: var(--text-secondary);
            text-transform: uppercase;
            letter-spacing: 1px;
            margin-bottom: 10px;
        }
        .probe-list {
            display: flex;
            flex-direction: column;
            gap: 10px;
            max-height: 320px;
            overflow-y: auto;
            padding-right: 4px;
        }
        .probe-item {
            border: 1px solid rgba(255, 255, 255, 0.08);
            background: rgba(0, 0, 0, 0.2);
            border-radius: 10px;
            padding: 10px 12px;
        }
        .probe-item-head {
            display: flex;
            justify-content: space-between;
            align-items: center;
            margin-bottom: 6px;
            gap: 8px;
        }
        .probe-node-id {
            font-size: 14px;
            color: #ffffff;
            font-family: 'Courier New', Courier, monospace;
        }
        .probe-online {
            font-size: 12px;
            padding: 2px 8px;
            border-radius: 999px;
            border: 1px solid rgba(255, 255, 255, 0.16);
        }
        .probe-online.yes {
            color: #7fe0a0;
            border-color: rgba(46, 160, 67, 0.5);
        }
        .probe-online.no {
            color: #ff9b94;
            border-color: rgba(248, 81, 73, 0.5);
        }
        .probe-metrics {
            display: grid;
            grid-template-columns: repeat(2, minmax(120px, 1fr));
            gap: 4px 12px;
            font-size: 12px;
            color: var(--text-primary);
            font-family: 'Courier New', Courier, monospace;
        }
        .probe-last-seen {
            margin-top: 6px;
            color: var(--text-secondary);
            font-size: 12px;
            font-family: 'Courier New', Courier, monospace;
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="glass-panel">
            <h1>
                <div class="brand-icon" aria-hidden="true">
                    <svg viewBox="0 0 24 24" fill="none" xmlns="http://www.w3.org/2000/svg">
                        <path d="M8 7L4 12L8 17" stroke="#9cd0ff" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>
                        <path d="M16 7L20 12L16 17" stroke="#9cd0ff" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>
                        <path d="M11 5L13 19" stroke="#4ad08f" stroke-width="2" stroke-linecap="round"/>
                    </svg>
                </div>
                CloudHelper Probe Controller
            </h1>
            <div class="info-grid">
                <div class="info-card">
                    <div class="info-label">Uptime</div>
                    <div class="info-value" id="uptime">--:--:--</div>
                </div>
            </div>
            <div class="probe-panel-title">Probe Runtime Metrics (Public, Desensitized)</div>
            <div id="probe-list" class="probe-list">
                <div class="probe-item">No probe data</div>
            </div>
        </div>
    </div>
    <script>
        const uptimeEl = document.getElementById('uptime');
        const probeListEl = document.getElementById('probe-list');

        function formatUptime(seconds) {
            const h = Math.floor(seconds / 3600).toString().padStart(2, '0');
            const m = Math.floor((seconds % 3600) / 60).toString().padStart(2, '0');
            const s = Math.floor(seconds % 60).toString().padStart(2, '0');
            return h + ":" + m + ":" + s;
        }

        async function checkStatus() {
            try {
                const res = await fetch('/dashboard/status');
                if (!res.ok) throw new Error('Not OK');
                const data = await res.json();
                uptimeEl.innerText = formatUptime(data.uptime);
            } catch (err) {
                // keep last known uptime
            }
        }

        function pct(v) {
            if (typeof v !== 'number' || Number.isNaN(v)) return '--';
            return v.toFixed(1) + '%';
        }

        function renderProbes(items) {
            if (!Array.isArray(items) || items.length === 0) {
                probeListEl.innerHTML = '<div class="probe-item">No probe data</div>';
                return;
            }
            probeListEl.innerHTML = items.map((item, index) => {
                const sys = item.system || {};
                const onlineClass = item.online ? 'yes' : 'no';
                const onlineText = item.online ? 'online' : 'offline';
                const name = (item.node_name && String(item.node_name).trim()) ? String(item.node_name).trim() : ('probe #' + String(index + 1));
                const lastSeen = item.last_seen ? String(item.last_seen) : '-';
                return '<div class="probe-item">'
                    + '<div class="probe-item-head">'
                    + '<div class="probe-node-id">' + name + '</div>'
                    + '<div class="probe-online ' + onlineClass + '">' + onlineText + '</div>'
                    + '</div>'
                    + '<div class="probe-metrics">'
                    + '<div>CPU: ' + pct(sys.cpu_percent) + '</div>'
                    + '<div>RAM: ' + pct(sys.memory_used_percent) + '</div>'
                    + '<div>SWAP: ' + pct(sys.swap_used_percent) + '</div>'
                    + '<div>DISK: ' + pct(sys.disk_used_percent) + '</div>'
                    + '</div>'
                    + '<div class="probe-last-seen">Last Seen: ' + lastSeen + '</div>'
                    + '</div>';
            }).join('');
        }

        async function checkProbes() {
            try {
                const res = await fetch('/dashboard/probes');
                if (!res.ok) throw new Error('Not OK');
                const data = await res.json();
                renderProbes(data.items || []);
            } catch (err) {
                // keep old view
            }
        }

        checkStatus();
        checkProbes();
        setInterval(checkStatus, 2000);
        setInterval(checkProbes, 5000);
    </script>
</body>
</html>`

func Handler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(pageHTML))
}
