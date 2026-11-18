// Package subject holds the optional "what is this about" columns that
// domain model v2 gives bills, loans and maintenance: typed, nullable
// property_id / vehicle_id / asset_id foreign keys. A vehicle excludes the
// other two; when an asset is set the database derives property_id from it;
// no subject at all means the record is family-level.
//
// The legacy polymorphic entity_type/entity_id request fields are still
// accepted and mapped onto the typed columns, and responses still carry them
// (derived), so clients written before v2 keep working. Migration 056 drops
// the columns themselves.
package subject

import (
	"errors"

	"github.com/google/uuid"
)

// Subject is the typed subject of a bill, loan or maintenance record.
//
// It is embedded in bill.Bill, loan.Loan and guides.Guide, so its fields carry
// json tags: untagged, any of those structs serialized directly would publish
// "PropertyID"/"VehicleID"/"AssetID" and a client reading property_id would
// get undefined (#5300's bug class). No consumer reads these three from JSON
// today — handlers map them to property_id/vehicle_id/asset_id explicitly — so
// the tags name the wire contract those mappers already use.
type Subject struct {
	PropertyID *uuid.UUID `json:"property_id"`
	VehicleID  *uuid.UUID `json:"vehicle_id"`
	AssetID    *uuid.UUID `json:"asset_id"`
}

// Fields are the subject fields of a create or update request. An empty
// string clears a field on update.
type Fields struct {
	PropertyID *string `json:"property_id,omitempty"`
	VehicleID  *string `json:"vehicle_id,omitempty"`
	AssetID    *string `json:"asset_id,omitempty"`
	// Deprecated: use property_id, vehicle_id or asset_id.
	EntityType *string `json:"entity_type,omitempty"`
	// Deprecated: use property_id, vehicle_id or asset_id.
	EntityID *string `json:"entity_id,omitempty"`
}

// ErrVehicleExclusive is returned when a vehicle is combined with a property
// or an asset.
var ErrVehicleExclusive = errors.New("vehicle_id cannot be combined with property_id or asset_id")

// Apply merges the request fields onto s. Setting a vehicle clears a property
// and asset the request did not also name, and setting a property or asset
// clears a vehicle the request did not also name, so switching a record from
// one kind of subject to another is a single field change. The returned
// error message is safe to show to the caller.
func (f Fields) Apply(s *Subject) error {
	prop, veh, asset := f.PropertyID, f.VehicleID, f.AssetID

	if f.EntityType != nil || f.EntityID != nil {
		et, eid := "", ""
		if f.EntityType != nil {
			et = *f.EntityType
		}
		if f.EntityID != nil {
			eid = *f.EntityID
		}
		switch et {
		case "property":
			prop = firstSet(prop, eid)
		case "vehicle":
			veh = firstSet(veh, eid)
		case "asset":
			asset = firstSet(asset, eid)
		case "", "household":
			// family-level: nothing to set
		default:
			return errors.New("entity_type must be property, vehicle or asset")
		}
	}

	var err error
	if s.PropertyID, err = merge(s.PropertyID, prop, "property_id"); err != nil {
		return err
	}
	if s.VehicleID, err = merge(s.VehicleID, veh, "vehicle_id"); err != nil {
		return err
	}
	if s.AssetID, err = merge(s.AssetID, asset, "asset_id"); err != nil {
		return err
	}

	if isSet(veh) {
		if prop == nil {
			s.PropertyID = nil
		}
		if asset == nil {
			s.AssetID = nil
		}
	} else if (isSet(prop) || isSet(asset)) && veh == nil {
		s.VehicleID = nil
	}
	return s.Validate()
}

// Validate enforces the subject rule: a vehicle excludes property and asset.
func (s Subject) Validate() error {
	if s.VehicleID != nil && (s.PropertyID != nil || s.AssetID != nil) {
		return ErrVehicleExclusive
	}
	return nil
}

// Legacy returns the pre-v2 entity_type/entity_id pair for a response: the
// asset if set, else the vehicle, else the property; nil for family-level.
func (s Subject) Legacy() (entityType, entityID *string) {
	var kind string
	var id *uuid.UUID
	switch {
	case s.AssetID != nil:
		kind, id = "asset", s.AssetID
	case s.VehicleID != nil:
		kind, id = "vehicle", s.VehicleID
	case s.PropertyID != nil:
		kind, id = "property", s.PropertyID
	default:
		return nil, nil
	}
	v := id.String()
	return &kind, &v
}

// String formats an optional UUID for a JSON response.
func String(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	v := id.String()
	return &v
}

func firstSet(v *string, fallback string) *string {
	if v != nil {
		return v
	}
	return &fallback
}

func isSet(v *string) bool { return v != nil && *v != "" }

func merge(cur *uuid.UUID, v *string, name string) (*uuid.UUID, error) {
	if v == nil {
		return cur, nil
	}
	if *v == "" {
		return nil, nil
	}
	id, err := uuid.Parse(*v)
	if err != nil {
		return nil, errors.New("invalid " + name)
	}
	return &id, nil
}
