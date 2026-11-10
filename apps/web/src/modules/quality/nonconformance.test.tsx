import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { Correction, CorrectionStep, Nonconformance, OrderView } from "./api/client";
import quality from "./module";
import { validateNcr, type NcrValues } from "./NonconformancesPage";

const NCR = "0195f0a0-0000-7000-8000-000000000071";
const ORDER = "0195f0a0-0000-7000-8000-000000000001";
const ITEM = "0195f0a0-0000-7000-8000-0000000000d1";
const VENDOR = "0195f0a0-0000-7000-8000-0000000000e9";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
function pageViolations(container: HTMLElement) {
  return checkAxeAA(container).filter((v) => !shellRules.has(v.id));
}

let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit; body: unknown }[];
const json = (status: number, body: unknown): Reply => ({ status, body });

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, init, body: init.body ? JSON.parse(String(init.body)) : undefined });
      const reply = handler(url, init) ?? json(404, { code: "TEST-404", detail: `no fake for ${init.method ?? "GET"} ${url.pathname}` });
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
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
        {quality.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
        <Route path="*" element={<p>Elsewhere</p>} />
      </Routes>
    </MemoryRouter>,
  );
}

function header(call: { init: RequestInit }, name: string): string | null {
  return new Headers(call.init.headers).get(name);
}

const ncr: Nonconformance = {
  id: NCR,
  source: "VENDOR",
  itemId: ITEM,
  dims: { batch: "B-7" },
  qty: "10.000000",
  uom: "EA",
  problemType: "USAGE_DECISION_REJECT",
  severity: "MAJOR",
  description: "Raised by the usage decision on quality order 0195f0a0: REJECT 10.",
  qualityOrderId: ORDER,
  vendorId: VENDOR,
  status: "NEW",
  createdBy: "qa-1",
  createdAt: "2026-10-26T10:00:00Z",
  updatedAt: "2026-10-26T10:00:00Z",
  version: 1,
};

const names = ["Team", "Problem description", "Containment", "Root cause", "Corrective action", "Implement and validate", "Prevent recurrence", "Close and recognise"];

function correction(done: number, patch: Partial<Correction> = {}): Correction {
  const steps: CorrectionStep[] = names.map((name, i) => ({
    step: i + 1,
    name,
    ...(i === 2 ? { dueAt: "2026-10-28T10:00:00Z" } : i === 4 ? { dueAt: "2026-11-25T10:00:00Z" } : {}),
    ...(i < done ? { notes: `D${i + 1} notes`, completedBy: "qe-1", completedAt: "2026-10-27T10:00:00Z" } : {}),
  }));
  return {
    id: "0195f0a0-0000-7000-8000-000000000081",
    status: done >= 7 ? "EFFECTIVENESS_PENDING" : "OPEN",
    currentStep: done + 1,
    steps,
    startedBy: "qe-1",
    startedAt: "2026-10-26T10:00:00Z",
    effectivenessDays: 90,
    reopened: 0,
    overdueSteps: [],
    effectivenessCheckDue: false,
    ...patch,
  };
}

describe("nonconformance list", () => {
  it("shows a loading state, then each NCR with severity, order link and status", async () => {
    handler = (url) => (url.pathname === "/api/v1/quality/nonconformances" ? json(200, { items: [ncr] }) : undefined);
    const { container } = renderAt("/quality/nonconformances");
    expect(screen.getByRole("table", { name: "Nonconformances" })).toHaveAttribute("aria-busy", "true");
    const table = await findTable({ name: "Nonconformances" });
    await waitFor(() => expect(within(table).getByText("Major")).toBeInTheDocument());
    expect(within(table).getByRole("link", { name: "Open NCR 0195f0a0" })).toHaveAttribute("href", `/quality/nonconformances/${NCR}`);
    expect(within(table).getByText("10 EA")).toBeInTheDocument();
    expect(within(table).getByText("New")).toBeInTheDocument();
    expect(within(table).getByText("Vendor")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("sends the filters as query parameters and explains an empty filtered list", async () => {
    handler = (url) => (url.pathname === "/api/v1/quality/nonconformances" ? json(200, { items: [] }) : undefined);
    renderAt("/quality/nonconformances?status=IN_CORRECTION");
    expect(await screen.findByText("No nonconformances match")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Severity"), { target: { value: "CRITICAL" } });
    await waitFor(() => expect(calls.at(-1)?.url.search).toBe("?status=IN_CORRECTION&severity=CRITICAL"));
  });

  it("shows the empty state without filters and an error with retry", async () => {
    let fail = true;
    handler = (url) =>
      url.pathname === "/api/v1/quality/nonconformances" ? (fail ? json(500, { code: "COM-0500", detail: "database unavailable" }) : json(200, { items: [] })) : undefined;
    renderAt("/quality/nonconformances");
    expect(await screen.findByText("database unavailable")).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No nonconformances yet")).toBeInTheDocument();
  });
});

describe("raise a nonconformance", () => {
  it("prefills the quality order, posts with an Idempotency-Key and opens the new NCR", async () => {
    handler = (url, init) => {
      if (url.pathname === "/api/v1/quality/nonconformances" && init.method === "POST") return json(201, { ...ncr, source: "INTERNAL" });
      if (url.pathname === `/api/v1/quality/nonconformances/${NCR}`) return json(200, ncr);
      if (url.pathname === "/api/v1/quality/quality-orders") return json(200, { items: [{ id: ORDER, trigger: "PURCHASE_RECEIPT" }] });
      return undefined;
    };
    const { container } = renderAt(`/quality/nonconformances/new?qualityOrderId=${ORDER}`);
    const form = screen.getByRole("form", { name: "Raise a nonconformance" });
    await within(form).findByRole("option", { name: "0195f0a0 · Purchase receipt" });
    expect(within(form).getByLabelText("Quality order")).toHaveValue(ORDER);
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(within(form).getByLabelText("Problem type"), { target: { value: "CRACK" } });
    fireEvent.change(within(form).getByLabelText("Nonconforming quantity"), { target: { value: "4" } });
    fireEvent.click(within(form).getByRole("button", { name: "Raise nonconformance" }));
    expect(await screen.findByRole("heading", { name: "Nonconformance 0195f0a0" })).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.body).toEqual({ source: "INTERNAL", severity: "MAJOR", problemType: "CRACK", qty: "4", qualityOrderId: ORDER });
    expect(header(post!, "Idempotency-Key")).toBeTruthy();
  });

  it("checks the form before posting and shows the service's field errors", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/quality/quality-orders"
        ? json(200, { items: [{ id: ORDER, trigger: "PURCHASE_RECEIPT" }] })
        : url.pathname === "/api/v1/quality/nonconformances"
        ? json(400, { code: "COM-0001", detail: "request failed validation", errors: { qty: "must be at most the quality order's lot quantity 100" } })
        : undefined;
    renderAt("/quality/nonconformances/new");
    const form = screen.getByRole("form", { name: "Raise a nonconformance" });
    fireEvent.click(within(form).getByRole("button", { name: "Raise nonconformance" }));
    expect(await within(form).findByText(/Choose an item/)).toBeInTheDocument();
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(0);
    await within(form).findByRole("option", { name: "0195f0a0 · Purchase receipt" });
    fireEvent.change(within(form).getByLabelText("Quality order"), { target: { value: ORDER } });
    fireEvent.change(within(form).getByLabelText("Problem type"), { target: { value: "CRACK" } });
    fireEvent.change(within(form).getByLabelText("Nonconforming quantity"), { target: { value: "400" } });
    fireEvent.click(within(form).getByRole("button", { name: "Raise nonconformance" }));
    expect(await within(form).findByText("must be at most the quality order's lot quantity 100")).toBeInTheDocument();
  });
});

describe("nonconformance detail", () => {
  it("approves a NEW report with If-Match and then offers the 8D", async () => {
    handler = (url, init) => {
      if (url.pathname.endsWith(":approve")) return json(200, { ...ncr, status: "APPROVED", version: 2 });
      if (url.pathname === `/api/v1/quality/nonconformances/${NCR}` && !init.method) return json(200, ncr);
      return undefined;
    };
    const { container } = renderAt(`/quality/nonconformances/${NCR}`);
    expect(await screen.findByRole("heading", { name: "Nonconformance 0195f0a0" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: ORDER })).toHaveAttribute("href", `/quality/orders/${ORDER}`);
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Approve" }));
    expect(await screen.findByRole("button", { name: "Start 8D" })).toBeInTheDocument();
    const approve = calls.find((c) => c.url.pathname.endsWith(":approve"));
    expect(header(approve!, "If-Match")).toBe('"1"');
  });

  it("needs a reason to refuse and shows a stale version as a reload hint", async () => {
    handler = (url, init) => {
      if (url.pathname.endsWith(":refuse")) return json(412, { code: "conflict_version", detail: "current version is 2, not 1" });
      if (!init.method) return json(200, ncr);
      return undefined;
    };
    renderAt(`/quality/nonconformances/${NCR}`);
    const form = await screen.findByRole("form", { name: "Review the nonconformance" });
    fireEvent.click(within(form).getByRole("button", { name: "Refuse" }));
    expect(await within(form).findByText(/Enter why the report is refused/)).toBeInTheDocument();
    expect(calls.some((c) => c.url.pathname.endsWith(":refuse"))).toBe(false);
    fireEvent.change(within(form).getByLabelText(/Refusal reason/), { target: { value: "duplicate" } });
    fireEvent.click(within(form).getByRole("button", { name: "Refuse" }));
    expect(await within(form).findByText("Someone else changed this nonconformance")).toBeInTheDocument();
    expect(calls.find((c) => c.url.pathname.endsWith(":refuse"))?.body).toEqual({ reason: "duplicate" });
  });

  it("starts the 8D with the effectiveness period, an Idempotency-Key and If-Match", async () => {
    const approved = { ...ncr, status: "APPROVED" as const, version: 2 };
    handler = (url, init) => {
      if (url.pathname.endsWith("/corrections")) return json(201, { ...approved, status: "IN_CORRECTION", version: 3, correction: correction(0) });
      if (!init.method) return json(200, approved);
      return undefined;
    };
    renderAt(`/quality/nonconformances/${NCR}`);
    const form = await screen.findByRole("form", { name: "Start the 8D correction" });
    fireEvent.change(within(form).getByLabelText(/Effectiveness check after/), { target: { value: "20" } });
    fireEvent.click(within(form).getByRole("button", { name: "Start 8D" }));
    expect(await within(form).findByText("Enter 30 to 365 days.")).toBeInTheDocument();
    fireEvent.change(within(form).getByLabelText(/Effectiveness check after/), { target: { value: "60" } });
    fireEvent.click(within(form).getByRole("button", { name: "Start 8D" }));
    expect(await screen.findByRole("list", { name: "8D steps" })).toBeInTheDocument();
    const start = calls.find((c) => c.url.pathname.endsWith("/corrections"));
    expect(start?.body).toEqual({ effectivenessCheckDays: 60 });
    expect(header(start!, "If-Match")).toBe('"2"');
    expect(header(start!, "Idempotency-Key")).toBeTruthy();
  });

  it("lists the 8D steps with due dates and overdue flags and completes the current step", async () => {
    const open = { ...ncr, status: "IN_CORRECTION" as const, version: 5, correction: correction(2, { overdueSteps: [3] }) };
    handler = (url, init) => {
      if (url.pathname.endsWith(":complete-step")) return json(200, { ...open, version: 6, correction: correction(3) });
      if (!init.method) return json(200, open);
      return undefined;
    };
    const { container } = renderAt(`/quality/nonconformances/${NCR}`);
    const steps = await screen.findByRole("list", { name: "8D steps" });
    const items = within(steps).getAllByRole("listitem");
    expect(items).toHaveLength(8);
    expect(items[2]).toHaveTextContent("D3 Containment");
    expect(items[2]).toHaveTextContent("Due 2026-10-28");
    expect(within(items[2]).getByText("Overdue")).toBeInTheDocument();
    expect(items[2]).toHaveAttribute("aria-current", "step");
    expect(screen.getByText("D3 is past due.")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    const form = screen.getByRole("form", { name: "Complete D3" });
    fireEvent.click(within(form).getByRole("button", { name: "Complete D3" }));
    expect(await within(form).findByText(/Record what was done/)).toBeInTheDocument();
    fireEvent.change(within(form).getByLabelText("D3 Containment"), { target: { value: "Quarantined 40 units at dock" } });
    fireEvent.click(within(form).getByRole("button", { name: "Complete D3" }));
    expect(await screen.findByRole("form", { name: "Complete D4" })).toBeInTheDocument();
    const post = calls.find((c) => c.url.pathname.endsWith(":complete-step"));
    expect(post?.body).toEqual({ step: 3, notes: "Quarantined 40 units at dock" });
    expect(header(post!, "If-Match")).toBe('"5"');
  });

  it("shows the scheduled effectiveness check, then the check form once it is due", async () => {
    const pending = { ...ncr, status: "IN_CORRECTION" as const, version: 10, correction: correction(7, { effectivenessDueAt: "2027-01-24T10:00:00Z" }) };
    handler = (url, init) => (!init.method && url.pathname.endsWith(NCR) ? json(200, pending) : undefined);
    const first = renderAt(`/quality/nonconformances/${NCR}`);
    expect(await screen.findByText("The check falls due on 2027-01-24.")).toBeInTheDocument();
    expect(screen.queryByRole("form", { name: "Effectiveness check" })).toBeNull();
    first.unmount();

    const due = { ...pending, correction: { ...pending.correction, effectivenessCheckDue: true } };
    handler = (url, init) => {
      if (url.pathname.endsWith(":check-effectiveness"))
        return json(422, { code: "QM-0175", detail: "the effectiveness check must be made by a user other than the one who completed D7" });
      if (!init.method) return json(200, due);
      return undefined;
    };
    renderAt(`/quality/nonconformances/${NCR}`);
    const form = await screen.findByRole("form", { name: "Effectiveness check" });
    fireEvent.change(within(form).getByLabelText("Result"), { target: { value: "NOT_EFFECTIVE" } });
    fireEvent.change(within(form).getByLabelText("Evidence"), { target: { value: "Recurred on lot 9" } });
    fireEvent.click(within(form).getByRole("button", { name: "Record effectiveness check" }));
    expect(await within(form).findByText(/must be made by a user other than the one who completed D7/)).toBeInTheDocument();
    expect(calls.find((c) => c.url.pathname.endsWith(":check-effectiveness"))?.body).toEqual({ result: "NOT_EFFECTIVE", note: "Recurred on lot 9" });
  });

  it("shows a missing NCR as an error with retry", async () => {
    handler = () => json(404, { code: "QM-0170", detail: "nonconformance not found" });
    renderAt(`/quality/nonconformances/${NCR}`);
    expect(await screen.findByText("nonconformance not found")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

describe("quality order with a reject decision", () => {
  it("links to the NCR the decision raised and offers an amending NCR", async () => {
    const order: OrderView = {
      id: ORDER,
      trigger: "PURCHASE_RECEIPT",
      sourceRef: { type: "receipt_line", id: ITEM },
      itemId: ITEM,
      siteId: ITEM,
      warehouseId: ITEM,
      dims: {},
      lotQty: "100",
      sampleQty: "1",
      uom: "EA",
      testGroupId: ITEM,
      testGroupVersion: 1,
      samplingMethod: "FIXED",
      severity: "NORMAL",
      codeLetter: "",
      acceptNo: 0,
      rejectNo: 1,
      status: "CLOSED",
      version: 4,
      createdAt: "2026-10-26T08:00:00Z",
      certificateRequired: "NONE",
      certificateStatus: "NOT_REQUIRED",
      testGroup: { id: ITEM, code: "GRP", version: 1, tests: [] },
      results: [],
      evaluation: { defectiveSamples: 1, passed: false, missingRequired: [] },
      decision: {
        id: "d",
        orderId: ORDER,
        lines: [{ decision: "ACCEPT", quantity: "90" }, { decision: "REJECT", quantity: "10" }],
        split: { acceptQty: "90", rejectQty: "10", deviationQty: "0" },
        decidedBy: "qa-1",
        decidedAt: "2026-10-26T10:00:00Z",
        nonconformanceId: NCR,
      },
    };
    handler = (url) => (url.pathname.endsWith(`/quality-orders/${ORDER}`) ? json(200, order) : json(200, { items: [] }));
    renderAt(`/quality/orders/${ORDER}`);
    expect(await screen.findByRole("link", { name: "Open NCR 0195f0a0 for the rejected quantity" })).toHaveAttribute("href", `/quality/nonconformances/${NCR}`);
    expect(screen.getByRole("link", { name: "Amend through a nonconformance" })).toHaveAttribute("href", `/quality/nonconformances/new?qualityOrderId=${ORDER}`);
  });
});

describe("validateNcr", () => {
  const base: NcrValues = { source: "INTERNAL", severity: "MINOR", problemType: "CRACK", qualityOrderId: "", itemId: ITEM, qty: "2.5", uom: "EA", vendorId: "", customerId: "", description: "" };

  it("builds the request with only the fields that are set", () => {
    expect(validateNcr({ ...base, description: "  hairline  " }).input).toEqual({ source: "INTERNAL", severity: "MINOR", problemType: "CRACK", qty: "2.5", itemId: ITEM, uom: "EA", description: "hairline" });
  });

  it("needs an item and unit unless a quality order supplies them", () => {
    expect(Object.keys(validateNcr({ ...base, itemId: "", uom: "" }).errors).sort()).toEqual(["itemId", "uom"]);
    expect(validateNcr({ ...base, itemId: "", uom: "", qualityOrderId: ORDER }).input).toEqual({ source: "INTERNAL", severity: "MINOR", problemType: "CRACK", qty: "2.5", qualityOrderId: ORDER });
  });

  it("rejects codes, quantities and links the service would refuse", () => {
    const { errors } = validateNcr({ ...base, problemType: "crack", qty: "0", vendorId: "v-1", qualityOrderId: "qo" });
    expect(Object.keys(errors).sort()).toEqual(["problemType", "qty", "qualityOrderId", "vendorId"]);
    expect(validateNcr({ ...base, qty: "1.1234567" }).errors.qty).toBeTruthy();
  });
});
