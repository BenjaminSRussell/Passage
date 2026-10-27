package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sort"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const contractTenant = "tenant-a"

type seededDuty struct {
	Permissions []struct {
		Resource string   `json:"resource"`
		Methods  []string `json:"methods"`
	} `json:"permissions"`
}

func startRealPolicy(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("opa")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("opa binary is required in CI")
		}
		t.Skip("opa binary not on PATH")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()

	cmd := exec.Command(bin, "run", "--server", "--addr", addr, "../../policies/authz")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	base := "http://" + addr
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := http.Get(base + "/health")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return base
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("opa server did not become healthy")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func putAssignments(t *testing.T, base string, assignments map[string][]string) {
	t.Helper()
	body, err := json.Marshal(map[string]map[string][]string{contractTenant: assignments})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPut, base+"/v1/data/assignments", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("seed assignments status = %d", resp.StatusCode)
	}
}

func TestDutyContractAgainstRealPolicy(t *testing.T) {
	base := startRealPolicy(t)

	raw, err := os.ReadFile("../../policies/authz/data.json")
	if err != nil {
		t.Fatal(err)
	}
	var seeded struct {
		Duties map[string]seededDuty `json:"duties"`
	}
	if err := json.Unmarshal(raw, &seeded); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(seeded.Duties))
	for name := range seeded.Duties {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) < 20 {
		t.Fatalf("seeded duties = %d, want at least 20", len(names))
	}

	assignments := map[string][]string{}
	userFor := map[string]string{}
	for i, name := range names {
		user := fmt.Sprintf("user-%d", i)
		userFor[name] = user
		assignments[user] = []string{name}
	}
	putAssignments(t, base, assignments)

	client, err := NewOPAClient(base, nil)
	if err != nil {
		t.Fatal(err)
	}

	serve := func(user, method, resource string) int {
		handler := RequireOPA(client, method, resource)(
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }),
		)
		req := httptest.NewRequest(method, resource+"/item-1", nil)
		ctx := tenant.With(req.Context(), tenant.Principal{TenantID: contractTenant, UserID: user})
		req = req.WithContext(context.WithValue(ctx, verifiedPrincipalKey{}, true))
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		return resp.Code
	}

	for _, name := range names {
		for _, permission := range seeded.Duties[name].Permissions {
			for _, method := range permission.Methods {
				t.Run(fmt.Sprintf("%s/%s %s", name, method, permission.Resource), func(t *testing.T) {
					if got := serve(userFor[name], method, permission.Resource); got < 200 || got > 299 {
						t.Errorf("with duty status = %d, want 2xx", got)
					}
					if got := serve("user-without-duty", method, permission.Resource); got != http.StatusForbidden {
						t.Errorf("without duty status = %d, want 403", got)
					}
				})
			}
		}
	}
}
