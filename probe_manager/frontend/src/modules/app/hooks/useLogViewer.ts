import { useCallback, useState } from "react";
import { GetLocalManagerLogs } from "../../../../wailsjs/go/main/App";
import { fetchAdminStatus, fetchServerLogs } from "../services/controller-api";
import type { LogContentResponse, LogEntry, LogLevel, LogSource } from "../types";
import { normalizeBaseUrl } from "../utils/url";

function isUnauthorizedError(error: unknown): boolean {
  if (!(error instanceof Error)) {
    return false;
  }
  const message = error.message.toLowerCase();
  return message.includes("401") || message.includes("invalid or expired session token");
}

function isNetworkLikeFetchError(error: unknown): boolean {
  if (!(error instanceof Error)) {
    return false;
  }
  const message = error.message.toLowerCase();
  return message.includes("failed to fetch") || message.includes("networkerror") || message.includes("load failed");
}

function clampLogLines(lines: number): number {
  if (!Number.isFinite(lines)) {
    return 200;
  }
  const normalized = Math.trunc(lines);
  if (normalized <= 0) {
    return 200;
  }
  if (normalized > 2000) {
    return 2000;
  }
  return normalized;
}

function clampSinceMinutes(minutes: number): number {
  if (!Number.isFinite(minutes)) {
    return 0;
  }
  const normalized = Math.trunc(minutes);
  if (normalized <= 0) {
    return 0;
  }
  if (normalized > 2000) {
    return 2000;
  }
  return normalized;
}

function normalizeLogLevel(raw: unknown): LogLevel {
  switch (String(raw || "").trim().toLowerCase()) {
    case "realtime":
      return "realtime";
    case "warning":
    case "warn":
      return "warning";
    case "error":
    case "err":
      return "error";
    default:
      return "normal";
  }
}

function normalizeLogEntry(raw: Partial<LogEntry>): LogEntry {
  return {
    time: typeof raw.time === "string" ? raw.time : "",
    level: normalizeLogLevel(raw.level),
    message: typeof raw.message === "string" ? raw.message : "",
    line: typeof raw.line === "string" ? raw.line : "",
  };
}

function buildLogContent(entries: LogEntry[], fallbackContent: string): string {
  if (entries.length > 0) {
    return entries.map((entry) => entry.line || entry.message).filter((line) => line.trim()).join("\n");
  }
  return fallbackContent;
}

export function useLogViewer() {
  const [source, setSource] = useState<LogSource>("local");
  const [lines, setLines] = useState(200);
  const [sinceMinutes, setSinceMinutes] = useState(0);
  const [minLevel, setMinLevel] = useState<LogLevel>("normal");
  const [autoScroll, setAutoScroll] = useState(true);
  const [isLoading, setIsLoading] = useState(false);
  const [status, setStatus] = useState("未加载日志");
  const [logFilePath, setLogFilePath] = useState("");
  const [content, setContent] = useState("");
  const [copyStatus, setCopyStatus] = useState("");

  const refreshLogs = useCallback(async (baseUrlInput: string, token: string, reauthenticate?: () => Promise<string>) => {
    const lineLimit = clampLogLines(lines);
    setIsLoading(true);
    setStatus(`正在刷新${source === "local" ? "本地" : "服务器"}日志...`);
    try {
      let data: LogContentResponse;
      if (source === "local") {
        data = (await GetLocalManagerLogs(lineLimit, sinceMinutes, minLevel)) as LogContentResponse;
      } else {
        const base = normalizeBaseUrl(baseUrlInput);
        if (!base) {
          setStatus("Controller URL is required");
          return;
        }
        if (!token) {
          setStatus("未登录，无法读取服务器日志");
          return;
        }

        let activeToken = token;
        let reloginUsed = false;
        try {
          data = await fetchServerLogs(base, activeToken, lineLimit, sinceMinutes, minLevel);
        } catch (error) {
          if (!reauthenticate || !isUnauthorizedError(error)) {
            throw error;
          }
          setStatus("会话已过期，正在自动重新登录...");
          activeToken = await reauthenticate();
          reloginUsed = true;
          data = await fetchServerLogs(base, activeToken, lineLimit, sinceMinutes, minLevel);
        }

        if (reloginUsed) {
          setStatus("已自动重新登录并刷新服务器日志");
        }
      }

      const entries = Array.isArray(data.entries) ? data.entries.map((entry) => normalizeLogEntry(entry)) : [];
      const resolvedContent = buildLogContent(entries, data.content || "");
      setLogFilePath(data.file_path || "");
      setContent(resolvedContent);
      setCopyStatus("");
      setStatus(`已加载${data.source === "server" ? "服务器" : "本地"}日志（${entries.length || data.lines} 行，级别≥${minLevel}）`);
    } catch (error) {
      if (source === "server" && isNetworkLikeFetchError(error)) {
        const base = normalizeBaseUrl(baseUrlInput);
        if (base && token) {
          try {
            await fetchAdminStatus(base, token);
            setStatus("日志加载失败：服务端可能未升级到支持 /api/admin/logs 的版本（或该路径被网关拦截）");
            return;
          } catch {
            // fallback to original error message below
          }
        }
      }
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`日志加载失败：${msg}`);
    } finally {
      setIsLoading(false);
    }
  }, [lines, minLevel, sinceMinutes, source]);

  function clearLogs() {
    setStatus("未加载日志");
    setLogFilePath("");
    setContent("");
    setCopyStatus("");
  }

  const copyLogs = useCallback(async () => {
    const text = content.trim();
    if (!text) {
      setCopyStatus("暂无日志可复制");
      return;
    }

    try {
      if (typeof navigator !== "undefined" && navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(content);
      } else if (typeof document !== "undefined") {
        const textarea = document.createElement("textarea");
        textarea.value = content;
        textarea.style.position = "fixed";
        textarea.style.opacity = "0";
        document.body.appendChild(textarea);
        textarea.focus();
        textarea.select();
        document.execCommand("copy");
        document.body.removeChild(textarea);
      } else {
        throw new Error("clipboard api unavailable");
      }
      setCopyStatus("已复制日志内容");
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setCopyStatus(`复制失败：${msg}`);
    }
  }, [content]);

  const updateLines = useCallback((value: number) => {
    setLines(clampLogLines(value));
  }, []);

  const updateSource = useCallback((value: LogSource) => {
    setSource(value);
    setCopyStatus("");
  }, []);

  const updateSinceMinutes = useCallback((value: number) => {
    setSinceMinutes(clampSinceMinutes(value));
  }, []);

  return {
    source,
    setSource: updateSource,
    lines,
    setLines: updateLines,
    sinceMinutes,
    setSinceMinutes: updateSinceMinutes,
    minLevel,
    setMinLevel,
    autoScroll,
    setAutoScroll,
    isLoading,
    status,
    logFilePath,
    content,
    copyStatus,
    refreshLogs,
    copyLogs,
    clearLogs,
  };
}
