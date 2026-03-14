import { ALL_TABS, OPERATOR_TABS, VIEWER_TABS } from "./constants";
import type { TabItem } from "./types";

export function normalizeClaim(v: string | undefined, fallback: string): string {
  const normalized = (v ?? "").trim().toLowerCase();
  return normalized || fallback;
}

export function normalizeUsernameClaim(v: string | undefined, fallback: string): string {
  const normalized = (v ?? "").trim();
  return normalized || fallback;
}

export function resolveTabs(userRole: string, certType: string): TabItem[] {
  const role = normalizeClaim(userRole, "viewer");
  const type = normalizeClaim(certType, role);

  if (role === "admin" || type === "admin") {
    return ALL_TABS;
  }
  if (role === "operator" || type === "operator" || type === "ops") {
    return OPERATOR_TABS;
  }
  return VIEWER_TABS;
}
