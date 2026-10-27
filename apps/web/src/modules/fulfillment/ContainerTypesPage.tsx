import { useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import { describeProblem, listContainerTypes, saveContainerType, type ContainerType, type Problem } from "./api/client";
import { CONTAINER_CODE, dash, DECIMAL, MATERIAL_CODE } from "./format";
import { ProblemAlert, useCommandKey } from "./forms";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<ContainerType>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "tareKg", header: "Tare kg", align: "end" },
  { key: "returnable", header: "Returnable", render: (row) => (row.returnable ? <Badge tone="info">Returnable</Badge> : "No") },
  { key: "packagingMaterial", header: "Packaging material", mono: true, render: (row) => row.packagingMaterial ?? dash },
];

interface Values {
  code: string;
  tareKg: string;
  packagingMaterial: string;
  returnable: boolean;
}

const blank: Values = { code: "", tareKg: "", packagingMaterial: "", returnable: false };

/** Container types (FR-28.14, FR-28.15): tare, returnable transport packaging and packaging material. */
export function ContainerTypesPage() {
  const state = useLoad((signal) => listContainerTypes(signal), "container-types");
  const [values, setValues] = useState<Values>(blank);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useCommandKey();

  function set(patch: Partial<Values>) {
    setValues({ ...values, ...patch });
    key.renew();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (!CONTAINER_CODE.test(values.code.trim())) found.code = "Enter a code of up to 64 letters, digits, '.', '_' or '-', such as C12 or PALLET.";
    if (!DECIMAL.test(values.tareKg.trim())) found.tareKg = "Enter the empty weight in kg, 0 or more, with at most 6 decimals.";
    if (values.packagingMaterial.trim() !== "" && !MATERIAL_CODE.test(values.packagingMaterial.trim())) found.packagingMaterial = "Enter a material code of up to 32 letters or digits, such as CARDBOARD.";
    setErrors(found);
    setFailure(null);
    setNotice(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const saved = await saveContainerType(
        { code: values.code.trim(), tareKg: values.tareKg.trim(), returnable: values.returnable, packagingMaterial: values.packagingMaterial.trim() || null },
        key.current(),
      );
      key.renew();
      setValues(blank);
      setNotice(`Saved container type ${saved.code}.`);
      state.reload();
    } catch (error) {
      setFailure(describeProblem(error));
      key.renew();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Container types"
        description="Cartons, crates and pallets the packers use. Packed containers take the tare, returnable flag and material of their type."
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load container types" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Container types"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.code}
          loading={state.loading}
          onRowOpen={(row) => {
            setValues({ code: row.code, tareKg: row.tareKg, packagingMaterial: row.packagingMaterial ?? "", returnable: row.returnable });
            key.renew();
          }}
          empty={{ title: "No container types yet", description: "Add the cartons and pallets you pack into, then add packing rules that use them." }}
        />
      )}
      <Card title="Add or change a container type">
        <form className={styles.form} onSubmit={submit} noValidate>
          <p className={styles.note}>Saving an existing code replaces it. Open a row above to edit it.</p>
          <FormField id="ct-code" label="Code" error={errors.code}>
            {(control) => <Input {...control} maxLength={64} value={values.code} invalid={Boolean(errors.code)} onChange={(event) => set({ code: event.target.value })} />}
          </FormField>
          <FormField id="ct-tare" label="Tare weight (kg)" error={errors.tareKg}>
            {(control) => <Input {...control} inputMode="decimal" value={values.tareKg} invalid={Boolean(errors.tareKg)} onChange={(event) => set({ tareKg: event.target.value })} />}
          </FormField>
          <FormField id="ct-material" label="Packaging material" hint="Its tare counts toward the packaging fee unless the type is returnable." error={errors.packagingMaterial} optional>
            {(control) => <Input {...control} maxLength={32} value={values.packagingMaterial} invalid={Boolean(errors.packagingMaterial)} onChange={(event) => set({ packagingMaterial: event.target.value.toUpperCase() })} />}
          </FormField>
          <FormField id="ct-returnable" label="Returnable transport packaging" hint="Shipped containers of this type count toward the customer's returnable balance.">
            {(control) => <Input {...control} type="checkbox" checked={values.returnable} onChange={(event) => set({ returnable: event.target.checked })} />}
          </FormField>
          <ProblemAlert title="Could not save the container type" problem={failure} />
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save container type
            </Button>
            {notice ? (
              <p role="status" className={styles.note}>
                {notice}
              </p>
            ) : null}
          </div>
        </form>
      </Card>
    </div>
  );
}
