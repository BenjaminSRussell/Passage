import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { InterchangeDetail, MonitorMessage, MonitorMessageDetail, Partner, PartnerSlaReport, PayloadTree, ReprocessResult } from "./api/client";
import integration from "./module";

const PARTNER_ID = "0195f0a0-0000-7000-8000-000000000001";
const INTERCHANGE_ID = "0195f0a0-0000-7000-8000-000000000002";
const FAILED_ID = "0195f0a0-0000-7000-8000-000000000010";
const OUT_ID = "0195f0a0-0000-7000-8000-000000000011";

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
const messageCalls = () => calls.filter((c) => c.url.pathname === "/api/v1/edi/messages");

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

const failed: MonitorMessage = {
  id: FAILED_ID,
  interchangeId: INTERCHANGE_ID,
  partnerId: PARTNER_ID,
  partnerName: "Demo distributor",
  direction: "IN",
  standard: "X12",
  controlNumber: "000000905",
  ackStatus: "REJECTED",
  transactionSet: "850",
  stControl: "0001",
  status: "FAILED",
  error: "EDI-0103: CTT01 2 does not match PO1 count 1",
  createdAt: "2026-10-21T12:00:00Z",
};

const outbound: MonitorMessage = {
  ...failed,
  id: OUT_ID,
  interchangeId: "0195f0a0-0000-7000-8000-000000000020",
  direction: "OUT",
  controlNumber: "000000002",
  ackStatus: "ACCEPTED",
  transactionSet: "856",
  status: "ACKED",
  error: undefined,
  createdAt: "2026-10-21T11:00:00Z",
};

describe("message monitor", () => {
  it("is registered behind edi.operate with the SLA dashboard", () => {
    const nav = Object.fromEntries(integration.nav.map((n) => [n.to, n.duty]));
    expect(nav["/integration/messages"]).toBe("edi.operate");
    expect(nav["/integration/partner-sla"]).toBe("edi.operate");
    const paths = integration.routes.map((r) => r.path);
    expect(paths).toEqual(expect.arrayContaining(["/integration/messages", "/integration/messages/:id", "/integration/partner-sla"]));
  });

  it("lists messages with their failing rule and sends only complete filters", async () => {
    handler = (url) => {
      if (url.pathname === "/api/v1/edi/partners") return json(200, { items: [partner] });
      if (url.pathname === "/api/v1/edi/messages") return json(200, { items: [failed, outbound] });
      return undefined;
    };
    const { container } = renderAt("/integration/messages?status=FAILED");
    const table = await findTable({ name: "Messages" });
    await waitFor(() => expect(within(table).getByText("EDI-0103: CTT01 2 does not match PO1 count 1")).toBeInTheDocument());
    expect(within(table).getByText("Outbound")).toBeInTheDocument();
    expect(within(table).getByText("Acknowledged")).toBeInTheDocument();
    expect(messageCalls()[0].url.searchParams.get("status")).toBe("FAILED");
    expect(messageCalls()[0].url.searchParams.get("limit")).toBe("50");
    expect(pageViolations(container)).toEqual([]);

    // A partial transaction set is not sent; a complete one is.
    fireEvent.change(screen.getByLabelText("Transaction set", { exact: false }), { target: { value: "85" } });
    fireEvent.change(screen.getByLabelText("Transaction set", { exact: false }), { target: { value: "850" } });
    fireEvent.change(screen.getByLabelText("ISA13", { exact: false }), { target: { value: "905" } });
    await waitFor(() => {
      const last = messageCalls().at(-1);
      expect(last?.url.searchParams.get("transactionSet")).toBe("850");
      expect(last?.url.searchParams.get("controlNumber")).toBe("905");
    });
    expect(messageCalls().some((c) => c.url.searchParams.get("transactionSet") === "85")).toBe(false);
  });

  it("loads the next page with the cursor and stops at the last page", async () => {
    handler = (url) => {
      if (url.pathname === "/api/v1/edi/partners") return json(200, { items: [] });
      if (url.pathname === "/api/v1/edi/messages") {
        return url.searchParams.get("cursor") === "page-2" ? json(200, { items: [outbound] }) : json(200, { items: [failed], nextCursor: "page-2" });
      }
      return undefined;
    };
    renderAt("/integration/messages");
    fireEvent.click(await screen.findByRole("button", { name: "Load more" }));
    const table = screen.getByRole("table", { name: "Messages" });
    await waitFor(() => expect(within(table).getByText("856")).toBeInTheDocument());
    expect(within(table).getByText("850")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Load more" })).not.toBeInTheDocument();
  });

  it("explains an empty monitor and a filtered one", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/integration/messages");
    expect(await screen.findByText("No messages yet")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Direction"), { target: { value: "OUT" } });
    expect(await screen.findByText("No messages match")).toBeInTheDocument();
  });

  it("shows the problem and retries when the list fails", async () => {
    let fail = true;
    handler = (url) => {
      if (url.pathname === "/api/v1/edi/partners") return json(200, { items: [] });
      if (url.pathname === "/api/v1/edi/messages") return fail ? json(403, { code: "COM-0403", detail: "duty edi.operate required" }) : json(200, { items: [failed] });
      return undefined;
    };
    renderAt("/integration/messages");
    const alert = await findAlert();
    expect(alert).toHaveTextContent("duty edi.operate required (COM-0403)");
    fail = false;
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await findTable({ name: "Messages" })).toBeInTheDocument();
  });

  it("shows one message with its failing rule and links to its interchange to reprocess", async () => {
    const detail: MonitorMessageDetail = {
      ...failed,
      piiMasked: true,
      canonical: { purpose: "00", orderType: "SA", poNumber: "PO-1001", poDate: "20261021", lines: [{ lineNo: "1", quantity: "10", uom: "EA", unitPrice: "2.5" }] },
    };
    handler = (url) => (url.pathname === `/api/v1/edi/messages/${FAILED_ID}` ? json(200, detail) : undefined);
    const { container } = renderAt(`/integration/messages/${FAILED_ID}`);
    expect(await screen.findByRole("heading", { name: "850 · ST02 0001" })).toBeInTheDocument();
    expect(screen.getByText("EDI-0103: CTT01 2 does not match PO1 count 1")).toBeInTheDocument();
    expect(screen.getByText("PO-1001")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Reprocess from interchange" })).toHaveAttribute("href", `/integration/interchanges/${INTERCHANGE_ID}`);
    expect(pageViolations(container)).toEqual([]);
  });

  it("reports a missing message", async () => {
    handler = () => json(404, { code: "EDI-0005", detail: "message not found" });
    renderAt(`/integration/messages/${FAILED_ID}`);
    expect(await findAlert()).toHaveTextContent("message not found (EDI-0005)");
  });
});

describe("reprocess", () => {
  const interchange: InterchangeDetail = {
    id: INTERCHANGE_ID,
    partnerId: PARTNER_ID,
    partnerName: "Demo distributor",
    direction: "IN",
    standard: "X12",
    version: "00401",
    controlNumber: "000000905",
    ackStatus: "REJECTED",
    receivedAt: "2026-10-21T12:00:00Z",
    messageCount: 1,
    failedCount: 1,
    ack997: "ISA*00*~",
    messages: [{ id: FAILED_ID, transactionSet: "850", stControl: "0001", status: "FAILED", error: failed.error }],
  };
  const payload: PayloadTree = {
    interchangeId: INTERCHANGE_ID,
    direction: "IN",
    piiMasked: true,
    tree: { kind: "interchange", name: "ISA", elements: [{ ref: "ISA13", name: "Interchange Control Number", value: "000000905" }] },
  };
  const result = (dryRun: boolean): ReprocessResult => ({
    interchangeId: INTERCHANGE_ID,
    dryRun,
    changed: 1,
    messages: [{ messageId: FAILED_ID, transactionSet: "850", stControl: "0001", previousError: failed.error, status: "MAPPED", changed: true }],
  });

  it("previews without a key, then reprocesses with one key and reloads the interchange", async () => {
    let reprocessed = false;
    handler = (url, init) => {
      if (url.pathname === `/api/v1/edi/interchanges/${INTERCHANGE_ID}`) {
        return json(200, reprocessed ? { ...interchange, failedCount: 0, messages: [{ ...interchange.messages[0], status: "MAPPED", error: undefined }] } : interchange);
      }
      if (url.pathname === `/api/v1/edi/interchanges/${INTERCHANGE_ID}:reprocess` && init.method === "POST") {
        const dry = url.searchParams.get("dryRun") === "true";
        if (!dry) reprocessed = true;
        return json(200, result(dry));
      }
      return undefined;
    };
    const { container } = renderAt(`/integration/interchanges/${INTERCHANGE_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Preview" }));
    expect(await screen.findByText("1 of 1 would change. Nothing was saved.")).toBeInTheDocument();
    const preview = calls.find((c) => c.url.pathname.endsWith(":reprocess"));
    expect(preview?.url.searchParams.get("dryRun")).toBe("true");
    expect(header(preview!, "Idempotency-Key")).toBeNull();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Reprocess" }));
    expect(await screen.findByText("1 of 1 changed.")).toBeInTheDocument();
    const apply = calls.filter((c) => c.url.pathname.endsWith(":reprocess")).at(-1)!;
    expect(apply.url.searchParams.get("dryRun")).toBeNull();
    expect(header(apply, "Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
    // The interchange is read again and the outcome stays on screen.
    await waitFor(() => expect(calls.filter((c) => c.url.pathname === `/api/v1/edi/interchanges/${INTERCHANGE_ID}`)).toHaveLength(2));
    expect(screen.getByRole("table", { name: "Reprocess outcome" })).toBeInTheDocument();
  });

  it("keeps the key after a failure so the retry cannot run twice, and shows the problem", async () => {
    let attempts = 0;
    handler = (url, init) => {
      if (url.pathname === `/api/v1/edi/interchanges/${INTERCHANGE_ID}`) return json(200, interchange);
      if (url.pathname === `/api/v1/edi/interchanges/${INTERCHANGE_ID}/payload`) return json(200, payload);
      if (url.pathname.endsWith(":reprocess") && init.method === "POST") {
        attempts += 1;
        return attempts === 1 ? json(503, { code: "COM-0503", detail: "database unavailable" }) : json(200, result(false));
      }
      return undefined;
    };
    renderAt(`/integration/interchanges/${INTERCHANGE_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Reprocess" }));
    expect(await findAlert()).toHaveTextContent("database unavailable (COM-0503)");
    fireEvent.click(screen.getByRole("button", { name: "Reprocess" }));
    expect(await screen.findByText("1 of 1 changed.")).toBeInTheDocument();
    const keys = calls.filter((c) => c.url.pathname.endsWith(":reprocess")).map((c) => header(c, "Idempotency-Key"));
    expect(keys).toHaveLength(2);
    expect(keys[0]).toBe(keys[1]);
  });

  it("is not offered for an outbound interchange or one without failures", async () => {
    handler = (url) =>
      url.pathname === `/api/v1/edi/interchanges/${INTERCHANGE_ID}`
        ? json(200, { ...interchange, direction: "OUT", ackStatus: "PENDING", ack997: undefined })
        : undefined;
    renderAt(`/integration/interchanges/${INTERCHANGE_ID}`);
    expect(await screen.findByRole("heading", { name: "Interchange 000000905" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Reprocess" })).not.toBeInTheDocument();
  });
});

describe("partner SLA", () => {
  const report: PartnerSlaReport = {
    days: 30,
    items: [
      {
        partnerId: PARTNER_ID,
        partnerName: "Demo distributor",
        environment: "TEST",
        ackSlaMinutes: 60,
        messages: 3,
        failed: 1,
        failureRatePct: 33.33,
        outbound: 2,
        acked: 1,
        p95AckMinutes: 90.5,
        lateAcks: 1,
        overdueAcks: 1,
      },
    ],
  };

  it("shows volume, failure rate, 997 latency and breaches per partner, and changes the period", async () => {
    handler = (url) => (url.pathname === "/api/v1/edi/partners/sla" ? json(200, { ...report, days: Number(url.searchParams.get("days")) }) : undefined);
    const { container } = renderAt("/integration/partner-sla");
    const table = await findTable({ name: "Partner SLA" });
    await waitFor(() => expect(within(table).getByText("Demo distributor (TEST)")).toBeInTheDocument());
    expect(within(table).getByText("33.33 %")).toBeInTheDocument();
    expect(within(table).getByText("1 of 2")).toBeInTheDocument();
    expect(within(table).getByText("90.5 min")).toBeInTheDocument();
    expect(within(table).getByText("1 late, 1 overdue")).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Totals for the last 30 days" })).toHaveTextContent("Transactions 3");
    expect(calls[0].url.searchParams.get("days")).toBe("30");
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Period"), { target: { value: "7" } });
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("days")).toBe("7"));
  });

  it("explains a period without traffic", async () => {
    handler = () => json(200, { days: 30, items: [] });
    renderAt("/integration/partner-sla");
    expect(await screen.findByText("No partner traffic in this period")).toBeInTheDocument();
  });

  it("shows the problem when the dashboard cannot load", async () => {
    handler = () => json(500, { code: "COM-0500", detail: "internal error" });
    renderAt("/integration/partner-sla");
    expect(await findAlert()).toHaveTextContent("internal error (COM-0500)");
  });
});
