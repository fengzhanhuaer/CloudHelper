import { useEffect, useMemo, useRef, useState } from "react";
import {
  addTGAssistantSchedule,
  addTGAssistantAccount,
  completeTGAssistantLogin,
  fetchTGAssistantAPIKey,
  fetchTGAssistantAccounts,
  fetchTGAssistantSchedules,
  logoutTGAssistantAccount,
  refreshTGAssistantAccounts,
  removeTGAssistantAccount,
  removeTGAssistantSchedule,
  setTGAssistantAPIKey,
  sendTGAssistantLoginCode,
} from "../services/controller-api";
import type { TGAssistantAPIKey, TGAssistantAccount, TGAssistantSchedule } from "../types";

type TGAssistantTabProps = {
  controllerBaseUrl: string;
  sessionToken: string;
};

type TGAccountDetailSubTab = "basic-info" | "scheduled-send";

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
  const [showAccountManageModal, setShowAccountManageModal] = useState(false);
  const [apiKeyDraftID, setAPIKeyDraftID] = useState("");
  const [apiKeyDraftHash, setAPIKeyDraftHash] = useState("");

  const [selectedAccountID, setSelectedAccountID] = useState("");
  const [activeLoggedInAccountID, setActiveLoggedInAccountID] = useState("");
  const [activeAccountDetailSubTab, setActiveAccountDetailSubTab] = useState<TGAccountDetailSubTab>("basic-info");
  const [codeInput, setCodeInput] = useState("");
  const [passwordInput, setPasswordInput] = useState("");
  const [scheduleEnabled, setScheduleEnabled] = useState(false);
  const [scheduleTimeDraft, setScheduleTimeDraft] = useState("每天 09:00");
  const [scheduleMessageDraft, setScheduleMessageDraft] = useState("");
  const [scheduleList, setScheduleList] = useState<TGAssistantSchedule[]>([]);
  const [isScheduleLoading, setIsScheduleLoading] = useState(false);
  const scheduleRequestSeqRef = useRef(0);

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
    void loadData({ silent: false });
  }, [props.controllerBaseUrl, props.sessionToken]);

  useEffect(() => {
    const accountID = activeLoggedInAccountID.trim();
    if (!accountID) {
      setScheduleList([]);
      setScheduleEnabled(false);
      setScheduleTimeDraft("每天 09:00");
      setScheduleMessageDraft("");
      return;
    }
    void loadSchedule(accountID, { silent: true });
  }, [activeLoggedInAccountID, props.controllerBaseUrl, props.sessionToken]);

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

  function applyScheduleList(accountID: string, schedules: TGAssistantSchedule[]) {
    setScheduleList(schedules);
    setAccounts((prev) =>
      prev.map((item) => (item.id === accountID ? { ...item, schedules } : item)),
    );
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

  async function loadSchedule(accountID: string, options?: { silent?: boolean }) {
    const normalizedAccountID = accountID.trim();
    if (!normalizedAccountID) {
      return;
    }
    const requestSeq = scheduleRequestSeqRef.current + 1;
    scheduleRequestSeqRef.current = requestSeq;
    const silent = options?.silent === true;
    setIsScheduleLoading(true);
    if (!silent) {
      setStatus("正在加载任务列表...");
    }
    try {
      const schedules = await fetchTGAssistantSchedules(props.controllerBaseUrl, props.sessionToken, normalizedAccountID);
      if (requestSeq !== scheduleRequestSeqRef.current) {
        return;
      }
      applyScheduleList(normalizedAccountID, schedules);
      if (!silent) {
        setStatus(`已加载账号 ${activeLoggedInAccount?.label || normalizedAccountID} 的任务列表（${schedules.length} 个）`);
      }
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      if (!silent) {
        setStatus(`加载任务列表失败：${msg}`);
      }
    } finally {
      setIsScheduleLoading(false);
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

  function openAccountManageModal() {
    setShowAccountManageModal(true);
  }

  function openAddAccountModal() {
    setAddAccountSelectedID("");
    setAddAccountLabelDraft("");
    setAddAccountPhoneDraft("");
    setSelectedAccountID("");
    setCodeInput("");
    setPasswordInput("");
    setShowAddAccountModal(true);
  }

  function handleSelectAccountForLogin(value: string) {
    const selectedID = value.trim();
    setAddAccountSelectedID(selectedID);
    setSelectedAccountID(selectedID);
    if (!selectedID) {
      setAddAccountLabelDraft("");
      setAddAccountPhoneDraft("");
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
      await loadAccounts({ silent: true });
      setSelectedAccountID(account.id);
      setCodeInput("");
      setPasswordInput("");
      setStatus(`验证码已发送到 ${account.phone}，请在弹窗继续完成登录`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`发送验证码失败：${msg}`);
    } finally {
      setIsLoading(false);
    }
  }

  async function handleSignIn() {
    if (!selectedAccount) {
      setStatus("请先在弹窗选择或输入账号并发送验证码");
      return;
    }
    const code = codeInput.trim();
    if (!code) {
      setStatus("请输入验证码");
      return;
    }

    setIsLoading(true);
    setStatus(`正在登陆账号 ${selectedAccount.label}...`);
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
      setShowAddAccountModal(false);
      setStatus(`登陆成功：${account.label}`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`完成登录失败：${msg}`);
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

  async function handleAddScheduleTask() {
    if (!activeLoggedInAccount) {
      setStatus("请先选择一个已登录账号");
      return;
    }
    const sendAt = scheduleTimeDraft.trim();
    const message = scheduleMessageDraft.trim();
    if (scheduleEnabled && !sendAt) {
      setStatus("请填写定时发送时间");
      return;
    }
    if (scheduleEnabled && !message) {
      setStatus("请填写定时发送内容");
      return;
    }

    setIsScheduleLoading(true);
    setStatus(`正在为账号 ${activeLoggedInAccount.label} 新增任务...`);
    try {
      const schedules = await addTGAssistantSchedule(props.controllerBaseUrl, props.sessionToken, {
        account_id: activeLoggedInAccount.id,
        task_type: "scheduled_send",
        enabled: scheduleEnabled,
        send_at: sendAt,
        message,
      });
      applyScheduleList(activeLoggedInAccount.id, schedules);
      setScheduleMessageDraft("");
      setStatus(`已新增任务：${activeLoggedInAccount.label}（当前共 ${schedules.length} 个）`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`新增任务失败：${msg}`);
    } finally {
      setIsScheduleLoading(false);
    }
  }

  async function handleRemoveScheduleTask(taskID: string) {
    if (!activeLoggedInAccount) {
      setStatus("请先选择一个已登录账号");
      return;
    }
    if (!window.confirm("确认删除该任务吗？")) {
      return;
    }
    setIsScheduleLoading(true);
    setStatus(`正在删除任务：${taskID}...`);
    try {
      const schedules = await removeTGAssistantSchedule(props.controllerBaseUrl, props.sessionToken, {
        account_id: activeLoggedInAccount.id,
        task_id: taskID,
      });
      applyScheduleList(activeLoggedInAccount.id, schedules);
      setStatus(`任务已删除，当前共 ${schedules.length} 个`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`删除任务失败：${msg}`);
    } finally {
      setIsScheduleLoading(false);
    }
  }

  return (
    <div className="content-block">
      <div style={{ display: "flex", alignItems: "center", justifyContent: "flex-start", gap: 10, marginBottom: 12 }}>
        <h2 style={{ marginBottom: 0 }}>TG助手</h2>
        <div className="content-actions inline">
          <button className="btn" onClick={openAccountManageModal} disabled={isLoading}>账号管理</button>
          <button className="btn" onClick={openAddAccountModal} disabled={isLoading || !sharedAPIConfigured}>登录账号</button>
          <button className="btn" onClick={openAPIKeyModal} disabled={isLoading}>设置API</button>
        </div>
      </div>

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
                  onClick={() => {
                    setActiveLoggedInAccountID(item.id);
                    setActiveAccountDetailSubTab("basic-info");
                  }}
                  disabled={isLoading}
                >
                  {item.label}
                </button>
              ))}
            </div>

            <div className="subtab-list tg-account-detail-subtabs">
              <button
                className={`subtab-btn ${activeAccountDetailSubTab === "basic-info" ? "active" : ""}`}
                onClick={() => setActiveAccountDetailSubTab("basic-info")}
                disabled={isLoading || isScheduleLoading || !activeLoggedInAccount}
              >
                基础信息
              </button>
              <button
                className={`subtab-btn ${activeAccountDetailSubTab === "scheduled-send" ? "active" : ""}`}
                onClick={() => setActiveAccountDetailSubTab("scheduled-send")}
                disabled={isLoading || isScheduleLoading || !activeLoggedInAccount}
              >
                定时发送
              </button>
            </div>

            {activeLoggedInAccount ? (
              <div className="tg-account-summary">
                {activeAccountDetailSubTab === "basic-info" ? (
                  <div className="tg-account-basic-list">
                    <div className="tg-account-basic-row">
                      <div className="tg-account-basic-label">当前账号</div>
                      <div className="tg-account-basic-value">{activeLoggedInAccount.label || "-"}</div>
                    </div>
                    <div className="tg-account-basic-row">
                      <div className="tg-account-basic-label">手机号</div>
                      <div className="tg-account-basic-value">{activeLoggedInAccount.phone || "-"}</div>
                    </div>
                    <div className="tg-account-basic-row">
                      <div className="tg-account-basic-label">显示名</div>
                      <div className="tg-account-basic-value">{activeLoggedInAccount.self_display_name || "-"}</div>
                    </div>
                    <div className="tg-account-basic-row">
                      <div className="tg-account-basic-label">用户名</div>
                      <div className="tg-account-basic-value">{activeLoggedInAccount.self_username || "-"}</div>
                    </div>
                    <div className="tg-account-basic-row">
                      <div className="tg-account-basic-label">最近登录</div>
                      <div className="tg-account-basic-value">{formatDateTime(activeLoggedInAccount.last_login_at || "")}</div>
                    </div>
                  </div>
                ) : (
                  <div className="tg-schedule-panel">
                    <div className="tg-account-basic-row">
                      <div className="tg-account-basic-label">发送账号</div>
                      <div className="tg-account-basic-value">
                        {activeLoggedInAccount.label} ({activeLoggedInAccount.phone})
                      </div>
                    </div>
                    <label className="tg-schedule-toggle">
                      <input
                        type="checkbox"
                        checked={scheduleEnabled}
                        onChange={(event) => setScheduleEnabled(event.target.checked)}
                        disabled={isLoading || isScheduleLoading}
                      />
                      启用定时发送
                    </label>
                    <div className="tg-schedule-field">
                      <label className="tg-account-basic-label" htmlFor="tg-schedule-time">
                        发送时间
                      </label>
                      <input
                        id="tg-schedule-time"
                        className="input"
                        value={scheduleTimeDraft}
                        onChange={(event) => setScheduleTimeDraft(event.target.value)}
                        placeholder="例如：每天 09:00 / 每周一 10:30"
                        disabled={isLoading || isScheduleLoading}
                      />
                    </div>
                    <div className="tg-schedule-field">
                      <label className="tg-account-basic-label" htmlFor="tg-schedule-message">
                        发送内容
                      </label>
                      <textarea
                        id="tg-schedule-message"
                        className="input tg-schedule-textarea"
                        value={scheduleMessageDraft}
                        onChange={(event) => setScheduleMessageDraft(event.target.value)}
                        placeholder="请输入计划发送的消息内容"
                        disabled={isLoading || isScheduleLoading}
                      />
                    </div>
                    <div className="content-actions" style={{ marginTop: 10 }}>
                      <button className="btn" onClick={() => void handleAddScheduleTask()} disabled={isLoading || isScheduleLoading || !activeLoggedInAccount}>
                        新增定时发送任务
                      </button>
                    </div>
                    <div className="tg-schedule-list">
                      {scheduleList.length === 0 ? (
                        <div className="tg-account-summary-line">当前账号暂无任务。</div>
                      ) : (
                        scheduleList.map((task) => (
                          <div className="identity-card" key={`tg-task-${task.id}`}>
                            <div className="tg-account-basic-row">
                              <div className="tg-account-basic-label">任务类型</div>
                              <div className="tg-account-basic-value">{task.task_type === "scheduled_send" ? "定时发送" : task.task_type}</div>
                            </div>
                            <div className="tg-account-basic-row">
                              <div className="tg-account-basic-label">状态</div>
                              <div className="tg-account-basic-value">{task.enabled ? "启用" : "停用"}</div>
                            </div>
                            <div className="tg-account-basic-row">
                              <div className="tg-account-basic-label">发送时间</div>
                              <div className="tg-account-basic-value">{task.send_at || "-"}</div>
                            </div>
                            <div className="tg-account-basic-row">
                              <div className="tg-account-basic-label">发送内容</div>
                              <div className="tg-account-basic-value">{task.message || "-"}</div>
                            </div>
                            <div className="tg-account-basic-row">
                              <div className="tg-account-basic-label">更新时间</div>
                              <div className="tg-account-basic-value">{formatDateTime(task.updated_at || "")}</div>
                            </div>
                            <div className="content-actions" style={{ marginTop: 8 }}>
                              <button className="btn" onClick={() => void handleRemoveScheduleTask(task.id)} disabled={isLoading || isScheduleLoading}>
                                删除任务
                              </button>
                            </div>
                          </div>
                        ))
                      )}
                    </div>
                  </div>
                )}
              </div>
            ) : (
              <div className="tg-account-summary-line">请先选择一个已登录账号。</div>
            )}
          </>
        )}
      </div>

      {showAddAccountModal ? (
        <div className="probe-settings-modal-mask" onClick={() => setShowAddAccountModal(false)}>
          <div className="probe-settings-modal" onClick={(event) => event.stopPropagation()}>
            <h3 style={{ marginTop: 0 }}>登录账号</h3>
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
            <div className="row">
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
            {selectedAccount ? (
              <div
                className="tg-account-summary-line"
                style={{ color: "#dceaff", marginTop: 10 }}
                title={`当前账号：${selectedAccount.label} (${selectedAccount.phone}) ${selectedAccount.authorized ? "[已登录]" : selectedAccount.pending_code ? "[待验证]" : "[未发送验证码]"}`}
              >
                当前账号：{selectedAccount.label} ({selectedAccount.phone}) {selectedAccount.authorized ? "[已登录]" : selectedAccount.pending_code ? "[待验证]" : "[未发送验证码]"}
              </div>
            ) : null}
            <div className="content-actions">
              <button className="btn" onClick={() => void handleStartLoginFromInput()} disabled={isLoading}>发送验证码</button>
              <button className="btn" onClick={() => void handleSignIn()} disabled={isLoading || !selectedAccount || !selectedAccount.pending_code || selectedAccount.authorized}>完成登录</button>
              <button className="btn" onClick={() => setShowAddAccountModal(false)} disabled={isLoading}>取消</button>
            </div>
          </div>
        </div>
      ) : null}

      {showAccountManageModal ? (
        <div className="probe-settings-modal-mask" onClick={() => setShowAccountManageModal(false)}>
          <div className="probe-settings-modal" onClick={(event) => event.stopPropagation()}>
            <h3 style={{ marginTop: 0 }}>账号管理</h3>
            <div className="row" style={{ marginBottom: 0 }}>
              <label>账号</label>
              <select
                className="input"
                value={selectedAccountID}
                onChange={(event) => setSelectedAccountID(event.target.value)}
                disabled={isLoading}
              >
                <option value="">请选择账号</option>
                {accounts.map((item) => (
                  <option key={`tg-manage-account-${item.id}`} value={item.id}>
                    {item.label} ({item.phone}) {item.authorized ? "[已登录]" : "[未登录]"}
                  </option>
                ))}
              </select>
            </div>
            {selectedAccount ? (
              <div
                className="tg-account-summary-line"
                style={{ color: "#dceaff", marginTop: 10 }}
                title={`当前账号：${selectedAccount.label} (${selectedAccount.phone}) ${selectedAccount.authorized ? "[已登录]" : "[未登录]"}`}
              >
                当前账号：{selectedAccount.label} ({selectedAccount.phone}) {selectedAccount.authorized ? "[已登录]" : "[未登录]"}
              </div>
            ) : null}
            <div className="content-actions">
              <button className="btn" onClick={() => void handleRefreshAccounts()} disabled={isLoading}>刷新状态</button>
              <button className="btn" onClick={() => void handleLogout()} disabled={isLoading || !selectedAccount}>登出账号</button>
              <button className="btn" onClick={() => void handleRemove()} disabled={isLoading || !selectedAccount}>删除账号</button>
              <button className="btn" onClick={() => setShowAccountManageModal(false)} disabled={isLoading}>关闭</button>
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
