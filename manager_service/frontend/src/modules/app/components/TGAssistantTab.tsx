import { useEffect, useMemo, useRef, useState } from "react";
import {
  addTGAssistantSchedule,
  addTGAssistantAccount,
  completeTGAssistantLogin,
  fetchTGAssistantAPIKey,
  fetchTGAssistantAccounts,
  fetchTGAssistantBotAPIKey,
  fetchTGAssistantPendingTasks,
  fetchTGAssistantSchedules,
  fetchTGAssistantScheduleTaskHistory,
  fetchTGAssistantTargets,
  logoutTGAssistantAccount,
  refreshTGAssistantAccounts,
  removeTGAssistantAccount,
  removeTGAssistantSchedule,
  refreshTGAssistantTargets,
  sendNowTGAssistantSchedule,
  setTGAssistantAPIKey,
  setTGAssistantBotAPIKey,
  setTGAssistantScheduleEnabled,
  testSendTGAssistantBotMessage,
  updateTGAssistantSchedule,
  sendTGAssistantLoginCode,
} from "../services/controller-api";
import type {
  TGAssistantAPIKey,
  TGAssistantAccount,
  TGAssistantBotAPIKey,
  TGAssistantBotTestSendResult,
  TGAssistantPendingTask,
  TGAssistantSchedule,
  TGAssistantTarget,
  TGAssistantTaskHistoryRecord,
} from "../types";

type TGAssistantTabProps = {
  controllerBaseUrl: string;
  sessionToken: string;
};

type TGAccountDetailSubTab = "basic-info" | "scheduled-send" | "tg-bot";

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
  const [scheduleTargetDraft, setScheduleTargetDraft] = useState("");
  const [targetSearchDraft, setTargetSearchDraft] = useState("");
  const [scheduleTimeDraft, setScheduleTimeDraft] = useState("每天 09:00");
  const [scheduleMessageDraft, setScheduleMessageDraft] = useState("");
  const [scheduleDelayMaxDraft, setScheduleDelayMaxDraft] = useState("0");
  const [scheduleList, setScheduleList] = useState<TGAssistantSchedule[]>([]);
  const [targetList, setTargetList] = useState<TGAssistantTarget[]>([]);
  const [isScheduleLoading, setIsScheduleLoading] = useState(false);
  const [isPendingLoading, setIsPendingLoading] = useState(false);
  const [isTargetRefreshing, setIsTargetRefreshing] = useState(false);
  const [showScheduleTaskModal, setShowScheduleTaskModal] = useState(false);
  const [editingScheduleTaskID, setEditingScheduleTaskID] = useState("");
  const [showScheduleHistoryModal, setShowScheduleHistoryModal] = useState(false);
  const [scheduleHistoryTaskID, setScheduleHistoryTaskID] = useState("");
  const [scheduleHistoryItems, setScheduleHistoryItems] = useState<TGAssistantTaskHistoryRecord[]>([]);
  const [pendingTaskItems, setPendingTaskItems] = useState<TGAssistantPendingTask[]>([]);
  const [isScheduleHistoryLoading, setIsScheduleHistoryLoading] = useState(false);
  const [showBotAPIKeyModal, setShowBotAPIKeyModal] = useState(false);
  const [botAPIKeyDraft, setBotAPIKeyDraft] = useState("");
  const [botModeDraft, setBotModeDraft] = useState<"polling" | "webhook">("polling");
  const [botAPIKeyInput, setBotAPIKeyInput] = useState("");
  const [botConfigured, setBotConfigured] = useState(false);
  const [botModeInput, setBotModeInput] = useState<"polling" | "webhook">("polling");
  const [botWebhookPath, setBotWebhookPath] = useState("");
  const [botWebhookEnabled, setBotWebhookEnabled] = useState(false);
  const [botTestMessageDraft, setBotTestMessageDraft] = useState("ping from bot test");
  const [isBotLoading, setIsBotLoading] = useState(false);
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
      setPendingTaskItems([]);
      setTargetList([]);
      setTargetSearchDraft("");
      setScheduleEnabled(false);
      setScheduleTargetDraft("");
      setScheduleTimeDraft("每天 09:00");
      setScheduleMessageDraft("");
      setScheduleDelayMaxDraft("0");
      setBotAPIKeyInput("");
      setBotConfigured(false);
      setBotModeInput("polling");
      setBotModeDraft("polling");
      setBotWebhookPath("");
      setBotWebhookEnabled(false);
      setBotTestMessageDraft("ping from bot test");
      return;
    }
    void loadSchedule(accountID, { silent: true });
    void loadPendingTasks(accountID, { silent: true });
    void loadTargets(accountID, { silent: true });
    void loadBotAPIKey(accountID, { silent: true });
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

  function applyBotAPIKey(payload: TGAssistantBotAPIKey) {
    setBotAPIKeyInput(payload.api_key || "");
    setBotConfigured(payload.configured === true);
    setBotModeInput(payload.mode === "webhook" ? "webhook" : "polling");
    setBotWebhookPath(payload.webhook_path || "");
    setBotWebhookEnabled(payload.webhook_enabled === true);
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
          setStatus("请先设置 API，再添加/登录账号");
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

  async function loadTargets(accountID: string, options?: { silent?: boolean }) {
    const normalizedAccountID = accountID.trim();
    if (!normalizedAccountID) {
      return;
    }
    const silent = options?.silent === true;
    if (!silent) {
      setStatus("正在加载发送对象列表...");
    }
    try {
      const targets = await fetchTGAssistantTargets(props.controllerBaseUrl, props.sessionToken, normalizedAccountID);
      applyTargetList(targets);
      if (!silent) {
        setStatus(`已加载发送对象（${targets.length} 个）`);
      }
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      if (!silent) {
        setStatus(`加载发送对象失败：${msg}`);
      }
    }
  }

  async function handleRefreshTargets() {
    if (!activeLoggedInAccount) {
      setStatus("请先选择一个已登录账号");
      return;
    }
    setIsTargetRefreshing(true);
    setStatus(`正在从服务器刷新会话对象：${activeLoggedInAccount.label}...`);
    try {
      const targets = await refreshTGAssistantTargets(props.controllerBaseUrl, props.sessionToken, activeLoggedInAccount.id);
      applyTargetList(targets);
      setStatus(`已刷新发送对象（${targets.length} 个）`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`刷新发送对象失败：${msg}`);
    } finally {
      setIsTargetRefreshing(false);
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
      setStatus("共享 API ID 必须是正整数");
      return;
    }
    if (!apiHash) {
      setStatus("共享 API Hash 不能为空");
      return;
    }

    setIsLoading(true);
    setStatus("正在设置 API...");
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
      setStatus("请先设置 API");
      return;
    }

    setIsLoading(true);
    setStatus("正在准备步骤1：输入账号并开始登录...");
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
    setStatus(`正在登录账号 ${selectedAccount.label}...`);
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

  function openAddScheduleTaskModal() {
    if (!activeLoggedInAccount) {
      setStatus("请先选择一个已登录账号");
      return;
    }
    setEditingScheduleTaskID("");
    setScheduleEnabled(false);
    setScheduleTargetDraft("");
    setTargetSearchDraft("");
    setScheduleTimeDraft("每天 09:00");
    setScheduleMessageDraft("");
    setScheduleDelayMaxDraft("0");
    setShowScheduleTaskModal(true);
  }

  function openEditScheduleTaskModal(task: TGAssistantSchedule) {
    if (!activeLoggedInAccount) {
      setStatus("请先选择一个已登录账号");
      return;
    }
    setEditingScheduleTaskID(task.id);
    setScheduleEnabled(task.enabled);
    setScheduleTargetDraft(task.target || "");
    setTargetSearchDraft("");
    setScheduleTimeDraft(task.send_at || "");
    setScheduleMessageDraft(task.message || "");
    setScheduleDelayMaxDraft(String(Math.max(0, task.delay_max_sec ?? 0)));
    setShowScheduleTaskModal(true);
  }

  function closeScheduleTaskModal() {
    if (isScheduleLoading) {
      return;
    }
    setShowScheduleTaskModal(false);
    setEditingScheduleTaskID("");
    setTargetSearchDraft("");
  }

  async function handleSaveScheduleTask() {
    if (!activeLoggedInAccount) {
      setStatus("请先选择一个已登录账号");
      return;
    }
    const editingTaskID = editingScheduleTaskID.trim();
    const isEditing = editingTaskID.length > 0;
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
    setStatus(isEditing ? `正在修改任务：${editingTaskID}...` : `正在为账号 ${activeLoggedInAccount.label} 新增任务...`);
    try {
      const schedules = isEditing
        ? await updateTGAssistantSchedule(props.controllerBaseUrl, props.sessionToken, {
          account_id: activeLoggedInAccount.id,
          task_id: editingTaskID,
          task_type: "scheduled_send",
          enabled: scheduleEnabled,
          target,
          send_at: sendAt,
          message,
          delay_min_sec: 0,
          delay_max_sec: delayMax,
        })
        : await addTGAssistantSchedule(props.controllerBaseUrl, props.sessionToken, {
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
      await loadPendingTasks(activeLoggedInAccount.id, { silent: true });
      setShowScheduleTaskModal(false);
      setEditingScheduleTaskID("");
      setScheduleMessageDraft("");
      setTargetSearchDraft("");
      setStatus(isEditing ? `任务已更新（当前共 ${schedules.length} 个）` : `已新增任务：${activeLoggedInAccount.label}（当前共 ${schedules.length} 个）`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`${isEditing ? "修改任务" : "新增任务"}失败：${msg}`);
    } finally {
      setIsScheduleLoading(false);
    }
  }

  async function loadPendingTasks(accountID: string, options?: { silent?: boolean }) {
    const normalizedAccountID = accountID.trim();
    if (!normalizedAccountID) {
      return;
    }
    const silent = options?.silent === true;
    setIsPendingLoading(true);
    if (!silent) {
      setStatus("正在加载待执行队列...");
    }
    try {
      const pending = await fetchTGAssistantPendingTasks(props.controllerBaseUrl, props.sessionToken, normalizedAccountID);
      setPendingTaskItems(pending);
      if (!silent) {
        setStatus(`已加载待执行队列（${pending.length} 条）`);
      }
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      if (!silent) {
        setStatus(`加载待执行队列失败：${msg}`);
      }
    } finally {
      setIsPendingLoading(false);
    }
  }

  async function loadBotAPIKey(accountID: string, options?: { silent?: boolean }) {
    const normalizedAccountID = accountID.trim();
    if (!normalizedAccountID) {
      return;
    }
    const silent = options?.silent === true;
    setIsBotLoading(true);
    if (!silent) {
      setStatus("正在加载 TG BOT 配置...");
    }
    try {
      const payload = await fetchTGAssistantBotAPIKey(props.controllerBaseUrl, props.sessionToken, normalizedAccountID);
      applyBotAPIKey(payload);
      if (!silent) {
        setStatus(payload.configured ? "已加载 TG BOT 配置" : "当前账号尚未配置 TG BOT API key");
      }
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      if (!silent) {
        setStatus(`加载 TG BOT 配置失败：${msg}`);
      }
    } finally {
      setIsBotLoading(false);
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
      await loadPendingTasks(activeLoggedInAccount.id, { silent: true });
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
      setStatus("请先选择一个已登录账号");
      return;
    }
    setIsScheduleLoading(true);
    setStatus(`正在更新任务状态：${taskID}...`);
    try {
      const schedules = await setTGAssistantScheduleEnabled(props.controllerBaseUrl, props.sessionToken, {
        account_id: activeLoggedInAccount.id,
        task_id: taskID,
        enabled,
      });
      applyScheduleList(activeLoggedInAccount.id, schedules);
      await loadPendingTasks(activeLoggedInAccount.id, { silent: true });
      setStatus(`任务状态已更新：${enabled ? "启用" : "停用"}`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`更新任务状态失败：${msg}`);
    } finally {
      setIsScheduleLoading(false);
    }
  }

  async function handleSendNowScheduleTask(taskID: string) {
    if (!activeLoggedInAccount) {
      setStatus("请先选择一个已登录账号");
      return;
    }
    const timeoutMs = 90000;
    setIsScheduleLoading(true);
    setStatus(`正在立即发送任务：${taskID}...`);
    try {
      const result = await sendNowTGAssistantSchedule(props.controllerBaseUrl, props.sessionToken, {
        account_id: activeLoggedInAccount.id,
        task_id: taskID,
      }, { timeoutMs });
      await loadPendingTasks(activeLoggedInAccount.id, { silent: true });
      const tgMessage = (result.tg_message || "").trim();
      setStatus(tgMessage ? `立即发送成功：${tgMessage}` : "立即发送成功");
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`立即发送失败：${msg}`);
    } finally {
      setIsScheduleLoading(false);
    }
  }

  async function openScheduleTaskHistoryModal(taskID: string) {
    if (!activeLoggedInAccount) {
      setStatus("请先选择一个已登录账号");
      return;
    }
    setScheduleHistoryTaskID(taskID);
    setScheduleHistoryItems([]);
    setShowScheduleHistoryModal(true);
    setIsScheduleHistoryLoading(true);
    setStatus(`正在加载任务记录：${taskID}...`);
    try {
      const history = await fetchTGAssistantScheduleTaskHistory(props.controllerBaseUrl, props.sessionToken, {
        account_id: activeLoggedInAccount.id,
        task_id: taskID,
        limit: 360,
      });
      setScheduleHistoryItems(history);
      setStatus(`已加载任务记录：${history.length} 条`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`加载任务记录失败：${msg}`);
    } finally {
      setIsScheduleHistoryLoading(false);
    }
  }

  function closeScheduleTaskHistoryModal() {
    if (isScheduleHistoryLoading) {
      return;
    }
    setShowScheduleHistoryModal(false);
    setScheduleHistoryTaskID("");
    setScheduleHistoryItems([]);
  }

  function openBotAPIKeyModal() {
    if (!activeLoggedInAccount) {
      setStatus("请先选择一个已登录账号");
      return;
    }
    setBotAPIKeyDraft(botAPIKeyInput);
    setBotModeDraft(botModeInput);
    setShowBotAPIKeyModal(true);
  }

  function closeBotAPIKeyModal() {
    if (isBotLoading) {
      return;
    }
    setShowBotAPIKeyModal(false);
    setBotAPIKeyDraft("");
  }

  async function handleSaveBotAPIKey() {
    if (!activeLoggedInAccount) {
      setStatus("请先选择一个已登录账号");
      return;
    }
    const apiKey = botAPIKeyDraft.trim();
    const mode = botModeDraft;
    if (!apiKey) {
      setStatus("BOT API key 不能为空");
      return;
    }
    if (!apiKey.includes(":")) {
      setStatus("BOT API key 格式不正确");
      return;
    }

    setIsBotLoading(true);
    setStatus("正在保存 TG BOT API key...");
    try {
      const payload = await setTGAssistantBotAPIKey(props.controllerBaseUrl, props.sessionToken, {
        account_id: activeLoggedInAccount.id,
        api_key: apiKey,
        mode,
      });
      applyBotAPIKey(payload);
      setShowBotAPIKeyModal(false);
      setStatus(`TG BOT 配置已保存（模式：${mode === "webhook" ? "webhook" : "getUpdates"}）`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`保存 TG BOT API key 失败：${msg}`);
    } finally {
      setIsBotLoading(false);
    }
  }

  async function handleTestBotSend() {
    if (!activeLoggedInAccount) {
      setStatus("请先选择一个已登录账号");
      return;
    }
    const message = botTestMessageDraft.trim();
    if (!message) {
      setStatus("请输入测试消息内容");
      return;
    }

    setIsBotLoading(true);
    setStatus("正在发送 TG BOT 测试消息...");
    try {
      const result: TGAssistantBotTestSendResult = await testSendTGAssistantBotMessage(
        props.controllerBaseUrl,
        props.sessionToken,
        {
          account_id: activeLoggedInAccount.id,
          message,
        },
      );
      const text = (result.message || "").trim();
      if (text) {
        setStatus(`TG BOT 测试发送成功：${text}`);
      } else {
        setStatus(`TG BOT 测试发送成功：chat_id=${result.chat_id} message_id=${result.message_id}`);
      }
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`TG BOT 测试发送失败：${msg}`);
    } finally {
      setIsBotLoading(false);
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
              <button
                className={`subtab-btn ${activeAccountDetailSubTab === "tg-bot" ? "active" : ""}`}
                onClick={() => setActiveAccountDetailSubTab("tg-bot")}
                disabled={isLoading || isBotLoading || !activeLoggedInAccount}
              >
                TG BOT
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
                ) : activeAccountDetailSubTab === "tg-bot" ? (
                  <div className="tg-schedule-panel">
                    <div className="tg-account-basic-row">
                      <div className="tg-account-basic-label">当前账号</div>
                      <div className="tg-account-basic-value">
                        {activeLoggedInAccount.label} ({activeLoggedInAccount.phone})
                      </div>
                    </div>
                    <div className="tg-account-basic-row">
                      <div className="tg-account-basic-label">BOT 状态</div>
                      <div className="tg-account-basic-value">{botConfigured ? "已配置" : "未配置"}</div>
                    </div>
                    <div className="tg-account-basic-row">
                      <div className="tg-account-basic-label">接收模式</div>
                      <div className="tg-account-basic-value">{botModeInput === "webhook" ? "webhook" : "getUpdates"}</div>
                    </div>
                    <div className="tg-account-basic-row">
                      <div className="tg-account-basic-label">当前 Key</div>
                      <div className="tg-account-basic-value">{botConfigured ? botAPIKeyInput : "-"}</div>
                    </div>
                    {botModeInput === "webhook" ? (
                      <div className="tg-account-basic-row">
                        <div className="tg-account-basic-label">Webhook 回调</div>
                        <div className="tg-account-basic-value">
                          {botWebhookPath || "-"} {botWebhookEnabled ? "(已启用)" : "(未启用)"}
                        </div>
                      </div>
                    ) : null}
                    <div className="tg-schedule-field">
                      <label className="tg-account-basic-label" htmlFor="tg-bot-test-message">
                        测试消息
                      </label>
                      <textarea
                        id="tg-bot-test-message"
                        className="input tg-schedule-textarea"
                        value={botTestMessageDraft}
                        onChange={(event) => setBotTestMessageDraft(event.target.value)}
                        placeholder="输入测试消息内容"
                        disabled={isLoading || isBotLoading}
                      />
                    </div>
                    <div className="content-actions">
                      <button className="btn" onClick={openBotAPIKeyModal} disabled={isLoading || isBotLoading || !activeLoggedInAccount}>
                        设置 BOT API key
                      </button>
                      <button
                        className="btn"
                        onClick={() => void loadBotAPIKey(activeLoggedInAccount.id)}
                        disabled={isLoading || isBotLoading || !activeLoggedInAccount}
                      >
                        {isBotLoading ? "刷新中..." : "刷新 BOT 配置"}
                      </button>
                      <button
                        className="btn"
                        onClick={() => void handleTestBotSend()}
                        disabled={isLoading || isBotLoading || !activeLoggedInAccount || !botConfigured}
                      >
                        测试发送
                      </button>
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
                    <div className="content-actions" style={{ marginTop: 10 }}>
                      <button className="btn" onClick={openAddScheduleTaskModal} disabled={isLoading || isScheduleLoading || isTargetRefreshing || !activeLoggedInAccount}>
                        新增定时发送任务
                      </button>
                      <button
                        className="btn"
                        onClick={() => void handleRefreshTargets()}
                        disabled={isLoading || isScheduleLoading || isTargetRefreshing || !activeLoggedInAccount}
                      >
                        {isTargetRefreshing ? "刷新对象中..." : "刷新发送对象"}
                      </button>
                      <button
                        className="btn"
                        onClick={() => void loadPendingTasks(activeLoggedInAccount.id)}
                        disabled={isLoading || isScheduleLoading || isPendingLoading || !activeLoggedInAccount}
                      >
                        {isPendingLoading ? "刷新队列中..." : "刷新待执行队列"}
                      </button>
                    </div>
                    <div className="tg-schedule-list">
                      {scheduleList.length === 0 ? (
                        <div className="tg-account-summary-line">当前账号暂无任务。</div>
                      ) : (
                        <div className="probe-table-wrap">
                          <table className="probe-table" style={{ minWidth: 900 }}>
                            <thead>
                              <tr>
                                <th>任务类型</th>
                                <th>启用</th>
                                <th>发送目标</th>
                                <th>发送时间</th>
                                <th>发送内容</th>
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
                                    <div className="tg-schedule-table-actions">
                                      <button
                                        className="btn"
                                        onClick={() => openEditScheduleTaskModal(task)}
                                        disabled={isLoading || isScheduleLoading || task.task_type !== "scheduled_send"}
                                      >
                                        编辑
                                      </button>
                                      <button
                                        className="btn"
                                        onClick={() => void handleSendNowScheduleTask(task.id)}
                                        disabled={isLoading || isScheduleLoading || task.task_type !== "scheduled_send"}
                                      >
                                        立即发送
                                      </button>
                                      <button
                                        className="btn"
                                        onClick={() => void openScheduleTaskHistoryModal(task.id)}
                                        disabled={isLoading || isScheduleLoading}
                                      >
                                        任务记录
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
                    <div className="tg-schedule-list">
                      <div className="tg-account-basic-label">待执行队列（{pendingTaskItems.length}）</div>
                      {pendingTaskItems.length === 0 ? (
                        <div className="tg-account-summary-line">当前没有待执行任务。</div>
                      ) : (
                        <div className="probe-table-wrap" style={{ marginTop: 0 }}>
                          <table className="probe-table" style={{ minWidth: 980 }}>
                            <thead>
                              <tr>
                                <th>任务ID</th>
                                <th>发送目标</th>
                                <th>发送时间</th>
                                <th>发送内容</th>
                                <th>随机延时(秒)</th>
                                <th>下一次执行</th>
                                <th>超时执行</th>
                                <th>状态</th>
                                <th>更新时间</th>
                              </tr>
                            </thead>
                            <tbody>
                              {pendingTaskItems.map((item) => (
                                <tr key={`tg-pending-${item.job_key}`}>
                                  <td>
                                    <div>{item.task_id || "-"}</div>
                                    <div className="probe-table-sub">{item.job_key || "-"}</div>
                                  </td>
                                  <td>{renderTargetLabel(item.target || "")}</td>
                                  <td>{item.send_at || "-"}</td>
                                  <td>
                                    <div className="tg-schedule-table-message">{item.message || "-"}</div>
                                  </td>
                                  <td>{item.delay_sec}</td>
                                  <td>{formatDateTime(item.next_run_at)}</td>
                                  <td>{formatDateTime(item.timeout_at || "")}</td>
                                  <td>{!item.task_exists ? "任务不存在" : item.enabled ? "待执行" : "任务停用"}</td>
                                  <td>{formatDateTime(item.updated_at || "")}</td>
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

      {showScheduleTaskModal ? (
        <div className="probe-settings-modal-mask" onClick={closeScheduleTaskModal}>
          <div className="probe-settings-modal" onClick={(event) => event.stopPropagation()}>
            <h3 style={{ marginTop: 0 }}>{editingScheduleTaskID ? "修改任务" : "新增定时发送任务"}</h3>
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
              <label className="tg-account-basic-label" htmlFor="tg-schedule-modal-target">
                发送目标
              </label>
              <input
                className="input"
                value={targetSearchDraft}
                onChange={(event) => setTargetSearchDraft(event.target.value)}
                placeholder="搜索名称 / @username / ID"
                disabled={isLoading || isScheduleLoading || isTargetRefreshing}
                style={{ marginBottom: 8 }}
              />
              <div className="tg-target-picker">
                <select
                  id="tg-schedule-modal-target"
                  className="input"
                  value={scheduleTargetDraft}
                  onChange={(event) => setScheduleTargetDraft(event.target.value)}
                  disabled={isLoading || isScheduleLoading || isTargetRefreshing}
                >
                  <option value="">请选择发送对象</option>
                  {scheduleTargetDraft && !filteredTargetList.some((item) => item.id === scheduleTargetDraft) ? (
                    <option value={scheduleTargetDraft}>{scheduleTargetDraft}</option>
                  ) : null}
                  {filteredTargetList.map((item) => (
                    <option key={`tg-target-modal-${item.id}`} value={item.id}>
                      {item.username ? `${item.name} (@${item.username})` : item.name}
                    </option>
                  ))}
                </select>
                <button
                  className="btn"
                  onClick={() => void handleRefreshTargets()}
                  disabled={isLoading || isScheduleLoading || isTargetRefreshing || !activeLoggedInAccount}
                >
                  {isTargetRefreshing ? "刷新中..." : "刷新"}
                </button>
              </div>
            </div>
            <div className="tg-schedule-field">
              <label className="tg-account-basic-label" htmlFor="tg-schedule-modal-time">
                发送时间
              </label>
              <input
                id="tg-schedule-modal-time"
                className="input"
                value={scheduleTimeDraft}
                onChange={(event) => setScheduleTimeDraft(event.target.value)}
                placeholder="例如：每天 09:00 / 每周一 10:30"
                disabled={isLoading || isScheduleLoading}
              />
            </div>
            <div className="tg-schedule-field">
              <label className="tg-account-basic-label" htmlFor="tg-schedule-modal-message">
                发送内容
              </label>
              <textarea
                id="tg-schedule-modal-message"
                className="input tg-schedule-textarea"
                value={scheduleMessageDraft}
                onChange={(event) => setScheduleMessageDraft(event.target.value)}
                placeholder="请输入计划发送的消息内容"
                disabled={isLoading || isScheduleLoading}
              />
            </div>
            <div className="tg-schedule-field">
              <label className="tg-account-basic-label" htmlFor="tg-schedule-modal-delay-max">随机延时最大值（秒）</label>
              <input
                id="tg-schedule-modal-delay-max"
                className="input"
                type="number"
                min={0}
                value={scheduleDelayMaxDraft}
                onChange={(event) => setScheduleDelayMaxDraft(event.target.value)}
                placeholder="例如：30"
                disabled={isLoading || isScheduleLoading}
              />
            </div>
            <div className="content-actions">
              <button className="btn" onClick={() => void handleSaveScheduleTask()} disabled={isLoading || isScheduleLoading}>
                {editingScheduleTaskID ? "保存修改" : "新增任务"}
              </button>
              <button className="btn" onClick={closeScheduleTaskModal} disabled={isLoading || isScheduleLoading}>取消</button>
            </div>
          </div>
        </div>
      ) : null}

      {showScheduleHistoryModal ? (
        <div className="probe-settings-modal-mask" onClick={closeScheduleTaskHistoryModal}>
          <div className="probe-settings-modal" onClick={(event) => event.stopPropagation()}>
            <h3 style={{ marginTop: 0 }}>任务记录</h3>
            <div className="tg-account-summary-line" style={{ color: "#dceaff", marginBottom: 10 }}>
              任务ID：{scheduleHistoryTaskID || "-"}
            </div>
            {isScheduleHistoryLoading ? (
              <div className="tg-account-summary-line">加载中...</div>
            ) : scheduleHistoryItems.length === 0 ? (
              <div className="tg-account-summary-line">暂无任务记录。</div>
            ) : (
              <div className="probe-table-wrap" style={{ marginTop: 0 }}>
                <table className="probe-table" style={{ minWidth: 860 }}>
                  <thead>
                    <tr>
                      <th>时间</th>
                      <th>动作</th>
                      <th>结果</th>
                      <th>内容</th>
                    </tr>
                  </thead>
                  <tbody>
                    {scheduleHistoryItems.map((item, idx) => (
                      <tr key={`tg-task-history-${idx}`}>
                        <td>{formatDateTime(item.time || "")}</td>
                        <td>{item.action || "-"}</td>
                        <td>{item.success ? "成功" : "失败"}</td>
                        <td>
                          <div className="tg-schedule-table-message">{item.message || "-"}</div>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
            <div className="content-actions">
              <button className="btn" onClick={closeScheduleTaskHistoryModal} disabled={isScheduleHistoryLoading}>关闭</button>
            </div>
          </div>
        </div>
      ) : null}

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
              <label>共享 API ID</label>
              <input
                className="input"
                value={apiKeyDraftID}
                onChange={(event) => setAPIKeyDraftID(event.target.value)}
                placeholder="来自 my.telegram.org"
                disabled={isLoading}
              />
            </div>
            <div className="row">
              <label>共享 API Hash</label>
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

      {showBotAPIKeyModal ? (
        <div className="probe-settings-modal-mask" onClick={closeBotAPIKeyModal}>
          <div className="probe-settings-modal" onClick={(event) => event.stopPropagation()}>
            <h3 style={{ marginTop: 0 }}>设置 TG BOT API key</h3>
            <div className="row">
              <label>接收模式</label>
              <select
                className="input"
                value={botModeDraft}
                onChange={(event) => setBotModeDraft(event.target.value === "webhook" ? "webhook" : "polling")}
                disabled={isBotLoading}
              >
                <option value="polling">getUpdates（长轮询）</option>
                <option value="webhook">webhook（回调）</option>
              </select>
            </div>
            <div className="row" style={{ marginBottom: 0 }}>
              <label>BOT API key</label>
              <input
                className="input"
                value={botAPIKeyDraft}
                onChange={(event) => setBotAPIKeyDraft(event.target.value)}
                placeholder="例如：123456:AA..."
                disabled={isBotLoading}
              />
            </div>
            <div className="content-actions">
              <button className="btn" onClick={() => void handleSaveBotAPIKey()} disabled={isBotLoading}>保存</button>
              <button className="btn" onClick={closeBotAPIKeyModal} disabled={isBotLoading}>取消</button>
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
