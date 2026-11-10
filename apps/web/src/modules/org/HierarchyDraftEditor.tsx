import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { Alert, Badge, Button, ConfirmDialog, EmptyState, FormField, Input, Select, Textarea } from "../../shell/ui";
import { describeProblem, type Problem } from "./api/client";
import {
  publishHierarchyVersion,
  replaceHierarchyNodes,
  type HierarchyChange,
  type HierarchyNode,
  type HierarchyNodeKind,
  type HierarchyType,
  type HierarchyVersion,
} from "./api/hierarchy";
import {
  addNode,
  dayNumber,
  diffNodes,
  indentNode,
  moveNode,
  outdentNode,
  parentChoices,
  removeNode,
  replaceNode,
  shiftNode,
  siblingIds,
  todayIso,
  treeSignature,
  type NodeChange,
} from "./hierarchyTree";
import { HierarchyTreeView, RemovedSection, type Label, type MoveCommand } from "./HierarchyTreeView";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

export interface Candidate {
  id: string;
  name: string;
  code: string;
  kind: HierarchyNodeKind;
}

const REASON_MIN = 10;
const REASON_MAX = 500;

export function ProblemAlert({ title, problem, onReload }: { title: string; problem: Problem; onReload?: () => void }) {
  const denied = problem.code.startsWith("AUTH-");
  const stale = problem.code === "ORG-VERSION-CONFLICT" || problem.code === "ORG-HIER-VERSION-IMMUTABLE";
  return (
    <Alert
      tone="danger"
      live="alert"
      title={denied ? "You don't have permission to do this" : stale ? "This draft changed elsewhere" : title}
      action={stale && onReload ? <Button size="sm" onClick={onReload}>Reload draft</Button> : undefined}
    >
      <ProblemMessage problem={problem} />
    </Alert>
  );
}

interface PickerOption {
  value: string;
  label: string;
}

interface PickerFormProps {
  id: string;
  label: string;
  placeholder: string;
  options: PickerOption[];
  hint?: string;
  actionLabel: string;
  disabled?: boolean;
  onSubmit(value: string): void;
}

function PickerForm({ id, label, placeholder, options, hint, actionLabel, disabled = false, onSubmit }: PickerFormProps) {
  const [value, setValue] = useState("");
  const submit = () => {
    if (value !== "") {
      onSubmit(value);
      setValue("");
    }
  };
  return (
    <div className={styles.pickerForm}>
      <FormField id={id} label={label} hint={hint}>
        {(control) => (
          <Select {...control} value={value} disabled={disabled} onChange={(event) => setValue(event.target.value)}>
            <option value="">{placeholder}</option>
            {options.map((option) => (
              <option key={option.value} value={option.value}>
                {option.label}
              </option>
            ))}
          </Select>
        )}
      </FormField>
      <Button disabled={disabled || value === ""} onClick={submit}>
        {actionLabel}
      </Button>
    </div>
  );
}

interface PublishPanelProps {
  versionId: string;
  versionNo: number;
  lock: number;
  type: HierarchyType | undefined;
  canBackdate: boolean;
  reason: string;
  onReasonChange(reason: string): void;
  onBackdateChange(backdated: boolean): void;
  onPublished(effectiveFrom: string): void;
}

function PublishPanel({ versionId, versionNo, lock, type, canBackdate, reason, onReasonChange, onBackdateChange, onPublished }: PublishPanelProps) {
  const [effectiveFrom, setEffectiveFrom] = useState(todayIso);
  const [submitted, setSubmitted] = useState(false);
  const [publishing, setPublishing] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);

  const validDate = dayNumber(effectiveFrom) !== null;
  const backdated = validDate && effectiveFrom < todayIso();
  useEffect(() => {
    onBackdateChange(backdated);
    return () => onBackdateChange(false);
  }, [backdated, onBackdateChange]);
  const reasonLength = [...reason.trim()].length;
  const dateError = !validDate
    ? "Enter a valid date."
    : backdated && type?.backdateAllowed === false
      ? "This hierarchy type does not allow backdating. Choose today or a later date."
      : backdated && !canBackdate
        ? "Publishing a past date needs the backdate duty. Choose today or a later date."
        : undefined;
  const reasonError = backdated && (reasonLength < REASON_MIN || reasonLength > REASON_MAX) ? `Give a reason of ${REASON_MIN} to ${REASON_MAX} characters.` : undefined;

  const submit = async () => {
    setSubmitted(true);
    if (dateError || reasonError) {
      return;
    }
    setPublishing(true);
    setFailure(null);
    try {
      await publishHierarchyVersion(versionId, { version: lock, effectiveFrom, reason: backdated ? reason.trim() : "" });
      onPublished(effectiveFrom);
    } catch (error) {
      setFailure(describeProblem(error));
      setPublishing(false);
    }
  };

  return (
    <section aria-label="Publish draft" className={styles.editorGroup}>
      <div className={styles.publishFields}>
        <FormField id="publish-effective-from" label="Effective from" hint={`Dates are in UTC. Today is ${todayIso()}.`} error={submitted ? dateError : undefined}>
          {(control) => <Input {...control} type="date" value={effectiveFrom} onChange={(event) => setEffectiveFrom(event.target.value)} />}
        </FormField>
        {backdated ? (
          <FormField
            id="publish-reason"
            label="Backdate reason"
            hint={`Required because this date is in the past. ${REASON_MIN} to ${REASON_MAX} characters.`}
            error={submitted ? reasonError : undefined}
          >
            {(control) => <Textarea {...control} rows={3} required value={reason} onChange={(event) => onReasonChange(event.target.value)} />}
          </FormField>
        ) : null}
      </div>
      {failure ? <ProblemAlert title="Could not publish the draft" problem={failure} /> : null}
      <div className={styles.rowActions}>
        <Button variant="primary" loading={publishing} onClick={submit}>
          Publish version {versionNo}
        </Button>
      </div>
    </section>
  );
}

const operationsFor = (list: HierarchyNode[]): Record<MoveCommand, (id: string) => HierarchyNode[] | null> => ({
  up: (id) => shiftNode(list, id, -1),
  down: (id) => shiftNode(list, id, 1),
  indent: (id) => indentNode(list, id),
  outdent: (id) => outdentNode(list, id),
});

interface DraftEditorProps {
  version: HierarchyVersion;
  type: HierarchyType | undefined;
  labels: ReadonlyMap<string, Label>;
  candidates: Candidate[];
  canPublish: boolean;
  canBackdate: boolean;
  previousVersionNo: number | null;
  previousNodes: HierarchyNode[] | null;
  serverChanges: ReadonlyMap<string, NodeChange>;
  serverRemoved: readonly HierarchyChange[];
  announce(text: string): void;
  notify(text: string): void;
  onDirtyChange(dirty: boolean): void;
  onReload(): void;
  onPublished(effectiveFrom: string): void;
}

export function HierarchyDraftEditor({
  version,
  type,
  labels,
  candidates,
  canPublish,
  canBackdate,
  previousVersionNo,
  previousNodes,
  serverChanges,
  serverRemoved,
  announce,
  notify,
  onDirtyChange,
  onReload,
  onPublished,
}: DraftEditorProps) {
  const [base, setBase] = useState({ source: version.nodes, nodes: version.nodes, lock: version.version });
  const [nodes, setNodes] = useState<HierarchyNode[]>(version.nodes);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [focusSignal, setFocusSignal] = useState(0);
  const [saving, setSaving] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [publishOpen, setPublishOpen] = useState(false);
  const [publishReason, setPublishReason] = useState("");
  const [publishBackdated, setPublishBackdated] = useState(false);
  const [confirmDiscard, setConfirmDiscard] = useState(false);
  const discardOnReload = useRef(false);

  const dirty = useMemo(() => treeSignature(nodes) !== treeSignature(base.nodes), [nodes, base.nodes]);

  if (base.source !== version.nodes) {
    setBase({ source: version.nodes, nodes: version.nodes, lock: version.version });
    if (discardOnReload.current || !dirty) {
      setNodes(version.nodes);
    }
    discardOnReload.current = false;
  }

  useEffect(() => {
    onDirtyChange(dirty);
  }, [dirty, onDirtyChange]);
  useEffect(() => () => onDirtyChange(false), [onDirtyChange]);

  const nameOf = (id: string) => labels.get(id)?.name ?? id;
  const optionLabel = (id: string) => {
    const label = labels.get(id);
    return label ? `${label.name} (${label.code})` : id;
  };

  const commit = (next: HierarchyNode[] | null, message: (updated: HierarchyNode[]) => string) => {
    if (next === null || saving) {
      return;
    }
    setNodes(next);
    setFailure(null);
    announce(message(next));
  };

  const placement = (id: string) => (updated: HierarchyNode[]) => {
    const parentId = updated.find((node) => node.orgUnitId === id)?.parentOrgUnitId ?? null;
    const siblings = siblingIds(updated, parentId);
    return `${nameOf(id)} is now under ${parentId === null ? "no parent" : nameOf(parentId)}, position ${siblings.indexOf(id) + 1} of ${siblings.length}`;
  };

  const availability = useMemo(() => {
    const operations = operationsFor(nodes);
    const check = (command: MoveCommand) => selectedId !== null && operations[command](selectedId) !== null;
    return { up: check("up"), down: check("down"), indent: check("indent"), outdent: check("outdent") };
  }, [nodes, selectedId]);
  const available = (command: MoveCommand) => !saving && availability[command];
  const removable = useMemo(() => selectedId !== null && removeNode(nodes, selectedId) !== null, [nodes, selectedId]);
  const focusTree = () => setFocusSignal((signal) => signal + 1);

  const move = (command: MoveCommand, refocus: boolean) => {
    if (selectedId === null) {
      return;
    }
    const next = operationsFor(nodes)[command](selectedId);
    commit(next, placement(selectedId));
    if (next && (refocus || operationsFor(next)[command](selectedId) === null)) {
      focusTree();
    }
  };

  const moveUnder = (id: string, parentId: string) => {
    commit(moveNode(nodes, id, parentId), placement(id));
    focusTree();
  };

  const remove = () => {
    if (selectedId === null || saving) {
      return;
    }
    const parentId = nodes.find((node) => node.orgUnitId === selectedId)?.parentOrgUnitId ?? null;
    const adopted = siblingIds(nodes, selectedId).length;
    const name = nameOf(selectedId);
    commit(removeNode(nodes, selectedId), () =>
      adopted === 0 ? `${name} removed` : `${name} removed, its ${adopted === 1 ? "child" : `${adopted} children`} moved up under ${nameOf(parentId ?? "")}`,
    );
    setSelectedId(parentId);
    focusTree();
  };

  const free = useMemo(() => {
    const present = new Set(nodes.map((node) => node.orgUnitId));
    return candidates.filter((candidate) => !present.has(candidate.id));
  }, [candidates, nodes]);
  const freeOptions = useMemo(() => free.map((candidate) => ({ value: candidate.id, label: `${candidate.name} (${candidate.code})` })), [free]);
  const candidate = (id: string) => free.find((entry) => entry.id === id);
  const moveOptions = useMemo(
    () => (selectedId === null ? [] : parentChoices(nodes, selectedId)).map((id) => ({ value: id, label: optionLabel(id) })),
    [nodes, selectedId, labels],
  );
  const rootless = nodes.length === 0;

  const add = (id: string) => {
    const chosen = candidate(id);
    if (chosen) {
      commit(addNode(nodes, id, chosen.kind, rootless ? null : selectedId), () => `${chosen.name} added${rootless ? " as the root" : ` under ${nameOf(selectedId ?? "")}`}`);
      if (rootless) {
        setSelectedId(id);
      }
      focusTree();
    }
  };
  const replace = (id: string) => {
    const chosen = candidate(id);
    if (chosen && selectedId !== null) {
      const previous = nameOf(selectedId);
      commit(replaceNode(nodes, selectedId, id, chosen.kind), () => `${previous} replaced by ${chosen.name}`);
      setSelectedId(id);
      focusTree();
    }
  };

  const save = async () => {
    setSaving(true);
    setFailure(null);
    try {
      const saved = await replaceHierarchyNodes(version.id, base.lock, nodes);
      setBase((current) => ({ ...current, nodes: saved.nodes, lock: saved.version }));
      setNodes(saved.nodes);
      setSaving(false);
      notify(`Draft version ${version.versionNo} saved`);
      onReload();
    } catch (error) {
      setFailure(describeProblem(error));
      setSaving(false);
    }
  };

  const discard = () => {
    if (saving) return;
    setNodes(base.nodes);
    setFailure(null);
    setSelectedId(base.nodes.some((node) => node.orgUnitId === selectedId) ? selectedId : (base.nodes.find((node) => node.parentOrgUnitId === null)?.orgUnitId ?? null));
    focusTree();
    setConfirmDiscard(false);
    announce("Changes discarded");
  };

  const reloadDraft = () => {
    discardOnReload.current = true;
    onReload();
  };

  const onKeyDown = (event: KeyboardEvent) => {
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "s") {
      event.preventDefault();
      if (dirty && !saving && !confirmDiscard) {
        void save();
      }
    } else if (event.key === "Escape") {
      if (publishOpen) {
        if (!publishBackdated || publishReason === "") {
          setPublishOpen(false);
        }
      } else if (dirty && !saving) {
        setConfirmDiscard(true);
      }
    }
  };

  const diff = useMemo(() => (previousNodes ? diffNodes(previousNodes, nodes) : null), [previousNodes, nodes]);
  const selectionHint =
    selectedId === null ? "Select a node to move or replace it. Drag a node onto its new parent, or use the buttons." : `Selected: ${nameOf(selectedId)}`;

  return (
    <div onKeyDown={onKeyDown}>
      <section aria-label="Edit draft" className={styles.editor}>
        <div className={styles.editorHeader}>
          <Badge tone={dirty ? "warning" : "success"}>{dirty ? "Unsaved changes" : "No unsaved changes"}</Badge>
          <div className={styles.rowActions}>
            <Button variant="ghost" size="sm" disabled={!dirty || saving} onClick={discard}>
              Discard changes
            </Button>
            <Button variant="primary" size="sm" loading={saving} disabled={!dirty} onClick={save}>
              Save draft
            </Button>
            {canPublish ? (
              <Button size="sm" aria-expanded={publishOpen} disabled={dirty || rootless} onClick={() => setPublishOpen((open) => !open)}>
                Publish…
              </Button>
            ) : null}
          </div>
        </div>
        {dirty && canPublish ? <p className={styles.editorHint}>Save your changes before publishing.</p> : null}
        {failure ? <ProblemAlert title="Could not save the draft" problem={failure} onReload={reloadDraft} /> : null}
        <div role="group" aria-label="Move selected node" className={styles.editorGroup}>
          <p className={styles.editorHint}>{selectionHint}</p>
          <div className={styles.rowActions}>
            <Button size="sm" disabled={!available("up")} onClick={() => move("up", false)}>
              Move up
            </Button>
            <Button size="sm" disabled={!available("down")} onClick={() => move("down", false)}>
              Move down
            </Button>
            <Button size="sm" disabled={!available("indent")} onClick={() => move("indent", false)}>
              Indent
            </Button>
            <Button size="sm" disabled={!available("outdent")} onClick={() => move("outdent", false)}>
              Outdent
            </Button>
            <Button size="sm" variant="danger" disabled={saving || !removable} onClick={remove}>
              Remove
            </Button>
          </div>
          <PickerForm
            id="move-under"
            label="Move under"
            placeholder="Choose a new parent"
            options={moveOptions}
            hint="Alt with the arrow keys also moves the selected node. Ctrl+S saves and Esc cancels."
            actionLabel="Move"
            disabled={saving || selectedId === null || moveOptions.length === 0}
            onSubmit={(parentId) => selectedId !== null && moveUnder(selectedId, parentId)}
          />
        </div>
        <div role="group" aria-label="Add or replace a node" className={styles.editorGroup}>
          <PickerForm
            id="add-node"
            label="Add org unit"
            placeholder={freeOptions.length === 0 ? "Everything is already in the tree" : "Choose an org unit"}
            options={freeOptions}
            hint={rootless ? "It becomes the root of this hierarchy." : selectedId === null ? "Select a parent node first." : `It is added under ${nameOf(selectedId)}.`}
            actionLabel="Add"
            disabled={saving || (!rootless && selectedId === null)}
            onSubmit={add}
          />
          <PickerForm
            id="replace-node"
            label="Replace selected with"
            placeholder="Choose an org unit"
            options={freeOptions}
            hint={selectedId === null ? "Select the node to replace first." : "Children and position stay in place."}
            actionLabel="Replace"
            disabled={saving || selectedId === null}
            onSubmit={replace}
          />
        </div>
        {publishOpen && !dirty && canPublish ? (
          <PublishPanel
            versionId={version.id}
            versionNo={version.versionNo}
            lock={base.lock}
            type={type}
            canBackdate={canBackdate}
            reason={publishReason}
            onReasonChange={setPublishReason}
            onBackdateChange={setPublishBackdated}
            onPublished={onPublished}
          />
        ) : null}
      </section>
      {rootless ? (
        <EmptyState compact title="This draft has no nodes yet" description="Add an org unit to start the hierarchy. The first one becomes the root." />
      ) : (
        <HierarchyTreeView
          nodes={nodes}
          labels={labels}
          changes={diff?.changes ?? serverChanges}
          editing={{
            selectedId,
            focusSignal,
            locked: saving,
            onSelect: setSelectedId,
            onMove: move,
            onDrop: (id, parentId) => {
              setSelectedId(id);
              moveUnder(id, parentId);
            },
          }}
        />
      )}
      {previousVersionNo !== null ? <RemovedSection since={previousVersionNo} removed={diff?.removed ?? serverRemoved} labels={labels} /> : null}
      {confirmDiscard ? (
        <ConfirmDialog
          title="Discard your changes?"
          description="Your unsaved edits to this draft will be lost."
          confirmLabel="Discard changes"
          cancelLabel="Keep editing"
          onConfirm={discard}
          onCancel={() => setConfirmDiscard(false)}
        />
      ) : null}
    </div>
  );
}
