package cmd

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthLoginPipedToken(t *testing.T) {
	f := newFakeCF(t)
	dir := testEnv(t, f)

	stdout, stderr, err := runCLI(t, goodToken+"\n", "auth", "login", "--account", acctID)
	if err != nil {
		t.Fatalf("login: %v\nstderr: %s", err, stderr)
	}
	assertNoToken(t, "login output", stdout, stderr)
	if !strings.Contains(stdout, "Authenticated with Cloudflare") || !strings.Contains(stdout, acctID) {
		t.Errorf("unexpected login output:\n%s", stdout)
	}

	// Verified the token; no account discovery.
	if r := f.find("GET", "/user/tokens/verify"); r == nil || r.Auth != "Bearer "+goodToken {
		t.Errorf("token was not verified with bearer auth: %+v", r)
	}
	if f.find("GET", "/accounts") != nil {
		t.Error("login must not list accounts")
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

	stdout, stderr, err := runCLI(t, goodToken, "auth", "login", "--json", "--account", acctID)
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

	stdout, stderr, err := runCLI(t, badToken, "auth", "login", "--account", acctID)
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
	if _, _, err := runCLI(t, "\n\n", "auth", "login", "--account", acctID); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected empty token error, got %v", err)
	}
}

func TestAuthLoginAccountIDFirst(t *testing.T) {
	f := newFakeCF(t)
	dir := testEnv(t, f)

	// No --account, no cache, no terminal: fail before reading the token.
	_, _, err := runCLI(t, goodToken, "auth", "login")
	if err == nil || !strings.Contains(err.Error(), "--account") || !strings.Contains(err.Error(), "token not read") {
		t.Fatalf("expected --account error, got %v", err)
	}
	if n := len(f.Requests()); n != 0 {
		t.Errorf("no API calls expected, got %d", n)
	}

	// Malformed --account is rejected.
	if _, _, err := runCLI(t, goodToken, "auth", "login", "--account", "nope"); err == nil || !strings.Contains(err.Error(), "32 hex") {
		t.Errorf("expected invalid account ID error, got %v", err)
	}

	// Explicit account for a user token: stored as given.
	if _, _, err := runCLI(t, goodToken, "auth", "login", "--account", acctID2); err != nil {
		t.Fatalf("login --account: %v", err)
	}
	if !strings.Contains(readConfig(t, dir), "account_id: "+acctID2) {
		t.Error("account ID not stored")
	}

	// After logout the cached account ID is reused for a piped login.
	if _, _, err := runCLI(t, "", "auth", "logout"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, goodToken, "auth", "login"); err != nil {
		t.Fatalf("login with cached account: %v", err)
	}
}

func TestPromptAccountID(t *testing.T) {
	var out strings.Builder
	id, err := promptAccountID(bufio.NewReader(strings.NewReader("nope\nABC\n"+strings.ToUpper(acctID)+"\n")), &out)
	if err != nil || id != acctID {
		t.Fatalf("got %q %v", id, err)
	}
	if strings.Count(out.String(), "Cloudflare account ID: ") != 3 || !strings.Contains(out.String(), "32 hex") {
		t.Errorf("expected re-prompts:\n%s", out.String())
	}
	if _, err := promptAccountID(bufio.NewReader(strings.NewReader("bad\n")), io.Discard); err == nil {
		t.Error("expected error at EOF without a valid ID")
	}
	for in, want := range map[string]bool{acctID: true, strings.ToUpper(acctID): false, acctID[:31]: false, acctID + "0": false, "": false} {
		if validAccountID(in) != want {
			t.Errorf("validAccountID(%q) != %v", in, want)
		}
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
