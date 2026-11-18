package legacyfinance

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func ptr(s string) *string { return &s }

func TestAmount(t *testing.T) {
	tests := []struct {
		in      *string
		want    *string
		wantErr bool
	}{
		{nil, nil, false},
		{ptr("  "), nil, false},
		{ptr("$300,000.50"), ptr("300000.50"), false},
		{ptr("1800"), ptr("1800"), false},
		{ptr("-1"), nil, true},
		{ptr("abc"), nil, true},
	}
	for _, tt := range tests {
		got, err := Amount("x", tt.in)
		if tt.wantErr {
			_, ok := IsInvalid(err)
			assert.True(t, ok, "%v", tt.in)
			continue
		}
		assert.NoError(t, err)
		assert.Equal(t, tt.want, got)
	}
}

func TestRateAndMonths(t *testing.T) {
	r, err := Rate("r", ptr("6.5%"))
	assert.NoError(t, err)
	assert.Equal(t, "6.5", *r)
	_, err = Rate("r", ptr("100"))
	assert.Error(t, err)

	m, err := Months("m", ptr("360"))
	assert.NoError(t, err)
	assert.Equal(t, 360, *m)
	_, err = Months("m", ptr("0"))
	assert.Error(t, err)

	_, err = Date("d", ptr("07/01/2018"))
	assert.Error(t, err)
}

func TestWithDueMonths(t *testing.T) {
	assert.Equal(t, "Due months: Jan, Jul", *withDueMonths(nil, "Jan, Jul"))
	assert.Equal(t, "Copied.\nDue months: 4, 10",
		*withDueMonths(ptr("Copied.\nDue months: Jan, Jul"), "4, 10"))
	assert.Equal(t, "Copied.", *withDueMonths(ptr("Copied.\nDue months: Jan"), ""))
	assert.Nil(t, withDueMonths(ptr("Due months: Jan"), ""))
}

func TestMonth(t *testing.T) {
	for in, want := range map[string]bool{"4": true, "12": true, "13": false, "0": false, "Jan": false, "4,10": false} {
		_, ok := month(in)
		assert.Equal(t, want, ok, in)
	}
}

func TestSQLFragmentsQuotePatterns(t *testing.T) {
	assert.Equal(t, "ARRAY['%hoa%', '%association%']::text[]", sqlArray(HOAFee.Patterns))
	assert.Equal(t, "ARRAY[]::text[]", sqlArray(nil))
}
