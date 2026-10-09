package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// webhookQueueDepth bounds the pending-event buffer. The carrier webhook is
// best-effort by design: it mirrors events that the dispatcher rail and the
// audit log already own durably, so a slow endpoint sheds the oldest event
// rather than backing up into call control.
const webhookQueueDepth = 64

// webhookPostTimeout bounds one POST attempt.
const webhookPostTimeout = 3 * time.Second

// WebhookAdapter delivers call/PTT/emergency events as JSON POSTs to the
// configured URL. It carries no audio (AudioSink discards frames) and never
// blocks: events are queued to one worker, order preserved, oldest shed
// under pressure.
type WebhookAdapter struct {
	url    string
	client *http.Client

	events chan webhookJob
	drops  atomic.Int64
	wg     sync.WaitGroup
	done   chan struct{}
}

// Compile-time proof that the webhook adapter satisfies the stable contract.
var _ RXAdapter = (*WebhookAdapter)(nil)

// NewWebhookAdapter builds the adapter and starts its delivery worker.
func NewWebhookAdapter(url string) *WebhookAdapter {
	a := &WebhookAdapter{
		url:    url,
		client: &http.Client{Timeout: webhookPostTimeout},
		events: make(chan webhookJob, webhookQueueDepth),
		done:   make(chan struct{}),
	}
	a.wg.Add(1)
	go a.deliver()
	return a
}

// webhookJob is one queued delivery.
type webhookJob struct {
	payload []byte
}

// webhookEvent is the JSON body of every carrier event post.
type webhookEvent struct {
	Event     string `json:"event"` // "call.started" | "call.ended" | "floor.granted" | "floor.released" | "ptt.emergency"
	Timestamp string `json:"ts"`    // RFC3339Nano
	CallID    string `json:"callId,omitempty"`
	GroupID   string `json:"groupId,omitempty"`
	Kind      string `json:"kind,omitempty"` // call events: group | private | broadcast
	UserID    string `json:"userId,omitempty"`
	Priority  int    `json:"priority,omitempty"`
	Emergency bool   `json:"emergency,omitempty"`
	Reason    string `json:"reason,omitempty"` // floor releases: released | revoked | preempted | expired
	By        string `json:"by,omitempty"`
}

// OnCallEvent queues a call lifecycle event.
func (a *WebhookAdapter) OnCallEvent(ev CallEvent) {
	name := ""
	switch ev.Type {
	case CallStarted:
		name = "call.started"
	case CallEnded:
		name = "call.ended"
	default:
		name = string(ev.Type)
	}
	a.enqueue(webhookEvent{
		Event:     name,
		Timestamp: ev.Timestamp.Format(time.RFC3339Nano),
		CallID:    ev.CallID,
		GroupID:   ev.GroupID,
		Kind:      ev.Kind,
		UserID:    ev.By,
	})
}

// OnFloorGrant queues a floor grant; emergency grants additionally queue an
// explicit ptt.emergency event so carriers can route alarms without parsing
// priority fields.
func (a *WebhookAdapter) OnFloorGrant(up FloorUpdate) {
	a.enqueue(webhookEvent{
		Event:     "floor.granted",
		Timestamp: up.Timestamp.Format(time.RFC3339Nano),
		CallID:    up.CallID,
		GroupID:   up.GroupID,
		UserID:    up.Talker,
		Priority:  int(up.Priority),
		Emergency: up.Emergency,
	})
	if up.Emergency {
		a.enqueue(webhookEvent{
			Event:     "ptt.emergency",
			Timestamp: up.Timestamp.Format(time.RFC3339Nano),
			CallID:    up.CallID,
			GroupID:   up.GroupID,
			UserID:    up.Talker,
			Priority:  int(up.Priority),
			Emergency: true,
		})
	}
}

// OnFloorRelease queues a floor loss (release, revoke, pre-emption, expiry).
func (a *WebhookAdapter) OnFloorRelease(up FloorUpdate) {
	a.enqueue(webhookEvent{
		Event:     "floor.released",
		Timestamp: up.Timestamp.Format(time.RFC3339Nano),
		CallID:    up.CallID,
		GroupID:   up.GroupID,
		UserID:    up.Talker,
		Priority:  int(up.Priority),
		Emergency: up.Emergency,
		Reason:    up.Reason,
		By:        up.By,
	})
}

// AudioSink returns a discarding sink: the webhook carries events only.
func (a *WebhookAdapter) AudioSink() AudioSink { return discardSink{} }

// Close stops the worker. Pending events are not drained — callers needing
// durable delivery do not use the carrier mirror.
func (a *WebhookAdapter) Close() error {
	select {
	case <-a.done:
	default:
		close(a.done)
	}
	a.wg.Wait()
	return nil
}

// enqueue queues one event without blocking; on a full queue the oldest
// pending event is shed and the drop counter grows.
func (a *WebhookAdapter) enqueue(ev webhookEvent) {
	payload, err := json.Marshal(ev)
	if err != nil {
		log.Printf("carrier webhook: marshal %s: %v", ev.Event, err)
		return
	}
	job := webhookJob{payload: payload}
	select {
	case a.events <- job:
	default:
		// Shed oldest, keep newest (the newest is the most actionable).
		select {
		case <-a.events:
			a.drops.Add(1)
			log.Printf("carrier webhook: queue full (%d drops total), shedding oldest", a.drops.Load())
		default:
		}
		select {
		case a.events <- job:
		default:
			a.drops.Add(1)
		}
	}
}

// deliver is the worker loop.
func (a *WebhookAdapter) deliver() {
	defer a.wg.Done()
	for {
		select {
		case <-a.done:
			return
		case job := <-a.events:
			a.post(job)
		}
	}
}

func (a *WebhookAdapter) post(job webhookJob) {
	ctx, cancel := context.WithTimeout(context.Background(), webhookPostTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.url, bytes.NewReader(job.payload))
	if err != nil {
		log.Printf("carrier webhook: build request: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		log.Printf("carrier webhook: post: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		log.Printf("carrier webhook: endpoint returned %d", resp.StatusCode)
	}
}

// discardSink implements AudioSink for adapters without an audio path.
type discardSink struct{}

func (discardSink) WriteAudio(AudioFrame) {}
