package main

import (
	"context"
	"testing"
	"time"

	doolla "doolla.ai/sdk"
)

func TestScenarioCanRunTwiceInOneChat(t *testing.T) {
	prior := make(map[string]bool)
	for range 2 {
		created := make(map[string]string)
		types := make(map[string]bool)
		updates := 0
		err := runScenario(context.Background(), time.Nanosecond, func(event doolla.Component) error {
			if event.ID == "" || prior[event.ID] {
				t.Fatalf("missing or reused component ID for %s", event.Type)
			}
			if event.Lifecycle == "update" {
				updates++
				if created[event.ID] != event.Type {
					t.Fatalf("%s update has no matching creation", event.Type)
				}
			}
			created[event.ID] = event.Type
			types[event.Type] = true
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		// The page exercises 16 visible component types and three UI state events.
		if len(types) != 19 || updates != 14 {
			t.Fatalf("received %d component types and %d updates", len(types), updates)
		}
		for id := range created {
			prior[id] = true
		}
	}
}
