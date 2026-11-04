import { useRef, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { buttonClassName, Card, PageHeader } from "../../shell/ui";
import { createProcessCategory, describeProblem, newKey, type NewProcessCategory, type Problem } from "./api/client";
import styles from "./pages.module.css";
import { emptyCategoryValues, ProcessCategoryForm } from "./ProcessCategoryForm";

/** Create a process message category, or the row that replaces a built-in one (FR-25.17). */
export function NewProcessCategoryPage() {
  const navigate = useNavigate();
  const [failure, setFailure] = useState<Problem | null>(null);
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  async function submit(input: NewProcessCategory) {
    setBusy(true);
    setFailure(null);
    setFieldErrors({});
    try {
      await createProcessCategory(input, key.current);
      navigate("/shopfloor/process-categories");
    } catch (error) {
      const problem = describeProblem(error);
      const found: Record<string, string> = { ...problem.fields };
      if (problem.code === "SF-0142") found.code = "This code is already used.";
      setFieldErrors(found);
      // A used code is shown on the code field; anything else also gets the alert, so a field the
      // form does not render (such as the number of characteristics) is never swallowed.
      if (problem.code !== "SF-0142") setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New process category" description="A category decides where a process message is delivered and which values it accepts." />
      <Card>
        <ProcessCategoryForm
          initial={emptyCategoryValues()}
          creating
          submitLabel="Create category"
          failureTitle="Could not create the category"
          failure={failure}
          busy={busy}
          fieldErrors={fieldErrors}
          onChange={() => {
            key.current = newKey();
          }}
          onSubmit={(input) => void submit(input)}
          actions={
            <Link className={buttonClassName("ghost")} to="/shopfloor/process-categories">
              Cancel
            </Link>
          }
        />
      </Card>
    </div>
  );
}
