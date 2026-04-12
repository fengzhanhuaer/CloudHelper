import React, { useState } from "react";
import type { StatusTone } from "../types";

type LoginPanelProps = {
  baseUrl: string;
  onBaseUrlChange: (value: string) => void;
  isAuthenticating: boolean;
  onLogin: (user: string, pass: string) => void;
  loginTone: StatusTone;
  loginStatus: string;
};

export function LoginPanel(props: LoginPanelProps) {
  const [username, setUsername] = React.useState("admin");
  const [password, setPassword] = React.useState("");

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
        <label htmlFor="username">Username</label>
        <input
          id="username"
          className="input"
          value={username}
          onChange={(e) => setUsername(e.target.value)}
          disabled={props.isAuthenticating}
        />
      </div>

      <div className="row">
        <label htmlFor="password">Password</label>
        <input
          id="password"
          className="input"
          type="password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          disabled={props.isAuthenticating}
        />
      </div>

      <div className="btn-row">
        <button 
          className="btn" 
          onClick={() => props.onLogin(username, password)} 
          disabled={props.isAuthenticating}
        >
          {props.isAuthenticating ? "Logging in..." : "Login"}
        </button>
      </div>

      <div className={`status auth-status ${props.loginTone}`}>{props.loginStatus}</div>
    </div>
  );
}
