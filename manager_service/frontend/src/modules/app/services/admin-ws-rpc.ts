/**
 * @removed [PKG-FIX-P0-03 / RQ-003]
 * Direct WebSocket connections to the controller bypass manager_service and violate the
 * single-entry-point architecture constraint (RQ-003, D-03).
 *
 * This module is DISABLED. All callers in controller-api.ts will surface an explicit
 * "not available in Web mode" error. Business features that relied on this path must be
 * migrated to manager_service backend proxy endpoints in a future work package (W4+).
 */

// eslint-disable-next-line @typescript-eslint/no-unused-vars
export async function callAdminWSRpc<T>(
  _baseUrl: string,
  _token: string,
  action: string,
  _payload?: unknown,
  _options?: { timeoutMs?: number },
): Promise<T> {
  throw new Error(
    `[RQ-003] Direct controller access is disabled in Web mode. ` +
    `Action "${action}" must be proxied through manager_service. ` +
    `See W4 migration plan for implementation schedule.`
  );
}
