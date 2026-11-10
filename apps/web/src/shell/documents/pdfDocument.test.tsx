import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { PrincipalProvider } from "../principal";
import { json, server, useFakeServer } from "../testing/fakeServer";
import { DownloadPdfButton, legalEntityParty, pdfText, type DocumentRequest } from "./pdfDocument";

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
    expect(screen.getByText(/PDF layout cannot show/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Download PDF" })).toBeEnabled();
  });
});

describe("document data", () => {
  it("keeps text the PDF font can print", () => {
    expect(pdfText("Acme – “East” ✓")).toBe('Acme - "East" ?');
  });

  it("reads the legal entity name and its primary address lines", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith("/legal-entities/le1")) return json(200, { name: "Cedar Ridge Pumps", primaryAddressId: "a1" });
      if (url.pathname.endsWith("/addresses/a1")) return json(200, { formatted: "400 Industrial Way, Cedar Rapids IA" });
      return undefined;
    };
    expect(await legalEntityParty("le1")).toEqual({ name: "Cedar Ridge Pumps", address: ["400 Industrial Way", "Cedar Rapids IA"] });
  });
});
