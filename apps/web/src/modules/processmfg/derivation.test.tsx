import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import { findAlert, findTable } from "../../test-utils";
import { DERIVE_DUTY } from "./DerivationPages";
import { INHERITANCE_RULE_DUTY } from "./InheritanceRulePages";
import processmfg from "./module";

const ITEM = "1a000000-0000-4000-8000-000000000001";
const RULE = "6d000000-0000-4000-8000-0000000000e1";
const RECV = "5b000000-0000-4000-8000-0000000000b1";
const S1 = "5b000000-0000-4000-8000-0000000000a1";
const S2 = "5b000000-0000-4000-8000-0000000000a2";

type Reply = { status: number; body: unknown };
let handler: (url: URL, init: RequestInit) => Reply | undefined;
let calls: { method: string; url: URL; init: RequestInit; body: unknown }[];

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, init, body: init.body ? JSON.parse(String(init.body)) : undefined });
      const reply = handler(url, init) ?? { status: 404, body: { code: "TEST-404", detail: `no fake for ${url.pathname}` } };
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});

afterEach(() => vi.unstubAllGlobals());

function renderAt(path: string, duties: string[] = [INHERITANCE_RULE_DUTY, DERIVE_DUTY, "production.batch-order.read"]) {
  return render(
    <PrincipalProvider duties={duties}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          {processmfg.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
        </Routes>
      </MemoryRouter>
    </PrincipalProvider>,
  );
}

const rule = (over: Record<string, unknown> = {}) => ({
  id: RULE,
  legalEntityId: "11111111-0000-4000-8000-000000000001",
  itemId: ITEM,
  attribute: "potency",
  aggregation: "weighted-average",
  decimals: 2,
  version: 1,
  createdBy: "u",
  createdAt: "2026-10-20T08:00:00Z",
  updatedAt: "2026-10-20T08:00:00Z",
  ...over,
});

const batch = (over: Record<string, unknown> = {}) => ({
  id: RECV,
  legalEntityId: "11111111-0000-4000-8000-000000000001",
  itemId: ITEM,
  batchNumber: "R-1",
  disposition: "available",
  version: 1,
  createdBy: "u",
  createdAt: "2026-10-20T08:00:00Z",
  updatedAt: "2026-10-20T08:00:00Z",
  ...over,
});

function fill(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

describe("inheritance rule list (slice 16)", () => {
  it("lists the rules, filtered by item and active status", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/production/inheritance-rules"
        ? { status: 200, body: { items: [rule(), rule({ id: "x", attribute: "grade", aggregation: "copy", decimals: undefined, retiredAt: "2026-10-21T08:00:00Z" })] } }
        : undefined;
    const { container } = renderAt("/processmfg/inheritance-rules");
    const table = await findTable({ name: "Inheritance rules" });
    expect(within(table).getByText("Weighted average by quantity")).toBeInTheDocument();
    expect(within(table).getByText("Retired")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Define rule" })).toHaveAttribute("href", "/processmfg/inheritance-rules/new");
    fill("Item", ITEM);
    fireEvent.click(screen.getByLabelText(/Active rules only/));
    await waitFor(() => expect(calls.some((c) => c.url.searchParams.get("itemId") === ITEM && c.url.searchParams.get("active") === "true")).toBe(true));
    await waitFor(() => expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]));
  });

  it("shows the empty state, hides the define link without the duty and ignores a typed item that is not an id", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    renderAt("/processmfg/inheritance-rules", ["production.batch-order.read"]);
    expect(await screen.findByText("No inheritance rules yet")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Define rule" })).toBeNull();
    fill("Item", "nope");
    expect(screen.queryByText("Enter the item id as a UUID.")).toBeNull();
  });

  it("shows a load failure with a retry", async () => {
    handler = () => ({ status: 500, body: { code: "COM-0500", detail: "database unavailable" } });
    renderAt("/processmfg/inheritance-rules");
    expect(await findAlert()).toHaveTextContent("database unavailable");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

describe("define an inheritance rule (slice 16)", () => {
  it("sends the rule under one key and opens it", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return { status: 201, body: rule() };
      if (url.pathname.endsWith(`/${RULE}`)) return { status: 200, body: rule() };
      return undefined;
    };
    renderAt("/processmfg/inheritance-rules/new");
    fill("Item", ITEM);
    fill("Attribute", "potency");
    fill("Derived by", "weighted-average");
    fill("Decimals", "2");
    fireEvent.click(screen.getByRole("button", { name: "Define rule" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.url.pathname).toBe("/api/v1/production/inheritance-rules");
    expect(post.body).toEqual({ itemId: ITEM, attribute: "potency", aggregation: "weighted-average", decimals: 2 });
    expect(new Headers(post.init.headers).get("Idempotency-Key")).toBeTruthy();
    expect(await screen.findByRole("button", { name: "Retire rule" })).toBeInTheDocument();
  });

  it("sends a fixed value of the chosen kind and checks the form before calling", async () => {
    handler = () => ({ status: 201, body: rule({ aggregation: "fixed" }) });
    renderAt("/processmfg/inheritance-rules/new");
    fireEvent.click(screen.getByRole("button", { name: "Define rule" }));
    expect(await screen.findByText("Enter the item id as a UUID.")).toBeInTheDocument();
    expect(screen.getByText("Enter the attribute, at most 40 characters.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
    fill("Item", ITEM);
    fill("Attribute", "expiry");
    fill("Derived by", "fixed");
    fill("Fixed value is", "date");
    fill("Fixed value", "soon");
    fireEvent.click(screen.getByRole("button", { name: "Define rule" }));
    expect(await screen.findByText("Enter a date as yyyy-mm-dd.")).toBeInTheDocument();
    fill("Fixed value", "2027-05-01");
    fireEvent.click(screen.getByRole("button", { name: "Define rule" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    expect(calls.find((c) => c.method === "POST")!.body).toEqual({ itemId: ITEM, attribute: "expiry", aggregation: "fixed", fixed: { date: "2027-05-01" } });
  });

  it("shows the service's refusal and refuses the page without the duty", async () => {
    handler = () => ({ status: 409, body: { code: "PMF-0127", detail: "item already has an active rule" } });
    const { unmount } = renderAt("/processmfg/inheritance-rules/new");
    fill("Item", ITEM);
    fill("Attribute", "potency");
    fireEvent.click(screen.getByRole("button", { name: "Define rule" }));
    expect(await findAlert()).toHaveTextContent("PMF-0127");
    unmount();
    renderAt("/processmfg/inheritance-rules/new", ["production.batch-order.read"]);
    expect(screen.getByText(/needs the inheritance rule duty/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Define rule" })).toBeNull();
  });
});

describe("inheritance rule detail (slice 16)", () => {
  it("retires under the version read and offers a reload on a stale version", async () => {
    let retired = false;
    handler = (url, init) => {
      if (url.pathname.endsWith(":retire")) return retired ? { status: 200, body: rule({ retiredAt: "2026-10-22T08:00:00Z", retiredBy: "u", version: 2 }) } : { status: 409, body: { code: "PMF-0104", detail: "rule is at version 2" } };
      if (init.method === undefined || init.method === "GET") return { status: 200, body: retired ? rule({ retiredAt: "2026-10-22T08:00:00Z", retiredBy: "u", version: 2 }) : rule() };
      return undefined;
    };
    renderAt(`/processmfg/inheritance-rules/${RULE}`);
    fireEvent.click(await screen.findByRole("button", { name: "Retire rule" }));
    expect(await findAlert()).toHaveTextContent("PMF-0104");
    expect(calls.find((c) => c.method === "POST")!.body).toEqual({ version: 1 });
    retired = true;
    fireEvent.click(screen.getByRole("button", { name: "Retire rule" }));
    expect(await screen.findByText("This rule is retired")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Retire rule" })).toBeNull();
  });

  it("shows a load failure and a loading state", async () => {
    handler = () => ({ status: 404, body: { code: "PMF-0126", detail: "inheritance rule not found" } });
    renderAt(`/processmfg/inheritance-rules/${RULE}`);
    expect(screen.getByText("Loading the rule…")).toBeInTheDocument();
    expect(await findAlert()).toHaveTextContent("PMF-0126");
  });
});

describe("derive batch attributes (slice 16)", () => {
  const result = {
    batch: batch({ version: 2, expiryDate: "2027-03-01" }),
    derived: [
      { ruleId: RULE, attribute: "potency", aggregation: "weighted-average", recorded: true, value: "43", missing: [] },
      { ruleId: "r2", attribute: "grade", aggregation: "copy", recorded: false, missing: [S2] },
    ],
    warnings: ["PMF-0007: 1 sender batch(es) have no value for grade"],
  };

  it("reads the receiver version, derives under one key and shows the values and warnings", async () => {
    handler = (url, init) => {
      if (url.pathname === `/api/v1/production/batches/${RECV}`) return { status: 200, body: batch() };
      if (init.method === "POST") return { status: 200, body: result };
      return undefined;
    };
    const { container } = renderAt("/processmfg/derive-stored");
    fill("Receiver batch id", RECV);
    fill("Sender 1 batch id", S1);
    fill("Sender 1 quantity", "200");
    fireEvent.click(screen.getByRole("button", { name: "Add sender" }));
    fill("Sender 2 batch id", S2);
    fill("Sender 2 quantity", "300");
    fireEvent.click(screen.getByRole("button", { name: "Derive attributes" }));
    const table = await findTable({ name: "Derived attributes" });
    expect(within(table).getByText("43")).toBeInTheDocument();
    expect(within(table).getByText("Left as it was")).toBeInTheDocument();
    expect(screen.getByText(/PMF-0007/)).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.url.pathname).toBe(`/api/v1/production/batches/${RECV}:derive`);
    expect(post.body).toEqual({ version: 1, senders: [{ batchId: S1, qty: "200" }, { batchId: S2, qty: "300" }] });
    expect(new Headers(post.init.headers).get("Idempotency-Key")).toBeTruthy();
    await waitFor(() => expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]));
  });

  it("checks the form first, shows a refusal and needs the duty", async () => {
    handler = (url, init) => {
      if (url.pathname === `/api/v1/production/batches/${RECV}`) return { status: 200, body: batch() };
      if (init.method === "POST") return { status: 422, body: { code: "PMF-0129", detail: "item has no active inheritance rule" } };
      return undefined;
    };
    const { unmount } = renderAt("/processmfg/derive-stored");
    fireEvent.click(screen.getByRole("button", { name: "Derive attributes" }));
    expect(await screen.findByText("Enter the receiver batch id as a UUID.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
    fill("Receiver batch id", RECV);
    fill("Sender 1 batch id", RECV);
    fill("Sender 1 quantity", "1");
    fireEvent.click(screen.getByRole("button", { name: "Derive attributes" }));
    expect(await screen.findByText("A batch cannot be its own sender.")).toBeInTheDocument();
    fill("Sender 1 batch id", S1);
    fireEvent.click(screen.getByRole("button", { name: "Derive attributes" }));
    expect(await findAlert()).toHaveTextContent("PMF-0129");
    unmount();
    renderAt("/processmfg/derive-stored", ["production.batch-order.read"]);
    expect(screen.getByText(/needs the batch attribute duty/)).toBeInTheDocument();
  });
});

describe("derivation monitor (slice 16)", () => {
  const row = (over: Record<string, unknown> = {}) => ({
    id: "d1",
    legalEntityId: "11111111-0000-4000-8000-000000000001",
    receiverBatchId: RECV,
    batchNumber: "R-1",
    receiverVersion: 2,
    ruleId: RULE,
    attribute: "potency",
    aggregation: "weighted-average",
    recorded: true,
    value: "43",
    senders: [{ batchId: S1, batchNumber: "S-A", qty: "200" }],
    missing: [],
    runBy: "planner-1",
    runAt: "2026-10-22T08:00:00Z",
    ...over,
  });

  it("lists the log and filters by batch and by senders without a value", async () => {
    handler = (url) => (url.pathname === "/api/v1/production/batch-derivations" ? { status: 200, body: { items: [row(), row({ id: "d2", recorded: false, value: undefined, valueBefore: "41.5", missing: [S2] })] } } : undefined);
    renderAt("/processmfg/derivations");
    const table = await findTable({ name: "Derivations" });
    expect(within(table).getByText("43")).toBeInTheDocument();
    expect(within(table).getByText("Left as it was")).toBeInTheDocument();
    expect(within(table).getByText("41.5")).toBeInTheDocument();
    expect(within(table).getByRole("columnheader", { name: "Before" })).toBeInTheDocument();
    expect(within(table).getAllByRole("link", { name: "R-1" })).toHaveLength(2);
    fill("Receiver batch id", RECV);
    fireEvent.click(screen.getByLabelText(/Only derivations with senders lacking a value/));
    await waitFor(() => expect(calls.some((c) => c.url.searchParams.get("batchId") === RECV && c.url.searchParams.get("withMissing") === "true")).toBe(true));
  });

  it("shows the empty state, a typed item that is not an id and a load failure", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    const { unmount } = renderAt("/processmfg/derivations");
    expect(await screen.findByText("Nothing derived yet")).toBeInTheDocument();
    fill("Item", "nope");
    expect(screen.queryByText("Enter the item id as a UUID.")).toBeNull();
    unmount();
    handler = () => ({ status: 500, body: { code: "COM-0500", detail: "database unavailable" } });
    renderAt("/processmfg/derivations");
    expect(await findAlert()).toHaveTextContent("database unavailable");
  });
});
