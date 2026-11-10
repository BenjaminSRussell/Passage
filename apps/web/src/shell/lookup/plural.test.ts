import { plural } from "./EntityLookup";

describe("plural", () => {
  it("pluralises lookup nouns for placeholders", () => {
    expect(plural("legal entity")).toBe("legal entities");
    expect(plural("currency")).toBe("currencies");
    expect(plural("party")).toBe("parties");
    expect(plural("warehouse")).toBe("warehouses");
    expect(plural("vendor")).toBe("vendors");
  });
});
