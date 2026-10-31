import { findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { ClearingDocument, OpenItem, ToleranceGroup } from "./api/client";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";
import { emptyTolerance, validateTolerance } from "./ToleranceGroupFormPage";
import { outcomeText } from "./OpenItemsPage";

useFakeServer();

const LEDGER = "0190a000-0000-7000-8000-000000000001";
const ITEM_A = "0190a000-0000-7000-8000-0000000000e1";
const ITEM_B = "0190a000-0000-7000-8000-0000000000e2";
const DOC = "0190a000-0000-7000-8000-0000000000f1";
const ENTRY = "0190a000-0000-7000-8000-0000000000a1";
const api = "/api/v1/ledger";

function item(patch: Partial<OpenItem> = {}): OpenItem {
  return {
    id: ITEM_A,
    ledgerId: LEDGER,
    account: "1999",
    accountName: "Clearing",
    dimensions: {},
    sourceType: "gl_line",
    amount: "100.00",
    debitCredit: "D",
    currency: "USD",
    assignment: "PO-7",
    postingDate: "2026-03-10",
    status: "open",
    entryId: ENTRY,
    voucher: "GL-0000001",
    lineNumber: 1,
    ...patch,
  };
}

const itemB = () => item({ id: ITEM_B, amount: "99.98", debitCredit: "C", voucher: "GL-0000002" });

function group(patch: Partial<ToleranceGroup> = {}): ToleranceGroup {
  return {
    ledgerId: LEDGER,
    code: "STD",
    name: "Standard",
    debitLimit: "0.05",
    creditLimit: "0.05",
    percentLimit: "0",
    differenceAccount: "6990",
    active: true,
    version: 3,
    createdAt: "2026-03-01T00:00:00Z",
    updatedAt: "2026-03-01T00:00:00Z",
    ...patch,
  };
}

function doc(patch: Partial<ClearingDocument> = {}): ClearingDocument {
  return {
    id: DOC,
    ledgerId: LEDGER,
    account: "1999",
    clearingDate: "2026-03-11",
    mode: "manual",
    itemIds: [ITEM_A, ITEM_B],
    toleranceGroup: "STD",
    differenceAmount: "0.02",
    differenceEntryId: ENTRY,
    status: "cleared",
    createdBy: "clerk",
    createdAt: "2026-03-11T09:00:00Z",
    items: [item({ status: "cleared", clearingDocumentId: DOC }), { ...itemB(), status: "cleared", clearingDocumentId: DOC }],
    ...patch,
  };
}

const preview = {
  ledgerId: LEDGER,
  account: "1999",
  currency: "USD",
  itemCount: 2,
  debit: "100.00",
  credit: "99.98",
  net: "0.02",
  toleranceGroup: "STD",
  outcome: "difference",
  differenceAmount: "0.02",
  residualAmount: "0.00",
};

describe("clearing helpers", () => {
  it("describes each preview outcome", () => {
    const base = { ...preview, outcome: "balanced" } as never;
    expect(outcomeText(base)).toContain("net to zero");
    expect(outcomeText({ ...preview, outcome: "difference" } as never)).toContain("written off");
    expect(outcomeText({ ...preview, outcome: "residual" } as never)).toContain("new open item");
    expect(outcomeText({ ...preview, outcome: "unbalanced" } as never)).toContain("Add items");
  });

  it("mirrors the tolerance group rules", () => {
    expect(Object.keys(validateTolerance(emptyTolerance)).sort()).toEqual(["code", "creditLimit", "debitLimit", "differenceAccount", "name"]);
    const ok = { ...emptyTolerance, code: "STD", name: "Standard", debitLimit: "0.05", creditLimit: "0.01", differenceAccount: "6990" };
    expect(validateTolerance(ok)).toEqual({});
    expect(validateTolerance({ ...ok, percentLimit: "101" }).percentLimit).toBeDefined();
    expect(validateTolerance({ ...ok, debitLimit: "0.1234567" }).debitLimit).toBeDefined();
    expect(validateTolerance({ ...ok, creditLimit: "-1" }).creditLimit).toBeDefined();
  });
});

describe("open items", () => {
  const list = (items: OpenItem[]) => (url: URL) => (url.pathname === `${api}/open-items` ? json(200, { items }) : url.pathname.endsWith("/tolerance-groups") ? json(200, { items: [group()] }) : undefined);

  it("asks for a ledger first", () => {
    renderAt("/ledger/open-items");
    expect(screen.getByText("Enter a ledger id to see its open items.")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
  });

  it("lists items on both sides, filters and shows the empty and error states", async () => {
    server.handler = list([item(), itemB()]);
    const { container } = renderAt(`/ledger/open-items?ledger=${LEDGER}`);
    const table = await findTable({ name: "Open items" });
    expect(within(table).getByText("100.00").closest("tr")?.textContent).toContain("PO-7");
    expect(within(table).getByText("99.98")).toBeInTheDocument();
    expect(server.calls[0].url.searchParams.get("status")).toBe("open");
    expect(pageViolations(container)).toEqual([]);

    server.handler = list([]);
    fireEvent.change(screen.getByLabelText("Account"), { target: { value: "bad account" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    expect(await screen.findByText("Up to 20 letters, digits, dots, underscores or dashes.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Account"), { target: { value: "1999" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    expect(await screen.findByText("No open items")).toBeInTheDocument();
    expect(server.calls.at(-1)?.url.searchParams.get("account")).toBe("1999");
  });

  it("shows a load error with a retry", async () => {
    let fail = true;
    server.handler = (url) => (url.pathname === `${api}/open-items` ? (fail ? json(500, { code: "COM-0500", detail: "internal error" }) : json(200, { items: [] })) : undefined);
    renderAt(`/ledger/open-items?ledger=${LEDGER}`);
    expect(await screen.findByText("Could not load the open items")).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No open items")).toBeInTheDocument();
  });

  it("previews, then clears the selection with one key per attempt and opens the document", async () => {
    let failFirst = true;
    server.handler = (url, init) => {
      if (url.pathname === `${api}/clearings:simulate`) return json(200, preview);
      if (url.pathname === `${api}/clearings` && init.method === "POST") {
        if (failFirst) {
          failFirst = false;
          return json(422, { code: "LEDGER_CLEARING_UNBALANCED", detail: "Selected items leave a residual of 0.02 USD" });
        }
        return json(201, doc());
      }
      if (url.pathname === `${api}/clearings/${DOC}`) return json(200, doc());
      return list([item(), itemB()])(url);
    };
    const { container } = renderAt(`/ledger/open-items?ledger=${LEDGER}`);
    await findTable({ name: "Open items" });
    expect(screen.getByText("Select at least two open items to clear them.")).toBeInTheDocument();
    fireEvent.click(screen.getByLabelText("Select item GL-0000001"));
    fireEvent.click(screen.getByLabelText("Select item GL-0000002"));
    expect(await screen.findByRole("button", { name: "Clear 2 items" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    await screen.findByRole("option", { name: "STD - debit up to 0.05, credit up to 0.05" });
    fireEvent.change(screen.getByLabelText("Clearing date"), { target: { value: "31/03/2026" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview" }));
    expect(await screen.findByText("Dates use the form YYYY-MM-DD.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Clearing date"), { target: { value: "2026-03-11" } });
    fireEvent.change(screen.getByLabelText("Tolerance group"), { target: { value: "STD" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview" }));
    expect(await screen.findByText("Preview: difference")).toBeInTheDocument();
    const sim = server.calls.find((c) => c.url.pathname.endsWith(":simulate"));
    expect(sim?.body).toMatchObject({ ledgerId: LEDGER, itemIds: [ITEM_A, ITEM_B], clearingDate: "2026-03-11", toleranceGroup: "STD" });

    fireEvent.click(screen.getByRole("button", { name: "Clear 2 items" }));
    expect(await screen.findByText(/LEDGER_CLEARING_UNBALANCED/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Clear 2 items" }));
    await waitFor(() => expect(screen.getByTestId("location").textContent).toBe(`/ledger/clearings/${DOC}`));
    const posts = server.calls.filter((c) => c.method === "POST" && c.url.pathname === `${api}/clearings`);
    expect(posts).toHaveLength(2);
    expect(idempotencyKey(posts[0])).toBeTruthy();
    expect(idempotencyKey(posts[1])).toBe(idempotencyKey(posts[0]));
  });
});

describe("clearing documents", () => {
  it("lists documents, filters and opens one", async () => {
    server.handler = (url) => {
      if (url.pathname === `${api}/clearings`) return json(200, { items: url.searchParams.get("status") === "reset" ? [] : [doc()] });
      if (url.pathname === `${api}/clearings/${DOC}`) return json(200, doc());
      return undefined;
    };
    const { container } = renderAt(`/ledger/clearings?ledger=${LEDGER}`);
    const table = await findTable({ name: "Clearing documents" });
    expect(within(table).getByText("0.02").closest("tr")?.textContent).toContain("Cleared");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "reset" } });
    expect(await screen.findByText("No reset documents")).toBeInTheDocument();
  });

  it("shows the empty state", async () => {
    server.handler = (url) => (url.pathname === `${api}/clearings` ? json(200, { items: [] }) : undefined);
    renderAt(`/ledger/clearings?ledger=${LEDGER}`);
    expect(await screen.findByText("Nothing cleared yet")).toBeInTheDocument();
  });

  it("resets a cleared document with a reason and its own key", async () => {
    let reset = false;
    server.handler = (url, init) => {
      if (url.pathname === `${api}/clearings/${DOC}/reset` && init.method === "POST") {
        reset = true;
        return json(200, doc({ status: "reset", resetReasonCode: "CORR", resetBy: "boss" }));
      }
      if (url.pathname === `${api}/clearings/${DOC}`) return json(200, reset ? doc({ status: "reset", resetReasonCode: "CORR", resetBy: "boss" }) : doc());
      return undefined;
    };
    const { container } = renderAt(`/ledger/clearings/${DOC}?ledger=${LEDGER}`);
    expect(await screen.findByText("Account 1999, cleared on 2026-03-11.")).toBeInTheDocument();
    expect(within(await findTable({ name: "Cleared items" })).getByText("99.98")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    const reasonError = "Enter the reason code: 1 to 10 capital letters, digits or underscores.";
    fireEvent.click(screen.getByRole("button", { name: "Reset clearing" }));
    expect(await screen.findByText(reasonError)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Reason code"), { target: { value: "bad code" } });
    fireEvent.click(screen.getByRole("button", { name: "Reset clearing" }));
    expect(await screen.findByText(reasonError)).toBeInTheDocument();
    expect(server.calls.some((c) => c.method === "POST")).toBe(false);

    fireEvent.change(screen.getByLabelText("Reason code"), { target: { value: "corr" } });
    fireEvent.change(screen.getByLabelText("Comment"), { target: { value: "wrong items" } });
    fireEvent.click(screen.getByRole("button", { name: "Reset clearing" }));
    await waitFor(() => expect(screen.queryByRole("button", { name: "Reset clearing" })).not.toBeInTheDocument());
    const post = server.calls.find((c) => c.method === "POST");
    expect(post?.body).toEqual({ reasonCode: "CORR", comment: "wrong items" });
    expect(idempotencyKey(post as never)).toBeTruthy();
    expect(await screen.findByText(/reason CORR/)).toBeInTheDocument();
  });

  it("shows a 404 as an error with a retry", async () => {
    server.handler = () => json(404, { code: "LEDGER_CLEARING_NOT_FOUND", detail: "clearing document not found" });
    renderAt(`/ledger/clearings/${DOC}`);
    expect(await screen.findByText("Could not load the clearing document")).toBeInTheDocument();
  });
});

describe("tolerance groups", () => {
  it("lists groups, and shows the empty state", async () => {
    server.handler = (url) => (url.pathname.endsWith("/tolerance-groups") ? json(200, { items: [group(), group({ code: "OLD", name: "Old", active: false, percentLimit: "0.5" })] }) : undefined);
    const { container } = renderAt(`/ledger/tolerance-groups?ledger=${LEDGER}`);
    const table = await findTable({ name: "Tolerance groups" });
    expect(within(table).getByText("Standard").closest("tr")?.textContent).toContain("Active");
    expect(within(table).getByText("Old").closest("tr")?.textContent).toContain("0.5 %");
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the empty and error states", async () => {
    server.handler = (url) => (url.pathname.endsWith("/tolerance-groups") ? json(200, { items: [] }) : undefined);
    renderAt(`/ledger/tolerance-groups?ledger=${LEDGER}`);
    expect(await screen.findByText("No tolerance groups yet")).toBeInTheDocument();
  });

  it("checks the form, then creates with PUT", async () => {
    server.handler = (url, init) => {
      if (url.pathname === `${api}/ledgers/${LEDGER}/tolerance-groups/STD` && init.method === "PUT") return json(201, group());
      if (url.pathname.endsWith("/tolerance-groups")) return json(200, { items: [group()] });
      return undefined;
    };
    const { container } = renderAt(`/ledger/tolerance-groups/new?ledger=${LEDGER}`);
    fireEvent.click(screen.getByRole("button", { name: "Save tolerance group" }));
    expect(await screen.findAllByText("Enter an amount such as 0.05, with at most 6 decimals.")).toHaveLength(2);
    expect(server.calls).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "std" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Standard" } });
    fireEvent.change(screen.getByLabelText("Debit limit"), { target: { value: "0.05" } });
    fireEvent.change(screen.getByLabelText("Credit limit"), { target: { value: "0.01" } });
    fireEvent.change(screen.getByLabelText("Difference account"), { target: { value: "6990" } });
    fireEvent.click(screen.getByRole("button", { name: "Save tolerance group" }));
    await waitFor(() => expect(screen.getByTestId("location").textContent).toBe("/ledger/tolerance-groups"));
    const put = server.calls.find((c) => c.method === "PUT");
    expect(put?.body).toMatchObject({ name: "Standard", debitLimit: "0.05", creditLimit: "0.01", percentLimit: "0", differenceAccount: "6990", active: true });
    expect(new Headers(put?.init.headers).get("If-Match")).toBeNull();
  });

  it("edits with If-Match and reports a version conflict", async () => {
    server.handler = (url, init) => {
      if (url.pathname.endsWith("/tolerance-groups/STD") && init.method === "PUT") return json(412, { code: "LEDGER_VERSION_CONFLICT", detail: "the tolerance group changed" });
      if (url.pathname.endsWith("/tolerance-groups/STD")) return json(200, group());
      return undefined;
    };
    renderAt(`/ledger/tolerance-groups/STD?ledger=${LEDGER}`);
    expect(await screen.findByDisplayValue("Standard")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Debit limit"), { target: { value: "0.10" } });
    fireEvent.click(screen.getByRole("button", { name: "Save tolerance group" }));
    expect(await screen.findByText(/LEDGER_VERSION_CONFLICT/)).toBeInTheDocument();
    const put = server.calls.find((c) => c.method === "PUT");
    expect(new Headers(put?.init.headers).get("If-Match")).toBe('"3"');
  });
});

describe("open-item accounts", () => {
  const account = { ledgerId: LEDGER, account: "1999", name: "Clearing", enabled: true, openItems: 4, changedBy: "admin", changedAt: "2026-03-01T00:00:00Z" };

  it("lists accounts and switches one off", async () => {
    server.handler = (url, init) => {
      if (init.method === "PUT") return json(200, { ...account, enabled: false });
      if (url.pathname.endsWith("/open-item-accounts")) return json(200, { items: [account] });
      return undefined;
    };
    const { container } = renderAt(`/ledger/open-item-accounts?ledger=${LEDGER}`);
    const table = await findTable({ name: "Open-item accounts" });
    expect(within(table).getByText("1999").closest("tr")?.textContent).toContain("On");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Switch off 1999" }));
    await waitFor(() => expect(server.calls.some((c) => c.method === "PUT")).toBe(true));
    expect(server.calls.find((c) => c.method === "PUT")?.body).toEqual({ enabled: false });
  });

  it("checks the account number and shows the service's refusal", async () => {
    server.handler = (url, init) => {
      if (init.method === "PUT") return json(422, { code: "LEDGER_OI_ACTIVATION_BALANCE", detail: "open-item activation needs an activation run" });
      if (url.pathname.endsWith("/open-item-accounts")) return json(200, { items: [] });
      return undefined;
    };
    renderAt(`/ledger/open-item-accounts?ledger=${LEDGER}`);
    expect(await screen.findByText("No open-item accounts yet")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Switch on" }));
    expect(await screen.findByText(/Enter the account number/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Account number"), { target: { value: "1100" } });
    fireEvent.click(screen.getByRole("button", { name: "Switch on" }));
    expect(await screen.findByText(/LEDGER_OI_ACTIVATION_BALANCE/)).toBeInTheDocument();
  });
});
