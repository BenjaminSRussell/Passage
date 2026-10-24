import { render, screen } from "@testing-library/react";
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
});
