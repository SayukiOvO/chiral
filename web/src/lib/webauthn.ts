import { getToken } from "../api";

/**
 * The browser side of WebAuthn.
 *
 * The protocol travels as JSON but the browser API speaks ArrayBuffer, so
 * every challenge and credential id has to be converted in both directions.
 * Getting that wrong is the classic way a passkey integration fails: the
 * ceremony runs, the authenticator lights up, and the server rejects the
 * response because it decoded a different challenge.
 */

function b64urlToBuffer(value: string): ArrayBuffer {
  const padded = value.replace(/-/g, "+").replace(/_/g, "/");
  const raw = atob(padded + "=".repeat((4 - (padded.length % 4)) % 4));
  const bytes = new Uint8Array(raw.length);
  for (let i = 0; i < raw.length; i++) bytes[i] = raw.charCodeAt(i);
  return bytes.buffer;
}

function bufferToB64url(buf: ArrayBuffer): string {
  const bytes = new Uint8Array(buf);
  let s = "";
  for (const b of bytes) s += String.fromCharCode(b);
  return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

/** Supported reports whether this browser can do WebAuthn at all. */
export function supported(): boolean {
  return typeof window !== "undefined" && !!window.PublicKeyCredential;
}

type CreationOptions = {
  publicKey: {
    challenge: string;
    user: { id: string; name: string; displayName: string };
    excludeCredentials?: { id: string; type: string; transports?: AuthenticatorTransport[] }[];
    [k: string]: unknown;
  };
};

type RequestOptions = {
  publicKey: {
    challenge: string;
    allowCredentials?: { id: string; type: string; transports?: AuthenticatorTransport[] }[];
    [k: string]: unknown;
  };
};

/** register runs a creation ceremony and returns the response to POST back. */
export async function register(options: CreationOptions): Promise<unknown> {
  const pk = options.publicKey;
  const credential = (await navigator.credentials.create({
    publicKey: {
      ...(pk as unknown as PublicKeyCredentialCreationOptions),
      challenge: b64urlToBuffer(pk.challenge),
      user: {
        ...pk.user,
        id: b64urlToBuffer(pk.user.id),
      },
      excludeCredentials: (pk.excludeCredentials ?? []).map((c) => ({
        ...c,
        id: b64urlToBuffer(c.id),
        type: "public-key" as const,
      })),
    },
  })) as PublicKeyCredential | null;
  if (!credential) throw new Error("no credential was created");

  const response = credential.response as AuthenticatorAttestationResponse;
  return {
    id: credential.id,
    rawId: bufferToB64url(credential.rawId),
    type: credential.type,
    response: {
      clientDataJSON: bufferToB64url(response.clientDataJSON),
      attestationObject: bufferToB64url(response.attestationObject),
    },
  };
}

/** authenticate runs an assertion ceremony. */
export async function authenticate(options: RequestOptions): Promise<unknown> {
  const pk = options.publicKey;
  const credential = (await navigator.credentials.get({
    publicKey: {
      ...(pk as unknown as PublicKeyCredentialRequestOptions),
      challenge: b64urlToBuffer(pk.challenge),
      allowCredentials: (pk.allowCredentials ?? []).map((c) => ({
        ...c,
        id: b64urlToBuffer(c.id),
        type: "public-key" as const,
      })),
    },
  })) as PublicKeyCredential | null;
  if (!credential) throw new Error("no assertion was produced");

  const response = credential.response as AuthenticatorAssertionResponse;
  return {
    id: credential.id,
    rawId: bufferToB64url(credential.rawId),
    type: credential.type,
    response: {
      clientDataJSON: bufferToB64url(response.clientDataJSON),
      authenticatorData: bufferToB64url(response.authenticatorData),
      signature: bufferToB64url(response.signature),
      userHandle: response.userHandle ? bufferToB64url(response.userHandle) : null,
    },
  };
}

/**
 * The two passkey flows post to endpoints that are not covered by the normal
 * API client: the login pair is unauthenticated (the challenge IS the
 * credential), and the registration pair needs the session token.
 */
async function post<T>(path: string, body: unknown, authed: boolean): Promise<T> {
  const res = await fetch(path, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      ...(authed ? { Authorization: `Bearer ${getToken()}` } : {}),
    },
    body: JSON.stringify(body),
  });
  if (!res.ok) {
    const detail = await res.json().catch(() => ({}));
    throw new Error((detail as { error?: string }).error ?? `HTTP ${res.status}`);
  }
  return res.status === 204 ? (undefined as T) : ((await res.json()) as T);
}

/** enrol adds a passkey to the signed-in account. */
export async function enrol(
  name: string,
): Promise<{ confirmed: boolean; recovery_codes?: string[] }> {
  const begin = await post<{ challenge: string; options: CreationOptions }>(
    "/api/mfa/passkey/begin",
    {},
    true,
  );
  const response = await register(begin.options);
  return post("/api/mfa/passkey/finish", { challenge: begin.challenge, name, response }, true);
}

/** completeLogin spends a login challenge with a passkey. */
export async function completeLogin(
  challenge: string,
): Promise<{ token: string; expires_at: number }> {
  const options = await post<RequestOptions>(
    "/api/login/passkey/begin",
    { challenge },
    false,
  );
  const response = await authenticate(options);
  return post("/api/login/passkey/finish", { challenge, response }, false);
}
