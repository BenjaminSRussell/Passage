import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import sales from "./module";
import { changeValuesOf, rebaseChange, validateChange } from "./QuotationMaintenance";
import type { Quotation } from "./api/client";

const QUOTE_ID = "77777777-0000-4000-8000-000000000007";
const UUID_A = "33333333-0000-4000-8000-000000000003";
const UUID_B = "44444444-0000-4000-8000-000000000004";
const UUID_C = "55555555-0000-4000-8000-000000000005";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);

type Reply = { status: number; body: unknown; etag?: string };
let handler: (url: URL, init: RequestInit) => Reply;
let calls: { method: string; url: URL; headers: Headers; body: unknown }[];

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, headers: new Headers(init.headers), body: init.body ? JSON.parse(init.body as string) : undefined });
      const { status, body, etag } = handler(url, init);
      const headers: Record<string, string> = { "Content-Type": "application/json" };
      if (etag) headers.ETag = etag;
      return new Response(JSON.stringify(body), { status, headers });
    }),
  );
});

afterEach(() => vi.unstubAllGlobals());

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {sales.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

function line(lineNo: number) {
  return {
    id: `l${lineNo}`,
    lineNo,
    productId: UUID_A,
    quantity: String(lineNo),
    unit: "EA",
    siteId: UUID_B,
    warehouseId: UUID_C,
    unitPrice: "10",
    discPct: "0",
    lineNet: String(10 * lineNo),
    requestedShipDate: "2099-03-04",
    lineStatus: "OPEN",
  };
}

function quotation(overrides: Record<string, unknown> = {}): Quotation {
  return {
    id: QUOTE_ID,
    legalEntityId: UUID_B,
    quotationNumber: "SQ-0000012",
    customerId: UUID_C,
    invoiceAccountId: UUID_C,
    currency: "USD",
    status: "DRAFT",
    validUntil: "2099-03-31",
    probabilityPct: 60,
    netTotal: "30",
    version: 1,
    createdBy: "sales-rep",
    createdAt: "2027-03-01T09:00:00Z",
    updatedAt: "2027-03-01T09:00:00Z",
    lines: [line(1), line(2)],
    lineCount: 2,
    openLines: 2,
    ...overrides,
  } as Quotation;
}

describe("validateChange", () => {
  it("returns only what differs, and refuses a change that changes nothing", () => {
    const q = quotation();
    const values = changeValuesOf(q);
    expect(validateChange(values, q, "2027-03-01")).toEqual({ errors: { body: "Nothing differs from the stored quotation." } });
    const same = { ...values, lines: values.lines.map((l) => ({ ...l, quantity: `${l.quantity}.000000` })) };
    expect(validateChange(same, q, "2027-03-01").input).toBeUndefined();
    const { input } = validateChange({ ...values, validUntil: "2099-04-30", probabilityPct: "75" }, q, "2027-03-01");
    expect(input).toEqual({ validUntil: "2099-04-30", probabilityPct: 75 });
  });

  it("sends the whole line set when a line differs, but never lines for a sent quotation", () => {
    const draft = quotation();
    const values = changeValuesOf(draft);
    values.lines[1] = { ...values.lines[1], unitPrice: "9.50" };
    const { input } = validateChange(values, draft, "2027-03-01");
    expect(input?.lines).toHaveLength(2);
    expect(input?.lines?.[1].unitPrice).toBe("9.50");
    const sent = quotation({ status: "SENT" });
    const sentValues = changeValuesOf(sent);
    sentValues.lines[0] = { ...sentValues.lines[0], unitPrice: "1" };
    expect(validateChange(sentValues, sent, "2027-03-01").errors.body).toBeDefined();
  });

  it("rebases on a re-read quotation: edited fields stay, untouched ones take the stored value", () => {
    const before = quotation();
    const after = quotation({ validUntil: "2099-06-30", probabilityPct: 70, lines: [line(1)], version: 2 });
    const edited = { ...changeValuesOf(before), probabilityPct: "15" };
    const rebased = rebaseChange(edited, before, after);
    expect(rebased.validUntil).toBe("2099-06-30");
    expect(rebased.probabilityPct).toBe("15");
    expect(rebased.lines).toHaveLength(1);
    const ownLines = { ...changeValuesOf(before), lines: [{ ...changeValuesOf(before).lines[0], unitPrice: "12" }] };
    expect(rebaseChange(ownLines, before, after).lines[0].unitPrice).toBe("12");
  });

  it("compares long decimals exactly", () => {
    const q = quotation({ lines: [{ ...line(1), unitPrice: "12345678901234567.000001" }] });
    const values = changeValuesOf(q);
    values.lines[0] = { ...values.lines[0], unitPrice: "12345678901234567.000002" };
    expect(validateChange(values, q, "2027-03-01").input?.lines).toHaveLength(1);
  });

  it("checks validity, probability and lines as the service does", () => {
    const q = quotation();
    const values = changeValuesOf(q);
    const { errors } = validateChange({ ...values, validUntil: "2027-02-28", probabilityPct: "101", lines: [{ ...values.lines[0], unitPrice: "ten" }] }, q, "2027-03-01");
    expect(Object.keys(errors).sort()).toEqual(["lines[0].unitPrice", "probabilityPct", "validUntil"]);
  });
});

describe("Quotation maintenance on the detail page", () => {
  it("saves a changed validity and probability with If-Match and reloads", async () => {
    let current = quotation();
    handler = (_url, init) => {
      if (init.method === "PATCH") {
        current = quotation({ validUntil: "2099-05-01", probabilityPct: 80, version: 2 });
        return { status: 200, body: current, etag: '"2"' };
      }
      return { status: 200, body: current, etag: `"${current.version}"` };
    };
    const { container } = renderAt(`/sales/quotations/${QUOTE_ID}`);
    expect(await screen.findByRole("form", { name: "Change the quotation" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("Nothing differs from the stored quotation.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Valid until"), { target: { value: "2099-05-01" } });
    fireEvent.change(screen.getByLabelText("Win probability %"), { target: { value: "80" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("Quotation changed.")).toBeInTheDocument();
    const patch = calls.find((c) => c.method === "PATCH");
    expect(patch?.url.pathname).toBe(`/api/v1/sales/quotations/${QUOTE_ID}`);
    expect(patch?.headers.get("If-Match")).toBe('"1"');
    expect(patch?.body).toEqual({ validUntil: "2099-05-01", probabilityPct: 80 });
    expect(await screen.findByText("80%")).toBeInTheDocument();
    expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]);
  });

  it("on a version conflict re-reads the quotation, keeps the user's edit, takes the other change and saves with the new version", async () => {
    // Someone else extended the validity to 2099-06-30 (version 2) after this page read version 1.
    let reads = 0;
    handler = (_url, init) => {
      if (init.method === "PATCH") {
        if (init.headers && new Headers(init.headers).get("If-Match") === '"1"') {
          return { status: 412, body: { code: "SLS-0006", detail: "the resource was changed by another user; reload and reapply" } };
        }
        return { status: 200, body: quotation({ validUntil: "2099-06-30", probabilityPct: 10, version: 3 }), etag: '"3"' };
      }
      reads += 1;
      return reads === 1
        ? { status: 200, body: quotation(), etag: '"1"' }
        : { status: 200, body: quotation({ validUntil: "2099-06-30", version: 2 }), etag: '"2"' };
    };
    renderAt(`/sales/quotations/${QUOTE_ID}`);
    await screen.findByRole("form", { name: "Change the quotation" });
    fireEvent.change(screen.getByLabelText("Win probability %"), { target: { value: "10" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText(/SLS-0006/)).toBeInTheDocument();
    expect(screen.getByText(/it has been reloaded and your changes are kept/)).toBeInTheDocument();
    await waitFor(() => expect(screen.getByLabelText("Valid until")).toHaveValue("2099-06-30"));
    expect(screen.getByLabelText("Win probability %")).toHaveValue("10");
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("Quotation changed.")).toBeInTheDocument();
    const patches = calls.filter((c) => c.method === "PATCH");
    expect(patches).toHaveLength(2);
    expect(patches[1].headers.get("If-Match")).toBe('"2"');
    // Only the user's own edit is sent: the other user's validity is not undone.
    expect(patches[1].body).toEqual({ probabilityPct: 10 });
  });

  it("keeps a sent quotation's lines read-only", async () => {
    handler = () => ({ status: 200, body: quotation({ status: "SENT", version: 2 }), etag: '"2"' });
    renderAt(`/sales/quotations/${QUOTE_ID}`);
    await screen.findByRole("form", { name: "Change the quotation" });
    expect(screen.getByText(/A sent quotation keeps its lines and prices/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Add line" })).not.toBeInTheDocument();
  });

  it("cancels only with a reason and says so", async () => {
    let current = quotation({ status: "SENT", version: 2 });
    handler = (_url, init) => {
      if (init.method === "POST") {
        current = quotation({ status: "CANCELLED", cancelReason: "customer withdrew", version: 3 });
        return { status: 200, body: current, etag: '"3"' };
      }
      return { status: 200, body: current, etag: `"${current.version}"` };
    };
    renderAt(`/sales/quotations/${QUOTE_ID}`);
    await screen.findByRole("form", { name: "Cancel the quotation" });
    fireEvent.click(screen.getByRole("button", { name: "Cancel quotation" }));
    expect(await screen.findByText(/Give a reason of 1 to 200 characters/)).toBeInTheDocument();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    fireEvent.change(screen.getByLabelText("Cancellation reason"), { target: { value: "  customer withdrew " } });
    fireEvent.click(screen.getByRole("button", { name: "Cancel quotation" }));
    expect(await screen.findByText("Quotation cancelled.")).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe(`/api/v1/sales/quotations/${QUOTE_ID}:cancel`);
    expect(post?.headers.get("If-Match")).toBe('"2"');
    expect(post?.body).toEqual({ reason: "customer withdrew" });
    expect(await screen.findByText("customer withdrew")).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByRole("form", { name: "Cancel the quotation" })).not.toBeInTheDocument());
  });

  it("offers no maintenance on a converted or cancelled quotation", async () => {
    handler = () => ({ status: 200, body: quotation({ status: "CONVERTED", version: 3 }), etag: '"3"' });
    renderAt(`/sales/quotations/${QUOTE_ID}`);
    await screen.findByRole("heading", { name: "Sales quotation SQ-0000012" });
    expect(screen.queryByRole("form", { name: "Change the quotation" })).not.toBeInTheDocument();
    expect(screen.queryByRole("form", { name: "Cancel the quotation" })).not.toBeInTheDocument();
  });
});
