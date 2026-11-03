-- Open items created from approved expense reports name the employee as payee, and no
-- link from a worker to a party vendor exists yet. Such an item is offered to a payment
-- proposal only while the payee resolves in the party vendor master.
alter table ap_open_item add column payee_unverified boolean not null default false;
