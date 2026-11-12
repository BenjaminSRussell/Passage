import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Prospect } from "./api/client";
import { json, pageViolations, problem, renderAt, useFakeApi } from "./testServer";

const api = useFakeApi();

const ID = "0190b000-0000-7000-8000-000000000001";
const VENDOR = "0190b000-0000-7000-8000-000000000002";
const PARTY = "0190b000-0000-7000-8000-000000000003";

function prospect(patch: Partial<Prospect> = {}): Prospect {
  return {
    id: ID,
    companyName: "Lakeshore Machining LLC",
    contactName: "Dana Whitfield",
    email: "dana@lakeshore.example",
    country: "US",
    taxId: "39-2214876",
    status: "NEW",
    createdAt: "2026-11-10T09:00:00Z",
    ...patch,
  };
}

describe("supplier registrations", () => {
  it("lists registrations with the status in words and opens one", async () => {
    api.handler = (call) => (call.url.pathname === "/api/v1/party/vendor-prospects" ? json(200, { items: [prospect(), prospect({ id: "x", companyName: "Bayside", status: "REJECTED" })] }) : undefined);
    const { container } = renderAt("/party/vendor-prospects");
    const table = await screen.findByRole("table", { name: "Supplier registrations" });
    expect(await within(table).findByText("Lakeshore Machining LLC")).toBeInTheDocument();
    expect(within(table).getByText("Waiting for review")).toBeInTheDocument();
    expect(within(table).getByText("Rejected")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByText("Lakeshore Machining LLC"));
    await waitFor(() => expect(screen.getByTestId("where")).toHaveTextContent(`/party/vendor-prospects/${ID}`));
  });

  it("loads the next page of registrations on request", async () => {
    api.handler = (call) => {
      if (call.url.pathname !== "/api/v1/party/vendor-prospects") return undefined;
      return call.url.searchParams.get("cursor") === "next"
        ? json(200, { items: [prospect({ id: "p2", companyName: "Northfield Coatings" })] })
        : json(200, { items: [prospect()], nextCursor: "next" });
    };
    renderAt("/party/vendor-prospects");
    await screen.findByText("Lakeshore Machining LLC");
    fireEvent.click(screen.getByRole("button", { name: "Load more registrations" }));
    expect(await screen.findByText("Northfield Coatings")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Load more registrations" })).toBeNull();
  });

  it("sends the status filter to the service", async () => {
    api.handler = (call) => (call.url.pathname === "/api/v1/party/vendor-prospects" ? json(200, { items: [prospect()] }) : undefined);
    renderAt("/party/vendor-prospects");
    await screen.findByText("Lakeshore Machining LLC");
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "NEW" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    await waitFor(() => expect(api.calls.at(-1)?.url.searchParams.get("status")).toBe("NEW"));
  });

  it("checks the accept form before sending and lists what is missing", async () => {
    api.handler = (call) => {
      if (call.url.pathname === `/api/v1/party/vendor-prospects/${ID}`) return json(200, prospect());
      if (call.url.pathname === "/api/v1/party/vendor-questionnaires") return json(200, { items: [{ id: "0190b000-0000-7000-8000-0000000000aa", name: "Onboarding", version: 2, createdAt: "2026-01-01T00:00:00Z" }] });
      return undefined;
    };
    const { container } = renderAt(`/party/vendor-prospects/${ID}`, ["party.read", "party.write", "party.vendor-request.decide"]);
    expect(await screen.findByRole("heading", { name: "Lakeshore Machining LLC" })).toBeInTheDocument();
    expect(await screen.findByRole("option", { name: "Onboarding (version 2)" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Accept and create vendor" }));
    const summary = await screen.findByRole("alert", { name: /problem/i });
    expect(within(summary).getByText(/Vendor account number/)).toBeInTheDocument();
    expect(within(summary).getByText(/Vendor group/)).toBeInTheDocument();
    expect(api.calls.some((c) => c.method === "POST")).toBe(false);
    expect(pageViolations(container)).toEqual([]);
  });

  it("rejects with a note after confirming", async () => {
    let rejected = false;
    api.handler = (call) => {
      if (call.method === "POST" && call.url.pathname === `/api/v1/party/vendor-prospects/${ID}:reject`) {
        rejected = true;
        return json(200, prospect({ status: "REJECTED", decisionNote: "Not a manufacturer", decidedAt: "2026-11-11T09:00:00Z" }));
      }
      if (call.url.pathname === `/api/v1/party/vendor-prospects/${ID}`) return json(200, rejected ? prospect({ status: "REJECTED", decisionNote: "Not a manufacturer", decidedAt: "2026-11-11T09:00:00Z" }) : prospect());
      if (call.url.pathname === "/api/v1/party/vendor-questionnaires") return json(200, { items: [] });
      return undefined;
    };
    renderAt(`/party/vendor-prospects/${ID}`, ["party.read", "party.vendor-request.decide"]);
    expect(await screen.findByText("Accepting a registration needs the party.write duty.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Reject registration" }));
    expect(await screen.findByText(/Enter a note/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Note"), { target: { value: "Not a manufacturer" } });
    fireEvent.click(screen.getByRole("button", { name: "Reject registration" }));
    fireEvent.click(await screen.findByRole("button", { name: "Confirm rejection" }));
    expect(await screen.findByText(/Rejected. Nothing was created/)).toBeInTheDocument();
    const post = api.calls.find((c) => c.method === "POST");
    expect(post?.body).toEqual({ note: "Not a manufacturer" });
    expect(await screen.findByText("Not a manufacturer")).toBeInTheDocument();
  });

  it("shows the decision and the vendor of an accepted registration, and explains a failure", async () => {
    api.handler = (call) => (call.url.pathname === `/api/v1/party/vendor-prospects/${ID}` ? json(200, prospect({ status: "ACCEPTED", vendorId: VENDOR, partyId: PARTY, decidedAt: "2026-11-01T10:00:00Z" })) : undefined);
    renderAt(`/party/vendor-prospects/${ID}`);
    expect(await screen.findByRole("link", { name: "Open the party" })).toHaveAttribute("href", `/party/${PARTY}`);
    expect(screen.queryByRole("button", { name: "Accept and create vendor" })).toBeNull();
  });

  it("explains a registration that cannot be found", async () => {
    api.handler = () => problem(404, "PTY-PROSPECT-NOT-FOUND", "not found");
    renderAt(`/party/vendor-prospects/${ID}`);
    expect(await screen.findByText("Could not load the registration")).toBeInTheDocument();
  });
});
