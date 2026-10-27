import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Entry, ReasonCode } from "./api/client";
import { emptyReason, toReasonInput, validateReason } from "./ReasonCodeFormPage";
import { ledgerReasons, validateReverse } from "./ReverseEntryForm";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const ENTRY = "0190a000-0000-7000-8000-000000000003";
const REVERSING = "0190a000-0000-7000-8000-000000000004";
const LEDGER = "0190a000-0000-7000-8000-000000000001";

function reason(patch: Partial<ReasonCode>): ReasonCode {
  return {
    code: "ERR01",
    name: "Input error",
    appliesTo: ["ledger"],
    requiresComment: false,
    negativePostingAllowed: false,
    alternativePostingDateAllowed: true,
    active: true,
    updatedAt: "2026-10-27T10:00:00Z",
    ...patch,
  };
}

function entry(patch: Partial<Entry> = {}): Entry {
  return {
    id: ENTRY,
    ledgerId: LEDGER,
    voucher: "GL-000001",
    postingSequence: 1,
    postingDate: "2026-03-10",
    periodKey: "2026-03",
    description: "Vendor invoice 7F3A",
    traceId: "a".repeat(32),
    totals: { currency: "USD", debit: "40.00", credit: "40.00" },
    lines: [
      { id: "l1", lineNumber: 1, account: "6010", displayName: "6010", debitCredit: "D", amount: "40.00" },
      { id: "l2", lineNumber: 2, account: "2100", displayName: "2100", debitCredit: "C", amount: "40.00" },
    ],
    ...patch,
  };
}

const codes = [
  reason({}),
  reason({ code: "STORNO", name: "Storno", negativePostingAllowed: true }),
  reason({ code: "NOTE", name: "Needs a note", requiresComment: true }),
  reason({ code: "SAMED", name: "Same date", alternativePostingDateAllowed: false }),
  reason({ code: "BANK1", name: "Bank only", appliesTo: ["bank"] }),
  reason({ code: "OLD", name: "Retired", active: false }),
];

describe("reason codes list", () => {
  it("lists the tenant's codes with their flags and opens one for editing", async () => {
    server.handler = (url, init) => {
      if (url.pathname === "/api/v1/ledger/reason-codes" && (init.method ?? "GET") === "GET") return json(200, { items: codes });
      if (url.pathname === "/api/v1/ledger/reason-codes/STORNO") return json(200, codes[1]);
      return undefined;
    };
    const { container } = renderAt("/ledger/reason-codes");
    await screen.findByRole("button", { name: "STORNO" });
    const table = screen.getByRole("table", { name: "Reason codes" });
    const storno = within(table).getByRole("button", { name: "STORNO" }).closest("tr") as HTMLElement;
    expect(within(storno).getByText("Storno")).toBeInTheDocument();
    expect(within(table).getByRole("button", { name: "OLD" }).closest("tr")?.textContent).toContain("Inactive");
    expect(within(table).getByRole("button", { name: "BANK1" }).closest("tr")?.textContent).toContain("Bank");
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(within(table).getByRole("button", { name: "STORNO" }));
    expect(screen.getByTestId("location").textContent).toBe("/ledger/reason-codes/STORNO");
    expect(await screen.findByLabelText("Name")).toHaveValue("Storno");
    expect(screen.getByLabelText("Code")).toHaveAttribute("readonly");
    expect(screen.getByLabelText("Negative posting")).toBeChecked();
  });

  it("shows the empty state when the tenant has no codes", async () => {
    server.handler = () => json(200, { items: [] });
    renderAt("/ledger/reason-codes");
    expect(await screen.findByText("No reason codes yet")).toBeInTheDocument();
  });

  it("shows the error and retries", async () => {
    let fail = true;
    server.handler = () => (fail ? json(500, { code: "COM-0500", detail: "internal error" }) : json(200, { items: codes }));
    renderAt("/ledger/reason-codes");
    expect(await screen.findByText("Could not load the reason codes")).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("button", { name: "ERR01" })).toBeInTheDocument();
  });
});

describe("reason code form", () => {
  it("validates like the service before saving", () => {
    expect(validateReason({ ...emptyReason, code: "err-1", name: "", appliesTo: [] })).toEqual({
      code: "Use 1 to 10 capital letters, digits or underscores.",
      name: "Enter a name of up to 60 characters.",
      appliesTo: "Pick at least one module.",
    });
    expect(validateReason({ ...emptyReason, code: "ERR_01", name: "Input error" })).toEqual({});
    // Modules are sent in the PRD order whatever order they were ticked in.
    expect(toReasonInput({ ...emptyReason, code: "X", name: " X ", appliesTo: ["vendor", "ledger"] }).appliesTo).toEqual(["ledger", "vendor"]);
  });

  it("does not call the service while the form is invalid", async () => {
    const { container } = renderAt("/ledger/reason-codes/new");
    fireEvent.click(screen.getByLabelText("Ledger"));
    fireEvent.click(screen.getByRole("button", { name: "Save reason code" }));
    expect(await screen.findByText("Pick at least one module.")).toBeInTheDocument();
    expect(screen.getByText("Enter a name of up to 60 characters.")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);
  });

  it("creates a code with PUT and returns to the list", async () => {
    server.handler = (_url, init) => {
      if (init.method === "PUT") return json(201, reason({ code: "STORNO", name: "Storno", negativePostingAllowed: true }));
      return json(200, { items: [] });
    };
    renderAt("/ledger/reason-codes/new");
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "storno" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Storno" } });
    fireEvent.click(screen.getByLabelText("Negative posting"));
    fireEvent.click(screen.getByLabelText("Vendor"));
    fireEvent.click(screen.getByRole("button", { name: "Save reason code" }));
    await waitFor(() => expect(screen.getByTestId("location").textContent).toBe("/ledger/reason-codes"));
    const put = server.calls.find((c) => c.method === "PUT");
    expect(put?.url.pathname).toBe("/api/v1/ledger/reason-codes/STORNO");
    expect(put?.body).toEqual({
      name: "Storno",
      appliesTo: ["ledger", "vendor"],
      requiresComment: false,
      negativePostingAllowed: true,
      alternativePostingDateAllowed: true,
      active: true,
    });
  });

  it("shows a service rejection and stays on the form", async () => {
    server.handler = () => json(400, { code: "COM-0001", detail: "validation failed" });
    renderAt("/ledger/reason-codes/new");
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "ERR01" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Input error" } });
    fireEvent.click(screen.getByRole("button", { name: "Save reason code" }));
    expect(await screen.findByText("validation failed (COM-0001)")).toBeInTheDocument();
    expect(screen.getByTestId("location").textContent).toBe("/ledger/reason-codes/new");
  });

  it("reports a missing code on the edit page", async () => {
    server.handler = () => json(404, { code: "LEDGER_REASON_NOT_FOUND", detail: "reason code not found" });
    renderAt("/ledger/reason-codes/NOPE");
    expect(await screen.findByText("Could not load the reason code")).toBeInTheDocument();
    expect(screen.getByText("reason code not found")).toBeInTheDocument();
  });
});

describe("entry screen and reversal", () => {
  function entryServer(reply: (init: RequestInit) => ReturnType<typeof json> | undefined) {
    server.handler = (url, init) => {
      if (url.pathname === "/api/v1/ledger/reason-codes") return json(200, { items: codes });
      if (url.pathname === `/api/v1/ledger/journal-entries/${ENTRY}/reverse`) return reply(init);
      if (url.pathname === `/api/v1/ledger/journal-entries/${ENTRY}`) return json(200, entry());
      if (url.pathname === `/api/v1/ledger/journal-entries/${REVERSING}`) {
        return json(200, entry({
          id: REVERSING,
          voucher: "GL-000002",
          reversalOfEntryId: ENTRY,
          reasonCode: "STORNO",
          reasonComment: "wrong vendor",
          lines: [
            { id: "r1", lineNumber: 1, account: "6010", displayName: "6010", debitCredit: "C", amount: "40.00", negativePosting: true },
            { id: "r2", lineNumber: 2, account: "2100", displayName: "2100", debitCredit: "D", amount: "40.00", negativePosting: true },
          ],
        }));
      }
      return undefined;
    };
  }

  it("offers only active ledger codes, reverses with a key and opens the reversing entry", async () => {
    entryServer(() => json(201, { entries: [entry({ id: REVERSING, voucher: "GL-000002" })] }));
    const { container } = renderAt(`/ledger/entries/${ENTRY}`);
    expect(await screen.findByRole("heading", { name: "Journal entry GL-000001" })).toBeInTheDocument();
    const select = screen.getByLabelText("Reason code") as HTMLSelectElement;
    await waitFor(() => expect(select.options.length).toBe(5));
    expect(Array.from(select.options).map((o) => o.value)).toEqual(["", "ERR01", "STORNO", "NOTE", "SAMED"]);
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(select, { target: { value: "STORNO" } });
    expect(screen.getByText("Storno reversal")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Reason comment"), { target: { value: " wrong vendor " } });
    fireEvent.click(screen.getByRole("button", { name: "Reverse entry" }));

    await waitFor(() => expect(screen.getByTestId("location").textContent).toBe(`/ledger/entries/${REVERSING}`));
    const post = server.calls.find((c) => c.method === "POST");
    expect(post?.body).toEqual({ reasonCode: "STORNO", reasonComment: "wrong vendor" });
    expect(idempotencyKey(post!)).toMatch(/^[0-9a-f-]{36}$/);

    expect(await screen.findByRole("heading", { name: "Journal entry GL-000002" })).toBeInTheDocument();
    expect(screen.getByText("wrong vendor")).toBeInTheDocument();
    expect(screen.getAllByText("Storno")).toHaveLength(2);
    expect(screen.getByRole("link", { name: "Original entry" })).toHaveAttribute("href", `/ledger/entries/${ENTRY}`);
  });

  it("shows the service's reason violation on the reason field", async () => {
    entryServer(() =>
      json(422, {
        code: "LEDGER_REASON_REQUIRED",
        detail: "entry rejected",
        violations: [{ code: "LEDGER_REASON_REQUIRED", field: "reasonCode", message: "a reason code is required for this reversal" }],
      }),
    );
    renderAt(`/ledger/entries/${ENTRY}`);
    fireEvent.click(await screen.findByRole("button", { name: "Reverse entry" }));
    expect(await screen.findByText("a reason code is required for this reversal")).toBeInTheDocument();
    expect(screen.getByLabelText("Reason code")).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByTestId("location").textContent).toBe(`/ledger/entries/${ENTRY}`);
  });

  it("shows a non-field rejection such as an entry already reversed", async () => {
    entryServer(() =>
      json(422, {
        code: "LEDGER_REVERSAL_ALREADY_REVERSED",
        detail: "entry rejected",
        violations: [{ code: "LEDGER_REVERSAL_ALREADY_REVERSED", field: "id", message: "entry GL-000001 was already reversed by GL-000002" }],
      }),
    );
    renderAt(`/ledger/entries/${ENTRY}`);
    fireEvent.click(await screen.findByRole("button", { name: "Reverse entry" }));
    expect(await screen.findByText("entry GL-000001 was already reversed by GL-000002 (LEDGER_REVERSAL_ALREADY_REVERSED)")).toBeInTheDocument();
  });

  it("checks comment and date rules of the chosen code before calling the service", () => {
    const e = entry();
    expect(validateReverse({ reasonCode: "NOTE", reasonComment: " ", postingDate: "", description: "" }, e, codes[2])).toEqual({
      reasonComment: "Reason code NOTE needs a comment.",
    });
    expect(validateReverse({ reasonCode: "SAMED", reasonComment: "", postingDate: "2026-03-11", description: "" }, e, codes[3])).toEqual({
      postingDate: "Reason code SAMED keeps the original posting date (2026-03-10).",
    });
    expect(validateReverse({ reasonCode: "", reasonComment: "", postingDate: "2026-03-09", description: "" }, e, undefined)).toEqual({
      postingDate: "The reversal can't be dated before the original (2026-03-10).",
    });
    expect(ledgerReasons(codes).map((c) => c.code)).toEqual(["ERR01", "STORNO", "NOTE", "SAMED"]);
  });

  it("reports an entry that cannot be loaded", async () => {
    server.handler = (url) =>
      url.pathname.endsWith("/reason-codes") ? json(200, { items: [] }) : json(404, { code: "LEDGER_ENTRY_NOT_FOUND", detail: "journal entry not found" });
    renderAt(`/ledger/entries/${ENTRY}`);
    expect(await screen.findByText("Could not load the entry")).toBeInTheDocument();
    expect(screen.getByText("journal entry not found")).toBeInTheDocument();
  });
});
