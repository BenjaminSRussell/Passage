import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { BulkReprocessResult, InterchangeDetail, MonitorMessage, MonitorMessageDetail, PayloadTree } from "./api/client";
import { reprocessTargets } from "./BulkReprocessCard";
import integration from "./module";

const PARTNER_ID = "0195f0a0-0000-7000-8000-000000000001";
const IC_A = "0195f0a0-0000-7000-8000-00000000000a";
const IC_B = "0195f0a0-0000-7000-8000-00000000000b";
const MSG_A = "0195f0a0-0000-7000-8000-0000000000a1";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit }[];
const json = (status: number, body: unknown): Reply => ({ status, body });
const header = (call: { init: RequestInit }, name: string) => new Headers(call.init.headers).get(name);

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
      </Routes>
    </MemoryRouter>,
  );
}

const interchange: InterchangeDetail = {
  id: IC_A,
  partnerId: PARTNER_ID,
  partnerName: "Demo distributor",
  direction: "IN",
  standard: "X12",
  version: "00401",
  controlNumber: "000000001",
  ackStatus: "ACCEPTED",
  receivedAt: "2026-10-21T12:00:00Z",
  messageCount: 1,
  failedCount: 0,
  ack997: "ISA*00*~",
  messages: [{ id: MSG_A, transactionSet: "850", stControl: "0001", status: "MAPPED" }],
};

const maskedTree: PayloadTree = {
  interchangeId: IC_A,
  direction: "IN",
  piiMasked: true,
  tree: {
    kind: "interchange",
    name: "ISA",
    elements: [{ ref: "ISA13", name: "Interchange Control Number", value: "000000001" }],
    children: [
      {
        kind: "group",
        name: "GS",
        elements: [{ ref: "GS01", name: "Functional Identifier Code", value: "PO" }],
        children: [
          {
            kind: "transaction",
            name: "ST",
            elements: [
              { ref: "ST01", name: "Transaction Set Identifier Code", value: "850" },
              { ref: "ST02", name: "Transaction Set Control Number", value: "0001" },
            ],
            children: [
              { kind: "segment", name: "BEG", elements: [{ ref: "BEG03", name: "Purchase Order Number", value: "PO-88001" }] },
              {
                kind: "segment",
                name: "N1",
                elements: [
                  { ref: "N101", name: "Entity Identifier Code", value: "ST" },
                  { ref: "N102", name: "Name", value: "***", masked: true },
                  { ref: "N104", name: "Identification Code", value: "0012345000007" },
                ],
              },
              { kind: "segment", name: "ZZZ", elements: [{ ref: "ZZZ01", value: "X" }] },
            ],
          },
        ],
      },
    ],
  },
};

describe("raw payload viewer", () => {
  it("shows the segment tree with dictionary names on hover and the masking badge", async () => {
    handler = (url) => {
      if (url.pathname === `/api/v1/edi/interchanges/${IC_A}`) return json(200, interchange);
      if (url.pathname === `/api/v1/edi/interchanges/${IC_A}/payload`) return json(200, maskedTree);
      return undefined;
    };
    const { container } = renderAt(`/integration/interchanges/${IC_A}`);
    const tree = await screen.findByRole("list", { name: "Segment tree" });
    expect(within(tree).getByTitle("BEG03 Purchase Order Number")).toHaveTextContent("PO-88001");
    expect(within(tree).getByTitle("N102 Name (masked)")).toHaveTextContent("***");
    expect(within(tree).getByTitle("N104 Identification Code")).toHaveTextContent("0012345000007");
    // An element outside the dictionary still shows its reference.
    expect(within(tree).getByTitle("ZZZ01")).toHaveTextContent("X");
    // Envelopes nest: the transaction is labelled and holds its body segments.
    const st = within(tree).getByRole("list", { name: "Transaction set ST" });
    expect(within(st).getByText("BEG")).toBeInTheDocument();
    expect(screen.getByText("Personal data masked")).toBeInTheDocument();
    expect(screen.getByText(/because you do not hold EDI_PII_VIEW/)).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("says personal data is shown to a user with EDI_PII_VIEW", async () => {
    handler = (url) => {
      if (url.pathname === `/api/v1/edi/interchanges/${IC_A}`) return json(200, interchange);
      if (url.pathname === `/api/v1/edi/interchanges/${IC_A}/payload`) return json(200, { ...maskedTree, piiMasked: false });
      return undefined;
    };
    renderAt(`/integration/interchanges/${IC_A}`);
    expect(await screen.findByText(/because you hold EDI_PII_VIEW/)).toBeInTheDocument();
    expect(screen.queryByText("Personal data masked")).not.toBeInTheDocument();
  });

  it("shows the problem and retries when the payload cannot be read", async () => {
    let fail = true;
    handler = (url) => {
      if (url.pathname === `/api/v1/edi/interchanges/${IC_A}`) return json(200, interchange);
      if (url.pathname === `/api/v1/edi/interchanges/${IC_A}/payload`) {
        return fail ? json(422, { code: "EDI-0003", detail: "the stored interchange no longer parses" }) : json(200, maskedTree);
      }
      return undefined;
    };
    renderAt(`/integration/interchanges/${IC_A}`);
    const alert = await findAlert();
    expect(alert).toHaveTextContent("the stored interchange no longer parses (EDI-0003)");
    fail = false;
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("list", { name: "Segment tree" })).toBeInTheDocument();
  });

  it("marks a masked canonical document on the message page", async () => {
    const detail: MonitorMessageDetail = {
      id: MSG_A,
      interchangeId: IC_A,
      partnerId: PARTNER_ID,
      partnerName: "Demo distributor",
      direction: "IN",
      standard: "X12",
      controlNumber: "000000001",
      ackStatus: "ACCEPTED",
      transactionSet: "850",
      stControl: "0001",
      status: "MAPPED",
      createdAt: "2026-10-21T12:00:00Z",
      piiMasked: true,
      canonical: { purpose: "00", orderType: "SA", poNumber: "PO-88001", poDate: "20261021", shipToName: "***", lines: [] },
    };
    handler = (url) => (url.pathname === `/api/v1/edi/messages/${MSG_A}` ? json(200, detail) : undefined);
    renderAt(`/integration/messages/${MSG_A}`);
    expect(await screen.findByText("Personal data masked")).toBeInTheDocument();
    expect(screen.getByText(/Party names are shown as \*\*\*/)).toBeInTheDocument();
  });
});

const row = (over: Partial<MonitorMessage>): MonitorMessage => ({
  id: MSG_A,
  interchangeId: IC_A,
  partnerId: PARTNER_ID,
  partnerName: "Demo distributor",
  direction: "IN",
  standard: "X12",
  controlNumber: "000000001",
  ackStatus: "REJECTED",
  transactionSet: "850",
  stControl: "0001",
  status: "FAILED",
  error: "EDI-0103: CTT01 2 does not match PO1 count 1",
  createdAt: "2026-10-21T12:00:00Z",
  ...over,
});

describe("bulk reprocess", () => {
  it("takes each failed inbound interchange once and skips outbound, 997 and healthy rows", () => {
    const rows = [
      row({}),
      row({ id: "m2", stControl: "0002" }), // same interchange
      row({ id: "m3", interchangeId: IC_B, controlNumber: "000000002" }),
      row({ id: "m4", interchangeId: "out", direction: "OUT" }),
      row({ id: "m5", interchangeId: "ack", transactionSet: "997" }),
      row({ id: "m6", interchangeId: "ok", status: "MAPPED" }),
    ];
    expect(reprocessTargets(rows).map((t) => t.interchangeId)).toEqual([IC_A, IC_B]);
  });

  const outcome = (dryRun: boolean): BulkReprocessResult => ({
    dryRun,
    requested: 2,
    changed: 1,
    items: [
      { interchangeId: IC_A, outcome: "REPROCESSED", changed: 1, messages: [{ messageId: MSG_A, transactionSet: "850", stControl: "0001", status: "MAPPED", changed: true }] },
      { interchangeId: IC_B, outcome: "UNREADABLE", changed: 0, error: "the stored interchange no longer parses", messages: [] },
    ],
  });

  it("previews without a key, then runs with one key, reloads the list and keeps the outcome", async () => {
    let ran = false;
    handler = (url, init) => {
      if (url.pathname === "/api/v1/edi/partners") return json(200, { items: [] });
      if (url.pathname === "/api/v1/edi/messages") {
        return json(200, { items: ran ? [] : [row({}), row({ id: "m3", interchangeId: IC_B, controlNumber: "000000002" })] });
      }
      if (url.pathname === "/api/v1/edi/interchanges:reprocess" && init.method === "POST") {
        const dry = url.searchParams.get("dryRun") === "true";
        if (!dry) ran = true;
        return json(200, outcome(dry));
      }
      return undefined;
    };
    const { container } = renderAt("/integration/messages?status=FAILED");
    fireEvent.click(await screen.findByRole("button", { name: "Preview" }));
    expect(await screen.findByText("1 transaction in 2 interchanges would change. Nothing was saved.")).toBeInTheDocument();
    const preview = calls.find((c) => c.url.pathname.endsWith("interchanges:reprocess"))!;
    expect(preview.url.searchParams.get("dryRun")).toBe("true");
    expect(header(preview, "Idempotency-Key")).toBeNull();
    expect(JSON.parse(String(preview.init.body))).toEqual({ interchangeIds: [IC_A, IC_B] });
    const table = screen.getByRole("table", { name: "Preview of bulk reprocess" });
    expect(within(table).getByText("000000002")).toBeInTheDocument();
    expect(within(table).getByText("Unreadable")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Reprocess 2" }));
    expect(await screen.findByText("1 transaction in 2 interchanges changed.")).toBeInTheDocument();
    const apply = calls.filter((c) => c.url.pathname.endsWith("interchanges:reprocess")).at(-1)!;
    expect(apply.url.searchParams.get("dryRun")).toBeNull();
    expect(header(apply, "Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
    // The list is read again; the outcome stays with the ISA13s it ran on.
    await waitFor(() => expect(calls.filter((c) => c.url.pathname === "/api/v1/edi/messages")).toHaveLength(2));
    const done = await findTable({ name: "Bulk reprocess outcome" });
    expect(within(done).getByText("000000001")).toBeInTheDocument();
    expect(screen.getByText("No failed inbound transaction is left in this list.")).toBeInTheDocument();
  });

  it("keeps the key after a failure so the retry cannot run twice", async () => {
    let attempts = 0;
    handler = (url, init) => {
      if (url.pathname === "/api/v1/edi/partners") return json(200, { items: [] });
      if (url.pathname === "/api/v1/edi/messages") return json(200, { items: [row({})] });
      if (url.pathname === "/api/v1/edi/interchanges:reprocess" && init.method === "POST") {
        attempts += 1;
        return attempts === 1 ? json(503, { code: "COM-0503", detail: "database unavailable" }) : json(200, { ...outcome(false), requested: 1, items: outcome(false).items.slice(0, 1) });
      }
      return undefined;
    };
    renderAt("/integration/messages");
    fireEvent.click(await screen.findByRole("button", { name: "Reprocess 1" }));
    expect(await findAlert()).toHaveTextContent("database unavailable (COM-0503)");
    fireEvent.click(screen.getByRole("button", { name: "Reprocess 1" }));
    expect(await screen.findByText("1 transaction in 1 interchange changed.")).toBeInTheDocument();
    const keys = calls.filter((c) => c.url.pathname.endsWith("interchanges:reprocess")).map((c) => header(c, "Idempotency-Key"));
    expect(keys).toHaveLength(2);
    expect(keys[0]).toBe(keys[1]);
  });

  it("is not offered when no failed inbound transaction is listed", async () => {
    handler = (url) => {
      if (url.pathname === "/api/v1/edi/partners") return json(200, { items: [] });
      if (url.pathname === "/api/v1/edi/messages") return json(200, { items: [row({ status: "MAPPED", error: undefined }), row({ id: "o", direction: "OUT" })] });
      return undefined;
    };
    renderAt("/integration/messages");
    expect(await findTable({ name: "Messages" })).toBeInTheDocument();
    await waitFor(() => expect(screen.getAllByText("Demo distributor").length).toBeGreaterThan(0));
    expect(screen.queryByRole("button", { name: /^Reprocess/ })).not.toBeInTheDocument();
  });
});
