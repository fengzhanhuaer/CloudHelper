import { useCallback, useState } from "react";
import { GetLocalManagerLogs } from "../../../../wailsjs/go/main/App";
import { fetchServerLogs } from "../services/controller-api";
import type { LogContentResponse, LogSource } from "../types";
import { normalizeBaseUrl } from "../utils/url";

function isUnauthorizedError(error: unknown): boolean {
  if (!(error instanceof Error)) {
    return false;
  }
  const message = error.message.toLowerCase();
  return message.includes("401") || message.includes("invalid or expired session token");
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

export function useLogViewer() {
  const [source, setSource] = useState<LogSource>("local");
  const [lines, setLines] = useState(200);
  const [sinceMinutes, setSinceMinutes] = useState(0);
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
        data = (await GetLocalManagerLogs(lineLimit, sinceMinutes)) as LogContentResponse;
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
          data = await fetchServerLogs(base, activeToken, lineLimit, sinceMinutes);
        } catch (error) {
          if (!reauthenticate || !isUnauthorizedError(error)) {
            throw error;
          }
          setStatus("会话已过期，正在自动重新登录...");
          activeToken = await reauthenticate();
          reloginUsed = true;
          data = await fetchServerLogs(base, activeToken, lineLimit, sinceMinutes);
        }

        if (reloginUsed) {
          setStatus("已自动重新登录并刷新服务器日志");
        }
      }

      setLogFilePath(data.file_path || "");
      setContent(data.content || "");
      setCopyStatus("");
      setStatus(`已加载${data.source === "server" ? "服务器" : "本地"}日志（${data.lines} 行）`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`日志加载失败：${msg}`);
    } finally {
      setIsLoading(false);
    }
  }, [lines, sinceMinutes, source]);

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
