import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import { findAlert, findTable } from "../../test-utils";
import { RECORD_APPROVE_DUTY, RECORD_WRITE_DUTY } from "./EbrPages";
import processmfg from "./module";

const ITEM = "1a000000-0000-4000-8000-000000000001";
const BATCH = "2b000000-0000-4000-8000-000000000002";
const RECORD = "7e000000-0000-4000-8000-0000000000a1";
const EXC1 = "7e000000-0000-4000-8000-0000000000b1";
const EXC2 = "7e000000-0000-4000-8000-0000000000b2";
const SIG = "7e000000-0000-4000-8000-0000000000c1";
const PROFILE = "7e000000-0000-4000-8000-0000000000d1";

type Reply = { status: number; body: unknown };
let handler: (url: URL, init: RequestInit) => Reply | undefined;
let calls: { method: string; url: URL; init: RequestInit; body: unknown }[];

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const READ = "production.batch-order.read";

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, init, body: init.body ? JSON.parse(String(init.body)) : undefined });
      const reply = handler(url, init) ?? { status: 404, body: { code: "TEST-404", detail: `no fake for ${url.pathname}` } };
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});

afterEach(() => vi.unstubAllGlobals());

function renderAt(path: string, duties: string[] = [READ, RECORD_WRITE_DUTY, RECORD_APPROVE_DUTY]) {
  return render(
    <PrincipalProvider duties={duties}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          {processmfg.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
        </Routes>
      </MemoryRouter>
    </PrincipalProvider>,
  );
}

const exception = (id: string, over: Record<string, unknown> = {}) => ({
  id,
  sourceType: "attribute",
  sourceId: "11",
  description: "potency outside its limits: 70 is above 60",
  severity: "major",
  createdBy: "u",
  createdAt: "2026-10-20T08:00:00Z",
  ...over,
});

const record = (over: Record<string, unknown> = {}) => ({
  id: RECORD,
  legalEntityId: "11111111-0000-4000-8000-000000000001",
  batchOrderId: "3c000000-0000-4000-8000-000000000003",
  batchId: BATCH,
  batchNumber: "L-100",
  itemId: ITEM,
  profileId: PROFILE,
  status: "in-review",
  compiledAt: "2026-10-20T08:00:00Z",
  version: 3,
  exceptionCount: 2,
  openExceptions: 2,
  createdBy: "u",
  createdAt: "2026-10-20T08:00:00Z",
  updatedAt: "2026-10-20T08:00:00Z",
  exceptions: [exception(EXC1), exception(EXC2, { sourceType: "manual", description: "Torn seal on drum 4", severity: "critical" })],
  ...over,
});

const profile = (over: Record<string, unknown> = {}) => ({
  id: PROFILE,
  legalEntityId: "11111111-0000-4000-8000-000000000001",
  code: "EBR-1",
  itemId: ITEM,
  approvalRequiredForRelease: true,
  version: 1,
  createdBy: "u",
  createdAt: "2026-10-20T08:00:00Z",
  updatedAt: "2026-10-20T08:00:00Z",
  ...over,
});

function fill(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

describe("batch record list (slice 15)", () => {
  it("lists the records with status and open exceptions, filtered by status", async () => {
    handler = (url) => (url.pathname === "/api/v1/production/batch-records" ? { status: 200, body: { items: [record({ exceptions: undefined })] } } : undefined);
    const { container } = renderAt("/processmfg/batch-records");
    const table = await findTable({ name: "Batch records" });
    expect(within(table).getByText("L-100")).toBeInTheDocument();
    expect(within(table).getByText("In review")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "sealed" } });
    await waitFor(() => expect(calls.some((c) => c.url.searchParams.get("status") === "sealed")).toBe(true));
    await waitFor(() => expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]));
  });

  it("shows the empty state and a load failure with a retry", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    const first = renderAt("/processmfg/batch-records");
    expect(await screen.findByText("No batch records yet")).toBeInTheDocument();
    first.unmount();
    handler = () => ({ status: 500, body: { code: "COM-0500", detail: "database unavailable" } });
    renderAt("/processmfg/batch-records");
    expect(await findAlert()).toHaveTextContent("database unavailable (COM-0500)");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("compiles the record of a batch under one key and opens it", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return { status: 200, body: record() };
      if (url.pathname.endsWith(`/${RECORD}`)) return { status: 200, body: record() };
      return { status: 200, body: { items: [] } };
    };
    renderAt("/processmfg/batch-records");
    await screen.findByText("No batch records yet");
    fireEvent.click(screen.getByRole("button", { name: "Compile record" }));
    expect(await screen.findByText("Enter the batch id as a UUID.")).toBeInTheDocument();
    fill("Batch id", BATCH);
    fireEvent.click(screen.getByRole("button", { name: "Compile record" }));
    expect(await screen.findByText("Batch record of L-100")).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.body).toEqual({ batchId: BATCH });
    expect(new Headers(post.init.headers).get("Idempotency-Key")).toBeTruthy();
  });

  it("hides the compile form without the write duty and shows a refusal", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    const first = renderAt("/processmfg/batch-records", [READ]);
    await screen.findByText("No batch records yet");
    expect(screen.queryByRole("button", { name: "Compile record" })).toBeNull();
    first.unmount();
    handler = (_url, init) => (init.method === "POST" ? { status: 409, body: { code: "PMF-0119", detail: "item has no active batch record profile" } } : { status: 200, body: { items: [] } });
    renderAt("/processmfg/batch-records");
    await screen.findByText("No batch records yet");
    fill("Batch id", BATCH);
    fireEvent.click(screen.getByRole("button", { name: "Compile record" }));
    expect(await findAlert()).toHaveTextContent("PMF-0119");
  });
});

describe("batch record review (slice 15)", () => {
  it("decides an exception with a signature at the version read, then approves", async () => {
    let state = record();
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(":disposition")) {
        state = record({ version: 4, openExceptions: 1, exceptions: [exception(EXC1, { disposition: "deviation-raised", signatureId: SIG }), state.exceptions![1]] });
        return { status: 200, body: state };
      }
      if (init.method === "POST" && url.pathname.endsWith(":approve")) {
        state = record({ status: "approved", version: 5, openExceptions: 0 });
        return { status: 200, body: state };
      }
      if (url.pathname.endsWith(`/${RECORD}`)) return { status: 200, body: state };
      return undefined;
    };
    const { container } = renderAt(`/processmfg/batch-records/${RECORD}`);
    const table = await findTable({ name: "Exceptions" });
    expect(within(table).getAllByText("Open")).toHaveLength(2);
    fireEvent.click(within(table).getAllByRole("button", { name: "Decide" })[0]);
    expect(screen.getAllByText(/signs it electronically under your name/).length).toBeGreaterThan(0);
    fireEvent.click(screen.getByRole("button", { name: "Record decision" }));
    expect(await screen.findByText("Enter your printed name to sign.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Decision"), { target: { value: "deviation-raised" } });
    fill("Printed name", "Rita Reviewer");
    fill("Comment", "DEV-12");
    fireEvent.click(screen.getByRole("button", { name: "Record decision" }));
    await waitFor(() => expect(calls.some((c) => c.url.pathname.endsWith(":disposition"))).toBe(true));
    const post = calls.find((c) => c.url.pathname.endsWith(":disposition"))!;
    expect(post.url.pathname).toBe(`/api/v1/production/batch-records/${RECORD}/exceptions/${EXC1}:disposition`);
    expect(post.body).toEqual({ version: 3, disposition: "deviation-raised", comment: "DEV-12", printedName: "Rita Reviewer" });
    expect(new Headers(post.init.headers).get("Idempotency-Key")).toBeTruthy();
    await waitFor(() => expect(within(screen.getByRole("table", { name: "Exceptions" })).getByText("Deviation raised")).toBeInTheDocument());
    // A decision is final: only the open exception can still be decided.
    expect(within(screen.getByRole("table", { name: "Exceptions" })).getAllByRole("button", { name: "Decide" })).toHaveLength(1);
    await waitFor(() => expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]));

    fill("Printed name for the approval", "Quinn QA");
    fireEvent.click(screen.getByRole("button", { name: "Approve record" }));
    await waitFor(() => expect(calls.some((c) => c.url.pathname.endsWith(":approve"))).toBe(true));
    expect(calls.find((c) => c.url.pathname.endsWith(":approve"))!.body).toEqual({ version: 4, printedName: "Quinn QA" });
    expect(await screen.findByRole("button", { name: "Seal record" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve record" })).toBeNull();
  });

  it("shows PMF-0014 when approval is refused with open exceptions, and a stale version offers a reload", async () => {
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(":approve")) return { status: 422, body: { code: "PMF-0014", detail: "batch record cannot be approved: 2 exceptions without disposition and signature" } };
      if (init.method === "POST") return { status: 409, body: { code: "PMF-0104", detail: "batch record of batch L-100 is at version 5, not 3" } };
      return { status: 200, body: record() };
    };
    renderAt(`/processmfg/batch-records/${RECORD}`);
    await screen.findByRole("table", { name: "Exceptions" });
    fill("Printed name for the approval", "Quinn QA");
    fireEvent.click(screen.getByRole("button", { name: "Approve record" }));
    expect(await findAlert()).toHaveTextContent("PMF-0014");
    fill("What was found", "Foam in the vessel");
    fireEvent.click(screen.getByRole("button", { name: "Raise exception" }));
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("PMF-0104"));
    expect(screen.getByRole("button", { name: "Reload" })).toBeInTheDocument();
  });

  it("raises an exception by hand and compiles again", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return { status: 200, body: record() };
      if (url.pathname.endsWith(`/${RECORD}`)) return { status: 200, body: record() };
      return undefined;
    };
    renderAt(`/processmfg/batch-records/${RECORD}`);
    await screen.findByRole("table", { name: "Exceptions" });
    fireEvent.click(screen.getByRole("button", { name: "Raise exception" }));
    expect(await screen.findByText("Describe the exception in 1 to 500 characters.")).toBeInTheDocument();
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(0);
    fill("What was found", "Torn seal on drum 4");
    fireEvent.change(screen.getByLabelText("Severity"), { target: { value: "critical" } });
    fireEvent.click(screen.getByRole("button", { name: "Raise exception" }));
    await waitFor(() => expect(calls.some((c) => c.url.pathname.endsWith("/exceptions"))).toBe(true));
    expect(calls.find((c) => c.url.pathname.endsWith("/exceptions"))!.body).toEqual({ version: 3, description: "Torn seal on drum 4", severity: "critical" });
    fireEvent.click(screen.getByRole("button", { name: "Compile again" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST" && c.url.pathname === "/api/v1/production/batch-records")).toBe(true));
    expect(calls.find((c) => c.url.pathname === "/api/v1/production/batch-records" && c.method === "POST")!.body).toEqual({ batchId: BATCH });
  });

  it("seals an approved record and shows the hash and the document once sealed", async () => {
    let state = record({ status: "approved", openExceptions: 0, version: 6 });
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(":seal")) {
        state = record({ status: "sealed", version: 7, openExceptions: 0, sealedBy: "qa-1", sealedAt: "2026-10-21T08:00:00Z", contentHash: "ab".repeat(32), sealedDocument: '{"recordId":"x"}' });
        return { status: 200, body: state };
      }
      return { status: 200, body: state };
    };
    renderAt(`/processmfg/batch-records/${RECORD}`);
    fireEvent.click(await screen.findByRole("button", { name: "Seal record" }));
    await waitFor(() => expect(calls.find((c) => c.url.pathname.endsWith(":seal"))?.body).toEqual({ version: 6 }));
    expect(await screen.findByText("ab".repeat(32))).toBeInTheDocument();
    expect(screen.getByText('{"recordId":"x"}')).toBeInTheDocument();
    expect(screen.getByText(/not a 21 CFR Part 11 signed record/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Download the PDF" })).toHaveAttribute("href", `/api/v1/production/batch-records/${RECORD}/pdf`);
    expect(screen.queryByRole("button", { name: "Seal record" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Compile again" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Decide" })).toBeNull();
  });

  it("offers only what the duties allow", async () => {
    handler = () => ({ status: 200, body: record() });
    renderAt(`/processmfg/batch-records/${RECORD}`, [READ]);
    await screen.findByRole("table", { name: "Exceptions" });
    for (const name of ["Approve record", "Compile again", "Raise exception"]) {
      expect(screen.queryByRole("button", { name })).toBeNull();
    }
    expect(screen.queryByRole("button", { name: "Decide" })).toBeNull();
  });

  it("splits review from approval: the reviewer decides, QA approves, and PMF-0124 is shown", async () => {
    const decided = record({ openExceptions: 1, exceptions: [exception(EXC1, { disposition: "accepted", signatureId: SIG, dispositionedBy: "rev-1" }), exception(EXC2)] });
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(":approve"))
        return { status: 422, body: { code: "PMF-0124", detail: "rev-1 reviewed this batch record and cannot also approve it; another QA approver approves" } };
      return { status: 200, body: decided };
    };
    const reviewer = renderAt(`/processmfg/batch-records/${RECORD}`, [READ, RECORD_WRITE_DUTY]);
    const table = await findTable({ name: "Exceptions" });
    expect(within(table).getByText("rev-1")).toBeInTheDocument();
    expect(within(table).getAllByRole("button", { name: "Decide" })).toHaveLength(1);
    expect(screen.queryByRole("button", { name: "Approve record" })).toBeNull();
    reviewer.unmount();

    renderAt(`/processmfg/batch-records/${RECORD}`, [READ, RECORD_APPROVE_DUTY]);
    await screen.findByRole("table", { name: "Exceptions" });
    expect(screen.queryByRole("button", { name: "Decide" })).toBeNull();
    fill("Printed name for the approval", "Quinn QA");
    fireEvent.click(screen.getByRole("button", { name: "Approve record" }));
    expect(await findAlert()).toHaveTextContent("PMF-0124");
  });

  it("shows a record with no exceptions and a load failure", async () => {
    handler = () => ({ status: 200, body: record({ exceptions: [], exceptionCount: 0, openExceptions: 0 }) });
    const first = renderAt(`/processmfg/batch-records/${RECORD}`);
    expect(await screen.findByText("No exceptions")).toBeInTheDocument();
    first.unmount();
    handler = () => ({ status: 404, body: { code: "PMF-0118", detail: "batch record not found" } });
    renderAt(`/processmfg/batch-records/${RECORD}`);
    expect(await findAlert()).toHaveTextContent("PMF-0118");
  });
});

describe("batch record profiles (slice 15)", () => {
  it("lists profiles, retires one at the version read and filters by item", async () => {
    let retired = false;
    handler = (url, init) => {
      if (init.method === "POST") {
        retired = true;
        return { status: 200, body: profile({ version: 2, retiredAt: "2026-10-22T08:00:00Z" }) };
      }
      if (url.pathname === "/api/v1/production/batch-record-profiles") {
        return { status: 200, body: { items: [retired ? profile({ version: 2, retiredAt: "2026-10-22T08:00:00Z" }) : profile()] } };
      }
      return undefined;
    };
    const { container } = renderAt("/processmfg/batch-record-profiles");
    const table = await findTable({ name: "Batch record profiles" });
    expect(within(table).getByText("EBR-1")).toBeInTheDocument();
    expect(within(table).getByText("Active")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Define profile" })).toHaveAttribute("href", "/processmfg/batch-record-profiles/new");
    fireEvent.click(within(table).getByRole("button", { name: "Retire EBR-1" }));
    await waitFor(() => expect(within(screen.getByRole("table", { name: "Batch record profiles" })).getByText("Retired")).toBeInTheDocument());
    expect(calls.find((c) => c.method === "POST")!.body).toEqual({ version: 1 });
    fill("Item id", ITEM);
    await waitFor(() => expect(calls.some((c) => c.url.searchParams.get("itemId") === ITEM)).toBe(true));
    await waitFor(() => expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]));
  });

  it("shows the empty state, flags a bad filter and hides the actions without the duty", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    renderAt("/processmfg/batch-record-profiles", [READ]);
    expect(await screen.findByText("No batch record profiles yet")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Define profile" })).toBeNull();
    fill("Item id", "nope");
    expect(await screen.findByText("Enter the item id as a UUID.")).toBeInTheDocument();
    expect(calls.every((c) => c.url.searchParams.get("itemId") !== "nope")).toBe(true);
  });

  it("shows a load failure with a retry", async () => {
    handler = () => ({ status: 500, body: { code: "COM-0500", detail: "database unavailable" } });
    renderAt("/processmfg/batch-record-profiles");
    expect(await findAlert()).toHaveTextContent("database unavailable (COM-0500)");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("defines a profile under one key after client-side checks", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return { status: 201, body: profile() };
      if (url.pathname === "/api/v1/production/batch-record-profiles") return { status: 200, body: { items: [profile()] } };
      return undefined;
    };
    renderAt("/processmfg/batch-record-profiles/new");
    fireEvent.click(screen.getByRole("button", { name: "Define profile" }));
    expect(await screen.findByText(/Use 1 to 40 upper-case letters/)).toBeInTheDocument();
    expect(screen.getByText("Enter the item id as a UUID.")).toBeInTheDocument();
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(0);
    fill("Code", "EBR-1");
    fill("Item id", ITEM);
    fireEvent.click(screen.getByLabelText(/needs the approved record/));
    fireEvent.click(screen.getByRole("button", { name: "Define profile" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.body).toEqual({ code: "EBR-1", itemId: ITEM, approvalRequiredForRelease: false });
    expect(new Headers(post.init.headers).get("Idempotency-Key")).toBeTruthy();
    expect(await screen.findByText("Batch record profiles")).toBeInTheDocument();
  });

  it("shows a refusal and explains a missing duty", async () => {
    handler = () => ({ status: 409, body: { code: "PMF-0116", detail: "item already has an active batch record profile" } });
    const first = renderAt("/processmfg/batch-record-profiles/new");
    fill("Code", "EBR-1");
    fill("Item id", ITEM);
    fireEvent.click(screen.getByRole("button", { name: "Define profile" }));
    expect(await findAlert()).toHaveTextContent("PMF-0116");
    first.unmount();
    renderAt("/processmfg/batch-record-profiles/new", [READ]);
    expect(screen.getByText(/needs the batch record duty/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Define profile" })).toBeNull();
  });
});
