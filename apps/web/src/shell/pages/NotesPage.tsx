import { copy } from "../copy";
import { FormRenderer } from "../forms";
import { demoNote, fakeCatalog } from "../metadata";
import { Card, PageHeader } from "../ui";

export interface NotesPageProps {
  catalog?: ReturnType<typeof fakeCatalog>;
}

export function NotesPage({ catalog = fakeCatalog() }: NotesPageProps) {
  return (
    <>
      <PageHeader title={copy.notes.title} description={copy.notes.description} />
      <Card>
        <FormRenderer entity={catalog.get(demoNote.id) ?? demoNote} />
      </Card>
    </>
  );
}
