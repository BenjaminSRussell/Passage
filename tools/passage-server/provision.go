package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Provisioner prepares the per-service databases. Tests fake it.
type Provisioner interface {
	// EnsureDatabase creates the database, owned by the connecting (owner) role, when it does not exist.
	EnsureDatabase(ctx context.Context, name string) error
	// GrantApp lets the passage_app role use the database: connect, use the public schema, and read and write every table the owner
	// creates there later (default privileges). Idempotent.
	GrantApp(ctx context.Context, db, role string) error
}

// ProvisionDatabases runs before the migrating children start. The owner role (PG_DSN) creates each passage_<service> database, so it
// owns the tables its migrations create; passage_app only gets the privileges to use them. When no passage_app URL is configured,
// only the databases are created.
func ProvisionDatabases(ctx context.Context, cfg Config, procs []Process, p Provisioner, log *slog.Logger) error {
	role := ""
	if cfg.AppDSN != "" {
		u, err := url.Parse(cfg.AppDSN)
		if err != nil || u.User == nil || u.User.Username() == "" {
			return errors.New("PG_APP_DSN has no user name")
		}
		role = u.User.Username()
	}
	for _, proc := range procs {
		raw, ok := proc.Env["DATABASE_URL"]
		if !ok {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", proc.Name, err)
		}
		db := strings.TrimPrefix(u.Path, "/")
		if db == "" {
			return fmt.Errorf("%s: database url has no database name", proc.Name)
		}
		if err := p.EnsureDatabase(ctx, db); err != nil {
			return fmt.Errorf("database %s: %w", db, err)
		}
		if role != "" {
			if err := p.GrantApp(ctx, db, role); err != nil {
				return fmt.Errorf("grant %s on %s: %w", role, db, err)
			}
		}
		log.Debug("database ready", "database", db)
	}
	return nil
}

// pgProvisioner connects with the owner DSN.
type pgProvisioner struct{ dsn string }

func (p pgProvisioner) EnsureDatabase(ctx context.Context, name string) error {
	conn, err := pgx.Connect(ctx, p.dsn)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()
	var exists bool
	if err := conn.QueryRow(ctx, `select exists(select 1 from pg_database where datname = $1)`, name).Scan(&exists); err != nil || exists {
		return err
	}
	_, err = conn.Exec(ctx, "create database "+pgx.Identifier{name}.Sanitize())
	return err
}

func (p pgProvisioner) GrantApp(ctx context.Context, db, role string) error {
	u, err := url.Parse(p.dsn)
	if err != nil {
		return err
	}
	u.Path = "/" + db
	conn, err := pgx.Connect(ctx, u.String())
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()
	r := pgx.Identifier{role}.Sanitize()
	for _, stmt := range []string{
		"grant connect on database " + pgx.Identifier{db}.Sanitize() + " to " + r,
		"grant usage on schema public to " + r,
		"grant select, insert, update, delete on all tables in schema public to " + r,
		"grant usage, select on all sequences in schema public to " + r,
		"alter default privileges in schema public grant select, insert, update, delete on tables to " + r,
		"alter default privileges in schema public grant usage, select on sequences to " + r,
		"alter default privileges in schema public grant execute on functions to " + r,
	} {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}
