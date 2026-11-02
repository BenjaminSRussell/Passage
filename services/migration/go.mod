module github.com/BenjaminSRussell/passage/services/migration

go 1.25.0

require (
	github.com/BenjaminSRussell/passage/platform v0.0.0
	github.com/go-chi/chi/v5 v5.5.0
	github.com/go-sql-driver/mysql v1.10.1
	github.com/jackc/pgx/v5 v5.11.0
	github.com/microsoft/go-mssqldb v1.11.2
	github.com/shopspring/decimal v1.4.0
	github.com/sijms/go-ora/v2 v2.9.0
	golang.org/x/text v0.41.0
	pgregory.net/rapid v1.3.0
)

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/golang-sql/civil v0.0.0-20220223132316-b832511892a9 // indirect
	github.com/golang-sql/sqlexp v0.1.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/crypto v0.55.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
)

replace github.com/BenjaminSRussell/passage/platform => ../../platform/go
