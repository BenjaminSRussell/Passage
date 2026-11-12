import { screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, pageViolations, renderRoutes, server, useFakeServer } from "../../shell/testing/fakeServer";
import documentDesign from "./module";

useFakeServer();

const template = { code: "INV-STD", name: "Standard invoice", documentType: "CUSTOMER_INVOICE", default: true, version: 3, config: { title: "Tax invoice", hideColumns: ["Unit"], noteBlocks: ["TERMS"] } };
const block = { code: "TERMS", language: "de", body: "Zahlbar innerhalb von {{days}} Tagen.", placeholders: ["days"], version: 1 };
const rule = { id: "0b7e1f84-0000-4000-8000-000000000001", documentType: "CUSTOMER_INVOICE", copyKind: "COPY", channel: "ARCHIVE", copies: 1, sequence: 1 };

describe("Document design", () => {
  it("lists templates, text blocks and output rules with readable labels", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith("/document-templates")) return json(200, { items: [template] });
      if (url.pathname.endsWith("/text-blocks")) return json(200, { items: [block] });
      if (url.pathname.endsWith("/output-rules")) return json(200, { items: [rule] });
      return undefined;
    };
    const { container } = renderRoutes(documentDesign.routes, "/document-design");
    const templates = await screen.findByRole("table", { name: "Document templates" });
    expect(within(templates).getByText("Standard invoice")).toBeInTheDocument();
    expect(within(templates).getByText("Default for this document")).toBeInTheDocument();
    expect(within(templates).getByText("1 hidden columns, 1 form notes")).toBeInTheDocument();
    const blocks = await screen.findByRole("table", { name: "Text blocks" });
    expect(within(blocks).getByText("days")).toBeInTheDocument();
    const rules = await screen.findByRole("table", { name: "Output rules" });
    expect(within(rules).getByText("Archive")).toBeInTheDocument();
    expect(within(rules).getByText("Every legal entity and account")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("says when a list cannot be loaded without hiding the others", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith("/document-templates")) return json(503, { code: "PRN-0016", detail: "not available" });
      return json(200, { items: [] });
    };
    renderRoutes(documentDesign.routes, "/document-design");
    expect(await screen.findByText("Could not load document templates")).toBeInTheDocument();
    expect(await screen.findByText("No text blocks yet")).toBeInTheDocument();
  });
});
