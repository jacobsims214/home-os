package caldav

import (
	"encoding/xml"
	"strings"
	"testing"
)

// TestClassifySetProp covers the (namespace, local-name) → action mapping that
// drives PROPPATCH's DB update and response classification. The function is
// pure (no XML/HTTP/DB) so we can exercise every branch directly.
//
// The cases below mirror the formats Apple Calendar and other CalDAV clients
// actually send:
//   - Apple Calendar sends calendar-color in the http://apple.com/ns/ical/
//     namespace (the "I" prefix in its PROPPATCH bodies).
//   - RFC 6264 §3 / RFC 4791 §5.2.1{Calendar Properties} allow calendar-color
//     in the urn:ietf:params:xml:ns:caldav namespace (the "C" prefix).
//   - displayname is always in the DAV: namespace.
//   - Anything else (random Apple extensions like CS:publishing-enabled, or
//     principal-URL on a calendar, or future props we don't store yet) must be
//     classified as unsupported so the response reports 403 for them.
func TestClassifySetProp(t *testing.T) {
	cases := []struct {
		name       string
		xmlName    xml.Name
		value      string
		wantAction proppatchAction
		wantValue  string
	}{
		// Supported: displayname in DAV: namespace.
		{
			name: "DAV:displayname with simple value",
			xmlName: xml.Name{Space: nsDAV, Local: "displayname"},
			value:    "Johnson Family",
			wantAction: actionSetName,
			wantValue:  "Johnson Family",
		},
		{
			name: "DAV:displayname value is whitespace-trimmed",
			xmlName: xml.Name{Space: nsDAV, Local: "displayname"},
			value:    "\n  Johnson Family\n  ",
			wantAction: actionSetName,
			wantValue:  "Johnson Family",
		},
		{
			name: "DAV:displayname empty value kept (caller decides to skip)",
			xmlName: xml.Name{Space: nsDAV, Local: "displayname"},
			value:    "",
			wantAction: actionSetName,
			wantValue:  "",
		},
		{
			name: "DAV:displayname whitespace-only value trims to empty",
			xmlName: xml.Name{Space: nsDAV, Local: "displayname"},
			value:    "   \t\n",
			wantAction: actionSetName,
			wantValue:  "",
		},

		// Supported: calendar-color in CalDAV namespace.
		{
			name: "C:calendar-color (urn:ietf:params:xml:ns:caldav)",
			xmlName: xml.Name{Space: nsC, Local: "calendar-color"},
			value:    "#1BADF8",
			wantAction: actionSetColor,
			wantValue:  "#1BADF8",
		},
		{
			name: "C:calendar-color value is whitespace-trimmed",
			xmlName: xml.Name{Space: nsC, Local: "calendar-color"},
			value:    "\n#1BADF8\n",
			wantAction: actionSetColor,
			wantValue:  "#1BADF8",
		},
		{
			name: "C:calendar-color empty value kept (means \"clear color\")",
			xmlName: xml.Name{Space: nsC, Local: "calendar-color"},
			value:    "",
			wantAction: actionSetColor,
			wantValue:  "",
		},

		// Supported: calendar-color in Apple iCal namespace. Apple Calendar
		// uses this namespace in practice; the value targets the same column.
		{
			name: "I:calendar-color (http://apple.com/ns/ical/)",
			xmlName: xml.Name{Space: nsIcal, Local: "calendar-color"},
			value:    "#FFCC00",
			wantAction: actionSetColor,
			wantValue:  "#FFCC00",
		},
		{
			name: "I:calendar-color empty value kept (means \"clear color\")",
			xmlName: xml.Name{Space: nsIcal, Local: "calendar-color"},
			value:    "",
			wantAction: actionSetColor,
			wantValue:  "",
		},

		// Unsupported: other DAV props we don't persist on calendars.
		{
			name: "DAV:owner unsupported",
			xmlName: xml.Name{Space: nsDAV, Local: "owner"},
			value:    "<D:href>/dav/</D:href>",
			wantAction: actionUnsupported,
		},
		{
			name: "DAV:supported-report-set unsupported",
			xmlName: xml.Name{Space: nsDAV, Local: "supported-report-set"},
			value:    "",
			wantAction: actionUnsupported,
		},

		// Unsupported: other CalDAV props we don't persist.
		{
			name: "C:calendar-description unsupported",
			xmlName: xml.Name{Space: nsC, Local: "calendar-description"},
			value:    "some text",
			wantAction: actionUnsupported,
		},
		{
			name: "C:supported-calendar-component-set unsupported",
			xmlName: xml.Name{Space: nsC, Local: "supported-calendar-component-set"},
			value:    "",
			wantAction: actionUnsupported,
		},

		// Unsupported: CalendarServer extensions.
		{
			name: "CS:getctag unsupported (read-only)",
			xmlName: xml.Name{Space: nsCS, Local: "getctag"},
			value:    "abc",
			wantAction: actionUnsupported,
		},

		// Unsupported: anything in a totally unknown namespace.
		{
			name: "unknown namespace unsupported",
			xmlName: xml.Name{Space: "http://example.com/ns/random", Local: "foo"},
			value:    "bar",
			wantAction: actionUnsupported,
		},
		{
			name: "empty namespace unsupported",
			xmlName: xml.Name{Space: "", Local: "foo"},
			value:    "bar",
			wantAction: actionUnsupported,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifySetProp(rawProp{XMLName: tc.xmlName, Value: tc.value})
			if got.action != tc.wantAction {
				t.Fatalf("classifySetProp action = %d, want %d (name=%s/%s)",
					got.action, tc.wantAction, tc.xmlName.Space, tc.xmlName.Local)
			}
			// Only check value for supported actions; unsupported values are
			// intentionally ignored by the handler.
			if tc.wantAction != actionUnsupported && got.value != tc.wantValue {
				t.Errorf("classifySetProp value = %q, want %q", got.value, tc.wantValue)
			}
			if got.name != tc.xmlName {
				t.Errorf("classifySetProp name = %+v, want %+v", got.name, tc.xmlName)
			}
		})
	}
}

// TestClassifyRemoveProp verifies that every remove is classified as
// unsupported. The PROPPATCH handler does not support removing any calendar
// property: displayname is NOT NULL so it cannot be removed; calendar-color
// removal (clearing to NULL) is handled by the SET path with an empty value
// (which is what Apple Calendar actually sends when a user clears a color).
// Reporting 403 for every remove is RFC 4918 §9.2.2 compliant.
func TestClassifyRemoveProp(t *testing.T) {
	cases := []xml.Name{
		{Space: nsDAV, Local: "displayname"},
		{Space: nsC, Local: "calendar-color"},
		{Space: nsIcal, Local: "calendar-color"},
		{Space: nsDAV, Local: "owner"},
		{Space: nsCS, Local: "getctag"},
		{Space: "http://example.com/ns/random", Local: "foo"},
		{Space: "", Local: "bar"},
	}
	for _, name := range cases {
		t.Run(name.Space+":"+name.Local, func(t *testing.T) {
			got := classifyRemoveProp(rawProp{XMLName: name})
			if got.action != actionUnsupported {
				t.Fatalf("classifyRemoveProp action = %d, want %d for %s/%s",
					got.action, actionUnsupported, name.Space, name.Local)
			}
			if got.name != name {
				t.Errorf("classifyRemoveProp name = %+v, want %+v", got.name, name)
			}
		})
	}
}

// TestParsePropertyupdateBody covers the XML unmarshal of a PROPPATCH body
// into the propertyupdate struct. We verify that set/remove sections and
// their nested <D:prop> children are correctly captured, including across
// multiple namespaces (DAV:, C:, I:) used by Apple Calendar.
//
// This is a regression guard: a prior version of the struct used a single
// rawPropList field instead of slices for set/remove, which silently dropped
// all but the first set block. The cases below would have failed against
// that version.
func TestParsePropertyupdateBody(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantSets  int
		wantRm    int
		wantProps []xml.Name // first set's props, in order
	}{
		{
			name: "Apple Calendar rename + recolor",
			body: `<?xml version="1.0" encoding="utf-8"?>
<D:propertyupdate xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:I="http://apple.com/ns/ical/">
  <D:set>
    <D:prop>
      <D:displayname>Johnson Family</D:displayname>
      <I:calendar-color>#1BADF8</I:calendar-color>
    </D:prop>
  </D:set>
</D:propertyupdate>`,
			wantSets:  1,
			wantRm:    0,
			wantProps: []xml.Name{{Space: nsDAV, Local: "displayname"}, {Space: nsIcal, Local: "calendar-color"}},
		},
		{
			name: "RFC 6264 CalDAV-namespace color",
			body: `<?xml version="1.0" encoding="utf-8"?>
<D:propertyupdate xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:set>
    <D:prop>
      <C:calendar-color>#FF0000</C:calendar-color>
    </D:prop>
  </D:set>
</D:propertyupdate>`,
			wantSets:  1,
			wantRm:    0,
			wantProps: []xml.Name{{Space: nsC, Local: "calendar-color"}},
		},
		{
			name: "multiple set blocks are all captured",
			body: `<?xml version="1.0" encoding="utf-8"?>
<D:propertyupdate xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:set><D:prop><D:displayname>A</D:displayname></D:prop></D:set>
  <D:set><D:prop><C:calendar-color>#0</C:calendar-color></D:prop></D:set>
  <D:remove><D:prop><D:displayname/></D:prop></D:remove>
</D:propertyupdate>`,
			wantSets:  2,
			wantRm:    1,
			wantProps: []xml.Name{{Space: nsDAV, Local: "displayname"}},
		},
		{
			name: "empty body unmarshal fails",
			body: ``,
			// We don't unmarshal an empty body here; the handler rejects it
			// earlier with 400. This case is here to document that the
			// parser would error on it.
			wantSets: 0,
			wantRm:   0,
		},
		{
			name: "garbage XML unmarshal fails",
			body: `not xml`,
			// As above — handler returns 400 before this case reaches
			// the struct. Documenting the parser's behavior.
			wantSets: 0,
			wantRm:   0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var req propertyupdate
			err := xml.Unmarshal([]byte(tc.body), &req)
			if err != nil {
				if strings.Contains(tc.name, "fails") {
					// Expected.
					return
				}
				t.Fatalf("unexpected unmarshal error: %v (body=%s)", err, tc.body)
			}
			if strings.Contains(tc.name, "fails") {
				t.Fatalf("expected unmarshal to fail, but it succeeded: %+v", req)
			}
			if len(req.Sets) != tc.wantSets {
				t.Errorf("len(Sets) = %d, want %d", len(req.Sets), tc.wantSets)
			}
			if len(req.Removes) != tc.wantRm {
				t.Errorf("len(Removes) = %d, want %d", len(req.Removes), tc.wantRm)
			}
			if len(req.Sets) > 0 {
				gotNames := make([]xml.Name, 0, len(req.Sets[0].Prop.Props))
				for _, p := range req.Sets[0].Prop.Props {
					gotNames = append(gotNames, p.XMLName)
				}
				if !equalXMLNames(gotNames, tc.wantProps) {
					t.Errorf("first set props = %+v, want %+v", gotNames, tc.wantProps)
				}
			}
		})
	}
}

// equalXMLNames reports whether two slices of xml.Name are equal in order.
func equalXMLNames(a, b []xml.Name) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
