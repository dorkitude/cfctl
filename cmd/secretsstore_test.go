package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testStoreID = "33333333333333333333333333333333"
const testSecretID = "44444444444444444444444444444444"

func secretsFake(t *testing.T) *fakeCF {
	store := map[string]any{"id": testStoreID, "name": "default_secrets_store", "created": "2026-10-01T00:00:00Z"}
	secret := map[string]any{"id": testSecretID, "name": "API_KEY", "scopes": []any{"workers"}, "status": "active", "comment": "c"}
	s := "/secrets_store/stores/" + testStoreID
	return stFake(t, map[string]stHandler{
		"GET /secrets_store/stores":          stListH([]map[string]any{store}),
		"POST /secrets_store/stores":         stJSON(store),
		"GET " + s:                           stJSON(store),
		"DELETE " + s:                        stJSON(nil),
		"GET /secrets_store/quota":           stJSON(map[string]any{"secrets": map[string]any{"usage": 1, "quota": 100}}),
		"GET " + s + "/secrets":              stListH([]map[string]any{secret}),
		"POST " + s + "/secrets":             stJSON([]any{secret}),
		"GET " + s + "/secrets/*":            stJSON(secret),
		"PATCH " + s + "/secrets/*":          stJSON(secret),
		"DELETE " + s + "/secrets/*":         stJSON(nil),
		"POST " + s + "/secrets/*/duplicate": stJSON(secret),
	})
}

func TestSecretsStoreStores(t *testing.T) {
	f := secretsFake(t)
	out, _ := stRun(t, "", "secrets-store", "store", "list")
	stMust(t, out, "default_secrets_store", testStoreID)
	out, _ = stRun(t, "", "secrets-store", "quota")
	stMust(t, out, "quota:", "100")
	stRun(t, "", "secrets-store", "store", "create", "s2")
	if r := stFind(t, f, "POST", "/secrets_store/stores"); r.Body["name"] != "s2" {
		t.Fatalf("body %v", r.Body)
	}
	stRunErr(t, "", "secrets-store", "store", "delete", "default_secrets_store")
	stRun(t, "", "secrets-store", "store", "delete", "default_secrets_store", "--force", "-y")
	if r := stFind(t, f, "DELETE", "/secrets_store/stores/"+testStoreID); r.Query != "force=true" {
		t.Fatalf("query %q", r.Query)
	}
}

func TestSecretsStoreSecrets(t *testing.T) {
	f := secretsFake(t)
	out, _ := stRun(t, "", "secrets-store", "secret", "list", "default_secrets_store")
	stMust(t, out, "API_KEY", "workers")
	out, _ = stRun(t, "", "secrets-store", "secret", "get", "default_secrets_store", "API_KEY")
	stMust(t, out, testSecretID)

	out, errOut := stRun(t, "sup3rsecret\n", "secrets-store", "secret", "create", "default_secrets_store", "NEW", "--scopes", "workers,ai_gateway", "--comment", "x")
	if strings.Contains(out+errOut, "sup3rsecret") {
		t.Fatal("secret value printed")
	}
	r := stFind(t, f, "POST", "/secrets_store/stores/"+testStoreID+"/secrets")
	if !strings.Contains(string(r.RawBody), `"value":"sup3rsecret"`) || !strings.Contains(string(r.RawBody), `"ai_gateway"`) {
		t.Fatalf("create body %s", r.RawBody)
	}
	stRunErr(t, "x", "secrets-store", "secret", "create", "default_secrets_store", "NEW")

	vf := filepath.Join(t.TempDir(), "v")
	_ = os.WriteFile(vf, []byte("fromfile"), 0o600)
	stRun(t, "", "secrets-store", "secret", "update", "default_secrets_store", "API_KEY", "--value-file", vf, "--comment", "rotated")
	r = stFind(t, f, "PATCH", "/secrets_store/stores/"+testStoreID+"/secrets/"+testSecretID)
	if r.Body["value"] != "fromfile" || r.Body["comment"] != "rotated" {
		t.Fatalf("update body %v", r.Body)
	}
	stRunErr(t, "", "secrets-store", "secret", "update", "default_secrets_store", "API_KEY")

	stRun(t, "", "secrets-store", "secret", "duplicate", "default_secrets_store", "API_KEY", "COPY")
	r = stFind(t, f, "POST", "/secrets_store/stores/"+testStoreID+"/secrets/"+testSecretID+"/duplicate")
	if r.Body["name"] != "COPY" || r.Body["scopes"] == nil {
		t.Fatalf("duplicate body %v", r.Body)
	}
	stRun(t, "", "secrets-store", "secret", "delete", "default_secrets_store", "API_KEY", "-y")
	stFind(t, f, "DELETE", "/secrets_store/stores/"+testStoreID+"/secrets/"+testSecretID)
}

func TestSecretsStoreReadOnly(t *testing.T) {
	f := secretsFake(t)
	stRunErr(t, "", "--read-only", "secrets-store", "secret", "create", "default_secrets_store", "X", "--scopes", "workers", "--value", "v")
	stNoMutation(t, f)
}
