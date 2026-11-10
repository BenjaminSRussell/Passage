# UX writing style guide

How the web app talks to people. It covers every string a person can read or hear: page titles and descriptions, labels, hints, buttons, empty states, notices, error messages and accessible names. The design system's microcopy list (`design-system.md`, section 7) applies these rules to the shell.

## Voice

- **Plain and direct.** Write for a finance clerk, buyer or warehouse lead on a busy day, not for the engineer who built the screen. One idea per sentence. Prefer short, common words: "use" over "utilize", "about" over "regarding".
- **Second person, active voice.** "You can't approve your own requisition", not "Self-approval is not permitted".
- **Calm and specific.** No blame, no exclamation marks, no "Oops". Say what is true and what to do.
- **Describe what the person sees and does, not how it was built.** Never cite spec ids (FR-25.3, BR-25.3, AC-31.14), delivery waves or slices, sagas, idempotency keys, ETags or HTTP status codes. If a reference helps the next developer, put it in a code comment next to the string. `src/shell/plainLanguage.test.ts` fails the build when a spec id, wave or slice reaches user-facing text.
- **Say "isn't available yet" for missing features**, not which wave will add them.

## Sentence case

Use sentence case everywhere: page titles, card titles, column headers, buttons, menu items, tabs and field labels.

- Capitalize the first word and proper nouns only: "Purchase orders", "Post packing slip", "Send an advance ship notice (ASN)".
- Product and standard names keep their own capitals: IBAN, SSCC, EDI, DDMRP, CSV.
- Status values coming from the API in upper snake case (`IN_REVIEW`) are shown in sentence case ("In review"). Use the module's `statusLabel` helper or `StatusPill`; never print the raw enum.
- End full sentences with a period. Labels, buttons, titles and column headers have no final period.

## Task verbs

Buttons and links name the action with a verb, and the same action uses the same verb everywhere.

| Use | For | Avoid |
|---|---|---|
| Create | a new record from scratch | Add new, Make |
| Add | something to an existing record (a line, a note, a vendor to a category) | Insert, Append |
| Save | keep changes to a draft or settings | Submit (unless it starts approval), Apply |
| Submit | send for approval or review | Send, Save |
| Approve / Reject | an approval decision | Accept, Deny, Decline |
| Post | write to the ledger or stock | Book, Commit |
| Cancel | stop a document that hasn't finished | Void, Abort |
| Reverse | undo something already posted | Undo, Delete |
| Remove | take something off a record without destroying it | Delete |
| Delete | destroy something permanently | Remove |
| Release | free stock or an order from a hold, or release against an agreement | Unblock |
| Try again | repeat a request that failed | Retry |
| Reload | fetch the latest version | Refresh |

Name the object when the button stands alone or there are several on the page: "Post packing slip", "Approve requisition". A confirm dialog repeats the verb of the button that opened it.

## Error messages

Every error says **what happened, why, and what to do**, in that order, in one or two sentences.

| Part | Example |
|---|---|
| What happened | "The quantity is more than the purchase order line allows." |
| Why (when it isn't obvious) | "...because the line has no open quantity left." |
| What to do | "Receive no more than the open quantity plus its tolerance." |

Rules:

- **Show server problems with `ProblemMessage`** (`src/shell/errors`). It looks the problem code up in the catalog (`src/shell/errors/catalog.ts`) and shows the catalog message and action. When there is no entry it falls back to the server's `detail`, then `title`. The code, the server's own words and the request id sit under a "Details" disclosure so support can trace the request without the code being the message.
- **Never render a bare code** (`{problem.code}`, `{error.code}`) or "message (CODE)" as the message. If a module needs its own wording for a code, pass it as `message`; `ProblemMessage` still keeps the code under Details.
- **Add catalog entries** for codes your screen can hit. Keep `message` to what happened and why, `action` to what to do, both full sentences, neither repeating the code.
- **Title + body.** Alerts get a short title naming the failed task ("Could not save the purchase order") and the `ProblemMessage` as the body.
- **Field errors** go next to the field, say what's wrong and how to fix it, and don't repeat the label: "Enter a date on or after the order date", not "Invalid date".
- **Conflicts** (someone else changed the record): say so and offer Reload.
- **Permissions:** name the missing duty in plain words when you know it, and point to an administrator.
- **Don't blame or guess.** Avoid "you entered an invalid value" when the service may be at fault; "Something went wrong on our side" is fine for a 500.

## Glossary

Use these words, with these meanings, and the same word for the same thing on every screen.

| Term | Meaning | Not |
|---|---|---|
| Legal entity | The company that owns a transaction, with its own books | Company code, LE |
| Tenant | The whole customer installation; people rarely need to see it | Org, instance |
| Party | A person or organization known to the system; vendors, customers and workers are parties with a role | Contact, entity |
| Vendor / customer | A party in that role | Supplier (except in "supplier portal"), client |
| Worker | A person who works for a legal entity; an employment links them | Employee record, resource |
| Duty | A permission a person is given, such as "Post packing slips" | Right, privilege, scope |
| Requisition | An internal request to buy something | Purchase request, PR |
| Purchase order | An order sent to a vendor; "PO" is fine in tight columns | Order (alone, when sales orders are on the same page) |
| Receipt | Goods or services recorded as received against a purchase order | GRN, goods receipt |
| Invoice | A vendor's bill (payables) or a bill to a customer (receivables) | Bill |
| Sales order | A customer's order | SO, order (alone, when purchase orders are on the same page) |
| Packing slip | The document that ships goods and posts the issue | Delivery note, shipment doc |
| Hold | Something that stops an order or invoice from moving on until it is released | Block (except stock blocking) |
| Ledger | The general ledger of a legal entity | GL (fine in tight columns) |
| Journal / journal entry | A set of balanced ledger lines posted together | Voucher |
| Post | Write to the ledger or to stock; posted records can only be reversed | Book, commit |
| Period | An accounting period that can be open or closed | Fiscal period, month |
| Dimension | An extra attribute on a ledger line, such as department or cost center | Segment (except in account structures) |
| Batch | A quantity of an item made or received together (inventory, process manufacturing); in the ledger, a group of journal lines entered together. Say "journal batch" when both could appear | Lot |
| Count sheet | A list of locations and items to count | Stock take sheet |

### When technical terms are acceptable

- **Industry standards the audience uses daily:** SSCC, ASN, EDI, IBAN, BIC, DDMRP, WIP, ATP, UoM, CSV. Spell out an abbreviation the first time on a page when the reader might not know it: "advance ship notice (ASN)".
- **Identifiers a person must type or paste**, such as "Party id" or "Run id", until a picker replaces the field. Say what the id points to in the hint: "The employee's party record".
- **Under "Details" in `ProblemMessage`:** problem codes, server detail and request ids are for support and may be technical.
- **Admin, integration and developer screens** (connectors, message logs, metadata) may show field names and raw values when the person needs them to configure or debug. Labels and hints still follow this guide.
- **Never in user text:** spec ids (FR-, BR-, AC-), waves, slices, sagas, idempotency, ETags, If-Match, HTTP status numbers, stack traces, enum values in upper snake case, internal service names.

## Copy review checklist

Before a change with user-facing text is merged, check that:

- [ ] Text is in sentence case and uses the verbs and glossary terms above.
- [ ] No spec id, wave or slice appears in rendered text (`plainLanguage.test.ts` passes).
- [ ] Every error says what happened and what to do; server problems use `ProblemMessage`, and new codes the screen can hit have catalog entries.
- [ ] Buttons name the action, and accessible names match the visible label.
