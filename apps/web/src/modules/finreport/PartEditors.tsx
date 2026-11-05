import { Button, Card, FormField, Input, Select } from "../../shell/ui";
import { CURRENCY_TYPES, ROW_KINDS, type CurrencyType, type DefinitionColumn, type DefinitionRow, type RowKind } from "./api/client";
import { CURRENCY_TYPE_LABELS, formatDimensionFilter, parseDimensionFilter, parseReferences, ROW_KIND_LABELS } from "./format";
import styles from "./pages.module.css";

export const CASH_FLOW_TAGS = ["", "operating_adjustment", "working_capital", "investing", "financing", "cash"] as const;
const CASH_FLOW_LABELS: Record<(typeof CASH_FLOW_TAGS)[number], string> = {
  "": "None",
  operating_adjustment: "Operating adjustment",
  working_capital: "Working capital",
  investing: "Investing",
  financing: "Financing",
  cash: "Cash",
};

export function move<T>(list: T[], from: number, to: number): T[] {
  if (to < 0 || to >= list.length) return list;
  const next = list.slice();
  const [item] = next.splice(from, 1);
  next.splice(to, 0, item);
  return next;
}

/** The row cards of the report designer; shared by definitions and row sets. */
export function RowsEditor({ rows, onChange }: { rows: DefinitionRow[]; onChange: (rows: DefinitionRow[]) => void }) {
  function changeRow(i: number, patch: Partial<DefinitionRow>) {
    onChange(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  }
  return (
    <Card title={`Rows (${rows.length})`} actions={<Button size="sm" onClick={() => onChange([...rows, { code: `R${rows.length + 1}`, kind: "account_range", label: "", normalBalance: "debit", format: {} }])}>Add row</Button>}>
      <div className={styles.editorGrid}>
        {rows.map((row, i) => (
          <fieldset key={i} className={styles.fieldset}>
            <legend className={styles.legend}>{`Row ${i + 1}${row.label ? `: ${row.label}` : ""}`}</legend>
            <div className={styles.editorLine}>
              <FormField id={`fr-row-${i}-code`} label="Code">
                {(c) => <Input {...c} className={styles.mono} value={row.code} onChange={(e) => changeRow(i, { code: e.target.value })} />}
              </FormField>
              <FormField id={`fr-row-${i}-kind`} label="Kind">
                {(c) => (
                  <Select {...c} value={row.kind} onChange={(e) => changeRow(i, { kind: e.target.value as RowKind })}>
                    {ROW_KINDS.map((k) => (
                      <option key={k} value={k}>
                        {ROW_KIND_LABELS[k]}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
              <FormField id={`fr-row-${i}-label`} label="Label">
                {(c) => <Input {...c} value={row.label} onChange={(e) => changeRow(i, { label: e.target.value })} />}
              </FormField>
              {row.kind === "account_range" ? (
                <>
                  <FormField id={`fr-row-${i}-from`} label="Account from">
                    {(c) => <Input {...c} className={styles.mono} value={row.accountFrom ?? ""} onChange={(e) => changeRow(i, { accountFrom: e.target.value })} />}
                  </FormField>
                  <FormField id={`fr-row-${i}-to`} label="Account to">
                    {(c) => <Input {...c} className={styles.mono} value={row.accountTo ?? ""} onChange={(e) => changeRow(i, { accountTo: e.target.value })} />}
                  </FormField>
                </>
              ) : null}
              {row.kind === "account_range" || row.kind === "dimension_filter" ? (
                <FormField id={`fr-row-${i}-dims`} label="Dimension filter" optional={row.kind === "account_range"} hint="attribute=value,value; use * for any value">
                  {(c) => (
                    <Input
                      {...c}
                      key={`${i}-${formatDimensionFilter(row.dimensionFilter)}`}
                      className={styles.mono}
                      defaultValue={formatDimensionFilter(row.dimensionFilter)}
                      onBlur={(e) => changeRow(i, { dimensionFilter: parseDimensionFilter(e.target.value) })}
                    />
                  )}
                </FormField>
              ) : null}
              {row.kind === "total" ? (
                <FormField id={`fr-row-${i}-refs`} label="Rows to total" hint="Row codes separated by commas">
                  {(c) => (
                    <Input
                      {...c}
                      key={`${i}-${(row.references ?? []).join(",")}`}
                      className={styles.mono}
                      defaultValue={(row.references ?? []).join(", ")}
                      onBlur={(e) => changeRow(i, { references: parseReferences(e.target.value) })}
                    />
                  )}
                </FormField>
              ) : null}
              {row.kind === "calculation" ? (
                <FormField id={`fr-row-${i}-expr`} label="Expression" hint="Row codes with + - * / and numbers; prefix raw: for stored net">
                  {(c) => <Input {...c} className={styles.mono} value={row.expression ?? ""} onChange={(e) => changeRow(i, { expression: e.target.value })} />}
                </FormField>
              ) : null}
              <FormField id={`fr-row-${i}-normal`} label="Normal balance">
                {(c) => (
                  <Select {...c} value={row.normalBalance} onChange={(e) => changeRow(i, { normalBalance: e.target.value as "debit" | "credit" })}>
                    <option value="debit">Debit</option>
                    <option value="credit">Credit (shown with sign flipped)</option>
                  </Select>
                )}
              </FormField>
              <FormField id={`fr-row-${i}-cf`} label="Cash flow tag">
                {(c) => (
                  <Select {...c} value={row.cashFlowTag ?? ""} onChange={(e) => changeRow(i, { cashFlowTag: e.target.value })}>
                    {CASH_FLOW_TAGS.map((t) => (
                      <option key={t} value={t}>
                        {CASH_FLOW_LABELS[t]}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
              <FormField id={`fr-row-${i}-indent`} label="Indent">
                {(c) => (
                  <Select {...c} value={String(row.format?.indent ?? 0)} onChange={(e) => changeRow(i, { format: { ...row.format, indent: Number(e.target.value) } })}>
                    {[0, 1, 2, 3].map((n) => (
                      <option key={n} value={n}>
                        {n}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
              <span className={styles.check}>
                <Input id={`fr-row-${i}-bold`} type="checkbox" checked={Boolean(row.format?.bold)} onChange={(e) => changeRow(i, { format: { ...row.format, bold: e.target.checked } })} />
                <label htmlFor={`fr-row-${i}-bold`}>Bold</label>
              </span>
              <span className={styles.check}>
                <Input id={`fr-row-${i}-zero`} type="checkbox" checked={Boolean(row.format?.printZero)} onChange={(e) => changeRow(i, { format: { ...row.format, printZero: e.target.checked } })} />
                <label htmlFor={`fr-row-${i}-zero`}>Print zero</label>
              </span>
              <div className={styles.lineActions}>
                <Button size="sm" variant="ghost" aria-label={`Move row ${i + 1} up`} disabled={i === 0} onClick={() => onChange(move(rows, i, i - 1))}>
                  Up
                </Button>
                <Button size="sm" variant="ghost" aria-label={`Move row ${i + 1} down`} disabled={i === rows.length - 1} onClick={() => onChange(move(rows, i, i + 1))}>
                  Down
                </Button>
                <Button size="sm" variant="ghost" aria-label={`Remove row ${i + 1}`} disabled={rows.length === 1} onClick={() => onChange(rows.filter((_, j) => j !== i))}>
                  Remove
                </Button>
              </div>
            </div>
          </fieldset>
        ))}
      </div>
    </Card>
  );
}

/** The column cards of the report designer; shared by definitions and column sets. */
export function ColumnsEditor({ columns, onChange }: { columns: DefinitionColumn[]; onChange: (columns: DefinitionColumn[]) => void }) {
  function changeColumn(i: number, patch: Partial<DefinitionColumn>) {
    onChange(columns.map((c, j) => (j === i ? { ...c, ...patch } : c)));
  }
  return (
    <Card
      title={`Columns (${columns.length})`}
      actions={<Button size="sm" onClick={() => onChange([...columns, { code: `C${columns.length + 1}`, label: "", periodExpr: "P" }])}>Add column</Button>}
    >
      <div className={styles.editorGrid}>
        {columns.map((col, i) => (
          <fieldset key={i} className={styles.fieldset}>
            <legend className={styles.legend}>{`Column ${i + 1}${col.label ? `: ${col.label}` : ""}`}</legend>
            <div className={styles.editorLine}>
              <FormField id={`fr-col-${i}-code`} label="Code">
                {(c) => <Input {...c} className={styles.mono} value={col.code} onChange={(e) => changeColumn(i, { code: e.target.value })} />}
              </FormField>
              <FormField id={`fr-col-${i}-label`} label="Label">
                {(c) => <Input {...c} value={col.label} onChange={(e) => changeColumn(i, { label: e.target.value })} />}
              </FormField>
              <FormField id={`fr-col-${i}-period`} label="Period" hint="P, P-n, YTD, PY, PYTD, Q, FY or CALC">
                {(c) => <Input {...c} className={styles.mono} value={col.periodExpr} onChange={(e) => changeColumn(i, { periodExpr: e.target.value.toUpperCase() })} />}
              </FormField>
              {col.periodExpr.trim() === "CALC" ? (
                <FormField id={`fr-col-${i}-calc`} label="Calculation" hint="Column codes, such as C1-C2">
                  {(c) => <Input {...c} className={styles.mono} value={col.calcExpression ?? ""} onChange={(e) => changeColumn(i, { calcExpression: e.target.value })} />}
                </FormField>
              ) : (
                <FormField id={`fr-col-${i}-currency`} label="Currency type">
                  {(c) => (
                    <Select {...c} value={col.currencyType ?? ""} onChange={(e) => changeColumn(i, { currencyType: (e.target.value || undefined) as CurrencyType | undefined })}>
                      <option value="">The run's currency type</option>
                      {CURRENCY_TYPES.map((t) => (
                        <option key={t} value={t}>
                          {CURRENCY_TYPE_LABELS[t]}
                        </option>
                      ))}
                    </Select>
                  )}
                </FormField>
              )}
              {col.periodExpr.trim() === "CALC" ? null : (
                <FormField id={`fr-col-${i}-dims`} label="Dimension filter" optional hint="attribute=value,value; use * for any value">
                  {(c) => (
                    <Input
                      {...c}
                      key={`${i}-${formatDimensionFilter(col.dimensionFilter)}`}
                      className={styles.mono}
                      defaultValue={formatDimensionFilter(col.dimensionFilter)}
                      onBlur={(e) => changeColumn(i, { dimensionFilter: parseDimensionFilter(e.target.value) })}
                    />
                  )}
                </FormField>
              )}
              <div className={styles.lineActions}>
                <Button size="sm" variant="ghost" aria-label={`Remove column ${i + 1}`} disabled={columns.length === 1} onClick={() => onChange(columns.filter((_, j) => j !== i))}>
                  Remove
                </Button>
              </div>
            </div>
          </fieldset>
        ))}
      </div>
    </Card>
  );
}
