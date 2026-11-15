// Package jsonopt tells a JSON field that was left out from one sent as null.
//
// A plain pointer field cannot: both decode to nil. Partial updates need the
// difference, because "absent" means "keep the stored value" and null means
// "clear it".
package jsonopt

import (
	"bytes"
	"encoding/json"
)

// Field is a JSON field that may be absent, null, or a value.
//
//	absent: Set == false
//	null:   Set == true, Value == nil
//	value:  Set == true, Value != nil
type Field[T any] struct {
	Set   bool
	Value *T
}

// UnmarshalJSON implements json.Unmarshaler. It is only called when the key
// is present, which is what sets Set.
func (f *Field[T]) UnmarshalJSON(b []byte) error {
	f.Set = true
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		f.Value = nil
		return nil
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	f.Value = &v
	return nil
}

// Apply writes the field into dst when it was sent: dst becomes the value,
// or nil for null. An absent field leaves dst alone.
func (f Field[T]) Apply(dst **T) {
	if f.Set {
		*dst = f.Value
	}
}
