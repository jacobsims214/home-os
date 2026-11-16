package bill

// The due rule's own tables (TestBillDueFrequencies, TestPaidForDueDateFrequencies)
// moved to home-os/billdue with the rule in #4819, so there is one copy of
// them rather than two that can drift. The MCP-side coverage that remains
// here is the check that this package's names reach the shared rule, in
// TestDueRuleIsTheSharedOne in tools_test.go.
