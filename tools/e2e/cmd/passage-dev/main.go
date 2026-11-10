package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/BenjaminSRussell/passage/tools/e2e/devenv"
	"github.com/BenjaminSRussell/passage/tools/e2e/stack"
)

const (
	webOrigin   = "http://localhost:5173"
	keycloakURL = "http://localhost:8080"
	gatewayAddr = "127.0.0.1:8088"
	opaURL      = "http://127.0.0.1:8181"
	servicePort = 18100
	pgPort      = "15432"
)

var routeOf = map[string]string{"purchasing": "procurement"}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "passage-dev:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: passage-dev render|up")
	}
	root, err := repoRoot()
	if err != nil {
		return err
	}
	switch args[0] {
	case "render":
		_, err = render(root)
		return err
	case "up":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
		defer stop()
		return up(ctx, root)
	}
	return fmt.Errorf("unknown command %q", args[0])
}

func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "deploy", "local", "seed.json")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("run from inside the repository: deploy/local/seed.json not found")
		}
		dir = parent
	}
}

func devDir(root string) string { return filepath.Join(root, ".run", "dev") }

func render(root string) (devenv.Seed, error) {
	seed, err := devenv.LoadSeed(filepath.Join(root, "deploy", "local", "seed.json"))
	if err != nil {
		return seed, err
	}
	base, err := os.ReadFile(filepath.Join(root, "deploy", "keycloak", "realm-passage.json"))
	if err != nil {
		return seed, err
	}
	realm, err := devenv.RenderRealm(base, seed, webOrigin)
	if err != nil {
		return seed, err
	}
	assignments, err := devenv.RenderAssignments(seed)
	if err != nil {
		return seed, err
	}
	if err := os.MkdirAll(devDir(root), 0o755); err != nil {
		return seed, err
	}
	if err := os.WriteFile(filepath.Join(devDir(root), "realm-passage.json"), realm, 0o600); err != nil {
		return seed, err
	}
	return seed, os.WriteFile(filepath.Join(devDir(root), "assignments.json"), assignments, 0o600)
}

func compose(ctx context.Context, root string, args ...string) error {
	cmd := exec.CommandContext(ctx, "docker", append([]string{"compose", "-f", filepath.Join(root, "deploy", "local", "compose.yaml")}, args...)...)
	cmd.Env = append(os.Environ(), "PASSAGE_DEV_DIR="+devDir(root), "PASSAGE_DEV_PG_PORT="+pgPort)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func preflight() error {
	for _, tool := range []string{"docker", "psql", "go", "npm"} {
		if _, err := exec.LookPath(tool); err != nil {
			return fmt.Errorf("%s is required and was not found on PATH", tool)
		}
	}
	return nil
}

func up(ctx context.Context, root string) error {
	if err := preflight(); err != nil {
		return err
	}
	seed, err := render(root)
	if err != nil {
		return err
	}
	fmt.Println("starting postgres, kafka, keycloak and opa")
	if err := compose(ctx, root, "up", "-d", "--wait"); err != nil {
		return fmt.Errorf("docker compose up: %w", err)
	}
	stk, err := stack.New(stack.Config{
		Root: root, AdminDB: "postgres://postgres:postgres@127.0.0.1:" + pgPort + "/postgres?sslmode=disable",
		DBPrefix: "passage_dev", RunDir: devDir(root), BasePort: servicePort, KeepDBs: true,
	})
	if err != nil {
		return err
	}
	defer stk.Stop()
	fmt.Println("building and starting services")
	if err := stk.Start(ctx, false); err != nil {
		return err
	}
	if err := grantLimits(ctx, root, stk, seed); err != nil {
		return err
	}
	if err := startGateway(ctx, root, stk, seed); err != nil {
		return err
	}
	if err := startWeb(root, stk, seed); err != nil {
		return err
	}
	fmt.Printf("\nPassage is up: %s\n", webOrigin)
	for _, u := range seed.Users {
		fmt.Printf("  sign in as %s / %s\n", u.Username, u.Password)
	}
	fmt.Printf("logs: %s\nCtrl-C stops the services; make dev-down removes the containers\n", filepath.Join(devDir(root), "logs"))
	select {
	case <-ctx.Done():
	case name := <-stk.FirstExit():
		return fmt.Errorf("%s exited; see %s", name, filepath.Join(devDir(root), "logs", name+".log"))
	}
	return nil
}

func grantLimits(ctx context.Context, root string, stk *stack.Stack, seed devenv.Seed) error {
	for _, u := range seed.Users {
		if !slices.Contains(u.Duties, "procurement.purchase-order.confirm") {
			continue
		}
		marker := filepath.Join(devDir(root), "limit-"+u.Sub)
		if _, err := os.Stat(marker); err == nil {
			continue
		}
		if err := stack.GrantPurchaseOrderLimit(ctx, stk.URL("purchasing"), u.Sub); err != nil {
			return fmt.Errorf("grant %s a purchase order limit: %w", u.Username, err)
		}
		if err := os.WriteFile(marker, nil, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func startGateway(ctx context.Context, root string, stk *stack.Stack, seed devenv.Seed) error {
	binary := filepath.Join(devDir(root), "bin", "devgateway")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/devgateway")
	build.Dir = filepath.Join(root, "services", "identity")
	if out, err := build.CombinedOutput(); err != nil {
		return fmt.Errorf("build devgateway: %w\n%s", err, out)
	}
	env := map[string]string{
		"DEVGATEWAY_ADDR": gatewayAddr, "DEVGATEWAY_TENANT_ID": seed.TenantID, "DEVGATEWAY_OPA_URL": opaURL,
		"DEVGATEWAY_ISSUER":   keycloakURL + "/realms/" + devenv.RealmName,
		"DEVGATEWAY_JWKS_URL": keycloakURL + "/realms/" + devenv.RealmName + "/protocol/openid-connect/certs",
	}
	for _, name := range stk.Names() {
		route := name
		if r, ok := routeOf[name]; ok {
			route = r
		}
		env["PASSAGE_SVC_"+strings.ToUpper(route)+"_URL"] = stk.URL(name)
	}
	if err := stk.Spawn("devgateway", "", env, binary); err != nil {
		return err
	}
	return waitHTTP(ctx, "http://"+gatewayAddr+"/healthz")
}

func startWeb(root string, stk *stack.Stack, seed devenv.Seed) error {
	web := filepath.Join(root, "apps", "web")
	if _, err := os.Stat(filepath.Join(web, "node_modules")); err != nil {
		return errors.New("apps/web has no node_modules; run `npm ci` in apps/web first")
	}
	return stk.SpawnGroup("web", web, map[string]string{
		"PASSAGE_OIDC_ISSUER": keycloakURL + "/realms/" + devenv.RealmName, "PASSAGE_OIDC_CLIENT_ID": devenv.WebClientID,
		"PASSAGE_GATEWAY_URL": "http://" + gatewayAddr, "PASSAGE_DEV_TENANT": seed.TenantID,
	}, "npm", "run", "dev")
}

func waitHTTP(ctx context.Context, target string) error {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return err
		}
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_ = resp.Body.Close()
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("%s did not answer within 30s", target)
}
