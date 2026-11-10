import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { CommandPalette, type PaletteEntry } from "../CommandPalette";
import { pushRecent, readFavourites, readRecent, toggleFavourite } from "./recentRecords";
import { isAppPath, searchRecords, type RecordHit, type RecordSearch } from "./records";

const pages: PaletteEntry[] = [{ id: "p:1", group: "Purchasing", label: "Purchase orders", to: "/purchasing/purchase-orders" }];

const po: RecordHit = { type: "purchase_order", id: "po-1", number: "PO-1042", title: "PO-1042", subtitle: "USD 120.00", status: "CONFIRMED", link: "/purchasing/purchase-orders/po-1" };
const vendor: RecordHit = { type: "party", id: "v-1", number: "V-0007", title: "Contoso", subtitle: "organization", status: "active", link: "/party/v-1" };

beforeEach(() => window.localStorage.clear());
afterEach(() => vi.unstubAllGlobals());

function open(search: RecordSearch, identity = "user-a", onSelect = vi.fn()) {
  render(<CommandPalette entries={pages} onSelect={onSelect} onClose={vi.fn()} announce={vi.fn()} identity={identity} searchRecords={search} />);
  return { onSelect, user: userEvent.setup() };
}

describe("palette record results", () => {
  it("shows records grouped by type with their status and opens one with Enter", async () => {
    const search = vi.fn<RecordSearch>().mockResolvedValue([po, vendor]);
    const { user, onSelect } = open(search);
    await user.type(screen.getByRole("combobox"), "contoso");
    const group = await screen.findByRole("group", { name: "Purchase orders" });
    expect(within(group).getByRole("option", { name: /Purchase order PO-1042, Confirmed · USD 120.00/ })).toBeInTheDocument();
    expect(screen.getByRole("group", { name: "Customers and vendors" })).toBeInTheDocument();
    await user.keyboard("{Enter}");
    expect(onSelect).toHaveBeenCalledWith(expect.objectContaining({ to: po.link, record: po }));
    expect(readRecent("user-a")).toEqual([po]);
  });

  it("keeps page results after the records", async () => {
    const { user } = open(vi.fn<RecordSearch>().mockResolvedValue([po]));
    await user.type(screen.getByRole("combobox"), "purchase");
    await screen.findByRole("group", { name: "Purchase orders" });
    const labels = screen.getAllByRole("option").map((o) => o.getAttribute("aria-label"));
    expect(labels[labels.length - 1]).toBe("Purchase orders, Purchasing");
  });

  it("does not search for one character and says so when the service fails", async () => {
    const search = vi.fn<RecordSearch>().mockRejectedValue(new Error("down"));
    const { user } = open(search);
    await user.type(screen.getByRole("combobox"), "p");
    expect(search).not.toHaveBeenCalled();
    await user.type(screen.getByRole("combobox"), "u");
    expect(await screen.findByRole("status")).toHaveTextContent(/can't be searched/);
    expect(screen.getByRole("option", { name: "Purchase orders, Purchasing" })).toBeInTheDocument();
  });

  it("lists favourites, then recent records, when the box is empty", () => {
    pushRecent("user-a", po);
    pushRecent("user-a", vendor);
    toggleFavourite("user-a", vendor);
    open(vi.fn<RecordSearch>());
    expect(within(screen.getByRole("group", { name: "Favourite records" })).getByRole("option", { name: /V-0007/ })).toBeInTheDocument();
    const recent = screen.getByRole("group", { name: "Recent records" });
    expect(within(recent).getAllByRole("option")).toHaveLength(1);
    expect(within(recent).getByRole("option", { name: /PO-1042/ })).toBeInTheDocument();
  });

  it("favourites the active record with Alt+F and removes it again", async () => {
    const { user } = open(vi.fn<RecordSearch>().mockResolvedValue([po]));
    await user.type(screen.getByRole("combobox"), "po-10");
    await screen.findByRole("group", { name: "Purchase orders" });
    await user.keyboard("{Alt>}f{/Alt}");
    expect(readFavourites("user-a")).toEqual([po]);
    expect(screen.getByRole("option", { name: /PO-1042.*Favourite/ })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Remove PO-1042 from favourites" }));
    await waitFor(() => expect(readFavourites("user-a")).toEqual([]));
  });

  it("keeps recent records per user", () => {
    pushRecent("user-a", po);
    expect(readRecent("user-b")).toEqual([]);
    pushRecent("", po);
    expect(readRecent("")).toEqual([]);
  });

  it("keeps the newest eight recent records without duplicates", () => {
    for (let i = 0; i < 10; i++) pushRecent("user-a", { ...po, id: `po-${i}`, number: `PO-${i}` });
    pushRecent("user-a", { ...po, id: "po-5", number: "PO-5" });
    const recent = readRecent("user-a");
    expect(recent).toHaveLength(8);
    expect(recent[0].id).toBe("po-5");
    expect(recent.filter((r) => r.id === "po-5")).toHaveLength(1);
  });
});

describe("searchRecords", () => {
  it("calls the search service and keeps only known types with in-app links", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          results: [
            { type: "receipt", id: "r1", number: "RC-1", title: "RC-1", link: "/receiving/receipts/r1" },
            { type: "mystery", id: "x", number: "X", title: "X", link: "/x" },
            { type: "party", id: "p", number: "P", title: "P", link: "https://evil.example/p" },
          ],
        }),
        { status: 200 },
      ),
    );
    vi.stubGlobal("fetch", fetchMock);
    const found = await searchRecords("rc-1", new AbortController().signal);
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/search?q=rc-1&limit=20");
    expect(found.map((r) => r.id)).toEqual(["r1"]);
  });

  it("rejects when the service answers with an error", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("{}", { status: 403 })));
    await expect(searchRecords("abc", new AbortController().signal)).rejects.toThrow(/403/);
  });

  it("follows in-app paths only", () => {
    expect(isAppPath("/party/1")).toBe(true);
    expect(isAppPath("//evil.example")).toBe(false);
    expect(isAppPath("https://evil.example")).toBe(false);
  });
});
