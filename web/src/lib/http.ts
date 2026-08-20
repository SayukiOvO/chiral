/**
 * The bit of HTTP both interfaces share.
 *
 * Deliberately does NOT know where the token comes from. The console and the
 * portal keep theirs under different localStorage keys — an operator and a
 * customer using the same browser would otherwise overwrite each other's
 * session, and worse, a portal build linked against the console's client
 * would cheerfully send an admin token to admin endpoints.
 */

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

/** Builds a request function bound to one token source. */
export function makeRequest(getToken: () => string) {
  return async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
    const token = getToken();
    const res = await fetch(path, {
      method,
      headers: {
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
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
    // Emptiness is a property of the body, not of the status code. 204 is
    // merely the most common way to say it — 202 Accepted with nothing to
    // report is another, and reading THAT as JSON is how "restart the kernel"
    // came back as "Unexpected end of JSON input" for a command that had in
    // fact been delivered.
    const text = await res.text();
    if (text === "") return undefined as T;
    return JSON.parse(text) as T;
  };
}
