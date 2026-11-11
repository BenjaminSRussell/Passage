// An in-process stand-in for a Valkey or Redis server: RESP2 over TCP with AUTH, SELECT, PING, SET (EX, PX, KEEPTTL), GET, GETDEL
// and DEL, and expiry against a clock the test controls. It also lets a test cut every connection or stop answering.

import { createServer, type Server, type Socket } from "node:net";
import type { AddressInfo } from "node:net";
import { parseReply, RespError } from "../src/resp.ts";

export interface FakeRedis {
  port: number;
  /** Key to value, for what is not expired. */
  dump(): Map<string, string>;
  commands: string[][];
  clock: { ms: number };
  /** Drops every open connection (a server restart). */
  cut(): void;
  /** While true, commands are read and never answered. */
  mute: boolean;
  /** The password AUTH must carry, when set. */
  password: string | undefined;
  close(): Promise<void>;
}

export async function startFakeRedis(opts: { password?: string } = {}): Promise<FakeRedis> {
  const store = new Map<string, { value: string; expiresAt: number | undefined }>();
  const sockets = new Set<Socket>();
  const sets = new Map<string, Set<string>>();
  const clock = { ms: 1_000_000 };
  const fake = { commands: [] as string[][], clock, mute: false, password: opts.password } as FakeRedis;

  const live = (key: string) => {
    const e = store.get(key);
    if (e && e.expiresAt !== undefined && e.expiresAt <= clock.ms) store.delete(key);
    return store.get(key);
  };

  const run = (args: string[], state: { authed: boolean }): string => {
    const [name, ...rest] = [args[0]!.toUpperCase(), ...args.slice(1)] as [string, ...string[]];
    if (name === "AUTH") {
      if (fake.password === undefined) return "-ERR AUTH called without any password configured\r\n";
      if (rest[rest.length - 1] !== fake.password) return "-WRONGPASS invalid username-password pair\r\n";
      state.authed = true;
      return "+OK\r\n";
    }
    if (fake.password !== undefined && !state.authed) return "-NOAUTH Authentication required.\r\n";
    switch (name) {
      case "PING":
        return "+PONG\r\n";
      case "SELECT":
        return "+OK\r\n";
      case "GET": {
        const e = live(rest[0]!);
        return e ? `$${Buffer.byteLength(e.value)}\r\n${e.value}\r\n` : "$-1\r\n";
      }
      case "GETDEL": {
        const e = live(rest[0]!);
        store.delete(rest[0]!);
        return e ? `$${Buffer.byteLength(e.value)}\r\n${e.value}\r\n` : "$-1\r\n";
      }
      case "DEL": {
        let n = 0;
        for (const k of rest) {
          if (live(k)) n += store.delete(k) ? 1 : 0;
          else if (sets.delete(k)) n += 1;
        }
        return `:${n}\r\n`;
      }
      case "SADD": {
        const set = sets.get(rest[0]!) ?? new Set<string>();
        set.add(rest[1]!);
        sets.set(rest[0]!, set);
        return ":1\r\n";
      }
      case "PEXPIRE":
        return ":1\r\n";
      case "SMEMBERS": {
        const m = [...(sets.get(rest[0]!) ?? [])];
        return `*${m.length}\r\n${m.map((x) => `$${Buffer.byteLength(x)}\r\n${x}\r\n`).join("")}`;
      }
      case "SET": {
        const [key, value, ...options] = rest as [string, string, ...string[]];
        let expiresAt: number | undefined;
        let keepTtl = false;
        let onlyIfExists = false;
        for (let i = 0; i < options.length; i++) {
          const o = options[i]!.toUpperCase();
          if (o === "XX") {
            onlyIfExists = true;
            continue;
          }
          if (o === "EX") expiresAt = clock.ms + Number(options[++i]) * 1000;
          else if (o === "PX") expiresAt = clock.ms + Number(options[++i]);
          else if (o === "KEEPTTL") keepTtl = true;
          else return "-ERR syntax error\r\n";
        }
        if (onlyIfExists && !live(key)) return "$-1\r\n";
        if (keepTtl) expiresAt = live(key)?.expiresAt;
        store.set(key, { value, expiresAt });
        return "+OK\r\n";
      }
      default:
        return `-ERR unknown command '${name}'\r\n`;
    }
  };

  const server: Server = createServer((socket) => {
    sockets.add(socket);
    socket.on("close", () => sockets.delete(socket));
    socket.on("error", () => {});
    const state = { authed: false };
    let buf: Buffer = Buffer.alloc(0);
    socket.on("data", (chunk) => {
      buf = Buffer.concat([buf, chunk]);
      for (;;) {
        const parsed = parseReply(buf, 0);
        if (!parsed || parsed.value instanceof RespError || !Array.isArray(parsed.value)) break;
        buf = buf.subarray(parsed.next);
        const args = parsed.value.map(String);
        fake.commands.push(args);
        if (!fake.mute) socket.write(run(args, state));
      }
    });
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  fake.port = (server.address() as AddressInfo).port;
  fake.dump = () => {
    const out = new Map<string, string>();
    for (const k of store.keys()) {
      const e = live(k);
      if (e) out.set(k, e.value);
    }
    return out;
  };
  fake.cut = () => {
    for (const s of sockets) s.destroy();
  };
  fake.close = () =>
    new Promise((resolve) => {
      for (const s of sockets) s.destroy();
      server.close(() => resolve());
    });
  return fake;
}
