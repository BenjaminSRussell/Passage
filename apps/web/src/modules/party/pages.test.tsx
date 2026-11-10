import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useNavigate } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { PrincipalProvider } from "../../shell/principal";
import party from "./module";
import { findTable } from "../../test-utils";
import type { Party, PendingChange, Vendor } from "./api/client";
import { json, pageViolations, problem, renderAt, useFakeApi } from "./testServer";

const PARTY = "0195f0a0-0000-7000-8000-0000000000aa";
const OTHER = "0195f0a0-0000-7000-8000-0000000000bb";
const VENDOR = "0195f0a0-0000-7000-8000-0000000000c1";
const CHANGE = "0195f0a0-0000-7000-8000-0000000000d1";
const LE = "0195f0a0-0000-7000-8000-0000000000e1";

const api = useFakeApi();

const org = (id: string, name: string, extra: Partial<Party> = {}): Party => ({
  id,
  partyNumber: id === PARTY ? "PTY-000001" : "PTY-000002",
  partyType: "organization",
  name,
  searchName: name.toLowerCase(),
  status: "active",
  legalName: name,
  registrationNumber: id === PARTY ? "HRB-1" : "HRB-2",
  version: 1,
  createdAt: "2026-01-05T09:00:00Z",
  updatedAt: "2026-01-06T10:00:00Z",
  ...extra,
});

const vendor = (partyId: string): Vendor => ({
  id: VENDOR,
  partyId,
  legalEntityId: LE,
  accountNumber: "V-100",
  vendorGroupId: "0195f0a0-0000-7000-8000-0000000000g1",
  currencyCode: "EUR",
  blocked: "none",
  version: 1,
});

const change = (extra: Partial<PendingChange> = {}): PendingChange => ({
  id: CHANGE,
  customerId: "0195f0a0-0000-7000-8000-0000000000f1",
  legalEntityId: LE,
  field: "credit_limit",
  currentValue: "10000.00",
  requestedValue: "25000.00",
  status: "pending",
  requestedBy: "ada",
  requestedAt: "2026-03-01T09:00:00Z",
  ...extra,
});

function noMergeControls() {
  expect(screen.queryByRole("button", { name: /merge/i })).not.toBeInTheDocument();
  expect(api.calls.some((c) => c.url.pathname.includes(":merge"))).toBe(false);
}

describe("party search", () => {
  it("looks up a party by id, opens it, and passes the page checks", async () => {
    api.handler = (call) => (call.url.pathname.endsWith(`/parties/${PARTY}`) ? json(200, org(PARTY, "Northwind")) : undefined);
    const { container } = renderAt("/party");
    fireEvent.change(screen.getByLabelText("Party"), {
      target: { value: PARTY },
    });
    fireEvent.click(screen.getByRole("button", { name: "Find party" }));
    const table = await findTable({ name: "Matching party" });
    expect(within(table).getByText("Northwind")).toBeInTheDocument();
    expect(within(table).getByText("Organization")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: "PTY-000001" }));
    expect(screen.getByTestId("where")).toHaveTextContent(`/party/${PARTY}`);
    noMergeControls();
  });

  it("validates the id before calling the API", async () => {
    renderAt("/party");
    fireEvent.change(screen.getByLabelText("Party"), {
      target: { value: "x" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Find party" }));
    expect(await screen.findByText("Enter the party id (a UUID).")).toBeInTheDocument();
    expect(api.calls).toHaveLength(0);
  });

  it("shows a load failure and recovers on Try again", async () => {
    let failing = true;
    api.handler = () => (failing ? problem(500, "COM-0500", "boom") : json(200, org(PARTY, "Northwind")));
    renderAt(`/party?id=${PARTY}`);
    expect(await screen.findByText(/boom \(COM-0500\)/)).toBeInTheDocument();
    failing = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("Northwind")).toBeInTheDocument();
  });

  it("reports a network failure as COM-0001", async () => {
    api.handler = () => {
      throw new Error("offline");
    };
    renderAt(`/party?id=${PARTY}`);
    expect(await screen.findByText(/offline \(COM-0001\)/)).toBeInTheDocument();
  });

  it("explains a missing party", async () => {
    api.handler = () => problem(404, "PTY-PARTY-NOT-FOUND", "party not found");
    renderAt(`/party?id=${PARTY}`);
    expect(await screen.findByText(/No party exists with that id/)).toBeInTheDocument();
  });
});

describe("party detail", () => {
  it("shows the party, its vendor roles, and no merge button", async () => {
    api.handler = (call) => {
      if (call.url.pathname.endsWith(`/parties/${PARTY}/vendors`)) return json(200, { vendors: [vendor(PARTY)] });
      if (call.url.pathname.endsWith(`/parties/${PARTY}`)) return json(200, org(PARTY, "Northwind"));
      return undefined;
    };
    const { container } = renderAt(`/party/${PARTY}`);
    expect(await screen.findByRole("heading", { name: "Northwind" })).toBeInTheDocument();
    expect(screen.getByText("PTY-000001")).toBeInTheDocument();
    const roles = await findTable({ name: "Vendor roles" });
    expect(within(roles).getByText("V-100")).toBeInTheDocument();
    expect(within(roles).getByText("EUR")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    noMergeControls();
  });

  it("warns and links to the survivor of a merged party", async () => {
    api.handler = (call) =>
      call.url.pathname.endsWith("/vendors")
        ? json(200, { vendors: [] })
        : json(
            200,
            org(PARTY, "Old Northwind", {
              status: "merged",
              mergedIntoId: OTHER,
            }),
          );
    renderAt(`/party/${PARTY}`);
    expect(await screen.findByText("Merged party")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: OTHER })).toHaveAttribute("href", `/party/${OTHER}`);
    expect(screen.queryByRole("button", { name: "Save changes" })).not.toBeInTheDocument();
  });

  it("notes when the opened id redirected to a survivor", async () => {
    api.handler = (call) => {
      if (call.url.pathname.endsWith("/vendors")) return json(200, { vendors: [] });
      return json(200, org(PARTY, "Northwind", { redirected: true, requestedId: OTHER }));
    };
    renderAt(`/party/${OTHER}`);
    expect(await screen.findByText(/This id was merged/)).toBeInTheDocument();
    expect(screen.getByText(/surviving party PTY-000001/)).toBeInTheDocument();
    noMergeControls();
  });
});

describe("tax registrations", () => {
  it("posts a VAT number and shows the validation status", async () => {
    api.handler = (call) =>
      call.method === "POST" && call.url.pathname.endsWith("/tax-registrations:validate")
        ? json(200, {
            type: "VAT",
            country: "DE",
            number: "DE123456789",
            validationStatus: "valid",
          })
        : undefined;
    const { container } = renderAt("/party/tax-registrations");
    fireEvent.change(screen.getByLabelText("Country"), {
      target: { value: "de" },
    });
    fireEvent.change(screen.getByLabelText("Number"), {
      target: { value: "DE123456789" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Validate" }));
    expect(await screen.findByText("DE123456789")).toBeInTheDocument();
    expect(screen.getByText("Valid")).toBeInTheDocument();
    expect(api.calls[0]?.body).toEqual({
      type: "VAT",
      number: "DE123456789",
      country: "DE",
    });
    expect(pageViolations(container)).toEqual([]);
  });

  it("requires a country for VAT before calling the API", async () => {
    renderAt("/party/tax-registrations");
    fireEvent.change(screen.getByLabelText("Number"), {
      target: { value: "DE1" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Validate" }));
    expect(await screen.findByText("Enter the two-letter country code, for example DE.")).toBeInTheDocument();
    expect(api.calls).toHaveLength(0);
  });

  it("maps server field errors onto the form", async () => {
    api.handler = () =>
      problem(422, "PTY-0001", "tax registration failed validation", {
        number: "an EIN has 9 digits",
      });
    renderAt("/party/tax-registrations");
    fireEvent.change(screen.getByLabelText("Type"), {
      target: { value: "EIN" },
    });
    fireEvent.change(screen.getByLabelText("Number"), {
      target: { value: "12" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Validate" }));
    expect(await screen.findByText("an EIN has 9 digits")).toBeInTheDocument();
  });

  it("shows a server error keyed to the type field", async () => {
    api.handler = () => problem(422, "PTY-0001", "invalid", { type: "type is not supported" });
    renderAt("/party/tax-registrations");
    fireEvent.change(screen.getByLabelText("Country"), {
      target: { value: "de" },
    });
    fireEvent.change(screen.getByLabelText("Number"), {
      target: { value: "x" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Validate" }));
    expect(await screen.findByText("type is not supported")).toBeInTheDocument();
  });

  it("announces the result through a persistent status region", async () => {
    api.handler = () =>
      json(200, {
        type: "VAT",
        country: "DE",
        number: "DE1",
        validationStatus: "valid",
      });
    renderAt("/party/tax-registrations");
    const region = screen.getByRole("status");
    expect(region).toBeEmptyDOMElement();
    fireEvent.change(screen.getByLabelText("Country"), {
      target: { value: "de" },
    });
    fireEvent.change(screen.getByLabelText("Number"), {
      target: { value: "DE1" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Validate" }));
    expect(await within(region).findByText("Valid")).toBeInTheDocument();
  });

  it("hides the validate action without party.write", async () => {
    renderAt("/party/tax-registrations", ["party.read"]);
    expect(screen.queryByRole("button", { name: "Validate" })).not.toBeInTheDocument();
    expect(screen.getByText(/needs party.write/)).toBeInTheDocument();
  });
});

describe("duplicate candidates", () => {
  it("compares two parties and does not offer merge", async () => {
    api.handler = (call) => {
      if (call.url.pathname.endsWith(`/parties/${PARTY}`)) return json(200, org(PARTY, "Northwind"));
      if (call.url.pathname.endsWith(`/parties/${OTHER}`)) return json(200, org(OTHER, "North Wind GmbH"));
      return undefined;
    };
    const { container } = renderAt("/party/duplicates");
    fireEvent.change(screen.getByLabelText("First party"), {
      target: { value: PARTY },
    });
    fireEvent.change(screen.getByLabelText("Second party"), {
      target: { value: OTHER },
    });
    fireEvent.click(screen.getByRole("button", { name: "Compare" }));
    const table = await findTable({ name: "Field comparison" });
    expect(within(table).getAllByText("Northwind").length).toBeGreaterThan(0);
    expect(within(table).getAllByText("North Wind GmbH").length).toBeGreaterThan(0);
    expect(within(table).getAllByText("Different").length).toBeGreaterThan(0);
    expect(pageViolations(container)).toEqual([]);
    noMergeControls();
  });

  it("marks identical fields as Same", async () => {
    api.handler = (call) => {
      if (call.url.pathname.endsWith(`/parties/${PARTY}`)) return json(200, org(PARTY, "Northwind"));
      if (call.url.pathname.endsWith(`/parties/${OTHER}`)) return json(200, org(OTHER, "Northwind"));
      return undefined;
    };
    renderAt(`/party/duplicates?a=${PARTY}&b=${OTHER}`);
    const table = await findTable({ name: "Field comparison" });
    expect(within(table).getAllByText("Same").length).toBeGreaterThan(0);
    expect(within(table).getAllByText("Different").length).toBeGreaterThan(0);
  });

  it("states that merge is ungranted", () => {
    renderAt("/party/duplicates");
    expect(screen.getByText(/merge is ungranted until its approval workflow \(#450\)/)).toBeInTheDocument();
  });

  it("rejects comparing a party with itself", async () => {
    renderAt("/party/duplicates");
    fireEvent.change(screen.getByLabelText("First party"), {
      target: { value: PARTY },
    });
    fireEvent.change(screen.getByLabelText("Second party"), {
      target: { value: PARTY },
    });
    fireEvent.click(screen.getByRole("button", { name: "Compare" }));
    expect(await screen.findByText("Enter a different party id to compare.")).toBeInTheDocument();
    expect(api.calls).toHaveLength(0);
  });
});

describe("credit limit approvals", () => {
  it("opens a pending change by id", async () => {
    renderAt("/party/pending-changes");
    fireEvent.change(screen.getByLabelText("Pending change id"), {
      target: { value: CHANGE },
    });
    fireEvent.click(screen.getByRole("button", { name: "Open change" }));
    expect(screen.getByTestId("where")).toHaveTextContent(`/party/pending-changes/${CHANGE}`);
  });

  it("shows the held increase and approves it", async () => {
    api.handler = (call) =>
      call.method === "POST"
        ? json(
            200,
            change({
              status: "approved",
              decidedBy: "grace",
              decidedAt: "2026-03-02T09:00:00Z",
            }),
          )
        : json(200, change());
    const { container } = renderAt(`/party/pending-changes/${CHANGE}`);
    expect(await screen.findByText("25000.00")).toBeInTheDocument();
    expect(screen.getByText("10000.00")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Reason"), {
      target: { value: "reviewed statements" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Approve" }));
    expect(api.calls.some((c) => c.method === "POST")).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: "Confirm approval" }));
    expect(await screen.findByText(/The stored credit limit is now the requested value/)).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveFocus();
    const post = api.calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe(`/api/v1/party/pending-changes/${CHANGE}:approve`);
    expect(post?.body).toEqual({ reason: "reviewed statements" });
  });

  it("rejects with an empty body when no reason is given", async () => {
    api.handler = (call) => (call.method === "POST" ? json(200, change({ status: "rejected" })) : json(200, change()));
    renderAt(`/party/pending-changes/${CHANGE}`);
    await screen.findByText("25000.00");
    fireEvent.click(screen.getByRole("button", { name: "Reject" }));
    fireEvent.click(screen.getByRole("button", { name: "Confirm rejection" }));
    expect(await screen.findByText(/The stored credit limit is unchanged/)).toBeInTheDocument();
    expect(api.calls.find((c) => c.method === "POST")?.body).toBeUndefined();
    expect(api.calls.find((c) => c.method === "POST")?.url.pathname).toContain(":reject");
  });

  it("hides decide actions without party.approve", async () => {
    api.handler = () => json(200, change());
    renderAt(`/party/pending-changes/${CHANGE}`, ["party.read"]);
    await screen.findByText("25000.00");
    expect(screen.queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();
    expect(screen.getByText(/needs the party.approve duty/)).toBeInTheDocument();
  });

  it("explains a self-approval refusal", async () => {
    api.handler = (call) =>
      call.method === "POST" ? problem(403, "PTY-PENDING-SELF-APPROVAL", "requester cannot decide") : json(200, change());
    renderAt(`/party/pending-changes/${CHANGE}`);
    await screen.findByText("25000.00");
    fireEvent.click(screen.getByRole("button", { name: "Approve" }));
    fireEvent.click(screen.getByRole("button", { name: "Confirm approval" }));
    expect(await screen.findByText(/You cannot approve or reject a change you requested/)).toBeInTheDocument();
  });

  it("does not decide when the confirmation is cancelled", async () => {
    api.handler = () => json(200, change());
    renderAt(`/party/pending-changes/${CHANGE}`);
    await screen.findByText("25000.00");
    fireEvent.click(screen.getByRole("button", { name: "Reject" }));
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    expect(api.calls.some((c) => c.method === "POST")).toBe(false);
  });

  it.each([
    ["PTY-PENDING-STALE", /can only be rejected/],
    ["PTY-CHANGE-DECIDED", /already decided/],
  ])("explains %s", async (code, text) => {
    api.handler = (call) => (call.method === "POST" ? problem(409, code, "conflict") : json(200, change()));
    renderAt(`/party/pending-changes/${CHANGE}`);
    await screen.findByText("25000.00");
    fireEvent.click(screen.getByRole("button", { name: "Approve" }));
    fireEvent.click(screen.getByRole("button", { name: "Confirm approval" }));
    expect(await screen.findByText(text)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Approve" })).toBeInTheDocument();
  });

  it("does not carry a decision outcome or reason to the next change", async () => {
    const NEXT = "0195f0a0-0000-7000-8000-0000000000d2";
    api.handler = (call) => {
      if (call.method === "POST") return json(200, change({ status: "approved" }));
      return call.url.pathname.endsWith(NEXT) ? json(200, change({ id: NEXT, requestedValue: "30000.00" })) : json(200, change());
    };
    function Jump() {
      const navigate = useNavigate();
      return <button onClick={() => navigate(`/party/pending-changes/${NEXT}`)}>Go next</button>;
    }
    render(
      <PrincipalProvider duties={["party.read", "party.approve"]}>
        <MemoryRouter initialEntries={[`/party/pending-changes/${CHANGE}`]}>
          <Routes>
            {party.routes.map((route) => (
              <Route key={route.path} path={route.path} element={route.element} />
            ))}
          </Routes>
          <Jump />
        </MemoryRouter>
      </PrincipalProvider>,
    );
    await screen.findByText("25000.00");
    fireEvent.click(screen.getByRole("button", { name: "Approve" }));
    fireEvent.click(screen.getByRole("button", { name: "Confirm approval" }));
    expect(await screen.findByText(/^Approved\./)).toBeInTheDocument();
    await waitFor(() => expect(screen.getByLabelText("Reason")).toBeInTheDocument());
    fireEvent.change(screen.getByLabelText("Reason"), { target: { value: "draft for the first change" } });
    fireEvent.click(screen.getByRole("button", { name: "Go next" }));
    expect(await screen.findByText("30000.00")).toBeInTheDocument();
    expect(screen.queryByText(/^Approved\./)).not.toBeInTheDocument();
    expect(screen.getByLabelText("Reason")).toHaveValue("");
  });
});
