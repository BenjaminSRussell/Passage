// Test-only: answers the shell's entity lookups (names behind ids) without touching the page's own fetch stub, so a
// test that counts or scripts its service calls is not disturbed by them. A name reads "Vendor 22222222": the kind and
// the first block of the id.
import { clearResolvedOptions, setLookupFetcher } from "../../shell/lookup/entities";

const KINDS: [RegExp, string][] = [
  [/^\/api\/v1\/org\/legal-entities\/([^/]+)$/, "Legal entity"],
  [/^\/api\/v1\/org\/sites\/([^/]+)$/, "Site"],
  [/^\/api\/v1\/org\/warehouses\/([^/]+)$/, "Warehouse"],
  [/^\/api\/v1\/party\/parties\/(?:vendor:|customer:)?([^/]+)$/, "Party"],
  [/^\/api\/v1\/product\/products\/([^/]+)$/, "Product"],
  [/^\/api\/v1\/hr\/workers\/([^/]+)$/, "Worker"],
];

const reply = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

export function lookupName(kind: string, id: string): string {
  return `${kind} ${id.slice(0, 8)}`;
}

export function installLookupStub(): void {
  clearResolvedOptions();
  setLookupFetcher(async (url) => {
    const path = decodeURIComponent(new URL(url, "http://localhost").pathname);
    const role = /^\/api\/v1\/party\/(vendors|customers)\/([^/]+)$/.exec(path);
    if (role) return reply({ id: role[2], partyId: `${role[1] === "vendors" ? "vendor" : "customer"}:${role[2]}` });
    if (path === "/api/v1/product/setup/item-groups") return reply({ items: [] });
    for (const [pattern, kind] of KINDS) {
      const found = pattern.exec(path);
      if (!found) continue;
      const prefixed = /^\/api\/v1\/party\/parties\/(vendor|customer):/.exec(path);
      const label = prefixed ? (prefixed[1] === "vendor" ? "Vendor" : "Customer") : kind;
      const id = found[1];
      return reply({ id, name: lookupName(label, id), preferredName: lookupName(label, id) });
    }
    return reply({ items: [] });
  });
}

export function removeLookupStub(): void {
  setLookupFetcher(null);
  clearResolvedOptions();
}
