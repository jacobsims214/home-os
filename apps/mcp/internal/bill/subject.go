package bill

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
)

// Subjects follow domain model v2 (migrations 038/039, KB
// decisions/domain-model-v2.md): a bill always belongs to the family and is
// optionally about one property, vehicle or asset, through the typed
// property_id / vehicle_id / asset_id columns. A vehicle excludes the other
// two (bills_subject_check); an asset bill's property_id is derived from the
// asset by derive_subject_property(). The legacy entity_type/entity_id pair
// is never read or written (056 drops it); callers that still send it are
// mapped onto the typed columns by subjectAliases.

// Subject types reported in subject_type.
const (
	subjectProperty = "property"
	subjectVehicle  = "vehicle"
	subjectAsset    = "asset"
)

// subjectParams are the typed subject params, in summary order.
var subjectParams = []string{"property_id", "vehicle_id", "asset_id"}

// subject is a parsed subject: the three typed ids, replaced together.
type subject struct {
	propertyID *uuid.UUID
	vehicleID  *uuid.UUID
	assetID    *uuid.UUID
}

// subjectGiven reports whether any typed subject param is present (a blank
// string counts: it clears the subject on update).
func subjectGiven(args map[string]any) bool {
	for _, p := range subjectParams {
		if given(args, p) {
			return true
		}
	}
	return false
}

// subjectAliases maps the legacy entity_type/entity_id pair onto the typed
// subject params and removes it from args (which it modifies). The typed
// params win when both are sent. entity_type "household", "family" or blank
// means "not about anything", which clears the subject on update.
func subjectAliases(args map[string]any) error {
	if !given(args, "entity_type") && !given(args, "entity_id") {
		delete(args, "entity_type")
		delete(args, "entity_id")
		return nil
	}
	kind, err := optionalString(args, "entity_type")
	if err != nil {
		return err
	}
	id, err := optionalString(args, "entity_id")
	if err != nil {
		return err
	}
	delete(args, "entity_type")
	delete(args, "entity_id")
	if subjectGiven(args) {
		return nil
	}
	k := ""
	if kind != nil {
		k = strings.ToLower(*kind)
	}
	switch k {
	case subjectProperty, subjectVehicle, subjectAsset:
		if id == nil {
			return badArg(k+"_id", "entity_type needs entity_id.",
				fmt.Sprintf("Pass %s_id with the %s's id instead.", k, k))
		}
		args[k+"_id"] = *id
	case "", "household", "family":
		if id != nil && kind == nil {
			return badArg("property_id", "entity_id was given without entity_type.",
				"Pass property_id, vehicle_id or asset_id instead.")
		}
		args["property_id"] = "" // clear
	default:
		return badArg("property_id", fmt.Sprintf("A bill can't be tied to a %q.", *kind),
			"Pass property_id, vehicle_id or asset_id, or leave them out for a family-level bill.")
	}
	return nil
}

// parseSubject validates the typed subject params.
func parseSubject(args map[string]any) (subject, error) {
	var s subject
	var err error
	if s.propertyID, err = optionalUUID(args, "property_id"); err != nil {
		return s, err
	}
	if s.vehicleID, err = optionalUUID(args, "vehicle_id"); err != nil {
		return s, err
	}
	if s.assetID, err = optionalUUID(args, "asset_id"); err != nil {
		return s, err
	}
	if s.vehicleID != nil && (s.propertyID != nil || s.assetID != nil) {
		return s, vehicleExclusive()
	}
	return s, nil
}

func vehicleExclusive() error {
	return badArg("vehicle_id", "A bill for a vehicle can't also be for a property or asset.",
		"Pass only vehicle_id, or only property_id and/or asset_id.")
}

// checkSubject returns an isError result when a given subject id is not a
// row of this family. A row of another family looks the same as a missing
// one.
func checkSubject(ctx context.Context, tx pgx.Tx, s subject, householdID string, example map[string]any) (*mcp.CallToolResult, error) {
	for _, c := range []struct {
		param, table, what, finder string
		id                         *uuid.UUID
	}{
		{"property_id", "properties", "property", "property_find", s.propertyID},
		{"vehicle_id", "vehicles", "vehicle", "vehicle_find", s.vehicleID},
		{"asset_id", "assets", "asset", "asset_find", s.assetID},
	} {
		if c.id == nil {
			continue
		}
		var exists bool
		// c.table comes from the literal list above, never from the caller.
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM `+c.table+` WHERE id = $1 AND household_id = $2)`,
			*c.id, householdID,
		).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			return respond.Invalid(c.param, fmt.Sprintf("No %s with id %q in this family.", c.what, c.id.String()),
				fmt.Sprintf("Call %s to get the %s, or leave it out.", c.finder, c.param), example), nil
		}
	}
	return nil, nil
}

// subjectDBError maps a subject violation checkSubject could not see (a
// subject deleted meanwhile, the subject CHECK) to an isError result; nil
// for any other error.
func subjectDBError(err error, example map[string]any) *mcp.CallToolResult {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return nil
	}
	switch pgErr.Code {
	case "23503":
		if strings.Contains(pgErr.ConstraintName, "vendor") || strings.Contains(pgErr.Message, "vendor") {
			return nil
		}
		what := subjectProperty
		for _, k := range []string{subjectVehicle, subjectAsset} {
			if strings.Contains(pgErr.ConstraintName, k) || strings.Contains(pgErr.Message, k) {
				what = k
			}
		}
		return respond.Invalid(what+"_id", "That "+what+" does not exist in this family.",
			"Call "+what+"_find to get its id, or leave it out.", example)
	case "23514":
		if pgErr.ConstraintName == "bills_subject_check" {
			ae := vehicleExclusive().(*argError)
			return respond.Invalid(ae.field, ae.message, ae.fix, example)
		}
	}
	return nil
}

// subjectType is property, vehicle or asset for a row's typed subject, nil
// for a family-level bill. An asset bill also has its asset's property_id,
// so the asset is checked first.
func subjectType(propertyID, vehicleID, assetID *uuid.UUID) *string {
	var s string
	switch {
	case vehicleID != nil:
		s = subjectVehicle
	case assetID != nil:
		s = subjectAsset
	case propertyID != nil:
		s = subjectProperty
	default:
		return nil
	}
	return &s
}
