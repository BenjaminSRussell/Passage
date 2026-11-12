-- #25: the gateway terminates the connector's mTLS and forwards the certificate's common name (edge:<node id>) as
-- the caller. The certificate names a node, not a tenant, so the service finds the node's tenant itself. Edge
-- node ids are UUIDs, unique across tenants, and this function returns nothing but the tenant of one node: the
-- caller still has to be that node's bound connector subject, which the service checks inside the tenant.
DO $do$
BEGIN
  EXECUTE format($f$
    CREATE OR REPLACE FUNCTION edge_node_tenant(p_id uuid) RETURNS uuid
      LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$ SELECT tenant_id FROM edge_node WHERE id = p_id LIMIT 1 $body$;
  $f$, current_schema());
END
$do$;

REVOKE ALL ON FUNCTION edge_node_tenant(uuid) FROM PUBLIC;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT EXECUTE ON FUNCTION edge_node_tenant(uuid) TO passage_app';
  END IF;
END
$$;
