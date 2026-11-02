import { randomUUID } from "node:crypto";
import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import pg from "pg";

import { readFileSync } from "node:fs";

import { FieldKeys } from "../src/crypt.ts";
import { HubStore, MIGRATIONS_DIR, migrate } from "../src/pgStore.ts";
import { createHubHandler, type HubApiDeps } from "../src/server.ts";

export const ownerUrl = process.env["PASSAGE_TEST_DATABASE_URL"];
const appUrl = process.env["PASSAGE_TEST_APP_DATABASE_URL"] ?? ownerUrl;

const vectors = JSON.parse(readFileSync(new URL("../testdata/fieldcrypt-vectors.json", import.meta.url), "utf8")) as { keys: string; currentKeyId: string };
/** Test master keys (the Go cross-language vectors' keys). */
export const testKeys = FieldKeys.fromEnv({ FIELDCRYPT_KEYS: vectors.keys, FIELDCRYPT_CURRENT_KEY_ID: vectors.currentKeyId }) as FieldKeys;

export interface Fixture {
  store: HubStore;
  /** The same database through passage_app, with no field encryption keys. */
  keyless: HubStore;
  app: pg.Pool;
  owner: pg.Pool;
  schema: string;
  close(): Promise<void>;
}

/** A private schema migrated by the owner role, used through the passage_app role when one is configured. */
export async function fixture(): Promise<Fixture> {
  const schema = `hub_${randomUUID().replaceAll("-", "").slice(0, 12)}`;
  const admin = new pg.Pool({ connectionString: ownerUrl, max: 2 });
  await admin.query(`create schema ${schema}`);
  const options = `-c search_path=${schema}`;
  const owner = new pg.Pool({ connectionString: ownerUrl, options, max: 2 });
  await migrate(owner, MIGRATIONS_DIR);
  const app = new pg.Pool({ connectionString: appUrl, options, max: 8 });
  return {
    store: new HubStore(app, testKeys),
    keyless: new HubStore(app, null),
    app,
    owner,
    schema,
    async close() {
      await app.end();
      await owner.end();
      await admin.query(`drop schema ${schema} cascade`);
      await admin.end();
    },
  };
}

export interface Receiver {
  url: string;
  hits: { headers: Record<string, string | string[] | undefined>; body: string }[];
  respond: (hit: number) => number;
  server: Server;
}

export async function receiver(respond: (hit: number) => number = () => 200): Promise<Receiver> {
  const r: Receiver = { url: "", hits: [], respond, server: createServer() };
  r.server.on("request", (req, res) => {
    const chunks: Buffer[] = [];
    req.on("data", (c: Buffer) => chunks.push(c));
    req.on("end", () => {
      r.hits.push({ headers: req.headers, body: Buffer.concat(chunks).toString() });
      res.writeHead(r.respond(r.hits.length)).end();
    });
  });
  await new Promise<void>((ok) => r.server.listen(0, "127.0.0.1", ok));
  // The host name is public to the create-time guard (stub resolver) and pinned to loopback by the transport stub below.
  r.url = `http://receiver.example:${(r.server.address() as AddressInfo).port}/hook`;
  return r;
}

export const guard = { allowHttp: true, resolve: async () => ["93.184.216.34"] };
export const loopbackValidate = async (u: string) => ({ url: new URL(u), address: "127.0.0.1", family: 4 as const });

export interface Api {
  base: string;
  server: Server;
  call(method: string, path: string, who: Who, body?: unknown, extra?: Record<string, string>): Promise<{ status: number; body: any }>;
}

export interface Who {
  tenant: string;
  duties?: string[];
}

export async function api(deps: HubApiDeps): Promise<Api> {
  const server = createServer(createHubHandler(deps));
  await new Promise<void>((ok) => server.listen(0, "127.0.0.1", ok));
  const base = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
  return {
    base,
    server,
    async call(method, path, who, body, extra = {}) {
      const res = await fetch(base + path, {
        method,
        headers: {
          "x-passage-tenant": who.tenant,
          "x-passage-user": "11111111-1111-4111-8111-111111111111",
          "x-passage-duties": (who.duties ?? ["hub.subscription.write", "hub.delivery.read"]).join(","),
          "content-type": "application/json",
          ...extra,
        },
        ...(body === undefined ? {} : { body: JSON.stringify(body) }),
      });
      const text = await res.text();
      return { status: res.status, body: text === "" ? null : JSON.parse(text) };
    },
  };
}
