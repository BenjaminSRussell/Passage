import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { CoA, OrderView, ReceiptCheck, Study, TestGroup } from "./api/client";
import { specification, validateCoA } from "./CoAPanel";
import quality from "./module";
import { validateReceipt } from "./ReceivingChecksPage";
import { nextMoves, validateTermination } from "./StabilityStudiesPage";

const STUDY = "0195f0a0-0000-7000-8000-000000000501";
const ORDER = "0195f0a0-0000-7000-8000-000000000502";
const GROUP = "0195f0a0-0000-7000-8000-000000000503";
const PH = "0195f0a0-0000-7000-8000-000000000504";
const ITEM = "0195f0a0-0000-7000-8000-000000000505";
const PO = "0195f0a0-0000-7000-8000-000000000506";
const LINE = "0195f0a0-0000-7000-8000-000000000507";
const SHIP = "0195f0a0-0000-7000-8000-000000000508";
const COA1 = "0195f0a0-0000-7000-8000-000000000509";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit; body: unknown }[];
const json = (status: number, body: unknown): Reply => ({ status, body });
const posts = () => calls.filter((c) => c.method === "POST");

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
      </Routes>
    </MemoryRouter>,
  );
}

function study(patch: Partial<Study> = {}): Study {
  return {
    id: STUDY,
    status: "PLANNED",
    version: 1,
    createdAt: "2026-01-20T08:00:00Z",
    itemId: ITEM,
    batchId: "B-9",
    protocol: "ICH Q1A(R2)",
    condition: "25C/60RH",
    startDate: "2026-01-31T00:00:00Z",
    testGroupId: GROUP,
    pullMonths: [0, 3],
    leadDays: 7,
    windowDays: 3,
    pulls: [
      { months: 0, pullDate: "2026-01-31T00:00:00Z", orderDate: "2026-01-24T00:00:00Z", testGroupId: GROUP, status: "ORDERED" },
      { months: 3, pullDate: "2026-04-30T00:00:00Z", orderDate: "2026-04-23T00:00:00Z", testGroupId: GROUP, status: "PLANNED" },
    ],
    ...patch,
  };
}

describe("stability study lifecycle", () => {
  it("starts a planned study under its ETag and refuses a termination without a reason", async () => {
    let current = study();
    handler = (url, init) => {
      if (url.pathname === `/api/v1/quality/stability-studies/${STUDY}:transition` && init.method === "POST") {
        current = study({ status: "ACTIVE", version: 2, statusChangedBy: "stab-1", statusChangedAt: "2026-02-01T09:00:00Z" });
        return json(200, current);
      }
      if (url.pathname === `/api/v1/quality/stability-studies/${STUDY}`) return json(200, current);
      return undefined;
    };
    const { container } = renderAt(`/quality/stability/${STUDY}`);
    const lifecycle = await screen.findByRole("form", { name: "Terminate the study" });
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(within(lifecycle).getByRole("button", { name: "Terminate the study" }));
    expect(await screen.findByText("Give the reason for terminating the study.")).toBeInTheDocument();
    expect(posts()).toHaveLength(0);

    fireEvent.click(screen.getByRole("button", { name: "Start the study" }));
    await waitFor(() => expect(posts()).toHaveLength(1));
    expect(posts()[0].body).toEqual({ to: "ACTIVE" });
    expect(new Headers(posts()[0].init.headers).get("If-Match")).toBe('"1"');
    expect(await screen.findByRole("button", { name: "Complete the study" })).toBeInTheDocument();
    expect(screen.getByText("Status changed").nextElementSibling).toHaveTextContent("stab-1");
  });

  it("terminates with a reason and shows the cancelled pulls and the final state", async () => {
    let current = study({ status: "ACTIVE", version: 2 });
    handler = (url, init) => {
      if (url.pathname.endsWith(":transition") && init.method === "POST") {
        current = study({
          status: "TERMINATED",
          version: 3,
          statusReason: "Chamber 3 failed",
          pulls: [current.pulls[0], { ...current.pulls[1], status: "CANCELLED" }],
        });
        return json(200, current);
      }
      if (url.pathname === `/api/v1/quality/stability-studies/${STUDY}`) return json(200, current);
      return undefined;
    };
    renderAt(`/quality/stability/${STUDY}`);
    const form = await screen.findByRole("form", { name: "Terminate the study" });
    fireEvent.change(within(form).getByLabelText("Termination reason"), { target: { value: "  Chamber 3 failed " } });
    fireEvent.click(within(form).getByRole("button", { name: "Terminate the study" }));
    await waitFor(() => expect(posts()).toHaveLength(1));
    expect(posts()[0].body).toEqual({ to: "TERMINATED", reason: "Chamber 3 failed" });
    expect(await screen.findByText(/its remaining pulls are cancelled/)).toBeInTheDocument();
    expect(screen.queryByRole("form", { name: "Terminate the study" })).toBeNull();
    const pulls = screen.getByRole("table", { name: "Pull schedule" });
    expect(within(pulls).getByText("Cancelled")).toBeInTheDocument();
    expect(screen.getByText("Reason").nextElementSibling).toHaveTextContent("Chamber 3 failed");
  });

  it("explains a stale version and offers a reload", async () => {
    handler = (url, init) => {
      if (url.pathname.endsWith(":transition") && init.method === "POST") return json(412, { code: "conflict_version", detail: "current version is 2, not 1" });
      if (url.pathname === `/api/v1/quality/stability-studies/${STUDY}`) return json(200, study());
      return undefined;
    };
    renderAt(`/quality/stability/${STUDY}`);
    fireEvent.click(await screen.findByRole("button", { name: "Start the study" }));
    expect(await screen.findByText("The study changed since you opened it")).toBeInTheDocument();
    const gets = calls.filter((c) => c.method === "GET").length;
    fireEvent.click(screen.getByRole("button", { name: "Reload the study" }));
    await waitFor(() => expect(calls.filter((c) => c.method === "GET").length).toBeGreaterThan(gets));
  });

  it("offers the moves of each status like the service", () => {
    expect(nextMoves("PLANNED")).toEqual(["ACTIVE", "TERMINATED"]);
    expect(nextMoves("ACTIVE")).toEqual(["COMPLETED", "TERMINATED"]);
    expect(nextMoves("COMPLETED")).toEqual([]);
    expect(nextMoves("TERMINATED")).toEqual([]);
    expect(validateTermination(" ")).toBeTruthy();
    expect(validateTermination("x".repeat(501))).toBeTruthy();
    expect(validateTermination("recall")).toBeUndefined();
  });
});

describe("calibration due list", () => {
  it("asks for the instruments due within the chosen days and explains an empty list", async () => {
    handler = (url) => {
      if (url.pathname !== "/api/v1/quality/instruments") return undefined;
      return json(200, {
        items: url.searchParams.get("dueWithinDays")
          ? []
          : [{ id: ITEM, code: "PH-01", name: "pH meter", calibrationIntervalDays: 365, lastCalibratedAt: "2025-11-01T00:00:00Z", dueAt: "2026-11-01T00:00:00Z", calibrationStatus: "DUE_SOON" }],
      });
    };
    renderAt("/quality/instruments");
    expect(await screen.findByText("PH-01")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Due within"), { target: { value: "30" } });
    expect(await screen.findByText("No instrument is due within 30 days")).toBeInTheDocument();
    expect(calls.at(-1)?.url.searchParams.get("dueWithinDays")).toBe("30");
  });
});

const skipped: ReceiptCheck = {
  id: "0195f0a0-0000-7000-8000-000000000510",
  poLineId: PO,
  batch: "B",
  receiptLineId: LINE,
  receivedQty: "1000.000000",
  acceptedQty: "1000.000000",
  coveredBefore: "0.000000",
  skip: true,
  coveredQty: "1000.000000",
  normalQty: "0.000000",
  code: "QM-0128",
  message: "Batch B was source-inspected; receiving inspection skipped.",
  checkedBy: "rcv-1",
  checkedAt: "2026-10-26T08:00:00Z",
};

describe("receipts against source inspection", () => {
  it("lists checked receipt lines and records a check that skips receiving inspection", async () => {
    handler = (url, init) => {
      if (url.pathname !== "/api/v1/quality/receipt-checks") return undefined;
      if (init.method === "POST") return json(201, skipped);
      return json(200, { items: [{ ...skipped, id: "0195f0a0-0000-7000-8000-000000000511", skip: false, code: undefined, normalQty: "200", coveredQty: "400", receivedQty: "600" }] });
    };
    const { container } = renderAt("/quality/receiving");
    const table = await screen.findByRole("table", { name: "Checked receipt lines" });
    await waitFor(() => expect(within(table).getByText("Quality order needed")).toBeInTheDocument());
    expect(within(table).getByText("200")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    const form = screen.getByRole("form", { name: "Check a receipt line" });
    fireEvent.click(within(form).getByRole("button", { name: "Check receipt" }));
    expect(await within(form).findByText("Enter the PO line id (a UUID).")).toBeInTheDocument();
    expect(posts()).toHaveLength(0);

    fireEvent.change(within(form).getByLabelText("PO line"), { target: { value: PO } });
    fireEvent.change(within(form).getByLabelText("Batch"), { target: { value: " B " } });
    fireEvent.change(within(form).getByLabelText("Receipt line"), { target: { value: LINE } });
    fireEvent.change(within(form).getByLabelText("Received quantity"), { target: { value: "1000" } });
    fireEvent.click(within(form).getByRole("button", { name: "Check receipt" }));
    expect(await screen.findByText("Receiving inspection skipped")).toBeInTheDocument();
    expect(posts()[0].body).toEqual({ poLineId: PO, batch: "B", receiptLineId: LINE, receivedQty: "1000" });
    expect(new Headers(posts()[0].init.headers).get("Idempotency-Key")).toBeTruthy();
  });

  it("maps a receipt line checked with other values to its field", async () => {
    handler = (url, init) => {
      if (url.pathname !== "/api/v1/quality/receipt-checks") return undefined;
      if (init.method === "POST") return json(409, { code: "QM-0185", detail: "already checked" });
      return json(200, { items: [] });
    };
    renderAt("/quality/receiving");
    expect(await screen.findByText("No receipt lines checked yet")).toBeInTheDocument();
    const form = screen.getByRole("form", { name: "Check a receipt line" });
    fireEvent.change(within(form).getByLabelText("PO line"), { target: { value: PO } });
    fireEvent.change(within(form).getByLabelText("Batch"), { target: { value: "B" } });
    fireEvent.change(within(form).getByLabelText("Receipt line"), { target: { value: LINE } });
    fireEvent.change(within(form).getByLabelText("Received quantity"), { target: { value: "5" } });
    fireEvent.click(within(form).getByRole("button", { name: "Check receipt" }));
    expect(await within(form).findByText("This receipt line was already checked with another PO line, batch or quantity.")).toBeInTheDocument();
  });

  it("shows an error with a retry when the checks cannot load", async () => {
    handler = () => json(503, { code: "COM-0503", detail: "quality is unavailable" });
    renderAt("/quality/receiving");
    expect(await screen.findByText("Could not load the receipt checks")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("checks a receipt like the service", () => {
    const ok = validateReceipt({ poLineId: PO, batch: " B ", receiptLineId: LINE, receivedQty: "2.5" });
    expect(ok.input).toEqual({ poLineId: PO, batch: "B", receiptLineId: LINE, receivedQty: "2.5" });
    const bad = validateReceipt({ poLineId: "PO-1", batch: "", receiptLineId: "", receivedQty: "0" });
    expect(Object.keys(bad.errors).sort()).toEqual(["batch", "poLineId", "receiptLineId", "receivedQty"]);
  });
});

const group: TestGroup = {
  id: GROUP,
  code: "PH",
  version: 2,
  tests: [{ testId: PH, name: "pH", kind: "VARIABLE", required: true, decimals: 1, min: "6.5", max: "7.5", destructive: false }],
};

function closedView(accept = "100", reject = "0"): OrderView {
  return {
    id: ORDER,
    trigger: "PRODUCTION_RAF",
    sourceRef: { type: "raf_line", id: ITEM },
    itemId: ITEM,
    siteId: ITEM,
    warehouseId: ITEM,
    dims: { batch: "B-7" },
    lotQty: "100",
    sampleQty: "1",
    uom: "KG",
    testGroupId: GROUP,
    testGroupVersion: 2,
    samplingMethod: "FIXED",
    severity: "NORMAL",
    codeLetter: "",
    acceptNo: 0,
    rejectNo: 1,
    status: "CLOSED",
    version: 3,
    createdAt: "2026-10-20T08:00:00Z",
    certificateRequired: "NONE",
    certificateStatus: "NOT_REQUIRED",
    testGroup: group,
    results: [{ testId: PH, sampleNo: 1, retestNo: 0, value: "7.1", passed: true, recordedBy: "lab-1", recordedAt: "2026-10-21T08:00:00Z" }],
    evaluation: { defectiveSamples: 0, passed: true, missingRequired: [] },
    decision: {
      id: "0195f0a0-0000-7000-8000-000000000520",
      orderId: ORDER,
      lines: [{ decision: accept === "0" ? "REJECT" : "ACCEPT", quantity: "100" }],
      split: { acceptQty: accept, rejectQty: reject, deviationQty: "0" },
      decidedBy: "qa-release",
      decidedAt: "2026-10-22T08:00:00Z",
    },
  };
}

const coa1: CoA = {
  id: COA1,
  qualityOrderId: ORDER,
  itemId: ITEM,
  batch: "B-7",
  dims: { batch: "B-7" },
  uom: "KG",
  lotQty: "100",
  acceptQty: "100",
  deviationQty: "0",
  deviations: [],
  testGroupId: GROUP,
  testGroupCode: "PH",
  testGroupVersion: 2,
  specificationSet: "DEFAULT",
  revision: 1,
  lines: [{ testId: PH, testName: "pH", kind: "VARIABLE", required: true, decimals: 1, min: "6.5", max: "7.5", sampleNo: 1, retestNo: 0, value: "7.1", passed: true, recordedBy: "lab-1", recordedAt: "2026-10-21T08:00:00Z" }],
  decisionId: "0195f0a0-0000-7000-8000-000000000520",
  signedBy: "qa-release",
  signedAt: "2026-10-22T08:00:00Z",
  issuedBy: "clerk-1",
  issuedAt: "2026-10-23T08:00:00Z",
};

function serveOrder(view: OrderView, coas: CoA[] | Reply) {
  handler = (url, init) => {
    if ((init.method ?? "GET") !== "GET") return undefined;
    if (url.pathname === `/api/v1/quality/quality-orders/${ORDER}`) return json(200, view);
    if (url.pathname === `/api/v1/quality/quality-orders/${ORDER}/coas`) return Array.isArray(coas) ? json(200, { items: coas }) : coas;
    if (url.pathname === `/api/v1/quality/quality-orders/${ORDER}/certificates`) return json(200, { required: "NONE", certificateStatus: "NOT_REQUIRED", items: [] });
    if (url.pathname === "/api/v1/quality/samples" || url.pathname === "/api/v1/quality/instruments") return json(200, { items: [] });
    return undefined;
  };
}

function withPost(path: string, reply: Reply) {
  const prev = handler;
  handler = (url, init) => ((init.method ?? "GET") === "POST" && url.pathname === path ? reply : prev(url, init));
}

describe("certificates of analysis on a closed order", () => {
  it("lists the revisions, previews one, and needs a reason for the next batch revision", async () => {
    serveOrder(closedView(), [coa1]);
    const { container } = renderAt(`/quality/orders/${ORDER}`);
    const table = await screen.findByRole("table", { name: "Certificate of analysis revisions" });
    await waitFor(() => expect(within(table).getByText("Batch certificate")).toBeInTheDocument());
    fireEvent.click(within(table).getByRole("button", { name: "View revision 1" }));
    const doc = await screen.findByRole("region", { name: "Certificate of analysis revision 1" });
    expect(within(doc).getByText("6.5 – 7.5")).toBeInTheDocument();
    expect(within(doc).getByText("Signed").nextElementSibling).toHaveTextContent("qa-release");
    expect(pageViolations(container)).toEqual([]);

    const form = screen.getByRole("form", { name: "Issue a certificate of analysis" });
    fireEvent.click(within(form).getByRole("button", { name: "Issue certificate of analysis" }));
    expect(await within(form).findByText("A certificate already exists; give the reason for the new revision.")).toBeInTheDocument();
    expect(posts()).toHaveLength(0);

    withPost("/api/v1/quality/coa:generate", json(201, { ...coa1, id: "0195f0a0-0000-7000-8000-000000000530", revision: 1, shipmentId: SHIP }));
    fireEvent.change(within(form).getByLabelText("Shipment"), { target: { value: SHIP } });
    fireEvent.click(within(form).getByRole("button", { name: "Issue certificate of analysis" }));
    expect(await screen.findByText("Revision 1 issued for shipment 0195f0a0.")).toBeInTheDocument();
    expect(posts()[0].body).toEqual({ qualityOrderId: ORDER, shipmentId: SHIP });
    expect(new Headers(posts()[0].init.headers).get("Idempotency-Key")).toBeTruthy();
  });

  it("shows the service refusal of a revision without a reason on the reason field", async () => {
    serveOrder(closedView(), []);
    renderAt(`/quality/orders/${ORDER}`);
    expect(await screen.findByText("No certificate issued yet")).toBeInTheDocument();
    withPost("/api/v1/quality/coa:generate", json(422, { code: "QM-0187", detail: "a certificate of analysis already exists; give the reason for the new revision" }));
    const form = screen.getByRole("form", { name: "Issue a certificate of analysis" });
    fireEvent.click(within(form).getByRole("button", { name: "Issue certificate of analysis" }));
    expect(await within(form).findByText(/give the reason for the new revision/)).toBeInTheDocument();
    expect(posts()[0].body).toEqual({ qualityOrderId: ORDER });
  });

  it("offers no certificate for a rejected lot and none on an open order", async () => {
    serveOrder(closedView("0", "100"), []);
    renderAt(`/quality/orders/${ORDER}`);
    expect(await screen.findByText(/released no quantity/)).toBeInTheDocument();
    expect(screen.queryByRole("form", { name: "Issue a certificate of analysis" })).toBeNull();
  });

  it("does not ask for certificates of analysis while the order is open", async () => {
    serveOrder({ ...closedView(), status: "PENDING_DECISION", decision: undefined }, []);
    renderAt(`/quality/orders/${ORDER}`);
    expect(await screen.findByRole("heading", { name: "Quality order 0195f0a0" })).toBeInTheDocument();
    await screen.findByRole("table", { name: "Certificates" });
    expect(screen.queryByText("Certificates of analysis")).toBeNull();
    expect(calls.some((c) => c.url.pathname.endsWith("/coas"))).toBe(false);
  });

  it("shows an error with a retry when the certificates cannot load", async () => {
    serveOrder(closedView(), json(500, { code: "COM-0500", detail: "boom" }));
    renderAt(`/quality/orders/${ORDER}`);
    expect(await screen.findByText("Could not load the certificates of analysis")).toBeInTheDocument();
    expect(screen.queryByRole("form", { name: "Issue a certificate of analysis" })).toBeNull();
  });

  it("checks a CoA request like the service", () => {
    expect(validateCoA(ORDER, { shipmentId: "", customerId: "", reason: "" }, []).input).toEqual({ qualityOrderId: ORDER });
    expect(validateCoA(ORDER, { shipmentId: "", customerId: "", reason: "" }, [coa1]).errors.reason).toBeTruthy();
    expect(validateCoA(ORDER, { shipmentId: SHIP, customerId: "", reason: "" }, [coa1]).input).toEqual({ qualityOrderId: ORDER, shipmentId: SHIP });
    const bad = validateCoA(ORDER, { shipmentId: "S-1", customerId: "C-1", reason: "x".repeat(501) }, []);
    expect(Object.keys(bad.errors).sort()).toEqual(["customerId", "reason", "shipmentId"]);
    expect(specification({ ...coa1.lines[0], max: undefined })).toBe("≥ 6.5");
    expect(specification({ ...coa1.lines[0], kind: "ATTRIBUTE" })).toBe("Passing outcome");
  });
});

describe("slice 5 registration", () => {
  it("registers the receiving page with a duty-gated nav entry", () => {
    expect(quality.routes.map((r) => r.path)).toContain("/quality/receiving");
    expect(quality.nav.find((n) => n.to === "/quality/receiving")?.duty).toBe("QM_VIEW");
  });
});
