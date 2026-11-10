// Server-side sessions: the browser holds only an opaque, HttpOnly cookie, never a token. In memory, so a restart signs
// everyone out and a second replica would not know the first one's sessions (NOT-IMPLEMENTED.md: shared store).

import { randomBytes, timingSafeEqual } from "node:crypto";
import type { SupplierIdentity } from "./oidc.ts";

export const SESSION_COOKIE = "__Host-passage_supplier";
export const IDLE_TIMEOUT_MS = 30 * 60 * 1000;
export const ABSOLUTE_TIMEOUT_MS = 8 * 60 * 60 * 1000;
export const LOGIN_TTL_MS = 10 * 60 * 1000;
const MAX_SESSIONS = 10_000;

export interface Session {
  id: string;
  identity: SupplierIdentity;
  /** Double-submit token: the app reads it from /bff/me and sends it back on every non-GET request. */
  csrf: string;
  createdAt: number;
  lastSeenAt: number;
}

export interface PendingLogin {
  nonce: string;
  verifier: string;
  expiresAt: number;
}

export class SessionStore {
  private sessions = new Map<string, Session>();
  private logins = new Map<string, PendingLogin>();

  create(identity: SupplierIdentity, now: number): Session {
    if (this.sessions.size >= MAX_SESSIONS) this.sweep(now);
    if (this.sessions.size >= MAX_SESSIONS) throw new Error("too many sessions");
    const session: Session = { id: randomBytes(32).toString("base64url"), identity, csrf: randomBytes(32).toString("base64url"), createdAt: now, lastSeenAt: now };
    this.sessions.set(session.id, session);
    return session;
  }

  get(id: string | undefined, now: number): Session | undefined {
    const s = id === undefined ? undefined : this.sessions.get(id);
    if (!s) return undefined;
    if (now - s.lastSeenAt > IDLE_TIMEOUT_MS || now - s.createdAt > ABSOLUTE_TIMEOUT_MS) {
      this.sessions.delete(s.id);
      return undefined;
    }
    s.lastSeenAt = now;
    return s;
  }

  delete(id: string): void {
    this.sessions.delete(id);
  }

  putLogin(state: string, login: PendingLogin): void {
    if (this.logins.size >= MAX_SESSIONS) this.sweep(Date.now());
    if (this.logins.size >= MAX_SESSIONS) throw new Error("too many pending sign-ins");
    this.logins.set(state, login);
  }

  /** A state is used once: a replayed callback finds nothing. */
  takeLogin(state: string, now: number): PendingLogin | undefined {
    const l = this.logins.get(state);
    this.logins.delete(state);
    return l && l.expiresAt >= now ? l : undefined;
  }

  size(): number {
    return this.sessions.size;
  }

  private sweep(now: number): void {
    for (const [id, s] of this.sessions) if (now - s.lastSeenAt > IDLE_TIMEOUT_MS || now - s.createdAt > ABSOLUTE_TIMEOUT_MS) this.sessions.delete(id);
    for (const [state, l] of this.logins) if (l.expiresAt < now) this.logins.delete(state);
  }
}

export function parseCookies(header: string | undefined): Map<string, string> {
  const out = new Map<string, string>();
  for (const part of (header ?? "").split(";")) {
    const i = part.indexOf("=");
    if (i > 0) out.set(part.slice(0, i).trim(), part.slice(i + 1).trim());
  }
  return out;
}

export function sameToken(a: string | undefined, b: string): boolean {
  if (a === undefined) return false;
  const x = Buffer.from(a);
  const y = Buffer.from(b);
  return x.length === y.length && timingSafeEqual(x, y);
}

/** A fixed-window limiter per key (the session id), in memory per pod. */
export class RateLimiter {
  private windows = new Map<string, { start: number; count: number }>();
  private readonly limit: number;
  private readonly windowMs: number;
  constructor(limit: number, windowMs: number) {
    this.limit = limit;
    this.windowMs = windowMs;
  }

  allow(key: string, now: number): boolean {
    const w = this.windows.get(key);
    if (!w || now - w.start >= this.windowMs) {
      if (this.windows.size > MAX_SESSIONS) this.windows.clear();
      this.windows.set(key, { start: now, count: 1 });
      return true;
    }
    w.count += 1;
    return w.count <= this.limit;
  }
}
