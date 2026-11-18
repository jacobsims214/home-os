package allergen

// Warning is one ingredient that matches a family member's allergy. It is the
// JSON the MCP recipe tools put in allergy_warnings and the API puts in
// allergy_warnings on GET /recipes, so both surfaces describe a match the same
// way (person, person_id, allergy, ingredient).
//
// Moved verbatim from apps/mcp/internal/meals.AllergyWarning (#4891); the
// jsonschema_description tags are what the MCP tools advertise in their input
// schema, so they moved with it.
type Warning struct {
	Person     string `json:"person"`
	PersonID   string `json:"person_id"`
	Allergy    string `json:"allergy" jsonschema_description:"The allergy as saved on the person, e.g. tree nuts."`
	Ingredient string `json:"ingredient" jsonschema_description:"The ingredient or name that matched, e.g. walnuts."`
}
