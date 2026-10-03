package cmd

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudflare/cloudflare-go/v7/accounts"
)

func TestAuthLoginPipedToken(t *testing.T) {
	f := newFakeCF(t)
	dir := testEnv(t, f)

	stdout, stderr, err := runCLI(t, goodToken+"\n", "auth", "login")
	if err != nil {
		t.Fatalf("login: %v\nstderr: %s", err, stderr)
	}
	assertNoToken(t, "login output", stdout, stderr)
	if !strings.Contains(stdout, "Authenticated with Cloudflare") || !strings.Contains(stdout, acctID) {
		t.Errorf("unexpected login output:\n%s", stdout)
	}

	// Verified the token, then discovered the account.
	if r := f.find("GET", "/user/tokens/verify"); r == nil || r.Auth != "Bearer "+goodToken {
		t.Errorf("token was not verified with bearer auth: %+v", r)
	}
	if f.find("GET", "/accounts") == nil {
		t.Error("accounts were not listed")
	}

	// Files and permissions.
	st, err := os.Stat(dir)
	if err != nil || st.Mode().Perm() != 0700 {
		t.Errorf("config dir mode = %v, want 0700 (err %v)", st.Mode().Perm(), err)
	}
	tokenPath := filepath.Join(dir, "token")
	st, err = os.Stat(tokenPath)
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatalf("token file mode = %v, want 0600 (err %v)", st.Mode().Perm(), err)
	}
	if b, _ := os.ReadFile(tokenPath); string(b) != goodToken {
		t.Errorf("token file contents wrong (len %d)", len(b))
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	cfgBytes, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfgBytes), "account_id: "+acctID) {
		t.Errorf("config.yaml missing account_id:\n%s", cfgBytes)
	}
	assertNoToken(t, "config.yaml", string(cfgBytes))
	if st, _ := os.Stat(cfgPath); st.Mode().Perm() != 0600 {
		t.Errorf("config.yaml mode = %v, want 0600", st.Mode().Perm())
	}

	// Second login is a no-op without --force.
	stdout, _, err = runCLI(t, goodToken, "auth", "login")
	if err != nil || !strings.Contains(stdout, "Already authenticated") {
		t.Errorf("second login: err=%v out=%s", err, stdout)
	}
}

func TestAuthLoginJSON(t *testing.T) {
	f := newFakeCF(t)
	testEnv(t, f)

	stdout, stderr, err := runCLI(t, goodToken, "auth", "login", "--json")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	assertNoToken(t, "login --json", stdout, stderr)
	var res LoginResult
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("stdout is not pure JSON: %v\n%s", err, stdout)
	}
	if !res.Authenticated || res.AccountID != acctID || res.TokenID != "tok-id-123" {
		t.Errorf("unexpected result: %+v", res)
	}
}

func TestAuthLoginBadToken(t *testing.T) {
	f := newFakeCF(t)
	dir := testEnv(t, f)

	stdout, stderr, err := runCLI(t, badToken, "auth", "login")
	if err == nil || !strings.Contains(err.Error(), "invalid token") || !strings.Contains(err.Error(), "Invalid API Token") {
		t.Fatalf("expected invalid token error, got %v", err)
	}
	assertNoToken(t, "bad login", stdout, stderr, errString(err))
	if _, err := os.Stat(filepath.Join(dir, "token")); !os.IsNotExist(err) {
		t.Error("token file was written for an invalid token")
	}
}

func TestAuthLoginEmptyToken(t *testing.T) {
	f := newFakeCF(t)
	testEnv(t, f)
	if _, _, err := runCLI(t, "\n\n", "auth", "login"); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected empty token error, got %v", err)
	}
}

func TestAuthLoginMultipleAccounts(t *testing.T) {
	f := newFakeCF(t)
	dir := testEnv(t, f)
	f.accounts = append(f.accounts, map[string]interface{}{"id": acctID2, "name": "Other", "type": "standard"})

	// --account picks one explicitly.
	if _, _, err := runCLI(t, goodToken, "auth", "login", "--account", acctID2); err != nil {
		t.Fatalf("login --account: %v", err)
	}
	cfg, _ := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if !strings.Contains(string(cfg), acctID2) {
		t.Errorf("expected %s in config, got:\n%s", acctID2, cfg)
	}

	// Unknown --account is rejected.
	_, _, err := runCLI(t, goodToken, "auth", "login", "--force", "--account", "nope")
	if err == nil || !strings.Contains(err.Error(), "not visible") {
		t.Errorf("expected not-visible error, got %v", err)
	}
}

func TestPickAccount(t *testing.T) {
	a := []accounts.Account{{ID: acctID, Name: "One"}, {ID: acctID2, Name: "Two"}}
	noTTY := func() (io.Reader, func()) { return nil, func() {} }
	tty := func(s string) func() (io.Reader, func()) {
		return func() (io.Reader, func()) { return strings.NewReader(s), func() {} }
	}

	if got, err := pickAccount(a[:1], "", "", noTTY); err != nil || got.ID != acctID {
		t.Errorf("single account: %v %v", got.ID, err)
	}
	if got, err := pickAccount(a, "two", "", noTTY); err != nil || got.ID != acctID2 {
		t.Errorf("by name: %v %v", got.ID, err)
	}
	if got, err := pickAccount(a, "", acctID2, noTTY); err != nil || got.ID != acctID2 {
		t.Errorf("cached: %v %v", got.ID, err)
	}
	if _, err := pickAccount(a, "", "", noTTY); err == nil || !strings.Contains(err.Error(), "--account") {
		t.Errorf("no tty: expected --account hint, got %v", err)
	}
	if got, err := pickAccount(a, "", "", tty("2\n")); err != nil || got.ID != acctID2 {
		t.Errorf("prompt: %v %v", got.ID, err)
	}
	if _, err := pickAccount(a, "", "", tty("9\n")); err == nil {
		t.Error("prompt: expected error for out-of-range choice")
	}
	if _, err := pickAccount(nil, "", "", noTTY); err == nil {
		t.Error("expected error for no accounts")
	}
}

func TestAuthStatusLogout(t *testing.T) {
	f := newFakeCF(t)
	testEnv(t, f)

	stdout, _, _ := runCLI(t, "", "auth", "status")
	if !strings.Contains(stdout, "Not authenticated") {
		t.Errorf("expected not authenticated, got %s", stdout)
	}

	login(t)
	stdout, stderr, _ := runCLI(t, "", "auth", "status")
	if !strings.Contains(stdout, "Authenticated") || !strings.Contains(stdout, acctID) {
		t.Errorf("unexpected status: %s", stdout)
	}
	assertNoToken(t, "status", stdout, stderr)

	stdout, _, _ = runCLI(t, "", "auth", "status", "--json")
	assertNoToken(t, "status --json", stdout)
	var s AuthStatus
	if err := json.Unmarshal([]byte(stdout), &s); err != nil || !s.Authenticated || s.TokenSource != "file" {
		t.Errorf("status --json: %v %+v", err, s)
	}

	if _, _, err := runCLI(t, "", "auth", "logout"); err != nil {
		t.Fatal(err)
	}
	stdout, _, _ = runCLI(t, "", "auth", "status")
	if !strings.Contains(stdout, "Not authenticated") {
		t.Errorf("expected not authenticated after logout, got %s", stdout)
	}
}

func TestEnvTokenOverridesFile(t *testing.T) {
	f := newFakeCF(t)
	dir := testEnv(t, f)
	_ = os.MkdirAll(dir, 0700)
	_ = os.WriteFile(filepath.Join(dir, "token"), []byte(badToken), 0600)
	t.Setenv("CLOUDFLARE_API_TOKEN", goodToken)

	stdout, stderr, err := runCLI(t, "", "whoami", "--json")
	if err != nil {
		t.Fatalf("whoami with env token: %v", err)
	}
	assertNoToken(t, "whoami", stdout, stderr)
	if r := f.find("GET", "/user/tokens/verify"); r == nil || r.Auth != "Bearer "+goodToken {
		t.Errorf("env token was not used: %+v", r)
	}

	stdout, _, _ = runCLI(t, "", "auth", "status", "--json")
	if !strings.Contains(stdout, `"token_source": "CLOUDFLARE_API_TOKEN"`) {
		t.Errorf("status should report env source: %s", stdout)
	}
	assertNoToken(t, "status", stdout)
}

func TestWhoami(t *testing.T) {
	f := newFakeCF(t)
	testEnv(t, f)
	login(t)

	stdout, stderr, err := runCLI(t, "", "whoami")
	if err != nil {
		t.Fatal(err)
	}
	assertNoToken(t, "whoami", stdout, stderr)
	for _, want := range []string{"tok-id-123", "active", acctID, "Kyle's Account"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("whoami missing %q:\n%s", want, stdout)
		}
	}
}

func TestNotAuthenticated(t *testing.T) {
	f := newFakeCF(t)
	testEnv(t, f)
	_, _, err := runCLI(t, "", "domains", "list")
	if err == nil || !strings.Contains(err.Error(), "not authenticated") {
		t.Fatalf("expected not authenticated error, got %v", err)
	}
}
