-- Slice 4 (PSG-4104): project categories, the project transaction subledger
-- (FR-41.4) and invoice proposals built from billable transactions (FR-41.6,
-- BR-41.3, BR-41.8). Additive only.

-- ProjectCategory (§8.41.4: ProjectTransaction.category_id is a type-bound FK).
CREATE TABLE IF NOT EXISTS project_category (
  tenant_id uuid NOT NULL,
  id text NOT NULL CHECK (id ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  txn_type text NOT NULL CHECK (txn_type IN ('HOUR', 'COST', 'ITEM', 'FEE', 'ON_ACCOUNT')),
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  -- Target of the composite FK that binds a transaction to its category's type.
  UNIQUE (tenant_id, id, txn_type)
);
ALTER TABLE project_category ENABLE ROW LEVEL SECURITY;
ALTER TABLE project_category FORCE ROW LEVEL SECURITY;
CREATE POLICY project_category_tenant_access ON project_category AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- InvoiceProposal (§8.41.4). txn_types is the selection the proposal was built
-- with; totals are the sums of the lines. The billing rule, tax amount and
-- funding split come with later slices (see NOT-IMPLEMENTED.md).
CREATE TABLE IF NOT EXISTS project_invoice_proposal (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  project_id text NOT NULL,
  customer_party_id uuid,
  status text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'APPROVED', 'POSTED', 'CANCELED')),
  currency char(3) NOT NULL,
  from_date date NOT NULL,
  to_date date NOT NULL,
  txn_types text[] NOT NULL CHECK (cardinality(txn_types) >= 1
    AND txn_types <@ ARRAY['HOUR', 'COST', 'ITEM', 'FEE', 'ON_ACCOUNT']::text[]),
  total_cost numeric(20,6) NOT NULL DEFAULT 0,
  total_sales numeric(20,6) NOT NULL DEFAULT 0,
  created_by text NOT NULL CHECK (char_length(created_by) >= 1),
  approved_by text,
  version integer NOT NULL DEFAULT 1,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, project_id) REFERENCES project (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  CHECK (to_date >= from_date),
  CHECK ((status = 'DRAFT') = (approved_by IS NULL) OR status = 'CANCELED')
);
ALTER TABLE project_invoice_proposal ENABLE ROW LEVEL SECURITY;
ALTER TABLE project_invoice_proposal FORCE ROW LEVEL SECURITY;
CREATE POLICY project_invoice_proposal_tenant_access ON project_invoice_proposal AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
-- Legal-entity scope through the owning project (itself scoped by RLS).
CREATE POLICY project_invoice_proposal_legal_entity_scope ON project_invoice_proposal AS RESTRICTIVE
  USING (EXISTS (SELECT 1 FROM project p WHERE p.tenant_id = project_invoice_proposal.tenant_id AND p.id = project_invoice_proposal.project_id));
CREATE INDEX IF NOT EXISTS project_invoice_proposal_project_idx ON project_invoice_proposal (tenant_id, project_id, status, created_at);

-- ProjectTransaction (§8.41.4). Amounts are in the project currency; the
-- accounting- and reporting-currency columns, journal posting and hash
-- partitioning come later (NOT-IMPLEMENTED.md D2a, D2b). wbs_code is resolved
-- from wbs_node_id on entry, because node ids change when a WORKING plan's WBS
-- is replaced. proposal_id is set while the transaction sits on a DRAFT,
-- APPROVED or POSTED proposal, so it is on at most one live proposal.
CREATE TABLE IF NOT EXISTS project_transaction (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  project_id text NOT NULL,
  wbs_node_id uuid,
  wbs_code text CHECK (wbs_code IS NULL OR char_length(wbs_code) BETWEEN 1 AND 40),
  txn_type text NOT NULL CHECK (txn_type IN ('HOUR', 'COST', 'ITEM', 'FEE', 'ON_ACCOUNT')),
  category_id text NOT NULL,
  line_property text NOT NULL CHECK (line_property IN ('CHARGEABLE', 'NON_CHARGEABLE')),
  txn_date date NOT NULL,
  quantity numeric(14,3) NOT NULL CHECK (quantity >= 0),
  cost_price numeric(20,6) NOT NULL CHECK (cost_price >= 0),
  sales_price numeric(20,6) NOT NULL,
  currency char(3) NOT NULL,
  cost_amount numeric(20,6) NOT NULL,
  sales_amount numeric(20,6) NOT NULL,
  worker_id uuid,
  item_id uuid,
  vendor_party_id uuid,
  source_doc_type text NOT NULL CHECK (source_doc_type IN ('TIMESHEET', 'EXPENSE', 'PO_RECEIPT', 'INV_JOURNAL', 'MANUAL')),
  source_doc_id uuid,
  description text NOT NULL DEFAULT '' CHECK (char_length(description) <= 500),
  journal_id uuid,
  proposal_id uuid,
  created_by text NOT NULL CHECK (char_length(created_by) >= 1),
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, project_id) REFERENCES project (tenant_id, id),
  FOREIGN KEY (tenant_id, category_id, txn_type) REFERENCES project_category (tenant_id, id, txn_type),
  FOREIGN KEY (tenant_id, proposal_id) REFERENCES project_invoice_proposal (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  CHECK ((wbs_node_id IS NULL) = (wbs_code IS NULL)),
  CHECK (sales_price >= 0 OR txn_type = 'ON_ACCOUNT'),
  CHECK (txn_type <> 'HOUR' OR worker_id IS NOT NULL),
  CHECK (txn_type <> 'ITEM' OR item_id IS NOT NULL)
);
ALTER TABLE project_transaction ENABLE ROW LEVEL SECURITY;
ALTER TABLE project_transaction FORCE ROW LEVEL SECURITY;
CREATE POLICY project_transaction_tenant_access ON project_transaction AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY project_transaction_legal_entity_scope ON project_transaction AS RESTRICTIVE
  USING (EXISTS (SELECT 1 FROM project p WHERE p.tenant_id = project_transaction.tenant_id AND p.id = project_transaction.project_id));
CREATE INDEX IF NOT EXISTS project_transaction_project_date_idx ON project_transaction (tenant_id, project_id, txn_date);
-- Proposal selection scans only unbilled chargeable rows (§8.41.4, §8.41.19).
CREATE INDEX IF NOT EXISTS project_transaction_unbilled_idx ON project_transaction (tenant_id, project_id, txn_date)
  WHERE proposal_id IS NULL AND line_property = 'CHARGEABLE';
CREATE INDEX IF NOT EXISTS project_transaction_proposal_idx ON project_transaction (tenant_id, proposal_id)
  WHERE proposal_id IS NOT NULL;

-- Proposal lines. A CANCELED proposal keeps its lines as history, so the same
-- transaction can appear on a canceled proposal and on the live one that
-- billed it later; the live link is project_transaction.proposal_id.
CREATE TABLE IF NOT EXISTS project_invoice_proposal_line (
  tenant_id uuid NOT NULL,
  proposal_id uuid NOT NULL,
  line_no integer NOT NULL CHECK (line_no > 0),
  transaction_id uuid NOT NULL,
  quantity numeric(14,3) NOT NULL,
  unit_price numeric(20,6) NOT NULL,
  cost_amount numeric(20,6) NOT NULL,
  amount numeric(20,6) NOT NULL,
  PRIMARY KEY (tenant_id, proposal_id, line_no),
  UNIQUE (tenant_id, proposal_id, transaction_id),
  FOREIGN KEY (tenant_id, proposal_id) REFERENCES project_invoice_proposal (tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, transaction_id) REFERENCES project_transaction (tenant_id, id)
);
ALTER TABLE project_invoice_proposal_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE project_invoice_proposal_line FORCE ROW LEVEL SECURITY;
CREATE POLICY project_invoice_proposal_line_tenant_access ON project_invoice_proposal_line AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY project_invoice_proposal_line_legal_entity_scope ON project_invoice_proposal_line AS RESTRICTIVE
  USING (EXISTS (SELECT 1 FROM project_invoice_proposal pr
    WHERE pr.tenant_id = project_invoice_proposal_line.tenant_id AND pr.id = project_invoice_proposal_line.proposal_id));
CREATE INDEX IF NOT EXISTS project_invoice_proposal_line_txn_idx ON project_invoice_proposal_line (tenant_id, transaction_id);

-- Application role privileges for the slice-4 tables (see 0005_grants.sql). No
-- DELETE except on proposal lines, which RemoveProposalLine deletes. Skipped
-- when the role does not exist (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON project_category, project_transaction, project_invoice_proposal TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, DELETE ON project_invoice_proposal_line TO passage_app';
  END IF;
END
$$;
