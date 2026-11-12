import { afterEach, describe, expect, it } from "vitest";
import { RedisSessionStore } from "../src/redisStore.ts";
import { parseStoreUrl, RespClient, RespError } from "../src/resp.ts";
import { ABSOLUTE_TIMEOUT_MS, IDLE_TIMEOUT_MS, LOGIN_TTL_MS, MemorySessionStore, StoreUnavailable, type SessionStore } from "../src/session.ts";
import { sessionStoreFromEnv } from "../src/main.ts";
import { api, CUSTOMER_A, LE_1, signIn, startRig, TENANT, type Rig } from "./helpers.ts";
import { startFakeRedis, type FakeRedis } from "./fakeRedis.ts";

const identity = { userId: "user-1", tenantId: TENANT, customerId: CUSTOMER_A, legalEntityIds: [LE_1], name: "Pat Buyer", sessionId: "kc-sid-1" };
const T0 = 1_000_000;

let redis: FakeRedis | undefined;
let clients: RespClient[] = [];
let rig: Rig | undefined;
afterEach(async () => {
  for (const c of clients) c.close();
  clients = [];
  await rig?.close();
  rig = undefined;
  await redis?.close();
  redis = undefined;
});

function client(r: FakeRedis, extra: { password?: string } = {}): RespClient {
  const c = new RespClient({ host: "127.0.0.1", port: r.port, tls: false, commandTimeoutMs: 300, ...extra });
  clients.push(c);
  return c;
}

// The same behaviour is asked of the development store and the shared one.
const stores: Array<[string, () => Promise<{ store: SessionStore; second: SessionStore }>]> = [
  ["memory", async () => {
    const s = new MemorySessionStore();
    return { store: s, second: s };
  }],
  ["redis", async () => {
    redis = await startFakeRedis();
    return { store: new RedisSessionStore(client(redis)), second: new RedisSessionStore(client(redis)) };
  }],
];

describe.each(stores)("%s session store", (_name, make) => {
  it("creates a session that another pod finds, and deletes it for everyone", async () => {
    const { store, second } = await make();
    const s = await store.create(identity, "id-token", T0);
    expect(s.idToken).toBe("id-token");
    expect(await second.get(s.id, T0 + 1000)).toMatchObject({ id: s.id, csrf: s.csrf, idToken: "id-token", identity });
    await second.delete(s.id);
    expect(await store.get(s.id, T0 + 2000)).toBeUndefined();
  });

  it("knows no session it did not issue", async () => {
    const { store } = await make();
    expect(await store.get(undefined, T0)).toBeUndefined();
    expect(await store.get("forged", T0)).toBeUndefined();
    expect(await store.get("a".repeat(43), T0)).toBeUndefined();
  });

  it("ends a session after the idle timeout, which each use restarts, and after the absolute lifetime", async () => {
    const { store } = await make();
    const s = await store.create(identity, "t", T0);
    for (let i = 1; i <= 3; i++) expect(await store.get(s.id, T0 + i * (IDLE_TIMEOUT_MS - 60_000))).toBeDefined();
    expect(await store.get(s.id, T0 + 3 * (IDLE_TIMEOUT_MS - 60_000) + IDLE_TIMEOUT_MS + 1000)).toBeUndefined();

    const long = await store.create(identity, "t", T0);
    let now = T0;
    while (now - T0 < ABSOLUTE_TIMEOUT_MS) {
      now += 20 * 60 * 1000;
      if (now - T0 <= ABSOLUTE_TIMEOUT_MS) expect(await store.get(long.id, now)).toBeDefined();
    }
    expect(await store.get(long.id, T0 + ABSOLUTE_TIMEOUT_MS + 1000)).toBeUndefined();
  });

  it("uses a pending sign-in once, on whichever pod takes it, and not after it expired", async () => {
    const { store, second } = await make();
    const state = "s".repeat(43);
    await store.putLogin(state, { nonce: "n", verifier: "v", expiresAt: T0 + LOGIN_TTL_MS });
    expect(await second.takeLogin(state, T0 + 1000)).toEqual({ nonce: "n", verifier: "v", expiresAt: T0 + LOGIN_TTL_MS });
    expect(await store.takeLogin(state, T0 + 2000)).toBeUndefined();
    const late = "l".repeat(43);
    await store.putLogin(late, { nonce: "n", verifier: "v", expiresAt: T0 + LOGIN_TTL_MS });
    expect(await store.takeLogin(late, T0 + LOGIN_TTL_MS + 1)).toBeUndefined();
    expect(await store.takeLogin("unknown-state-unknown-state", T0)).toBeUndefined();
  });
});

describe("the Redis store on the wire", () => {
  it("writes one JSON value per session with the absolute lifetime as its expiry, and touches it without changing the expiry", async () => {
    redis = await startFakeRedis();
    const store = new RedisSessionStore(client(redis), "p:");
    const s = await store.create(identity, "tok", T0);
    expect(redis.commands[0]!.slice(0, 2)).toEqual(["SET", `p:session:${s.id}`]);
    expect(redis.commands[0]!.slice(3)).toEqual(["PX", String(ABSOLUTE_TIMEOUT_MS)]);
    await store.get(s.id, T0 + 10_000);
    expect(redis.commands.filter((c) => c[0] === "SET")).toHaveLength(1); // fresh enough: no write
    await store.get(s.id, T0 + 5 * 60_000);
    const touch = redis.commands.filter((c) => c[0] === "SET").at(-1)!;
    expect(touch.slice(-2)).toEqual(["KEEPTTL", "XX"]);
    expect(JSON.parse(redis.dump().get(`p:session:${s.id}`) as string).lastSeenAt).toBe(T0 + 5 * 60_000);
  });

  it("takes a sign-in with GETDEL and sends nothing for a state that cannot be one", async () => {
    redis = await startFakeRedis();
    const store = new RedisSessionStore(client(redis));
    await store.takeLogin("short", T0);
    await store.get("short", T0);
    expect(redis.commands).toHaveLength(0);
    await store.takeLogin("x".repeat(43), T0);
    expect(redis.commands[0]![0]).toBe("GETDEL");
  });

  it("drops a stored value that is not a session, and answers nothing for it", async () => {
    redis = await startFakeRedis();
    const c = client(redis);
    const store = new RedisSessionStore(c);
    const id = "z".repeat(43);
    await c.command("SET", `customer-portal:session:${id}`, "not json");
    expect(await store.get(id, T0)).toBeUndefined();
    expect(redis.dump().has(`customer-portal:session:${id}`)).toBe(false);
    await c.command("SET", `customer-portal:session:${id}`, JSON.stringify({ id: "another-id", identity, csrf: "c", idToken: "t", createdAt: T0, lastSeenAt: T0 }));
    expect(await store.get(id, T0)).toBeUndefined();
  });

  it("reports an unreachable server as StoreUnavailable, and heals when it is back", async () => {
    redis = await startFakeRedis();
    const store = new RedisSessionStore(client(redis));
    const s = await store.create(identity, "t", T0);
    redis.cut();
    await expect(store.get(s.id, T0 + 1)).rejects.toBeInstanceOf(StoreUnavailable);
    // The value is still in the server: the next command reconnects and finds it.
    expect(await store.get(s.id, T0 + 2)).toBeDefined();
    redis.mute = true;
    await expect(store.get(s.id, T0 + 3)).rejects.toThrow(StoreUnavailable);
    redis.mute = false;
    expect(await store.get(s.id, T0 + 4)).toBeDefined();
  });
});

describe("session store: touch and back-channel deletion", () => {
  it("a touch after the session was deleted does not bring it back", async () => {
    redis = await startFakeRedis();
    const c = client(redis);
    const store = new RedisSessionStore(c);
    const s = await store.create(identity, "t", T0);
    // The touch is a SET ... KEEPTTL XX: when the key is gone (a logout raced it) nothing is written.
    await c.command("DEL", `customer-portal:session:${s.id}`);
    const answer = await c.command("SET", `customer-portal:session:${s.id}`, "x", "KEEPTTL", "XX");
    expect(answer).toBeNull();
    expect(redis.dump().has(`customer-portal:session:${s.id}`)).toBe(false);
    const again = await store.create(identity, "t", T0);
    await store.get(again.id, T0 + 5 * 60_000);
    expect(redis.commands.filter((x) => x[0] === "SET").at(-1)).toContain("XX");
  });

  it("ends the sessions of a Keycloak session or of a user, on any pod", async () => {
    redis = await startFakeRedis();
    const a = new RedisSessionStore(client(redis));
    const b = new RedisSessionStore(client(redis));
    const one = await a.create(identity, "t", T0);
    const two = await a.create({ ...identity, sessionId: "kc-sid-2" }, "t", T0);
    const other = await a.create({ ...identity, userId: "user-2", sessionId: "kc-sid-3" }, "t", T0);
    expect(await b.deleteByKeycloak("kc-sid-1", "")).toBe(1);
    expect(await a.get(one.id, T0 + 1)).toBeUndefined();
    expect(await a.get(two.id, T0 + 1)).toBeDefined();
    expect(await b.deleteByKeycloak("", "user-1")).toBe(1); // the index still names the ended one, which counts for nothing
    expect(await a.get(two.id, T0 + 2)).toBeUndefined();
    expect(await a.get(other.id, T0 + 2)).toBeDefined();
    expect(await b.deleteByKeycloak("unknown", "")).toBe(0);
    expect(await b.deleteByKeycloak("bad key with spaces", "")).toBe(0);
  });
});

describe("the RESP client", () => {
  it("gives up on a handshake that does not finish, and leaves no connection behind", async () => {
    redis = await startFakeRedis({ password: "pw" });
    redis.mute = true;
    const c = new RespClient({ host: "127.0.0.1", port: redis.port, tls: false, password: "pw", connectTimeoutMs: 200, commandTimeoutMs: 5000 });
    clients.push(c);
    const started = Date.now();
    await expect(c.command("PING")).rejects.toThrow(/in time/);
    expect(Date.now() - started).toBeLessThan(2000);
    redis.mute = false;
    expect(await c.command("PING")).toBe("PONG");
  });

  it("answers concurrent commands in order", async () => {
    redis = await startFakeRedis();
    const c = client(redis);
    const results = await Promise.all([c.command("SET", "a", "1"), c.command("SET", "b", "2"), c.command("GET", "a"), c.command("GET", "b"), c.command("GET", "missing"), c.command("DEL", "a", "b")]);
    expect(results).toEqual(["OK", "OK", "1", "2", null, 2]);
  });

  it("carries multi-byte values and values with line breaks", async () => {
    redis = await startFakeRedis();
    const c = client(redis);
    const value = "Zoë 製造\r\nline two $5 *3";
    await c.command("SET", "k", value);
    expect(await c.command("GET", "k")).toBe(value);
  });

  it("authenticates, and refuses a wrong password without leaking it", async () => {
    redis = await startFakeRedis({ password: "s3cret" });
    expect(await client(redis, { password: "s3cret" }).command("PING")).toBe("PONG");
    const bad = client(redis, { password: "wrong-one" });
    await expect(bad.command("PING")).rejects.toBeInstanceOf(RespError);
    await expect(bad.command("PING")).rejects.not.toThrow(/wrong-one/);
    await expect(client(redis).command("PING")).rejects.toThrow(/NOAUTH/);
  });

  it("answers server errors as RespError and keeps working", async () => {
    redis = await startFakeRedis();
    const c = client(redis);
    await expect(c.command("NOPE")).rejects.toBeInstanceOf(RespError);
    expect(await c.command("PING")).toBe("PONG");
  });

  it("gives up on a server that does not answer, and reconnects", async () => {
    redis = await startFakeRedis();
    const c = client(redis);
    redis.mute = true;
    await expect(c.command("PING")).rejects.toThrow(/did not answer/);
    redis.mute = false;
    expect(await c.command("PING")).toBe("PONG");
  });

  it("fails fast when nothing listens", async () => {
    redis = await startFakeRedis();
    const port = redis.port;
    await redis.close();
    redis = undefined;
    const c = new RespClient({ host: "127.0.0.1", port, tls: false, commandTimeoutMs: 300 });
    clients.push(c);
    await expect(c.command("PING")).rejects.toThrow();
  });
});

describe("store settings", () => {
  it("parses redis and rediss URLs", () => {
    expect(parseStoreUrl("redis://valkey.svc:6380/2")).toEqual({ host: "valkey.svc", port: 6380, tls: false, username: undefined, password: undefined, db: 2 });
    expect(parseStoreUrl("rediss://app:p%40ss@valkey.svc")).toEqual({ host: "valkey.svc", port: 6379, tls: true, username: "app", password: "p@ss", db: 0 });
    expect(parseStoreUrl("redis://valkey.svc", "from-file").password).toBe("from-file");
    for (const bad of ["http://valkey", "redis://", "redis://h/16", "redis://h/x", "not a url"]) expect(() => parseStoreUrl(bad), bad).toThrow();
  });

  it("falls back to memory with a warning, and uses the shared store when it is set", () => {
    const warnings: string[] = [];
    expect(sessionStoreFromEnv({}, () => "", (m) => warnings.push(m))).toBeInstanceOf(MemorySessionStore);
    expect(warnings).toHaveLength(1);
    expect(sessionStoreFromEnv({ SESSION_STORE_URL: "redis://valkey.svc:6379" }, () => "", (m) => warnings.push(m))).toBeInstanceOf(RedisSessionStore);
    expect(warnings).toHaveLength(1);
    const read: string[] = [];
    sessionStoreFromEnv({ SESSION_STORE_URL: "redis://valkey.svc", SESSION_STORE_PASSWORD_FILE: "/secret/pw" }, (p) => (read.push(p), "pw\n"));
    expect(read).toEqual(["/secret/pw"]);
  });
});

describe("two pods behind one store", () => {
  it("keep a contact signed in across pods, and sign out everywhere", async () => {
    redis = await startFakeRedis();
    const shared = new RedisSessionStore(client(redis));
    rig = await startRig({ sessions: shared });
    const other = await startRig({ sessions: new RedisSessionStore(client(redis)) });
    try {
      const s = await signIn(rig);
      // The second pod never saw this sign-in, and knows the session.
      const me = await fetch(`${other.base}/bff/me`, { headers: { cookie: s.cookie } });
      expect(me.status).toBe(200);
      expect(((await me.json()) as { csrfToken: string }).csrfToken).toBe(s.csrf);
      expect((await api(other, s, "GET", "/api/v1/orders")).status).toBe(200);
      expect((await api(other, s, "POST", "/bff/logout")).status).toBe(200);
      expect((await fetch(`${rig.base}/bff/me`, { headers: { cookie: s.cookie } })).status).toBe(401);
    } finally {
      await other.close();
    }
  });

  it("completes a sign-in begun on one pod on another, once", async () => {
    redis = await startFakeRedis();
    rig = await startRig({ sessions: new RedisSessionStore(client(redis)) });
    const login = await fetch(`${rig.base}/bff/login`, { redirect: "manual" });
    const url = new URL(login.headers.get("location") as string);
    (rig as Rig & { setNonce: (n: string) => void }).setNonce(url.searchParams.get("nonce") as string);
    const state = url.searchParams.get("state");
    expect((await fetch(`${rig.base}/bff/callback?code=abc&state=${state}`, { redirect: "manual" })).status).toBe(302);
    expect((await fetch(`${rig.base}/bff/callback?code=abc&state=${state}`, { redirect: "manual" })).status).toBe(400);
  });

  it("answers 503, not 401, when the store is down, so a contact is not told to sign in again for an outage", async () => {
    redis = await startFakeRedis();
    rig = await startRig({ sessions: new RedisSessionStore(client(redis)) });
    const s = await signIn(rig);
    redis.mute = true;
    const res = await fetch(`${rig.base}/bff/me`, { headers: { cookie: s.cookie } });
    expect(res.status).toBe(503);
    expect(((await res.json()) as { code: string }).code).toBe("CUS-BFF-503");
    redis.mute = false;
    expect((await fetch(`${rig.base}/bff/me`, { headers: { cookie: s.cookie } })).status).toBe(200);
  });
});
