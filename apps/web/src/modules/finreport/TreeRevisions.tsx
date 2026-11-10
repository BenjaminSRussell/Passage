import { useState } from "react";
import { Alert, Badge, Button, Card, DataTable, Skeleton, type DataTableColumn } from "../../shell/ui";
import { getRunTree, getTreeRevision, listTreeRevisions, type Tree, type TreeRevision, type TreeUnit } from "./api/client";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

/** The unit and every unit below it. */
export function unitAndBelow(tree: Tree, unit: string): Set<string> {
  const out = new Set<string>([unit]);
  let grew = true;
  while (grew) {
    grew = false;
    for (const u of tree.units) {
      if (u.parent && out.has(u.parent) && !out.has(u.code)) {
        out.add(u.code);
        grew = true;
      }
    }
  }
  return out;
}

/** The units of a tree as a read-only table; marked units are the ones a run covered. */
function UnitsTable({ tree, covered }: { tree: Tree; covered?: Set<string> }) {
  const columns: DataTableColumn<TreeUnit>[] = [
    { key: "code", header: "Unit", mono: true },
    { key: "name", header: "Name" },
    { key: "parent", header: "Parent", mono: true, render: (u) => u.parent ?? "" },
    { key: "values", header: `Values of ${tree.attribute}`, mono: true, render: (u) => (u.values ?? []).join(", ") },
    ...(covered ? [{ key: "covered", header: "In this run", render: (u: TreeUnit) => (covered.has(u.code) ? <Badge tone="info">Covered</Badge> : "") }] : []),
  ];
  return <DataTable label={`Units of ${tree.code}`} columns={columns} rows={tree.units} getRowKey={(u) => u.code} empty={{ title: "This revision lists no units" }} />;
}

/**
 * The revisions a reporting tree went through (FR-15.4). A statement run records
 * the revision it used; opening that revision shows the units behind the run.
 */
export function TreeRevisionsCard({ code }: { code: string }) {
  const list = useLoad((signal) => listTreeRevisions(code, signal), code);
  const [open, setOpen] = useState<number | null>(null);
  const shown = useLoad((signal) => (open === null ? Promise.resolve(null) : getTreeRevision(code, open, signal)), `${code}/${open}`);

  const columns: DataTableColumn<TreeRevision>[] = [
    { key: "revision", header: "Revision", mono: true },
    { key: "updatedBy", header: "Saved by" },
    { key: "createdAt", header: "Saved", render: (r) => <time dateTime={r.createdAt}>{r.createdAt}</time> },
    {
      key: "view",
      header: "Units",
      render: (r) => (
        <Button size="sm" variant="ghost" aria-pressed={open === r.revision} onClick={() => setOpen(open === r.revision ? null : r.revision)} aria-label={`Show the units of revision ${r.revision}`}>
          {open === r.revision ? "Hide" : "Show"}
        </Button>
      ),
    },
  ];
  return (
    <>
      <Card title="Revisions">
        <p className={styles.note}>Every save keeps its revision, so a statement run made on an older revision can still show its units.</p>
        {list.error ? (
          <p className={styles.note}>
            Revisions are unavailable: {list.error.message} ({list.error.code}){" "}
            <Button size="sm" onClick={list.reload}>
              Try again
            </Button>
          </p>
        ) : (
          <DataTable
            label="Tree revisions"
            columns={columns}
            rows={list.data ?? []}
            getRowKey={(r) => String(r.revision)}
            loading={list.loading && !list.data}
            empty={{ title: "No revisions stored", description: "Save the tree to keep its first revision." }}
          />
        )}
      </Card>
      {open !== null ? (
        <Card title={`Units at revision ${open}`}>
          {shown.error ? (
            <Alert tone="danger" title="This revision is unavailable" live="alert">
              <ProblemMessage problem={shown.error} />
            </Alert>
          ) : shown.data ? (
            <UnitsTable tree={shown.data} />
          ) : (
            <Skeleton width="full" height="tile" />
          )}
        </Card>
      ) : null}
    </>
  );
}

/**
 * The tree behind a statement run, as it was when the run was made: the run's
 * unit and the units it rolled up. A revision the service did not keep is said
 * so, not shown as an error of the statement.
 */
export function RunTreeCard({ runId }: { runId: string }) {
  const view = useLoad((signal) => getRunTree(runId, signal), runId);
  if (view.error) {
    return (
      <Card title="Reporting tree">
        {view.error.status === 404 ? (
          <p className={styles.note}>The tree revision this run used was not kept, so its units cannot be shown.</p>
        ) : (
          <Alert tone="danger" title="The tree is unavailable" live="alert" action={<Button size="sm" onClick={view.reload}>Try again</Button>}>
            <ProblemMessage problem={view.error} />
          </Alert>
        )}
      </Card>
    );
  }
  if (!view.data) {
    return (
      <Card title="Reporting tree">
        <Skeleton width="full" height="tile" />
      </Card>
    );
  }
  const { data } = view;
  const covered = data.unit ? unitAndBelow(data.tree, data.unit) : new Set(data.tree.units.map((u) => u.code));
  return (
    <Card title={`Reporting tree ${data.code}, revision ${data.revision}`}>
      {data.currentRevision > data.revision ? (
        <p className={styles.note}>
          The tree has changed since this run (now revision {data.currentRevision}). These are the units the run used.
        </p>
      ) : null}
      <UnitsTable tree={data.tree} covered={covered} />
    </Card>
  );
}
