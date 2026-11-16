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

// fakeAPI stands in for Dex's gRPC API. Only the password calls are
// implemented; any other call panics on the nil embedded interface.
type fakeAPI struct {
	dexapi.DexClient
	createResp *dexapi.CreatePasswordResp
	createErr  error
	updateResp *dexapi.UpdatePasswordResp
	updateErr  error

	created *dexapi.CreatePasswordReq
	updated *dexapi.UpdatePasswordReq
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
