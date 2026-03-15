import { normalizeBaseUrl } from "../utils/url";

type WsRpcRequest = {
  id: string;
  action: string;
  payload?: unknown;
};

type WsRpcResponse<T> = {
  id?: string;
  ok?: boolean;
  data?: T;
  error?: string;
  type?: string;
};

function buildAdminWSRpcURL(baseUrl: string): string {
  const base = normalizeBaseUrl(baseUrl);
  if (!base) {
    throw new Error("Controller URL is required");
  }

  if (base.startsWith("https://")) {
    return `wss://${base.slice("https://".length)}/api/admin/ws`;
  }
  if (base.startsWith("wss://")) {
    return `${base}/api/admin/ws`;
  }
  throw new Error("Only wss is allowed for manager-server communication");
}

export async function callAdminWSRpc<T>(baseUrl: string, token: string, action: string, payload?: unknown): Promise<T> {
  const trimmedToken = token.trim();
  if (!trimmedToken) {
    throw new Error("session token is required");
  }

  const url = buildAdminWSRpcURL(baseUrl);
  const authId = `${Date.now()}-auth-${Math.random().toString(16).slice(2)}`;
  const id = `${Date.now()}-${Math.random().toString(16).slice(2)}`;

  return await new Promise<T>((resolve, reject) => {
    let done = false;
    const ws = new WebSocket(url);
    const timer = window.setTimeout(() => {
      if (done) {
        return;
      }
      done = true;
      try {
        ws.close();
      } catch {
        // ignore
      }
      reject(new Error(`ws rpc timeout: ${action}`));
    }, 15000);

    const finalize = (fn: () => void) => {
      if (done) {
        return;
      }
      done = true;
      window.clearTimeout(timer);
      fn();
      try {
        ws.close();
      } catch {
        // ignore
      }
    };

    ws.onerror = () => {
      finalize(() => reject(new Error(`ws rpc failed: ${action}`)));
    };

    ws.onopen = () => {
      const authReq: WsRpcRequest = { id: authId, action: "auth.session", payload: { token: trimmedToken } };
      ws.send(JSON.stringify(authReq));
    };

    ws.onmessage = (event) => {
      let msg: WsRpcResponse<T>;
      try {
        msg = JSON.parse(String(event.data)) as WsRpcResponse<T>;
      } catch {
        return;
      }

      if (msg.type) {
        return;
      }
      if (msg.id === authId) {
        if (!msg.ok) {
          finalize(() => reject(new Error(msg.error || "ws auth failed")));
          return;
        }
        const req: WsRpcRequest = { id, action, payload };
        ws.send(JSON.stringify(req));
        return;
      }
      if (msg.id !== id) {
        return;
      }

      if (!msg.ok) {
        finalize(() => reject(new Error(msg.error || `ws rpc error: ${action}`)));
        return;
      }
      finalize(() => resolve((msg.data ?? ({} as T)) as T));
    };
  });
}
