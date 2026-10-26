import { useState } from "react";
import { copy } from "../copy";
import { FormRenderer } from "../forms";
import { demoNote, fakeCatalog } from "../metadata";
import { Alert, Card, PageHeader } from "../ui";
import styles from "./pages.module.css";

export interface NotesPageProps {
  catalog?: ReturnType<typeof fakeCatalog>;
}

export function NotesPage({ catalog = fakeCatalog() }: NotesPageProps) {
  const entity = catalog.get(demoNote.id) ?? demoNote;
  const [savedTitle, setSavedTitle] = useState<string | null>(null);
  return (
    <>
      <PageHeader title={copy.notes.title} description={copy.notes.description} />
      <Card>
        <FormRenderer entity={entity} onSubmit={() => setSavedTitle(entity.title)} />
      </Card>
      <div className={styles.notice}>
        <Alert tone="success" live="status">
          {savedTitle ? copy.notes.saved(savedTitle) : null}
        </Alert>
      </div>
    </>
  );
}
