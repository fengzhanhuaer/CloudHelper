/**
 * useLogViewer.ts — 管理日志查看状态管理
 *
 * 重构说明 (PKG-FE-R03 / RQ-003 / C-FE-01 / C-FE-04):
 * - local 模式: GET /api/logs/manager — 已由 manager_service 实现
 * - server 模式（主控日志）: [W4-PENDING] 后端代理端点未实现，显式报告不可用
 * - 禁止调用 fetchServerLogs / fetchAdminStatus (controller-api 直连)
 */
import { useCallback, useState } from "react";
import { apiGetLogs } from "../manager-api";
import type { LogEntry, LogLevel, LogSource } from "../types";

function clampLogLines(lines: number): number {
  if (!Number.isFinite(lines)) return 200;
  const n = Math.trunc(lines);
  if (n <= 0) return 200;
  if (n > 2000) return 2000;
  return n;
}

function clampSinceMinutes(minutes: number): number {
  if (!Number.isFinite(minutes)) return 0;
  const n = Math.trunc(minutes);
  if (n <= 0) return 0;
  if (n > 2000) return 2000;
  return n;
}

function normalizeLogLevel(raw: unknown): LogLevel {
  switch (String(raw || "").trim().toLowerCase()) {
    case "realtime": return "realtime";
    case "warning":
    case "warn": return "warning";
    case "error":
    case "err": return "error";
    default: return "normal";
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
    return entries.map((e) => e.line || e.message).filter((l) => l.trim()).join("\n");
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

  /** refreshLogs — local 模式调用 manager_service API，server 模式显式不可用 */
  const refreshLogs = useCallback(
    async (_baseUrlInput: string, _token: string, _reauthenticate?: () => Promise<string>) => {
      const lineLimit = clampLogLines(lines);
      setIsLoading(true);
      setStatus(`正在刷新${source === "local" ? "本地" : "服务器"}日志...`);
      try {
        if (source === "local") {
          // GET /api/logs/manager — 已实现
          const data = await apiGetLogs({ lines: lineLimit, sinceMinutes, minLevel });
          const entries = Array.isArray(data.entries)
            ? (data.entries as Partial<LogEntry>[]).map(normalizeLogEntry)
            : [];
          const resolvedContent = buildLogContent(entries, "");
          setContent(resolvedContent);
          setCopyStatus("");
          setStatus(`已加载本地日志 (${entries.length} 行，级别≥${minLevel})`);
          setLogFilePath("");
        } else {
          // [W4-PENDING] 主控日志代理端点未实现
          throw new Error("[W4-PENDING] 主控日志查看功能暂未在 manager_service 实现，请等待 W4 后端代理端点就绪");
        }
      } catch (error) {
        const msg = error instanceof Error ? error.message : "unknown error";
        setStatus(`日志加载失败：${msg}`);
      } finally {
        setIsLoading(false);
      }
    },
    [lines, minLevel, sinceMinutes, source]
  );

  function clearLogs() {
    setStatus("未加载日志");
    setLogFilePath("");
    setContent("");
    setCopyStatus("");
  }

  const copyLogs = useCallback(async () => {
    const text = content.trim();
    if (!text) { setCopyStatus("暂无日志可复制"); return; }
    try {
      if (navigator?.clipboard?.writeText) {
        await navigator.clipboard.writeText(content);
      } else {
        const ta = document.createElement("textarea");
        ta.value = content;
        ta.style.cssText = "position:fixed;opacity:0";
        document.body.appendChild(ta);
        ta.select();
        document.execCommand("copy");
        ta.remove();
      }
      setCopyStatus("已复制日志内容");
    } catch (error) {
      setCopyStatus(`复制失败：${error instanceof Error ? error.message : "unknown"}`);
    }
  }, [content]);

  const updateLines = useCallback((value: number) => setLines(clampLogLines(value)), []);
  const updateSource = useCallback((value: LogSource) => {
    setSource(value);
    setCopyStatus("");
  }, []);
  const updateSinceMinutes = useCallback((value: number) => setSinceMinutes(clampSinceMinutes(value)), []);

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
