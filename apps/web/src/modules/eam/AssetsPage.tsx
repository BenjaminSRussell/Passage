import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { createAsset, describeProblem, listAssets, newKey, type Asset, type NewAsset, type Problem } from "./api/client";
import { CODE, dayOf, UUID } from "./format";
import { PagedTable, State, StatusFilter } from "./parts";
import styles from "./pages.module.css";
import { usePaged } from "./useLoad";

const columns: DataTableColumn<Asset>[] = [
  { key: "assetNumber", header: "Asset", mono: true },
  { key: "name", header: "Name" },
  { key: "assetType", header: "Type", mono: true },
  { key: "criticality", header: "Criticality", align: "end" },
  { key: "lifecycleState", header: "State", render: (row) => <State value={row.lifecycleState} /> },
  { key: "createdAt", header: "Created", mono: true, render: (row) => dayOf(row.createdAt) },
];

/** Assets in the tenant (FR-44.2), newest first, filterable by lifecycle state. */
export function AssetsPage() {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const status = params.get("status") ?? "";
  const state = usePaged((cursor, signal) => listAssets({ status, cursor }, signal), `assets|${status}`);
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Assets"
        description="Equipment you maintain. Only active assets receive work orders."
        actions={
          <Link className={buttonClassName("primary")} to="/eam/assets/new">
            New asset
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <StatusFilter
          id="asset-status"
          value={status}
          options={["CREATED", "ACTIVE", "INACTIVE", "DISPOSED"]}
          onChange={(next) => setParams(next ? { status: next } : {})}
        />
      </div>
      <PagedTable
        label="Assets"
        noun="assets"
        columns={columns}
        state={state}
        empty={status ? { title: "No assets in this state", description: "Choose another state." } : { title: "No assets yet", description: "Create an asset, then activate it to plan maintenance." }}
        onRowOpen={(row) => navigate(`/eam/assets/${row.id}`)}
      />
    </div>
  );
}

interface AssetValues {
  name: string;
  assetType: string;
  siteId: string;
  criticality: string;
  productionResourceId: string;
}

type AssetErrors = Partial<Record<keyof AssetValues, string>>;

/** The checks the service makes on a new asset, and the request the form sends. */
export function validateAsset(values: AssetValues): { errors: AssetErrors; input?: NewAsset } {
  const errors: AssetErrors = {};
  const name = values.name.trim();
  if (name.length < 1 || name.length > 120) errors.name = "Enter a name of up to 120 characters.";
  const assetType = values.assetType.trim().toUpperCase();
  if (!CODE.test(assetType)) errors.assetType = "Use 2 to 20 characters: A to Z, 0 to 9 or an underscore.";
  if (!UUID.test(values.siteId.trim())) errors.siteId = "Enter the site id (a UUID).";
  const criticality = Number(values.criticality);
  if (!Number.isInteger(criticality) || criticality < 1 || criticality > 5) errors.criticality = "Choose 1 to 5.";
  const resource = values.productionResourceId.trim();
  if (resource && !UUID.test(resource)) errors.productionResourceId = "Enter the production resource id (a UUID), or leave it empty.";
  if (Object.keys(errors).length > 0) return { errors };
  const input: NewAsset = { name, assetType, siteId: values.siteId.trim(), criticality };
  if (resource) input.productionResourceId = resource;
  return { errors, input };
}

/** Register an asset. It starts in CREATED; activate it from its page. */
export function NewAssetPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<AssetValues>({ name: "", assetType: "", siteId: "", criticality: "3", productionResourceId: "" });
  const [errors, setErrors] = useState<AssetErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<AssetValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateAsset(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const asset = await createAsset(input, key.current);
      navigate(`/eam/assets/${asset.id}`);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  const text = (id: keyof AssetValues, label: string, hint?: string, optional?: boolean, maxLength?: number) => (
    <FormField id={`asset-${id}`} label={label} hint={hint} optional={optional} error={errors[id]}>
      {(control) => (
        <Input {...control} value={values[id]} maxLength={maxLength} invalid={Boolean(errors[id])} onChange={(event) => update({ [id]: event.target.value })} />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader title="New asset" description="An asset is one piece of equipment at a site. It gets an AST number when you create it." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {text("name", "Name", undefined, false, 120)}
          {text("assetType", "Asset type", "A code such as PUMP or CONVEYOR.", false, 20)}
          {text("siteId", "Site", "The site id from organization setup.")}
          <FormField id="asset-criticality" label="Criticality" hint="5 is the most critical. Corrective orders default to priority 6 minus criticality." error={errors.criticality}>
            {(control) => (
              <Select {...control} value={values.criticality} onChange={(event) => update({ criticality: event.target.value })}>
                {[1, 2, 3, 4, 5].map((n) => (
                  <option key={n} value={String(n)}>
                    {n}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {text("productionResourceId", "Production resource", "Links downtime to production scheduling.", true)}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not create the asset" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create asset
            </Button>
            <Link className={buttonClassName("ghost")} to="/eam/assets">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
