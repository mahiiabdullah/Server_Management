import { afterEach, describe, expect, it, vi } from "vitest";
import { z } from "zod";
import { api, ApiError } from "@/lib/api";

function mockFetch(status: number, body: unknown) {
  // eslint-disable-next-line @typescript-eslint/no-unused-vars
  const fn = vi.fn(async (_url: string, _init?: RequestInit) => new Response(body === undefined ? null : JSON.stringify(body), { status }));
  vi.stubGlobal("fetch", fn);
  return fn;
}

afterEach(() => {
  vi.unstubAllGlobals();
  document.cookie = "bm_csrf=; expires=Thu, 01 Jan 1970 00:00:00 GMT";
});

describe("api client", () => {
  it("sends the CSRF cookie as a header on writes only", async () => {
    document.cookie = "bm_csrf=tok123";
    const f = mockFetch(200, { ok: true });
    await api("/servers", { method: "POST", body: { a: 1 } });
    const init = f.mock.calls[0][1] as RequestInit;
    expect((init.headers as Record<string, string>)["X-CSRF-Token"]).toBe("tok123");
    expect(f.mock.calls[0][0]).toBe("/api/v1/servers");

    await api("/servers");
    const getInit = f.mock.calls[1][1] as RequestInit;
    expect((getInit.headers as Record<string, string>)["X-CSRF-Token"]).toBeUndefined();
  });

  it("turns the error JSON into a typed ApiError with field messages", async () => {
    mockFetch(400, { error: { code: "validation_failed", message: "Some fields are invalid.", fields: { name: "Required." } } });
    const err = (await api("/servers", { method: "POST", body: {} }).catch((e) => e)) as ApiError;
    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(400);
    expect(err.code).toBe("validation_failed");
    expect(err.fields.name).toBe("Required.");
  });

  it("returns undefined for 204", async () => {
    mockFetch(204, undefined);
    await expect(api("/auth/logout", { method: "POST" })).resolves.toBeUndefined();
  });

  it("reports network failures clearly", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => Promise.reject(new TypeError("Failed to fetch"))));
    const err = (await api("/me").catch((e) => e)) as ApiError;
    expect(err.code).toBe("network");
  });

  it("fails loudly in development when a response does not match its schema", async () => {
    mockFetch(200, { total: "not a number" });
    const spy = vi.spyOn(console, "error").mockImplementation(() => {});
    const err = (await api("/x", { schema: z.object({ total: z.number() }) }).catch((e) => e)) as ApiError;
    expect(err.code).toBe("schema_mismatch");
    spy.mockRestore();
  });
});
