// Virus scan hook for documents a supplier uploads (#511). The portal scans an upload before it reaches party. The scanner is an
// interface so the engine is a deployment choice: ClamAV over the clamd INSTREAM protocol is the adapter that ships, and tests use
// the fake. A scanner that is configured is required: if it cannot answer, the upload is refused, never waved through.

import { createConnection } from "node:net";

export type ScanVerdict = { clean: true } | { clean: false; signature: string };

export interface Scanner {
  /** Scans one upload. Resolves with the verdict, rejects when the scan could not be made. */
  scan(content: Buffer): Promise<ScanVerdict>;
}

/** What the portal does with an upload: `scanner` is absent when none is configured; `allowUnscanned` must then be set on purpose. */
export interface ScanPolicy {
  scanner?: Scanner | undefined;
  allowUnscanned: boolean;
}

export type ScanOutcome = { accept: true; scanned: boolean } | { accept: false; reason: "infected"; signature: string } | { accept: false; reason: "unavailable" };

/** Applies the policy to an upload. Nothing configured is refused unless `allowUnscanned` says otherwise; a configured scanner that errors is refused. */
export async function screenUpload(policy: ScanPolicy, content: Buffer, log: (entry: Record<string, unknown>) => void): Promise<ScanOutcome> {
  if (!policy.scanner) {
    if (policy.allowUnscanned) return { accept: true, scanned: false };
    log({ msg: "upload refused: no virus scanner is configured and unscanned uploads are not allowed" });
    return { accept: false, reason: "unavailable" };
  }
  try {
    const verdict = await policy.scanner.scan(content);
    if (verdict.clean) return { accept: true, scanned: true };
    log({ msg: "upload refused: infected", signature: verdict.signature });
    return { accept: false, reason: "infected", signature: verdict.signature };
  } catch (err) {
    log({ msg: "upload refused: the virus scan failed", err: String(err) });
    return { accept: false, reason: "unavailable" };
  }
}

/** A scanner for tests: a verdict by content. */
export class FakeScanner implements Scanner {
  readonly seen: Buffer[] = [];
  private readonly verdict: (content: Buffer) => ScanVerdict | Error;
  constructor(verdict: (content: Buffer) => ScanVerdict | Error = () => ({ clean: true })) {
    this.verdict = verdict;
  }
  async scan(content: Buffer): Promise<ScanVerdict> {
    this.seen.push(content);
    const v = this.verdict(content);
    if (v instanceof Error) throw v;
    return v;
  }
}

const CHUNK_BYTES = 64 * 1024;
const MAX_REPLY_BYTES = 4096;

export interface ClamdOptions {
  host: string;
  port: number;
  timeoutMs?: number;
}

/** ClamAV through clamd's INSTREAM command over TCP: `zINSTREAM\0`, length-prefixed chunks, a zero length, then one reply line. */
export class ClamdScanner implements Scanner {
  private readonly host: string;
  private readonly port: number;
  private readonly timeoutMs: number;
  constructor(options: ClamdOptions) {
    this.host = options.host;
    this.port = options.port;
    this.timeoutMs = options.timeoutMs ?? 30_000;
  }

  scan(content: Buffer): Promise<ScanVerdict> {
    return new Promise((resolve, reject) => {
      const socket = createConnection({ host: this.host, port: this.port });
      const reply: Buffer[] = [];
      let size = 0;
      let done = false;
      const finish = (fn: () => void): void => {
        if (done) return;
        done = true;
        socket.destroy();
        fn();
      };
      socket.setTimeout(this.timeoutMs, () => finish(() => reject(new Error("clamd timed out"))));
      socket.on("error", (err) => finish(() => reject(err)));
      socket.on("data", (d) => {
        size += d.length;
        if (size > MAX_REPLY_BYTES) return finish(() => reject(new Error("clamd reply is too long")));
        reply.push(d);
      });
      socket.on("close", () => finish(() => {
        try {
          resolve(parseClamdReply(Buffer.concat(reply).toString("utf8")));
        } catch (err) {
          reject(err);
        }
      }));
      socket.on("connect", () => {
        socket.write("zINSTREAM\0");
        for (let i = 0; i < content.length; i += CHUNK_BYTES) {
          const chunk = content.subarray(i, i + CHUNK_BYTES);
          const len = Buffer.alloc(4);
          len.writeUInt32BE(chunk.length);
          socket.write(len);
          socket.write(chunk);
        }
        socket.write(Buffer.alloc(4));
      });
    });
  }
}

/** `stream: OK`, `stream: Eicar-Test-Signature FOUND` or `... ERROR`; anything else is an error, never a clean verdict. */
export function parseClamdReply(raw: string): ScanVerdict {
  const line = raw.replace(/\0/g, "").trim();
  if (/^stream:\s+OK$/.test(line)) return { clean: true };
  const found = /^stream:\s+(.+)\s+FOUND$/.exec(line);
  if (found) return { clean: false, signature: found[1] as string };
  throw new Error(`clamd did not give a verdict: ${line.slice(0, 120) || "(empty reply)"}`);
}

/** CLAMAV_ADDR (host:port) turns the scanner on; CLAMAV_TIMEOUT_MS bounds one scan; SCAN_ALLOW_UNSCANNED=true accepts uploads with no scanner. */
export function scanPolicyFromEnv(env: Record<string, string | undefined>): ScanPolicy {
  const addr = env["CLAMAV_ADDR"];
  const allowUnscanned = env["SCAN_ALLOW_UNSCANNED"] === "true";
  if (!addr) return { allowUnscanned };
  const i = addr.lastIndexOf(":");
  const port = Number(addr.slice(i + 1));
  if (i < 1 || !Number.isInteger(port) || port < 1 || port > 65535) throw new TypeError(`CLAMAV_ADDR ${JSON.stringify(addr)} is not host:port`);
  const timeout = env["CLAMAV_TIMEOUT_MS"] ? Number(env["CLAMAV_TIMEOUT_MS"]) : 30_000;
  if (!Number.isInteger(timeout) || timeout < 1000) throw new TypeError("CLAMAV_TIMEOUT_MS must be at least 1000");
  return { scanner: new ClamdScanner({ host: addr.slice(0, i), port, timeoutMs: timeout }), allowUnscanned };
}
