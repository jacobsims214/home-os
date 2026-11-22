// Package vendor manages vendor records.
// Vendors are household-scoped entities representing service providers and
// contractors. A vendor belongs to the family and may serve any property
// (domain model v2 dropped vendors.property_id).
package vendors

import (
	"time"

	"github.com/google/uuid"
)

// Vendor represents a vendor from the vendors table.
type Vendor struct {
	ID          uuid.UUID
	HouseholdID uuid.UUID
	Name        string
	// Kind is what the row is for (080, #5529): "service_provider" (does
	// work -- the contacts list), "payee" (is paid -- the bill payee
	// picker) or "both". The database default is service_provider.
	Kind      string
	Trades    []string // lower-case trades (plumber, vet, hvac, pest_control); never nil
	Specialty *string
	Phone     *string
	Email     *string
	Website   *string
	Address   *string // one line; 065 moved MCP's "Address: ..." notes line here
	Notes     *string
	// PortalSecretID is the payee's portal login, as a row in `secrets` (075,
	// #5497). METADATA ONLY: the ciphertext stays in `secrets`, so the API can
	// say "a portal login is stored" without decrypting anything. Its uuid is
	// never serialized -- only its presence, and only to a role the credential
	// policy allows (auth.Can on ResSecrets; see handler.toVendorResponse).
	PortalSecretID *uuid.UUID
	CreatedAt      time.Time
	UpdatedAt time.Time
}

// The three vendor kinds (080). Mirrored by the vendor_kind enum.
const (
	KindServiceProvider = "service_provider"
	KindPayee           = "payee"
	KindBoth            = "both"
)

// Kinds is every valid kind, in enum order.
var Kinds = []string{KindServiceProvider, KindPayee, KindBoth}

// ValidKind reports whether k is a vendor_kind enum value.
func ValidKind(k string) bool {
	for _, v := range Kinds {
		if v == k {
			return true
		}
	}
	return false
}
