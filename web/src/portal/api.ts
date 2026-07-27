import { ApiError, makeRequest } from "../lib/http";

/**
 * The subscriber's client. Deliberately not src/api.ts.
 *
 * Different storage key, so an operator and a customer can use the same
 * browser without evicting each other; and importing the console's client
 * here would pull the whole fleet-management surface into the portal bundle.
 */

const TOKEN_KEY = "chiral_portal_token";

export function getToken(): string {
  return localStorage.getItem(TOKEN_KEY) ?? "";
}

export function setToken(token: string) {
  if (token) localStorage.setItem(TOKEN_KEY, token);
  else localStorage.removeItem(TOKEN_KEY);
}

/** What the sign-in screen may offer, read before anyone has signed in. */
export interface PortalConfig {
  mode: "off" | "closed" | "open";
  registration_open: boolean;
  invite_required: boolean;
  email_ready: boolean;
}

/**
 * The account's single status word. `no_access` is the one people meet first:
 * a fresh registration with nothing granted yet.
 */
export type AccountStatus =
  | "active"
  | "suspended"
  | "expired"
  | "quota_exhausted"
  | "no_access";

export interface Account {
  name: string;
  quota_bytes: number;
  used_bytes: number;
  expires_at: number;
  renew_period: number;
  created_at: number;
  device_limit: number;
  status: AccountStatus;
}

export interface TrafficPoint {
  at: number;
  up_bytes: number;
  down_bytes: number;
}

export interface PortalNode {
  id: string;
  /** Empty when the operator has not set a customer-facing name. */
  name: string;
  /** 1-based position, used to label an unnamed line in the reader's language. */
  index: number;
  availability: "available" | "provisioning" | "unavailable";
  traffic_24h: TrafficPoint[];
}

export interface Subscription {
  /** False for accounts whose token predates recoverable storage. */
  available: boolean;
  url?: string;
  kinds: string[];
}

export interface PortalDevice {
  ip: string;
  last_seen: number;
}

export interface Me {
  account: Account;
  nodes: PortalNode[];
  subscription: Subscription;
  features: { devices: boolean };
  devices?: PortalDevice[];
}

const req = makeRequest(getToken);

/**
 * Wraps a call so an expired session lands on the sign-in screen instead of
 * an error message the reader cannot act on.
 *
 * Centralised here because the console learned this the hard way: only its
 * nodes page turns a 401 into a sign-out, and the other four render the text.
 * The portal's audience is not going to work out what "unauthorized" means.
 */
export function onUnauthorized(handler: () => void) {
  unauthorizedHandler = handler;
}
let unauthorizedHandler: () => void = () => {};

async function guarded<T>(p: Promise<T>): Promise<T> {
  try {
    return await p;
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) {
      setToken("");
      unauthorizedHandler();
    }
    throw e;
  }
}

export const portal = {
  config: () => req<PortalConfig>("GET", "/api/portal/config"),

  register: (email: string, password: string, invite_code?: string) =>
    req<{ registered: boolean; message: string }>("POST", "/api/portal/register", {
      email,
      password,
      invite_code,
    }),

  login: (email: string, password: string) =>
    req<{ token: string; expires_at: number }>("POST", "/api/portal/login", {
      email,
      password,
    }),

  claimLookup: (token: string) =>
    req<{ user_name: string }>("POST", "/api/portal/claim/lookup", { token }),

  claim: (token: string, password: string, email?: string) =>
    req<{ token: string; expires_at: number }>("POST", "/api/portal/claim", {
      token,
      password,
      email,
    }),

  me: () => guarded(req<Me>("GET", "/api/portal/me")),

  logout: () => req<void>("POST", "/api/portal/logout"),

  changePassword: (current_password: string, new_password: string) =>
    guarded(
      req<void>("POST", "/api/portal/password", { current_password, new_password }),
    ),
};

export { ApiError };
