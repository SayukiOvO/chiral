// Minimal typed client for the Core REST API. The admin token is kept in
// localStorage for this M1 UI; proper login replaces it later.

export interface NodeMetrics {
  cpu_percent: number;
  mem_used_bytes: number;
  mem_total_bytes: number;
  disk_used_bytes: number;
  disk_total_bytes: number;
  net_tx_bps: number;
  net_rx_bps: number;
  config_version: number;
}

export interface Node {
  id: string;
  name: string;
  hostname: string;
  public_ip: string;
  agent_version: string;
  xray_version: string;
  created_at: number;
  registered_at?: number;
  last_seen_at?: number;
  online: boolean;
  xray_state?: string;
  metrics?: NodeMetrics;
}

export interface CreateNodeResult {
  node: Node;
  join_token: string;
  compose: string;
}

const TOKEN_KEY = "chiral_admin_token";

export function getToken(): string {
  return localStorage.getItem(TOKEN_KEY) ?? "";
}

export function setToken(token: string) {
  localStorage.setItem(TOKEN_KEY, token);
}

async function req<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: {
      Authorization: `Bearer ${getToken()}`,
      ...(body ? { "Content-Type": "application/json" } : {}),
    },
    body: body ? JSON.stringify(body) : undefined,
  });
  if (res.status === 401) throw new Error("unauthorized");
  if (!res.ok) {
    const detail = await res.json().catch(() => ({}));
    throw new Error((detail as { error?: string }).error ?? `HTTP ${res.status}`);
  }
  if (res.status === 204) return undefined as T;
  return res.json() as Promise<T>;
}

export const api = {
  listNodes: () => req<{ nodes: Node[] }>("GET", "/api/nodes"),
  createNode: (name: string) =>
    req<CreateNodeResult>("POST", "/api/nodes", { name }),
  deleteNode: (id: string) => req<void>("DELETE", `/api/nodes/${id}`),
  resetJoinToken: (id: string) =>
    req<{ join_token: string; compose: string }>(
      "POST",
      `/api/nodes/${id}/join-token`,
    ),
  restartXray: (id: string) =>
    req<void>("POST", `/api/nodes/${id}/restart-xray`),
};
