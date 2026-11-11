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
  | "vendorGroup"
  | "bankAccount"
  | "position"
  | "compensationPlan"
  | "calendar"
  | "resourceGroup"
  | "campaign"
  | "fund"
  | "user"
  | "load";

export interface LookupOption {
  id: string;
  name: string;
  number?: string;
  unit?: string;
  itemGroup?: string;
  batchTracked?: boolean;
  serialTracked?: boolean;
  catchWeight?: boolean;
  currency?: string;
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

/** One record of a service that answers GET {path}/{id} with the record itself. */
async function getOne(path: string, id: string, signal?: AbortSignal): Promise<Raw> {
  return (await getJson(`${path}/${encodeURIComponent(id)}`, signal)) as Raw;
}

/** A lookup over a list route with no text search: every page is read (bounded) and filtered in the browser. */
function listedEntity(
  noun: string,
  path: string,
  map: (row: Raw) => LookupOption | null,
  params: (scope: LookupScope) => Record<string, string | undefined> = () => ({}),
  keep: (row: Raw, scope: LookupScope) => boolean = () => true,
): LookupDef {
  return {
    noun,
    search: async (q, scope, signal) =>
      narrow(present((await listRows(path, params(scope), signal)).filter((row) => keep(row, scope)).map(map)), q),
    resolve: async (id, signal) => map(await getOne(path, id, signal)),
  };
}

/** A bank account reads "OPS-EUR (DE89 **** 3000 · EUR · DE01)": accounts carry a code, not a name. */
function bankAccountOption(row: Raw, entityCode?: string): LookupOption | null {
  const hit = option(row, ["accountCode"], []);
  if (!hit) {
    return null;
  }
  const currency = text(row, "currency");
  const number = [text(row, "ibanMasked"), currency, entityCode].filter(Boolean).join(" · ");
  return { ...hit, number: number || undefined, currency };
}

/** Bank accounts are listed per legal entity; with no legal entity in scope every entity's accounts are read. */
const bankAccountDef: LookupDef = {
  noun: "bank account",
  createHref: "/treasury/bank-accounts/new",
  search: async (q, scope, signal) => {
    const entities = scope.legalEntityId
      ? [{ id: scope.legalEntityId, code: undefined as string | undefined }]
      : present(items(await getJson(`/api/v1/org/legal-entities${query({ limit: String(PAGE_SIZE) })}`, signal)).map((row) => option(row, ["name"], ["code"]))).map(
          (entry) => ({ id: entry.id, code: entry.number }),
        );
    const lists = await Promise.all(
      entities.map(async (entity) =>
        items(await getJson(`/api/v1/treasury/bank-accounts${query({ legalEntityId: entity.id })}`, signal)).map((row) => bankAccountOption(row, entity.code)),
      ),
    );
    return narrow(present(lists.flat()), q);
  },
  resolve: async (id, signal) => bankAccountOption(await getOne("/api/v1/treasury/bank-accounts", id, signal)),
};

/** A position has a number and a job code but no name of its own. */
const positionDef = listedEntity(
  "position",
  "/api/v1/hr/positions",
  (row) => option(row, ["positionNumber"], ["jobCode"]),
  () => ({ limit: "100" }),
  (row, scope) => !scope.legalEntityId || text(row, "legalEntityId") === scope.legalEntityId,
);

async function compensationPlanOptions(signal?: AbortSignal): Promise<LookupOption[]> {
  return present(
    (await listRows("/api/v1/hr/compensation/plans", { limit: "100" }, signal)).map((row) => {
      const hit = option(row, ["name"], ["code"]);
      return hit ? { ...hit, currency: text(row, "currency") } : null;
    }),
  );
}

/** Compensation plans have no single-plan route, so a plan's name is found in the list. */
const compensationPlanDef: LookupDef = {
  noun: "compensation plan",
  createHref: "/hr/compensation/plans/new",
  search: async (q, _scope, signal) => narrow(await compensationPlanOptions(signal), q),
  resolve: async (id, signal) => (await compensationPlanOptions(signal)).find((entry) => entry.id === id) ?? null,
};

const codeAndName = (row: Raw) => option(row, ["name"], ["code"]);

/** A load has no name: it reads by where it goes and when it is picked up, with its mode and status. */
function loadOption(row: Raw): LookupOption | null {
  const id = text(row, "id");
  const region = text(row, "shipToRegion");
  const pickup = text(row, "pickupDate");
  if (!id || !region) {
    return null;
  }
  const number = [text(row, "mode"), text(row, "status")?.replace(/_/g, " ").toLowerCase()].filter(Boolean).join(" · ");
  return { id, name: pickup ? `${region}, pickup ${pickup}` : region, number: number || undefined };
}

function userOption(row: Raw): LookupOption | null {
  return option(row, ["displayName", "email"], ["email"], ["userId", "id"]);
}

export const lookupDefs: Record<LookupEntityKey, LookupDef> = {
  bankAccount: bankAccountDef,
  position: positionDef,
  compensationPlan: compensationPlanDef,
  calendar: listedEntity("work calendar", "/api/v1/engineering/calendars", codeAndName, () => ({ limit: "100" })),
  resourceGroup: listedEntity("resource group", "/api/v1/engineering/resource-groups", codeAndName, (scope) => ({ limit: "100", siteId: scope.siteId })),
  campaign: listedEntity(
    "campaign",
    "/api/v1/pricing/campaigns",
    (row) => {
      const hit = option(row, ["name"], []);
      const from = text(row, "validFrom");
      const to = text(row, "validTo");
      return hit && from && to ? { ...hit, number: `${from} to ${to}` } : hit;
    },
    (scope) => ({ legalEntityId: scope.legalEntityId }),
  ),
  fund: listedEntity(
    "fund",
    "/api/v1/pricing/funds",
    (row) => {
      const hit = option(row, ["name"], ["type"]);
      return hit ? { ...hit, currency: text(row, "currency") } : null;
    },
    (scope) => ({ legalEntityId: scope.legalEntityId }),
  ),
  load: listedEntity("load", "/api/v1/transport/loads", loadOption, () => ({}), (row, scope) => !scope.siteId || text(row, "shipFromSiteId") === scope.siteId),
  user: {
    noun: "user",
    search: async (q, scope, signal) =>
      present(
        items(
          await getJson(`/api/v1/identity/users${query({ q, status: "active", legalEntityId: scope.legalEntityId, limit: String(PAGE_SIZE) })}`, signal),
        ).map(userOption),
      ),
    // user-names needs only the read-a-name duty every role can hold; the full user record is for administrators.
    resolve: async (id, signal) => {
      const found = items(await getJson(`/api/v1/identity/user-names${query({ ids: id })}`, signal))[0];
      return found ? userOption(found) : null;
    },
  },
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
/** Lookups the server answered with 403: the caller may not read them, so asking again can only fail again. */
const denied = new Set<string>();
/** Keys whose lookup failed for a reason other than "missing" (an outage); the caller must not treat them as gone. */
const unreachable = new Set<string>();

export function resolveOption(
  entity: LookupEntityKey,
  id: string,
): Promise<LookupOption | null> {
  const key = `${entity}:${id}`;
  let pending = resolved.get(key);
  if (!pending) {
    pending = lookupDefs[entity].resolve(id).then(
      (entry) => {
        unreachable.delete(key);
        return entry;
      },
      (error: unknown) => {
        if (error instanceof LookupError && error.status === 403) {
          denied.add(key);
        }
        if (!(error instanceof LookupError && (error.status === 404 || error.status === 410))) {
          unreachable.add(key);
        }
        return null;
      },
    );
    resolved.set(key, pending);
    void pending.then((entry) => {
      // A denied lookup stays cached, so a list of hits does not ask again for every row of a legal entity it cannot read.
      if (entry === null && !denied.has(key)) {
        resolved.delete(key);
      }
    });
  }
  return pending;
}

/**
 * Whether a record is gone: true only when the server said it does not exist (404, 410, or an answer with no usable name).
 * A 500, a network failure or a denied read says nothing about the record, so callers that would drop a stored choice
 * (the working context) keep it.
 */
export async function isOptionMissing(
  entity: LookupEntityKey,
  id: string,
): Promise<boolean> {
  const entry = await resolveOption(entity, id);
  return entry === null && !unreachable.has(`${entity}:${id}`);
}

export function rememberOption(
  entity: LookupEntityKey,
  entry: LookupOption,
): void {
  resolved.set(`${entity}:${entry.id}`, Promise.resolve(entry));
}

export function clearResolvedOptions(): void {
  resolved.clear();
  denied.clear();
  unreachable.clear();
  itemGroupNames = null;
}
