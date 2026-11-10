export interface GlossaryTerm {
  /** Stable key used by `<Term>`. */
  id: string;
  /** The word as people read it on screen. */
  term: string;
  /** One or two plain sentences. */
  meaning: string;
}

/** Domain words the app uses without explaining. Keep in step with the glossary in apps/web/docs/ux-writing.md. */
export const glossary: readonly GlossaryTerm[] = [
  { id: "legal-entity", term: "Legal entity", meaning: "The company that owns a transaction and keeps its own books." },
  { id: "party", term: "Party", meaning: "A person or organization the system knows. Vendors, customers and workers are parties with a role." },
  { id: "duty", term: "Duty", meaning: "A permission you are given, such as posting packing slips." },
  { id: "requisition", term: "Requisition", meaning: "An internal request to buy something. A buyer turns it into a purchase order." },
  { id: "purchase-order", term: "Purchase order", meaning: "An order sent to a vendor for goods or services." },
  { id: "receipt", term: "Receipt", meaning: "A record that goods or services arrived against a purchase order." },
  { id: "gr-ir", term: "GR/IR", meaning: "Goods received, invoice not yet received. The value of receipts that no vendor invoice has matched yet." },
  { id: "three-way-match", term: "Three-way match", meaning: "Checking that the purchase order, the receipt and the vendor invoice agree before the invoice is paid." },
  { id: "sales-order", term: "Sales order", meaning: "A customer's order for goods or services." },
  { id: "packing-slip", term: "Packing slip", meaning: "The document that ships goods to a customer. Posting it takes the goods out of stock." },
  { id: "hold", term: "Hold", meaning: "Something that stops an order or invoice from moving on until someone releases it." },
  { id: "post", term: "Post", meaning: "Write a document to the ledger or to stock. A posted record can only be reversed, not edited." },
  { id: "period", term: "Period", meaning: "An accounting period, such as a month. Posting is only allowed in an open period." },
  { id: "dimension", term: "Dimension", meaning: "An extra label on a ledger line, such as department or cost center, used for reporting." },
  { id: "dimension-set", term: "Dimension set", meaning: "The combination of dimension values carried by one ledger line." },
  { id: "inventory-status", term: "Inventory status", meaning: "Whether stock can be used, for example available, blocked or in quality inspection." },
  { id: "batch", term: "Batch", meaning: "A quantity of an item made or received together, tracked as one lot." },
  { id: "serial", term: "Serial number", meaning: "A number that identifies one unit of an item." },
  { id: "catch-weight", term: "Catch weight", meaning: "Stock counted in units but priced by its actual weight, as with cases of meat." },
  { id: "atp", term: "ATP", meaning: "Available to promise. Stock and incoming supply that can still be promised to customers." },
  { id: "asn", term: "ASN", meaning: "Advance ship notice. A vendor's message saying what is on its way." },
  { id: "sscc", term: "SSCC", meaning: "Serial shipping container code. The barcode that identifies a pallet or carton." },
  { id: "origin", term: "Origin", meaning: "Where a record came from, such as an import, another service or a person entering it." },
] as const;

export const glossaryById = new Map(glossary.map((t) => [t.id, t]));
