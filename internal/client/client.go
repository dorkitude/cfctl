package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/accounts"
	"github.com/cloudflare/cloudflare-go/v7/option"
	"github.com/cloudflare/cloudflare-go/v7/user"
	"github.com/cloudflare/cloudflare-go/v7/zones"
	"github.com/dorkitude/cfctl/internal/config"
)

// App holds the authenticated Cloudflare client and resolved account ID.
type App struct {
	Client    *cloudflare.Client
	AccountID string
	token     string
}

// VerifyToken verifies the App's token with the endpoint matching its type.
func (a *App) VerifyToken(ctx context.Context) (*TokenInfo, error) {
	return VerifyToken(ctx, a.Client, a.token, []string{a.AccountID}, config.TokenType())
}

// NewClient builds a Cloudflare SDK client that authenticates with token.
//
// The SDK also reads CLOUDFLARE_API_KEY / CLOUDFLARE_EMAIL from the
// environment; those headers are stripped so only the bearer token is used.
func NewClient(token string) *cloudflare.Client {
	opts := []option.RequestOption{
		option.WithHeaderDel("X-Auth-Key"),
		option.WithHeaderDel("X-Auth-Email"),
		option.WithHeaderDel("X-Auth-User-Service-Key"),
		option.WithAPIToken(token),
	}
	if base := config.APIBaseURL(); base != "" {
		opts = append(opts, option.WithBaseURL(base))
	}
	return cloudflare.NewClient(opts...)
}

// New creates an App from the stored (or env) token and the effective
// account ID. If no account ID is known and the token sees exactly one
// account, that account is used and cached.
func New(ctx context.Context) (*App, error) {
	token, err := config.LoadToken()
	if err != nil {
		return nil, err
	}
	c := NewClient(token)

	accountID := config.AccountID()
	if accountID == "" {
		accts, err := ListAccounts(ctx, c)
		if err != nil {
			return nil, fmt.Errorf("failed to identify account: %w", err)
		}
		switch len(accts) {
		case 0:
			return nil, fmt.Errorf("this token cannot see any accounts (needs Account Settings: Read)")
		case 1:
			accountID = accts[0].ID
			cfg, _ := config.Load()
			if cfg == nil {
				cfg = &config.Config{}
			}
			cfg.AccountID = accountID
			_ = config.Save(cfg)
		default:
			return nil, fmt.Errorf("token can see %d accounts; pass --account <id> or re-run 'cfctl auth login'", len(accts))
		}
	}

	return &App{Client: c, AccountID: accountID, token: token}, nil
}

// TokenInfo describes a verified API token. It never holds the token itself.
type TokenInfo struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	ExpiresOn time.Time `json:"expires_on,omitempty"`
	// Type is config.TokenTypeUser or config.TokenTypeAccount.
	Type string `json:"type"`
	// AccountID is the owning account, for account-owned tokens.
	AccountID string `json:"account_id,omitempty"`
}

// AccountTokenPrefix marks account-owned API tokens.
const AccountTokenPrefix = "cfat_"

// ValidateToken verifies a token and works out whether it is a user token or
// an account-owned token. See VerifyToken.
func ValidateToken(ctx context.Context, token string, candidates []string, hint string) (*TokenInfo, error) {
	return VerifyToken(ctx, NewClient(token), token, candidates, hint)
}

// VerifyToken verifies a token against the right endpoint:
//
//   - user tokens:    GET /user/tokens/verify
//   - account tokens: GET /accounts/{id}/tokens/verify
//
// The user endpoint is tried first, unless the token starts with cfat_ or hint
// says "account". If it rejects the token (401), each candidate account ID is
// tried; with no candidates, the accounts the token can list are used.
func VerifyToken(ctx context.Context, c *cloudflare.Client, token string, candidates []string, hint string) (*TokenInfo, error) {
	var userErr error
	if hint != config.TokenTypeAccount && !strings.HasPrefix(token, AccountTokenPrefix) {
		resp, err := c.User.Tokens.Verify(ctx)
		if err == nil {
			if resp.Status != user.TokenVerifyResponseStatusActive {
				return nil, fmt.Errorf("token is %s, not active", resp.Status)
			}
			return &TokenInfo{ID: resp.ID, Status: string(resp.Status), ExpiresOn: resp.ExpiresOn, Type: config.TokenTypeUser}, nil
		}
		if StatusCode(err) != http.StatusUnauthorized && StatusCode(err) != http.StatusForbidden {
			return nil, fmt.Errorf("invalid token: %w", APIError(err))
		}
		userErr = err
	}

	ids := dedupe(candidates)
	if len(ids) == 0 {
		if accts, err := ListAccounts(ctx, c); err == nil {
			for _, a := range accts {
				ids = append(ids, a.ID)
			}
		}
	}

	var acctErr error
	for _, id := range ids {
		resp, err := c.Accounts.Tokens.Verify(ctx, accounts.TokenVerifyParams{AccountID: cloudflare.F(id)})
		if err != nil {
			acctErr = err
			continue
		}
		if resp.Status != accounts.TokenVerifyResponseStatusActive {
			return nil, fmt.Errorf("account token is %s, not active", resp.Status)
		}
		return &TokenInfo{ID: resp.ID, Status: string(resp.Status), ExpiresOn: resp.ExpiresOn, Type: config.TokenTypeAccount, AccountID: id}, nil
	}

	var tried []string
	if userErr != nil {
		tried = append(tried, "/user/tokens/verify ("+APIError(userErr).Error()+")")
	}
	switch {
	case acctErr != nil:
		tried = append(tried, "/accounts/{id}/tokens/verify ("+APIError(acctErr).Error()+")")
	case len(ids) == 0:
		tried = append(tried, "/accounts/{id}/tokens/verify (no account ID known)")
	}
	return nil, fmt.Errorf("invalid token: rejected by %s; for an account-owned token (%s...), pass --account <account-id>",
		strings.Join(tried, " and "), AccountTokenPrefix)
}

func dedupe(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// ListAccounts returns every account the token can see (GET /accounts).
func ListAccounts(ctx context.Context, c *cloudflare.Client) ([]accounts.Account, error) {
	var out []accounts.Account
	iter := c.Accounts.ListAutoPaging(ctx, accounts.AccountListParams{})
	for iter.Next() {
		out = append(out, iter.Current())
	}
	if err := iter.Err(); err != nil {
		return nil, APIError(err)
	}
	return out, nil
}

var zoneIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ResolveZone accepts a zone name (example.com) or a 32-char zone ID and
// returns the zone.
func ResolveZone(ctx context.Context, app *App, nameOrID string) (*zones.Zone, error) {
	nameOrID = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(nameOrID)), ".")
	if zoneIDPattern.MatchString(nameOrID) {
		z, err := app.Client.Zones.Get(ctx, zones.ZoneGetParams{ZoneID: cloudflare.F(nameOrID)})
		if err != nil {
			if IsNotFound(err) {
				return nil, fmt.Errorf("zone '%s' not found", nameOrID)
			}
			return nil, APIError(err)
		}
		return z, nil
	}

	params := zones.ZoneListParams{Name: cloudflare.F(nameOrID)}
	if app.AccountID != "" {
		params.Account = cloudflare.F(zones.ZoneListParamsAccount{ID: cloudflare.F(app.AccountID)})
	}
	page, err := app.Client.Zones.List(ctx, params)
	if err != nil {
		return nil, APIError(err)
	}
	for i := range page.Result {
		if strings.EqualFold(page.Result[i].Name, nameOrID) {
			return &page.Result[i], nil
		}
	}
	return nil, fmt.Errorf("zone '%s' not found in this Cloudflare account", nameOrID)
}

// StatusCode returns the HTTP status of a Cloudflare API error, or 0.
func StatusCode(err error) int {
	var apiErr *cloudflare.Error
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode
	}
	return 0
}

// IsNotFound reports whether err is an API 404.
func IsNotFound(err error) bool {
	return StatusCode(err) == http.StatusNotFound
}

// Messages returns the API error messages from a Cloudflare error, joined.
func Messages(err error) string {
	var apiErr *cloudflare.Error
	if !errors.As(err, &apiErr) {
		return err.Error()
	}
	var msgs []string
	for _, e := range apiErr.Errors {
		if e.Code != 0 {
			msgs = append(msgs, fmt.Sprintf("%s (code %d)", e.Message, e.Code))
		} else if e.Message != "" {
			msgs = append(msgs, e.Message)
		}
	}
	if len(msgs) == 0 {
		return http.StatusText(apiErr.StatusCode)
	}
	return strings.Join(msgs, "; ")
}

// APIError turns an SDK error into a short "HTTP 403: message" error.
// Request headers (and so the token) are never included.
func APIError(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *cloudflare.Error
	if errors.As(err, &apiErr) {
		return fmt.Errorf("HTTP %d: %s", apiErr.StatusCode, Messages(err))
	}
	return err
}
