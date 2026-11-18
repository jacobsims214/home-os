package search

import (
	"strings"
	"unicode/utf8"
)

// Chunk sizing. The limit that matters is the embedding model's input window,
// not a Typesense size limit: all-MiniLM-L12-v2 reads 256 WordPiece tokens
// (including [CLS]/[SEP]) and silently drops the rest, and the file name is
// embedded in front of every chunk. English averages roughly 4 characters per
// token and OCR noise (numbers, broken words) pushes that lower, so a chunk of
// at most MaxChunkChars characters plus a typical file name stays inside the
// window and every word of the chunk counts toward its vector.
const (
	// MaxChunkChars is the maximum length of a chunk in bytes (UTF-8).
	MaxChunkChars = 700

	// ChunkOverlap is how much text consecutive chunks share, so a sentence
	// cut at a chunk boundary is still whole in one of them.
	ChunkOverlap = 100

	// MaxChunks caps the chunks per file (about 180KB of text, ~90 pages).
	// Each chunk costs one embedding at index time; text past the cap is not
	// indexed. Typesense has no hard document-size limit, but very large
	// documents make imports slow and memory-hungry, so the cap keeps one
	// huge upload from stalling the worker.
	MaxChunks = 300
)

// Chunk splits extracted text into chunks of at most MaxChunkChars bytes for
// indexing. Whitespace runs (OCR output is full of line breaks and column
// gaps) are collapsed to single spaces first. Chunks end at a sentence end
// when one falls in the second half of the window, else at a space, else
// (one very long "word") at a rune boundary; consecutive chunks overlap by
// about ChunkOverlap bytes. Empty text yields no chunks. truncated reports
// that the text was longer than MaxChunks chunks and the rest was dropped.
func Chunk(text string) (chunks []string, truncated bool) {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return nil, false
	}
	start := 0
	for start < len(text) {
		if len(chunks) == MaxChunks {
			return chunks, true
		}
		if len(text)-start <= MaxChunkChars {
			chunks = append(chunks, text[start:])
			break
		}
		end := breakPoint(text, start, start+MaxChunkChars)
		chunks = append(chunks, strings.TrimSpace(text[start:end]))

		next := end - ChunkOverlap
		// Start the overlap at a word boundary.
		if sp := strings.IndexByte(text[next:end], ' '); sp >= 0 {
			next += sp + 1
		} else {
			next = end
		}
		for next < len(text) && !utf8.RuneStart(text[next]) {
			next++
		}
		if next <= start {
			next = end
		}
		start = next
	}
	return chunks, false
}

// breakPoint returns where the chunk text[start:limit] should end:
// just after the last sentence end, or at the last space, in the second half
// of the window; otherwise at limit moved back to a rune boundary.
func breakPoint(text string, start, limit int) int {
	window := text[start:limit]
	half := len(window) / 2
	best := -1
	for _, sep := range []string{". ", "? ", "! ", "; "} {
		if i := strings.LastIndex(window, sep); i >= half && i+1 > best {
			best = i + 1
		}
	}
	if best > 0 {
		return start + best
	}
	if i := strings.LastIndexByte(window, ' '); i >= half {
		return start + i
	}
	end := limit
	for end > start+1 && !utf8.RuneStart(text[end]) {
		end--
	}
	return end
}
