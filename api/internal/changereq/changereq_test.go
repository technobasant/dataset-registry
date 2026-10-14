package changereq

import "testing"

func TestTransitions(t *testing.T) {
	allowed := []struct {
		from State
		e    Event
		to   State
	}{
		{Open, AllAcknowledged, Approved},
		{Open, ConsumerRejected, Rejected},
		{Open, Withdraw, Withdrawn},
		{Approved, Publish, Published},
		{Approved, Withdraw, Withdrawn},
	}
	ok := map[[2]string]bool{}
	for _, c := range allowed {
		got, err := Next(c.from, c.e)
		if err != nil || got != c.to {
			t.Errorf("%s --%s--> got %s, %v; want %s", c.from, c.e, got, err, c.to)
		}
		ok[[2]string{string(c.from), string(c.e)}] = true
	}
	// Everything else is refused, and the state does not move.
	for _, s := range []State{Open, Approved, Published, Rejected, Withdrawn} {
		for _, e := range []Event{AllAcknowledged, ConsumerRejected, Publish, Withdraw} {
			if ok[[2]string{string(s), string(e)}] {
				continue
			}
			got, err := Next(s, e)
			if err == nil || got != s {
				t.Errorf("%s --%s--> should be refused, got %s, %v", s, e, got, err)
			}
		}
	}
}

func TestInitial(t *testing.T) {
	if Initial(0) != Approved || Initial(2) != Open {
		t.Error("a change with nobody to ask starts approved; otherwise open")
	}
	if !Terminal(Published) || Terminal(Approved) {
		t.Error("terminal states")
	}
}
