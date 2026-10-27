import { Alert, Badge, Button, Card, Skeleton } from "../../shell/ui";
import { getPayload, type PayloadTree, type SegmentElement, type SegmentNode } from "./api/client";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const KIND_LABELS: Record<SegmentNode["kind"], string> = {
  interchange: "Interchange",
  group: "Functional group",
  transaction: "Transaction set",
  segment: "Segment",
};

/** Hover text: the X12 reference and, when known, its dictionary name. */
const describe = (e: SegmentElement) => (e.name ? `${e.ref} ${e.name}` : e.ref);

function Element({ element }: { element: SegmentElement }) {
  const label = describe(element);
  return (
    <span className={element.masked ? `${styles.element} ${styles.masked}` : styles.element} title={element.masked ? `${label} (masked)` : label}>
      <span className="visually-hidden">{`${label}${element.masked ? ", masked" : ""}: `}</span>
      {element.value}
    </span>
  );
}

/** One segment as X12 text with each element carrying its dictionary name. */
function SegmentLine({ node }: { node: SegmentNode }) {
  return (
    <li className={styles.segment}>
      <span className={styles.segmentId}>{node.name}</span>
      {(node.elements ?? []).map((e) => (
        <span key={e.ref}>
          <span aria-hidden="true" className={styles.separator}>
            *
          </span>
          <Element element={e} />
        </span>
      ))}
    </li>
  );
}

/** An envelope level (ISA, GS, ST) with its own elements and nested levels. */
function Level({ node }: { node: SegmentNode }) {
  const segments = (node.children ?? []).filter((c) => c.kind === "segment");
  const levels = (node.children ?? []).filter((c) => c.kind !== "segment");
  return (
    <li className={styles.level}>
      <ul className={styles.segments} aria-label={`${KIND_LABELS[node.kind]} ${node.name}`}>
        <SegmentLine node={node} />
        {segments.map((s, i) => (
          <SegmentLine key={`${s.name}-${i}`} node={s} />
        ))}
      </ul>
      {levels.length > 0 ? (
        <ul className={styles.levels}>
          {levels.map((l, i) => (
            <Level key={`${l.name}-${i}`} node={l} />
          ))}
        </ul>
      ) : null}
    </li>
  );
}

/**
 * The raw payload of an interchange as a segment tree (FR-35.10, PRD §6.35.6):
 * envelopes nest, every element shows its X12 reference and dictionary name on
 * hover. Party names, addresses and contacts come back as "***" unless the
 * user holds EDI_PII_VIEW (BR-35.12); masking is done by the service.
 */
export function RawPayloadCard({ interchangeId }: { interchangeId: string }) {
  const state = useLoad<PayloadTree>((signal) => getPayload(interchangeId, signal), `payload:${interchangeId}`);
  return (
    <Card title="Raw payload" actions={state.data?.piiMasked ? <Badge tone="neutral">Personal data masked</Badge> : null}>
      {state.error ? (
        <Alert tone="danger" title="Could not load the raw payload" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : !state.data ? (
        <div aria-busy="true">
          <span className="visually-hidden">Loading the raw payload…</span>
          <Skeleton height="tile" />
        </div>
      ) : (
        <div className={styles.stack}>
          <p className={styles.note}>
            {state.data.piiMasked
              ? "Hover an element for its X12 name. Party names, addresses and contacts are shown as *** because you do not hold EDI_PII_VIEW. Trailers (SE, GE, IEA) are left out."
              : "Hover an element for its X12 name. Personal data is shown in full because you hold EDI_PII_VIEW. Trailers (SE, GE, IEA) are left out."}
          </p>
          <ul className={`${styles.levels} ${styles.tree}`} aria-label="Segment tree">
            <Level node={state.data.tree} />
          </ul>
        </div>
      )}
    </Card>
  );
}
