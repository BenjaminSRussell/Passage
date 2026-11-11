import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createElement } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { clearResolvedOptions, setLookupFetcher } from "../../../shell/lookup/entities";
import { PrincipalProvider } from "../../../shell/principal";
import { json, pageViolations, renderRoutes, server, useFakeServer, type Reply } from "../../../shell/testing/fakeServer";
import admin from "../module";

useFakeServer();

const DUTY = "identity.customer-contact.maintain";
const CONTACT = "11111111-1111-4111-8111-111111111101";
const DISABLED = "11111111-1111-4111-8111-111111111102";
const CUSTOMER = "22222222-2222-4222-8222-222222222201";
const OTHER_CUSTOMER = "22222222-2222-4222-8222-222222222202";
const LE_US = "33333333-3333-4333-8333-333333333301";
const LE_DE = "33333333-3333-4333-8333-333333333302";

const entities = {
  items: [
    { legalEntityId: LE_US, code: "US", displayName: "Cedar Ridge Pumps, Inc.", status: "active", createdAt: "2027-01-01T00:00:00Z" },
    { legalEntityId: LE_DE, code: "DE", displayName: "Cedar Ridge Pumps Europe GmbH", status: "active", createdAt: "2027-01-01T00:00:00Z" },
  ],
};
const contact = { contactId: CONTACT, email: "dana@bluewater.example", displayName: "Dana Ortiz", customerId: CUSTOMER, legalEntityIds: [LE_US], status: "enabled", invitationSent: true };
const disabled = { ...contact, contactId: DISABLED, email: "gone@bluewater.example", displayName: "", status: "disabled" };

function lookups(url: URL): Reply | undefined {
  if (url.pathname === "/api/v1/legal-entities") return json(200, entities);
  if (url.pathname === "/api/v1/party/customers:search")
    return json(200, { items: [{ id: CUSTOMER, partyId: "p1", name: "Bluewater Municipal Utilities", accountNumber: "C-10021" }, { id: OTHER_CUSTOMER, partyId: "p2", name: "Harlan Co-op", accountNumber: "C-10048" }] });
  if (url.pathname === `/api/v1/party/customers/${CUSTOMER}`) return json(200, { id: CUSTOMER, partyId: "p1", name: "Bluewater Municipal Utilities", accountNumber: "C-10021" });
  if (url.pathname === "/api/v1/party/parties/p1") return json(200, { id: "p1", name: "Bluewater Municipal Utilities" });
  if (url.pathname.startsWith("/api/v1/org/legal-entities/")) {
    const id = url.pathname.split("/").pop();
    return json(200, { id, code: id === LE_US ? "US" : "DE", name: id === LE_US ? "Cedar Ridge Pumps, Inc." : "Cedar Ridge Pumps Europe GmbH" });
  }
  return undefined;
}

function serve(extra: (url: URL, method: string) => Reply | undefined = () => undefined, list: unknown[] = [contact, disabled]) {
  server.handler = (url, init) => {
    const method = init.method ?? "GET";
    const mine = extra(url, method);
    if (mine) return mine;
    if (url.pathname === "/api/v1/identity/customer-contacts" && method === "GET") return json(200, { items: list });
    return lookups(url);
  };
}

// The entity lookups (names for ids, the customer search) use their own fetcher, answered from the same fakes.
afterEach(() => setLookupFetcher(null));

function renderAt(duties: string[] = [DUTY]) {
  clearResolvedOptions();
  setLookupFetcher(async (url) => {
    const reply = lookups(new URL(url, "http://localhost"));
    return new Response(JSON.stringify(reply?.body ?? {}), { status: reply?.status ?? 404, headers: { "Content-Type": "application/json" } });
  });
  return renderRoutes(
    admin.routes.map((route) => ({ path: route.path, element: createElement(PrincipalProvider, { duties, children: route.element }) })),
    "/admin/security/customer-contacts",
  );
}

async function pickCustomer(name: string) {
  const user = userEvent.setup();
  await user.type(await screen.findByRole("combobox", { name: /^Customer/ }), name.slice(0, 4));
  await user.click(await screen.findByRole("option", { name: new RegExp(name) }));
}

describe("customer portal contacts", () => {
  it("lists the contacts with customer and company names, never ids", async () => {
    serve();
    const { container } = renderAt();
    expect(await screen.findByText("Dana Ortiz (dana@bluewater.example)")).toBeInTheDocument();

    expect((await screen.findAllByText(/Bluewater Municipal Utilities/)).length).toBeGreaterThan(0);
    expect(await screen.findAllByText("Cedar Ridge Pumps, Inc.")).not.toHaveLength(0);
    expect(screen.getByText("gone@bluewater.example")).toBeInTheDocument();
    for (const id of [CONTACT, CUSTOMER, LE_US]) expect(container.textContent).not.toContain(id);
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows no form or actions without the duty, and says when the list is empty", async () => {
    serve(() => undefined, []);
    renderAt([]);
    expect(await screen.findByText("No contacts yet")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Send invitation" })).toBeNull();
  });

  it("checks the form before anything is sent", async () => {
    serve();
    renderAt();
    fireEvent.click(await screen.findByRole("button", { name: "Send invitation" }));
    expect(await screen.findByText("Enter the contact's email address.")).toBeInTheDocument();
    expect(screen.getByText("Choose the customer this contact orders for.")).toBeInTheDocument();
    expect(screen.getByText("Choose at least one company the contact may deal with.")).toBeInTheDocument();
    expect(server.calls.some((c) => c.method === "POST")).toBe(false);
  });

  it("invites a contact for a customer and companies picked by name", async () => {
    serve((url, method) => (method === "POST" && url.pathname === "/api/v1/identity/customer-contacts" ? json(201, { ...contact, email: "new@bluewater.example", displayName: "New Buyer" }) : undefined));
    const { container } = renderAt();
    fireEvent.change(await screen.findByLabelText("Email address"), { target: { value: " new@bluewater.example " } });
    fireEvent.change(screen.getByLabelText("First name"), { target: { value: "New" } });
    fireEvent.change(screen.getByLabelText("Last name"), { target: { value: "Buyer" } });
    await pickCustomer("Bluewater");
    fireEvent.click(await screen.findByLabelText("Cedar Ridge Pumps, Inc."));
    fireEvent.click(screen.getByLabelText("Cedar Ridge Pumps Europe GmbH"));
    fireEvent.click(screen.getByRole("button", { name: "Send invitation" }));
    expect(await screen.findByText("Invitation sent to new@bluewater.example")).toBeInTheDocument();
    expect(server.calls.find((c) => c.method === "POST")?.body).toEqual({ email: "new@bluewater.example", givenName: "New", familyName: "Buyer", customerId: CUSTOMER, legalEntityIds: [LE_US, LE_DE] });
    expect(pageViolations(container)).toEqual([]);
  });

  it("says so when the invitation email could not be sent", async () => {
    serve((_url, method) => (method === "POST" ? json(201, { ...contact, invitationSent: false }) : undefined));
    renderAt();
    fireEvent.change(await screen.findByLabelText("Email address"), { target: { value: "a@b.example" } });
    await pickCustomer("Bluewater");
    fireEvent.click(await screen.findByLabelText("Cedar Ridge Pumps, Inc."));
    fireEvent.click(screen.getByRole("button", { name: "Send invitation" }));
    expect(await screen.findByText(/invitation email could not be sent/)).toBeInTheDocument();
  });

  it("reports a duplicate address and a sign-in service that is off", async () => {
    let answer: Reply = json(409, { code: "IDN-CONTACT-EXISTS", detail: "a customer contact with this email address already exists" });
    serve((_url, method) => (method === "POST" ? answer : undefined));
    renderAt();
    fireEvent.change(await screen.findByLabelText("Email address"), { target: { value: "dana@bluewater.example" } });
    await pickCustomer("Bluewater");
    fireEvent.click(await screen.findByLabelText("Cedar Ridge Pumps, Inc."));
    fireEvent.click(screen.getByRole("button", { name: "Send invitation" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("already exists");
    answer = json(503, { code: "IDN-CONTACT-KEYCLOAK-OFF", detail: "Keycloak administration is not configured for identity" });
    fireEvent.click(screen.getByRole("button", { name: "Send invitation" }));
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("not configured"));
  });

  it("changes the customer and companies of a contact", async () => {
    serve((url, method) => (method === "PATCH" && url.pathname === `/api/v1/identity/customer-contacts/${CONTACT}` ? json(200, { ...contact, customerId: OTHER_CUSTOMER, legalEntityIds: [LE_US, LE_DE] }) : undefined));
    renderAt();
    fireEvent.click(await screen.findByRole("button", { name: "Change dana@bluewater.example" }));
    expect(await screen.findByRole("heading", { name: "Change dana@bluewater.example" })).toBeInTheDocument();
    expect(screen.queryByLabelText("Email address")).toBeNull();
    expect(await screen.findByLabelText("Cedar Ridge Pumps, Inc.")).toBeChecked();
    fireEvent.click(screen.getByLabelText("Cedar Ridge Pumps Europe GmbH"));
    await pickCustomer("Harlan");
    fireEvent.click(screen.getByRole("button", { name: "Save mapping" }));
    expect(await screen.findByText(/now acts for the chosen customer; they were signed out/)).toBeInTheDocument();
    const sent = server.calls.find((c) => c.method === "PATCH");
    expect(sent?.body).toEqual({ customerId: OTHER_CUSTOMER, legalEntityIds: [LE_US, LE_DE] });
    expect(JSON.stringify(sent?.body)).not.toContain("@");
  });

  it("resends an invitation and disables a contact, and offers nothing for a disabled one", async () => {
    serve((url, method) => {
      if (method === "POST" && url.pathname.endsWith(`${CONTACT}:resend`)) return json(200, contact);
      if (method === "POST" && url.pathname.endsWith(`${CONTACT}:disable`)) return json(200, { ...contact, status: "disabled" });
      return undefined;
    });
    renderAt();
    fireEvent.click(await screen.findByRole("button", { name: "Resend invitation to dana@bluewater.example" }));
    expect(await screen.findByText("Invitation sent again to dana@bluewater.example")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Disable dana@bluewater.example" }));
    expect(await screen.findByText("dana@bluewater.example was disabled and signed out")).toBeInTheDocument();
    expect(server.calls.filter((c) => c.method === "POST").map((c) => c.url.pathname)).toEqual([`/api/v1/identity/customer-contacts/${CONTACT}:resend`, `/api/v1/identity/customer-contacts/${CONTACT}:disable`]);
    const disabledRow = screen.getByText("gone@bluewater.example").closest("tr") as HTMLElement;
    expect(within(disabledRow).queryByRole("button", { name: /^(Change|Resend|Disable) / })).toBeNull();
  });

  it("shows a permission problem for the list", async () => {
    server.handler = (url) => (url.pathname === "/api/v1/identity/customer-contacts" ? json(403, { code: "AUTH-0003", detail: "missing duty identity.customer-contact.maintain" }) : lookups(url));
    renderAt([]);
    expect(await screen.findByText("You do not have permission")).toBeInTheDocument();
  });
});
