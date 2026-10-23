# entitygen

The generator CLI is `services/metadata/cmd/entitygen`: the root Makefile only vets and tests Go modules under `platform/go` and `services`, so a module here would never run in CI.

Usage: `cd services/metadata && go run ./cmd/entitygen -in <definitions dir> -out <output dir> [-check]`
