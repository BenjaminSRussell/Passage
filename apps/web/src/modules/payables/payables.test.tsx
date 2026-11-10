import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { clearResolvedOptions, setLookupFetcher } from "../../shell/lookup/entities";
import { WorkContextProvider } from "../../shell/lookup/workContext";
import { PrincipalProvider } from "../../shell/principal";
import { json, pageViolations, server, useFakeServer } from "../../shell/testing/fakeServer";
import type { OpenItem, Proposal } from "./api/client";
import { formatAmount, today, utcToday } from "./format";
import payables from "./module";
import { validateDraft } from "./NewProposalPage";

useFakeServer();

const LE = "0190a000-0000-7000-8000-0000000000aa";
const V1 = "11111111-1111-4111-8111-111111111111";
const V2 = "22222222-2222-4222-8222-222222222222";
const ITEM = "33333333-3333-4333-8333-333333333333";
const PROPOSAL = "44444444-4444-4444-8444-444444444444";

const READ = ["open-item.read", "payment-proposal.read"];

function Where() {
  return <output data-testid="location">{useLocation().pathname}</output>;
}

function renderAt(path: string, duties: string[] = READ) {
  window.localStorage.setItem("passage.workContext.t.u", JSON.stringify({ legalEntityId: LE, siteId: "" }));
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <PrincipalProvider duties={duties}>
        <WorkContextProvider identity="t.u">
          <MemoryRouter initialEntries={[path]}>
            <Routes>
              {payables.routes.map((r) => (
                <Route key={r.path} path={r.path} element={r.element} />
              ))}
            </Routes>
            <Where />
          </MemoryRouter>
        </WorkContextProvider>
      </PrincipalProvider>
    </QueryClientProvider>,
  );
}

function item(patch: Partial<OpenItem> = {}): OpenItem {
  return {
    id: ITEM,
    legalEntityId: LE,
    vendorId: V1,
    invoiceNumber: "INV-1001",
    invoiceDate: "2026-09-01",
    dueDate: "2999-01-01",
    currency: "USD",
    amount: "1250.50",
    openAmount: "1250.50",
    status: "OPEN",
    paymentHold: false,
    ...patch,
  };
}

function proposal(patch: Partial<Proposal> = {}): Proposal {
  return {
    id: PROPOSAL,
    legalEntityId: LE,
    runDate: "2026-11-20",
    runId: "run-1",
    dueBy: "2026-11-30",
    legalEntityCurrency: "USD",
    paymentMethod: "TRANSFER",
    status: "DRAFT",
    createdBy: "ana",
    createdAt: "2026-11-10T10:00:00Z",
    totalValued: "1520.35",
    totalWithheld: { USD: "12.00" },
    groups: [
      {
        id: "g1",
        vendorId: V1,
        currency: "USD",
        paymentMethod: "TRANSFER",
        gross: "1000.10",
        discount: "20.00",
        withheld: "12.00",
        net: "968.10",
        valued: "968.10",
        lines: [
          { openItemId: ITEM, openAmount: "600.10", discount: "20.00", pay: "580.10", withheld: "12.00", valued: "568.10" },
          { openItemId: "55555555-5555-4555-8555-555555555555", openAmount: "400.00", discount: "0", pay: "400.00", valued: "400.00" },
        ],
      },
      {
        id: "g2",
        vendorId: V2,
        currency: "EUR",
        paymentMethod: "WIRE",
        gross: "500.00",
        discount: "0",
        net: "500.00",
        valued: "552.25",
        lines: [{ openItemId: "66666666-6666-4666-8666-666666666666", openAmount: "500.00", discount: "0", pay: "500.00", valued: "552.25" }],
      },
    ],
    ...patch,
  };
}

function api(overrides: (url: URL, init: RequestInit) => ReturnType<typeof json> | undefined = () => undefined) {
  server.handler = (url, init) => {
    const own = overrides(url, init);
    if (own) return own;
    if (url.pathname === `/api/v1/party/vendors/${V1}`) return json(200, { partyId: "p1", accountNumber: "V-1" });
    if (url.pathname === `/api/v1/party/vendors/${V2}`) return json(200, { partyId: "p2", accountNumber: "V-2" });
    if (url.pathname === "/api/v1/party/parties/p1") return json(200, { name: "Contoso Ltd" });
    if (url.pathname === "/api/v1/party/parties/p2") return json(200, { name: "Fabrikam GmbH" });
    if (url.pathname === "/api/v1/org/currencies") return json(200, { items: [{ code: "USD", name: "US dollar" }] });
    return undefined;
  };
}

const calls = (method: string, path: string) => server.calls.filter((c) => c.method === method && c.url.pathname === path);

beforeEach(() => {
  setLookupFetcher(null);
  clearResolvedOptions();
  window.localStorage.clear();
});

describe("vendor open items", () => {
  it("shows the vendor by name with document, due date, overdue marker and exact amount", async () => {
    api((url) => (url.pathname === "/api/v1/payables/open-items" ? json(200, { items: [item({ dueDate: "2020-01-01", openAmount: "1250.5" }), item({ id: "i2", invoiceNumber: "INV-1002" })] }) : undefined));
    const { container } = renderAt("/payables/open-items");
    expect(await screen.findByText("INV-1001")).toBeInTheDocument();
    expect(await screen.findAllByText("Contoso Ltd (V-1)")).toHaveLength(2);
    expect(screen.getAllByText("1,250.50 USD", { selector: "td" })).toHaveLength(2);
    expect(screen.getAllByText("Overdue")).toHaveLength(1);
    const list = calls("GET", "/api/v1/payables/open-items")[0].url.searchParams;
    expect(list.get("legal_entity")).toBe(LE);
    expect(list.get("status")).toBe("OPEN");
    expect(pageViolations(container)).toEqual([]);
  });

  it("filters by due-by date and pages with the cursor", async () => {
    const user = userEvent.setup();
    api((url) => {
      if (url.pathname !== "/api/v1/payables/open-items") return undefined;
      return url.searchParams.get("cursor") === "c1" ? json(200, { items: [item({ id: "i2", invoiceNumber: "INV-2000" })] }) : json(200, { items: [item()], nextCursor: "c1" });
    });
    renderAt("/payables/open-items");
    await screen.findByText("INV-1001");
    await user.click(screen.getByRole("button", { name: "Load more open items" }));
    expect(await screen.findByText("INV-2000")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Load more open items" })).not.toBeInTheDocument();
    await user.type(screen.getByLabelText(/Due by/), "2026-12-31");
    await waitFor(() => expect(server.calls.some((c) => c.url.searchParams.get("due_to") === "2026-12-31")).toBe(true));
  });

  it("explains an empty list and a failed load in plain words", async () => {
    const user = userEvent.setup();
    let fail = true;
    api((url) => (url.pathname === "/api/v1/payables/open-items" ? (fail ? json(503, { code: "COM-0503", detail: "down" }) : json(200, { items: [] })) : undefined));
    renderAt("/payables/open-items");
    expect(await screen.findByText(/This service is unavailable right now/)).toBeInTheDocument();
    fail = false;
    await user.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("Nothing owed to vendors here")).toBeInTheDocument();
  });

  it("shows the hold reason and only offers hold and release to users with the duty", async () => {
    api((url) => (url.pathname === "/api/v1/payables/open-items" ? json(200, { items: [item({ paymentHold: true, paymentHoldReason: "bank query" })] }) : undefined));
    const view = renderAt("/payables/open-items");
    expect(await screen.findByText("On hold: bank query")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Release hold/ })).not.toBeInTheDocument();
    view.unmount();
    renderAt("/payables/open-items", [...READ, "payment-hold.manage"]);
    expect(await screen.findByRole("button", { name: "Release hold on INV-1001" })).toBeInTheDocument();
  });

  it("puts an item on hold with a reason and releases it again", async () => {
    const user = userEvent.setup();
    api((url) => {
      if (url.pathname === "/api/v1/payables/open-items") return json(200, { items: [item()] });
      if (url.pathname.endsWith(":hold") || url.pathname.endsWith(":release")) return json(200, item());
      return undefined;
    });
    renderAt("/payables/open-items", [...READ, "payment-hold.manage"]);
    await user.click(await screen.findByRole("button", { name: "Hold payment of INV-1001" }));
    await user.type(screen.getByLabelText(/Reason for the hold/), "bank query");
    await user.click(screen.getByRole("button", { name: "Put on hold" }));
    expect(await screen.findByText(/INV-1001 is on hold/)).toBeInTheDocument();
    expect(calls("POST", `/api/v1/payables/open-items/${ITEM}:hold`)[0].body).toEqual({ reason: "bank query" });
    await user.click(await screen.findByRole("button", { name: "Hold payment of INV-1001" }));
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("form", { name: /Hold payment/ })).not.toBeInTheDocument();
  });

  it("releases a held item and shows a refused change", async () => {
    const user = userEvent.setup();
    api((url) => {
      if (url.pathname === "/api/v1/payables/open-items") return json(200, { items: [item({ paymentHold: true })] });
      if (url.pathname.endsWith(":release")) return json(409, { code: "AP-OPN-001", detail: "closed" });
      return undefined;
    });
    renderAt("/payables/open-items", [...READ, "payment-hold.manage"]);
    await user.click(await screen.findByRole("button", { name: "Release hold on INV-1001" }));
    expect(await screen.findByText(/A settled or reversed open item can't be held or released/)).toBeInTheDocument();
  });
});

describe("payment proposals", () => {
  it("lists proposals with totals in the company currency and opens one", async () => {
    const user = userEvent.setup();
    api((url) => {
      if (url.pathname === "/api/v1/payables/payment-proposals") return json(200, { items: [{ ...proposal(), groups: undefined }] });
      if (url.pathname === `/api/v1/payables/payment-proposals/${PROPOSAL}`) return json(200, proposal());
      return undefined;
    });
    renderAt("/payables", [...READ, "payment-proposal.write"]);
    expect(await screen.findByText("1,520.35 USD")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "New payment proposal" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "2026-11-20" }));
    expect(await screen.findByText("Payment proposal for 2026-11-20")).toBeInTheDocument();
    expect(screen.getByTestId("location")).toHaveTextContent(`/payables/proposals/${PROPOSAL}`);
  });

  it("hides the create link without the duty and explains an empty list", async () => {
    api((url) => (url.pathname === "/api/v1/payables/payment-proposals" ? json(200, { items: [] }) : undefined));
    renderAt("/payables");
    expect(await screen.findByText("No payment proposals yet")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "New payment proposal" })).not.toBeInTheDocument();
  });

  it("checks the form before sending", () => {
    const draft = { legalEntityId: "", currency: "", runDate: "2020-01-01", dueBy: "2019-01-01", method: "TRANSFER" as const, exceptions: [] };
    expect(validateDraft(draft, "2026-11-10")).toEqual({
      currency: "Choose the currency your company reports in.",
      runDate: "The payment date can't be in the past.",
      dueBy: "This can't be earlier than the payment date.",
    });
    expect(validateDraft({ ...draft, currency: "USD", runDate: "2026-11-10", dueBy: "" }, "2026-11-10")).toEqual({});
  });

  it("creates a proposal from the lookups, keeps one run id for a retry and a new one after an edit", async () => {
    const user = userEvent.setup();
    let refuse = true;
    api((url, init) => {
      if (url.pathname === "/api/v1/payables/payment-proposals" && init.method === "POST") return refuse ? json(422, { code: "AP-PRP-005", detail: "nothing due" }) : json(201, proposal());
      if (url.pathname === `/api/v1/payables/payment-proposals/${PROPOSAL}`) return json(200, proposal());
      return undefined;
    });
    renderAt("/payables/proposals/new", [...READ, "payment-proposal.write"]);
    await user.click(screen.getByLabelText("Company currency"));
    await user.click(await screen.findByRole("option", { name: /US dollar/ }));
    await user.click(screen.getByRole("button", { name: "Create proposal" }));
    expect(await screen.findByText(/Nothing is due for payment on the run date/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Create proposal" }));
    await waitFor(() => expect(calls("POST", "/api/v1/payables/payment-proposals")).toHaveLength(2));
    const [first, second] = calls("POST", "/api/v1/payables/payment-proposals");
    expect(first.body).toMatchObject({ legalEntityId: LE, legalEntityCurrency: "USD", runDate: utcToday(), paymentMethod: "TRANSFER" });
    expect((second.body as { runId: string }).runId).toBe((first.body as { runId: string }).runId);

    await user.selectOptions(screen.getByLabelText("Pay by"), "WIRE");
    refuse = false;
    await user.click(screen.getByRole("button", { name: "Create proposal" }));
    await waitFor(() => expect(screen.getByTestId("location")).toHaveTextContent(`/payables/proposals/${PROPOSAL}`));
    const third = calls("POST", "/api/v1/payables/payment-proposals")[2].body as { runId: string; paymentMethod: string };
    expect(third.paymentMethod).toBe("WIRE");
    expect(third.runId).not.toBe((first.body as { runId: string }).runId);
  });

  it("sends a different payment method for a chosen vendor", async () => {
    const user = userEvent.setup();
    api((url, init) => {
      if (url.pathname === "/api/v1/party/vendors:search") return json(200, { items: [{ id: V2, name: "Fabrikam GmbH", accountNumber: "V-2", legalEntityId: LE }] });
      if (url.pathname === "/api/v1/payables/payment-proposals" && init.method === "POST") return json(201, proposal());
      return undefined;
    });
    renderAt("/payables/proposals/new", [...READ, "payment-proposal.write"]);
    await user.click(screen.getByLabelText("Company currency"));
    await user.click(await screen.findByRole("option", { name: /US dollar/ }));
    await user.click(screen.getByRole("button", { name: "Pay a vendor a different way" }));
    await user.click(screen.getByLabelText("Vendor 1"));
    await user.click(await screen.findByRole("option", { name: /Fabrikam GmbH/ }));
    await user.selectOptions(screen.getByLabelText("Pay vendor 1 by"), "CHECK");
    await user.click(screen.getByRole("button", { name: "Create proposal" }));
    await waitFor(() => expect(calls("POST", "/api/v1/payables/payment-proposals")).toHaveLength(1));
    expect(calls("POST", "/api/v1/payables/payment-proposals")[0].body).toMatchObject({ vendorPaymentMethods: { [V2]: "CHECK" } });
  });
});

describe("payment proposal detail", () => {
  const detail = (p: Proposal) => (url: URL) => (url.pathname === `/api/v1/payables/payment-proposals/${PROPOSAL}` ? json(200, p) : undefined);

  it("shows groups and the totals the service reports without adding amounts itself", async () => {
    const user = userEvent.setup();
    api((url) => detail(proposal())(url) ?? (url.pathname === `/api/v1/payables/open-items/${ITEM}` ? json(200, item()) : undefined));
    const { container } = renderAt(`/payables/proposals/${PROPOSAL}`);
    expect(await screen.findByText("Contoso Ltd (V-1)")).toBeInTheDocument();
    const totals = screen.getByRole("list", { name: "Proposal totals" });
    expect(within(totals).getByText("1,520.35 USD")).toBeInTheDocument();
    expect(within(totals).getByText("12.00 USD")).toBeInTheDocument();
    expect(screen.getByText("552.25 USD")).toBeInTheDocument();
    expect(screen.getAllByText("500.00 EUR", { selector: "td" }).length).toBeGreaterThan(0);
    await user.click(screen.getByRole("button", { name: "Show 2 invoices" }));
    expect(await screen.findByText("INV-1001")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("asks for confirmation with the count and total, then approves", async () => {
    const user = userEvent.setup();
    api((url, init) => {
      if (url.pathname.endsWith(":approve")) return json(200, proposal({ status: "APPROVED", approvedBy: "bo" }));
      return init.method === "GET" || init.method === undefined ? detail(proposal())(url) : undefined;
    });
    renderAt(`/payables/proposals/${PROPOSAL}`, [...READ, "payment-proposal.approve"]);
    await user.click(await screen.findByRole("button", { name: "Approve proposal" }));
    const dialog = screen.getByRole("alertdialog");
    expect(within(dialog).getByText("2 payments for 3 invoices, 1,520.35 USD in total. You can't change it after approval.")).toBeInTheDocument();
    expect(calls("POST", `/api/v1/payables/payment-proposals/${PROPOSAL}:approve`)).toHaveLength(0);
    await user.click(within(dialog).getByRole("button", { name: "Approve proposal" }));
    expect(await screen.findByText(/The proposal is approved/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve proposal" })).not.toBeInTheDocument();
  });

  it("tells the creator to ask another approver when separation of duties refuses", async () => {
    const user = userEvent.setup();
    api((url, init) => (url.pathname.endsWith(":approve") ? json(403, { code: "AP-PRP-003", detail: "same user" }) : init.method === "POST" ? undefined : detail(proposal())(url)));
    renderAt(`/payables/proposals/${PROPOSAL}`, [...READ, "payment-proposal.approve"]);
    await user.click(await screen.findByRole("button", { name: "Approve proposal" }));
    await user.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Approve proposal" }));
    expect(await screen.findByText(/must be approved by someone other than its creator\. Ask another approver to approve it/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Approve proposal" })).toBeInTheDocument();
  });

  it("offers no approve or cancel without the duty, or once the proposal is not a draft", async () => {
    api(detail(proposal()));
    const view = renderAt(`/payables/proposals/${PROPOSAL}`, READ);
    await screen.findByText("Contoso Ltd (V-1)");
    expect(screen.queryByRole("button", { name: "Approve proposal" })).not.toBeInTheDocument();
    expect(screen.getByText(/You don't have approval rights/)).toBeInTheDocument();
    view.unmount();
    api(detail(proposal({ status: "APPROVED" })));
    renderAt(`/payables/proposals/${PROPOSAL}`, [...READ, "payment-proposal.approve", "payment-proposal.write"]);
    await screen.findByText("Contoso Ltd (V-1)");
    expect(screen.queryByRole("button", { name: "Approve proposal" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Cancel proposal" })).not.toBeInTheDocument();
  });

  it("cancels a draft after confirmation", async () => {
    const user = userEvent.setup();
    api((url, init) => (url.pathname.endsWith(":cancel") ? json(200, proposal({ status: "CANCELLED" })) : init.method === "POST" ? undefined : detail(proposal())(url)));
    renderAt(`/payables/proposals/${PROPOSAL}`, [...READ, "payment-proposal.write"]);
    await user.click(await screen.findByRole("button", { name: "Cancel proposal" }));
    await user.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Cancel proposal" }));
    expect(await screen.findByText(/The proposal is cancelled/)).toBeInTheDocument();
  });

  it("says why an invoice was left out of the proposal", async () => {
    api((url) => detail(proposal({ excludedItems: [{ itemId: ITEM, vendorId: V1, reason: "PAYEE_UNVERIFIED" }] }))(url) ?? (url.pathname === `/api/v1/payables/open-items/${ITEM}` ? json(200, item()) : undefined));
    renderAt(`/payables/proposals/${PROPOSAL}`);
    expect(await screen.findByText(/The payee isn't verified yet/)).toBeInTheDocument();
    expect(await screen.findByText("Left out of this proposal")).toBeInTheDocument();
  });

  it("shows a load failure with a retry", async () => {
    api((url) => (url.pathname === `/api/v1/payables/payment-proposals/${PROPOSAL}` ? json(404, { code: "COM-0404", detail: "gone" }) : undefined));
    renderAt(`/payables/proposals/${PROPOSAL}`);
    expect(await screen.findByText(/We couldn't find that record/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

describe("dates and money", () => {
  const evening = () => Object.assign(new Date("2026-11-11T02:30:00Z"), { getTimezoneOffset: () => 300 });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllEnvs();
  });

  it("uses the UTC day for the payment date and the local day for the overdue marker", () => {
    expect(utcToday(evening())).toBe("2026-11-11");
    expect(today(evening())).toBe("2026-11-10");
  });

  it("defaults the payment date to the UTC day on a western-time-zone evening", () => {
    vi.stubEnv("TZ", "America/New_York");
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date("2026-11-11T02:30:00Z"));
    api();
    renderAt("/payables/proposals/new", [...READ, "payment-proposal.write"]);
    expect(screen.getByLabelText("Payment date")).toHaveValue("2026-11-11");
  });

  it("formats by the currency's minor units without rounding the digits", () => {
    expect(formatAmount("1000", "JPY")).toBe("1,000 JPY");
    expect(formatAmount("1000.5", "KWD")).toBe("1,000.500 KWD");
    expect(formatAmount("1000", "USD")).toBe("1,000.00 USD");
    expect(formatAmount("-0.1", "EUR")).toBe("-0.10 EUR");
    expect(formatAmount("12345678901234567.891", "USD")).toBe("12,345,678,901,234,567.891 USD");
    expect(formatAmount("5.20", "ZZZ")).toBe("5.20 ZZZ");
  });
});

describe("one request per action", () => {
  const lines = (n: number) => Array.from({ length: n }, (_, i) => ({ openItemId: `0000000${i}-0000-4000-8000-000000000000`, openAmount: "1.00", discount: "0", pay: "1.00", valued: "1.00" }));

  it("sends approve once when the confirm button is double clicked", async () => {
    const user = userEvent.setup();
    api((url, init) => (url.pathname.endsWith(":approve") ? json(200, proposal({ status: "APPROVED" })) : init.method === "POST" ? undefined : url.pathname === `/api/v1/payables/payment-proposals/${PROPOSAL}` ? json(200, proposal()) : undefined));
    renderAt(`/payables/proposals/${PROPOSAL}`, [...READ, "payment-proposal.approve"]);
    await user.click(await screen.findByRole("button", { name: "Approve proposal" }));
    await user.dblClick(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Approve proposal" }));
    expect(await screen.findByText(/The proposal is approved/)).toBeInTheDocument();
    expect(calls("POST", `/api/v1/payables/payment-proposals/${PROPOSAL}:approve`)).toHaveLength(1);
    expect(screen.queryByText(/Could not approve/)).not.toBeInTheDocument();
  });

  it("explains a refused approval plainly and reloads the proposal", async () => {
    const user = userEvent.setup();
    api((url, init) => (url.pathname.endsWith(":approve") ? json(409, { code: "AP-PRP-001", detail: "items changed" }) : init.method === "POST" ? undefined : url.pathname === `/api/v1/payables/payment-proposals/${PROPOSAL}` ? json(200, proposal()) : undefined));
    renderAt(`/payables/proposals/${PROPOSAL}`, [...READ, "payment-proposal.approve"]);
    await user.click(await screen.findByRole("button", { name: "Approve proposal" }));
    await user.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Approve proposal" }));
    expect(await screen.findByText(/The proposal changed: an invoice was paid/)).toBeInTheDocument();
    await waitFor(() => expect(calls("GET", `/api/v1/payables/payment-proposals/${PROPOSAL}`).length).toBeGreaterThan(1));
  });

  it("sends hold once and release once on a double click", async () => {
    const user = userEvent.setup();
    let held = false;
    api((url) => {
      if (url.pathname === "/api/v1/payables/open-items") return json(200, { items: [item({ paymentHold: held })] });
      if (url.pathname.endsWith(":hold")) {
        held = true;
        return json(200, item());
      }
      if (url.pathname.endsWith(":release")) return json(200, item());
      return undefined;
    });
    const stubbed = globalThis.fetch;
    vi.stubGlobal("fetch", async (input: string, init?: RequestInit) => {
      if (/:(hold|release)$/.test(String(input))) await new Promise((resolve) => setTimeout(resolve, 100));
      return stubbed(input, init);
    });
    renderAt("/payables/open-items", [...READ, "payment-hold.manage"]);
    await user.click(await screen.findByRole("button", { name: "Hold payment of INV-1001" }));
    await user.dblClick(screen.getByRole("button", { name: "Put on hold" }));
    await screen.findByText(/INV-1001 is on hold/);
    expect(calls("POST", `/api/v1/payables/open-items/${ITEM}:hold`)).toHaveLength(1);
    await user.dblClick(await screen.findByRole("button", { name: "Release hold on INV-1001" }));
    await screen.findByText(/INV-1001 is released/);
    expect(calls("POST", `/api/v1/payables/open-items/${ITEM}:release`)).toHaveLength(1);
  });

  it("looks up invoice numbers only for the visible lines and only with the open item duty", async () => {
    const user = userEvent.setup();
    const big = proposal({ groups: [{ ...proposal().groups[0], lines: lines(12) }] });
    api((url) => (url.pathname === `/api/v1/payables/payment-proposals/${PROPOSAL}` ? json(200, big) : url.pathname.startsWith("/api/v1/payables/open-items/") ? json(200, item({ invoiceNumber: "INV-X" })) : undefined));
    const view = renderAt(`/payables/proposals/${PROPOSAL}`, READ);
    await user.click(await screen.findByRole("button", { name: "Show 12 invoices" }));
    await screen.findAllByText("INV-X");
    expect(server.calls.filter((c) => c.url.pathname.startsWith("/api/v1/payables/open-items/"))).toHaveLength(10);
    await user.click(screen.getByRole("button", { name: /Show more invoices \(2 left\)/ }));
    await waitFor(() => expect(server.calls.filter((c) => c.url.pathname.startsWith("/api/v1/payables/open-items/"))).toHaveLength(12));
    view.unmount();
    server.calls = [];
    renderAt(`/payables/proposals/${PROPOSAL}`, ["payment-proposal.read"]);
    await user.click(await screen.findByRole("button", { name: "Show 12 invoices" }));
    expect(screen.queryByRole("columnheader", { name: "Document" })).not.toBeInTheDocument();
    expect(server.calls.filter((c) => c.url.pathname.startsWith("/api/v1/payables/open-items/"))).toHaveLength(0);
  });

  it("tells a filtered empty list from a first visit and clears the filters", async () => {
    const user = userEvent.setup();
    api((url) => (url.pathname === "/api/v1/payables/open-items" ? json(200, { items: url.searchParams.get("due_to") ? [] : [item()] }) : undefined));
    renderAt("/payables/open-items");
    await screen.findByText("INV-1001");
    expect(screen.queryByRole("button", { name: "Clear filters" })).not.toBeInTheDocument();
    await user.type(screen.getByLabelText(/Due by/), "2020-01-01");
    expect(await screen.findByText("No open items match these filters")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Clear filters" }));
    expect(await screen.findByText("INV-1001")).toBeInTheDocument();
  });
});

describe("payment runs", () => {
  const RUN = "77777777-7777-4777-8777-777777777777";
  const run = (patch: Record<string, unknown> = {}) => ({
    id: RUN,
    legalEntityId: LE,
    proposalId: PROPOSAL,
    status: "RUNNING",
    requestedBy: "marcus",
    createdAt: "2026-11-11T09:00:00Z",
    payments: [{ groupId: "g1", vendorId: V1, currency: "USD", paymentMethod: "TRANSFER", endToEndId: "E2E-1", amount: "968.10", valuedAmount: "968.10" }],
    ...patch,
  });

  it("guards formatAmount against empty or malformed input", () => {
    expect(formatAmount("", "USD")).toBe("Not available");
    expect(formatAmount("abc", "USD")).toBe("Not available");
    expect(formatAmount("12.5", "USD")).toBe("12.50 USD");
  });

  it("starts a run from an approved proposal and opens it", async () => {
    const user = userEvent.setup();
    api((url, init) => {
      if (url.pathname === "/api/v1/payables/payment-runs" && init.method === "POST") return json(201, run());
      if (url.pathname === `/api/v1/payables/payment-runs/${RUN}`) return json(200, run());
      if (url.pathname === `/api/v1/payables/payment-proposals/${PROPOSAL}`) return json(200, proposal({ status: "APPROVED" }));
      return undefined;
    });
    renderAt(`/payables/proposals/${PROPOSAL}`, [...READ, "payment-run.execute"]);
    await user.click(await screen.findByRole("button", { name: "Start payment run" }));
    expect(await screen.findByRole("button", { name: "Generate the bank file" })).toBeInTheDocument();
    expect(calls("POST", "/api/v1/payables/payment-runs")).toHaveLength(1);
  });

  it("offers each step in order and shows the next one after a step succeeds", async () => {
    const user = userEvent.setup();
    let state = run();
    api((url, init) => {
      if (url.pathname === `/api/v1/payables/payment-runs/${RUN}:generate-file` && init.method === "POST") {
        state = run({ fileGeneratedAt: "2026-11-11T09:01:00Z", fileNumber: "PF-1", paymentCount: 1 });
        return json(200, state);
      }
      if (url.pathname === `/api/v1/payables/payment-runs/${RUN}`) return json(200, state);
      return undefined;
    });
    renderAt(`/payables/runs/${RUN}`, [...READ, "payment-run.execute"]);
    await user.click(await screen.findByRole("button", { name: "Generate the bank file" }));
    expect(await screen.findByRole("button", { name: "Post to the ledger" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Download bank file" })).toBeInTheDocument();
  });

  it("fails a run with a reason", async () => {
    const user = userEvent.setup();
    let state = run();
    api((url, init) => {
      if (url.pathname.endsWith(":fail") && init.method === "POST") {
        state = run({ status: "FAILED", failureReason: JSON.parse(String(init.body)).reason });
        return json(200, state);
      }
      if (url.pathname === `/api/v1/payables/payment-runs/${RUN}`) return json(200, state);
      return undefined;
    });
    renderAt(`/payables/runs/${RUN}`, [...READ, "payment-run.execute"]);
    await user.click(await screen.findByRole("button", { name: "Mark run as failed" }));
    await user.type(screen.getByLabelText(/Why is this run failing/), "Bank rejected the file");
    await user.click(screen.getByRole("button", { name: "Mark run as failed" }));
    expect(await screen.findByText(/Bank rejected the file/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Post to the ledger" })).not.toBeInTheDocument();
  });

  it("hides run actions without the execute duty", async () => {
    api((url) => (url.pathname === `/api/v1/payables/payment-runs/${RUN}` ? json(200, run()) : undefined));
    renderAt(`/payables/runs/${RUN}`, [...READ, "payment-run.read"]);
    expect(await screen.findByText(/You don't have rights to run payments/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Generate the bank file" })).not.toBeInTheDocument();
  });
});
