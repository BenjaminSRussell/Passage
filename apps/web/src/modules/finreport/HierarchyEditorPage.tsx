import { useEffect, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, Select, Skeleton, Textarea, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  getHierarchy,
  getHierarchyMembers,
  HIERARCHY_SUBJECTS,
  publishHierarchy,
  ReportingApiError,
  saveHierarchy,
  type Hierarchy,
  type HierarchyNode,
  type HierarchySubject,
  type Membership,
  type Problem,
} from "./api/client";
import { DEFINITION_CODE, SUBJECT_LABELS } from "./format";
import { formatRules, parseMembers, parseRules } from "./hierarchyText";
import styles from "./pages.module.css";

export function blankHierarchy(): Hierarchy {
  return {
    schemaVersion: "1",
    code: "",
    name: "",
    version: 0,
    status: "draft",
    subject: "main_account",
    strict: true,
    nodes: [
      { code: "TOTAL", label: "Total" },
      { code: "N1", label: "", parent: "TOTAL", rules: [] },
    ],
  };
}

/** Drops empty optional fields so the service sees a clean hierarchy. */
export function cleanHierarchy(h: Hierarchy): Hierarchy {
  const out: Hierarchy = {
    ...h,
    code: h.code.trim(),
    nodes: h.nodes.map((n) => {
      const node: HierarchyNode = { code: n.code.trim(), label: n.label };
      if (n.parent) node.parent = n.parent;
      if (n.rules && n.rules.length > 0) node.rules = n.rules;
      return node;
    }),
  };
  if (out.subject !== "dimension_value") delete out.dimensionAttribute;
  if (!out.effectiveFrom) delete out.effectiveFrom;
  return out;
}

interface Loaded {
  h: Hierarchy;
  etag: string | null;
  basedOn: number;
}

type Published = Membership & { version: number };

interface LeafRow {
  code: string;
  label: string;
  members: number;
}

const leafColumns: DataTableColumn<LeafRow>[] = [
  { key: "code", header: "Leaf", mono: true },
  { key: "label", header: "Label" },
  { key: "members", header: "Members", mono: true, align: "end" },
];

function notFound(error: unknown) {
  if (describeProblem(error).status !== 404) throw error;
  return null;
}

/**
 * Derived hierarchy editor (FR-15.13, BR-15.13): nodes with membership rules on
 * the leaves, a revision-checked draft, and publish with the members to
 * classify. The published version's membership (members per leaf and the
 * unassigned ones) is shown below.
 */
export function HierarchyEditorPage() {
  const { code: routeCode } = useParams();
  const isNew = routeCode === undefined;
  const navigate = useNavigate();
  const [loaded, setLoaded] = useState<Loaded | null>(isNew ? { h: blankHierarchy(), etag: null, basedOn: 0 } : null);
  const [loadError, setLoadError] = useState<Problem | null>(null);
  const [attempt, setAttempt] = useState(0);
  const [h, setH] = useState<Hierarchy>(blankHierarchy());
  const [etag, setEtag] = useState<string | null>(null);
  const [dirty, setDirty] = useState(false);
  const [membership, setMembership] = useState<Published | null>(null);
  const [membersText, setMembersText] = useState("");
  const [membersError, setMembersError] = useState<string | undefined>();
  const [codeError, setCodeError] = useState<string | undefined>();
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState<"save" | "publish" | null>(null);

  useEffect(() => {
    if (isNew) {
      setH(blankHierarchy());
      return;
    }
    const controller = new AbortController();
    const code = routeCode ?? "";
    setLoaded(null);
    setLoadError(null);
    (async () => {
      const [draft, latest, members] = await Promise.all([
        getHierarchy(code, "draft", controller.signal).catch(notFound),
        getHierarchy(code, "latest", controller.signal).catch(notFound),
        getHierarchyMembers(code, controller.signal).catch(notFound),
      ]);
      const basedOn = latest?.hierarchy.version ?? 0;
      let result: Loaded;
      if (draft) result = { h: draft.hierarchy, etag: draft.etag, basedOn };
      else if (latest) result = { h: { ...latest.hierarchy, version: 0, status: "draft" }, etag: null, basedOn };
      else throw new ReportingApiError(404, "REPORT-0027", `Hierarchy ${code} has no draft and no published version.`);
      return { result, members };
    })().then(
      ({ result, members }) => {
        setLoaded(result);
        setH(result.h);
        setEtag(result.etag);
        setMembership(members);
        setDirty(false);
      },
      (error: unknown) => {
        if (!controller.signal.aborted) setLoadError(describeProblem(error));
      },
    );
    return () => controller.abort();
  }, [routeCode, isNew, attempt]);

  function change(patch: Partial<Hierarchy>) {
    setH((prev) => ({ ...prev, ...patch }));
    setDirty(true);
    setNotice(null);
  }
  function changeNode(i: number, patch: Partial<HierarchyNode>) {
    change({ nodes: h.nodes.map((n, j) => (j === i ? { ...n, ...patch } : n)) });
  }

  async function save(event?: FormEvent) {
    event?.preventDefault();
    if (!DEFINITION_CODE.test(h.code.trim())) {
      setCodeError("Use 2 to 30 upper-case letters, digits or hyphens, starting with a letter.");
      return;
    }
    setCodeError(undefined);
    setBusy("save");
    setFailure(null);
    try {
      const saved = await saveHierarchy(cleanHierarchy(h), etag);
      setEtag(saved.etag);
      setH(saved.hierarchy);
      setDirty(false);
      setNotice("Draft saved.");
      if (isNew) navigate(`/finreport/hierarchies/${encodeURIComponent(saved.hierarchy.code)}`, { replace: true });
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  async function publish(event: FormEvent) {
    event.preventDefault();
    const parsed = parseMembers(membersText);
    if (parsed.error) {
      setMembersError(parsed.error);
      return;
    }
    setMembersError(undefined);
    setBusy("publish");
    setFailure(null);
    try {
      const result = await publishHierarchy(h.code, parsed.members);
      const version = result.hierarchy.version;
      setLoaded({ h: { ...result.hierarchy, version: 0, status: "draft" }, etag: null, basedOn: version });
      setH({ ...result.hierarchy, version: 0, status: "draft" });
      setEtag(null);
      setMembership({ ...result.membership, version });
      const unassigned = result.membership.unassigned.length;
      setNotice(
        `Published version ${version}.${unassigned > 0 ? ` ${unassigned} member${unassigned === 1 ? "" : "s"} matched no leaf; the owner is notified.` : " Every member is assigned."}`,
      );
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  const title = isNew ? "New derived hierarchy" : `Derived hierarchy ${routeCode}`;
  if (loadError) {
    const missing = loadError.status === 404;
    return (
      <div className={styles.stack}>
        <PageHeader title={title} />
        <Alert
          tone="danger"
          title={missing ? "This hierarchy does not exist" : "This hierarchy could not be loaded"}
          live="alert"
          action={missing ? undefined : <Button size="sm" onClick={() => setAttempt((n) => n + 1)}>Try again</Button>}
        >
          {loadError.message} ({loadError.code})
        </Alert>
        <Link to="/finreport/hierarchies">Back to derived hierarchies</Link>
      </div>
    );
  }
  if (!loaded) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title={title} />
        <p className={styles.note}>Loading hierarchy…</p>
        <Skeleton width="full" height="tile" />
      </div>
    );
  }

  const hasDraft = etag !== null;
  const parents = new Set(h.nodes.map((n) => n.parent).filter(Boolean));
  const counts: Record<string, number> = {};
  for (const leaf of Object.values(membership?.assigned ?? {})) counts[leaf] = (counts[leaf] ?? 0) + 1;
  const leafRows: LeafRow[] = h.nodes.filter((n) => !parents.has(n.code)).map((n) => ({ code: n.code, label: n.label, members: counts[n.code] ?? 0 }));
  for (const code of Object.keys(counts)) {
    if (!leafRows.some((r) => r.code === code)) leafRows.push({ code, label: "(not in the current draft)", members: counts[code] });
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title={title}
        description={
          hasDraft
            ? `You are editing the draft. Publishing turns it into version ${loaded.basedOn + 1}.`
            : loaded.basedOn > 0
              ? `No draft is open. Saving starts a draft from published version ${loaded.basedOn}.`
              : "Saving creates the first draft of this hierarchy."
        }
        meta={
          <>
            {hasDraft ? <Badge tone="info">Draft</Badge> : null}
            {loaded.basedOn > 0 ? <Badge tone="success">{`v${loaded.basedOn} published`}</Badge> : null}
          </>
        }
      />
      <form className={styles.stack} onSubmit={save} noValidate>
        <Card title="Hierarchy">
          <div className={styles.form}>
            <FormField id="fr-h-code" label="Hierarchy code" hint={isNew ? "Upper-case letters, digits and hyphens, such as HIER-BS." : undefined} error={codeError}>
              {(c) => <Input {...c} className={styles.mono} value={h.code} readOnly={!isNew} invalid={Boolean(codeError)} onChange={(e) => change({ code: e.target.value.toUpperCase() })} />}
            </FormField>
            <FormField id="fr-h-name" label="Name">
              {(c) => <Input {...c} value={h.name} onChange={(e) => change({ name: e.target.value })} />}
            </FormField>
            <FormField id="fr-h-subject" label="Classifies">
              {(c) => (
                <Select {...c} value={h.subject} onChange={(e) => change({ subject: e.target.value as HierarchySubject })}>
                  {HIERARCHY_SUBJECTS.map((s) => (
                    <option key={s} value={s}>
                      {SUBJECT_LABELS[s]}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            {h.subject === "dimension_value" ? (
              <FormField id="fr-h-attr" label="Dimension" hint="The dimension whose values this hierarchy groups, such as COST_CENTER.">
                {(c) => <Input {...c} className={styles.mono} value={h.dimensionAttribute ?? ""} onChange={(e) => change({ dimensionAttribute: e.target.value.toUpperCase() })} />}
              </FormField>
            ) : null}
            <FormField id="fr-h-from" label="Effective from" optional>
              {(c) => <Input {...c} type="date" value={h.effectiveFrom ?? ""} onChange={(e) => change({ effectiveFrom: e.target.value })} />}
            </FormField>
            <span className={styles.check}>
              <Input id="fr-h-strict" type="checkbox" checked={h.strict} onChange={(e) => change({ strict: e.target.checked })} />
              <label htmlFor="fr-h-strict">Strict: refuse to publish while any member matches no leaf</label>
            </span>
          </div>
        </Card>

        <Card
          title={`Nodes (${h.nodes.length})`}
          actions={
            <Button size="sm" onClick={() => change({ nodes: [...h.nodes, { code: `N${h.nodes.length}`, label: "", parent: h.nodes[0]?.code, rules: [] }] })}>
              Add node
            </Button>
          }
        >
          <div className={styles.editorGrid}>
            <p className={styles.note}>
              Only leaves (nodes without children) carry rules. One rule per line: a range such as 100000..199999, values such as 61*, 6200 (a trailing * matches a prefix), or
              an attribute test such as region=EMEA.
            </p>
            {h.nodes.map((node, i) => {
              const isLeaf = !parents.has(node.code);
              return (
                <fieldset key={i} className={styles.fieldset}>
                  <legend className={styles.legend}>{`Node ${i + 1}${node.label ? `: ${node.label}` : ""}${isLeaf ? " (leaf)" : ""}`}</legend>
                  <div className={styles.editorLine}>
                    <FormField id={`fr-node-${i}-code`} label="Code">
                      {(c) => <Input {...c} className={styles.mono} value={node.code} onChange={(e) => changeNode(i, { code: e.target.value.toUpperCase() })} />}
                    </FormField>
                    <FormField id={`fr-node-${i}-label`} label="Label">
                      {(c) => <Input {...c} value={node.label} onChange={(e) => changeNode(i, { label: e.target.value })} />}
                    </FormField>
                    <FormField id={`fr-node-${i}-parent`} label="Parent">
                      {(c) => (
                        <Select {...c} value={node.parent ?? ""} onChange={(e) => changeNode(i, { parent: e.target.value || undefined })}>
                          <option value="">None (top level)</option>
                          {h.nodes
                            .filter((other, j) => j !== i && other.code !== "")
                            .map((other) => (
                              <option key={other.code} value={other.code}>
                                {other.code}
                              </option>
                            ))}
                        </Select>
                      )}
                    </FormField>
                    {isLeaf ? (
                      <FormField id={`fr-node-${i}-rules`} label="Membership rules" optional>
                        {(c) => (
                          <Textarea
                            {...c}
                            key={`${i}-${formatRules(node.rules)}`}
                            className={styles.mono}
                            rows={2}
                            defaultValue={formatRules(node.rules)}
                            onBlur={(e) => changeNode(i, { rules: parseRules(e.target.value) })}
                          />
                        )}
                      </FormField>
                    ) : null}
                    <div className={styles.lineActions}>
                      <Button
                        size="sm"
                        variant="ghost"
                        aria-label={`Remove node ${i + 1}`}
                        disabled={h.nodes.length === 1}
                        onClick={() => change({ nodes: h.nodes.filter((_, j) => j !== i) })}
                      >
                        Remove
                      </Button>
                    </div>
                  </div>
                </fieldset>
              );
            })}
          </div>
        </Card>

        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy === "save"}>
            Save draft
          </Button>
          {dirty ? <span className={styles.note}>Unsaved changes. Save the draft before publishing.</span> : null}
        </div>
      </form>

      <Card title="Publish">
        <form className={styles.form} onSubmit={publish} noValidate>
          <FormField
            id="fr-h-members"
            label="Members to classify"
            hint='One per line: id, key, then optional attribute=value pairs, such as "acct-110100, 110100, type=bank".'
            error={membersError}
          >
            {(c) => (
              <Textarea
                {...c}
                className={styles.mono}
                rows={6}
                value={membersText}
                invalid={Boolean(membersError)}
                onChange={(e) => setMembersText(e.target.value)}
              />
            )}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" loading={busy === "publish"} disabled={!hasDraft || dirty}>
              Publish
            </Button>
            {!hasDraft ? <span className={styles.note}>Save a draft to publish it.</span> : null}
          </div>
        </form>
      </Card>

      {notice ? (
        <Alert tone="success" live="status">
          {notice}
        </Alert>
      ) : null}
      {failure ? (
        <Alert
          tone="danger"
          title={
            failure.code === "REPORT-0013"
              ? "Some members match no leaf"
              : failure.code === "REPORT-0014"
                ? "A member matches two leaves"
                : failure.code === "conflict_version" || failure.code === "precondition_required"
                  ? "Someone else changed this draft"
                  : "That did not work"
          }
          live="alert"
          action={
            failure.code === "conflict_version" || failure.code === "precondition_required" ? (
              <Button size="sm" onClick={() => setAttempt((n) => n + 1)}>
                Reload the draft
              </Button>
            ) : undefined
          }
        >
          {failure.message} ({failure.code})
        </Alert>
      ) : null}

      <Card title={membership ? `Membership of version ${membership.version}` : "Membership"}>
        {membership ? (
          <div className={styles.stack}>
            <DataTable label="Members per leaf" columns={leafColumns} rows={leafRows} getRowKey={(r) => r.code} empty={{ title: "No leaves" }} />
            {membership.unassigned.length > 0 ? (
              <Alert tone="warning" title={`${membership.unassigned.length} unassigned`}>
                Shown last in statements as Unassigned: <span className={styles.mono}>{membership.unassigned.join(", ")}</span>
              </Alert>
            ) : (
              <p className={styles.note}>Every member is assigned to exactly one leaf.</p>
            )}
          </div>
        ) : (
          <p className={styles.note}>No published version yet. Publishing materializes which leaf each member belongs to.</p>
        )}
      </Card>
    </div>
  );
}
