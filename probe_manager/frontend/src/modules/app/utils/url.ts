export function normalizeBaseUrl(input: string): string {
  return input.trim().replace(/\/+$/, "");
}

export function buildAdminStatusWSURL(inputBaseURL: string, token: string): string {
  const base = normalizeBaseUrl(inputBaseURL);
  if (!base) {
    return "";
  }

  let wsBase = base;
  if (base.startsWith("https://")) {
    wsBase = `wss://${base.slice("https://".length)}`;
  } else if (base.startsWith("http://")) {
    wsBase = `ws://${base.slice("http://".length)}`;
  } else if (!base.startsWith("ws://") && !base.startsWith("wss://")) {
    wsBase = `ws://${base}`;
  }

  return `${wsBase}/api/admin/ws/status?token=${encodeURIComponent(token)}`;
}
