package calendar

import (
	"encoding/json"
	"fmt"
	"time"
)

// eventJSON is the JSON structure stored in ical_data column.
type eventJSON struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Start       string `json:"start"`
	End         string `json:"end"`
	AllDay      bool   `json:"all_day"`
	Location    string `json:"location"`
	Color       string `json:"color"`
	EventType   string `json:"event_type"`
}

func marshalEventJSON(ev *Event) (string, error) {
	ej := eventJSON{
		Title:       ev.Title,
		Description: ev.Description,
		Start:       ev.Start.UTC().Format(time.RFC3339),
		End:         ev.End.UTC().Format(time.RFC3339),
		AllDay:      ev.AllDay,
		Location:    ev.Location,
		Color:       ev.Color,
		EventType:   ev.EventType,
	}
	b, err := json.Marshal(ej)
	if err != nil {
		return "", fmt.Errorf("marshal event: %w", err)
	}
	return string(b), nil
}

func parseEventJSON(id, calID, icalData, eventType string, entityType, entityID *string, createdAt, updatedAt time.Time) (*Event, error) {
	var ej eventJSON
	if err := json.Unmarshal([]byte(icalData), &ej); err != nil {
		return nil, fmt.Errorf("parse event json: %w", err)
	}
	start, err := time.Parse(time.RFC3339, ej.Start)
	if err != nil {
		return nil, fmt.Errorf("parse start: %w", err)
	}
	end, err := time.Parse(time.RFC3339, ej.End)
	if err != nil {
		return nil, fmt.Errorf("parse end: %w", err)
	}
	ev := &Event{
		ID:          id,
		CalendarID:  calID,
		EventType:   eventType,
		Title:       ej.Title,
		Description: ej.Description,
		Start:       start,
		End:         end,
		AllDay:      ej.AllDay,
		Location:    ej.Location,
		Color:       ej.Color,
		CreatedAt:   createdAt,
		UpdatedAt:   updatedAt,
	}
	if entityType != nil {
		ev.EntityType = *entityType
	}
	if entityID != nil {
		ev.EntityID = *entityID
	}
	return ev, nil
}
