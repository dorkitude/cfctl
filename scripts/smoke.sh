#!/usr/bin/env bash
# smoke.sh: read-only smoke test of cfctl's hand-written READ commands against
# the real Cloudflare account in ~/.config/cfctl.
#
# - Forces read-only mode (CFCTL_READONLY=1) and proves the guard works before
#   sending anything to Cloudflare. Nothing in the account is changed.
# - Discovers representative arguments at runtime (first zone, bucket, object,
#   Worker, ...). Where the account has no such resource, a placeholder name is
#   used and a friendly "not found" error is the expected result.
# - Runs every command in text mode and with --json; asserts exit codes, that
#   --json output parses, and that errors are friendly (no panics, no empty
#   output, no raw JSON bodies).
# - Classifies each run as PASS, EXPECTED-UNAVAILABLE (401/403/404, product not
#   enabled, token-type limits, missing resource), or FAIL; prints a summary
#   table and exits non-zero on any FAIL.
#
# Usage: scripts/smoke.sh            (or: make smoke)
# Env:   CFCTL=/path/to/cfctl        use this binary instead of building one (also CFCTL_BIN)
#        SMOKE_ZONE=example.com      zone to test against (default: the zone with the most DNS records)
#        SMOKE_DELAY=0.4             seconds to sleep between runs (rate limiting)
#        SMOKE_FILTER=regex          only run commands whose label matches
#        SMOKE_VERBOSE=1             print stderr for every non-PASS run
#        SMOKE_KEEP=dir              keep every run's stdout/stderr in dir
#        SMOKE_RESULTS=file.tsv      copy the per-run results (TSV) here
set -euo pipefail

export CFCTL_READONLY=1
if [ "${CFCTL_READONLY:-}" != 1 ]; then
	echo "smoke: refusing to run without CFCTL_READONLY=1" >&2
	exit 2
fi
unset CFCTL_API_BASE_URL

command -v jq >/dev/null || { echo "smoke: jq is required" >&2; exit 2; }

ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

CFCTL=${CFCTL:-${CFCTL_BIN:-}}
if [ -z "$CFCTL" ]; then
	echo "smoke: building cfctl..." >&2
	(cd "$ROOT" && go build -o "$WORK/cfctl" .)
	CFCTL="$WORK/cfctl"
fi
DELAY=${SMOKE_DELAY:-0.4}
FILTER=${SMOKE_FILTER:-}

# --- 1. Prove the read-only guard is on, against a closed local port (never Cloudflare).
guard_out=$(CFCTL_API_BASE_URL=http://127.0.0.1:9/client/v4 "$CFCTL" api request DELETE /zones/00000000000000000000000000000000 2>&1 || true)
if ! grep -q "read-only" <<<"$guard_out"; then
	echo "smoke: read-only guard self-check FAILED; refusing to touch the real account" >&2
	echo "$guard_out" >&2
	exit 2
fi
echo "smoke: read-only guard verified" >&2

cf() { "$CFCTL" "$@"; }
# first <jq-expr> <cfctl args...>: first value from a --json listing, or "".
first() {
	local expr=$1
	shift
	sleep "$DELAY"
	cf "$@" --json 2>/dev/null | jq -r "$expr" 2>/dev/null | grep -v '^null$' | head -n1 || true
}

NX=cfctl-smoke-nonexistent
NXID=00000000000000000000000000000000

# --- 2. Discover representative arguments.
echo "smoke: discovering resources..." >&2
ZONE=${SMOKE_ZONE:-}
if [ -z "$ZONE" ]; then
	best=0
	for z in $(cf zones list --json | jq -r '.[].name'); do
		n=$(cf records list "$z" --json 2>/dev/null | jq 'length' 2>/dev/null || echo 0)
		if [ "$n" -gt "$best" ]; then best=$n; ZONE=$z; fi
		sleep "$DELAY"
	done
fi
[ -n "$ZONE" ] || { echo "smoke: no zone found" >&2; exit 2; }

RECORD=$(first '.[0].id' records list "$ZONE")
SETTING=$(first '.[0].id' zones settings list "$ZONE")
DOMAIN=$(first '.[0].domain_name' domains list)
ACCOUNT=$(first '.[0].id' accounts list)
MEMBER=$(first '.[0].id' members list)
ROLE=$(first '.[0].id' roles list)
TOKEN_ID=$(first '.[0].id' tokens list)
POLICY=$(first '.[0].id' notifications policies list)
WORKER=$(first '.[0].id' workers list)
WORKER=${WORKER:-$NX}
VERSION=$(first '.[0].id' workers versions list "$WORKER")
VERSION=${VERSION:-$NXID}
MODEL=$(first '.[] | select(.task.name=="Text Generation") | .name' ai models list)
MODEL=${MODEL:-@cf/meta/llama-3.1-8b-instruct}
RULESET=$(first '.[0].id' rulesets list --zone "$ZONE")
RULESET=${RULESET:-$NXID}
PACK=$(first '.[0].id' ssl packs list "$ZONE")
PACK=${PACK:-$NXID}

# Bucket and object: the first bucket that has an object at the top level.
BUCKET="" OBJECT=""
for b in $(cf r2 buckets list --json 2>/dev/null | jq -r '.buckets[]?.name' 2>/dev/null); do
	[ -n "$BUCKET" ] || BUCKET=$b
	sleep "$DELAY"
	k=$(cf r2 ls "$b" --json 2>/dev/null | jq -r '.objects[0].key // empty' 2>/dev/null || true)
	if [ -n "$k" ]; then BUCKET=$b; OBJECT=$k; break; fi
done
BUCKET=${BUCKET:-$NX}
OBJECT=${OBJECT:-$NX}

KV=$(first '.[0].id' kv namespace list)
KV=${KV:-$NXID}
KVKEY=$(first '.[0].name' kv key list "$KV")
KVKEY=${KVKEY:-$NX}
D1=$(first '.[0].uuid' d1 list)
D1=${D1:-$NX}
QUEUE=$(first '.[0].queue_name' queues list)
QUEUE=${QUEUE:-$NX}
HYPERDRIVE=$(first '.[0].id' hyperdrive list)
HYPERDRIVE=${HYPERDRIVE:-$NXID}
VECTORIZE=$(first '.[0].name' vectorize list)
VECTORIZE=${VECTORIZE:-$NX}
STORE=$(first '.[0].id' secrets-store store list)
STORE=${STORE:-$NXID}
PAGES=$(first '.[0].name' pages project list)
PAGES=${PAGES:-$NX}
WORKFLOW=$(first '.[0].name' workflows list)
WORKFLOW=${WORKFLOW:-$NX}
TUNNEL=$(first '.[0].id' tunnel list)
TUNNEL=${TUNNEL:-$NXID}
LIST=$(first '.[0].id' lists list)
LIST=${LIST:-$NXID}
AISEARCH=$(first '.[0].id' ai-search list)
AISEARCH=${AISEARCH:-$NX}

cat >&2 <<EOF
smoke: zone=$ZONE record=${RECORD:-none} bucket=$BUCKET object=$OBJECT worker=$WORKER
       kv=$KV d1=$D1 queue=$QUEUE ruleset=$RULESET model=$MODEL
EOF

# --- 3. Run and classify.
RESULTS="$WORK/results.tsv"
: >"$RESULTS"
n=0

# classify <exit> <stdout> <stderr> <mode>: prints OUTCOME<TAB>reason
classify() {
	local rc=$1 out=$2 err=$3 mode=$4
	if grep -qE '^panic:|goroutine [0-9]+ \[|runtime error' "$err" "$out"; then
		printf 'FAIL\tpanic\n'; return
	fi
	if [ "$rc" -eq 0 ]; then
		if [ "$mode" = json ]; then
			if [ ! -s "$out" ]; then printf 'FAIL\tempty --json output\n'; return; fi
			if ! jq empty "$out" >/dev/null 2>&1; then printf 'FAIL\t--json output does not parse\n'; return; fi
			if [ "$(jq -c . "$out" 2>/dev/null | head -c 5)" = null ]; then printf 'FAIL\t--json printed null\n'; return; fi
		elif [ ! -s "$out" ] && [ ! -s "$err" ]; then
			printf 'FAIL\tno output\n'; return
		elif grep -qE '%!|<nil>|map\[|0001-01-01' "$out"; then
			printf 'FAIL\tGo formatting leaked into text output: %s\n' "$(grep -oE '.{0,20}(%!|<nil>|map\[|0001-01-01).{0,20}' "$out" | head -1 | tr '\t' ' ')"; return
		fi
		printf 'PASS\t\n'; return
	fi
	if [ "$rc" -eq 124 ]; then printf 'FAIL\ttimeout\n'; return; fi
	if [ ! -s "$err" ]; then printf 'FAIL\texit %s with no error message\n' "$rc"; return; fi
	if grep -qE '^\s*[{[]"|"errors":\[|"success":' "$err"; then printf 'FAIL\traw JSON in error\n'; return; fi
	if grep -qE 'HTTP 5[0-9][0-9]' "$err"; then printf 'FAIL\t%s\n' "$(grep -oE 'HTTP 5[0-9][0-9]' "$err" | head -1)"; return; fi
	if grep -qE 'read-only mode' "$err"; then
		# A few reads need a POST (e.g. a short-lived registry credential) and
		# are documented as refused in read-only mode.
		if [ -n "${READONLY_REFUSAL_OK:-}" ]; then printf 'EXPECTED-UNAVAILABLE\tneeds a POST; refused in read-only mode (documented)\n'; return; fi
		printf 'FAIL\tread command refused by read-only guard\n'; return
	fi
	if grep -qE 'HTTP (400|401|403|404|405|409|422)|not found|[Nn]o such|not enabled|not available|isn.t available|requires|not entitled|not allowed|[Uu]nauthorized|does not exist|not applicable|unknown (bucket|namespace|database|queue|index|store|worker|project)' "$err"; then
		printf 'EXPECTED-UNAVAILABLE\t%s\n' "$(grep -oE 'HTTP [0-9]{3}[^.;(]{0,60}|[^:]*not found[^.;]{0,40}' "$err" | head -1 | tr '\t' ' ')"
		return
	fi
	printf 'FAIL\tunclassified error: %s\n' "$(head -c 120 "$err" | tr '\n\t' '  ')"
}

# t <label> <cfctl args...>: run in text mode and with --json.
# MODES=text t ...: text mode only (downloads, whose output is the raw bytes).
t() {
	local label=$1
	shift
	if [ -n "$FILTER" ] && ! grep -qE "$FILTER" <<<"$label"; then return; fi
	local mode out err rc res
	for mode in ${MODES:-text json}; do
		out="$WORK/out" err="$WORK/err"
		local args=("$@")
		[ "$mode" = json ] && args+=(--json)
		set +e
		timeout 120 "$CFCTL" --no-color "${args[@]}" </dev/null >"$out" 2>"$err"
		rc=$?
		set -e
		res=$(classify "$rc" "$out" "$err" "$mode")
		if [ -n "${SMOKE_KEEP:-}" ]; then
			mkdir -p "$SMOKE_KEEP"
			cp "$out" "$SMOKE_KEEP/${label// /_}.$mode.out"
			cp "$err" "$SMOKE_KEEP/${label// /_}.$mode.err"
		fi
		printf '%s\t%s\t%s\t%s\n' "$label" "$mode" "$rc" "$res" >>"$RESULTS"
		n=$((n + 1))
		case $res in
		PASS*) printf '.' >&2 ;;
		EXPECTED*) printf 'e' >&2 ;;
		*) printf 'F' >&2 ;;
		esac
		if [ -n "${SMOKE_VERBOSE:-}" ] && [[ $res != PASS* ]]; then
			printf '\n[%s %s] rc=%s\n' "$label" "$mode" "$rc" >&2
			head -c 400 "$err" >&2
		fi
		sleep "$DELAY"
	done
}

cd "$WORK" # no wrangler config in the cwd
echo "smoke: running read commands (. pass, e expected-unavailable, F fail)" >&2

# identity, accounts, admin
t "whoami" whoami
t "auth status" auth status
t "accounts list" accounts list
t "accounts get" accounts get "$ACCOUNT"
t "members list" members list
t "members get" members get "${MEMBER:-$NXID}"
t "roles list" roles list
t "roles get" roles get "${ROLE:-$NXID}"
t "tokens list" tokens list
t "tokens get" tokens get "${TOKEN_ID:-$NXID}"
t "tokens verify" tokens verify
t "tokens permission-groups" tokens permission-groups
t "audit-logs list" audit-logs list
t "billing profile" billing profile
t "billing subscriptions" billing subscriptions
t "billing history" billing history
t "billing zone" billing zone "$ZONE"
t "user get" user get
t "user invites" user invites
t "user memberships" user memberships
t "notifications available" notifications available
t "notifications destinations" notifications destinations
t "notifications history" notifications history
t "notifications policies list" notifications policies list
t "notifications policies get" notifications policies get "${POLICY:-$NXID}"
t "logpush jobs list (account)" logpush jobs list
t "logpush jobs list (zone)" logpush jobs list --zone "$ZONE"
t "logpush jobs get" logpush jobs get 1 --zone "$ZONE"
t "logpush fields" logpush fields http_requests --zone "$ZONE"

# registrar, zones, DNS
t "domains list" domains list
t "domains get" domains get "${DOMAIN:-$ZONE}"
t "domains autorenew (show)" domains autorenew "${DOMAIN:-$ZONE}"
t "zones list" zones list
t "zones get" zones get "$ZONE"
t "zones file" zones file "$ZONE"
t "zones settings list" zones settings list "$ZONE"
t "zones settings get" zones settings get "$ZONE" "${SETTING:-ssl}"
t "records list" records list "$ZONE"
t "records get" records get "$ZONE" "${RECORD:-$NXID}"
t "dns dnssec status" dns dnssec status "$ZONE"
t "dns export" dns export "$ZONE"
t "dns settings get" dns settings get "$ZONE"

# zone config: cache, ssl, rules, security
t "cache settings" cache settings "$ZONE"
t "cache dev-mode (show)" cache dev-mode "$ZONE"
t "ssl mode (show)" ssl mode "$ZONE"
t "ssl status" ssl status "$ZONE"
t "ssl universal" ssl universal "$ZONE"
t "ssl verification" ssl verification "$ZONE"
t "ssl packs list" ssl packs list "$ZONE"
t "ssl packs get" ssl packs get "$ZONE" "$PACK"
t "ssl custom list" ssl custom list "$ZONE"
t "ssl custom get" ssl custom get "$ZONE" "$NXID"
t "ssl origin list" ssl origin list "$ZONE"
t "ssl origin get" ssl origin get "$NXID"
t "rulesets list (account)" rulesets list
t "rulesets list (zone)" rulesets list --zone "$ZONE"
t "rulesets get" rulesets get "$RULESET" --zone "$ZONE"
t "rulesets versions" rulesets versions "$RULESET" --zone "$ZONE"
t "rulesets phase" rulesets phase http_request_dynamic_redirect --zone "$ZONE"
t "redirects list" redirects list "$ZONE"
t "redirects bulk lists" redirects bulk lists
t "redirects bulk rules" redirects bulk rules
t "redirects bulk items" redirects bulk items "$NXID"
t "transform list" transform list "$ZONE"
t "waf overview" waf overview "$ZONE"
t "waf custom list" waf custom list "$ZONE"
t "waf managed" waf managed "$ZONE"
t "waf rate-limits" waf rate-limits "$ZONE"
t "page-rules list" page-rules list "$ZONE"
t "page-rules get" page-rules get "$ZONE" "$NXID"
t "firewall access-rules list (account)" firewall access-rules list
t "firewall access-rules list (zone)" firewall access-rules list --zone "$ZONE"
t "lists list" lists list
t "lists get" lists get "$LIST"
t "lists items" lists items "$LIST"
t "lists operation" lists operation "$NXID"
t "lb list" lb list "$ZONE"
t "lb get" lb get "$ZONE" "$NXID"
t "lb pools list" lb pools list
t "lb pools get" lb pools get "$NXID"
t "lb pools health" lb pools health "$NXID"
t "lb monitors list" lb monitors list
t "lb monitors get" lb monitors get "$NXID"
t "healthchecks list" healthchecks list "$ZONE"
t "healthchecks get" healthchecks get "$ZONE" "$NXID"
t "waiting-rooms list" waiting-rooms list "$ZONE"
t "waiting-rooms get" waiting-rooms get "$ZONE" "$NXID"
t "waiting-rooms status" waiting-rooms status "$ZONE" "$NXID"
t "waiting-rooms events" waiting-rooms events "$ZONE" "$NXID"
t "spectrum apps list" spectrum apps list "$ZONE"
t "spectrum apps get" spectrum apps get "$ZONE" "$NXID"
t "email routing list" email routing list
t "email routing settings" email routing settings "$ZONE"
t "email routing dns get" email routing dns get "$ZONE"
t "email routing catch-all get" email routing catch-all get "$ZONE"
t "email routing rules list" email routing rules list "$ZONE"
t "email routing rules get" email routing rules get "$ZONE" "$NXID"
t "email routing addresses list" email routing addresses list
t "email routing addresses get" email routing addresses get "$NXID"
t "email sending list" email sending list
t "email sending settings" email sending settings "$ZONE"
t "email sending dns get" email sending dns get "$ZONE"
t "turnstile widget list" turnstile widget list
t "turnstile widget get" turnstile widget get "$NX"
t "cert list" cert list
t "cert get" cert get "$NXID"
t "cert associations" cert associations "$NXID"
t "mtls-certificate list" mtls-certificate list
t "mtls-certificate get" mtls-certificate get "$NXID"
t "mtls-certificate associations" mtls-certificate associations "$NXID"
t "access organization" access organization
t "access apps list" access apps list
t "access apps get" access apps get "$NXID"
t "access policies list" access policies list
t "access policies get" access policies get "$NXID"
t "access groups list" access groups list
t "access groups get" access groups get "$NXID"
t "access idps list" access idps list
t "access idps get" access idps get "$NXID"
t "tunnel list" tunnel list
t "tunnel info" tunnel info "$TUNNEL"
t "tunnel connections" tunnel connections "$TUNNEL"
t "tunnel config get" tunnel config get "$TUNNEL"
t "tunnel route list" tunnel route list
t "tunnel vnet list" tunnel vnet list
t "vpc service list" vpc service list
t "vpc service get" vpc service get "$NXID"

# analytics (GraphQL)
t "analytics zone" analytics zone "$ZONE"
t "analytics paths" analytics paths "$ZONE"
t "analytics countries" analytics countries "$ZONE"
t "analytics firewall" analytics firewall "$ZONE"
t "analytics workers" analytics workers
t "analytics r2" analytics r2

# Workers
t "workers list" workers list
t "workers get" workers get "$WORKER"
t "workers subdomain get" workers subdomain get
t "workers dev-url get" workers dev-url get "$WORKER"
t "workers routes list" workers routes list
t "workers routes list (zone)" workers routes list --zone "$ZONE"
t "workers domains list" workers domains list
t "workers crons list" workers crons list "$WORKER"
t "workers triggers list" workers triggers list "$WORKER"
t "workers secret list" workers secret list --name "$WORKER"
t "workers versions list" workers versions list "$WORKER"
t "workers versions view" workers versions view "$VERSION" --name "$WORKER"
t "workers versions secret list" workers versions secret list --name "$WORKER"
t "workers deployments list" workers deployments list "$WORKER"
t "workers deployments status" workers deployments status "$WORKER"
t "workers preview list" workers preview list "$WORKER"
t "workers preview get" workers preview get --name "$WORKER" --preview "$NX"
t "workers preview deployments" workers preview deployments --name "$WORKER" --preview "$NX"
t "workers preview secret list" workers preview secret list --name "$WORKER" --preview "$NX"
t "workers preview base-config secret list" workers preview base-config secret list --name "$WORKER"
t "workers dispatch-namespace list" workers dispatch-namespace list
t "workers dispatch-namespace get" workers dispatch-namespace get "$NX"
t "workers dispatch-namespace scripts" workers dispatch-namespace scripts "$NX"
t "deployments list (alias)" deployments list "$WORKER"
t "versions list (alias)" versions list "$WORKER"
t "triggers list (alias)" triggers list "$WORKER"
t "secret list (alias)" secret list --name "$WORKER"
t "workflows list" workflows list
t "workflows describe" workflows describe "$WORKFLOW"
t "workflows instances list" workflows instances list "$WORKFLOW"
t "workflows versions list" workflows versions list "$WORKFLOW"

# storage
t "r2 buckets list" r2 buckets list
t "r2 buckets get" r2 buckets get "$BUCKET"
t "r2 buckets cors list" r2 buckets cors list "$BUCKET"
t "r2 buckets dev-url get" r2 buckets dev-url get "$BUCKET"
t "r2 buckets domain list" r2 buckets domain list "$BUCKET"
t "r2 buckets lifecycle list" r2 buckets lifecycle list "$BUCKET"
t "r2 buckets lock list" r2 buckets lock list "$BUCKET"
t "r2 buckets notification list" r2 buckets notification list "$BUCKET"
t "r2 buckets sippy get" r2 buckets sippy get "$BUCKET"
t "r2 buckets local-uploads get" r2 buckets local-uploads get "$BUCKET"
t "r2 buckets jobs list" r2 buckets jobs list "$BUCKET"
t "r2 buckets catalog list" r2 buckets catalog list
t "r2 buckets catalog get" r2 buckets catalog get "$BUCKET"
t "r2 buckets catalog credential status" r2 buckets catalog credential status "$BUCKET"
t "r2 buckets catalog maintenance get" r2 buckets catalog maintenance get "$BUCKET"
t "r2 buckets catalog namespaces" r2 buckets catalog namespaces "$BUCKET"
t "r2 ls" r2 ls "$BUCKET"
t "r2 du" r2 du "$BUCKET"
t "r2 stat" r2 stat "$BUCKET/$OBJECT"
t "r2 object stat" r2 object stat "$BUCKET/$OBJECT"
MODES=text t "r2 get" r2 get "$BUCKET/$OBJECT" --pipe
t "r2 usage" r2 usage
t "basin catalog list" basin catalog list
t "basin pipelines list" basin pipelines list
t "basin pipelines streams list" basin pipelines streams list
t "basin pipelines sinks list" basin pipelines sinks list
t "kv namespace list" kv namespace list
t "kv namespace get" kv namespace get "$KV"
t "kv key list" kv key list "$KV"
t "kv key get" kv key get "$KV" "$KVKEY"
t "d1 list" d1 list
t "d1 info" d1 info "$D1"
t "d1 insights" d1 insights "$D1"
t "d1 time-travel info" d1 time-travel info "$D1"
t "d1 migrations list" d1 migrations list "$D1"
t "queues list" queues list
t "queues get" queues get "$QUEUE"
t "queues consumer list" queues consumer list "$QUEUE"
t "queues metrics" queues metrics "$QUEUE"
t "queues purge status" queues purge status "$QUEUE"
t "queues subscription list" queues subscription list
t "queues subscription get" queues subscription get "$NXID"
t "hyperdrive list" hyperdrive list
t "hyperdrive get" hyperdrive get "$HYPERDRIVE"
t "vectorize list" vectorize list
t "vectorize get" vectorize get "$VECTORIZE"
t "vectorize info" vectorize info "$VECTORIZE"
t "vectorize list-vectors" vectorize list-vectors "$VECTORIZE"
t "vectorize metadata-index list" vectorize metadata-index list "$VECTORIZE"
t "secrets-store store list" secrets-store store list
t "secrets-store store get" secrets-store store get "$STORE"
t "secrets-store secret list" secrets-store secret list "$STORE"
t "secrets-store quota" secrets-store quota
t "k2 streams list" k2 streams list
t "k2 streams get" k2 streams get "$NX"
t "pages project list" pages project list
t "pages project get" pages project get "$PAGES"
t "pages deployment list" pages deployment list "$PAGES"
t "pages domains list" pages domains list "$PAGES"
t "pages secret list" pages secret list "$PAGES"
t "pages download config" pages download config "$PAGES"

# AI and platform products
t "ai models list" ai models list
t "ai models get" ai models get "$MODEL"
t "ai models schema" ai models schema "$MODEL"
t "ai models search" ai models search llama
t "ai tasks" ai tasks
t "ai authors" ai authors
t "ai finetune list" ai finetune list
t "ai finetune public" ai finetune public
t "ai-search list" ai-search list
t "ai-search get" ai-search get "$AISEARCH"
t "ai-search stats" ai-search stats "$AISEARCH"
t "ai-search jobs list" ai-search jobs list "$AISEARCH"
t "ai-search items list" ai-search items list "$AISEARCH"
t "ai-search namespace list" ai-search namespace list
t "ai-search tokens list" ai-search tokens list
t "agent-memory namespace list" agent-memory namespace list
t "agent-memory namespace get" agent-memory namespace get "$NX"
t "artifacts namespaces list" artifacts namespaces list
t "artifacts namespaces get" artifacts namespaces get "$NX"
t "artifacts repos list" artifacts repos list "$NX"
t "browser list" browser list
t "browser get" browser get "$NXID"
t "containers list" containers list
t "containers info" containers info "$NX"
READONLY_REFUSAL_OK=1 t "containers images list" containers images list
t "containers registries list" containers registries list
t "flagship apps list" flagship apps list
t "flagship apps get" flagship apps get "$NXID"
t "flagship flags list" flagship flags list "$NXID"
t "dispatch-namespace list" dispatch-namespace list

# --- 4. Summary.
echo >&2
pass=$(awk -F'\t' '$4=="PASS"' "$RESULTS" | wc -l)
exp=$(awk -F'\t' '$4=="EXPECTED-UNAVAILABLE"' "$RESULTS" | wc -l)
fail=$(awk -F'\t' '$4=="FAIL"' "$RESULTS" | wc -l)
cmds=$(cut -f1 "$RESULTS" | sort -u | wc -l)

echo
printf '%-48s %-5s %-4s %-22s %s\n' COMMAND MODE EXIT OUTCOME DETAIL
printf '%-48s %-5s %-4s %-22s %s\n' ------- ---- ---- ------- ------
awk -F'\t' '{ printf "%-48s %-5s %-4s %-22s %s\n", $1, $2, $3, $4, substr($5, 1, 70) }' "$RESULTS"
echo
echo "zone: $ZONE   commands: $cmds   runs: $n   PASS: $pass   EXPECTED-UNAVAILABLE: $exp   FAIL: $fail"
if [ -n "${SMOKE_RESULTS:-}" ]; then cp "$RESULTS" "$SMOKE_RESULTS"; fi
[ "$fail" -eq 0 ]
