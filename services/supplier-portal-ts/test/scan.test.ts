import { createServer, type Server } from "node:net";
import { afterEach, describe, expect, it } from "vitest";
import { ClamdScanner, FakeScanner, parseClamdReply, scanPolicyFromEnv, screenUpload } from "../src/scan.ts";

const servers: Server[] = [];
afterEach(() => {
  for (const s of servers.splice(0)) s.close();
});

/** A clamd stand-in: reads zINSTREAM and the length-prefixed chunks, then answers `reply` (or hangs / drops). */
async function fakeClamd(reply: (content: Buffer) => string | undefined): Promise<{ port: number; commands: string[] }> {
  const commands: string[] = [];
  const server = createServer((socket) => {
    let buf = Buffer.alloc(0);
    let started = false;
    const body: Buffer[] = [];
    socket.on("data", (d) => {
      buf = Buffer.concat([buf, d]);
      if (!started) {
        const nul = buf.indexOf(0);
        if (nul < 0) return;
        commands.push(buf.subarray(0, nul).toString());
        buf = buf.subarray(nul + 1);
        started = true;
      }
      for (;;) {
        if (buf.length < 4) return;
        const len = buf.readUInt32BE(0);
        if (len === 0) {
          const out = reply(Buffer.concat(body));
          if (out === undefined) return;
          socket.end(`${out}\0`);
          return;
        }
        if (buf.length < 4 + len) return;
        body.push(buf.subarray(4, 4 + len));
        buf = buf.subarray(4 + len);
      }
    });
  });
  servers.push(server);
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  return { port: (server.address() as { port: number }).port, commands };
}

describe("clamd INSTREAM adapter", () => {
  it("streams the content in chunks and reads a clean verdict", async () => {
    const seen: Buffer[] = [];
    const clamd = await fakeClamd((c) => (seen.push(c), "stream: OK"));
    const big = Buffer.alloc(200_000, 7);
    const verdict = await new ClamdScanner({ host: "127.0.0.1", port: clamd.port }).scan(big);
    expect(verdict).toEqual({ clean: true });
    expect(clamd.commands).toEqual(["zINSTREAM"]);
    expect(seen[0]!.equals(big)).toBe(true);
  });

  it("reads a signature from a FOUND verdict", async () => {
    const clamd = await fakeClamd(() => "stream: Eicar-Test-Signature FOUND");
    expect(await new ClamdScanner({ host: "127.0.0.1", port: clamd.port }).scan(Buffer.from("x"))).toEqual({ clean: false, signature: "Eicar-Test-Signature" });
  });

  it("is an error, never a clean verdict, when clamd errors, answers nonsense, hangs or is down", async () => {
    const errored = await fakeClamd(() => "INSTREAM size limit exceeded. ERROR");
    await expect(new ClamdScanner({ host: "127.0.0.1", port: errored.port }).scan(Buffer.from("x"))).rejects.toThrow(/did not give a verdict/);
    const hung = await fakeClamd(() => undefined);
    await expect(new ClamdScanner({ host: "127.0.0.1", port: hung.port, timeoutMs: 100 }).scan(Buffer.from("x"))).rejects.toThrow(/timed out/);
    const closed = await fakeClamd(() => "stream: OK");
    const port = closed.port;
    servers.pop()!.close();
    await expect(new ClamdScanner({ host: "127.0.0.1", port, timeoutMs: 500 }).scan(Buffer.from("x"))).rejects.toThrow();
  });

  it("parses replies exactly", () => {
    expect(parseClamdReply("stream: OK\0")).toEqual({ clean: true });
    expect(() => parseClamdReply("")).toThrow();
    expect(() => parseClamdReply("stream: OK and more")).toThrow();
    expect(() => parseClamdReply("stream: x FOUNDED")).toThrow();
  });
});

describe("the upload policy", () => {
  const log = (): void => {};
  it("refuses with no scanner unless unscanned uploads are allowed on purpose", async () => {
    expect(await screenUpload({ allowUnscanned: false }, Buffer.from("x"), log)).toEqual({ accept: false, reason: "unavailable" });
    expect(await screenUpload({ allowUnscanned: true }, Buffer.from("x"), log)).toEqual({ accept: true, scanned: false });
  });

  it("requires a configured scanner: infected is refused and a failing scanner is refused even when unscanned uploads are allowed", async () => {
    const infected = new FakeScanner(() => ({ clean: false, signature: "Win.Test" }));
    expect(await screenUpload({ scanner: infected, allowUnscanned: true }, Buffer.from("x"), log)).toEqual({ accept: false, reason: "infected", signature: "Win.Test" });
    const broken = new FakeScanner(() => new Error("down"));
    expect(await screenUpload({ scanner: broken, allowUnscanned: true }, Buffer.from("x"), log)).toEqual({ accept: false, reason: "unavailable" });
    expect(await screenUpload({ scanner: new FakeScanner(), allowUnscanned: false }, Buffer.from("x"), log)).toEqual({ accept: true, scanned: true });
  });

  it("reads its settings from the environment", () => {
    expect(scanPolicyFromEnv({})).toEqual({ allowUnscanned: false });
    expect(scanPolicyFromEnv({ SCAN_ALLOW_UNSCANNED: "true" })).toEqual({ allowUnscanned: true });
    expect(scanPolicyFromEnv({ SCAN_ALLOW_UNSCANNED: "yes" }).allowUnscanned).toBe(false);
    const policy = scanPolicyFromEnv({ CLAMAV_ADDR: "clamav.clamav.svc:3310", CLAMAV_TIMEOUT_MS: "5000" });
    expect(policy.scanner).toBeInstanceOf(ClamdScanner);
    expect(policy.allowUnscanned).toBe(false);
    expect(() => scanPolicyFromEnv({ CLAMAV_ADDR: "nohost" })).toThrow(TypeError);
    expect(() => scanPolicyFromEnv({ CLAMAV_ADDR: "h:99999" })).toThrow(TypeError);
    expect(() => scanPolicyFromEnv({ CLAMAV_ADDR: "h:3310", CLAMAV_TIMEOUT_MS: "10" })).toThrow(TypeError);
  });
});
