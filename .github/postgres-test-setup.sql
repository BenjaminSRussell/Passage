-- Extensions stay in public: services/party migration 0001 qualifies public.gin_trgm_ops.
CREATE ROLE passage_app LOGIN NOSUPERUSER NOBYPASSRLS PASSWORD 'passage_app';
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE EXTENSION IF NOT EXISTS btree_gist;
CREATE ROLE hr_loan_gate NOLOGIN NOSUPERUSER BYPASSRLS;
GRANT hr_loan_gate TO passage_app;
GRANT CREATE ON SCHEMA public TO hr_loan_gate;
