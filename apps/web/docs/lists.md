# List standard

`ListPage` in `src/shell/ui/ListPage.tsx` is the one list screen. It sits on `DataTable` and is exported from `src/shell/ui/index.ts`. Use it for every new list; roll existing lists over with the checklist below (#505).

## What a page supplies

```tsx
<ListPage
  id="ap-invoices"            // storage key part; stable and unique per list
  title="Vendor invoices"     // page title and table label
  noun="vendor invoices"      // used in sentences: "12 vendor invoices loaded"
  columns={columns}           // ListColumn<T>[]
  filters={filters}           // ListFilterDef[]
  useRows={useInvoiceRows}    // hook: (query) => ListData<T>
  getRowKey={(row) => row.id}
  empty={{ title, description, action }}
/>
```

- **`useRows(query)`** is the page's own fetcher, called by `ListPage` with `{ filters, sort }`. Keep your existing data code inside it (react-query `useInfiniteQuery`, `useResource`, anything). It returns `{ rows, loading, loadingMore, error, forbidden, hasMore, loadMore, retry }`. Key the fetch on `query.filters` only unless the API sorts (see below), so a sort change does not refetch.
- **`columns`** are `DataTableColumn` plus `sortValue(row)` (what to sort by when the cell is rendered markup), `csv(row)` (what to export; may be async, use `entityText("vendor", id)` for names) and `defaultHidden`. A column with a rendered name that is really an id (a vendor link) sets `sortable: false`.
- **`filters`** are declarative: `text`, `select` (with `options` and optional `anyLabel`), `dateRange` (values `${key}From` and `${key}To`) and `entity` (the shared lookups from `shell/lookup`). Values reach `useRows` as strings in `query.filters`; empty ones are left out. Add only filters the API accepts.
- **`empty`** is the first-run state: say what the list is for and give the next step as `action`. The filtered-empty state and its "Clear filters" button are built in.
- **`serverSort`** lists the column keys the API sorts. Those get `query.sort` passed to `useRows` and the rows are shown as returned. Every other column sorts the rows loaded so far, and the page says so while more rows exist ("Sorted within the N rows loaded so far").

## Behaviour you get

- **Filters** apply with the "Apply filters" button. Applied filters show as chips (remove one, or "Clear all filters").
- **Sorting**: header buttons cycle ascending, descending, none. `aria-sort` is set on the header.
- **Columns**: the Columns menu shows or hides columns, keeps at least one, and resets to the defaults.
- **Views**: the Views menu saves the current filters, sort and columns under a name, updates or deletes a view, and marks one as the default (applied when the list opens). Views live behind `PreferenceStorage` (`src/shell/preferenceStorage.ts`), keyed `passage.list.v1.{person}.{list id}` and stored as `{ version: 1, views, defaultId }`. A record of another version, or an unreadable one, is ignored. A saved view remembers which columns existed when it was saved, so a column added later appears and a removed one is dropped without breaking the view. To move views to a server, implement `PreferenceStorage` over the API and pass it as `storage`; no page changes.
- **Export CSV** writes the visible columns of the rows loaded now, in the order shown, with a UTF-8 byte order mark, quoted fields and `CRLF` line ends. A cell that starts with `=`, `+`, `-` or `@` (or a tab or return) gets a leading `'` so a spreadsheet does not run it; a plain number such as `-15.00` is left alone. When more rows exist the page says the file holds only the loaded rows.
- **States**: loading (busy table), first-run empty with a next step, filtered-empty with "Clear filters", error with "Try again" (the rows already loaded stay), and no access (403) with no retry and no list tools.
- **Paging**: keyset "Load more {noun}" under the table.
- **Density** is the shell's Compact view button; it is remembered per person (`passage.density.v1.{person}`) and every table follows it.

## Text search

A list's text search finds what the command palette finds: both follow the record search service (`services/search`, #506). The rules, on the trimmed, lower-cased query:

1. Under three characters, or shaped like a number (it has a digit, no space and no run of more than three letters: `po-10`, `inv-2026`, `1042`): the record's number starts with it. Nothing else matches, so `1042` does not find `PO-1042`.
2. Anything else is a name query. It matches when the number and name together hold it (`toso` finds Contoso), or when it is close enough to a stretch of them to forgive a typo (`contsoo` finds Contoso): trigram word similarity of 0.4 or more, as `pg_trgm`'s `word_similarity` counts it.
3. When rows are ranked rather than kept in list order: the exact number first, then numbers that start with the query, then the most similar, then by number.

- **Rows filtered in the browser** (a `text` filter the page applies to the rows it loaded, a picker over a short list): use `filterBySearch(rows, text, (row) => ({ number, name }))` from `src/shell/search/match.ts` inside `useRows`. It keeps the list's order; `rankBySearch` returns the best match first. Do not write a `toLowerCase().includes()` filter of your own.
- **Rows filtered by the API** (`q` on a list route): the route should apply the same three rules to the record's number and name. In Postgres that is a range on the lower-cased number for rule 1 and `LIKE` plus `word_similarity` at 0.4 for rule 2; `plan` in `services/search/internal/store/store.go` is the reference. Until a route is brought in line, say in its OpenAPI description how it matches.

`services/search/internal/store/testdata/matching.json` states what a query finds, case by case. The service's store test runs it against Postgres and `match.test.ts` runs it against the browser matcher, so the two cannot drift apart.

## Accessibility

- The table keeps `DataTable` semantics: `th scope="col"`, sort state in `aria-sort`, a labelled region when it scrolls sideways.
- The filter bar is a labelled `search` form; fields use `FormField` labels. Filters are search inputs, so they carry no Required or Optional marks.
- Columns and Views are disclosure buttons (`aria-expanded`) that open a labelled group. Focus moves into the group on open, Escape closes it and returns focus to the button, and tabbing out or pressing elsewhere closes it.
- The count line ("3 vendor invoices loaded, more available") and the action message line (view saved, export done) are polite live regions that stay mounted.
- Tests: `src/shell/ui/ListPage.test.tsx` covers each feature and runs the in-repo AA checks.

## Examples

- `src/modules/ap-invoices/InvoicesPage.tsx` (react-query infinite query, server filters).
- `src/modules/purchasing/PurchaseOrderPages.tsx` (`PurchaseOrdersPage`, `useResource` with an accumulating cursor).

## Rollout checklist (505-3)

For each list (purchase orders, receipts, vendor invoices, sales orders, packing slips, billing invoices, AR payments):

1. Move the fetch into a `use...Rows(query)` hook that returns `ListData<T>`. Do not change what is requested for the filters the page already had. Set `forbidden` from a 403 so the no-access state shows.
2. Turn the page's filters into `ListFilterDef`s. Add a filter only when the API takes the parameter.
3. Convert the columns to `ListColumn`. Give rendered columns a `sortValue` and `csv`; mark id-only link columns `sortable: false`; hide technical columns with `defaultHidden`.
4. Pick a unique `id` and a plain `noun`. Write the first-run `empty` text with a next-step action.
5. Replace the page header, filter form, table, empty and error states and the load-more button with `<ListPage />`. Keep the "create" action in `actions`.
6. Keep the existing wording the page's tests assert, or update the test to the standard wording ("Load more {noun}", "Could not load {noun}").
7. If the API sorts, add the column keys to `serverSort` and send `query.sort` in the request.
8. Run the page tests, `npx tsc --noEmit`, and the route's axe check.
