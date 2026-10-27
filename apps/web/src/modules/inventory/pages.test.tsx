import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import inventory from "./module";
import { validateLine } from "./JournalDetailPage";
import { validateJournal } from "./NewJournalPage";

const JOURNAL_ID = "0a1b2c3d-0000-4000-8000-000000000001";
const PRODUCT_ID = "5f2c1e7a-0000-4000-8000-000000000002";
const ENTITY_ID = "9e8d7c6b-0000-4000-8000-000000000003";

type Handler = (url: URL, init: RequestInit) => { status: number; body: unknown };

// The shell supplies the h1, landmarks and skip link; a page rendered alone is
// checked for everything else (labels, button names, duplicate ids, icons).
const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
function pageViolations(container: HTMLElement) {
  return checkAxeAA(container).filter((v) => !shellRules.has(v.id));
}

let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit }[];

function json(status: number, body: unknown) {
  return { status, body };
}

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, init });
      const { status, body } = handler(url, init);
      return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json", ETag: '"1"' } });
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

const onHandRow = {
  group: { product: PRODUCT_ID, site: "S1" },
  physicalInvent: "12",
  availPhysical: "12",
  availOrdered: "12",
  postedValue: "36.00",
  reservedPhysical: "0",
  currency: "USD",
  negative: true,
};

function journal(overrides: Record<string, unknown> = {}) {
  return {
    id: JOURNAL_ID,
    legalEntityId: ENTITY_ID,
    journalType: "MOVEMENT",
    status: "DRAFT",
    postingDate: "2026-09-01",
    offsetAccountId: "500100",
    currency: "USD",
    version: 2,
    lineCount: 1,
    createdAt: "2026-09-01T08:00:00Z",
    lines: [
      { id: "l1", lineNo: 1, productId: PRODUCT_ID, dimensions: { site: "S1", warehouse: "W1" }, qty: "10", enteredQty: "10", enteredUnit: "EA", costPrice: "3" },
    ],
    ...overrides,
  };
}

describe("On-hand inquiry", () => {
  it("shows a loading table, then grouped rows with negative stock named in text", async () => {
    handler = () => json(200, { items: [onHandRow], truncated: false });
    const { container } = renderAt("/inventory/onhand");
    expect(screen.getByRole("heading", { name: "On-hand inquiry" })).toBeInTheDocument();
    expect(screen.getByRole("table", { name: "On-hand" })).toHaveAttribute("aria-busy", "true");
    const table = await screen.findByRole("table", { name: "On-hand" });
    await waitFor(() => expect(table).toHaveTextContent("Negative stock"));
    expect(table).toHaveTextContent("36.00 USD");
    expect(table).toHaveTextContent("S1");
    expect(calls[0].url.searchParams.get("groupBy")).toBe("product,site");
    expect(pageViolations(container)).toEqual([]);
  });

  it("refetches when a grouping is toggled and when filters are applied", async () => {
    handler = () => json(200, { items: [onHandRow], truncated: false });
    renderAt("/inventory");
    await screen.findByText("Negative stock");
    fireEvent.click(within(screen.getByRole("group", { name: "Group by" })).getByRole("checkbox", { name: "Batch" }));
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("groupBy")).toBe("product,site,batch"));
    expect(await screen.findByRole("columnheader", { name: "Batch" })).toBeInTheDocument();

    fireEvent.change(screen.getByRole("textbox", { name: "Warehouse" }), { target: { value: "W1" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("warehouse")).toBe("W1"));
  });

  it("refuses a product filter that is not an id, without calling the service", async () => {
    handler = () => json(200, { items: [], truncated: false });
    renderAt("/inventory/onhand");
    await screen.findByText("No stock matches");
    const before = calls.length;
    fireEvent.change(screen.getByRole("textbox", { name: "Product" }), { target: { value: "SHIRT" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    expect(await screen.findByText("Enter the product id (a UUID).")).toBeInTheDocument();
    expect(calls.length).toBe(before);
  });

  it("explains an empty result and warns when the result is cut", async () => {
    handler = () => json(200, { items: [], truncated: true });
    renderAt("/inventory/onhand");
    expect(await screen.findByText("No stock matches")).toBeInTheDocument();
    expect(screen.getByText("Showing the first 10,000 groups")).toBeInTheDocument();
  });

  it("shows the problem code when the inquiry fails and retries on request", async () => {
    handler = () => json(403, { code: "AUTH-0003", detail: "missing duty inventory.read" });
    renderAt("/inventory/onhand");
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("missing duty inventory.read (AUTH-0003)");
    expect(screen.queryByRole("table", { name: "On-hand" })).not.toBeInTheDocument();
    handler = () => json(200, { items: [onHandRow], truncated: false });
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("Negative stock")).toBeInTheDocument();
  });
});

describe("Journal list", () => {
  it("lists journals and opens one", async () => {
    handler = (url) =>
      url.pathname.endsWith(`/journals/${JOURNAL_ID}`) ? json(200, journal()) : json(200, { items: [journal({ lines: undefined })] });
    const { container } = renderAt("/inventory/journals");
    const table = await screen.findByRole("table", { name: "Journals" });
    await waitFor(() => expect(table).toHaveTextContent("Movement"));
    expect(table).toHaveTextContent("Draft");
    expect(screen.getByRole("link", { name: "New journal" })).toHaveAttribute("href", "/inventory/journals/new");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: "0a1b2c3d" }));
    expect(await screen.findByRole("heading", { name: "Movement journal" })).toBeInTheDocument();
  });

  it("filters by status and loads more pages", async () => {
    handler = (url) =>
      url.searchParams.get("cursor") === "c1"
        ? json(200, { items: [journal({ id: "ffffffff-0000-4000-8000-000000000009", journalType: "ADJUSTMENT" })] })
        : json(200, { items: [journal()], nextCursor: "c1" });
    renderAt("/inventory/journals");
    fireEvent.click(await screen.findByRole("button", { name: "Load more journals" }));
    await waitFor(() => expect(screen.getByRole("table", { name: "Journals" })).toHaveTextContent("Adjustment"));
    expect(screen.queryByRole("button", { name: "Load more journals" })).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "POSTED" } });
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("status")).toBe("POSTED"));
  });

  it("explains an empty list and shows load failures", async () => {
    handler = () => json(200, { items: [] });
    const first = renderAt("/inventory/journals");
    expect(await screen.findByText("No journals yet")).toBeInTheDocument();
    first.unmount();
    handler = () => json(500, { code: "COM-0500", detail: "internal error" });
    renderAt("/inventory/journals");
    expect(await screen.findByRole("alert")).toHaveTextContent("internal error (COM-0500)");
  });
});

describe("New journal", () => {
  it("validates before calling the service", () => {
    expect(validateJournal({ journalType: "MOVEMENT", legalEntityId: "x", postingDate: "", currency: "usd", offsetAccountId: "" })).toEqual({
      legalEntityId: "Enter the legal entity id (a UUID).",
      postingDate: "Choose a posting date.",
      currency: "Enter a three-letter currency code, such as USD.",
      offsetAccountId: "A movement journal needs an offset account.",
    });
    expect(validateJournal({ journalType: "ADJUSTMENT", legalEntityId: ENTITY_ID, postingDate: "2026-09-01", currency: "USD", offsetAccountId: "" })).toEqual({});
  });

  it("creates the journal and opens it", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(201, journal({ status: "DRAFT", lines: [] })) : json(200, journal({ lines: [] })));
    const { container } = renderAt("/inventory/journals/new");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Journal type"), { target: { value: "ADJUSTMENT" } });
    expect(screen.queryByLabelText("Offset account")).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Journal type"), { target: { value: "MOVEMENT" } });
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: ENTITY_ID } });
    fireEvent.change(screen.getByLabelText("Currency"), { target: { value: "usd" } });
    fireEvent.change(screen.getByLabelText("Offset account"), { target: { value: "500100" } });
    fireEvent.click(screen.getByRole("button", { name: "Create journal" }));
    expect(await screen.findByRole("heading", { name: "Movement journal" })).toBeInTheDocument();
    const create = calls.find((c) => c.method === "POST");
    expect(JSON.parse(create?.init.body as string)).toMatchObject({ journalType: "MOVEMENT", legalEntityId: ENTITY_ID, currency: "USD", offsetAccountId: "500100" });
    expect(new Headers(create?.init.headers).get("Idempotency-Key")).toBeTruthy();
  });

  it("shows field errors and form-level failures from the service", async () => {
    handler = () => json(400, { code: "COM-0001", detail: "one or more fields are invalid", errors: { currency: "must be an ISO 4217 alpha-3 code" } });
    renderAt("/inventory/journals/new");
    fireEvent.change(screen.getByLabelText("Journal type"), { target: { value: "OPENING_BALANCE" } });
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: ENTITY_ID } });
    fireEvent.change(screen.getByLabelText("Currency"), { target: { value: "XXX" } });
    fireEvent.click(screen.getByRole("button", { name: "Create journal" }));
    expect(await screen.findByText("must be an ISO 4217 alpha-3 code")).toBeInTheDocument();

    handler = () => json(403, { code: "AUTH-0003", detail: "the legal entity is outside your scope" });
    fireEvent.click(screen.getByRole("button", { name: "Create journal" }));
    expect(await screen.findByText("the legal entity is outside your scope (AUTH-0003)")).toBeInTheDocument();
  });
});

describe("Journal detail", () => {
  it("shows a loading state, then the header, lines and add-line form of a draft", async () => {
    handler = () => json(200, journal());
    const { container } = renderAt(`/inventory/journals/${JOURNAL_ID}`);
    expect(screen.getByText("Loading journal…")).toBeInTheDocument();
    expect(await screen.findByRole("heading", { name: "Movement journal" })).toBeInTheDocument();
    expect(screen.getByRole("table", { name: "Journal lines" })).toHaveTextContent("Site S1 · Warehouse W1");
    expect(screen.getByRole("heading", { name: "Add line" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("adds a line and shows it", async () => {
    handler = (_url, init) =>
      init.method === "POST"
        ? json(201, journal({ version: 3, lines: [...journal().lines, { id: "l2", lineNo: 2, productId: PRODUCT_ID, dimensions: { site: "S2" }, qty: "-4", enteredQty: "-4", enteredUnit: "EA", costPrice: "0" }] }))
        : json(200, journal());
    renderAt(`/inventory/journals/${JOURNAL_ID}`);
    await screen.findByRole("heading", { name: "Add line" });
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: PRODUCT_ID } });
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "-4" } });
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: "S2" } });
    fireEvent.click(screen.getByRole("button", { name: "Add line" }));
    await waitFor(() => expect(screen.getByRole("table", { name: "Journal lines" })).toHaveTextContent("Site S2"));
    const sent = JSON.parse(calls.find((c) => c.method === "POST")?.init.body as string);
    expect(sent.lines[0]).toMatchObject({ productId: PRODUCT_ID, qty: "-4", enteredUnit: "EA", dimensions: { site: "S2" } });
  });

  it("checks lines before sending them", () => {
    const base = { productId: PRODUCT_ID, qty: "2", enteredUnit: "EA", costPrice: "", dimensions: { site: "S1", warehouse: "", location: "", batch: "", serial: "", inventory_status: "" } };
    expect(validateLine(base)).toEqual({});
    expect(validateLine({ ...base, dimensions: { ...base.dimensions, site: "" } })).toEqual({ site: "Site is required on every line." });
    expect(validateLine({ ...base, dimensions: { ...base.dimensions, serial: "SN-1" } }).qty).toBe("A serial number moves one unit at a time: use 1 or -1.");
    expect(validateLine({ ...base, qty: "0", costPrice: "-1" })).toMatchObject({ qty: expect.any(String), costPrice: "Enter a cost of zero or more." });
  });

  it("posts with the shown version and shows the GL entry", async () => {
    const posted = journal({
      status: "POSTED",
      version: 3,
      postedAt: "2026-09-01T09:00:00Z",
      glEntry: { currency: "USD", ledgerEntryId: null, lines: [{ account: "1300", debit: "30", credit: "0" }, { account: "500100", debit: "0", credit: "30" }] },
    });
    handler = (_url, init) => (init.method === "POST" ? json(200, posted) : json(200, journal()));
    renderAt(`/inventory/journals/${JOURNAL_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Post journal" }));
    expect(await screen.findByRole("table", { name: "GL entry" })).toHaveTextContent("500100");
    expect(screen.getByRole("status")).toHaveTextContent("Journal posted: 1 line.");
    expect(screen.queryByRole("button", { name: "Post journal" })).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Add line" })).not.toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe(`/api/v1/inventory/journals/${JOURNAL_ID}:post`);
    expect(new Headers(post?.init.headers).get("If-Match")).toBe('"2"');
  });

  it("explains a rejected posting such as negative stock", async () => {
    handler = (_url, init) =>
      init.method === "POST"
        ? json(422, { code: "INV-NEG-001", detail: "issue would make available physical stock negative for product P at site S1 (shortfall 4)" })
        : json(200, journal());
    renderAt(`/inventory/journals/${JOURNAL_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Post journal" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("The journal was not posted");
    expect(alert).toHaveTextContent("(shortfall 4) (INV-NEG-001)");
    expect(screen.getByRole("button", { name: "Post journal" })).toBeInTheDocument();
  });

  it("does not offer posting for an empty draft", async () => {
    handler = () => json(200, journal({ lines: [], lineCount: 0 }));
    renderAt(`/inventory/journals/${JOURNAL_ID}`);
    expect(await screen.findByRole("button", { name: "Post journal" })).toBeDisabled();
    expect(screen.getByText("No lines yet")).toBeInTheDocument();
  });

  it("shows a not-found state for an unknown journal", async () => {
    handler = () => json(404, { code: "INV-JNL-NOT-FOUND", detail: "inventory journal not found" });
    renderAt(`/inventory/journals/${JOURNAL_ID}`);
    expect(await screen.findByRole("heading", { name: "Journal not found" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Back to journals" })).toHaveAttribute("href", "/inventory/journals");
  });
});
