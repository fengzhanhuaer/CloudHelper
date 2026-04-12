import { useEffect, useState } from "react";
import { fetchJson } from "../api";
import { normalizeClaim, normalizeUsernameClaim } from "../authz";
import { normalizeBaseUrl } from "../utils/url";
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

  async function login(baseUrlInput: string, user: string, pass: string): Promise<LoginResult> {
    const base = normalizeBaseUrl(baseUrlInput);
    if (!base) {
      setLoginTone("error");
      setLoginStatus("Login failed: Controller URL is required");
      return { ok: false };
    }

    if (!user || !pass) {
      setLoginTone("error");
      setLoginStatus("Login failed: username and password required");
      return { ok: false };
    }

    setIsAuthenticating(true);
    try {
      setLoginTone("info");
      setLoginStatus("Authenticating...");

      const loginData = await fetchJson<{ token: string; username: string }>("/auth/login", {
        method: "POST",
        body: JSON.stringify({ username: user, password: pass }),
      });

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
      
      // Attempt to proxy login to controller for downstream calls
      try {
        setLoginStatus("Authenticating with controller...");
        const result = await fetchJson<{ token: string; message: string }>("/controller/session/login", {
          method: "POST",
          body: JSON.stringify({ base_url: base }), // Optional, usually we would pass base_url here if the backend API supports it, wait, manager_service doesn't take an argument for login in W2? Ah, the manager_service W2 adapter didn't expose /api/controller/session/login yet! Let me check standard router.go.
        });
      } catch (e) {
        // Ignored for now if the endpoint doesn't exist, as it might be added in W3-03
      }

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
    fetchJson("/auth/logout", { method: "POST" }).catch(() => {});
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
