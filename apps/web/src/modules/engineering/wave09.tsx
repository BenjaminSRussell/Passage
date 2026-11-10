import { useEffect, useRef, useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, Select, Skeleton, StatusPill, Textarea, type DataTableColumn } from "../../shell/ui";
import {
  ApiError,
  acceptReleaseLine,
  createDocument,
  createOrganization,
  getDocument,
  importEbom,
  listDocuments,
  listEbomImports,
  listOrganizations,
  listReleaseLines,
  listSignatures,
  rejectReleaseLine,
  signDocument,
  transitionDocument,
  type DocumentSignature,
  type EbomImport,
  type EngineeringDocument,
  type EngineeringOrganization,
  type ReleaseLine,
} from "./api/client";
import styles from "./pages.module.css";

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : "Request failed";
}

const orgColumns: DataTableColumn<EngineeringOrganization>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
  { key: "legalEntityId", header: "Legal entity", mono: true },
];

export function OrganizationsPage({ onCreate }: { onCreate?: () => void }) {
  const [rows, setRows] = useState<EngineeringOrganization[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    let current = true;
    setLoading(true);
    setError("");
    listOrganizations()
      .then((page) => current && setRows(page.items))
      .catch((e: unknown) => {
        if (!current) return;
        setRows([]);
        setError(errorText(e));
      })
      .finally(() => current && setLoading(false));
    return () => {
      current = false;
    };
  }, [attempt]);

  return (
    <>
      <PageHeader title="Engineering organizations" description="Legal entities that own engineering versions, nomenclatures and release policies." />
      <Card
        actions={
          onCreate ? (
            <Button size="sm" variant="primary" onClick={onCreate}>
              New organization
            </Button>
          ) : undefined
        }
      />
      <div className={styles.alertSlot}>
        <Alert tone="danger" live="alert" action={error ? <Button size="sm" onClick={() => setAttempt((n) => n + 1)}>Retry</Button> : undefined}>
          {error || null}
        </Alert>
      </div>
      <div className={styles.results}>
        {error && rows.length === 0 ? null : (
          <DataTable
            label="Engineering organizations"
            columns={orgColumns}
            rows={rows}
            loading={loading}
            getRowKey={(row) => row.id}
            empty={{ title: "No engineering organizations yet" }}
          />
        )}
      </div>
    </>
  );
}

export function OrganizationCreatePage({ onCreated, onCancel }: { onCreated?: (id: string) => void; onCancel?: () => void }) {
  const [legalEntityId, setLegalEntityId] = useState("");
  const [code, setCode] = useState("");
  const [name, setName] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");
  const [fields, setFields] = useState<Record<string, string>>({});
  const idempotency = useRef({ body: "", key: "" });

  async function submit(event: FormEvent) {
    event.preventDefault();
    setError("");
    setFields({});
    setSubmitting(true);
    const input = { legalEntityId: legalEntityId.trim(), code: code.trim(), name: name.trim() };
    const body = JSON.stringify(input);
    if (idempotency.current.body !== body) idempotency.current = { body, key: crypto.randomUUID() };
    try {
      const created = await createOrganization(input, idempotency.current.key);
      onCreated?.(created.id);
    } catch (e) {
      setError(errorText(e));
      if (e instanceof ApiError) setFields(e.errors);
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <>
      <PageHeader title="New engineering organization" description="One organization per engineering legal entity." />
      <Card>
        <form className={styles.form} onSubmit={submit}>
          <div className={styles.field}>
            <FormField id="new-org-le" label="Legal entity" hint="The legal entity id (a UUID)." error={fields.legalEntityId}>
              {(control) => <Input {...control} value={legalEntityId} onChange={(e) => setLegalEntityId(e.target.value)} required maxLength={64} />}
            </FormField>
          </div>
          <div className={styles.field}>
            <FormField id="new-org-code" label="Code" error={fields.code}>
              {(control) => <Input {...control} value={code} onChange={(e) => setCode(e.target.value)} required maxLength={64} />}
            </FormField>
          </div>
          <div className={styles.field}>
            <FormField id="new-org-name" label="Name" error={fields.name}>
              {(control) => <Input {...control} value={name} onChange={(e) => setName(e.target.value)} required maxLength={200} />}
            </FormField>
          </div>
          <div className={styles.toolbar}>
            <Button type="submit" variant="primary" loading={submitting}>
              Create organization
            </Button>
            {onCancel ? <Button onClick={onCancel}>Cancel</Button> : null}
          </div>
        </form>
      </Card>
      <div className={styles.alertSlot}>
        <Alert tone="danger" live="alert">
          {error || null}
        </Alert>
      </div>
    </>
  );
}

const documentColumns: DataTableColumn<EngineeringDocument>[] = [
  { key: "number", header: "Document", mono: true },
  { key: "version", header: "Version", render: (row) => row.version || "" },
  { key: "typeCode", header: "Type", mono: true },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
  { key: "part", header: "Part", render: (row) => row.part || "" },
];

export function DocumentsPage({ onOpen, onCreate }: { onOpen?: (id: string) => void; onCreate?: () => void }) {
  const [numberInput, setNumberInput] = useState("");
  const [number, setNumber] = useState("");
  const [rows, setRows] = useState<EngineeringDocument[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    let current = true;
    setLoading(true);
    setError("");
    listDocuments(number)
      .then((page) => current && setRows(page.items))
      .catch((e: unknown) => {
        if (!current) return;
        setRows([]);
        setError(errorText(e));
      })
      .finally(() => current && setLoading(false));
    return () => {
      current = false;
    };
  }, [number, attempt]);

  return (
    <>
      <PageHeader title="Engineering documents" description="Document info records, status network and Part 11 electronic signatures." />
      <Card
        actions={
          onCreate ? (
            <Button size="sm" variant="primary" onClick={onCreate}>
              New document
            </Button>
          ) : undefined
        }
      >
        <form
          className={styles.form}
          onSubmit={(event) => {
            event.preventDefault();
            setNumber(numberInput.trim());
          }}
        >
          <div className={styles.field}>
            <FormField id="doc-number" label="Document number" optional>
              {(control) => <Input {...control} value={numberInput} onChange={(e) => setNumberInput(e.target.value)} />}
            </FormField>
          </div>
          <Button type="submit">Filter</Button>
        </form>
      </Card>
      <div className={styles.alertSlot}>
        <Alert tone="danger" live="alert" action={error ? <Button size="sm" onClick={() => setAttempt((n) => n + 1)}>Retry</Button> : undefined}>
          {error || null}
        </Alert>
      </div>
      <div className={styles.results}>
        {error && rows.length === 0 ? null : (
          <DataTable
            label="Engineering documents"
            columns={documentColumns}
            rows={rows}
            loading={loading}
            getRowKey={(row) => row.id}
            empty={{ title: "No documents match" }}
            onRowOpen={(row) => onOpen?.(row.id)}
          />
        )}
      </div>
    </>
  );
}

export function DocumentCreatePage({ onCreated, onCancel }: { onCreated?: (id: string) => void; onCancel?: () => void }) {
  const [typeCode, setTypeCode] = useState("");
  const [number, setNumber] = useState("");
  const [part, setPart] = useState("");
  const [version, setVersion] = useState("");
  const [storageKey, setStorageKey] = useState("");
  const [sha256, setSha256] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");
  const [fields, setFields] = useState<Record<string, string>>({});
  const idempotency = useRef({ body: "", key: "" });

  async function submit(event: FormEvent) {
    event.preventDefault();
    setError("");
    setFields({});
    setSubmitting(true);
    const originals = storageKey.trim() && sha256.trim() ? [{ storageKey: storageKey.trim(), sha256: sha256.trim() }] : undefined;
    const input = { typeCode: typeCode.trim(), number: number.trim(), ...(part.trim() ? { part: part.trim() } : {}), ...(version.trim() ? { version: version.trim() } : {}), ...(originals ? { originals } : {}) };
    const body = JSON.stringify(input);
    if (idempotency.current.body !== body) idempotency.current = { body, key: crypto.randomUUID() };
    try {
      const created = await createDocument(input, idempotency.current.key);
      onCreated?.(created.id);
    } catch (e) {
      setError(errorText(e));
      if (e instanceof ApiError) setFields(e.errors);
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <>
      <PageHeader title="New document" description="Create a DIR version in work. Release needs an independent Part 11 signature." />
      <Card>
        <form className={styles.form} onSubmit={submit}>
          <div className={styles.field}>
            <FormField id="new-doc-type" label="Type code" error={fields.typeCode}>
              {(control) => <Input {...control} value={typeCode} onChange={(e) => setTypeCode(e.target.value)} required maxLength={64} />}
            </FormField>
          </div>
          <div className={styles.field}>
            <FormField id="new-doc-number" label="Number" error={fields.number}>
              {(control) => <Input {...control} value={number} onChange={(e) => setNumber(e.target.value)} required maxLength={64} />}
            </FormField>
          </div>
          <div className={styles.field}>
            <FormField id="new-doc-part" label="Part" optional>
              {(control) => <Input {...control} value={part} onChange={(e) => setPart(e.target.value)} />}
            </FormField>
          </div>
          <div className={styles.field}>
            <FormField id="new-doc-version" label="Version" optional>
              {(control) => <Input {...control} value={version} onChange={(e) => setVersion(e.target.value)} />}
            </FormField>
          </div>
          <div className={styles.field}>
            <FormField id="new-doc-key" label="Original storage key" hint="Starts with your tenant id and a slash." error={fields["originals[0]"]} optional>
              {(control) => <Input {...control} value={storageKey} onChange={(e) => setStorageKey(e.target.value)} />}
            </FormField>
          </div>
          <div className={styles.field}>
            <FormField id="new-doc-hash" label="Original SHA-256" hint="64 lowercase hex characters." optional>
              {(control) => <Input {...control} value={sha256} onChange={(e) => setSha256(e.target.value)} />}
            </FormField>
          </div>
          <div className={styles.toolbar}>
            <Button type="submit" variant="primary" loading={submitting}>
              Create document
            </Button>
            {onCancel ? <Button onClick={onCancel}>Cancel</Button> : null}
          </div>
        </form>
      </Card>
      <div className={styles.alertSlot}>
        <Alert tone="danger" live="alert">
          {error || null}
        </Alert>
      </div>
    </>
  );
}

const signatureColumns: DataTableColumn<DocumentSignature>[] = [
  { key: "signedAt", header: "Signed at", mono: true },
  { key: "signerId", header: "Signer", mono: true },
  { key: "printedName", header: "Printed name" },
  { key: "meaning", header: "Meaning" },
];

export function DocumentPage({ id, onBack, canWrite = false, canSign = false }: { id: string; onBack?: () => void; canWrite?: boolean; canSign?: boolean }) {
  const [doc, setDoc] = useState<EngineeringDocument | null>(null);
  const [sigs, setSigs] = useState<DocumentSignature[]>([]);
  const [loadError, setLoadError] = useState("");
  const [attempt, setAttempt] = useState(0);
  const [printedName, setPrintedName] = useState("");
  const [busy, setBusy] = useState("");
  const [actionError, setActionError] = useState("");
  const [announcement, setAnnouncement] = useState("");

  useEffect(() => {
    let current = true;
    setDoc(null);
    setLoadError("");
    setActionError("");
    Promise.all([getDocument(id), listSignatures(id)])
      .then(([loaded, signatures]) => {
        if (!current) return;
        setDoc(loaded);
        setSigs(signatures.items);
      })
      .catch((e: unknown) => current && setLoadError(errorText(e)));
    return () => {
      current = false;
    };
  }, [id, attempt]);

  async function sendToReview() {
    setActionError("");
    setBusy("review");
    try {
      const next = await transitionDocument(id, "in_review");
      setDoc(next);
      setAnnouncement(`Document ${next.number} is in review`);
    } catch (e) {
      setActionError(errorText(e));
    } finally {
      setBusy("");
    }
  }

  async function sign(event: FormEvent) {
    event.preventDefault();
    setActionError("");
    setBusy("sign");
    try {
      const next = await signDocument(id, { printedName: printedName.trim(), method: "password" });
      setDoc(next);
      setAnnouncement(next.status === "released" ? `Document ${next.number} is signed and released` : `Document ${next.number} is signed and waits for its change order to release it`);
      const signatures = await listSignatures(id);
      setSigs(signatures.items);
    } catch (e) {
      setActionError(errorText(e));
    } finally {
      setBusy("");
    }
  }

  return (
    <>
      <PageHeader title={doc ? `Document ${doc.number}` : "Engineering document"} description="Status, originals and Part 11 signatures (re-authentication, meaning, timestamp, record link)." />
      <div className={styles.toolbar}>
        {onBack ? (
          <Button size="sm" onClick={onBack}>
            Back to documents
          </Button>
        ) : null}
        {loadError ? (
          <Button size="sm" onClick={() => setAttempt((n) => n + 1)}>
            Retry
          </Button>
        ) : null}
        {doc?.status === "in_work" && canWrite ? (
          <Button size="sm" loading={busy === "review"} onClick={sendToReview}>
            Send to review
          </Button>
        ) : null}
      </div>
      <p className="visually-hidden" aria-live="polite">
        {announcement}
      </p>
      <div className={styles.alertSlot}>
        <Alert tone="danger" live="alert">
          {loadError || actionError || null}
        </Alert>
      </div>
      {!doc && !loadError ? (
        <Card>
          <Skeleton width="1/2" />
          <Skeleton />
        </Card>
      ) : null}
      {doc ? (
        <>
          <Card title="Document">
            <dl className={styles.facts}>
              <dt>Number</dt>
              <dd>{doc.number}</dd>
              <dt>Version</dt>
              <dd>{doc.version || "—"}</dd>
              <dt>Type</dt>
              <dd>{doc.typeCode}</dd>
              <dt>Status</dt>
              <dd>
                <StatusPill status={doc.status} />
              </dd>
              <dt>Last editor</dt>
              <dd>{doc.lastEditor || "—"}</dd>
            </dl>
          </Card>
          {doc.status === "in_review" && canSign && !doc.signedAt ? (
            <Card title="Electronic signature">
              <form className={styles.form} onSubmit={sign}>
                <div className={styles.field}>
                  <FormField id="sign-name" label="Printed name">
                    {(control) => <Input {...control} value={printedName} onChange={(e) => setPrintedName(e.target.value)} required />}
                  </FormField>
                </div>
                <Button type="submit" variant="primary" loading={busy === "sign"}>
                  Sign and release
                </Button>
              </form>
            </Card>
          ) : null}
          <div className={styles.results}>
            <DataTable
              label="Signatures"
              columns={signatureColumns}
              rows={sigs}
              getRowKey={(row) => row.id}
              empty={{ title: "No signatures yet" }}
            />
          </div>
        </>
      ) : null}
    </>
  );
}

const releaseColumns: DataTableColumn<ReleaseLine>[] = [
  { key: "productId", header: "Product", mono: true },
  { key: "engineeringVersion", header: "Version" },
  { key: "legalEntityId", header: "Legal entity", mono: true },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
  { key: "acceptedBy", header: "Accepted by", render: (row) => row.acceptedBy || "" },
];

export function ReleaseLinesPage({ canAccept = false }: { canAccept?: boolean }) {
  const [status, setStatus] = useState("sent");
  const [rows, setRows] = useState<ReleaseLine[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    let current = true;
    setLoading(true);
    setError("");
    listReleaseLines({ status })
      .then((page) => current && setRows(page.items))
      .catch((e: unknown) => {
        if (!current) return;
        setRows([]);
        setError(errorText(e));
      })
      .finally(() => current && setLoading(false));
    return () => {
      current = false;
    };
  }, [status, attempt]);

  async function decide(id: string, accept: boolean) {
    setError("");
    setBusy(id);
    try {
      const line = accept ? await acceptReleaseLine(id) : await rejectReleaseLine(id);
      setRows((existing) => existing.map((row) => (row.id === id ? line : row)));
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy("");
    }
  }

  const columns: DataTableColumn<ReleaseLine>[] = [
    ...releaseColumns,
    {
      key: "id",
      header: "Decision",
      render: (row) =>
        canAccept && row.status === "sent" ? (
          <>
            <Button size="sm" variant="primary" loading={busy === row.id} onClick={() => decide(row.id, true)}>
              Accept
            </Button>{" "}
            <Button size="sm" loading={busy === row.id} onClick={() => decide(row.id, false)}>
              Reject
            </Button>
          </>
        ) : (
          ""
        ),
    },
  ];

  return (
    <>
      <PageHeader title="Product releases" description="Accept or reject engineering versions sent to operational legal entities." />
      <Card>
        <form className={styles.form} onSubmit={(event) => event.preventDefault()}>
          <div className={styles.field}>
            <FormField id="release-status" label="Status" optional>
              {(control) => (
                <Select {...control} value={status} onChange={(e) => setStatus(e.target.value)}>
                  <option value="">Any status</option>
                  {["pending", "sent", "accepted", "rejected", "failed"].map((s) => (
                    <option key={s} value={s}>
                      {s}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
          </div>
        </form>
      </Card>
      <div className={styles.alertSlot}>
        <Alert tone="danger" live="alert" action={error ? <Button size="sm" onClick={() => setAttempt((n) => n + 1)}>Retry</Button> : undefined}>
          {error || null}
        </Alert>
      </div>
      <div className={styles.results}>
        {error && rows.length === 0 ? null : (
          <DataTable
            label="Release lines"
            columns={columns}
            rows={rows}
            loading={loading}
            getRowKey={(row) => row.id}
            empty={{ title: "No release lines match" }}
          />
        )}
      </div>
    </>
  );
}

const ebomColumns: DataTableColumn<EbomImport>[] = [
  { key: "partNumber", header: "Part", mono: true },
  { key: "revision", header: "Revision", mono: true },
  { key: "source", header: "Source" },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status ?? ""} /> },
  { key: "exceptions", header: "Unknown parts", align: "end", render: (row) => String(row.exceptions?.length ?? 0) },
];

export function EbomPage({ onCreate }: { onCreate?: () => void }) {
  const [rows, setRows] = useState<EbomImport[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    let current = true;
    setLoading(true);
    setError("");
    listEbomImports()
      .then((page) => current && setRows(page.items))
      .catch((e: unknown) => {
        if (!current) return;
        setRows([]);
        setError(errorText(e));
      })
      .finally(() => current && setLoading(false));
    return () => {
      current = false;
    };
  }, [attempt]);

  return (
    <>
      <PageHeader title="eBOM imports" description="CAD and PLM bills of material. Unknown part numbers are listed as exceptions." />
      <Card
        actions={
          onCreate ? (
            <Button size="sm" variant="primary" onClick={onCreate}>
              Import eBOM
            </Button>
          ) : undefined
        }
      />
      <div className={styles.alertSlot}>
        <Alert tone="danger" live="alert" action={error ? <Button size="sm" onClick={() => setAttempt((n) => n + 1)}>Retry</Button> : undefined}>
          {error || null}
        </Alert>
      </div>
      <div className={styles.results}>
        {error && rows.length === 0 ? null : (
          <DataTable
            label="eBOM imports"
            columns={ebomColumns}
            rows={rows}
            loading={loading}
            getRowKey={(row) => row.id}
            empty={{ title: "No eBOM imports yet" }}
          />
        )}
      </div>
    </>
  );
}

export function EbomImportPage({ onCreated, onCancel }: { onCreated?: (id: string) => void; onCancel?: () => void }) {
  const [source, setSource] = useState("cad");
  const [partNumber, setPartNumber] = useState("");
  const [revision, setRevision] = useState("");
  const [linesText, setLinesText] = useState("10,WHEEL,1");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");
  const [fields, setFields] = useState<Record<string, string>>({});
  const [result, setResult] = useState<EbomImport | null>(null);
  const [importedId, setImportedId] = useState("");
  const idempotency = useRef({ body: "", key: "" });

  async function submit(event: FormEvent) {
    event.preventDefault();
    setError("");
    setFields({});
    setResult(null);
    setImportedId("");
    setSubmitting(true);
    const lines = linesText
      .split("\n")
      .map((line) => line.trim())
      .filter(Boolean)
      .map((line) => {
        const [findNumber, part, quantity] = line.split(",");
        return { findNumber: Number(findNumber) || 0, partNumber: (part ?? "").trim(), quantity: (quantity ?? "1").trim() };
      });
    const input = { source: source.trim(), partNumber: partNumber.trim(), revision: revision.trim(), lines };
    const body = JSON.stringify(input);
    if (idempotency.current.body !== body) idempotency.current = { body, key: crypto.randomUUID() };
    try {
      const created = await importEbom(input, idempotency.current.key);
      setResult(created);
      setImportedId(created.id);
    } catch (e) {
      setError(errorText(e));
      if (e instanceof ApiError) setFields(e.errors);
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <>
      <PageHeader title="Import eBOM" description="One line per component as find number, part number, quantity. Unknown parts stay on the import as exceptions." />
      <Card>
        <form className={styles.form} onSubmit={submit}>
          <div className={styles.field}>
            <FormField id="ebom-source" label="Source" error={fields.source}>
              {(control) => <Input {...control} value={source} onChange={(e) => setSource(e.target.value)} required maxLength={64} />}
            </FormField>
          </div>
          <div className={styles.field}>
            <FormField id="ebom-part" label="Part number" error={fields.partNumber}>
              {(control) => <Input {...control} value={partNumber} onChange={(e) => setPartNumber(e.target.value)} required maxLength={64} />}
            </FormField>
          </div>
          <div className={styles.field}>
            <FormField id="ebom-rev" label="Revision" error={fields.revision}>
              {(control) => <Input {...control} value={revision} onChange={(e) => setRevision(e.target.value)} required maxLength={64} />}
            </FormField>
          </div>
          <div className={styles.field}>
            <FormField id="ebom-lines" label="Lines" hint="findNumber,partNumber,quantity — one per line" error={fields.lines}>
              {(control) => <Textarea {...control} value={linesText} onChange={(e) => setLinesText(e.target.value)} required rows={6} />}
            </FormField>
          </div>
          <div className={styles.toolbar}>
            <Button type="submit" variant="primary" loading={submitting}>
              Import
            </Button>
            {onCancel ? <Button onClick={onCancel}>Cancel</Button> : null}
          </div>
        </form>
      </Card>
      <div className={styles.alertSlot}>
        <Alert tone="danger" live="alert">
          {error || null}
        </Alert>
      </div>
      {result ? (
        <Card
          title="Import result"
          actions={
            onCreated ? (
              <Button size="sm" variant="primary" onClick={() => onCreated(importedId)}>
                View imports
              </Button>
            ) : undefined
          }
        >
          <p className={styles.lead}>
            {result.partNumber} revision {result.revision}: {result.added ?? 0} added, {result.removed ?? 0} removed, {result.changed ?? 0} changed against the active BOM.
          </p>
          {result.exceptions && result.exceptions.length > 0 ? (
            <>
              <p>
                {result.exceptions.length} unknown part{result.exceptions.length === 1 ? "" : "s"} left out of the draft BOM:
              </p>
              <ul aria-label="Unknown parts">
                {result.exceptions.map((e) => (
                  <li key={e.partNumber}>
                    {e.partNumber} <span className="visually-hidden">({e.code})</span>
                  </li>
                ))}
              </ul>
            </>
          ) : (
            <p>Every part resolved.</p>
          )}
        </Card>
      ) : null}
    </>
  );
}
