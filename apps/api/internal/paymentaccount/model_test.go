package paymentaccount

import "testing"

// TestValidateLastFour_RefusesFullAccountAndRoutingNumbers is the test that
// keeps a full account number or a routing number out of the API, and it starts
// where the person starts: the VALUES A PERSON PASTES into the last-four field,
// not a hand-built invalid input.
//
// It is deliberately a table of realistic pastes:
//
//   - a 12-digit checking account number copied out of a statement
//   - a 9-digit routing number (the most likely wrong paste, because banks put
//     it immediately beside the account number on a cheque)
//   - a full "routing / account" line as a person copies it
//   - four characters that are not all digits
//   - one, three and five digits, the near-misses
//
// Every one must be REFUSED (msg != ""), because a silent truncation to the last
// four would attach a bill to the wrong account. Fail on regression: reverting
// ValidateLastFour to `return raw[:4]`-style truncation, or dropping the
// digit-only loop, turns the routing-number and "12a4" cases red.
func TestValidateLastFour_RefusesFullAccountAndRoutingNumbers(t *testing.T) {
	refused := []struct{ name, in string }{
		{"full 12-digit account number", "123456789012"},
		{"9-digit routing number", "021000021"},
		{"one digit", "7"},
		{"three digits", "123"},
		{"five digits", "12345"},
		{"four chars but not all digits", "12a4"},
		{"letters", "abcd"},
		{"routing and account on one line", "021000021 123456789012"},
	}
	for _, tc := range refused {
		t.Run("refuses "+tc.name, func(t *testing.T) {
			got, empty, msg := ValidateLastFour(tc.in)
			if msg == "" {
				t.Fatalf("ValidateLastFour(%q) was ACCEPTED as %q (empty=%v); a full "+
					"account or routing number must be refused, never truncated",
					tc.in, got, empty)
			}
			if got != "" {
				t.Errorf("refused value still returned %q; a refusal must not hand back a "+
					"value a caller could write", got)
			}
			if empty {
				t.Errorf("a refused value reported empty=true, which would clear the column " +
					"instead of failing the request")
			}
		})
	}

	// The routing-number refusal must SAY it is a routing number: someone who
	// pasted one needs a different fix from someone who pasted an account
	// number, and a generic "invalid" sends them to try again with the same
	// value.
	if _, _, msg := ValidateLastFour("021000021"); msg == "" ||
		!containsFold(msg, "routing") {
		t.Errorf("9-digit refusal does not mention routing: %q", msg)
	}
}

// TestValidateLastFour_AcceptsExactlyFourDigits pins the accepted shape, so the
// test above cannot pass by refusing everything.
func TestValidateLastFour_AcceptsExactlyFourDigits(t *testing.T) {
	cases := []struct{ in, want string }{
		{"1234", "1234"},
		{"0000", "0000"},
		{" 4821 ", "4821"}, // trimmed, the way a person copies it
	}
	for _, tc := range cases {
		got, empty, msg := ValidateLastFour(tc.in)
		if msg != "" {
			t.Errorf("ValidateLastFour(%q) refused a valid last-four: %s", tc.in, msg)
			continue
		}
		if empty || got != tc.want {
			t.Errorf("ValidateLastFour(%q) = (%q, empty=%v), want (%q, false)",
				tc.in, got, empty, tc.want)
		}
	}

	// No digits at all is allowed: an account the family has not looked up yet.
	// It must come back EMPTY, which the handler writes as NULL rather than "".
	got, empty, msg := ValidateLastFour("")
	if msg != "" || !empty || got != "" {
		t.Errorf(`ValidateLastFour("") = (%q, empty=%v, %q), want ("", true, "")`,
			got, empty, msg)
	}
}

// TestBuildAccount_NoFullNumberAnywhere checks the door a client actually comes
// through: the request fields of POST /api/v1/payment-accounts. buildAccount is
// what Create and Update both call, so this is the one place a full number could
// be let in.
func TestBuildAccount_NoFullNumberAnywhere(t *testing.T) {
	// A realistic paste: the whole account number in the only digit field.
	if _, msg := buildAccount("Joint checking", "First National", "", "", "123456789012", ""); msg == "" {
		t.Fatal("buildAccount accepted a 12-digit account number in last_four")
	}
	// A routing number in the same field, and in the notes field it is NOT a
	// problem (notes are free text and are not a digit column) -- but it must
	// not be copied into last_four.
	if _, msg := buildAccount("Joint checking", "", "", "", "021000021", ""); msg == "" {
		t.Fatal("buildAccount accepted a 9-digit routing number in last_four")
	}
	// The happy path still works, with the type defaulting to checking.
	a, msg := buildAccount(" Joint checking ", " First National ", " main bill account ",
		"", "4821", "")
	if msg != "" {
		t.Fatalf("valid account refused: %s", msg)
	}
	if a.Name != "Joint checking" {
		t.Errorf("name not trimmed: %q", a.Name)
	}
	if a.LastFour == nil || *a.LastFour != "4821" {
		t.Errorf("last_four not stored as 4821: %v", a.LastFour)
	}
	if a.AccountType != TypeChecking {
		t.Errorf("omitted account_type = %q, want the checking default", a.AccountType)
	}
	// An unknown type is refused: the enum is four values, and a client that
	// mistypes must not be handed a silent 'other'.
	if _, msg := buildAccount("X", "", "", "chequing", "1234", ""); msg == "" {
		t.Error("buildAccount accepted account_type \"chequing\"")
	}
}

// TestCanSeeCredentialExistence_IsFamilyOnly pins the role rule the ticket
// calls out: account details follow the money policy, but the EXISTENCE of a
// stored credential is family only, so a viewer does not see even the name.
//
// Fail on regression: widening ResSecrets to rolesReaders, or swapping this
// helper for auth.CanSeeMoney, turns the viewer case red.
func TestCanSeeCredentialExistence_IsFamilyOnly(t *testing.T) {
	// Roles that must see the credential fact.
	for _, role := range []string{"owner", "family_manager", "family_member"} {
		if !canSeeCredentialExistence(role) {
			t.Errorf("%s must be able to see that a login is stored", role)
		}
	}
	// Roles that must NOT -- a viewer reads bills and money, and still gets no
	// credential fact. The empty role (no claims) fails closed.
	for _, role := range []string{"viewer", "housesitter", "vendor", ""} {
		if canSeeCredentialExistence(role) {
			t.Errorf("%q must NOT learn that a credential exists", role)
		}
	}
}

// containsFold is a tiny case-insensitive substring check (the refusal messages
// are prose; the test only cares that the word is in there).
func containsFold(s, sub string) bool {
	ls, lsub := lower(s), lower(sub)
	for i := 0; i+len(lsub) <= len(ls); i++ {
		if ls[i:i+len(lsub)] == lsub {
			return true
		}
	}
	return false
}

func lower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}
