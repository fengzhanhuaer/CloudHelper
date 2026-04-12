import { useEffect, useState } from "react";
import { apiLogin, apiLogout, apiGetControllerSession } from "../manager-api";
import { normalizeUsernameClaim } from "../authz";
import type { StatusTone } from "../types";

type LoginResult = {
  ok: boolean;
  userRole?: string;
  certType?: string;
  sessionToken?: string;
};

export function useAuthFlow() {
  const [sessionToken, setSessionToken] = useState(localStorage.getItem("manager_session_token") || "");
  const [loginStatus, setLoginStatus] = useState("Please login");
  const [loginTone, setLoginTone] = useState<StatusTone>("info");
  const [isAuthenticating, setIsAuthenticating] = useState(false);

  const [username, setUsername] = useState("admin");
  const [userRole, setUserRole] = useState("admin"); // Default to admin for simplicity in manager service
  const [certType, setCertType] = useState("admin");

  useEffect(() => {
    const handleUnauthorized = () => {
      logout();
      setLoginTone("error");
      setLoginStatus("Session expired, please login again");
    };
    window.addEventListener("unauthorized", handleUnauthorized);
    return () => window.removeEventListener("unauthorized", handleUnauthorized);
  }, []);

  async function login(_baseUrlInput: string, user: string, pass: string): Promise<LoginResult> {
    if (!user || !pass) {
      setLoginTone("error");
      setLoginStatus("Login failed: username and password required");
      return { ok: false };
    }

    setIsAuthenticating(true);
    try {
      setLoginTone("info");
      setLoginStatus("Authenticating...");

      const loginData = await apiLogin(user, pass);

      const normalizedUser = normalizeUsernameClaim(loginData.username, "admin");
      // we assume manager_service gives admin
      const role = "admin";
      const type = "admin";

      setSessionToken(loginData.token);
      localStorage.setItem("manager_session_token", loginData.token);
      setUsername(normalizedUser);
      setUserRole(role);
      setCertType(type);

      setLoginTone("success");
      setLoginStatus(`Login successful: username=${normalizedUser}`);

      // Attempt to refresh cached controller session token (non-blocking, C-FE-04)
      apiGetControllerSession().catch(() => {
        // Controller session may not be configured yet — not a login failure.
      });

      return { ok: true, userRole: role, certType: type, sessionToken: loginData.token };
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setSessionToken("");
      localStorage.removeItem("manager_session_token");
      setLoginTone("error");
      setLoginStatus(`Login failed: ${msg}`);
      return { ok: false };
    } finally {
      setIsAuthenticating(false);
    }
  }

  function logout() {
    setSessionToken("");
    localStorage.removeItem("manager_session_token");
    apiLogout();
    setUsername("admin");
    setUserRole("viewer");
    setCertType("viewer");
    setLoginTone("info");
    setLoginStatus("Logged out");
  }

  return {
    sessionToken,
    loginStatus,
    loginTone,
    isAuthenticating,
    username,
    userRole,
    certType,
    login,
    logout,
  };
}
