// The one place the portal talks to its BFF. The session lives in an HttpOnly cookie the page cannot read; the only secret the
// page holds is the CSRF token from /bff/me, sent on every write. The vendor is never sent: the BFF knows it from the session.

export class ApiError extends Error {
  status: number;
  code: string;
  fields: Record<string, string>;
  constructor(status: number, code: string, message: string, fields: Record<string, string> = {}) {
    super(message);
    this.status = status;
    this.code = code;
    this.fields = fields;
  }
}

export interface Me {
  userId: string;
  name: string;
  vendorId: string;
  legalEntityIds: string[];
  isAdmin: boolean;
  csrfToken: string;
}

let csrfToken = "";
let legalEntityId = "";
let onSignedOut: () => void = () => window.location.assign("/bff/login");

export function configureApi(opts: { csrfToken?: string; legalEntityId?: string; onSignedOut?: () => void }): void {
  if (opts.csrfToken !== undefined) csrfToken = opts.csrfToken;
  if (opts.legalEntityId !== undefined) legalEntityId = opts.legalEntityId;
  if (opts.onSignedOut) onSignedOut = opts.onSignedOut;
}

function headers(method: string, json: boolean): Record<string, string> {
  const h: Record<string, string> = { accept: "application/json" };
  if (legalEntityId) h["x-legal-entity"] = legalEntityId;
  if (method !== "GET") h["x-csrf-token"] = csrfToken;
  if (json) h["content-type"] = "application/json";
  return h;
}

async function fail(res: Response): Promise<never> {
  if (res.status === 401) onSignedOut();
  let code = `HTTP-${res.status}`;
  let message = res.statusText || "Request failed";
  let fields: Record<string, string> = {};
  try {
    const p = (await res.json()) as { code?: string; detail?: string; title?: string; errors?: Record<string, string> };
    code = p.code ?? code;
    message = p.detail ?? p.title ?? message;
    fields = p.errors ?? {};
  } catch {
    /* not JSON */
  }
  throw new ApiError(res.status, code, message, fields);
}

export async function request<T>(method: "GET" | "POST", path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    credentials: "same-origin",
    headers: headers(method, body !== undefined),
    ...(body !== undefined ? { body: JSON.stringify(body) } : {}),
  });
  if (!res.ok) return fail(res);
  return (res.status === 204 ? undefined : await res.json()) as T;
}

/** A call that needs no session (the registration page, accepting an invitation): no CSRF token, and a 401 is just an answer. */
export async function publicPost<T>(path: string, body: unknown): Promise<T> {
  const res = await fetch(path, { method: "POST", credentials: "same-origin", headers: { accept: "application/json", "content-type": "application/json" }, body: JSON.stringify(body) });
  if (!res.ok) {
    let code = `HTTP-${res.status}`;
    let message = res.statusText || "Request failed";
    let fields: Record<string, string> = {};
    try {
      const p = (await res.json()) as { code?: string; detail?: string; title?: string; errors?: Record<string, string> };
      code = p.code ?? code;
      message = p.detail ?? p.title ?? message;
      fields = p.errors ?? {};
    } catch {
      /* not JSON */
    }
    throw new ApiError(res.status, code, message, fields);
  }
  return (await res.json()) as T;
}

export const get = <T>(path: string): Promise<T> => request<T>("GET", path);
export const post = <T>(path: string, body?: unknown): Promise<T> => request<T>("POST", path, body ?? {});

/** Fetches a file and hands it to the browser as a download. */
export async function download(path: string, fallbackName: string): Promise<void> {
  const res = await fetch(path, { credentials: "same-origin", headers: headers("GET", false) });
  if (!res.ok) return fail(res);
  const name = /filename="([^"]+)"/.exec(res.headers.get("content-disposition") ?? "")?.[1] ?? fallbackName;
  const url = URL.createObjectURL(await res.blob());
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  URL.revokeObjectURL(url);
}

/**
 * Ends the session here and sends the browser to Keycloak's end-session endpoint, so the single sign-on session ends too. When the
 * portal could not end the session (its session store is down, or the request failed) the browser stays and the message to show is
 * returned; the server has cleared the cookie on this device all the same.
 */
export async function signOut(go: (url: string) => void = (url) => window.location.assign(url)): Promise<string | null> {
  try {
    const out = await request<{ logoutUrl?: string } | undefined>("POST", "/bff/logout", undefined);
    go(out && typeof out.logoutUrl === "string" ? out.logoutUrl : "/");
    return null;
  } catch (err) {
    const detail = err instanceof Error && err.message ? ` (${err.message})` : "";
    return `Sign-out did not finish${detail}. This device no longer holds your session; close the browser, or try again in a minute to sign out everywhere.`;
  }
}

/** A file as base64, for the upload endpoints. */
export async function toBase64(file: Blob): Promise<string> {
  const bytes = new Uint8Array(await file.arrayBuffer());
  let binary = "";
  for (let i = 0; i < bytes.length; i += 0x8000) binary += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(binary);
}
