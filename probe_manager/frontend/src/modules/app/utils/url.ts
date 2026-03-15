export function normalizeBaseUrl(input: string): string {
  return input.trim().replace(/\/+$/, "");
}

export function buildAdminStatusWSURL(inputBaseURL: string): string {
  const base = normalizeBaseUrl(inputBaseURL);
  if (!base) {
    return "";
  }

  let wsBase = "";
  if (base.startsWith("https://")) {
    wsBase = `wss://${base.slice("https://".length)}`;
  } else if (base.startsWith("wss://")) {
    wsBase = base;
  } else {
    return "";
  }

  return `${wsBase}/api/admin/ws`;
}
