// The one place the portal talks to its BFF. The session lives in an HttpOnly cookie the page cannot read; the page holds no token
// and only reads: there is no write route. The customer is never sent: the BFF knows it from the session.

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
let onSignedOut: () => void = () => window.location.assign("/bff/login");

export function configureApi(opts: { csrfToken?: string; legalEntityId?: string; onSignedOut?: () => void }): void {
  if (opts.csrfToken !== undefined) csrfToken = opts.csrfToken;
  if (opts.legalEntityId !== undefined) legalEntityId = opts.legalEntityId;
  if (opts.onSignedOut) onSignedOut = opts.onSignedOut;
}

function headers(method: string): Record<string, string> {
  const h: Record<string, string> = { accept: "application/json" };
  if (legalEntityId) h["x-legal-entity"] = legalEntityId;
  if (method !== "GET") h["x-csrf-token"] = csrfToken;
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

export async function get<T>(path: string): Promise<T> {
  const res = await fetch(path, { method: "GET", credentials: "same-origin", headers: headers("GET") });
  if (!res.ok) return fail(res);
  return (await res.json()) as T;
}

export async function signOut(): Promise<void> {
  await fetch("/bff/logout", { method: "POST", credentials: "same-origin", headers: headers("POST") }).catch(() => undefined);
  window.location.assign("/");
}
