// Server-side sessions: the browser holds only an opaque, HttpOnly cookie, never a token. The store is behind an interface so the
// pods of a replicated deployment can share it: MemorySessionStore is the development fallback (one pod, a restart signs everyone
// out), RedisSessionStore keeps sessions in a Valkey or Redis server (see redisStore.ts and resp.ts).

import { randomBytes, timingSafeEqual } from "node:crypto";
import type { CustomerIdentity } from "./oidc.ts";

export const SESSION_COOKIE = "__Host-passage_customer";
export const IDLE_TIMEOUT_MS = 30 * 60 * 1000;
export const ABSOLUTE_TIMEOUT_MS = 8 * 60 * 60 * 1000;
export const LOGIN_TTL_MS = 10 * 60 * 1000;
const MAX_SESSIONS = 10_000;

/** Thrown by a store that cannot reach its backing server: the portal answers 503, it does not sign the contact out. */
export class StoreUnavailable extends Error {}

export interface Session {
  id: string;
  identity: CustomerIdentity;
  /** The ID token of the sign-in, kept server side only: it is the id_token_hint of the end-session request at Keycloak. */
  idToken: string;
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

/** Where sessions and pending sign-ins live. Every method may fail when the store is unreachable; the server answers 503 then. */
export interface SessionStore {
  create(identity: CustomerIdentity, idToken: string, now: number): Promise<Session>;
  /** The live session of id (refreshing its idle clock), or undefined when unknown, idle too long or past its absolute lifetime. */
  get(id: string | undefined, now: number): Promise<Session | undefined>;
  delete(id: string): Promise<void>;
  /** Ends every session of a Keycloak session id and/or user (back-channel logout); answers how many were ended. */
  deleteByKeycloak(sid: string, sub: string): Promise<number>;
  putLogin(state: string, login: PendingLogin): Promise<void>;
  /** A state is used once: a replayed callback finds nothing. */
  takeLogin(state: string, now: number): Promise<PendingLogin | undefined>;
}

export function newSession(identity: CustomerIdentity, idToken: string, now: number): Session {
  return { id: randomBytes(32).toString("base64url"), identity, idToken, csrf: randomBytes(32).toString("base64url"), createdAt: now, lastSeenAt: now };
}

export function expired(s: Session, now: number): boolean {
  return now - s.lastSeenAt > IDLE_TIMEOUT_MS || now - s.createdAt > ABSOLUTE_TIMEOUT_MS;
}

export class MemorySessionStore implements SessionStore {
  private sessions = new Map<string, Session>();
  private logins = new Map<string, PendingLogin>();

  async create(identity: CustomerIdentity, idToken: string, now: number): Promise<Session> {
    if (this.sessions.size >= MAX_SESSIONS) this.sweep(now);
    if (this.sessions.size >= MAX_SESSIONS) throw new StoreUnavailable("too many sessions");
    const session = newSession(identity, idToken, now);
    this.sessions.set(session.id, session);
    return session;
  }

  async get(id: string | undefined, now: number): Promise<Session | undefined> {
    const s = id === undefined ? undefined : this.sessions.get(id);
    if (!s) return undefined;
    if (expired(s, now)) {
      this.sessions.delete(s.id);
      return undefined;
    }
    s.lastSeenAt = now;
    return s;
  }

  async delete(id: string): Promise<void> {
    this.sessions.delete(id);
  }

  async deleteByKeycloak(sid: string, sub: string): Promise<number> {
    let n = 0;
    for (const [id, s] of this.sessions) {
      if ((sid !== "" && s.identity.sessionId === sid) || (sid === "" && sub !== "" && s.identity.userId === sub)) {
        this.sessions.delete(id);
        n++;
      }
    }
    return n;
  }

  async putLogin(state: string, login: PendingLogin): Promise<void> {
    if (this.logins.size >= MAX_SESSIONS) this.sweep(Date.now());
    if (this.logins.size >= MAX_SESSIONS) throw new StoreUnavailable("too many pending sign-ins");
    this.logins.set(state, login);
  }

  async takeLogin(state: string, now: number): Promise<PendingLogin | undefined> {
    const l = this.logins.get(state);
    this.logins.delete(state);
    return l && l.expiresAt >= now ? l : undefined;
  }

  size(): number {
    return this.sessions.size;
  }

  private sweep(now: number): void {
    for (const [id, s] of this.sessions) if (expired(s, now)) this.sessions.delete(id);
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

/** A fixed-window limiter per key (the session id), in memory per pod: with several pods the limit applies to each. */
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
