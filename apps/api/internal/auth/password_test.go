package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidatePasswordLength is the one password rule: the boundaries signup,
// admin create, reset and change-password all call. A second, differently
// worded rule in one of them is how the endpoints drift apart.
func TestValidatePasswordLength(t *testing.T) {
	tests := []struct {
		name, password string
		wantErr        error
	}{
		{"empty", "", ErrPasswordTooShort},
		{"seven bytes", "1234567", ErrPasswordTooShort},
		{"eight bytes", "12345678", nil},
		{"seventy-two bytes", strings.Repeat("a", 72), nil},
		{"seventy-three bytes", strings.Repeat("a", 73), ErrPasswordTooLong},
		{"multibyte counts bytes", strings.Repeat("é", 37), ErrPasswordTooLong},
		{"spaces are not trimmed", "        ", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePasswordLength(tc.password)
			if tc.wantErr == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tc.wantErr)
			// The messages clients have always seen for these two conditions.
			assert.Equal(t, tc.wantErr.Error(), err.Error())
		})
	}
}

// TestPasswordCheckCost: the equalising comparison never matches and costs
// what a real one costs, which is the whole point of it.
func TestPasswordCheckCost(t *testing.T) {
	assert.False(t, passwordCheckCost(""), "the dummy hash is nobody's password")
	assert.False(t, passwordCheckCost("home-os-change-password-timing-equalizer"),
		"the dummy hash is not the string it was built from either")

	start := time.Now()
	passwordCheckCost("anything")
	elapsed := time.Since(start)
	assert.Greater(t, elapsed, 50*time.Millisecond,
		"cost 12 must take real time, or it cannot hide a lookup that skipped it")
}
