// Typed client for the operator console's half of the Core REST API.
//
// The portal has its own client under portal/, with its own storage key. They
// share only lib/http.ts: an operator and a customer in the same browser must
// not overwrite each other's session, and a portal build that imported this
// file would send console tokens to console endpoints.

import { makeRequest } from "./lib/http";

export { ApiError } from "./lib/http";

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
  /**
   * What subscribers see in the portal. Empty means unset, and the portal
   * numbers the line instead — it never falls back to `name`, which usually
   * encodes the provider and datacentre.
   */
  display_name: string;
  hostname: string;
  public_ip: string;
  /** Operator override; empty means the detected public_ip is in use. */
  address: string;
  /** What clients are actually told to dial. */
  dialable: string;
  agent_version: string;
  xray_version: string;
  xray_installed_version: string;
  platform: string;
  created_at: number;
  registered_at?: number;
  last_seen_at?: number;
  online: boolean;
  xray_state?: string;
  /** What a byte through this node costs the subscriber's quota. */
  traffic_rate: number;
  metrics?: NodeMetrics;
}

export interface CreateNodeResult {
  node: Node;
  join_token: string;
  /** The one line to run on the node. */
  install: string;
  /** For anyone already running containers. */
  compose: string;
}

export interface Credential {
  profile_id: string;
  node_id: string;
  /** The relayed exit this one leaves through; absent for the node's own. */
  exit_proxy_id?: string;
  exit_name?: string;
  /** What a byte on this credential costs the quota. */
  traffic_rate: number;
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
  /**
   * Expected concurrent source addresses, 0 for none. Nothing enforces it —
   * no Xray API can end an established session — so it is shown, not applied.
   */
  device_limit: number;
  /**
   * Current address count. Absent when recording is switched off, which is
   * why it is optional rather than 0: "not measuring" and "nobody connected"
   * must not look the same.
   */
  online_devices?: number;
  profile_ids: string[];
  /** Routing configuration their clash subscription uses; empty for none. */
  ruleset_id: string;
  credentials?: Credential[];
  created_at: number;
}

export interface UserInput {
  name: string;
  quota_bytes: number;
  expires_at: number;
  renew_period: number;
  device_limit?: number;
  enabled?: boolean;
}

/** One observed source address. Superadmin only; every read is audited. */
export interface UserDevice {
  ip: string;
  node_id: string;
  node_name: string;
  first_seen: number;
  last_seen: number;
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

/** One entry in the audit trail. */
export interface AuditEntry {
  id: number;
  at: number;
  actor_id: string;
  actor_name: string;
  action: string;
  target_type: string;
  target_id: string;
  target_name: string;
  detail: string;
}

/** A place node availability alerts get delivered to. */
export interface AlertTarget {
  id: string;
  kind: "telegram" | "webhook";
  name: string;
  /** The panel never returns the config itself, only enough to recognise it. */
  config_hint: string;
  enabled: boolean;
  created_at: number;
  last_error: string;
  last_sent_at: number;
}

const TOKEN_KEY = "chiral_admin_token";

export function getToken(): string {
  return localStorage.getItem(TOKEN_KEY) ?? "";
}

export function setToken(token: string) {
  localStorage.setItem(TOKEN_KEY, token);
}

const req = makeRequest(getToken);

export type Scope = "global" | "profile" | "node";

export interface Component {
  name: string;
  /** Secret components come back masked; the panel never serves their value. */
  value: string;
  secret: boolean;
}

export interface Ruleset {
  id: string;
  name: string;
  /** Built-in preset key, empty for a custom source. */
  preset: string;
  url: string;
  fetched_at: number;
  last_error: string;
  /** What the fetched .ini contains; 0 until it has been fetched. */
  groups: number;
  rules: number;
  lists: number;
}

export interface Preset {
  Key: string;
  Name: string;
  Features: string[] | null;
  Groups: number;
  Lists: number;
}

export interface NodeAccessEntry {
  id: string;
  name: string;
  /** "fleet", or the external source's name. */
  source: string;
  allowed: boolean;
  /** Whether anything reaches this node for this user at all. */
  entitled: boolean;
  /**
   * Set on an external node whose relay this subscriber will not get. The
   * proxy cannot be carried without it, so it leaves the subscription too —
   * this is what says so instead of leaving the toggle looking on.
   */
  chained_via?: string;
}

/**
 * One row of the single list that decides the order subscribers see — and,
 * because the group generator walks that list, the order inside every group.
 */
export interface OrderEntry {
  kind: "node" | "external" | "relay";
  id: string;
  name: string;
  /** "fleet", or the external source's name. */
  source: string;
  /** Only meaningful for a fleet node; absent for an external one. */
  online?: boolean;
}

/**
 * A line out through another of our nodes: subscribers connect to the entry,
 * their traffic leaves at the exit.
 *
 * There is no credential here on purpose. The line has one, but both ends are
 * ours and both receive it in an assembled config — nobody ever has to read
 * or type it, so showing it would be exposure without use.
 */
export interface Relay {
  id: string;
  entry_node_id: string;
  exit_node_id: string;
  profile_id: string;
  label: string;
  enabled: boolean;
  entry_name: string;
  exit_name: string;
  profile_name: string;
  traffic_rate: number;
  /** Why the line cannot currently be assembled; absent when it can. */
  problem?: string;
}

/**
 * A network some nodes reach that most subscribers must not — DN42 being the
 * motivating case. Stored as ALLOWS, unlike every other permission here:
 * default-nobody is the only safe resting state for a private network.
 */
export interface RestrictedDestination {
  id: string;
  name: string;
  cidrs: string[];
  domains: string[];
  /** Where the network exists; relay entries inherit enforcement automatically. */
  node_ids: string[];
  allowed_user_ids: string[];
}

/**
 * One "this traffic leaves that way" rule on a node. Order is priority:
 * routing is first-match.
 */
export interface EgressRule {
  id: string;
  node_id: string;
  label: string;
  domains: string[];
  ips: string[];
  target_kind: "direct" | "external" | "node";
  target_proxy_id?: string;
  target_node_id?: string;
  target_profile_id?: string;
  target_name: string;
  enabled: boolean;
  /** Why this rule cannot be assembled; absent when it can. */
  problem?: string;
}

/** One subscriber, seen from an external node's side. */
export interface ProxyUser {
  id: string;
  name: string;
  allowed: boolean;
  /**
   * Whether anything this subscriber holds reaches the thing being toggled at
   * all. Only relay lines answer it — an external node has no profile in
   * between to be entitled by, so it is absent there.
   */
  entitled?: boolean;
}

export interface Settings {
  /** What a subscription is called when it reaches a client. */
  subscription_name: string;
}

export interface ExternalProxy {
  id: string;
  /** What subscribers see: the operator's label if set, else the provider's. */
  name: string;
  /** Always the provider's, so the console can show what a rename overrides. */
  provider_name: string;
  type: string;
  server: string;
  port: number;
  /** Fleet node this one is dialled through; empty for a direct dial. */
  chain_node_id: string;
  /** Another external node this one dials through; at most one of the two. */
  chain_proxy_id: string;
  enabled: boolean;
  /** A node of this fleet carries its traffic; subscribers never see it. */
  relayed: boolean;
  /** Hand the provider's own address out alongside the relay. */
  relay_exposed: boolean;
  /** What a byte through this exit costs the quota. */
  traffic_rate: number;
}

export interface ExternalSub {
  id: string;
  name: string;
  url: string;
  enabled: boolean;
  fetched_at: number;
  last_error: string;
  proxies: ExternalProxy[];
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
  /** Stored kind → whether subscribers receive it; false = machinery only. */
  client_serve?: Record<string, boolean>;
  /** Which clients have a template; present on list responses too. */
  client_kinds: string[];
  node_ids: string[];
  created_at: number;
  updated_at: number;
}

/** One entry of a node's config history. Metadata only — no config body. */
export interface ConfigVersion {
  version: number;
  created_at: number;
  /** 0 pending, 1 applied, -1 the node rejected it (error holds the reason). */
  applied: number;
  error: string;
}

export interface ConfigPreview {
  config: unknown;
  inbound_tags: string[];
  tested: boolean;
  test_error: string;
  /** Which kernel judged it, and whether that is the one the node runs. */
  kernel_version: string;
  kernel_exact: boolean;
  kernel_note: string;
  /**
   * Configurations that validate and still will not work for somebody — a
   * REALITY inbound that refuses every clash client, for instance. Not errors:
   * the apply is allowed, the consequence is just made visible first.
   */
  advisories: string[] | null;
}

export interface XrayAvailable {
  version: string;
  tag: string;
  prerelease: boolean;
  /** Whether the panel can already validate configs for it. */
  panel_has: boolean;
  panel_versions: string[];
}

export interface XrayInstall {
  node_id: string;
  version: string;
  sha256: string;
  activate: boolean;
  /**
   * Mirrors chiral.v1.XrayInstallPhase by name. ACTIVE and INCONCLUSIVE are
   * both "it is running" but only ACTIVE means "and it answered" — the
   * distinction the promote decision rests on.
   */
  phase: string;
  message: string;
  started_at: number;
  updated_at: number;
}

export interface XrayUpgrade {
  id: string;
  version: string;
  /** canary | awaiting_promote | promoting | done | blocked */
  state: string;
  canary_node_id: string;
  message: string;
  started_at: number;
  updated_at: number;
}

export const api = {
  listNodes: () => req<{ nodes: Node[] }>("GET", "/api/nodes"),
  createNode: (name: string) =>
    req<CreateNodeResult>("POST", "/api/nodes", { name }),
  updateNode: (
    id: string,
    patch: { name?: string; display_name?: string; address?: string; traffic_rate?: number },
  ) =>
    req<Node>("PUT", `/api/nodes/${id}`, patch),
  deleteNode: (id: string) => req<void>("DELETE", `/api/nodes/${id}`),
  resetJoinToken: (id: string) =>
    req<{ join_token: string; install: string; compose: string }>(
      "POST",
      `/api/nodes/${id}/join-token`,
    ),
  restartXray: (id: string) =>
    req<void>("POST", `/api/nodes/${id}/restart-xray`),

  // --- variables ---
  userNodeAccess: (id: string) =>
    req<{ fleet: NodeAccessEntry[]; external: NodeAccessEntry[]; relay: NodeAccessEntry[] }>(
      "GET",
      `/api/users/${id}/nodes`,
    ),
  setUserNodeAccess: (
    id: string,
    denied: { denied_nodes: string[]; denied_proxies: string[]; denied_relays: string[] },
  ) => req<void>("PUT", `/api/users/${id}/nodes`, denied),

  // --- relay lines: one of our nodes leaving through another ---
  listRelays: () => req<{ relays: Relay[] }>("GET", "/api/relays"),
  createRelay: (r: {
    entry_node_id: string;
    exit_node_id: string;
    profile_id: string;
    label: string;
    traffic_rate?: number;
  }) => req<Relay>("POST", "/api/relays", r),
  updateRelay: (
    id: string,
    patch: { label?: string; enabled?: boolean; traffic_rate?: number },
  ) => req<Relay>("PUT", `/api/relays/${id}`, patch),
  deleteRelay: (id: string) => req<void>("DELETE", `/api/relays/${id}`),
  relayUsers: (id: string) => req<{ users: ProxyUser[] }>("GET", `/api/relays/${id}/users`),
  setRelayUsers: (id: string, denied: { denied_users: string[] }) =>
    req<void>("PUT", `/api/relays/${id}/users`, denied),

  // --- per-node egress: which traffic leaves by which route ---
  listEgress: (nodeID: string) =>
    req<{ rules: EgressRule[] }>("GET", `/api/nodes/${nodeID}/egress`),
  createEgress: (
    nodeID: string,
    r: {
      label: string;
      domains: string;
      ips: string;
      target_kind: string;
      target_proxy_id?: string;
      target_node_id?: string;
      target_profile_id?: string;
    },
  ) => req<EgressRule>("POST", `/api/nodes/${nodeID}/egress`, r),
  updateEgress: (
    id: string,
    patch: { label?: string; domains?: string; ips?: string; enabled?: boolean },
  ) => req<EgressRule>("PUT", `/api/egress/${id}`, patch),
  deleteEgress: (id: string) => req<void>("DELETE", `/api/egress/${id}`),
  reorderEgress: (nodeID: string, ids: string[]) =>
    req<void>("PUT", `/api/nodes/${nodeID}/egress/order`, { ids }),

  // --- restricted destinations: networks only some subscribers may enter ---
  listRestricted: () =>
    req<{ destinations: RestrictedDestination[] }>("GET", "/api/restricted"),
  createRestricted: (d: { name: string; cidrs: string; domains: string }) =>
    req<RestrictedDestination>("POST", "/api/restricted", d),
  updateRestricted: (id: string, d: { name: string; cidrs: string; domains: string }) =>
    req<RestrictedDestination>("PUT", `/api/restricted/${id}`, d),
  deleteRestricted: (id: string) => req<void>("DELETE", `/api/restricted/${id}`),
  setRestrictedNodes: (id: string, node_ids: string[]) =>
    req<void>("PUT", `/api/restricted/${id}/nodes`, { node_ids }),
  setRestrictedUsers: (id: string, allowed_user_ids: string[]) =>
    req<void>("PUT", `/api/restricted/${id}/users`, { allowed_user_ids }),
  // Reading the link and replacing it are different requests, because they are
  // very different acts: one shows an operator what a subscriber already has,
  // the other breaks every client that subscriber has configured.
  subToken: (id: string) =>
    req<{ subscription_url?: string; recoverable: boolean }>("GET", `/api/users/${id}/sub-token`),
  proxyOrder: () => req<{ entries: OrderEntry[] }>("GET", "/api/proxy-order"),
  setProxyOrder: (entries: { kind: string; id: string }[]) =>
    req<void>("PUT", "/api/proxy-order", { entries }),
  // The same relation as userNodeAccess, read from the node's end.
  externalProxyUsers: (subId: string, proxyId: string) =>
    req<{ users: ProxyUser[] }>("GET", `/api/externals/${subId}/proxies/${proxyId}/users`),
  setExternalProxyUsers: (subId: string, proxyId: string, denied: { denied_users: string[] }) =>
    req<void>("PUT", `/api/externals/${subId}/proxies/${proxyId}/users`, denied),

  getSettings: () => req<Settings>("GET", "/api/settings"),
  updateSettings: (patch: { subscription_name?: string }) =>
    req<Settings>("PUT", "/api/settings", patch),

  listExternals: () => req<{ externals: ExternalSub[] }>("GET", "/api/externals"),
  createExternal: (e: { name: string; url?: string; body?: string }) =>
    req<ExternalSub>("POST", "/api/externals", e),
  updateExternal: (id: string, patch: { name?: string; url?: string; enabled?: boolean }) =>
    req<ExternalSub>("PUT", `/api/externals/${id}`, patch),
  deleteExternal: (id: string) => req<void>("DELETE", `/api/externals/${id}`),
  refreshExternal: (id: string) => req<ExternalSub>("POST", `/api/externals/${id}/refresh`),
  setExternalProxy: (
    subId: string,
    proxyId: string,
    patch: {
      chain_node_id?: string;
      chain_proxy_id?: string;
      enabled?: boolean;
      name?: string;
      relay_exposed?: boolean;
      traffic_rate?: number;
    },
  ) => req<void>("PUT", `/api/externals/${subId}/proxies/${proxyId}`, patch),

  listRulesets: () => req<{ rulesets: Ruleset[] }>("GET", "/api/rulesets"),
  listPresets: () => req<{ presets: Preset[] }>("GET", "/api/rulesets/presets"),
  createRuleset: (r: { name?: string; preset?: string; url?: string }) =>
    req<Ruleset>("POST", "/api/rulesets", r),
  updateRuleset: (id: string, patch: { name?: string; url?: string }) =>
    req<Ruleset>("PUT", `/api/rulesets/${id}`, patch),
  deleteRuleset: (id: string) => req<void>("DELETE", `/api/rulesets/${id}`),
  refreshRuleset: (id: string) => req<Ruleset>("POST", `/api/rulesets/${id}/refresh`),
  setUserRuleset: (userID: string, rulesetID: string) =>
    req<void>("PUT", `/api/users/${userID}/ruleset`, { ruleset_id: rulesetID }),

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
  // Re-scopes a variable in place, value untouched — see store.MoveVariable.
  moveVariable: (id: string, to: { scope: Scope; profile_id?: string; node_id?: string }) =>
    req<Variable>("POST", `/api/variables/${id}/move`, to),
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
  // Whether subscribers receive this template. Separate from writing it: a
  // template can exist purely for machinery (relay dialling, upgrade probes).
  setClientTemplateServe: (id: string, client: string, serve: boolean) =>
    req<void>("PUT", `/api/profiles/${id}/clients/${client}/serve`, { serve }),
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

  // --- admins (superadmin only) ---
  listAdmins: () => req<{ admins: Admin[] }>("GET", "/api/admins"),
  createAdmin: (username: string, password: string, role: Admin["role"]) =>
    req<Admin>("POST", "/api/admins", { username, password, role }),
  updateAdmin: (id: string, patch: { role?: Admin["role"]; disabled?: boolean }) =>
    req<Admin>("PUT", `/api/admins/${id}`, patch),
  deleteAdmin: (id: string) => req<void>("DELETE", `/api/admins/${id}`),

  // --- audit ---
  auditLog: (params: { before?: number; action?: string; actor_id?: string } = {}) => {
    const q = new URLSearchParams();
    if (params.before) q.set("before", String(params.before));
    if (params.action) q.set("action", params.action);
    if (params.actor_id) q.set("actor_id", params.actor_id);
    const qs = q.toString();
    return req<{ entries: AuditEntry[] }>("GET", `/api/audit${qs ? "?" + qs : ""}`);
  },

  // --- alert targets ---
  // Runtime Xray-core upgrades.
  xrayAvailable: () => req<XrayAvailable>("GET", "/api/xray/available"),
  xrayInstalls: () => req<XrayInstall[]>("GET", "/api/xray/installs"),
  xrayUpgrade: () =>
    req<{ active: XrayUpgrade | null; history?: XrayUpgrade[] }>("GET", "/api/xray/upgrade"),
  startXrayCanary: (version: string, canaryNodeId: string) =>
    req<XrayUpgrade>("POST", "/api/xray/upgrade", {
      version,
      canary_node_id: canaryNodeId,
    }),
  promoteXray: () => req<XrayUpgrade>("POST", "/api/xray/upgrade/promote"),
  retryXray: () => req<XrayUpgrade>("POST", "/api/xray/upgrade/retry"),
  abandonXray: () => req<XrayUpgrade>("DELETE", "/api/xray/upgrade"),
  installXrayOn: (nodeId: string, version: string, activate: boolean) =>
    req<XrayInstall>("POST", `/api/nodes/${nodeId}/xray/install`, { version, activate }),

  listAlertTargets: () => req<{ targets: AlertTarget[] }>("GET", "/api/alerts"),
  createAlertTarget: (t: { kind: string; name: string; config: string }) =>
    req<AlertTarget>("POST", "/api/alerts", t),
  updateAlertTarget: (id: string, patch: { enabled?: boolean }) =>
    req<AlertTarget>("PUT", `/api/alerts/${id}`, patch),
  deleteAlertTarget: (id: string) => req<void>("DELETE", `/api/alerts/${id}`),
  testAlertTarget: (id: string) => req<void>("POST", `/api/alerts/${id}/test`),

  // --- users ---
  listUsers: () => req<{ users: User[] }>("GET", "/api/users"),
  getUser: (id: string) => req<User>("GET", `/api/users/${id}`),
  createUser: (u: UserInput) =>
    req<{ user: User; subscription_url: string }>("POST", "/api/users", u),
  updateUser: (id: string, u: UserInput) =>
    req<User>("PUT", `/api/users/${id}`, u),
  deleteUser: (id: string) => req<void>("DELETE", `/api/users/${id}`),
  /** Address COUNT. Available to any admin — it answers "why can't they connect". */
  userOnline: (id: string) =>
    req<{ count: number; partial: boolean; nodes_reporting: number; device_limit: number; recording: boolean }>(
      "GET",
      `/api/users/${id}/online`,
    ),
  /** The addresses themselves. Superadmin only, and audited on every read. */
  userDevices: (id: string) =>
    req<{ devices: UserDevice[]; recording: boolean }>("GET", `/api/users/${id}/devices`),
  /** Mints a one-time link the subscriber uses to set their own password. */
  portalLink: (id: string) =>
    req<{ claim_url: string; expires_at: number }>("POST", `/api/users/${id}/portal-link`),
  /** Portal sign-in, separate from whether their proxy credentials work. */
  setPortalAccess: (id: string, disabled: boolean) =>
    req<void>("PUT", `/api/users/${id}/portal-access`, { disabled }),
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
  // Without this the editor opened on the default text and saving replaced
  // whatever the operator had actually written.
  getSkeleton: (id: string) =>
    req<{ skeleton: unknown; custom: boolean }>("GET", `/api/nodes/${id}/skeleton`),
  putSkeleton: (id: string, skeleton: unknown) =>
    req<void>("PUT", `/api/nodes/${id}/skeleton`, skeleton),
  configVersions: (id: string) =>
    req<{ versions: ConfigVersion[]; depth: number }>(
      "GET",
      `/api/nodes/${id}/config/versions`,
    ),
  rollbackConfig: (id: string, version: number) =>
    req<{ version: number }>("POST", `/api/nodes/${id}/config/rollback`, { version }),
  previewConfig: (id: string) =>
    req<ConfigPreview>("GET", `/api/nodes/${id}/config/preview`),
  applyConfig: (id: string) =>
    req<{ version: number }>("POST", `/api/nodes/${id}/config/apply`),
};
