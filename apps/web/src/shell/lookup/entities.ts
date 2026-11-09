export type LookupEntityKey =
  | "legalEntity"
  | "site"
  | "warehouse"
  | "vendor"
  | "customer"
  | "party"
  | "product"
  | "worker"
  | "currency";

export interface LookupOption {
  id: string;
  name: string;
  number?: string;
}

export interface LookupScope {
  legalEntityId?: string;
  siteId?: string;
}

export interface LookupDef {
  noun: string;
  createHref?: string;
  search(
    query: string,
    scope: LookupScope,
    signal?: AbortSignal,
  ): Promise<LookupOption[]>;
  resolve(id: string, signal?: AbortSignal): Promise<LookupOption | null>;
}

export const PAGE_SIZE = 20;
export const LOOKUP_HEADER = "X-Passage-Lookup";

type Raw = Record<string, unknown>;

async function getJson(url: string, signal?: AbortSignal): Promise<unknown> {
  const res = await fetch(url, {
    headers: { Accept: "application/json", [LOOKUP_HEADER]: "1" },
    signal,
  });
  if (!res.ok) {
    throw new Error(`lookup failed with status ${res.status}`);
  }
  return res.json();
}

function items(body: unknown): Raw[] {
  const list = (body as { items?: unknown } | null)?.items;
  return Array.isArray(list)
    ? (list.filter((row) => typeof row === "object" && row !== null) as Raw[])
    : [];
}

function text(row: Raw, ...keys: string[]): string | undefined {
  for (const key of keys) {
    const value = row[key];
    if (typeof value === "string" && value !== "") {
      return value;
    }
  }
  return undefined;
}

function query(params: Record<string, string | undefined>): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value) {
      search.set(key, value);
    }
  }
  const out = search.toString();
  return out === "" ? "" : `?${out}`;
}

function option(
  row: Raw,
  nameKeys: string[],
  numberKeys: string[],
  idKeys = ["id", "ID"],
): LookupOption | null {
  const id = text(row, ...idKeys);
  const name = text(row, ...nameKeys);
  if (!id || !name) {
    return null;
  }
  return { id, name, number: text(row, ...numberKeys) };
}

function present(list: (LookupOption | null)[]): LookupOption[] {
  return list.filter((entry): entry is LookupOption => entry !== null);
}

function orgEntity(
  noun: string,
  path: string,
  scoped: (scope: LookupScope) => Record<string, string | undefined>,
): LookupDef {
  const map = (row: Raw) => option(row, ["name"], ["code"]);
  return {
    noun,
    search: async (q, scope, signal) =>
      present(
        items(
          await getJson(
            `/api/v1/org/${path}${query({ q, limit: String(PAGE_SIZE), ...scoped(scope) })}`,
            signal,
          ),
        ).map(map),
      ),
    resolve: async (id, signal) =>
      map(
        (await getJson(
          `/api/v1/org/${path}/${encodeURIComponent(id)}`,
          signal,
        )) as Raw,
      ),
  };
}

function partyRole(
  noun: string,
  kind: "vendors" | "customers",
  createHref: string,
): LookupDef {
  return {
    noun,
    createHref,
    search: async (q, _scope, signal) =>
      present(
        items(
          await getJson(
            `/api/v1/party/${kind}:search${query({ q, limit: String(PAGE_SIZE) })}`,
            signal,
          ),
        ).map((row) => option(row, ["name"], ["accountNumber", "partyNumber"])),
      ),
    resolve: async (id, signal) => {
      const role = (await getJson(
        `/api/v1/party/${kind}/${encodeURIComponent(id)}`,
        signal,
      )) as Raw;
      const partyId = text(role, "partyId");
      if (!partyId) {
        return null;
      }
      const party = (await getJson(
        `/api/v1/party/parties/${encodeURIComponent(partyId)}`,
        signal,
      )) as Raw;
      const name = text(party, "name");
      return name
        ? {
            id,
            name,
            number: text(role, "accountNumber") ?? text(party, "partyNumber"),
          }
        : null;
    },
  };
}

const productOption = (row: Raw) =>
  option(row, ["name", "Name"], ["productNumber", "ProductNumber"]);

export const lookupDefs: Record<LookupEntityKey, LookupDef> = {
  legalEntity: orgEntity("legal entity", "legal-entities", () => ({})),
  site: orgEntity("site", "sites", (scope) => ({
    legalEntityId: scope.legalEntityId,
  })),
  warehouse: orgEntity("warehouse", "warehouses", (scope) => ({
    legalEntityId: scope.legalEntityId,
    siteId: scope.siteId,
  })),
  vendor: partyRole("vendor", "vendors", "/party/new"),
  customer: partyRole("customer", "customers", "/party/new"),
  party: {
    noun: "party",
    createHref: "/party/new",
    search: async (q, _scope, signal) =>
      present(
        items(
          await getJson(
            `/api/v1/party/parties:search${query({ q, limit: String(PAGE_SIZE) })}`,
            signal,
          ),
        ).map((row) => option(row, ["name"], ["partyNumber"])),
      ),
    resolve: async (id, signal) =>
      option(
        (await getJson(
          `/api/v1/party/parties/${encodeURIComponent(id)}`,
          signal,
        )) as Raw,
        ["name"],
        ["partyNumber"],
      ),
  },
  product: {
    noun: "product",
    search: async (q, _scope, signal) =>
      present(
        items(
          await getJson(
            `/api/v1/product/products:search${query({ q })}`,
            signal,
          ),
        ).map(productOption),
      ),
    resolve: async (id, signal) =>
      productOption(
        (await getJson(
          `/api/v1/product/products/${encodeURIComponent(id)}`,
          signal,
        )) as Raw,
      ),
  },
  worker: {
    noun: "worker",
    search: async (q, _scope, signal) =>
      present(
        items(
          await getJson(
            `/api/v1/hr/workers${query({ q, limit: String(PAGE_SIZE) })}`,
            signal,
          ),
        ).map((row) =>
          option(row, ["preferredName", "legalName"], ["personnelNumber"]),
        ),
      ),
    resolve: async (id, signal) =>
      option(
        (await getJson(
          `/api/v1/hr/workers/${encodeURIComponent(id)}`,
          signal,
        )) as Raw,
        ["preferredName", "legalName"],
        ["personnelNumber"],
      ),
  },
  currency: {
    noun: "currency",
    search: async (q, _scope, signal) => {
      const needle = q.trim().toLowerCase();
      const all = present(
        items(
          await getJson("/api/v1/org/currencies?status=active", signal),
        ).map((row) => option(row, ["name"], ["code"], ["code"])),
      );
      return all
        .filter(
          (entry) =>
            needle === "" ||
            entry.name.toLowerCase().includes(needle) ||
            (entry.number ?? "").toLowerCase().includes(needle),
        )
        .slice(0, PAGE_SIZE);
    },
    resolve: async (id, signal) => {
      const code = id.toUpperCase();
      const match = items(await getJson("/api/v1/org/currencies", signal)).find(
        (row) => text(row, "code") === code,
      );
      return match ? option(match, ["name"], ["code"], ["code"]) : null;
    },
  },
};

export function formatOption(entry: LookupOption): string {
  return entry.number ? `${entry.name} (${entry.number})` : entry.name;
}

const resolved = new Map<string, Promise<LookupOption | null>>();

export function resolveOption(
  entity: LookupEntityKey,
  id: string,
): Promise<LookupOption | null> {
  const key = `${entity}:${id}`;
  let pending = resolved.get(key);
  if (!pending) {
    pending = lookupDefs[entity].resolve(id).catch(() => null);
    resolved.set(key, pending);
    void pending.then((entry) => {
      if (entry === null) {
        resolved.delete(key);
      }
    });
  }
  return pending;
}

export function rememberOption(
  entity: LookupEntityKey,
  entry: LookupOption,
): void {
  resolved.set(`${entity}:${entry.id}`, Promise.resolve(entry));
}

export function clearResolvedOptions(): void {
  resolved.clear();
}
