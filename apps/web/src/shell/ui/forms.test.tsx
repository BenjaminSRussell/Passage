import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import axe from "axe-core";
import { useEffect, useState } from "react";
import { createMemoryRouter, Link, MemoryRouter, RouterProvider, useNavigate } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { FormErrorSummary, FormField, FormSection, Input, summaryItems, useFormBaseline, useUnsavedChanges } from "./index";

function Demo() {
  const [name, setName] = useState("");
  const [note, setNote] = useState("");
  const [attempt, setAttempt] = useState(0);
  const errors = attempt > 0 && name === "" ? [{ fieldId: "demo-name", label: "Name", message: "Enter a name." }] : [];
  const noteErrors = attempt > 0 && note.length > 5 ? [{ fieldId: "demo-note", label: "Note", message: "Use up to 5 characters." }] : [];
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        setAttempt((n) => n + 1);
      }}
      noValidate
    >
      <FormErrorSummary items={[...noteErrors, ...errors]} attempt={attempt} />
      <FormSection title="Basics">
        <FormField id="demo-name" label="Name" required error={errors[0]?.message}>
          {(c) => <Input {...c} value={name} onChange={(e) => setName(e.target.value)} />}
        </FormField>
      </FormSection>
      <FormSection collapse="more">
        <FormField id="demo-note" label="Note" optional hint="Shown on the printout." error={noteErrors[0]?.message}>
          {(c) => <Input {...c} value={note} onChange={(e) => setNote(e.target.value)} />}
        </FormField>
      </FormSection>
      <button type="submit">Save</button>
    </form>
  );
}

describe("form sections and marking", () => {
  it("collapses optional fields behind a More options summary that is reachable by Tab", async () => {
    const user = userEvent.setup();
    const { container } = render(<Demo />);
    const details = container.querySelector("details")!;
    expect(details.open).toBe(false);
    await user.tab();
    await user.tab();
    expect(screen.getByText("More options")).toHaveFocus();
    await user.click(screen.getByText("More options"));
    expect(details.open).toBe(true);
  });

  it("marks required and optional fields for assistive technology and sighted users", () => {
    render(<Demo />);
    expect(screen.getByLabelText("Name")).toHaveAttribute("aria-required", "true");
    expect(screen.getByText("Required")).toBeInTheDocument();
    expect(screen.getByLabelText("Note")).not.toHaveAttribute("aria-required");
    expect(screen.getByText("Optional")).toBeInTheDocument();
    expect(screen.getByLabelText("Note")).toHaveAccessibleDescription("Shown on the printout.");
  });

  it("labels a titled section by its heading", () => {
    render(<Demo />);
    expect(screen.getByRole("region", { name: "Basics" })).toBeInTheDocument();
  });

  it("passes axe with sections, help, required marks and a visible error summary", async () => {
    const user = userEvent.setup();
    const { container } = render(<Demo />);
    await user.click(screen.getByRole("button", { name: "Save" }));
    const result = await axe.run(container, { rules: { "color-contrast": { enabled: false }, region: { enabled: false } } });
    expect(result.violations.map((v) => v.id)).toEqual([]);
  });
});

describe("inline validation summary", () => {
  it("stays hidden until there is something to fix", () => {
    const { container } = render(<Demo />);
    expect(container.querySelector('[aria-label="Problems with this form"]')).not.toBeVisible();
  });

  it("moves focus to the first problem on a failed submit and lists it", async () => {
    const user = userEvent.setup();
    render(<Demo />);
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(screen.getByLabelText("Name")).toHaveFocus();
    expect(screen.getByText("1 thing to fix before you can save")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Name: Enter a name." })).toBeInTheDocument();
  });

  it("opens a collapsed section to focus a problem inside it", async () => {
    const user = userEvent.setup();
    const { container } = render(<Demo />);
    await user.type(screen.getByLabelText("Name"), "Ada");
    await user.type(screen.getByLabelText("Note"), "too long");
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(container.querySelector("details")!.open).toBe(true);
    expect(screen.getByLabelText("Note")).toHaveFocus();
  });

  it("focuses a field from its summary entry", async () => {
    const user = userEvent.setup();
    render(<Demo />);
    await user.click(screen.getByRole("button", { name: "Save" }));
    screen.getByRole("button", { name: "Save" }).focus();
    await user.click(screen.getByRole("button", { name: "Name: Enter a name." }));
    expect(screen.getByLabelText("Name")).toHaveFocus();
  });
});

function Baseline() {
  const [value, setValue] = useState("");
  const dirty = useFormBaseline(value);
  useEffect(() => {
    const timer = setTimeout(() => setValue("filled in by the system"), 0);
    return () => clearTimeout(timer);
  }, []);
  return (
    <>
      <button type="button">Sidebar</button>
      <input aria-label="Outside" />
      <form>
        <input aria-label="Field" value={value} onChange={(e) => setValue(e.target.value)} />
        <ul>
          <li role="option" aria-selected="false" onClick={() => setValue("picked")}>
            Pick me
          </li>
        </ul>
      </form>
      <output>{dirty ? "dirty" : "clean"}</output>
    </>
  );
}

describe("form baseline", () => {
  it("treats a value filled in by the system after mount as untouched, and a user edit as dirty", async () => {
    render(<Baseline />);
    await waitFor(() => expect(screen.getByLabelText("Field")).toHaveValue("filled in by the system"));
    expect(screen.getByRole("status")).toHaveTextContent("clean");
    fireEvent.change(screen.getByLabelText("Field"), { target: { value: "mine" } });
    expect(screen.getByRole("status")).toHaveTextContent("dirty");
  });

  it("is not frozen by a key press, a click or typing outside the form before a late prefill", async () => {
    const user = userEvent.setup();
    render(<Baseline />);
    await user.tab();
    await user.click(screen.getByRole("button", { name: "Sidebar" }));
    await user.type(screen.getByLabelText("Outside"), "search");
    await waitFor(() => expect(screen.getByLabelText("Field")).toHaveValue("filled in by the system"));
    expect(screen.getByRole("status")).toHaveTextContent("clean");
  });

  it("counts a pick from a list inside the form as an edit", async () => {
    const user = userEvent.setup();
    render(<Baseline />);
    await waitFor(() => expect(screen.getByLabelText("Field")).toHaveValue("filled in by the system"));
    await user.click(screen.getByRole("option", { name: "Pick me" }));
    expect(screen.getByRole("status")).toHaveTextContent("dirty");
  });
});

describe("summary without a field to focus", () => {
  it("takes focus itself and is announced as an alert", () => {
    const { rerender } = render(<FormErrorSummary items={[]} attempt={0} />);
    rerender(<FormErrorSummary items={[{ label: "Lines", message: "Add at least one line." }]} attempt={1} />);
    const summary = screen.getByRole("alert", { name: "Problems with this form" });
    expect(summary).toHaveFocus();
    expect(summary).toHaveTextContent("Lines: Add at least one line.");
  });

  it("focuses the first entry that has a field, skipping fieldless ones", () => {
    render(
      <>
        <input id="f" aria-label="F" />
        <FormErrorSummary items={[{ label: "Lines", message: "x" }, { fieldId: "f", label: "F", message: "y" }]} attempt={1} />
      </>,
    );
    expect(screen.getByLabelText("F")).toHaveFocus();
  });

  it("builds items in declared form order, then computed ones", () => {
    const items = summaryItems(
      { b: "B bad", a: "A bad", "row[1]": "Row bad" },
      { a: { id: "a-id", label: "A" }, b: { id: "b-id", label: "B" } },
      (key) => (key === "row[1]" ? { id: "row-1", label: "Row 2" } : undefined),
    );
    expect(items.map((i) => i.fieldId)).toEqual(["a-id", "b-id", "row-1"]);
  });
});

function Guarded({ saveTo }: { saveTo: string }) {
  const [value, setValue] = useState("");
  const { prompt, release } = useUnsavedChanges(value !== "");
  const navigate = useNavigate();
  return (
    <div>
      <label htmlFor="g">Value</label>
      <input id="g" value={value} onChange={(e) => setValue(e.target.value)} />
      <button
        type="button"
        onClick={() => {
          release();
          navigate(saveTo);
        }}
      >
        Save
      </button>
      <Link to="/elsewhere">Go elsewhere</Link>
      {prompt}
    </div>
  );
}

function renderGuarded() {
  const router = createMemoryRouter(
    [
      { path: "/", element: <Guarded saveTo="/saved" /> },
      { path: "/elsewhere", element: <p>Elsewhere page</p> },
      { path: "/saved", element: <p>Saved page</p> },
    ],
    { initialEntries: ["/"] },
  );
  render(<RouterProvider router={router} />);
  return router;
}

describe("unsaved-changes guard", () => {
  it("lets a clean form navigate away", async () => {
    const user = userEvent.setup();
    renderGuarded();
    await user.click(screen.getByRole("link", { name: "Go elsewhere" }));
    expect(screen.getByText("Elsewhere page")).toBeInTheDocument();
  });

  it("asks before leaving a dirty form and keeps editing on cancel", async () => {
    const user = userEvent.setup();
    renderGuarded();
    await user.type(screen.getByLabelText("Value"), "x");
    await user.click(screen.getByRole("link", { name: "Go elsewhere" }));
    expect(screen.getByRole("alertdialog", { name: "Leave without saving?" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Keep editing" }));
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Value")).toHaveValue("x");
  });

  it("leaves when the user confirms, and the dialog is operable by keyboard", async () => {
    const user = userEvent.setup();
    renderGuarded();
    await user.type(screen.getByLabelText("Value"), "x");
    await user.click(screen.getByRole("link", { name: "Go elsewhere" }));
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    await user.click(screen.getByRole("link", { name: "Go elsewhere" }));
    await user.click(screen.getByRole("button", { name: "Leave without saving" }));
    expect(await screen.findByText("Elsewhere page")).toBeInTheDocument();
  });

  it("does not block the navigation that follows a successful save", async () => {
    const user = userEvent.setup();
    renderGuarded();
    await user.type(screen.getByLabelText("Value"), "x");
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText("Saved page")).toBeInTheDocument();
  });

  it("warns on tab close only while dirty", async () => {
    const user = userEvent.setup();
    renderGuarded();
    const clean = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(clean);
    expect(clean.defaultPrevented).toBe(false);
    await user.type(screen.getByLabelText("Value"), "x");
    const dirty = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(dirty);
    expect(dirty.defaultPrevented).toBe(true);
  });

  it("still works outside a data router (no navigation prompt, tab-close guard only)", async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <Guarded saveTo="/" />
      </MemoryRouter>,
    );
    await user.type(screen.getByLabelText("Value"), "x");
    await waitFor(() => {
      const event = new Event("beforeunload", { cancelable: true });
      window.dispatchEvent(event);
      expect(event.defaultPrevented).toBe(true);
    });
  });
});
