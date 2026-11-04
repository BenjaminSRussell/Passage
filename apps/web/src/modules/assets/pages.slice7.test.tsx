import { findAlert, findStatus } from "../../test-utils";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { periodLabel } from "./LeasePostingPage";
import { leasePeriodEnd, validateTermination } from "./TerminateLeasePage";
import assets from "./module";

const LE = "11111111-0000-4000-8000-000000000001";
const LEASE = "44444444-0000-4000-8000-000000000004";
const TERM = "bbbbbbbb-0000-4000-8000-00000000000b";

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

const schedule = [1, 2, 3, 4, 5].map((n) => ({ periodNo: n, opening: "1", remeasurement: "0", interest: "1", payment: "1", closing: "1", rouDepreciation: "1" }));
const lease = {
  id: LEASE, legalEntityId: LE, leaseNumber: "L-2026-01", commencementDate: "2026-01-01", termMonths: 60, paymentsPerYear: 1, paymentAmount: "10000",
  paymentTiming: "arrears", ibrRate: "0.05", rateMethod: "Nominal", currency: "USD", liability: "43294.77", rouAsset: "43294.77", status: "Active", version: 1,
  createdAt: "2026-01-01T00:00:00Z", schedule,
};
const termination = {
  id: TERM, leaseId: LEASE, afterPeriod: 2, terminationDate: "2027-12-31", reason: "site closed", currency: "USD", liabilityDerecognized: "27232.49",
  rouGross: "43294.77", rouAccumulated: "17317.90", rouCarrying: "25976.87", terminationPayment: "1000.00", gainLoss: "255.62", journalId: null,
  createdBy: "u", createdAt: "2027-12-31T00:00:00Z",
};
const terminated = { ...lease, status: "Terminated", version: 2, schedule: schedule.slice(0, 2), termination };

describe("lease termination", () => {
  it("registers the terminate route", () => {
    expect(assets.routes.map((r) => r.path)).toContain("/assets/leases/:id/terminate");
  });

  it("validates like the service", () => {
    const base = { afterPeriod: "2", terminationPayment: "", reason: "" };
    expect(validateTermination(base, 5).input).toEqual({ afterPeriod: 2 });
    expect(validateTermination({ ...base, terminationPayment: "1000.50", reason: " site closed " }, 5).input).toEqual({ afterPeriod: 2, terminationPayment: "1000.50", reason: "site closed" });
    expect(validateTermination({ ...base, afterPeriod: "0" }, 5).errors.afterPeriod).toBeDefined();
    expect(validateTermination({ ...base, afterPeriod: "5" }, 5).errors.afterPeriod).toBeDefined();
    expect(validateTermination({ ...base, terminationPayment: "-1" }, 5).errors.terminationPayment).toBeDefined();
    expect(validateTermination({ ...base, reason: "x".repeat(501) }, 5).errors.reason).toBeDefined();
  });

  it("dates the period end like the service and labels the termination journal", () => {
    expect(leasePeriodEnd("2026-01-01", 1, 2)).toBe("2027-12-31");
    expect(leasePeriodEnd("2026-01-31", 4, 1)).toBe("2026-04-29");
    expect(leasePeriodEnd("2026-01-31", 12, 1)).toBe("2026-02-27");
    expect(leasePeriodEnd("2026-03-15", 12, 10)).toBe("2027-01-14");
    expect(periodLabel(-1)).toBe("Termination");
    expect(periodLabel(0)).toBe("Recognition");
    expect(periodLabel(3)).toBe("Period 3");
  });

  it("offers Terminate on an active lease and shows the termination on a terminated one", async () => {
    handler = (url) =>
      url.pathname.endsWith("/gl-postings")
        ? { status: 200, body: { leaseId: LEASE, recognitionJournalId: null, terminationJournalId: null, periods: [] } }
        : { status: 200, body: lease, etag: '"1"' };
    const first = renderAt(`/assets/leases/${LEASE}`);
    expect(await screen.findByRole("link", { name: "Terminate lease" })).toHaveAttribute("href", `/assets/leases/${LEASE}/terminate`);
    first.unmount();
    handler = (url) =>
      url.pathname.endsWith("/gl-postings")
        ? { status: 200, body: { leaseId: LEASE, recognitionJournalId: null, terminationJournalId: null, periods: [] } }
        : { status: 200, body: terminated, etag: '"2"' };
    const { container } = renderAt(`/assets/leases/${LEASE}`);
    const gain = await screen.findByText("Gain", { selector: "dt" });
    expect(gain.nextElementSibling).toHaveTextContent("255.62 USD");
    expect(screen.getByText("Reason", { selector: "dt" }).nextElementSibling).toHaveTextContent("site closed");
    expect(await screen.findByText("Termination", { selector: "td" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Terminate lease" })).not.toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("posts the termination with If-Match and an Idempotency-Key, then shows AST-0036", async () => {
    handler = (_url, init) =>
      init.method === "POST"
        ? { status: 201, body: { termination, lease: terminated, messages: ["AST-0036: lease terminated after period 2"] }, etag: '"2"' }
        : { status: 200, body: lease, etag: '"1"' };
    const { container } = renderAt(`/assets/leases/${LEASE}/terminate`);
    await screen.findByRole("form", { name: "Terminate lease" });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Terminate lease" }));
    expect(await screen.findByText(/Enter the last period the lease stays in force, 1 to 4/)).toBeInTheDocument();
    expect(posts()).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Last period in force"), { target: { value: "2" } });
    fireEvent.change(screen.getByLabelText("Termination payment"), { target: { value: "1000.00" } });
    fireEvent.change(screen.getByLabelText("Reason"), { target: { value: "site closed" } });
    fireEvent.click(screen.getByRole("button", { name: "Terminate lease" }));
    // Nothing is sent until the irreversible termination is confirmed; cancelling keeps the lease.
    let dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent("The lease ends on 2027-12-31, after period 2.");
    fireEvent.click(within(dialog).getByRole("button", { name: "Keep the lease" }));
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    expect(posts()).toHaveLength(0);
    fireEvent.click(screen.getByRole("button", { name: "Terminate lease" }));
    dialog = await screen.findByRole("alertdialog");
    fireEvent.click(within(dialog).getByRole("button", { name: "Confirm termination" }));
    expect(await findStatus()).toHaveTextContent("AST-0036: lease terminated after period 2");
    const post = posts()[0];
    expect(post.url.pathname).toBe(`/api/v1/leases/${LEASE}/terminate`);
    expect(post.headers.get("If-Match")).toBe('"1"');
    expect(post.headers.get("Idempotency-Key")).toBeTruthy();
    expect(post.body).toEqual({ afterPeriod: 2, terminationPayment: "1000.00", reason: "site closed" });
    expect(screen.getByRole("list", { name: "Termination" })).toHaveTextContent("Gain 255.62 USD");
  });

  it("explains a refused termination and a stale version", async () => {
    handler = (_url, init) =>
      init.method === "POST" ? { status: 422, body: { code: "AST-0027", detail: "a period after 2 is already posted to the ledger" } } : { status: 200, body: lease, etag: '"1"' };
    renderAt(`/assets/leases/${LEASE}/terminate`);
    await screen.findByRole("form", { name: "Terminate lease" });
    fireEvent.change(screen.getByLabelText("Last period in force"), { target: { value: "2" } });
    const terminate = async () => {
      fireEvent.click(screen.getByRole("button", { name: "Terminate lease" }));
      fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Confirm termination" }));
    };
    await terminate();
    expect(await findAlert()).toHaveTextContent("AST-0027");
    handler = (_url, init) => (init.method === "POST" ? { status: 412, body: { code: "AST-0006", detail: "the lease changed since you read it" } } : { status: 200, body: lease, etag: '"1"' });
    await terminate();
    const alert = await screen.findByText("The lease changed since you opened it");
    expect(within(alert.closest("[role=alert]") as HTMLElement).getByRole("button", { name: "Reload" })).toBeInTheDocument();
  });

  it("refuses a terminated lease and shows a load error", async () => {
    handler = () => ({ status: 200, body: terminated, etag: '"2"' });
    const first = renderAt(`/assets/leases/${LEASE}/terminate`);
    expect(await screen.findByText("This lease cannot be terminated")).toBeInTheDocument();
    expect(screen.queryByRole("form", { name: "Terminate lease" })).not.toBeInTheDocument();
    first.unmount();
    handler = () => ({ status: 404, body: { code: "AST-0005", detail: "no lease" } });
    renderAt(`/assets/leases/${LEASE}/terminate`);
    expect(await findAlert()).toHaveTextContent("AST-0005");
  });
});
