import { useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, FormField, Input, Select, type DataTableColumn } from "../../shell/ui";
import { describeProblem, type Problem } from "./api/client";
import {
  convertUnit,
  createConversion,
  createUnit,
  listConversions,
  listUnits,
  setUnitStatus,
  unitClasses,
  type Conversion,
  type Unit,
  type UnitClass,
  type UomConversion,
} from "./api/reference";
import { useLegalEntity } from "./legalEntityContext";
import styles from "./pages.module.css";
import { FilterSelect, StatusTable, statusOptions, useRows, type RowsState, type StatusFilterValue } from "./referenceParts";

const unitCodePattern = /^[A-Za-z0-9/%]{1,10}$/;
const precisionPattern = /^[0-6]$/;
const factorPattern = /^[0-9]{1,16}(\.[0-9]{1,12})?$/;
const offsetPattern = /^-?[0-9]{1,16}(\.[0-9]{1,12})?$/;
const quantityPattern = /^-?[0-9]+(\.[0-9]+)?$/;

const conversionMessages: Record<string, string> = {
  "ORG-UOM-UNIT-NOT-FOUND": "One of the selected units does not exist.",
  "ORG-UOM-CONVERSION-DUPLICATE": "A conversion between these two units already exists.",
  "ORG-UOM-UNIT-INACTIVE": "One of the selected units is inactive.",
  "ORG-UOM-CLASS-MISMATCH": "The two units belong to different classes.",
};

type Errors<K extends string> = Partial<Record<K, string>>;

export interface UnitValues {
  code: string;
  name: string;
  decimalPrecision: string;
}

export function validateUnit({ code, name, decimalPrecision }: UnitValues): Errors<keyof UnitValues> {
  const errors: Errors<keyof UnitValues> = {};
  if (!unitCodePattern.test(code)) {
    errors.code = "Use 1 to 10 letters, digits, / or %.";
  }
  if (name.trim() === "" || name.trim().length > 120) {
    errors.name = "Enter a name of 1 to 120 characters.";
  }
  if (!precisionPattern.test(decimalPrecision)) {
    errors.decimalPrecision = "Enter a whole number from 0 to 6.";
  }
  return errors;
}

export interface ConversionValues {
  fromCode: string;
  toCode: string;
  factor: string;
  offset: string;
}

export function validateConversion({ fromCode, toCode, factor, offset }: ConversionValues): Errors<keyof ConversionValues> {
  const errors: Errors<keyof ConversionValues> = {};
  if (fromCode === "") {
    errors.fromCode = "Select a unit.";
  }
  if (toCode === "") {
    errors.toCode = "Select a unit.";
  } else if (toCode === fromCode) {
    errors.toCode = "Choose a different unit from the from unit.";
  }
  if (!factorPattern.test(factor) || !/[1-9]/.test(factor)) {
    errors.factor = "Enter a positive decimal with up to 16 integer and 12 fraction digits.";
  }
  if (offset !== "" && !offsetPattern.test(offset)) {
    errors.offset = "Enter a signed decimal with up to 16 integer and 12 fraction digits.";
  }
  return errors;
}

export function validateQuantity(quantity: string): string | undefined {
  return quantityPattern.test(quantity) ? undefined : "Enter a decimal number such as 12.5.";
}

const formatProblem = ({ message, code }: Problem) => `${message} (${code})`;

function AddUnitForm({ onCreated }: { onCreated(): void }) {
  const [values, setValues] = useState<UnitValues>({ code: "", name: "", decimalPrecision: "2" });
  const [unitClass, setUnitClass] = useState<UnitClass>("quantity");
  const [errors, setErrors] = useState<Errors<keyof UnitValues>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const found = validateUnit(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) {
      return;
    }
    setBusy(true);
    try {
      await createUnit({ code: values.code, name: values.name.trim(), class: unitClass, decimalPrecision: Number(values.decimalPrecision) });
      setValues({ code: "", name: "", decimalPrecision: "2" });
      onCreated();
    } catch (error) {
      const problem = describeProblem(error);
      if (problem.code === "ORG-UOM-CODE-DUPLICATE") {
        setErrors({ code: formatProblem(problem) });
      } else {
        setFailure(problem);
      }
    } finally {
      setBusy(false);
    }
  };

  const field = (key: keyof UnitValues) => ({
    value: values[key],
    onChange: (event: { target: { value: string } }) => setValues({ ...values, [key]: event.target.value }),
  });

  return (
    <Card title="Add custom unit">
      <form onSubmit={submit} noValidate className={styles.form}>
        <FormField id="unit-code" label="Code" error={errors.code}>
          {(control) => <Input {...control} {...field("code")} maxLength={10} />}
        </FormField>
        <FormField id="unit-name" label="Name" error={errors.name}>
          {(control) => <Input {...control} {...field("name")} />}
        </FormField>
        <FormField id="unit-class" label="Class">
          {(control) => (
            <Select {...control} value={unitClass} onChange={(event) => setUnitClass(event.target.value as UnitClass)}>
              {unitClasses.map((name) => (
                <option key={name} value={name}>
                  {name}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="unit-precision" label="Decimal precision" error={errors.decimalPrecision}>
          {(control) => <Input {...control} {...field("decimalPrecision")} inputMode="numeric" />}
        </FormField>
        {failure ? (
          <Alert tone="danger" title="Could not add the unit" live="alert">
            {formatProblem(failure)}
          </Alert>
        ) : null}
        <div>
          <Button type="submit" variant="primary" loading={busy}>
            Add unit
          </Button>
        </div>
      </form>
    </Card>
  );
}

const classOptions = [{ value: "", label: "All" }, ...unitClasses.map((name) => ({ value: name, label: name }))];

const unitColumns: DataTableColumn<Unit>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
  { key: "class", header: "Class" },
  { key: "decimalPrecision", header: "Decimal precision", align: "end" },
  { key: "system", header: "Origin", render: (row) => <Badge tone={row.system ? "info" : "neutral"}>{row.system ? "System" : "Custom"}</Badge> },
];

export function UnitsTab() {
  const { canWriteReference } = useLegalEntity();
  const [unitClass, setUnitClass] = useState("");
  const [status, setStatus] = useState<StatusFilterValue>("");
  const state = useRows((signal) => listUnits({ class: unitClass, status: status || undefined }, { signal }), `${unitClass}|${status}`);
  return (
    <>
      {canWriteReference ? <AddUnitForm onCreated={state.reload} /> : null}
      <div className={styles.filters}>
        <FilterSelect id="units-class" label="Filter by class" value={unitClass} options={classOptions} onChange={setUnitClass} />
        <FilterSelect id="units-status" label="Filter by status" value={status} options={statusOptions} onChange={(value) => setStatus(value as StatusFilterValue)} />
      </div>
      <StatusTable
        label="Units"
        emptyTitle="No units"
        columns={unitColumns}
        state={state}
        getRowKey={(row) => row.code}
        action={{ subject: (row) => row.code, apply: (row, next) => setUnitStatus(row.code, next) }}
      />
    </>
  );
}

function UnitSelect({ id, label, units, value, error, onChange }: { id: string; label: string; units: Unit[]; value: string; error?: string; onChange(value: string): void }) {
  return (
    <FormField id={id} label={label} error={error}>
      {(control) => (
        <Select {...control} value={value} onChange={(event) => onChange(event.target.value)}>
          <option value="">Select a unit</option>
          {units.map((unit) => (
            <option key={unit.code} value={unit.code}>
              {unit.code} - {unit.name}
            </option>
          ))}
        </Select>
      )}
    </FormField>
  );
}

function AddConversionForm({ units, onCreated }: { units: Unit[]; onCreated(): void }) {
  const [values, setValues] = useState<ConversionValues>({ fromCode: "", toCode: "", factor: "", offset: "" });
  const [errors, setErrors] = useState<Errors<keyof ConversionValues>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const found = validateConversion(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) {
      return;
    }
    setBusy(true);
    try {
      await createConversion({ fromCode: values.fromCode, toCode: values.toCode, factor: values.factor, offset: values.offset === "" ? undefined : values.offset });
      setValues({ fromCode: "", toCode: "", factor: "", offset: "" });
      onCreated();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card title="Add conversion">
      <form onSubmit={submit} noValidate className={styles.form}>
        <UnitSelect id="conversion-from" label="From unit" units={units} value={values.fromCode} error={errors.fromCode} onChange={(fromCode) => setValues({ ...values, fromCode })} />
        <UnitSelect id="conversion-to" label="To unit" units={units} value={values.toCode} error={errors.toCode} onChange={(toCode) => setValues({ ...values, toCode })} />
        <FormField id="conversion-factor" label="Factor" error={errors.factor}>
          {(control) => <Input {...control} value={values.factor} onChange={(event) => setValues({ ...values, factor: event.target.value })} inputMode="decimal" />}
        </FormField>
        <FormField id="conversion-offset" label="Offset" optional error={errors.offset}>
          {(control) => <Input {...control} value={values.offset} onChange={(event) => setValues({ ...values, offset: event.target.value })} inputMode="decimal" />}
        </FormField>
        {failure ? (
          <Alert tone="danger" title="Could not add the conversion" live="alert">
            {conversionMessages[failure.code] ?? failure.message} ({failure.code})
          </Alert>
        ) : null}
        <div>
          <Button type="submit" variant="primary" loading={busy}>
            Add conversion
          </Button>
        </div>
      </form>
    </Card>
  );
}

function TestConversionPanel({ units }: { units: Unit[] }) {
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [quantity, setQuantity] = useState("");
  const [errors, setErrors] = useState<Errors<"from" | "to" | "quantity">>({});
  const [result, setResult] = useState<Conversion | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const found: Errors<"from" | "to" | "quantity"> = {};
    if (from === "") {
      found.from = "Select a unit.";
    }
    if (to === "") {
      found.to = "Select a unit.";
    }
    const quantityError = validateQuantity(quantity);
    if (quantityError) {
      found.quantity = quantityError;
    }
    setErrors(found);
    setResult(null);
    setFailure(null);
    if (Object.keys(found).length > 0) {
      return;
    }
    setBusy(true);
    try {
      setResult(await convertUnit(from, to, quantity));
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(
        problem.code === "ORG-UOM-NO-PATH" ? { code: problem.code, message: `No conversion from ${from} to ${to} within two steps.` } : problem,
      );
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card title="Test conversion">
      <form onSubmit={submit} noValidate className={styles.form}>
        <UnitSelect id="test-from" label="Test from unit" units={units} value={from} error={errors.from} onChange={setFrom} />
        <UnitSelect id="test-to" label="Test to unit" units={units} value={to} error={errors.to} onChange={setTo} />
        <FormField id="test-quantity" label="Quantity" error={errors.quantity}>
          {(control) => <Input {...control} value={quantity} onChange={(event) => setQuantity(event.target.value)} inputMode="decimal" />}
        </FormField>
        <div>
          <Button type="submit" variant="primary" loading={busy}>
            Convert
          </Button>
        </div>
      </form>
      <div role="status">
        {result ? (
          <dl className={styles.result}>
            <dt>Quantity</dt>
            <dd>{result.quantity}</dd>
            <dt>Display quantity</dt>
            <dd>{result.displayQuantity}</dd>
          </dl>
        ) : null}
      </div>
      {failure ? (
        <Alert tone="danger" title="Could not convert" live="alert">
          {formatProblem(failure)}
        </Alert>
      ) : null}
    </Card>
  );
}

const yesNo = (value: boolean) => (value ? "Yes" : "No");

const conversionColumns: DataTableColumn<UomConversion>[] = [
  { key: "fromCode", header: "From", mono: true },
  { key: "toCode", header: "To", mono: true },
  { key: "class", header: "Class" },
  { key: "factor", header: "Factor", align: "end", mono: true },
  { key: "offset", header: "Offset", align: "end", mono: true },
  { key: "inverseFactor", header: "Inverse factor", align: "end", mono: true },
  { key: "roundTrip", header: "Round trip", render: (row) => yesNo(row.roundTrip) },
  { key: "system", header: "Origin", render: (row) => <Badge tone={row.system ? "info" : "neutral"}>{row.system ? "System" : "Custom"}</Badge> },
];

function unitOptions(state: RowsState<Unit>) {
  return [{ value: "", label: "All" }, ...state.rows.map((unit) => ({ value: unit.code, label: unit.code }))];
}

export function ConversionsTab() {
  const { canWriteReference } = useLegalEntity();
  const [unitClass, setUnitClass] = useState("");
  const [unit, setUnit] = useState("");
  const [status, setStatus] = useState<StatusFilterValue>("");
  const units = useRows((signal) => listUnits({ status: "active" }, { signal }), "active");
  const state = useRows(
    (signal) => listConversions({ class: unitClass, unit, status: status || undefined }, { signal }),
    `${unitClass}|${unit}|${status}`,
  );
  return (
    <>
      {units.error ? (
        <Alert tone="danger" title="Could not load units" live="alert">
          {formatProblem(units.error)}
        </Alert>
      ) : null}
      {canWriteReference ? <AddConversionForm units={units.rows} onCreated={state.reload} /> : null}
      <TestConversionPanel units={units.rows} />
      <div className={styles.filters}>
        <FilterSelect id="conversions-class" label="Filter by class" value={unitClass} options={classOptions} onChange={setUnitClass} />
        <FilterSelect id="conversions-unit" label="Filter by unit" value={unit} options={unitOptions(units)} onChange={setUnit} />
        <FilterSelect id="conversions-status" label="Filter by status" value={status} options={statusOptions} onChange={(value) => setStatus(value as StatusFilterValue)} />
      </div>
      <StatusTable label="Conversions" emptyTitle="No conversions" columns={conversionColumns} state={state} getRowKey={(row) => row.id} />
    </>
  );
}
