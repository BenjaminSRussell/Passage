import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Party } from "./api/client";
import { MemoryRouter, Route, Routes, useNavigate } from "react-router-dom";
import { PrincipalProvider } from "../../shell/principal";
import party from "./module";
import { json, pageViolations, problem, renderAt, useFakeApi, type Call } from "./testServer";

const PARTY = "0195f0a0-0000-7000-8000-0000000000aa";
const GROUP = "0195f0a0-0000-7000-8000-0000000000a1";
const TERM = "0195f0a0-0000-7000-8000-0000000000a2";
const PROFILE = "0195f0a0-0000-7000-8000-0000000000a3";
const CUSTOMER = "0195f0a0-0000-7000-8000-0000000000c2";
const CHANGE = "0195f0a0-0000-7000-8000-0000000000d1";

const api = useFakeApi();

const org = (extra: Partial<Party> = {}): Party => ({
  id: PARTY,
  partyNumber: "PTY-000001",
  partyType: "organization",
  name: "Northwind",
  searchName: "northwind",
  status: "active",
  legalName: "Northwind",
  version: 3,
  createdAt: "2026-01-05T09:00:00Z",
  updatedAt: "2026-01-06T10:00:00Z",
  ...extra,
});

const type = (label: string, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } });
const click = (name: string) => fireEvent.click(screen.getByRole("button", { name }));
const posts = () => api.calls.filter((c) => c.method === "POST");

function detail(extra: (call: Call) => ReturnType<typeof json> | undefined = () => undefined) {
  api.handler = (call) => {
    const reply = extra(call);
    if (reply) return reply;
    if (call.url.pathname.endsWith("/vendors")) return json(200, { vendors: [] });
    if (call.method === "GET") return json(200, org());
    return undefined;
  };
}

describe("create party", () => {
  it("creates an organization with an Idempotency-Key and opens it", async () => {
    api.handler = (call) =>
      call.method === "POST" ? json(201, org()) : call.url.pathname.endsWith("/vendors") ? json(200, { vendors: [] }) : json(200, org());
    const { container } = renderAt("/party/new");
    expect(pageViolations(container)).toEqual([]);
    type("Legal name", " Northwind ");
    click("Create party");
    await waitFor(() => expect(screen.getByTestId("where")).toHaveTextContent(`/party/${PARTY}`));
    const post = posts()[0]!;
    expect(post.url.pathname).toBe("/api/v1/party/parties");
    expect(post.body).toEqual({
      partyType: "organization",
      legalName: "Northwind",
    });
    expect(post.headers.get("Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("creates a person with given and family name", async () => {
    api.handler = (call) => (call.method === "POST" ? json(201, org({ partyType: "person" })) : json(200, org()));
    renderAt("/party/new");
    type("Type", "person");
    type("Given name", "Ada");
    type("Family name", "Lovelace");
    click("Create party");
    await waitFor(() => expect(posts()).toHaveLength(1));
    expect(posts()[0]?.body).toEqual({
      partyType: "person",
      givenName: "Ada",
      familyName: "Lovelace",
    });
  });

  it("requires the name before calling the API", async () => {
    renderAt("/party/new");
    click("Create party");
    expect(await screen.findByText("Enter the legal name.")).toBeInTheDocument();
    expect(api.calls).toHaveLength(0);
  });

  it("maps server field errors and shows the problem", async () => {
    api.handler = () =>
      problem(422, "PTY-PARTY-INVALID", "party failed validation", {
        legalName: "legal name is too long",
      });
    renderAt("/party/new");
    type("Legal name", "x");
    click("Create party");
    expect(await screen.findByText("legal name is too long")).toBeInTheDocument();
    expect(screen.getByText(/party failed validation/)).toBeInTheDocument();
  });

  it("reuses the key on an identical retry and issues a new one when the body changes", async () => {
    let fail = true;
    api.handler = () => {
      if (fail) throw new Error("offline");
      return json(201, org());
    };
    renderAt("/party/new");
    type("Legal name", "Northwind");
    click("Create party");
    await screen.findByText(/offline/);
    click("Create party");
    await waitFor(() => expect(posts()).toHaveLength(2));
    expect(posts()[1]?.headers.get("Idempotency-Key")).toBe(posts()[0]?.headers.get("Idempotency-Key"));
    type("Legal name", "Northwind GmbH");
    click("Create party");
    await waitFor(() => expect(posts()).toHaveLength(3));
    expect(posts()[2]?.headers.get("Idempotency-Key")).not.toBe(posts()[0]?.headers.get("Idempotency-Key"));
    fail = false;
  });

  it("is gated on party.write", () => {
    renderAt("/party/new", ["party.read"]);
    expect(screen.queryByRole("button", { name: "Create party" })).not.toBeInTheDocument();
    expect(screen.getByText(/needs party.write/)).toBeInTheDocument();
  });
});

describe("edit party", () => {
  it("sends only changed fields with If-Match and confirms in a status region", async () => {
    detail((call) => (call.method === "PATCH" ? json(200, org({ version: 4 })) : undefined));
    const { container } = renderAt(`/party/${PARTY}`);
    await screen.findByRole("heading", { name: "Northwind" });
    expect(pageViolations(container)).toEqual([]);
    type("Trade name", "NW");
    click("Save changes");
    await waitFor(() => expect(api.calls.some((c) => c.method === "PATCH")).toBe(true));
    const patch = api.calls.find((c) => c.method === "PATCH")!;
    expect(patch.url.pathname).toBe(`/api/v1/party/parties/${PARTY}`);
    expect(patch.headers.get("If-Match")).toBe('"3"');
    expect(patch.body).toEqual({ tradeName: "NW" });
    expect(await screen.findByText("Saved.")).toBeInTheDocument();
  });

  it("requires a change before calling the API", async () => {
    detail();
    renderAt(`/party/${PARTY}`);
    await screen.findByRole("heading", { name: "Northwind" });
    click("Save changes");
    expect(await screen.findByText("Change at least one field before saving.")).toBeInTheDocument();
    expect(api.calls.some((c) => c.method === "PATCH")).toBe(false);
  });

  it("explains a stale version and offers a reload", async () => {
    detail((call) => (call.method === "PATCH" ? problem(409, "PTY-VERSION-CONFLICT", "stale") : undefined));
    renderAt(`/party/${PARTY}`);
    await screen.findByRole("heading", { name: "Northwind" });
    type("Trade name", "NW");
    click("Save changes");
    expect(await screen.findByText(/Someone changed this party after you opened it/)).toBeInTheDocument();
    const before = api.calls.length;
    click("Try again");
    await waitFor(() => expect(api.calls.length).toBeGreaterThan(before));
  });
});

describe("vendor role", () => {
  const fill = () => {
    type("Vendor account number", "V-100");
    type("Vendor group id", GROUP);
    type("Vendor currency", "eur");
  };

  it("posts the role with an Idempotency-Key, refreshes the roles and clears the form", async () => {
    detail((call) => (call.method === "POST" ? json(201, { id: "v1", partyId: PARTY }) : undefined));
    renderAt(`/party/${PARTY}`);
    await screen.findByRole("heading", { name: "Northwind" });
    fill();
    click("Add vendor role");
    expect(await screen.findByText("Vendor role V-100 added.")).toBeInTheDocument();
    const post = posts()[0]!;
    expect(post.url.pathname).toBe(`/api/v1/party/parties/${PARTY}/vendor`);
    expect(post.body).toEqual({
      accountNumber: "V-100",
      vendorGroupId: GROUP,
      currencyCode: "EUR",
    });
    expect(post.headers.get("Idempotency-Key")).toBeTruthy();
    expect(screen.getByLabelText("Vendor account number")).toHaveValue("");
    expect(api.calls.filter((c) => c.url.pathname.endsWith("/vendors")).length).toBeGreaterThan(1);
  });

  it("validates ids and currency before calling the API", async () => {
    detail();
    renderAt(`/party/${PARTY}`);
    await screen.findByRole("heading", { name: "Northwind" });
    click("Add vendor role");
    expect(await screen.findByText("Enter the account number.")).toBeInTheDocument();
    expect(screen.getByText("Enter the vendor group id (a UUID).")).toBeInTheDocument();
    expect(screen.getByText(/three-letter currency code/)).toBeInTheDocument();
    expect(posts()).toHaveLength(0);
  });

  it("sends the optional payment term when given and explains a duplicate role", async () => {
    detail((call) => (call.method === "POST" ? problem(409, "PTY-VENDOR-EXISTS", "exists") : undefined));
    renderAt(`/party/${PARTY}`);
    await screen.findByRole("heading", { name: "Northwind" });
    fill();
    type("Vendor payment term id", TERM);
    click("Add vendor role");
    expect(await screen.findByText(/already has a vendor role here/)).toBeInTheDocument();
    expect((posts()[0]?.body as Record<string, string>).paymentTermId).toBe(TERM);
  });
});

describe("customer role and credit limit", () => {
  async function addCustomer() {
    type("Customer account number", "C-100");
    type("Customer group id", GROUP);
    type("Customer payment term id", TERM);
    type("Posting profile id", PROFILE);
    type("Customer currency", "usd");
    click("Add customer role");
  }

  it("creates the role and carries its id into the credit-limit form", async () => {
    detail((call) => (call.method === "POST" ? json(201, { id: CUSTOMER, partyId: PARTY, accountNumber: "C-100" }) : undefined));
    renderAt(`/party/${PARTY}`);
    await screen.findByRole("heading", { name: "Northwind" });
    await addCustomer();
    expect(await screen.findByText(/Customer role C-100 added/)).toBeInTheDocument();
    const post = posts()[0]!;
    expect(post.url.pathname).toBe(`/api/v1/party/parties/${PARTY}/customer`);
    expect(post.body).toEqual({
      accountNumber: "C-100",
      customerGroupId: GROUP,
      paymentTermId: TERM,
      postingProfileId: PROFILE,
      currencyCode: "USD",
    });
    expect(post.headers.get("Idempotency-Key")).toBeTruthy();
    await waitFor(() => expect(screen.getByLabelText("Customer role id")).toHaveValue(CUSTOMER));
  });

  it("validates the customer role before calling the API", async () => {
    detail();
    renderAt(`/party/${PARTY}`);
    await screen.findByRole("heading", { name: "Northwind" });
    click("Add customer role");
    expect(await screen.findByText("Enter the posting profile id (a UUID).")).toBeInTheDocument();
    expect(posts()).toHaveLength(0);
  });

  it("applies a decrease at once", async () => {
    detail((call) => (call.method === "POST" ? json(200, { creditLimit: "5000.00" }) : undefined));
    renderAt(`/party/${PARTY}`);
    await screen.findByRole("heading", { name: "Northwind" });
    type("Customer role id", CUSTOMER);
    type("Credit limit", "5000.00");
    click("Set credit limit");
    expect(await screen.findByText("The stored credit limit is 5000.00.")).toBeInTheDocument();
    expect(posts()[0]?.url.pathname).toBe(`/api/v1/party/customers/${CUSTOMER}/credit-limit`);
    expect(posts()[0]?.body).toEqual({ creditLimit: "5000.00" });
  });

  it("shows the held increase on 202 and links to the pending change", async () => {
    detail((call) =>
      call.method === "POST"
        ? json(202, {
            creditLimit: "10000.00",
            pendingChange: {
              id: CHANGE,
              customerId: CUSTOMER,
              legalEntityId: "le",
              field: "credit_limit",
              currentValue: "10000.00",
              requestedValue: "25000.00",
              status: "pending",
              requestedBy: "ada",
              requestedAt: "2026-03-01T09:00:00Z",
            },
          })
        : undefined,
    );
    renderAt(`/party/${PARTY}`);
    await screen.findByRole("heading", { name: "Northwind" });
    type("Customer role id", CUSTOMER);
    type("Credit limit", "25000.00");
    click("Set credit limit");
    const link = await screen.findByRole("link", {
      name: "Open the pending change",
    });
    expect(link).toHaveAttribute("href", `/party/pending-changes/${CHANGE}`);
    expect(screen.getByText(/until 25000.00 is approved/)).toBeInTheDocument();
  });

  it("validates the amount and the customer id", async () => {
    detail();
    renderAt(`/party/${PARTY}`);
    await screen.findByRole("heading", { name: "Northwind" });
    type("Customer role id", "nope");
    type("Credit limit", "1.234");
    click("Set credit limit");
    expect(await screen.findByText("Enter the customer role id (a UUID).")).toBeInTheDocument();
    expect(screen.getByText(/at most 2 decimals/)).toBeInTheDocument();
    expect(posts()).toHaveLength(0);
  });

  it("links to the open change when one is already pending", async () => {
    detail((call) =>
      call.method === "POST"
        ? problem(409, "PTY-CHANGE-PENDING", "pending", {
            pendingChangeId: CHANGE,
          })
        : undefined,
    );
    renderAt(`/party/${PARTY}`);
    await screen.findByRole("heading", { name: "Northwind" });
    type("Customer role id", CUSTOMER);
    type("Credit limit", "9.00");
    click("Set credit limit");
    expect(await screen.findByRole("link", { name: "Open the pending change" })).toHaveAttribute(
      "href",
      `/party/pending-changes/${CHANGE}`,
    );
  });
});

describe("write gating", () => {
  it("shows no write forms without party.write and never offers merge", async () => {
    detail();
    renderAt(`/party/${PARTY}`, ["party.read"]);
    await screen.findByRole("heading", { name: "Northwind" });
    for (const name of ["Save changes", "Add vendor role", "Add customer role", "Set credit limit"]) {
      expect(screen.queryByRole("button", { name })).not.toBeInTheDocument();
    }
    expect(screen.queryByRole("button", { name: /merge/i })).not.toBeInTheDocument();
    expect(api.calls.every((c) => c.method === "GET")).toBe(true);
  });
});

describe("edit conflict rebase", () => {
  it("keeps only the user's edits after a version conflict and drops the retry alert", async () => {
    let server = org({ tradeName: "Old" });
    api.handler = (call) => {
      if (call.url.pathname.endsWith("/vendors")) return json(200, { vendors: [] });
      if (call.method === "PATCH")
        return server.version === 3 ? problem(409, "PTY-VERSION-CONFLICT", "stale") : json(200, org({ version: 5 }));
      return json(200, server);
    };
    renderAt(`/party/${PARTY}`);
    await screen.findByRole("heading", { name: "Northwind" });
    type("Legal name", "Northwind GmbH");
    click("Save changes");
    await screen.findByText(/Someone changed this party after you opened it/);
    server = org({ version: 4, tradeName: "New" });
    click("Try again");
    await waitFor(() => expect(screen.getByLabelText("Trade name")).toHaveValue("New"));
    expect(screen.queryByText(/Someone changed this party after you opened it/)).not.toBeInTheDocument();
    expect(screen.getByLabelText("Legal name")).toHaveValue("Northwind GmbH");
    click("Save changes");
    await waitFor(() => expect(api.calls.filter((c) => c.method === "PATCH")).toHaveLength(2));
    const retry = api.calls.filter((c) => c.method === "PATCH")[1]!;
    expect(retry.headers.get("If-Match")).toBe('"4"');
    expect(retry.body).toEqual({ legalName: "Northwind GmbH" });
  });
});

describe("idempotency key follows the sent body", () => {
  it("reuses the key when only whitespace changes", async () => {
    let fail = true;
    detail((call) => {
      if (call.method !== "POST") return undefined;
      if (fail) throw new Error("offline");
      return json(201, { id: "v1", partyId: PARTY });
    });
    renderAt(`/party/${PARTY}`);
    await screen.findByRole("heading", { name: "Northwind" });
    type("Vendor account number", "V-100");
    type("Vendor group id", GROUP);
    type("Vendor currency", "EUR");
    click("Add vendor role");
    await screen.findByText(/offline/);
    fail = false;
    type("Vendor account number", "V-100 ");
    click("Add vendor role");
    await waitFor(() => expect(posts()).toHaveLength(2));
    expect(posts()[1]?.headers.get("Idempotency-Key")).toBe(posts()[0]?.headers.get("Idempotency-Key"));
  });
});

describe("focus and navigation", () => {
  it("focuses the first invalid field on a failed submit", async () => {
    detail();
    renderAt(`/party/${PARTY}`);
    await screen.findByRole("heading", { name: "Northwind" });
    click("Add vendor role");
    await waitFor(() => expect(screen.getByLabelText("Vendor account number")).toHaveFocus());
  });

  it("does not carry a new customer role id to the next party", async () => {
    const NEXT = "0195f0a0-0000-7000-8000-0000000000bb";
    api.handler = (call) => {
      if (call.url.pathname.endsWith("/vendors")) return json(200, { vendors: [] });
      if (call.method === "POST") return json(201, { id: CUSTOMER, partyId: PARTY, accountNumber: "C-100" });
      return json(200, org({ id: call.url.pathname.endsWith(NEXT) ? NEXT : PARTY }));
    };
    function Jump() {
      const navigate = useNavigate();
      return <button onClick={() => navigate(`/party/${NEXT}`)}>Go next</button>;
    }
    render(
      <PrincipalProvider duties={["party.read", "party.write"]}>
        <MemoryRouter initialEntries={[`/party/${PARTY}`]}>
          <Routes>
            {party.routes.map((route) => (
              <Route key={route.path} path={route.path} element={route.element} />
            ))}
          </Routes>
          <Jump />
        </MemoryRouter>
      </PrincipalProvider>,
    );
    await screen.findByRole("heading", { name: "Northwind" });
    type("Customer account number", "C-100");
    type("Customer group id", GROUP);
    type("Customer payment term id", TERM);
    type("Posting profile id", PROFILE);
    type("Customer currency", "usd");
    click("Add customer role");
    await waitFor(() => expect(screen.getByLabelText("Customer role id")).toHaveValue(CUSTOMER));
    click("Go next");
    await waitFor(() => expect(api.calls.some((c) => c.url.pathname.endsWith(NEXT))).toBe(true));
    await screen.findByLabelText("Customer role id");
    await waitFor(() => expect(screen.getByLabelText("Customer role id")).toHaveValue(""));
  });
});

describe("same-field edit conflict", () => {
  it("keeps the user's value, warns with the other value and focuses the field before the second save", async () => {
    let server = org();
    api.handler = (call) => {
      if (call.url.pathname.endsWith("/vendors")) return json(200, { vendors: [] });
      if (call.method === "PATCH")
        return server.version === 3 ? problem(409, "PTY-VERSION-CONFLICT", "stale") : json(200, org({ version: 5 }));
      return json(200, server);
    };
    renderAt(`/party/${PARTY}`);
    await screen.findByRole("heading", { name: "Northwind" });
    type("Legal name", "Northwind GmbH");
    click("Save changes");
    await screen.findByText(/Someone changed this party after you opened it/);
    server = org({ version: 4, legalName: "Northwind AG" });
    click("Try again");
    expect(await screen.findByText(/Changed by someone else to \u201cNorthwind AG\u201d\. Saving replaces it\./)).toBeInTheDocument();
    expect(screen.getByLabelText("Legal name")).toHaveValue("Northwind GmbH");
    expect(screen.getByText(/Your values are kept/)).toBeInTheDocument();
    await waitFor(() => expect(screen.getByLabelText("Legal name")).toHaveFocus());
    expect(api.calls.filter((c) => c.method === "PATCH")).toHaveLength(1);
    click("Save changes");
    await waitFor(() => expect(api.calls.filter((c) => c.method === "PATCH")).toHaveLength(2));
    expect(api.calls.filter((c) => c.method === "PATCH")[1]?.body).toEqual({ legalName: "Northwind GmbH" });
  });

  it("drops the warning once the user takes the other value", async () => {
    let server = org();
    api.handler = (call) => {
      if (call.url.pathname.endsWith("/vendors")) return json(200, { vendors: [] });
      if (call.method === "PATCH") return problem(409, "PTY-VERSION-CONFLICT", "stale");
      return json(200, server);
    };
    renderAt(`/party/${PARTY}`);
    await screen.findByRole("heading", { name: "Northwind" });
    type("Legal name", "Northwind GmbH");
    click("Save changes");
    await screen.findByText(/Someone changed this party after you opened it/);
    server = org({ version: 4, legalName: "Northwind AG" });
    click("Try again");
    expect(await screen.findByText(/Changed by someone else to/)).toBeInTheDocument();
    type("Legal name", "Northwind AG");
    expect(screen.queryByText(/Changed by someone else to/)).not.toBeInTheDocument();
    type("Legal name", "Northwind SE");
    expect(screen.getByText(/Changed by someone else to \u201cNorthwind AG\u201d/)).toBeInTheDocument();
  });
});
