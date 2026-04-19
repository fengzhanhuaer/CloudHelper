import { ALL_TABS, OPERATOR_TABS, VIEWER_TABS } from "./config/tabs";

function normalizeClaim(value, fallback) {
  const normalized = String(value || "").trim().toLowerCase();
  return normalized || fallback;
}

function normalizeUsernameClaim(value, fallback) {
  const normalized = String(value || "").trim();
  return normalized || fallback;
}

function resolveTabs(userRole, certType) {
  const role = normalizeClaim(userRole, "viewer");
  const type = normalizeClaim(certType, role);

  if (role === "admin" || type === "admin") return ALL_TABS;
  if (role === "operator" || type === "operator" || type === "ops") return OPERATOR_TABS;
  return VIEWER_TABS;
}

export { normalizeClaim, normalizeUsernameClaim, resolveTabs };
