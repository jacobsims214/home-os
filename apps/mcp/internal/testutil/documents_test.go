package testutil

// Tests for migration 048 (document metadata on files). See KB
// decisions/domain-model-v2.md, "048 document metadata".

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// Up/down/up with files in place: existing files become category 'other',
// down drops the columns and keeps the files, up again works.
func TestDocuments048_Reversible(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)
	blob := insertID(t, pool, `INSERT INTO file_blobs (data, size) VALUES ('x', 1) RETURNING id`)

	applyMigration(t, pool, "061_notify_creator_role.down.sql")  // newest first: restores 054's notify_entity_creator
	applyMigration(t, pool, "060_drop_pet_vet_columns.down.sql") // newest first: restores pets.vet_name / vet_phone
	applyMigration(t, pool, "058_same_family_fks.down.sql")      // newest first: 058's FKs need 043's vendors key
	applyMigration(t, pool, "057_secret_agent_share.down.sql")
	applyMigration(t, pool, "056_domain_v2_cleanup.down.sql") // newest first: 056 sits on top of 038-054
	applyMigration(t, pool, "055_activity_event_titles.down.sql")
	applyMigration(t, pool, "054_change_notifications.down.sql") // its trigger is on 050's activity_log
	applyMigration(t, pool, "050_authorship.down.sql")
	applyMigration(t, pool, "048_document_metadata.down.sql")
	if n := countRows(t, pool, `SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'files' AND column_name IN ('title','category','document_date','expires_on','description','person_id')`); n != 0 {
		t.Fatalf("down: %d metadata columns left", n)
	}
	// A file written before 048.
	old := insertID(t, pool, `INSERT INTO files (household_id, blob_id, name, size) VALUES ($1, $2, 'scan.pdf', 1) RETURNING id`, f.household, blob)

	for cycle := 0; cycle < 2; cycle++ {
		applyMigration(t, pool, "048_document_metadata.up.sql")
		var category string
		var title, description *string
		mustScan(t, pool.QueryRow(ctx, `SELECT category, title, description FROM files WHERE id = $1`, old), &category, &title, &description)
		if category != "other" || title != nil || description != nil {
			t.Errorf("cycle %d: existing file got category=%q title=%v description=%v", cycle, category, title, description)
		}
		person := insertID(t, pool, `INSERT INTO people (household_id, display_name) VALUES ($1, 'Jo') RETURNING id`, f.household)
		mustExec(t, pool, `UPDATE files SET category = 'medical', person_id = $2, expires_on = '2027-01-01' WHERE id = $1`, old, person)
		applyMigration(t, pool, "048_document_metadata.down.sql")
		if n := countRows(t, pool, `SELECT count(*) FROM files WHERE id = $1`, old); n != 1 {
			t.Fatalf("cycle %d: down lost the file", cycle)
		}
	}
	applyMigration(t, pool, "048_document_metadata.up.sql")
	applyMigration(t, pool, "050_authorship.up.sql")
}

// Category check, same-family person, person delete, date order, blank title
// and the indexes.
func TestDocuments048_Rules(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)
	other := seedV2Family(t, pool)
	blob := insertID(t, pool, `INSERT INTO file_blobs (data, size) VALUES ('x', 1) RETURNING id`)
	newFile := func(household uuid.UUID) uuid.UUID {
		return insertID(t, pool, `INSERT INTO files (household_id, blob_id, name, size) VALUES ($1, $2, 'doc.pdf', 1) RETURNING id`, household, blob)
	}

	file := newFile(f.household)
	var category string
	mustScan(t, pool.QueryRow(ctx, `SELECT category FROM files WHERE id = $1`, file), &category)
	if category != "other" {
		t.Errorf("default category = %q, want other", category)
	}
	for _, c := range []string{"receipt", "tax", "insurance", "medical", "manual", "warranty", "school", "legal", "vehicle", "home", "other"} {
		mustExec(t, pool, `UPDATE files SET category = $2 WHERE id = $1`, file, c)
	}
	_, err := pool.Exec(ctx, `UPDATE files SET category = 'passport' WHERE id = $1`, file)
	wantSQLState(t, err, sqlstateCheckViolation)
	_, err = pool.Exec(ctx, `UPDATE files SET category = NULL WHERE id = $1`, file)
	wantSQLState(t, err, "23502") // not_null_violation

	_, err = pool.Exec(ctx, `UPDATE files SET title = '   ' WHERE id = $1`, file)
	wantSQLState(t, err, sqlstateCheckViolation)
	mustExec(t, pool, `UPDATE files SET title = '2026 homeowners policy', description = 'Renewed in March' WHERE id = $1`, file)

	// expires_on may not be before document_date; either alone is fine.
	_, err = pool.Exec(ctx, `UPDATE files SET document_date = '2026-03-01', expires_on = '2026-02-01' WHERE id = $1`, file)
	wantSQLState(t, err, sqlstateCheckViolation)
	mustExec(t, pool, `UPDATE files SET document_date = '2026-03-01', expires_on = '2027-03-01' WHERE id = $1`, file)
	mustExec(t, pool, `UPDATE files SET document_date = NULL, expires_on = '2020-01-01' WHERE id = $1`, file)

	// person_id must be a person in the same family.
	jo := insertID(t, pool, `INSERT INTO people (household_id, display_name) VALUES ($1, 'Jo') RETURNING id`, f.household)
	stranger := insertID(t, pool, `INSERT INTO people (household_id, display_name) VALUES ($1, 'Sam') RETURNING id`, other.household)
	_, err = pool.Exec(ctx, `UPDATE files SET person_id = $2 WHERE id = $1`, file, stranger)
	wantSQLState(t, err, sqlstateFKViolation)
	mustExec(t, pool, `UPDATE files SET person_id = $2 WHERE id = $1`, file, jo)
	// A file can be Jo's and about a vehicle at the same time.
	mustExec(t, pool, `UPDATE files SET entity_type = 'vehicle', entity_id = $2 WHERE id = $1`, file, f.vehicle)

	// Deleting the person keeps the file (and its household) and clears person_id.
	mustExec(t, pool, `DELETE FROM people WHERE id = $1`, jo)
	var personID *uuid.UUID
	var household uuid.UUID
	mustScan(t, pool.QueryRow(ctx, `SELECT person_id, household_id FROM files WHERE id = $1`, file), &personID, &household)
	if personID != nil || household != f.household {
		t.Errorf("after person delete: person_id=%v household=%s, want NULL and %s", personID, household, f.household)
	}

	for _, idx := range []string{"idx_files_household_category", "idx_files_expires_on", "idx_files_person"} {
		if n := countRows(t, pool, `SELECT count(*) FROM pg_indexes WHERE tablename = 'files' AND indexname = $1`, idx); n != 1 {
			t.Errorf("index %s missing", idx)
		}
	}
	// The category filter uses the index (seqscan off: the table is tiny).
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(ctx, `EXPLAIN (FORMAT TEXT) SELECT id FROM files WHERE household_id = $1 AND category = 'insurance'`, f.household)
	if err != nil {
		t.Fatal(err)
	}
	var plan []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if want := "idx_files_household_category"; !strings.Contains(strings.Join(plan, "\n"), want) {
		t.Errorf("category filter plan does not use %s:\n%s", want, strings.Join(plan, "\n"))
	}
}
