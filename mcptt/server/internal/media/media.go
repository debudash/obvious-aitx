// Package media implements the MCPTT media plane: a Pion WebRTC selective
// forwarding unit (SFU) that carries call audio inside the same server
// process as the control plane, so floor decisions and media routing share
// memory and cannot disagree (spec architecture invariant).
//
// Invariants:
//
//   - The floor controller is the single writer of talk permission. The SFU
//     reads floor state from the in-process Gate — written exclusively with
//     floor.FloorController decisions by the control plane — and never
//     decides who may speak.
//   - An un-granted microphone is never relayed: its RTP is received and
//     dropped at the SFU.
//   - A removed participant's transport is closed; from the moment Remove
//     returns they receive zero packets.
//   - Every transport is authorized by a short-lived, room-scoped token
//     minted per call, verified before any SDP is processed.
//
// Wire shape: each client offers exactly one sendrecv audio m-line — its own
// microphone — and the answer binds the floor-audio track to that same line.
// One m-line per participant means speaker changes never require
// renegotiation: the SFU relays whoever holds the floor down the one
// already-negotiated flow, with SSRC and payload type rewritten per listener
// (Pion's TrackLocalStaticRTP does the rewrite on WriteRTP).
package media

import (
	"fmt"

	"github.com/pion/ice/v4"
	"github.com/pion/webrtc/v4"
)

// opusCapability is the one audio codec MCPTT peers negotiate: Opus at
// 48 kHz, payload type 111. The spec's "48 kHz mono" is the encoder
// configuration at the clients; the SDP uses the interoperable opus/48000
// container so browser and mobile WebRTC stacks interoperate without
// transcoding. The SFU never decodes — it forwards payloads untouched.
var opusCapability = webrtc.RTPCodecCapability{
	MimeType:    webrtc.MimeTypeOpus,
	ClockRate:   48000,
	Channels:    2,
	SDPFmtpLine: "minptime=10;useinbandfec=1",
}

// NewMediaEngine returns a MediaEngine containing exactly that codec set —
// no video, no fallback audio codecs, so negotiation is deterministic and
// every hop agrees on one payload type.
func NewMediaEngine() (*webrtc.MediaEngine, error) {
	m := &webrtc.MediaEngine{}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: opusCapability,
		PayloadType:        111,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, fmt.Errorf("media: register opus: %w", err)
	}
	return m, nil
}

// newPeerAPI builds the pion API every local transport uses: the single
// Opus codec and plain host-candidate ICE without mDNS anonymization — the
// server is the media network, and deterministic SDP beats discovery here.
func newPeerAPI() (*webrtc.API, error) {
	engine, err := NewMediaEngine()
	if err != nil {
		return nil, err
	}
	var se webrtc.SettingEngine
	se.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	return webrtc.NewAPI(webrtc.WithMediaEngine(engine), webrtc.WithSettingEngine(se)), nil
}
