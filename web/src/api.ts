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

export interface Credential {
  profile_id: string;
  node_id: string;
  email: string;
  up_bytes: number;
  down_bytes: number;
}

export interface User {
  id: string;
  name: string;
  quota_bytes: number;
  used_bytes: number;
  expires_at: number;
  renew_period: number;
  enabled: boolean;
  /** What the nodes were last told, as opposed to what should be true. */
  active: boolean;
  /** Computed: enabled, in date, and under quota. */
  allowed: boolean;
  profile_ids: string[];
  credentials?: Credential[];
  created_at: number;
}

export interface UserInput {
  name: string;
  quota_bytes: number;
  expires_at: number;
  renew_period: number;
  enabled?: boolean;
}

export interface NodeSample {
  at: number;
  cpu_percent: number;
  mem_used_bytes: number;
  mem_total_bytes: number;
  disk_used_bytes: number;
  disk_total_bytes: number;
  net_tx_bps: number;
  net_rx_bps: number;
}

export interface NodeSamplesResult {
  from: number;
  to: number;
  interval: number;
  samples: NodeSample[];
  retention_hours: number;
}

export interface TrafficPoint {
  at: number;
  up_bytes: number;
  down_bytes: number;
}

export interface TrafficResult {
  from: number;
  to: number;
  interval: number;
  points: TrafficPoint[];
}


// --- authentication ---

export interface Admin {
  id: string;
  username: string;
  role: "superadmin" | "operator" | "viewer";
  disabled: boolean;
  created_at: number;
  last_login: number;
}

export interface MfaMethod {
  kind: "totp" | "passkey" | "email";
  id: string;
  name: string;
}

/** A login either completes, or comes back needing a second factor. */
export type LoginResult =
  | { kind: "session"; token: string; expires_at: number; admin: Admin }
  | {
      kind: "mfa";
      challenge: string;
      expires_at: number;
      methods: MfaMethod[];
      has_recovery: boolean;
      passkey_ready: boolean;
    };

export interface MfaFactor {
  id: string;
  kind: "totp" | "passkey" | "email";
  name: string;
  confirmed: boolean;
  created_at: number;
  last_used_at: number;
}

export interface MfaStatus {
  factors: MfaFactor[];
  recovery_left: number;
  email: string;
  email_verified: boolean;
  passkey_ready: boolean;
  email_ready: boolean;
  passkey_rp_id: string;
}

export interface Whoami {
  id: string;
  name: string;
  role: string;
  via_token: boolean;
  can_write: boolean;
  can_admin: boolean;
}

const TOKEN_KEY = "chiral_admin_token";

export function getToken(): string {
  return localStorage.getItem(TOKEN_KEY) ?? "";
}

export function setToken(token: string) {
  localStorage.setItem(TOKEN_KEY, token);
}

/**
 * Carries the status alongside the message. A 401 means two different things
 * — an expired session on a console page, a rejected credential on the login
 * page — and only the caller knows which; the message stays the server's, so
 * "that code is not valid" reaches the person who typed it.
 */
export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
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
  if (!res.ok) {
    const detail = await res.json().catch(() => ({}));
    throw new ApiError(
      res.status,
      (detail as { error?: string }).error ?? `HTTP ${res.status}`,
    );
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


  // --- authentication ---
  login: async (username: string, password: string): Promise<LoginResult> => {
    const r = await req<any>("POST", "/api/login", { username, password });
    return r.mfa_required
      ? {
          kind: "mfa",
          challenge: r.challenge,
          expires_at: r.expires_at,
          methods: r.methods ?? [],
          has_recovery: !!r.has_recovery,
          passkey_ready: !!r.passkey_ready,
        }
      : { kind: "session", token: r.token, expires_at: r.expires_at, admin: r.admin };
  },
  verifyMfa: (challenge: string, method: string, code: string) =>
    req<{ token: string; expires_at: number; admin: Admin }>("POST", "/api/login/mfa", {
      challenge,
      method,
      code,
    }),
  sendLoginEmailCode: (challenge: string) =>
    req<{ sent_to: string }>("POST", "/api/login/email", { challenge }),
  logout: () => req<void>("POST", "/api/logout"),
  whoami: () => req<Whoami>("GET", "/api/whoami"),
  changePassword: (id: string, current: string, next: string) =>
    req<void>("POST", `/api/admins/${id}/password`, {
      current_password: current,
      new_password: next,
    }),

  // --- second factors ---
  mfaStatus: () => req<MfaStatus>("GET", "/api/mfa"),
  beginTotp: (name: string) =>
    req<{ id: string; secret: string; uri: string }>("POST", "/api/mfa/totp/begin", { name }),
  confirmTotp: (id: string, code: string) =>
    req<{ confirmed: boolean; recovery_codes?: string[] }>("POST", "/api/mfa/totp/confirm", {
      id,
      code,
    }),
  sendEmailVerification: (email: string) =>
    req<{ sent_to: string }>("POST", "/api/mfa/email/send", { email }),
  confirmEmail: (code: string) =>
    req<{ confirmed: boolean; recovery_codes?: string[] }>("POST", "/api/mfa/email/confirm", {
      code,
    }),
  regenerateRecoveryCodes: () => req<{ codes: string[] }>("POST", "/api/mfa/recovery"),
  deleteMfaFactor: (id: string) => req<void>("DELETE", `/api/mfa/${id}`),

  // --- users ---
  listUsers: () => req<{ users: User[] }>("GET", "/api/users"),
  getUser: (id: string) => req<User>("GET", `/api/users/${id}`),
  createUser: (u: UserInput) =>
    req<{ user: User; subscription_url: string }>("POST", "/api/users", u),
  updateUser: (id: string, u: UserInput) =>
    req<User>("PUT", `/api/users/${id}`, u),
  deleteUser: (id: string) => req<void>("DELETE", `/api/users/${id}`),
  resetSubToken: (id: string) =>
    req<{ subscription_url: string }>("POST", `/api/users/${id}/sub-token`),
  bindUserProfile: (userId: string, profileId: string) =>
    req<void>("POST", `/api/users/${userId}/profiles/${profileId}`),
  unbindUserProfile: (userId: string, profileId: string) =>
    req<void>("DELETE", `/api/users/${userId}/profiles/${profileId}`),

  // --- history ---
  nodeSamples: (id: string, windowSec: number) =>
    req<NodeSamplesResult>("GET", `/api/nodes/${id}/samples?window=${windowSec}`),
  trafficSeries: (opts: { userId?: string; nodeId?: string; windowSec: number }) => {
    const q = new URLSearchParams({ window: String(opts.windowSec) });
    if (opts.userId) q.set("user_id", opts.userId);
    if (opts.nodeId) q.set("node_id", opts.nodeId);
    return req<TrafficResult>("GET", `/api/traffic?${q}`);
  },

  // --- node config assembly ---
  putSkeleton: (id: string, skeleton: unknown) =>
    req<void>("PUT", `/api/nodes/${id}/skeleton`, skeleton),
  previewConfig: (id: string) =>
    req<ConfigPreview>("GET", `/api/nodes/${id}/config/preview`),
  applyConfig: (id: string) =>
    req<{ version: number }>("POST", `/api/nodes/${id}/config/apply`),
};
