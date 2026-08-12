package wailsapp

import (
	"reflect"
	"testing"

	"github.com/rescale/rescale-int/internal/events"
)

// The event bridge hand-pairs Go event structs with JSON DTOs, and nothing
// type-checks across that boundary: a field added to the event but not the DTO
// silently vanishes from the frontend payload while every suite stays green.
// This test makes the pairing a compile-adjacent contract for the batch
// progress event, whose Cancelled/CancelRequested fields were dropped exactly
// that way once.
func TestBatchProgressEventDTOCoversEventFields(t *testing.T) {
	eventType := reflect.TypeOf(events.BatchProgressEvent{})
	dtoType := reflect.TypeOf(BatchProgressEventDTO{})

	dtoFields := make(map[string]bool, dtoType.NumField())
	for i := 0; i < dtoType.NumField(); i++ {
		dtoFields[dtoType.Field(i).Name] = true
	}

	for i := 0; i < eventType.NumField(); i++ {
		f := eventType.Field(i)
		if f.Anonymous || !f.IsExported() {
			continue // embedded base event; its data travels as Timestamp
		}
		if !dtoFields[f.Name] {
			t.Errorf("events.BatchProgressEvent.%s has no counterpart in BatchProgressEventDTO — the frontend will never see it", f.Name)
		}
	}
}
