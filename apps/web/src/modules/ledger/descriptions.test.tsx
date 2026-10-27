import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { DescriptionTemplate } from "./api/client";
import { emptyTemplate, placeholdersOf, previewDescription, toTemplateInput, validateTemplate } from "./DescriptionTemplateFormPage";
import { bindingsLabel } from "./DescriptionTemplatesPage";
import { json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const TENANT_TPL = "0190a000-0000-7000-8000-0000000000a1";
const LE_TPL = "0190a000-0000-7000-8000-0000000000a2";
const LE = "0190a000-0000-7000-8000-0000000000e1";

function template(patch: Partial<DescriptionTemplate>): DescriptionTemplate {
  return {
    id: TENANT_TPL,
    scope: "tenant",
    legalEntityId: null,
    sourceType: "manual_journal",
    language: "en",
    template: "Journal %1 of %2",
    fieldBindings: { "1": "voucher", "2": "document_date" },
    updatedAt: "2026-10-27T10:00:00Z",
    ...patch,
  };
}

const templates = [
  template({}),
  template({ id: LE_TPL, scope: "legal_entity", legalEntityId: LE, sourceType: "vendor_invoice", template: "Invoice %1", fieldBindings: { "1": "source_id" } }),
];

describe("description template list", () => {
  it("lists templates with scope and placeholders and opens one for editing", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/ledger/description-templates") return json(200, { items: templates });
      if (url.pathname === `/api/v1/ledger/description-templates/${TENANT_TPL}`) return json(200, templates[0]);
      return undefined;
    };
    const { container } = renderAt("/ledger/description-templates");
    const table = await screen.findByRole("table", { name: "Description templates" });
    await within(table).findByRole("button", { name: "manual_journal" });
    const tenantRow = within(table).getByRole("button", { name: "manual_journal" }).closest("tr") as HTMLElement;
    expect(tenantRow.textContent).toContain("All legal entities");
    expect(tenantRow.textContent).toContain("%1 = voucher, %2 = document_date");
    const leRow = within(table).getByRole("button", { name: "vendor_invoice" }).closest("tr") as HTMLElement;
    expect(leRow.textContent).toContain(`Legal entity ${LE}`);
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(within(table).getByRole("button", { name: "manual_journal" }));
    expect(screen.getByTestId("location").textContent).toBe(`/ledger/description-templates/${TENANT_TPL}`);
    expect(await screen.findByLabelText("Template")).toHaveValue("Journal %1 of %2");
    expect(screen.getByLabelText("%1")).toHaveValue("voucher");
    expect(screen.getByLabelText("%2")).toHaveValue("document_date");
    expect(screen.getByTestId("dt-preview").textContent).toBe("Journal GL-0000123 of 2026-03-31");
  });

  it("shows the empty state when there are no templates", async () => {
    server.handler = () => json(200, { items: [] });
    renderAt("/ledger/description-templates");
    expect(await screen.findByText("No description templates yet")).toBeInTheDocument();
  });

  it("shows the error and retries", async () => {
    let fail = true;
    server.handler = () => (fail ? json(500, { code: "COM-0500", detail: "internal error" }) : json(200, { items: templates }));
    renderAt("/ledger/description-templates");
    expect(await screen.findByText("Could not load the description templates")).toBeInTheDocument();
    expect(screen.getByText("internal error")).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("button", { name: "manual_journal" })).toBeInTheDocument();
  });
});

describe("description template form", () => {
  it("validates like the service before saving", () => {
    expect(placeholdersOf("%2 of %1 and %2, 100%")).toEqual(["1", "2"]);
    expect(
      validateTemplate({ ...emptyTemplate, sourceType: "Manual Journal", language: "english", scope: "legal_entity", template: "J %1" }),
    ).toEqual({
      sourceType: "Use lowercase letters, digits and underscores, starting with a letter.",
      language: "Use a language code such as en or en-US.",
      legalEntityId: "Enter the legal entity's id (a UUID).",
      "binding-1": "Pick the field for %1.",
    });
    expect(validateTemplate({ ...emptyTemplate, sourceType: "x", template: "x".repeat(256) })).toEqual({
      template: "Keep the template to 255 characters.",
    });
    expect(validateTemplate({ ...emptyTemplate, sourceType: "x", template: "  " })).toEqual({ template: "Enter the template text." });
    expect(validateTemplate({ ...emptyTemplate, sourceType: "manual_journal", template: "Plain text" })).toEqual({});
  });

  it("sends only the bindings of placeholders still in the text", () => {
    const input = toTemplateInput({
      ...emptyTemplate,
      sourceType: "manual_journal",
      template: "Journal %2",
      bindings: { "1": "voucher", "2": "period" },
    });
    expect(input).toEqual({ scope: "tenant", sourceType: "manual_journal", language: "en", template: "Journal %2", fieldBindings: { "2": "period" } });
    expect(toTemplateInput({ ...emptyTemplate, scope: "legal_entity", legalEntityId: ` ${LE} `, sourceType: "x", template: "T" }).legalEntityId).toBe(LE);
    expect(bindingsLabel({ "2": "period", "1": "voucher" })).toBe("%1 = voucher, %2 = period");
  });

  it("previews the expansion and cuts it at 255 characters like the service", () => {
    expect(previewDescription("Invoice %1 (%2)", { "1": "source_id", "2": "currency_txn" })).toEqual({ text: "Invoice INV-1001 (USD)", truncated: false });
    const long = previewDescription("é".repeat(250) + "%1", { "1": "source_id" });
    expect(long.truncated).toBe(true);
    expect([...long.text]).toHaveLength(255);
    expect(long.text.endsWith("…")).toBe(true);
  });

  it("does not call the service while the form is invalid", async () => {
    const { container } = renderAt("/ledger/description-templates/new");
    fireEvent.change(screen.getByLabelText("Template"), { target: { value: "Journal %1" } });
    fireEvent.click(screen.getByRole("button", { name: "Save template" }));
    expect(await screen.findByText("Pick the field for %1.")).toBeInTheDocument();
    expect(screen.getByText("Use lowercase letters, digits and underscores, starting with a letter.")).toBeInTheDocument();
    expect(screen.getByLabelText("%1")).toHaveAttribute("aria-invalid", "true");
    expect(server.calls).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);
  });

  it("creates a legal entity template with PUT and returns to the list", async () => {
    server.handler = (_url, init) => {
      if (init.method === "PUT") return json(201, templates[1]);
      return json(200, { items: [] });
    };
    renderAt("/ledger/description-templates/new");
    fireEvent.change(screen.getByLabelText("Source type"), { target: { value: "Vendor_Invoice" } });
    fireEvent.change(screen.getByLabelText("Applies to"), { target: { value: "legal_entity" } });
    fireEvent.change(screen.getByLabelText("Legal entity id"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Template"), { target: { value: "Invoice %1 for %2" } });
    fireEvent.change(screen.getByLabelText("%1"), { target: { value: "source_id" } });
    fireEvent.change(screen.getByLabelText("%2"), { target: { value: "amount_txn" } });
    expect(screen.getByTestId("dt-preview").textContent).toBe("Invoice INV-1001 for 1250.00");
    fireEvent.click(screen.getByRole("button", { name: "Save template" }));

    await waitFor(() => expect(screen.getByTestId("location").textContent).toBe("/ledger/description-templates"));
    const put = server.calls.find((c) => c.method === "PUT");
    expect(put?.url.pathname).toMatch(/^\/api\/v1\/ledger\/description-templates\/[0-9a-f-]{36}$/);
    expect(put?.body).toEqual({
      scope: "legal_entity",
      legalEntityId: LE,
      sourceType: "vendor_invoice",
      language: "en",
      template: "Invoice %1 for %2",
      fieldBindings: { "1": "source_id", "2": "amount_txn" },
    });
  });

  it("saves an edit under the template's own id", async () => {
    server.handler = (url, init) => {
      if (init.method === "PUT") return json(200, template({ template: "GJ %1" }));
      if (url.pathname === `/api/v1/ledger/description-templates/${TENANT_TPL}`) return json(200, templates[0]);
      return json(200, { items: templates });
    };
    renderAt(`/ledger/description-templates/${TENANT_TPL}`);
    fireEvent.change(await screen.findByLabelText("Template"), { target: { value: "GJ %1" } });
    fireEvent.click(screen.getByRole("button", { name: "Save template" }));
    await waitFor(() => expect(screen.getByTestId("location").textContent).toBe("/ledger/description-templates"));
    const put = server.calls.find((c) => c.method === "PUT");
    expect(put?.url.pathname).toBe(`/api/v1/ledger/description-templates/${TENANT_TPL}`);
    expect(put?.body).toMatchObject({ template: "GJ %1", fieldBindings: { "1": "voucher" } });
  });

  it("shows a duplicate rejection and stays on the form", async () => {
    server.handler = () =>
      json(409, { code: "LEDGER_DESCRIPTION_TEMPLATE_DUPLICATE", detail: "a template already exists for this scope, legal entity, source type and language" });
    renderAt("/ledger/description-templates/new");
    fireEvent.change(screen.getByLabelText("Source type"), { target: { value: "manual_journal" } });
    fireEvent.change(screen.getByLabelText("Template"), { target: { value: "Manual journal" } });
    fireEvent.click(screen.getByRole("button", { name: "Save template" }));
    expect(
      await screen.findByText("a template already exists for this scope, legal entity, source type and language (LEDGER_DESCRIPTION_TEMPLATE_DUPLICATE)"),
    ).toBeInTheDocument();
    expect(screen.getByTestId("location").textContent).toBe("/ledger/description-templates/new");
  });

  it("reports a missing template on the edit page", async () => {
    server.handler = () => json(404, { code: "LEDGER_DESCRIPTION_TEMPLATE_NOT_FOUND", detail: "description template not found" });
    renderAt(`/ledger/description-templates/${TENANT_TPL}`);
    expect(await screen.findByText("Could not load the description template")).toBeInTheDocument();
    expect(screen.getByText("description template not found")).toBeInTheDocument();
  });
});
