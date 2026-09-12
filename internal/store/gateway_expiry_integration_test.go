package store

import (
	"testing"
	"time"
)

func TestNativeGatewayUnclaimedTicketEventuallyRetiresConnection(t *testing.T) {
	db, request := gatewayFixture(t, true)
	ticket, err := db.CreateNativeGatewayTicket(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if due, err := db.DesktopConnectionsDueForRevocation(t.Context(), 10); err != nil || len(due) != 0 {
		t.Fatal("fresh ticket retired before its deadline", err)
	}
	// Do not disable immutable-authority triggers or rewrite its expiry. Wait
	// against PostgreSQL's own clock, independent of Docker/host clock skew.
	deadline := time.Now().Add(40 * time.Second)
	for {
		var expired bool
		if err := db.pool.QueryRow(t.Context(), `SELECT $1::timestamptz <= clock_timestamp()`, ticket.ExpiresAt).Scan(&expired); err != nil {
			t.Fatal(err)
		}
		if expired {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("ticket did not expire")
		}
		select {
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
	due, err := db.DesktopConnectionsDueForRevocation(t.Context(), 10)
	if err != nil || len(due) != 1 || due[0].ID != request.ConnectionID || due[0].State != "revoking" || !due[0].ExpiresAt.After(time.Now().Add(time.Minute)) {
		t.Fatal("abandoned short ticket left a long-lived OS credential", err)
	}
	if terminate, err := db.DesktopConnectionTerminationRequired(t.Context(), request.ConnectionID); err != nil || terminate {
		t.Fatal("unclaimed ticket terminated retained OS desktop", err)
	}
}
