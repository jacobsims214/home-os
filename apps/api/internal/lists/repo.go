// Package lists serves shared family lists (grocery, packing, shopping, todo,
// chores) and their items (migration 046).
//
// Concurrency rules. Several family members edit the same list at once, so
// no write ever rewrites the whole list:
//   - every item change is a single-row (or id-set) UPDATE/INSERT/DELETE, so
//     checking off one item never overwrites another;
//   - batch operations run in one transaction and are all-or-nothing;
//   - adds and moves take a FOR NO KEY UPDATE lock on the list row, which
//     serialises adders/movers of the same list (so positions are distinct)
//     without blocking check-offs, edits or reads;
//   - order uses a fractional position, so a move updates one row. The list
//     is renumbered only when two neighbours' positions get too close.
package lists

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/api/pkg/apierr"
)

// Kinds mirrors lists_kind_check (migration 046).
var validKinds = map[string]bool{
	"grocery": true, "packing": true, "shopping": true,
	"todo": true, "chores": true, "other": true,
}

// ValidKind reports whether kind is an allowed list kind.
func ValidKind(kind string) bool { return validKinds[kind] }

// ErrArchived is returned when items of an archived list are changed.
var ErrArchived = errors.New("list is archived")

// minGap is the smallest position gap a move may split before the list is
// renumbered. float64 has ~15 significant digits, so this leaves room.
const minGap = 1e-9

// List is a row of lists.
type List struct {
	ID          uuid.UUID
	HouseholdID uuid.UUID
	Name        string
	Kind        string
	EntityType  string
	EntityID    uuid.UUID
	IsTemplate  bool
	Archived    bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
	ItemCount   int
	OpenCount   int
}

// Item is a row of list_items.
type Item struct {
	ID          uuid.UUID
	ListID      uuid.UUID
	HouseholdID uuid.UUID
	Text        string
	Quantity    *float64
	Unit        *string
	Section     *string
	Checked     bool
	CheckedAt   *time.Time
	// CheckedBy is the member who checked the item off (migration 066,
	// filled by trigger from the request's actor); nil when unchecked or
	// unknown. CheckedByName is that user's name.
	CheckedBy        *uuid.UUID
	CheckedByName    *string
	AssigneePersonID *uuid.UUID
	DueDate          *string // YYYY-MM-DD
	Position         float64
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// NewItem is the input for adding an item.
type NewItem struct {
	Text             string
	Quantity         *float64
	Unit             *string
	Section          *string
	AssigneePersonID *uuid.UUID
	DueDate          *string
	Checked          bool
}

// NewList is the input for creating a list. A nil EntityID means the
// household itself (the 046 lists_default_entity trigger fills it in).
type NewList struct {
	Name       string
	Kind       string
	EntityType *string
	EntityID   *uuid.UUID
	IsTemplate bool
	Items      []NewItem
}

// ListPatch holds the optional fields of a list update.
type ListPatch struct {
	Name       *string
	Kind       *string
	EntityType *string
	EntityID   *uuid.UUID
	IsTemplate *bool
	Archived   *bool
}

// ItemPatch holds the optional fields of an item update. The Set flags mark
// nullable fields the caller sent (possibly as null, to clear them).
type ItemPatch struct {
	Text                *string
	Quantity            *float64
	QuantitySet         bool
	Unit                *string
	UnitSet             bool
	Section             *string
	SectionSet          bool
	AssigneePersonID    *uuid.UUID
	AssigneePersonIDSet bool
	DueDate             *string
	DueDateSet          bool
	Checked             *bool
}

// ListFilter narrows List.
type ListFilter struct {
	Kind       string
	Archived   *bool
	IsTemplate *bool
	EntityType string
	EntityID   *uuid.UUID
}

// Repo is the Postgres store for lists.
type Repo struct {
	pool *pgxpool.Pool
}

// NewRepo creates a Repo.
func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

const listCols = `l.id, l.household_id, l.name, l.kind, l.entity_type::text, l.entity_id,
	l.is_template, l.archived, l.created_at, l.updated_at,
	(SELECT count(*) FROM list_items i WHERE i.list_id = l.id)::int,
	(SELECT count(*) FROM list_items i WHERE i.list_id = l.id AND NOT i.checked)::int`

func scanList(row pgx.Row) (*List, error) {
	var l List
	err := row.Scan(&l.ID, &l.HouseholdID, &l.Name, &l.Kind, &l.EntityType, &l.EntityID,
		&l.IsTemplate, &l.Archived, &l.CreatedAt, &l.UpdatedAt, &l.ItemCount, &l.OpenCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apierr.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &l, nil
}

const itemCols = `i.id, i.list_id, i.household_id, i.text, i.quantity::float8, i.unit, i.section,
	i.checked, i.checked_at, i.assignee_person_id, i.due_date::text, i.position,
	i.created_at, i.updated_at,
	i.checked_by, (SELECT u.name FROM users u WHERE u.id = i.checked_by)`

// itemOrder is the canonical item order: position is not unique.
const itemOrder = `ORDER BY i.position, i.created_at, i.id`

func scanItems(rows pgx.Rows) ([]*Item, error) {
	defer rows.Close()
	out := []*Item{}
	for rows.Next() {
		var it Item
		if err := rows.Scan(&it.ID, &it.ListID, &it.HouseholdID, &it.Text, &it.Quantity, &it.Unit,
			&it.Section, &it.Checked, &it.CheckedAt, &it.AssigneePersonID, &it.DueDate,
			&it.Position, &it.CreatedAt, &it.UpdatedAt, &it.CheckedBy, &it.CheckedByName); err != nil {
			return nil, err
		}
		out = append(out, &it)
	}
	return out, rows.Err()
}

// --- lists ---

// List returns the household's lists, newest first.
func (r *Repo) List(ctx context.Context, hid uuid.UUID, f ListFilter) ([]*List, error) {
	q := `SELECT ` + listCols + ` FROM lists l WHERE l.household_id = $1`
	args := []any{hid}
	add := func(cond string, v any) {
		args = append(args, v)
		q += fmt.Sprintf(" AND "+cond, len(args))
	}
	if f.Kind != "" {
		add("l.kind = $%d", f.Kind)
	}
	if f.Archived != nil {
		add("l.archived = $%d", *f.Archived)
	}
	if f.IsTemplate != nil {
		add("l.is_template = $%d", *f.IsTemplate)
	}
	if f.EntityType != "" {
		add("l.entity_type::text = $%d", f.EntityType)
	}
	if f.EntityID != nil {
		add("l.entity_id = $%d", *f.EntityID)
	}
	q += ` ORDER BY l.updated_at DESC, l.id`
	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*List{}
	for rows.Next() {
		l, err := scanList(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Get returns one list of the household, or apierr.ErrNotFound.
func (r *Repo) Get(ctx context.Context, hid, id uuid.UUID) (*List, error) {
	return scanList(r.pool.QueryRow(ctx,
		`SELECT `+listCols+` FROM lists l WHERE l.id = $1 AND l.household_id = $2`, id, hid))
}

// Create inserts a list with its initial items in one transaction. When
// templateID is set, the source list's items are copied (unchecked) after
// in.Items, and empty Name/Kind default to the source's.
func (r *Repo) Create(ctx context.Context, hid uuid.UUID, in NewList, templateID *uuid.UUID) (*List, error) {
	var out *List
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if templateID != nil {
			var srcName, srcKind string
			err := tx.QueryRow(ctx,
				`SELECT name, kind FROM lists WHERE id = $1 AND household_id = $2`,
				*templateID, hid).Scan(&srcName, &srcKind)
			if errors.Is(err, pgx.ErrNoRows) {
				return apierr.ErrNotFound
			}
			if err != nil {
				return err
			}
			if in.Name == "" {
				in.Name = srcName
			}
			if in.Kind == "" {
				in.Kind = srcKind
			}
		}
		if in.Kind == "" {
			in.Kind = "other"
		}
		var id uuid.UUID
		if err := tx.QueryRow(ctx,
			`INSERT INTO lists (household_id, name, kind, entity_type, entity_id, is_template)
			 VALUES ($1, $2, $3, $4::entity_kind, $5, $6) RETURNING id`,
			hid, in.Name, in.Kind, in.EntityType, in.EntityID, in.IsTemplate).Scan(&id); err != nil {
			return err
		}
		if err := insertItems(ctx, tx, id, hid, in.Items); err != nil {
			return err
		}
		if templateID != nil {
			// position NULL lets the 046 trigger append after in.Items, in
			// the source's order (one INSERT ... SELECT runs row by row).
			if _, err := tx.Exec(ctx, `
				INSERT INTO list_items (list_id, household_id, text, quantity, unit, section,
				                        assignee_person_id, due_date)
				SELECT $1, i.household_id, i.text, i.quantity, i.unit, i.section,
				       i.assignee_person_id, i.due_date
				  FROM list_items i
				 WHERE i.list_id = $2 AND i.household_id = $3
				 `+itemOrder, id, *templateID, hid); err != nil {
				return err
			}
		}
		var err error
		out, err = scanList(tx.QueryRow(ctx, `SELECT `+listCols+` FROM lists l WHERE l.id = $1`, id))
		return err
	})
	return out, err
}

// Update applies a patch to a list.
func (r *Repo) Update(ctx context.Context, hid, id uuid.UUID, p ListPatch) (*List, error) {
	// entity_type and entity_id move together: sending only entity_id keeps
	// the current type; sending entity_type 'household' with no id means the
	// family itself.
	var hhEntity bool
	if p.EntityType != nil && *p.EntityType == "household" && p.EntityID == nil {
		hhEntity = true
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE lists SET
		  name        = COALESCE($3, name),
		  kind        = COALESCE($4, kind),
		  entity_type = COALESCE($5::entity_kind, entity_type),
		  entity_id   = CASE WHEN $8 THEN household_id ELSE COALESCE($6, entity_id) END,
		  is_template = COALESCE($7, is_template),
		  archived    = COALESCE($9, archived)
		WHERE id = $1 AND household_id = $2`,
		id, hid, p.Name, p.Kind, p.EntityType, p.EntityID, p.IsTemplate, hhEntity, p.Archived)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, apierr.ErrNotFound
	}
	return r.Get(ctx, hid, id)
}

// Delete removes a list and (by cascade) its items.
func (r *Repo) Delete(ctx context.Context, hid, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM lists WHERE id = $1 AND household_id = $2`, id, hid)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apierr.ErrNotFound
	}
	return nil
}

// --- items ---

// Items returns a list's items in order. With since set, only items changed
// after it (updated_at > since) are returned, for polling clients.
func (r *Repo) Items(ctx context.Context, hid, listID uuid.UUID, since *time.Time) ([]*Item, error) {
	if err := r.listExists(ctx, hid, listID); err != nil {
		return nil, err
	}
	q := `SELECT ` + itemCols + ` FROM list_items i WHERE i.list_id = $1 AND i.household_id = $2`
	args := []any{listID, hid}
	if since != nil {
		q += ` AND i.updated_at > $3`
		args = append(args, *since)
	}
	rows, err := r.pool.Query(ctx, q+" "+itemOrder, args...)
	if err != nil {
		return nil, err
	}
	return scanItems(rows)
}

func (r *Repo) listExists(ctx context.Context, hid, listID uuid.UUID) error {
	var ok bool
	err := r.pool.QueryRow(ctx,
		`SELECT true FROM lists WHERE id = $1 AND household_id = $2`, listID, hid).Scan(&ok)
	if errors.Is(err, pgx.ErrNoRows) {
		return apierr.ErrNotFound
	}
	return err
}

// lockList takes the list row lock used by adders and movers and checks the
// list is the household's and not archived.
func lockList(ctx context.Context, tx pgx.Tx, hid, listID uuid.UUID) error {
	var archived bool
	err := tx.QueryRow(ctx,
		`SELECT archived FROM lists WHERE id = $1 AND household_id = $2 FOR NO KEY UPDATE`,
		listID, hid).Scan(&archived)
	if errors.Is(err, pgx.ErrNoRows) {
		return apierr.ErrNotFound
	}
	if err != nil {
		return err
	}
	if archived {
		return ErrArchived
	}
	return nil
}

// checkWritable checks the list is the household's and not archived, with
// no lock (check-offs and edits must not queue behind adders).
func checkWritable(ctx context.Context, tx pgx.Tx, hid, listID uuid.UUID) error {
	var archived bool
	err := tx.QueryRow(ctx,
		`SELECT archived FROM lists WHERE id = $1 AND household_id = $2`,
		listID, hid).Scan(&archived)
	if errors.Is(err, pgx.ErrNoRows) {
		return apierr.ErrNotFound
	}
	if err != nil {
		return err
	}
	if archived {
		return ErrArchived
	}
	return nil
}

func insertItems(ctx context.Context, tx pgx.Tx, listID, hid uuid.UUID, items []NewItem) error {
	for _, it := range items {
		if _, err := tx.Exec(ctx, `
			INSERT INTO list_items (list_id, household_id, text, quantity, unit, section,
			                        assignee_person_id, due_date, checked)
			VALUES ($1, $2, $3, $4::numeric, $5, $6, $7, $8::date, $9)`,
			listID, hid, it.Text, it.Quantity, it.Unit, it.Section,
			it.AssigneePersonID, it.DueDate, it.Checked); err != nil {
			return err
		}
	}
	return nil
}

// AddItems appends items to a list atomically and returns them in order.
func (r *Repo) AddItems(ctx context.Context, hid, listID uuid.UUID, items []NewItem) ([]*Item, error) {
	var out []*Item
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if err := lockList(ctx, tx, hid, listID); err != nil {
			return err
		}
		ids := make([]uuid.UUID, 0, len(items))
		for _, it := range items {
			var id uuid.UUID
			if err := tx.QueryRow(ctx, `
				INSERT INTO list_items (list_id, household_id, text, quantity, unit, section,
				                        assignee_person_id, due_date, checked)
				VALUES ($1, $2, $3, $4::numeric, $5, $6, $7, $8::date, $9) RETURNING id`,
				listID, hid, it.Text, it.Quantity, it.Unit, it.Section,
				it.AssigneePersonID, it.DueDate, it.Checked).Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		rows, err := tx.Query(ctx, `SELECT `+itemCols+` FROM list_items i WHERE i.id = ANY($1) `+itemOrder, ids)
		if err != nil {
			return err
		}
		out, err = scanItems(rows)
		return err
	})
	return out, err
}

// SetChecked checks or unchecks a set of items atomically: every id must be
// an item of the list, or nothing changes and apierr.ErrNotFound is
// returned. Items already in the wanted state are left untouched (their
// checked_at and updated_at are kept). Returns the items, in order.
func (r *Repo) SetChecked(ctx context.Context, hid, listID uuid.UUID, ids []uuid.UUID, checked bool) ([]*Item, error) {
	var out []*Item
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if err := checkWritable(ctx, tx, hid, listID); err != nil {
			return err
		}
		if err := lockItems(ctx, tx, hid, listID, ids); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE list_items SET checked = $4
			 WHERE list_id = $1 AND household_id = $2 AND id = ANY($3) AND checked <> $4`,
			listID, hid, ids, checked); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+itemCols+` FROM list_items i WHERE i.id = ANY($1) `+itemOrder, ids)
		if err != nil {
			return err
		}
		out, err = scanItems(rows)
		return err
	})
	return out, err
}

// lockItems row-locks exactly the given items and fails with
// apierr.ErrNotFound unless every id is an item of the list.
func lockItems(ctx context.Context, tx pgx.Tx, hid, listID uuid.UUID, ids []uuid.UUID) error {
	var n int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM (
		  SELECT id FROM list_items
		   WHERE list_id = $1 AND household_id = $2 AND id = ANY($3)
		   ORDER BY id FOR NO KEY UPDATE) t`, listID, hid, ids).Scan(&n); err != nil {
		return err
	}
	if n != len(ids) {
		return apierr.ErrNotFound
	}
	return nil
}

// RemoveItems deletes a set of items atomically (all ids must be items of
// the list, else nothing is deleted and apierr.ErrNotFound is returned).
func (r *Repo) RemoveItems(ctx context.Context, hid, listID uuid.UUID, ids []uuid.UUID) error {
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if err := checkWritable(ctx, tx, hid, listID); err != nil {
			return err
		}
		// Lock in id order first (as SetChecked does) so overlapping batches
		// can't deadlock, and fail before deleting anything.
		if err := lockItems(ctx, tx, hid, listID, ids); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx,
			`DELETE FROM list_items WHERE list_id = $1 AND household_id = $2 AND id = ANY($3)`,
			listID, hid, ids)
		if err != nil {
			return err
		}
		if int(tag.RowsAffected()) != len(ids) {
			return apierr.ErrNotFound // rolls back the partial delete
		}
		return nil
	})
}

// UpdateItem applies a patch to one item (a single-row UPDATE).
func (r *Repo) UpdateItem(ctx context.Context, hid, listID, itemID uuid.UUID, p ItemPatch) (*Item, error) {
	var out *Item
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if err := checkWritable(ctx, tx, hid, listID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE list_items SET
			  text               = COALESCE($4, text),
			  quantity           = CASE WHEN $6  THEN $5::numeric ELSE quantity END,
			  unit               = CASE WHEN $8  THEN $7 ELSE unit END,
			  section            = CASE WHEN $10 THEN $9 ELSE section END,
			  assignee_person_id = CASE WHEN $12 THEN $11::uuid ELSE assignee_person_id END,
			  due_date           = CASE WHEN $14 THEN $13::date ELSE due_date END,
			  checked            = COALESCE($15, checked)
			WHERE id = $1 AND list_id = $2 AND household_id = $3`,
			itemID, listID, hid, p.Text,
			p.Quantity, p.QuantitySet, p.Unit, p.UnitSet, p.Section, p.SectionSet,
			p.AssigneePersonID, p.AssigneePersonIDSet, p.DueDate, p.DueDateSet, p.Checked)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return apierr.ErrNotFound
		}
		rows, err := tx.Query(ctx, `SELECT `+itemCols+` FROM list_items i WHERE i.id = $1`, itemID)
		if err != nil {
			return err
		}
		items, err := scanItems(rows)
		if err != nil {
			return err
		}
		out = items[0]
		return nil
	})
	return out, err
}

type posKey struct {
	pos float64
	at  time.Time
	id  uuid.UUID
}

// MoveItem moves one item to just after afterID (nil: to the top). Only the
// moved item's row is written, unless its new neighbours' positions are too
// close together, in which case the list is renumbered first.
func (r *Repo) MoveItem(ctx context.Context, hid, listID, itemID uuid.UUID, afterID *uuid.UUID) (*Item, error) {
	if afterID != nil && *afterID == itemID {
		return nil, apierr.ErrBadRequest
	}
	var out *Item
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if err := lockList(ctx, tx, hid, listID); err != nil {
			return err
		}
		ids := []uuid.UUID{itemID}
		if afterID != nil {
			ids = append(ids, *afterID)
		}
		if err := lockItems(ctx, tx, hid, listID, ids); err != nil {
			return err
		}
		for attempt := 0; ; attempt++ {
			pos, ok, err := newPosition(ctx, tx, listID, itemID, afterID)
			if err != nil {
				return err
			}
			if ok || attempt > 0 {
				if _, err := tx.Exec(ctx,
					`UPDATE list_items SET position = $2 WHERE id = $1 AND position IS DISTINCT FROM $2`,
					itemID, pos); err != nil {
					return err
				}
				break
			}
			if err := renumber(ctx, tx, listID); err != nil {
				return err
			}
		}
		rows, err := tx.Query(ctx, `SELECT `+itemCols+` FROM list_items i WHERE i.id = $1`, itemID)
		if err != nil {
			return err
		}
		items, err := scanItems(rows)
		if err != nil {
			return err
		}
		out = items[0]
		return nil
	})
	return out, err
}

// newPosition computes the moved item's position. ok is false when the gap
// to split is too small and the list should be renumbered.
func newPosition(ctx context.Context, tx pgx.Tx, listID, itemID uuid.UUID, afterID *uuid.UUID) (float64, bool, error) {
	next := func(after *posKey) (*posKey, error) {
		q := `SELECT position, created_at, id FROM list_items
		       WHERE list_id = $1 AND id <> $2`
		args := []any{listID, itemID}
		if after != nil {
			q += ` AND (position, created_at, id) > ($3, $4, $5)`
			args = append(args, after.pos, after.at, after.id)
		}
		var k posKey
		err := tx.QueryRow(ctx, q+` ORDER BY position, created_at, id LIMIT 1`, args...).
			Scan(&k.pos, &k.at, &k.id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return &k, nil
	}

	if afterID == nil {
		first, err := next(nil)
		if err != nil || first == nil {
			return 0, true, err
		}
		return first.pos - 1, true, nil
	}
	var lo posKey
	if err := tx.QueryRow(ctx,
		`SELECT position, created_at, id FROM list_items WHERE id = $1`, *afterID).
		Scan(&lo.pos, &lo.at, &lo.id); err != nil {
		return 0, false, err
	}
	hi, err := next(&lo)
	if err != nil {
		return 0, false, err
	}
	if hi == nil {
		return lo.pos + 1, true, nil
	}
	if hi.pos-lo.pos < minGap {
		return lo.pos, false, nil
	}
	return lo.pos + (hi.pos-lo.pos)/2, true, nil
}

// renumber rewrites positions to 0..n-1 in the current order. Rows already
// at their number are skipped. Callers hold the list lock.
func renumber(ctx context.Context, tx pgx.Tx, listID uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		UPDATE list_items i SET position = o.rn
		  FROM (SELECT id, (row_number() OVER (ORDER BY position, created_at, id) - 1)::float8 AS rn
		          FROM list_items WHERE list_id = $1) o
		 WHERE i.id = o.id AND i.position IS DISTINCT FROM o.rn`, listID)
	return err
}
