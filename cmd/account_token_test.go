package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readConfig(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// An account-owned token is rejected by /user/tokens/verify (401, code 1000)
// and must be verified with /accounts/{id}/tokens/verify instead.
func TestAccountTokenLogin(t *testing.T) {
	for _, tok := range []string{acctToken, acctTokenNoPrefix} {
		f := newFakeCF(t)
		dir := testEnv(t, f)

		stdout, stderr, err := runCLI(t, tok+"\n", "auth", "login", "--json")
		if err != nil {
			t.Fatalf("%s: login: %v", tok[:5], err)
		}
		assertNoToken(t, "account login", stdout, stderr)
		var res LoginResult
		if err := json.Unmarshal([]byte(stdout), &res); err != nil {
			t.Fatalf("bad JSON: %v\n%s", err, stdout)
		}
		if res.TokenType != "account" || res.AccountID != acctID || res.TokenID != "acct-tok-id-456" || res.AccountName != "Kyle's Account" {
			t.Errorf("%s: unexpected result %+v", tok[:5], res)
		}

		if f.find("GET", "/accounts/"+acctID+"/tokens/verify") == nil {
			t.Error("account token verify endpoint not called")
		}
		userTried := f.find("GET", "/user/tokens/verify") != nil
		if strings.HasPrefix(tok, "cfat_") && userTried {
			t.Error("cfat_ token should skip /user/tokens/verify")
		}
		if !strings.HasPrefix(tok, "cfat_") && !userTried {
			t.Error("unprefixed token should try /user/tokens/verify first")
		}

		cfg := readConfig(t, dir)
		for _, want := range []string{"account_id: " + acctID, "token_type: account"} {
			if !strings.Contains(cfg, want) {
				t.Errorf("config.yaml missing %q:\n%s", want, cfg)
			}
		}
		assertNoToken(t, "config.yaml", cfg)
		if b, _ := os.ReadFile(filepath.Join(dir, "token")); string(b) != tok {
			t.Error("token file not written")
		}

		// whoami and status use the account verify endpoint and say so.
		stdout, stderr, err = runCLI(t, "", "whoami")
		if err != nil {
			t.Fatalf("whoami: %v", err)
		}
		assertNoToken(t, "whoami", stdout, stderr)
		if !strings.Contains(stdout, "account (owned by Kyle's Account, "+acctID+")") {
			t.Errorf("whoami should show account token type:\n%s", stdout)
		}
		stdout, _, err = runCLI(t, "", "auth", "status", "--verify")
		if err != nil || !strings.Contains(stdout, "Token type: account (owned by") || !strings.Contains(stdout, "Token status: active") {
			t.Errorf("status --verify: %v\n%s", err, stdout)
		}
		assertNoToken(t, "status", stdout)
	}
}

func TestAccountTokenLoginWithAccountFlag(t *testing.T) {
	f := newFakeCF(t)
	dir := testEnv(t, f)

	stdout, stderr, err := runCLI(t, acctToken, "auth", "login", "--account", acctID)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	assertNoToken(t, "login", stdout, stderr)
	if !strings.Contains(stdout, "Token type: account") {
		t.Errorf("unexpected output:\n%s", stdout)
	}
	// With --account there is no need to list accounts to find the owner.
	if f.find("GET", "/accounts") != nil {
		t.Error("should verify against --account directly, not list accounts")
	}
	if !strings.Contains(readConfig(t, dir), "account_id: "+acctID) {
		t.Error("account ID not stored")
	}

	// A wrong --account fails with a helpful message.
	_, _, err = runCLI(t, acctToken, "auth", "login", "--force", "--account", acctID2)
	if err == nil || !strings.Contains(err.Error(), "--account") || !strings.Contains(err.Error(), "/accounts/{id}/tokens/verify") {
		t.Errorf("expected helpful error, got %v", err)
	}
	assertNoToken(t, "error", errString(err))
}

func TestAccountTokenInactive(t *testing.T) {
	f := newFakeCF(t)
	dir := testEnv(t, f)

	stdout, stderr, err := runCLI(t, disabledAcctToken, "auth", "login")
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("expected inactive token error, got %v", err)
	}
	assertNoToken(t, "inactive login", stdout, stderr, errString(err))
	if _, err := os.Stat(filepath.Join(dir, "token")); !os.IsNotExist(err) {
		t.Error("token saved for an inactive token")
	}
}

func TestInvalidTokenMentionsBothEndpoints(t *testing.T) {
	f := newFakeCF(t)
	testEnv(t, f)
	_, _, err := runCLI(t, badToken, "auth", "login")
	if err == nil || !strings.Contains(err.Error(), "/user/tokens/verify") || !strings.Contains(err.Error(), "/accounts/{id}/tokens/verify") || !strings.Contains(err.Error(), "--account") {
		t.Fatalf("expected both endpoints + --account hint, got %v", err)
	}
	assertNoToken(t, "error", errString(err))
}

func TestUserTokenTypeRecorded(t *testing.T) {
	f := newFakeCF(t)
	dir := testEnv(t, f)
	login(t)
	if !strings.Contains(readConfig(t, dir), "token_type: user") {
		t.Error("user token type not recorded")
	}
	if f.find("GET", "/accounts/"+acctID+"/tokens/verify") != nil {
		t.Error("user token should not hit the account verify endpoint")
	}
	stdout, _, _ := runCLI(t, "", "whoami")
	if !strings.Contains(stdout, "Type:") || !strings.Contains(stdout, "user") {
		t.Errorf("whoami should show user type:\n%s", stdout)
	}
}
