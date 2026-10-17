// The single API client. Adds the CSRF header to writes, turns error JSON into
// typed ApiErrors, and validates responses with Zod when a schema is given.
import type { z } from "zod";

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public fields: Record<string, string> = {},
  ) {
    super(message);
  }
}

function csrfToken(): string {
  if (typeof document === "undefined") return "";
  const m = document.cookie.match(/(?:^|;\s*)bm_csrf=([^;]+)/);
  return m ? decodeURIComponent(m[1]) : "";
}

type Options<T> = {
  method?: "GET" | "POST" | "PATCH" | "PUT" | "DELETE";
  body?: unknown;
  schema?: z.ZodType<T>;
  form?: FormData;
  signal?: AbortSignal;
};

export async function api<T = unknown>(path: string, opts: Options<T> = {}): Promise<T> {
  const method = opts.method ?? "GET";
  const headers: Record<string, string> = { Accept: "application/json" };
  let body: BodyInit | undefined;
  if (opts.form) {
    body = opts.form;
  } else if (opts.body !== undefined) {
    headers["Content-Type"] = "application/json";
    body = JSON.stringify(opts.body);
  }
  if (method !== "GET") headers["X-CSRF-Token"] = csrfToken();

  let res: Response;
  try {
    res = await fetch(`/api/v1${path}`, { method, headers, body, credentials: "same-origin", signal: opts.signal });
  } catch {
    throw new ApiError(0, "network", "Cannot reach the server. Check your connection.");
  }
  if (res.status === 204) return undefined as T;
  let json: unknown = null;
  try {
    json = await res.json();
  } catch {
    if (!res.ok) throw new ApiError(res.status, "bad_response", `The server answered HTTP ${res.status}.`);
  }
  if (!res.ok) {
    const e = (json as { error?: { code?: string; message?: string; fields?: Record<string, string> } })?.error;
    throw new ApiError(res.status, e?.code ?? "error", e?.message ?? `HTTP ${res.status}`, e?.fields ?? {});
  }
  if (opts.schema) {
    const parsed = opts.schema.safeParse(json);
    if (!parsed.success) {
      // A wrong shape is a bug: fail loudly in development, log in production.
      console.error(`Response of ${path} does not match its schema`, parsed.error.issues);
      if (process.env.NODE_ENV !== "production") {
        throw new ApiError(500, "schema_mismatch", `Unexpected response shape from ${path}.`);
      }
      return json as T;
    }
    return parsed.data;
  }
  return json as T;
}

export function isApiError(e: unknown): e is ApiError {
  return e instanceof ApiError;
}
