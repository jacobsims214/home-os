package bill

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
)

// Provenance for bills filed from a document (#4941, war game R1 gap G1).
//
// In R1 a parent pasted "Well visit copay — $35.00"; a bill of 5.00 and a
// document whose body had been re-typed as "… 5.00" were filed, and a later
// check compared the two artifacts with each other, found them equal, and
// reported the record verified. The pair could only agree because both were
// written from the same mistake.
//
// bills.source_document_id (migration 071) records the document a bill came
// from. bill_create and bill_update then read that document's stored text,
// pull the money amounts out of it, and compare them with the amount being
// recorded: on a mismatch the result says so in as many words and quotes the
// source line, so the caller has to either fix the amount or knowingly file a
// bill that disagrees with its own source. The comparison happens at write
// time, when the person's original text is still there to compare against.

// amountPattern matches a money amount in a person's text: a currency symbol
// or code, then a number. "$35.00", "35.00 USD", "USD 35", "$1,240.50".
var amountPattern = regexp.MustCompile(`(?i)(?:\$|usd\s*)\s?(\d[\d,]*(?:\.\d{1,2})?)|(\d[\d,]*(?:\.\d{1,2})?)\s*(?:usd|dollars?)`)

// maxSourceText bounds how much of a source document is scanned for amounts:
// a pasted statement is short, and a 25 MB document must not be pulled into
// memory to answer one question.
const maxSourceText = 64 << 10

// sourceLine is one money amount found in a source document's text.
type sourceLine struct {
	// Amount is the number as written, e.g. 35 for "$35.00".
	Amount float64
	// Line is the line it was read from, trimmed and cut short, so the
	// caller can quote the person's own words back to them.
	Line string
}

// amountsIn returns the money amounts in text, in the order they appear,
// with the line each was read from. Repeated identical amounts on the same
// line are reported once.
func amountsIn(text string) []sourceLine {
	if len(text) > maxSourceText {
		text = text[:maxSourceText]
	}
	var out []sourceLine
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(strings.TrimRight(line, "\r"))
		if trimmed == "" {
			continue
		}
		for _, m := range amountPattern.FindAllStringSubmatch(trimmed, -1) {
			raw := m[1]
			if raw == "" {
				raw = m[2]
			}
			v, err := strconv.ParseFloat(strings.ReplaceAll(raw, ",", ""), 64)
			if err != nil {
				continue
			}
			key := fmt.Sprintf("%.2f|%s", v, trimmed)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, sourceLine{Amount: v, Line: clip(trimmed, 120)})
		}
	}
	return out
}

// clip shortens s to at most n characters, marking the cut.
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// sameMoney reports whether a and b are the same amount to the cent (the
// precision bills.amount stores).
func sameMoney(a, b float64) bool {
	return (a-b) < 0.005 && (b-a) < 0.005
}

// findAmount returns the amount in lines that equals want, if any.
func findAmount(lines []sourceLine, want float64) *sourceLine {
	for i := range lines {
		if sameMoney(lines[i].Amount, want) {
			return &lines[i]
		}
	}
	return nil
}

// sourceDoc is a bill's source document: its id, title and, when it is raw
// text, the amounts read from it.
type sourceDoc struct {
	ID     uuid.UUID
	Title  string
	Origin string
	// Lines are the amounts in the document's text. Empty when the document
	// has no text, or its text is derived (extracted) rather than the
	// caller's own.
	Lines []sourceLine
}

// loadSourceDoc loads the source document of a bill being written: nil when
// ref is nil, an isError result when no such document is in the family.
func loadSourceDoc(ctx context.Context, q querier, householdID uuid.UUID, ref *uuid.UUID, example map[string]any) (*sourceDoc, *mcp.CallToolResult, error) {
	if ref == nil {
		return nil, nil, nil
	}
	d := &sourceDoc{ID: *ref}
	var text *string
	err := q.QueryRow(ctx,
		`SELECT COALESCE(NULLIF(btrim(title), ''), name), text_origin,
		        CASE WHEN text_origin = 'raw' THEN extracted_text END
		   FROM files WHERE id = $1 AND household_id = $2`,
		*ref, householdID,
	).Scan(&d.Title, &d.Origin, &text)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, respond.Invalid("source_document_id",
			fmt.Sprintf("No document with id %s was found in this family.", *ref),
			"Call document_find to get the document's id, or leave source_document_id out.", example), nil
	}
	if err != nil {
		return nil, nil, err
	}
	if text != nil {
		d.Lines = amountsIn(*text)
	}
	return d, nil, nil
}

// amountCheck describes how a recorded amount compares with the source
// document's text.
type amountCheck struct {
	Amount float64
	Doc    sourceDoc
	// Match is the source line carrying the same amount, nil when the source
	// has none.
	Match *sourceLine
}

// agreed reports whether the recorded amount appears in the source.
func (c amountCheck) agreed() bool { return c.Match != nil }

// enumAmounts lists the distinct amounts found, as money, for a message.
func (c amountCheck) enumAmounts() string {
	var parts []string
	seen := map[string]bool{}
	for _, l := range c.Doc.Lines {
		s := money(l.Amount)
		if seen[s] {
			continue
		}
		seen[s] = true
		parts = append(parts, s)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ", ")
}

// firstLine is the source line a caller is pointed at: the first amount found.
func (c amountCheck) firstLine() string {
	if len(c.Doc.Lines) == 0 {
		return ""
	}
	return c.Doc.Lines[0].Line
}

// checkAmount compares want with the amounts in doc.
func checkAmount(doc *sourceDoc, want float64) *amountCheck {
	if doc == nil {
		return nil
	}
	c := amountCheck{Amount: want, Doc: *doc}
	c.Match = findAmount(doc.Lines, want)
	return &c
}

// warn is the sentence a mismatch adds to the summary, or "" when the
// recorded amount is in the source or the source has no amounts to compare.
func (c *amountCheck) warn() string {
	if c == nil || c.agreed() || len(c.Doc.Lines) == 0 {
		return ""
	}
	return fmt.Sprintf(" This amount (%s) is not in %q, which says %s: %s. "+
		"Re-read the document and use its amount, or leave source_document_id out if it came from somewhere else.",
		money(c.Amount), c.Doc.Title, c.enumAmounts(), strconv.Quote(c.firstLine()))
}

// cite is the sentence a match adds, or "".
func (c *amountCheck) cite() string {
	if c == nil || !c.agreed() {
		return ""
	}
	return fmt.Sprintf(" Its amount %s matches %q.", money(c.Amount), c.Match.Line)
}

// note is what a source document with no comparable amount adds, or "".
func (c *amountCheck) note() string {
	if c == nil || len(c.Doc.Lines) > 0 {
		return ""
	}
	if c.Doc.Origin != "raw" {
		return fmt.Sprintf(" %q holds no pasted text to compare the amount with.", c.Doc.Title)
	}
	return fmt.Sprintf(" No money amount could be read from %q to compare with.", c.Doc.Title)
}

// querier is the subset of pgx.Tx and pgxpool.Pool the source lookup uses.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// storedSourceDocument returns the source document already linked to a bill,
// or nil when it has none. pgx.ErrNoRows when the bill is not in the family.
func storedSourceDocument(ctx context.Context, q querier, billID uuid.UUID, householdID string) (*uuid.UUID, error) {
	var id *uuid.UUID
	err := q.QueryRow(ctx,
		`SELECT source_document_id FROM bills WHERE id = $1 AND household_id = $2`,
		billID, householdID,
	).Scan(&id)
	return id, err
}
