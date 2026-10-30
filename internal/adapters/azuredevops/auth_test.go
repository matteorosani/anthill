package azuredevops_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/adapters/azuredevops"
	"github.com/matteorosani/anthill/internal/adapters/clisession"
	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

// jwt builds an unsigned token shaped like an Entra ID access token that
// expires at exp.
func jwt(exp time.Time) string {
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"typ":"JWT","alg":"RS256"}`))
	payload := enc.EncodeToString(fmt.Appendf(nil, `{"aud":"499b84ac-1321-427f-aa17-267ca6975798","exp":%d}`, exp.Unix()))
	return header + "." + payload + ".c2lnbmF0dXJl"
}

// A PAT from the credential store is sent with Basic authentication and
// an empty user name, with the fed-auth redirect suppressed, and never
// logged (task 8.1).
func TestRequestsCarryThePAT(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("GET /contoso/_apis/connectionData", http.StatusOK, "connection_data.json")
	e.srv.fixture("GET "+reposPath, http.StatusOK, "repositories_one.json")
	if _, err := e.a.Repositories(ctx(t), shop); err != nil {
		t.Fatal(err)
	}
	got := e.srv.sent()
	if len(got) != 1 {
		t.Fatalf("sent %d requests", len(got))
	}
	h := got[0].Header
	if h.Get("Authorization") != basicPAT || h.Get("X-TFS-FedAuthRedirect") != "Suppress" || h.Get("User-Agent") != "anthill/1.2.3" {
		t.Errorf("headers = %v", h)
	}
	if !strings.Contains(got[0].Query, "api-version=7.1") {
		t.Errorf("query = %q, want api-version=7.1", got[0].Query)
	}
	e.noToken(t, nil, pat)
}

// An Entra ID token, such as the az session gives, is sent as a Bearer
// token, and read again two minutes before it expires.
func TestEntraTokenIsBearerAndRefreshed(t *testing.T) {
	e := newEnv(t)
	first, second := jwt(serverDate.Add(20*time.Minute)), jwt(serverDate.Add(80*time.Minute))
	key := domain.AzureDevOpsCredential("contoso")
	e.creds.Set(key, first)
	e.srv.fixture("GET "+reposPath, http.StatusOK, "repositories_one.json")
	if _, err := e.a.Repositories(ctx(t), shop); err != nil { // 10:00, valid until 10:20
		t.Fatal(err)
	}
	e.creds.Set(key, second)
	// 10:19 is within two minutes of the expiry, and after the ten
	// minutes the repository list is cached.
	e.clock.Advance(19 * time.Minute)
	if _, err := e.a.Repositories(ctx(t), shop); err != nil {
		t.Fatal(err)
	}
	got := e.srv.matching(http.MethodGet, reposPath)
	if len(got) != 2 {
		t.Fatalf("sent %d requests", len(got))
	}
	if got[0].Header.Get("Authorization") != "Bearer "+first || got[1].Header.Get("Authorization") != "Bearer "+second {
		t.Errorf("Authorization = %q then %q", got[0].Header.Get("Authorization"), got[1].Header.Get("Authorization"))
	}
	e.noToken(t, nil, first)
}

// With no token in the credential store, the az session is asked through
// app.Credentials, with the command runner faked (task 8.1).
func TestAzureCLISession(t *testing.T) {
	token := jwt(serverDate.Add(time.Hour))
	var ran []string
	az := clisession.New(func(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
		ran = append(ran, name+" "+strings.Join(args, " "))
		return []byte(token + "\n"), nil, nil
	})
	creds := app.NewCredentials(nil, &testkit.CredentialStore{}, az)
	e := newEnv(t, func(o *azuredevops.Options) { o.Credentials = creds })
	e.srv.fixture("GET "+reposPath, http.StatusOK, "repositories_one.json")
	if _, err := e.a.Repositories(ctx(t), shop); err != nil {
		t.Fatal(err)
	}
	if h := e.srv.sent()[0].Header.Get("Authorization"); h != "Bearer "+token {
		t.Errorf("Authorization = %q", h)
	}
	if len(ran) != 1 || !strings.HasPrefix(ran[0], "az account get-access-token --resource "+clisession.AzureDevOpsResource) {
		t.Errorf("ran %q", ran)
	}
	e.noToken(t, nil, token)
}

// Scenario "Token never logged": a request failing with an
// authentication error leaves no credential value in the error or the
// logs, even when Azure DevOps echoes it, and the token is read again
// for the next request.
func TestAuthenticationErrorHasNoToken(t *testing.T) {
	e := newEnv(t)
	e.srv.handle("GET "+itemPath, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprintf(w, `{"message":"TF400813: bad credential %s (%s)"}`, pat, r.Header.Get("Authorization"))
	})
	_, _, err := e.a.Item(ctx(t), ado12)
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), domain.Redacted) {
		t.Fatalf("err = %v", err)
	}
	e.noToken(t, err, pat)
	_, _, _ = e.a.Item(ctx(t), ado12)
	if n := len(e.creds.Lookups); n != 2 {
		t.Errorf("credential lookups = %d, want 2 (the 401 drops the token)", n)
	}
}

// A 203 sign-in page, which Azure DevOps serves for a bad PAT when it
// redirects, is an error, not an empty answer.
func TestSignInPageIsAnError(t *testing.T) {
	e := newEnv(t)
	e.srv.handle("GET "+itemPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusNonAuthoritativeInfo)
		_, _ = w.Write([]byte("<html>Sign in</html>"))
	})
	if _, _, err := e.a.Item(ctx(t), ado12); err == nil || !strings.Contains(err.Error(), "203") {
		t.Errorf("err = %v, want a 203 error", err)
	}
}

func TestMissingCredential(t *testing.T) {
	e := newEnv(t, func(o *azuredevops.Options) { o.Credentials = &testkit.CredentialStore{} })
	_, _, err := e.a.Item(ctx(t), ado12)
	if !errors.Is(err, ports.ErrNoCredential) {
		t.Errorf("err = %v, want ErrNoCredential", err)
	}
	if len(e.srv.sent()) != 0 {
		t.Errorf("sent %d requests without a token", len(e.srv.sent()))
	}
}

// Another organization's repositories and refs are refused before any
// request.
func TestOtherOrganizationRefused(t *testing.T) {
	e := newEnv(t)
	other := domain.Ref{Repo: domain.Repository{Provider: domain.AzureDevOps, Org: "fabrikam", Project: "shop"}, Kind: domain.KindItem, Number: 1}
	if _, _, err := e.a.Item(ctx(t), other); err == nil {
		t.Error("Item of another organization succeeded")
	}
	if _, err := e.a.Items(ctx(t), web, ""); err == nil {
		t.Error("Items of a code repository succeeded")
	}
	if len(e.srv.sent()) != 0 {
		t.Errorf("sent %d requests", len(e.srv.sent()))
	}
}
