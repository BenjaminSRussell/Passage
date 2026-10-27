import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { PrintJob, Printer, RoutingRule } from "./api/print";
import mobileFlows from "./module";
import { validateJob, validYYMMDD } from "./NewPrintJobPage";
import { validatePrinter } from "./NewPrinterPage";
import { validateRule } from "./NewRoutingRulePage";

const JOB = "0195f0a0-0000-7000-8000-0000000000a1";
const REPRINT = "0195f0a0-0000-7000-8000-0000000000a2";

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
        {mobileFlows.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const printer: Printer = {
  id: "0195f0a0-0000-7000-8000-000000000001",
  code: "DOCK-1",
  name: "Dock door 1",
  deviceType: "ZPL_II",
  dpi: 203,
  host: "10.0.4.20",
  port: 9100,
  queueCode: "DOCK",
  failoverRank: 1,
  createdAt: "2026-10-27T08:00:00Z",
};

const job: PrintJob = {
  id: JOB,
  templateCode: "LP_LABEL",
  queueCode: "DOCK",
  printerCode: "DOCK-2",
  status: "PRINTED",
  attempts: 3,
  createdBy: "worker-7",
  createdAt: "2026-10-27T08:05:00Z",
  updatedAt: "2026-10-27T08:05:02Z",
};

describe("printers", () => {
  it("lists printers with their queue and failover rank", async () => {
    handler = (url) => (url.pathname === "/api/v1/print/printers" ? json(200, { items: [printer] }) : undefined);
    const { container } = renderAt("/mobile-flows/printers");
    const table = await screen.findByRole("table", { name: "Printers" });
    await waitFor(() => expect(within(table).getByText("DOCK-1")).toBeInTheDocument());
    expect(within(table).getByText("10.0.4.20:9100")).toBeInTheDocument();
    expect(within(table).getByText("ZPL II")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Register printer" })).toHaveAttribute("href", "/mobile-flows/printers/new");
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the empty state and a retryable error", async () => {
    handler = () => json(200, { items: [] });
    const { unmount } = renderAt("/mobile-flows/printers");
    expect(await screen.findByText("No printers yet")).toBeInTheDocument();
    unmount();

    handler = () => json(503, { code: "COM-0500", detail: "internal error" });
    renderAt("/mobile-flows/printers");
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Could not load printers");
    expect(alert).toHaveTextContent("COM-0500");
    handler = () => json(200, { items: [printer] });
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("DOCK-1")).toBeInTheDocument();
  });

  it("validates the registration like the service and leaves the default port to it", () => {
    const base = { code: "DOCK-1", name: "Dock", deviceType: "ZPL_II" as const, dpi: "203", host: "10.0.4.20", port: "", queueCode: "DOCK", failoverRank: "1" };
    expect(validatePrinter(base).input).toEqual({ code: "DOCK-1", name: "Dock", deviceType: "ZPL_II", dpi: 203, host: "10.0.4.20", queueCode: "DOCK", failoverRank: 1 });
    // Every device type has a service default port (9100 raw TCP, 631 IPP), so the port may be left empty.
    expect(validatePrinter({ ...base, deviceType: "PDF_IPP" }).input).not.toHaveProperty("port");
    expect(validatePrinter({ ...base, deviceType: "PCL_6", port: "9101" }).input).toMatchObject({ deviceType: "PCL_6", port: 9101 });
    expect(Object.keys(validatePrinter({ ...base, code: "dock", queueCode: "", failoverRank: "0", port: "70000" }).errors).sort()).toEqual([
      "code",
      "failoverRank",
      "port",
      "queueCode",
    ]);
  });

  it("registers a printer with an Idempotency-Key and shows a taken failover rank on its field", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(409, { code: "PRN-0006", detail: "rank taken" }) : json(200, { items: [] }));
    renderAt("/mobile-flows/printers/new");
    fireEvent.change(screen.getByLabelText("Printer code"), { target: { value: "DOCK-2" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Dock 2" } });
    fireEvent.change(screen.getByLabelText("Host"), { target: { value: "10.0.4.21" } });
    fireEvent.change(screen.getByLabelText("Queue"), { target: { value: "DOCK" } });
    fireEvent.click(screen.getByRole("button", { name: "Register printer" }));
    expect(await screen.findByText("Another printer in this queue already has this failover rank.")).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe("/api/v1/print/printers");
    expect(new Headers(post?.init.headers).get("Idempotency-Key")).toBeTruthy();
    expect(post?.body).toEqual({ code: "DOCK-2", name: "Dock 2", deviceType: "ZPL_II", dpi: 203, host: "10.0.4.21", queueCode: "DOCK", failoverRank: 1 });
  });

  it("returns to the list after a successful registration", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(201, printer) : json(200, { items: [printer] }));
    renderAt("/mobile-flows/printers/new");
    fireEvent.change(screen.getByLabelText("Printer code"), { target: { value: "DOCK-1" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Dock door 1" } });
    fireEvent.change(screen.getByLabelText("Host"), { target: { value: "10.0.4.20" } });
    fireEvent.change(screen.getByLabelText("Queue"), { target: { value: "DOCK" } });
    fireEvent.click(screen.getByRole("button", { name: "Register printer" }));
    expect(await screen.findByRole("table", { name: "Printers" })).toBeInTheDocument();
  });
});

describe("routing rules", () => {
  const rules: RoutingRule[] = [
    { id: "r1", documentType: "PICK_LIST", queueCode: "OFFICE", sequence: 1 },
    { id: "r2", documentType: "LP_LABEL", zone: "COLD", queueCode: "COLD", sequence: 2 },
  ];

  it("lists rules with the scope they apply to", async () => {
    handler = () => json(200, { items: rules });
    const { container } = renderAt("/mobile-flows/routing-rules");
    const table = await screen.findByRole("table", { name: "Routing rules" });
    await waitFor(() => expect(within(table).getByText("Zone COLD")).toBeInTheDocument());
    expect(within(table).getByText("Default for the document type")).toBeInTheDocument();
    expect(within(table).getByText("Pick list")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("sends exactly one scope field", async () => {
    expect(validateRule({ documentType: "COA", scope: "zone", scopeValue: " LAB ", queueCode: "LAB", sequence: "3" }).input).toEqual({
      documentType: "COA",
      zone: "LAB",
      queueCode: "LAB",
      sequence: 3,
    });
    expect(validateRule({ documentType: "COA", scope: "userId", scopeValue: "", queueCode: "lab", sequence: "0" }).errors).toEqual({
      scopeValue: expect.any(String),
      queueCode: expect.any(String),
      sequence: expect.any(String),
    });

    handler = (_url, init) => (init.method === "POST" ? json(201, { ...rules[1], id: "r3" }) : json(200, { items: rules }));
    renderAt("/mobile-flows/routing-rules/new");
    fireEvent.change(screen.getByLabelText("Applies to"), { target: { value: "warehouseId" } });
    fireEvent.change(screen.getByLabelText("Warehouse"), { target: { value: "WH1" } });
    fireEvent.change(screen.getByLabelText("Queue"), { target: { value: "WH" } });
    fireEvent.click(screen.getByRole("button", { name: "Add rule" }));
    expect(await screen.findByRole("table", { name: "Routing rules" })).toBeInTheDocument();
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({ documentType: "LP_LABEL", warehouseId: "WH1", queueCode: "WH", sequence: 1 });
  });
});

describe("print jobs", () => {
  it("lists the job log, filters by status and opens a job", async () => {
    handler = (url) => {
      if (url.pathname === "/api/v1/print/jobs") return json(200, { items: [job] });
      if (url.pathname === `/api/v1/print/jobs/${JOB}`) return json(200, job);
      return undefined;
    };
    const { container } = renderAt("/mobile-flows/print-jobs");
    const table = await screen.findByRole("table", { name: "Print jobs" });
    await waitFor(() => expect(within(table).getByText("Printed")).toBeInTheDocument());
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "FAILED" } });
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("status")).toBe("FAILED"));

    fireEvent.click(await screen.findByRole("button", { name: formatted(job.createdAt) }));
    expect(await screen.findByText("DOCK-2")).toBeInTheDocument();
  });

  it("shows the empty log", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/mobile-flows/print-jobs");
    expect(await screen.findByText("No print jobs")).toBeInTheDocument();
  });

  it("explains a failed job and prints it again as a new job with reprintOf", async () => {
    const failed: PrintJob = { ...job, status: "FAILED", printerCode: undefined, lastError: "MOB-0111" };
    handler = (url, init) => {
      if (init.method === "POST") return json(201, { ...job, id: REPRINT, reprintOf: JOB });
      if (url.pathname === `/api/v1/print/jobs/${JOB}`) return json(200, failed);
      if (url.pathname === `/api/v1/print/jobs/${REPRINT}`) return json(200, { ...job, id: REPRINT, reprintOf: JOB });
      return undefined;
    };
    const { container } = renderAt(`/mobile-flows/print-jobs/${JOB}`);
    expect(await screen.findByText("The label did not print")).toBeInTheDocument();
    expect(screen.getAllByText(/MOB-0111/).length).toBeGreaterThan(0);
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Print again" }));
    expect(await screen.findByRole("link", { name: "Original job" })).toHaveAttribute("href", `/mobile-flows/print-jobs/${JOB}`);
    const post = calls.find((c) => c.method === "POST");
    expect(post?.body).toEqual({ queueCode: "DOCK", reprintOf: JOB });
    expect(new Headers(post?.init.headers).get("Idempotency-Key")).toBeTruthy();
  });

  it("shows a load error on the detail page", async () => {
    handler = () => json(404, { code: "PRN-0005", detail: "print job not found" });
    renderAt(`/mobile-flows/print-jobs/${JOB}`);
    expect(await screen.findByRole("alert")).toHaveTextContent("print job not found (PRN-0005)");
  });

  it("validates label data like the template renderer", () => {
    const values = {
      templateCode: "LP_LABEL" as const,
      target: "queue" as const,
      queueCode: "DOCK",
      zone: "",
      warehouseId: "",
      data: { lp: "LP-000123", item: "WIDGET", lot: "L42", expiry: "2027-06-30", sscc: "340123456789012340" },
    };
    expect(validateJob(values).input).toEqual({ templateCode: "LP_LABEL", queueCode: "DOCK", data: values.data });
    const bad = validateJob({ ...values, data: { ...values.data, item: "^XA", sscc: "340123456789012341" } }).errors;
    expect(Object.keys(bad).sort()).toEqual(["item", "sscc"]);
    expect(validateJob({ ...values, target: "rule", zone: "COLD" }).input).toEqual({
      templateCode: "LP_LABEL",
      documentType: "LP_LABEL",
      zone: "COLD",
      data: values.data,
    });
  });

  it("validates the GS1 item and carton labels like the service (BR-32.1, BR-32.10)", () => {
    const item = {
      templateCode: "ITEM_LABEL" as const,
      target: "queue" as const,
      queueCode: "DOCK",
      zone: "",
      warehouseId: "",
      data: { gtin: "09501101020917", lot: "", expiry: "260200", serial: "SN0007" },
    };
    // Empty optional fields are left out of the request, and so out of the barcode.
    expect(validateJob(item).input).toEqual({ templateCode: "ITEM_LABEL", queueCode: "DOCK", data: { gtin: "09501101020917", expiry: "260200", serial: "SN0007" } });
    const bad = validateJob({ ...item, data: { gtin: "09501101020918", lot: "AB C", expiry: "261301", serial: "S>8" } }).errors;
    expect(bad).toEqual({
      gtin: "Enter the 14-digit GTIN with a valid check digit.",
      lot: "Use letters, digits and GS1 punctuation only, without spaces or >.",
      expiry: "Enter the expiry as YYMMDD, such as 261231 (DD 00 means the month end).",
      serial: "Use letters, digits and GS1 punctuation only, without spaces or >.",
    });
    expect(validateJob({ ...item, data: { expiry: "250229" } }).errors).toEqual({ gtin: "Enter the GTIN.", expiry: expect.stringMatching(/YYMMDD/) });

    const carton = { ...item, templateCode: "GS1_CARTON_LABEL" as const, data: { sscc: "340123456789012340", shipToName: "", poNumber: "x".repeat(31) } };
    expect(validateJob(carton).errors).toEqual({ shipToName: "Enter the ship-to name.", poNumber: "Use at most 30 characters." });
  });

  it("accepts leap days and the month-end day 00 in YYMMDD", () => {
    expect(validYYMMDD("280229")).toBe(true);
    expect(validYYMMDD("270229")).toBe(false);
    expect(validYYMMDD("261100")).toBe(true);
    expect(validYYMMDD("260431")).toBe(false);
    expect(validYYMMDD("26123")).toBe(false);
  });

  it("prints an item label with only the GS1 fields that were filled in", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return json(201, { ...job, templateCode: "ITEM_LABEL" });
      if (url.pathname === `/api/v1/print/jobs/${JOB}`) return json(200, { ...job, templateCode: "ITEM_LABEL" });
      return undefined;
    };
    const { container } = renderAt("/mobile-flows/print-jobs/new");
    fireEvent.change(screen.getByLabelText("Template"), { target: { value: "ITEM_LABEL" } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Queue"), { target: { value: "DOCK" } });
    fireEvent.change(screen.getByLabelText("GTIN"), { target: { value: "09501101020917" } });
    fireEvent.change(screen.getByLabelText(/^Lot/), { target: { value: "123ABC" } });
    fireEvent.click(screen.getByRole("button", { name: "Print label" }));
    expect(await screen.findByText("ITEM_LABEL")).toBeInTheDocument();
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({ templateCode: "ITEM_LABEL", queueCode: "DOCK", data: { gtin: "09501101020917", lot: "123ABC" } });
  });

  it("shows the service's GS1 refusal on the form", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(400, { code: "PRN-0001", detail: "AI 01: check digit invalid" }) : undefined);
    renderAt("/mobile-flows/print-jobs/new");
    fireEvent.change(screen.getByLabelText("Template"), { target: { value: "GS1_CARTON_LABEL" } });
    fireEvent.change(screen.getByLabelText("Queue"), { target: { value: "SHIP" } });
    fireEvent.change(screen.getByLabelText("SSCC"), { target: { value: "340123456789012340" } });
    fireEvent.change(screen.getByLabelText("Ship-to name"), { target: { value: "ACME DC" } });
    fireEvent.click(screen.getByRole("button", { name: "Print label" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("AI 01: check digit invalid (PRN-0001)");
  });

  it("prints a location label and opens the recorded job", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return json(201, { ...job, templateCode: "LOCATION_LABEL" });
      if (url.pathname === `/api/v1/print/jobs/${JOB}`) return json(200, { ...job, templateCode: "LOCATION_LABEL" });
      return undefined;
    };
    const { container } = renderAt("/mobile-flows/print-jobs/new");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Template"), { target: { value: "LOCATION_LABEL" } });
    fireEvent.change(screen.getByLabelText("Queue"), { target: { value: "DOCK" } });
    fireEvent.change(screen.getByLabelText("Location"), { target: { value: "A-01-02" } });
    fireEvent.click(screen.getByRole("button", { name: "Print label" }));
    expect(await screen.findByText("LOCATION_LABEL")).toBeInTheDocument();
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({ templateCode: "LOCATION_LABEL", queueCode: "DOCK", data: { location: "A-01-02" } });
  });

  it("surfaces MOB-0112 when no routing rule matches", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(422, { code: "MOB-0112", detail: "no printer is routed for LOCATION_LABEL" }) : undefined);
    renderAt("/mobile-flows/print-jobs/new");
    fireEvent.change(screen.getByLabelText("Template"), { target: { value: "LOCATION_LABEL" } });
    fireEvent.change(screen.getByLabelText("Printer selection"), { target: { value: "rule" } });
    fireEvent.change(screen.getByLabelText("Location"), { target: { value: "A-01-02" } });
    fireEvent.click(screen.getByRole("button", { name: "Print label" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("no printer is routed for LOCATION_LABEL (MOB-0112)");
  });
});

function formatted(iso: string): string {
  return `${new Date(iso).toISOString().slice(0, 16).replace("T", " ")} UTC`;
}
