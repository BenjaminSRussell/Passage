import { findTable } from "../../test-utils";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { Journal, PostingProfile, ProductionJob } from "./api/client";
import { emptyDraft, toJournalInput } from "./JournalsPanel";
import { addDecimals, negate } from "./labels";
import production from "./module";
import { blankProfile, validateProfile } from "./PostingProfilesPage";
import { json, order, orderId, problem } from "./testData";

afterEach(() => vi.unstubAllGlobals());

type Handler = (url: string, init?: RequestInit) => Response | Promise<Response>;

function stubFetch(handler: Handler) {
  const mock = vi.fn(async (url: string, init?: RequestInit) => handler(url, init));
  vi.stubGlobal("fetch", mock);
  return mock;
}

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <a href="#main">Skip to content</a>
      <nav aria-label="Workspaces" />
      <main id="main">
        <h1>Production</h1>
        <Routes>
          {production.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
        </Routes>
      </main>
    </MemoryRouter>,
  );
}

const journalId = "0195d6a0-0000-7000-8000-00000000a001";

function journal(overrides: Partial<Journal> = {}): Journal {
  return {
    id: journalId,
    orderId,
    type: "picking-list",
    journalNumber: "PJ-000001",
    postingDate: "2026-10-26",
    posted: false,
    qtyGood: "0",
    qtyError: "0",
    createdBy: "supervisor-1",
    createdAt: "2026-10-26T13:00:00Z",
    lines: [
      { kind: "issue", lineNo: "10", itemId: "RM-STEEL", qty: "203", qtyGood: "0", qtyError: "0", finished: false, unitCost: "10.2", amount: "0", overheadAmount: "0" },
    ],
    postings: [],
    ...overrides,
  };
}

const postedJournal = (overrides: Partial<Journal> = {}) =>
  journal({
    posted: true,
    postedBy: "supervisor-1",
    lines: [{ kind: "issue", lineNo: "10", itemId: "RM-STEEL", qty: "203", qtyGood: "0", qtyError: "0", finished: false, unitCost: "10.2", amount: "2070.6", overheadAmount: "0" }],
    postings: [
      { postingType: "wip-material", account: "1450", dr: "2070.6", cr: "0", wip: true },
      { postingType: "inventory-issue", account: "1400", dr: "0", cr: "2070.6", wip: false },
    ],
    ...overrides,
  });

const costs = {
  orderId,
  currency: "USD",
  totals: { material: "3000.00", labor: "1500.00", overhead: "500.00" },
  total: "5000.00",
  rows: [{ group: "material", key: "RM-STEEL", qty: "300", amount: "3000", fixed: "0" }],
  actualTotals: { material: "3111.00", labor: "1380.00", overhead: "460.00" },
  actualTotal: "4951.00",
  actualRows: [],
  wipBalance: "-49.00",
  variances: [] as unknown[],
  varianceTotal: "0.00",
};

interface Server {
  order: ReturnType<typeof order>;
  journals: Journal[];
  costs: typeof costs;
  jobs?: ProductionJob[];
  onPost?: Handler;
}

function serve(server: Server) {
  return stubFetch((url, init) => {
    if (url.endsWith("/status-log")) {
      return json({ orderId, items: [] });
    }
    if (url.endsWith("/costs")) {
      return json(server.costs);
    }
    if (url.endsWith("/jobs")) {
      return json({ orderId, items: server.jobs ?? [], plannedHours: "0" });
    }
    if (url.endsWith("/journals") && init?.method !== "POST") {
      return json({ orderId, items: server.journals });
    }
    if (init?.method === "POST" || init?.method === "DELETE") {
      return server.onPost ? server.onPost(url, init) : json({});
    }
    return json(server.order);
  });
}

const posts = (mock: ReturnType<typeof stubFetch>) =>
  (mock.mock.calls as unknown as Array<[string, RequestInit | undefined]>).filter(([, init]) => init?.method === "POST" || init?.method === "DELETE");

describe("Journals on the order detail", () => {
  it("lists journals with their status and shows actual cost and the WIP balance", async () => {
    serve({
      order: order({ status: "ReportedAsFinished", version: 8 }),
      costs,
      journals: [
        postedJournal({ reversedBy: "x" }),
        postedJournal({ id: "j2", journalNumber: "PJ-000002", reversalOf: journalId }),
        journal({ id: "j3", journalNumber: "PJ-000003", type: "route-card", lines: [] }),
      ],
    });
    const { container } = renderAt(`/production/${orderId}`);
    const table = await findTable({ name: "Production journals" });
    await waitFor(() => expect(within(table).getByText("PJ-000003")).toBeInTheDocument());
    expect(within(table).getByText("Reversed")).toBeInTheDocument();
    expect(within(table).getByText("Reversal")).toBeInTheDocument();
    expect(within(table).getByText("Draft")).toBeInTheDocument();
    expect(within(table).getAllByText("2070.60 USD")).toHaveLength(2);
    const actual = await screen.findByRole("list", { name: "Actual cost by group" });
    expect(actual).toHaveTextContent("Actual material 3111.00 USD");
    expect(actual).toHaveTextContent("WIP balance -49.00 USD");
    const comparison = screen.getByRole("table", { name: "Planned against actual cost" });
    expect(within(comparison).getByText("111.00")).toBeInTheDocument();
    expect(within(comparison).getByText("-120.00")).toBeInTheDocument();
    expect(checkAxeAA(container)).toEqual([]);
  });

  it("names the split-transfer journal a split of a Started order posted (BR-21.11)", async () => {
    serve({
      order: order({ status: "Started", version: 8 }),
      costs,
      journals: [postedJournal({ id: "j4", journalNumber: "PJ-000009", type: "split-transfer", counterpartOrderId: "0195d6a0-0000-7000-8000-00000000b009" })],
    });
    renderAt(`/production/${orderId}`);
    const table = await findTable({ name: "Production journals" });
    await waitFor(() => expect(within(table).getByText("PJ-000009")).toBeInTheDocument());
    expect(within(table).getByText("Split transfer")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "PJ-000009" }));
    const detail = await screen.findByRole("region", { name: "Journal PJ-000009" });
    expect(detail).toHaveTextContent("to the order split off from this one");
    expect(within(detail).getByRole("button", { name: "Open the other order" })).toBeInTheDocument();
  });

  it("names the order a split-transfer journal came from on the new order", async () => {
    const from = "0195d6a0-0000-7000-8000-00000000b001";
    serve({
      order: order({ status: "Started", version: 2, splitFromOrderId: from }),
      costs,
      journals: [postedJournal({ id: "j5", journalNumber: "PJ-000010", type: "split-transfer", counterpartOrderId: from })],
    });
    renderAt(`/production/${orderId}`);
    await userEvent.click(await screen.findByRole("button", { name: "PJ-000010" }));
    const detail = await screen.findByRole("region", { name: "Journal PJ-000010" });
    expect(detail).toHaveTextContent("posted on the order it was split from");
  });

  it("creates a proposed picking list, shows its lines, then posts it and reloads the order", async () => {
    const server: Server = { order: order({ status: "Started", version: 5 }), costs, journals: [] };
    const fetchMock = serve(server);
    server.onPost = (url) => {
      if (url.endsWith("/journals")) {
        server.journals = [journal()];
        return json(journal(), 201);
      }
      server.journals = [postedJournal()];
      server.order = order({ status: "Started", version: 6 });
      return json(postedJournal());
    };
    renderAt(`/production/${orderId}`);
    const form = await screen.findByRole("form", { name: "New journal" });
    await userEvent.click(within(form).getByRole("button", { name: "Create journal" }));
    expect(await screen.findByText("Journal PJ-000001 created with 1 lines. Review it, then post it.")).toBeInTheDocument();
    const [createUrl, createInit] = posts(fetchMock)[0];
    expect(createUrl).toBe(`/api/v1/production/orders/${orderId}/journals`);
    expect(JSON.parse(String(createInit?.body))).toEqual({ type: "picking-list", proposal: "remaining" });
    expect((createInit?.headers as Record<string, string>)["Idempotency-Key"]).toMatch(/.+/);
    const lines = await findTable({ name: "Lines of PJ-000001" });
    expect(within(lines).getByText("Issue 10")).toBeInTheDocument();

    const ordersBefore = fetchMock.mock.calls.filter(([url]) => url === `/api/v1/production/orders/${orderId}`).length;
    await userEvent.click(screen.getByRole("button", { name: "Post journal" }));
    expect(await screen.findByText("Journal PJ-000001 posted.")).toBeInTheDocument();
    const [postUrl, postInit] = posts(fetchMock)[1];
    expect(postUrl).toBe(`/api/v1/production/journals/${journalId}:post`);
    expect((postInit?.headers as Record<string, string>)["Idempotency-Key"]).toMatch(/.+/);
    await waitFor(() =>
      expect(fetchMock.mock.calls.filter(([url]) => url === `/api/v1/production/orders/${orderId}`).length).toBeGreaterThan(ordersBefore),
    );
    expect(await findTable({ name: "Postings of PJ-000001" })).toBeInTheDocument();
  });

  it("enters route-card hours by hand and validates them first", async () => {
    const server: Server = { order: order({ status: "Started", version: 5 }), costs, journals: [] };
    const fetchMock = serve(server);
    server.onPost = () => json(journal({ type: "route-card", lines: [] }), 201);
    renderAt(`/production/${orderId}`);
    const form = await screen.findByRole("form", { name: "New journal" });
    await userEvent.selectOptions(within(form).getByLabelText("Journal type"), "route-card");
    await userEvent.selectOptions(within(form).getByLabelText("Lines"), "none");
    await userEvent.click(within(form).getByRole("button", { name: "Create journal" }));
    expect(await within(form).findByText("Enter hours, a good or an error quantity for at least one operation.")).toBeInTheDocument();
    const operation = within(form).getByRole("group", { name: "Operation 10" });
    await userEvent.type(within(operation).getByLabelText("Hours"), "1.005");
    await userEvent.click(within(form).getByRole("button", { name: "Create journal" }));
    expect(await within(form).findByText("Enter at least 0.01 hours with up to 2 decimals.")).toBeInTheDocument();
    expect(posts(fetchMock)).toHaveLength(0);

    await userEvent.clear(within(operation).getByLabelText("Hours"));
    await userEvent.type(within(operation).getByLabelText("Hours"), "46");
    await userEvent.type(within(operation).getByLabelText("Good quantity"), "100");
    await userEvent.click(within(form).getByRole("button", { name: "Create journal" }));
    await waitFor(() => expect(posts(fetchMock)).toHaveLength(1));
    expect(JSON.parse(String(posts(fetchMock)[0][1]?.body))).toEqual({
      type: "route-card",
      proposal: "none",
      hours: [{ operNumber: 10, hours: "46", qtyGood: "100" }],
    });
  });

  it("enters job-card hours against the order's jobs, finishing one", async () => {
    const jobBase: ProductionJob = {
      id: "0195d6a0-0000-7000-8000-00000000b001",
      orderId,
      orderNumber: "PO-000001",
      orderStatus: "Started",
      legalEntityId: "0195d6a0-0000-7000-8000-000000000001",
      siteId: "0195d6a0-0000-7000-8000-000000000002",
      productId: "0195d6a0-0000-7000-8000-000000000003",
      operNumber: 10,
      seq: 2,
      jobType: "process",
      resourceId: "WC-WELD",
      plannedStart: "2026-11-02T08:00:00Z",
      plannedEnd: "2026-11-02T18:00:00Z",
      plannedHours: "46",
      hoursReported: "4",
      qtyGood: "0",
      qtyError: "0",
      status: "started",
    };
    const done: ProductionJob = { ...jobBase, id: "0195d6a0-0000-7000-8000-00000000b002", seq: 1, jobType: "setup", status: "finished" };
    const server: Server = { order: order({ status: "Started", version: 5 }), costs, journals: [], jobs: [done, jobBase] };
    const fetchMock = serve(server);
    server.onPost = () => json(journal({ type: "job-card", lines: [] }), 201);
    renderAt(`/production/${orderId}`);
    const form = await screen.findByRole("form", { name: "New journal" });
    await userEvent.selectOptions(within(form).getByLabelText("Journal type"), "job-card");
    await userEvent.selectOptions(within(form).getByLabelText("Lines"), "none");
    const job = await within(form).findByRole("group", { name: "Operation 10, process job (4 of 46 h reported)" });
    expect(within(form).queryByRole("group", { name: /setup job/ })).toBeNull();
    await userEvent.click(within(form).getByRole("button", { name: "Create journal" }));
    expect(await within(form).findByText("Enter hours, a good or an error quantity, or finish at least one job.")).toBeInTheDocument();
    await userEvent.type(within(job).getByLabelText("Hours"), "42");
    await userEvent.type(within(job).getByLabelText("Good quantity"), "100");
    await userEvent.click(within(job).getByRole("checkbox", { name: "Job finished" }));
    await userEvent.click(within(form).getByRole("button", { name: "Create journal" }));
    await waitFor(() => expect(posts(fetchMock)).toHaveLength(1));
    expect(JSON.parse(String(posts(fetchMock)[0][1]?.body))).toEqual({
      type: "job-card",
      proposal: "none",
      hours: [{ jobId: jobBase.id, hours: "42", qtyGood: "100", finished: true }],
    });
  });

  it("names the job a posted job-card line reported on", async () => {
    const line = { kind: "hours" as const, operNumber: 10, jobType: "setup" as const, qty: "1", qtyGood: "0", qtyError: "0", finished: false, unitCost: "30", amount: "30", overheadAmount: "0" };
    const server: Server = { order: order({ status: "Started", version: 5 }), costs, journals: [journal({ type: "job-card", posted: true, lines: [line] })] };
    serve(server);
    renderAt(`/production/${orderId}`);
    await userEvent.click(await screen.findByText("PJ-000001"));
    expect(await screen.findByText("Operation 10, setup job")).toBeInTheDocument();
  });

  it("shows a refused post with its code", async () => {
    const server: Server = { order: order({ status: "Started", version: 5 }), costs, journals: [journal()] };
    server.onPost = () => problem(422, "PRODUCTION_OVER_CONSUMPTION", "line 10: consumption 221 exceeds required 200 plus tolerance 10%");
    serve(server);
    renderAt(`/production/${orderId}`);
    await userEvent.click(await screen.findByRole("button", { name: "PJ-000001" }));
    await userEvent.click(screen.getByRole("button", { name: "Post journal" }));
    const refused = await screen.findByText("The journal was refused");
    expect(refused.closest("[role=alert]")).toHaveTextContent("PRODUCTION_OVER_CONSUMPTION");
  });

  it("asks before deleting a draft", async () => {
    const server: Server = { order: order({ status: "Started", version: 5 }), costs, journals: [journal()] };
    const fetchMock = serve(server);
    server.onPost = () => {
      server.journals = [];
      return new Response(null, { status: 204 });
    };
    renderAt(`/production/${orderId}`);
    await userEvent.click(await screen.findByRole("button", { name: "PJ-000001" }));
    await userEvent.click(screen.getByRole("button", { name: "Delete draft" }));
    await userEvent.click(screen.getByRole("button", { name: "Keep it" }));
    expect(posts(fetchMock)).toHaveLength(0);
    await userEvent.click(screen.getByRole("button", { name: "Delete draft" }));
    await userEvent.click(screen.getByRole("button", { name: "Yes, delete the draft" }));
    expect(await screen.findByText("Draft PJ-000001 deleted.")).toBeInTheDocument();
    expect(posts(fetchMock)[0]).toEqual([`/api/v1/production/journals/${journalId}`, expect.objectContaining({ method: "DELETE" })]);
    expect(await screen.findByRole("heading", { name: "No journals yet" })).toBeInTheDocument();
  });

  it("explains when an order takes no journals and when the list fails", async () => {
    serve({ order: order({ status: "Estimated", version: 2 }), costs, journals: [] });
    renderAt(`/production/${orderId}`);
    expect(await screen.findByText(/Journals are posted on Released \(route cards and job cards\), Started and Reported-as-finished orders/)).toBeInTheDocument();
    expect(screen.queryByRole("form", { name: "New journal" })).not.toBeInTheDocument();
    vi.unstubAllGlobals();

    stubFetch((url) => (url.endsWith("/journals") ? problem(403, "AUTH-0003", "missing duty production.order.read") : url.endsWith("/costs") ? json(costs) : url.endsWith("/status-log") ? json({ items: [] }) : json(order())));
    renderAt(`/production/${orderId}`);
    expect(await screen.findByText("Could not load the journals")).toBeInTheDocument();
  });

  it("lists the variances once the order is ended", async () => {
    serve({
      order: order({ status: "Ended", version: 9 }),
      journals: [],
      costs: {
        ...costs,
        wipBalance: "0.00",
        variances: [
          { type: "production-quantity", costGroup: "material", amount: "50" },
          { type: "production-quantity", costGroup: "labor", amount: "-120" },
        ],
      },
    });
    renderAt(`/production/${orderId}`);
    const table = await findTable({ name: "Variances posted at End" });
    expect(within(table).getByText("-120.00")).toBeInTheDocument();
    expect(within(table).getAllByText("Quantity")).toHaveLength(2);
  });
});

describe("journal form rules", () => {
  it("requires a good quantity on a report as finished and keeps only entered lines", () => {
    expect(toJournalInput({ ...emptyDraft("report-as-finished") }).errors).toHaveProperty("quantity");
    const ok = toJournalInput({ ...emptyDraft("report-as-finished"), quantity: "40", errorQuantity: "2" });
    expect(ok.errors).toEqual({});
    expect(ok.input).toEqual({ type: "report-as-finished", proposal: "proportional", quantity: "40", errorQuantity: "2" });

    const manual = toJournalInput({
      ...emptyDraft("picking-list"),
      proposal: "none",
      postingDate: "2026-10-27",
      issues: { "10": { quantity: "203", unitCost: "10.20" }, "30.1": { quantity: "", unitCost: "" } },
    });
    expect(manual.input).toEqual({ type: "picking-list", proposal: "none", postingDate: "2026-10-27", issues: [{ lineNo: "10", quantity: "203", unitCost: "10.20" }] });
    expect(toJournalInput({ ...emptyDraft("picking-list"), proposal: "none", issues: { "10": { quantity: "0", unitCost: "" } } }).errors).toHaveProperty("qty.10");
  });

  it("builds a job card line by job id and keeps a finish-only line", () => {
    const draft = { ...emptyDraft("job-card"), proposal: "none" as const };
    expect(toJournalInput(draft).errors).toHaveProperty("lines");
    const built = toJournalInput({ ...draft, hours: { "job-1": { hours: "", qtyGood: "", finished: true }, "job-2": { hours: "2.5", qtyGood: "" }, "job-3": { hours: "", qtyGood: "" } } });
    expect(built.errors).toEqual({});
    expect(built.input?.hours).toEqual([
      { jobId: "job-1", hours: "0", finished: true },
      { jobId: "job-2", hours: "2.5" },
    ]);
    expect(toJournalInput({ ...draft, hours: { "job-1": { hours: "1.005", qtyGood: "" } } }).errors).toHaveProperty("hours.job-1");
  });

  it("adds and negates decimal strings exactly", () => {
    expect(addDecimals(["0.1", "0.2"])).toBe("0.3");
    expect(addDecimals(["3111", negate("3000")])).toBe("111");
    expect(addDecimals(["1380", negate("1500")])).toBe("-120");
    expect(addDecimals([])).toBe("0");
    expect(negate("-49")).toBe("49");
  });
});

describe("Posting profiles page", () => {
  const profile = (overrides: Partial<PostingProfile> = {}): PostingProfile => ({
    ...blankProfile(),
    legalEntityId: "0195d6a0-0000-7000-8000-0000000000e1",
    wipAccount: "1450",
    inventoryAccount: "1400",
    finishedGoodsAccount: "1410",
    laborAbsorptionAccount: "5510",
    overheadAbsorptionAccount: "5520",
    lotSizeVarianceAccount: "5610",
    quantityVarianceAccount: "5620",
    priceVarianceAccount: "5630",
    substitutionVarianceAccount: "5640",
    version: 1,
    updatedBy: "admin",
    updatedAt: "2026-10-26T12:00:00Z",
    ...overrides,
  });

  it("lists profiles", async () => {
    const fetchMock = stubFetch(() => json({ items: [profile(), profile({ itemGroup: "FG", wipAccount: "1460" })] }));
    const { container } = renderAt("/production/posting-profiles");
    const table = await findTable({ name: "Posting profiles" });
    await waitFor(() => expect(within(table).getByText("1460")).toBeInTheDocument());
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/production/posting-profiles");
    expect(checkAxeAA(container)).toEqual([]);
  });

  it("shows loading, empty and error states", async () => {
    stubFetch(() => new Promise<Response>(() => undefined));
    const first = renderAt("/production/posting-profiles");
    expect(await screen.findByRole("table", { name: "Posting profiles" })).toHaveAttribute("aria-busy", "true");
    first.unmount();
    vi.unstubAllGlobals();
    stubFetch(() => json({ items: [] }));
    const second = renderAt("/production/posting-profiles");
    expect(await screen.findByRole("heading", { name: "No posting profiles yet" })).toBeInTheDocument();
    second.unmount();
    vi.unstubAllGlobals();
    stubFetch(() => problem(403, "AUTH-0003", "missing duty production.setup.read"));
    renderAt("/production/posting-profiles");
    expect(await screen.findByText("Could not load posting profiles")).toBeInTheDocument();
  });

  it("validates, then creates a profile without If-Match", async () => {
    const fetchMock = stubFetch((_, init) => (init?.method === "PUT" ? json(profile(), 201) : json({ items: [] })));
    renderAt("/production/posting-profiles");
    const form = await screen.findByRole("form", { name: "Posting profile" });
    await userEvent.click(within(form).getByRole("button", { name: "Save posting profile" }));
    expect(within(form).getAllByText("Enter the ledger account (up to 64 characters).")).toHaveLength(9);
    expect(fetchMock.mock.calls.some(([, init]) => init?.method === "PUT")).toBe(false);

    const values = profile();
    for (const [label, value] of [
      ["WIP account", values.wipAccount],
      ["Inventory (picking-list offset)", values.inventoryAccount],
      ["Finished goods (RAF receipt)", values.finishedGoodsAccount],
      ["Labor absorption", values.laborAbsorptionAccount],
      ["Overhead absorption", values.overheadAbsorptionAccount],
      ["Lot-size variance", values.lotSizeVarianceAccount],
      ["Quantity variance", values.quantityVarianceAccount],
      ["Price variance", values.priceVarianceAccount],
      ["Substitution variance", values.substitutionVarianceAccount],
    ]) {
      await userEvent.type(within(form).getByLabelText(label), value);
    }
    await userEvent.click(within(form).getByRole("button", { name: "Save posting profile" }));
    expect(await screen.findByText("Posting profile All × All saved (version 1).")).toBeInTheDocument();
    const put = fetchMock.mock.calls.find(([, init]) => init?.method === "PUT");
    expect((put?.[1]?.headers as Record<string, string>)["If-Match"]).toBeUndefined();
    expect(JSON.parse(String(put?.[1]?.body))).toMatchObject({ itemGroup: "All", siteId: "All", wipAccount: "1450", substitutionVarianceAccount: "5640" });
  });

  it("edits a profile under If-Match and shows a version conflict", async () => {
    const fetchMock = stubFetch((_, init) =>
      init?.method === "PUT" ? problem(412, "conflict_version", "the posting profile was changed by another user; current version is 2") : json({ items: [profile()] }),
    );
    renderAt("/production/posting-profiles");
    await userEvent.click(await screen.findByRole("button", { name: "All" }));
    const form = screen.getByRole("form", { name: "Posting profile" });
    expect(within(form).getByLabelText("Item group")).toHaveAttribute("readonly");
    const wip = within(form).getByLabelText("WIP account");
    await userEvent.clear(wip);
    await userEvent.type(wip, "1451");
    await userEvent.click(within(form).getByRole("button", { name: "Save posting profile" }));
    expect(await within(form).findByText("The profile was not saved")).toBeInTheDocument();
    expect(within(form).getByText(/conflict_version/)).toBeInTheDocument();
    const put = fetchMock.mock.calls.find(([, init]) => init?.method === "PUT");
    expect((put?.[1]?.headers as Record<string, string>)["If-Match"]).toBe('"1"');
  });

  it("checks the site and item group like the service", () => {
    const p = { ...blankProfile(), siteId: "plant-1", itemGroup: "" };
    const errors = validateProfile(p);
    expect(errors).toHaveProperty("siteId");
    expect(errors).toHaveProperty("itemGroup");
  });
});
