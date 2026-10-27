import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { InterchangeDetail, OutboundResult, Partner } from "./api/client";
import { OUTBOUND_EXAMPLES } from "./format";
import integration from "./module";
import { validateOutbound } from "./OutboundDocumentPage";

const PARTNER_ID = "0195f0a0-0000-7000-8000-000000000001";
const OUT_ID = "0195f0a0-0000-7000-8000-000000000020";
const ACK_ID = "0195f0a0-0000-7000-8000-000000000021";

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
const posts = () => calls.filter((c) => c.method === "POST");

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

const result: OutboundResult = {
  interchangeId: OUT_ID,
  messageId: "0195f0a0-0000-7000-8000-000000000022",
  transactionSet: "856",
  controlNumber: "000000002",
  x12: "ISA*00*~GS*SH*PASSAGE*DISTRIBUTOR~ST*856*0001~BSN*00*LOAD42-SHIP7~",
};

const partnersOnly: Handler = (url) => (url.pathname === "/api/v1/edi/partners" ? json(200, { items: [partner] }) : undefined);

describe("send EDI document", () => {
  it("is registered behind EDI_OPERATE and reachable from the nav", () => {
    const nav = integration.nav.find((n) => n.to === "/integration/interchanges/outbound");
    expect(nav?.duty).toBe("EDI_OPERATE");
    expect(integration.routes.map((r) => r.path)).toContain("/integration/interchanges/outbound");
  });

  it("generates an 856 for the preselected partner with an Idempotency-Key and shows the X12", async () => {
    handler = (url, init) => {
      if (url.pathname === `/api/v1/edi/partners/${PARTNER_ID}/outbound` && init.method === "POST") return json(201, result);
      return partnersOnly(url, init);
    };
    const { container } = renderAt(`/integration/interchanges/outbound?partnerId=${PARTNER_ID}`);
    const partnerSelect = await screen.findByLabelText("Partner");
    expect(partnerSelect).toHaveValue(PARTNER_ID);
    expect(screen.getByLabelText("Canonical document (JSON)")).toHaveValue(OUTBOUND_EXAMPLES["856"]);
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Generate document" }));
    expect(await screen.findByRole("heading", { name: "856 advance ship notice recorded as interchange 000000002" })).toBeInTheDocument();
    expect(screen.getByLabelText("Generated X12 interchange")).toHaveTextContent("BSN*00*LOAD42-SHIP7");
    expect(screen.getByRole("link", { name: "Open interchange" })).toHaveAttribute("href", `/integration/interchanges/${OUT_ID}`);

    const post = posts()[0];
    expect(header(post, "Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
    expect(JSON.parse(String(post.init.body))).toEqual({ transactionSet: "856", document: JSON.parse(OUTBOUND_EXAMPLES["856"]) });
    expect(pageViolations(container)).toEqual([]);
  });

  it("swaps in the example for another set unless the document was edited", async () => {
    handler = partnersOnly;
    renderAt("/integration/interchanges/outbound");
    const docField = await screen.findByLabelText("Canonical document (JSON)");
    fireEvent.change(screen.getByLabelText("Document"), { target: { value: "810" } });
    expect(docField).toHaveValue(OUTBOUND_EXAMPLES["810"]);

    fireEvent.change(docField, { target: { value: '{"number":"MINE"}' } });
    fireEvent.change(screen.getByLabelText("Document"), { target: { value: "855" } });
    expect(docField).toHaveValue('{"number":"MINE"}');
    fireEvent.click(screen.getByRole("button", { name: "Reset to example" }));
    expect(docField).toHaveValue(OUTBOUND_EXAMPLES["855"]);
  });

  it("checks the partner and the JSON before sending anything", async () => {
    handler = partnersOnly;
    renderAt("/integration/interchanges/outbound");
    fireEvent.change(await screen.findByLabelText("Canonical document (JSON)"), { target: { value: "{not json" } });
    fireEvent.click(screen.getByRole("button", { name: "Generate document" }));
    expect(await screen.findByText("Choose the partner to send the document to.")).toBeInTheDocument();
    expect(screen.getByText(/The document is not valid JSON/)).toBeInTheDocument();
    expect(screen.getByLabelText("Canonical document (JSON)")).toHaveAttribute("aria-invalid", "true");
    expect(posts()).toHaveLength(0);
  });

  it("puts a mapping-rule refusal (EDI-0007) on the document field", async () => {
    handler = (url, init) =>
      init.method === "POST"
        ? json(422, { code: "EDI-0007", detail: 'edi: outbound document: pack SSCC "1" is invalid or repeated' })
        : partnersOnly(url, init);
    renderAt(`/integration/interchanges/outbound?partnerId=${PARTNER_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Generate document" }));
    expect(await screen.findByText('edi: outbound document: pack SSCC "1" is invalid or repeated')).toBeInTheDocument();
    expect(screen.getByLabelText("Canonical document (JSON)")).toHaveAttribute("aria-invalid", "true");
    expect(screen.queryByLabelText("Generated X12 interchange")).not.toBeInTheDocument();
  });

  it("shows other failures in an alert and uses a fresh key after the key was reused", async () => {
    handler = (url, init) =>
      init.method === "POST" ? json(422, { code: "idempotency_key_reuse", detail: "Idempotency-Key was already used" }) : partnersOnly(url, init);
    renderAt(`/integration/interchanges/outbound?partnerId=${PARTNER_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Generate document" }));
    expect(await findAlert()).toHaveTextContent("Idempotency-Key was already used (idempotency_key_reuse)");
    fireEvent.click(screen.getByRole("button", { name: "Generate document" }));
    await waitFor(() => expect(posts()).toHaveLength(2));
    expect(header(posts()[1], "Idempotency-Key")).not.toBe(header(posts()[0], "Idempotency-Key"));
  });

  it("explains that a partner is needed when none is active", async () => {
    handler = () => json(200, { items: [{ ...partner, status: "inactive" }] });
    renderAt("/integration/interchanges/outbound");
    expect(await screen.findByText("No active trading partners")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "New trading partner" })).toHaveAttribute("href", "/integration/partners/new");
  });

  it("offers a retry when the partners cannot be loaded", async () => {
    let fail = true;
    handler = (url, init) => (fail ? json(503, { code: "COM-0503", detail: "unavailable" }) : partnersOnly(url, init));
    renderAt("/integration/interchanges/outbound");
    const alert = await findAlert();
    expect(alert).toHaveTextContent("unavailable (COM-0503)");
    fail = false;
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await screen.findByLabelText("Partner")).toBeInTheDocument();
  });

  it("validates like the form does", () => {
    expect(validateOutbound({ partnerId: PARTNER_ID, transactionSet: "855", document: OUTBOUND_EXAMPLES["855"] }).document).toEqual(
      JSON.parse(OUTBOUND_EXAMPLES["855"]),
    );
    expect(validateOutbound({ partnerId: PARTNER_ID, transactionSet: "856", document: "[1]" }).errors.document).toBe("The document must be a JSON object.");
  });
});

const summary = {
  id: OUT_ID,
  partnerId: PARTNER_ID,
  partnerName: "Demo distributor",
  direction: "OUT" as const,
  standard: "X12" as const,
  version: "00401",
  controlNumber: "000000002",
  ackStatus: "PENDING" as const,
  receivedAt: "2026-10-21T12:00:00Z",
  messageCount: 1,
  failedCount: 0,
};

describe("outbound and acknowledgment interchanges", () => {
  it("filters the list by direction, starting from the URL", async () => {
    handler = (url) => {
      if (url.pathname === "/api/v1/edi/partners") return json(200, { items: [partner] });
      if (url.pathname === "/api/v1/edi/interchanges") return json(200, { items: [summary] });
      return undefined;
    };
    renderAt("/integration/interchanges?direction=OUT");
    const table = await findTable({ name: "Interchanges" });
    await waitFor(() => expect(within(table).getByText("Outbound")).toBeInTheDocument());
    expect(within(table).getByText("Pending")).toBeInTheDocument();
    const listCalls = () => calls.filter((c) => c.url.pathname === "/api/v1/edi/interchanges");
    expect(listCalls()[0].url.searchParams.get("direction")).toBe("OUT");
    expect(screen.getByRole("link", { name: "Send document" })).toHaveAttribute("href", "/integration/interchanges/outbound");

    fireEvent.change(screen.getByLabelText("Direction"), { target: { value: "IN" } });
    await waitFor(() => expect(listCalls().at(-1)?.url.searchParams.get("direction")).toBe("IN"));
    fireEvent.change(screen.getByLabelText("Direction"), { target: { value: "" } });
    await waitFor(() => expect(listCalls().at(-1)?.url.searchParams.has("direction")).toBe(false));
  });

  it("shows an outbound 856 with its packs and waits for the partner's 997", async () => {
    const detail: InterchangeDetail = {
      ...summary,
      messages: [
        { id: "0195f0a0-0000-7000-8000-000000000022", transactionSet: "856", stControl: "0001", status: "MAPPED", canonical: JSON.parse(OUTBOUND_EXAMPLES["856"]) },
      ],
    };
    handler = (url) => (url.pathname === `/api/v1/edi/interchanges/${OUT_ID}` ? json(200, detail) : undefined);
    const { container } = renderAt(`/integration/interchanges/${OUT_ID}`);
    expect(await screen.findByRole("heading", { name: "Interchange 000000002" })).toBeInTheDocument();
    expect(screen.getByText(/Sent to Demo distributor/)).toBeInTheDocument();
    const packs = screen.getByRole("table", { name: "Lines of 856 0001" });
    expect(within(packs).getByText("376104250021234569")).toBeInTheDocument();
    expect(within(packs).getByText("10 EA")).toBeInTheDocument();
    expect(screen.getByText("812.5 KG")).toBeInTheDocument();
    expect(screen.getByText(/Waiting for the partner's 997/)).toBeInTheDocument();
    expect(screen.queryByLabelText("997 functional acknowledgment")).not.toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows a rejected outbound 810 with the partner's finding", async () => {
    const detail: InterchangeDetail = {
      ...summary,
      ackStatus: "REJECTED",
      failedCount: 1,
      messages: [
        {
          id: "0195f0a0-0000-7000-8000-000000000023",
          transactionSet: "810",
          stControl: "0001",
          status: "FAILED",
          error: "rejected by the partner 997 (AK5 R)",
          canonical: JSON.parse(OUTBOUND_EXAMPLES["810"]),
        },
      ],
    };
    handler = (url) => (url.pathname === `/api/v1/edi/interchanges/${OUT_ID}` ? json(200, detail) : undefined);
    renderAt(`/integration/interchanges/${OUT_ID}`);
    expect(await screen.findByText("rejected by the partner 997 (AK5 R)")).toBeInTheDocument();
    expect(screen.getByText("INV-7001")).toBeInTheDocument();
    expect(screen.getByText("140.00")).toBeInTheDocument();
    expect(screen.getByText(/The partner's 997 has been matched/)).toBeInTheDocument();
  });

  it("shows a received 997 with the groups and transactions it answers", async () => {
    const detail: InterchangeDetail = {
      ...summary,
      id: ACK_ID,
      direction: "IN",
      controlNumber: "000000077",
      ackStatus: "ACCEPTED",
      messages: [
        {
          id: "0195f0a0-0000-7000-8000-000000000024",
          transactionSet: "997",
          stControl: "0001",
          status: "POSTED",
          canonical: { functionalId: "SH", groupControl: "2", code: "A", sets: [{ setId: "856", control: "0001", code: "A" }] },
        },
      ],
    };
    handler = (url) => (url.pathname === `/api/v1/edi/interchanges/${ACK_ID}` ? json(200, detail) : undefined);
    const { container } = renderAt(`/integration/interchanges/${ACK_ID}`);
    expect(await screen.findByText("SH 2")).toBeInTheDocument();
    expect(screen.getByText("Accepted (A)")).toBeInTheDocument();
    expect(within(screen.getByRole("list", { name: "Lines of 997 0001" })).getByText("856 ST02 0001")).toBeInTheDocument();
    expect(screen.getByText(/A 997 received from the partner is never acknowledged/)).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("links an active partner to the send page", async () => {
    handler = (url) => {
      if (url.pathname === `/api/v1/edi/partners/${PARTNER_ID}`) return json(200, partner);
      if (url.pathname === "/api/v1/edi/interchanges") return json(200, { items: [summary] });
      return undefined;
    };
    renderAt(`/integration/partners/${PARTNER_ID}`);
    expect(await screen.findByRole("link", { name: "Send document" })).toHaveAttribute(
      "href",
      `/integration/interchanges/outbound?partnerId=${PARTNER_ID}`,
    );
  });
});
