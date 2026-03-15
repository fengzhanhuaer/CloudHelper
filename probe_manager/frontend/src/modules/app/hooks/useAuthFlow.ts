import { useEffect, useState } from "react";
import { GetLocalPrivateKeyStatus, SignNonceWithLocalKey } from "../../../../wailsjs/go/main/App";
import { normalizeClaim, normalizeUsernameClaim } from "../authz";
import { requestAuthNonce, submitAuthLogin } from "../services/controller-api";
import { normalizeBaseUrl } from "../utils/url";
import type { PrivateKeyStatus, StatusTone } from "../types";

type LoginResult = {
  ok: boolean;
  userRole?: string;
  certType?: string;
  sessionToken?: string;
};

export function useAuthFlow() {
  const [sessionToken, setSessionToken] = useState("");
  const [loginStatus, setLoginStatus] = useState("请点击 Login 开始鉴权");
  const [loginTone, setLoginTone] = useState<StatusTone>("info");
  const [isAuthenticating, setIsAuthenticating] = useState(false);

  const [privateKeyStatus, setPrivateKeyStatus] = useState("");
  const [privateKeyPath, setPrivateKeyPath] = useState("");

  const [username, setUsername] = useState("admin");
  const [userRole, setUserRole] = useState("viewer");
  const [certType, setCertType] = useState("viewer");

  async function refreshPrivateKeyStatus() {
    try {
      const status = (await GetLocalPrivateKeyStatus()) as PrivateKeyStatus;
      if (status.found) {
        setPrivateKeyStatus("本地私钥可用");
        setPrivateKeyPath(status.path ?? "");
      } else {
        setPrivateKeyStatus(`本地私钥不可用：${status.message ?? "未找到"}`);
        setPrivateKeyPath("");
      }
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setPrivateKeyStatus(`本地私钥检查异常：${msg}`);
      setPrivateKeyPath("");
    }
  }

  async function login(baseUrlInput: string): Promise<LoginResult> {
    const base = normalizeBaseUrl(baseUrlInput);
    if (!base) {
      setLoginTone("error");
      setLoginStatus("登录失败：Controller URL is required");
      return { ok: false };
    }

    setIsAuthenticating(true);
    try {
      setLoginTone("info");
      setLoginStatus("登录验证中：检查本地私钥...");

      const keyState = (await GetLocalPrivateKeyStatus()) as PrivateKeyStatus;
      if (!keyState.found) {
        const reason = keyState.message ?? "not found";
        setSessionToken("");
        setPrivateKeyStatus(`本地私钥不可用：${reason}`);
        setPrivateKeyPath("");
        setLoginTone("error");
        setLoginStatus(`登录失败：本地私钥不可用（${reason}）`);
        return { ok: false };
      }

      setPrivateKeyStatus("本地私钥可用");
      setPrivateKeyPath(keyState.path ?? "");

      setLoginStatus("登录验证中：请求 challenge nonce...");
      const nonceData = await requestAuthNonce(base);

      setLoginStatus("登录验证中：使用本地私钥签名...");
      const signature = await SignNonceWithLocalKey(nonceData.nonce);

      setLoginStatus("登录验证中：提交签名验证...");
      const loginData = await submitAuthLogin(base, nonceData.nonce, signature);

      const user = normalizeUsernameClaim(loginData.username, "admin");
      const role = normalizeClaim(loginData.user_role, "admin");
      const type = normalizeClaim(loginData.cert_type, role);

      setSessionToken(loginData.session_token);
      setUsername(user);
      setUserRole(role);
      setCertType(type);

      setLoginTone("success");
      setLoginStatus(`登录成功：username=${user}, role=${role}, certType=${type}, TTL=${loginData.ttl}s`);
      return { ok: true, userRole: role, certType: type, sessionToken: loginData.session_token };
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setSessionToken("");
      setLoginTone("error");
      setLoginStatus(`登录失败：${msg}`);
      return { ok: false };
    } finally {
      setIsAuthenticating(false);
    }
  }

  function logout() {
    setSessionToken("");
    setUsername("admin");
    setUserRole("viewer");
    setCertType("viewer");
    setLoginTone("info");
    setLoginStatus("已退出登录");
  }

  useEffect(() => {
    void refreshPrivateKeyStatus();
  }, []);

  return {
    sessionToken,
    loginStatus,
    loginTone,
    isAuthenticating,
    privateKeyStatus,
    privateKeyPath,
    username,
    userRole,
    certType,
    refreshPrivateKeyStatus,
    login,
    logout,
  };
}
