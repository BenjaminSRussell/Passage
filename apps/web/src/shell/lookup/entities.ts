import { apiFetch } from "../api/http";
export type LookupEntityKey =
  | "legalEntity"
  | "site"
  | "warehouse"
  | "vendor"
  | "customer"
  | "party"
  | "product"
  | "worker"
  | "currency"
  | "operatingUnit"
  | "itemGroup"
  | "vendorGroup";

export interface LookupOption {
  id: string;
  name: string;
  number?: string;
  unit?: string;
  itemGroup?: string;
  batchTracked?: boolean;
  serialTracked?: boolean;
  catchWeight?: boolean;
}

export interface LookupScope {
  legalEntityId?: string;
  siteId?: string;
}

export interface LookupDef {
  noun: string;
  cap?: number;
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

export type LookupFetcher = (url: string, init: RequestInit) => Promise<Response>;

const browserFetcher: LookupFetcher = (url, init) => apiFetch(url, init);
let lookupFetcher: LookupFetcher = browserFetcher;

export function setLookupFetcher(fetcher: LookupFetcher | null): void {
  lookupFetcher = fetcher ?? browserFetcher;
}

export class LookupError extends Error {
  constructor(readonly status: number) {
    super(`lookup failed with status ${status}`);
  }
}

async function getJson(url: string, signal?: AbortSignal): Promise<unknown> {
  const res = await lookupFetcher(url, {
    headers: { Accept: "application/json", [LOOKUP_HEADER]: "1" },
    signal,
  });
  if (!res.ok) {
    throw new LookupError(res.status);
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

const MAX_LIST_PAGES = 5;

/** Every row of a list route that has no text search, following nextCursor for a bounded number of pages. */
async function listRows(
  path: string,
  params: Record<string, string | undefined>,
  signal?: AbortSignal,
): Promise<Raw[]> {
  const rows: Raw[] = [];
  let cursor: string | undefined;
  for (let page = 0; page < MAX_LIST_PAGES; page++) {
    const body = await getJson(`${path}${query({ ...params, cursor })}`, signal);
    rows.push(...items(body));
    cursor = text((body ?? {}) as Raw, "nextCursor");
    if (!cursor) {
      break;
    }
  }
  return rows;
}

/** The options whose name or number contains the typed text, for lists the service cannot search. */
function narrow(all: LookupOption[], q: string): LookupOption[] {
  const needle = q.trim().toLowerCase();
  return all
    .filter(
      (entry) =>
        needle === "" ||
        entry.name.toLowerCase().includes(needle) ||
        (entry.number ?? "").toLowerCase().includes(needle),
    )
    .slice(0, PAGE_SIZE);
}

/** An operating unit reads "Assembly (ASM · cost center)": departments, cost centers and the rest share one list. */
function operatingUnitOption(row: Raw): LookupOption | null {
  const hit = option(row, ["name"], ["code"]);
  const type = text(row, "type")?.replace(/_/g, " ");
  return hit && type
    ? { ...hit, number: hit.number ? `${hit.number} · ${type}` : type }
    : hit;
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

async function entityCodes(
  idList: (string | undefined)[],
): Promise<Map<string, string>> {
  const unique = [...new Set(idList.filter((id): id is string => Boolean(id)))];
  const found = await Promise.all(
    unique.map((id) => resolveOption("legalEntity", id)),
  );
  const codes = new Map<string, string>();
  unique.forEach((id, at) => {
    const code = found[at]?.number;
    if (code) {
      codes.set(id, code);
    }
  });
  return codes;
}

function partyRole(
  noun: string,
  kind: "vendors" | "customers",
  createHref: string,
): LookupDef {
  return {
    noun,
    createHref,
    search: async (q, scope, signal) => {
      const rows = items(
        await getJson(
          `/api/v1/party/${kind}:search${query({ q, limit: String(PAGE_SIZE), legalEntityId: scope.legalEntityId })}`,
          signal,
        ),
      );
      const entities = scope.legalEntityId
        ? new Map<string, string>()
        : await entityCodes(rows.map((row) => text(row, "legalEntityId")));
      return present(
        rows.map((row) => {
          const hit = option(row, ["name"], ["accountNumber", "partyNumber"]);
          const code = entities.get(text(row, "legalEntityId") ?? "");
          return hit && code
            ? { ...hit, number: hit.number ? `${hit.number} · ${code}` : code }
            : hit;
        }),
      );
    },
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

let itemGroupNames: Promise<Map<string, string>> | null = null;

function loadItemGroupNames(): Promise<Map<string, string>> {
  itemGroupNames ??= getJson("/api/v1/product/setup/item-groups").then(
    (body) => new Map(items(body).map((row): [string, string] => [text(row, "id") ?? "", text(row, "name") ?? ""])),
    () => {
      itemGroupNames = null;
      return new Map<string, string>();
    },
  );
  return itemGroupNames;
}

const flag = (row: Raw, ...keys: string[]) => keys.some((key) => row[key] === true);

async function productOption(row: Raw): Promise<LookupOption | null> {
  const found = option(row, ["name"], ["productNumber"], ["id"]);
  if (!found) {
    return null;
  }
  const groupId = text(row, "itemGroupId");
  return {
    ...found,
    unit: text(row, "baseUom"),
    itemGroup: groupId ? (await loadItemGroupNames()).get(groupId) : undefined,
    batchTracked: flag(row, "batchTracked"),
    serialTracked: flag(row, "serialTracked"),
    catchWeight: flag(row, "catchWeight"),
  };
}

export function productHint(entry: LookupOption | null): string {
  if (!entry) {
    return "";
  }
  return [
    entry.itemGroup ? `Item group: ${entry.itemGroup}.` : "",
    entry.batchTracked ? "Batch tracked: the batch is recorded at receipt." : "",
    entry.serialTracked ? "Serial tracked: serial numbers are recorded at receipt." : "",
    entry.catchWeight ? "Catch weight: the actual weight is recorded at receipt." : "",
  ]
    .filter((part) => part !== "")
    .join(" ");
}

export interface PickedLine {
  unit: string;
  autoUnit?: string;
}

export function pickProductPatch(line: PickedLine, entry: LookupOption | null, keepUnit = false, defaultUnit = "EA") {
  const fillUnit = Boolean(entry?.unit) && !keepUnit && (line.unit.trim() === "" || line.unit === defaultUnit || line.unit === line.autoUnit);
  return { productHint: productHint(entry), ...(fillUnit ? { unit: entry?.unit ?? "", autoUnit: entry?.unit } : {}) };
}

const operatingUnitDef: LookupDef = {
  noun: "operating unit",
  search: async (q, _scope, signal) =>
    narrow(
      present(
        (
          await listRows(
            "/api/v1/org/operating-units",
            { status: "active", limit: "100" },
            signal,
          )
        ).map(operatingUnitOption),
      ),
      q,
    ),
  resolve: async (id, signal) =>
    operatingUnitOption(
      (await getJson(
        `/api/v1/org/operating-units/${encodeURIComponent(id)}`,
        signal,
      )) as Raw,
    ),
};

async function itemGroupOptions(signal?: AbortSignal): Promise<LookupOption[]> {
  return present(
    items(await getJson("/api/v1/product/setup/item-groups", signal)).map(
      (row) => option(row, ["name"], ["code"]),
    ),
  );
}

const itemGroupDef: LookupDef = {
  noun: "item group",
  createHref: "/product/setup/item-groups",
  search: async (q, _scope, signal) => narrow(await itemGroupOptions(signal), q),
  resolve: async (id, signal) =>
    (await itemGroupOptions(signal)).find((entry) => entry.id === id) ?? null,
};

/** The party service lists vendor groups under `vendorGroups`. */
function vendorGroupRows(body: unknown): Raw[] {
  return items({ items: (body as { vendorGroups?: unknown } | null)?.vendorGroups });
}

const vendorGroupDef: LookupDef = {
  noun: "vendor group",
  search: async (q, _scope, signal) =>
    narrow(
      present(
        vendorGroupRows(await getJson("/api/v1/party/vendor-groups", signal)).map(
          (row) => option(row, ["name"], ["code"]),
        ),
      ),
      q,
    ),
  resolve: async (id, signal) =>
    option(
      (await getJson(
        `/api/v1/party/vendor-groups/${encodeURIComponent(id)}`,
        signal,
      )) as Raw,
      ["name"],
      ["code"],
    ),
};

export const lookupDefs: Record<LookupEntityKey, LookupDef> = {
  operatingUnit: operatingUnitDef,
  itemGroup: itemGroupDef,
  vendorGroup: vendorGroupDef,
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
    cap: 10,
    createHref: "/product/items/new",
    search: async (q, _scope, signal) =>
      present(
        await Promise.all(
          items(
            await getJson(
              `/api/v1/product/products:search${query({ q })}`,
              signal,
            ),
          ).map(productOption),
        ),
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
  itemGroupNames = null;
}
