import { useEffect, useMemo, useState } from "react";
import {
  addTGAssistantAccount,
  completeTGAssistantLogin,
  fetchTGAssistantAPIKey,
  fetchTGAssistantAccounts,
  logoutTGAssistantAccount,
  refreshTGAssistantAccounts,
  removeTGAssistantAccount,
  setTGAssistantAPIKey,
  sendTGAssistantLoginCode,
} from "../services/controller-api";
import type { TGAssistantAPIKey, TGAssistantAccount } from "../types";

type TGAssistantTabProps = {
  controllerBaseUrl: string;
  sessionToken: string;
};

export function TGAssistantTab(props: TGAssistantTabProps) {
  const [accounts, setAccounts] = useState<TGAssistantAccount[]>([]);
  const [status, setStatus] = useState("正在加载 TG 账号列表...");
  const [isLoading, setIsLoading] = useState(false);

  const [showAddAccountModal, setShowAddAccountModal] = useState(false);
  const [addAccountSelectedID, setAddAccountSelectedID] = useState("");
  const [addAccountLabelDraft, setAddAccountLabelDraft] = useState("");
  const [addAccountPhoneDraft, setAddAccountPhoneDraft] = useState("");
  const [sharedApiIDInput, setSharedApiIDInput] = useState("");
  const [sharedApiHashInput, setSharedApiHashInput] = useState("");
  const [sharedAPIConfigured, setSharedAPIConfigured] = useState(false);
  const [showAPIKeyModal, setShowAPIKeyModal] = useState(false);
  const [apiKeyDraftID, setAPIKeyDraftID] = useState("");
  const [apiKeyDraftHash, setAPIKeyDraftHash] = useState("");

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
  const loginStep = useMemo(() => {
    if (!selectedAccount) {
      return 1;
    }
    if (selectedAccount.authorized) {
      return 3;
    }
    if (selectedAccount.pending_code) {
      return 2;
    }
    return 1;
  }, [selectedAccount]);

  useEffect(() => {
    void loadData({ silent: false });
  }, [props.controllerBaseUrl, props.sessionToken]);

  function applyAccountList(nextAccounts: TGAssistantAccount[]) {
    const ordered = [...nextAccounts];
    setAccounts(ordered);
    setSelectedAccountID((prev) => (ordered.some((item) => item.id === prev) ? prev : ""));
    const loggedIn = ordered.filter((item) => item.authorized);
    setActiveLoggedInAccountID((prev) => (loggedIn.some((item) => item.id === prev) ? prev : (loggedIn[0]?.id ?? "")));
  }

  function applyAPIKey(apiKey: TGAssistantAPIKey) {
    setSharedApiIDInput(apiKey.api_id > 0 ? String(apiKey.api_id) : "");
    setSharedApiHashInput(apiKey.api_hash || "");
    setSharedAPIConfigured(apiKey.configured);
  }

  async function loadData(options?: { silent?: boolean }) {
    const silent = options?.silent === true;
    if (!silent) {
      setIsLoading(true);
      setStatus("正在加载 TG 设置与账号...");
    }
    try {
      const [apiKey, accountsData] = await Promise.all([
        fetchTGAssistantAPIKey(props.controllerBaseUrl, props.sessionToken),
        fetchTGAssistantAccounts(props.controllerBaseUrl, props.sessionToken),
      ]);
      applyAPIKey(apiKey);
      applyAccountList(accountsData);
      if (!silent) {
        if (!apiKey.configured) {
          setStatus("请先设置API，再添加/登录账号");
        } else {
          setStatus(accountsData.length > 0 ? `已加载 TG 账号（${accountsData.length} 个）` : "暂无 TG 账号，请先添加");
        }
      }
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      if (!silent) {
        setStatus(`加载 TG 数据失败：${msg}`);
      }
    } finally {
      if (!silent) {
        setIsLoading(false);
      }
    }
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
      const [apiKey, items] = await Promise.all([
        fetchTGAssistantAPIKey(props.controllerBaseUrl, props.sessionToken),
        refreshTGAssistantAccounts(props.controllerBaseUrl, props.sessionToken),
      ]);
      applyAPIKey(apiKey);
      applyAccountList(items);
      setStatus(`刷新完成：共 ${items.length} 个账号`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`刷新状态失败：${msg}`);
    } finally {
      setIsLoading(false);
    }
  }

  async function handleSaveSharedAPIKey() {
    const apiID = Number.parseInt(apiKeyDraftID.trim(), 10);
    const apiHash = apiKeyDraftHash.trim();
    if (!Number.isFinite(apiID) || apiID <= 0) {
      setStatus("共用 API ID 必须是正整数");
      return;
    }
    if (!apiHash) {
      setStatus("共用 API Hash 不能为空");
      return;
    }

    setIsLoading(true);
    setStatus("正在设置API...");
    try {
      const result = await setTGAssistantAPIKey(props.controllerBaseUrl, props.sessionToken, {
        api_id: apiID,
        api_hash: apiHash,
      });
      applyAPIKey(result);
      setShowAPIKeyModal(false);
      setStatus("API已保存，所有账号将使用该配置");
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`设置API失败：${msg}`);
    } finally {
      setIsLoading(false);
    }
  }

  function openAPIKeyModal() {
    setAPIKeyDraftID(sharedApiIDInput);
    setAPIKeyDraftHash(sharedApiHashInput);
    setShowAPIKeyModal(true);
  }

  function openAddAccountModal() {
    setAddAccountSelectedID("");
    setAddAccountLabelDraft("");
    setAddAccountPhoneDraft("");
    setShowAddAccountModal(true);
  }

  function handleSelectAccountForLogin(value: string) {
    const selectedID = value.trim();
    setAddAccountSelectedID(selectedID);
    if (!selectedID) {
      return;
    }
    const account = accounts.find((item) => item.id === selectedID);
    if (!account) {
      return;
    }
    setAddAccountLabelDraft(account.label || "");
    setAddAccountPhoneDraft(account.phone || "");
  }

  async function handleStartLoginFromInput() {
    if (!sharedAPIConfigured) {
      setStatus("请先设置API");
      return;
    }

    setIsLoading(true);
    setStatus("正在准备步骤1：输入账号并开始登陆...");
    try {
      let account: TGAssistantAccount | null = null;
      if (addAccountSelectedID.trim()) {
        account = accounts.find((item) => item.id === addAccountSelectedID.trim()) ?? null;
        if (!account) {
          throw new Error("所选账号不存在，请重新选择");
        }
      } else {
        const phone = addAccountPhoneDraft.trim();
        if (!phone) {
          throw new Error("请输入手机号（建议包含国家码，例如 +886xxxxxxxxx）");
        }
        const normalizedPhone = normalizePhone(phone);
        account = accounts.find((item) => normalizePhone(item.phone) === normalizedPhone) ?? null;
        if (!account) {
          account = await addTGAssistantAccount(props.controllerBaseUrl, props.sessionToken, {
            label: addAccountLabelDraft.trim(),
            phone,
          });
        }
      }

      await sendTGAssistantLoginCode(props.controllerBaseUrl, props.sessionToken, account.id);
      setAddAccountSelectedID("");
      setAddAccountLabelDraft("");
      setAddAccountPhoneDraft("");
      setShowAddAccountModal(false);
      await loadAccounts({ silent: true });
      setSelectedAccountID(account.id);
      setCodeInput("");
      setPasswordInput("");
      setStatus(`步骤1完成：验证码已发送到 ${account.phone}，请继续步骤2`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`步骤1失败：${msg}`);
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
    setStatus(`正在执行步骤2：登陆账号 ${selectedAccount.label}...`);
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
      setStatus(`步骤2完成：登陆成功，当前账号 ${account.label}`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`步骤2失败：${msg}`);
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
        <div style={{ marginTop: 4, color: "#dceaff" }}>API（所有账号共用）</div>
        <div>共用配置状态：{sharedAPIConfigured ? "已配置" : "未配置"}</div>
        <div>共用 API ID：{sharedApiIDInput || "-"}</div>
        <div>共用 API Hash：{sharedAPIConfigured ? "已配置" : "-"}</div>
        <div className="content-actions">
          <button className="btn" onClick={openAPIKeyModal} disabled={isLoading}>设置API</button>
          <button className="btn" onClick={() => void handleRefreshAccounts()} disabled={isLoading}>刷新状态</button>
        </div>

        <div style={{ marginTop: 8, color: "#dceaff" }}>登陆引导</div>
        <div>步骤1：输入账号并开始登陆</div>
        <div className="content-actions">
          <button className="btn" onClick={openAddAccountModal} disabled={isLoading || !sharedAPIConfigured}>步骤1：输入账号并开始登陆</button>
        </div>
        <div>当前登陆账号：{selectedAccount ? `${selectedAccount.label} (${selectedAccount.phone})` : "未开始，请先执行步骤1"}</div>
        <div>当前步骤：{loginStep === 1 ? "步骤1：输入账号并开始登陆" : loginStep === 2 ? "步骤2：输入验证码并登陆" : "步骤3：已登陆，可切换账号 Tab"}</div>
        <div>步骤2：输入验证码并完成登陆</div>
        <div>登录状态：{selectedAccount?.authorized ? "已登录" : "未登录"}</div>
        <div>验证码状态：{selectedAccount?.pending_code ? "已发送，待验证" : "未发送"}</div>
        <div>最后错误：{selectedAccount?.last_error || "-"}</div>
        <div>填写验证码（如开启2FA再填密码）后点击“完成登录”</div>
        <div className="row" style={{ marginBottom: 0 }}>
          <label>验证码</label>
          <input
            className="input"
            value={codeInput}
            onChange={(event) => setCodeInput(event.target.value)}
            placeholder="输入短信/APP 验证码"
            disabled={isLoading || !selectedAccount || !selectedAccount.pending_code}
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
            disabled={isLoading || !selectedAccount || !selectedAccount.pending_code}
          />
        </div>
        <div className="content-actions">
          <button className="btn" onClick={() => void handleSignIn()} disabled={isLoading || !selectedAccount || !selectedAccount.pending_code || selectedAccount.authorized}>步骤2：完成登录</button>
        </div>
        <div>步骤3：已登陆后可在顶部“已登录账号 Tab”切换使用账号。</div>
        <div className="content-actions">
          <button className="btn" onClick={() => void handleLogout()} disabled={isLoading || !selectedAccount}>登出账号</button>
          <button className="btn" onClick={() => void handleRemove()} disabled={isLoading || !selectedAccount}>删除账号</button>
        </div>
      </div>

      {showAddAccountModal ? (
        <div className="probe-settings-modal-mask" onClick={() => setShowAddAccountModal(false)}>
          <div className="probe-settings-modal" onClick={(event) => event.stopPropagation()}>
            <h3 style={{ marginTop: 0 }}>步骤1：输入账号并开始登陆</h3>
            <div className="row">
              <label>已有账号</label>
              <select
                className="input"
                value={addAccountSelectedID}
                onChange={(event) => handleSelectAccountForLogin(event.target.value)}
                disabled={isLoading}
              >
                <option value="">不选择（手动输入）</option>
                {accounts.map((item) => (
                  <option key={`tg-login-account-${item.id}`} value={item.id}>
                    {item.label} ({item.phone}) {item.authorized ? "[已登录]" : "[未登录]"}
                  </option>
                ))}
              </select>
            </div>
            <div className="row">
              <label>账号备注</label>
              <input
                className="input"
                value={addAccountLabelDraft}
                onChange={(event) => setAddAccountLabelDraft(event.target.value)}
                placeholder="例如：运营主号"
                disabled={isLoading || !!addAccountSelectedID}
              />
            </div>
            <div className="row">
              <label>手机号</label>
              <input
                className="input"
                value={addAccountPhoneDraft}
                onChange={(event) => setAddAccountPhoneDraft(event.target.value)}
                placeholder="例如：+886912345678"
                disabled={isLoading || !!addAccountSelectedID}
              />
            </div>
            <div className="content-actions">
              <button className="btn" onClick={() => void handleStartLoginFromInput()} disabled={isLoading}>开始登陆</button>
              <button className="btn" onClick={() => setShowAddAccountModal(false)} disabled={isLoading}>取消</button>
            </div>
          </div>
        </div>
      ) : null}

      {showAPIKeyModal ? (
        <div className="probe-settings-modal-mask" onClick={() => setShowAPIKeyModal(false)}>
          <div className="probe-settings-modal" onClick={(event) => event.stopPropagation()}>
            <h3 style={{ marginTop: 0 }}>设置API</h3>
            <div className="row">
              <label>共用 API ID</label>
              <input
                className="input"
                value={apiKeyDraftID}
                onChange={(event) => setAPIKeyDraftID(event.target.value)}
                placeholder="来自 my.telegram.org"
                disabled={isLoading}
              />
            </div>
            <div className="row">
              <label>共用 API Hash</label>
              <input
                className="input"
                value={apiKeyDraftHash}
                onChange={(event) => setAPIKeyDraftHash(event.target.value)}
                placeholder="来自 my.telegram.org"
                disabled={isLoading}
              />
            </div>
            <div className="content-actions">
              <button className="btn" onClick={() => void handleSaveSharedAPIKey()} disabled={isLoading}>保存</button>
              <button className="btn" onClick={() => setShowAPIKeyModal(false)} disabled={isLoading}>取消</button>
            </div>
          </div>
        </div>
      ) : null}

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

function normalizePhone(raw: string): string {
  const value = raw.trim();
  if (!value) {
    return "";
  }
  return value.replace(/[^+\d]/g, "");
}
