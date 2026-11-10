import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Button, DataTable, FormField, Input, StatusPill } from "./index";

describe("design-system primitives", () => {
  it("a loading Button stays focusable and ignores clicks and submits", async () => {
    const user = userEvent.setup();
    const clicks: string[] = [];
    const submits: string[] = [];
    const { rerender } = render(
      <form onSubmit={(e) => { e.preventDefault(); submits.push("submit"); }}>
        <Button type="submit" variant="primary" onClick={() => clicks.push("click")}>
          Find parents
        </Button>
      </form>,
    );
    const button = screen.getByRole("button", { name: "Find parents" });
    button.focus();
    rerender(
      <form onSubmit={(e) => { e.preventDefault(); submits.push("submit"); }}>
        <Button type="submit" variant="primary" loading onClick={() => clicks.push("click")}>
          Find parents
        </Button>
      </form>,
    );
    expect(button).not.toBeDisabled();
    expect(button).toHaveAttribute("aria-disabled", "true");
    expect(button).toHaveAttribute("aria-busy", "true");
    expect(button).toHaveFocus();
    await user.click(button);
    expect(clicks).toEqual([]);
    expect(submits).toEqual([]);
  });

  it("an explicitly disabled Button is natively disabled", () => {
    render(<Button disabled>Save</Button>);
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  });

  it("StatusPill maps a status to its label without adding a live role", () => {
    render(<StatusPill status="failed" />);
    expect(screen.getByText("Failed")).toBeInTheDocument();
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });

  it("FormField wires hint and error into aria-describedby", () => {
    render(
      <FormField id="name" label="Name" hint="Shown to everyone" error="Name is required">
        {(control) => <Input {...control} />}
      </FormField>,
    );
    const input = screen.getByLabelText("Name");
    const error = screen.getByRole("alert");
    expect(error).toHaveTextContent("Name is required");
    expect(input).toHaveAttribute("aria-invalid", "true");
    expect(input.getAttribute("aria-describedby")).toBe(`name-hint ${error.id}`);
  });

  it("DataTable renders the empty title in one spanning cell", () => {
    render(
      <DataTable
        label="Things"
        columns={[
          { key: "a", header: "A" },
          { key: "b", header: "B" },
          { key: "c", header: "C" },
        ]}
        rows={[]}
        getRowKey={(_, i) => String(i)}
        empty={{ title: "Nothing yet" }}
      />,
    );
    expect(screen.getByText("Nothing yet").closest("td")).toHaveAttribute("colspan", "3");
  });

  it("DataTable opens a row from its first-column button", () => {
    const opened: string[] = [];
    render(
      <DataTable
        label="Things"
        columns={[{ key: "name", header: "Name" }]}
        rows={[{ name: "alpha" }]}
        getRowKey={(row) => row.name}
        onRowOpen={(row) => opened.push(row.name)}
      />,
    );
    screen.getByRole("button", { name: "alpha" }).click();
    expect(opened).toEqual(["alpha"]);
  });
});
