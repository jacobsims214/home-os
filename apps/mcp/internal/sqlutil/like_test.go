package sqlutil

import "testing"

func TestEscapeLike(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"Nora", "Nora"},
		{"%", `\%`},
		{"_", `\_`},
		{`a\b`, `a\\b`},
		{`100%_\`, `100\%\_\\`},
	}
	for _, tc := range cases {
		if got := EscapeLike(tc.in); got != tc.want {
			t.Errorf("EscapeLike(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := ContainsPattern("a_b"); got != `%a\_b%` {
		t.Errorf("ContainsPattern = %q", got)
	}
}
