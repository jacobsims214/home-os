package kb

import (
	"home-os/api/internal/search"
)

// Document is the household_search document of a KB doc.
//
// WHY THE TITLE LEADS THE BODY. Typesense computes the embedding from
// title + body and the model only reads roughly the first 256 tokens, so a
// doc whose body opens with a heading benefits from its title being first.
// The PATH is appended to the body text so a keyword query for
// "networking" or "vet-after-hours" matches the address as well as the prose.
//
// The document is derived from Postgres ONLY: this function is called by
// searchrecon for every live kb_docs row (the sweep and the reindex CLI), so
// an agent that wrote through the MCP, a person who wrote through the REST
// API and a row restored from a dump all become searchable the same way. A
// handler may also call it on write for freshness, but it cannot change what
// the document IS (#5298).
func Document(d *Doc) search.SearchDocument {
	return search.SearchDocument{
		ID:          DocumentID(d),
		HouseholdID: d.HouseholdID.String(),
		EntityType:  EntityType,
		EntityID:    d.ID.String(),
		Title:       d.Title,
		Body:        Body(d),
		Tags:        d.Tags,
		CreatedAt:   d.CreatedAt.Unix(),
		UpdatedAt:   d.UpdatedAt.Unix(),
	}
}

// DocumentID is the doc's household_search document id ("kb_doc-<uuid>").
func DocumentID(d *Doc) string { return EntityType + "-" + d.ID.String() }

// Body is the text indexed (and embedded) for a doc: the path, the tags, then
// the markdown. The path and tags are searchable words a person would
// plausibly type, and the excerpt shown to a caller is cut from the body.
func Body(d *Doc) string {
	out := d.Path + "\n\n" + d.Body
	if len(d.Tags) > 0 {
		out = d.Path + "\n\nTags: " + joinTags(d.Tags) + "\n\n" + d.Body
	}
	return out
}

func joinTags(tags []string) string {
	out := ""
	for i, t := range tags {
		if i > 0 {
			out += ", "
		}
		out += t
	}
	return out
}
