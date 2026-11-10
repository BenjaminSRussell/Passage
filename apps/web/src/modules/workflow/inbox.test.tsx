import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { describe, expect, it } from "vitest";
import { WorkContext } from "../../shell/lookup/workContextState";
import { PrincipalProvider } from "../../shell/principal";
import { json, pageViolations, renderRoutes, server, useFakeServer } from "../../shell/testing/fakeServer";
import { findTable } from "../../test-utils";
import { InboxPage } from "./InboxPage";

useFakeServer();

const YESTERDAY = new Date(Date.now() - 3 * 86_400_000).toISOString();
const LONG_AGO = "2020-01-02T00:00:00Z";

const requisition = {
  id: "0f9d1e0e-1111-4111-8111-000000000001",
  source: "purchasing",
  kind: "Requisition approval",
  reason: "Waiting for approval",
  number: "PR-1042",
  link: "/purchasing/requisitions/0f9d1e0e-1111-4111-8111-000000000001",
  actions: ["approve", "reject", "open"],
  createdAt: YESTERDAY,
};
const change = {
  id: "0f9d1e0e-2222-4222-8222-000000000002",
  source: "party",
  kind: "Customer change",
  reason: "A change to a protected customer field needs a second person to approve it",
  number: "credit limit 5000",
  link: "/party/pending-changes/0f9d1e0e-2222-4222-8222-000000000002",
  actions: ["approve", "reject", "open"],
  createdAt: YESTERDAY,
};
const hold = {
  id: "0f9d1e0e-3333-4333-8333-000000000003",
  source: "payables",
  kind: "Payment hold",
  reason: "On payment hold; due 2020-01-31",
  number: "INV-77",
  link: "/payables",
  actions: ["open"],
  createdAt: LONG_AGO,
  dueDate: "2020-01-31",
};

const page = (overrides: Record<string, unknown> = {}) => ({ items: [requisition, change, hold], unavailable: [], truncated: [], ...overrides });
const inboxCalls = () => server.calls.filter((c) => c.url.pathname === "/api/v1/inbox");

function mount(duties: string[] = [], legalEntityId = "", extra?: ReactNode) {
  return renderRoutes(
    [
      {
        path: "/workflow/inbox",
        element: (
          <PrincipalProvider duties={duties}>
            <WorkContext.Provider value={{ identity: "u", legalEntityId, siteId: "", setLegalEntityId: () => undefined, setSiteId: () => undefined }}>
              <InboxPage />
              {extra}
            </WorkContext.Provider>
          </PrincipalProvider>
        ),
      },
    ],
    "/workflow/inbox",
  );
}

describe("My work inbox", () => {
  it("lists the waiting work with plain-language reason, age, due date and an Open link, and never shows ids", async () => {
    server.handler = () => json(200, page());
    const { container } = mount();
    const table = await findTable({ name: "Work waiting for you" });
    expect(table).toHaveTextContent("Requisition approval");
    expect(table).toHaveTextContent("PR-1042");
    expect(table).toHaveTextContent("Waiting for approval");
    expect(table).toHaveTextContent("3 days");
    expect(table).toHaveTextContent("Jan 31, 2020");
    expect(table).toHaveTextContent("Overdue");
    expect(within(table).getByRole("link", { name: "Open Requisition approval PR-1042" })).toHaveAttribute("href", requisition.link);
    expect(container.textContent).not.toMatch(/0f9d1e0e/);
    expect(pageViolations(container)).toEqual([]);
  });

  it("offers no Approve or Reject without the duty, and only Open for sources with no existing action", async () => {
    server.handler = () => json(200, page());
    mount([]);
    await findTable();
    expect(screen.queryByRole("button", { name: /Approve|Reject/ })).not.toBeInTheDocument();
  });

  it("filters by type of work", async () => {
    const user = userEvent.setup();
    server.handler = () => json(200, page());
    mount();
    await findTable();
    await user.selectOptions(screen.getByLabelText("Type of work"), "Payment hold");
    const table = screen.getByRole("table");
    expect(table).toHaveTextContent("INV-77");
    expect(table).not.toHaveTextContent("PR-1042");
  });

  it("says You're all caught up when nothing is waiting", async () => {
    server.handler = () => json(200, page({ items: [] }));
    const { container } = mount();
    expect(await screen.findByText("You're all caught up")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows an error with retry that loads again", async () => {
    const user = userEvent.setup();
    let fail = true;
    server.handler = () => (fail ? json(500, { code: "COM-0500", detail: "boom" }) : json(200, page()));
    mount();
    expect(await screen.findByText("My work could not be loaded")).toBeInTheDocument();
    fail = false;
    await user.click(screen.getByRole("button", { name: "Try again" }));
    await findTable();
    expect(inboxCalls()).toHaveLength(2);
  });

  it("names the sources that could not be loaded and retries", async () => {
    const user = userEvent.setup();
    server.handler = () => json(200, page({ unavailable: [{ source: "ledger", reason: "timeout" }, { source: "party", reason: "down" }] }));
    mount();
    expect(await screen.findByText("Some items could not be loaded: journal batches, customer changes")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(inboxCalls()).toHaveLength(2));
  });

  it("tells the user when a source has more items than were read", async () => {
    server.handler = () => json(200, page({ truncated: ["purchasing"] }));
    mount();
    expect(await screen.findByText(/Only the newest items from requisition approvals are shown/)).toBeInTheDocument();
  });

  it("loads more with the cursor and appends", async () => {
    const user = userEvent.setup();
    server.handler = (url) =>
      url.searchParams.get("cursor") === "next-1" ? json(200, page({ items: [hold] })) : json(200, page({ items: [requisition], nextCursor: "next-1" }));
    mount();
    await findTable();
    await user.click(screen.getByRole("button", { name: "Load more" }));
    await waitFor(() => expect(screen.getByRole("table")).toHaveTextContent("INV-77"));
    expect(screen.getByRole("table")).toHaveTextContent("PR-1042");
    expect(screen.queryByRole("button", { name: "Load more" })).not.toBeInTheDocument();
  });

  it("warns that a filter only covers the loaded pages", async () => {
    const user = userEvent.setup();
    server.handler = () => json(200, page({ nextCursor: "next-1" }));
    mount();
    await findTable();
    await user.selectOptions(screen.getByLabelText("Type of work"), "Payment hold");
    expect(screen.getByText(/More items may match/)).toBeInTheDocument();
  });

  it("stops a page load that is still running when the user leaves", async () => {
    const user = userEvent.setup();
    server.handler = (url) => json(200, page({ nextCursor: url.searchParams.get("cursor") ? undefined : "next-1" }));
    const view = mount();
    await findTable();
    await user.click(screen.getByRole("button", { name: "Load more" }));
    view.unmount();
    expect((inboxCalls().at(-1)?.init.signal as AbortSignal).aborted).toBe(true);
  });

  it("asks to reload from the start when the sources changed between pages", async () => {
    const user = userEvent.setup();
    server.handler = (url) =>
      url.searchParams.get("cursor") ? json(409, { code: "COM-0409", detail: "changed" }) : json(200, page({ items: [requisition], nextCursor: "next-1" }));
    mount();
    await findTable();
    await user.click(screen.getByRole("button", { name: "Load more" }));
    expect(await screen.findByText(/Reload My work from the start/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Reload from the start" }));
    await waitFor(() => expect(screen.queryByText(/Reload My work from the start/)).not.toBeInTheDocument());
    expect(inboxCalls().at(-1)?.url.searchParams.get("cursor")).toBeNull();
  });

  it("works in the chosen legal entity so the server never has to ask", async () => {
    server.handler = () => json(200, page());
    mount([], "00000000-0000-4000-8000-0000000000e1");
    await findTable();
    expect(inboxCalls()[0].url.searchParams.get("legalEntityId")).toBe("00000000-0000-4000-8000-0000000000e1");
  });

  it("asks the user to choose a legal entity if the server still needs one", async () => {
    server.handler = () => json(400, { code: "COM-0400", detail: "legalEntityId is required" });
    mount();
    expect(await screen.findByText("Choose a legal entity at the top of the page to see your work.")).toBeInTheDocument();
  });

  describe("approving a customer change", () => {
    it("confirms, calls the existing decision endpoint, removes the row and announces the result", async () => {
      const user = userEvent.setup();
      server.handler = (url, init) => {
        if (url.pathname === "/api/v1/inbox") return json(200, page());
        if (url.pathname === `/api/v1/party/pending-changes/${change.id}:approve` && init.method === "POST") return json(200, { id: change.id, status: "approved" });
        return undefined;
      };
      mount(["party.approve"]);
      await findTable();
      expect(screen.queryByRole("button", { name: /PR-1042/ })).not.toBeInTheDocument();
      await user.click(screen.getByRole("button", { name: "Approve Customer change credit limit 5000" }));
      expect(screen.getByRole("alertdialog")).toBeInTheDocument();
      await user.click(screen.getByRole("button", { name: "Confirm approval" }));
      await waitFor(() => expect(screen.getByRole("table")).not.toHaveTextContent("credit limit 5000"));
      const status = screen.getAllByRole("status").find((s) => s.tabIndex === -1) as HTMLElement;
      expect(status).toHaveTextContent("Approved: credit limit 5000.");
      expect(status).toHaveFocus();
    });

    it("keeps the row and explains a failed decision", async () => {
      const user = userEvent.setup();
      server.handler = (url) => (url.pathname === "/api/v1/inbox" ? json(200, page()) : json(500, { code: "TEST-500", detail: "Service down" }));
      mount(["party.approve"]);
      await findTable();
      await user.click(screen.getByRole("button", { name: "Reject Customer change credit limit 5000" }));
      await user.click(screen.getByRole("button", { name: "Confirm rejection" }));
      expect(await screen.findByText("The decision failed")).toBeInTheDocument();
      expect(screen.getByRole("table")).toHaveTextContent("credit limit 5000");
    });

    it("falls back to all types when the last item of the filtered type is approved", async () => {
      const user = userEvent.setup();
      server.handler = (url) => (url.pathname === "/api/v1/inbox" ? json(200, page()) : json(200, { id: change.id }));
      mount(["party.approve"]);
      await findTable();
      await user.selectOptions(screen.getByLabelText("Type of work"), "Customer change");
      await user.click(screen.getByRole("button", { name: "Approve Customer change credit limit 5000" }));
      await user.click(screen.getByRole("button", { name: "Confirm approval" }));
      await waitFor(() => expect(screen.getByRole("table")).toHaveTextContent("PR-1042"));
      expect(screen.getByLabelText("Type of work")).toHaveValue("");
    });

    it("takes a row off the list when someone else already decided it", async () => {
      const user = userEvent.setup();
      server.handler = (url) => (url.pathname === "/api/v1/inbox" ? json(200, page()) : json(409, { code: "TEST-409", detail: "Already decided" }));
      mount(["party.approve"]);
      await findTable();
      await user.click(screen.getByRole("button", { name: "Approve Customer change credit limit 5000" }));
      await user.click(screen.getByRole("button", { name: "Confirm approval" }));
      expect(await screen.findByText("credit limit 5000 was already decided, so it has been taken off your list.")).toBeInTheDocument();
      expect(screen.getByRole("table")).not.toHaveTextContent("Customer change");
    });

    it("does nothing when the confirmation is cancelled", async () => {
      const user = userEvent.setup();
      server.handler = () => json(200, page());
      mount(["party.approve"]);
      await findTable();
      await user.click(screen.getByRole("button", { name: "Approve Customer change credit limit 5000" }));
      await user.click(screen.getByRole("button", { name: "Cancel" }));
      expect(server.calls.filter((c) => c.method === "POST")).toHaveLength(0);
    });
  });
});
