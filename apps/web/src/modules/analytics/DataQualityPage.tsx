import { useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  buttonClassName,
  Card,
  DataTable,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  Select,
  Skeleton,
  StatGroup,
  StatTile,
  StatusPill,
  Textarea,
  type DataTableColumn,
} from "../../shell/ui";
import {
  ASSET_FQN,
  describeProblem,
  getDQRule,
  listDQRules,
  listDQScores,
  putDQRule,
  RULE_ID,
  RULE_STATUSES,
  RULE_TYPES,
  SEVERITIES,
  UUID,
  type DQRule,
  type DQRuleFilter,
  type DQRuleInput,
  type DQScore,
  type Problem,
} from "./api/client";
import { count, dateTime, enumLabel } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

/** Mirrors BR-46.15: three consecutive below-threshold evaluations escalate. */
const ESCALATE_AFTER = 3;

interface Row {
  rule: DQRule;
  latest?: DQScore;
}

function isMuted(rule: DQRule, now = Date.now()): boolean {
  return rule.muteUntil !== undefined && new Date(rule.muteUntil).getTime() > now;
}

function latestByRule(scores: DQScore[]): Map<string, DQScore> {
  const out = new Map<string, DQScore>();
  for (const s of scores) {
    const seen = out.get(s.ruleId);
    if (!seen || s.date > seen.date) out.set(s.ruleId, s);
  }
  return out;
}

function ScoreCell({ score }: { score?: DQScore }) {
  if (!score) return <span className={styles.muted}>Not scored yet</span>;
  return score.below ? <Badge tone="danger">{score.passPct}%</Badge> : <Badge tone="success">{score.passPct}%</Badge>;
}

const columns: DataTableColumn<Row>[] = [
  { key: "ruleId", header: "Rule", mono: true, render: (r) => r.rule.ruleId },
  { key: "assetFqn", header: "Asset", mono: true, render: (r) => r.rule.assetFqn },
  { key: "ruleType", header: "Type", render: (r) => enumLabel(r.rule.ruleType) },
  { key: "thresholdPct", header: "Threshold", align: "end", render: (r) => `${r.rule.thresholdPct}%` },
  { key: "passPct", header: "Pass rate", align: "end", render: (r) => <ScoreCell score={r.latest} /> },
  { key: "failedRows", header: "Failed rows", align: "end", render: (r) => (r.latest ? count(r.latest.failedRows) : "") },
  {
    key: "streak",
    header: "Below in a row",
    align: "end",
    render: (r) => (r.latest && r.latest.belowStreak >= ESCALATE_AFTER ? <Badge tone="warning">{r.latest.belowStreak} · escalated</Badge> : (r.latest?.belowStreak ?? "")),
  },
  { key: "severity", header: "Severity", render: (r) => enumLabel(r.rule.severity) },
  {
    key: "status",
    header: "Status",
    render: (r) => (isMuted(r.rule) ? <Badge tone="neutral">Muted</Badge> : <StatusPill status={r.rule.status} />),
  },
];

/**
 * Data-quality scorecard (FR-46.16, PRD §9.46.6): every rule with its latest
 * pass rate, failed rows and below-threshold streak.
 */
export function DataQualityPage() {
  const navigate = useNavigate();
  const [draft, setDraft] = useState<Required<DQRuleFilter>>({ asset: "", ruleType: "", owner: "" });
  const [filter, setFilter] = useState<Required<DQRuleFilter>>(draft);
  const [filterErrors, setFilterErrors] = useState<Partial<Record<keyof DQRuleFilter, string>>>({});
  const state = useLoad(async (signal) => {
    const [rules, scores] = await Promise.all([
      listDQRules(filter, undefined, signal),
      listDQScores({ asset: filter.asset, limit: 366 }, undefined, signal),
    ]);
    const latest = latestByRule(scores);
    return rules.map((rule): Row => ({ rule, latest: latest.get(rule.ruleId) }));
  }, `dq/${filter.asset}/${filter.ruleType}/${filter.owner}`);
  const filtered = filter.asset !== "" || filter.ruleType !== "" || filter.owner !== "";

  function applyFilter(event: FormEvent) {
    event.preventDefault();
    const next = { asset: draft.asset.trim(), ruleType: draft.ruleType, owner: draft.owner.trim() };
    const errors: Partial<Record<keyof DQRuleFilter, string>> = {};
    if (next.asset !== "" && !ASSET_FQN.test(next.asset)) errors.asset = "Use a dotted lower-case asset such as core.vendor.";
    if (next.owner !== "" && !UUID.test(next.owner)) errors.owner = "Use the owner's user id (a UUID).";
    setFilterErrors(errors);
    if (Object.keys(errors).length === 0) setFilter(next);
  }
  const rows = state.data ?? [];
  const failing = rows.filter((r) => r.rule.status === "ACTIVE" && r.latest?.below).length;
  const escalated = rows.filter((r) => (r.latest?.belowStreak ?? 0) >= ESCALATE_AFTER).length;

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Data quality"
        description="Rules on catalog assets are evaluated every hour. A rule below its threshold creates one task for its owner each day; three failures in a row also go to the catalog steward."
        actions={
          <Link className={buttonClassName("primary")} to="/analytics/data-quality/new">
            New rule
          </Link>
        }
      />
      <form className={styles.toolbar} onSubmit={applyFilter} noValidate aria-label="Scorecard filter">
        <FormField id="dq-filter-asset" label="Asset" error={filterErrors.asset} optional>
          {(control) => (
            <Input
              {...control}
              className={styles.mono}
              placeholder="core.vendor"
              value={draft.asset}
              invalid={Boolean(filterErrors.asset)}
              onChange={(e) => setDraft((d) => ({ ...d, asset: e.target.value }))}
            />
          )}
        </FormField>
        <FormField id="dq-filter-type" label="Rule type">
          {(control) => (
            <Select {...control} value={draft.ruleType} onChange={(e) => setDraft((d) => ({ ...d, ruleType: e.target.value }))}>
              <option value="">Any type</option>
              {RULE_TYPES.map((t) => (
                <option key={t} value={t}>
                  {enumLabel(t)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="dq-filter-owner" label="Owner user id" error={filterErrors.owner} optional>
          {(control) => (
            <Input
              {...control}
              className={styles.mono}
              value={draft.owner}
              invalid={Boolean(filterErrors.owner)}
              onChange={(e) => setDraft((d) => ({ ...d, owner: e.target.value }))}
            />
          )}
        </FormField>
        <Button type="submit">Show rules</Button>
      </form>
      {state.error ? (
        <Alert tone="danger" title="Could not load data-quality rules" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <>
          {state.data ? (
            <StatGroup label="Data-quality summary">
              <StatTile label="Rules" value={rows.length} />
              <StatTile label="Below threshold" value={failing} tone="danger" />
              <StatTile label="Escalated" value={escalated} tone="warning" />
              <StatTile label="Muted" value={rows.filter((r) => isMuted(r.rule)).length} />
            </StatGroup>
          ) : null}
          <DataTable
            label="Data-quality rules"
            columns={columns}
            rows={rows}
            getRowKey={(r) => r.rule.ruleId}
            loading={state.loading}
            empty={
              filtered
                ? { title: "No rules match the filter", description: "Change the asset, rule type or owner, or clear them to see every rule." }
                : { title: "No data-quality rules yet", description: "Add a rule to start scoring a catalog asset." }
            }
            onRowOpen={(r) => navigate(`/analytics/data-quality/${encodeURIComponent(r.rule.ruleId)}`)}
          />
        </>
      )}
    </div>
  );
}

type Draft = DQRuleInput & { ruleId: string };
type DraftErrors = Partial<Record<keyof Draft, string>>;

const blank: Draft = {
  ruleId: "",
  assetFqn: "",
  ruleType: "COMPLETENESS",
  expression: "",
  thresholdPct: "98",
  severity: "MEDIUM",
  ownerUserId: "",
  status: "ACTIVE",
  muted: false,
};

const expressionHints: Record<DQRule["ruleType"], string> = {
  COMPLETENESS: "The column that must not be empty, for example tax_id.",
  VALIDITY: "A regular expression each value must match.",
  UNIQUENESS: "The column or comma-separated columns that must be unique.",
  REFERENTIAL: "The referenced asset and column, for example core.vendor.vendor_id.",
  FRESHNESS: "The maximum age of _ingested_at, for example 2h.",
};

/** Checks that mirror domain.DQRule.Validate in services/analytics. */
export function validateRule(d: Draft): DraftErrors {
  const errors: DraftErrors = {};
  if (!RULE_ID.test(d.ruleId)) errors.ruleId = "Use 2 to 63 lowercase letters, digits and underscores, starting with a letter.";
  if (!ASSET_FQN.test(d.assetFqn)) errors.assetFqn = "Use a dotted lowercase asset name such as core.vendor.";
  if (d.expression.trim() === "") errors.expression = "Enter the expression the rule checks.";
  else if (d.expression.length > 2048) errors.expression = "Keep the expression to 2048 characters.";
  else if (d.ruleType === "VALIDITY") {
    try {
      new RegExp(d.expression);
    } catch {
      errors.expression = "Enter a valid regular expression.";
    }
  }
  if (!/^\d{1,3}(\.\d{1,2})?$/.test(d.thresholdPct) || Number(d.thresholdPct) > 100) {
    errors.thresholdPct = "Enter a percentage from 0 to 100 with at most two decimals.";
  }
  if (!UUID.test(d.ownerUserId)) errors.ownerUserId = "Enter the owner's user id.";
  return errors;
}

function fromRule(rule: DQRule): Draft {
  return {
    ruleId: rule.ruleId,
    assetFqn: rule.assetFqn,
    ruleType: rule.ruleType,
    expression: rule.expression,
    thresholdPct: rule.thresholdPct,
    severity: rule.severity,
    ownerUserId: rule.ownerUserId,
    status: rule.status,
    muted: isMuted(rule),
  };
}

const scoreColumns: DataTableColumn<DQScore>[] = [
  { key: "date", header: "Day", mono: true },
  { key: "passPct", header: "Pass rate", align: "end", render: (s) => <ScoreCell score={s} /> },
  { key: "failedRows", header: "Failed rows", align: "end", render: (s) => count(s.failedRows) },
  { key: "totalRows", header: "Rows checked", align: "end", render: (s) => count(s.totalRows) },
  { key: "belowStreak", header: "Below in a row", align: "end" },
];

function RuleForm({ initial, existing, onSaved }: { initial: Draft; existing: DQRule | null; onSaved(rule: DQRule): void }) {
  const [draft, setDraft] = useState<Draft>(initial);
  const [errors, setErrors] = useState<DraftErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const set = <K extends keyof Draft>(key: K, value: Draft[K]) => setDraft((d) => ({ ...d, [key]: value }));

  async function save(next: Draft) {
    const found = validateRule(next);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const { ruleId, ...input } = next;
      onSaved(await putDQRule(ruleId, input, existing ? existing.version : null));
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      setErrors(problem.fields as DraftErrors);
    } finally {
      setBusy(false);
    }
  }

  function submit(event: FormEvent) {
    event.preventDefault();
    void save(draft);
  }

  const field = (key: keyof Draft) => ({ invalid: Boolean(errors[key]) });

  return (
    <Card title={existing ? "Rule definition" : "New rule"}>
      <form className={styles.form} onSubmit={submit} noValidate aria-label="Data-quality rule">
        <FormField id="dq-rule-id" label="Rule id" error={errors.ruleId} hint={existing ? "A rule id cannot change." : undefined}>
          {(control) => (
            <Input {...control} {...field("ruleId")} className={styles.mono} value={draft.ruleId} readOnly={existing !== null} onChange={(e) => set("ruleId", e.target.value)} />
          )}
        </FormField>
        <FormField id="dq-asset" label="Asset" error={errors.assetFqn}>
          {(control) => <Input {...control} {...field("assetFqn")} className={styles.mono} value={draft.assetFqn} onChange={(e) => set("assetFqn", e.target.value)} />}
        </FormField>
        <FormField id="dq-type" label="Rule type">
          {(control) => (
            <Select {...control} value={draft.ruleType} onChange={(e) => set("ruleType", e.target.value as Draft["ruleType"])}>
              {RULE_TYPES.map((t) => (
                <option key={t} value={t}>
                  {enumLabel(t)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="dq-threshold" label="Threshold %" hint="The rule fails below this pass rate." error={errors.thresholdPct}>
          {(control) => <Input {...control} {...field("thresholdPct")} inputMode="decimal" value={draft.thresholdPct} onChange={(e) => set("thresholdPct", e.target.value)} />}
        </FormField>
        <div className={styles.wide}>
          <FormField id="dq-expression" label="Expression" hint={expressionHints[draft.ruleType]} error={errors.expression}>
            {(control) => (
              <Textarea {...control} {...field("expression")} className={styles.mono} value={draft.expression} onChange={(e) => set("expression", e.target.value)} />
            )}
          </FormField>
        </div>
        <FormField id="dq-severity" label="Severity">
          {(control) => (
            <Select {...control} value={draft.severity} onChange={(e) => set("severity", e.target.value as Draft["severity"])}>
              {SEVERITIES.map((s) => (
                <option key={s} value={s}>
                  {enumLabel(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="dq-owner" label="Owner user id" hint="The owner gets the daily task when the rule fails." error={errors.ownerUserId}>
          {(control) => <Input {...control} {...field("ownerUserId")} className={styles.mono} value={draft.ownerUserId} onChange={(e) => set("ownerUserId", e.target.value)} />}
        </FormField>
        <FormField id="dq-status" label="Status">
          {(control) => (
            <Select {...control} value={draft.status} onChange={(e) => set("status", e.target.value as Draft["status"])}>
              {RULE_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {enumLabel(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title={failure.code === "ANA-0113" ? "Someone else changed this rule" : "The rule was not saved"} live="alert">
              {failure.code === "ANA-0113" ? "Reload the page to see the current version, then make your change again." : failure.message} ({failure.code})
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            {existing ? "Save rule" : "Create rule"}
          </Button>
          {existing ? (
            <Button
              onClick={() => {
                const next = { ...draft, muted: !draft.muted };
                setDraft(next);
                void save(next);
              }}
              disabled={busy}
            >
              {draft.muted ? "Unmute rule" : "Mute for 7 days"}
            </Button>
          ) : null}
        </div>
      </form>
    </Card>
  );
}

/** Creates a data-quality rule. */
export function DQRuleCreatePage() {
  const navigate = useNavigate();
  return (
    <div className={styles.stack}>
      <PageHeader title="New data-quality rule" description="The rule is scored at the next hourly evaluation." />
      <RuleForm initial={blank} existing={null} onSaved={(rule) => navigate(`/analytics/data-quality/${encodeURIComponent(rule.ruleId)}`)} />
    </div>
  );
}

/** One rule: its definition, mute window and score history. */
export function DQRuleDetailPage() {
  const { ruleId = "" } = useParams();
  const [saved, setSaved] = useState<DQRule | null>(null);
  const state = useLoad(
    async (signal) => {
      const [rule, scores] = await Promise.all([getDQRule(ruleId, undefined, signal), listDQScores({ rule: ruleId }, undefined, signal)]);
      return { rule, scores };
    },
    `dq/${ruleId}`,
  );

  if (state.loading && !state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Data-quality rule" />
        <Card>
          <div className={styles.loading}>
            <Skeleton width="1/2" height="control" />
            <Skeleton width="3/4" height="control" />
          </div>
        </Card>
        <p role="status" className={styles.muted}>
          Loading rule {ruleId}…
        </p>
      </div>
    );
  }
  if (state.error?.code === "ANA-0112") {
    return (
      <Card>
        <EmptyState
          headingLevel={2}
          icon="database"
          title="We couldn't find that rule"
          description={`No data-quality rule has the id ${ruleId}.`}
          action={
            <Link className={buttonClassName("secondary")} to="/analytics/data-quality">
              Back to data quality
            </Link>
          }
        />
      </Card>
    );
  }
  if (state.error || !state.data) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Data-quality rule" />
        <Alert tone="danger" title="Could not load the rule" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  const rule = saved ?? state.data.rule;
  const scores = state.data.scores;
  return (
    <div className={styles.stack}>
      <PageHeader
        title={rule.ruleId}
        description={`${enumLabel(rule.ruleType)} rule on ${rule.assetFqn}, version ${rule.version}.`}
        meta={isMuted(rule) ? <Badge tone="neutral">Muted</Badge> : <StatusPill status={rule.status} />}
      />
      <p role="status" className={styles.muted}>
        {saved ? `Rule saved as version ${saved.version}.` : ""}
      </p>
      {isMuted(rule) ? (
        <Alert tone="info" title={`Muted until ${dateTime(rule.muteUntil)}`}>
          The rule keeps scoring, but its owner and the steward are not notified.
        </Alert>
      ) : null}
      <RuleForm key={rule.version} initial={fromRule(rule)} existing={rule} onSaved={setSaved} />
      <DataTable
        label="Score history"
        columns={scoreColumns}
        rows={scores}
        getRowKey={(s) => s.date}
        empty={{ title: "Not scored yet", description: "Scores appear after the next hourly evaluation." }}
      />
    </div>
  );
}
