import { useEffect, useState } from "react";
import { Alert, Button, Card, FormField, Select } from "../../shell/ui";
import { describeProblem, getRowSet, listColumnSets, listColumnTemplates, listRowSets, type ColumnSet, type DefinitionColumn, type DefinitionRow, type Problem, type RowSet } from "./api/client";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

interface Parts {
  rowSets: RowSet[];
  columnSets: ColumnSet[];
  templates: ColumnSet[];
}

/**
 * Starts a definition from saved parts (FR-15.2, FR-15.3): replaces its rows with a row set or
 * its columns with a column set or comparative template. Nothing is linked: the rows and columns
 * are copied, and later edits of the set do not change the definition.
 */
export function PartsPicker({ onRows, onColumns }: { onRows: (rows: DefinitionRow[]) => void; onColumns: (columns: DefinitionColumn[]) => void }) {
  const [parts, setParts] = useState<Parts | null>(null);
  const [error, setError] = useState<Problem | null>(null);
  const [attempt, setAttempt] = useState(0);
  const [rowCode, setRowCode] = useState("");
  const [colCode, setColCode] = useState("");
  const [notice, setNotice] = useState<string | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    setError(null);
    Promise.all([listRowSets(controller.signal), listColumnSets(controller.signal), listColumnTemplates(controller.signal)]).then(
      ([rowSets, columnSets, templates]) => setParts({ rowSets, columnSets, templates }),
      (failure: unknown) => {
        if (!controller.signal.aborted) setError(describeProblem(failure));
      },
    );
    return () => controller.abort();
  }, [attempt]);

  if (error) {
    return (
      <Alert tone="warning" title="Row sets and column sets could not be loaded" live="alert" action={<Button size="sm" onClick={() => setAttempt((n) => n + 1)}>Try again</Button>}>
        <ProblemMessage problem={error} />
      </Alert>
    );
  }
  const columnChoices = parts ? [...parts.columnSets, ...parts.templates.filter((t) => !parts.columnSets.some((s) => s.code === t.code))] : [];

  async function useRows() {
    try {
      const { part } = await getRowSet(rowCode);
      onRows(part.rows);
      setNotice(`Rows replaced with the ${part.rows.length} rows of ${part.code}.`);
    } catch (failure) {
      setError(describeProblem(failure));
    }
  }
  function useColumns() {
    const set = columnChoices.find((c) => c.code === colCode);
    if (!set) return;
    onColumns(set.columns);
    setNotice(`Columns replaced with the ${set.columns.length} columns of ${set.code}.`);
  }

  return (
    <Card title="Start from saved parts">
      <div className={styles.form}>
        <FormField id="fr-use-rows" label="Row set" hint="Replaces all rows of this definition." optional>
          {(c) => (
            <Select {...c} value={rowCode} disabled={!parts} onChange={(e) => setRowCode(e.target.value)}>
              <option value="">{parts ? (parts.rowSets.length > 0 ? "Choose a row set" : "No row sets saved yet") : "Loading row sets…"}</option>
              {parts?.rowSets.map((s) => (
                <option key={s.code} value={s.code}>{`${s.code}: ${s.name}`}</option>
              ))}
            </Select>
          )}
        </FormField>
        <div className={styles.lineActions}>
          <Button size="sm" onClick={useRows} disabled={rowCode === ""}>
            Use rows
          </Button>
        </div>
        <FormField id="fr-use-columns" label="Column set or comparative template" hint="Replaces all columns of this definition." optional>
          {(c) => (
            <Select {...c} value={colCode} disabled={!parts} onChange={(e) => setColCode(e.target.value)}>
              <option value="">{parts ? "Choose a column set" : "Loading column sets…"}</option>
              {columnChoices.map((s) => (
                <option key={s.code} value={s.code}>{`${s.code}: ${s.name}`}</option>
              ))}
            </Select>
          )}
        </FormField>
        <div className={styles.lineActions}>
          <Button size="sm" onClick={useColumns} disabled={colCode === ""}>
            Use columns
          </Button>
        </div>
      </div>
      {notice ? (
        <Alert tone="success" live="status">
          {notice}
        </Alert>
      ) : null}
    </Card>
  );
}
