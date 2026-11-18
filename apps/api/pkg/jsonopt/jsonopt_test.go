package jsonopt

import (
	"encoding/json"
	"testing"
)

func TestField(t *testing.T) {
	type body struct {
		N Field[int]    `json:"n"`
		S Field[string] `json:"s"`
	}
	tests := []struct {
		in         string
		nSet, sSet bool
		nNil, sNil bool
	}{
		{`{}`, false, false, true, true},
		{`{"n": null, "s": null}`, true, true, true, true},
		{`{"n": 3, "s": ""}`, true, true, false, false},
	}
	for _, tc := range tests {
		var b body
		if err := json.Unmarshal([]byte(tc.in), &b); err != nil {
			t.Fatalf("%s: %v", tc.in, err)
		}
		if b.N.Set != tc.nSet || b.S.Set != tc.sSet || (b.N.Value == nil) != tc.nNil || (b.S.Value == nil) != tc.sNil {
			t.Errorf("%s: got %+v", tc.in, b)
		}
	}
	if err := json.Unmarshal([]byte(`{"n": "x"}`), new(body)); err == nil {
		t.Error("want a type error")
	}

	keep := 7
	dst := &keep
	Field[int]{}.Apply(&dst)
	if dst == nil || *dst != 7 {
		t.Error("absent must keep the value")
	}
	Field[int]{Set: true}.Apply(&dst)
	if dst != nil {
		t.Error("null must clear the value")
	}
}
