// Sessions in a Valkey or Redis server shared by every pod of the portal. A session is one JSON value with a time to live of its
// absolute lifetime; the idle timeout is checked on read and the idle clock is written back when it is more than a minute stale (the
// KEEPTTL option leaves the expiry alone). A pending sign-in is one value that is read and deleted in one step (GETDEL), so a
// replayed callback finds nothing on any pod.

import type { RespClient, RespValue } from "./resp.ts";
import { ABSOLUTE_TIMEOUT_MS, expired, LOGIN_TTL_MS, newSession, StoreUnavailable, type PendingLogin, type Session, type SessionStore } from "./session.ts";
import type { SupplierIdentity } from "./oidc.ts";

const TOUCH_AFTER_MS = 60_000;

export class RedisSessionStore implements SessionStore {
  private readonly client: Pick<RespClient, "command">;
  private readonly prefix: string;

  constructor(client: Pick<RespClient, "command">, prefix = "supplier-portal:") {
    this.client = client;
    this.prefix = prefix;
  }

  private async run(...args: (string | number)[]): Promise<RespValue> {
    try {
      return await this.client.command(...args);
    } catch (err) {
      throw new StoreUnavailable(`the session store failed: ${err instanceof Error ? err.message : String(err)}`);
    }
  }

  private sessionKey(id: string): string {
    return `${this.prefix}session:${id}`;
  }

  private loginKey(state: string): string {
    return `${this.prefix}login:${state}`;
  }

  async create(identity: SupplierIdentity, idToken: string, now: number): Promise<Session> {
    const session = newSession(identity, idToken, now);
    await this.run("SET", this.sessionKey(session.id), JSON.stringify(session), "PX", ABSOLUTE_TIMEOUT_MS);
    // Indexes for back-channel logout: the sessions of a Keycloak session and of a user. Members outlive their session harmlessly.
    for (const key of this.indexKeys(identity.sessionId, identity.userId)) {
      await this.run("SADD", key, session.id);
      await this.run("PEXPIRE", key, ABSOLUTE_TIMEOUT_MS);
    }
    return session;
  }

  async get(id: string | undefined, now: number): Promise<Session | undefined> {
    // An id that is not what newSession issues never reaches the store, whatever a client sends in its cookie.
    if (id === undefined || !/^[A-Za-z0-9_-]{20,100}$/.test(id)) return undefined;
    const raw = await this.run("GET", this.sessionKey(id));
    if (typeof raw !== "string") return undefined;
    const session = parseSession(raw);
    if (!session || session.id !== id) {
      await this.run("DEL", this.sessionKey(id));
      return undefined;
    }
    if (expired(session, now)) {
      await this.run("DEL", this.sessionKey(id));
      return undefined;
    }
    if (now - session.lastSeenAt > TOUCH_AFTER_MS) {
      session.lastSeenAt = now;
      // XX: only while the session exists, so a touch that races a logout cannot bring the session back.
      await this.run("SET", this.sessionKey(id), JSON.stringify(session), "KEEPTTL", "XX");
    }
    return session;
  }

  async delete(id: string): Promise<void> {
    await this.run("DEL", this.sessionKey(id));
  }

  async deleteByKeycloak(sid: string, sub: string): Promise<number> {
    const key = sid !== "" ? this.indexKeys(sid, "")[0] : this.indexKeys("", sub)[0];
    if (!key) return 0;
    const members = await this.run("SMEMBERS", key);
    let ended = 0;
    for (const id of Array.isArray(members) ? members : []) {
      if (typeof id === "string" && /^[A-Za-z0-9_-]{20,100}$/.test(id)) ended += Number(await this.run("DEL", this.sessionKey(id)));
    }
    await this.run("DEL", key);
    return ended;
  }

  /** The index sets a session belongs to: by Keycloak session id and by user. */
  private indexKeys(sid: string, sub: string): string[] {
    const safe = (v: string) => /^[A-Za-z0-9_.:@-]{1,200}$/.test(v);
    return [...(safe(sid) ? [`${this.prefix}sid:${sid}`] : []), ...(safe(sub) ? [`${this.prefix}sub:${sub}`] : [])];
  }

  async putLogin(state: string, login: PendingLogin): Promise<void> {
    await this.run("SET", this.loginKey(state), JSON.stringify(login), "PX", LOGIN_TTL_MS);
  }

  async takeLogin(state: string, now: number): Promise<PendingLogin | undefined> {
    if (!/^[A-Za-z0-9_-]{20,100}$/.test(state)) return undefined;
    const raw = await this.run("GETDEL", this.loginKey(state));
    if (typeof raw !== "string") return undefined;
    try {
      const l = JSON.parse(raw) as PendingLogin;
      return typeof l.nonce === "string" && typeof l.verifier === "string" && typeof l.expiresAt === "number" && l.expiresAt >= now ? l : undefined;
    } catch {
      return undefined;
    }
  }
}

function parseSession(raw: string): Session | undefined {
  try {
    const s = JSON.parse(raw) as Partial<Session>;
    const i = s.identity;
    if (
      typeof s.id !== "string" || typeof s.csrf !== "string" || typeof s.idToken !== "string" || typeof s.createdAt !== "number" || typeof s.lastSeenAt !== "number" ||
      !i || typeof i.userId !== "string" || typeof i.tenantId !== "string" || typeof i.vendorId !== "string" || typeof i.isAdmin !== "boolean" || !Array.isArray(i.legalEntityIds)
    ) {
      return undefined;
    }
    return s as Session;
  } catch {
    return undefined;
  }
}

