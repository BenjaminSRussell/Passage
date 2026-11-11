import { findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { DUTY_LEDGER_ADMIN, type ActivationRun } from "./api/client";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LEDGER = "0190a000-0000-7000-8000-000000000001";
const ADMIN = [DUTY_LEDGER_ADMIN];

function run(patch: Partial<ActivationRun> = {}): ActivationRun {
  return {
    id: "0190a000-0000-7000-8000-0000000000f1",
    ledgerId: LEDGER,
    account: "1999",
    accountName: "Clearing",
    activationDate: "2026-04-01",
    itemsCreated: 3,
    lineItemsCreated: 1,
    totalDebit: "157.00",
    totalCredit: "30.00",
    createdBy: "user-1",
    createdAt: "2026-04-02T08:00:00Z",
    ...patch,
  };
}

describe("open-item activation list", () => {
  it("shows the runs and passes the accessibility check", async () => {
    server.handler = (url) => (url.pathname.endsWith("/open-item-activations") ? json(200, { items: [run()] }) : undefined);
    const { container } = renderAt(`/ledger/open-item-activations?ledger=${LEDGER}`, ADMIN);
    const table = await findTable({ name: "Activation runs" });
    expect(within(table).getByText("1999 Clearing")).toBeInTheDocument();
    expect(within(table).getByText("157.00")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Activate an account" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("offers the run only to holders of the Administer ledger duty", async () => {
    server.handler = (url) => (url.pathname.endsWith("/open-item-activations") ? json(200, { items: [run()] }) : undefined);
    renderAt(`/ledger/open-item-activations?ledger=${LEDGER}`, ["ledger.journal.read", "ledger.account.write"]);
    await findTable({ name: "Activation runs" });
    expect(screen.queryByRole("link", { name: "Activate an account" })).not.toBeInTheDocument();
  });

  it("shows the empty state", async () => {
    server.handler = (url) => (url.pathname.endsWith("/open-item-activations") ? json(200, { items: [] }) : undefined);
    renderAt(`/ledger/open-item-activations?ledger=${LEDGER}`);
    expect(await screen.findByText("No activation runs yet")).toBeInTheDocument();
  });

  it("shows the load error with a retry", async () => {
    server.handler = (url) => (url.pathname.endsWith("/open-item-activations") ? json(500, { code: "COM-0500", detail: "boom" }) : undefined);
    renderAt(`/ledger/open-item-activations?ledger=${LEDGER}`);
    expect(await screen.findByText("Could not load the activation runs")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("asks for a ledger first", () => {
    renderAt("/ledger/open-item-activations");
    expect(screen.getByText("Choose a ledger to see its activation runs.")).toBeInTheDocument();
  });
});

describe("activate an account form", () => {
  it("checks the account and the date before any request", async () => {
    renderAt(`/ledger/open-item-activations/new?ledger=${LEDGER}`, ADMIN);
    fireEvent.click(await screen.findByRole("button", { name: "Activate account" }));
    expect(await screen.findByText(/Enter the account number/)).toBeInTheDocument();
    expect(screen.getByText(/Enter the activation date/)).toBeInTheDocument();
    expect(server.calls.filter((c) => c.method === "POST")).toHaveLength(0);
  });

  it("posts the run with an Idempotency-Key and returns to the list", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(201, run());
      if (url.pathname.endsWith("/open-item-activations")) return json(200, { items: [run()] });
      return undefined;
    };
    const { container } = renderAt(`/ledger/open-item-activations/new?ledger=${LEDGER}`, ADMIN);
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(await screen.findByLabelText("Account number"), { target: { value: "1999" } });
    fireEvent.change(screen.getByLabelText("Activation date"), { target: { value: "2026-04-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Activate account" }));
    await waitFor(() => expect(server.calls.some((c) => c.method === "POST")).toBe(true));
    const call = server.calls.find((c) => c.method === "POST")!;
    expect(call.url.pathname).toBe(`/api/v1/ledger/ledgers/${LEDGER}/open-item-accounts/1999:activate`);
    expect(call.body).toEqual({ activationDate: "2026-04-01" });
    expect(idempotencyKey(call)).toBeTruthy();
    expect(await screen.findByRole("heading", { name: "Open-item activation" })).toBeInTheDocument();
  });

  it("shows the service's refusal", async () => {
    server.handler = (_url, init) => (init.method === "POST" ? json(409, { code: "LEDGER_OI_ALREADY_ACTIVE", detail: "account 1999 is already open-item managed" }) : undefined);
    renderAt(`/ledger/open-item-activations/new?ledger=${LEDGER}`, ADMIN);
    fireEvent.change(await screen.findByLabelText("Account number"), { target: { value: "1999" } });
    fireEvent.change(screen.getByLabelText("Activation date"), { target: { value: "2026-04-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Activate account" }));
    expect(await screen.findByText(/LEDGER_OI_ALREADY_ACTIVE/)).toBeInTheDocument();
  });

  it("tells a user without the duty instead of showing the form", () => {
    renderAt(`/ledger/open-item-activations/new?ledger=${LEDGER}`);
    expect(screen.getByText("You cannot run an activation")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Activate account" })).not.toBeInTheDocument();
  });

  it("needs a ledger", () => {
    renderAt("/ledger/open-item-activations/new");
    expect(screen.getByText(/Open this page from the activation list/)).toBeInTheDocument();
  });
});
