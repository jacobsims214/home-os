package testutil

// Tests for migration 079 (#5516): the notification vocabularies.
//
// THE DEFECT. notifications.type and notifications.channel were bare VARCHARs
// (015) with no CHECK at all. On a live cluster at 078, `type = 'bill_du'`,
// `channel = 'carrier_pigeon'` and even `type = ''` were ACCEPTED. A
// notification whose type is misspelled is delivered, is unreadable by every
// client that filters on type, and drops out of idx_notifications_item_changed
// (a partial index hard-coded on the literal 'item_changed') at the same time.
//
// THE SECOND HALF. document_links pairs link_type with link_id/url, and nothing
// tied them together, so a 'url' link could carry no url and render blank. (The
// vocabulary half was already done by 056, and enforce_entity_owner() has
// refused a non-UUID link_id for file/secret since 038 -- both verified below so
// this test states what is true rather than what the review assumed.)
//
// WHAT THESE TESTS PIN DOWN
//
//  1. the two domains exist on the two columns, with their original
//     nullability and DEFAULT;
//  2. every Go constant in home-os/notify is a member of the domain in the
//     DATABASE (criterion 2), and every domain member is a Go constant -- so a
//     new type needs both halves or this fails;
//  3. the vocabulary is exactly the measured one: the set of values the
//     repository's writers produce equals the domain's membership;
//  4. the bad writes are refused and the good ones accepted, for type, channel
//     and the document_links link target;
//  5. up with a row outside the vocabulary fails and changes NOTHING; down
//     restores VARCHAR and the defect really comes back.

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/notify"
)

// hasSQLState reports whether err is a Postgres error with the given SQLSTATE.
// It is the non-fatal sibling of wantSQLState: these tests check many values in
// one loop, so one bad case must not stop the rest.
func hasSQLState(err error, code string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code
}

// domainMembers reads a domain's allowed values out of the catalogue's own
// constraint definition, so the assertion compares the schema to itself rather
// than to a second hand-written list.
func domainMembers(t *testing.T, pool *pgxpool.Pool, domain string) []string {
	t.Helper()
	var def string
	err := pool.QueryRow(context.Background(),
		`SELECT pg_get_constraintdef(oid) FROM pg_constraint
		  WHERE contypid = (SELECT oid FROM pg_type WHERE typname = $1)
		    AND contype = 'c'`, domain).Scan(&def)
	if err != nil {
		t.Fatalf("read %s's constraint: %v", domain, err)
	}
	// CHECK (((VALUE)::text = ANY ((ARRAY['a'::character varying, ...])::text[])))
	// or, for a single member, CHECK (((VALUE)::text = 'a'::text)).
	vals := regexp.MustCompile(`'([^']*)'`).FindAllStringSubmatch(def, -1)
	if len(vals) == 0 {
		t.Fatalf("%s: no values in %q", domain, def)
	}
	out := make([]string, 0, len(vals))
	for _, m := range vals {
		out = append(out, m[1])
	}
	sort.Strings(out)
	return out
}

// declaredVocab enumerates the Type and Channel constants DECLARED in
// packages/notify, by parsing the source rather than reading the Types/Channels
// slices.
//
// This is the difference that makes criterion 2 real: iterating notify.Types
// only proves the values someone remembered to register, so a constant added to
// the file and forgotten in the slice would pass. Parsing the declarations means
// ANY `X Type = "value"` in the package is checked against the database.
func declaredVocab(t *testing.T) (types, channels []string) {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test's source")
	}
	// apps/mcp/internal/testutil -> repo root -> packages/notify
	src := filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "packages", "notify", "notify.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || len(vs.Values) != 1 {
				continue
			}
			lit, ok := vs.Values[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			var val string
			if err := json.Unmarshal([]byte(lit.Value), &val); err != nil {
				continue
			}
			// The declared type, written as a bare identifier because every
			// constant in this package is in the package's own block.
			ident, ok := vs.Type.(*ast.Ident)
			if !ok {
				continue
			}
			switch ident.Name {
			case "Type":
				types = append(types, val)
			case "Channel":
				channels = append(channels, val)
			}
		}
	}
	if len(types) == 0 || len(channels) == 0 {
		t.Fatalf("found no Type/Channel constants in %s (the parse broke, not the code)", src)
	}
	sort.Strings(types)
	sort.Strings(channels)
	return types, channels
}

// TestNotificationVocabularySubset is criterion 2: the Go constants must be a
// subset of the CHECK values in the database.
//
// It reads the domain from the catalogue AND the constants from the package's
// source, so it cannot be satisfied by editing a list in Go. It checks the
// reverse direction too, so a type added to the migration without a constant is
// also reported.
func TestNotificationVocabularySubset(t *testing.T) {
	pool := NewPool(t)

	inDB := domainMembers(t, pool, "notification_type")
	channelsInDB := domainMembers(t, pool, "notification_channel")
	declaredTypes, declaredChannels := declaredVocab(t)

	has := func(set []string, v string) bool {
		for _, s := range set {
			if s == v {
				return true
			}
		}
		return false
	}

	// 1. every DECLARED constant is in the database. This is criterion 2: a
	// constant added to the package without extending 079 fails here.
	for _, typ := range declaredTypes {
		if !has(inDB, typ) {
			t.Errorf("notify.Type %q is not in the notification_type domain %v: "+
				"add it to migration 079 before using it", typ, inDB)
		}
	}
	for _, ch := range declaredChannels {
		if !has(channelsInDB, ch) {
			t.Errorf("notify.Channel %q is not in the notification_channel domain %v", ch, channelsInDB)
		}
	}

	// The registered slices must cover the declarations, or a caller iterating
	// notify.Types would silently miss a value.
	registered := map[string]bool{}
	for _, typ := range notify.Types {
		registered[string(typ)] = true
	}
	for _, typ := range declaredTypes {
		if !registered[typ] {
			t.Errorf("constant %q is declared in home-os/notify but missing from notify.Types", typ)
		}
	}

	// 2. every database value has a Go constant, so the two lists cannot drift
	// in the direction that leaves a writable value no caller can name.
	goTypes := map[string]bool{}
	for _, typ := range notify.Types {
		goTypes[string(typ)] = true
	}
	for _, v := range inDB {
		if !goTypes[v] {
			t.Errorf("notification_type member %q has no constant in home-os/notify", v)
		}
	}
	goChannels := map[string]bool{}
	for _, ch := range notify.Channels {
		goChannels[string(ch)] = true
	}
	for _, v := range channelsInDB {
		if !goChannels[v] {
			t.Errorf("notification_channel member %q has no constant in home-os/notify", v)
		}
	}

	// 3. no duplicates: two constants with one value would make the set
	// comparison above pass while a writer silently used the wrong name.
	if len(goTypes) != len(notify.Types) {
		t.Errorf("notify.Types has %d entries but only %d distinct values", len(notify.Types), len(goTypes))
	}
}

// TestNotificationVocabularyMatchesWriters pins the vocabulary to the values the
// repository's writers actually produce. Criterion 1 and 4 of the ticket are
// about the constraint existing; this is about it being the RIGHT list, which is
// the part a migration alone cannot prove.
//
// The expected set is written out here rather than grepped at test time: a grep
// would pass if a writer switched to a new literal AND the migration gained it
// in the same change, which is exactly the drift worth catching. Each value
// names its writer, so a reader can check the claim.
func TestNotificationVocabularyMatchesWriters(t *testing.T) {
	pool := NewPool(t)

	want := map[string]string{
		// packages/billdue/policy.go NotificationTypeBill
		// -> apps/worker/cmd/worker/billreminders.go
		"bill_due": "worker bill_due job (billdue.NotificationTypeBill)",
		// packages/billdue/policy.go NotificationTypeDigest -> same job
		"bill_digest": "worker bill digest (billdue.NotificationTypeDigest)",
		// apps/worker/cmd/worker/main.go maintenanceDueSQL
		"maintenance_due": "worker maintenance job",
		// apps/worker/internal/alarms NotificationType
		"calendar_alarm": "worker alarm job (alarms.NotificationType)",
		// notify_entity_creator() in 054/061/070
		"item_changed": "notify_entity_creator() trigger",
	}

	got := domainMembers(t, pool, "notification_type")
	if len(got) != len(want) {
		t.Errorf("notification_type has %d members %v, want %d", len(got), got, len(want))
	}
	for _, v := range got {
		if _, ok := want[v]; !ok {
			t.Errorf("notification_type member %q has no writer: %v", v, want)
		}
	}
	for v, writer := range want {
		found := false
		for _, g := range got {
			if g == v {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("notification_type is missing %q (%s): a writer produces it and "+
				"its insert would be refused", v, writer)
		}
	}

	// One channel, because one writer: 015's DEFAULT and every SQL writer set
	// in_app, and no Go code sets a channel at all.
	chans := domainMembers(t, pool, "notification_channel")
	if len(chans) != 1 || chans[0] != string(notify.ChannelInApp) {
		t.Errorf("notification_channel = %v, want exactly [in_app]: nothing writes another", chans)
	}
}

// TestNotificationVocabularyColumns checks the columns kept their shape:
// domain-typed, NOT NULL, and channel still DEFAULTing to in_app so a writer
// that omits it (every Go writer) keeps working.
func TestNotificationVocabularyColumns(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()

	for _, tc := range []struct{ column, domain string }{
		{"type", "notification_type"},
		{"channel", "notification_channel"},
	} {
		var domain *string
		var nullable, def *string
		err := pool.QueryRow(ctx,
			`SELECT domain_name, is_nullable, column_default
			   FROM information_schema.columns
			  WHERE table_name = 'notifications' AND column_name = $1`, tc.column,
		).Scan(&domain, &nullable, &def)
		if err != nil {
			t.Fatalf("read notifications.%s: %v", tc.column, err)
		}
		if domain == nil || *domain != tc.domain {
			t.Errorf("notifications.%s domain = %v, want %s", tc.column, domain, tc.domain)
		}
		if nullable == nil || *nullable != "NO" {
			t.Errorf("notifications.%s nullable = %v, want NO", tc.column, nullable)
		}
		if tc.column == "channel" && (def == nil || !strings.Contains(*def, "in_app")) {
			t.Errorf("notifications.channel default = %v, want in_app: Go writers omit the column", def)
		}
	}
}

// TestNotificationVocabularyRejects is the acceptance criterion 1 probe: an
// insert with an invalid type or channel is REJECTED, a valid one is accepted.
func TestNotificationVocabularyRejects(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)

	insert := func(typ, channel string) error {
		_, err := pool.Exec(ctx,
			`INSERT INTO notifications (household_id, type, title, channel)
			 VALUES ($1, $2, 'n', $3)`, f.household, typ, channel)
		return err
	}

	// The typo the ticket names, and the other ways a free-text column goes
	// wrong: empty, wrong case, and a plausible word nobody writes.
	for _, bad := range []string{"bill_du", "", "BILL_DUE", "reminder", "billdue", "bill_due "} {
		if err := insert(bad, string(notify.ChannelInApp)); err == nil {
			t.Errorf("type %q was ACCEPTED; the domain must refuse it", bad)
		} else if !hasSQLState(err, sqlstateCheckViolation) {
			t.Errorf("type %q: want a check violation, got %v", bad, err)
		}
	}
	for _, bad := range []string{"carrier_pigeon", "", "IN_APP", "email", "push", "sms"} {
		if err := insert(string(notify.TypeBillDue), bad); err == nil {
			t.Errorf("channel %q was ACCEPTED; the domain must refuse it", bad)
		} else if !hasSQLState(err, sqlstateCheckViolation) {
			t.Errorf("channel %q: want a check violation, got %v", bad, err)
		}
	}

	// Every valid type still inserts, with the default channel and explicitly.
	for _, typ := range notify.Types {
		if err := insert(string(typ), string(notify.ChannelInApp)); err != nil {
			t.Errorf("valid type %q was REFUSED: %v", typ, err)
		}
	}
	// A writer that omits the channel (every Go writer) still works: 015's
	// DEFAULT survives the type change.
	if _, err := pool.Exec(ctx,
		`INSERT INTO notifications (household_id, type, title) VALUES ($1, 'bill_due', 'no channel')`,
		f.household); err != nil {
		t.Errorf("insert without a channel was REFUSED (the DEFAULT was lost): %v", err)
	}
}

// TestDocumentLinksLinkTarget is acceptance criterion 3: link_type decides which
// column is the target.
func TestDocumentLinksLinkTarget(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)

	// link_type=url needs a url; a url link with link_id only is the case the
	// ticket names.
	if _, err := pool.Exec(ctx,
		`INSERT INTO document_links (household_id, entity_type, entity_id, link_type, link_id)
		 VALUES ($1, 'household', $1, 'url', gen_random_uuid()::text)`, f.household); err == nil {
		t.Error("a url link with link_id and no url was ACCEPTED; it renders as a blank row")
	} else if !hasSQLState(err, sqlstateCheckViolation) {
		t.Errorf("url link with no url: want a check violation, got %v", err)
	}
	// Blank is as useless as absent.
	if _, err := pool.Exec(ctx,
		`INSERT INTO document_links (household_id, entity_type, entity_id, link_type, url)
		 VALUES ($1, 'household', $1, 'url', '  ')`, f.household); err == nil {
		t.Error("a url link with a blank url was ACCEPTED")
	}

	// A file link with a link_id that is not a UUID. Refused by the trigger
	// since 038 and by the constraint since 079; either message is correct.
	for _, badID := range []string{"not-a-uuid", "", "1234", "paperless-42"} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO document_links (household_id, entity_type, entity_id, link_type, link_id)
			 VALUES ($1, 'household', $1, 'file', $2)`, f.household, badID); err == nil {
			t.Errorf("a file link with link_id %q was ACCEPTED", badID)
		}
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO document_links (household_id, entity_type, entity_id, link_type)
		 VALUES ($1, 'household', $1, 'file')`, f.household); err == nil {
		t.Error("a file link with no link_id was ACCEPTED")
	}

	// Two valid links are accepted: a url link, and a file link at a real file
	// of the same family.
	// A real file of the same family, so the trigger's same-family resolution
	// succeeds and only the link shape is under test.
	blob := insertID(t, pool, `INSERT INTO file_blobs (data, size) VALUES ('x', 1) RETURNING id`)
	fileID := insertID(t, pool,
		`INSERT INTO files (household_id, blob_id, name, size) VALUES ($1, $2, 'warranty.pdf', 1) RETURNING id`,
		f.household, blob)
	if _, err := pool.Exec(ctx,
		`INSERT INTO document_links (household_id, entity_type, entity_id, link_type, url)
		 VALUES ($1, 'household', $1, 'url', 'https://example.com/warranty')`, f.household); err != nil {
		t.Errorf("a url link with a url was REFUSED: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO document_links (household_id, entity_type, entity_id, link_type, link_id)
		 VALUES ($1, 'household', $1, 'file', $2)`, f.household, fileID); err != nil {
		t.Errorf("a file link at a real file was REFUSED: %v", err)
	}
}

// TestNotificationVocabularyUpRefusesBadRows is criterion 4: the migration names
// any row it could not constrain and why, rather than rewriting or dropping it.
//
// The walk is: down to 078, insert a row the old schema allowed, up again. The
// up must fail and change nothing.
func TestNotificationVocabularyUpRefusesBadRows(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)

	migrations, err := migrationsDir()
	if err != nil {
		t.Fatalf("locate migrations: %v", err)
	}
	down, err := os.ReadFile(filepath.Join(migrations, "079_notification_vocabularies.down.sql"))
	if err != nil {
		t.Fatalf("read 079 down: %v", err)
	}
	up, err := os.ReadFile(filepath.Join(migrations, "079_notification_vocabularies.up.sql"))
	if err != nil {
		t.Fatalf("read 079 up: %v", err)
	}

	if _, err := pool.Exec(ctx, string(down)); err != nil {
		t.Fatalf("apply 079 down: %v", err)
	}
	// The old schema accepts it: that is the defect.
	if _, err := pool.Exec(ctx,
		`INSERT INTO notifications (household_id, type, title) VALUES ($1, 'bill_du', 'typo')`,
		f.household); err != nil {
		t.Fatalf("the 078 schema refused 'bill_du', which contradicts the defect: %v", err)
	}

	_, upErr := pool.Exec(ctx, string(up))
	if upErr == nil {
		t.Fatal("079 up ACCEPTED a row with type='bill_du'; it must refuse and change nothing")
	}
	if !strings.Contains(upErr.Error(), "bill_du") {
		t.Errorf("the refusal must name the offending value, got: %v", upErr)
	}

	// Nothing changed: the column is still VARCHAR, and the row is still there.
	var domain *string
	if err := pool.QueryRow(ctx,
		`SELECT domain_name FROM information_schema.columns
		  WHERE table_name = 'notifications' AND column_name = 'type'`).Scan(&domain); err != nil {
		t.Fatalf("read type column: %v", err)
	}
	if domain != nil {
		t.Errorf("079 up changed the column before refusing: domain = %v", domain)
	}
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM notifications WHERE type = 'bill_du'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("the unconstrainable row was %d, want 1: the migration must not delete it", n)
	}

	// Repair it the way the migration's HINT says, and the up succeeds.
	if _, err := pool.Exec(ctx, `UPDATE notifications SET type = 'bill_due' WHERE type = 'bill_du'`); err != nil {
		t.Fatalf("repair: %v", err)
	}
	if _, err := pool.Exec(ctx, string(up)); err != nil {
		t.Fatalf("079 up after repair: %v", err)
	}
}

// TestNotificationVocabularyDownReversible is criterion 5's rollback half: down
// restores the VARCHARs, keeps every row, and the defect really comes back.
func TestNotificationVocabularyDownReversible(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)

	migrations, err := migrationsDir()
	if err != nil {
		t.Fatalf("locate migrations: %v", err)
	}
	down, err := os.ReadFile(filepath.Join(migrations, "079_notification_vocabularies.down.sql"))
	if err != nil {
		t.Fatalf("read 079 down: %v", err)
	}
	up, err := os.ReadFile(filepath.Join(migrations, "079_notification_vocabularies.up.sql"))
	if err != nil {
		t.Fatalf("read 079 up: %v", err)
	}

	var before int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM notifications`).Scan(&before); err != nil {
		t.Fatalf("count: %v", err)
	}
	if _, err := pool.Exec(ctx, string(down)); err != nil {
		t.Fatalf("apply 079 down: %v", err)
	}

	// Both columns are plain VARCHAR again, NOT NULL, channel still defaulted.
	for _, col := range []string{"type", "channel"} {
		var domain, nullable *string
		if err := pool.QueryRow(ctx,
			`SELECT domain_name, is_nullable FROM information_schema.columns
			  WHERE table_name = 'notifications' AND column_name = $1`, col).Scan(&domain, &nullable); err != nil {
			t.Fatalf("read %s: %v", col, err)
		}
		if domain != nil {
			t.Errorf("after down, notifications.%s is still domain %v", col, domain)
		}
		if nullable == nil || *nullable != "NO" {
			t.Errorf("after down, notifications.%s nullable = %v, want NO", col, nullable)
		}
	}
	var after int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM notifications`).Scan(&after); err != nil {
		t.Fatalf("count: %v", err)
	}
	if after != before {
		t.Errorf("down changed the row count: %d -> %d", before, after)
	}

	// The defect is back.
	if _, err := pool.Exec(ctx,
		`INSERT INTO notifications (household_id, type, title) VALUES ($1, 'bill_du', 'back')`,
		f.household); err != nil {
		t.Errorf("after down, 'bill_du' was still refused: the down did not restore the old schema: %v", err)
	}
	// Down twice is safe.
	if _, err := pool.Exec(ctx, string(down)); err != nil {
		t.Errorf("079 down is not idempotent: %v", err)
	}
	// And up again, after clearing the deliberate typo.
	if _, err := pool.Exec(ctx, `DELETE FROM notifications WHERE type = 'bill_du'`); err != nil {
		t.Fatalf("clear typo: %v", err)
	}
	if _, err := pool.Exec(ctx, string(up)); err != nil {
		t.Errorf("079 up after down: %v", err)
	}
}
