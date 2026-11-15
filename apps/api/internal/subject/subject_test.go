package subject

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func str(s string) *string { return &s }

func TestApply(t *testing.T) {
	p, v, a := uuid.New(), uuid.New(), uuid.New()
	tests := []struct {
		name    string
		start   Subject
		fields  Fields
		want    Subject
		wantErr bool
	}{
		{name: "empty request keeps subject", start: Subject{PropertyID: &p}, want: Subject{PropertyID: &p}},
		{name: "set property", fields: Fields{PropertyID: str(p.String())}, want: Subject{PropertyID: &p}},
		{name: "vehicle clears property and asset", start: Subject{PropertyID: &p, AssetID: &a}, fields: Fields{VehicleID: str(v.String())}, want: Subject{VehicleID: &v}},
		{name: "asset clears vehicle", start: Subject{VehicleID: &v}, fields: Fields{AssetID: str(a.String())}, want: Subject{AssetID: &a}},
		{name: "empty string clears", start: Subject{PropertyID: &p}, fields: Fields{PropertyID: str("")}, want: Subject{}},
		{name: "vehicle with property is rejected", fields: Fields{VehicleID: str(v.String()), PropertyID: str(p.String())}, wantErr: true},
		{name: "bad uuid", fields: Fields{AssetID: str("nope")}, wantErr: true},
		{name: "legacy vehicle", fields: Fields{EntityType: str("vehicle"), EntityID: str(v.String())}, want: Subject{VehicleID: &v}},
		{name: "legacy household is family-level", start: Subject{}, fields: Fields{EntityType: str("household")}, want: Subject{}},
		{name: "legacy pet is rejected", fields: Fields{EntityType: str("pet"), EntityID: str(v.String())}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.start
			err := tt.fields.Apply(&got)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestLegacy(t *testing.T) {
	p, a := uuid.New(), uuid.New()
	et, eid := Subject{PropertyID: &p, AssetID: &a}.Legacy()
	require.NotNil(t, et)
	assert.Equal(t, "asset", *et)
	assert.Equal(t, a.String(), *eid)
	et, eid = Subject{}.Legacy()
	assert.Nil(t, et)
	assert.Nil(t, eid)
}
