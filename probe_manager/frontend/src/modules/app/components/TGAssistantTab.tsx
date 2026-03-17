import { useEffect, useMemo, useState } from "react";
import {
  addTGAssistantAccount,
  completeTGAssistantLogin,
  fetchTGAssistantAccounts,
  logoutTGAssistantAccount,
  refreshTGAssistantAccounts,
  removeTGAssistantAccount,
  sendTGAssistantLoginCode,
} from "../services/controller-api";
import type { TGAssistantAccount } from "../types";

type TGAssistantTabProps = {
  controllerBaseUrl: string;
  sessionToken: string;
};

export function TGAssistantTab(props: TGAssistantTabProps) {
  const [accounts, setAccounts] = useState<TGAssistantAccount[]>([]);
  const [status, setStatus] = useState("正在加载 TG 账号列表...");
  const [isLoading, setIsLoading] = useState(false);

  const [labelInput, setLabelInput] = useState("");
  const [phoneInput, setPhoneInput] = useState("");
  const [apiIDInput, setAPIIDInput] = useState("");
  const [apiHashInput, setAPIHashInput] = useState("");

  const [selectedAccountID, setSelectedAccountID] = useState("");
  const [activeLoggedInAccountID, setActiveLoggedInAccountID] = useState("");
  const [codeInput, setCodeInput] = useState("");
  const [passwordInput, setPasswordInput] = useState("");

  const loggedInAccounts = useMemo(() => accounts.filter((item) => item.authorized), [accounts]);
  const selectedAccount = useMemo(
    () => accounts.find((item) => item.id === selectedAccountID) ?? null,
    [accounts, selectedAccountID],
  );
  const activeLoggedInAccount = useMemo(
    () => loggedInAccounts.find((item) => item.id === activeLoggedInAccountID) ?? null,
    [loggedInAccounts, activeLoggedInAccountID],
  );

  useEffect(() => {
    void loadAccounts({ silent: false });
  }, [props.controllerBaseUrl, props.sessionToken]);

  function applyAccountList(nextAccounts: TGAssistantAccount[]) {
    const ordered = [...nextAccounts];
    setAccounts(ordered);
    setSelectedAccountID((prev) => (ordered.some((item) => item.id === prev) ? prev : (ordered[0]?.id ?? "")));
    const loggedIn = ordered.filter((item) => item.authorized);
    setActiveLoggedInAccountID((prev) => (loggedIn.some((item) => item.id === prev) ? prev : (loggedIn[0]?.id ?? "")));
  }

  async function loadAccounts(options?: { silent?: boolean }) {
    const silent = options?.silent === true;
    if (!silent) {
      setIsLoading(true);
      setStatus("正在加载 TG 账号列表...");
    }
    try {
      const items = await fetchTGAssistantAccounts(props.controllerBaseUrl, props.sessionToken);
      applyAccountList(items);
      if (!silent) {
        setStatus(items.length > 0 ? `已加载 TG 账号（${items.length} 个）` : "暂无 TG 账号，请先添加");
      }
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      if (!silent) {
        setStatus(`加载 TG 账号失败：${msg}`);
      }
    } finally {
      if (!silent) {
        setIsLoading(false);
      }
    }
  }

  async function handleRefreshAccounts() {
    setIsLoading(true);
    setStatus("正在刷新 TG 登录状态...");
    try {
      const items = await refreshTGAssistantAccounts(props.controllerBaseUrl, props.sessionToken);
      applyAccountList(items);
      setStatus(`刷新完成：共 ${items.length} 个账号`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`刷新状态失败：${msg}`);
    } finally {
      setIsLoading(false);
    }
  }

  async function handleAddAccount() {
    const phone = phoneInput.trim();
    const apiHash = apiHashInput.trim();
    const apiID = Number.parseInt(apiIDInput.trim(), 10);
    if (!phone) {
      setStatus("请输入手机号（建议包含国家码，例如 +886xxxxxxxxx）");
      return;
    }
    if (!Number.isFinite(apiID) || apiID <= 0) {
      setStatus("API ID 必须是正整数");
      return;
    }
    if (!apiHash) {
      setStatus("请输入 API Hash");
      return;
    }

    setIsLoading(true);
    setStatus("正在添加 TG 账号...");
    try {
      const account = await addTGAssistantAccount(props.controllerBaseUrl, props.sessionToken, {
        label: labelInput.trim(),
        phone,
        api_id: apiID,
        api_hash: apiHash,
      });
      setLabelInput("");
      setPhoneInput("");
      setAPIIDInput("");
      setAPIHashInput("");
      await loadAccounts({ silent: true });
      setSelectedAccountID(account.id);
      setStatus(`账号已添加：${account.label}（${account.phone}）`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`添加账号失败：${msg}`);
    } finally {
      setIsLoading(false);
    }
  }

  async function handleSendCode() {
    if (!selectedAccount) {
      setStatus("请先选择一个账号");
      return;
    }

    setIsLoading(true);
    setStatus(`正在发送验证码：${selectedAccount.label}...`);
    try {
      await sendTGAssistantLoginCode(props.controllerBaseUrl, props.sessionToken, selectedAccount.id);
      await loadAccounts({ silent: true });
      setStatus(`验证码已发送到 ${selectedAccount.phone}，请填写验证码完成登录`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`发送验证码失败：${msg}`);
    } finally {
      setIsLoading(false);
    }
  }

  async function handleSignIn() {
    if (!selectedAccount) {
      setStatus("请先选择一个账号");
      return;
    }
    const code = codeInput.trim();
    if (!code) {
      setStatus("请输入验证码");
      return;
    }

    setIsLoading(true);
    setStatus(`正在登录账号：${selectedAccount.label}...`);
    try {
      const account = await completeTGAssistantLogin(props.controllerBaseUrl, props.sessionToken, {
        account_id: selectedAccount.id,
        code,
        password: passwordInput,
      });
      setCodeInput("");
      setPasswordInput("");
      await loadAccounts({ silent: true });
      if (account.authorized) {
        setActiveLoggedInAccountID(account.id);
      }
      setStatus(`登录成功：${account.label}`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`登录失败：${msg}`);
    } finally {
      setIsLoading(false);
    }
  }

  async function handleLogout() {
    if (!selectedAccount) {
      setStatus("请先选择一个账号");
      return;
    }

    setIsLoading(true);
    setStatus(`正在登出账号：${selectedAccount.label}...`);
    try {
      await logoutTGAssistantAccount(props.controllerBaseUrl, props.sessionToken, selectedAccount.id);
      await loadAccounts({ silent: true });
      setStatus(`已登出账号：${selectedAccount.label}`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`登出失败：${msg}`);
    } finally {
      setIsLoading(false);
    }
  }

  async function handleRemove() {
    if (!selectedAccount) {
      setStatus("请先选择一个账号");
      return;
    }
    if (!window.confirm(`确认删除账号 ${selectedAccount.label} (${selectedAccount.phone}) 吗？`)) {
      return;
    }

    setIsLoading(true);
    setStatus(`正在删除账号：${selectedAccount.label}...`);
    try {
      const next = await removeTGAssistantAccount(props.controllerBaseUrl, props.sessionToken, selectedAccount.id);
      applyAccountList(next);
      setCodeInput("");
      setPasswordInput("");
      setStatus(`账号已删除：${selectedAccount.label}`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`删除账号失败：${msg}`);
    } finally {
      setIsLoading(false);
    }
  }

  return (
    <div className="content-block">
      <h2>TG助手</h2>

      <div className="identity-card" style={{ marginBottom: 12 }}>
        <div>已登录账号 Tab</div>
        {loggedInAccounts.length === 0 ? (
          <div>当前没有已登录账号。</div>
        ) : (
          <>
            <div className="subtab-list">
              {loggedInAccounts.map((item) => (
                <button
                  key={item.id}
                  className={`subtab-btn ${activeLoggedInAccountID === item.id ? "active" : ""}`}
                  onClick={() => setActiveLoggedInAccountID(item.id)}
                  disabled={isLoading}
                >
                  {item.label}
                </button>
              ))}
            </div>

            <div className="tg-account-summary">
              <div>当前账号：{activeLoggedInAccount?.label || "-"}</div>
              <div>手机号：{activeLoggedInAccount?.phone || "-"}</div>
              <div>显示名：{activeLoggedInAccount?.self_display_name || "-"}</div>
              <div>用户名：{activeLoggedInAccount?.self_username || "-"}</div>
              <div>最近登录：{formatDateTime(activeLoggedInAccount?.last_login_at || "")}</div>
            </div>
          </>
        )}
      </div>

      <div className="identity-card" style={{ marginBottom: 12 }}>
        <div>账号管理（添加 + 登录）</div>
        <div style={{ marginTop: 4, color: "#dceaff" }}>添加账号</div>
        <div className="row" style={{ marginBottom: 0 }}>
          <label>账号备注</label>
          <input
            className="input"
            value={labelInput}
            onChange={(event) => setLabelInput(event.target.value)}
            placeholder="例如：运营主号"
            disabled={isLoading}
          />
        </div>
        <div className="row" style={{ marginBottom: 0 }}>
          <label>手机号</label>
          <input
            className="input"
            value={phoneInput}
            onChange={(event) => setPhoneInput(event.target.value)}
            placeholder="例如：+886912345678"
            disabled={isLoading}
          />
        </div>
        <div className="row" style={{ marginBottom: 0 }}>
          <label>API ID</label>
          <input
            className="input"
            value={apiIDInput}
            onChange={(event) => setAPIIDInput(event.target.value)}
            placeholder="来自 my.telegram.org"
            disabled={isLoading}
          />
        </div>
        <div className="row" style={{ marginBottom: 0 }}>
          <label>API Hash</label>
          <input
            className="input"
            value={apiHashInput}
            onChange={(event) => setAPIHashInput(event.target.value)}
            placeholder="来自 my.telegram.org"
            disabled={isLoading}
          />
        </div>
        <div className="content-actions">
          <button className="btn" onClick={() => void handleAddAccount()} disabled={isLoading}>添加账号</button>
          <button className="btn" onClick={() => void handleRefreshAccounts()} disabled={isLoading}>刷新状态</button>
        </div>
        <div style={{ marginTop: 8, color: "#dceaff" }}>账号登录与管理</div>
        <div className="row" style={{ marginBottom: 0 }}>
          <label>选择账号</label>
          <select
            className="input"
            value={selectedAccountID}
            onChange={(event) => setSelectedAccountID(event.target.value)}
            disabled={isLoading || accounts.length === 0}
          >
            {accounts.length === 0 ? (
              <option value="">暂无账号</option>
            ) : (
              accounts.map((item) => (
                <option key={item.id} value={item.id}>
                  {item.label} ({item.phone}) {item.authorized ? "[已登录]" : "[未登录]"}
                </option>
              ))
            )}
          </select>
        </div>
        <div>登录状态：{selectedAccount?.authorized ? "已登录" : "未登录"}</div>
        <div>验证码状态：{selectedAccount?.pending_code ? "已发送，待验证" : "未发送"}</div>
        <div>最后错误：{selectedAccount?.last_error || "-"}</div>
        <div className="row" style={{ marginBottom: 0 }}>
          <label>验证码</label>
          <input
            className="input"
            value={codeInput}
            onChange={(event) => setCodeInput(event.target.value)}
            placeholder="输入短信/APP 验证码"
            disabled={isLoading || !selectedAccount}
          />
        </div>
        <div className="row" style={{ marginBottom: 0 }}>
          <label>2FA密码</label>
          <input
            className="input"
            type="password"
            value={passwordInput}
            onChange={(event) => setPasswordInput(event.target.value)}
            placeholder="如账号开启两步验证则必填"
            disabled={isLoading || !selectedAccount}
          />
        </div>
        <div className="content-actions">
          <button className="btn" onClick={() => void handleSendCode()} disabled={isLoading || !selectedAccount}>发送验证码</button>
          <button className="btn" onClick={() => void handleSignIn()} disabled={isLoading || !selectedAccount}>完成登录</button>
          <button className="btn" onClick={() => void handleLogout()} disabled={isLoading || !selectedAccount}>登出账号</button>
          <button className="btn" onClick={() => void handleRemove()} disabled={isLoading || !selectedAccount}>删除账号</button>
        </div>
      </div>

      <div className="status">{status}</div>
    </div>
  );
}

function formatDateTime(raw: string): string {
  const value = raw.trim();
  if (!value) {
    return "-";
  }
  const dt = new Date(value);
  if (Number.isNaN(dt.getTime())) {
    return value;
  }
  return dt.toLocaleString();
}
