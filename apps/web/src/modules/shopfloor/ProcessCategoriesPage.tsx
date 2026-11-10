import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { createProcessCategory, describeProblem, listProcessCategories, newKey, updateProcessCategory, type ProcessCategoryDef, type Problem } from "./api/client";
import styles from "./pages.module.css";
import { categoryLabel, DESTINATION_LABELS } from "./processLabels";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

/**
 * The process message category master (FR-25.17): the destinations, limits and characteristic
 * definitions behind each category. A built-in default is customized by creating its row.
 */
export function ProcessCategoriesPage() {
  const state = useLoad((signal) => listProcessCategories({}, signal), "process-categories");
  const navigate = useNavigate();
  const [busy, setBusy] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);

  async function toggle(category: ProcessCategoryDef) {
    setBusy(category.code);
    setFailure(null);
    try {
      await updateProcessCategory(category, { active: !category.active });
      state.reload();
    } catch (error) {
      setFailure(describeProblem(error));
      state.reload();
    } finally {
      setBusy(null);
    }
  }

  async function customize(category: ProcessCategoryDef) {
    setBusy(category.code);
    setFailure(null);
    try {
      const created = await createProcessCategory(
        {
          code: category.code,
          name: categoryLabel(category.code, category.name),
          destinations: category.destinations,
          rejectOutOfLimit: category.rejectOutOfLimit,
          characteristics: category.characteristics,
        },
        newKey(),
      );
      navigate(`/shopfloor/process-categories/${created.id ?? ""}`);
    } catch (error) {
      setFailure(describeProblem(error));
      state.reload();
    } finally {
      setBusy(null);
    }
  }

  const columns: DataTableColumn<ProcessCategoryDef>[] = [
    { key: "code", header: "Code", mono: true },
    { key: "name", header: "Name", render: (row) => categoryLabel(row.code, row.name) },
    { key: "destinations", header: "Destinations", render: (row) => row.destinations.map((d) => DESTINATION_LABELS[d] ?? d).join(", ") },
    { key: "reject", header: "Out-of-limit values", render: (row) => (row.rejectOutOfLimit ? <Badge tone="warning">Refused</Badge> : <Badge tone="neutral">Flagged</Badge>) },
    { key: "defs", header: "Characteristics", align: "end", render: (row) => row.characteristics.length },
    {
      key: "status",
      header: "Status",
      render: (row) => (row.builtIn ? <Badge tone="neutral">Built-in default</Badge> : row.active ? <Badge tone="success">Active</Badge> : <Badge tone="neutral">Inactive</Badge>),
    },
    {
      key: "action",
      header: "Action",
      render: (row) =>
        row.builtIn ? (
          <Button size="sm" loading={busy === row.code} aria-label={`Customize category ${row.code}`} onClick={() => void customize(row)}>
            Customize
          </Button>
        ) : (
          <span>
            <Link to={`/shopfloor/process-categories/${row.id ?? ""}`} aria-label={`Edit category ${row.code}`}>
              Edit
            </Link>{" "}
            <Button size="sm" loading={busy === row.code} aria-label={`${row.active ? "Deactivate" : "Activate"} category ${row.code}`} onClick={() => void toggle(row)}>
              {row.active ? "Deactivate" : "Activate"}
            </Button>
          </span>
        ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Process categories"
        description="Each category routes process messages to destinations and may define the characteristics, units and limits its values are checked against. Built-in defaults apply until you customize them."
        actions={
          <Link className={buttonClassName("primary")} to="/shopfloor/process-categories/new">
            New process category
          </Link>
        }
      />
      {failure ? (
        <Alert tone="danger" title="Could not change the category" live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      {state.error ? (
        <Alert tone="danger" title="Could not load process categories" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Process categories"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id ?? `builtin-${row.code}`}
          loading={state.loading}
          empty={{ title: "No process categories", description: "Create one, such as a weigh-in with limits, to check and route its messages." }}
        />
      )}
    </div>
  );
}
