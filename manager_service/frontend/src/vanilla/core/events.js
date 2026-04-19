export const APP_EVENTS = {
  AUTH_CHANGED: "auth:changed",
  TAB_CHANGED: "tab:changed",
  STATUS_MESSAGE: "status:message",
  ERROR_MESSAGE: "error:message",
  NETWORK_STATUS_REFRESH_REQUESTED: "network:status:refresh:requested",
  NETWORK_STATUS_REFRESHED: "network:status:refreshed",
  LOG_VIEWER_REFRESH_REQUESTED: "log-viewer:refresh:requested",
  LOG_VIEWER_REFRESHED: "log-viewer:refreshed",
};

export function createEventBus() {
  const listeners = new Map();

  function on(eventName, handler) {
    if (!listeners.has(eventName)) {
      listeners.set(eventName, new Set());
    }
    const bucket = listeners.get(eventName);
    bucket.add(handler);
    return () => off(eventName, handler);
  }

  function off(eventName, handler) {
    const bucket = listeners.get(eventName);
    if (!bucket) return;
    bucket.delete(handler);
    if (bucket.size === 0) listeners.delete(eventName);
  }

  function emit(eventName, payload) {
    const bucket = listeners.get(eventName);
    if (!bucket || bucket.size === 0) return;
    for (const handler of bucket) {
      try {
        handler(payload);
      } catch (error) {
        console.error("[event-bus] listener failed", eventName, error);
      }
    }
  }

  function clear() {
    listeners.clear();
  }

  return { on, off, emit, clear };
}
