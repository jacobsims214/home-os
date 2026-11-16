package dex

import (
	"context"
	"errors"
	"testing"

	dexapi "github.com/dexidp/dex/api/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// fakeAPI stands in for Dex's gRPC API. Only the password and refresh calls
// are implemented; any other call panics on the nil embedded interface.
type fakeAPI struct {
	dexapi.DexClient
	createResp *dexapi.CreatePasswordResp
	createErr  error
	updateResp *dexapi.UpdatePasswordResp
	updateErr  error
	verifyResp *dexapi.VerifyPasswordResp
	verifyErr  error
	listResp   *dexapi.ListRefreshResp
	listErr    error
	revokeResp *dexapi.RevokeRefreshResp
	revokeErr  error

	created  *dexapi.CreatePasswordReq
	updated  *dexapi.UpdatePasswordReq
	verified *dexapi.VerifyPasswordReq
	listed   *dexapi.ListRefreshReq
	revoked  []*dexapi.RevokeRefreshReq
}

func (f *fakeAPI) VerifyPassword(_ context.Context, in *dexapi.VerifyPasswordReq, _ ...grpc.CallOption) (*dexapi.VerifyPasswordResp, error) {
	f.verified = in
	return f.verifyResp, f.verifyErr
}

func (f *fakeAPI) ListRefresh(_ context.Context, in *dexapi.ListRefreshReq, _ ...grpc.CallOption) (*dexapi.ListRefreshResp, error) {
	f.listed = in
	return f.listResp, f.listErr
}

func (f *fakeAPI) RevokeRefresh(_ context.Context, in *dexapi.RevokeRefreshReq, _ ...grpc.CallOption) (*dexapi.RevokeRefreshResp, error) {
	f.revoked = append(f.revoked, in)
	return f.revokeResp, f.revokeErr
}

func (f *fakeAPI) CreatePassword(_ context.Context, in *dexapi.CreatePasswordReq, _ ...grpc.CallOption) (*dexapi.CreatePasswordResp, error) {
	f.created = in
	return f.createResp, f.createErr
}

func (f *fakeAPI) UpdatePassword(_ context.Context, in *dexapi.UpdatePasswordReq, _ ...grpc.CallOption) (*dexapi.UpdatePasswordResp, error) {
	f.updated = in
	return f.updateResp, f.updateErr
}

func TestCreatePassword(t *testing.T) {
	ctx := context.Background()
	grpcErr := errors.New("unavailable")
	tests := []struct {
		name    string
		resp    *dexapi.CreatePasswordResp
		err     error
		wantErr error
	}{
		{"created", &dexapi.CreatePasswordResp{}, nil, nil},
		{"already exists with nil error", &dexapi.CreatePasswordResp{AlreadyExists: true}, nil, ErrPasswordExists},
		{"grpc error", nil, grpcErr, grpcErr},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeAPI{createResp: tc.resp, createErr: tc.err}
			c := &Client{api: api}
			err := c.CreatePassword(ctx, "a@example.com", "$2a$12$hash", "uid-1")
			if tc.wantErr == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tc.wantErr)
			}
			require.NotNil(t, api.created)
			assert.Equal(t, "a@example.com", api.created.Password.Email)
			assert.Equal(t, "a@example.com", api.created.Password.Username)
			assert.Equal(t, "uid-1", api.created.Password.UserId)
			assert.Equal(t, []byte("$2a$12$hash"), api.created.Password.Hash)
		})
	}
}

func TestUpdatePassword(t *testing.T) {
	ctx := context.Background()
	grpcErr := errors.New("unavailable")
	tests := []struct {
		name    string
		resp    *dexapi.UpdatePasswordResp
		err     error
		wantErr error
	}{
		{"updated", &dexapi.UpdatePasswordResp{}, nil, nil},
		{"not found with nil error", &dexapi.UpdatePasswordResp{NotFound: true}, nil, ErrPasswordNotFound},
		{"grpc error", nil, grpcErr, grpcErr},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeAPI{updateResp: tc.resp, updateErr: tc.err}
			c := &Client{api: api}
			err := c.UpdatePassword(ctx, "a@example.com", "$2a$12$new")
			if tc.wantErr == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tc.wantErr)
			}
			require.NotNil(t, api.updated)
			assert.Equal(t, "a@example.com", api.updated.Email)
			assert.Equal(t, []byte("$2a$12$new"), api.updated.NewHash)
			assert.Empty(t, api.updated.NewUsername, "username is kept")
		})
	}
}

func TestSetPassword(t *testing.T) {
	ctx := context.Background()

	t.Run("created: no update", func(t *testing.T) {
		api := &fakeAPI{createResp: &dexapi.CreatePasswordResp{}}
		require.NoError(t, SetPassword(ctx, &Client{api: api}, "a@example.com", "h", "uid"))
		assert.Nil(t, api.updated)
	})
	t.Run("already exists: updates the same email", func(t *testing.T) {
		api := &fakeAPI{
			createResp: &dexapi.CreatePasswordResp{AlreadyExists: true},
			updateResp: &dexapi.UpdatePasswordResp{},
		}
		require.NoError(t, SetPassword(ctx, &Client{api: api}, "a@example.com", "h", "uid"))
		require.NotNil(t, api.updated)
		assert.Equal(t, "a@example.com", api.updated.Email)
		assert.Equal(t, []byte("h"), api.updated.NewHash)
	})
	t.Run("other create error: no update", func(t *testing.T) {
		api := &fakeAPI{createErr: errors.New("unavailable")}
		require.Error(t, SetPassword(ctx, &Client{api: api}, "a@example.com", "h", "uid"))
		assert.Nil(t, api.updated)
	})
}

// TestVerifyPassword: Dex answers a wrong password with Verified=false and an
// unknown email with NotFound=true, both under a nil error. Neither may look
// like a transport failure, and both must be reported as a failed check.
func TestVerifyPassword(t *testing.T) {
	ctx := context.Background()
	grpcErr := errors.New("unavailable")
	tests := []struct {
		name         string
		resp         *dexapi.VerifyPasswordResp
		err          error
		wantErr      error
		wantNotFound bool
	}{
		{"verified", &dexapi.VerifyPasswordResp{Verified: true}, nil, nil, false},
		{"wrong password", &dexapi.VerifyPasswordResp{Verified: false}, nil, ErrPasswordNotVerified, false},
		{"no password for the email", &dexapi.VerifyPasswordResp{NotFound: true}, nil, ErrPasswordNotVerified, true},
		{"grpc error", nil, grpcErr, grpcErr, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeAPI{verifyResp: tc.resp, verifyErr: tc.err}
			err := (&Client{api: api}).VerifyPassword(ctx, "a@example.com", "hunter2hunter2")
			if tc.wantErr == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tc.wantErr)
			}
			assert.Equal(t, tc.wantNotFound, errors.Is(err, ErrPasswordLookup),
				"a missing entry must be distinguishable for logging, but only for logging")
			require.NotNil(t, api.verified)
			assert.Equal(t, "a@example.com", api.verified.Email)
			assert.Equal(t, "hunter2hunter2", api.verified.Password)
		})
	}

	t.Run("empty password is a failed check, not an outage", func(t *testing.T) {
		api := &fakeAPI{}
		err := (&Client{api: api}).VerifyPassword(ctx, "a@example.com", "")
		require.ErrorIs(t, err, ErrPasswordNotVerified)
		assert.Nil(t, api.verified, "Dex is not called: it answers an empty password with a bare error")
	})
}

// TestRefreshClientIDs turns Dex's refresh-token list into the set of clients
// that can still mint id tokens for a user.
func TestRefreshClientIDs(t *testing.T) {
	ctx := context.Background()

	t.Run("two clients", func(t *testing.T) {
		api := &fakeAPI{listResp: &dexapi.ListRefreshResp{RefreshTokens: []*dexapi.RefreshTokenRef{
			{Id: "r1", ClientId: "home-os-ui"},
			{Id: "r2", ClientId: "home-os-mcp"},
		}}}
		got, err := (&Client{api: api}).RefreshClientIDs(ctx, "sub-1")
		require.NoError(t, err)
		assert.Equal(t, []string{"home-os-ui", "home-os-mcp"}, got)
		require.NotNil(t, api.listed)
		assert.Equal(t, "sub-1", api.listed.UserId)
	})

	t.Run("no refresh tokens is empty, not an error", func(t *testing.T) {
		got, err := (&Client{api: &fakeAPI{listResp: &dexapi.ListRefreshResp{}}}).RefreshClientIDs(ctx, "sub-1")
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("grpc error", func(t *testing.T) {
		_, err := (&Client{api: &fakeAPI{listErr: errors.New("unavailable")}}).RefreshClientIDs(ctx, "sub-1")
		require.Error(t, err)
	})
}

// TestRevokeRefresh: NotFound is a normal outcome (that client signed out or
// never asked for offline_access), not a failure.
func TestRevokeRefresh(t *testing.T) {
	ctx := context.Background()
	grpcErr := errors.New("unavailable")
	tests := []struct {
		name    string
		resp    *dexapi.RevokeRefreshResp
		err     error
		wantErr error
	}{
		{"revoked", &dexapi.RevokeRefreshResp{}, nil, nil},
		{"nothing to revoke", &dexapi.RevokeRefreshResp{NotFound: true}, nil, ErrRefreshNotFound},
		{"grpc error", nil, grpcErr, grpcErr},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeAPI{revokeResp: tc.resp, revokeErr: tc.err}
			err := (&Client{api: api}).RevokeRefresh(ctx, "sub-1", "home-os-ui")
			if tc.wantErr == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tc.wantErr)
			}
			require.Len(t, api.revoked, 1)
			assert.Equal(t, "sub-1", api.revoked[0].UserId)
			assert.Equal(t, "home-os-ui", api.revoked[0].ClientId)
		})
	}
}
