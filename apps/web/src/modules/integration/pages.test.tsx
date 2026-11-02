import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { loadModules } from "../registry";
import type { InterchangeDetail, InterchangeSummary, Partner } from "./api/client";
import integration from "./module";
import { validatePartner } from "./NewPartnerPage";

const PARTNER_ID = "0195f0a0-0000-7000-8000-000000000001";
const INTERCHANGE_ID = "0195f0a0-0000-7000-8000-000000000002";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
function pageViolations(container: HTMLElement) {
  return checkAxeAA(container).filter((v) => !shellRules.has(v.id));
}

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
        <Route path="*" element={<p>elsewhere</p>} />
      </Routes>
    </MemoryRouter>,
  );
}

const header = (call: { init: RequestInit }, name: string) => new Headers(call.init.headers).get(name);
const fill = (label: string, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } });

const partner: Partner = {
  id: PARTNER_ID,
  name: "Demo distributor",
  isaQualifier: "ZZ",
  isaId: "DISTRIBUTOR",
  environment: "TEST",
  ackSlaMinutes: 60,
  status: "active",
  version: 1,
  createdAt: "2026-10-20T08:00:00Z",
};

const summary: InterchangeSummary = {
  id: INTERCHANGE_ID,
  partnerId: PARTNER_ID,
  partnerName: "Demo distributor",
  direction: "IN",
  standard: "X12",
  version: "00401",
  controlNumber: "000000905",
  ackStatus: "ACCEPTED_WITH_ERRORS",
  receivedAt: "2026-10-21T12:00:00Z",
  messageCount: 2,
  failedCount: 1,
};

const detail: InterchangeDetail = {
  ...summary,
  ack997: "ISA*00*~GS*FA*MANUFACTURER*DISTRIBUTOR~ST*997*0001~AK1*PO*1~AK9*P*2*2*1~",
  messages: [
    {
      id: "0195f0a0-0000-7000-8000-000000000010",
      transactionSet: "850",
      stControl: "0001",
      status: "MAPPED",
      canonical: {
        purpose: "00",
        orderType: "SA",
        poNumber: "PO-1001",
        poDate: "20261021",
        currency: "EUR",
        shipToName: "ACME DC",
        shipToGln: "0614141000001",
        charges: [{ indicator: "C", code: "D240", amount: "12.50" }],
        lines: [{ lineNo: "1", quantity: "10", uom: "EA", unitPrice: "12.5", priceBasis: "PE", itemRef: { qualifier: "BP", value: "BUY-1" }, description: "Widget" }],
      },
    },
    { id: "0195f0a0-0000-7000-8000-000000000011", transactionSet: "810", stControl: "0002", status: "FAILED", error: "transaction set 810 is not supported yet" },
  ],
};

describe("integration module", () => {
  it("uses its folder name, is discovered by the registry and routes every nav entry", () => {
    expect(integration.id).toBe("integration");
    expect(loadModules().map((m) => m.id)).toContain("integration");
    const paths = integration.routes.map((r) => r.path);
    for (const item of integration.nav) {
      expect(item.to.startsWith("/integration")).toBe(true);
      expect(paths).toContain(item.to);
      expect(item.duty).toMatch(/^(EDI_(ADMIN|OPERATE)|PUR_REQUISITION_CREATE|SELF_BILLING_RECONCILE|EINV_OPERATE|hub\.delivery\.read)$/);
    }
  });
});

describe("trading partners", () => {
  it("lists partners from the service and filters by environment", async () => {
    handler = (url) => (url.pathname === "/api/v1/edi/partners" ? json(200, { items: [partner] }) : undefined);
    const { container } = renderAt("/integration/partners");
    const table = await findTable({ name: "Trading partners" });
    await waitFor(() => expect(within(table).getByText("ZZ/DISTRIBUTOR")).toBeInTheDocument());
    expect(within(table).getByText("60 min")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Environment"), { target: { value: "PROD" } });
    await waitFor(() => expect(calls.at(-1)?.url.search).toBe("?environment=PROD"));
  });

  it("shows the empty state when the tenant has no partners", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/integration/partners");
    expect(await screen.findByText("No trading partners yet")).toBeInTheDocument();
  });

  it("shows the problem and retries when the list fails", async () => {
    let fail = true;
    handler = () => (fail ? json(500, { code: "COM-0500", detail: "internal error" }) : json(200, { items: [partner] }));
    renderAt("/integration/partners");
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Could not load partners");
    expect(alert).toHaveTextContent("internal error (COM-0500)");
    fail = false;
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("ZZ/DISTRIBUTOR")).toBeInTheDocument();
  });

  it("validates the profile like the service does", () => {
    const base = { name: "Demo", isaQualifier: "ZZ", isaId: "DISTRIBUTOR", environment: "TEST" as const, ackSlaMinutes: "" };
    expect(validatePartner(base).input).toEqual({ name: "Demo", isaQualifier: "ZZ", isaId: "DISTRIBUTOR", environment: "TEST" });
    expect(validatePartner({ ...base, ackSlaMinutes: "30" }).input?.ackSlaMinutes).toBe(30);
    const bad = validatePartner({ name: " ", isaQualifier: "z", isaId: "HAS SPACE", environment: "TEST", ackSlaMinutes: "4" });
    expect(bad.input).toBeUndefined();
    expect(Object.keys(bad.errors).sort()).toEqual(["ackSlaMinutes", "isaId", "isaQualifier", "name"]);
  });

  it("creates a partner with one Idempotency-Key per attempt and opens it", async () => {
    handler = (url, init) => {
      if (url.pathname === "/api/v1/edi/partners" && init.method === "POST") return json(201, partner);
      if (url.pathname === `/api/v1/edi/partners/${PARTNER_ID}`) return json(200, partner);
      if (url.pathname === "/api/v1/edi/interchanges") return json(200, { items: [] });
      return undefined;
    };
    const { container } = renderAt("/integration/partners/new");
    expect(pageViolations(container)).toEqual([]);
    fill("Partner name", "Demo distributor");
    fill("ISA sender id", "DISTRIBUTOR");
    fireEvent.click(screen.getByRole("button", { name: "Create partner" }));

    expect(await screen.findByRole("heading", { name: "Demo distributor" })).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe("/api/v1/edi/partners");
    expect(header(post!, "Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
    expect(JSON.parse(String(post!.init.body))).toEqual({ name: "Demo distributor", isaQualifier: "ZZ", isaId: "DISTRIBUTOR", environment: "TEST" });
    expect(await screen.findByText("No interchanges yet")).toBeInTheDocument();
  });

  it("puts a duplicate ISA identity on the sender id field and sends nothing for an invalid form", async () => {
    handler = () => json(409, { code: "EDI-0004", detail: "a partner with this ISA identity already exists in this environment" });
    renderAt("/integration/partners/new");
    fireEvent.click(screen.getByRole("button", { name: "Create partner" }));
    expect(await screen.findByText("Enter the partner name, up to 120 characters.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);

    fill("Partner name", "Demo distributor");
    fill("ISA sender id", "DISTRIBUTOR");
    fireEvent.click(screen.getByRole("button", { name: "Create partner" }));
    expect(await screen.findByText("A partner with this ISA qualifier and id already exists in this environment.")).toBeInTheDocument();
    expect(screen.getByLabelText("ISA sender id")).toHaveAttribute("aria-invalid", "true");
  });

  it("reports a missing partner on the detail page", async () => {
    handler = (url) =>
      url.pathname.startsWith("/api/v1/edi/partners/") ? json(404, { code: "EDI-0005", detail: "partner not found" }) : json(200, { items: [] });
    renderAt(`/integration/partners/${PARTNER_ID}`);
    expect(await findAlert()).toHaveTextContent("partner not found (EDI-0005)");
  });
});

describe("interchanges", () => {
  it("lists interchanges with their 997 status and filters by status", async () => {
    handler = (url) => {
      if (url.pathname === "/api/v1/edi/partners") return json(200, { items: [partner] });
      if (url.pathname === "/api/v1/edi/interchanges") return json(200, { items: [summary] });
      return undefined;
    };
    const { container } = renderAt("/integration/interchanges");
    const table = await findTable({ name: "Interchanges" });
    await waitFor(() => expect(within(table).getByText("000000905")).toBeInTheDocument());
    expect(within(table).getByText("Accepted with errors")).toBeInTheDocument();
    expect(within(table).getByText("X12 00401")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("997 status"), { target: { value: "REJECTED" } });
    await waitFor(() => {
      const last = calls.filter((c) => c.url.pathname === "/api/v1/edi/interchanges").at(-1);
      expect(last?.url.searchParams.get("ackStatus")).toBe("REJECTED");
    });
  });

  it("explains an empty list", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/integration/interchanges");
    expect(await screen.findByText("No interchanges yet")).toBeInTheDocument();
  });

  it("shows each transaction, its canonical order and the 997", async () => {
    handler = (url) => (url.pathname === `/api/v1/edi/interchanges/${INTERCHANGE_ID}` ? json(200, detail) : undefined);
    const { container } = renderAt(`/integration/interchanges/${INTERCHANGE_ID}`);
    expect(await screen.findByRole("heading", { name: "Interchange 000000905" })).toBeInTheDocument();
    const lines = screen.getByRole("table", { name: "Lines of 850 0001" });
    expect(within(lines).getByText("BP BUY-1")).toBeInTheDocument();
    expect(within(lines).getByText("10 EA")).toBeInTheDocument();
    expect(screen.getByText("ACME DC · GLN 0614141000001")).toBeInTheDocument();
    expect(within(screen.getByRole("list", { name: "Lines of 850 0001 charges" })).getByText("12.50")).toBeInTheDocument();
    expect(screen.getByText("transaction set 810 is not supported yet")).toBeInTheDocument();
    expect(screen.getByLabelText("997 functional acknowledgment")).toHaveTextContent("AK9*P*2*2*1");
    expect(pageViolations(container)).toEqual([]);
  });

  it("receives a pasted interchange and opens the result", async () => {
    handler = (url, init) => {
      if (url.pathname === "/api/v1/edi/inbound/x12" && init.method === "POST")
        return json(201, { interchangeId: INTERCHANGE_ID, ackStatus: "ACCEPTED", messages: [], ack997: "ISA" });
      if (url.pathname === `/api/v1/edi/interchanges/${INTERCHANGE_ID}`) return json(200, detail);
      return undefined;
    };
    renderAt("/integration/interchanges/receive");
    fireEvent.click(screen.getByRole("button", { name: "Receive interchange" }));
    expect(await screen.findByText("Paste the whole interchange, starting with the ISA segment.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);

    fireEvent.change(screen.getByLabelText("Environment"), { target: { value: "TEST" } });
    fill("Raw X12 interchange", "ISA*00*...~IEA*1*000000905~");
    fireEvent.click(screen.getByRole("button", { name: "Receive interchange" }));
    expect(await screen.findByRole("heading", { name: "Interchange 000000905" })).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.url.searchParams.get("environment")).toBe("TEST");
    expect(header(post, "Content-Type")).toBe("text/plain");
    expect(post.init.body).toBe("ISA*00*...~IEA*1*000000905~");
  });

  it("explains a duplicate control number", async () => {
    handler = () => json(409, { code: "EDI-0102", detail: "duplicate interchange control number 000000905 inside the duplicate window; TA1 025" });
    renderAt("/integration/interchanges/receive");
    fill("Raw X12 interchange", "ISA*00*");
    fireEvent.click(screen.getByRole("button", { name: "Receive interchange" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("This interchange was already received");
    expect(alert).toHaveTextContent("EDI-0102");
  });
});

describe("map tester", () => {
  it("shows each transaction's status, findings and mapped lines", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/edi/maps:test"
        ? json(200, {
            transactions: [
              {
                transactionSet: "850",
                stControl: "0001",
                status: "FAILED",
                order: { ...detail.messages[0].canonical, lines: [] },
                issues: [{ code: "EDI-0103", detail: "CTT01 2 does not match PO1 count 1", reject: true }],
              },
            ],
          })
        : undefined;
    const { container } = renderAt("/integration/edi/map-tester");
    fill("Raw X12 interchange", "ISA*00*");
    fireEvent.click(screen.getByRole("button", { name: "Run map test" }));
    const findings = await screen.findByRole("list", { name: "Findings for 850 0001" });
    expect(findings).toHaveTextContent("Rejects");
    expect(findings).toHaveTextContent("EDI-0103");
    expect(screen.getByText("No lines mapped")).toBeInTheDocument();
    expect(JSON.parse(String(calls[0].init.body))).toEqual({ raw: "ISA*00*" });
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the problem detail when the service cannot parse the interchange", async () => {
    handler = () => json(400, { code: "EDI-0003", detail: "x12: syntax error: interchange does not start with a 106-character ISA header" });
    renderAt("/integration/edi/map-tester");
    fill("Raw X12 interchange", "garbage");
    fireEvent.click(screen.getByRole("button", { name: "Run map test" }));
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("106-character ISA header (EDI-0003)"));
  });
});
