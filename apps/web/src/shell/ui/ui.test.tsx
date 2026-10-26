import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { DataTable, FormField, Input, StatusPill, WorkspacePlaceholder } from "./index";

describe("design-system primitives", () => {
  it("WorkspacePlaceholder explains the workspace and links back home", () => {
    render(
      <MemoryRouter>
        <WorkspacePlaceholder title="Tax" icon="percent" description="Tax configuration is coming." />
      </MemoryRouter>,
    );
    expect(screen.getByRole("heading", { level: 2, name: "Tax" })).toBeInTheDocument();
    expect(screen.getByText("This workspace isn't available yet")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Browse workspaces" })).toHaveAttribute("href", "/");
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
