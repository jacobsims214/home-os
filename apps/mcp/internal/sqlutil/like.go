// Package sqlutil holds small SQL helpers shared by the MCP tool packages.
package sqlutil

import "strings"

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// EscapeLike escapes the LIKE wildcards % and _ (and the escape character \)
// so s matches literally in a LIKE/ILIKE pattern using ESCAPE '\'.
func EscapeLike(s string) string {
	return likeEscaper.Replace(s)
}

// ContainsPattern turns query into a LIKE/ILIKE pattern that matches it
// literally anywhere in the text. Use it with ESCAPE '\':
//
//	name ILIKE $1 ESCAPE '\'
func ContainsPattern(query string) string {
	return "%" + EscapeLike(query) + "%"
}
