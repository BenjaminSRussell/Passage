import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { GlossaryPage } from "./GlossaryPage";
import { Term } from "./Term";
import { glossary } from "./terms";

describe("glossary", () => {
  it("has unique ids and a plain meaning that ends like a sentence", () => {
    expect(new Set(glossary.map((t) => t.id)).size).toBe(glossary.length);
    for (const t of glossary) expect(t.meaning, t.id).toMatch(/[.]$/);
  });

  it("links a term to its meaning, which can be dismissed with Escape", async () => {
    const user = userEvent.setup();
    const { container } = render(<Term id="gr-ir" />);
    const word = screen.getByText("GR/IR");
    const tip = screen.getByRole("tooltip");
    expect(word).toHaveAttribute("aria-describedby", tip.id);
    await user.tab();
    expect(word).toHaveFocus();
    await user.keyboard("{Escape}");
    expect(container.firstElementChild).toHaveAttribute("data-dismissed", "true");
  });

  it("renders the text unchanged for an unknown term", () => {
    render(<Term id="nope">plain</Term>);
    expect(screen.getByText("plain")).toBeInTheDocument();
    expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
  });

  it("lists every term on the glossary page", () => {
    render(<GlossaryPage />);
    expect(screen.getAllByRole("term")).toHaveLength(glossary.length);
  });
});
