package ws

import (
	"encoding/json"
	"log"

	"github.com/debudash/obvious-aitx/mcptt/server/internal/callcontrol"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/floor"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/protocol"
)

// RenderCallEvent maps one call-control event to the protocol frames the
// wire contract defines. Pure — no I/O, no ordering surprises — so the
// fan-out contract is testable without a server, and the dispatch path
// (this package) and the api event path share one renderer: a call start
// or floor grant encodes identically no matter which plane emitted it. A
// grant that displaced a talker emits the pre-emption notice first, then
// the new holder's grant: clients rendering both see the cause before the
// effect.
func RenderCallEvent(e callcontrol.Event) [][]byte {
	switch e.Type {
	case callcontrol.EventCallStarted:
		return mustFrames(protocol.CallStart{
			Type: protocol.TypeCallStart, CallID: e.CallID,
			GroupID: e.GroupID, Kind: protocol.CallKind(e.Kind), InitiatorID: e.Actor,
		})
	case callcontrol.EventParticipantJoined:
		return mustFrames(protocol.CallJoined{
			Type: protocol.TypeCallJoined, CallID: e.CallID, UserID: e.Actor,
		})
	case callcontrol.EventFloorReleased:
		return mustFrames(protocol.FloorReleased{
			Type: protocol.TypeFloorReleased, CallID: e.CallID, UserID: e.Actor,
		})
	case callcontrol.EventParticipantRemoved:
		return mustFrames(protocol.ParticipantRemoved{
			Type: protocol.TypeParticipantRemoved, CallID: e.CallID,
			UserID: e.Target, By: e.Actor,
		})
	case callcontrol.EventCallEnded:
		return mustFrames(protocol.CallEnded{
			Type: protocol.TypeCallEnded, CallID: e.CallID, By: e.Actor,
		})
	case callcontrol.EventFloorDecisions:
		var out [][]byte
		for _, d := range e.Decisions {
			if d.Outcome == floor.OutcomeGranted && d.PreemptedUserID != "" {
				out = append(out, mustFrames(protocol.FloorPreempted{
					Type: protocol.TypeFloorPreempt, CallID: e.CallID,
					By: d.UserID, Emergency: d.Emergency,
				})...)
			}
			switch d.Outcome {
			case floor.OutcomeGranted:
				out = append(out, mustFrames(protocol.FloorGranted{
					Type: protocol.TypeFloorGranted, CallID: e.CallID,
					UserID: d.UserID, Queue: e.Queue,
				})...)
			case floor.OutcomeQueued, floor.OutcomeDenied:
				out = append(out, mustFrames(protocol.FloorDenied{
					Type: protocol.TypeFloorDenied, CallID: e.CallID,
					UserID: d.UserID, Reason: d.Reason, QueuePosition: d.QueuePosition,
				})...)
			}
		}
		return out
	default:
		return nil
	}
}

// mustFrames marshals one wire message; a marshal failure of a static
// struct is a programmer error — log loudly and skip rather than
// half-serve the event.
func mustFrames(v any) [][]byte {
	b, err := json.Marshal(v)
	if err != nil {
		log.Printf("ws: marshal call event: %v", err)
		return nil
	}
	return [][]byte{b}
}
