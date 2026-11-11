package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	scratchURLEnv = "PASSAGE_RESTORE_SCRATCH_URL"
	liveURLEnv    = "PASSAGE_RESTORE_LIVE_URL"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Getenv, time.Now()); err != nil {
		fmt.Fprintln(os.Stderr, "tenant-restore:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer, getenv func(string) string, now time.Time) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: tenant-restore scratch-manifest|copy|drill|clone [flags]")
	}
	switch args[0] {
	case "scratch-manifest":
		return runManifest(args[1:], out, now)
	case "copy":
		return runCopy(ctx, args[1:], out, getenv)
	case "drill":
		return runDrill(ctx, args[1:], out, getenv)
	case "clone":
		return runClone(ctx, args[1:], out, getenv)
	}
	return fmt.Errorf("unknown command %q", args[0])
}

func runManifest(args []string, out io.Writer, now time.Time) error {
	fs := flag.NewFlagSet("scratch-manifest", flag.ContinueOnError)
	var spec ScratchSpec
	var target string
	fs.StringVar(&spec.Name, "name", "", "name of the scratch cluster to create")
	fs.StringVar(&spec.Namespace, "namespace", "passage-data", "namespace of the cluster")
	fs.StringVar(&spec.SourceName, "source", "passage-pg", "name of the cluster whose backups are restored")
	fs.StringVar(&spec.StoreName, "object-store", "", "barman-cloud ObjectStore (default <source>-backup)")
	fs.StringVar(&target, "target-time", "", "recover to this RFC 3339 timestamp")
	fs.StringVar(&spec.Image, "image", "ghcr.io/cloudnative-pg/postgresql:17", "Postgres image; must match the source cluster's major version")
	fs.StringVar(&spec.Storage, "storage", "500Gi", "scratch volume size")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if spec.StoreName == "" {
		spec.StoreName = spec.SourceName + "-backup"
	}
	var err error
	if spec.TargetTime, err = time.Parse(time.RFC3339, target); err != nil {
		return fmt.Errorf("--target-time: %w", err)
	}
	return RenderScratchCluster(out, spec, now)
}

func runCopy(ctx context.Context, args []string, out io.Writer, getenv func(string) string) error {
	fs := flag.NewFlagSet("copy", flag.ContinueOnError)
	var tenantID, schema string
	var apply bool
	fs.StringVar(&tenantID, "tenant", "", "tenant uuid whose rows are copied back")
	fs.StringVar(&schema, "schema", "public", "schema holding the tenant tables")
	fs.BoolVar(&apply, "apply", false, "commit the copy; without it the copy is run and rolled back (dry run)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	scratch, err := connect(ctx, getenv, scratchURLEnv)
	if err != nil {
		return err
	}
	defer func() { _ = scratch.Close(ctx) }()
	live, err := connect(ctx, getenv, liveURLEnv)
	if err != nil {
		return err
	}
	defer func() { _ = live.Close(ctx) }()
	result, err := RestoreTenant(ctx, scratch, live, schema, tenantID, apply)
	if err != nil {
		return err
	}
	printResult(out, tenantID, result)
	return nil
}

func runDrill(ctx context.Context, args []string, out io.Writer, getenv func(string) string) error {
	fs := flag.NewFlagSet("drill", flag.ContinueOnError)
	var o DrillOptions
	var record string
	fs.StringVar(&o.Tenant, "tenant", "", "tenant uuid to restore in the drill")
	fs.StringVar(&o.Schema, "schema", "public", "schema holding the tenant tables")
	fs.StringVar(&o.TargetTime, "target-time", "", "the timestamp the scratch cluster was recovered to (recorded)")
	fs.DurationVar(&o.RTO, "rto", time.Hour, "longest the restore may take (0 for no limit)")
	fs.IntVar(&o.MinRows, "min-rows", 1, "fewest rows the tenant must have in the scratch database")
	fs.StringVar(&record, "record", "", "write the drill record (JSON) to this file as well")
	if err := fs.Parse(args); err != nil {
		return err
	}
	scratch, err := connect(ctx, getenv, scratchURLEnv)
	if err != nil {
		return err
	}
	defer func() { _ = scratch.Close(ctx) }()
	live, err := connect(ctx, getenv, liveURLEnv)
	if err != nil {
		return err
	}
	defer func() { _ = live.Close(ctx) }()
	rec := RunDrill(ctx, scratch, live, o, time.Now)
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(out, string(b))
	if record != "" {
		if err := os.WriteFile(record, append(b, '\n'), 0o600); err != nil {
			return err
		}
	}
	if rec.Status != "PASS" {
		return fmt.Errorf("restore drill failed: %s", strings.Join(rec.Failures, "; "))
	}
	return nil
}

const (
	cloneSourceEnv = "PASSAGE_CLONE_SOURCE_URL"
	cloneTargetEnv = "PASSAGE_CLONE_TARGET_URL"
)

func runClone(ctx context.Context, args []string, out io.Writer, getenv func(string) string) error {
	fs := flag.NewFlagSet("clone", flag.ContinueOnError)
	var tenantID, schema, maskFile string
	var apply bool
	fs.StringVar(&tenantID, "tenant", "", "tenant uuid to clone")
	fs.StringVar(&schema, "schema", "public", "schema holding the tenant tables")
	fs.StringVar(&maskFile, "masks", "", "mask configuration (JSON file)")
	fs.BoolVar(&apply, "apply", false, "commit the clone; without it the clone is run and rolled back (dry run)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if maskFile == "" {
		return errors.New("--masks <file> is required: a clone without masks is a plain copy")
	}
	raw, err := os.ReadFile(maskFile)
	if err != nil {
		return err
	}
	masks, err := ParseMasks(raw)
	if err != nil {
		return err
	}
	if getenv(cloneSourceEnv) != "" && getenv(cloneSourceEnv) == getenv(cloneTargetEnv) {
		return errors.New(cloneSourceEnv + " and " + cloneTargetEnv + " are the same database; refusing to mask a tenant in place")
	}
	source, err := connect(ctx, getenv, cloneSourceEnv)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close(ctx) }()
	target, err := connect(ctx, getenv, cloneTargetEnv)
	if err != nil {
		return err
	}
	defer func() { _ = target.Close(ctx) }()
	result, err := CloneTenantMasked(ctx, source, target, schema, tenantID, apply, masks)
	if err != nil {
		return err
	}
	printResult(out, tenantID, result)
	return nil
}

func connect(ctx context.Context, getenv func(string) string, env string) (*pgx.Conn, error) {
	url := getenv(env)
	if url == "" {
		return nil, fmt.Errorf("%s is not set", env)
	}
	return pgx.Connect(ctx, url)
}

func printResult(out io.Writer, tenantID string, r Result) {
	mode := "DRY RUN (rolled back)"
	if r.Applied {
		mode = "APPLIED"
	}
	_, _ = fmt.Fprintf(out, "tenant %s: %s\n", tenantID, mode)
	for _, t := range r.Tables {
		_, _ = fmt.Fprintf(out, "%-32s restored=%d inserted=%d updated=%d unchanged=%d live-only=%d\n", t.Table, t.Source, t.Inserted, t.Updated, t.Unchanged, t.LiveOnly)
	}
}
