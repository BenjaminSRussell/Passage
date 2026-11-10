import { findAlert, findStatus, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { validateMailbox } from "./CaseMailboxPage";
import { validateDecision } from "./SupplierClaimDetailPage";
import { validateClaim, type ClaimValues } from "./SupplierClaimsPage";
import service from "./module";

const LE = "22222222-0000-4000-8000-000000000002";
const VENDOR = "99999999-0000-4000-8000-000000000009";
const WO = "44444444-0000-4000-8000-000000000004";
const WO2 = "44444444-0000-4000-8000-000000000005";
const ITEM = "66666666-0000-4000-8000-000000000006";
const CLAIM = "aaaaaaaa-0000-4000-8000-00000000000a";
const CASE = "55555555-0000-4000-8000-000000000005";
const CATEGORY = "77777777-0000-4000-8000-000000000007";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

type Reply = { status: number; body: unknown; etag?: string };
type Handler = (url: URL, init: RequestInit) => Reply | Promise<Reply>;

let handler: Handler;
let calls: { method: string; url: URL; headers: Headers; body: unknown }[];

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, headers: new Headers(init.headers), body: init.body ? JSON.parse(init.body as string) : undefined });
      const { status, body, etag } = await handler(url, init);
      const headers: Record<string, string> = { "Content-Type": "application/json" };
      if (etag) headers.ETag = etag;
      return new Response(JSON.stringify(body), { status, headers });
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
        {service.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
        <Route path="*" element={<p>Elsewhere</p>} />
      </Routes>
    </MemoryRouter>,
  );
}

const writes = (suffix: string) => calls.filter((c) => c.method !== "GET" && c.url.pathname.endsWith(suffix));
const empty = { status: 200, body: { items: [] } };

function claim(overrides: Record<string, unknown> = {}) {
  return {
    id: CLAIM,
    number: "SWC-000001",
    vendorPartyId: VENDOR,
    legalEntityId: LE,
    sourceKind: "SERVICE_WO",
    sourceIds: [WO],
    currency: "USD",
    laborRate: "60",
    lines: [{ sourceId: WO, itemId: ITEM, serial: "PUMP-1", failureCode: "BEARING", partAmount: "180", laborHours: "1.5", laborRate: "60", claimedAmount: "270", acceptedAmount: "0" }],
    claimedTotal: "270",
    acceptedTotal: "0",
    recoveredAmount: "0",
    status: "DRAFT",
    completedOn: "2026-10-20",
    windowDays: 30,
    windowEndsOn: "2026-11-19",
    returnRequired: false,
    returnOrderId: "",
    vendorReference: "",
    decisionReason: "",
    creditNoteId: "",
    version: 1,
    createdAt: "2026-10-28T09:00:00Z",
    updatedAt: "2026-10-28T09:00:00Z",
    ...overrides,
  };
}

const claimValues = (overrides: Partial<ClaimValues> = {}): ClaimValues => ({
  sourceKind: "SERVICE_WO",
  vendorPartyId: VENDOR,
  sourceIds: `${WO}\n${WO2}`,
  laborRate: "60.00",
  failureCode: "",
  windowDays: "30",
  returnRequired: false,
  legalEntityId: "",
  currency: "",
  completedOn: "",
  itemId: "",
  serial: "",
  partAmount: "",
  laborHours: "",
  ...overrides,
});

describe("supplier claim and mailbox rules mirrored from the service", () => {
  it("validates a claim from posted work orders and from an EAM work order", () => {
    expect(validateClaim(claimValues()).input).toEqual({
      vendorPartyId: VENDOR,
      sourceKind: "SERVICE_WO",
      sourceIds: [WO, WO2],
      laborRate: "60.00",
      windowDays: 30,
      returnRequired: false,
    });
    expect(validateClaim(claimValues({ laborRate: "" })).input?.laborRate).toBe("0");
    expect(validateClaim(claimValues({ sourceIds: `${WO}\n${WO}` })).errors.sourceIds).toMatch(/twice/);
    expect(validateClaim(claimValues({ sourceIds: "", vendorPartyId: "x", laborRate: "-1", windowDays: "400" })).errors).toMatchObject({
      sourceIds: expect.any(String),
      vendorPartyId: expect.any(String),
      laborRate: expect.any(String),
      windowDays: expect.any(String),
    });
    const eam = claimValues({ sourceKind: "EAM_WO", sourceIds: WO, legalEntityId: LE, currency: "eur", completedOn: "2026-10-01", itemId: ITEM, partAmount: "99.50" });
    expect(validateClaim(eam).input).toMatchObject({ currency: "EUR", completedOn: "2026-10-01", lines: [{ itemId: ITEM, partAmount: "99.50", laborHours: "0" }] });
    expect(validateClaim({ ...eam, sourceIds: `${WO}\n${WO2}`, itemId: "" }).errors).toMatchObject({ sourceIds: expect.any(String), itemId: expect.any(String) });
  });

  it("validates a vendor decision without floating point", () => {
    const lines = claim().lines;
    expect(validateDecision("PARTIALLY_ACCEPTED", "", ["250.00"], lines).input).toEqual({ decision: "PARTIALLY_ACCEPTED", acceptedAmounts: ["250.00"] });
    expect(validateDecision("PARTIALLY_ACCEPTED", "", ["270.000001"], lines).errors["acceptedAmounts[0]"]).toMatch(/At most/);
    expect(validateDecision("PARTIALLY_ACCEPTED", "", [""], lines).errors["acceptedAmounts[0]"]).toBeDefined();
    expect(validateDecision("REJECTED", " ", [], lines).errors.reason).toBeDefined();
    expect(validateDecision("ACCEPTED", "", [], lines).input).toEqual({ decision: "ACCEPTED" });
    expect(validateDecision("", "", [], lines).errors.decision).toBeDefined();
  });

  it("validates a mailbox", () => {
    expect(validateMailbox({ address: " Service@Example.test ", legalEntityId: LE, categoryId: CATEGORY }).input).toEqual({
      address: "service@example.test",
      legalEntityId: LE,
      categoryId: CATEGORY,
    });
    expect(validateMailbox({ address: "service", legalEntityId: "", categoryId: "" }).errors).toMatchObject({
      address: expect.any(String),
      legalEntityId: expect.any(String),
      categoryId: expect.any(String),
    });
  });
});

describe("supplier claim list", () => {
  it("lists claims with their amounts, filters by status and opens one", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/service/supplier-claims" ? { status: 200, body: { items: [claim()] } } : { status: 200, body: claim(), etag: '"1"' };
    const { container } = renderAt("/service/supplier-claims");
    const table = await findTable({ name: "Supplier claims" });
    expect(within(table).getByText("SWC-000001")).toBeInTheDocument();
    expect(within(table).getByText("270 USD")).toBeInTheDocument();
    expect(within(table).getByText("2026-11-19")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "SUBMITTED" } });
    await waitFor(() => expect(calls.some((c) => c.url.searchParams.get("status") === "SUBMITTED")).toBe(true));
    fireEvent.click(within(await findTable({ name: "Supplier claims" })).getByText("SWC-000001"));
    expect(await screen.findByText("Supplier claim SWC-000001")).toBeInTheDocument();
  });

  it("shows the empty state and a failed load with a retry", async () => {
    handler = () => empty;
    renderAt("/service/supplier-claims");
    expect(await screen.findByText("No supplier claims match")).toBeInTheDocument();
    document.body.innerHTML = "";
    handler = () => ({ status: 500, body: { code: "COM-0500", detail: "boom" } });
    renderAt("/service/supplier-claims");
    expect(await findAlert()).toHaveTextContent("Could not load supplier claims");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

describe("new supplier claim", () => {
  it("creates a claim from posted work orders with an Idempotency-Key and lands on it", async () => {
    handler = (_url, init) => (init.method === "POST" ? { status: 201, body: claim(), etag: '"1"' } : { status: 200, body: claim(), etag: '"1"' });
    const { container } = renderAt("/service/supplier-claims/new");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Vendor"), { target: { value: VENDOR } });
    fireEvent.change(screen.getByLabelText("Work orders"), { target: { value: WO } });
    fireEvent.change(screen.getByLabelText(/Vendor labor rate/), { target: { value: "60.00" } });
    fireEvent.click(screen.getByLabelText(/failed part back/));
    fireEvent.click(screen.getByRole("button", { name: "Create claim" }));
    expect(await screen.findByText("Supplier claim SWC-000001")).toBeInTheDocument();
    const [created] = writes("/api/v1/service/supplier-claims");
    expect(created?.headers.get("Idempotency-Key")).toBeTruthy();
    expect(created?.body).toEqual({ vendorPartyId: VENDOR, sourceKind: "SERVICE_WO", sourceIds: [WO], laborRate: "60.00", windowDays: 30, returnRequired: true });
  });

  it("shows field errors before calling the service and the service's refusal after", async () => {
    handler = () => ({ status: 422, body: { code: "SVC-SWC-008", detail: "work order SWO-000001 has no WARRANTY part or labor line" } });
    renderAt("/service/supplier-claims/new");
    fireEvent.click(screen.getByRole("button", { name: "Create claim" }));
    expect(await screen.findByText("Enter the vendor party's UUID.")).toBeInTheDocument();
    expect(writes("/supplier-claims")).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Vendor"), { target: { value: VENDOR } });
    fireEvent.change(screen.getByLabelText("Work orders"), { target: { value: WO } });
    fireEvent.click(screen.getByRole("button", { name: "Create claim" }));
    expect(await findAlert()).toHaveTextContent("SVC-SWC-008");
  });

  it("asks for the EAM work order's facts", async () => {
    handler = () => empty;
    renderAt("/service/supplier-claims/new");
    fireEvent.change(screen.getByLabelText("Source"), { target: { value: "EAM_WO" } });
    expect(screen.getByLabelText("Legal entity")).toBeInTheDocument();
    expect(screen.getByLabelText("Failed item")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Create claim" }));
    expect(await screen.findByText("Enter the completion date.")).toBeInTheDocument();
  });
});

describe("supplier claim detail", () => {
  const detail = (c: Record<string, unknown>, write?: Handler): Handler => (url, init) =>
    init.method && init.method !== "GET" && write ? write(url, init) : { status: 200, body: c, etag: `"${String(c.version)}"` };

  it("submits a draft with If-Match and an Idempotency-Key, insisting on the return order the vendor wants", async () => {
    handler = detail(claim({ returnRequired: true }), () => ({ status: 200, body: claim({ status: "SUBMITTED", version: 2, returnOrderId: "RMA-7", vendorReference: "VR-1" }), etag: '"2"' }));
    const { container } = renderAt(`/service/supplier-claims/${CLAIM}`);
    expect(await screen.findByText("Supplier claim SWC-000001")).toBeInTheDocument();
    expect(within(await findTable({ name: "Claim lines" })).getByText("270 USD")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Submit claim" }));
    expect(await screen.findByText(/enter the return order first/)).toBeInTheDocument();
    expect(writes(":submit")).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Return order"), { target: { value: "RMA-7" } });
    fireEvent.change(screen.getByLabelText(/Vendor reference/), { target: { value: "VR-1" } });
    fireEvent.click(screen.getByRole("button", { name: "Submit claim" }));
    expect(await findStatus()).toHaveTextContent("Claim submitted to the vendor.");
    const [sent] = writes(":submit");
    expect(sent?.headers.get("If-Match")).toBe('"1"');
    expect(sent?.headers.get("Idempotency-Key")).toBeTruthy();
    expect(sent?.body).toEqual({ vendorReference: "VR-1", returnOrderId: "RMA-7" });
    // The decision form replaces the submit form once SUBMITTED.
    expect(screen.getByRole("form", { name: "Vendor decision" })).toBeInTheDocument();
  });

  it("shows the window refusal from the service", async () => {
    handler = detail(claim(), () => ({ status: 422, body: { code: "SVC-SWC-001", detail: "claim window for the vendor ended on 2026-11-19" } }));
    renderAt(`/service/supplier-claims/${CLAIM}`);
    fireEvent.click(await screen.findByRole("button", { name: "Submit claim" }));
    expect(await findAlert()).toHaveTextContent("claim window for the vendor ended on 2026-11-19");
  });

  it("records a partial acceptance (AC-45.13) with one amount per line", async () => {
    handler = detail(claim({ status: "SUBMITTED", version: 2, vendorReference: "VR-1" }), () => ({
      status: 200,
      body: claim({ status: "PARTIALLY_ACCEPTED", version: 3, acceptedTotal: "250", vendorReference: "VR-1" }),
      etag: '"3"',
    }));
    renderAt(`/service/supplier-claims/${CLAIM}`);
    fireEvent.change(await screen.findByLabelText("Decision"), { target: { value: "PARTIALLY_ACCEPTED" } });
    fireEvent.change(screen.getByLabelText(/Accepted on line 1/), { target: { value: "300" } });
    fireEvent.click(screen.getByRole("button", { name: "Record decision" }));
    expect(await screen.findByText("At most the claimed 270.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText(/Accepted on line 1/), { target: { value: "250.00" } });
    fireEvent.click(screen.getByRole("button", { name: "Record decision" }));
    expect(await findStatus()).toHaveTextContent("Vendor decision recorded: partially accepted.");
    const [sent] = writes("/decision");
    expect(sent?.headers.get("If-Match")).toBe('"2"');
    expect(sent?.body).toEqual({ decision: "PARTIALLY_ACCEPTED", acceptedAmounts: ["250.00"] });
    expect(screen.getByRole("form", { name: "Match credit note" })).toBeInTheDocument();
  });

  it("settles with the credit note in the claim currency and closes after confirming", async () => {
    let settled = false;
    handler = detail(claim({ status: "PARTIALLY_ACCEPTED", version: 3, acceptedTotal: "250", vendorReference: "VR-1" }), (url) => {
      if (url.pathname.endsWith(":settle")) {
        settled = true;
        return { status: 200, body: claim({ status: "SETTLED", version: 4, acceptedTotal: "250", recoveredAmount: "250", creditNoteId: "CN-9", vendorReference: "VR-1" }), etag: '"4"' };
      }
      return { status: 200, body: claim({ status: "CLOSED", version: 5, recoveredAmount: "250", creditNoteId: "CN-9" }), etag: '"5"' };
    });
    renderAt(`/service/supplier-claims/${CLAIM}`);
    fireEvent.change(await screen.findByLabelText("Credit note"), { target: { value: "CN-9" } });
    fireEvent.change(screen.getByLabelText("Vendor reference"), { target: { value: "VR-1" } });
    fireEvent.change(screen.getByLabelText(/Credited amount/), { target: { value: "250.00" } });
    fireEvent.click(screen.getByRole("button", { name: "Settle claim" }));
    expect(await findStatus()).toHaveTextContent("Claim settled: 250 USD recovered.");
    expect(settled).toBe(true);
    expect(writes(":settle")[0]?.body).toEqual({ creditNoteId: "CN-9", vendorReference: "VR-1", amount: "250.00", currency: "USD" });
    fireEvent.click(screen.getByRole("button", { name: "Close claim" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent("recovery stays posted");
    fireEvent.click(within(dialog).getByRole("button", { name: "Close claim" }));
    await waitFor(() => expect(writes(":close")).toHaveLength(1));
    expect(writes(":close")[0]?.headers.get("If-Match")).toBe('"4"');
  });

  it("reloads when the claim changed underneath", async () => {
    let version = 2;
    handler = (_url, init) => {
      if (init.method === "POST") {
        version = 3;
        return { status: 412, body: { code: "conflict_version", detail: "current version is 3" } };
      }
      return { status: 200, body: claim({ status: "SUBMITTED", version }), etag: `"${version}"` };
    };
    renderAt(`/service/supplier-claims/${CLAIM}`);
    fireEvent.change(await screen.findByLabelText("Decision"), { target: { value: "ACCEPTED" } });
    fireEvent.click(screen.getByRole("button", { name: "Record decision" }));
    expect(await findAlert()).toHaveTextContent("The claim changed; it was reloaded");
    await waitFor(() => expect(calls.filter((c) => c.method === "GET")).toHaveLength(2));
  });

  it("shows a failed load", async () => {
    handler = () => ({ status: 404, body: { code: "SVC-SWC-007", detail: "supplier claim not found" } });
    renderAt(`/service/supplier-claims/${CLAIM}`);
    expect(await findAlert()).toHaveTextContent("Could not load the claim");
  });
});

describe("case mailbox", () => {
  const categories = {
    items: [
      { id: CATEGORY, code: "INQ", name: "Inquiries", categoryType: "INTERNAL", defaultPriority: 3, reopenWindowDays: 14, accessDuty: "", resolutionCodes: [] },
      { id: "77777777-0000-4000-8000-000000000008", code: "COLL", name: "Collections", categoryType: "COLLECTIONS", defaultPriority: 3, reopenWindowDays: 14, accessDuty: "", resolutionCodes: [] },
      { id: "77777777-0000-4000-8000-000000000009", code: "SVC", name: "Service", categoryType: "SERVICE", defaultPriority: 3, reopenWindowDays: 14, accessDuty: "", resolutionCodes: [] },
    ],
  };
  const mailbox = { address: "service@example.test", legalEntityId: LE, categoryId: CATEGORY, categoryCode: "INQ", updatedAt: "2026-10-28T09:00:00Z" };
  const message = (overrides: Record<string, unknown> = {}) => ({
    id: "bbbbbbbb-0000-4000-8000-00000000000b",
    mailbox: "service@example.test",
    messageId: "<m1@customer.test>",
    inReplyTo: "",
    fromAddress: "pat@customer.test",
    subject: "pump noise",
    body: "It rattles.",
    receivedAt: "2026-10-28T09:00:00Z",
    caseId: CASE,
    caseNumber: "CS-001042",
    route: "NEW_CASE",
    automated: false,
    acknowledge: true,
    attachments: [],
    createdAt: "2026-10-28T09:00:00Z",
    ...overrides,
  });

  it("lists mailboxes and inbound email with where each message landed, and filters by mailbox", async () => {
    handler = (url) => {
      if (url.pathname === "/api/v1/case-mailboxes") return { status: 200, body: { items: [mailbox] } };
      if (url.pathname === "/api/v1/case-categories") return { status: 200, body: categories };
      return {
        status: 200,
        body: {
          items: [
            message(),
            message({ id: "bbbbbbbb-0000-4000-8000-00000000000c", messageId: "<m2@c>", subject: "Re: [CS-1042] pump noise", route: "ATTACHED", acknowledge: false }),
            message({ id: "bbbbbbbb-0000-4000-8000-00000000000d", messageId: "<m3@c>", subject: "Out of office", acknowledge: false, automated: true, caseNumber: "CS-001044" }),
          ],
        },
      };
    };
    const { container } = renderAt("/service/case-mailbox");
    const boxes = await findTable({ name: "Case mailboxes" });
    expect(within(boxes).getByText("service@example.test")).toBeInTheDocument();
    const inbound = await findTable({ name: "Inbound email" });
    expect(within(inbound).getByText("New case CS-001042")).toBeInTheDocument();
    expect(within(inbound).getByText("Attached to CS-001042")).toBeInTheDocument();
    expect(within(inbound).getByText("No (auto-reply)")).toBeInTheDocument();
    // Email cannot open service or collections cases, so the form leaves them out.
    await waitFor(() => expect(screen.getByRole("option", { name: "INQ — Inquiries" })).toBeInTheDocument());
    expect(screen.queryByRole("option", { name: "COLL — Collections" })).toBeNull();
    expect(screen.queryByRole("option", { name: "SVC — Service" })).toBeNull();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Mailbox"), { target: { value: "service@example.test" } });
    await waitFor(() => expect(calls.some((c) => c.url.pathname === "/api/v1/inbound-messages" && c.url.searchParams.get("mailbox") === "service@example.test")).toBe(true));
  });

  it("saves a mailbox and shows the service's refusal", async () => {
    let refuse = false;
    handler = (url, init) => {
      if (init.method === "POST") {
        return refuse
          ? { status: 422, body: { code: "CASE-MAIL-003", detail: "a mailbox cannot default to a COLLECTIONS category" } }
          : { status: 201, body: mailbox };
      }
      if (url.pathname === "/api/v1/case-categories") return { status: 200, body: categories };
      return empty;
    };
    renderAt("/service/case-mailbox");
    expect(await screen.findByText("No case mailboxes yet")).toBeInTheDocument();
    expect(await screen.findByText("No inbound email")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Save mailbox" }));
    expect(await screen.findByText("Enter the mailbox's email address.")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("option", { name: "INQ — Inquiries" })).toBeInTheDocument());
    fireEvent.change(screen.getByLabelText("Address"), { target: { value: "Service@Example.test" } });
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Default category"), { target: { value: CATEGORY } });
    fireEvent.click(screen.getByRole("button", { name: "Save mailbox" }));
    expect(await findStatus()).toHaveTextContent("Mail to service@example.test now opens cases in INQ.");
    expect(writes("/api/v1/case-mailboxes")[0]?.body).toEqual({ address: "service@example.test", legalEntityId: LE, categoryId: CATEGORY });
    refuse = true;
    fireEvent.change(screen.getByLabelText("Address"), { target: { value: "ar@example.test" } });
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Default category"), { target: { value: CATEGORY } });
    fireEvent.click(screen.getByRole("button", { name: "Save mailbox" }));
    expect(await findAlert()).toHaveTextContent("CASE-MAIL-003");
  });

  it("shows a failed mailbox load with a retry", async () => {
    handler = (url) => (url.pathname === "/api/v1/case-mailboxes" ? { status: 500, body: { code: "COM-0500", detail: "boom" } } : empty);
    renderAt("/service/case-mailbox");
    expect(await findAlert()).toHaveTextContent("Could not load the mailboxes");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});
