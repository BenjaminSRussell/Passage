import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { CertificateList, CertificateRecord, OrderView, Sample, TestGroup } from "./api/client";
import { validateCertificate } from "./CertificatesPanel";
import quality from "./module";
import { validateSample, type SampleValues } from "./SamplesPage";
import { validateDecision } from "./UsageDecisionForm";

const ORDER = "0195f0a0-0000-7000-8000-000000000001";
const GROUP = "0195f0a0-0000-7000-8000-0000000000a1";
const YIELD = "0195f0a0-0000-7000-8000-0000000000b1";
const SURFACE = "0195f0a0-0000-7000-8000-0000000000b2";
const GOOD = "0195f0a0-0000-7000-8000-0000000000c1";
const ITEM = "0195f0a0-0000-7000-8000-0000000000d1";
const NCR = "0195f0a0-0000-7000-8000-000000000071";
const CERT = "0195f0a0-0000-7000-8000-000000000091";
const PRIMARY = "0195f0a0-0000-7000-8000-0000000000s1".replace("s", "a");
const RESERVE = "0195f0a0-0000-7000-8000-0000000000a2";

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

const group: TestGroup = {
  id: GROUP,
  code: "STEEL-3.1",
  version: 1,
  tests: [
    { testId: YIELD, name: "Yield strength", kind: "VARIABLE", required: true, decimals: 0, min: "355", destructive: false },
    { testId: SURFACE, name: "Surface", kind: "ATTRIBUTE", required: true, decimals: 0, passingOutcomeIds: [GOOD], destructive: false },
  ],
};

function view(patch: Partial<OrderView> = {}): OrderView {
  return {
    id: ORDER,
    trigger: "PURCHASE_RECEIPT",
    sourceRef: { type: "receipt_line", id: ITEM },
    itemId: ITEM,
    siteId: ITEM,
    warehouseId: ITEM,
    dims: {},
    lotQty: "40.000000",
    sampleQty: "1.000000",
    uom: "KG",
    testGroupId: GROUP,
    testGroupVersion: 1,
    samplingMethod: "FIXED",
    severity: "NORMAL",
    codeLetter: "",
    acceptNo: 0,
    rejectNo: 1,
    status: "PENDING_DECISION",
    version: 2,
    createdAt: "2026-10-26T08:00:00Z",
    certificateRequired: "EN10204_31",
    certificateStatus: "MISSING",
    testGroup: group,
    results: [],
    evaluation: { defectiveSamples: 0, passed: true, missingRequired: [] },
    ...patch,
  };
}

const nokRecord: CertificateRecord = {
  id: CERT,
  qualityOrderId: ORDER,
  type: "EN10204_31",
  documentRef: "EDI-4711",
  values: { [YIELD]: { value: "340" } },
  inSpec: false,
  deviations: ["Yield strength", "Surface"],
  waiver: false,
  nonconformanceId: NCR,
  recordedBy: "qa-1",
  recordedAt: "2026-10-26T09:00:00Z",
};

const reserve: Sample = {
  id: RESERVE,
  qualityOrderId: ORDER,
  kind: "RESERVE",
  primaryIds: [],
  quantity: "0.500000",
  uom: "KG",
  container: "jar",
  storageLocation: "RET-01",
  condition: "",
  batchExpiresOn: "2025-10-27",
  retentionYears: 1,
  retentionExpiresOn: "2026-10-27",
  status: "DUE_FOR_DISPOSAL",
  recordedBy: "qa-1",
  createdAt: "2026-10-20T09:00:00Z",
};

const primary: Sample = { ...reserve, id: PRIMARY, kind: "PRIMARY", quantity: "1", batchExpiresOn: undefined, retentionExpiresOn: undefined, status: "ACTIVE" };

function serve(order: OrderView, certs: CertificateList, samples: Sample[] = []) {
  handler = (url, init) => {
    const method = init.method ?? "GET";
    if (method !== "GET") return undefined;
    if (url.pathname === `/api/v1/quality/quality-orders/${ORDER}`) return json(200, order);
    if (url.pathname === `/api/v1/quality/quality-orders/${ORDER}/certificates`) return json(200, certs);
    if (url.pathname === "/api/v1/quality/samples") return json(200, { items: samples });
    if (url.pathname === "/api/v1/quality/instruments") return json(200, { items: [] });
    return undefined;
  };
}

function withPost(path: string, reply: Reply) {
  const prev = handler;
  handler = (url, init) => ((init.method ?? "GET") === "POST" && url.pathname === path ? reply : prev(url, init));
}

describe("incoming certificates on the order", () => {
  it("shows the missing certificate, blocks an accepting decision and records an in-specification certificate", async () => {
    serve(view(), { required: "EN10204_31", certificateStatus: "MISSING", items: [] });
    const { container } = renderAt(`/quality/orders/${ORDER}`);
    expect(await screen.findByText("Acceptance is blocked")).toBeInTheDocument();
    expect(await screen.findByText("No certificates yet")).toBeInTheDocument();
    expect(screen.getByText("Vendor certificate").nextElementSibling).toHaveTextContent("EN 10204 3.1 inspection certificate");
    expect(screen.getByRole("form", { name: "Waive the certificate" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Post usage decision" }));
    expect(await screen.findByText(/no quantity can be accepted/)).toBeInTheDocument();
    expect(calls.some((c) => c.method === "POST")).toBe(false);

    withPost(`/api/v1/quality/quality-orders/${ORDER}/certificates`, json(201, { ...nokRecord, inSpec: true, deviations: [], nonconformanceId: undefined, certificateStatus: "RECEIVED_OK" }));
    fireEvent.change(screen.getByLabelText("Document reference"), { target: { value: " EDI-4711 " } });
    fireEvent.change(screen.getByLabelText("Certificate value: Yield strength"), { target: { value: "360" } });
    fireEvent.change(screen.getByLabelText("Certificate value: Surface"), { target: { value: GOOD } });
    fireEvent.click(screen.getByRole("button", { name: "Record certificate" }));
    expect(await screen.findByText("The certificate is in specification.")).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe(`/api/v1/quality/quality-orders/${ORDER}/certificates`);
    expect(post?.body).toEqual({ type: "EN10204_31", documentRef: "EDI-4711", values: { [YIELD]: { value: "360" }, [SURFACE]: { outcome: GOOD } } });
    expect(new Headers(post?.init.headers).get("Idempotency-Key")).toBeTruthy();
    await waitFor(() => expect(calls.filter((c) => c.url.pathname.endsWith("/certificates") && c.method === "GET").length).toBeGreaterThan(1));
  });

  it("lists an out-of-specification certificate with its deviations and the NCR it raised", async () => {
    serve(view({ certificateStatus: "RECEIVED_NOK" }), { required: "EN10204_31", certificateStatus: "RECEIVED_NOK", items: [nokRecord] });
    renderAt(`/quality/orders/${ORDER}`);
    const table = await findTable({ name: "Certificates" });
    await waitFor(() => expect(within(table).getByText("Out of specification")).toBeInTheDocument());
    expect(within(table).getByText("Yield strength, Surface")).toBeInTheDocument();
    expect(within(table).getByRole("link", { name: "Open NCR 0195f0a0" })).toHaveAttribute("href", `/quality/nonconformances/${NCR}`);
    expect(screen.getByText(/raised a vendor nonconformance/)).toBeInTheDocument();
  });

  it("waives the certificate with a reason and refuses a blank one", async () => {
    serve(view(), { required: "EN10204_31", certificateStatus: "MISSING", items: [] });
    renderAt(`/quality/orders/${ORDER}`);
    const form = await screen.findByRole("form", { name: "Waive the certificate" });
    fireEvent.click(within(form).getByRole("button", { name: "Waive the en 10204 3.1 inspection certificate" }));
    expect(await screen.findByText("Give the reason for the waiver, up to 500 characters.")).toBeInTheDocument();
    withPost(`/api/v1/quality/quality-orders/${ORDER}/certificates:waive`, json(201, { ...nokRecord, waiver: true, reason: "audit", certificateStatus: "WAIVED" }));
    fireEvent.change(within(form).getByLabelText("Waiver reason"), { target: { value: "Supplier audit on file" } });
    fireEvent.click(within(form).getByRole("button", { name: "Waive the en 10204 3.1 inspection certificate" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe(`/api/v1/quality/quality-orders/${ORDER}/certificates:waive`);
    expect(post?.body).toEqual({ reason: "Supplier audit on file" });
  });

  it("shows the service refusal and a retry when the certificates cannot load", async () => {
    serve(view({ certificateRequired: "NONE", certificateStatus: "NOT_REQUIRED" }), { required: "NONE", certificateStatus: "NOT_REQUIRED", items: [] });
    const prev = handler;
    handler = (url, init) => (url.pathname.endsWith("/certificates") ? json(503, { code: "COM-0503", detail: "quality is unavailable" }) : prev(url, init));
    renderAt(`/quality/orders/${ORDER}`);
    expect(await screen.findByText("Could not load the certificates")).toBeInTheDocument();
    expect(screen.getByText("quality is unavailable")).toBeInTheDocument();
    expect(screen.queryByRole("form", { name: "Record a certificate" })).toBeNull();
    expect(screen.getByRole("button", { name: "Record a certificate" })).toBeInTheDocument();
  });
});

describe("certificate and decision rules", () => {
  it("checks certificate values like the service", () => {
    const empty = { type: "COA" as const, documentRef: "", values: {}, other: {} };
    expect(validateCertificate(empty, group.tests).input).toEqual({ type: "COA", values: {} });
    const bad = validateCertificate({ ...empty, documentRef: "x".repeat(201), values: { [YIELD]: "12,5", [SURFACE]: "__other__" }, other: { [SURFACE]: "rough" } }, group.tests);
    expect(Object.keys(bad.errors).sort()).toEqual([SURFACE, YIELD, "documentRef"].sort());
    const other = validateCertificate({ ...empty, values: { [SURFACE]: "__other__" }, other: { [SURFACE]: GOOD } }, group.tests);
    expect(other.input?.values).toEqual({ [SURFACE]: { outcome: GOOD } });
  });

  it("refuses accepting lines while the certificate gate is closed, but not a full reject", () => {
    const accept = [{ decision: "ACCEPT" as const, quantity: "40", reason: "" }];
    expect(validateDecision(accept, "40", false).errors.total).toMatch(/no quantity can be accepted/);
    expect(validateDecision([{ decision: "ACCEPT_WITH_DEVIATION", quantity: "40", reason: "cosmetic" }], "40", false).input).toBeUndefined();
    expect(validateDecision([{ decision: "REJECT", quantity: "40", reason: "" }], "40", false).input).toEqual([{ decision: "REJECT", quantity: "40" }]);
    expect(validateDecision(accept, "40").input).toEqual([{ decision: "ACCEPT", quantity: "40" }]);
  });
});

describe("physical samples", () => {
  it("lists every order's samples, filters to those due for disposal, and explains an empty list", async () => {
    handler = (url) => (url.pathname === "/api/v1/quality/samples" ? json(200, { items: url.searchParams.get("status") ? [] : [reserve] }) : undefined);
    const { container } = renderAt("/quality/samples");
    expect(screen.getByRole("table", { name: "Physical samples" })).toHaveAttribute("aria-busy", "true");
    const table = await findTable({ name: "Physical samples" });
    await waitFor(() => expect(within(table).getByText("Due for disposal")).toBeInTheDocument());
    expect(within(table).getByRole("link", { name: "Open order 0195f0a0" })).toHaveAttribute("href", `/quality/orders/${ORDER}`);
    expect(within(table).getByText("0.5 KG")).toBeInTheDocument();
    expect(within(table).getByText("2026-10-27")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "DUE_FOR_DISPOSAL" } });
    expect(await screen.findByText("No samples are due for disposal")).toBeInTheDocument();
    expect(calls.at(-1)?.url.searchParams.get("status")).toBe("DUE_FOR_DISPOSAL");
  });

  it("shows an error with a retry when samples cannot load", async () => {
    handler = () => json(500, { code: "COM-0500", detail: "boom" });
    renderAt("/quality/samples");
    expect(await screen.findByText("Could not load the samples")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("registers a pooled sample from a primary sample of the order and maps the lot refusal to the quantity", async () => {
    serve(view({ certificateRequired: "NONE", certificateStatus: "NOT_REQUIRED" }), { required: "NONE", certificateStatus: "NOT_REQUIRED", items: [] }, [primary, reserve]);
    renderAt(`/quality/orders/${ORDER}`);
    const form = await screen.findByRole("form", { name: "Register a sample" });
    await findTable({ name: "Samples of this order" });
    fireEvent.change(within(form).getByLabelText("Kind"), { target: { value: "RESERVE" } });
    fireEvent.change(within(form).getByLabelText("Quantity (KG)"), { target: { value: "0.5" } });
    fireEvent.click(within(form).getByRole("button", { name: "Register sample" }));
    expect(await within(form).findByText("A retention sample needs the batch expiry date.")).toBeInTheDocument();

    fireEvent.change(within(form).getByLabelText("Kind"), { target: { value: "POOLED" } });
    await waitFor(() => expect(within(form).getByLabelText(/Primary 0195f0a0/)).toBeInTheDocument());
    fireEvent.click(within(form).getByLabelText(/Primary 0195f0a0/));
    fireEvent.change(within(form).getByLabelText("Quantity (KG)"), { target: { value: "1" } });
    withPost("/api/v1/quality/samples", json(422, { code: "QM-0182", detail: "samples drawn (41) would exceed the lot quantity 40" }));
    fireEvent.click(within(form).getByRole("button", { name: "Register sample" }));
    expect(await within(form).findByText("samples drawn (41) would exceed the lot quantity 40")).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.body).toEqual({ qualityOrderId: ORDER, kind: "POOLED", quantity: "1", primaryIds: [PRIMARY] });
    expect(new Headers(post?.init.headers).get("Idempotency-Key")).toBeTruthy();
  });

  it("checks a sample request like the service", () => {
    const base: SampleValues = { kind: "RESERVE", quantity: "0.5", container: "", storageLocation: "", condition: "", batchExpiresOn: "2027-06-30", retentionYears: "", primaryIds: [] };
    expect(validateSample(ORDER, base).input).toEqual({ qualityOrderId: ORDER, kind: "RESERVE", quantity: "0.5", batchExpiresOn: "2027-06-30" });
    expect(validateSample(ORDER, { ...base, retentionYears: "3" }).input?.retentionYears).toBe(3);
    const bad = validateSample(ORDER, { ...base, quantity: "0", batchExpiresOn: "", retentionYears: "11", container: "x".repeat(61) });
    expect(Object.keys(bad.errors).sort()).toEqual(["batchExpiresOn", "container", "quantity", "retentionYears"]);
    expect(validateSample(ORDER, { ...base, kind: "POOLED" }).errors.primaryIds).toBeTruthy();
    expect(validateSample(ORDER, { ...base, kind: "PRIMARY", batchExpiresOn: "" }).input).toEqual({ qualityOrderId: ORDER, kind: "PRIMARY", quantity: "0.5" });
  });
});

describe("certificate requirement on setup", () => {
  it("sends the vendor certificate an association demands", async () => {
    handler = (url, init) => {
      if (url.pathname === "/api/v1/quality/test-groups") return json(200, { items: [group] });
      if (url.pathname === "/api/v1/quality/associations" && init.method === "POST") return json(201, {});
      if (url.pathname === "/api/v1/quality/associations") return json(200, { items: [] });
      return undefined;
    };
    renderAt("/quality/associations/new");
    await screen.findByRole("option", { name: "STEEL-3.1 v1" });
    fireEvent.change(screen.getByLabelText("Test group"), { target: { value: GROUP } });
    fireEvent.change(screen.getByLabelText("Vendor certificate"), { target: { value: "EN10204_31" } });
    fireEvent.click(screen.getByRole("button", { name: "Save association" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    expect((calls.find((c) => c.method === "POST")?.body as { certificateRequired?: string }).certificateRequired).toBe("EN10204_31");
  });
});
