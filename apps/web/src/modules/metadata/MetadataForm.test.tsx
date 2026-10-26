import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MetadataForm } from "./MetadataForm";

afterEach(() => vi.restoreAllMocks());

describe("MetadataForm", () => {
  it("renders extension fields and enum values from the effective schema", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          title: "Vendor",
          type: "object",
          required: ["code"],
          properties: {
            code: { type: "string", title: "Code" },
            status: { type: "string", title: "Status", enum: ["active", "preferred"] },
            accountManager: { type: "string", title: "Account manager" },
          },
        }),
        { status: 200, headers: { "Content-Type": "application/schema+json" } },
      ),
    );

    render(<MetadataForm entity="Vendor" />);

    expect(await screen.findByLabelText("Account manager")).toBeInTheDocument();
    expect(fetch).toHaveBeenCalledWith(
      "/api/v1/metadata/entities/Vendor/schema",
      expect.objectContaining({
        headers: expect.objectContaining({
          "X-Passage-Tenant": "00000000-0000-0000-0000-000000000001",
          "X-Passage-Duties": expect.stringContaining("metadata.read"),
        }),
      }),
    );
    expect(screen.getByRole("option", { name: "preferred" })).toBeInTheDocument();
    expect(screen.getByLabelText("Code")).toBeRequired();
  });

  it("shows a loading message, then offers a retry when the schema fails to load", async () => {
    const schema = { title: "Vendor", properties: { code: { type: "string", title: "Code" } } };
    vi.spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(new Response("{}", { status: 500 }))
      .mockResolvedValueOnce(new Response(JSON.stringify(schema), { status: 200 }));

    render(<MetadataForm entity="Vendor" />);

    expect(screen.getByText("Loading Vendor metadata…")).toBeInTheDocument();
    expect(await screen.findByRole("alert")).toHaveTextContent("We couldn't load this form");
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByLabelText("Code")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save Vendor" })).toBeInTheDocument();
  });

  it("confirms a save and keeps the form usable", async () => {
    const schema = { title: "Vendor", properties: { code: { type: "string", title: "Code" } } };
    vi.spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(new Response(JSON.stringify(schema), { status: 200 }))
      .mockResolvedValueOnce(new Response("{}", { status: 201 }));

    render(<MetadataForm entity="Vendor" />);

    fireEvent.change(await screen.findByLabelText("Code"), { target: { value: "V-1" } });
    fireEvent.click(screen.getByRole("button", { name: "Save Vendor" }));
    expect(await screen.findByText("Vendor saved.")).toBeInTheDocument();
  });
});

describe("MetadataForm messages", () => {
  it("says when no form is defined for the entity", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response("{}", { status: 404 }));
    render(<MetadataForm entity="Gadget" />);
    expect(await screen.findByRole("alert")).toHaveTextContent("No form is defined for Gadget yet");
  });

  it("clears the saved confirmation as soon as a field is edited", async () => {
    const schema = { title: "Vendor", properties: { code: { type: "string", title: "Code" } } };
    vi.spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(new Response(JSON.stringify(schema), { status: 200 }))
      .mockResolvedValueOnce(new Response("{}", { status: 201 }));
    render(<MetadataForm entity="Vendor" />);
    fireEvent.change(await screen.findByLabelText("Code"), { target: { value: "V-1" } });
    fireEvent.click(screen.getByRole("button", { name: "Save Vendor" }));
    expect(await screen.findByText("Vendor saved.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "V-2" } });
    expect(screen.queryByText("Vendor saved.")).not.toBeInTheDocument();
  });
});
