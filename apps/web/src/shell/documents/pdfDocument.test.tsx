import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { setLookupFetcher } from "../lookup/entities";
import { PrincipalProvider } from "../principal";
import { json, server, useFakeServer } from "../testing/fakeServer";
import {
  addressLines,
  counterparty,
  DownloadPdfButton,
  legalEntityParty,
  orgUnitParty,
  partyAddress,
  pdfMultilineText,
  pdfText,
  type DocumentRequest,
} from "./pdfDocument";

useFakeServer();

const doc: DocumentRequest = {
  documentType: "PURCHASE_ORDER",
  number: "PO-1",
  legalEntity: { name: "Cedar Ridge Pumps" },
  columns: [{ header: "Item" }],
  lines: [["Casting"]],
};

function renderButton(duties: string[]) {
  return render(
    <PrincipalProvider duties={duties}>
      <DownloadPdfButton build={async () => doc} filename="po.pdf" />
    </PrincipalProvider>,
  );
}

beforeEach(() => {
  setLookupFetcher(null); // name lookups go to the fake server like every other read
  URL.createObjectURL = vi.fn(() => "blob:x");
  URL.revokeObjectURL = vi.fn();
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined);
});

describe("Download PDF", () => {
  it("is hidden without the render duty", () => {
    renderButton(["print.job.submit"]);
    expect(screen.queryByRole("button", { name: "Download PDF" })).not.toBeInTheDocument();
  });

  it("posts the document to the print service and saves the answer", async () => {
    server.handler = (url) => (url.pathname === "/api/v1/print/documents:render" ? json(200, {}) : undefined);
    renderButton(["print.document.render"]);
    await userEvent.click(screen.getByRole("button", { name: "Download PDF" }));
    await vi.waitFor(() => expect(URL.createObjectURL).toHaveBeenCalled());
    expect(server.calls[0].body).toMatchObject({ documentType: "PURCHASE_ORDER", number: "PO-1" });
  });

  it("explains a document that cannot be rendered and leaves the page usable", async () => {
    server.handler = () => json(422, { code: "PRN-0008", detail: "number is required" });
    renderButton(["print.document.render"]);
    await userEvent.click(screen.getByRole("button", { name: "Download PDF" }));
    expect(await screen.findByText("This document can't be downloaded as a PDF")).toBeInTheDocument();
    expect(screen.getByText(/has a value the PDF can't show/)).toBeInTheDocument();
    expect(screen.getByText("number is required")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Download PDF" })).toBeEnabled();
  });

  it("says that a document with too many lines cannot be one PDF", async () => {
    server.handler = () => json(422, { code: "PRN-0009", detail: "lines has 501 entries; one document holds at most 500" });
    renderButton(["print.document.render"]);
    await userEvent.click(screen.getByRole("button", { name: "Download PDF" }));
    expect(await screen.findByText(/more lines than one PDF can hold/)).toBeInTheDocument();
  });

  it("downloads a document with characters the PDF font lacks and says how many show as a placeholder", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response("%PDF-1.5", { status: 200, headers: { "Content-Type": "application/pdf", "X-Passage-Unprintable-Characters": "4" } })),
    );
    renderButton(["print.document.render"]);
    await userEvent.click(screen.getByRole("button", { name: "Download PDF" }));
    expect(await screen.findByText("The PDF was downloaded, but some characters could not be printed")).toBeInTheDocument();
    expect(screen.getByText(/4 characters are not in the PDF font/)).toBeInTheDocument();
    expect(URL.createObjectURL).toHaveBeenCalled();
    expect(screen.queryByText("This document can't be downloaded as a PDF")).not.toBeInTheDocument();
  });

  it("shows no note when every character was printed", async () => {
    server.handler = (url) => (url.pathname === "/api/v1/print/documents:render" ? json(200, {}) : undefined);
    renderButton(["print.document.render"]);
    await userEvent.click(screen.getByRole("button", { name: "Download PDF" }));
    await vi.waitFor(() => expect(URL.createObjectURL).toHaveBeenCalled());
    expect(screen.queryByText(/could not be printed/)).not.toBeInTheDocument();
  });
});

const LE = "le-doc";
const address = (formatted: string, ...purposes: [string, boolean][]) => ({ formatted, purposes: purposes.map(([purpose, primary]) => ({ purpose, primary })) });
const headerOf = (path: string, name: string) => (server.calls.find((c) => c.url.pathname === path)?.init.headers as Record<string, string> | undefined)?.[name];

describe("document data", () => {
  it("keeps Unicode text and drops only what the service refuses", () => {
    expect(pdfText("Łódź – “East” ✓ 東京")).toBe("Łódź – “East” ✓ 東京");
    expect(pdfText("  Αθήνα\tСофия\nline\u0007 ")).toBe("Αθήνα София line");
    expect(pdfText(undefined)).toBe("");
  });

  it("keeps the line breaks of a description", () => {
    expect(pdfMultilineText("Pump\r\nwith seal\u0000\tkit")).toBe("Pump\nwith seal kit");
  });

  it("splits an address into its printed lines", () => {
    expect(addressLines("400 Industrial Way\nCedar Rapids, IA 52404\nUS")).toEqual(["400 Industrial Way", "Cedar Rapids, IA 52404", "US"]);
    expect(addressLines("400 Industrial Way, Cedar Rapids IA")).toEqual(["400 Industrial Way", "Cedar Rapids IA"]);
    expect(addressLines(undefined)).toEqual([]);
    expect(addressLines(Array.from({ length: 12 }, (_, i) => `Line ${i}`).join("\n"))).toHaveLength(8);
  });

  it("reads the legal entity name and its primary address lines", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith("/legal-entities/le1")) return json(200, { name: "Cedar Ridge Pumps", primaryAddressId: "a1" });
      if (url.pathname.endsWith("/addresses/a1")) return json(200, { formatted: "400 Industrial Way, Cedar Rapids IA" });
      return undefined;
    };
    expect(await legalEntityParty("le1")).toEqual({ name: "Cedar Ridge Pumps", address: ["400 Industrial Way", "Cedar Rapids IA"] });
    expect(headerOf("/api/v1/org/addresses/a1", "X-Passage-LegalEntity")).toBe("le1");
  });

  it("prints the legal entity without an address when the address cannot be read", async () => {
    server.handler = (url) => (url.pathname.endsWith("/legal-entities/le2") ? json(200, { name: "Łódzkie Zakłady Pomp", primaryAddressId: "gone" }) : undefined);
    expect(await legalEntityParty("le2")).toEqual({ name: "Łódzkie Zakłady Pomp", address: [] });
  });

  it("picks a party's primary address for the preferred purpose, then any with it, then the first", async () => {
    const business = address("1 Office Rd\nPraha", ["business", true]);
    const invoicePlain = address("2 Accounts St\nPraha", ["invoice", false]);
    const invoicePrimary = address("3 Billing Sq\nPraha", ["invoice", true]);
    let items: unknown[] = [business, invoicePlain, invoicePrimary];
    server.handler = (url) => (url.pathname === "/api/v1/org/addresses" ? json(200, { items }) : undefined);

    expect(await partyAddress("p1", ["invoice", "business"], LE)).toEqual(["3 Billing Sq", "Praha"]);
    expect(server.calls[0].url.searchParams.get("ownerType")).toBe("party");
    expect(server.calls[0].url.searchParams.get("ownerId")).toBe("p1");
    expect(headerOf("/api/v1/org/addresses", "X-Passage-LegalEntity")).toBe(LE);

    items = [business, invoicePlain];
    expect(await partyAddress("p1", ["invoice", "business"], LE)).toEqual(["1 Office Rd", "Praha"]);
    expect(await partyAddress("p1", ["invoice"], LE)).toEqual(["2 Accounts St", "Praha"]);
    expect(await partyAddress("p1", ["delivery"], LE)).toEqual(["1 Office Rd", "Praha"]);
    items = [];
    expect(await partyAddress("p1", ["delivery"], LE)).toEqual([]);
  });

  it("gives no address lines when the party's addresses cannot be read", async () => {
    server.handler = () => json(403, { code: "AUTH-0002" });
    expect(await partyAddress("p1", ["business"], LE)).toEqual([]);
  });

  it("shows a vendor by its party name and address, with the account number apart", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/party/vendors/v-doc-1") return json(200, { id: "v-doc-1", partyId: "p-doc-1", accountNumber: "V-20004" });
      if (url.pathname === "/api/v1/party/parties/p-doc-1") return json(200, { id: "p-doc-1", name: "Žluťoučký kůň s.r.o." });
      if (url.pathname === "/api/v1/org/addresses") return json(200, { items: [address("Náměstí Míru 15\n120 00 Praha 2\nCZ", ["business", true])] });
      return undefined;
    };
    expect(await counterparty("vendor", "v-doc-1", { label: "Vendor", fallbackName: "Vendor", purposes: ["business"], legalEntityId: LE })).toEqual({
      party: { label: "Vendor", name: "Žluťoučký kůň s.r.o.", address: ["Náměstí Míru 15", "120 00 Praha 2", "CZ"] },
      accountNumber: "V-20004",
    });
  });

  it("falls back to a plain name when the customer cannot be read", async () => {
    server.handler = () => json(403, { code: "AUTH-0002" });
    const opts = { label: "Bill to", fallbackName: "Customer", purposes: ["invoice" as const], legalEntityId: LE };
    expect(await counterparty("customer", "c-doc-denied", opts)).toEqual({ party: { label: "Bill to", name: "Customer", address: [] }, accountNumber: "" });
    expect(await counterparty("customer", undefined, opts)).toEqual({ party: { label: "Bill to", name: "Customer", address: [] }, accountNumber: "" });
  });

  it("shows a warehouse with its own address, or its site's", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/org/warehouses/w1") return json(200, { name: "Finished goods", addressId: "wa", siteId: "s1" });
      if (url.pathname === "/api/v1/org/warehouses/w2") return json(200, { name: "Raw materials", siteId: "s1" });
      if (url.pathname === "/api/v1/org/sites/s1") return json(200, { name: "Cedar Rapids plant", addressId: "sa" });
      if (url.pathname === "/api/v1/org/addresses/wa") return json(200, { formatted: "1200 Dock Rd\nCedar Rapids, IA 52404" });
      if (url.pathname === "/api/v1/org/addresses/sa") return json(200, { formatted: "400 Industrial Way\nCedar Rapids, IA 52404" });
      return undefined;
    };
    expect(await orgUnitParty("warehouses", "w1", "Deliver to", LE)).toEqual({ label: "Deliver to", name: "Finished goods", address: ["1200 Dock Rd", "Cedar Rapids, IA 52404"] });
    expect(await orgUnitParty("warehouses", "w2", "Deliver to", LE)).toEqual({ label: "Deliver to", name: "Raw materials", address: ["400 Industrial Way", "Cedar Rapids, IA 52404"] });
    expect(await orgUnitParty("warehouses", "missing", "Deliver to", LE)).toBeNull();
    expect(await orgUnitParty("sites", undefined, "Deliver to", LE)).toBeNull();
  });
});
