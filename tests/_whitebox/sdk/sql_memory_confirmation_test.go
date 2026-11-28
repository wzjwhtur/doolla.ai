package doolla

import (
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestSQLConfirmationExpiresAtDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var confirmations sqlConfirmations
		turn := Turn{User: User{ID: "analyst"}, ConversationID: "report"}
		card := confirmations.offer(t.Context(), turn, "How many?", sqlReceipt{statement: "SELECT 42", callID: "call-42"})
		action := card.Data["actions"].([]map[string]string)[0]["action"]
		id, ok := strings.CutPrefix(action, "/confirm-sql ")
		if !ok || id == "" {
			t.Fatalf("invalid confirmation action %q", action)
		}
		time.Sleep(15*time.Minute - time.Nanosecond)
		if got, index := confirmations.find(id, turn); index < 0 || got.statement != "SELECT 42" || got.call != "call-42" {
			t.Fatal("live confirmation lost its executed SQL receipt")
		}
		time.Sleep(time.Nanosecond)
		if _, index := confirmations.find(id, turn); index >= 0 {
			t.Fatal("expired action still resolves")
		}
	})
}
