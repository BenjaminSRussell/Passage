import { useState } from "react";
import { Alert, Badge, Button, EmptyState, FormField, Input, Select, Textarea } from "../../shell/ui";
import { describeProblem, type Problem } from "./api/client";
import {
  publishHierarchyVersion,
  replaceHierarchyNodes,
  type HierarchyNode,
  type HierarchyNodeKind,
  type HierarchyType,
  type HierarchyVersion,
} from "./api/hierarchy";
import {
  addNode,
  dayNumber,
  indentNode,
  moveNode,
  outdentNode,
  parentChoices,
  replaceNode,
  shiftNode,
  siblingIds,
  todayIso,
  type NodeChange,
} from "./hierarchyTree";
import { HierarchyTreeView, type Label, type MoveCommand } from "./HierarchyTreeView";
import styles from "./pages.module.css";

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
      {problem.message} ({problem.code})
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
  version: HierarchyVersion;
  type: HierarchyType | undefined;
  onPublished(effectiveFrom: string): void;
}

function PublishPanel({ version, type, onPublished }: PublishPanelProps) {
  const [effectiveFrom, setEffectiveFrom] = useState(todayIso);
  const [reason, setReason] = useState("");
  const [submitted, setSubmitted] = useState(false);
  const [publishing, setPublishing] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);

  const validDate = dayNumber(effectiveFrom) !== null;
  const backdated = validDate && effectiveFrom < todayIso();
  const reasonLength = reason.trim().length;
  const dateError = !validDate
    ? "Enter a valid date."
    : backdated && type?.backdateAllowed === false
      ? "This hierarchy type does not allow backdating. Choose today or a later date."
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
      await publishHierarchyVersion(version.id, { version: version.version, effectiveFrom, reason: backdated ? reason.trim() : "" });
      onPublished(effectiveFrom);
    } catch (error) {
      setFailure(describeProblem(error));
      setPublishing(false);
    }
  };

  return (
    <section aria-label="Publish draft" className={styles.editorGroup}>
      <div className={styles.publishFields}>
        <FormField id="publish-effective-from" label="Effective from" error={submitted ? dateError : undefined}>
          {(control) => <Input {...control} type="date" value={effectiveFrom} onChange={(event) => setEffectiveFrom(event.target.value)} />}
        </FormField>
        {backdated ? (
          <FormField
            id="publish-reason"
            label="Backdate reason"
            hint={`Required because this date is in the past. ${REASON_MIN} to ${REASON_MAX} characters.`}
            error={submitted ? reasonError : undefined}
          >
            {(control) => <Textarea {...control} rows={3} required value={reason} onChange={(event) => setReason(event.target.value)} />}
          </FormField>
        ) : null}
      </div>
      {failure ? <ProblemAlert title="Could not publish the draft" problem={failure} /> : null}
      <div className={styles.rowActions}>
        <Button variant="primary" loading={publishing} onClick={submit}>
          Publish version {version.versionNo}
        </Button>
      </div>
    </section>
  );
}

interface DraftEditorProps {
  version: HierarchyVersion;
  type: HierarchyType | undefined;
  labels: ReadonlyMap<string, Label>;
  candidates: Candidate[];
  changes: ReadonlyMap<string, NodeChange>;
  announce(text: string): void;
  notify(text: string): void;
  onReload(): void;
  onPublished(effectiveFrom: string): void;
}

export function HierarchyDraftEditor({ version, type, labels, candidates, changes, announce, notify, onReload, onPublished }: DraftEditorProps) {
  const [nodes, setNodes] = useState<HierarchyNode[]>(version.nodes);
  const [dirty, setDirty] = useState(false);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [focusSignal, setFocusSignal] = useState(0);
  const [saving, setSaving] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [publishOpen, setPublishOpen] = useState(false);

  const nameOf = (id: string) => labels.get(id)?.name ?? id;
  const optionLabel = (id: string) => {
    const label = labels.get(id);
    return label ? `${label.name} (${label.code})` : id;
  };

  const commit = (next: HierarchyNode[] | null, message: (updated: HierarchyNode[]) => string) => {
    if (next === null) {
      return;
    }
    setNodes(next);
    setDirty(true);
    setFailure(null);
    announce(message(next));
  };

  const placement = (id: string) => (updated: HierarchyNode[]) => {
    const parentId = updated.find((node) => node.orgUnitId === id)?.parentOrgUnitId ?? null;
    const siblings = siblingIds(updated, parentId);
    return `${nameOf(id)} is now under ${parentId === null ? "no parent" : nameOf(parentId)}, position ${siblings.indexOf(id) + 1} of ${siblings.length}`;
  };

  const operations: Record<MoveCommand, (id: string) => HierarchyNode[] | null> = {
    up: (id) => shiftNode(nodes, id, -1),
    down: (id) => shiftNode(nodes, id, 1),
    indent: (id) => indentNode(nodes, id),
    outdent: (id) => outdentNode(nodes, id),
  };
  const available = (command: MoveCommand) => selectedId !== null && operations[command](selectedId) !== null;

  const move = (command: MoveCommand, refocus: boolean) => {
    if (selectedId !== null) {
      commit(operations[command](selectedId), placement(selectedId));
      if (refocus) {
        setFocusSignal((signal) => signal + 1);
      }
    }
  };

  const moveUnder = (id: string, parentId: string) => commit(moveNode(nodes, id, parentId), placement(id));

  const present = new Set(nodes.map((node) => node.orgUnitId));
  const free = candidates.filter((candidate) => !present.has(candidate.id));
  const freeOptions = free.map((candidate) => ({ value: candidate.id, label: `${candidate.name} (${candidate.code})` }));
  const candidate = (id: string) => free.find((entry) => entry.id === id);
  const moveOptions = (selectedId === null ? [] : parentChoices(nodes, selectedId)).map((id) => ({ value: id, label: optionLabel(id) }));
  const rootless = nodes.length === 0;

  const add = (id: string) => {
    const chosen = candidate(id);
    if (chosen) {
      commit(addNode(nodes, id, chosen.kind, rootless ? null : selectedId), () => `${chosen.name} added${rootless ? " as the root" : ` under ${nameOf(selectedId ?? "")}`}`);
    }
  };
  const replace = (id: string) => {
    const chosen = candidate(id);
    if (chosen && selectedId !== null) {
      const previous = nameOf(selectedId);
      commit(replaceNode(nodes, selectedId, id, chosen.kind), () => `${previous} replaced by ${chosen.name}`);
      setSelectedId(id);
    }
  };

  const save = async () => {
    setSaving(true);
    setFailure(null);
    try {
      await replaceHierarchyNodes(version.id, version.version, nodes);
      setSaving(false);
      notify(`Draft version ${version.versionNo} saved`);
      onReload();
    } catch (error) {
      setFailure(describeProblem(error));
      setSaving(false);
    }
  };

  const discard = () => {
    setNodes(version.nodes);
    setDirty(false);
    setFailure(null);
    setSelectedId(null);
    announce("Changes discarded");
  };

  const selectionHint =
    selectedId === null ? "Select a node to move or replace it. Drag a node onto its new parent, or use the buttons." : `Selected: ${nameOf(selectedId)}`;

  return (
    <>
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
            <Button size="sm" aria-expanded={publishOpen} disabled={dirty || rootless} onClick={() => setPublishOpen((open) => !open)}>
              Publish…
            </Button>
          </div>
        </div>
        {dirty ? <p className={styles.editorHint}>Save your changes before publishing.</p> : null}
        {failure ? <ProblemAlert title="Could not save the draft" problem={failure} onReload={onReload} /> : null}
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
          </div>
          <PickerForm
            id="move-under"
            label="Move under"
            placeholder="Choose a new parent"
            options={moveOptions}
            hint="Alt with the arrow keys also moves the selected node."
            actionLabel="Move"
            disabled={selectedId === null || moveOptions.length === 0}
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
            disabled={!rootless && selectedId === null}
            onSubmit={add}
          />
          <PickerForm
            id="replace-node"
            label="Replace selected with"
            placeholder="Choose an org unit"
            options={freeOptions}
            hint={selectedId === null ? "Select the node to replace first." : "Children and position stay in place."}
            actionLabel="Replace"
            disabled={selectedId === null}
            onSubmit={replace}
          />
        </div>
        {publishOpen && !dirty ? <PublishPanel version={version} type={type} onPublished={onPublished} /> : null}
      </section>
      {rootless ? (
        <EmptyState compact title="This draft has no nodes yet" description="Add an org unit to start the hierarchy. The first one becomes the root." />
      ) : (
        <HierarchyTreeView
          nodes={nodes}
          labels={labels}
          changes={changes}
          editing={{
            selectedId,
            focusSignal,
            onSelect: setSelectedId,
            onMove: move,
            onDrop: (id, parentId) => {
              setSelectedId(id);
              moveUnder(id, parentId);
            },
          }}
        />
      )}
    </>
  );
}
