import { findAlert } from "../../test-utils";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { validateLease } from "./LeasesPage";
import { validateProfile } from "./LeasePostingPage";
import assets from "./module";

const LE = "11111111-0000-4000-8000-000000000001";
const LEASE = "44444444-0000-4000-8000-000000000004";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

type Reply = { status: number; body: unknown; etag?: string };
let handler: (url: URL, init: RequestInit) => Reply;
let calls: { method: string; url: URL; headers: Headers; body: unknown }[];

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, headers: new Headers(init.headers), body: init.body ? JSON.parse(init.body as string) : undefined });
      const { status, body, etag } = handler(url, init);
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

const schedule = [1, 2, 3, 4].map((n) => ({ periodNo: n, opening: "0", remeasurement: "0", interest: "0", payment: "750", closing: "0", rouDepreciation: "0" }));
const exempt = {
  id: LEASE, legalEntityId: LE, leaseNumber: "L-ST-1", commencementDate: "2026-01-01", termMonths: 12, paymentsPerYear: 4, paymentAmount: "750",
  paymentTiming: "advance", ibrRate: "0.05", rateMethod: "Nominal", election: "ShortTerm", currency: "USD", liability: "0", rouAsset: "0", status: "Active", version: 1,
  createdAt: "2026-01-01T00:00:00Z", schedule,
};
const base = { legalEntityId: LE, leaseNumber: "L-1", commencementDate: "2026-01-01", termMonths: "12", paymentsPerYear: "4", paymentAmount: "750", paymentTiming: "advance" as const, ibrRate: "0.05", currency: "USD" };

describe("lease elections", () => {
  it("validates short-term and low-value elections like the service", () => {
    expect(validateLease({ ...base, election: "ShortTerm" }).input).toMatchObject({ election: "ShortTerm", termMonths: 12 });
    expect(validateLease({ ...base, election: "ShortTerm" }).input).not.toHaveProperty("underlyingAssetValue");
    expect(validateLease({ ...base, termMonths: "24", election: "ShortTerm" }).errors.election).toBeDefined();
    expect(validateLease({ ...base, election: "ShortTerm", purchaseOption: true }).errors.election).toMatch(/purchase option/);
    expect(validateLease({ ...base, election: "LowValue", underlyingAssetValue: "4200.00" }).input).toMatchObject({ election: "LowValue", underlyingAssetValue: "4200.00", underlyingAssetCurrency: "USD" });
    expect(validateLease({ ...base, currency: "GBP", election: "LowValue", underlyingAssetValue: "5000", underlyingAssetCurrency: "EUR" }).input).toMatchObject({ currency: "GBP", underlyingAssetCurrency: "EUR" });
    expect(validateLease({ ...base, election: "LowValue", underlyingAssetValue: "5000.01", underlyingAssetCurrency: "EUR" }).errors.underlyingAssetValue).toMatch(/EUR 5000/);
    expect(validateLease({ ...base, election: "LowValue", underlyingAssetValue: "" }).errors.underlyingAssetValue).toBeDefined();
    expect(validateLease({ ...base, election: "LowValue", underlyingAssetValue: "5000.01" }).errors.underlyingAssetValue).toBeDefined();
    expect(validateLease({ ...base, election: "LowValue", underlyingAssetValue: "0" }).errors.underlyingAssetValue).toBeDefined();
    expect(validateLease({ ...base, election: "None" }).input).not.toHaveProperty("election");
    expect(validateLease({ ...base, election: "None", purchaseOption: true }).input).toMatchObject({ purchaseOption: true });
  });

  it("recognizes a short-term lease from the form and sends the election", async () => {
    handler = (url, init) =>
      url.pathname.endsWith("/gl-postings")
        ? { status: 200, body: { leaseId: LEASE, recognitionJournalId: null, terminationJournalId: null, periods: [] } }
        : init.method === "POST"
          ? { status: 201, body: exempt }
          : { status: 200, body: exempt };
    const { container } = renderAt("/assets/leases/new");
    expect(pageViolations(container)).toEqual([]);
    expect(screen.queryByLabelText("Underlying asset value when new")).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Lease number"), { target: { value: "L-ST-1" } });
    fireEvent.change(screen.getByLabelText("Term (months)"), { target: { value: "12" } });
    fireEvent.change(screen.getByLabelText("Payment amount"), { target: { value: "750" } });
    fireEvent.change(screen.getByLabelText("Recognition exemption"), { target: { value: "ShortTerm" } });
    fireEvent.click(screen.getByRole("button", { name: "Recognize lease" }));
    expect(await screen.findByRole("heading", { name: "Lease L-ST-1" })).toBeInTheDocument();
    expect(posts()[0].body).toMatchObject({ election: "ShortTerm", termMonths: 12, paymentAmount: "750" });
  });

  it("asks for the underlying value of a low-value lease and shows the service's refusal", async () => {
    handler = () => ({ status: 422, body: { code: "AST-0037", detail: "the underlying asset when new is worth more than 5000; it is not a low-value lease" } });
    renderAt("/assets/leases/new");
    fireEvent.change(screen.getByLabelText("Recognition exemption"), { target: { value: "LowValue" } });
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Lease number"), { target: { value: "L-LV-1" } });
    fireEvent.change(screen.getByLabelText("Payment amount"), { target: { value: "100" } });
    fireEvent.click(screen.getByRole("button", { name: "Recognize lease" }));
    expect(await screen.findByText("Enter the value of the underlying asset when new, such as 4200.00.")).toBeInTheDocument();
    expect(posts()).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Underlying asset value when new"), { target: { value: "4999" } });
    fireEvent.change(screen.getByLabelText("Value stated in"), { target: { value: "EUR" } });
    fireEvent.click(screen.getByRole("button", { name: "Recognize lease" }));
    expect(await findAlert()).toHaveTextContent("AST-0037");
    expect(posts()[0].body).toMatchObject({ election: "LowValue", underlyingAssetValue: "4999", underlyingAssetCurrency: "EUR" });
  });

  it("explains a lease under an election and offers neither modify nor terminate", async () => {
    const periods = ["2026-03-31", "2026-06-30", "2026-09-30", "2026-12-31"].map((periodEnd, i) => ({ periodNo: i + 1, periodEnd, postedJournalId: null }));
    handler = (url) =>
      url.pathname.endsWith("/gl-postings")
        ? { status: 200, body: { leaseId: LEASE, recognitionJournalId: null, terminationJournalId: null, periods } }
        : { status: 200, body: exempt, etag: '"1"' };
    const { container } = renderAt(`/assets/leases/${LEASE}`);
    expect(await screen.findByText("Not recognized on the balance sheet")).toBeInTheDocument();
    expect(screen.getByText("Recognition exemption", { selector: "dt" }).nextElementSibling).toHaveTextContent("Short-term lease");
    expect(screen.queryByRole("link", { name: "Modify lease" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Terminate lease" })).not.toBeInTheDocument();
    // No recognition journal is ever booked for it, so the ledger card lists only the four payment periods.
    expect(await screen.findByText("0 of 4 journals are in the ledger. A period that is booked cannot be remeasured.")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("refuses to terminate a lease under an election when opened by its address", async () => {
    handler = () => ({ status: 200, body: exempt, etag: '"1"' });
    renderAt(`/assets/leases/${LEASE}/terminate`);
    expect(await screen.findByText("This lease cannot be terminated")).toBeInTheDocument();
    expect(screen.getByText(/no liability or right-of-use asset to derecognize/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Terminate lease" })).not.toBeInTheDocument();
  });

  it("refuses a short-term lease with a purchase option before sending it", async () => {
    handler = () => ({ status: 201, body: exempt });
    renderAt("/assets/leases/new");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Lease number"), { target: { value: "L-ST-2" } });
    fireEvent.change(screen.getByLabelText("Term (months)"), { target: { value: "12" } });
    fireEvent.change(screen.getByLabelText("Payment amount"), { target: { value: "750" } });
    fireEvent.change(screen.getByLabelText("Recognition exemption"), { target: { value: "ShortTerm" } });
    fireEvent.click(screen.getByLabelText(/contains a purchase option/));
    fireEvent.click(screen.getByRole("button", { name: "Recognize lease" }));
    expect(await screen.findByText("A lease that contains a purchase option is not a short-term lease.")).toBeInTheDocument();
    expect(posts()).toHaveLength(0);
  });

  it("lists the exemption of each lease", async () => {
    handler = () => ({ status: 200, body: { items: [{ ...exempt, schedule: undefined }, { ...exempt, id: "x", leaseNumber: "L-2", election: "None" }] } });
    renderAt("/assets/leases");
    const table = await screen.findByRole("table", { name: "Leases" });
    expect(within(table).getByText("Short-term")).toBeInTheDocument();
  });
});

describe("lease posting profile accounts", () => {
  const accounts = ["rouAssetAccount", "rouAccumulatedDepreciationAccount", "liabilityAccount", "interestExpenseAccount", "depreciationExpenseAccount", "paymentClearingAccount", "remeasurementGainAccount", "terminationGainLossAccount", "exemptLeaseExpenseAccount"];
  const profile = Object.fromEntries(accounts.map((k, i) => [k, String(1700 + i)]));

  it("checks the termination and exempt-lease accounts like the service", () => {
    expect(validateProfile(profile as never)).toEqual({});
    expect(Object.keys(validateProfile({ ...profile, terminationGainLossAccount: "a b", exemptLeaseExpenseAccount: "" } as never)).sort()).toEqual(["exemptLeaseExpenseAccount", "terminationGainLossAccount"]);
  });

  it("shows both accounts and saves them", async () => {
    handler = (_url, init) =>
      init.method === "PUT"
        ? { status: 200, body: { legalEntityId: LE, ...profile, version: 2 }, etag: '"2"' }
        : { status: 200, body: { legalEntityId: LE, ...profile, version: 1 }, etag: '"1"' };
    renderAt(`/assets/leases/posting-profile?legalEntityId=${LE}`);
    fireEvent.click(screen.getByRole("button", { name: "Show accounts" }));
    const termination = await screen.findByLabelText("Termination gain / loss");
    expect(termination).toHaveValue("1707");
    fireEvent.change(screen.getByLabelText("Short-term and low-value lease expense"), { target: { value: "6860" } });
    fireEvent.click(screen.getByRole("button", { name: "Replace accounts" }));
    expect(await screen.findByText(/Journals booked from now on use these accounts/)).toBeInTheDocument();
    expect(posts()[0].body).toMatchObject({ terminationGainLossAccount: "1707", exemptLeaseExpenseAccount: "6860" });
    expect(posts()[0].headers.get("If-Match")).toBe('"1"');
  });
});
