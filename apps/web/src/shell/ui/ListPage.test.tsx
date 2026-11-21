import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { clearResolvedOptions, rememberOption, setLookupFetcher } from "../lookup/entities";
import { WorkContext } from "../lookup/workContextState";
import type { PreferenceStorage } from "../preferenceStorage";
import { pageViolations } from "../testing/fakeServer";
import { ListPage, type BulkAction, type BulkResult, type ListColumn, type ListData } from "./ListPage";
import { viewStoreKey, type ListFilterDef, type ListQuery } from "./listViews";

interface Row {
  id: string;
  number: string;
  amount: string;
  status: string;
  vendorId: string;
}

const VENDOR = "11111111-1111-4111-8111-111111111111";

const rows: Row[] = [
  { id: "1", number: "INV-10", amount: "9.50", status: "OPEN", vendorId: VENDOR },
  { id: "2", number: "INV-9", amount: "120.00", status: "PAID", vendorId: VENDOR },
  { id: "3", number: "=EVIL()", amount: "-15.00", status: "OPEN, late", vendorId: VENDOR },
];

const columns: ListColumn<Row>[] = [
  { key: "number", header: "Number" },
  { key: "amount", header: "Amount", align: "end" },
  { key: "status", header: "Status" },
  { key: "note", header: "Note", defaultHidden: true },
];

const filters: ListFilterDef[] = [
  { kind: "text", key: "q", label: "Search" },
  { kind: "select", key: "status", label: "Status", anyLabel: "All statuses", options: [{ value: "OPEN", label: "Open" }, { value: "PAID", label: "Paid" }] },
  { kind: "dateRange", key: "created", label: "Created" },
  { kind: "entity", key: "vendorId", label: "Vendor", entity: "vendor" },
];

type MemoryStorage = PreferenceStorage & { data: Record<string, string> };

function memory(initial: Record<string, string> = {}, failWrites = false): MemoryStorage {
  const data = { ...initial };
  return {
    data,
    read: (k) => data[k] ?? null,
    write: (k, v) => {
      if (failWrites) {
        return false;
      }
      data[k] = v;
      return true;
    },
    remove: (k) => {
      delete data[k];
    },
  };
}

let source: Partial<ListData<Row>>;
let seen: ListQuery[];
const loadMore = vi.fn();
const retry = vi.fn();

function useFake(query: ListQuery): ListData<Row> {
  seen.push(query);
  return { rows, loading: false, loadingMore: false, hasMore: false, loadMore, retry, ...source };
}

function renderList(storage: MemoryStorage = memory(), props: Partial<Parameters<typeof ListPage<Row>>[0]> = {}) {
  const tree = () => (
    <WorkContext.Provider value={{ identity: "ada", legalEntityId: "", siteId: "", setLegalEntityId: () => undefined, setSiteId: () => undefined }}>
      <main>
        <nav aria-label="Test" />
        <h1>Invoices</h1>
        <ListPage<Row>
          id="invoices"
          title="Invoices"
          noun="invoices"
          singular="invoice"
          columns={columns}
          filters={filters}
          useRows={useFake}
          getRowKey={(r) => r.id}
          empty={{ title: "No invoices yet", description: "Post one to begin.", action: <a href="/post">Post an invoice</a> }}
          storage={storage}
          {...props}
        />
      </main>
    </WorkContext.Provider>
  );
  return { storage, ...render(tree()), tree };
}

const bodyNumbers = () => within(screen.getByRole("table", { name: "Invoices" })).getAllByRole("row").slice(1).map((r) => within(r).getAllByRole("cell")[0].textContent);
const last = () => seen[seen.length - 1];

beforeEach(() => {
  source = {};
  seen = [];
  loadMore.mockClear();
  retry.mockClear();
  window.localStorage.clear();
  clearResolvedOptions();
  rememberOption("vendor", { id: VENDOR, name: "Acme Supply", number: "V-100" });
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("ListPage sorting", () => {
  it("sorts loaded rows ascending, descending and back, and exposes the state with aria-sort", async () => {
    const user = userEvent.setup();
    renderList();
    const header = screen.getByRole("columnheader", { name: "Amount" });
    expect(header).toHaveAttribute("aria-sort", "none");
    await user.click(within(header).getByRole("button", { name: "Amount" }));
    expect(header).toHaveAttribute("aria-sort", "ascending");
    expect(bodyNumbers()).toEqual(["=EVIL()", "INV-10", "INV-9"]);
    await user.click(within(header).getByRole("button", { name: "Amount" }));
    expect(header).toHaveAttribute("aria-sort", "descending");
    expect(bodyNumbers()).toEqual(["INV-9", "INV-10", "=EVIL()"]);
    await user.click(within(header).getByRole("button", { name: "Amount" }));
    expect(header).toHaveAttribute("aria-sort", "none");
    expect(bodyNumbers()).toEqual(["INV-10", "INV-9", "=EVIL()"]);
  });

  it("sorts from the keyboard and moves aria-sort to the newest column", async () => {
    const user = userEvent.setup();
    renderList();
    await user.tab();
    const first = screen.getByRole("button", { name: "Number" });
    first.focus();
    await user.keyboard("{Enter}");
    expect(screen.getByRole("columnheader", { name: "Number" })).toHaveAttribute("aria-sort", "ascending");
    screen.getByRole("button", { name: "Status" }).focus();
    await user.keyboard(" ");
    expect(screen.getByRole("columnheader", { name: "Status" })).toHaveAttribute("aria-sort", "ascending");
    expect(screen.getByRole("columnheader", { name: "Number" })).toHaveAttribute("aria-sort", "none");
  });

  it("says the order covers only the loaded rows while more exist", async () => {
    source = { hasMore: true };
    const user = userEvent.setup();
    renderList();
    expect(screen.queryByText(/Sorted within/)).toBeNull();
    await user.click(screen.getByRole("button", { name: "Number" }));
    const note = screen.getByText("Sorted within the 3 rows loaded so far. Load more to include the rest.");
    expect(note.closest("[role='status']")).toHaveTextContent("3 invoices loaded, more available");
    expect(screen.getByRole("button", { name: "Number" })).toHaveAccessibleDescription(note.textContent ?? "");
  });

  it("clears the sort when its column is hidden", async () => {
    const user = userEvent.setup();
    renderList();
    await user.click(screen.getByRole("button", { name: "Amount" }));
    expect(last().sort).toEqual({ key: "amount", direction: "ascending" });
    await user.click(screen.getByRole("button", { name: "Columns" }));
    await user.click(screen.getByRole("checkbox", { name: "Amount" }));
    expect(last().sort).toBeNull();
  });

  it("shows a loading message while rows load", () => {
    source = { loading: true, rows: [] };
    renderList();
    expect(screen.getByText("Loading invoices…")).toHaveAttribute("role", "status");
  });

  it("hands a server-side sort to the data source and leaves the row order alone", async () => {
    const user = userEvent.setup();
    renderList(memory(), { serverSort: ["number"] });
    await user.click(screen.getByRole("button", { name: "Number" }));
    expect(last().sort).toEqual({ key: "number", direction: "ascending" });
    expect(bodyNumbers()).toEqual(["INV-10", "INV-9", "=EVIL()"]);
    expect(screen.queryByText(/Sorted within/)).toBeNull();
  });

  it("does not offer sorting on a column marked unsortable", () => {
    renderList(memory(), { columns: columns.map((c) => (c.key === "status" ? { ...c, sortable: false } : c)) });
    expect(screen.queryByRole("button", { name: "Status" })).toBeNull();
    expect(screen.getByRole("columnheader", { name: "Status" })).not.toHaveAttribute("aria-sort");
  });
});

describe("ListPage columns", () => {
  it("hides, shows and resets columns, and keeps at least one", async () => {
    const user = userEvent.setup();
    renderList();
    expect(screen.queryByRole("columnheader", { name: "Note" })).toBeNull();
    await user.click(screen.getByRole("button", { name: "Columns" }));
    const panel = screen.getByRole("group", { name: "Columns" });
    await user.click(within(panel).getByRole("checkbox", { name: "Note" }));
    expect(screen.getByRole("columnheader", { name: "Note" })).toBeInTheDocument();
    await user.click(within(panel).getByRole("checkbox", { name: "Amount" }));
    expect(screen.queryByRole("columnheader", { name: "Amount" })).toBeNull();
    await user.click(within(panel).getByRole("button", { name: "Reset columns" }));
    expect(screen.queryByRole("columnheader", { name: "Note" })).toBeNull();
    expect(screen.getByRole("columnheader", { name: "Amount" })).toBeInTheDocument();

    await user.click(within(panel).getByRole("checkbox", { name: "Amount" }));
    await user.click(within(panel).getByRole("checkbox", { name: "Status" }));
    expect(within(panel).getByRole("checkbox", { name: "Number" })).toBeDisabled();
    expect(screen.getByText("Keep at least one column.")).toBeInTheDocument();
  });

  it("opens with focus inside, closes on Escape and returns focus to its button", async () => {
    const user = userEvent.setup();
    renderList();
    const trigger = screen.getByRole("button", { name: "Columns" });
    expect(trigger).toHaveAttribute("aria-expanded", "false");
    trigger.focus();
    await user.keyboard("{Enter}");
    expect(trigger).toHaveAttribute("aria-expanded", "true");
    expect(within(screen.getByRole("group", { name: "Columns" })).getByRole("checkbox", { name: "Number" })).toHaveFocus();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("group", { name: "Columns" })).toBeNull();
    expect(trigger).toHaveFocus();
    expect(trigger).toHaveAttribute("aria-expanded", "false");
  });

  it("closes when focus moves on or the page is pressed elsewhere", async () => {
    const user = userEvent.setup();
    renderList();
    await user.click(screen.getByRole("button", { name: "Columns" }));
    await user.tab({ shift: true });
    await user.tab({ shift: true });
    expect(screen.queryByRole("group", { name: "Columns" })).toBeNull();
    await user.click(screen.getByRole("button", { name: "Columns" }));
    fireEvent.pointerDown(screen.getByRole("heading", { name: "Invoices", level: 1 }));
    expect(screen.queryByRole("group", { name: "Columns" })).toBeNull();
  });
});

describe("ListPage filters", () => {
  it("applies text, select and date range filters together, shows chips and removes one", async () => {
    const user = userEvent.setup();
    renderList();
    expect(last().filters).toEqual({});
    await user.type(screen.getByLabelText("Search"), " late ");
    await user.selectOptions(screen.getByLabelText("Status"), "PAID");
    fireEvent.change(screen.getByLabelText("Created from"), { target: { value: "2026-10-01" } });
    expect(last().filters).toEqual({});
    await user.click(screen.getByRole("button", { name: "Apply filters" }));
    expect(last().filters).toEqual({ q: "late", status: "PAID", createdFrom: "2026-10-01" });

    const chips = within(screen.getByRole("list", { name: "Active filters" }));
    expect(chips.getAllByRole("listitem").map((li) => li.textContent)).toEqual(["Search: late×", "Status: Paid×", "Created: from 2026-10-01×"]);
    await user.click(screen.getByRole("button", { name: "Remove filter Status" }));
    expect(last().filters).toEqual({ q: "late", createdFrom: "2026-10-01" });
    expect(screen.getByLabelText("Status")).toHaveValue("");
  });

  it("clears every filter at once", async () => {
    const user = userEvent.setup();
    renderList();
    await user.type(screen.getByLabelText("Search"), "x");
    await user.selectOptions(screen.getByLabelText("Status"), "OPEN");
    await user.click(screen.getByRole("button", { name: "Apply filters" }));
    await user.click(screen.getByRole("button", { name: "Clear all filters" }));
    expect(last().filters).toEqual({});
    expect(screen.queryByRole("list", { name: "Active filters" })).toBeNull();
    expect(screen.getByLabelText("Search")).toHaveValue("");
  });

  it("picks a vendor from the lookup and names it in the chip", async () => {
    setLookupFetcher(
      async () => new Response(JSON.stringify({ items: [{ id: VENDOR, partyId: "p1", partyNumber: "P-1", name: "Acme Supply", accountNumber: "V-100" }] }), { status: 200, headers: { "Content-Type": "application/json" } }),
    );
    const user = userEvent.setup();
    renderList();
    await user.click(screen.getByLabelText("Vendor"));
    await user.click(await screen.findByRole("option", { name: /Acme Supply/ }));
    await user.click(screen.getByRole("button", { name: "Apply filters" }));
    expect(last().filters).toEqual({ vendorId: VENDOR });
    expect(await screen.findByText("Vendor: Acme Supply (V-100)")).toBeInTheDocument();
  });
});

describe("ListPage states", () => {
  it("shows a busy table while loading", () => {
    source = { loading: true, rows: [] };
    renderList();
    expect(screen.getByRole("table", { name: "Invoices" })).toHaveAttribute("aria-busy", "true");
  });

  it("offers a next step when the list has never had rows", () => {
    source = { rows: [] };
    renderList();
    expect(screen.getByRole("heading", { name: "No invoices yet" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Post an invoice" })).toBeInTheDocument();
    expect(screen.queryByRole("table")).toBeNull();
  });

  it("says nothing matches filters and clears them", async () => {
    source = { rows: [] };
    const user = userEvent.setup();
    renderList();
    await user.selectOptions(screen.getByLabelText("Status"), "PAID");
    await user.click(screen.getByRole("button", { name: "Apply filters" }));
    expect(screen.getByRole("heading", { name: "No invoices match these filters" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "No invoices yet" })).toBeNull();
    await user.click(screen.getByRole("button", { name: "Clear filters" }));
    expect(screen.getByRole("heading", { name: "No invoices yet" })).toBeInTheDocument();
    expect(last().filters).toEqual({});
  });

  it("explains a failure with a retry, and keeps the filters usable", async () => {
    source = { rows: [], error: { message: "boom", code: "X-1" } };
    const user = userEvent.setup();
    renderList();
    expect(screen.getByText("Could not load invoices")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Try again" }));
    expect(retry).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("search", { name: "Filter invoices" })).toBeInTheDocument();
  });

  it("tells a user without access, offers no retry and no tools", () => {
    source = { rows: [], error: { message: "no", code: "403" }, forbidden: true };
    renderList();
    expect(screen.getByText("You can't view invoices")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Try again" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Export CSV" })).toBeNull();
    expect(screen.queryByRole("search")).toBeNull();
  });

  it("keeps the loaded rows when a later page fails", () => {
    source = { error: { message: "boom", code: "X-1" } };
    renderList();
    expect(screen.getByText("Could not load invoices")).toBeInTheDocument();
    expect(bodyNumbers()).toHaveLength(3);
  });

  it("pages on request and shows progress while the next page loads", async () => {
    source = { hasMore: true };
    const user = userEvent.setup();
    const { rerender, tree } = renderList();
    await user.click(screen.getByRole("button", { name: "Load more invoices" }));
    expect(loadMore).toHaveBeenCalledTimes(1);
    source = { hasMore: true, loadingMore: true };
    rerender(tree());
    expect(screen.getByRole("button", { name: "Load more invoices" })).toHaveAttribute("aria-busy", "true");
    source = { hasMore: false };
    rerender(tree());
    expect(screen.queryByRole("button", { name: "Load more invoices" })).toBeNull();
  });

  it("announces how many rows are loaded in a polite live region that is always mounted", () => {
    source = { hasMore: true };
    const { rerender, tree } = renderList();
    const status = screen.getByText("3 invoices loaded, more available");
    expect(status).toHaveAttribute("role", "status");
    source = { hasMore: false, rows: rows.slice(0, 1) };
    rerender(tree());
    expect(status).toBeInTheDocument();
    expect(status).toHaveTextContent("1 invoice loaded");
  });
});

describe("ListPage saved views", () => {
  async function setUp(storage: MemoryStorage = memory()) {
    const user = userEvent.setup();
    const view = renderList(storage);
    return { user, ...view };
  }

  async function saveAs(user: ReturnType<typeof userEvent.setup>, name: string) {
    await user.click(screen.getByRole("button", { name: "Views" }));
    await user.type(screen.getByLabelText("View name"), name);
    await user.click(screen.getByRole("button", { name: "Save as new view" }));
  }

  it("saves the filters, sort and columns under a name and restores them after a reload", async () => {
    const { user, storage } = await setUp();
    await user.selectOptions(screen.getByLabelText("Status"), "PAID");
    await user.click(screen.getByRole("button", { name: "Apply filters" }));
    await user.click(screen.getByRole("button", { name: "Amount" }));
    await user.click(screen.getByRole("button", { name: "Columns" }));
    await user.click(screen.getByRole("checkbox", { name: "Status" }));
    await user.click(screen.getByRole("button", { name: "Columns" }));
    await saveAs(user, "Paid by amount");
    expect(screen.getAllByRole("status").some((s) => s.textContent === "Saved view Paid by amount")).toBe(true);

    const saved = JSON.parse(storage.data[viewStoreKey("ada", "invoices")]);
    expect(saved.version).toBe(1);
    expect(saved.views[0]).toMatchObject({ name: "Paid by amount", query: { filters: { status: "PAID" }, sort: { key: "amount", direction: "ascending" } }, visible: ["number", "amount"] });

    cleanup();
    seen = [];
    renderList(storage);
    await user.click(screen.getByRole("button", { name: "Views" }));
    await user.click(screen.getByRole("button", { name: "Show Paid by amount" }));
    expect(last()).toEqual({ filters: { status: "PAID" }, sort: { key: "amount", direction: "ascending" } });
    expect(screen.queryByRole("columnheader", { name: "Status" })).toBeNull();
    expect(screen.getByLabelText("Status")).toHaveValue("PAID");
  });

  it("applies the default view on arrival and can change or clear the default", async () => {
    const storage = memory();
    const first = await setUp(storage);
    await first.user.selectOptions(screen.getByLabelText("Status"), "OPEN");
    await first.user.click(screen.getByRole("button", { name: "Apply filters" }));
    await saveAs(first.user, "Open ones");
    await first.user.click(screen.getByRole("button", { name: "Make Open ones the default" }));
    expect(screen.getAllByRole("status").some((s) => s.textContent === "Open ones is now your default view")).toBe(true);
    expect(screen.getByText("Default")).toBeInTheDocument();
    first.unmount();

    seen = [];
    const user = userEvent.setup();
    renderList(storage);
    expect(seen[0].filters).toEqual({ status: "OPEN" });
    expect(screen.getByRole("list", { name: "Active filters" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Views" }));
    await user.click(screen.getByRole("button", { name: "Stop using Open ones as the default" }));
    expect(JSON.parse(storage.data[viewStoreKey("ada", "invoices")]).defaultId).toBeNull();
  });

  it("offers to update the active view only after it changes, then updates it", async () => {
    const { user, storage } = await setUp();
    await saveAs(user, "Everything");
    expect(screen.queryByRole("button", { name: "Update Everything" })).toBeNull();
    await user.click(screen.getByRole("button", { name: "Number" }));
    await user.click(screen.getByRole("button", { name: "Views" }));
    expect(screen.getByRole("button", { name: "Update Everything" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Update Everything" }));
    expect(JSON.parse(storage.data[viewStoreKey("ada", "invoices")]).views[0].query.sort).toEqual({ key: "number", direction: "ascending" });
    expect(screen.queryByRole("button", { name: "Update Everything" })).toBeNull();
  });

  it("deletes a view and drops it as the default", async () => {
    const { user, storage } = await setUp();
    await saveAs(user, "Temp");
    await user.click(screen.getByRole("button", { name: "Make Temp the default" }));
    await user.click(screen.getByRole("button", { name: "Delete Temp" }));
    expect(screen.getByText(/No saved views yet/)).toBeInTheDocument();
    expect(JSON.parse(storage.data[viewStoreKey("ada", "invoices")])).toMatchObject({ views: [], defaultId: null });
    expect(screen.getByRole("group", { name: "Views" })).toHaveFocus();
  });

  it("restores a deleted view, and its place as the default, with Undo", async () => {
    const { user, storage } = await setUp();
    await saveAs(user, "Temp");
    await user.click(screen.getByRole("button", { name: "Make Temp the default" }));
    await user.click(screen.getByRole("button", { name: "Delete Temp" }));
    await user.click(screen.getByRole("button", { name: "Undo" }));
    expect(screen.getByText("Restored view Temp")).toBeInTheDocument();
    expect(JSON.parse(storage.data[viewStoreKey("ada", "invoices")])).toMatchObject({ views: [{ name: "Temp" }], defaultId: expect.any(String) });
    expect(screen.queryByRole("button", { name: "Undo" })).toBeNull();
  });

  it("asks for a name and keeps views per person and per list", async () => {
    const { user, storage } = await setUp();
    await user.click(screen.getByRole("button", { name: "Views" }));
    await user.click(screen.getByRole("button", { name: "Save as new view" }));
    expect(screen.getByText("Give the view a name.")).toBeInTheDocument();
    expect(storage.data).toEqual({});
    expect(viewStoreKey("ada", "invoices")).not.toBe(viewStoreKey("grace", "invoices"));
    expect(viewStoreKey("ada", "invoices")).not.toBe(viewStoreKey("ada", "orders"));
  });

  it("opens an old view cleanly when its column set has changed", async () => {
    const old = {
      version: 1,
      defaultId: "v1",
      views: [{ id: "v1", name: "Old", query: { filters: { status: "PAID", retired: "1" }, sort: { key: "retired", direction: "ascending" } }, visible: ["number", "retired"], known: ["number", "retired", "amount", "status", "note"] }],
    };
    renderList(memory({ [viewStoreKey("ada", "invoices")]: JSON.stringify(old) }));
    expect(screen.getAllByRole("columnheader").map((h) => h.textContent)).toEqual(["Number"]);
    expect(last()).toEqual({ filters: { status: "PAID" }, sort: null });
  });

  it("opens on the list's working set without a default view, and never over a default view the person saved", () => {
    renderList(memory(), { defaultFilters: { status: "OPEN" } });
    expect(last()).toEqual({ filters: { status: "OPEN" }, sort: null });
    cleanup();
    const saved = {
      version: 1,
      defaultId: "v1",
      views: [{ id: "v1", name: "Everything", query: { filters: {}, sort: null }, visible: ["number", "amount", "status"], known: ["number", "amount", "status", "note"] }],
    };
    renderList(memory({ [viewStoreKey("ada", "invoices")]: JSON.stringify(saved) }), { defaultFilters: { status: "OPEN" } });
    expect(last()).toEqual({ filters: {}, sort: null });
  });

  it("opens on the filters a link asked for, over the default view's filters but with its columns", () => {
    const saved = {
      version: 1,
      defaultId: "v1",
      views: [{ id: "v1", name: "Paid", query: { filters: { status: "PAID" }, sort: null }, visible: ["number", "status"], known: ["number", "amount", "status", "note"] }],
    };
    renderList(memory({ [viewStoreKey("ada", "invoices")]: JSON.stringify(saved) }), { initialFilters: { status: "OPEN" }, defaultFilters: { status: "PAID" } });
    expect(last()).toEqual({ filters: { status: "OPEN" }, sort: null });
    expect(screen.getAllByRole("columnheader").map((h) => h.textContent)).toEqual(["Number", "Status"]);
  });

  it("starts empty from a record it cannot read, and says when the browser will not keep a view", async () => {
    const user = userEvent.setup();
    renderList(memory({ [viewStoreKey("ada", "invoices")]: JSON.stringify({ version: 99, views: [{}] }) }, true));
    expect(screen.getAllByRole("columnheader")).toHaveLength(3);
    await saveAs(user, "Lost");
    expect(screen.getAllByRole("status").some((s) => /would not keep the view/.test(s.textContent ?? ""))).toBe(true);
  });
});

describe("ListPage CSV export", () => {
  let blob: Blob | undefined;
  let name: string | undefined;

  beforeEach(() => {
    blob = undefined;
    name = undefined;
    URL.createObjectURL = vi.fn((b: Blob) => {
      blob = b;
      return "blob:test";
    });
    URL.revokeObjectURL = vi.fn();
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
      name = this.download;
    });
  });

  const bytes = () =>
    new Promise<Uint8Array>((resolve) => {
      const reader = new FileReader();
      reader.onload = () => resolve(new Uint8Array(reader.result as ArrayBuffer));
      reader.readAsArrayBuffer(blob as Blob);
    });
  const text = async () => new TextDecoder("utf-8", { ignoreBOM: true }).decode(await bytes());

  it("writes the visible columns of the loaded rows, quoted and formula-safe, with a byte order mark", async () => {
    const user = userEvent.setup();
    renderList();
    await user.click(screen.getByRole("button", { name: "Export CSV" }));
    await waitFor(() => expect(blob).toBeDefined());
    const body = await text();
    expect([...(await bytes()).slice(0, 3)]).toEqual([0xef, 0xbb, 0xbf]);
    expect(body.slice(1).split("\r\n")).toEqual(["Number,Amount,Status", "INV-10,9.50,OPEN", "INV-9,120.00,PAID", "'=EVIL(),-15.00,\"OPEN, late\"", ""]);
    expect(name).toMatch(/^invoices-\d{4}-\d{2}-\d{2}\.csv$/);
    expect(screen.getAllByRole("status").some((s) => s.textContent === "Exported 3 invoices to a CSV file.")).toBe(true);
  });

  it("follows the current sort and columns, and uses a column's own export value", async () => {
    const user = userEvent.setup();
    renderList(memory(), { columns: columns.map((c) => (c.key === "status" ? { ...c, csv: (r: Row) => r.status.toLowerCase() } : c)) });
    await user.click(screen.getByRole("button", { name: "Number" }));
    await user.click(screen.getByRole("button", { name: "Columns" }));
    await user.click(screen.getByRole("checkbox", { name: "Amount" }));
    await user.keyboard("{Escape}");
    await user.click(screen.getByRole("button", { name: "Export CSV" }));
    await waitFor(() => expect(blob).toBeDefined());
    expect((await text()).slice(1).split("\r\n").slice(0, 3)).toEqual(["Number,Status", "'=EVIL(),\"open, late\"", "INV-9,paid"]);
  });

  it("says plainly when more rows exist than were exported", async () => {
    source = { hasMore: true };
    const user = userEvent.setup();
    renderList();
    await user.click(screen.getByRole("button", { name: "Export CSV" }));
    await waitFor(() => expect(blob).toBeDefined());
    expect(screen.getAllByRole("status").some((s) => s.textContent === "Exported the 3 invoices loaded so far. More exist that are not in the file; use Load more first to include them.")).toBe(true);
  });

  it("always exports the currency column beside an amount that names it, even while hidden", async () => {
    const withCurrency: ListColumn<Row>[] = [
      { key: "number", header: "Number" },
      { key: "amount", header: "Amount", csvWith: "currency" },
      { key: "currency", header: "Currency", defaultHidden: true, sortValue: () => "USD", csv: () => "USD" },
    ];
    const user = userEvent.setup();
    renderList(memory(), { columns: withCurrency });
    await user.click(screen.getByRole("button", { name: "Export CSV" }));
    await waitFor(() => expect(blob).toBeDefined());
    expect((await text()).slice(1).split("\r\n")[0]).toBe("Number,Amount,Currency");
  });

  it("downloads nothing when there are no rows", async () => {
    source = { rows: [] };
    const user = userEvent.setup();
    renderList();
    await user.click(screen.getByRole("button", { name: "Export CSV" }));
    expect(blob).toBeUndefined();
    expect(screen.getAllByRole("status").some((s) => s.textContent === "There are no rows to export.")).toBe(true);
  });
});

describe("ListPage accessibility", () => {
  it("has no WCAG AA violations with rows, with panels open, and when empty", async () => {
    const user = userEvent.setup();
    const { container } = renderList();
    expect(pageViolations(container)).toEqual([]);
    await user.click(screen.getByRole("button", { name: "Columns" }));
    expect(pageViolations(container)).toEqual([]);
    await user.click(screen.getByRole("button", { name: "Views" }));
    expect(pageViolations(container)).toEqual([]);
  });

  it("has no violations in the empty, filtered-empty and error states", async () => {
    source = { rows: [], error: { message: "boom", code: "X-1" } };
    const failed = renderList();
    expect(pageViolations(failed.container)).toEqual([]);
    failed.unmount();
    source = { rows: [] };
    const empty = renderList();
    expect(pageViolations(empty.container)).toEqual([]);
    await act(async () => {
      fireEvent.change(screen.getByLabelText("Search"), { target: { value: "zzz" } });
      fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    });
    expect(screen.getByRole("heading", { name: "No invoices match these filters" })).toBeInTheDocument();
    expect(pageViolations(empty.container)).toEqual([]);
  });

  it("labels the table, the filter form and the tool group", () => {
    renderList();
    expect(screen.getByRole("table", { name: "Invoices" })).toBeInTheDocument();
    expect(screen.getByRole("search", { name: "Filter invoices" })).toBeInTheDocument();
    expect(screen.getByRole("group", { name: "List tools" })).toBeInTheDocument();
  });
});

describe("ListPage search box", () => {
  const search = (row: Row) => ({ number: row.number, name: row.status });

  it("offers no search box unless the page says what a row is searched by", () => {
    renderList();
    expect(screen.queryByRole("searchbox", { name: "Search invoices" })).toBeNull();
  });

  it("finds a number by its start and a name by what it holds, the way the record search does", async () => {
    const user = userEvent.setup();
    renderList(memory(), { search });
    const box = screen.getByRole("searchbox", { name: "Search invoices" });
    await user.type(box, "INV-1");
    expect(bodyNumbers()).toEqual(["INV-10"]);
    await user.clear(box);
    await user.type(box, "paid");
    expect(bodyNumbers()).toEqual(["INV-9"]);
    await user.clear(box);
    expect(bodyNumbers()).toEqual(["INV-10", "INV-9", "=EVIL()"]);
  });

  it("says so when nothing matches, and clears the search from the empty state", async () => {
    const user = userEvent.setup();
    renderList(memory(), { search });
    await user.type(screen.getByRole("searchbox", { name: "Search invoices" }), "INV-77");
    expect(screen.getByRole("heading", { name: "No invoices match this search" })).toBeInTheDocument();
    await user.click(screen.getAllByRole("button", { name: "Clear search" })[0]);
    expect(bodyNumbers()).toEqual(["INV-10", "INV-9", "=EVIL()"]);
  });

  it("says the search covers the rows loaded so far when more are available", async () => {
    const user = userEvent.setup();
    source = { hasMore: true };
    renderList(memory(), { search });
    await user.type(screen.getByRole("searchbox", { name: "Search invoices" }), "INV-1");
    expect(screen.getByText(/Searching the 3 rows loaded so far/)).toBeInTheDocument();
  });

  it("has no accessibility violations with the search box", () => {
    const { container } = renderList(memory(), { search });
    expect(pageViolations(container)).toEqual([]);
  });
});


describe("ListPage data designation", () => {
  const designation = { entityType: "invoice", idOf: (row: Row) => row.id };
  let resolveBodies: { refs: { entityType: string; recordId: string }[] }[];

  function stubResolver(cuiIds: string[], status = 200) {
    resolveBodies = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (_input: string, init: RequestInit = {}) => {
        const body = JSON.parse(init.body as string) as (typeof resolveBodies)[number];
        resolveBodies.push(body);
        const items = body.refs.map((r) => ({ ...r, class: cuiIds.includes(r.recordId) ? "CUI" : "NONE", source: "RECORD" }));
        return new Response(JSON.stringify(status === 200 ? { items } : { code: "AUTH-0003" }), { status, headers: { "Content-Type": "application/json" } });
      }),
    );
  }

  it("adds no column, filter or resolver call without the prop", () => {
    stubResolver([]);
    renderList();
    expect(screen.queryByRole("columnheader", { name: "Data designation" })).toBeNull();
    expect(screen.queryByLabelText("Data designation")).toBeNull();
    expect(resolveBodies).toHaveLength(0);
  });

  it("shows the column from one batch call for the whole page and keeps it out of sorting", async () => {
    stubResolver(["2"]);
    const { container } = renderList(memory(), { designation });
    const table = screen.getByRole("table", { name: "Invoices" });
    await waitFor(() => expect(within(table).getByText("CUI")).toBeInTheDocument());
    expect(within(table).getAllByText("None")).toHaveLength(2);
    expect(resolveBodies).toHaveLength(1);
    expect(resolveBodies[0]!.refs.map((r) => r.recordId)).toEqual(["1", "2", "3"]);
    expect(within(screen.getByRole("columnheader", { name: "Data designation" })).queryByRole("button")).toBeNull();
    expect(pageViolations(container)).toEqual([]);
  });

  it("asks only for the new rows when another page loads, never per row", async () => {
    stubResolver([]);
    source = { rows: rows.slice(0, 2) };
    const { rerender, tree } = renderList(memory(), { designation });
    await waitFor(() => expect(resolveBodies).toHaveLength(1));
    source = { rows };
    rerender(tree());
    await waitFor(() => expect(resolveBodies).toHaveLength(2));
    expect(resolveBodies[1]!.refs.map((r) => r.recordId)).toEqual(["3"]);
  });

  it("filters the loaded rows to CUI and offers only All and CUI", async () => {
    const user = userEvent.setup();
    stubResolver(["2"]);
    renderList(memory(), { designation });
    await waitFor(() => expect(screen.getByText("CUI")).toBeInTheDocument());
    const select = screen.getByLabelText("Data designation");
    expect(within(select).getAllByRole("option").map((o) => o.textContent)).toEqual(["All", "CUI"]);
    await user.selectOptions(select, "CUI");
    await user.click(screen.getByRole("button", { name: "Apply filters" }));
    expect(bodyNumbers()).toEqual(["INV-9"]);
    expect(last().filters.designation).toBe("CUI");
  });

  it("keeps the rows and says the designation is unavailable when the resolver refuses", async () => {
    stubResolver([], 403);
    renderList(memory(), { designation });
    expect((await screen.findAllByText("Designation unavailable")).length).toBe(3);
    expect(bodyNumbers()).toEqual(["INV-10", "INV-9", "=EVIL()"]);
  });

  it("offers no way to hide the designation column and keeps it when a saved view lacks it", async () => {
    const user = userEvent.setup();
    stubResolver([]);
    const saved = { version: 1, defaultId: "v", views: [{ id: "v", name: "Slim", query: { filters: {}, sort: null }, visible: ["number"], known: ["number", "amount", "status"] }] };
    renderList(memory({ [viewStoreKey("ada", "invoices")]: JSON.stringify(saved) }), { designation });
    expect(screen.getByRole("columnheader", { name: "Data designation" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Columns" }));
    expect(screen.queryByRole("checkbox", { name: "Data designation" })).toBeNull();
  });

  describe("CSV marking", () => {
    let blob: Blob | undefined;

    beforeEach(() => {
      blob = undefined;
      URL.createObjectURL = vi.fn((b: Blob) => {
        blob = b;
        return "blob:test";
      });
      URL.revokeObjectURL = vi.fn();
      vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined);
    });

    const exported = async () => {
      await userEvent.setup().click(screen.getByRole("button", { name: "Export CSV" }));
      await waitFor(() => expect(blob).toBeDefined());
      return (await (blob as Blob).text()).replace("\uFEFF", "").split("\r\n");
    };

    it("starts the file with the CUI notice and marks each CUI row in a trailing column", async () => {
      stubResolver(["2"]);
      renderList(memory(), { designation });
      await waitFor(() => expect(screen.getByText("CUI")).toBeInTheDocument());
      const lines = await exported();
      expect(lines[0]).toBe("CUI,Contains CUI. Handle per your CUI policy.");
      expect(lines[1]).toBe("Number,Amount,Status,Data designation,Marking");
      expect(lines.slice(2, 5).map((l) => l.split(",").slice(-2).join(","))).toEqual([",", "CUI,CUI", ","]);
    });

    it("writes no notice when no row is CUI", async () => {
      stubResolver([]);
      renderList(memory(), { designation });
      await waitFor(() => expect(screen.getAllByText("None")).toHaveLength(3));
      const lines = await exported();
      expect(lines[0]).toBe("Number,Amount,Status,Data designation,Marking");
    });

    it("marks every row CUI when the resolver is unavailable", async () => {
      stubResolver([], 403);
      renderList(memory(), { designation });
      await screen.findAllByText("Designation unavailable");
      const lines = await exported();
      expect(lines[0]).toBe("CUI,Contains CUI. Handle per your CUI policy.");
      expect(lines.slice(2, 5).every((l) => l.endsWith("CUI,CUI"))).toBe(true);
    });
  });
});

describe("loadedRows", () => {
  it("reads a 403 as no access, whichever way the client carries it", async () => {
    const { loadedRows, isForbidden } = await import("./ListPage");
    expect(isForbidden({ status: 403 })).toBe(true);
    expect(isForbidden({ code: "AUTH-0003", message: "missing duty" })).toBe(true);
    expect(isForbidden({ code: "HTTP-403" })).toBe(true);
    expect(isForbidden({ code: "COM-0500" })).toBe(false);
    expect(loadedRows({ data: null, loading: false, error: { code: "AUTH-0003" }, reload: () => undefined }).forbidden).toBe(true);
    expect(loadedRows({ data: [1], loading: false, reload: () => undefined })).toMatchObject({ rows: [1], forbidden: false, hasMore: false });
  });
});

describe("ListPage bulk actions and paste", () => {
  const ok = (keys: string[]): BulkResult => ({ done: keys, failed: [] });
  const bulkProps = (run: BulkAction<Row>["run"], extra: Partial<BulkAction<Row>> = {}) => ({
    bulk: [{ id: "pay", label: "Pay selected", run, ...extra }],
    rowLabel: (r: Row) => r.number,
  });
  const rowBox = (name: string) => screen.getByRole("checkbox", { name: `Select ${name}` });

  it("shows no checkboxes and no bar without bulk, and no bar until a row is picked", async () => {
    renderList();
    expect(screen.queryAllByRole("checkbox")).toEqual([]);
    cleanup();
    renderList(memory(), bulkProps(vi.fn()));
    expect(screen.queryByRole("region", { name: "Actions for the selected rows" })).toBeNull();
    await userEvent.setup().click(rowBox("INV-10"));
    expect(screen.getByRole("region", { name: "Actions for the selected rows" })).toBeInTheDocument();
  });

  it("hands run only the selected rows and reports the count", async () => {
    const user = userEvent.setup();
    const run = vi.fn(async (picked: Row[]) => ok(picked.map((r) => r.id)));
    renderList(memory(), bulkProps(run));
    await user.click(rowBox("INV-10"));
    await user.click(rowBox("=EVIL()"));
    expect(screen.getAllByText("2 of 3 invoices selected").length).toBeGreaterThan(0);
    await user.click(screen.getByRole("button", { name: "Pay selected" }));
    expect(run).toHaveBeenCalledTimes(1);
    expect(run.mock.calls[0][0].map((r) => r.id)).toEqual(["1", "3"]);
    expect(await screen.findByText("Done for 2 invoices.")).toBeInTheDocument();
  });

  it("selects only the shown rows with the header checkbox and sets it indeterminate for a partial pick", async () => {
    const user = userEvent.setup();
    const run = vi.fn(async (picked: Row[]) => ok(picked.map((r) => r.id)));
    renderList(memory(), { ...bulkProps(run), search: (r: Row) => ({ number: r.number }) });
    await user.click(rowBox("INV-10"));
    expect((screen.getByRole("checkbox", { name: "Select all rows" }) as HTMLInputElement).indeterminate).toBe(true);
    await user.type(screen.getByRole("searchbox", { name: "Search invoices" }), "INV-9");
    await user.click(screen.getByRole("checkbox", { name: "Select all rows" }));
    await user.click(screen.getByRole("button", { name: "Pay selected" }));
    expect(run.mock.calls[0][0].map((r) => r.id)).toEqual(["2"]);
  });

  it("keeps only the failed rows selected and lists each reason", async () => {
    const user = userEvent.setup();
    const run = vi.fn(async (): Promise<BulkResult> => ({ done: ["1", "2"], failed: [{ key: "3", reason: "On hold" }] }));
    renderList(memory(), bulkProps(run));
    await user.click(screen.getByRole("checkbox", { name: "Select all rows" }));
    await user.click(screen.getByRole("button", { name: "Pay selected" }));
    expect(await screen.findByText("=EVIL(): On hold")).toBeInTheDocument();
    expect(screen.getByText("2 done, 1 not changed. The rows that failed are still selected.")).toBeInTheDocument();
    expect(rowBox("=EVIL()")).toBeChecked();
    expect(rowBox("INV-10")).not.toBeChecked();
  });

  it("shows a rejected run as a problem, keeps the selection and offers no undo", async () => {
    const user = userEvent.setup();
    const run = vi.fn(async (): Promise<BulkResult> => {
      throw { status: 403, code: "AUTH-0003", message: "missing duty" };
    });
    renderList(memory(), bulkProps(run, { undo: vi.fn() }));
    await user.click(rowBox("INV-10"));
    await user.click(screen.getByRole("button", { name: "Pay selected" }));
    expect(await screen.findByText("That did not finish")).toBeInTheDocument();
    expect(rowBox("INV-10")).toBeChecked();
    expect(screen.queryByRole("button", { name: "Undo" })).toBeNull();
  });

  it("treats a result without done and failed arrays as a failure", async () => {
    const user = userEvent.setup();
    renderList(memory(), bulkProps((async () => undefined) as unknown as BulkAction<Row>["run"]));
    await user.click(rowBox("INV-10"));
    await user.click(screen.getByRole("button", { name: "Pay selected" }));
    expect(await screen.findByText("That did not finish")).toBeInTheDocument();
  });

  it("asks first when the action has a confirmation and runs nothing on cancel", async () => {
    const user = userEvent.setup();
    const run = vi.fn(async (picked: Row[]) => ok(picked.map((r) => r.id)));
    renderList(memory(), bulkProps(run, { confirm: { title: "Pay 1 invoice?", description: "This cannot be undone.", confirmLabel: "Pay" } }));
    await user.click(rowBox("INV-10"));
    await user.click(screen.getByRole("button", { name: "Pay selected" }));
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(run).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Pay selected" }));
    await user.click(screen.getByRole("button", { name: "Pay" }));
    expect(run).toHaveBeenCalledTimes(1);
  });

  it("drops a selected row that a reload removes, so it never reaches run", async () => {
    const user = userEvent.setup();
    const run = vi.fn(async (picked: Row[]) => ok(picked.map((r) => r.id)));
    const { rerender, tree } = renderList(memory(), bulkProps(run));
    await user.click(rowBox("INV-10"));
    await user.click(rowBox("INV-9"));
    source = { rows: rows.slice(1) };
    rerender(tree());
    await user.click(screen.getByRole("button", { name: "Pay selected" }));
    expect(run.mock.calls[0][0].map((r) => r.id)).toEqual(["2"]);
  });

  it("undoes a bulk run once, and announces an undo that fails", async () => {
    const user = userEvent.setup();
    const undo = vi.fn(async () => undefined);
    renderList(memory(), bulkProps(async (picked) => ok(picked.map((r) => r.id)), { undo }));
    await user.click(rowBox("INV-10"));
    await user.click(screen.getByRole("button", { name: "Pay selected" }));
    await user.click(await screen.findByRole("button", { name: "Undo" }));
    expect(undo).toHaveBeenCalledWith(["1"]);
    expect(await screen.findByText("Undone.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Undo" })).toBeNull();
    cleanup();

    const failing = vi.fn(async () => {
      throw new Error("Already paid");
    });
    renderList(memory(), bulkProps(async (picked) => ok(picked.map((r) => r.id)), { undo: failing }));
    await user.click(rowBox("INV-10"));
    await user.click(screen.getByRole("button", { name: "Pay selected" }));
    await user.click(await screen.findByRole("button", { name: "Undo" }));
    expect(await screen.findByText("Could not undo")).toBeInTheDocument();
    expect(screen.queryByText("Undone.")).toBeNull();
  });

  it("offers no undo for an action that supplies none", async () => {
    const user = userEvent.setup();
    renderList(memory(), bulkProps(async (picked) => ok(picked.map((r) => r.id))));
    await user.click(rowBox("INV-10"));
    await user.click(screen.getByRole("button", { name: "Pay selected" }));
    await screen.findByText("Done for 1 invoice.");
    expect(screen.queryByRole("button", { name: "Undo" })).toBeNull();
  });

  it("renders no table, checkboxes or bar when the list is forbidden", () => {
    source = { rows: [], forbidden: true };
    renderList(memory(), bulkProps(vi.fn()));
    expect(screen.queryAllByRole("checkbox")).toEqual([]);
    expect(screen.queryByRole("region", { name: "Actions for the selected rows" })).toBeNull();
  });

  it("has no accessibility violations with a selection and the bar showing", async () => {
    const user = userEvent.setup();
    const { container } = renderList(memory(), bulkProps(vi.fn()));
    await user.click(rowBox("INV-10"));
    expect(pageViolations(container)).toEqual([]);
  });

  it("applies a valid paste once and refuses to apply while a row is invalid", async () => {
    const user = userEvent.setup();
    const onCommit = vi.fn(async (pasted: Record<string, unknown>[]) => ok(pasted.map((_, i) => String(i))));
    const paste = {
      fields: [
        { key: "number", header: "Number" },
        { key: "amount", header: "Amount", parse: (cell: string) => Number(cell) },
      ],
      onCommit,
    };
    renderList(memory(), { paste });
    await user.click(screen.getByRole("button", { name: "Paste rows" }));
    const dialog = screen.getByRole("dialog", { name: "Paste rows from a spreadsheet" });
    fireEvent.change(within(dialog).getByRole("textbox", { name: "Pasted rows" }), { target: { value: "INV-1\tabc\nINV-2\t5" } });
    expect(within(dialog).getByRole("button", { name: "Apply 2 rows" })).toBeDisabled();
    expect(within(dialog).getByText("Amount is not a number")).toBeInTheDocument();
    fireEvent.change(within(dialog).getByRole("textbox", { name: "Pasted rows" }), { target: { value: "Number\tAmount\nINV-1\t4\nINV-2\t5" } });
    await user.click(within(dialog).getByRole("button", { name: "Apply 2 rows" }));
    expect(onCommit).toHaveBeenCalledWith([
      { number: "INV-1", amount: 4 },
      { number: "INV-2", amount: 5 },
    ]);
    expect(await screen.findByText("Done for 2 invoices.")).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("closes the paste dialog with Escape and returns focus to the button", async () => {
    const user = userEvent.setup();
    renderList(memory(), { paste: { fields: [{ key: "number", header: "Number" }], onCommit: vi.fn() } });
    const open = screen.getByRole("button", { name: "Paste rows" });
    await user.click(open);
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(open).toHaveFocus();
  });

  it("has no accessibility violations with the paste dialog and a preview open", async () => {
    const user = userEvent.setup();
    const { container } = renderList(memory(), { paste: { fields: [{ key: "number", header: "Number" }], onCommit: vi.fn() } });
    await user.click(screen.getByRole("button", { name: "Paste rows" }));
    fireEvent.change(screen.getByRole("textbox", { name: "Pasted rows" }), { target: { value: "INV-1" } });
    expect(pageViolations(container)).toEqual([]);
  });
});
