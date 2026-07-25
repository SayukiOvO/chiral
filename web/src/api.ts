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

export type Scope = "global" | "profile" | "node";

export interface Component {
  name: string;
  /** Secret components come back masked; the panel never serves their value. */
  value: string;
  secret: boolean;
}

export interface Variable {
  id: string;
  name: string;
  scope: Scope;
  profile_id?: string;
  node_id?: string;
  generator?: string;
  components: Component[];
}

export interface GeneratorInfo {
  name: string;
  needs_xray: boolean;
  available: boolean;
}

export interface Profile {
  id: string;
  name: string;
  inbound_template: string;
  client_entry: string;
  client_templates?: Record<string, string>;
  /** Which clients have a template; present on list responses too. */
  client_kinds: string[];
  node_ids: string[];
  created_at: number;
  updated_at: number;
}

export interface ConfigPreview {
  config: unknown;
  inbound_tags: string[];
  tested: boolean;
  test_error: string;
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

  // --- variables ---
  listVariables: () => req<{ variables: Variable[] }>("GET", "/api/variables"),
  createVariable: (v: {
    name: string;
    scope: Scope;
    profile_id?: string;
    node_id?: string;
    generator?: string;
    value?: string;
  }) => req<Variable>("POST", "/api/variables", v),
  deleteVariable: (id: string) => req<void>("DELETE", `/api/variables/${id}`),
  listGenerators: () =>
    req<{ generators: GeneratorInfo[] }>("GET", "/api/generators"),

  // --- profiles ---
  listProfiles: () => req<{ profiles: Profile[] }>("GET", "/api/profiles"),
  getProfile: (id: string) => req<Profile>("GET", `/api/profiles/${id}`),
  createProfile: (name: string) =>
    req<Profile>("POST", "/api/profiles", { name }),
  updateProfile: (
    id: string,
    patch: {
      name?: string;
      inbound_template?: string;
      client_entry?: string;
    },
  ) => req<Profile>("PUT", `/api/profiles/${id}`, patch),
  deleteProfile: (id: string) => req<void>("DELETE", `/api/profiles/${id}`),
  putClientTemplate: (id: string, client: string, template: string) =>
    req<void>("PUT", `/api/profiles/${id}/clients/${client}`, { template }),
  deleteClientTemplate: (id: string, client: string) =>
    req<void>("DELETE", `/api/profiles/${id}/clients/${client}`),
  bindNode: (profileId: string, nodeId: string) =>
    req<void>("POST", `/api/profiles/${profileId}/nodes/${nodeId}`),
  unbindNode: (profileId: string, nodeId: string) =>
    req<void>("DELETE", `/api/profiles/${profileId}/nodes/${nodeId}`),
  applyProfile: (id: string) =>
    req<{ nodes: Record<string, string>; applied: number; failed: number }>(
      "POST",
      `/api/profiles/${id}/apply`,
    ),

  // --- node config assembly ---
  putSkeleton: (id: string, skeleton: unknown) =>
    req<void>("PUT", `/api/nodes/${id}/skeleton`, skeleton),
  previewConfig: (id: string) =>
    req<ConfigPreview>("GET", `/api/nodes/${id}/config/preview`),
  applyConfig: (id: string) =>
    req<{ version: number }>("POST", `/api/nodes/${id}/config/apply`),
};
