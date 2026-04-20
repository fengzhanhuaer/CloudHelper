package core

import (
	"encoding/json"
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
    .wrap { max-width:700px; margin:8vh auto; padding:24px; border:1px solid #30363d; border-radius:12px; background:#161b22; }
    h1 { margin-top:0; font-size:22px; }
    .grid { display:grid; grid-template-columns:repeat(auto-fit,minmax(220px,1fr)); gap:12px; margin-top:14px; }
    .card { border:1px solid #30363d; border-radius:10px; padding:14px; background:#0d1117; }
    .label { color:#8b949e; font-size:12px; text-transform:uppercase; letter-spacing:.5px; }
    .value { margin-top:6px; font-size:22px; font-family:Consolas,monospace; }
    .topbar { display:flex; justify-content:space-between; align-items:center; gap:12px; }
    button { padding:8px 12px; border-radius:8px; border:1px solid #30363d; background:#21262d; color:#c9d1d9; cursor:pointer; }
  </style>
</head>
<body>
  <div class="wrap">
    <div class="topbar">
      <h1>/mng/panel</h1>
      <button id="btn-logout" type="button">退出登录</button>
    </div>
    <div class="grid">
      <div class="card"><div class="label">Uptime</div><div class="value" id="uptime">--:--:--</div></div>
      <div class="card"><div class="label">Version</div><div class="value" id="version">--</div></div>
      <div class="card"><div class="label">Server Time</div><div class="value" id="server-time">--</div></div>
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

    async function logout() {
      await fetch('/mng/api/logout', { method: 'POST', credentials: 'same-origin' });
      location.href = '/mng';
    }

    document.getElementById('btn-logout').addEventListener('click', () => { logout().catch(() => { location.href = '/mng'; }); });
    loadSummary();
    setInterval(loadSummary, 2000);
  </script>
</body>
</html>`
