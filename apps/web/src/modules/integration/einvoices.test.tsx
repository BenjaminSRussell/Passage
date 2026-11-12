import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import { validateAnswer } from "./EInvoicePage";
import { validateDecision } from "./SelfBillingDisputesPage";
import integration from "./module";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

const LE = "0195f0a0-0000-7000-8000-0000000000e1";
const INV = "0195f0a0-0000-7000-8000-0000000000b1";
const EID = "0195f0a0-0000-7000-8000-0000000000c1";
const PARTNER = "0195f0a0-0000-7000-8000-0000000000d1";
const DISPUTE = "0195f0a0-0000-7000-8000-0000000000f1";

let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit }[];
const json = (status: number, body: unknown): Reply => ({ status, body });

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, init });
      const reply = handler(url, init) ?? json(404, { code: "TEST-404", detail: `no fake for ${init.method ?? "GET"} ${url.pathname}` });
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});
afterEach(() => vi.unstubAllGlobals());

function renderAt(path: string) {
  return render(
    <PrincipalProvider duties={["edi.einvoice.operate", "edi.self-billing.reconcile", "edi.admin"]}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          {integration.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
        </Routes>
      </MemoryRouter>
    </PrincipalProvider>,
  );
}

const einvoice = (status: string, extra: Record<string, unknown> = {}) => ({
  id: EID,
  legalEntityId: LE,
  invoiceId: INV,
  number: "INV-DE-10412",
  mandate: "PEPPOL",
  format: "UBL21",
  documentType: "INVOICE",
  status,
  xml: "<Invoice/>",
  issues: [],
  version: 1,
  createdAt: "2026-11-01T10:00:00Z",
  updatedAt: "2026-11-02T10:00:00Z",
  ...extra,
});

describe("the integration module's B2B routes", () => {
  it("registers the e-invoice and dispute pages with their duties", () => {
    const paths = integration.routes.map((r) => r.path);
    expect(paths).toEqual(expect.arrayContaining(["/integration/einvoices", "/integration/einvoices/:id", "/integration/self-billing/disputes"]));
    const nav = Object.fromEntries((integration.nav ?? []).map((n) => [n.to, n.duty]));
    expect(nav["/integration/einvoices"]).toBe("edi.einvoice.operate");
    expect(nav["/integration/self-billing/disputes"]).toBe("edi.self-billing.reconcile");
  });
});

describe("validateAnswer and validateDecision", () => {
  it("needs the reason of a rejection", () => {
    expect(validateAnswer("ACCEPTED", "")).toEqual({});
    expect(validateAnswer("REJECTED", " ")).toHaveProperty("reason");
    expect(validateAnswer("MAYBE", "")).toHaveProperty("outcome");
    expect(validateDecision("x".repeat(501))).toHaveProperty("note");
    expect(validateDecision("kept")).toEqual({});
  });
});

describe("E-invoices page", () => {
  it("lists e-invoices with the status in words and opens one", async () => {
    handler = (url) => {
      if (url.pathname === "/api/v1/edi/einvoice/invoices") return json(200, { items: [einvoice("SUBMITTED", { xml: undefined, reference: "AP-1" }), einvoice("VALID", { id: "0195f0a0-0000-7000-8000-0000000000c2", number: "INV-DE-10413", xml: undefined })] });
      return undefined;
    };
    const { container } = renderAt("/integration/einvoices");
    expect(await screen.findByText("INV-DE-10412")).toBeTruthy();
    expect(screen.getAllByText("Submitted").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Ready to submit").length).toBeGreaterThan(0);
    expect(screen.queryByText(EID)).toBeNull();
    expect(pageViolations(container)).toEqual([]);
  });
});

describe("E-invoice page", () => {
  it("submits a valid e-invoice once and shows the new status", async () => {
    let status = "VALID";
    handler = (url, init) => {
      if (url.pathname === `/api/v1/edi/einvoice/invoices/${EID}` && (init.method ?? "GET") === "GET") return json(200, einvoice(status));
      if (url.pathname === `/api/v1/edi/einvoice/invoices/${EID}:submit`) {
        status = "SUBMITTED";
        return json(200, einvoice("SUBMITTED", { reference: "AP-INV-DE-10412" }));
      }
      return undefined;
    };
    renderAt(`/integration/einvoices/${EID}`);
    const button = await screen.findByRole("button", { name: "Submit to the network" });
    fireEvent.click(button);
    fireEvent.click(button);
    expect(await screen.findByText("Record the network's answer")).toBeTruthy();
    expect(calls.filter((c) => c.url.pathname.endsWith(":submit"))).toHaveLength(1);
  });

  it("shows a refused submit in words and keeps the page", async () => {
    handler = (url, init) => {
      if (url.pathname === `/api/v1/edi/einvoice/invoices/${EID}` && (init.method ?? "GET") === "GET") return json(200, einvoice("VALID"));
      if (url.pathname.endsWith(":submit")) return json(409, { code: "EDI-0030", detail: "the e-invoice is already submitting" });
      return undefined;
    };
    renderAt(`/integration/einvoices/${EID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Submit to the network" }));
    expect(await screen.findByText(/isn't in a state where this can be done/)).toBeTruthy();
  });

  it("records a rejection only with its reason", async () => {
    handler = (url, init) => {
      if (url.pathname === `/api/v1/edi/einvoice/invoices/${EID}` && (init.method ?? "GET") === "GET") return json(200, einvoice("SUBMITTED"));
      if (url.pathname.endsWith(":response")) return json(200, einvoice("REJECTED"));
      return undefined;
    };
    renderAt(`/integration/einvoices/${EID}`);
    const select = await screen.findByLabelText("Answer");
    fireEvent.change(select, { target: { value: "REJECTED" } });
    fireEvent.click(screen.getByRole("button", { name: "Record the answer" }));
    expect(await screen.findByText("Enter the reason the network gave.")).toBeTruthy();
    expect(calls.some((c) => c.url.pathname.endsWith(":response"))).toBe(false);
    fireEvent.change(screen.getByLabelText("Reason"), { target: { value: "BR-CO-17" } });
    fireEvent.click(screen.getByRole("button", { name: "Record the answer" }));
    await waitFor(() => expect(calls.some((c) => c.url.pathname.endsWith(":response"))).toBe(true));
  });
});

describe("Self-billing disputes page", () => {
  const dispute = (extra: Record<string, unknown> = {}) => ({
    id: DISPUTE,
    partnerId: PARTNER,
    kind: "DIFFERENCE",
    code: "EDI-0136",
    detail: "Self-billed line 2 differs: unitPrice 482.50 vs 480.00.",
    status: "OPEN",
    createdAt: "2026-11-01T10:00:00Z",
    ...extra,
  });
  const partner = { id: PARTNER, name: "Bluewater Municipal Utilities", isaQualifier: "ZZ", isaId: "BLUE", environment: "PROD", ackSlaMinutes: 60, status: "active", version: 1, createdAt: "2026-10-01T00:00:00Z" };

  it("shows the customer by name and resolves a dispute", async () => {
    handler = (url, init) => {
      if (url.pathname === "/api/v1/edi/partners") return json(200, { items: [partner] });
      if (url.pathname === "/api/v1/edi/self-billing/disputes") return json(200, { items: [dispute()] });
      if (url.pathname.endsWith(":resolve")) return json(200, dispute({ status: "REJECTED" }));
      void init;
      return undefined;
    };
    const { container } = renderAt("/integration/self-billing/disputes");
    expect(await screen.findByText("Self-billed line 2 differs: unitPrice 482.50 vs 480.00.")).toBeTruthy();
    expect(screen.getAllByText("Bluewater Municipal Utilities").length).toBeGreaterThan(0);
    expect(screen.queryByText(PARTNER)).toBeNull();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByText("Self-billed line 2 differs: unitPrice 482.50 vs 480.00."));
    fireEvent.click(await screen.findByRole("button", { name: "Keep our figures" }));
    await waitFor(() => expect(calls.some((c) => c.url.pathname.endsWith(":resolve"))).toBe(true));
    const call = calls.find((c) => c.url.pathname.endsWith(":resolve"));
    expect(JSON.parse(String(call?.init.body))).toMatchObject({ decision: "REJECTED" });
  });
});
