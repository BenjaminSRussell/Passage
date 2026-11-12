// A small RESP2 client for Valkey and Redis, enough for the session store: AUTH, SELECT, SET, GET, GETDEL, DEL, PING. No runtime
// dependency (this service ships none). One connection carries the commands in order; a failed or timed-out command drops the
// connection and the next command opens a new one, so a restart of the server heals by itself.

import { connect as netConnect, type Socket } from "node:net";
import { connect as tlsConnect } from "node:tls";

export type RespValue = string | number | null | RespValue[];

export class RespError extends Error {}

export interface RespOptions {
  host: string;
  port: number;
  tls: boolean;
  username?: string | undefined;
  password?: string | undefined;
  db?: number | undefined;
  /** Bounds connect, the TLS handshake and AUTH/SELECT together (default 5 s). */
  connectTimeoutMs?: number;
  commandTimeoutMs?: number;
  /** PEM of the certificate authority that signed the server's certificate, for rediss:// with a private CA. */
  ca?: string | Buffer | undefined;
}

export type Dial = (opts: RespOptions) => Socket;

const defaultDial: Dial = (o) => {
  const socket = o.tls ? tlsConnect({ host: o.host, port: o.port, servername: o.host, ...(o.ca ? { ca: o.ca } : {}) }) : netConnect({ host: o.host, port: o.port });
  socket.setNoDelay(true);
  return socket;
};

export function encodeCommand(args: readonly (string | number)[]): Buffer {
  const parts: Buffer[] = [Buffer.from(`*${args.length}\r\n`)];
  for (const a of args) {
    const b = Buffer.from(String(a));
    parts.push(Buffer.from(`$${b.length}\r\n`), b, Buffer.from("\r\n"));
  }
  return Buffer.concat(parts);
}

/** Parses one reply at offset; undefined when the buffer does not hold a whole reply yet. */
export function parseReply(buf: Buffer, offset: number): { value: RespValue | RespError; next: number } | undefined {
  const eol = buf.indexOf("\r\n", offset);
  if (eol < 0 || offset >= buf.length) return undefined;
  const type = String.fromCharCode(buf[offset] as number);
  const line = buf.toString("utf8", offset + 1, eol);
  const after = eol + 2;
  switch (type) {
    case "+":
      return { value: line, next: after };
    case "-":
      return { value: new RespError(line), next: after };
    case ":":
      return { value: Number(line), next: after };
    case "$": {
      const len = Number(line);
      if (len < 0) return { value: null, next: after };
      if (buf.length < after + len + 2) return undefined;
      return { value: buf.toString("utf8", after, after + len), next: after + len + 2 };
    }
    case "*": {
      const n = Number(line);
      if (n < 0) return { value: null, next: after };
      const items: RespValue[] = [];
      let at = after;
      for (let i = 0; i < n; i++) {
        const item = parseReply(buf, at);
        if (!item) return undefined;
        items.push(item.value instanceof RespError ? null : item.value);
        at = item.next;
      }
      return { value: items, next: at };
    }
    default:
      throw new RespError(`unexpected reply type ${JSON.stringify(type)}`);
  }
}

interface Pending {
  resolve: (v: RespValue) => void;
  reject: (e: Error) => void;
  timer: NodeJS.Timeout;
}

export class RespClient {
  private socket: Socket | undefined;
  private ready: Promise<void> | undefined;
  private pending: Pending[] = [];
  private buffer: Buffer = Buffer.alloc(0);
  private readonly opts: RespOptions;
  private readonly dial: Dial;

  constructor(opts: RespOptions, dial: Dial = defaultDial) {
    this.opts = opts;
    this.dial = dial;
  }

  /** Sends one command and answers its reply; a server error reply rejects with RespError. */
  async command(...args: (string | number)[]): Promise<RespValue> {
    await this.open();
    return this.send(args);
  }

  close(): void {
    this.drop(new Error("the client is closed"));
  }

  private open(): Promise<void> {
    if (this.ready) return this.ready;
    const ready = this.connectWithin();
    this.ready = ready;
    ready.catch(() => {
      if (this.ready === ready) this.ready = undefined;
    });
    return ready;
  }

  /** The whole handshake (TCP, TLS, AUTH, SELECT) under one deadline; a failed or late one drops the socket so nothing leaks. */
  private async connectWithin(): Promise<void> {
    const limit = this.opts.connectTimeoutMs ?? 5000;
    let timer: NodeJS.Timeout | undefined;
    const late = new Promise<never>((_, reject) => {
      timer = setTimeout(() => reject(new Error("the store did not accept the connection in time")), limit);
    });
    const attempt = this.connectAndHandshake();
    try {
      await Promise.race([attempt, late]);
    } catch (err) {
      attempt.catch(() => undefined);
      this.drop(err instanceof Error ? err : new Error(String(err)));
      throw err;
    } finally {
      clearTimeout(timer);
    }
  }

  private async connectAndHandshake(): Promise<void> {
    const socket = this.dial(this.opts);
    this.socket = socket;
    this.buffer = Buffer.alloc(0);
    socket.on("data", (chunk: Buffer) => this.onData(socket, chunk));
    socket.on("error", (err) => this.drop(err, socket));
    socket.on("close", () => this.drop(new Error("the connection closed"), socket));
    await new Promise<void>((resolve, reject) => {
      const event = this.opts.tls ? "secureConnect" : "connect";
      socket.once(event, () => resolve());
      socket.once("error", reject);
    });
    if (this.opts.password !== undefined) {
      await this.send(this.opts.username ? ["AUTH", this.opts.username, this.opts.password] : ["AUTH", this.opts.password]);
    }
    if (this.opts.db) await this.send(["SELECT", this.opts.db]);
  }

  private send(args: readonly (string | number)[]): Promise<RespValue> {
    const socket = this.socket;
    if (!socket || socket.destroyed) return Promise.reject(new Error("the connection is not open"));
    return new Promise<RespValue>((resolve, reject) => {
      const timer = setTimeout(() => this.drop(new Error("the store did not answer in time"), socket), this.opts.commandTimeoutMs ?? 3000);
      this.pending.push({ resolve, reject, timer });
      socket.write(encodeCommand(args));
    });
  }

  private onData(socket: Socket, chunk: Buffer): void {
    if (socket !== this.socket) return;
    this.buffer = this.buffer.length === 0 ? chunk : Buffer.concat([this.buffer, chunk]);
    try {
      for (;;) {
        const reply = parseReply(this.buffer, 0);
        if (!reply) break;
        this.buffer = this.buffer.subarray(reply.next);
        const waiting = this.pending.shift();
        if (!waiting) throw new RespError("an unrequested reply arrived");
        clearTimeout(waiting.timer);
        if (reply.value instanceof RespError) waiting.reject(reply.value);
        else waiting.resolve(reply.value);
      }
    } catch (err) {
      this.drop(err instanceof Error ? err : new Error(String(err)), socket);
    }
  }

  /** Fails every command in flight and forgets the socket, so the next command reconnects. */
  private drop(err: Error, only?: Socket): void {
    if (only && only !== this.socket) return;
    const socket = this.socket;
    this.socket = undefined;
    this.ready = undefined;
    this.buffer = Buffer.alloc(0);
    socket?.destroy();
    const waiting = this.pending;
    this.pending = [];
    for (const p of waiting) {
      clearTimeout(p.timer);
      p.reject(err);
    }
  }
}

/** redis://[user:password@]host[:port][/db] or rediss:// (TLS). A password passed separately wins over the one in the URL. */
export function parseStoreUrl(raw: string, password?: string): RespOptions {
  const url = new URL(raw);
  if (url.protocol !== "redis:" && url.protocol !== "rediss:") throw new TypeError("SESSION_STORE_URL must be a redis:// or rediss:// URL");
  if (!url.hostname) throw new TypeError("SESSION_STORE_URL has no host");
  const dbText = url.pathname.replace(/^\//, "");
  const db = dbText === "" ? 0 : Number(dbText);
  if (!Number.isInteger(db) || db < 0 || db > 15) throw new TypeError("SESSION_STORE_URL names a database that is not 0 to 15");
  const urlPassword = url.password ? decodeURIComponent(url.password) : undefined;
  const username = url.username ? decodeURIComponent(url.username) : undefined;
  return {
    host: url.hostname.replace(/^\[|\]$/g, ""),
    port: url.port ? Number(url.port) : 6379,
    tls: url.protocol === "rediss:",
    username,
    password: password ?? urlPassword,
    db,
  };
}
