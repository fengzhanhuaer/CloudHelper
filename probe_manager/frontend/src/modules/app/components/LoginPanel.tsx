import type { StatusTone } from "../types";

type LoginPanelProps = {
  baseUrl: string;
  onBaseUrlChange: (value: string) => void;
  privateKeyStatus: string;
  privateKeyPath: string;
  isAuthenticating: boolean;
  onRefreshPrivateKey: () => void;
  onLogin: () => void;
  loginTone: StatusTone;
  loginStatus: string;
};

export function LoginPanel(props: LoginPanelProps) {
  return (
    <div className="panel login-panel">
      <div className="row">
        <label htmlFor="base-url">Controller URL</label>
        <input
          id="base-url"
          className="input"
          value={props.baseUrl}
          onChange={(e) => props.onBaseUrlChange(e.target.value)}
        />
      </div>

      <div className="row">
        <label>Local Private Key</label>
        <div className="status-inline">
          {props.privateKeyStatus || "未检查"}
          {props.privateKeyPath ? ` (${props.privateKeyPath})` : ""}
        </div>
      </div>

      <div className="btn-row">
        <button className="btn" onClick={props.onRefreshPrivateKey} disabled={props.isAuthenticating}>
          Refresh Key
        </button>
        <button className="btn" onClick={props.onLogin} disabled={props.isAuthenticating}>
          {props.isAuthenticating ? "登录验证中..." : "Login"}
        </button>
      </div>

      <div className={`status auth-status ${props.loginTone}`}>{props.loginStatus}</div>
    </div>
  );
}
