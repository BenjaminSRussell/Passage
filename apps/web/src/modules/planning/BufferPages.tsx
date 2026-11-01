import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  buttonClassName,
  Card,
  DataTable,
  FormField,
  Input,
  PageHeader,
  Select,
  Skeleton,
  type BadgeTone,
  type DataTableColumn,
} from "../../shell/ui";
import {
  createBufferProfile,
  getBufferProfile,
  listBufferProfiles,
  listDdmrpBuffers,
  recalculateDdmrpBuffer,
  updateBufferProfile,
  type BufferItemType,
  type BufferProfile,
  type BufferProfileInput,
  type BufferRecalculation,
  type BufferZone,
  type DdmrpBuffer,
} from "./api/client";
import { describeError, useLoad, type LoadError } from "./hooks";
import { compareQty } from "./ItemCoveragePage";
import { trimQty } from "./NetRequirementsPage";
import styles from "./pages.module.css";

const PROFILES = "/planning/buffer-profiles";
export const BUFFERS = "/planning/ddmrp-buffers";
const UUID = /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/;
const PROFILE_CODE = /^[A-Z0-9][A-Z0-9_-]{1,19}$/;
/** numeric(5,3): 0 to 99.999. */
const FACTOR = /^\d{1,2}(\.\d{1,3})?$/;
/** numeric(18,6), never negative. */
const QTY = /^\d{1,12}(\.\d{1,6})?$/;
/** A signed decimal, as on-hand may be. */
const SIGNED_QTY = /^-?\d{1,12}(\.\d{1,6})?$/;
const PERCENT = /^\d{1,3}(\.\d{1,2})?$/;
const dash = "—";

export const itemTypeLabel: Record<BufferItemType, string> = {
  manufactured: "Manufactured",
  purchased: "Purchased",
  distributed: "Distributed",
};

export const zoneTone: Record<BufferZone, BadgeTone> = { red: "danger", yellow: "warning", green: "success" };
export const zoneLabel: Record<BufferZone, string> = { red: "Red", yellow: "Yellow", green: "Green" };

/**
 * Moves the decimal point of a decimal string by `places` (2 turns 0.5306 into
 * 53.06, -2 turns 60 into 0.6) without going through a float.
 */
export function shiftDecimal(value: string, places: number): string {
  const negative = value.startsWith("-");
  const [int, frac = ""] = (negative ? value.slice(1) : value).split(".");
  let digits = int + frac;
  let point = int.length + places;
  if (point < 0) {
    digits = "0".repeat(-point) + digits;
    point = 0;
  }
  if (point > digits.length) digits = digits.padEnd(point, "0");
  const whole = digits.slice(0, point).replace(/^0+(?=\d)/, "") || "0";
  const rest = digits.slice(point).replace(/0+$/, "");
  const out = rest ? `${whole}.${rest}` : whole;
  return negative && out !== "0" ? `-${out}` : out;
}

/** Planning priority (NFP / TOG) as a percent, the way the PRD states it (53.06%). */
export const priorityPercent = (priority: string) => `${shiftDecimal(priority, 2)}%`;

/** The profile form as typed: numbers stay strings until validated. */
export interface BufferProfileDraft {
  code: string;
  itemType: BufferItemType;
  leadTimeFactor: string;
  variabilityFactor: string;
  orderCycleDays: string;
  minOrderQty: string;
  spikeThresholdPct: string;
  spikeHorizonDays: string;
  aduWindowPastDays: string;
  aduWindowFutureDays: string;
}

type ProfileErrors = Partial<Record<keyof BufferProfileDraft, string>>;

/** The 4.22.13 defaults: spike threshold 50%, ADU window 90 past and 0 future days, horizon = DLT. */
export const EMPTY_PROFILE: BufferProfileDraft = {
  code: "",
  itemType: "manufactured",
  leadTimeFactor: "",
  variabilityFactor: "",
  orderCycleDays: "0",
  minOrderQty: "0",
  spikeThresholdPct: "50",
  spikeHorizonDays: "",
  aduWindowPastDays: "90",
  aduWindowFutureDays: "0",
};

export function profileDraftOf(p: BufferProfile): BufferProfileDraft {
  return {
    code: p.code,
    itemType: p.itemType,
    leadTimeFactor: trimQty(p.leadTimeFactor),
    variabilityFactor: trimQty(p.variabilityFactor),
    orderCycleDays: String(p.orderCycleDays),
    minOrderQty: trimQty(p.minOrderQty),
    spikeThresholdPct: trimQty(p.spikeThresholdPct),
    spikeHorizonDays: p.spikeHorizonDays === null ? "" : String(p.spikeHorizonDays),
    aduWindowPastDays: String(p.aduWindowPastDays),
    aduWindowFutureDays: String(p.aduWindowFutureDays),
  };
}

function wholeNumber(raw: string, min: number, max: number): number | null {
  const v = raw.trim();
  if (!/^\d+$/.test(v)) return null;
  const n = Number(v);
  return n < min || n > max ? null : n;
}

/** Checks a profile draft with the service's rules (FR-22.16, 4.22.13) and returns the request body or the field errors. */
export function validateBufferProfile(d: BufferProfileDraft): { errors: ProfileErrors; input: BufferProfileInput | null } {
  const errors: ProfileErrors = {};
  const code = d.code.trim().toUpperCase();
  if (!PROFILE_CODE.test(code)) errors.code = "Use 2 to 20 characters: letters, digits, - or _.";
  const ltf = d.leadTimeFactor.trim();
  const vf = d.variabilityFactor.trim();
  if (!FACTOR.test(ltf)) errors.leadTimeFactor = "Enter a factor from 0 to 99.999 with at most 3 decimals.";
  if (!FACTOR.test(vf)) errors.variabilityFactor = "Enter a factor from 0 to 99.999 with at most 3 decimals.";
  const moq = d.minOrderQty.trim();
  if (!QTY.test(moq)) errors.minOrderQty = "Enter a quantity of 0 or more with at most 6 decimals.";
  const spike = d.spikeThresholdPct.trim();
  if (!PERCENT.test(spike) || compareQty(spike, "1") < 0 || compareQty(spike, "500") > 0) {
    errors.spikeThresholdPct = "Enter a percent from 1 to 500 with at most 2 decimals.";
  }
  const cycle = wholeNumber(d.orderCycleDays, 0, 3650);
  if (cycle === null) errors.orderCycleDays = "Enter a whole number from 0 to 3650.";
  let horizon: number | null = null;
  if (d.spikeHorizonDays.trim() !== "") {
    horizon = wholeNumber(d.spikeHorizonDays, 0, 3650);
    if (horizon === null) errors.spikeHorizonDays = "Enter a whole number from 0 to 3650, or leave it blank for the DLT.";
  }
  const past = wholeNumber(d.aduWindowPastDays, 0, 365);
  const future = wholeNumber(d.aduWindowFutureDays, 0, 365);
  if (past === null) errors.aduWindowPastDays = "Enter a whole number from 0 to 365.";
  if (future === null) errors.aduWindowFutureDays = "Enter a whole number from 0 to 365.";
  if (past !== null && future !== null && past + future < 7) errors.aduWindowPastDays = "The past and future windows together need at least 7 days.";
  if (Object.keys(errors).length > 0) return { errors, input: null };
  return {
    errors,
    input: {
      code,
      itemType: d.itemType,
      leadTimeFactor: ltf,
      variabilityFactor: vf,
      orderCycleDays: cycle ?? 0,
      minOrderQty: moq,
      spikeThresholdPct: spike,
      spikeHorizonDays: horizon,
      aduWindowPastDays: past ?? 90,
      aduWindowFutureDays: future ?? 0,
    },
  };
}

function newKey(): string {
  const c = globalThis.crypto as Crypto | undefined;
  return c?.randomUUID ? c.randomUUID() : `bp-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

function LoadAlert({ what, error }: { what: string; error: LoadError | null }) {
  return (
    <Alert tone="danger" title={error ? `Could not load ${what}` : undefined} live="alert">
      {error ? `${error.message} (${error.code})` : null}
    </Alert>
  );
}

const profileColumns: DataTableColumn<BufferProfile>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "itemType", header: "Item type", render: (p) => itemTypeLabel[p.itemType] },
  { key: "ltf", header: "Lead-time factor", align: "end", render: (p) => trimQty(p.leadTimeFactor) },
  { key: "vf", header: "Variability factor", align: "end", render: (p) => trimQty(p.variabilityFactor) },
  { key: "cycle", header: "Order cycle (days)", align: "end", render: (p) => String(p.orderCycleDays) },
  { key: "moq", header: "Min order qty", align: "end", render: (p) => trimQty(p.minOrderQty) },
  { key: "spike", header: "Spike threshold", align: "end", render: (p) => `${trimQty(p.spikeThresholdPct)}%` },
  { key: "adu", header: "ADU window", render: (p) => `${p.aduWindowPastDays} past / ${p.aduWindowFutureDays} future days` },
];

/** Buffer profiles of the tenant (FR-22.16): the factors that size each DDMRP buffer. */
export function BufferProfilesPage() {
  const navigate = useNavigate();
  const state = useLoad((signal) => listBufferProfiles({ signal }), []);
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Buffer profiles"
        description="Lead-time and variability factors, order cycle, minimum order quantity, spike threshold and ADU window that size the DDMRP buffers (FR-22.16)."
        actions={
          <>
            <Link className={buttonClassName("secondary")} to={BUFFERS}>
              Buffer status
            </Link>
            <Link className={buttonClassName("primary")} to={`${PROFILES}/new`}>
              New buffer profile
            </Link>
          </>
        }
      />
      <LoadAlert what="buffer profiles" error={state.error} />
      {state.error ? null : (
        <DataTable
          label="Buffer profiles"
          columns={profileColumns}
          rows={state.data ?? []}
          getRowKey={(p) => p.id}
          loading={state.loading}
          onRowOpen={(p) => navigate(`${PROFILES}/${encodeURIComponent(p.id)}`)}
          empty={{
            title: "No buffer profiles yet",
            description: "Create one, then point decoupling-point item coverage at it and recalculate the buffer.",
          }}
        />
      )}
    </div>
  );
}

interface ProfileFormProps {
  profile?: BufferProfile;
  onSaved: (p: BufferProfile) => void;
  onReload?: () => void;
}

const PROFILE_NUMBERS = [
  { key: "leadTimeFactor", label: "Lead-time factor (LTF)", hint: "Red base = ADU × DLT × LTF.", mode: "decimal" },
  { key: "variabilityFactor", label: "Variability factor (VF)", hint: "Red safety = red base × VF.", mode: "decimal" },
  { key: "orderCycleDays", label: "Order cycle (days)", hint: "Green is at least ADU × order cycle.", mode: "numeric" },
  { key: "minOrderQty", label: "Minimum order quantity", hint: "Green is at least the MOQ; orders are raised to it.", mode: "decimal" },
  { key: "spikeThresholdPct", label: "Spike threshold (% of red)", hint: "A day above it inside the horizon is a spike.", mode: "decimal" },
  { key: "aduWindowPastDays", label: "ADU window, past days", hint: "Days of history averaged into ADU.", mode: "numeric" },
  { key: "aduWindowFutureDays", label: "ADU window, future days", hint: "Days of forward demand averaged into ADU.", mode: "numeric" },
] as const;

function BufferProfileForm({ profile, onSaved, onReload }: ProfileFormProps) {
  const creating = !profile;
  const [draft, setDraft] = useState<BufferProfileDraft>(() => (profile ? profileDraftOf(profile) : EMPTY_PROFILE));
  const [errors, setErrors] = useState<ProfileErrors>({});
  const [failure, setFailure] = useState<LoadError | null>(null);
  const [busy, setBusy] = useState(false);
  // One key per create attempt, kept across retries of the same draft.
  const key = useRef(newKey());

  useEffect(() => {
    if (profile) {
      setDraft(profileDraftOf(profile));
      setErrors({});
    }
  }, [profile?.id, profile?.version]);

  const set = <K extends keyof BufferProfileDraft>(field: K, value: BufferProfileDraft[K]) => {
    setDraft((d) => ({ ...d, [field]: value }));
    key.current = newKey();
  };

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (busy) return;
    const { errors: found, input } = validateBufferProfile(draft);
    setErrors(found);
    if (!input) return;
    setBusy(true);
    setFailure(null);
    try {
      onSaved(profile ? await updateBufferProfile(profile.id, profile.version, input) : await createBufferProfile(input, key.current));
    } catch (err) {
      setFailure(describeError(err));
    } finally {
      setBusy(false);
    }
  }

  const stale = failure?.status === 412;
  const failureTitle = stale
    ? "Someone else changed this buffer profile"
    : failure?.code === "PLAN-PROFILE-EXISTS"
      ? "A buffer profile with this code already exists"
      : "The buffer profile was not saved";

  return (
    <form className={styles.form} onSubmit={submit} noValidate aria-label={creating ? "New buffer profile" : "Edit buffer profile"}>
      <div className={styles.wide}>
        <Alert
          tone="danger"
          title={failure ? failureTitle : undefined}
          live="alert"
          action={
            stale && onReload ? (
              <Button size="sm" onClick={onReload}>
                Reload profile
              </Button>
            ) : undefined
          }
        >
          {failure ? `${failure.message} (${failure.code})` : null}
        </Alert>
      </div>
      <FormField id="bp-code" label="Code" error={errors.code}>
        {(a11y) => <Input {...a11y} className={styles.mono} value={draft.code} onChange={(e) => set("code", e.target.value)} />}
      </FormField>
      <FormField id="bp-itemType" label="Item type">
        {(a11y) => (
          <Select {...a11y} value={draft.itemType} onChange={(e) => set("itemType", e.target.value as BufferItemType)}>
            {(Object.keys(itemTypeLabel) as BufferItemType[]).map((t) => (
              <option key={t} value={t}>
                {itemTypeLabel[t]}
              </option>
            ))}
          </Select>
        )}
      </FormField>
      {PROFILE_NUMBERS.map((f) => (
        <FormField key={f.key} id={`bp-${f.key}`} label={f.label} hint={f.hint} error={errors[f.key]}>
          {(a11y) => <Input {...a11y} inputMode={f.mode} value={draft[f.key]} onChange={(e) => set(f.key, e.target.value)} />}
        </FormField>
      ))}
      <FormField id="bp-spikeHorizonDays" label="Spike horizon (days)" hint="Blank uses the item's decoupled lead time." optional error={errors.spikeHorizonDays}>
        {(a11y) => <Input {...a11y} inputMode="numeric" value={draft.spikeHorizonDays} onChange={(e) => set("spikeHorizonDays", e.target.value)} />}
      </FormField>
      <div className={styles.formActions}>
        <Button type="submit" variant="primary" loading={busy}>
          {creating ? "Create buffer profile" : "Save changes"}
        </Button>
        <Link className={buttonClassName("secondary")} to={PROFILES}>
          Cancel
        </Link>
      </div>
    </form>
  );
}

export function NewBufferProfilePage() {
  const navigate = useNavigate();
  return (
    <div className={styles.stack}>
      <PageHeader title="New buffer profile" description="Defaults: spike threshold 50% of red, ADU over the past 90 days, spike horizon = DLT." />
      <Card>
        <BufferProfileForm onSaved={(p) => navigate(`${PROFILES}/${encodeURIComponent(p.id)}`)} />
      </Card>
    </div>
  );
}

/** Shows and edits one buffer profile under If-Match on its version. */
export function BufferProfilePage() {
  const { id = "" } = useParams();
  const [reload, setReload] = useState(0);
  const state = useLoad((signal) => getBufferProfile(id, { signal }), [id, reload]);
  const [saved, setSaved] = useState<BufferProfile | null>(null);
  const profile = saved && saved.id === id ? saved : state.data;

  useEffect(() => setSaved(null), [state.data]);

  return (
    <div className={styles.stack}>
      <PageHeader
        title={profile ? `Buffer profile ${profile.code}` : "Buffer profile"}
        description="Changes apply to buffers at their next recalculation."
        actions={
          <>
            <Link className={buttonClassName("secondary")} to={profile ? `${BUFFERS}?profile=${encodeURIComponent(profile.id)}` : BUFFERS}>
              Recalculate a buffer
            </Link>
            <Link className={buttonClassName("secondary")} to={PROFILES}>
              All buffer profiles
            </Link>
          </>
        }
      />
      <LoadAlert what="the buffer profile" error={state.error} />
      <Alert tone="success" live="status">
        {saved ? `Saved. Version ${saved.version}.` : null}
      </Alert>
      {state.loading ? (
        <Card>
          <div className={styles.stack} aria-busy="true" aria-label="Loading buffer profile">
            <Skeleton width="1/2" />
            <Skeleton height="control" />
            <Skeleton height="control" />
          </div>
        </Card>
      ) : null}
      {profile ? (
        <Card title="Settings">
          <BufferProfileForm profile={profile} onSaved={setSaved} onReload={() => setReload((n) => n + 1)} />
        </Card>
      ) : null}
    </div>
  );
}

const bufferColumns: DataTableColumn<DdmrpBuffer>[] = [
  { key: "item", header: "Item", mono: true, render: (b) => b.itemId },
  { key: "scope", header: "Site / warehouse", mono: true, render: (b) => (b.warehouseId ? `${b.siteId} / ${b.warehouseId}` : b.siteId) },
  { key: "profile", header: "Profile", mono: true, render: (b) => b.profileCode },
  { key: "nfp", header: "Net flow", align: "end", render: (b) => trimQty(b.netFlow) },
  { key: "tor", header: "Top of red", align: "end", render: (b) => trimQty(b.zones.topOfRed) },
  { key: "toy", header: "Top of yellow", align: "end", render: (b) => trimQty(b.zones.topOfYellow) },
  { key: "tog", header: "Top of green", align: "end", render: (b) => trimQty(b.zones.topOfGreen) },
  { key: "priority", header: "Priority", align: "end", render: (b) => priorityPercent(b.priority) },
  { key: "zone", header: "Zone", render: (b) => <Badge tone={zoneTone[b.zone]}>{zoneLabel[b.zone]}</Badge> },
  { key: "planned", header: "Planned qty", align: "end", render: (b) => (trimQty(b.plannedQty) === "0" ? dash : trimQty(b.plannedQty)) },
  { key: "at", header: "Calculated", render: (b) => b.calculatedAt.replace("T", " ").replace("Z", " UTC") },
];

/** The recalculation form as typed. */
export interface RecalcDraft {
  itemId: string;
  siteId: string;
  warehouseId: string;
  profileId: string;
  adu: string;
  dltDays: string;
  daf: string;
  onHand: string;
  openSupply: string;
  qualifiedDemand: string;
}

type RecalcErrors = Partial<Record<keyof RecalcDraft, string>>;

export const EMPTY_RECALC: RecalcDraft = {
  itemId: "",
  siteId: "",
  warehouseId: "",
  profileId: "",
  adu: "",
  dltDays: "",
  daf: "1",
  onHand: "0",
  openSupply: "0",
  qualifiedDemand: "0",
};

/** Checks the recalculation draft; qualified demand (past due, today and spikes) goes as day 0. */
export function validateRecalc(d: RecalcDraft): { errors: RecalcErrors; body: BufferRecalculation | null } {
  const errors: RecalcErrors = {};
  const id = (v: string) => v.trim().toLowerCase();
  if (!UUID.test(id(d.itemId))) errors.itemId = "Enter the item id as a UUID.";
  if (!UUID.test(id(d.siteId))) errors.siteId = "Enter the site id as a UUID.";
  if (id(d.warehouseId) !== "" && !UUID.test(id(d.warehouseId))) errors.warehouseId = "Enter the warehouse id as a UUID or leave it blank.";
  if (!UUID.test(d.profileId)) errors.profileId = "Choose a buffer profile.";
  if (!QTY.test(d.adu.trim())) errors.adu = "Enter the average daily usage, 0 or more.";
  if (!/^\d{1,4}(\.\d{1,2})?$/.test(d.dltDays.trim()) || compareQty(d.dltDays.trim(), "3650") > 0) {
    errors.dltDays = "Enter 0 to 3650 days with at most 2 decimals.";
  }
  if (!/^\d{1,3}(\.\d{1,3})?$/.test(d.daf.trim())) errors.daf = "Enter a factor from 0 to 999.999.";
  if (!SIGNED_QTY.test(d.onHand.trim())) errors.onHand = "Enter a quantity with at most 6 decimals.";
  if (!QTY.test(d.openSupply.trim())) errors.openSupply = "Enter a quantity of 0 or more.";
  if (!QTY.test(d.qualifiedDemand.trim())) errors.qualifiedDemand = "Enter a quantity of 0 or more.";
  if (Object.keys(errors).length > 0) return { errors, body: null };
  const demand = compareQty(d.qualifiedDemand.trim(), "0") > 0 ? [{ day: 0, qty: d.qualifiedDemand.trim() }] : [];
  return {
    errors,
    body: {
      itemId: id(d.itemId),
      siteId: id(d.siteId),
      ...(id(d.warehouseId) ? { warehouseId: id(d.warehouseId) } : {}),
      profileId: d.profileId,
      adu: d.adu.trim(),
      dltDays: d.dltDays.trim(),
      daf: d.daf.trim(),
      onHand: d.onHand.trim(),
      openSupply: d.openSupply.trim(),
      demand,
    },
  };
}

const RECALC_FIELDS = [
  { key: "itemId", label: "Item", mono: true, optional: false, hint: undefined },
  { key: "siteId", label: "Buffer site", mono: true, optional: false, hint: undefined },
  { key: "warehouseId", label: "Warehouse", mono: true, optional: true, hint: "Blank for the site scope." },
  { key: "adu", label: "Average daily usage (ADU)", mono: false, optional: false, hint: undefined },
  { key: "dltDays", label: "Decoupled lead time (days)", mono: false, optional: false, hint: undefined },
  { key: "daf", label: "Adjustment factor (DAF)", mono: false, optional: false, hint: "1 is no adjustment." },
  { key: "onHand", label: "On hand", mono: false, optional: false, hint: undefined },
  { key: "openSupply", label: "Open supply inside the DLT", mono: false, optional: false, hint: undefined },
  { key: "qualifiedDemand", label: "Qualified demand", mono: false, optional: false, hint: "Past due, due today and spikes." },
] as const;

function RecalculateCard({ profiles, initialProfile, onStored }: { profiles: BufferProfile[]; initialProfile: string; onStored: () => void }) {
  const [draft, setDraft] = useState<RecalcDraft>({ ...EMPTY_RECALC, profileId: initialProfile });
  const [errors, setErrors] = useState<RecalcErrors>({});
  const [failure, setFailure] = useState<LoadError | null>(null);
  const [result, setResult] = useState<DdmrpBuffer | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (initialProfile) setDraft((d) => ({ ...d, profileId: d.profileId || initialProfile }));
  }, [initialProfile]);

  const set = (field: keyof RecalcDraft, value: string) => setDraft((d) => ({ ...d, [field]: value }));

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (busy) return;
    const { errors: found, body } = validateRecalc(draft);
    setErrors(found);
    if (!body) return;
    setBusy(true);
    setFailure(null);
    setResult(null);
    try {
      setResult(await recalculateDdmrpBuffer(body));
      onStored();
    } catch (err) {
      setFailure(describeError(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Recalculate a buffer">
      <form className={styles.form} onSubmit={submit} noValidate aria-label="Recalculate a buffer">
        <p className={`${styles.summary} ${styles.wide}`}>
          Sizes the zones from the profile and today's state (BR-22.11) and replaces the stored buffer of the item, site and warehouse. Needs Run master planning.
        </p>
        <div className={styles.wide}>
          <Alert tone="danger" title={failure ? "The buffer was not recalculated" : undefined} live="alert">
            {failure ? `${failure.message} (${failure.code})` : null}
          </Alert>
          <Alert tone="success" title={result ? "Buffer stored" : undefined} live="status">
            {result
              ? `${zoneLabel[result.zone]} zone, net flow ${trimQty(result.netFlow)}, priority ${priorityPercent(result.priority)}` +
                (trimQty(result.plannedQty) === "0" ? ", no order needed." : `, order ${trimQty(result.plannedQty)}.`)
              : null}
          </Alert>
        </div>
        <FormField id="rc-profileId" label="Buffer profile" error={errors.profileId}>
          {(a11y) => (
            <Select {...a11y} value={draft.profileId} onChange={(e) => set("profileId", e.target.value)}>
              <option value="">{profiles.length ? "Choose a profile" : "No profiles yet"}</option>
              {profiles.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.code}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        {RECALC_FIELDS.map((f) => (
          <FormField key={f.key} id={`rc-${f.key}`} label={f.label} hint={f.hint} optional={f.optional} error={errors[f.key]}>
            {(a11y) => (
              <Input
                {...a11y}
                className={f.mono ? styles.mono : undefined}
                inputMode={f.mono ? undefined : "decimal"}
                value={draft[f.key]}
                onChange={(e) => set(f.key, e.target.value)}
              />
            )}
          </FormField>
        ))}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Recalculate buffer
          </Button>
        </div>
      </form>
    </Card>
  );
}

/**
 * DDMRP buffer status (4.22.14): stored buffers filtered by site, zone and a
 * priority ceiling kept in the query (`?site=&zone=&maxPct=`), most urgent first,
 * with a card that recalculates one buffer.
 */
export function DdmrpBuffersPage() {
  const [params, setParams] = useSearchParams();
  const rawSite = params.get("site") ?? "";
  const site = UUID.test(rawSite) ? rawSite.toLowerCase() : "";
  const rawZone = params.get("zone") ?? "";
  const zone = rawZone === "red" || rawZone === "yellow" || rawZone === "green" ? rawZone : "";
  const rawPct = params.get("maxPct") ?? "";
  const maxPct = PERCENT.test(rawPct) ? rawPct : "";
  const [filter, setFilter] = useState({ site, zone: zone as BufferZone | "", maxPct });
  const [filterErrors, setFilterErrors] = useState<{ site?: string; maxPct?: string }>({});
  const [reload, setReload] = useState(0);
  const state = useLoad(
    (signal) => listDdmrpBuffers({ siteId: site, zone, maxPriority: maxPct ? shiftDecimal(maxPct, -2) : undefined }, { signal }),
    [site, zone, maxPct, reload],
  );
  const profiles = useLoad((signal) => listBufferProfiles({ signal }), []);

  function submit(e: FormEvent) {
    e.preventDefault();
    const found: typeof filterErrors = {};
    const s = filter.site.trim();
    if (s !== "" && !UUID.test(s)) found.site = "Enter the site id as a UUID, or leave it blank.";
    const p = filter.maxPct.trim();
    if (p !== "" && !PERCENT.test(p)) found.maxPct = "Enter a percent such as 60, or leave it blank.";
    setFilterErrors(found);
    if (Object.keys(found).length > 0) return;
    const next = new URLSearchParams(params);
    for (const [k, v] of [
      ["site", s.toLowerCase()],
      ["zone", filter.zone],
      ["maxPct", p],
    ] as const) {
      if (v) next.set(k, v);
      else next.delete(k);
    }
    setParams(next);
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="DDMRP buffer status"
        description="Stored buffers with net-flow position, zone tops and planning priority (NFP / TOG), most urgent first."
        actions={
          <Link className={buttonClassName("secondary")} to={PROFILES}>
            Buffer profiles
          </Link>
        }
      />
      <form className={styles.toolbar} onSubmit={submit} noValidate aria-label="Filter buffers">
        <div className={styles.toolbarFieldWide}>
          <FormField id="db-site" label="Site" hint="Blank shows every site." error={filterErrors.site}>
            {(a11y) => <Input {...a11y} className={styles.mono} value={filter.site} onChange={(e) => setFilter((f) => ({ ...f, site: e.target.value }))} />}
          </FormField>
        </div>
        <div className={styles.toolbarField}>
          <FormField id="db-zone" label="Zone">
            {(a11y) => (
              <Select {...a11y} value={filter.zone} onChange={(e) => setFilter((f) => ({ ...f, zone: e.target.value as BufferZone | "" }))}>
                <option value="">All zones</option>
                <option value="red">Red</option>
                <option value="yellow">Yellow</option>
                <option value="green">Green</option>
              </Select>
            )}
          </FormField>
        </div>
        <div className={styles.toolbarField}>
          <FormField id="db-maxPct" label="Priority at most (%)" error={filterErrors.maxPct}>
            {(a11y) => <Input {...a11y} inputMode="decimal" value={filter.maxPct} onChange={(e) => setFilter((f) => ({ ...f, maxPct: e.target.value }))} />}
          </FormField>
        </div>
        <Button type="submit">Show buffers</Button>
      </form>
      <LoadAlert what="buffers" error={state.error} />
      {state.error ? null : (
        <DataTable
          label="DDMRP buffers"
          columns={bufferColumns}
          rows={state.data ?? []}
          getRowKey={(b) => b.id}
          loading={state.loading}
          empty={{
            title: site || zone || maxPct ? "No buffer matches these filters" : "No buffers calculated yet",
            description: "Recalculate a buffer below, or wait for the nightly recalculation.",
          }}
        />
      )}
      <LoadAlert what="buffer profiles" error={profiles.error} />
      <RecalculateCard profiles={profiles.data ?? []} initialProfile={params.get("profile") ?? ""} onStored={() => setReload((n) => n + 1)} />
    </div>
  );
}
