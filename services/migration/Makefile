SHELL := /bin/bash

.PHONY: ac1-load vet test test-ac1

# Seeded 50k/20k/100k/10k load against Postgres (starts Docker if the two PASSAGE_TEST_*DATABASE_URL variables are unset).
ac1-load:
	./scripts/ac1-load.sh

vet:
	go vet ./...

test:
	go test ./...

# Full-size AC1 guard. Needs PASSAGE_TEST_DATABASE_URL (or run `make ac1-load` first).
test-ac1:
	go test -timeout 35m -count=1 -run TestPostgres_AC1FullSizeLoadMeetsTimeBudgetAndReconciles ./internal/store/
