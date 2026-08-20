import { describe, expect, it } from "vitest";
import { ApiError, makeRequest } from "./http";

/**
 * Emptiness is a property of the body, not of the status code.
 *
 * The panel's "restart kernel" endpoint answers 202 with nothing to report,
 * and reading that as JSON produced "Unexpected end of JSON input" for a
 * command that had in fact been delivered — the operator saw a failure and
 * the node restarted anyway, which is the worst of both.
 */
function withFetch(res: Response, fn: () => Promise<unknown>) {
  const original = globalThis.fetch;
  globalThis.fetch = async () => res;
  return fn().finally(() => {
    globalThis.fetch = original;
  });
}

const req = makeRequest(() => "token");

describe("makeRequest", () => {
  it("returns undefined for any 2xx with an empty body, not only 204", async () => {
    for (const status of [200, 202, 204]) {
      // 204 forbids a body outright, so it is constructed without one; the
      // point is that all three reach the caller as "nothing to report".
      const res = status === 204 ? new Response(null, { status }) : new Response("", { status });
      await withFetch(res, async () => {
        await expect(req("POST", "/api/x")).resolves.toBeUndefined();
      });
    }
  });

  it("still parses a body when there is one", async () => {
    const res = new Response(JSON.stringify({ ok: 1 }), { status: 200 });
    await withFetch(res, async () => {
      await expect(req("GET", "/api/x")).resolves.toEqual({ ok: 1 });
    });
  });

  it("reports the server's message on an error, with the status", async () => {
    const res = new Response(JSON.stringify({ error: "no such node" }), { status: 404 });
    await withFetch(res, async () => {
      await expect(req("GET", "/api/x")).rejects.toMatchObject({
        status: 404,
        message: "no such node",
      });
    });
  });

  it("falls back to the status when an error carries no JSON", async () => {
    const res = new Response("", { status: 502 });
    await withFetch(res, async () => {
      await expect(req("GET", "/api/x")).rejects.toBeInstanceOf(ApiError);
    });
  });
});
