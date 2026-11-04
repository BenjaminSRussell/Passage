import { findAlert, findStatus, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { csvCell, onHandCsv } from "./csvExport";
import { parseLinesCsv, splitDelimited } from "./csvLines";
import { GROUP_LABELS } from "./OnHandPage";
import { validateLine } from "./JournalDetailPage";
import inventory from "./module";

const PRODUCT_ID = "5f2c1e7a-0000-4000-8000-000000000002";
const ENTITY_ID = "9e8d7c6b-0000-4000-8000-000000000003";
const JOURNAL_ID = "1a2b3c4d-0000-4000-8000-000000000001";
const COPY_ID = "7e7e7e7e-0000-4000-8000-000000000021";

type Handler = (url: URL, init: RequestInit) => { status: number; body: unknown };
const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

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
afterEach(() => vi.unstubAllGlobals());

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

function journal(overrides: Record<string, unknown> = {}) {
  return {
    id: JOURNAL_ID,
    legalEntityId: ENTITY_ID,
    journalType: "MOVEMENT",
    status: "DRAFT",
    postingDate: "2026-09-01",
    offsetAccountId: "500100",
    currency: "USD",
    version: 1,
    lineCount: 0,
    createdAt: "2026-09-01T08:00:00Z",
    lines: [],
    ...overrides,
  };
}

const header1 = "product\tqty\tunit\tcost\tsite\twarehouse\tbatch";

describe("splitDelimited", () => {
  it("handles quotes, doubled quotes, embedded delimiters and CRLF", () => {
    expect(splitDelimited('a,"b,1","say ""hi"""\r\nc,d,e\r\n\r\n', ",")).toEqual([
      ["a", "b,1", 'say "hi"'],
      ["c", "d", "e"],
    ]);
  });
});

describe("parseLinesCsv", () => {
  it("reads a spreadsheet paste into add-line inputs, defaulting the unit", () => {
    const out = parseLinesCsv(`${header1}\n${PRODUCT_ID}\t12\tkg\t2.5\tS1\tW1\tB7\n${PRODUCT_ID}\t-3\t\t\tS1\t\t`, "MOVEMENT", validateLine);
    expect(out.headerError).toBeUndefined();
    expect(out.errors).toEqual([]);
    expect(out.lines).toHaveLength(2);
    expect(out.lines[0]).toMatchObject({ productId: PRODUCT_ID, qty: "12", enteredUnit: "KG", costPrice: "2.5", dimensions: { site: "S1", warehouse: "W1", batch: "B7" } });
    expect(out.lines[1]).toMatchObject({ qty: "-3", enteredUnit: "EA" });
  });

  it("reads a comma file with a byte order mark and column aliases", () => {
    const out = parseLinesCsv(`﻿Product ID,Quantity,Site\n${PRODUCT_ID},4,S1\n`, "ADJUSTMENT", validateLine);
    expect(out.lines).toHaveLength(1);
    expect(out.lines[0].qty).toBe("4");
  });

  it("reports every bad row with its spreadsheet row number and adds nothing", () => {
    const out = parseLinesCsv(`product,qty,site\nnot-a-uuid,5,S1\n${PRODUCT_ID},0,S1\n${PRODUCT_ID},abc,\n${PRODUCT_ID},2,S1`, "MOVEMENT", validateLine);
    expect(out.lines).toEqual([]);
    expect(out.errors.map((e) => e.row)).toEqual([2, 3, 4]);
    expect(out.errors[0].messages.join(" ")).toMatch(/product id/);
    expect(out.errors[2].messages.join(" ")).toMatch(/number/);
    expect(out.errors[2].messages.join(" ")).toMatch(/Site is required/);
  });

  it("refuses a header it cannot use", () => {
    expect(parseLinesCsv("", "MOVEMENT", validateLine).headerError).toMatch(/header row/);
    expect(parseLinesCsv("product,qty,site,colour\n", "MOVEMENT", validateLine).headerError).toMatch(/Unknown column "colour"/);
    expect(parseLinesCsv("product,qty\n", "MOVEMENT", validateLine).headerError).toMatch(/needs the columns site/);
    expect(parseLinesCsv("product,qty,site,qty\n", "MOVEMENT", validateLine).headerError).toMatch(/appears twice/);
    expect(parseLinesCsv("product,qty,site\n", "MOVEMENT", validateLine).headerError).toMatch(/no lines/);
    expect(parseLinesCsv("product,qty,site,owner\n", "MOVEMENT", validateLine).headerError).toMatch(/ownership change/);
    expect(parseLinesCsv("product,qty,site,toSite\n", "MOVEMENT", validateLine).headerError).toMatch(/transfer/);
    expect(parseLinesCsv("product,qty,site\n", "TRANSFER", validateLine).headerError).toMatch(/toSite/);
  });

  it("carries a transfer's destination and keeps batch, serial and status", () => {
    const out = parseLinesCsv(`product,qty,site,batch,toSite,toWarehouse\n${PRODUCT_ID},4,S1,B1,S2,W9\n${PRODUCT_ID},4,S1,B1,S1,`, "TRANSFER", validateLine);
    expect(out.errors).toHaveLength(1);
    expect(out.errors[0]).toMatchObject({ row: 3 });
    const ok = parseLinesCsv(`product,qty,site,batch,toSite,toWarehouse\n${PRODUCT_ID},4,S1,B1,S2,W9`, "TRANSFER", validateLine);
    expect(ok.lines[0].toDimensions).toMatchObject({ site: "S2", warehouse: "W9", batch: "B1" });
  });
});

describe("Import lines on the journal page", () => {
  it("imports valid rows through lines:bulk and shows the updated lines", async () => {
    const after = journal({
      lineCount: 1,
      lines: [{ id: "l1", lineNo: 1, productId: PRODUCT_ID, dimensions: { site: "S1" }, qty: "12", enteredQty: "12", enteredUnit: "EA", costPrice: "2" }],
    });
    handler = (_url, init) => (init.method === "POST" ? json(200, after) : json(200, journal()));
    const { container } = renderAt(`/inventory/journals/${JOURNAL_ID}`);
    fireEvent.change(await screen.findByLabelText("Rows"), { target: { value: `product,qty,cost,site\n${PRODUCT_ID},12,2,S1` } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Import lines" }));
    expect(await findStatus()).toHaveTextContent("Imported 1 line.");
    const post = sent("POST")[0];
    expect(post.url.pathname).toBe(`/api/v1/inventory/journals/${JOURNAL_ID}/lines:bulk`);
    expect(bodyOf(post).lines).toHaveLength(1);
    expect(bodyOf(post).lines[0]).toMatchObject({ productId: PRODUCT_ID, qty: "12", costPrice: "2", dimensions: { site: "S1" } });
    expect(bodyOf(post).lines[0].clientKey).toBeTruthy();
    expect(within(screen.getByRole("table", { name: "Journal lines" })).getByText("12")).toBeInTheDocument();
  });

  it("sends nothing when a row is wrong and says which", async () => {
    handler = () => json(200, journal());
    renderAt(`/inventory/journals/${JOURNAL_ID}`);
    fireEvent.change(await screen.findByLabelText("Rows"), { target: { value: `product,qty,site\n${PRODUCT_ID},12,S1\nbad,1,S1` } });
    fireEvent.click(screen.getByRole("button", { name: "Import lines" }));
    expect(await findAlert()).toHaveTextContent("1 row needs fixing; nothing was added");
    expect(screen.getByText(/Row 3:/)).toBeInTheDocument();
    expect(sent("POST")).toHaveLength(0);
  });

  it("shows the service's refusal", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(422, { code: "INV-JNL-001", detail: "journal exceeds 1000 lines" }) : json(200, journal()));
    renderAt(`/inventory/journals/${JOURNAL_ID}`);
    fireEvent.change(await screen.findByLabelText("Rows"), { target: { value: `product,qty,site\n${PRODUCT_ID},12,S1` } });
    fireEvent.click(screen.getByRole("button", { name: "Import lines" }));
    expect(await findAlert()).toHaveTextContent("journal exceeds 1000 lines (INV-JNL-001)");
  });

  it("sends every row in one request, so a refusal adds nothing, and a retry keeps the line keys", async () => {
    const rows = Array.from({ length: 150 }, (_, i) => `${PRODUCT_ID},${i + 1},S1`).join("\n");
    let fail = true;
    handler = (_url, init) => {
      if (init.method !== "POST") return json(200, journal());
      if (fail) return json(503, { code: "COM-0503", detail: "service unavailable" });
      return json(201, journal({ lineCount: 150 }));
    };
    renderAt(`/inventory/journals/${JOURNAL_ID}`);
    fireEvent.change(await screen.findByLabelText("Rows"), { target: { value: `product,qty,site\n${rows}` } });
    fireEvent.click(screen.getByRole("button", { name: "Import lines" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Nothing was imported");
    expect(alert).toHaveTextContent("No line was added");
    expect(sent("POST")).toHaveLength(1);
    const first = bodyOf(sent("POST")[0]).lines as { clientKey: string }[];
    expect(first).toHaveLength(150);
    expect(new Set(first.map((l) => l.clientKey)).size).toBe(150);

    // The same text again: the service skips any line it already holds by key.
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Import lines" }));
    expect(await findStatus()).toHaveTextContent("Imported 150 lines.");
    expect(sent("POST")).toHaveLength(2);
    expect((bodyOf(sent("POST")[1]).lines as { clientKey: string }[]).map((l) => l.clientKey)).toEqual(first.map((l) => l.clientKey));
  });

  it("names the sheet rows the service refused and uses new keys for edited text", async () => {
    handler = (_url, init) =>
      init.method === "POST"
        ? json(400, { code: "COM-0400", detail: "invalid request", errors: { "lines[1].enteredUnit": "is required (1 to 12 characters of A-Z, 0-9, _ or -)" } })
        : json(200, journal());
    renderAt(`/inventory/journals/${JOURNAL_ID}`);
    fireEvent.change(await screen.findByLabelText("Rows"), { target: { value: `product,qty,site\n${PRODUCT_ID},1,S1\n${PRODUCT_ID},2,S1` } });
    fireEvent.click(screen.getByRole("button", { name: "Import lines" }));
    expect(await findAlert()).toHaveTextContent("Row 3: enteredUnit is required");
    const firstKey = bodyOf(sent("POST")[0]).lines[0].clientKey;
    fireEvent.change(screen.getByLabelText("Rows"), { target: { value: `product,qty,site\n${PRODUCT_ID},1,S1` } });
    fireEvent.click(screen.getByRole("button", { name: "Import lines" }));
    await waitFor(() => expect(sent("POST")).toHaveLength(2));
    expect(bodyOf(sent("POST")[1]).lines[0].clientKey).not.toBe(firstKey);
  });

  it("is not offered on a posted journal", async () => {
    handler = () => json(200, journal({ status: "POSTED" }));
    renderAt(`/inventory/journals/${JOURNAL_ID}`);
    await screen.findByRole("button", { name: "Copy journal" });
    expect(screen.queryByRole("heading", { name: "Import lines" })).toBeNull();
  });
});

describe("Copy journal", () => {
  it("copies a posted journal and opens the new draft", async () => {
    const copy = journal({ id: COPY_ID, postingDate: "2026-10-05" });
    handler = (url, init) => {
      if (init.method === "POST") return json(201, copy);
      return url.pathname.endsWith(COPY_ID) ? json(200, copy) : json(200, journal({ status: "POSTED" }));
    };
    renderAt(`/inventory/journals/${JOURNAL_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Copy journal" }));
    fireEvent.change(screen.getByLabelText("Posting date"), { target: { value: "2026-10-05" } });
    fireEvent.click(screen.getByRole("button", { name: "Create copy" }));
    expect(await screen.findByText("Posting date 2026-10-05 · USD · 0 lines")).toBeInTheDocument();
    const post = sent("POST")[0];
    expect(post.url.pathname).toBe(`/api/v1/inventory/journals/${JOURNAL_ID}:copy`);
    expect(bodyOf(post)).toEqual({ postingDate: "2026-10-05" });
    expect(header(post, "Idempotency-Key")).toBeTruthy();
  });

  it("validates the date and shows a refusal", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(409, { code: "INV-JNL-003", detail: "a reversal cannot be copied" }) : json(200, journal({ status: "POSTED" })));
    renderAt(`/inventory/journals/${JOURNAL_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Copy journal" }));
    fireEvent.change(screen.getByLabelText("Posting date"), { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Create copy" }));
    expect(await screen.findByText("Enter the posting date of the copy.")).toBeInTheDocument();
    expect(sent("POST")).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Posting date"), { target: { value: "2026-10-05" } });
    fireEvent.click(screen.getByRole("button", { name: "Create copy" }));
    expect(await findAlert()).toHaveTextContent("a reversal cannot be copied (INV-JNL-003)");
  });

  it("retries a failed copy under the same key and a new date under a new one", async () => {
    let status = 503;
    handler = (url, init) => {
      if (init.method === "POST") return status === 503 ? json(503, { code: "COM-0503", detail: "service unavailable" }) : json(201, journal({ id: COPY_ID }));
      return url.pathname.endsWith(COPY_ID) ? json(200, journal({ id: COPY_ID })) : json(200, journal({ status: "POSTED" }));
    };
    renderAt(`/inventory/journals/${JOURNAL_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Copy journal" }));
    fireEvent.click(screen.getByRole("button", { name: "Create copy" }));
    expect(await findAlert()).toHaveTextContent("service unavailable");
    fireEvent.click(screen.getByRole("button", { name: "Create copy" }));
    await waitFor(() => expect(sent("POST")).toHaveLength(2));
    expect(header(sent("POST")[1], "Idempotency-Key")).toBe(header(sent("POST")[0], "Idempotency-Key"));
    fireEvent.change(screen.getByLabelText("Posting date"), { target: { value: "2026-10-06" } });
    status = 201;
    fireEvent.click(screen.getByRole("button", { name: "Create copy" }));
    await waitFor(() => expect(sent("POST")).toHaveLength(3));
    expect(header(sent("POST")[2], "Idempotency-Key")).not.toBe(header(sent("POST")[0], "Idempotency-Key"));
  });

  it("is not offered on a reversal", async () => {
    handler = () => json(200, journal({ reversesJournalId: COPY_ID }));
    renderAt(`/inventory/journals/${JOURNAL_ID}`);
    await screen.findByRole("button", { name: "Validate" });
    expect(screen.queryByRole("button", { name: "Copy journal" })).toBeNull();
  });
});

const submitted = (id: string, createdAt: string, extra: Record<string, unknown> = {}) => ({
  id,
  legalEntityId: ENTITY_ID,
  journalType: "ADJUSTMENT",
  status: "SUBMITTED",
  postingDate: "2026-09-02",
  currency: "USD",
  version: 3,
  lineCount: 2,
  createdAt,
  ...extra,
});

describe("Journal approvals", () => {
  const A = "aaaaaaaa-0000-4000-8000-000000000031";
  const B = "bbbbbbbb-0000-4000-8000-000000000032";

  it("lists submitted journals oldest first and approves one at its version", async () => {
    let approved = false;
    handler = (url, init) => {
      if (init.method === "POST") {
        approved = true;
        return json(200, submitted(A, "2026-09-02T08:00:00Z", { status: "APPROVED", version: 4 }));
      }
      expect(url.searchParams.get("status")).toBe("SUBMITTED");
      return json(200, { items: approved ? [submitted(B, "2026-09-01T08:00:00Z")] : [submitted(A, "2026-09-02T08:00:00Z"), submitted(B, "2026-09-01T08:00:00Z")] });
    };
    const { container } = renderAt("/inventory/approvals");
    const table = await findTable({ name: "Journals awaiting approval" });
    expect(pageViolations(container)).toEqual([]);
    const rows = within(table).getAllByRole("row");
    expect(rows[1]).toHaveTextContent("bbbbbbbb");
    expect(rows[2]).toHaveTextContent("aaaaaaaa");
    fireEvent.click(within(rows[2]).getByRole("button", { name: "Approve" }));
    expect(await findStatus()).toHaveTextContent("approved");
    const post = sent("POST")[0];
    expect(post.url.pathname).toBe(`/api/v1/inventory/journals/${A}:approve`);
    expect(header(post, "If-Match")).toBe('"3"');
    await waitFor(() => expect(within(screen.getByRole("table", { name: "Journals awaiting approval" })).queryByText("aaaaaaaa")).toBeNull());
  });

  it("rejects, and shows why a decision was refused", async () => {
    handler = (_url, init) =>
      init.method === "POST" ? json(403, { code: "AUTH-0003", detail: "missing duty inventory.journal.post" }) : json(200, { items: [submitted(A, "2026-09-02T08:00:00Z")] });
    renderAt("/inventory/approvals");
    fireEvent.click(await screen.findByRole("button", { name: "Reject" }));
    expect(await findAlert()).toHaveTextContent("was not rejected");
    expect(sent("POST")[0].url.pathname).toBe(`/api/v1/inventory/journals/${A}:reject`);
  });

  it("says when nothing waits and when loading fails", async () => {
    handler = () => json(200, { items: [] });
    const first = renderAt("/inventory/approvals");
    expect(await screen.findByText("Nothing to approve")).toBeInTheDocument();
    first.unmount();
    handler = () => json(500, { code: "COM-0500", detail: "boom" });
    renderAt("/inventory/approvals");
    expect(await findAlert()).toHaveTextContent("Could not load the approvals");
  });

  it("is in the navigation under the read duty", () => {
    expect(inventory.nav?.find((n) => n.to === "/inventory/approvals")).toMatchObject({ label: "Approvals", duty: "inventory.read" });
  });
});

describe("On-hand drill-down and export", () => {
  const row = { group: { product: PRODUCT_ID, site: "S1" }, physicalInvent: "10", availPhysical: "8", availOrdered: "8", postedValue: "30.00", blocked: "0", currency: "USD", negative: false };

  it("links a product row to its transactions", async () => {
    handler = () => json(200, { items: [row], truncated: false });
    renderAt("/inventory/onhand");
    const link = await screen.findByRole("link", { name: "Transactions" });
    expect(link).toHaveAttribute("href", `/inventory/transactions?product=${PRODUCT_ID}&site=S1`);
  });

  it("opens the transaction inquiry on the linked product", async () => {
    handler = () => json(200, { items: [] });
    renderAt(`/inventory/transactions?product=${PRODUCT_ID}`);
    await screen.findByText(/No transactions|transactions/i);
    expect(calls[0].url.searchParams.get("product")).toBe(PRODUCT_ID);
    expect((screen.getByLabelText(/^Product/) as HTMLInputElement).value).toBe(PRODUCT_ID);
  });

  it("ignores a malformed product in the link", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/inventory/transactions?product=oops");
    await screen.findByLabelText(/^Product/);
    expect(calls[0].url.searchParams.get("product")).toBeNull();
  });

  it("exports the rows shown", async () => {
    const createObjectURL = vi.fn(() => "blob:x");
    vi.stubGlobal("URL", Object.assign(URL, { createObjectURL, revokeObjectURL: vi.fn() }));
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
    handler = () => json(200, { items: [row], truncated: false });
    renderAt("/inventory/onhand");
    await screen.findByRole("link", { name: "Transactions" });
    fireEvent.click(screen.getByRole("button", { name: "Export CSV" }));
    expect(await findStatus()).toHaveTextContent("Exported 1 row.");
    expect(createObjectURL).toHaveBeenCalledTimes(1);
  });

  it("builds a CSV that a spreadsheet cannot run as a formula", () => {
    expect(csvCell("=SUM(A1)")).toBe("'=SUM(A1)");
    expect(csvCell("-12.5")).toBe("-12.5");
    expect(csvCell("a,b")).toBe('"a,b"');
    const csv = onHandCsv([{ ...row, group: { product: PRODUCT_ID, site: "@x" } }], ["product", "site"], false, GROUP_LABELS);
    const [head, line] = csv.trim().split("\r\n");
    expect(head).toBe("Product,Site,Physical,Available physical,Available ordered,Reserved,Reserved on order,Held,Blocked,Posted value,Currency,Negative stock");
    expect(line).toBe(`${PRODUCT_ID},'@x,10,8,8,,,,0,30.00,USD,no`);
    expect(onHandCsv([row], ["product"], true, GROUP_LABELS).split("\r\n")[0]).toBe("Product,Physical,Blocked,Posted value,Currency,Negative stock");
  });
});
