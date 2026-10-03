package cmd

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dorkitude/cfctl/internal/client"
	"github.com/dorkitude/cfctl/internal/config"
)

func countRequests(f *fakeCF, method, path string) int {
	n := 0
	for _, r := range f.Requests() {
		if r.Method == method && r.Path == path {
			n++
		}
	}
	return n
}

func TestListAccountsTerminates(t *testing.T) {
	f := newFakeCF(t)
	testEnv(t, f)
	if err := config.Init(nil); err != nil {
		t.Fatal(err)
	}
	c := client.NewClient(goodToken)

	// Normal: total_pages=1 means one request.
	accts, err := client.ListAccounts(context.Background(), c)
	if err != nil || len(accts) != 1 || countRequests(f, "GET", "/accounts") != 1 {
		t.Fatalf("normal paging: %v, %d accounts, %d requests", err, len(accts), countRequests(f, "GET", "/accounts"))
	}

	// Broken API: same non-empty page forever, no result_info. Must stop at the cap.
	f.accountsForever = true
	done := make(chan struct{})
	go func() {
		defer close(done)
		accts, err = client.ListAccounts(context.Background(), c)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("ListAccounts did not terminate")
	}
	if err != nil {
		t.Fatal(err)
	}
	if n := countRequests(f, "GET", "/accounts") - 1; n > 20 {
		t.Errorf("made %d requests, want at most 20", n)
	}
}

func TestLoginTimeout(t *testing.T) {
	f := newFakeCF(t)
	testEnv(t, f)
	f.slow = 3 * time.Second

	oldLogin, oldReq := loginTimeout, client.RequestTimeout
	loginTimeout, client.RequestTimeout = 400*time.Millisecond, 150*time.Millisecond
	t.Cleanup(func() { loginTimeout, client.RequestTimeout = oldLogin, oldReq })

	start := time.Now()
	stdout, stderr, err := runCLI(t, acctToken, "auth", "login", "--account", acctID)
	if time.Since(start) > 2*time.Second {
		t.Errorf("login took %s; timeouts not applied", time.Since(start))
	}
	if err == nil || !strings.Contains(err.Error(), "timed out") || !strings.Contains(err.Error(), "--account") {
		t.Fatalf("expected helpful timeout error, got %v", err)
	}
	assertNoToken(t, "timeout", stdout, stderr, errString(err))
}

func TestDebugOutput(t *testing.T) {
	f := newFakeCF(t)
	testEnv(t, f)

	_, stderr, err := runCLI(t, goodToken, "auth", "login", "--account", acctID, "--debug")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "debug: GET /client/v4/user/tokens/verify -> 200") {
		t.Errorf("missing debug line:\n%s", stderr)
	}
	assertNoToken(t, "debug", stderr)
	if strings.Contains(strings.ToLower(stderr), "authorization") || strings.Contains(stderr, "Bearer") {
		t.Errorf("debug output must not include headers:\n%s", stderr)
	}

	// CFCTL_DEBUG=1 works too; failed requests are logged with their status.
	t.Setenv("CFCTL_DEBUG", "1")
	_, stderr, _ = runCLI(t, badToken, "auth", "login", "--force", "--account", acctID)
	if !strings.Contains(stderr, "-> 401") {
		t.Errorf("expected 401 debug line:\n%s", stderr)
	}
	assertNoToken(t, "debug", stderr)
}
