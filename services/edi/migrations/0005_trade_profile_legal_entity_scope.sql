-- #486: a trade profile binds its partner to one legal entity, and outbound:from-source reads that entity's shipments and
-- invoices under the edi service grant. Only a caller scoped to the entity may read or write the profile, so a caller of
-- another entity can neither generate documents from it nor repoint it.
DROP POLICY IF EXISTS partner_trade_profile_legal_entity_scope ON partner_trade_profile;
CREATE POLICY partner_trade_profile_legal_entity_scope ON partner_trade_profile AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
