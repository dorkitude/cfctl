package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmailRoutingCommands(t *testing.T) {
	p := platFake(t)
	z := "/zones/" + zoneID + "/email/routing"
	p.routes["GET "+platA+"/email/routing/zones"] = []any{map[string]any{"name": "example.com", "enabled": true, "status": "ready", "id": zoneID}}
	p.routes["GET "+z] = map[string]any{"name": "example.com", "enabled": true, "status": "ready"}
	p.routes["GET "+z+"/rules"] = []any{map[string]any{"id": "r1", "name": "hi", "enabled": true, "matchers": []any{map[string]any{"type": "literal", "field": "to", "value": "hi@example.com"}}, "actions": []any{map[string]any{"type": "forward", "value": []any{"me@gmail.com"}}}}}
	p.routes["GET "+z+"/dns"] = []any{map[string]any{"type": "MX", "name": "example.com", "content": "route1.mx.cloudflare.net", "priority": 10}}
	p.routes["GET "+platA+"/email/routing/addresses"] = []any{map[string]any{"id": "a1", "email": "me@gmail.com", "verified": "2026-01-01T00:00:00Z"}}
	platCheck(t, p, []platCase{
		{args: []string{"email", "routing", "list"}, method: "GET", path: platA + "/email/routing/zones", want: "example.com"},
		{args: []string{"email", "routing", "settings", "example.com"}, method: "GET", path: z, want: "ready"},
		{args: []string{"email", "routing", "enable", "example.com"}, method: "POST", path: z + "/enable"},
		{args: []string{"email", "routing", "disable", "example.com", "--yes"}, method: "POST", path: z + "/disable"},
		{args: []string{"email", "routing", "rules", "list", "example.com"}, method: "GET", path: z + "/rules", want: "me@gmail.com"},
		{args: []string{"email", "routing", "rules", "get", "example.com", "r1"}, method: "GET", path: z + "/rules/r1"},
		{args: []string{"email", "routing", "rules", "create", "example.com", "--to", "hi@example.com", "--forward", "me@gmail.com", "--name", "hi"}, method: "POST", path: z + "/rules",
			body: map[string]any{"name": "hi", "enabled": true, "matchers": []any{map[string]any{"type": "literal", "field": "to", "value": "hi@example.com"}}, "actions": []any{map[string]any{"type": "forward", "value": []any{"me@gmail.com"}}}}},
		{args: []string{"email", "routing", "rules", "update", "example.com", "r1", "--to", "bot@example.com", "--worker", "inbox", "--disabled"}, method: "PUT", path: z + "/rules/r1",
			body: map[string]any{"enabled": false, "actions": []any{map[string]any{"type": "worker", "value": []any{"inbox"}}}}},
		{args: []string{"email", "routing", "rules", "delete", "example.com", "r1", "--yes"}, method: "DELETE", path: z + "/rules/r1"},
		{args: []string{"email", "routing", "catch-all", "get", "example.com"}, method: "GET", path: z + "/rules/catch_all"},
		{args: []string{"email", "routing", "catch-all", "set", "example.com", "--drop"}, method: "PUT", path: z + "/rules/catch_all",
			body: map[string]any{"enabled": true, "matchers": []any{map[string]any{"type": "all"}}, "actions": []any{map[string]any{"type": "drop"}}}},
		{args: []string{"email", "routing", "addresses", "list"}, method: "GET", path: platA + "/email/routing/addresses", want: "me@gmail.com"},
		{args: []string{"email", "routing", "addresses", "get", "a1"}, method: "GET", path: platA + "/email/routing/addresses/a1"},
		{args: []string{"email", "routing", "addresses", "create", "you@gmail.com"}, method: "POST", path: platA + "/email/routing/addresses", body: map[string]any{"email": "you@gmail.com"}, want: "verification"},
		{args: []string{"email", "routing", "addresses", "delete", "a1", "--yes"}, method: "DELETE", path: platA + "/email/routing/addresses/a1"},
		{args: []string{"email", "routing", "dns", "get", "example.com"}, method: "GET", path: z + "/dns", want: "route1.mx.cloudflare.net"},
		{args: []string{"email", "routing", "dns", "unlock", "example.com"}, method: "POST", path: z + "/unlock"},
	})
	if msg := platRunErr(t, "", "email", "routing", "rules", "create", "example.com", "--to", "a@example.com"); !strings.Contains(msg, "exactly one action") {
		t.Fatal(msg)
	}
	if msg := platRunErr(t, "", "email", "routing", "settings", "nope.org"); !strings.Contains(msg, "not found") {
		t.Fatal(msg)
	}
}

func TestEmailSendingCommands(t *testing.T) {
	p := platFake(t)
	z := "/zones/" + zoneID + "/email/sending/subdomains"
	sub := map[string]any{"tag": "sd1", "name": "mail.example.com", "enabled": true, "dkim_selector": "cf1"}
	p.routes["GET "+z] = []any{sub}
	p.routes["GET "+platA+"/email/sending/zones"] = []any{map[string]any{"name": "example.com", "enabled": true}}
	p.routes["GET "+z+"/sd1/dns"] = []any{map[string]any{"type": "TXT", "name": "cf1._domainkey.mail.example.com", "content": "v=DKIM1"}}
	p.routes["POST "+platA+"/email/sending/send"] = map[string]any{"delivered": []any{"you@example.org"}, "queued": []any{}, "permanent_bounces": []any{}}
	p.routes["POST "+platA+"/email/sending/send_raw"] = map[string]any{"delivered": []any{}, "queued": []any{"q@example.org"}, "permanent_bounces": []any{}}
	att := filepath.Join(t.TempDir(), "a.txt")
	_ = os.WriteFile(att, []byte("hi"), 0o600)
	mimeFile := filepath.Join(t.TempDir(), "m.eml")
	_ = os.WriteFile(mimeFile, []byte("Subject: x\r\n\r\nbody"), 0o600)
	platCheck(t, p, []platCase{
		{args: []string{"email", "sending", "list"}, method: "GET", path: platA + "/email/sending/zones", want: "example.com"},
		{args: []string{"email", "sending", "list", "example.com"}, method: "GET", path: z, want: "mail.example.com"},
		// mail.example.com isn't a zone, so the lookup walks up to example.com.
		{args: []string{"email", "sending", "settings", "mail.example.com"}, method: "GET", path: z, want: "cf1"},
		{args: []string{"email", "sending", "enable", "news.example.com"}, method: "POST", path: z, body: map[string]any{"name": "news.example.com"}},
		{args: []string{"email", "sending", "disable", "mail.example.com", "--yes"}, method: "DELETE", path: z + "/sd1"},
		{args: []string{"email", "sending", "dns", "get", "mail.example.com"}, method: "GET", path: z + "/sd1/dns", want: "v=DKIM1"},
		{args: []string{"email", "sending", "send", "--from", "me@mail.example.com", "--from-name", "Me", "--to", "you@example.org", "--subject", "Hi", "--text", "Hello",
			"--header", "X-Tag: 1", "--attachment", att}, method: "POST", path: platA + "/email/sending/send",
			body: map[string]any{"from": map[string]any{"address": "me@mail.example.com", "name": "Me"}, "to": "you@example.org", "subject": "Hi", "text": "Hello", "headers": map[string]any{"X-Tag": "1"},
				"attachments": []any{map[string]any{"content": "aGk=", "filename": "a.txt", "type": "text/plain", "disposition": "attachment"}}},
			want: "Delivered to: you@example.org"},
		{args: []string{"email", "sending", "send-raw", "--from", "me@mail.example.com", "--to", "q@example.org", "--mime-file", mimeFile}, method: "POST", path: platA + "/email/sending/send_raw",
			body: map[string]any{"from": "me@mail.example.com", "recipients": []any{"q@example.org"}, "mime_message": "Subject: x\r\n\r\nbody"}, want: "Queued for"},
	})
	if msg := platRunErr(t, "", "email", "sending", "send", "--from", "a@b.c", "--to", "d@e.f", "--subject", "s"); !strings.Contains(msg, "--text or --html") {
		t.Fatal(msg)
	}
	if msg := platRunErr(t, "", "email", "sending", "settings", "other.example.com"); !strings.Contains(msg, "isn't enabled") {
		t.Fatal(msg)
	}
}
