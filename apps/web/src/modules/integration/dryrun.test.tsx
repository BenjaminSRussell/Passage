import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import integration from "./module";
import { PEPPOL_EXAMPLE, parseInvoice } from "./PeppolInvoicePage";
import { parsePairs, validatePunchout } from "./PunchoutCheckPage";
import { validateSelfBilling } from "./SelfBillingPage";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

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
      const reply = handler(url, init) ?? json(404, { code: "TEST-404", detail: `no fake for ${url.pathname}` });
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
        {integration.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const sent = (i = -1) => JSON.parse(String(calls.at(i)?.init.body)) as Record<string, unknown>;
const fill = (label: string, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } });
const submit = (name: string) => fireEvent.click(screen.getByRole("button", { name }));

describe("dry-run screens are registered", () => {
  it("routes and nav entries exist with the duties the service enforces", () => {
    const nav = Object.fromEntries(integration.nav.map((n) => [n.to, n.duty]));
    expect(nav["/integration/punchout"]).toBe("PUR_REQUISITION_CREATE");
    expect(nav["/integration/self-billing"]).toBe("SELF_BILLING_RECONCILE");
    expect(nav["/integration/peppol"]).toBe("EINV_OPERATE");
  });
});

describe("punchout cart check", () => {
  it("parses FIELD=value lines and reports the lines without a separator", () => {
    expect(parsePairs("A[1]=x\n\n B = y \nbroken")).toEqual({ pairs: { "A[1]": "x", B: "y" }, bad: [4] });
  });

  it("checks the client rules before calling the service", () => {
    const base = { format: "OCI" as const, document: "", fields: "NEW_ITEM-VENDORMAT[1]=A", currencies: "USD", tolerancePct: "0", uomMap: "", referencePrices: "" };
    expect(validatePunchout(base)).toEqual({});
    expect(validatePunchout({ ...base, fields: "" }).fields).toMatch(/Paste/);
    expect(validatePunchout({ ...base, tolerancePct: "25" }).tolerancePct).toMatch(/0 to 20/);
    expect(validatePunchout({ ...base, currencies: " , " }).currencies).toBeDefined();
    expect(validatePunchout({ ...base, format: "CXML" }).document).toBeDefined();
    expect(validatePunchout({ ...base, referencePrices: "nope" }).referencePrices).toMatch(/Lines 1/);
  });

  it("sends the OCI fields and supplier settings and shows lines, findings and the approval flag", async () => {
    handler = (url, init) =>
      url.pathname === "/api/v1/edi/punchout/returns:parse" && init.method === "POST"
        ? json(200, {
            lines: [
              { line: 2, supplierPartId: "OCI-200", description: "Flange DN50", quantity: "2", unitCode: "C62", unitPrice: "18.5", currency: "USD", unspsc: "", leadTimeDays: 5, catalog: true, needsApproval: true },
            ],
            issues: [{ code: "EDI-0133", detail: "Punchout line 1 dropped: unit \"BAG\" has no UN/ECE Recommendation 20 mapping.", reject: false }],
            cartRejected: false,
            computedTotal: "42.00",
          })
        : undefined;
    const { container } = renderAt("/integration/punchout");
    fill("Price tolerance (%)", "5");
    submit("Check cart");
    const table = await findTable({ name: "Requisition lines" });
    expect(within(table).getByText("OCI-200")).toBeInTheDocument();
    expect(within(table).getByText("Above catalog price")).toBeInTheDocument();
    expect(screen.getByText("1 requisition line would be created")).toBeInTheDocument();
    expect(within(screen.getByRole("list", { name: "Cart findings" })).getByText("EDI-0133")).toBeInTheDocument();
    const body = sent();
    expect(body.format).toBe("OCI");
    expect((body.fields as Record<string, string>)["NEW_ITEM-QUANTITY[1]"]).toBe("40");
    expect(body.supplier).toEqual({ currencies: ["USD"], tolerancePct: "5", uomMap: {}, referencePrices: { "OCI-200": "18.00" } });
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows a rejected cart with its totals and an empty line table", async () => {
    handler = () =>
      json(200, {
        lines: [],
        issues: [{ code: "EDI-0134", detail: "Punchout cart total 60.00 differs from line sum 40.00; cart rejected.", reject: true }],
        cartRejected: true,
        computedTotal: "40.00",
        statedTotal: "60.00",
      });
    renderAt("/integration/punchout");
    fill("Cart format", "CXML");
    submit("Check cart");
    expect(await screen.findByText("Cart rejected")).toBeInTheDocument();
    expect(screen.getByText(/stated total 60.00 differs from the line sum 40.00/)).toBeInTheDocument();
    expect(screen.getByText("The whole cart is rejected.")).toBeInTheDocument();
    expect(sent().format).toBe("CXML");
    expect(String(sent().document)).toContain("PunchOutOrderMessage");
  });

  it("shows the service problem and its rejected fields", async () => {
    handler = () => json(400, { code: "EDI-0001", detail: "request failed validation", errors: { "supplier.tolerancePct": "must be a number between 0 and 20" } });
    renderAt("/integration/punchout");
    submit("Check cart");
    const alert = await findAlert();
    expect(alert).toHaveTextContent("request failed validation (EDI-0001)");
    expect(within(alert).getByText("supplier.tolerancePct")).toBeInTheDocument();
  });

  it("does not call the service when the client rules fail", () => {
    renderAt("/integration/punchout");
    fill("Allowed currencies", "");
    submit("Check cart");
    expect(screen.getByText("Name at least one currency.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
  });
});

describe("self-billing match", () => {
  const values = { document: '{"shipments":[],"lines":[]}', asOf: "2026-10-27", waitDays: "30", quantityTolerance: "0", priceTolerance: "0.01" };

  it("validates the JSON shape and the parameter ranges", () => {
    expect(validateSelfBilling(values).data).toEqual({ shipments: [], lines: [] });
    expect(validateSelfBilling({ ...values, document: "[]" }).errors.document).toMatch(/shipments and lines/);
    expect(validateSelfBilling({ ...values, document: "{" }).errors.document).toMatch(/not valid JSON/);
    expect(validateSelfBilling({ ...values, waitDays: "121" }).errors.waitDays).toBeDefined();
    expect(validateSelfBilling({ ...values, waitDays: "2.5" }).errors.waitDays).toBeDefined();
    expect(validateSelfBilling({ ...values, priceTolerance: "-1" }).errors.priceTolerance).toBeDefined();
  });

  it("sends the document with its parameters and shows the outcomes, counts and unbilled shipments", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/edi/self-billing:match"
        ? json(200, {
            lines: [
              { line: "1", outcome: "MATCHED", shipmentId: "SHP-1" },
              { line: "2", outcome: "DIFFERENCE", shipmentId: "SHP-2", differences: [{ field: "unitPrice", theirs: "10.05", ours: "10" }] },
            ],
            unbilled: ["SHP-3"],
            issues: [
              { code: "EDI-0136", detail: "Self-billed line 2 differs: unitPrice 10.05 vs 10.", reject: false },
              { code: "EDI-0138", detail: "Shipment SHP-3 not self-billed after 30 days.", reject: false },
            ],
          })
        : undefined;
    const { container } = renderAt("/integration/self-billing");
    fill("Price tolerance per unit", "0.02");
    submit("Run match");
    const table = await findTable({ name: "Self-billed lines" });
    expect(within(table).getByText("Difference")).toBeInTheDocument();
    expect(within(table).getByText("Unit price 10.05 vs 10")).toBeInTheDocument();
    expect(within(screen.getByRole("list", { name: "Unbilled shipments" })).getByText("SHP-3")).toBeInTheDocument();
    const summary = screen.getByRole("list", { name: "Match summary" });
    expect(within(summary).getByText("Matched").parentElement).toHaveTextContent("1");
    const body = sent();
    expect(body).toMatchObject({ asOf: "2026-10-27", waitDays: 30, tolerance: { quantity: "0", price: "0.02" } });
    expect((body.shipments as unknown[]).length).toBe(3);
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows field errors from the service", async () => {
    handler = () => json(400, { code: "EDI-0001", detail: "request failed validation", errors: { "shipments[0].shippedOn": "must be a date in YYYY-MM-DD" } });
    renderAt("/integration/self-billing");
    submit("Run match");
    const alert = await findAlert();
    expect(within(alert).getByText("shipments[0].shippedOn")).toBeInTheDocument();
  });

  it("keeps a malformed document on the client", () => {
    renderAt("/integration/self-billing");
    fill("Shipments and self-billed lines (JSON)", "{");
    submit("Run match");
    expect(screen.getByText(/not valid JSON/)).toBeInTheDocument();
    expect(calls).toHaveLength(0);
  });
});

describe("peppol invoice", () => {
  it("accepts only a JSON object and the prefilled example is one", () => {
    expect(parseInvoice(PEPPOL_EXAMPLE).invoice).toMatchObject({ currency: "EUR" });
    expect(parseInvoice("[1]").error).toMatch(/JSON object/);
    expect(parseInvoice("{").error).toMatch(/not valid JSON/);
  });

  it("posts the invoice and shows the UBL when it is valid", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/edi/peppol/invoices:build" ? json(200, { xml: "<Invoice><cbc:ID>INV-2026-000123</cbc:ID></Invoice>", issues: [], valid: true }) : undefined;
    const { container } = renderAt("/integration/peppol");
    submit("Build UBL");
    expect(await screen.findByText("Ready for schematron")).toBeInTheDocument();
    expect(screen.getByLabelText("UBL XML")).toHaveTextContent("INV-2026-000123");
    expect(sent()).toEqual(JSON.parse(PEPPOL_EXAMPLE));
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the blocking findings and no XML when the invoice is blocked", async () => {
    handler = () =>
      json(200, { xml: "", valid: false, issues: [{ code: "EINV-0110", detail: "VAT category E needs an exemption reason", reject: true }] });
    renderAt("/integration/peppol");
    submit("Build UBL");
    expect(await screen.findByText("The invoice cannot be sent")).toBeInTheDocument();
    const findings = screen.getByRole("list", { name: "Blocking findings" });
    expect(within(findings).getByText("EINV-0110")).toBeInTheDocument();
    expect(within(findings).getByText("Rejects")).toBeInTheDocument();
    expect(screen.queryByLabelText("UBL XML")).toBeNull();
  });

  it("shows a request error and keeps the form", async () => {
    handler = () => json(400, { code: "EDI-0001", detail: "request failed validation", errors: { "lines[0].quantity": "must be a decimal number" } });
    renderAt("/integration/peppol");
    submit("Build UBL");
    const alert = await findAlert();
    expect(alert).toHaveTextContent("The invoice could not be read");
    expect(within(alert).getByText("lines[0].quantity")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("button", { name: "Build UBL" })).toBeEnabled());
  });

  it("does not call the service for malformed JSON", () => {
    renderAt("/integration/peppol");
    fill("Invoice (JSON)", "not json");
    submit("Build UBL");
    expect(screen.getByText(/not valid JSON/)).toBeInTheDocument();
    expect(calls).toHaveLength(0);
  });
});
