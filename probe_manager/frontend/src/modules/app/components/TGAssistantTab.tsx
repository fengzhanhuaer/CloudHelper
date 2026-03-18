import { useEffect, useMemo, useRef, useState } from "react";
import {
  addTGAssistantSchedule,
  addTGAssistantAccount,
  completeTGAssistantLogin,
  fetchTGAssistantAPIKey,
  fetchTGAssistantAccounts,
  fetchTGAssistantSchedules,
  fetchTGAssistantTargets,
  logoutTGAssistantAccount,
  refreshTGAssistantAccounts,
  removeTGAssistantAccount,
  removeTGAssistantSchedule,
  refreshTGAssistantTargets,
  sendNowTGAssistantSchedule,
  setTGAssistantAPIKey,
  setTGAssistantScheduleEnabled,
  sendTGAssistantLoginCode,
} from "../services/controller-api";
import type { TGAssistantAPIKey, TGAssistantAccount, TGAssistantSchedule, TGAssistantTarget } from "../types";

type TGAssistantTabProps = {
  controllerBaseUrl: string;
  sessionToken: string;
};

type TGAccountDetailSubTab = "basic-info" | "scheduled-send";

export function TGAssistantTab(props: TGAssistantTabProps) {
  const [accounts, setAccounts] = useState<TGAssistantAccount[]>([]);
  const [status, setStatus] = useState("姝ｅ湪鍔犺浇 TG 璐﹀彿鍒楄〃...");
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
  const [scheduleTargetDraft, setScheduleTargetDraft] = useState("");
  const [targetSearchDraft, setTargetSearchDraft] = useState("");
  const [scheduleTimeDraft, setScheduleTimeDraft] = useState("姣忓ぉ 09:00");
  const [scheduleMessageDraft, setScheduleMessageDraft] = useState("");
  const [scheduleDelayMaxDraft, setScheduleDelayMaxDraft] = useState("0");
  const [scheduleList, setScheduleList] = useState<TGAssistantSchedule[]>([]);
  const [targetList, setTargetList] = useState<TGAssistantTarget[]>([]);
  const [isScheduleLoading, setIsScheduleLoading] = useState(false);
  const [isTargetRefreshing, setIsTargetRefreshing] = useState(false);
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
  const filteredTargetList = useMemo(() => {
    const keyword = targetSearchDraft.trim().toLowerCase();
    if (!keyword) {
      return targetList;
    }
    const filtered = targetList.filter((item) => {
      const id = (item.id || "").toLowerCase();
      const name = (item.name || "").toLowerCase();
      const username = (item.username || "").toLowerCase();
      return id.includes(keyword) || name.includes(keyword) || username.includes(keyword);
    });
    if (scheduleTargetDraft.trim()) {
      const selected = targetList.find((item) => item.id === scheduleTargetDraft.trim());
      if (selected && !filtered.some((item) => item.id === selected.id)) {
        return [selected, ...filtered];
      }
    }
    return filtered;
  }, [targetList, targetSearchDraft, scheduleTargetDraft]);

  useEffect(() => {
    void loadData({ silent: false });
  }, [props.controllerBaseUrl, props.sessionToken]);

  useEffect(() => {
    const accountID = activeLoggedInAccountID.trim();
    if (!accountID) {
      setScheduleList([]);
      setTargetList([]);
      setTargetSearchDraft("");
      setScheduleEnabled(false);
      setScheduleTargetDraft("");
      setScheduleTimeDraft("姣忓ぉ 09:00");
      setScheduleMessageDraft("");
      setScheduleDelayMaxDraft("0");
      return;
    }
    void loadSchedule(accountID, { silent: true });
    void loadTargets(accountID, { silent: true });
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

  function applyTargetList(targets: TGAssistantTarget[]) {
    setTargetList(targets);
    setScheduleTargetDraft((prev) => (targets.some((item) => item.id === prev) ? prev : ""));
  }

  function renderTargetLabel(targetID: string): string {
    const id = targetID.trim();
    if (!id) {
      return "-";
    }
    const hit = targetList.find((item) => item.id === id);
    if (!hit) {
      return id;
    }
    if (hit.username && hit.username.trim()) {
      return `${hit.name} (@${hit.username})`;
    }
    return hit.name;
  }

  async function loadData(options?: { silent?: boolean }) {
    const silent = options?.silent === true;
    if (!silent) {
      setIsLoading(true);
      setStatus("姝ｅ湪鍔犺浇 TG 璁剧疆涓庤处鍙?..");
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
          setStatus("璇峰厛璁剧疆API锛屽啀娣诲姞/鐧诲綍璐﹀彿");
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
      setStatus("姝ｅ湪鍔犺浇 TG 璐﹀彿鍒楄〃...");
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
      setStatus("姝ｅ湪鍔犺浇浠诲姟鍒楄〃...");
    }
    try {
      const schedules = await fetchTGAssistantSchedules(props.controllerBaseUrl, props.sessionToken, normalizedAccountID);
      if (requestSeq !== scheduleRequestSeqRef.current) {
        return;
      }
      applyScheduleList(normalizedAccountID, schedules);
      if (!silent) {
        setStatus(`宸插姞杞借处鍙?${activeLoggedInAccount?.label || normalizedAccountID} 鐨勪换鍔″垪琛紙${schedules.length} 涓級`);
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

  async function loadTargets(accountID: string, options?: { silent?: boolean }) {
    const normalizedAccountID = accountID.trim();
    if (!normalizedAccountID) {
      return;
    }
    const silent = options?.silent === true;
    if (!silent) {
      setStatus("姝ｅ湪鍔犺浇鍙戦€佸璞″垪琛?..");
    }
    try {
      const targets = await fetchTGAssistantTargets(props.controllerBaseUrl, props.sessionToken, normalizedAccountID);
      applyTargetList(targets);
      if (!silent) {
        setStatus(`宸插姞杞藉彂閫佸璞★紙${targets.length} 涓級`);
      }
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      if (!silent) {
        setStatus(`鍔犺浇鍙戦€佸璞″け璐ワ細${msg}`);
      }
    }
  }

  async function handleRefreshTargets() {
    if (!activeLoggedInAccount) {
      setStatus("璇峰厛閫夋嫨涓€涓凡鐧诲綍璐﹀彿");
      return;
    }
    setIsTargetRefreshing(true);
    setStatus(`正在从服务器刷新会话对象：${activeLoggedInAccount.label}...`);
    try {
      const targets = await refreshTGAssistantTargets(props.controllerBaseUrl, props.sessionToken, activeLoggedInAccount.id);
      applyTargetList(targets);
      setStatus(`宸插埛鏂板彂閫佸璞★紙${targets.length} 涓級`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`鍒锋柊鍙戦€佸璞″け璐ワ細${msg}`);
    } finally {
      setIsTargetRefreshing(false);
    }
  }

  async function handleRefreshAccounts() {
    setIsLoading(true);
    setStatus("姝ｅ湪鍒锋柊 TG 鐧诲綍鐘舵€?..");
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
      setStatus(`鍒锋柊鐘舵€佸け璐ワ細${msg}`);
    } finally {
      setIsLoading(false);
    }
  }

  async function handleSaveSharedAPIKey() {
    const apiID = Number.parseInt(apiKeyDraftID.trim(), 10);
    const apiHash = apiKeyDraftHash.trim();
    if (!Number.isFinite(apiID) || apiID <= 0) {
      setStatus("鍏辩敤 API ID 蹇呴』鏄鏁存暟");
      return;
    }
    if (!apiHash) {
      setStatus("鍏辩敤 API Hash 涓嶈兘涓虹┖");
      return;
    }

    setIsLoading(true);
    setStatus("姝ｅ湪璁剧疆API...");
    try {
      const result = await setTGAssistantAPIKey(props.controllerBaseUrl, props.sessionToken, {
        api_id: apiID,
        api_hash: apiHash,
      });
      applyAPIKey(result);
      setShowAPIKeyModal(false);
      setStatus("API 已保存，所有账号将使用该配置");
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`设置 API 失败：${msg}`);
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
      setStatus("璇峰厛璁剧疆API");
      return;
    }

    setIsLoading(true);
    setStatus("姝ｅ湪鍑嗗姝ラ1锛氳緭鍏ヨ处鍙峰苟寮€濮嬬櫥闄?..");
    try {
      let account: TGAssistantAccount | null = null;
      if (addAccountSelectedID.trim()) {
        account = accounts.find((item) => item.id === addAccountSelectedID.trim()) ?? null;
        if (!account) {
          throw new Error("鎵€閫夎处鍙蜂笉瀛樺湪锛岃閲嶆柊閫夋嫨");
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
      setStatus("璇峰厛鍦ㄥ脊绐楅€夋嫨鎴栬緭鍏ヨ处鍙峰苟鍙戦€侀獙璇佺爜");
      return;
    }
    const code = codeInput.trim();
    if (!code) {
      setStatus("璇疯緭鍏ラ獙璇佺爜");
      return;
    }

    setIsLoading(true);
    setStatus(`姝ｅ湪鐧婚檰璐﹀彿 ${selectedAccount.label}...`);
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
      setStatus(`登录成功：${account.label}`);
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
      setStatus(`宸茬櫥鍑鸿处鍙凤細${selectedAccount.label}`);
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
    if (!window.confirm(`纭鍒犻櫎璐﹀彿 ${selectedAccount.label} (${selectedAccount.phone}) 鍚楋紵`)) {
      return;
    }

    setIsLoading(true);
    setStatus(`正在删除账号：${selectedAccount.label}...`);
    try {
      const next = await removeTGAssistantAccount(props.controllerBaseUrl, props.sessionToken, selectedAccount.id);
      applyAccountList(next);
      setCodeInput("");
      setPasswordInput("");
      setStatus(`璐﹀彿宸插垹闄わ細${selectedAccount.label}`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`删除账号失败：${msg}`);
    } finally {
      setIsLoading(false);
    }
  }

  async function handleAddScheduleTask() {
    if (!activeLoggedInAccount) {
      setStatus("璇峰厛閫夋嫨涓€涓凡鐧诲綍璐﹀彿");
      return;
    }
    const target = scheduleTargetDraft.trim();
    const sendAt = scheduleTimeDraft.trim();
    const message = scheduleMessageDraft.trim();
    const delayMax = Number.parseInt(scheduleDelayMaxDraft.trim() || "0", 10);
    if (!target) {
      setStatus("请填写发送目标");
      return;
    }
    if (scheduleEnabled && !sendAt) {
      setStatus("请填写定时发送时间");
      return;
    }
    if (scheduleEnabled && !message) {
      setStatus("请填写定时发送内容");
      return;
    }
    if (!Number.isFinite(delayMax) || delayMax < 0) {
      setStatus("随机延时范围必须是非负整数");
      return;
    }

    setIsScheduleLoading(true);
    setStatus(`姝ｅ湪涓鸿处鍙?${activeLoggedInAccount.label} 鏂板浠诲姟...`);
    try {
      const schedules = await addTGAssistantSchedule(props.controllerBaseUrl, props.sessionToken, {
        account_id: activeLoggedInAccount.id,
        task_type: "scheduled_send",
        enabled: scheduleEnabled,
        target,
        send_at: sendAt,
        message,
        delay_min_sec: 0,
        delay_max_sec: delayMax,
      });
      applyScheduleList(activeLoggedInAccount.id, schedules);
      setScheduleMessageDraft("");
      setStatus(`宸叉柊澧炰换鍔★細${activeLoggedInAccount.label}锛堝綋鍓嶅叡 ${schedules.length} 涓級`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`新增任务失败：${msg}`);
    } finally {
      setIsScheduleLoading(false);
    }
  }

  async function handleRemoveScheduleTask(taskID: string) {
    if (!activeLoggedInAccount) {
      setStatus("璇峰厛閫夋嫨涓€涓凡鐧诲綍璐﹀彿");
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

  async function handleToggleScheduleTaskEnabled(taskID: string, enabled: boolean) {
    if (!activeLoggedInAccount) {
      setStatus("璇峰厛閫夋嫨涓€涓凡鐧诲綍璐﹀彿");
      return;
    }
    setIsScheduleLoading(true);
    setStatus(`姝ｅ湪鏇存柊浠诲姟鐘舵€侊細${taskID}...`);
    try {
      const schedules = await setTGAssistantScheduleEnabled(props.controllerBaseUrl, props.sessionToken, {
        account_id: activeLoggedInAccount.id,
        task_id: taskID,
        enabled,
      });
      applyScheduleList(activeLoggedInAccount.id, schedules);
      setStatus(`任务状态已更新：${enabled ? "启用" : "停用"}`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`鏇存柊浠诲姟鐘舵€佸け璐ワ細${msg}`);
    } finally {
      setIsScheduleLoading(false);
    }
  }

  async function handleSendNowScheduleTask(taskID: string) {
    if (!activeLoggedInAccount) {
      setStatus("璇峰厛閫夋嫨涓€涓凡鐧诲綍璐﹀彿");
      return;
    }
    setIsScheduleLoading(true);
    setStatus(`姝ｅ湪绔嬪嵆鍙戦€佷换鍔★細${taskID}...`);
    try {
      const result = await sendNowTGAssistantSchedule(props.controllerBaseUrl, props.sessionToken, {
        account_id: activeLoggedInAccount.id,
        task_id: taskID,
      });
      setStatus(`绔嬪嵆鍙戦€佹垚鍔燂紙寤舵椂 ${result.delay_sec} 绉掞級`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`绔嬪嵆鍙戦€佸け璐ワ細${msg}`);
    } finally {
      setIsScheduleLoading(false);
    }
  }

  return (
    <div className="content-block">
      <div style={{ display: "flex", alignItems: "center", justifyContent: "flex-start", gap: 10, marginBottom: 12 }}>
        <h2 style={{ marginBottom: 0 }}>TG鍔╂墜</h2>
        <div className="content-actions inline">
          <button className="btn" onClick={openAccountManageModal} disabled={isLoading}>璐﹀彿绠＄悊</button>
          <button className="btn" onClick={openAddAccountModal} disabled={isLoading || !sharedAPIConfigured}>鐧诲綍璐﹀彿</button>
          <button className="btn" onClick={openAPIKeyModal} disabled={isLoading}>璁剧疆API</button>
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
                鍩虹淇℃伅
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
                      <div className="tg-account-basic-label">褰撳墠璐﹀彿</div>
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
                      <label className="tg-account-basic-label" htmlFor="tg-schedule-target">
                        发送目标
                      </label>
                      <input
                        className="input"
                        value={targetSearchDraft}
                        onChange={(event) => setTargetSearchDraft(event.target.value)}
                        placeholder="鎼滅储鍚嶇О / @username / ID"
                        disabled={isLoading || isScheduleLoading || isTargetRefreshing}
                        style={{ marginBottom: 8 }}
                      />
                      <div className="tg-target-picker">
                        <select
                          id="tg-schedule-target"
                          className="input"
                          value={scheduleTargetDraft}
                          onChange={(event) => setScheduleTargetDraft(event.target.value)}
                          disabled={isLoading || isScheduleLoading || isTargetRefreshing}
                        >
                          <option value="">请选择发送对象</option>
                          {filteredTargetList.map((item) => (
                            <option key={`tg-target-${item.id}`} value={item.id}>
                              {item.username ? `${item.name} (@${item.username})` : item.name}
                            </option>
                          ))}
                        </select>
                        <button
                          className="btn"
                          onClick={() => void handleRefreshTargets()}
                          disabled={isLoading || isScheduleLoading || isTargetRefreshing || !activeLoggedInAccount}
                        >
                          {isTargetRefreshing ? "鍒锋柊涓?.." : "鍒锋柊"}
                        </button>
                      </div>
                    </div>
                    <div className="tg-schedule-field">
                      <label className="tg-account-basic-label" htmlFor="tg-schedule-time">
                        鍙戦€佹椂闂?                      </label>
                      <input
                        id="tg-schedule-time"
                        className="input"
                        value={scheduleTimeDraft}
                        onChange={(event) => setScheduleTimeDraft(event.target.value)}
                        placeholder="渚嬪锛氭瘡澶?09:00 / 姣忓懆涓€ 10:30"
                        disabled={isLoading || isScheduleLoading}
                      />
                    </div>
                    <div className="tg-schedule-field">
                      <label className="tg-account-basic-label" htmlFor="tg-schedule-message">
                        鍙戦€佸唴瀹?                      </label>
                      <textarea
                        id="tg-schedule-message"
                        className="input tg-schedule-textarea"
                        value={scheduleMessageDraft}
                        onChange={(event) => setScheduleMessageDraft(event.target.value)}
                        placeholder="璇疯緭鍏ヨ鍒掑彂閫佺殑娑堟伅鍐呭"
                        disabled={isLoading || isScheduleLoading}
                      />
                    </div>
                    <div className="tg-schedule-field">
                      <label className="tg-account-basic-label" htmlFor="tg-schedule-delay-max">闅忔満寤舵椂鏈€澶у€硷紙绉掞級</label>
                      <input
                        id="tg-schedule-delay-max"
                        className="input"
                        type="number"
                        min={0}
                        value={scheduleDelayMaxDraft}
                        onChange={(event) => setScheduleDelayMaxDraft(event.target.value)}
                        placeholder="渚嬪锛?0"
                        disabled={isLoading || isScheduleLoading}
                      />
                    </div>
                    <div className="content-actions" style={{ marginTop: 10 }}>
                      <button className="btn" onClick={() => void handleAddScheduleTask()} disabled={isLoading || isScheduleLoading || isTargetRefreshing || !activeLoggedInAccount}>
                        鏂板瀹氭椂鍙戦€佷换鍔?                      </button>
                    </div>
                    <div className="tg-schedule-list">
                      {scheduleList.length === 0 ? (
                        <div className="tg-account-summary-line">当前账号暂无任务。</div>
                      ) : (
                        <div className="probe-table-wrap">
                          <table className="probe-table" style={{ minWidth: 1080 }}>
                            <thead>
                              <tr>
                                <th>任务类型</th>
                                <th>启用</th>
                                <th>发送目标</th>
                                <th>发送时间</th>
                                <th>发送内容</th>
                                <th>随机延时</th>
                                <th>更新时间</th>
                                <th>操作</th>
                              </tr>
                            </thead>
                            <tbody>
                              {scheduleList.map((task) => (
                                <tr key={`tg-task-${task.id}`}>
                                  <td>{task.task_type === "scheduled_send" ? "定时发送" : task.task_type}</td>
                                  <td>
                                    <label className="tg-schedule-row-toggle">
                                      <input
                                        type="checkbox"
                                        checked={task.enabled}
                                        onChange={(event) => void handleToggleScheduleTaskEnabled(task.id, event.target.checked)}
                                        disabled={isLoading || isScheduleLoading}
                                      />
                                      <span>{task.enabled ? "启用" : "停用"}</span>
                                    </label>
                                  </td>
                                  <td>{renderTargetLabel(task.target)}</td>
                                  <td>{task.send_at || "-"}</td>
                                  <td>
                                    <div className="tg-schedule-table-message">{task.message || "-"}</div>
                                  </td>
                                  <td>
                                    {Math.max(0, task.delay_min_sec ?? 0)} - {Math.max(0, task.delay_max_sec ?? 0)} 秒
                                  </td>
                                  <td>{formatDateTime(task.updated_at || "")}</td>
                                  <td>
                                    <div className="tg-schedule-table-actions">
                                      <button
                                        className="btn"
                                        onClick={() => void handleSendNowScheduleTask(task.id)}
                                        disabled={isLoading || isScheduleLoading || task.task_type !== "scheduled_send"}
                                      >
                                        立即发送
                                      </button>
                                      <button
                                        className="btn"
                                        onClick={() => void handleRemoveScheduleTask(task.id)}
                                        disabled={isLoading || isScheduleLoading}
                                      >
                                        删除任务
                                      </button>
                                    </div>
                                  </td>
                                </tr>
                              ))}
                            </tbody>
                          </table>
                        </div>
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
            <h3 style={{ marginTop: 0 }}>鐧诲綍璐﹀彿</h3>
            <div className="row">
              <label>宸叉湁璐﹀彿</label>
              <select
                className="input"
                value={addAccountSelectedID}
                onChange={(event) => handleSelectAccountForLogin(event.target.value)}
                disabled={isLoading}
              >
                <option value="">涓嶉€夋嫨锛堟墜鍔ㄨ緭鍏ワ級</option>
                {accounts.map((item) => (
                  <option key={`tg-login-account-${item.id}`} value={item.id}>
                    {item.label} ({item.phone}) {item.authorized ? "[宸茬櫥褰昡" : "[鏈櫥褰昡"}
                  </option>
                ))}
              </select>
            </div>
            <div className="row">
              <label>璐﹀彿澶囨敞</label>
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
              <label>2FA瀵嗙爜</label>
              <input
                className="input"
                type="password"
                value={passwordInput}
                onChange={(event) => setPasswordInput(event.target.value)}
                placeholder="濡傝处鍙峰紑鍚袱姝ラ獙璇佸垯蹇呭～"
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
              <button className="btn" onClick={() => void handleStartLoginFromInput()} disabled={isLoading}>鍙戦€侀獙璇佺爜</button>
              <button className="btn" onClick={() => void handleSignIn()} disabled={isLoading || !selectedAccount || !selectedAccount.pending_code || selectedAccount.authorized}>瀹屾垚鐧诲綍</button>
              <button className="btn" onClick={() => setShowAddAccountModal(false)} disabled={isLoading}>鍙栨秷</button>
            </div>
          </div>
        </div>
      ) : null}

      {showAccountManageModal ? (
        <div className="probe-settings-modal-mask" onClick={() => setShowAccountManageModal(false)}>
          <div className="probe-settings-modal" onClick={(event) => event.stopPropagation()}>
            <h3 style={{ marginTop: 0 }}>璐﹀彿绠＄悊</h3>
            <div className="row" style={{ marginBottom: 0 }}>
              <label>璐﹀彿</label>
              <select
                className="input"
                value={selectedAccountID}
                onChange={(event) => setSelectedAccountID(event.target.value)}
                disabled={isLoading}
              >
                <option value="">璇烽€夋嫨璐﹀彿</option>
                {accounts.map((item) => (
                  <option key={`tg-manage-account-${item.id}`} value={item.id}>
                    {item.label} ({item.phone}) {item.authorized ? "[宸茬櫥褰昡" : "[鏈櫥褰昡"}
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
              <button className="btn" onClick={() => void handleLogout()} disabled={isLoading || !selectedAccount}>鐧诲嚭璐﹀彿</button>
              <button className="btn" onClick={() => void handleRemove()} disabled={isLoading || !selectedAccount}>鍒犻櫎璐﹀彿</button>
              <button className="btn" onClick={() => setShowAccountManageModal(false)} disabled={isLoading}>鍏抽棴</button>
            </div>
          </div>
        </div>
      ) : null}

      {showAPIKeyModal ? (
        <div className="probe-settings-modal-mask" onClick={() => setShowAPIKeyModal(false)}>
          <div className="probe-settings-modal" onClick={(event) => event.stopPropagation()}>
            <h3 style={{ marginTop: 0 }}>璁剧疆API</h3>
            <div className="row">
              <label>鍏辩敤 API ID</label>
              <input
                className="input"
                value={apiKeyDraftID}
                onChange={(event) => setAPIKeyDraftID(event.target.value)}
                placeholder="鏉ヨ嚜 my.telegram.org"
                disabled={isLoading}
              />
            </div>
            <div className="row">
              <label>鍏辩敤 API Hash</label>
              <input
                className="input"
                value={apiKeyDraftHash}
                onChange={(event) => setAPIKeyDraftHash(event.target.value)}
                placeholder="鏉ヨ嚜 my.telegram.org"
                disabled={isLoading}
              />
            </div>
            <div className="content-actions">
              <button className="btn" onClick={() => void handleSaveSharedAPIKey()} disabled={isLoading}>淇濆瓨</button>
              <button className="btn" onClick={() => setShowAPIKeyModal(false)} disabled={isLoading}>鍙栨秷</button>
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
