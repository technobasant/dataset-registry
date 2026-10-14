// Package changereq is the state machine for breaking schema changes.
//
//	open ──all consumers acknowledged──▶ approved ──owner publishes──▶ published
//	open ──a consumer rejects──────────▶ rejected
//	open | approved ──owner withdraws──▶ withdrawn
//
// A change request with no consumers to ask starts approved. Terminal states
// never change again.
package changereq

import "fmt"

// State of a change request.
type State string

// States.
const (
	Open      State = "open"
	Approved  State = "approved"
	Published State = "published"
	Rejected  State = "rejected"
	Withdrawn State = "withdrawn"
)

// Event moves a change request between states.
type Event string

// Events.
const (
	AllAcknowledged  Event = "all_acknowledged"
	ConsumerRejected Event = "consumer_rejected"
	Publish          Event = "publish"
	Withdraw         Event = "withdraw"
)

var transitions = map[State]map[Event]State{
	Open:     {AllAcknowledged: Approved, ConsumerRejected: Rejected, Withdraw: Withdrawn},
	Approved: {Publish: Published, Withdraw: Withdrawn},
}

// Initial is the state a new change request starts in.
func Initial(pendingConsumers int) State {
	if pendingConsumers == 0 {
		return Approved
	}
	return Open
}

// Next returns the state after e, or an error naming why e is not allowed.
func Next(s State, e Event) (State, error) {
	if to, ok := transitions[s][e]; ok {
		return to, nil
	}
	if Terminal(s) {
		return s, fmt.Errorf("change request is %s; it cannot change any more", s)
	}
	return s, fmt.Errorf("cannot %s a change request that is %s", e, s)
}

// Terminal reports whether s is final.
func Terminal(s State) bool { return s == Published || s == Rejected || s == Withdrawn }
