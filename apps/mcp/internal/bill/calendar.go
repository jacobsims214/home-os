package bill

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"home-os/billcal"
)

// ─────────────────────────────────────────────
// Calendar mirror (#4485). Every bill with a usable schedule is one all-day
// event on the household's Bills calendar, kept by home-os/billcal: the one
// implementation the MCP bill tools, the API's bill endpoints and the
// worker's backfill share (#4716). bill_create, bill_update and bill_delete
// call syncBillEvent / deleteBillEvent in their own transaction. Paying a
// bill does not touch the event: it shows due dates, and the agenda /
// family_overview bills sections show what is still unpaid.
// ─────────────────────────────────────────────

const (
	// CalendarEventType is calendar_objects.event_type of a bill's mirror
	// event. The calendar tools leave these out of agenda/overview events
	// and conflicts: the bills sections already list bills.
	CalendarEventType = billcal.EventType
	// BillReminderDays is how many days before a due date phones alert.
	BillReminderDays = billcal.ReminderDays
)

// calendarBill is r as billcal builds the event from it.
func calendarBill(r billRow) billcal.Bill {
	return billcal.Bill{
		ID:         r.ID.String(),
		Name:       r.Name,
		Amount:     r.Amount,
		Category:   r.Category,
		AutoPay:    r.AutoPay,
		PaymentURL: r.PaymentURL,
		CreatedAt:  r.CreatedAt,
		Frequency:  r.Frequency,
		DueDay:     r.DueDay,
		DueDate:    r.DueDate,
	}
}

// syncBillEvent creates, updates or deletes the bill's mirror event inside
// tx and returns its id ("" when there is none).
func syncBillEvent(ctx context.Context, tx pgx.Tx, householdID string, r billRow) (string, error) {
	ch, err := billcal.Sync(ctx, tx, householdID, calendarBill(r))
	return ch.EventID, err
}

// deleteBillEvent deletes the bill's mirror event inside tx (call it before
// deleting the bill row).
func deleteBillEvent(ctx context.Context, tx pgx.Tx, householdID string, billID uuid.UUID) error {
	_, err := billcal.Delete(ctx, tx, householdID, billID.String())
	return err
}
