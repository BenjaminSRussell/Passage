module github.com/BenjaminSRussell/passage/services/metadata

go 1.23

require (
	github.com/BenjaminSRussell/passage/platform v0.0.0
	github.com/go-chi/chi/v5 v5.2.1
	github.com/jackc/pgx/v5 v5.7.2
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/rogpeppe/go-internal v1.13.1 // indirect
	golang.org/x/crypto v0.31.0 // indirect
	golang.org/x/sync v0.10.0 // indirect
	golang.org/x/text v0.21.0 // indirect
)

replace github.com/BenjaminSRussell/passage/platform => ../../platform/go
