package middleware_test

import (
	"context"
	"crypto"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"home-os/api/internal/auth"
	"home-os/api/internal/middleware"
)

// recordingResolver records the household requested through the context and
// answers with a fixed result.
type recordingResolver struct {
	gotRequested string
	err          error
}

func (r *recordingResolver) Resolve(ctx context.Context, _, _ string) (*auth.ResolvedMember, error) {
	r.gotRequested = auth.RequestedHousehold(ctx)
	if r.err != nil {
		return nil, r.err
	}
	hid := r.gotRequested
	if hid == "" {
		hid = "default-household"
	}
	return &auth.ResolvedMember{UserID: uuid.NewString(), HouseholdID: hid, Role: "family_member"}, nil
}

func TestRequireAuth_HouseholdHeader(t *testing.T) {
	s := newSigner(t)
	ks := &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{s.key.Public()}}
	wanted := uuid.NewString()

	cases := []struct {
		name          string
		header        string
		resolverErr   error
		wantStatus    int
		wantRequested string
		wantHousehold string
	}{
		{name: "no header uses default", wantStatus: http.StatusOK, wantHousehold: "default-household"},
		{name: "member header selects household", header: wanted, wantStatus: http.StatusOK,
			wantRequested: wanted, wantHousehold: wanted},
		{name: "header is trimmed and normalised", header: "  " + wanted + " ", wantStatus: http.StatusOK,
			wantRequested: wanted, wantHousehold: wanted},
		{name: "non-member header is 403", header: wanted, resolverErr: auth.ErrNotHouseholdMember,
			wantStatus: http.StatusForbidden, wantRequested: wanted},
		{name: "malformed header is 400", header: "not-a-uuid", wantStatus: http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := &recordingResolver{err: tc.resolverErr}
			v := auth.NewVerifierWithKeySet(testIssuer, ks, []string{"home-os-ui"}).WithResolver(res)
			var gotHousehold string
			h := middleware.RequireAuth(v)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotHousehold = middleware.ClaimsFromContext(r.Context()).HouseholdID
				w.WriteHeader(http.StatusOK)
			}))
			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			req.Header.Set("Authorization", "Bearer "+s.token(t, "sub-1", "a@example.test", "home-os-ui"))
			if tc.header != "" {
				req.Header.Set(auth.HouseholdHeader, tc.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			require.Equal(t, tc.wantStatus, rec.Code, rec.Body.String())
			assert.Equal(t, tc.wantRequested, res.gotRequested)
			assert.Equal(t, tc.wantHousehold, gotHousehold)
		})
	}
}

// Agent tokens (hos_...) are verified only by the MCP server. The REST API
// refuses them outright, so the header cannot move one to another household.
func TestRequireAuth_AgentTokenWithHouseholdHeaderIsRefused(t *testing.T) {
	s := newSigner(t)
	ks := &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{s.key.Public()}}
	res := &recordingResolver{}
	v := auth.NewVerifierWithKeySet(testIssuer, ks, []string{"home-os-ui"}).WithResolver(res)
	h := middleware.RequireAuth(v)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("handler must not run for an agent token")
	}))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer hos_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	req.Header.Set(auth.HouseholdHeader, uuid.NewString())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Empty(t, res.gotRequested, "resolver must not be reached")
}
