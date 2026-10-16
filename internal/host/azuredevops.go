package host

import (
	"context"
	"fmt"
	"time"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/gitops"
)

// Azure DevOps settings.
const (
	AzureDevOpsTokenKey = "PLUMBIX_AZURE_DEVOPS_TOKEN" // name in the secrets file
	// AzureDevOpsExpiryKey names the optional expiry date (YYYY-MM-DD) in the
	// secrets file. Azure DevOps does not report a token's expiry to the
	// token's holder, so the user records the date chosen when creating it.
	AzureDevOpsExpiryKey = "PLUMBIX_AZURE_DEVOPS_TOKEN_EXPIRES"
	azureDevOpsUser      = "plumbix" // ignored by Azure DevOps when the password is a token
	azureDevOpsExpiryFmt = "2006-01-02"
)

// AzureDevOps authenticates with a personal access token.
type AzureDevOps struct {
	Token   string
	Expires string // the token's expiry date as YYYY-MM-DD; empty means unknown
}

// Kind implements Host.
func (a *AzureDevOps) Kind() string { return change.HostAzureDevOps }

// Auth implements Host.
func (a *AzureDevOps) Auth(change.Repo) (gitops.Auth, bool) {
	if a.Token == "" {
		return gitops.Auth{}, false
	}
	return gitops.Auth{User: azureDevOpsUser, Token: a.Token}, true
}

// TokenExpiry returns the expiry date recorded in the secrets file. It makes
// no network call: a rejected token surfaces as a fetch failure.
func (a *AzureDevOps) TokenExpiry(context.Context) (time.Time, bool, error) {
	if a.Token == "" || a.Expires == "" {
		return time.Time{}, false, nil
	}
	exp, err := time.Parse(azureDevOpsExpiryFmt, a.Expires)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("%s must be a date like 2026-12-31", AzureDevOpsExpiryKey)
	}
	return exp.UTC(), true, nil
}
