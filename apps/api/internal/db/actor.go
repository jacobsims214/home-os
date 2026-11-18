// Package db builds the API's Postgres pool and tells the database who is
// acting on each request.
//
// Migration 050 (authorship) fills created_by/updated_by and activity_log's
// actor from the settings app.user_id, app.household_id and
// app.agent_token_id, and migration 054 notifies a record's creator when
// another member changes it. Both read those settings through
// current_actor_user_id() and friends, so a write that reaches Postgres
// without them is anonymous.
//
// Rather than touch every repository, the pool sets the settings itself:
// NewPool installs a PrepareConn hook that runs on every connection
// acquisition (pool.Exec, pool.Query, pool.QueryRow, pool.Begin and
// pool.Acquire all acquire) and writes the actor carried by the acquiring
// context onto the connection. A context without an actor (startup seeds,
// background goroutines) writes blank settings, which the triggers read as
// NULL ("the system"). Because every acquisition overwrites all three
// settings, a pooled connection never carries one request's actor into
// another caller's writes.
package db

import (
	"context"

	"github.com/google/uuid"
)

// Actor is the member a request acts as.
type Actor struct {
	// UserID is users.id.
	UserID uuid.UUID
	// HouseholdID is households.id of the household the request resolved
	// to. uuid.Nil when there is none (instance admin routes).
	HouseholdID uuid.UUID
}

type actorKey struct{}

// WithActor returns ctx carrying a. Database work done with the returned
// context (or anything derived from it) is attributed to a.UserID.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, a)
}

// WithActorIDs is WithActor for string ids, as auth.Claims carries them.
// An id that is not a UUID (for example the "local-dev" identity used when
// auth is disabled) is dropped, so the write is recorded as anonymous rather
// than failing. With no valid user id ctx is returned unchanged.
func WithActorIDs(ctx context.Context, userID, householdID string) context.Context {
	uid, err := uuid.Parse(userID)
	if err != nil || uid == uuid.Nil {
		return ctx
	}
	hid, err := uuid.Parse(householdID)
	if err != nil {
		hid = uuid.Nil
	}
	return WithActor(ctx, Actor{UserID: uid, HouseholdID: hid})
}

// WithoutActor returns ctx with any actor removed, for work started from a
// request that must not be attributed to the caller.
func WithoutActor(ctx context.Context) context.Context {
	return context.WithValue(ctx, actorKey{}, Actor{})
}

// ActorFromContext returns the actor carried by ctx, if any.
func ActorFromContext(ctx context.Context) (Actor, bool) {
	a, ok := ctx.Value(actorKey{}).(Actor)
	if !ok || a.UserID == uuid.Nil {
		return Actor{}, false
	}
	return a, true
}

// settings returns the values written to app.user_id and app.household_id
// for ctx. Blank means unset; current_actor_user_id() reads it as NULL.
func settings(ctx context.Context) (userID, householdID string) {
	a, ok := ActorFromContext(ctx)
	if !ok {
		return "", ""
	}
	if a.HouseholdID != uuid.Nil {
		householdID = a.HouseholdID.String()
	}
	return a.UserID.String(), householdID
}
