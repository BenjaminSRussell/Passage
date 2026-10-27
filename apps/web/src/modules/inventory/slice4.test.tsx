import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { validateTagCount } from "./CountTagsPage";
import inventory from "./module";

const PRODUCT_ID = "5f2c1e7a-0000-4000-8000-000000000002";
const ENTITY_ID = "9e8d7c6b-0000-4000-8000-000000000003";
const PLAN_ID = "3d3d3d3d-0000-4000-8000-000000000011";
const SHEET_ID = "4e4e4e4e-0000-4000-8000-000000000012";
const JOURNAL_ID = "1a2b3c4d-0000-4000-8000-000000000001";
const REVERSAL_ID = "6f6f6f6f-0000-4000-8000-000000000013";

type Handler = (url: URL, init: RequestInit) => { status: number; body: unknown };

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
function pageViolations(container: HTMLElement) {
  return checkAxeAA(container).filter((v) => !shellRules.has(v.id));
}

let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit }[];

const json = (status: number, body: unknown) => ({ status, body });
const sent = (method: string) => calls.filter((c) => c.method === method);
const bodyOf = (call?: { init: RequestInit }) => JSON.parse((call?.init.body as string) ?? "null");
const header = (call: { init: RequestInit } | undefined, name: string) => new Headers(call?.init.headers).get(name);

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, init });
      const { status, body } = handler(url, init);
      return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {inventory.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const plan = {
  id: PLAN_ID,
  legalEntityId: ENTITY_ID,
  name: "Weekly W1",
  warehouse: "W1",
  currency: "USD",
  unit: "EA",
  tolerancePct: "5",
  toleranceValue: "100",
  blind: false,
  createdAt: "2026-09-01T08:00:00Z",
};

const tag = (n: number, overrides: Record<string, unknown> = {}) => ({
  id: `7a7a7a7a-0000-4000-8000-${String(n).padStart(12, "0")}`,
  tagNumber: `TAG-${String(n).padStart(6, "0")}`,
  planId: PLAN_ID,
  status: "OPEN",
  dimensions: {},
  countedQty: null,
  version: 1,
  ...overrides,
});

/** Serves the plan list and the given tags; other calls go to rest. */
function tagsHandler(tags: unknown[], rest: Handler = () => json(500, {})): Handler {
  return (url, init) => {
    if ((init.method ?? "GET") === "GET" && url.pathname.endsWith("/count-plans")) return json(200, { items: [plan] });
    if ((init.method ?? "GET") === "GET" && url.pathname.endsWith("/tags")) return json(200, { items: tags });
    return rest(url, init);
  };
}

describe("Count tags", () => {
  it("validates a tag count like the service", () => {
    const ok = { productId: PRODUCT_ID, countedQty: "0", dimensions: { site: "S1", warehouse: "", location: "", batch: "", serial: "" } };
    expect(validateTagCount(ok, "W1")).toEqual({});
    expect(
      validateTagCount({ productId: "x", countedQty: "2", dimensions: { site: "", warehouse: "W2", location: "", batch: "", serial: "SN1" } }, "W1"),
    ).toEqual({
      productId: "Enter the product id (a UUID).",
      countedQty: "A serial number counts 0 or 1.",
      site: "Site is required.",
      warehouse: "This plan counts warehouse W1.",
    });
    expect(validateTagCount({ ...ok, countedQty: "-1" }).countedQty).toBe("Enter the counted quantity: zero or more.");
  });

  it("lists the plan's tags with their status and filters them", async () => {
    handler = tagsHandler([
      tag(1, { status: "USED", productId: PRODUCT_ID, dimensions: { site: "S1", warehouse: "W1" }, countedQty: "0", version: 2 }),
      tag(2, { status: "VOID" }),
      tag(3),
      tag(4, { status: "USED", productId: PRODUCT_ID, dimensions: { site: "S1" }, countedQty: "5", sheetId: SHEET_ID }),
    ]);
    const { container } = renderAt(`/inventory/count-plans/${PLAN_ID}/tags`);
    expect(screen.getByRole("table", { name: "Count tags" })).toHaveAttribute("aria-busy", "true");
    expect(await screen.findByText("Weekly W1 · warehouse W1")).toBeInTheDocument();
    const table = screen.getByRole("table", { name: "Count tags" });
    await waitFor(() => expect(table).toHaveTextContent("TAG-000004"));
    expect(table).toHaveTextContent("Site S1 · Warehouse W1");
    expect(within(table).getByRole("link", { name: "4e4e4e4e" })).toHaveAttribute("href", `/inventory/count-sheets/${SHEET_ID}`);
    expect(within(table).getByRole("button", { name: "Count TAG-000003" })).toBeInTheDocument();
    expect(within(table).queryByRole("button", { name: "Void TAG-000004" })).toBeNull();
    expect(screen.getByText(/1 used, 1 open and 1 void tags are waiting/)).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "VOID" } });
    expect(table).not.toHaveTextContent("TAG-000001");
    expect(table).toHaveTextContent("TAG-000002");
  });

  it("explains an empty plan, an unknown plan and a failure", async () => {
    handler = tagsHandler([]);
    const first = renderAt(`/inventory/count-plans/${PLAN_ID}/tags`);
    expect(await screen.findByText("No tags yet")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Transfer to count sheet" })).toBeDisabled();
    first.unmount();
    handler = () => json(404, { code: "INV-CNT-004", detail: "count plan not found" });
    const second = renderAt(`/inventory/count-plans/${PLAN_ID}/tags`);
    expect(await screen.findByRole("heading", { name: "Count plan not found" })).toBeInTheDocument();
    second.unmount();
    handler = () => json(403, { code: "AUTH-0003", detail: "missing duty inventory.read" });
    renderAt(`/inventory/count-plans/${PLAN_ID}/tags`);
    expect(await screen.findByRole("alert")).toHaveTextContent("missing duty inventory.read (AUTH-0003)");
  });

  it("generates tags with an idempotency key", async () => {
    handler = tagsHandler([], (url) => (url.pathname.endsWith(":generate-tags") ? json(201, { items: [tag(1), tag(2)] }) : json(500, {})));
    renderAt(`/inventory/count-plans/${PLAN_ID}/tags`);
    await screen.findByText("No tags yet");
    fireEvent.change(screen.getByLabelText("Number of tags"), { target: { value: "0" } });
    fireEvent.click(screen.getByRole("button", { name: "Generate tags" }));
    expect(await screen.findByText("Enter a number of tags from 1 to 1000.")).toBeInTheDocument();
    expect(sent("POST")).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Number of tags"), { target: { value: "2" } });
    fireEvent.click(screen.getByRole("button", { name: "Generate tags" }));
    expect(await screen.findByText("2 tags generated.")).toBeInTheDocument();
    const post = sent("POST")[0];
    expect(post.url.pathname).toBe(`/api/v1/inventory/count-plans/${PLAN_ID}:generate-tags`);
    expect(header(post, "Idempotency-Key")).toBeTruthy();
    expect(bodyOf(post)).toEqual({ count: 2 });
  });

  it("enters the count of an open tag at its version", async () => {
    handler = tagsHandler([tag(3, { version: 4 })], (url) =>
      url.pathname.endsWith(":use") ? json(200, tag(3, { status: "USED", productId: PRODUCT_ID, countedQty: "12", version: 5 })) : json(500, {}),
    );
    renderAt(`/inventory/count-plans/${PLAN_ID}/tags`);
    fireEvent.click(await screen.findByRole("button", { name: "Count TAG-000003" }));
    expect(screen.getByRole("heading", { name: "Count tag TAG-000003" })).toBeInTheDocument();
    expect(screen.getByLabelText("Warehouse")).toHaveValue("W1");
    fireEvent.click(screen.getByRole("button", { name: "Save count" }));
    expect(await screen.findByText("Site is required.")).toBeInTheDocument();
    expect(sent("POST")).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: PRODUCT_ID } });
    fireEvent.change(screen.getByLabelText("Counted quantity"), { target: { value: "12" } });
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: "S1" } });
    fireEvent.change(screen.getByLabelText("Batch"), { target: { value: "B1" } });
    fireEvent.click(screen.getByRole("button", { name: "Save count" }));
    expect(await screen.findByText("TAG-000003 counted: 12.")).toBeInTheDocument();
    const post = sent("POST")[0];
    expect(post.url.pathname).toBe(`/api/v1/inventory/count-tags/${tag(3).id}:use`);
    expect(header(post, "If-Match")).toBe('"4"');
    expect(bodyOf(post)).toEqual({ productId: PRODUCT_ID, countedQty: "12", dimensions: { site: "S1", warehouse: "W1", batch: "B1" } });
  });

  it("shows a used tag rejection and voids a tag", async () => {
    handler = tagsHandler([tag(3, { version: 2 })], (url) =>
      url.pathname.endsWith(":use")
        ? json(422, { code: "INV-TAG-001", detail: "tag TAG-000003 has already been used" })
        : json(200, tag(3, { status: "VOID", version: 3 })),
    );
    renderAt(`/inventory/count-plans/${PLAN_ID}/tags`);
    fireEvent.click(await screen.findByRole("button", { name: "Count TAG-000003" }));
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: PRODUCT_ID } });
    fireEvent.change(screen.getByLabelText("Counted quantity"), { target: { value: "1" } });
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: "S1" } });
    fireEvent.click(screen.getByRole("button", { name: "Save count" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("tag TAG-000003 has already been used (INV-TAG-001)");
    fireEvent.click(screen.getByRole("button", { name: "Void TAG-000003" }));
    expect(await screen.findByText("TAG-000003 voided.")).toBeInTheDocument();
    const voided = sent("POST").find((c) => c.url.pathname.endsWith(":void"));
    expect(header(voided, "If-Match")).toBe('"2"');
  });

  it("transfers used tags, marking open tags missing, and opens the count sheet", async () => {
    const sheet = { id: SHEET_ID, legalEntityId: ENTITY_ID, planId: PLAN_ID, planName: "Weekly W1", number: "CNT-1", status: "OPEN", blind: false, version: 1, lineCount: 1, countedCount: 1, createdAt: "2026-09-01T08:00:00Z", lines: [] };
    handler = (url, init) => {
      if (url.pathname.endsWith(":transfer-tags")) return json(201, sheet);
      if (url.pathname.endsWith(`/count-sheets/${SHEET_ID}`)) return json(200, sheet);
      return tagsHandler([tag(1, { status: "USED", productId: PRODUCT_ID, countedQty: "3" }), tag(2)])(url, init);
    };
    renderAt(`/inventory/count-plans/${PLAN_ID}/tags`);
    const transfer = await screen.findByRole("button", { name: "Transfer to count sheet" });
    expect(transfer).toBeDisabled();
    fireEvent.click(screen.getByLabelText("Mark the 1 open tag as missing (supervisor)"));
    fireEvent.click(transfer);
    expect(await screen.findByRole("heading", { name: "Count sheet CNT-1" })).toBeInTheDocument();
    const post = sent("POST")[0];
    expect(post.url.pathname).toBe(`/api/v1/inventory/count-plans/${PLAN_ID}:transfer-tags`);
    expect(header(post, "Idempotency-Key")).toBeTruthy();
    expect(bodyOf(post)).toEqual({ markOpenMissing: true });
  });

  it("explains a missing supervisor approval", async () => {
    handler = tagsHandler([tag(1, { status: "USED", productId: PRODUCT_ID, countedQty: "3" }), tag(2)], () =>
      json(403, { code: "INV-TAG-006", detail: "1 open tag(s) would be marked MISSING; a supervisor (inventory.count.approve) must confirm" }),
    );
    renderAt(`/inventory/count-plans/${PLAN_ID}/tags`);
    fireEvent.click(await screen.findByLabelText("Mark the 1 open tag as missing (supervisor)"));
    fireEvent.click(screen.getByRole("button", { name: "Transfer to count sheet" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Supervisor approval required");
    expect(alert).toHaveTextContent("(INV-TAG-006)");
  });

  it("is reached from the count plan list", async () => {
    handler = () => json(200, { items: [plan] });
    renderAt("/inventory/count-plans");
    expect(await screen.findByRole("link", { name: "Tags for Weekly W1" })).toHaveAttribute("href", `/inventory/count-plans/${PLAN_ID}/tags`);
  });
});

function journal(overrides: Record<string, unknown> = {}) {
  return {
    id: JOURNAL_ID,
    legalEntityId: ENTITY_ID,
    journalType: "MOVEMENT",
    status: "POSTED",
    postingDate: "2026-09-01",
    offsetAccountId: "500100",
    currency: "USD",
    version: 3,
    lineCount: 1,
    createdAt: "2026-09-01T08:00:00Z",
    postedAt: "2026-09-01T09:00:00Z",
    lines: [{ id: "l1", lineNo: 1, productId: PRODUCT_ID, dimensions: { site: "S1" }, qty: "10", enteredQty: "10", enteredUnit: "EA", costPrice: "3" }],
    ...overrides,
  };
}

const reversal = journal({
  id: REVERSAL_ID,
  status: "DRAFT",
  version: 1,
  postingDate: "2026-09-30",
  reversesJournalId: JOURNAL_ID,
  postedAt: undefined,
  lines: [{ id: "r1", lineNo: 1, reversesLineId: "l1", productId: PRODUCT_ID, dimensions: { site: "S1" }, qty: "-10", enteredQty: "-10", enteredUnit: "EA", costPrice: "3" }],
});

describe("Journal reversal", () => {
  it("reverses a posted journal and opens the reversal draft", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return json(201, reversal);
      return url.pathname.endsWith(REVERSAL_ID) ? json(200, reversal) : json(200, journal());
    };
    const { container } = renderAt(`/inventory/journals/${JOURNAL_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Reverse journal" }));
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Posting date"), { target: { value: "2026-09-30" } });
    fireEvent.click(screen.getByRole("button", { name: "Create reversal" }));
    expect(await screen.findByRole("button", { name: "Post reversal" })).toBeInTheDocument();
    const post = sent("POST")[0];
    expect(post.url.pathname).toBe(`/api/v1/inventory/journals/${JOURNAL_ID}:reverse`);
    expect(bodyOf(post)).toEqual({ postingDate: "2026-09-30" });
    expect(screen.getByRole("link", { name: "1a2b3c4d" })).toHaveAttribute("href", `/inventory/journals/${JOURNAL_ID}`);
    expect(screen.queryByRole("heading", { name: "Add line" })).toBeNull();
    expect(screen.getByRole("table", { name: "Journal lines" })).toHaveTextContent("-10");
  });

  it("links a reversed journal to its reversal and offers no second reversal", async () => {
    handler = () => json(200, journal({ reversedByJournalId: REVERSAL_ID }));
    renderAt(`/inventory/journals/${JOURNAL_ID}`);
    expect(await screen.findByRole("link", { name: "6f6f6f6f" })).toHaveAttribute("href", `/inventory/journals/${REVERSAL_ID}`);
    expect(screen.queryByRole("button", { name: "Reverse journal" })).toBeNull();
  });

  it("shows why a reversal was refused", async () => {
    handler = (_url, init) =>
      init.method === "POST" ? json(409, { code: "INV-JNL-004", detail: "only a POSTED journal can be reversed; this one is DRAFT" }) : json(200, journal());
    renderAt(`/inventory/journals/${JOURNAL_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Reverse journal" }));
    fireEvent.click(screen.getByRole("button", { name: "Create reversal" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("only a POSTED journal can be reversed; this one is DRAFT (INV-JNL-004)");
  });
});
