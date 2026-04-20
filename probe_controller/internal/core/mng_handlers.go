package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type mngRegisterRequest struct {
	Username        string `json:"username"`
	Password        string `json:"password"`
	ConfirmPassword string `json:"confirm_password"`
}

type mngLoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func mngEntryHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/mng" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(mngEntryPageHTML))
}

func mngPanelHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/mng/panel" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(mngPanelPageHTML))
}

func mngSettingsHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/mng/settings" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(mngSettingsPageHTML))
}

func mngBootstrapHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	mgr, err := ensureMngAuthManager()
	if err != nil {
		writeMngError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"registered": mgr.registered()})
}

func mngRegisterHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	mgr, err := ensureMngAuthManager()
	if err != nil {
		writeMngError(w, err)
		return
	}

	var req mngRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	if err := mgr.register(req.Username, req.Password, req.ConfirmPassword); err != nil {
		writeMngError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"registered": true,
	})
}

func mngLoginHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	mgr, err := ensureMngAuthManager()
	if err != nil {
		writeMngError(w, err)
		return
	}

	var req mngLoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}

	ip, _ := getClientIP(r)
	token, session, err := mgr.login(ip, req.Username, req.Password)
	if err != nil {
		writeMngError(w, err)
		return
	}
	setMngSessionCookie(w, r, token, session.ExpiresAt)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":         true,
		"username":   session.Username,
		"expires_at": session.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

func mngLogoutHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	mgr, err := ensureMngAuthManager()
	if err != nil {
		writeMngError(w, err)
		return
	}
	token, _ := extractMngSessionToken(r)
	mgr.logoutToken(token)
	clearMngSessionCookie(w, r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func mngSessionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	session, _, err := currentMngSessionFromRequest(r)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"authenticated": false,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"authenticated": true,
		"username":      session.Username,
		"expires_at":    session.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

func mngPanelSummaryHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	payload := map[string]interface{}{
		"uptime":      int(time.Since(serverStartTime).Seconds()),
		"server_time": time.Now().UTC().Format(time.RFC3339),
		"version":     strings.TrimSpace(currentControllerVersion()),
	}
	if strings.TrimSpace(payload["version"].(string)) == "" {
		payload["version"] = "dev"
	}
	writeJSON(w, http.StatusOK, payload)
}

func mngSystemVersionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	current := strings.TrimSpace(currentControllerVersion())
	if current == "" {
		current = "dev"
	}
	repo := releaseRepo()
	resp := map[string]interface{}{
		"current_version":   current,
		"latest_version":    "",
		"release_repo":      repo,
		"upgrade_available": false,
		"message":           "",
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	release, err := fetchLatestRelease(ctx, repo)
	if err != nil {
		resp["message"] = fmt.Sprintf("failed to query latest release: %v", err)
		writeJSON(w, http.StatusOK, resp)
		return
	}

	latest := strings.TrimSpace(release.TagName)
	resp["latest_version"] = latest
	if latest != "" {
		resp["upgrade_available"] = normalizeVersion(current) != normalizeVersion(latest)
	} else {
		resp["message"] = "latest release tag is empty"
	}
	writeJSON(w, http.StatusOK, resp)
}

func mngSystemUpgradeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	result, err := triggerControllerUpgradeTask()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
			"accepted":        false,
			"error":           err.Error(),
			"current_version": result.CurrentVersion,
			"latest_version":  result.LatestVersion,
		})
		return
	}

	accepted := true
	msg := strings.ToLower(strings.TrimSpace(result.Message))
	if strings.Contains(msg, "already running") {
		accepted = false
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"accepted":        accepted,
		"message":         result.Message,
		"current_version": result.CurrentVersion,
		"latest_version":  result.LatestVersion,
		"updated":         result.Updated,
	})
}

func mngSystemUpgradeProgressHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	progress := getControllerUpgradeProgress()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"active":          progress.Active,
		"phase":           progress.Phase,
		"percent":         progress.Percent,
		"message":         progress.Message,
		"current_version": strings.TrimSpace(currentControllerVersion()),
	})
}

func mngSystemReconnectCheckHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":          true,
		"server_time": time.Now().UTC().Format(time.RFC3339),
		"version":     strings.TrimSpace(currentControllerVersion()),
	})
}

const mngEntryPageHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>CloudHelper 管理入口</title>
  <style>
    :root { color-scheme: dark; }
    body { margin:0; font-family:Segoe UI,Roboto,Arial,sans-serif; background:#0d1117; color:#c9d1d9; }
    .wrap { max-width:460px; margin:8vh auto; padding:24px; border:1px solid #30363d; border-radius:12px; background:#161b22; }
    h1 { margin-top:0; font-size:22px; }
    .muted { color:#8b949e; font-size:14px; }
    .hidden { display:none; }
    .field { margin-top:12px; }
    .field label { display:block; margin-bottom:6px; font-size:13px; color:#8b949e; }
    .field input { width:100%; box-sizing:border-box; padding:10px; border-radius:8px; border:1px solid #30363d; background:#0d1117; color:#c9d1d9; }
    button { margin-top:14px; width:100%; padding:10px; border-radius:8px; border:1px solid #2f81f7; background:#1f6feb; color:#fff; font-weight:600; cursor:pointer; }
    .error { margin-top:10px; color:#ff7b72; font-size:13px; min-height:20px; }
    .ok { margin-top:10px; color:#7ee787; font-size:13px; min-height:20px; }
  </style>
</head>
<body>
  <div class="wrap">
    <h1>/mng 独立管理入口</h1>
    <p class="muted">首次访问需注册单账号，注册后仅保留登录。</p>

    <section id="register-box" class="hidden">
      <h2>注册</h2>
      <div class="field"><label>用户名</label><input id="reg-username" autocomplete="username"></div>
      <div class="field"><label>密码</label><input id="reg-password" type="password" autocomplete="new-password"></div>
      <div class="field"><label>确认密码</label><input id="reg-confirm" type="password" autocomplete="new-password"></div>
      <button id="btn-register" type="button">注册</button>
      <div class="error" id="reg-error"></div>
      <div class="ok" id="reg-ok"></div>
    </section>

    <section id="login-box" class="hidden">
      <h2>登录</h2>
      <div class="field"><label>用户名</label><input id="login-username" autocomplete="username"></div>
      <div class="field"><label>密码</label><input id="login-password" type="password" autocomplete="current-password"></div>
      <button id="btn-login" type="button">登录</button>
      <div class="error" id="login-error"></div>
    </section>
  </div>

  <script>
    const registerBox = document.getElementById('register-box');
    const loginBox = document.getElementById('login-box');

    function setMode(registered) {
      registerBox.classList.toggle('hidden', registered);
      loginBox.classList.toggle('hidden', !registered);
    }

    async function loadBootstrap() {
      const res = await fetch('/mng/api/bootstrap', { credentials: 'same-origin' });
      const data = await res.json();
      setMode(!!data.registered);
    }

    async function register() {
      document.getElementById('reg-error').textContent = '';
      document.getElementById('reg-ok').textContent = '';
      const payload = {
        username: document.getElementById('reg-username').value,
        password: document.getElementById('reg-password').value,
        confirm_password: document.getElementById('reg-confirm').value,
      };
      const res = await fetch('/mng/api/register', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'same-origin',
        body: JSON.stringify(payload),
      });
      const data = await res.json();
      if (!res.ok) {
        document.getElementById('reg-error').textContent = data.error || '注册失败';
        return;
      }
      document.getElementById('reg-ok').textContent = '注册成功，请登录';
      setMode(true);
      document.getElementById('login-username').value = payload.username;
      document.getElementById('login-password').value = '';
    }

    async function login() {
      document.getElementById('login-error').textContent = '';
      const payload = {
        username: document.getElementById('login-username').value,
        password: document.getElementById('login-password').value,
      };
      const res = await fetch('/mng/api/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'same-origin',
        body: JSON.stringify(payload),
      });
      const data = await res.json();
      if (!res.ok) {
        document.getElementById('login-error').textContent = data.error || '登录失败';
        return;
      }
      location.href = '/mng/panel';
    }

    document.getElementById('btn-register').addEventListener('click', () => { register().catch(e => {
      document.getElementById('reg-error').textContent = e && e.message ? e.message : '注册失败';
    });});

    document.getElementById('btn-login').addEventListener('click', () => { login().catch(e => {
      document.getElementById('login-error').textContent = e && e.message ? e.message : '登录失败';
    });});

    loadBootstrap().catch(() => {
      setMode(false);
    });
  </script>
</body>
</html>`

const mngPanelPageHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>CloudHelper 管理面板</title>
  <style>
    :root { color-scheme: dark; }
    body { margin:0; font-family:Segoe UI,Roboto,Arial,sans-serif; background:#0d1117; color:#c9d1d9; }
    .wrap { max-width:980px; margin:5vh auto; padding:20px; }
    .topbar { display:flex; justify-content:space-between; align-items:center; gap:12px; margin-bottom:14px; }
    h1 { margin:0; font-size:24px; }
    .sub { color:#8b949e; margin:4px 0 0; font-size:13px; }
    .btn { padding:8px 12px; border-radius:8px; border:1px solid #30363d; background:#21262d; color:#c9d1d9; cursor:pointer; }
    .tiles { display:grid; grid-template-columns:repeat(auto-fill, minmax(220px, 1fr)); gap:12px; }
    .tile { border:1px solid #30363d; border-radius:12px; background:#161b22; padding:16px; min-height:126px; box-sizing:border-box; }
    .tile-title { font-size:15px; font-weight:600; margin-bottom:8px; }
    .tile-value { font-size:24px; font-family:Consolas,monospace; }
    .tile-sub { margin-top:8px; color:#8b949e; font-size:12px; }
    .tile-link { display:block; text-decoration:none; color:#c9d1d9; transition:all .15s ease; }
    .tile-link:hover { border-color:#58a6ff; transform:translateY(-1px); box-shadow:0 4px 16px rgba(31,111,235,.2); }
    .ok { color:#7ee787; }
  </style>
</head>
<body>
  <div class="wrap">
    <div class="topbar">
      <div>
        <h1>/mng 磁贴面板</h1>
        <p class="sub">磁贴数量可扩展，系统设置入口位于下方磁贴。</p>
      </div>
      <button id="btn-logout" class="btn" type="button">退出登录</button>
    </div>

    <div class="tiles">
      <a href="/mng/settings" class="tile tile-link" id="tile-settings">
        <div class="tile-title">系统设置</div>
        <div class="tile-value">⚙</div>
        <div class="tile-sub">进入设置与升级页面</div>
      </a>

      <div class="tile">
        <div class="tile-title">运行状态</div>
        <div class="tile-value ok">在线</div>
        <div class="tile-sub" id="server-time">--</div>
      </div>

      <div class="tile">
        <div class="tile-title">当前版本</div>
        <div class="tile-value" id="version">--</div>
        <div class="tile-sub">Controller Version</div>
      </div>

      <div class="tile">
        <div class="tile-title">运行时长</div>
        <div class="tile-value" id="uptime">--:--:--</div>
        <div class="tile-sub">从服务启动开始计时</div>
      </div>

      <div class="tile">
        <div class="tile-title">当前账号</div>
        <div class="tile-value" id="username">--</div>
        <div class="tile-sub">/mng 独立会话</div>
      </div>
    </div>
  </div>

  <script>
    function formatUptime(seconds) {
      const sec = Number(seconds || 0);
      const h = Math.floor(sec / 3600).toString().padStart(2, '0');
      const m = Math.floor((sec % 3600) / 60).toString().padStart(2, '0');
      const s = Math.floor(sec % 60).toString().padStart(2, '0');
      return h + ':' + m + ':' + s;
    }

    async function loadSummary() {
      const res = await fetch('/mng/api/panel/summary', { credentials: 'same-origin' });
      if (res.status === 401) {
        location.href = '/mng';
        return;
      }
      const data = await res.json();
      if (!res.ok) return;
      document.getElementById('uptime').textContent = formatUptime(data.uptime);
      document.getElementById('version').textContent = data.version || 'dev';
      document.getElementById('server-time').textContent = data.server_time || '--';
    }

    async function loadSession() {
      const res = await fetch('/mng/api/session', { credentials: 'same-origin' });
      const data = await res.json();
      if (!res.ok || !data.authenticated) {
        location.href = '/mng';
        return;
      }
      document.getElementById('username').textContent = data.username || '--';
    }

    async function logout() {
      await fetch('/mng/api/logout', { method: 'POST', credentials: 'same-origin' });
      location.href = '/mng';
    }

    document.getElementById('btn-logout').addEventListener('click', () => { logout().catch(() => { location.href = '/mng'; }); });

    loadSession();
    loadSummary();
    setInterval(loadSummary, 2000);
  </script>
</body>
</html>`

const mngSettingsPageHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>CloudHelper 系统设置</title>
  <style>
    :root { color-scheme: dark; }
    body { margin:0; font-family:Segoe UI,Roboto,Arial,sans-serif; background:#0d1117; color:#c9d1d9; }
    .wrap { max-width:860px; margin:5vh auto; padding:20px; }
    .topbar { display:flex; justify-content:space-between; align-items:center; gap:12px; margin-bottom:14px; }
    h1 { margin:0; font-size:24px; }
    .btn { padding:9px 14px; border-radius:8px; border:1px solid #30363d; background:#21262d; color:#c9d1d9; cursor:pointer; }
    .btn.primary { background:#1f6feb; border-color:#2f81f7; color:#fff; }
    .btn:disabled { opacity:.6; cursor:not-allowed; }
    .panel { border:1px solid #30363d; background:#161b22; border-radius:12px; padding:16px; }
    .muted { color:#8b949e; font-size:13px; }
    .grid { display:grid; grid-template-columns:repeat(auto-fit,minmax(220px,1fr)); gap:10px; margin:14px 0; }
    .item { border:1px solid #30363d; border-radius:10px; background:#0d1117; padding:12px; }
    .label { color:#8b949e; font-size:12px; text-transform:uppercase; }
    .value { margin-top:6px; font-size:20px; font-family:Consolas,monospace; }
    .actions { display:flex; flex-wrap:wrap; gap:10px; margin-top:10px; }
    .status { margin-top:12px; border-radius:8px; background:#0d1117; border:1px solid #30363d; padding:10px; font-size:13px; white-space:pre-wrap; }
    .ok { color:#7ee787; }
    .warn { color:#f2cc60; }
    .err { color:#ff7b72; }
  </style>
</head>
<body>
  <div class="wrap">
    <div class="topbar">
      <h1>/mng/settings</h1>
      <div>
        <button id="btn-back" class="btn" type="button">返回磁贴面板</button>
        <button id="btn-logout" class="btn" type="button">退出登录</button>
      </div>
    </div>

    <div class="panel">
      <p class="muted">系统设置页包含版本检查与主控升级。升级触发后前端将进入延时重连流程。</p>

      <div class="grid">
        <div class="item">
          <div class="label">当前版本</div>
          <div class="value" id="current-version">--</div>
        </div>
        <div class="item">
          <div class="label">最新版本</div>
          <div class="value" id="latest-version">--</div>
        </div>
        <div class="item">
          <div class="label">可升级</div>
          <div class="value" id="upgrade-available">--</div>
        </div>
      </div>

      <div class="actions">
        <button id="btn-check-update" class="btn" type="button">检查更新</button>
        <button id="btn-upgrade" class="btn primary" type="button">主控升级</button>
      </div>

      <div class="status" id="status-box">尚未执行操作。</div>
    </div>
  </div>

  <script>
    const statusBox = document.getElementById('status-box');
    const checkBtn = document.getElementById('btn-check-update');
    const upgradeBtn = document.getElementById('btn-upgrade');
    let progressTimer = null;

    function setStatus(text, level) {
      statusBox.textContent = text;
      statusBox.className = 'status';
      if (level) {
        statusBox.classList.add(level);
      }
    }

    function setBusy(busy) {
      checkBtn.disabled = busy;
      upgradeBtn.disabled = busy;
    }

    async function ensureSession() {
      const res = await fetch('/mng/api/session', { credentials: 'same-origin' });
      const data = await res.json();
      if (!res.ok || !data.authenticated) {
        location.href = '/mng';
        return false;
      }
      return true;
    }

    async function checkUpdate() {
      setStatus('正在检查最新版本...', 'warn');
      const res = await fetch('/mng/api/system/version', { credentials: 'same-origin' });
      if (res.status === 401) {
        location.href = '/mng';
        return;
      }
      const data = await res.json();
      document.getElementById('current-version').textContent = data.current_version || 'dev';
      document.getElementById('latest-version').textContent = data.latest_version || '--';
      document.getElementById('upgrade-available').textContent = data.upgrade_available ? '是' : '否';

      const msg = data.message ? String(data.message) : '';
      if (msg) {
        setStatus('检查完成：' + msg, data.upgrade_available ? 'ok' : 'warn');
      } else {
        setStatus('检查完成。当前版本：' + (data.current_version || '--') + '，最新版本：' + (data.latest_version || '--'), data.upgrade_available ? 'ok' : 'warn');
      }
    }

    async function readUpgradeProgress() {
      const res = await fetch('/mng/api/system/upgrade/progress', { credentials: 'same-origin' });
      if (res.status === 401) {
        return null;
      }
      const data = await res.json();
      return data;
    }

    function sleep(ms) {
      return new Promise(resolve => setTimeout(resolve, ms));
    }

    async function startDelayedReconnect() {
      setStatus('升级任务已提交，等待服务重启中...\n将延时后开始重连检测。', 'warn');
      await sleep(7000);

      for (let attempt = 1; attempt <= 60; attempt++) {
        try {
          const res = await fetch('/mng/api/system/reconnect/check?ts=' + Date.now(), {
            cache: 'no-store',
            credentials: 'omit',
          });
          if (res.ok) {
            setStatus('服务已恢复，正在跳转登录页重新建立会话...', 'ok');
            await sleep(900);
            location.href = '/mng';
            return;
          }
        } catch (_) {
          // ignore and retry
        }
        setStatus('等待服务恢复中，重连检测第 ' + attempt + ' 次...', 'warn');
        await sleep(2000);
      }

      setStatus('重连检测超时，请稍后手动重新访问 /mng。', 'err');
      setBusy(false);
      if (progressTimer) {
        clearInterval(progressTimer);
        progressTimer = null;
      }
    }

    async function triggerUpgrade() {
      setBusy(true);
      setStatus('正在触发主控升级...', 'warn');

      const res = await fetch('/mng/api/system/upgrade', {
        method: 'POST',
        credentials: 'same-origin',
      });
      if (res.status === 401) {
        location.href = '/mng';
        return;
      }
      const data = await res.json();
      if (!res.ok) {
        setStatus('升级触发失败：' + (data.error || 'unknown error'), 'err');
        setBusy(false);
        return;
      }

      const accepted = !!data.accepted;
      const message = data.message ? String(data.message) : 'upgrade request accepted';
      setStatus('升级接口返回：' + message, accepted ? 'ok' : 'warn');

      if (progressTimer) {
        clearInterval(progressTimer);
      }
      progressTimer = setInterval(async () => {
        const progress = await readUpgradeProgress();
        if (!progress) {
          return;
        }
        const text = '升级阶段：' + (progress.phase || '-')
          + '\n进度：' + String(progress.percent || 0) + '%'
          + '\n信息：' + (progress.message || '-');
        setStatus(text, progress.active ? 'warn' : 'ok');
      }, 1500);

      startDelayedReconnect();
    }

    async function logout() {
      await fetch('/mng/api/logout', { method: 'POST', credentials: 'same-origin' });
      location.href = '/mng';
    }

    document.getElementById('btn-back').addEventListener('click', () => { location.href = '/mng/panel'; });
    document.getElementById('btn-logout').addEventListener('click', () => { logout().catch(() => { location.href = '/mng'; }); });
    checkBtn.addEventListener('click', () => { checkUpdate().catch(err => setStatus('检查更新失败：' + (err && err.message ? err.message : 'unknown'), 'err')); });
    upgradeBtn.addEventListener('click', () => { triggerUpgrade().catch(err => { setBusy(false); setStatus('升级失败：' + (err && err.message ? err.message : 'unknown'), 'err'); }); });

    ensureSession().then(ok => {
      if (!ok) return;
      checkUpdate().catch(() => {
        setStatus('初始化版本检查失败，可手动点击“检查更新”重试。', 'warn');
      });
    });
  </script>
</body>
</html>`
