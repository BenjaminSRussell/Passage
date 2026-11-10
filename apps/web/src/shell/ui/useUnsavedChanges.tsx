import { useCallback, useContext, useEffect, useRef, type ReactNode } from "react";
import { UNSAFE_DataRouterContext, useBlocker } from "react-router-dom";
import { copy } from "../copy";
import { ConfirmDialog } from "./ConfirmDialog";

export interface UnsavedChangesDialog {
  title: string;
  description: string;
  confirmLabel: string;
  cancelLabel: string;
}

const defaultDialog: UnsavedChangesDialog = {
  title: copy.forms.unsavedTitle,
  description: copy.forms.unsavedBody,
  confirmLabel: copy.forms.leave,
  cancelLabel: copy.forms.stay,
};

function NavigationPrompt({ shouldBlock, dialog }: { shouldBlock: () => boolean; dialog: UnsavedChangesDialog }) {
  const blocker = useBlocker(shouldBlock);
  if (blocker.state !== "blocked") {
    return null;
  }
  return (
    <ConfirmDialog
      {...dialog}
      onConfirm={blocker.proceed}
      onCancel={blocker.reset}
    />
  );
}

const FORM_EDIT_EVENTS = ["input", "change", "click"] as const;

function isEditInsideForm(event: Event): boolean {
  const target = event.target;
  if (!(target instanceof Element) || !target.closest("form")) {
    return false;
  }
  return event.type !== "click" || target.closest("[role='option']") !== null;
}

export function useFormBaseline(values: unknown): boolean {
  const snapshot = JSON.stringify(values);
  const baseline = useRef(snapshot);
  const touched = useRef(false);
  useEffect(() => {
    const mark = (event: Event) => {
      if (isEditInsideForm(event)) {
        touched.current = true;
      }
    };
    FORM_EDIT_EVENTS.forEach((type) => document.addEventListener(type, mark, true));
    return () => FORM_EDIT_EVENTS.forEach((type) => document.removeEventListener(type, mark, true));
  }, []);
  if (!touched.current) {
    baseline.current = snapshot;
  }
  return snapshot !== baseline.current;
}

export function useUnsavedChanges(dirty: boolean, dialog: UnsavedChangesDialog = defaultDialog): { prompt: ReactNode; release: () => void } {
  const dirtyRef = useRef(dirty);
  const released = useRef(false);
  dirtyRef.current = dirty;
  const shouldBlock = useCallback(() => dirtyRef.current && !released.current, []);
  const release = useCallback(() => {
    released.current = true;
  }, []);
  const inDataRouter = useContext(UNSAFE_DataRouterContext) !== null;

  useEffect(() => {
    if (!dirty) {
      return;
    }
    const warn = (event: BeforeUnloadEvent) => {
      if (shouldBlock()) {
        event.preventDefault();
        event.returnValue = "";
      }
    };
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty, shouldBlock]);

  return { prompt: inDataRouter ? <NavigationPrompt shouldBlock={shouldBlock} dialog={dialog} /> : null, release };
}
