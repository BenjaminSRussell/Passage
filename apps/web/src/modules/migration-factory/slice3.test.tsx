import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { rekeyFileUrl, type CutoverPlan } from "./api/client";
import { parsePostings } from "./domain";
import migrationFactory from "./module";

const PLAN = "0195f0a0-0000-7000-8000-0000000000b1";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

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

afterEach(() => vi.unstubAllGlobals());

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {migrationFactory.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const rollbacks = () => calls.filter((c) => c.method === "POST" && c.url.pathname.endsWith("/rollback"));

const plan: CutoverPlan = {
  planId: PLAN,
  t0: "2027-01-01T00:00:00Z",
  packs: ["open_items"],
  entities: ["USMF", "DEMF"],
  abortOffsetMin: 2160,
  rolledBack: false,
  approvers: [],
  version: 5,
  createdAt: "2026-12-01T00:00:00Z",
  stepsDone: 0,
  stepsTotal: 1,
  openGoNoGo: ["Go/no-go meeting"],
  abortDue: true,
  steps: [{ seq: 1, offsetMin: 1440, name: "Go/no-go meeting", owner: "Cutover manager", plannedDurationMin: 60, actualDurationMin: 0, status: "PENDING", goNoGo: true }],
};

const rolled: CutoverPlan = {
  ...plan,
  rolledBack: true,
  approvers: ["alice", "bob"],
  version: 6,
  abortDue: false,
  rekeying: { journalLines: 2, documentLines: 1, excluded: 1 },
};

const pasted = [
  "kind,entity,documentRef,postedAt,account,dimensions,currency,amount,text",
  "JOURNAL,USMF,JV-1,2027-01-01T09:00:00Z,6100,DEPT=SALES,USD,80.00,Travel",
  "JOURNAL;USMF;JV-1;2027-01-01T09:00:00Z;2000;;usd;-80.00",
  "DOCUMENT\tDEMF\tSI-9\t2027-01-02T09:00:00+01:00\t1200\t\tEUR\t125.50\tInvoice",
].join("\n");

describe("parsePostings", () => {
  it("reads journal and document lines with any separator, skips the header and sends UTC times", () => {
    const { postings, errors } = parsePostings(pasted, plan.entities);
    expect(errors).toEqual([]);
    expect(postings).toEqual([
      { kind: "JOURNAL", entity: "USMF", documentRef: "JV-1", postedAt: "2027-01-01T09:00:00Z", account: "6100", dimensions: "DEPT=SALES", currency: "USD", amount: "80.00", text: "Travel" },
      { kind: "JOURNAL", entity: "USMF", documentRef: "JV-1", postedAt: "2027-01-01T09:00:00Z", account: "2000", currency: "USD", amount: "-80.00" },
      { kind: "DOCUMENT", entity: "DEMF", documentRef: "SI-9", postedAt: "2027-01-02T08:00:00Z", account: "1200", currency: "EUR", amount: "125.50", text: "Invoice" },
    ]);
  });

  it("reports each bad line by number and keeps the good ones", () => {
    const text = [
      "TRANSFER,USMF,JV-1,2027-01-01T09:00:00Z,1100,,USD,1",
      "JOURNAL,GBSI,JV-2,2027-01-01T09:00:00Z,1100,,USD,1",
      "JOURNAL,USMF,JV-3,02.01.2027,1100,,USD,1",
      "JOURNAL,USMF,JV-4,2027-01-01T09:00:00Z,1100,,USD,1.5e3",
      "JOURNAL,USMF,JV-5,2027-01-01T09:00:00Z,1100,,US,1",
      "JOURNAL,USMF,,2027-01-01T09:00:00Z,1100,,USD,1",
      "JOURNAL,USMF,JV-6",
      "DOCUMENT,USMF,SI-1,2027-01-01T09:00:00Z,1200,,USD,10",
    ].join("\n");
    const { postings, errors } = parsePostings(text, plan.entities);
    expect(postings.map((p) => p.documentRef)).toEqual(["SI-1"]);
    expect(errors).toEqual([
      'Line 1: kind "TRANSFER" is not JOURNAL or DOCUMENT.',
      "Line 2: entity GBSI is not cut over by this plan (USMF, DEMF).",
      'Line 3: posted at "02.01.2027" is not a date-time such as 2027-01-02T10:00:00Z.',
      'Line 4: amount "1.5e3" is not a decimal such as -250.00.',
      'Line 5: currency "US" is not a three-letter code.',
      "Line 6: the entity, document and account are required.",
      "Line 7: expected 8 or 9 values (kind, entity, documentRef, postedAt, account, dimensions, currency, amount, text), found 3.",
    ]);
  });

  it("treats a blank paste as nothing posted", () => {
    expect(parsePostings("\n  \n", plan.entities)).toEqual({ postings: [], errors: [] });
  });
});

describe("Rollback re-keying export", () => {
  it("sends the parsed postings with the approvers and shows the re-keying files", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(200, rolled) : json(200, plan));
    const { container } = renderAt(`/migration-factory/cutover/${PLAN}`);
    fireEvent.change(await screen.findByLabelText("First approver"), { target: { value: "alice" } });
    fireEvent.change(screen.getByLabelText("Second approver"), { target: { value: "bob" } });
    fireEvent.change(screen.getByLabelText(/Postings since T-0/), { target: { value: pasted } });
    fireEvent.click(screen.getByRole("button", { name: "Roll back cutover" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent("3 posting lines are sent for re-keying");
    fireEvent.click(within(dialog).getByRole("button", { name: "Roll back" }));

    expect(await screen.findByRole("heading", { name: "Re-keying files" })).toBeInTheDocument();
    const [post] = rollbacks();
    expect(post.body).toMatchObject({ approvers: ["alice", "bob"], version: 5 });
    expect((post.body as { postings: unknown[] }).postings).toHaveLength(3);
    expect(new Headers(post.init.headers).get("Idempotency-Key")).toBeTruthy();

    const stats = screen.getByRole("list", { name: "Re-keying export" });
    expect(stats).toHaveTextContent("Journal lines 2");
    expect(stats).toHaveTextContent("Document lines 1");
    expect(stats).toHaveTextContent("Left out (before T-0) 1");
    expect(screen.getByRole("link", { name: "Download journals (CSV)" })).toHaveAttribute("href", rekeyFileUrl(PLAN, "journals.csv"));
    expect(screen.getByRole("link", { name: "Download documents (CSV)" })).toHaveAttribute("href", `/api/v1/cutover/plans/${PLAN}/rekeying/documents.csv`);
    expect(screen.queryByLabelText(/Postings since T-0/)).not.toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("stops a paste with bad lines before anything is sent", async () => {
    handler = () => json(200, plan);
    renderAt(`/migration-factory/cutover/${PLAN}`);
    fireEvent.change(await screen.findByLabelText("First approver"), { target: { value: "alice" } });
    fireEvent.change(screen.getByLabelText("Second approver"), { target: { value: "bob" } });
    fireEvent.change(screen.getByLabelText(/Postings since T-0/), { target: { value: "JOURNAL,GBSI,JV-2,2027-01-01T09:00:00Z,1100,,USD,1" } });
    fireEvent.click(screen.getByRole("button", { name: "Roll back cutover" }));
    expect(await screen.findByText(/entity GBSI is not cut over by this plan/)).toBeInTheDocument();
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    expect(rollbacks()).toHaveLength(0);
  });

  it("shows the service's rejection and leaves the plan not rolled back", async () => {
    handler = (_url, init) =>
      init.method === "POST" ? json(422, { code: "MIG-0001", detail: "journal JV-1 of USMF does not balance in USD (difference 1.000000)" }) : json(200, plan);
    renderAt(`/migration-factory/cutover/${PLAN}`);
    fireEvent.change(await screen.findByLabelText("First approver"), { target: { value: "alice" } });
    fireEvent.change(screen.getByLabelText("Second approver"), { target: { value: "bob" } });
    fireEvent.change(screen.getByLabelText(/Postings since T-0/), { target: { value: "JOURNAL,USMF,JV-1,2027-01-01T09:00:00Z,1100,,USD,1" } });
    fireEvent.click(screen.getByRole("button", { name: "Roll back cutover" }));
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Roll back" }));
    expect(await screen.findByText(/does not balance in USD/)).toBeInTheDocument();
    expect(screen.getByText(/MIG-0001/)).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Re-keying files" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Roll back cutover" })).toBeInTheDocument();
  });

  it("shows the files of a plan that was already rolled back", async () => {
    handler = () => json(200, { ...rolled, rekeying: { journalLines: 0, documentLines: 0, excluded: 0 } });
    renderAt(`/migration-factory/cutover/${PLAN}`);
    expect(await screen.findByRole("heading", { name: "Re-keying files" })).toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("list", { name: "Re-keying export" })).toHaveTextContent("Journal lines 0"));
    expect(screen.getByText("Rolled back with the approval of alice and bob.")).toBeInTheDocument();
  });
});
