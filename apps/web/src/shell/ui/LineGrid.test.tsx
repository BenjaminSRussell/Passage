import { useState } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { LineGrid, gridKeyAction, type KeyChord } from "./LineGrid";

type Line = Record<"item" | "qty" | "unit", string>;

const columns = [
  { name: "item" as const, label: "Item" },
  { name: "qty" as const, label: "Quantity", decimal: true },
  {
    name: "unit" as const,
    label: "Unit",
    options: [
      { value: "ea", label: "Each" },
      { value: "kg", label: "Kilogram" },
    ],
  },
];

const blank = (): Line => ({ item: "", qty: "", unit: "ea" });

function Harness({
  initial,
  errors,
  onSave,
  onSubmit,
}: {
  initial: Line[];
  errors?: Record<string, string>;
  onSave?: () => void;
  onSubmit?: () => void;
}) {
  const [lines, setLines] = useState(initial);
  return (
    <LineGrid
      idPrefix="t"
      noun="Line"
      columns={columns}
      lines={lines}
      errors={errors}
      newLine={blank}
      onChange={setLines}
      onSave={onSave}
      onSubmit={onSubmit}
    />
  );
}

const three: Line[] = [
  { item: "A", qty: "1", unit: "ea" },
  { item: "B", qty: "2", unit: "kg" },
  { item: "C", qty: "3", unit: "ea" },
];

const cell = (label: string, row: number) => screen.getByLabelText(`${label}, line ${row}`);

const chord = (key: string, extra: Partial<KeyChord> = {}): KeyChord => ({
  key,
  ctrlKey: false,
  metaKey: false,
  shiftKey: false,
  altKey: false,
  ...extra,
});

describe("LineGrid keyboard", () => {
  it("is one Tab stop and moves between cells with the arrow keys", async () => {
    const user = userEvent.setup();
    render(<Harness initial={three} />);
    await user.tab();
    expect(cell("Item", 1)).toHaveFocus();
    await user.keyboard("{ArrowRight}");
    expect(cell("Quantity", 1)).toHaveFocus();
    await user.keyboard("{ArrowDown}{ArrowDown}");
    expect(cell("Quantity", 3)).toHaveFocus();
    await user.keyboard("{ArrowLeft}{ArrowUp}");
    expect(cell("Item", 2)).toHaveFocus();
    await user.keyboard("{ArrowLeft}");
    expect(cell("Item", 2)).toHaveFocus();
  });

  it("moves Left and Right inside a cell being edited instead of leaving it", async () => {
    const user = userEvent.setup();
    render(<Harness initial={three} />);
    await user.click(cell("Item", 1));
    await user.keyboard("{Enter}{ArrowRight}");
    expect(cell("Item", 1)).toHaveFocus();
  });

  it("commits an edit with Enter and moves down, and Escape restores the value", async () => {
    const user = userEvent.setup();
    render(<Harness initial={three} />);
    await user.click(cell("Item", 1));
    await user.keyboard("{Enter}X{Enter}");
    expect(cell("Item", 1)).toHaveValue("AX");
    expect(cell("Item", 2)).toHaveFocus();
    await user.keyboard("{Enter}YY{Escape}");
    expect(cell("Item", 2)).toHaveValue("B");
    expect(cell("Item", 2)).toHaveFocus();
  });

  it("wraps Tab to the next row and lets Tab leave the grid at the last cell", async () => {
    const user = userEvent.setup();
    render(<Harness initial={three} />);
    await user.click(cell("Unit", 1));
    await user.keyboard("{Tab}");
    expect(cell("Item", 2)).toHaveFocus();
    await user.keyboard("{Shift>}{Tab}{/Shift}");
    expect(cell("Unit", 1)).toHaveFocus();
    await user.click(cell("Unit", 3));
    await user.keyboard("{Tab}");
    expect(screen.getByRole("button", { name: "Add line" })).toHaveFocus();
  });

  it("adds a line below with Ctrl+Enter and after Enter on a complete last row", async () => {
    const user = userEvent.setup();
    render(<Harness initial={[three[0], blank()]} />);
    await user.click(cell("Item", 1));
    await user.keyboard("{Control>}{Enter}{/Control}");
    expect(screen.getAllByRole("row")).toHaveLength(4);
    expect(cell("Item", 2)).toHaveFocus();
    await user.click(cell("Unit", 3));
    await user.keyboard("{Enter}{Enter}");
    expect(screen.getAllByRole("row")).toHaveLength(4);
    await user.click(cell("Item", 3));
    await user.keyboard("{Enter}Z{Enter}");
    await user.click(cell("Quantity", 3));
    await user.keyboard("{Enter}5{Enter}");
    await user.click(cell("Unit", 3));
    await user.keyboard("{Enter}{Enter}");
    expect(screen.getAllByRole("row")).toHaveLength(5);
    expect(cell("Item", 4)).toHaveFocus();
  });

  it("opens row actions with Shift+F10, fills down, duplicates and removes with undo", async () => {
    const user = userEvent.setup();
    render(<Harness initial={three} />);
    await user.click(cell("Quantity", 2));
    await user.keyboard("{Shift>}{F10}{/Shift}");
    expect(screen.getByRole("menu", { name: "Line 2 actions" })).toBeInTheDocument();
    await user.click(screen.getByRole("menuitem", { name: "Fill down from the row above" }));
    expect(cell("Quantity", 2)).toHaveValue("1");
    expect(cell("Quantity", 2)).toHaveFocus();

    await user.keyboard("{ContextMenu}");
    await user.click(screen.getByRole("menuitem", { name: "Duplicate line" }));
    expect(screen.getAllByRole("row")).toHaveLength(5);
    expect(cell("Item", 3)).toHaveValue("B");

    await user.keyboard("{ContextMenu}");
    await user.click(screen.getByRole("menuitem", { name: "Remove line" }));
    expect(screen.getAllByRole("row")).toHaveLength(4);
    await user.click(screen.getByRole("button", { name: "Undo" }));
    expect(screen.getAllByRole("row")).toHaveLength(5);
  });

  it("closes the row menu with Escape and returns focus to the cell", async () => {
    const user = userEvent.setup();
    render(<Harness initial={three} />);
    await user.click(cell("Item", 2));
    await user.keyboard("{ContextMenu}{Escape}");
    expect(screen.queryByRole("menu")).toBeNull();
    expect(cell("Item", 2)).toHaveFocus();
  });

  it("forwards Ctrl+S and Ctrl+Shift+Enter to the page only when it supplies them", async () => {
    const user = userEvent.setup();
    const onSave = vi.fn();
    const onSubmit = vi.fn();
    render(<Harness initial={three} onSave={onSave} onSubmit={onSubmit} />);
    await user.click(cell("Item", 1));
    await user.keyboard("{Control>}s{/Control}");
    await user.keyboard("{Control>}{Shift>}{Enter}{/Shift}{/Control}");
    expect(onSave).toHaveBeenCalledTimes(1);
    expect(onSubmit).toHaveBeenCalledTimes(1);
    expect(screen.getAllByRole("row")).toHaveLength(4);
  });

  it("leaves the grid for the next control on a second Escape", async () => {
    const user = userEvent.setup();
    render(<Harness initial={three} />);
    await user.click(cell("Item", 1));
    await user.keyboard("{Escape}");
    expect(screen.getByRole("button", { name: "Add line" })).toHaveFocus();
  });
});

describe("LineGrid edge cases", () => {
  it("renders the header and an Add button for an empty list and adds a line", async () => {
    const user = userEvent.setup();
    render(<Harness initial={[]} />);
    expect(screen.getAllByRole("row")).toHaveLength(1);
    await user.click(screen.getByRole("button", { name: "Add line" }));
    expect(cell("Item", 1)).toHaveFocus();
  });

  it("never removes the last remaining line", async () => {
    const user = userEvent.setup();
    render(<Harness initial={[blank()]} />);
    await user.click(cell("Item", 1));
    await user.keyboard("{ContextMenu}");
    expect(screen.getByRole("menuitem", { name: "Remove line" })).toBeDisabled();
  });

  it("marks only cells with a string error as invalid and ignores other error values", () => {
    const errors = { "lines[0].qty": "Enter a quantity", "lines[1].qty": 5, "lines[7].item": "gone" } as unknown as Record<string, string>;
    render(<Harness initial={three} errors={errors} />);
    expect(cell("Quantity", 1)).toBeInvalid();
    expect(cell("Quantity", 1)).toHaveAccessibleDescription("Enter a quantity");
    expect(cell("Quantity", 2)).not.toBeInvalid();
    expect(cell("Item", 1)).not.toBeInvalid();
  });

  it("renders a value that is not a string without throwing", () => {
    const odd = [{ item: undefined, qty: 7, unit: "lb" }] as unknown as Line[];
    render(<Harness initial={odd} />);
    expect(cell("Item", 1)).toHaveValue("");
    expect(cell("Quantity", 1)).toHaveValue("7");
  });
});

describe("gridKeyAction denylist", () => {
  const denied: [string, Partial<KeyChord>][] = [
    ["Insert", {}],
    ["CapsLock", {}],
    ["d", { ctrlKey: true }],
    ["D", { ctrlKey: true, shiftKey: true }],
    ["d", { metaKey: true }],
    ["l", { ctrlKey: true }],
    ["t", { ctrlKey: true }],
    ["w", { ctrlKey: true }],
    ["n", { ctrlKey: true }],
    ["p", { ctrlKey: true }],
    ["f", { ctrlKey: true }],
    ["F6", {}],
    ["F7", {}],
    ["F10", {}],
    ["a", {}],
    ["5", {}],
    ["ArrowDown", { altKey: true }],
    ["ArrowDown", { shiftKey: true }],
  ];

  it.each(denied)("binds nothing to %s with %o", (key, extra) => {
    expect(gridKeyAction(chord(key, extra), false)).toBeNull();
    expect(gridKeyAction(chord(key, extra), true)).toBeNull();
  });

  it("binds exactly the UXS-05 keymap", () => {
    expect(gridKeyAction(chord("ArrowUp"), false)).toBe("up");
    expect(gridKeyAction(chord("ArrowLeft"), true)).toBeNull();
    expect(gridKeyAction(chord("Enter"), false)).toBe("edit");
    expect(gridKeyAction(chord("Enter"), true)).toBe("commit");
    expect(gridKeyAction(chord("Enter", { metaKey: true }), false)).toBe("addBelow");
    expect(gridKeyAction(chord("Enter", { ctrlKey: true, shiftKey: true }), false)).toBe("submit");
    expect(gridKeyAction(chord("s", { metaKey: true }), false)).toBe("save");
    expect(gridKeyAction(chord("F10", { shiftKey: true }), false)).toBe("menu");
    expect(gridKeyAction(chord("ContextMenu"), false)).toBe("menu");
    expect(gridKeyAction(chord("Tab", { shiftKey: true }), false)).toBe("previous");
    expect(gridKeyAction(chord("Escape"), true)).toBe("cancel");
    expect(gridKeyAction(chord("Escape"), false)).toBe("leave");
  });
});
