import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { findTable } from "../../test-utils";
import { json, pageViolations, renderAt, server, useFakeServer } from "./testServer";
import { toFXSetting, validateBackfill, validateFX, validateMapping } from "./LedgerSetup";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";

describe("ledger setup validation", () => {
  it("checks a bank account mapping", () => {
    expect(validateMapping({ currency: "USD", accountRef: "", glAccount: "1010", name: "" })).toEqual({});
    expect(validateMapping({ currency: "usd", accountRef: "", glAccount: "1010", name: "" }).currency).toBeTruthy();
    expect(validateMapping({ currency: "USD", accountRef: "", glAccount: "10 10", name: "" }).glAccount).toBeTruthy();
    expect(validateMapping({ currency: "USD", accountRef: "DE-89", glAccount: "1010", name: "" }).accountRef).toBeTruthy();
  });

  it("needs a functional currency once cross-currency settlement is on", () => {
    const base = { crossCurrencyEnabled: false, functionalCurrency: "", gainAccount: "7100", lossAccount: "7200" };
    expect(validateFX(base)).toEqual({});
    expect(validateFX({ ...base, crossCurrencyEnabled: true }).functionalCurrency).toBeTruthy();
    expect(validateFX({ ...base, crossCurrencyEnabled: true, functionalCurrency: "USD" })).toEqual({});
    expect(validateFX({ ...base, lossAccount: "" }).lossAccount).toBeTruthy();
  });

  it("checks the FX tolerance against the service's bounds and leaves an empty one out", () => {
    const base = { crossCurrencyEnabled: false, functionalCurrency: "", gainAccount: "7100", lossAccount: "7200", toleranceAmount: "", tolerancePercent: "" };
    expect(validateFX(base)).toEqual({});
    expect(validateFX({ ...base, toleranceAmount: "0", tolerancePercent: "0.25" })).toEqual({});
    expect(validateFX({ ...base, toleranceAmount: "-1" }).toleranceAmount).toBeTruthy();
    expect(validateFX({ ...base, toleranceAmount: "1000000.5" }).toleranceAmount).toBeTruthy();
    expect(validateFX({ ...base, tolerancePercent: "100.5" }).tolerancePercent).toBeTruthy();
    expect(validateFX({ ...base, tolerancePercent: "0.12345" }).tolerancePercent).toBeTruthy();
    expect(toFXSetting(LE, { ...base, toleranceAmount: " 2.50 " })).toEqual({ legalEntityId: LE, crossCurrencyEnabled: false, functionalCurrency: "", gainAccount: "7100", lossAccount: "7200", toleranceAmount: "2.50" });
  });

  it("takes the opening account and date together", () => {
    expect(validateBackfill({ openingAccount: "", openingDate: "" })).toEqual({});
    expect(validateBackfill({ openingAccount: "3000", openingDate: "" }).openingAccount).toBeTruthy();
    expect(validateBackfill({ openingAccount: "3000", openingDate: "2027-01-01" })).toEqual({});
  });
});

describe("ledger setup page", () => {
  it("shows the bank accounts and exchange settings of the chosen entity and saves a change", async () => {
    server.handler = (url, init) => {
      if (url.pathname.endsWith("/bank-accounts") && !init.method) {
        return json(200, { items: [{ legalEntityId: LE, currency: "USD", accountRef: "", glAccount: "1010", name: "Operating account" }] });
      }
      if (url.pathname.endsWith("/fx-settings") && !init.method) {
        return json(200, { legalEntityId: LE, crossCurrencyEnabled: false, functionalCurrency: "", gainAccount: "7100", lossAccount: "7200", tolerancePercent: "0.5" });
      }
      if (url.pathname.endsWith("/fx-settings") && init.method === "PUT") return json(200, JSON.parse(String(init.body)));
      if (url.pathname.endsWith("/gl-journals:backfill")) {
        return json(200, { receipts: 2, applications: 1, openingItems: 0, openingAlreadyQueued: 0, dunningFees: 1, interest: 0, downPayments: 0, deductions: 0, more: false });
      }
      return undefined;
    };
    const { container } = renderAt("/receivables/ledger-setup");
    fireEvent.change(await screen.findByLabelText("Legal entity"), { target: { value: LE } });
    const table = await findTable({ name: "Bank accounts" });
    await waitFor(() => expect(table).toHaveTextContent("Operating account"));
    expect(table).toHaveTextContent("1010");
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(await screen.findByLabelText("Allow a payment to settle invoices in another currency"));
    fireEvent.click(screen.getByRole("button", { name: "Save exchange settings" }));
    expect(await screen.findByText("Enter the functional currency to settle across currencies.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Functional currency"), { target: { value: "usd" } });
    expect(screen.getByLabelText(/^Tolerance percent/)).toHaveValue("0.5");
    fireEvent.change(screen.getByLabelText(/^Tolerance amount/), { target: { value: "10" } });
    fireEvent.click(screen.getByRole("button", { name: "Save exchange settings" }));
    await waitFor(() => expect(server.calls.some((c) => c.method === "PUT" && c.url.pathname.endsWith("/fx-settings"))).toBe(true));
    const put = server.calls.find((c) => c.method === "PUT" && c.url.pathname.endsWith("/fx-settings"));
    expect(put?.body).toMatchObject({ legalEntityId: LE, crossCurrencyEnabled: true, functionalCurrency: "USD", gainAccount: "7100", lossAccount: "7200", toleranceAmount: "10", tolerancePercent: "0.5" });

    fireEvent.click(screen.getByRole("button", { name: "Queue the journals" }));
    expect(await screen.findByText(/2 cash receipts, 1 applications, 1 dunning fees/)).toBeInTheDocument();
  });
});
