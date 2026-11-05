// Keeps the gateway's grant table equal to policies/authz/data.json and platform/go/tenant/service.go.

import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { DUTY_LABEL, DUTY_RATE, DUTY_TRACK, SERVICE_GRANTS, SERVICE_NAME } from "../src/serviceToken.ts";

const root = new URL("../../../", import.meta.url);
const data = JSON.parse(readFileSync(new URL("policies/authz/data.json", root), "utf8")) as {
  duties: Record<string, { service_only?: boolean; permissions: Array<{ resource: string; methods: string[] }> }>;
  service_grants: Record<string, Record<string, string[]>>;
};

function fromData(): Record<string, string[]> {
  const out: Record<string, string[]> = {};
  for (const [iss, targets] of Object.entries(data.service_grants)) {
    if (targets[SERVICE_NAME]) out[iss] = targets[SERVICE_NAME];
  }
  return out;
}

function fromGo(): Record<string, string[]> {
  const src = readFileSync(new URL("platform/go/tenant/service.go", root), "utf8");
  const table = src.slice(src.indexOf("var serviceGrants = "), src.indexOf("// ServiceGrantTable"));
  const out: Record<string, string[]> = {};
  let issuer = "";
  for (const line of table.split("\n")) {
    const iss = /^\t"([^"]+)": \{$/.exec(line);
    if (iss) issuer = iss[1]!;
    const aud = /^\t\t"([^"]+)":\s*\{(.*)\},$/.exec(line);
    if (aud && aud[1] === SERVICE_NAME) out[issuer] = [...aud[2]!.matchAll(/"([^"]+)"/g)].map((m) => m[1]!);
  }
  return out;
}

describe("carrier-gateway service grants", () => {
  it("match policies/authz/data.json and the Go table", () => {
    expect(fromData()).toEqual(SERVICE_GRANTS);
    expect(fromGo()).toEqual(SERVICE_GRANTS);
  });

  it("are seeded service-only duties that open exactly the gateway's routes", () => {
    const routes: Record<string, Array<{ resource: string; methods: string[] }>> = {
      [DUTY_RATE]: [
        { resource: "/v1/carrier/rates", methods: ["POST"] },
        { resource: "/v1/carrier/carriers", methods: ["GET"] },
      ],
      [DUTY_LABEL]: [{ resource: "/v1/carrier/labels", methods: ["POST"] }],
      [DUTY_TRACK]: [{ resource: "/v1/carrier/tracking/*/*", methods: ["GET"] }],
    };
    for (const [duty, perms] of Object.entries(routes)) {
      expect(data.duties[duty]?.service_only).toBe(true);
      expect(data.duties[duty]?.permissions).toEqual(perms);
    }
    // api/openapi.yaml documents the same operations with the same duties.
    const documented: Record<string, string> = {};
    let path = "";
    let op = "";
    for (const line of readFileSync(new URL("../api/openapi.yaml", import.meta.url), "utf8").split("\n")) {
      const p = /^ {2}(\/\S+):$/.exec(line);
      const o = /^ {4}(get|post|put|patch|delete):$/.exec(line);
      const d = /^ {6}x-passage-duty: (\S+)$/.exec(line);
      if (p) path = p[1]!;
      else if (o) op = `${o[1]!.toUpperCase()} ${path.replace(/\{[^}]+\}/g, "*")}`;
      else if (d) documented[op] = d[1]!;
    }
    const fromRoutes = Object.fromEntries(Object.entries(routes).flatMap(([duty, perms]) => perms.map((p) => [`${p.methods[0]} ${p.resource}`, duty])));
    expect(documented).toEqual(fromRoutes);
  });
});
