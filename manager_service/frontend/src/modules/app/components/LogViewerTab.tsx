import { useEffect, useRef } from "react";
import type { LogLevel, LogSource } from "../types";

type LogViewerTabProps = {
  source: LogSource;
  onSourceChange: (value: LogSource) => void;
  lines: number;
  onLinesChange: (value: number) => void;
  sinceMinutes: number;
  onSinceMinutesChange: (value: number) => void;
  minLevel: LogLevel;
  onMinLevelChange: (value: LogLevel) => void;
  autoScroll: boolean;
  onAutoScrollChange: (value: boolean) => void;
  isLoading: boolean;
  status: string;
  copyStatus: string;
  logFilePath: string;
  content: string;
  onRefresh: () => void;
  onCopy: () => void;
};

export function LogViewerTab(props: LogViewerTabProps) {
  const outputRef = useRef<HTMLPreElement | null>(null);

  useEffect(() => {
    if (!props.autoScroll || !outputRef.current) {
      return;
    }
    outputRef.current.scrollTop = outputRef.current.scrollHeight;
  }, [props.autoScroll, props.content]);

  return (
    <div className="content-block">
      <h2>日志查看</h2>

      <div className="identity-card">
        <div className="row" style={{ marginBottom: 0 }}>
          <label htmlFor="log-source">日志来源</label>
          <select
            id="log-source"
            className="input"
            value={props.source}
            onChange={(event) => props.onSourceChange(event.target.value as LogSource)}
            disabled={props.isLoading}
          >
            <option value="local">本地日志</option>
            <option value="server">服务器日志</option>
          </select>
        </div>

        <div className="row" style={{ marginBottom: 0 }}>
          <label htmlFor="log-lines">显示行数</label>
          <input
            id="log-lines"
            className="input"
            type="number"
            min={1}
            max={2000}
            value={props.lines}
            onChange={(event) => props.onLinesChange(Number(event.target.value) || 200)}
            disabled={props.isLoading}
          />
        </div>

        <div className="row" style={{ marginBottom: 0 }}>
          <label htmlFor="log-since">最近分钟</label>
          <input
            id="log-since"
            className="input"
            type="number"
            min={0}
            max={2000}
            value={props.sinceMinutes}
            onChange={(event) => props.onSinceMinutesChange(Number(event.target.value) || 0)}
            disabled={props.isLoading}
          />
        </div>

        <div className="row" style={{ marginBottom: 0 }}>
          <label htmlFor="log-level">日志级别</label>
          <select
            id="log-level"
            className="input"
            value={props.minLevel}
            onChange={(event) => props.onMinLevelChange(event.target.value as LogLevel)}
            disabled={props.isLoading}
          >
            <option value="realtime">实时及以上</option>
            <option value="normal">普通及以上</option>
            <option value="warning">告警及以上</option>
            <option value="error">错误</option>
          </select>
        </div>
      </div>

      <div className="content-actions">
        <button className="btn" onClick={props.onRefresh} disabled={props.isLoading}>
          {props.isLoading ? "刷新中..." : "刷新日志"}
        </button>
        <button className="btn" onClick={props.onCopy} disabled={props.isLoading || !props.content.trim()}>
          复制日志
        </button>
        <label className="log-auto-scroll-toggle">
          <input
            type="checkbox"
            checked={props.autoScroll}
            onChange={(event) => props.onAutoScrollChange(event.target.checked)}
            disabled={props.isLoading}
          />
          自动滚动到底部
        </label>
      </div>

      <div className="status">{props.status}</div>
      <div className="status">{props.copyStatus || "复制状态：未执行"}</div>
      <div className="status">日志文件：{props.logFilePath || "未找到"}</div>
      <pre ref={outputRef} className="log-viewer-output">{props.content || "暂无日志内容"}</pre>
    </div>
  );
}
