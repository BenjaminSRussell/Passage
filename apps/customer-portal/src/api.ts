// The one place the portal talks to its BFF. The session lives in an HttpOnly cookie the page cannot read; the page holds no token.
// The customer is never in a body or a query: the BFF knows it from the session. The only writes are an order and a return, each
// with an Idempotency-Key. A contact who acts for a bill-to or ship-to account names it in X-Customer; the BFF signs it only when
// party lists it for the contact.

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
  legalEntityIds: string[];
  csrfToken: string;
}

let csrfToken = "";
let legalEntityId = "";
let accountId = "";
let onSignedOut: () => void = () => window.location.assign("/bff/login");

/** `accountId` is the account the contact acts for; empty means its own customer. */
export function configureApi(opts: { csrfToken?: string; legalEntityId?: string; accountId?: string; onSignedOut?: () => void }): void {
  if (opts.csrfToken !== undefined) csrfToken = opts.csrfToken;
  if (opts.legalEntityId !== undefined) legalEntityId = opts.legalEntityId;
  if (opts.accountId !== undefined) accountId = opts.accountId;
  if (opts.onSignedOut) onSignedOut = opts.onSignedOut;
}

function headers(method: string, homeOnly = false): Record<string, string> {
  const h: Record<string, string> = { accept: "application/json" };
  if (legalEntityId) h["x-legal-entity"] = legalEntityId;
  if (accountId && !homeOnly) h["x-customer"] = accountId;
  if (method !== "GET") h["x-csrf-token"] = csrfToken;
  return h;
}

/** A new key for one submit of a form: a retry of the same submit reuses it, so a double click never places two orders. */
export function newSubmitKey(): string {
  return `p-${crypto.randomUUID()}`;
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

export async function get<T>(path: string): Promise<T> {
  const res = await fetch(path, { method: "GET", credentials: "same-origin", headers: headers("GET", path.startsWith("/api/v1/accounts")) });
  if (!res.ok) return fail(res);
  return (await res.json()) as T;
}

/**
 * Downloads a PDF the BFF renders (an invoice or the statement): fetched with the same legal entity and account headers as every
 * read, then saved under the name the BFF gives. A plain link could not carry those headers.
 */
export async function download(path: string, fallbackName: string): Promise<void> {
  const res = await fetch(path, { method: "GET", credentials: "same-origin", headers: { ...headers("GET"), accept: "application/pdf" } });
  if (!res.ok) return fail(res);
  const name = /filename="([^"]+)"/.exec(res.headers.get("content-disposition") ?? "")?.[1] ?? fallbackName;
  const url = URL.createObjectURL(await res.blob());
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  document.body.append(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 60_000);
}

/** The two writes of the portal. `key` is one per submit (newSubmitKey). */
export async function post<T>(path: string, body: unknown, key: string): Promise<T> {
  const res = await fetch(path, {
    method: "POST",
    credentials: "same-origin",
    headers: { ...headers("POST"), "content-type": "application/json", "idempotency-key": key },
    body: JSON.stringify(body),
  });
  if (!res.ok) return fail(res);
  return (await res.json()) as T;
}

/** Ends the session here and sends the browser to Keycloak's end-session endpoint, so the single sign-on session ends too. */
export async function signOut(): Promise<void> {
  let target = "/";
  try {
    const res = await fetch("/bff/logout", { method: "POST", credentials: "same-origin", headers: headers("POST", true) });
    if (res.ok) target = ((await res.json()) as { logoutUrl?: string }).logoutUrl ?? "/";
  } catch {
    /* the session ends on the server by itself after the idle time; go home */
  }
  window.location.assign(target);
}
