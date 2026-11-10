import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { clearResolvedOptions, setLookupFetcher } from "../../shell/lookup/entities";
import { foreignCurrencies, validateMap } from "./GroupDetailPage";
import { validateGroup } from "./GroupsPage";
import consolidation from "./module";
import { unitIds } from "./TrialBalancePage";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, method: string, body: unknown) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

const GROUP = "g-1";
const EUR_UNIT = { id: "u-2", legalEntityId: "le-2", ledgerId: "CRP-DE-GL", currency: "EUR", currentPercent: "80.0000", ownership: [{ id: "h-1", effectiveFrom: "2022-04-01", percent: "80.0000" }] };
const USD_UNIT = { id: "u-1", legalEntityId: "le-1", ledgerId: "CRP-US-GL", currency: "USD", currentPercent: "100.0000", ownership: [] };
const groupDetail = { id: GROUP, code: "CRP", name: "Cedar Ridge Pumps group", currency: "USD", createdBy: "dev", createdAt: "2026-01-01T00:00:00Z", units: [USD_UNIT, EUR_UNIT] };

let handler: Handler;
let calls: { method: string; url: URL; body: unknown; headers: Headers }[];

beforeEach(() => {
  clearResolvedOptions();
  setLookupFetcher(async (url) => {
    const id = decodeURIComponent(url.split("/").pop() ?? "");
    const names: Record<string, string> = { "le-1": "Cedar Ridge Pumps, Inc.", "le-2": "Cedar Ridge Pumps Europe GmbH" };
    return new Response(JSON.stringify({ id, name: names[id] ?? id, code: id.toUpperCase() }), { status: 200, headers: { "Content-Type": "application/json" } });
  });
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      const body = init.body ? JSON.parse(String(init.body)) : undefined;
      const method = init.method ?? "GET";
      calls.push({ method, url, body, headers: new Headers(init.headers) });
      const reply = handler(url, method, body) ?? { status: 404, body: { code: "TEST-404", detail: `no fake for ${url.pathname}` } };
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});
afterEach(() => {
  vi.unstubAllGlobals();
  setLookupFetcher(null);
  clearResolvedOptions();
});

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {consolidation.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

describe("group form checks", () => {
  it("accepts a valid group and flags each bad field", () => {
    expect(validateGroup({ code: "CRP", name: "Cedar Ridge", currency: "usd" }).request).toEqual({ code: "CRP", name: "Cedar Ridge", currency: "USD" });
    expect(validateGroup({ code: "no good", name: "", currency: "US" }).errors).toEqual({
      code: "Enter a short code of letters, digits and . _ : -.",
      name: "Enter a name of up to 200 characters.",
      currency: "Enter a three-letter currency code such as USD.",
    });
  });

  it("rejects a main account mapped twice", () => {
    const rows = [
      { mainAccount: "1000", consolidationAccount: "CASH", accountType: "balance_sheet" },
      { mainAccount: "1000", consolidationAccount: "BANK", accountType: "balance_sheet" },
    ];
    expect(validateMap(rows).errors["accounts[1].mainAccount"]).toBe("This account is mapped twice.");
  });

  it("asks for rates only for foreign currencies", () => {
    expect(foreignCurrencies({ ...groupDetail, units: [USD_UNIT, EUR_UNIT, { ...EUR_UNIT, id: "u-3" }] })).toEqual(["EUR"]);
  });
});

describe("groups page", () => {
  it("lists the groups and creates one with an Idempotency-Key", async () => {
    handler = (url, method) => {
      if (url.pathname === "/api/v1/consol/groups" && method === "GET") return { status: 200, body: { items: [{ id: GROUP, code: "CRP", name: "Cedar Ridge Pumps group", currency: "USD", unitCount: 3, createdBy: "dev", createdAt: "2026-01-01T00:00:00Z" }] } };
      if (url.pathname === "/api/v1/consol/groups" && method === "POST") return { status: 201, body: { ...groupDetail, units: undefined } };
      if (url.pathname === `/api/v1/consol/groups/${GROUP}`) return { status: 200, body: groupDetail };
      return undefined;
    };
    const { container } = renderAt("/consolidation/groups");
    expect(await screen.findByText("Cedar Ridge Pumps group")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "NEW" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "New group" } });
    fireEvent.click(screen.getByRole("button", { name: "Create group" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    const post = calls.find((c) => c.method === "POST");
    expect(post?.body).toEqual({ code: "NEW", name: "New group", currency: "USD" });
    expect(post?.headers.get("Idempotency-Key")).toBeTruthy();
  });

  it("shows the service's reason when the code is taken", async () => {
    handler = (_url, method) => {
      if (method === "GET") return { status: 200, body: { items: [] } };
      return { status: 409, body: { code: "CNS-0925", detail: "a consolidation group with code CRP already exists" } };
    };
    renderAt("/consolidation/groups");
    await screen.findByText("No groups yet");
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "CRP" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Dup" } });
    fireEvent.click(screen.getByRole("button", { name: "Create group" }));
    expect(await screen.findByText("The group was not created")).toBeInTheDocument();
  });
});

describe("group page", () => {
  const detailHandler: Handler = (url, method, body) => {
    const p = url.pathname;
    if (p === `/api/v1/consol/groups/${GROUP}` && method === "GET") return { status: 200, body: groupDetail };
    if (p === `/api/v1/consol/groups/${GROUP}/account-map` && method === "GET") return { status: 200, body: { accounts: [{ mainAccount: "1000", consolidationAccount: "CASH", accountType: "balance_sheet" }] } };
    if (p === `/api/v1/consol/groups/${GROUP}/account-map` && method === "PUT") return { status: 200, body };
    if (p === `/api/v1/consol/groups/${GROUP}/collections` && method === "GET") return { status: 200, body: { items: [{ id: "c-1", groupId: GROUP, period: "2026-10", version: 2, currency: "USD", createdBy: "dev", createdAt: "2026-11-02T10:00:00Z" }] } };
    if (p === `/api/v1/consol/groups/${GROUP}/collections` && method === "POST") return { status: 201, body: { id: "c-9", groupId: GROUP, period: "2026-11", version: 1, currency: "USD", createdBy: "dev", createdAt: "2026-11-10T10:00:00Z" } };
    return undefined;
  };

  it("shows units with entity names, ownership history and the collections", async () => {
    handler = detailHandler;
    const { container } = renderAt(`/consolidation/groups/${GROUP}`);
    expect(await screen.findByRole("heading", { level: 2, name: "Cedar Ridge Pumps group" })).toBeInTheDocument();
    expect((await screen.findAllByText(/Cedar Ridge Pumps Europe GmbH/)).length).toBeGreaterThan(0);
    expect(screen.getByText("80.00 % from 2022-04-01")).toBeInTheDocument();
    expect(screen.getByText("2026-10")).toBeInTheDocument();
    expect(screen.queryByText("le-2")).not.toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("saves the mapping and asks for the rate of the foreign unit when collecting", async () => {
    handler = detailHandler;
    renderAt(`/consolidation/groups/${GROUP}`);
    await screen.findByRole("heading", { level: 2, name: "Cedar Ridge Pumps group" });
    fireEvent.click(screen.getByRole("button", { name: "Save mapping" }));
    await screen.findByText("Mapping saved");

    fireEvent.change(screen.getByLabelText("Period"), { target: { value: "2026-11" } });
    fireEvent.click(screen.getByRole("button", { name: "Collect balances" }));
    expect(await screen.findAllByText("Enter a rate above 0.")).toHaveLength(2);

    fireEvent.change(screen.getByLabelText("Average rate"), { target: { value: "1.084" } });
    fireEvent.change(screen.getByLabelText("Closing rate"), { target: { value: "1.095" } });
    fireEvent.click(screen.getByRole("button", { name: "Collect balances" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST" && c.url.pathname.endsWith("/collections"))).toBe(true));
    const post = calls.find((c) => c.method === "POST" && c.url.pathname.endsWith("/collections"));
    expect(post?.body).toEqual({ period: "2026-11", rates: [{ currency: "EUR", average: "1.084", closing: "1.095" }] });
    expect(post?.headers.get("Idempotency-Key")).toBeTruthy();
  });

  it("reports an ownership that would exceed 100 percent", async () => {
    handler = (url, method, body) => {
      if (url.pathname.endsWith("/ownership") && method === "POST") return { status: 422, body: { code: "CNS-0904", detail: "ownership totals 110.00% on 2026-02-01" } };
      return detailHandler(url, method, body);
    };
    renderAt(`/consolidation/groups/${GROUP}`);
    await screen.findByRole("heading", { level: 2, name: "Cedar Ridge Pumps group" });
    fireEvent.change(screen.getByLabelText("Effective from"), { target: { value: "2026-02-01" } });
    fireEvent.change(screen.getByLabelText("Share held (%)"), { target: { value: "30" } });
    fireEvent.click(screen.getByRole("button", { name: "Record ownership" }));
    expect(await screen.findByText("The ownership was not saved")).toBeInTheDocument();
  });
});

describe("trial balance page", () => {
  const tb = {
    id: "c-1",
    groupId: GROUP,
    period: "2026-10",
    version: 2,
    currency: "USD",
    createdBy: "dev",
    createdAt: "2026-11-02T10:00:00Z",
    rows: [
      { account: "CASH", accountType: "balance_sheet", total: "1357.30", units: { "u-1": "1200.00", "u-2": "157.30" } },
      { account: "REVENUE", accountType: "income", total: "-254.00", units: { "u-1": "-200.00", "u-2": "-54.00" } },
    ],
    unmapped: [{ unit: "u-2", mainAccount: "9000", unitCurrency: "EUR", unitAmount: "7.00", rate: "1.10", groupAmount: "7.70" }],
    total: "1103.30",
    unitDifferences: { "u-1": "1000.00", "u-2": "103.30" },
  };

  it("lists each account with every unit's share and warns about unmapped balances", async () => {
    handler = (url) => {
      if (url.pathname === "/api/v1/consol/collections/c-1/trial-balance") return { status: 200, body: tb };
      if (url.pathname === `/api/v1/consol/groups/${GROUP}`) return { status: 200, body: groupDetail };
      return undefined;
    };
    const { container } = renderAt("/consolidation/collections/c-1");
    expect(await screen.findByRole("heading", { level: 2, name: "Consolidated trial balance 2026-10" })).toBeInTheDocument();
    expect(screen.getByText("1357.30")).toBeInTheDocument();
    expect(screen.getByText("Some balances have no consolidation account")).toBeInTheDocument();
    expect(screen.getByText("9000")).toBeInTheDocument();
    expect((await screen.findAllByText(/Cedar Ridge Pumps Europe GmbH/)).length).toBeGreaterThan(0);
    expect(pageViolations(container)).toEqual([]);
  });

  it("keeps a stable column per unit", () => {
    expect(unitIds(tb as never)).toEqual(["u-1", "u-2"]);
  });
});
