import { useEffect, useMemo, useRef, useState } from "react";
import { Alert, Badge, Button, DataTable, EmptyState, FormField, Input, PageHeader, Select, Skeleton, StatusPill, type DataTableColumn } from "../../shell/ui";
import { ALL, describeProblem, listOperatingUnits, type Problem } from "./api/client";
import {
  compareHierarchyVersions,
  createHierarchyDraft,
  getHierarchyVersion,
  listHierarchyTypes,
  listHierarchyVersions,
  type HierarchyVersionSummary,
} from "./api/hierarchy";
import { HierarchyDraftEditor, ProblemAlert, type Candidate } from "./HierarchyDraftEditor";
import { HierarchyTreeView, type Label } from "./HierarchyTreeView";
import { useLegalEntity } from "./legalEntityContext";
import {
  buildTree,
  changeMap,
  dateFromDayNumber,
  dayNumber,
  hierarchyCsv,
  previousVersion,
  sliderRange,
  todayIso,
  versionAsOf,
  type NodeChange,
} from "./hierarchyTree";
import styles from "./pages.module.css";

interface LoadState<T> {
  key: string;
  data: T | null;
  loading: boolean;
  error: Problem | null;
}

const idle: LoadState<never> = { key: "", data: null, loading: false, error: null };

function useLoad<T>(key: string, load: (signal: AbortSignal) => Promise<T>): LoadState<T> {
  const [state, setState] = useState<LoadState<T>>(idle);
  const loader = useRef(load);
  loader.current = load;
  useEffect(() => {
    if (key === "") {
      return;
    }
    const controller = new AbortController();
    setState({ key, data: null, loading: true, error: null });
    loader.current(controller.signal).then(
      (data) => {
        if (!controller.signal.aborted) {
          setState({ key, data, loading: false, error: null });
        }
      },
      (failure) => {
        if (!controller.signal.aborted) {
          setState({ key, data: null, loading: false, error: describeProblem(failure) });
        }
      },
    );
    return () => controller.abort();
  }, [key]);
  if (key === "") {
    return idle;
  }
  return state.key === key ? state : { key, data: null, loading: true, error: null };
}

const rangeText = (version: HierarchyVersionSummary) =>
  version.effectiveFrom === null ? "Not yet effective" : `${version.effectiveFrom} to ${version.effectiveTo ?? "open"}`;

function downloadCsv(filename: string, csv: string) {
  const url = URL.createObjectURL(new Blob([csv], { type: "text/csv;charset=utf-8" }));
  const link = document.createElement("a");
  link.href = url;
  link.download = filename;
  link.click();
  URL.revokeObjectURL(url);
}

export function HierarchyDesignerPage() {
  const { entities } = useLegalEntity();
  const [typeId, setTypeId] = useState("");
  const [chosenDate, setChosenDate] = useState<string | null>(null);
  const [showDraft, setShowDraft] = useState(false);
  const [refresh, setRefresh] = useState(0);
  const [announcement, setAnnouncement] = useState("");
  const [notice, setNotice] = useState("");
  const [creating, setCreating] = useState(false);
  const [createFailure, setCreateFailure] = useState<Problem | null>(null);

  const types = useLoad("types", (signal) => listHierarchyTypes({}, { signal }));
  const units = useLoad("units", (signal) => listOperatingUnits(ALL, { signal }));
  const versionsLoad = useLoad(typeId === "" ? "" : `${typeId}#${refresh}`, (signal) => listHierarchyVersions(typeId, undefined, { signal }));
  const versions = versionsLoad.data ?? [];

  const labels = useMemo(() => {
    const map = new Map<string, Label>();
    for (const entity of entities) {
      map.set(entity.id, { name: entity.name, code: entity.code });
    }
    for (const unit of units.data?.items ?? []) {
      map.set(unit.id, { name: unit.name, code: unit.code });
    }
    return map;
  }, [entities, units.data]);

  const candidates = useMemo<Candidate[]>(
    () => [
      ...entities.map((entity) => ({ id: entity.id, name: entity.name, code: entity.code, kind: "legal_entity" as const })),
      ...(units.data?.items ?? [])
        .filter((unit) => unit.status !== "inactive")
        .map((unit) => ({ id: unit.id, name: unit.name, code: unit.code, kind: "operating_unit" as const })),
    ],
    [entities, units.data],
  );

  const draft = versions.find((version) => version.status === "draft");
  const defaultDate = versions.find((version) => version.status !== "draft" && version.effectiveFrom !== null)?.effectiveFrom ?? "";
  const asOf = chosenDate ?? defaultDate;
  const displayed = showDraft && draft ? draft : versionAsOf(versions, asOf);
  const previous = displayed ? previousVersion(versions, displayed) : undefined;

  const detail = useLoad(displayed ? `${displayed.id}#${refresh}` : "", (signal) => getHierarchyVersion(displayed?.id ?? "", { signal }));
  const comparison = useLoad(displayed && previous ? `${typeId}:${previous.versionNo}:${displayed.versionNo}#${refresh}` : "", (signal) =>
    compareHierarchyVersions(typeId, previous?.versionNo ?? 0, displayed?.versionNo ?? 0, { signal }),
  );
  const changes = useMemo(() => (comparison.data ? changeMap(comparison.data) : new Map<string, NodeChange>()), [comparison.data]);
  const removed = comparison.data?.removed ?? [];

  const selectedType = types.data?.find((type) => type.id === typeId);
  const range = sliderRange(versions, dayNumber(asOf) === null ? todayIso() : asOf);
  const rangeStart = dayNumber(range.min) ?? 0;
  const sliderValue = Math.min(Math.max((dayNumber(asOf) ?? rangeStart) - rangeStart, 0), (dayNumber(range.max) ?? rangeStart) - rangeStart);
  const nameOf = (id: string | null) => (id === null ? "no parent (root)" : (labels.get(id)?.name ?? id));

  const pickType = (id: string) => {
    setTypeId(id);
    setChosenDate(null);
    setShowDraft(false);
    setNotice("");
    setCreateFailure(null);
  };
  const pickDate = (value: string) => {
    if (dayNumber(value) !== null) {
      setChosenDate(value);
      setShowDraft(false);
    }
  };
  const pickVersion = (version: HierarchyVersionSummary) => {
    if (version.effectiveFrom === null) {
      setShowDraft(true);
    } else {
      setChosenDate(version.effectiveFrom);
      setShowDraft(false);
    }
  };
  const announce = (text: string) => {
    setNotice("");
    setAnnouncement(text);
  };
  const notify = (text: string) => {
    setAnnouncement("");
    setNotice(text);
  };
  const reload = () => setRefresh((count) => count + 1);

  const startDraft = async () => {
    setCreating(true);
    setCreateFailure(null);
    try {
      const created = await createHierarchyDraft(typeId);
      setShowDraft(true);
      notify(`Draft version ${created.versionNo} created`);
      reload();
    } catch (failure) {
      setCreateFailure(describeProblem(failure));
    } finally {
      setCreating(false);
    }
  };

  const exportCsv = () => {
    if (detail.data && displayed) {
      const csv = hierarchyCsv(buildTree(detail.data.nodes), (id) => labels.get(id) ?? { name: id, code: id });
      downloadCsv(`hierarchy-${selectedType?.code ?? typeId}-v${displayed.versionNo}.csv`, csv);
    }
  };

  const columns: DataTableColumn<HierarchyVersionSummary>[] = [
    { key: "versionNo", header: "Version", align: "end" },
    { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
    { key: "range", header: "Effective range", render: rangeText },
    { key: "nodeCount", header: "Nodes", align: "end" },
    {
      key: "view",
      header: "View",
      render: (row) => (
        <Button size="sm" aria-label={`View version ${row.versionNo}`} aria-pressed={displayed?.id === row.id} onClick={() => pickVersion(row)}>
          View
        </Button>
      ),
    },
  ];

  const banner = displayed
    ? displayed.effectiveFrom === null
      ? `Version ${displayed.versionNo} (draft), not yet effective`
      : `Version ${displayed.versionNo}, effective from ${displayed.effectiveFrom} to ${displayed.effectiveTo ?? "open"}`
    : "No version in force on this date";

  const detailData = detail.data;
  const editingDraft = displayed?.status === "draft" && detailData !== null;

  return (
    <>
      <PageHeader title="Hierarchies" description="Browse hierarchy versions as of a date, then edit the draft and publish it." />
      {types.error ? (
        <Alert tone="danger" title="Could not load hierarchy types" live="alert">
          {types.error.message} ({types.error.code})
        </Alert>
      ) : null}
      <div className={styles.filters}>
        <FormField id="hierarchy-type" label="Hierarchy type">
          {(control) => (
            <Select {...control} value={typeId} disabled={types.loading} onChange={(event) => pickType(event.target.value)}>
              <option value="">Select a hierarchy type</option>
              {(types.data ?? []).map((type) => (
                <option key={type.id} value={type.id}>
                  {type.code} · {type.name}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        {selectedType ? (
          <div className={styles.purposes}>
            {selectedType.purposes.map((purpose) => (
              <Badge key={purpose} tone="neutral">
                {purpose}
              </Badge>
            ))}
          </div>
        ) : null}
      </div>
      <div aria-live="polite">
        {notice ? <Alert tone="success">{notice}</Alert> : null}
        <span className="visually-hidden">{announcement}</span>
      </div>
      {typeId === "" ? null : versionsLoad.error ? (
        <Alert tone="danger" title="Could not load versions" live="alert">
          {versionsLoad.error.message} ({versionsLoad.error.code})
        </Alert>
      ) : versionsLoad.loading ? (
        <Skeleton height="tile" />
      ) : (
        <>
          {createFailure ? <ProblemAlert title="Could not create the draft" problem={createFailure} /> : null}
          {versions.length === 0 ? (
            <EmptyState
              title="No versions yet"
              description="Create a draft to start designing this hierarchy."
              action={
                <Button variant="primary" loading={creating} onClick={startDraft}>
                  Create draft
                </Button>
              }
            />
          ) : (
            <>
              {draft ? null : (
                <div className={styles.rowActions}>
                  <Button variant="primary" size="sm" loading={creating} onClick={startDraft}>
                    Create draft
                  </Button>
                  <span className={styles.editorHint}>Copies the latest published version so you can change it.</span>
                </div>
              )}
              <DataTable label="Hierarchy versions" columns={columns} rows={versions} getRowKey={(row) => row.id} />
              <div className={styles.sliderRow}>
                <FormField id="hierarchy-as-of" label="As of date">
                  {(control) => <Input {...control} type="date" value={asOf} onChange={(event) => pickDate(event.target.value)} />}
                </FormField>
                <FormField id="hierarchy-slider" label="Effective date slider">
                  {(control) => (
                    <input
                      {...control}
                      type="range"
                      min={0}
                      max={(dayNumber(range.max) ?? rangeStart) - rangeStart}
                      value={sliderValue}
                      aria-valuetext={asOf || "No date"}
                      onChange={(event) => pickDate(dateFromDayNumber(rangeStart + Number(event.target.value)))}
                    />
                  )}
                </FormField>
                {draft ? (
                  <label className={styles.filter}>
                    <Input type="checkbox" checked={showDraft} onChange={(event) => setShowDraft(event.target.checked)} />
                    <span>Show draft</span>
                  </label>
                ) : null}
              </div>
              <div className={styles.bannerRow}>
                <div role="status" className={styles.banner}>
                  {banner}
                </div>
                {detailData && displayed ? (
                  <Button size="sm" onClick={exportCsv}>
                    Export CSV
                  </Button>
                ) : null}
              </div>
              {displayed ? (
                <>
                  {comparison.error ? (
                    <Alert tone="danger" title="Could not load changes" live="alert">
                      {comparison.error.message} ({comparison.error.code})
                    </Alert>
                  ) : null}
                  {units.error && editingDraft ? (
                    <Alert tone="warning" title="Operating units did not load" live="alert">
                      Names show as ids and operating units cannot be added until this is fixed. {units.error.message} ({units.error.code})
                    </Alert>
                  ) : null}
                  {detail.error ? (
                    <Alert tone="danger" title="Could not load version" live="alert">
                      {detail.error.message} ({detail.error.code})
                    </Alert>
                  ) : detailData === null ? (
                    <Skeleton height="tile" />
                  ) : editingDraft ? (
                    <HierarchyDraftEditor
                      key={`${detailData.id}:${detailData.version}`}
                      version={detailData}
                      type={selectedType}
                      labels={labels}
                      candidates={candidates}
                      changes={changes}
                      announce={announce}
                      notify={notify}
                      onReload={reload}
                      onPublished={(effectiveFrom) => {
                        setShowDraft(false);
                        setChosenDate(effectiveFrom);
                        notify(`Version ${detailData.versionNo} published, effective from ${effectiveFrom}`);
                        reload();
                      }}
                    />
                  ) : (
                    <HierarchyTreeView key={detailData.id} nodes={detailData.nodes} labels={labels} changes={changes} />
                  )}
                  {previous && removed.length > 0 ? (
                    <section aria-labelledby="hierarchy-removed" className={styles.removed}>
                      <h3 id="hierarchy-removed" className={styles.removedTitle}>
                        Removed since v{previous.versionNo}
                      </h3>
                      <ul className={styles.removedList}>
                        {removed.map((entry) => (
                          <li key={entry.orgUnitId} className={styles.removedItem}>
                            <span className={styles.treeName}>{nameOf(entry.orgUnitId)}</span>
                            {labels.get(entry.orgUnitId) ? <span className={styles.treeCode}>{labels.get(entry.orgUnitId)?.code}</span> : null}
                            <span className={styles.treeCount}>was under {nameOf(entry.parentOrgUnitId)}</span>
                          </li>
                        ))}
                      </ul>
                    </section>
                  ) : null}
                </>
              ) : null}
            </>
          )}
        </>
      )}
    </>
  );
}
