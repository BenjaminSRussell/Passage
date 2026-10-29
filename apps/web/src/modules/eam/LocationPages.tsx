import { useRef, useState, type CSSProperties, type FormEvent } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, EmptyState, FormField, Input, PageHeader, Select, Skeleton, type DataTableColumn } from "../../shell/ui";
import {
  createLocation,
  describeProblem,
  getLocation,
  listLocationHistory,
  listLocations,
  newKey,
  placeAsset,
  type Asset,
  type AssetDetail,
  type FunctionalLocation,
  type LocationHistory,
  type NewLocation,
  type Problem,
} from "./api/client";
import { CODE, dash, dayOf, minuteOf, sentence, UUID } from "./format";
import { Entry, Failure, PagedTable, State } from "./parts";
import styles from "./pages.module.css";
import { useLoad, usePaged } from "./useLoad";

/** One functional-location path segment: ^[A-Z0-9][A-Z0-9_.-]{0,39}$ ("/" joins segments). */
export const LOCATION_CODE = /^[A-Z0-9][A-Z0-9_.-]{0,39}$/;

/** The deepest functional location (FR-44.1, EAM-FL-001). */
export const MAX_DEPTH = 12;

function Path({ location }: { location: FunctionalLocation }) {
  return (
    <span className={styles.indent} style={{ "--depth": location.depth } as CSSProperties}>
      {location.path}
    </span>
  );
}

const locationColumns: DataTableColumn<FunctionalLocation>[] = [
  { key: "path", header: "Location", mono: true, render: (row) => <Path location={row} /> },
  { key: "name", header: "Name" },
  { key: "locationType", header: "Type", mono: true, render: (row) => row.locationType ?? dash },
  { key: "depth", header: "Level", align: "end" },
];

const assetColumns: DataTableColumn<Asset>[] = [
  { key: "assetNumber", header: "Asset", mono: true },
  { key: "name", header: "Name" },
  { key: "assetType", header: "Type", mono: true },
  { key: "criticality", header: "Criticality", align: "end" },
  { key: "lifecycleState", header: "State", render: (row) => <State value={row.lifecycleState} /> },
];

/** The plant structure (8.44.6, FR-44.1): a site's functional locations in path order, optionally one subtree. */
export function LocationsPage() {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const site = params.get("site") ?? "";
  const under = params.get("under") ?? "";
  const [siteText, setSiteText] = useState(site);
  const [siteError, setSiteError] = useState<string | undefined>();
  const state = usePaged((cursor, signal) => listLocations({ site, under, cursor }, signal), `locations|${site}|${under}`);

  function filter(event: FormEvent) {
    event.preventDefault();
    const next = siteText.trim();
    if (next && !UUID.test(next)) {
      setSiteError("Enter the site id (a UUID), or leave it empty for every site.");
      return;
    }
    setSiteError(undefined);
    setParams(next ? { site: next } : {});
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Plant structure"
        description="Functional locations per site, up to 12 levels deep. Install assets at a location from the asset's page."
        actions={
          <Link className={buttonClassName("primary")} to={site ? `/eam/locations/new?site=${site}` : "/eam/locations/new"}>
            New location
          </Link>
        }
      />
      <form className={styles.toolbar} onSubmit={filter} noValidate aria-label="Filter locations">
        <FormField id="loc-site" label="Site" optional error={siteError}>
          {(control) => <Input {...control} value={siteText} invalid={Boolean(siteError)} onChange={(e) => setSiteText(e.target.value)} />}
        </FormField>
        <Button type="submit">Show locations</Button>
      </form>
      {under ? (
        <p className={styles.note}>
          Showing one location and everything under it. <Link to={site ? `/eam/locations?site=${site}` : "/eam/locations"}>Show every location</Link>
        </p>
      ) : null}
      <PagedTable
        label="Functional locations"
        noun="locations"
        columns={locationColumns}
        state={state}
        empty={
          site || under
            ? { title: "No locations here", description: "Create the site's root location, such as the plant." }
            : { title: "No functional locations yet", description: "Create a root location for a site, then add areas, lines and positions under it." }
        }
        onRowOpen={(row) => navigate(`/eam/locations/${row.id}`)}
      />
    </div>
  );
}

interface LocationValues {
  code: string;
  name: string;
  siteId: string;
  locationType: string;
}

type LocationErrors = Partial<Record<keyof LocationValues, string>>;

/** The checks the service makes on a new location, and the request the form sends. */
export function validateLocation(values: LocationValues, parentId?: string): { errors: LocationErrors; input?: NewLocation } {
  const errors: LocationErrors = {};
  const code = values.code.trim().toUpperCase();
  if (!LOCATION_CODE.test(code)) errors.code = "Use up to 40 capital letters, digits, dots, dashes or underscores, starting with a letter or digit. No slashes.";
  const name = values.name.trim();
  if (name.length < 1 || name.length > 120) errors.name = "Enter a name of up to 120 characters.";
  const siteId = values.siteId.trim();
  if (!UUID.test(siteId)) errors.siteId = "Enter the site id (a UUID).";
  const locationType = values.locationType.trim().toUpperCase();
  if (locationType && !CODE.test(locationType)) errors.locationType = "Use 2 to 20 capital letters, digits or underscores, such as LINE, or leave it empty.";
  if (Object.keys(errors).length > 0) return { errors };
  const input: NewLocation = { code, name, siteId };
  if (parentId) input.parentId = parentId;
  if (locationType) input.locationType = locationType;
  return { errors, input };
}

/** Create a root location of a site, or a child of ?parent= in the parent's site. */
export function NewLocationPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const parentId = params.get("parent") ?? "";
  const parent = useLoad((signal) => (parentId ? getLocation(parentId, signal) : Promise.resolve(null)), `parent|${parentId}`);
  const [values, setValues] = useState<LocationValues>({ code: "", name: "", siteId: params.get("site") ?? "", locationType: "" });
  const [errors, setErrors] = useState<LocationErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<LocationValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const at = parent.data;
    const { errors: found, input } = validateLocation(at ? { ...values, siteId: at.siteId } : values, at?.id);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const created = await createLocation(input, key.current);
      navigate(`/eam/locations/${created.id}`);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  if (parentId && parent.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="New location" />
        <Failure title="Could not load the parent location" problem={parent.error} retry={parent.reload} />
      </div>
    );
  }
  if (parentId && parent.loading) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="New location" />
        <Skeleton width="1/2" />
      </div>
    );
  }
  const at = parent.data;
  if (at && at.depth >= MAX_DEPTH) {
    return (
      <div className={styles.stack}>
        <PageHeader title="New location" />
        <Alert tone="warning" title="This location is at the deepest level">
          {at.path} is level {MAX_DEPTH}; functional locations cannot go deeper (EAM-FL-001). Add the location higher up instead.
        </Alert>
      </div>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New location"
        description={at ? `Under ${at.path}, at level ${at.depth + 1}.` : "A root location of a site, such as the plant. Add areas, lines and positions under it."}
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="loc-code" label="Code" hint={at ? `The path becomes ${at.path}/CODE.` : "The root code is the start of every path below it."} error={errors.code}>
            {(control) => <Input {...control} value={values.code} maxLength={40} invalid={Boolean(errors.code)} onChange={(e) => update({ code: e.target.value })} />}
          </FormField>
          <FormField id="loc-name" label="Name" error={errors.name}>
            {(control) => <Input {...control} value={values.name} maxLength={120} invalid={Boolean(errors.name)} onChange={(e) => update({ name: e.target.value })} />}
          </FormField>
          {at ? (
            <dl className={styles.details}>
              <Entry term="Site" mono>
                {at.siteId}
              </Entry>
            </dl>
          ) : (
            <FormField id="loc-site-id" label="Site" hint="The site id from organization setup." error={errors.siteId}>
              {(control) => <Input {...control} value={values.siteId} invalid={Boolean(errors.siteId)} onChange={(e) => update({ siteId: e.target.value })} />}
            </FormField>
          )}
          <FormField id="loc-type" label="Location type" hint="A code such as AREA, LINE or POSITION." optional error={errors.locationType}>
            {(control) => <Input {...control} value={values.locationType} maxLength={20} invalid={Boolean(errors.locationType)} onChange={(e) => update({ locationType: e.target.value })} />}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Failure title={failure.code === "EAM-DUP-001" ? "This path already exists at the site" : "Could not create the location"} problem={failure} />
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create location
            </Button>
            <Link className={buttonClassName("ghost")} to={at ? `/eam/locations/${at.id}` : "/eam/locations"}>
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}

/** One functional location: where it sits, what is under it, and the assets installed at it. */
export function LocationDetailPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const state = useLoad((signal) => getLocation(id, signal), id);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Functional location" />
        {state.error.code === "EAM-NF-001" ? (
          <Card>
            <EmptyState
              headingLevel={3}
              title="We couldn't find this location"
              description="It may belong to another tenant, or the link is wrong."
              action={
                <Link className={buttonClassName("secondary")} to="/eam/locations">
                  Back to the plant structure
                </Link>
              }
            />
          </Card>
        ) : (
          <Failure title="Could not load the location" problem={state.error} retry={state.reload} />
        )}
      </div>
    );
  }
  if (state.loading && !state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Functional location" />
        <Skeleton width="3/4" />
        <Skeleton width="1/2" />
      </div>
    );
  }
  const location = state.data!;
  const full = location.depth >= MAX_DEPTH;

  return (
    <div className={styles.stack}>
      <PageHeader
        title={location.name}
        description={location.path}
        actions={
          <div className={styles.actions}>
            {full ? null : (
              <Link className={buttonClassName("primary")} to={`/eam/locations/new?parent=${location.id}`}>
                Add location under it
              </Link>
            )}
            <Link className={buttonClassName("secondary")} to={`/eam/locations?site=${location.siteId}&under=${location.id}`}>
              Show its subtree
            </Link>
          </div>
        }
      />
      <Card title="Details">
        <dl className={styles.details}>
          <Entry term="Code" mono>
            {location.code}
          </Entry>
          <Entry term="Path" mono>
            {location.path}
          </Entry>
          <Entry term="Level">
            {location.depth} of {MAX_DEPTH}
          </Entry>
          <Entry term="Type" mono>
            {location.locationType ?? dash}
          </Entry>
          <Entry term="Site" mono>
            {location.siteId}
          </Entry>
          <Entry term="Created" mono>
            {dayOf(location.createdAt)}
          </Entry>
        </dl>
        {full ? <p className={styles.note}>This location is at the deepest level; nothing can be added under it.</p> : null}
      </Card>
      <Card title="Locations under it">
        <DataTable
          label="Child locations"
          columns={locationColumns}
          rows={location.children}
          getRowKey={(row) => row.id}
          empty={{ title: "Nothing under this location yet", description: full ? undefined : "Add an area, line or position under it." }}
          onRowOpen={(row) => navigate(`/eam/locations/${row.id}`)}
        />
      </Card>
      <Card title="Installed assets">
        <DataTable
          label="Installed assets"
          columns={assetColumns}
          rows={location.assets}
          getRowKey={(row) => row.id}
          empty={{ title: "No asset is installed here", description: "Install an asset from its page, in the Location card." }}
          onRowOpen={(row) => navigate(`/eam/assets/${row.id}`)}
        />
      </Card>
    </div>
  );
}

const actionLabel: Record<LocationHistory["action"], string> = { INSTALL: "Installed", MOVE: "Moved", REMOVE: "Removed" };

const historyColumns: DataTableColumn<LocationHistory>[] = [
  { key: "at", header: "When", mono: true, render: (row) => minuteOf(row.at) },
  { key: "action", header: "Change", render: (row) => actionLabel[row.action] ?? sentence(row.action) },
  { key: "fromPath", header: "From", mono: true, render: (row) => row.fromPath ?? dash },
  { key: "toPath", header: "To", mono: true, render: (row) => row.toPath ?? dash },
  { key: "actor", header: "By", mono: true },
];

/** The asset's functional location (FR-44.1, FR-44.2): install, move or remove it, and its location history. */
export function AssetLocationCard({ asset, onSaved }: { asset: AssetDetail; onSaved(text: string): void }) {
  const disposed = asset.lifecycleState === "DISPOSED";
  const options = useLoad((signal) => listLocations({ site: asset.siteId, limit: 200 }, signal), `site-locations|${asset.siteId}`);
  const history = useLoad((signal) => listLocationHistory(asset.id, signal), `history|${asset.id}|${asset.version}`);
  const [target, setTarget] = useState("");
  const [error, setError] = useState<string | undefined>();
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState<"place" | "remove" | null>(null);
  const key = useRef(newKey());
  const current = asset.location;

  async function place(to: string | null) {
    setFailure(null);
    if (to !== null && !to) {
      setError("Choose a location of the asset's site.");
      return;
    }
    setError(undefined);
    setBusy(to === null ? "remove" : "place");
    try {
      await placeAsset(asset.id, to, asset.version, key.current);
      key.current = newKey();
      setTarget("");
      const path = options.data?.items.find((l) => l.id === to)?.path;
      onSaved(to === null ? `Removed from ${current?.path ?? "its location"}.` : `${current ? "Moved" : "Installed"} at ${path ?? "the location"}.`);
    } catch (e) {
      setFailure(describeProblem(e));
    } finally {
      setBusy(null);
    }
  }

  const choices = (options.data?.items ?? []).filter((l) => l.id !== current?.id);

  return (
    <Card title="Location">
      <div className={styles.stack}>
        <dl className={styles.details}>
          <Entry term="Installed at" mono>
            {current ? <Link to={`/eam/locations/${current.id}`}>{current.path}</Link> : "Not installed"}
          </Entry>
        </dl>
        {disposed ? (
          <p className={styles.note}>A disposed asset cannot be placed at a location.</p>
        ) : options.error ? (
          <Failure title="Could not load the site's locations" problem={options.error} retry={options.reload} />
        ) : options.loading && !options.data ? (
          <Skeleton height="control" />
        ) : choices.length === 0 && !current ? (
          <p className={styles.note}>
            The asset's site has no functional locations yet. <Link to={`/eam/locations/new?site=${asset.siteId}`}>Create one</Link>
          </p>
        ) : (
          <form
            className={styles.toolbar}
            onSubmit={(event) => {
              event.preventDefault();
              void place(target);
            }}
            noValidate
            aria-label="Place the asset"
          >
            <FormField id="asset-location" label={current ? "Move to" : "Install at"} error={error}>
              {(control) => (
                <Select
                  {...control}
                  value={target}
                  onChange={(e) => {
                    setTarget(e.target.value);
                    key.current = newKey();
                  }}
                >
                  <option value="">Choose a location</option>
                  {choices.map((l) => (
                    <option key={l.id} value={l.id}>
                      {l.path} ({l.name})
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <Button type="submit" variant="primary" loading={busy === "place"}>
              {current ? "Move asset" : "Install asset"}
            </Button>
            {current ? (
              <Button variant="secondary" loading={busy === "remove"} onClick={() => void place(null)}>
                Remove from location
              </Button>
            ) : null}
          </form>
        )}
        {failure ? (
          <Failure title={failure.code === "conflict_version" ? "Someone else changed this asset. Reload it and try again." : "Could not place the asset"} problem={failure} />
        ) : null}
        {history.error ? (
          <Failure title="Could not load the location history" problem={history.error} retry={history.reload} />
        ) : (
          <DataTable
            label="Location history"
            columns={historyColumns}
            rows={history.data ?? []}
            getRowKey={(row) => row.id}
            loading={history.loading}
            empty={{ title: "No location changes yet" }}
          />
        )}
      </div>
    </Card>
  );
}
