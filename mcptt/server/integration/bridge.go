package integration

import (
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/debudash/obvious-aitx/mcptt/server/internal/callcontrol"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/config"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/floor"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/media"
)

// BridgeOptions tunes the bridge.
type BridgeOptions struct {
	// GroupOf resolves a call to its talkgroup ID. It must be a plain,
	// non-blocking lookup (an in-memory map keyed by call ID): the
	// call-started session event fires inside the session manager's
	// critical section, so a resolver that takes that lock deadlocks.
	// Call wiring owns the map's population. Nil resolves nothing — every
	// call is treated as unflagged, which keeps the bridge inert.
	GroupOf func(callID string) (string, bool)
}

// Bridge joins the two planes the carrier RX feed mirrors: it consumes
// callcontrol session events (the exported OnEvent surface — consumed, not
// extended) and registers itself as the media plane's per-call tap. Both
// sources are read-only observations of already-arbitrated state; the
// bridge grants nothing and accepts no inbound media.
//
// Group filter: a stream (and therefore any socket or tap) exists only for
// calls whose group is on the allowlist. Unflagged groups produce no events,
// no taps, and no packets — even with the master flag on.
type Bridge struct {
	cfg     config.CarrierConfig
	groupOf func(callID string) (string, bool)
	sfu     *media.SFU

	// mu serializes event handling and adapter calls so delivery order
	// follows event order. Adapters are non-blocking by contract and never
	// call back into the bridge, so this cannot deadlock.
	mu       sync.Mutex
	streams  map[string]*bridgeStream
	grants   map[string]map[string]grantInfo // callID → talker → held grant (mirror for loss detection)
	adapters []RXAdapter
}

type bridgeStream struct {
	groupID    string
	unregister func() // SFU tap unregistration; nil when no SFU attached
}

// grantInfo is one held grant in the bridge's mirror.
type grantInfo struct {
	level     floor.FloorLevel
	emergency bool
}

// NewFromConfig builds the bridge for an enabled [carrier] section, or
// returns (nil, nil) when disabled — the flag-off posture in which no
// adapter, socket, or tap is ever constructed. It returns an error for an
// enabled section that names no usable adapter.
func NewFromConfig(cfg config.CarrierConfig, opts BridgeOptions) (*Bridge, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	if opts.GroupOf == nil {
		opts.GroupOf = func(string) (string, bool) { return "", false }
	}
	b := &Bridge{
		cfg:     cfg,
		groupOf: opts.GroupOf,
		streams: make(map[string]*bridgeStream),
		grants:  make(map[string]map[string]grantInfo),
	}
	switch cfg.Adapter {
	case "rtp":
		b.adapters = append(b.adapters, NewRTPAdapter(cfg.Endpoint, cfg.Codec))
	case "webhook":
	default:
		return nil, errors.New("carrier: adapter must be rtp or webhook")
	}
	if cfg.EventsURL != "" {
		b.adapters = append(b.adapters, NewWebhookAdapter(cfg.EventsURL))
	}
	if len(b.adapters) == 0 {
		return nil, errors.New("carrier: enabled with no usable adapter (webhook adapter needs events_url)")
	}
	return b, nil
}

// AttachSFU connects the bridge to the media plane so flagged calls get a
// tap. Safe to call before any event arrives.
func (b *Bridge) AttachSFU(sfu *media.SFU) {
	if b == nil || sfu == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sfu = sfu
}

// HandleEvent consumes one callcontrol session event. It is nil-safe so
// flag-off wiring can pass a nil bridge as the event sink.
func (b *Bridge) HandleEvent(ev callcontrol.Event) {
	if b == nil {
		return
	}
	switch ev.Type {
	case callcontrol.EventCallStarted:
		b.callStarted(ev)
	case callcontrol.EventCallEnded:
		b.callEnded(ev)
	case callcontrol.EventFloorDecisions:
		b.floorDecisions(ev)
	default:
		// Participant joins/leaves/removals have no RX surface yet; the
		// carrier sees their effects through grants and silence.
	}
}

// WriteTap implements media.TapWriter: the SFU hands over each forwarded
// (granted) packet for the calls the bridge streams. Nil-safe like
// HandleEvent.
func (b *Bridge) WriteTap(p media.TapPacket) {
	if b == nil {
		return
	}
	b.mu.Lock()
	_, ok := b.streams[p.CallID]
	adapters := slices.Clone(b.adapters)
	b.mu.Unlock()
	if !ok {
		return
	}
	frame := AudioFrame{
		CallID:  p.CallID,
		Talker:  p.UserID,
		Codec:   FrameOpus, // the media plane negotiates Opus only and never decodes
		Payload: p.Payload,
		At:      time.Now(),
	}
	for _, a := range adapters {
		a.AudioSink().WriteAudio(frame)
	}
}

// Close tears every stream down and stops the adapters. Nil-safe.
func (b *Bridge) Close() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	streams := b.streams
	b.streams = make(map[string]*bridgeStream)
	b.grants = make(map[string]map[string]grantInfo)
	var errs []error
	for _, a := range b.adapters {
		if c, ok := a.(interface{ Close() error }); ok {
			if err := c.Close(); err != nil {
				errs = append(errs, err)
			}
		}
	}
	b.mu.Unlock()
	for _, s := range streams {
		if s.unregister != nil {
			s.unregister()
		}
	}
	return errors.Join(errs...)
}

// callStarted opens the RX stream for a flagged call.
func (b *Bridge) callStarted(ev callcontrol.Event) {
	group := ev.GroupID
	if group == "" {
		group, _ = b.groupOf(ev.CallID) // fallback for event sources that omit it
	}
	if !b.flagged(group) {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, exists := b.streams[ev.CallID]; exists {
		return // idempotent: call start is emitted once, but re-delivery is harmless
	}
	stream := &bridgeStream{groupID: group}
	if b.sfu != nil {
		stream.unregister = b.sfu.RegisterTap(ev.CallID, b)
	}
	b.streams[ev.CallID] = stream
	b.grants[ev.CallID] = make(map[string]grantInfo)
	ce := CallEvent{
		Type:      CallStarted,
		CallID:    ev.CallID,
		GroupID:   group,
		Kind:      string(ev.Kind),
		By:        ev.Actor,
		Timestamp: time.Now(),
	}
	for _, a := range b.adapters {
		a.OnCallEvent(ce)
	}
}

// callEnded closes a flagged call's RX stream.
func (b *Bridge) callEnded(ev callcontrol.Event) {
	b.mu.Lock()
	stream, ok := b.streams[ev.CallID]
	if !ok {
		b.mu.Unlock()
		return
	}
	delete(b.streams, ev.CallID)
	delete(b.grants, ev.CallID)
	adapters := slices.Clone(b.adapters)
	b.mu.Unlock()
	if stream.unregister != nil {
		stream.unregister()
	}
	ce := CallEvent{
		Type:      CallEnded,
		CallID:    ev.CallID,
		GroupID:   stream.groupID,
		By:        ev.Actor,
		Timestamp: time.Now(),
	}
	for _, a := range adapters {
		a.OnCallEvent(ce)
	}
}

// floorDecisions translates one FloorDecisions event into adapter calls.
// Grants and grant losses (revocation, pre-emption) are floor transitions;
// plain denies and queue entries are not, and have no RX surface — the
// carrier hears them as silence, which is exactly the contract.
//
// Known blind spot: a plain release with an empty queue emits no session
// event today (ReleaseFloor publishes only when decisions exist), so the
// mirror keeps that grant until the next transition self-corrects it. The
// audio path is unaffected — the tap mirrors what the gate forwards, and
// the gate stopped forwarding at release.
func (b *Bridge) floorDecisions(ev callcontrol.Event) {
	b.mu.Lock()
	stream, ok := b.streams[ev.CallID]
	if !ok {
		b.mu.Unlock()
		return
	}
	grants := b.grants[ev.CallID]
	group := stream.groupID
	now := time.Now()
	for _, d := range ev.Decisions {
		switch d.Outcome {
		case floor.OutcomeGranted:
			grants[d.UserID] = grantInfo{level: d.Level, emergency: d.Emergency}
			for _, a := range b.adapters {
				a.OnFloorGrant(FloorUpdate{
					Type: FloorGranted, CallID: ev.CallID, GroupID: group,
					Talker: d.UserID, Priority: d.Level, Emergency: d.Emergency,
					Timestamp: now,
				})
			}
			if d.PreemptedUserID != "" {
				pre := grantInfo{level: floor.PriorityAmbient}
				if held, ok := grants[d.PreemptedUserID]; ok {
					pre = held
				}
				delete(grants, d.PreemptedUserID)
				for _, a := range b.adapters {
					a.OnFloorRelease(FloorUpdate{
						Type: FloorReleased, CallID: ev.CallID, GroupID: group,
						Talker: d.PreemptedUserID, Priority: pre.level,
						Emergency: pre.emergency, Reason: "preempted",
						Timestamp: now,
					})
				}
			}
		case floor.OutcomeDenied:
			if _, held := grants[d.UserID]; !held {
				continue // a plain deny: no floor was lost
			}
			delete(grants, d.UserID)
			reason := d.Reason
			if reason == "" {
				reason = "revoked"
			}
			for _, a := range b.adapters {
				a.OnFloorRelease(FloorUpdate{
					Type: FloorReleased, CallID: ev.CallID, GroupID: group,
					Talker: d.UserID, Priority: d.Level, Emergency: d.Emergency,
					Reason: reason, By: ev.Actor, Timestamp: now,
				})
			}
		case floor.OutcomeQueued:
			// No floor change; the carrier hears silence.
		}
	}
	b.mu.Unlock()
}

// flagged reports whether a talkgroup is on the allowlist. The empty
// allowlist streams nothing, and private calls (no group) never stream.
func (b *Bridge) flagged(groupID string) bool {
	return groupID != "" && len(b.cfg.Groups) > 0 && slices.Contains(b.cfg.Groups, groupID)
}
