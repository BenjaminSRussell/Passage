import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { validatePost, validateProfile } from "./LeasePostingPage";
import assets from "./module";

const LE = "11111111-0000-4000-8000-000000000001";
const LEDGER = "99999999-0000-4000-8000-000000000009";
const LEASE = "aaaaaaaa-0000-4000-8000-00000000000a";
const ENTRY = "bbbbbbbb-0000-4000-8000-00000000000b";

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
        {assets.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const posts = () => calls.filter((c) => c.method !== "GET");

const lease = {
  id: LEASE,
  legalEntityId: LE,
  leaseNumber: "L-2026-01",
  commencementDate: "2026-01-01",
  termMonths: 60,
  paymentsPerYear: 1,
  paymentAmount: "10000",
  paymentTiming: "arrears",
  ibrRate: "0.05",
  rateMethod: "Nominal",
  currency: "USD",
  liability: "43294.77",
  rouAsset: "43294.77",
  status: "Active",
  version: 1,
  createdAt: "2026-01-01T00:00:00Z",
  schedule: [
    { periodNo: 1, opening: "43294.77", interest: "2164.74", payment: "10000", closing: "35459.51", rouDepreciation: "8658.95" },
    { periodNo: 2, opening: "35459.51", interest: "1772.98", payment: "10000", closing: "27232.49", rouDepreciation: "8658.95" },
  ],
};

const placeholder = {
  legalEntityId: LE,
  rouAssetAccount: "1700",
  rouAccumulatedDepreciationAccount: "1790",
  liabilityAccount: "2400",
  interestExpenseAccount: "7200",
  depreciationExpenseAccount: "6820",
  paymentClearingAccount: "2110",
  remeasurementGainAccount: "7120",
  terminationGainLossAccount: "7130",
  exemptLeaseExpenseAccount: "6830",
  placeholder: true,
};

describe("lease ledger posting", () => {
  it("validates like the service", () => {
    const base = { legalEntityId: LE, ledgerId: LEDGER, postThrough: "2026-12-31", leaseId: "" };
    expect(validatePost(base).input).toEqual({ legalEntityId: LE, ledgerId: LEDGER, postThrough: "2026-12-31" });
    expect(validatePost({ ...base, leaseId: LEASE }).input?.leaseId).toBe(LEASE);
    expect(Object.keys(validatePost({ legalEntityId: "x", ledgerId: "", postThrough: "31/12/2026", leaseId: "y" }).errors).sort()).toEqual(["leaseId", "ledgerId", "legalEntityId", "postThrough"]);
    expect(validatePost({ ...base, postThrough: "2026-13-45" }).errors.postThrough).toBeDefined();
    const ok = Object.fromEntries(["rouAssetAccount", "rouAccumulatedDepreciationAccount", "liabilityAccount", "interestExpenseAccount", "depreciationExpenseAccount", "paymentClearingAccount", "remeasurementGainAccount", "terminationGainLossAccount", "exemptLeaseExpenseAccount"].map((k) => [k, "1700"]));
    expect(validateProfile(ok as never)).toEqual({});
    expect(Object.keys(validateProfile({ ...ok, liabilityAccount: "a b" } as never))).toEqual(["liabilityAccount"]);
  });

  it("is registered with navigation gated on duties the service already knows", () => {
    const paths = assets.routes.map((r) => r.path);
    for (const path of ["/assets/leases/posting", "/assets/leases/posting-profile"]) expect(paths).toContain(path);
    expect(assets.nav.find((n) => n.to === "/assets/leases/posting")?.duty).toBe("assets.journal.post");
    expect(assets.nav.find((n) => n.to === "/assets/leases/posting-profile")?.duty).toBe("assets.lease.maintain");
  });

  it("starts empty, checks the form, posts and lists what was booked and what the ledger refused", async () => {
    handler = () => ({
      status: 200,
      body: {
        booked: [{ leaseId: LEASE, leaseNumber: "L-2026-01", period: 0, entryId: ENTRY, lines: 2, placeholderProfile: true }, { leaseId: LEASE, leaseNumber: "L-2026-01", period: 1, entryId: ENTRY, lines: 5 }],
        failed: [{ leaseId: LEASE, leaseNumber: "L-2026-02", period: 3, code: "LEDGER_PERIOD_CLOSED" }],
        more: true,
      },
    });
    const { container } = renderAt(`/assets/leases/posting?legalEntityId=${LE}&leaseId=${LEASE}`);
    expect(screen.getByText("Nothing posted yet")).toBeInTheDocument();
    expect(screen.getByLabelText("Legal entity")).toHaveValue(LE);
    expect(screen.getByLabelText("Lease (optional)")).toHaveValue(LEASE);
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Post to the ledger" }));
    expect(await screen.findByText("Enter the ledger's UUID.")).toBeInTheDocument();
    expect(posts()).toHaveLength(0);

    fireEvent.change(screen.getByLabelText("Ledger"), { target: { value: LEDGER } });
    fireEvent.change(screen.getByLabelText("Post through"), { target: { value: "2026-12-31" } });
    fireEvent.click(screen.getByRole("button", { name: "Post to the ledger" }));
    const booked = await findTable({ name: "Booked journals" });
    expect(within(booked).getByText("Recognition")).toBeInTheDocument();
    expect(within(booked).getByText("Period 1")).toBeInTheDocument();
    const refused = screen.getByRole("table", { name: "Journals not booked" });
    expect(within(refused).getByText("LEDGER_PERIOD_CLOSED")).toBeInTheDocument();
    expect(screen.getByText("Booked on placeholder accounts")).toBeInTheDocument();
    expect(screen.getByText("More journals are waiting")).toBeInTheDocument();
    expect(posts()[0].url.pathname).toBe("/api/v1/leases/gl-postings");
    expect(posts()[0].body).toEqual({ legalEntityId: LE, ledgerId: LEDGER, postThrough: "2026-12-31", leaseId: LEASE });
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows an empty result when everything is already booked", async () => {
    handler = () => ({ status: 200, body: { booked: [], failed: [], more: false } });
    renderAt(`/assets/leases/posting?legalEntityId=${LE}`);
    fireEvent.change(screen.getByLabelText("Ledger"), { target: { value: LEDGER } });
    fireEvent.click(screen.getByRole("button", { name: "Post to the ledger" }));
    expect(await screen.findByText("Nothing to book")).toBeInTheDocument();
    expect(screen.queryByText("Not booked")).not.toBeInTheDocument();
  });

  it("shows the service's refusal, such as a ledger that is not configured", async () => {
    handler = () => ({ status: 503, body: { code: "AST-0602", detail: "the general ledger is not configured for this service" } });
    renderAt(`/assets/leases/posting?legalEntityId=${LE}`);
    fireEvent.change(screen.getByLabelText("Ledger"), { target: { value: LEDGER } });
    fireEvent.click(screen.getByRole("button", { name: "Post to the ledger" }));
    expect(await findAlert()).toHaveTextContent("AST-0602");
  });

  it("shows which journals of a lease are in the ledger", async () => {
    handler = (url) =>
      url.pathname.endsWith("/gl-postings")
        ? { status: 200, body: { leaseId: LEASE, recognitionJournalId: ENTRY, periods: [{ periodNo: 1, periodEnd: "2026-12-31", postedJournalId: ENTRY }, { periodNo: 2, periodEnd: "2027-12-31", postedJournalId: null }] } }
        : { status: 200, body: lease, etag: '"1"' };
    const { container } = renderAt(`/assets/leases/${LEASE}`);
    const table = await findTable({ name: "Ledger postings" });
    expect(within(table).getByText("Recognition")).toBeInTheDocument();
    expect(within(table).getByText("2027-12-31")).toBeInTheDocument();
    expect(within(table).getByText("Not booked")).toBeInTheDocument();
    expect(screen.getByText("2 of 3 journals are in the ledger. A period that is booked cannot be remeasured.")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Post to the ledger" })).toHaveAttribute("href", `/assets/leases/posting?legalEntityId=${LE}&leaseId=${LEASE}`);
    expect(pageViolations(container)).toEqual([]);
  });

  it("keeps the lease readable when the ledger postings cannot be loaded", async () => {
    handler = (url) =>
      url.pathname.endsWith("/gl-postings")
        ? { status: 503, body: { code: "AST-0602", detail: "the general ledger is not configured for this service" } }
        : { status: 200, body: lease, etag: '"1"' };
    renderAt(`/assets/leases/${LEASE}`);
    expect(await screen.findByRole("heading", { name: "Lease L-2026-01" })).toBeInTheDocument();
    expect(await findAlert()).toHaveTextContent("Could not load the ledger postings");
    expect(screen.getByRole("table", { name: "Lease schedule" })).toBeInTheDocument();
  });
});

describe("lease accounts", () => {
  it("asks for a legal entity first, then shows the placeholder accounts and creates the profile", async () => {
    handler = (_url, init) =>
      init.method === "PUT"
        ? { status: 201, body: { ...placeholder, placeholder: false, version: 1 }, etag: '"1"' }
        : { status: 200, body: placeholder };
    const { container } = renderAt("/assets/leases/posting-profile");
    expect(screen.getByText("Choose a legal entity")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Show accounts" }));
    expect(await screen.findByText("Enter the legal entity's UUID.")).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.click(screen.getByRole("button", { name: "Show accounts" }));
    const form = await screen.findByRole("form", { name: "Lease posting accounts" });
    expect(screen.getByText("Placeholder accounts")).toBeInTheDocument();
    expect(within(form).getByLabelText("Lease liability")).toHaveValue("2400");
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(within(form).getByLabelText("Lease liability"), { target: { value: "2450" } });
    fireEvent.click(screen.getByRole("button", { name: "Save accounts" }));
    expect(await screen.findByText(/Journals booked from now on use these accounts/)).toBeInTheDocument();
    const put = posts()[0];
    expect(put.method).toBe("PUT");
    expect(put.url.pathname).toBe("/api/v1/leases/posting-profile");
    expect(put.headers.get("If-Match")).toBeNull();
    expect(put.body).toMatchObject({ legalEntityId: LE, liabilityAccount: "2450", rouAssetAccount: "1700" });
  });

  it("replaces a saved profile under its ETag and reports a stale one", async () => {
    const saved = { ...placeholder, placeholder: false, version: 3 };
    handler = (_url, init) =>
      init.method === "PUT" ? { status: 412, body: { code: "AST-0006", detail: "the lease posting profile was changed by another user; current version is 4" } } : { status: 200, body: saved, etag: '"3"' };
    renderAt(`/assets/leases/posting-profile?legalEntityId=${LE}`);
    fireEvent.click(screen.getByRole("button", { name: "Show accounts" }));
    await screen.findByRole("form", { name: "Lease posting accounts" });
    expect(screen.queryByText("Placeholder accounts")).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Interest expense"), { target: { value: "7250" } });
    fireEvent.click(screen.getByRole("button", { name: "Replace accounts" }));
    expect(await findAlert()).toHaveTextContent("The profile changed since you opened it");
    expect(posts()[0].headers.get("If-Match")).toBe('"3"');
    await waitFor(() => expect(screen.getByRole("button", { name: "Reload" })).toBeInTheDocument());
  });

  it("checks the accounts before sending", async () => {
    handler = () => ({ status: 200, body: placeholder });
    renderAt(`/assets/leases/posting-profile?legalEntityId=${LE}`);
    fireEvent.click(screen.getByRole("button", { name: "Show accounts" }));
    const form = await screen.findByRole("form", { name: "Lease posting accounts" });
    fireEvent.change(within(form).getByLabelText("Payment clearing"), { target: { value: "bad account" } });
    fireEvent.click(screen.getByRole("button", { name: "Save accounts" }));
    expect(await screen.findByText("Enter an account number of at most 40 characters.")).toBeInTheDocument();
    expect(posts()).toHaveLength(0);
  });

  it("shows the load failure with a retry", async () => {
    handler = () => ({ status: 503, body: { code: "AST-0602", detail: "the general ledger is not configured for this service" } });
    renderAt(`/assets/leases/posting-profile?legalEntityId=${LE}`);
    fireEvent.click(screen.getByRole("button", { name: "Show accounts" }));
    expect(await findAlert()).toHaveTextContent("Could not load the lease accounts");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});
