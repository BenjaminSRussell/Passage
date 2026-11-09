import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../a11y";
import { EntityLookup, type EntityLookupProps } from "./EntityLookup";
import { EntityName } from "./EntityName";
import { clearResolvedOptions, formatOption, lookupDefs, setLookupFetcher } from "./entities";
import { ContextPicker, useWorkContext, WorkContextProvider } from "./workContext";

const VENDOR_ID = "11111111-1111-4111-8111-111111111111";
const OTHER_ID = "22222222-2222-4222-8222-222222222222";

const vendors = [
  { id: VENDOR_ID, partyId: "p1", partyNumber: "P-1", name: "Contoso Ltd", accountNumber: "V-00042" },
  { id: OTHER_ID, partyId: "p2", partyNumber: "P-2", name: "Fabrikam Inc", accountNumber: "V-00043" },
];

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

let calls: string[];
let handler: (url: URL) => Response;

beforeEach(() => {
  calls = [];
  setLookupFetcher(null);
  window.localStorage.clear();
  clearResolvedOptions();
  handler = (url) => {
    if (url.pathname === "/api/v1/party/vendors:search") {
      const q = (url.searchParams.get("q") ?? "").toLowerCase();
      return json({ items: vendors.filter((v) => q === "" || v.name.toLowerCase().includes(q) || v.accountNumber.toLowerCase().includes(q)) });
    }
    if (url.pathname === `/api/v1/party/vendors/${VENDOR_ID}`) return json(vendors[0]);
    if (url.pathname === "/api/v1/party/parties/p1") return json({ id: "p1", name: "Contoso Ltd", partyNumber: "P-1" });
    return json({ items: [] });
  };
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string) => {
      calls.push(input);
      return handler(new URL(input, "http://localhost"));
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function Harness(props: Partial<EntityLookupProps> & { initial?: string; onValue?: (v: string) => void }) {
  const [value, setValue] = useState(props.initial ?? "");
  return (
    <MemoryRouter>
      <label htmlFor="vendor-field">Vendor</label>
      <EntityLookup
        id="vendor-field"
        entity="vendor"
        name="vendorId"
        {...props}
        value={value}
        onChange={(e) => {
          setValue(e.target.value);
          props.onValue?.(e.target.value);
        }}
      />
    </MemoryRouter>
  );
}

describe("EntityLookup", () => {
  it("searches by name, shows the name, and keeps the id in a hidden input", async () => {
    const user = userEvent.setup();
    const seen: string[] = [];
    const { container } = render(<Harness onValue={(v) => seen.push(v)} />);
    const box = screen.getByRole("combobox", { name: "Vendor" });
    await user.type(box, "contoso");
    await waitFor(() => expect(screen.queryByRole("option", { name: /Fabrikam/ })).toBeNull());
    const option = await screen.findByRole("option", { name: /Contoso Ltd/ });
    expect(calls.some((c) => c.includes("q=contoso"))).toBe(true);
    await user.click(option);
    expect(seen).toEqual([VENDOR_ID]);
    expect(box).toHaveValue("Contoso Ltd (V-00042)");
    expect(container.querySelector('input[type="hidden"][name="vendorId"]')).toHaveValue(VENDOR_ID);
    expect(screen.queryByText(VENDOR_ID)).toBeNull();
  });

  it("finds a record by its number", async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.type(screen.getByRole("combobox"), "V-00043");
    await waitFor(() => expect(screen.queryByRole("option", { name: /Contoso/ })).toBeNull());
    expect(await screen.findByRole("option", { name: /Fabrikam Inc/ })).toBeInTheDocument();
  });

  it("follows the combobox pattern and works from the keyboard", async () => {
    const user = userEvent.setup();
    const seen: string[] = [];
    render(<Harness onValue={(v) => seen.push(v)} />);
    const box = screen.getByRole("combobox");
    expect(box).toHaveAttribute("aria-expanded", "false");
    expect(box).toHaveAttribute("aria-autocomplete", "list");
    await user.click(box);
    expect(box).toHaveAttribute("aria-expanded", "true");
    const list = await screen.findByRole("listbox");
    expect(box).toHaveAttribute("aria-controls", list.id);
    await screen.findAllByRole("option");
    expect(box.getAttribute("aria-activedescendant")).toBe(screen.getAllByRole("option")[0].id);
    await user.keyboard("{ArrowDown}");
    expect(box.getAttribute("aria-activedescendant")).toBe(screen.getAllByRole("option")[1].id);
    expect(screen.getAllByRole("option")[1]).toHaveAttribute("aria-selected", "true");
    await user.keyboard("{ArrowUp}{ArrowDown}{Enter}");
    expect(seen).toEqual([OTHER_ID]);
    expect(box).toHaveAttribute("aria-expanded", "false");
    await user.keyboard("{ArrowDown}");
    expect(box).toHaveAttribute("aria-expanded", "true");
    await user.keyboard("{Escape}");
    expect(box).toHaveAttribute("aria-expanded", "false");
    expect(box).toHaveValue("Fabrikam Inc (V-00043)");
  });

  it("announces the number of matches in a live region", async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(screen.getByRole("combobox"));
    await waitFor(() => expect(screen.getAllByRole("status").some((s) => s.textContent === "2 matches.")).toBe(true));
  });

  it("shows the name of an existing value instead of its id", async () => {
    render(<Harness initial={VENDOR_ID} />);
    await waitFor(() => expect(screen.getByRole("combobox")).toHaveValue("Contoso Ltd (V-00042)"));
  });

  it("lists recent picks first when opened with no text", async () => {
    const user = userEvent.setup();
    const first = render(<Harness />);
    await user.type(screen.getByRole("combobox"), "fabrikam");
    await user.click(await screen.findByRole("option", { name: /Fabrikam/ }));
    first.unmount();
    render(<Harness />);
    await user.click(screen.getByRole("combobox"));
    const options = await screen.findAllByRole("option");
    expect(screen.getByText("Recent")).toBeInTheDocument();
    expect(options[0]).toHaveTextContent("Fabrikam Inc");
    expect(options).toHaveLength(2);
  });

  it("accepts an id copied from the details menu and shows its name", async () => {
    const seen: string[] = [];
    render(<Harness onValue={(v) => seen.push(v)} />);
    fireEvent.change(screen.getByRole("combobox"), { target: { value: VENDOR_ID.toUpperCase() } });
    expect(seen).toEqual([VENDOR_ID]);
    await waitFor(() => expect(screen.getByRole("combobox")).toHaveValue("Contoso Ltd (V-00042)"));
    fireEvent.change(screen.getByRole("combobox"), { target: { value: "not an id" } });
    expect(seen).toEqual([VENDOR_ID]);
  });

  it("clears the value", async () => {
    const user = userEvent.setup();
    const seen: string[] = [];
    render(<Harness initial={VENDOR_ID} onValue={(v) => seen.push(v)} />);
    await user.click(screen.getByRole("button", { name: "Clear vendor" }));
    expect(seen).toEqual([""]);
    expect(screen.getByRole("combobox")).toHaveValue("");
  });

  it("reports a failed search without throwing and offers a retry by typing", async () => {
    const user = userEvent.setup();
    handler = () => json({}, 500);
    render(<Harness />);
    await user.click(screen.getByRole("combobox"));
    expect((await screen.findAllByText(/Could not load vendor matches/)).length).toBeGreaterThan(0);
    handler = () => json({ items: vendors });
    await user.type(screen.getByRole("combobox"), "c");
    expect(await screen.findByRole("option", { name: /Contoso/ })).toBeInTheDocument();
  });

  it("offers a create-new link only where asked", async () => {
    const { rerender } = render(<Harness />);
    expect(screen.queryByRole("link")).toBeNull();
    rerender(<Harness allowCreate />);
    expect(screen.getByRole("link", { name: "Create a new vendor" })).toHaveAttribute("href", "/party/new");
  });

  it("falls back to a neutral label, never the id, when the name cannot be loaded", async () => {
    handler = () => json({}, 404);
    render(<Harness initial={OTHER_ID} />);
    await waitFor(() => expect(screen.getByRole("combobox")).toHaveValue("Selected vendor"));
  });
});

describe("lookup adapters", () => {
  it("maps product rows whatever the casing of the service JSON", async () => {
    handler = () => json({ items: [{ ID: "p-1", ProductNumber: "SHIRT1", Name: "Shirt" }, { id: "p-2", productNumber: "HAT1", name: "Hat" }] });
    const found = await lookupDefs.product.search("s", {});
    expect(found.map(formatOption)).toEqual(["Shirt (SHIRT1)", "Hat (HAT1)"]);
  });

  it("scopes site and warehouse searches to the legal entity", async () => {
    await lookupDefs.site.search("hq", { legalEntityId: "le-1" });
    await lookupDefs.warehouse.search("", { legalEntityId: "le-1", siteId: "s-1" });
    expect(calls).toContain("/api/v1/org/sites?q=hq&limit=20&legalEntityId=le-1");
    expect(calls).toContain("/api/v1/org/warehouses?limit=20&legalEntityId=le-1&siteId=s-1");
  });

  it("filters currencies by name or code", async () => {
    handler = () => json({ items: [{ code: "USD", name: "US Dollar" }, { code: "EUR", name: "Euro" }] });
    expect((await lookupDefs.currency.search("eur", {})).map((c) => c.id)).toEqual(["EUR"]);
    expect((await lookupDefs.currency.search("dollar", {})).map((c) => c.id)).toEqual(["USD"]);
  });

  it("resolves a customer through its party for the display name", async () => {
    handler = (url) =>
      url.pathname.endsWith("/customers/c-1") ? json({ id: "c-1", partyId: "p1", accountNumber: "C-9" }) : json({ id: "p1", name: "Contoso Ltd", partyNumber: "P-1" });
    expect(await lookupDefs.customer.resolve("c-1")).toEqual({ id: "c-1", name: "Contoso Ltd", number: "C-9" });
  });
});

describe("EntityName", () => {
  it("shows the name and number, and copies the id from the details menu", async () => {
    const user = userEvent.setup();
    const writeText = vi.fn(async () => undefined);
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });
    render(<EntityName entity="vendor" id={VENDOR_ID} />);
    expect(await screen.findByText("Contoso Ltd (V-00042)")).toBeInTheDocument();
    expect(screen.queryByText(VENDOR_ID)).toBeNull();
    await user.click(screen.getByText("⋯"));
    await user.click(screen.getByRole("button", { name: "Copy id" }));
    expect(writeText).toHaveBeenCalledWith(VENDOR_ID);
  });

  it("never prints the id when the name is unavailable", async () => {
    handler = () => json({}, 404);
    render(<EntityName entity="vendor" id={OTHER_ID} />);
    expect(await screen.findByText("Not available")).toBeInTheDocument();
    expect(screen.queryByText(OTHER_ID)).toBeNull();
  });
});

describe("work context", () => {
  function Reader() {
    const { legalEntityId, siteId } = useWorkContext();
    return <output aria-label="context">{`${legalEntityId}|${siteId}`}</output>;
  }

  it("is chosen once, remembered, and clears the site when the legal entity changes", async () => {
    const user = userEvent.setup();
    handler = (url) => {
      if (url.pathname === "/api/v1/org/legal-entities") return json({ items: [{ id: "le-1", code: "US01", name: "Acme US" }, { id: "le-2", code: "DE01", name: "Acme DE" }] });
      if (url.pathname === "/api/v1/org/sites") return json({ items: [{ id: "s-1", code: "HQ", name: "Head office" }] });
      return json({ items: [] });
    };
    render(
      <MemoryRouter>
        <WorkContextProvider>
          <ContextPicker />
          <Reader />
        </WorkContextProvider>
      </MemoryRouter>,
    );
    await user.click(screen.getByRole("combobox", { name: "Current legal entity" }));
    await user.click(await screen.findByRole("option", { name: /Acme US/ }));
    await user.click(screen.getByRole("combobox", { name: "Current site" }));
    await user.click(await screen.findByRole("option", { name: /Head office/ }));
    expect(screen.getByLabelText("context")).toHaveTextContent("le-1|s-1");
    expect(JSON.parse(window.localStorage.getItem("passage.workContext") ?? "{}")).toEqual({ legalEntityId: "le-1", siteId: "s-1" });
    await user.click(screen.getByRole("combobox", { name: "Current legal entity" }));
    await user.click(await screen.findByRole("option", { name: /Acme DE/ }));
    expect(screen.getByLabelText("context")).toHaveTextContent("le-2|");
  });

  it("picks the only legal entity on its own", async () => {
    handler = () => json({ items: [{ id: "le-1", code: "US01", name: "Acme US" }] });
    render(
      <MemoryRouter>
        <WorkContextProvider>
          <ContextPicker />
          <Reader />
        </WorkContextProvider>
      </MemoryRouter>,
    );
    await waitFor(() => expect(screen.getByLabelText("context")).toHaveTextContent("le-1|"));
  });

  it("starts from the remembered choice", () => {
    window.localStorage.setItem("passage.workContext", JSON.stringify({ legalEntityId: "le-9", siteId: "s-9" }));
    render(
      <WorkContextProvider>
        <Reader />
      </WorkContextProvider>,
    );
    expect(screen.getByLabelText("context")).toHaveTextContent("le-9|s-9");
  });
});

describe("EntityLookup accessibility", () => {
  it("passes axe with a FormField-style visible label, a value and an open list", async () => {
    const user = userEvent.setup();
    const { container } = render(<Harness initial={VENDOR_ID} />);
    await screen.findByDisplayValue("Contoso Ltd (V-00042)");
    await user.click(screen.getByRole("combobox", { name: "Vendor" }));
    await screen.findByRole("option", { name: /Contoso Ltd/ });
    const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
    expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]);
  });
});
