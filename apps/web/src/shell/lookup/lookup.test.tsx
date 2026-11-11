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
const IDENTITY = "tenant-1.user-1";
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
      <WorkContextProvider identity={IDENTITY}>
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
      </WorkContextProvider>
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
    expect(box).not.toHaveAttribute("aria-activedescendant");
    await user.keyboard("{ArrowDown}");
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

  it("does not pick anything on Enter until the user has navigated to an option", async () => {
    const user = userEvent.setup();
    const seen: string[] = [];
    render(<Harness onValue={(v) => seen.push(v)} />);
    await user.click(screen.getByRole("combobox"));
    await screen.findAllByRole("option");
    await user.keyboard("{Enter}");
    expect(seen).toEqual([]);
  });

  it("ignores Enter while a newer query is still loading, so a stale result is never picked", async () => {
    const user = userEvent.setup();
    const seen: string[] = [];
    render(<Harness onValue={(v) => seen.push(v)} />);
    const box = screen.getByRole("combobox");
    await user.type(box, "c");
    await screen.findByRole("option", { name: /Contoso/ });
    await user.keyboard("{ArrowDown}");
    handler = () => new Response(null, { status: 500 });
    await user.type(box, "o{Enter}");
    expect(seen).toEqual([]);
  });

  it("keeps Home and End for the text cursor", async () => {
    const user = userEvent.setup();
    render(<Harness />);
    const box = screen.getByRole("combobox");
    await user.click(box);
    await screen.findAllByRole("option");
    await user.keyboard("{ArrowDown}{ArrowDown}{Home}");
    expect(box.getAttribute("aria-activedescendant")).toBe(screen.getAllByRole("option")[1].id);
    expect(box).toHaveFocus();
  });

  it("says when more results exist than are shown", async () => {
    const user = userEvent.setup();
    const many = Array.from({ length: 20 }, (_, i) => ({ id: `00000000-0000-4000-8000-${String(i).padStart(12, "0")}`, partyId: `p${i}`, partyNumber: `P-${i}`, name: `Vendor ${i}`, accountNumber: `V-${i}` }));
    handler = () => json({ items: many });
    render(<Harness />);
    await user.click(screen.getByRole("combobox"));
    await waitFor(() => expect(screen.getAllByRole("status").some((s) => /More results exist/.test(s.textContent ?? ""))).toBe(true));
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

  it("tells a user without search access to paste an id instead", async () => {
    const user = userEvent.setup();
    handler = () => json({}, 403);
    render(<Harness />);
    await user.click(screen.getByRole("combobox"));
    expect((await screen.findAllByText("You can't search vendors. Paste its id instead.")).length).toBeGreaterThan(0);
  });

  it("does not edit or clear a read-only field, and keeps it focusable", async () => {
    render(<Harness initial={VENDOR_ID} readOnly />);
    const box = screen.getByRole("combobox");
    await waitFor(() => expect(box).toHaveValue("Contoso Ltd (V-00042)"));
    expect(box).toHaveAttribute("readonly");
    expect(box).not.toBeDisabled();
    expect(screen.queryByRole("button", { name: "Clear vendor" })).toBeNull();
  });

  it("adds the legal entity filter and shows the legal entity code on unscoped vendor hits", async () => {
    const LE = "33333333-3333-4333-8333-333333333333";
    handler = (url) => {
      if (url.pathname === "/api/v1/party/vendors:search") return json({ items: [{ ...vendors[0], legalEntityId: LE }] });
      if (url.pathname === `/api/v1/org/legal-entities/${LE}`) return json({ id: LE, code: "US01", name: "Acme US" });
      return json({ items: [] });
    };
    expect((await lookupDefs.vendor.search("con", {})).map(formatOption)).toEqual(["Contoso Ltd (V-00042 · US01)"]);
    expect((await lookupDefs.vendor.search("con", { legalEntityId: LE })).map(formatOption)).toEqual(["Contoso Ltd (V-00042)"]);
    expect(calls).toContain(`/api/v1/party/vendors:search?q=con&limit=20&legalEntityId=${LE}`);
  });

  it("does not ask again for a legal entity the caller may not read, one row at a time", async () => {
    const LE = "44444444-4444-4444-8444-444444444444";
    handler = (url) => {
      if (url.pathname === "/api/v1/party/vendors:search") return json({ items: [{ ...vendors[0], legalEntityId: LE }] });
      if (url.pathname === `/api/v1/org/legal-entities/${LE}`) return json({}, 403);
      return json({ items: [] });
    };
    await lookupDefs.vendor.search("con", {});
    await lookupDefs.vendor.search("con", {});
    await lookupDefs.vendor.search("co", {});
    expect(calls.filter((c) => c === `/api/v1/org/legal-entities/${LE}`)).toHaveLength(1);
  });

  it("clears a choice made under another legal entity when the form switches legal entity", async () => {
    const seen: string[] = [];
    const LE1 = "55555555-5555-4555-8555-555555555555";
    const LE2 = "66666666-6666-4666-8666-666666666666";
    const { rerender } = render(<Harness initial={VENDOR_ID} scope={{ legalEntityId: LE1 }} onValue={(v) => seen.push(v)} />);
    await waitFor(() => expect(screen.getByRole("combobox")).toHaveValue("Contoso Ltd (V-00042)"));
    rerender(<Harness initial={VENDOR_ID} scope={{ legalEntityId: LE2 }} onValue={(v) => seen.push(v)} />);
    await waitFor(() => expect(seen).toEqual([""]));
    expect(screen.getByRole("combobox")).toHaveValue("");
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
  it("maps product rows from the service JSON", async () => {
    handler = () => json({ items: [{ id: "p-1", productNumber: "SHIRT1", name: "Shirt" }, { id: "p-2", productNumber: "HAT1", name: "Hat" }] });
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

  it("reads every legal entity's bank accounts when no legal entity is in scope, and only one when it is", async () => {
    handler = (url) => {
      if (url.pathname === "/api/v1/org/legal-entities") return json({ items: [{ id: "le-1", name: "Germany", code: "DE01" }, { id: "le-2", name: "France", code: "FR01" }] });
      if (url.pathname === "/api/v1/treasury/bank-accounts") {
        const le = url.searchParams.get("legalEntityId");
        return json({ items: [{ id: `ba-${le}`, legalEntityId: le, accountCode: `OPS-${le}`, ibanMasked: "DE** 3000", currency: "EUR" }] });
      }
      return json({ items: [] });
    };
    expect((await lookupDefs.bankAccount.search("", {})).map(formatOption)).toEqual(["OPS-le-1 (DE** 3000 · EUR · DE01)", "OPS-le-2 (DE** 3000 · EUR · FR01)"]);
    calls = [];
    expect((await lookupDefs.bankAccount.search("ops", { legalEntityId: "le-2" })).map((entry) => entry.id)).toEqual(["ba-le-2"]);
    expect(calls).toEqual(["/api/v1/treasury/bank-accounts?legalEntityId=le-2"]);
  });

  it("filters lists the service cannot search, following the cursor", async () => {
    handler = (url) =>
      url.searchParams.get("cursor") === "c2"
        ? json({ items: [{ id: "pos-2", positionNumber: "POS-200", jobCode: "ENG", legalEntityId: "le-1" }] })
        : json({ items: [{ id: "pos-1", positionNumber: "POS-100", jobCode: "OPS", legalEntityId: "le-2" }], nextCursor: "c2" });
    expect((await lookupDefs.position.search("pos", {})).map(formatOption)).toEqual(["POS-100 (OPS)", "POS-200 (ENG)"]);
    expect((await lookupDefs.position.search("", { legalEntityId: "le-1" })).map((entry) => entry.id)).toEqual(["pos-2"]);
    expect(calls).toContain("/api/v1/hr/positions?limit=100&cursor=c2");
  });

  it("names a load by where and when it goes, and a user by display name", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/transport/loads"
        ? json({ items: [{ id: "l-1", mode: "TL", status: "IN_TRANSIT", shipFromSiteId: "s-1", shipToRegion: "Dallas TX", pickupDate: "2026-11-02" }] })
        : json({ items: [{ userId: "u-1", displayName: "Ada Lovelace", email: "ada@example.com", status: "active" }] });
    expect((await lookupDefs.load.search("dallas", {})).map(formatOption)).toEqual(["Dallas TX, pickup 2026-11-02 (TL · in transit)"]);
    expect(await lookupDefs.load.search("", { siteId: "s-2" })).toEqual([]);
    expect((await lookupDefs.user.search("ada", {})).map(formatOption)).toEqual(["Ada Lovelace (ada@example.com)"]);
    expect(calls).toContain("/api/v1/identity/users?q=ada&status=active&limit=20");
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
        <WorkContextProvider identity={IDENTITY}>
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
    expect(JSON.parse(window.localStorage.getItem(`passage.workContext.${IDENTITY}`) ?? "{}")).toEqual({ legalEntityId: "le-1", siteId: "s-1" });
    await user.click(screen.getByRole("combobox", { name: "Current legal entity" }));
    await user.click(await screen.findByRole("option", { name: /Acme DE/ }));
    expect(screen.getByLabelText("context")).toHaveTextContent("le-2|");
  });

  it("picks the only legal entity on its own", async () => {
    handler = () => json({ items: [{ id: "le-1", code: "US01", name: "Acme US" }] });
    render(
      <MemoryRouter>
        <WorkContextProvider identity={IDENTITY}>
          <ContextPicker />
          <Reader />
        </WorkContextProvider>
      </MemoryRouter>,
    );
    await waitFor(() => expect(screen.getByLabelText("context")).toHaveTextContent("le-1|"));
  });

  it("keeps the remembered legal entity through a server error and drops it only when it is gone", async () => {
    const remember = () => window.localStorage.setItem(`passage.workContext.${IDENTITY}`, JSON.stringify({ legalEntityId: "le-9", siteId: "" }));
    const mount = () =>
      render(
        <MemoryRouter>
          <WorkContextProvider identity={IDENTITY}>
            <ContextPicker />
            <Reader />
          </WorkContextProvider>
        </MemoryRouter>,
      );
    remember();
    handler = () => json({}, 500);
    const first = mount();
    await waitFor(() => expect(calls.some((c) => c.includes("/api/v1/org/legal-entities/le-9"))).toBe(true));
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(screen.getByLabelText("context")).toHaveTextContent("le-9|");
    first.unmount();

    clearResolvedOptions();
    handler = (url) => (url.pathname === "/api/v1/org/legal-entities/le-9" ? json({}, 404) : json({ items: [] }));
    mount();
    await waitFor(() => expect(screen.getByLabelText("context")).toHaveTextContent("|"));
    expect(screen.getByLabelText("context").textContent).toBe("|");
  });

  it("starts from the remembered choice", () => {
    window.localStorage.setItem(`passage.workContext.${IDENTITY}`, JSON.stringify({ legalEntityId: "le-9", siteId: "s-9" }));
    render(
      <WorkContextProvider identity={IDENTITY}>
        <Reader />
      </WorkContextProvider>,
    );
    expect(screen.getByLabelText("context")).toHaveTextContent("le-9|s-9");
  });
});

describe("work context isolation", () => {
  function Reader() {
    const { legalEntityId } = useWorkContext();
    return <output aria-label="context">{legalEntityId}</output>;
  }

  beforeEach(() => {
    window.localStorage.setItem(`passage.workContext.${IDENTITY}`, JSON.stringify({ legalEntityId: "le-9", siteId: "" }));
  });

  it("does not hand one user's choice to another user on the same browser", () => {
    render(
      <WorkContextProvider identity="tenant-1.user-2">
        <Reader />
      </WorkContextProvider>,
    );
    expect(screen.getByLabelText("context")).toHaveTextContent("");
  });

  it("ignores the stored choice when the context picker is not available to the user", () => {
    render(
      <WorkContextProvider identity={IDENTITY} enabled={false}>
        <Reader />
      </WorkContextProvider>,
    );
    expect(screen.getByLabelText("context")).toHaveTextContent("");
  });

  it("does not store anything when the identity is unknown", async () => {
    const user = userEvent.setup();
    window.localStorage.clear();
    handler = () => json({ items: [{ id: "le-1", code: "US01", name: "Acme US" }, { id: "le-2", code: "DE01", name: "Acme DE" }] });
    render(
      <MemoryRouter>
        <WorkContextProvider>
          <ContextPicker />
        </WorkContextProvider>
      </MemoryRouter>,
    );
    await user.click(screen.getByRole("combobox", { name: "Current legal entity" }));
    await user.click(await screen.findByRole("option", { name: /Acme US/ }));
    expect(window.localStorage.length).toBe(0);
  });

  it("drops a remembered legal entity that no longer resolves", async () => {
    handler = () => json({}, 404);
    render(
      <MemoryRouter>
        <WorkContextProvider identity={IDENTITY}>
          <ContextPicker />
          <Reader />
        </WorkContextProvider>
      </MemoryRouter>,
    );
    await waitFor(() => expect(screen.getByLabelText("context")).toHaveTextContent(""));
  });

  it("keeps recent picks apart per user", async () => {
    const user = userEvent.setup();
    const first = render(<Harness />);
    await user.type(screen.getByRole("combobox"), "fabrikam");
    await user.click(await screen.findByRole("option", { name: /Fabrikam/ }));
    first.unmount();
    render(
      <MemoryRouter>
        <WorkContextProvider identity="tenant-1.user-2">
          <EntityLookup entity="vendor" value="" onChange={() => undefined} />
        </WorkContextProvider>
      </MemoryRouter>,
    );
    await user.click(screen.getByRole("combobox"));
    await screen.findAllByRole("option");
    expect(screen.queryByText("Recent")).toBeNull();
  });
});

describe("work context prefill", () => {
  function Form({ prefill, disabled }: { prefill?: boolean; disabled?: boolean }) {
    const [entity, setEntity] = useState("");
    return (
      <MemoryRouter>
        <label htmlFor="le-field">Legal entity</label>
        <EntityLookup id="le-field" entity="legalEntity" prefill={prefill} disabled={disabled} value={entity} onChange={(e) => setEntity(e.target.value)} />
        <output aria-label="chosen">{entity}</output>
      </MemoryRouter>
    );
  }

  beforeEach(() => {
    window.localStorage.setItem(`passage.workContext.${IDENTITY}`, JSON.stringify({ legalEntityId: "le-9", siteId: "" }));
  });

  it("fills an empty legal entity field from the working context", async () => {
    render(
      <WorkContextProvider identity={IDENTITY}>
        <Form />
      </WorkContextProvider>,
    );
    await waitFor(() => expect(screen.getByLabelText("chosen")).toHaveTextContent("le-9"));
  });

  it("leaves the field empty when prefill is off or the field is disabled", () => {
    render(
      <WorkContextProvider identity={IDENTITY}>
        <Form prefill={false} />
        <Form disabled />
      </WorkContextProvider>,
    );
    screen.getAllByLabelText("chosen").forEach((out) => expect(out).toHaveTextContent(""));
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
