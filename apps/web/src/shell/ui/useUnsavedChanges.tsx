import { useCallback, useContext, useEffect, useRef, type ReactNode } from "react";
import { UNSAFE_DataRouterContext, useBlocker } from "react-router-dom";
import { copy } from "../copy";
import { ConfirmDialog } from "./ConfirmDialog";

function NavigationPrompt({ shouldBlock }: { shouldBlock: () => boolean }) {
  const blocker = useBlocker(shouldBlock);
  if (blocker.state !== "blocked") {
    return null;
  }
  return (
    <ConfirmDialog
      title={copy.forms.unsavedTitle}
      description={copy.forms.unsavedBody}
      confirmLabel={copy.forms.leave}
      cancelLabel={copy.forms.stay}
      onConfirm={blocker.proceed}
      onCancel={blocker.reset}
    />
  );
}

export function useUnsavedChanges(dirty: boolean): { prompt: ReactNode; release: () => void } {
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

  return { prompt: inDataRouter ? <NavigationPrompt shouldBlock={shouldBlock} /> : null, release };
}
