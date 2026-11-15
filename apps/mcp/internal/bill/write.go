package bill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/server"
)

// Column limits from the bills table (migrations 007 and 020).
const (
	maxNameLen       = 255
	maxCategoryLen   = 100
	maxEntityTypeLen = 50
	// NUMERIC(12,2) holds at most 9,999,999,999.99.
	maxAmount = 9_999_999_999.99
)

// createdBill is the create_bill response: the inserted row plus its next
// due date, computed from due_day against the database's CURRENT_DATE.
type createdBill struct {
	ID           uuid.UUID  `json:"id"`
	Name         string     `json:"name"`
	Amount       float64    `json:"amount"`
	DueDay       int        `json:"due_day"`
	Category     *string    `json:"category"`
	IsAutopay    bool       `json:"is_autopay"`
	VendorID     *uuid.UUID `json:"vendor_id"`
	EntityType   *string    `json:"entity_type"`
	EntityID     *uuid.UUID `json:"entity_id"`
	PaymentURL   *string    `json:"payment_url"`
	Notes        *string    `json:"notes"`
	CreatedAt    time.Time  `json:"created_at"`
	NextDueDate  string     `json:"next_due_date"`
	DaysUntilDue int        `json:"days_until_due"`
}

// createBillInput is the validated create_bill arguments. Optional text
// fields are nil when absent or blank so they are stored as NULL.
type createBillInput struct {
	name       string
	amount     float64
	dueDay     int
	category   *string
	isAutopay  bool
	vendorID   *uuid.UUID
	entityType *string
	entityID   *uuid.UUID
	paymentURL *string
	notes      *string
}

// NewCreateBillTool creates the create_bill MCP tool.
// It inserts a monthly bill (due on due_day each month; rrule is left unset)
// for the authenticated household and returns the created bill including its
// next due date. Invalid input returns a tool error and inserts nothing.
func NewCreateBillTool(pool *pgxpool.Pool) (string, mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	tool := mcp.NewTool("create_bill",
		mcp.WithDescription("Create a recurring monthly bill for the household. "+
			"Use this (not a calendar event) for bills so they appear in list_bills and get_upcoming_bills. "+
			"Returns the created bill with its next due date."),
		mcp.WithString("name",
			mcp.Description("Bill name (e.g. Electric, Mortgage, Netflix)"),
			mcp.Required(),
		),
		mcp.WithNumber("amount",
			mcp.Description("Amount due each month, in dollars (e.g. 125.50)"),
			mcp.Required(),
			mcp.Min(0),
		),
		mcp.WithNumber("due_day",
			mcp.Description("Day of the month the bill is due, 1-31. In shorter months a day past the end falls on the last day."),
			mcp.Required(),
			mcp.Min(1),
			mcp.Max(31),
		),
		mcp.WithString("category",
			mcp.Description("Optional category (e.g. Mortgage, Utilities, Subscription, Insurance)"),
		),
		mcp.WithBoolean("is_autopay",
			mcp.Description("Whether the bill is paid automatically (default false)"),
		),
		mcp.WithString("vendor_id",
			mcp.Description("Optional vendor ID (UUID) of a vendor in this household"),
		),
		mcp.WithString("entity_type",
			mcp.Description("Optional entity type the bill belongs to (e.g. property, vehicle, pet). Requires entity_id."),
		),
		mcp.WithString("entity_id",
			mcp.Description("Optional entity ID (UUID) the bill belongs to. Requires entity_type."),
		),
		mcp.WithString("payment_url",
			mcp.Description("Optional http(s) URL where the bill is paid"),
		),
		mcp.WithString("notes",
			mcp.Description("Optional free-text notes"),
		),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return mcp.NewToolResultError("unauthorized"), nil
		}

		in, err := parseCreateBillInput(req.GetArguments())
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return nil, fmt.Errorf("create bill: begin tx: %w", err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

		if in.vendorID != nil {
			var exists bool
			if err := tx.QueryRow(ctx,
				`SELECT EXISTS (SELECT 1 FROM vendors WHERE id = $1 AND household_id = $2)`,
				*in.vendorID, claims.HouseholdID,
			).Scan(&exists); err != nil {
				return nil, fmt.Errorf("create bill: check vendor: %w", err)
			}
			if !exists {
				return mcp.NewToolResultError("vendor_id not found in this household"), nil
			}
		}

		var b createdBill
		var today time.Time
		if err := tx.QueryRow(ctx,
			`INSERT INTO bills (household_id, name, amount, due_day, category, is_autopay,
			                    vendor_id, entity_type, entity_id, payment_url, notes)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			 RETURNING id, name, amount, due_day, category, is_autopay,
			           vendor_id, entity_type, entity_id, payment_url, notes,
			           created_at, CURRENT_DATE`,
			claims.HouseholdID, in.name, in.amount, in.dueDay, in.category, in.isAutopay,
			in.vendorID, in.entityType, in.entityID, in.paymentURL, in.notes,
		).Scan(&b.ID, &b.Name, &b.Amount, &b.DueDay, &b.Category, &b.IsAutopay,
			&b.VendorID, &b.EntityType, &b.EntityID, &b.PaymentURL, &b.Notes,
			&b.CreatedAt, &today); err != nil {
			return nil, fmt.Errorf("create bill: insert: %w", err)
		}

		// Same event the API writes on bill creation, so outbox consumers
		// see bills created by agents too.
		payload, err := json.Marshal(map[string]string{"id": b.ID.String(), "name": b.Name})
		if err != nil {
			return nil, fmt.Errorf("create bill: marshal event: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO outbox_events (type, payload, household_id) VALUES ($1, $2, $3)`,
			"bill.created", payload, claims.HouseholdID,
		); err != nil {
			return nil, fmt.Errorf("create bill: outbox event: %w", err)
		}

		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("create bill: commit: %w", err)
		}

		next := nextDueDate(today, b.DueDay)
		b.NextDueDate = next.Format("2006-01-02")
		b.DaysUntilDue = int(next.Sub(today).Hours() / 24)

		result, err := json.Marshal(b)
		if err != nil {
			return nil, fmt.Errorf("create bill: marshal result: %w", err)
		}
		return mcp.NewToolResultText(string(result)), nil
	}

	return "create_bill", tool, handler
}

// updatedBill is the bill_update response: the stored row after the update
// plus its next due date. amount and due_day are nullable because bills
// created through the API may lack them; next_due_date and days_until_due
// are null when the bill has no due_day.
type updatedBill struct {
	ID           uuid.UUID  `json:"id"`
	Name         string     `json:"name"`
	Amount       *float64   `json:"amount"`
	DueDay       *int       `json:"due_day"`
	Category     *string    `json:"category"`
	IsAutopay    bool       `json:"is_autopay"`
	VendorID     *uuid.UUID `json:"vendor_id"`
	EntityType   *string    `json:"entity_type"`
	EntityID     *uuid.UUID `json:"entity_id"`
	PaymentURL   *string    `json:"payment_url"`
	Notes        *string    `json:"notes"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	NextDueDate  *string    `json:"next_due_date"`
	DaysUntilDue *int       `json:"days_until_due"`
}

// billUpdate is the validated bill_update arguments: the bill to change and
// the columns to set, in a fixed order. Column names come only from
// parseUpdateBillInput, never from the caller.
type billUpdate struct {
	billID   uuid.UUID
	columns  []string
	values   []any
	vendorID *uuid.UUID // set when vendor_id is being set (not cleared)
}

func (u *billUpdate) set(column string, value any) {
	u.columns = append(u.columns, column)
	u.values = append(u.values, value)
}

// NewBillUpdateTool creates the bill_update MCP tool (catalog v2 name; see
// KB architecture/mcp-tool-catalog.md).
// It changes only the fields given for a bill in the authenticated household
// and returns the updated bill including its next due date. A bill in another
// household is reported as not found. Invalid input returns a tool error and
// changes nothing.
func NewBillUpdateTool(pool *pgxpool.Pool) (string, mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	tool := mcp.NewTool("bill_update",
		mcp.WithDescription("Change a bill; only the given fields change. "+
			"An empty string clears an optional text field. Returns the bill with its next due date."),
		mcp.WithTitleAnnotation("Update a bill"),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithString("bill_id",
			mcp.Description("Bill ID (UUID), e.g. from list_bills"),
			mcp.Required(),
		),
		mcp.WithString("name",
			mcp.Description("New bill name, e.g. Electric"),
		),
		mcp.WithNumber("amount",
			mcp.Description("New monthly amount in dollars, e.g. 125.50"),
			mcp.Min(0),
		),
		mcp.WithNumber("due_day",
			mcp.Description("Day of month due, 1-31; past a short month's end means its last day"),
			mcp.Min(1),
			mcp.Max(31),
		),
		mcp.WithString("category",
			mcp.Description("Category, e.g. Utilities; empty string clears it"),
		),
		mcp.WithBoolean("is_autopay",
			mcp.Description("Whether the bill is paid automatically"),
		),
		mcp.WithString("vendor_id",
			mcp.Description("Vendor ID (UUID) in this household; empty string clears it"),
		),
		mcp.WithString("entity_type",
			mcp.Description("e.g. property, vehicle, pet; pass with entity_id; both empty clears"),
		),
		mcp.WithString("entity_id",
			mcp.Description("Entity ID (UUID) the bill belongs to; pass with entity_type"),
		),
		mcp.WithString("payment_url",
			mcp.Description("http(s) URL where the bill is paid; empty string clears it"),
		),
		mcp.WithString("notes",
			mcp.Description("Free-text notes; empty string clears them"),
		),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return mcp.NewToolResultError("unauthorized"), nil
		}

		in, err := parseUpdateBillInput(req.GetArguments())
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return nil, fmt.Errorf("update bill: begin tx: %w", err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

		if in.vendorID != nil {
			var exists bool
			if err := tx.QueryRow(ctx,
				`SELECT EXISTS (SELECT 1 FROM vendors WHERE id = $1 AND household_id = $2)`,
				*in.vendorID, claims.HouseholdID,
			).Scan(&exists); err != nil {
				return nil, fmt.Errorf("update bill: check vendor: %w", err)
			}
			if !exists {
				return mcp.NewToolResultError("vendor_id not found in this household"), nil
			}
		}

		// $1 is the bill id, $2 the household; the SET values follow.
		args := []any{in.billID, claims.HouseholdID}
		sets := make([]string, 0, len(in.columns)+1)
		for i, col := range in.columns {
			sets = append(sets, fmt.Sprintf("%s = $%d", col, i+3))
			args = append(args, in.values[i])
		}
		sets = append(sets, "updated_at = NOW()")

		var b updatedBill
		var today time.Time
		err = tx.QueryRow(ctx,
			`UPDATE bills SET `+strings.Join(sets, ", ")+`
			 WHERE id = $1 AND household_id = $2
			 RETURNING id, name, amount, due_day, category, COALESCE(is_autopay, false),
			           vendor_id, entity_type, entity_id, payment_url, notes,
			           created_at, updated_at, CURRENT_DATE`,
			args...,
		).Scan(&b.ID, &b.Name, &b.Amount, &b.DueDay, &b.Category, &b.IsAutopay,
			&b.VendorID, &b.EntityType, &b.EntityID, &b.PaymentURL, &b.Notes,
			&b.CreatedAt, &b.UpdatedAt, &today)
		if errors.Is(err, pgx.ErrNoRows) {
			return mcp.NewToolResultError("bill not found"), nil
		}
		if err != nil {
			return nil, fmt.Errorf("update bill: update: %w", err)
		}

		// Same event the API writes on bill update.
		payload, err := json.Marshal(map[string]string{"id": b.ID.String(), "name": b.Name})
		if err != nil {
			return nil, fmt.Errorf("update bill: marshal event: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO outbox_events (type, payload, household_id) VALUES ($1, $2, $3)`,
			"bill.updated", payload, claims.HouseholdID,
		); err != nil {
			return nil, fmt.Errorf("update bill: outbox event: %w", err)
		}

		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("update bill: commit: %w", err)
		}

		// Bills created through the API may have no due_day.
		if b.DueDay != nil && *b.DueDay >= 1 && *b.DueDay <= 31 {
			next := nextDueDate(today, *b.DueDay)
			date := next.Format("2006-01-02")
			days := int(next.Sub(today).Hours() / 24)
			b.NextDueDate, b.DaysUntilDue = &date, &days
		}

		result, err := json.Marshal(b)
		if err != nil {
			return nil, fmt.Errorf("update bill: marshal result: %w", err)
		}
		return mcp.NewToolResultText(string(result)), nil
	}

	return "bill_update", tool, handler
}

// parseUpdateBillInput validates bill_update arguments. A field that is
// absent or null is left unchanged. name, amount, due_day and is_autopay
// cannot be cleared; a blank string clears the other text fields. The
// returned error message is shown to the caller as the tool error.
func parseUpdateBillInput(args map[string]any) (billUpdate, error) {
	var u billUpdate

	id, err := optionalUUID(args, "bill_id")
	if err != nil {
		return u, err
	}
	if id == nil {
		return u, errors.New("bill_id is required")
	}
	u.billID = *id

	if given(args, "name") {
		name, err := parseName(args)
		if err != nil {
			return u, err
		}
		if name == nil {
			return u, errors.New("name cannot be blank")
		}
		u.set("name", *name)
	}

	if amount, ok, err := parseAmount(args); err != nil {
		return u, err
	} else if ok {
		u.set("amount", amount)
	}

	if dueDay, ok, err := parseDueDay(args); err != nil {
		return u, err
	} else if ok {
		u.set("due_day", dueDay)
	}

	if given(args, "category") {
		category, err := parseCategory(args)
		if err != nil {
			return u, err
		}
		u.set("category", category)
	}

	if autopay, ok, err := parseIsAutopay(args); err != nil {
		return u, err
	} else if ok {
		u.set("is_autopay", autopay)
	}

	if given(args, "vendor_id") {
		vendorID, err := optionalUUID(args, "vendor_id")
		if err != nil {
			return u, err
		}
		u.vendorID = vendorID
		u.set("vendor_id", vendorID)
	}

	if given(args, "entity_type") || given(args, "entity_id") {
		if !given(args, "entity_type") || !given(args, "entity_id") {
			return u, errors.New("entity_type and entity_id must be provided together")
		}
		entityType, entityID, err := parseEntity(args)
		if err != nil {
			return u, err
		}
		if (entityType == nil) != (entityID == nil) {
			return u, errors.New("entity_type and entity_id must be set or cleared together")
		}
		u.set("entity_type", entityType)
		u.set("entity_id", entityID)
	}

	if given(args, "payment_url") {
		paymentURL, err := parsePaymentURL(args)
		if err != nil {
			return u, err
		}
		u.set("payment_url", paymentURL)
	}

	if given(args, "notes") {
		notes, err := optionalString(args, "notes")
		if err != nil {
			return u, err
		}
		u.set("notes", notes)
	}

	if len(u.columns) == 0 {
		return u, errors.New("no fields to update: pass at least one of name, amount, due_day, " +
			"category, is_autopay, vendor_id, entity_type/entity_id, payment_url, notes")
	}
	return u, nil
}

// given reports whether args has a non-null value for key.
func given(args map[string]any, key string) bool {
	v, present := args[key]
	return present && v != nil
}

// parseCreateBillInput validates create_bill arguments. The returned error
// message is shown to the caller as the tool error.
func parseCreateBillInput(args map[string]any) (createBillInput, error) {
	var in createBillInput

	name, err := parseName(args)
	if err != nil {
		return in, err
	}
	if name == nil {
		return in, errors.New("name is required")
	}
	in.name = *name

	amount, ok, err := parseAmount(args)
	if err != nil {
		return in, err
	}
	if !ok {
		return in, errors.New("amount is required")
	}
	in.amount = amount

	dueDay, ok, err := parseDueDay(args)
	if err != nil {
		return in, err
	}
	if !ok {
		return in, errors.New("due_day is required")
	}
	in.dueDay = dueDay

	if in.category, err = parseCategory(args); err != nil {
		return in, err
	}

	if in.isAutopay, _, err = parseIsAutopay(args); err != nil {
		return in, err
	}

	if in.vendorID, err = optionalUUID(args, "vendor_id"); err != nil {
		return in, err
	}

	if in.entityType, in.entityID, err = parseEntity(args); err != nil {
		return in, err
	}
	if (in.entityType == nil) != (in.entityID == nil) {
		return in, errors.New("entity_type and entity_id must be provided together")
	}

	if in.paymentURL, err = parsePaymentURL(args); err != nil {
		return in, err
	}

	if in.notes, err = optionalString(args, "notes"); err != nil {
		return in, err
	}

	return in, nil
}

// Field parsers shared by create_bill and bill_update so both tools apply
// the same rules and return the same messages. Each returns nil (or ok=false)
// when the argument is absent, null or blank.

func parseName(args map[string]any) (*string, error) {
	name, err := optionalString(args, "name")
	if err != nil {
		return nil, err
	}
	if name != nil && utf8.RuneCountInString(*name) > maxNameLen {
		return nil, fmt.Errorf("name must be at most %d characters", maxNameLen)
	}
	return name, nil
}

func parseAmount(args map[string]any) (float64, bool, error) {
	amount, ok, err := number(args, "amount")
	if err != nil || !ok {
		return 0, ok, err
	}
	if amount < 0 || amount > maxAmount {
		return 0, false, fmt.Errorf("amount must be between 0 and %.2f", maxAmount)
	}
	return math.Round(amount*100) / 100, true, nil
}

func parseDueDay(args map[string]any) (int, bool, error) {
	dueDay, ok, err := number(args, "due_day")
	if err != nil || !ok {
		return 0, ok, err
	}
	if dueDay != math.Trunc(dueDay) || dueDay < 1 || dueDay > 31 {
		return 0, false, errors.New("due_day must be a whole number from 1 to 31")
	}
	return int(dueDay), true, nil
}

func parseCategory(args map[string]any) (*string, error) {
	category, err := optionalString(args, "category")
	if err != nil {
		return nil, err
	}
	if category != nil && utf8.RuneCountInString(*category) > maxCategoryLen {
		return nil, fmt.Errorf("category must be at most %d characters", maxCategoryLen)
	}
	return category, nil
}

func parseIsAutopay(args map[string]any) (val, ok bool, err error) {
	v, present := args["is_autopay"]
	if !present || v == nil {
		return false, false, nil
	}
	b, isBool := v.(bool)
	if !isBool {
		return false, false, errors.New("is_autopay must be a boolean")
	}
	return b, true, nil
}

// parseEntity parses entity_type and entity_id. The caller checks that they
// are given together.
func parseEntity(args map[string]any) (*string, *uuid.UUID, error) {
	entityType, err := optionalString(args, "entity_type")
	if err != nil {
		return nil, nil, err
	}
	entityID, err := optionalUUID(args, "entity_id")
	if err != nil {
		return nil, nil, err
	}
	if entityType != nil && utf8.RuneCountInString(*entityType) > maxEntityTypeLen {
		return nil, nil, fmt.Errorf("entity_type must be at most %d characters", maxEntityTypeLen)
	}
	return entityType, entityID, nil
}

func parsePaymentURL(args map[string]any) (*string, error) {
	paymentURL, err := optionalString(args, "payment_url")
	if err != nil || paymentURL == nil {
		return nil, err
	}
	u, err := url.Parse(*paymentURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("payment_url must be an http or https URL")
	}
	return paymentURL, nil
}

// optionalString returns the trimmed string argument, or nil when it is
// absent, null or blank. A non-string value is an error.
func optionalString(args map[string]any, key string) (*string, error) {
	v, present := args[key]
	if !present || v == nil {
		return nil, nil
	}
	s, ok := v.(string)
	if !ok {
		return nil, fmt.Errorf("%s must be a string", key)
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	return &s, nil
}

// optionalUUID parses an optional UUID string argument.
func optionalUUID(args map[string]any, key string) (*uuid.UUID, error) {
	s, err := optionalString(args, key)
	if err != nil || s == nil {
		return nil, err
	}
	id, err := uuid.Parse(*s)
	if err != nil {
		return nil, fmt.Errorf("%s must be a UUID", key)
	}
	return &id, nil
}

// number reads a numeric argument. JSON-decoded arguments arrive as float64;
// integer types are accepted for direct callers. ok is false when the
// argument is absent or null.
func number(args map[string]any, key string) (val float64, ok bool, err error) {
	v, present := args[key]
	if !present || v == nil {
		return 0, false, nil
	}
	switch n := v.(type) {
	case float64:
		val = n
	case int:
		val = float64(n)
	case int64:
		val = float64(n)
	case json.Number:
		f, perr := n.Float64()
		if perr != nil {
			return 0, false, fmt.Errorf("%s must be a number", key)
		}
		val = f
	default:
		return 0, false, fmt.Errorf("%s must be a number", key)
	}
	if math.IsNaN(val) || math.IsInf(val, 0) {
		return 0, false, fmt.Errorf("%s must be a finite number", key)
	}
	return val, true, nil
}

// nextDueDate returns the first date on or after today that falls on dueDay
// of its month. When a month is shorter than dueDay, the bill is due on that
// month's last day (due_day 31 is due on 30 April).
func nextDueDate(today time.Time, dueDay int) time.Time {
	y, m, _ := today.Date()
	today = time.Date(y, m, today.Day(), 0, 0, 0, 0, time.UTC)
	candidate := dueDateInMonth(y, m, dueDay)
	if candidate.Before(today) {
		candidate = dueDateInMonth(y, m+1, dueDay)
	}
	return candidate
}

// dueDateInMonth returns dueDay of the given month, clamped to the month's
// last day. time.Date normalises a month of 13 into January of the next year.
func dueDateInMonth(year int, month time.Month, dueDay int) time.Time {
	lastDay := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if dueDay > lastDay {
		dueDay = lastDay
	}
	return time.Date(year, month, dueDay, 0, 0, 0, 0, time.UTC)
}
