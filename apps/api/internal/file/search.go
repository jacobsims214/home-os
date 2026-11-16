package file

import (
	"strings"

	"home-os/api/internal/search"
)

// EntityTypeDocument is the entity_type of a file's metadata document in
// the household_search collection.
const EntityTypeDocument = "document"

// SearchDocIDs returns the household_search ids a file's metadata document
// may have. A file attached to a credential is indexed as
// "secret-document-<id>" so search.VisibleResults drops it for roles that
// cannot read credentials (it hides every "secret-" document id); any other
// file is "document-<id>". Deletes remove both, so a file never leaves a
// stale document behind whichever id it was indexed under.
func SearchDocIDs(f *File) (current string, all []string) {
	plain := EntityTypeDocument + "-" + f.ID.String()
	secret := "secret-" + plain
	if f.EntityType == EntityTypeSecret {
		return secret, []string{plain, secret}
	}
	return plain, []string{plain, secret}
}

// SearchDocument builds a file's metadata document for household_search.
// The worker's files collection indexes the file name and its OCR text; this
// document makes the 048 metadata searchable (title, description, category,
// whose document it is, its dates and tags). household_search embeds
// title + body with Typesense's local ts/all-MiniLM-L12-v2 model, so the
// document also matches semantically ("car insurance" finds a category
// insurance document titled "Geico policy"). The household scope is the
// file's own household_id, never a client value.
func SearchDocument(f *File, personName string) search.SearchDocument {
	id, _ := SearchDocIDs(f)
	var body []string
	if f.Description != nil && *f.Description != "" {
		body = append(body, *f.Description)
	}
	body = append(body, "Category: "+f.Category)
	if personName != "" {
		body = append(body, "For: "+personName)
	}
	if f.DocumentDate != nil {
		body = append(body, "Dated "+*f.DocumentDate)
	}
	if f.ExpiresOn != nil {
		body = append(body, "Expires "+*f.ExpiresOn)
	}
	if f.DisplayTitle() != f.Name {
		body = append(body, "File: "+f.Name)
	}
	tags := append([]string{}, f.Tags...)
	if f.Category != "" && f.Category != CategoryOther {
		tags = append(tags, f.Category)
	}
	return search.SearchDocument{
		ID:          id,
		HouseholdID: f.HouseholdID.String(),
		EntityType:  EntityTypeDocument,
		EntityID:    f.ID.String(),
		Title:       f.DisplayTitle(),
		Body:        strings.Join(body, ". "),
		Tags:        tags,
		CreatedAt:   f.CreatedAt.Unix(),
		UpdatedAt:   f.UpdatedAt.Unix(),
	}
}
