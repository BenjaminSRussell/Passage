import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { HierarchyNode } from "./api/hierarchy";
import { draftNodes } from "./hierarchyTestKit";
import { HierarchyTreeView, type TreeEditing } from "./HierarchyTreeView";

const editing = (focusSignal: number): TreeEditing => ({
  selectedId: "ou-c",
  focusSignal,
  locked: false,
  onSelect: vi.fn(),
  onMove: vi.fn(),
  onDrop: vi.fn(),
});

const view = (focusSignal: number) => <HierarchyTreeView nodes={draftNodes as HierarchyNode[]} labels={new Map()} changes={new Map()} editing={editing(focusSignal)} />;

describe("tree focus request", () => {
  it("reveals a collapsed ancestor of the selection and focuses it when focus is requested", () => {
    const { rerender } = render(view(0));
    fireEvent.click(screen.getByTestId("toggle-ou-b"));
    expect(screen.queryByRole("treeitem", { name: "ou-c" })).toBeNull();
    rerender(view(1));
    expect(screen.getByRole("treeitem", { name: "ou-c" })).toHaveFocus();
  });
});
