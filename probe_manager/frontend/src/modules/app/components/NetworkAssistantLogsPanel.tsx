import { useEffect, useRef } from "react";
import type { NetworkAssistantLogFilterSource } from "../types";

const categoryLabels: Record<string, string> = {
  general: "通用",
  init: "初始化",
  mode: "模式",
  proxy: "系统代理",
  socks: "本地代理",
  mux: "隧道复用",
  tunnel: "隧道",
  node: "节点",
  rule: "规则",
  whitelist: "白名单",
  error: "错误",
  open: "打开流",
  read: "读取",
  write: "写入",
  state: "状态",
};

type NetworkAssistantLogsPanelProps = {
  logLines: number;
  onLogLinesChange: (value: number) => void;
  isLoadingLogs: boolean;
  logStatus: string;
  logCopyStatus: string;
  logContent: string;
  logSourceFilter: NetworkAssistantLogFilterSource;
  onLogSourceFilterChange: (value: NetworkAssistantLogFilterSource) => void;
  logCategoryFilter: string;
  onLogCategoryFilterChange: (value: string) => void;
  logCategories: string[];
  logVisibleCount: number;
  logTotalCount: number;
  logAutoScroll: boolean;
  onLogAutoScrollChange: (value: boolean) => void;
  onRefreshLogs: () => void;
  onCopyLogs: () => void;
  active: boolean;
};

export function NetworkAssistantLogsPanel(props: NetworkAssistantLogsPanelProps) {
  const outputRef = useRef<HTMLPreElement | null>(null);

  useEffect(() => {
    if (!props.active) {
      return;
    }
    props.onRefreshLogs();
    const timer = window.setInterval(() => {
      props.onRefreshLogs();
    }, 2000);
    return () => window.clearInterval(timer);
  }, [props.active, props.onRefreshLogs]);

  useEffect(() => {
    if (!props.active || !props.logAutoScroll || !outputRef.current) {
      return;
    }
    outputRef.current.scrollTop = outputRef.current.scrollHeight;
  }, [props.active, props.logAutoScroll, props.logContent]);

  return (
    <>
      <div className="identity-card">
        <div className="row" style={{ marginBottom: 0 }}>
          <label htmlFor="network-log-lines">显示行数</label>
          <input
            id="network-log-lines"
            className="input"
            type="number"
            min={1}
            max={2000}
            value={props.logLines}
            onChange={(event) => props.onLogLinesChange(Number(event.target.value) || 200)}
            disabled={props.isLoadingLogs}
          />
        </div>
        <div className="row" style={{ marginBottom: 0 }}>
          <label htmlFor="network-log-source">来源</label>
          <select
            id="network-log-source"
            className="input"
            value={props.logSourceFilter}
            onChange={(event) => props.onLogSourceFilterChange(event.target.value as NetworkAssistantLogFilterSource)}
            disabled={props.isLoadingLogs}
          >
            <option value="all">全部</option>
            <option value="manager">管理端</option>
            <option value="controller">主控端</option>
          </select>
        </div>
        <div className="row" style={{ marginBottom: 0 }}>
          <label htmlFor="network-log-category">分类</label>
          <select
            id="network-log-category"
            className="input"
            value={props.logCategoryFilter}
            onChange={(event) => props.onLogCategoryFilterChange(event.target.value)}
            disabled={props.isLoadingLogs}
          >
            <option value="all">全部</option>
            {props.logCategories.map((item) => (
              <option key={item} value={item}>{categoryLabels[item] || item}</option>
            ))}
          </select>
        </div>
      </div>

      <div className="content-actions">
        <button className="btn" onClick={props.onRefreshLogs} disabled={props.isLoadingLogs}>
          {props.isLoadingLogs ? "刷新中..." : "刷新日志"}
        </button>
        <button className="btn" onClick={props.onCopyLogs} disabled={props.isLoadingLogs || !props.logContent.trim()}>
          复制日志
        </button>
        <label className="log-auto-scroll-toggle">
          <input
            type="checkbox"
            checked={props.logAutoScroll}
            onChange={(event) => props.onLogAutoScrollChange(event.target.checked)}
            disabled={props.isLoadingLogs}
          />
          自动滚动到底部
        </label>
      </div>

      <div className="status">{props.logStatus}</div>
      <div className="status">日志筛选：{props.logVisibleCount}/{props.logTotalCount}</div>
      <div className="status">{props.logCopyStatus || "复制状态：未执行"}</div>
      <pre ref={outputRef} className="log-viewer-output">{props.logContent || "暂无网络助手日志"}</pre>
    </>
  );
}
