package bill

// Document filing (#5306, war game R2: "all filed" with no document).
//
// A parent asked the assistant to file the dog's vet bill, $82.50 due 12/05,
// and add a reminder. The assistant answered "Done, Amber — all filed in
// home-os…" and no document existed: document_find returned 0, and the
// bill's source_document_id was null. #4941 built the data layer for exactly
// this (bills.source_document_id, verbatim pasted text) and the agent-side
// half was never used.
//
// The claim was not a tool result — no tool said "filed". It was the shape of
// the result: bill_create answered ok:true with "Created bill …" and said
// NOTHING either way about documents, so the only available reading of a
// successful call in a "file this" turn was success at filing. That is the
// #5301 shape exactly: the tool layer sent no signal and the model filled the
// gap. Silence is read as consent, so it must not be silence.
//
// So every bill payload carries document_filed, computed from the row's
// source_document_id and never from prose, and a bill_create result with no
// document says so in its summary and offers the two steps that would file
// one. The tool cannot know whether the person wanted a document; it can know
// whether one exists, and that is what it now states.
//
// Why the tools do not simply file the document themselves: the pasted text
// must be stored VERBATIM (document_upload's content_text, text_origin 'raw',
// text_sha256). A bill_create that took the text and re-typed it into a
// document is the R1 failure #4941 exists to prevent — "$35.00 became $5.00,
// then verified" — where bill and document are two copies of one mistake and
// can only ever agree with each other. The honest fix is therefore to stop
// the claim, not to invent a second write path: stop claiming, then point at
// the two calls that file for real.

// filingNote is the summary sentence a bill with no document adds: what is
// NOT in Documents, stated plainly. "" when the bill is linked to one.
func filingNote(b billData) string {
	if b.DocumentFiled {
		return ""
	}
	return " No document is filed for this bill: it is not in Documents."
}

// linkedNote is the summary sentence a bill with a document adds, naming it.
// "" when no document is linked.
func linkedNote(b billData) string {
	if !b.DocumentFiled {
		return ""
	}
	if b.SourceDocumentTitle != nil && *b.SourceDocumentTitle != "" {
		return " Its document is " + quote(*b.SourceDocumentTitle) + "."
	}
	return " Its document is linked."
}

// filingHint is the next-step hint that makes filing a second, visible step:
// document_upload stores the text verbatim, bill_update links it. "" when the
// bill already has a document.
//
// Both tools are named with the arguments they need, because a hint that only
// says "file it" is the same gap in a different place.
func filingHint(b billData) string {
	if b.DocumentFiled {
		return ""
	}
	return "Nothing was filed in Documents. For a paper bill, paste its text exactly as written with " +
		"document_upload content_text=… (never retyped), then link it with bill_update bill_id=" + b.ID +
		" source_document_id=<document_id>."
}

// quote wraps s in double quotes for a summary sentence.
func quote(s string) string { return "\"" + s + "\"" }
