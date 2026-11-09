import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, PageHeader, Skeleton } from "../../shell/ui";
import { describeProblem, getProcessCategory, updateProcessCategory, type NewProcessCategory, type Problem } from "./api/client";
import styles from "./pages.module.css";
import { categoryValues, ProcessCategoryForm } from "./ProcessCategoryForm";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

/** One process message category with the form that changes it; the code stays (FR-25.17). */
export function ProcessCategoryDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getProcessCategory(id, signal), `process-category|${id}`);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [saved, setSaved] = useState(false);
  const [busy, setBusy] = useState(false);

  async function submit(input: NewProcessCategory) {
    if (!state.data) return;
    setBusy(true);
    setFailure(null);
    setSaved(false);
    try {
      const { code: _code, ...changes } = input;
      await updateProcessCategory(state.data, changes);
      setSaved(true);
      state.reload();
    } catch (error) {
      setFailure(describeProblem(error));
      // A stale version (SF-0143) is fixed by reading the category again.
      state.reload();
    } finally {
      setBusy(false);
    }
  }

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Process category" />
        <Alert tone="danger" title="Could not load the category" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Process category" />
        <Skeleton aria-label="Loading the category" />
      </div>
    );
  }
  const category = state.data;
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Process category ${category.code}`}
        description="Changes apply to messages received from now on; stored messages keep the routing they were received with."
        actions={category.active ? <Badge tone="success">Active</Badge> : <Badge tone="neutral">Inactive</Badge>}
      />
      {saved ? (
        <Alert tone="success" title="Category saved" live="status">
          The category is now at version {category.version}.
        </Alert>
      ) : null}
      <Card>
        <ProcessCategoryForm
          // A new version remounts the form with what the service stored.
          key={category.version}
          initial={categoryValues(category)}
          creating={false}
          submitLabel="Save category"
          failureTitle="Could not save the category"
          failure={failure}
          busy={busy}
          onSubmit={(input) => void submit(input)}
          actions={
            <Link className={buttonClassName("ghost")} to="/shopfloor/process-categories">
              Back to categories
            </Link>
          }
        />
      </Card>
    </div>
  );
}
