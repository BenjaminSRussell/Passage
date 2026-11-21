package cdc

// Table is one captured master-data table: where its changes come from and how a row becomes field changes.
//
// Adding a table (B-07 parts b and c) is wiring only:
//
//  1. In the owning service: route the transactions that change the table through auditctx.WithAuditedTx (or call
//     auditctx.Emit in them), and add a migration that sets REPLICA IDENTITY FULL on the table (so an update carries
//     its old values), adds it to the service's audit publication (depot_audit_<service>, created on first use) and
//     grants SELECT on it to debezium_cdc_<service>.
//  2. In deploy/kafka/connect.yaml: add the table to the audit-cdc-<service> connector's table.include.list (and its
//     P3 columns to column.exclude.list), or copy the party connector for a new service.
//  3. Here: add the Table entry; a new service also needs its topic in the audit-cdc consumer's KafkaUser.
//
// deploy/scripts/test_argocd_apps.py checks that the three agree.
type Table struct {
	// Source is the owning service; its captured changes are recorded with source = Source.
	Source string
	// Server is the connector's topic.prefix, which Debezium writes as source.name: depot.cdc.<service>.
	Server string
	// Table is schema.table in the source database.
	Table string
	// Entity is the audit entity the changes are recorded under (GET /v1/audit/records/{entity}/{recordId}).
	Entity string
	// Key are the columns, besides tenant_id, whose values joined by "/" are the record id.
	Key []string
	// LegalEntityColumn names the row's legal entity column; empty for a tenant-level table.
	LegalEntityColumn string
	// Fields is the allow-list: only these columns are recorded. P3 columns are never listed (and the connector
	// excludes them, so their values never leave the source database).
	Fields []Field
}

// Field maps a column to the audit field name.
type Field struct {
	Column string
	Name   string
	// Format renders the Debezium JSON value: "" as is (text, numbers with decimal.handling.mode string, booleans),
	// FormatDate for a date (days since the epoch).
	Format string
}

// FormatDate renders a Debezium date (io.debezium.time.Date, days since 1970-01-01) as YYYY-MM-DD.
const FormatDate = "date"

// tenantColumn is the tenant column of every captured table.
const tenantColumn = "tenant_id"

// Tables is the registry of captured tables. The pilot set is party's vendor bank accounts (B-07a); the A7 set
// (credit limit, payment terms, sales price, standard cost, role assignment, AP invoice, journal entry) follows in
// parts b and c.
var Tables = []Table{
	{
		Source: "party", Server: "depot.cdc.party", Table: "public.vendor_bank_account", Entity: "vendor_bank_account",
		Key: []string{"id"}, LegalEntityColumn: "legal_entity_id",
		Fields: []Field{
			{Column: "vendor_id", Name: "vendorId"},
			{Column: "account_holder", Name: "accountHolder"},
			{Column: "bank_country", Name: "bankCountry"},
			{Column: "currency_code", Name: "currencyCode"},
			{Column: "account_last4", Name: "accountLast4"},
			{Column: "status", Name: "status"},
		},
	},
}

// Topic is the single topic an audit CDC connector routes its changes and depot.ctx messages to, one partition so
// that a transaction's message is read before its changes.
func Topic(server string) string { return server }

// Registry looks captured tables up by the Debezium server name and schema.table.
type Registry struct {
	tables  map[string]Table
	sources map[string]string
	servers []string
}

// NewRegistry indexes tables.
func NewRegistry(tables []Table) Registry {
	r := Registry{tables: map[string]Table{}, sources: map[string]string{}}
	for _, t := range tables {
		r.tables[t.Server+"/"+t.Table] = t
		if _, seen := r.sources[t.Server]; !seen {
			r.servers = append(r.servers, t.Server)
		}
		r.sources[t.Server] = t.Source
	}
	return r
}

// Lookup returns the table of a change event.
func (r Registry) Lookup(server, table string) (Table, bool) {
	t, ok := r.tables[server+"/"+table]
	return t, ok
}

// Source returns the owning service of a connector's server name.
func (r Registry) Source(server string) (string, bool) {
	s, ok := r.sources[server]
	return s, ok
}

// Topics are the topics the consumer reads, one per connector.
func (r Registry) Topics() []string {
	out := make([]string, 0, len(r.servers))
	for _, s := range r.servers {
		out = append(out, Topic(s))
	}
	return out
}
