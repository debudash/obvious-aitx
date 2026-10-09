// Package integration implements the flag-gated carrier RX bridge: a
// receive-only mirror of already-arbitrated floor state and floor audio
// towards a carrier endpoint.
//
// Posture (spec, "Carrier RX integration, enabled by flags"): the system is
// over-the-top and stays that way. The bridge is a deployment-time
// capability, off by default; when off, nothing here is constructed and the
// server opens zero external sockets. When on, the bridge can only mirror
// what the control plane already arbitrated:
//
//   - audio: packets the media plane's gate already decided to forward (a
//     granted talker's bursts), mirrored by the SFU's tap hook;
//   - events: callcontrol session events (call started/ended, floor
//     grants, pre-emptions, revocations, emergency flags).
//
// It never grants floor, never accepts inbound audio, and never becomes a
// second control plane. A future SIP/IWF adapter slots in behind the same
// RXAdapter interface without touching callcontrol or media.
package integration

import (
	"time"

	"github.com/debudash/obvious-aitx/mcptt/server/internal/floor"
)

// CallEventType tags RX call lifecycle events.
type CallEventType string

const (
	CallStarted CallEventType = "call.started"
	CallEnded   CallEventType = "call.ended"
)

// FloorUpdateType tags RX floor updates.
type FloorUpdateType string

const (
	FloorGranted  FloorUpdateType = "granted"
	FloorReleased FloorUpdateType = "released"
)

// CallEvent is one call lifecycle change (started / ended).
type CallEvent struct {
	Type      CallEventType
	CallID    string // the call is the RX "room"
	GroupID   string // empty for private calls and unresolved calls
	Kind      string // "group" | "private" | "broadcast"
	By        string // initiator, or who ended the call
	Timestamp time.Time
}

// FloorUpdate is one floor grant or floor loss on a call.
type FloorUpdate struct {
	Type      FloorUpdateType
	CallID    string
	GroupID   string
	Talker    string
	Priority  floor.FloorLevel
	Emergency bool
	// Reason names why a floor was lost: "released", "revoked",
	// "preempted", or "expired". Empty on grants.
	Reason string
	// By names the authority for revocations; empty otherwise.
	By        string
	Timestamp time.Time
}

// FrameCodec names the encoding of an AudioFrame payload.
type FrameCodec string

const (
	// FrameOpus carries one Opus packet payload as produced by the media
	// plane (which never decodes). The canonical source format today.
	FrameOpus FrameCodec = "opus"
	// FramePCM16 carries 20 ms of linear 16-bit little-endian mono PCM at
	// 8 kHz — the G.711 encoding input. Supplied by taps that have PCM
	// available; the Pion media plane does not produce it (see README,
	// "Codec support").
	FramePCM16 FrameCodec = "pcm16"
)

// AudioFrame is one mirrored slice of talk-burst audio, delivered by the
// media tap to adapters via their audio sink.
type AudioFrame struct {
	CallID  string
	Talker  string
	Codec   FrameCodec
	Payload []byte
	At      time.Time
}

// AudioSink receives mirrored audio frames. Implementations must not block:
// the tap calls WriteAudio on a relay goroutine, and a behind sink sheds
// frames rather than stalling the media path.
type AudioSink interface {
	WriteAudio(frame AudioFrame)
}

// RXAdapter is the stable carrier-side contract (spec: the deliverable is a
// stable RXAdapter interface; a SIP/IWF adapter slots in later without
// touching callcontrol or media). Implementations observe already-made
// decisions; they must never block event handling.
type RXAdapter interface {
	// OnCallEvent delivers call lifecycle (started / ended).
	OnCallEvent(ev CallEvent)
	// OnFloorGrant delivers a floor grant: room, talker, priority.
	OnFloorGrant(up FloorUpdate)
	// OnFloorRelease delivers a floor loss (release, revoke, pre-emption,
	// expiry): room, talker, priority, reason.
	OnFloorRelease(up FloorUpdate)
	// AudioSink returns the handle the media tap feeds mirrored audio
	// into. Adapters without an audio path (webhook-only) return a sink
	// that discards frames.
	AudioSink() AudioSink
}
