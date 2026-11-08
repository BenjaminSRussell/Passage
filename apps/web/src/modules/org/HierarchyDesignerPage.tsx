import { useEffect, useMemo, useRef, useState } from "react";
import { useBlocker } from "react-router-dom";
import { Alert, Badge, Button, ConfirmDialog, DataTable, EmptyState, FormField, Input, PageHeader, Select, Skeleton, StatusPill, type DataTableColumn } from "../../shell/ui";
import { ALL, describeProblem, listOperatingUnits, type Problem } from "./api/client";
import {
  compareHierarchyVersions,
  createHierarchyDraft,
  getHierarchyVersion,
  listHierarchyTypes,
  listHierarchyVersions,
  type HierarchyVersionSummary,
} from "./api/hierarchy";
import { CHART_WARN_NODES, chartSize, hierarchyChartSvg } from "./hierarchyChart";
import { HierarchyDraftEditor, ProblemAlert, type Candidate } from "./HierarchyDraftEditor";
import { HierarchyTreeView, RemovedSection, type Label } from "./HierarchyTreeView";
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

function useLoad<T>(key: string, load: (signal: AbortSignal) => Promise<T>, refresh = 0): LoadState<T> {
  const [state, setState] = useState<LoadState<T>>(idle);
  const loader = useRef(load);
  loader.current = load;
  useEffect(() => {
    if (key === "") {
      return;
    }
    const controller = new AbortController();
    setState((previous) => (previous.key === key ? { ...previous, loading: true, error: null } : { key, data: null, loading: true, error: null }));
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
  }, [key, refresh]);
  if (key === "") {
    return idle;
  }
  return state.key === key ? state : { key, data: null, loading: true, error: null };
}

const rangeText = (version: HierarchyVersionSummary) =>
  version.effectiveFrom === null ? "Not yet effective" : `${version.effectiveFrom} to ${version.effectiveTo ?? "open"}`;

function downloadFile(filename: string, parts: BlobPart[], type: string) {
  const url = URL.createObjectURL(new Blob(parts, { type }));
  const link = document.createElement("a");
  link.href = url;
  link.download = filename;
  document.body.appendChild(link);
  link.click();
  link.remove();
  setTimeout(() => URL.revokeObjectURL(url), 0);
}

const downloadCsv = (filename: string, csv: string) => downloadFile(filename, ["\ufeff", csv], "text/csv;charset=utf-8");

// Type codes are A-Z, 0-9, _ and - today; anything else would be a path separator or space in the saved file name.
const fileSafe = (text: string) => text.replace(/[^A-Za-z0-9_-]/g, "_");

export function HierarchyDesignerPage() {
  const { entities, canWriteHierarchy, canPublishHierarchy, canBackdateHierarchy } = useLegalEntity();
  const [dirty, setDirty] = useState(false);
  const [pending, setPending] = useState<(() => void) | null>(null);
  const noticeRef = useRef<HTMLDivElement>(null);
  const blocker = useBlocker(dirty);
  const [typeId, setTypeId] = useState("");
  const [chosenDate, setChosenDate] = useState<string | null>(null);
  const [showDraft, setShowDraft] = useState(false);
  const [refresh, setRefresh] = useState(0);
  const [announcement, setAnnouncement] = useState("");
  const [notice, setNotice] = useState("");
  const [creating, setCreating] = useState(false);
  const [createFailure, setCreateFailure] = useState<Problem | null>(null);
  const [largeChart, setLargeChart] = useState<{ nodes: number; width: number; height: number } | null>(null);

  const types = useLoad("types", (signal) => listHierarchyTypes({}, { signal }));
  const units = useLoad("units", (signal) => listOperatingUnits(ALL, { signal }));
  const versionsLoad = useLoad(typeId, (signal) => listHierarchyVersions(typeId, undefined, { signal }), refresh);
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

  const editable = canWriteHierarchy && displayed?.status === "draft";
  const detail = useLoad(displayed ? displayed.id : "", (signal) => getHierarchyVersion(displayed?.id ?? "", { signal }), refresh);
  const comparison = useLoad(
    displayed && previous ? `${typeId}:${previous.versionNo}:${displayed.versionNo}` : "",
    (signal) => compareHierarchyVersions(typeId, previous?.versionNo ?? 0, displayed?.versionNo ?? 0, { signal }),
    refresh,
  );
  const previousDetail = useLoad(editable && previous ? previous.id : "", (signal) => getHierarchyVersion(previous?.id ?? "", { signal }));
  const changes = useMemo(() => (comparison.data ? changeMap(comparison.data) : new Map<string, NodeChange>()), [comparison.data]);
  const removed = comparison.data?.removed ?? [];

  const selectedType = types.data?.find((type) => type.id === typeId);
  const range = sliderRange(versions, dayNumber(asOf) === null ? todayIso() : asOf);
  const rangeStart = dayNumber(range.min) ?? 0;
  const sliderValue = Math.min(Math.max((dayNumber(asOf) ?? rangeStart) - rangeStart, 0), (dayNumber(range.max) ?? rangeStart) - rangeStart);

  const guard = (action: () => void) => (dirty ? setPending(() => action) : action());
  const confirmLeave = () => {
    if (blocker.state === "blocked") {
      blocker.proceed();
    } else {
      pending?.();
      setPending(null);
    }
  };
  const keepEditing = () => {
    if (blocker.state === "blocked") {
      blocker.reset();
    }
    setPending(null);
  };

  useEffect(() => {
    if (!dirty) {
      return;
    }
    const warn = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);

  useEffect(() => {
    if (notice !== "") {
      noticeRef.current?.focus();
    }
  }, [notice]);

  const pickType = (id: string) =>
    guard(() => {
      setTypeId(id);
      setChosenDate(null);
      setShowDraft(false);
      setNotice("");
      setCreateFailure(null);
    });
  const pickDate = (value: string) => {
    if (dayNumber(value) !== null) {
      guard(() => {
        setChosenDate(value);
        setShowDraft(false);
      });
    }
  };
  const pickVersion = (version: HierarchyVersionSummary) =>
    guard(() => {
      if (version.effectiveFrom === null) {
        setShowDraft(true);
      } else {
        setChosenDate(version.effectiveFrom);
        setShowDraft(false);
      }
    });
  const toggleDraft = (checked: boolean) => guard(() => setShowDraft(checked));
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

  // The unit carries only managerPartyId until party names can be resolved here, so the chart says whether a manager is
  // assigned rather than printing an internal id; legal entities have no manager and get no manager line.
  const exportSvg = (confirmed = false) => {
    if (!detail.data || !displayed) {
      return;
    }
    const roots = buildTree(detail.data.nodes);
    if (!confirmed && detail.data.nodes.length > CHART_WARN_NODES) {
      setLargeChart({ nodes: detail.data.nodes.length, ...chartSize(roots) });
      return;
    }
    setLargeChart(null);
    const managers = new Map<string, string | null>((units.data?.items ?? []).map((unit) => [unit.id, unit.managerPartyId ? "assigned" : null]));
    const typeCode = selectedType?.code ?? typeId;
    const svg = hierarchyChartSvg(
      roots,
      (id) => ({ ...(labels.get(id) ?? { name: id, code: id }), manager: managers.get(id) }),
      { typeCode, typeName: selectedType?.name ?? "", purposes: selectedType?.purposes ?? [], asOf: displayed.effectiveFrom === null ? "draft" : asOf },
      { versionNo: displayed.versionNo, printedAt: new Date().toISOString() },
    );
    downloadFile(`org-chart-${fileSafe(typeCode)}-v${displayed.versionNo}.svg`, [svg], "image/svg+xml;charset=utf-8");
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
  const editingDraft = editable && detailData !== null;

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
      <div aria-live="polite" ref={noticeRef} tabIndex={-1} className={styles.notice}>
        {notice ? <Alert tone="success">{notice}</Alert> : null}
        <span className="visually-hidden">{announcement}</span>
      </div>
      {typeId === "" ? null : versionsLoad.error ? (
        <Alert tone="danger" title="Could not load versions" live="alert">
          {versionsLoad.error.message} ({versionsLoad.error.code})
        </Alert>
      ) : versionsLoad.loading && versionsLoad.data === null ? (
        <Skeleton height="tile" />
      ) : (
        <>
          {createFailure ? <ProblemAlert title="Could not create the draft" problem={createFailure} /> : null}
          {versions.length === 0 ? (
            <EmptyState
              title="No versions yet"
              description={canWriteHierarchy ? "Create a draft to start designing this hierarchy." : "A draft needs the hierarchy write duty. Ask your workspace administrator for access."}
              action={
                canWriteHierarchy ? (
                  <Button variant="primary" loading={creating} onClick={startDraft}>
                    Create draft
                  </Button>
                ) : undefined
              }
            />
          ) : (
            <>
              {draft || !canWriteHierarchy ? null : (
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
                    <Input type="checkbox" checked={showDraft} onChange={(event) => toggleDraft(event.target.checked)} />
                    <span>Show draft</span>
                  </label>
                ) : null}
              </div>
              <div className={styles.bannerRow}>
                <div role="status" className={styles.banner}>
                  {banner}
                </div>
                {detailData && displayed ? (
                  <>
                    <Button size="sm" onClick={exportCsv}>
                      Export CSV
                    </Button>
                    <Button size="sm" onClick={() => exportSvg()}>
                      Export chart (SVG)
                    </Button>
                  </>
                ) : null}
              </div>
              {displayed ? (
                <>
                  {comparison.error ? (
                    <Alert tone="danger" title="Could not load changes" live="alert">
                      {comparison.error.message} ({comparison.error.code})
                    </Alert>
                  ) : null}
                  {displayed.status === "draft" && !canWriteHierarchy ? (
                    <Alert tone="info" title="This draft is read-only for you">
                      Editing a draft needs the hierarchy write duty.
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
                      key={detailData.id}
                      version={detailData}
                      type={selectedType}
                      labels={labels}
                      candidates={candidates}
                      canPublish={canPublishHierarchy}
                      canBackdate={canBackdateHierarchy}
                      previousVersionNo={previous?.versionNo ?? null}
                      previousNodes={previousDetail.data?.nodes ?? null}
                      serverChanges={changes}
                      serverRemoved={removed}
                      announce={announce}
                      notify={notify}
                      onDirtyChange={setDirty}
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
                  {previous && !editingDraft ? <RemovedSection since={previous.versionNo} removed={removed} labels={labels} /> : null}
                </>
              ) : null}
            </>
          )}
        </>
      )}
      {pending !== null || blocker.state === "blocked" ? (
        <ConfirmDialog
          title="Discard your unsaved changes?"
          description="You have unsaved edits to this draft. Leaving now loses them."
          confirmLabel="Discard changes"
          cancelLabel="Keep editing"
          onConfirm={confirmLeave}
          onCancel={keepEditing}
        />
      ) : null}
      {largeChart ? (
        <ConfirmDialog
          title="Export a very large chart?"
          description={`This version has ${largeChart.nodes.toLocaleString("en")} nodes, so the chart is about ${largeChart.width.toLocaleString("en")} by ${largeChart.height.toLocaleString("en")} pixels. It will not fit a printed page and may be slow to open. The CSV export suits large hierarchies better.`}
          confirmLabel="Export anyway"
          cancelLabel="Cancel"
          onConfirm={() => exportSvg(true)}
          onCancel={() => setLargeChart(null)}
        />
      ) : null}
    </>
  );
}
