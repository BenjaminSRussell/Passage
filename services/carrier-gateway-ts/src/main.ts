// Entry point: HTTP_ADDR (default :8080), PASSAGE_SERVICE_PUBLIC_KEYS (the callers' Ed25519 keys) and
// CARRIER_ADAPTERS (comma separated; only "sandbox" exists). Unknown adapters stop the process at startup.

import type { CarrierAdapter } from "./adapter.ts";
import { Gateway } from "./gateway.ts";
import { SandboxCarrier } from "./sandbox.ts";
import { createHttpServer } from "./server.ts";
import { createVerifier, parsePublicKeys } from "./serviceToken.ts";

export function adaptersFromEnv(raw: string | undefined): CarrierAdapter[] {
  const names = (raw ?? "sandbox").split(",").map((s) => s.trim()).filter((s) => s !== "");
  if (names.length === 0) throw new TypeError("CARRIER_ADAPTERS names no adapter");
  return names.map((name) => {
    if (name === "sandbox") return new SandboxCarrier();
    throw new TypeError(`CARRIER_ADAPTERS names ${JSON.stringify(name)}; only "sandbox" is built (NOT-IMPLEMENTED.md)`);
  });
}

function listenAddress(raw: string | undefined): { host?: string; port: number } {
  const addr = raw ?? ":8080";
  const i = addr.lastIndexOf(":");
  const port = Number(addr.slice(i + 1));
  if (i < 0 || !Number.isInteger(port) || port < 0 || port > 65535) throw new TypeError(`HTTP_ADDR ${JSON.stringify(addr)} is not [host]:port`);
  const host = addr.slice(0, i);
  return host ? { host, port } : { port };
}

function main(): void {
  const keys = parsePublicKeys(process.env["PASSAGE_SERVICE_PUBLIC_KEYS"] ?? "");
  if (keys.size === 0) console.warn(JSON.stringify({ msg: "PASSAGE_SERVICE_PUBLIC_KEYS is empty: every /v1 request is refused" }));
  const gateway = new Gateway(adaptersFromEnv(process.env["CARRIER_ADAPTERS"]));
  const server = createHttpServer({ gateway, verifier: createVerifier({ keys }) });
  const { host, port } = listenAddress(process.env["HTTP_ADDR"]);
  server.listen(port, host, () => console.log(JSON.stringify({ msg: "carrier-gateway listening", host: host ?? "", port })));
  const stop = (): void => {
    server.close(() => process.exit(0));
  };
  process.on("SIGTERM", stop);
  process.on("SIGINT", stop);
}

if (import.meta.url === `file://${process.argv[1]}`) main();
