package cmd

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const tnID = "c0ffee00-0000-4000-8000-000000000001"

func TestTunnelCommands(t *testing.T) {
	p := platFake(t)
	tn := platA + "/cfd_tunnel"
	p.routes["GET "+tn] = []any{map[string]any{"id": tnID, "name": "home", "status": "healthy", "remote_config": true, "connections": []any{map[string]any{"colo_name": "sjc"}, map[string]any{"colo_name": "lax"}}, "created_at": "2026-01-01T00:00:00Z"}}
	p.routes["GET "+tn+"/"+tnID+"/token"] = "TUNNEL-TOKEN-SECRET"
	p.routes["GET "+tn+"/"+tnID+"/configurations"] = map[string]any{"version": 3, "source": "cloudflare", "config": map[string]any{"ingress": []any{map[string]any{"hostname": "app.example.com", "service": "http://localhost:8080"}, map[string]any{"service": "http_status:404"}}}}
	p.routes["GET "+tn+"/"+tnID+"/connections"] = []any{map[string]any{"id": "conn1", "client_version": "2026.1.0", "arch": "linux_amd64", "conns": []any{map[string]any{"colo_name": "sjc", "origin_ip": "1.2.3.4"}}}}
	p.routes["POST "+tn] = map[string]any{"id": tnID, "name": "new", "token": "CREATE-TOKEN-SECRET", "credentials_file": map[string]any{"TunnelSecret": "CREDS-SECRET"}}
	p.routes["GET "+platA+"/teamnet/routes"] = []any{map[string]any{"id": "rt1", "network": "10.0.0.0/8", "tunnel_id": tnID, "tunnel_name": "home"}}
	p.routes["GET "+platA+"/teamnet/virtual_networks"] = []any{map[string]any{"id": "vn1", "name": "default", "is_default_network": true}}
	platCheck(t, p, []platCase{
		{args: []string{"tunnel", "list"}, method: "GET", path: tn, query: "is_deleted=false", want: "healthy"},
		{args: []string{"tunnel", "info", "home"}, method: "GET", path: tn + "/" + tnID},
		{args: []string{"tunnel", "delete", "home", "--yes"}, method: "DELETE", path: tn + "/" + tnID},
		{args: []string{"tunnel", "connections", "home"}, method: "GET", path: tn + "/" + tnID + "/connections", want: "1.2.3.4"},
		{args: []string{"tunnel", "cleanup", tnID, "--yes"}, method: "DELETE", path: tn + "/" + tnID + "/connections"},
		{args: []string{"tunnel", "config", "get", "home"}, method: "GET", path: tn + "/" + tnID + "/configurations", want: "http://localhost:8080"},
		{args: []string{"tunnel", "config", "set", "home", "--data", `{"config":{"ingress":[{"service":"http_status:404"}]}}`}, method: "PUT", path: tn + "/" + tnID + "/configurations",
			body: map[string]any{"config": map[string]any{"ingress": []any{map[string]any{"service": "http_status:404"}}}}},
		{args: []string{"tunnel", "route", "list"}, method: "GET", path: platA + "/teamnet/routes", want: "10.0.0.0/8"},
		{args: []string{"tunnel", "route", "add", "home", "192.168.0.0/16", "--comment", "lan"}, method: "POST", path: platA + "/teamnet/routes",
			body: map[string]any{"tunnel_id": tnID, "network": "192.168.0.0/16", "comment": "lan"}},
		{args: []string{"tunnel", "route", "delete", "rt1", "--yes"}, method: "DELETE", path: platA + "/teamnet/routes/rt1"},
		{args: []string{"tunnel", "route", "dns", "home", "app.example.com"}, method: "POST", path: "/zones/" + zoneID + "/dns_records",
			body: map[string]any{"type": "CNAME", "name": "app.example.com", "content": tnID + ".cfargotunnel.com", "proxied": true}},
		{args: []string{"tunnel", "vnet", "list"}, method: "GET", path: platA + "/teamnet/virtual_networks", want: "default"},
	})
	// The token is a secret: hidden without --reveal, in text and JSON.
	for _, args := range [][]string{{"tunnel", "token", "home"}, {"tunnel", "token", "home", "--json"}} {
		if out := platRunOK(t, "", args...); strings.Contains(out, "TUNNEL-TOKEN-SECRET") {
			t.Fatalf("%v printed the token: %s", args, out)
		}
	}
	if out := platRunOK(t, "", "tunnel", "token", "home", "--reveal"); strings.TrimSpace(out) != "TUNNEL-TOKEN-SECRET" {
		t.Fatalf("--reveal: %q", out)
	}
	// route dns refuses to clobber an existing record.
	if msg := platRunErr(t, "", "tunnel", "route", "dns", "home", "example.com"); !strings.Contains(msg, "--overwrite") {
		t.Fatal(msg)
	}
}

func TestTunnelCreateRunQuickStart(t *testing.T) {
	p := platFake(t)
	tn := platA + "/cfd_tunnel"
	p.routes["POST "+tn] = map[string]any{"id": tnID, "name": "new", "token": "CREATE-TOKEN-SECRET", "credentials_file": map[string]any{"TunnelSecret": "CREDS-SECRET"}}
	p.routes["GET "+tn] = []any{map[string]any{"id": tnID, "name": "new"}}
	p.routes["GET "+tn+"/"+tnID+"/token"] = "TUNNEL-TOKEN-SECRET"

	out := platRunOK(t, "", "tunnel", "create", "new", "--json")
	if strings.Contains(out, "SECRET") {
		t.Fatalf("create printed a secret: %s", out)
	}
	r := p.last(t, "POST", tn)
	if r.Body["config_src"] != "cloudflare" || len(platStr(r.Body["tunnel_secret"])) < 40 {
		t.Fatalf("create body: %s", r.RawBody)
	}
	cred := filepath.Join(t.TempDir(), "c.json")
	platRunOK(t, "", "tunnel", "create", "new", "--local", "--credentials-file", cred)
	r = p.last(t, "POST", tn)
	b, err := os.ReadFile(cred)
	st, _ := os.Stat(cred)
	var c map[string]string
	if err != nil || json.Unmarshal(b, &c) != nil || c["TunnelID"] != tnID || c["TunnelSecret"] != r.Body["tunnel_secret"] || c["AccountTag"] != acctID || st.Mode().Perm() != 0o600 {
		t.Fatalf("credentials file: %s (%v)", b, err)
	}

	dir := t.TempDir()
	rec := filepath.Join(dir, "calls")
	script := filepath.Join(dir, "cloudflared")
	_ = os.WriteFile(script, []byte("#!/bin/sh\necho \"args:$*\" >> "+rec+"\necho \"token:${TUNNEL_TOKEN}\" >> "+rec+"\n"), 0o755)
	t.Setenv("CFCTL_CLOUDFLARED", script)
	out = platRunOK(t, "", "tunnel", "run", "new", "--", "--loglevel", "debug")
	calls, _ := os.ReadFile(rec)
	if !strings.Contains(string(calls), "args:tunnel run --loglevel debug") || !strings.Contains(string(calls), "token:TUNNEL-TOKEN-SECRET") {
		t.Fatalf("cloudflared calls: %s", calls)
	}
	if strings.Contains(strings.SplitN(string(calls), "\n", 2)[0], "TUNNEL-TOKEN-SECRET") || strings.Contains(out, "TUNNEL-TOKEN-SECRET") {
		t.Fatal("token leaked into argv or output")
	}
	platRunOK(t, "", "tunnel", "quick-start", "localhost:8080")
	calls, _ = os.ReadFile(rec)
	if !strings.Contains(string(calls), "args:tunnel --url http://localhost:8080") {
		t.Fatalf("quick-start: %s", calls)
	}
	t.Setenv("CFCTL_CLOUDFLARED", filepath.Join(dir, "nope"))
	if msg := platRunErr(t, "", "tunnel", "quick-start", "localhost:1"); !strings.Contains(msg, "cloudflared not found") {
		t.Fatal(msg)
	}
	_ = http.StatusOK
}

func TestTurnstileVPCCert(t *testing.T) {
	p := platFake(t)
	ts := platA + "/challenges/widgets"
	widget := map[string]any{"sitekey": "0x4AAA", "secret": "0x4AAA-WIDGET-SECRET", "name": "signup", "mode": "managed", "domains": []any{"example.com"}, "region": "world", "created_on": "2026-01-01T00:00:00Z"}
	p.routes["GET "+ts] = []any{map[string]any{"sitekey": "0x4AAA", "name": "signup", "mode": "managed", "domains": []any{"example.com"}}}
	p.routes["GET "+ts+"/0x4AAA"] = func(fakeRequest) (int, any) { return 200, ok(widget) }
	p.routes["POST "+ts] = func(fakeRequest) (int, any) { return 200, ok(widget) }
	p.routes["PUT "+ts+"/0x4AAA"] = func(fakeRequest) (int, any) { return 200, ok(widget) }
	p.routes["POST "+ts+"/0x4AAA/rotate_secret"] = func(fakeRequest) (int, any) { return 200, ok(widget) }
	vpc := platA + "/connectivity/directory/services"
	p.routes["GET "+vpc] = []any{map[string]any{"service_id": "svc1", "name": "db", "type": "tcp", "tcp_port": 5432, "host": map[string]any{"ipv4": "10.0.0.5", "network": map[string]any{"tunnel_id": tnID}}}}
	mt := platA + "/mtls_certificates"
	p.routes["GET "+mt] = []any{map[string]any{"id": "c1", "name": "client", "ca": false, "issuer": "Me", "expires_on": "2027-01-01T00:00:00Z"}}
	p.routes["POST "+mt] = map[string]any{"id": "c2", "name": "up", "ca": false}
	dir := t.TempDir()
	certF, keyF := filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	_ = os.WriteFile(certF, []byte("-----BEGIN CERTIFICATE-----\nX\n-----END CERTIFICATE-----\n"), 0o600)
	_ = os.WriteFile(keyF, []byte("-----BEGIN PRIVATE KEY-----\nK\n-----END PRIVATE KEY-----\n"), 0o600)
	platCheck(t, p, []platCase{
		{args: []string{"turnstile", "widget", "list"}, method: "GET", path: ts, want: "signup"},
		{args: []string{"turnstile", "widget", "get", "0x4AAA"}, method: "GET", path: ts + "/0x4AAA", want: "redacted"},
		{args: []string{"turnstile", "widget", "create", "--name", "signup", "--domain", "example.com,www.example.com"}, method: "POST", path: ts,
			body: map[string]any{"name": "signup", "domains": []any{"example.com", "www.example.com"}, "mode": "managed"}},
		{args: []string{"turnstile", "widget", "update", "0x4AAA", "--mode", "invisible"}, method: "PUT", path: ts + "/0x4AAA",
			body: map[string]any{"name": "signup", "domains": []any{"example.com"}, "mode": "invisible", "region": "world"}},
		{args: []string{"turnstile", "widget", "delete", "0x4AAA", "--yes"}, method: "DELETE", path: ts + "/0x4AAA"},
		{args: []string{"turnstile", "widget", "rotate-secret", "0x4AAA", "--invalidate-immediately", "--yes"}, method: "POST", path: ts + "/0x4AAA/rotate_secret", body: map[string]any{"invalidate_immediately": true}},
		{args: []string{"vpc", "service", "list"}, method: "GET", path: vpc, want: "10.0.0.5"},
		{args: []string{"vpc", "service", "get", "svc1"}, method: "GET", path: vpc + "/svc1"},
		{args: []string{"vpc", "service", "create", "--name", "db", "--type", "tcp", "--tcp-port", "5432", "--app-protocol", "postgresql", "--ipv4", "10.0.0.5", "--tunnel-id", tnID}, method: "POST", path: vpc,
			body: map[string]any{"name": "db", "type": "tcp", "tcp_port": 5432, "app_protocol": "postgresql", "host": map[string]any{"ipv4": "10.0.0.5", "network": map[string]any{"tunnel_id": tnID}}}},
		{args: []string{"vpc", "service", "update", "svc1", "--name", "api", "--type", "http", "--hostname", "api.internal", "--resolver-ips", "10.0.0.2", "--tunnel-id", tnID, "--cert-verification-mode", "disabled"}, method: "PUT", path: vpc + "/svc1",
			body: map[string]any{"type": "http", "host": map[string]any{"hostname": "api.internal", "resolver_network": map[string]any{"tunnel_id": tnID, "resolver_ips": []any{"10.0.0.2"}}}, "tls_settings": map[string]any{"cert_verification_mode": "disabled"}}},
		{args: []string{"vpc", "service", "delete", "svc1", "--yes"}, method: "DELETE", path: vpc + "/svc1"},
		{args: []string{"cert", "list"}, method: "GET", path: mt, want: "client"},
		{args: []string{"cert", "get", "c1"}, method: "GET", path: mt + "/c1"},
		{args: []string{"cert", "associations", "c1"}, method: "GET", path: mt + "/c1/associations"},
		{args: []string{"cert", "delete", "c1", "--yes"}, method: "DELETE", path: mt + "/c1"},
		{args: []string{"cert", "upload", "mtls-certificate", "--cert", certF, "--key", keyF, "--name", "up"}, method: "POST", path: mt,
			body: map[string]any{"ca": false, "name": "up", "private_key": "-----BEGIN PRIVATE KEY-----\nK\n-----END PRIVATE KEY-----\n"}, want: "Uploaded up"},
		{args: []string{"cert", "upload", "certificate-authority", "--ca-cert", certF}, method: "POST", path: mt, body: map[string]any{"ca": true}},
		{args: []string{"mtls-certificate", "upload", "--cert", certF, "--key", keyF}, method: "POST", path: mt, body: map[string]any{"ca": false}},
		{args: []string{"mtls-certificate", "list"}, method: "GET", path: mt},
		{args: []string{"mtls-certificate", "delete", "c1", "--yes"}, method: "DELETE", path: mt + "/c1"},
	})
	for _, args := range [][]string{{"turnstile", "widget", "get", "0x4AAA", "--json"}, {"turnstile", "widget", "rotate-secret", "0x4AAA", "--yes"}, {"turnstile", "widget", "update", "0x4AAA", "--json"}} {
		if out := platRunOK(t, "", args...); strings.Contains(out, "WIDGET-SECRET") {
			t.Fatalf("%v printed the widget secret: %s", args, out)
		}
	}
	if out := platRunOK(t, "", "turnstile", "widget", "get", "0x4AAA", "--reveal"); !strings.Contains(out, "WIDGET-SECRET") {
		t.Fatal("--reveal")
	}
	if msg := platRunErr(t, "", "vpc", "service", "create", "--name", "x", "--type", "tcp", "--tunnel-id", tnID, "--ipv4", "nope"); !strings.Contains(msg, "invalid IPv4") {
		t.Fatal(msg)
	}
	if msg := platRunErr(t, "", "cert", "upload", "mtls-certificate", "--cert", certF); !strings.Contains(msg, "--key") {
		t.Fatal(msg)
	}
}
