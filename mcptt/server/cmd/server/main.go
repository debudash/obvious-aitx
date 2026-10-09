// Command server runs the MCPTT control plane: REST + WSS on one address.
// Media (Pion SFU) joins this process in the media-plane PR.
package main

import (
	"bufio"
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/debudash/obvious-aitx/mcptt/server/integration"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/api"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/auth"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/callcontrol"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/config"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/media"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/store"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/ws"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.DBPath, cfg.SQLiteBusyTimeout)
	if err != nil {
		return err
	}
	defer st.Close()

	tokens := auth.NewTokenizer(cfg.JWTSecret, cfg.JWTTTL)

	// Media plane: same process, same signing secret; room tokens carry a
	// separate claim set so access tokens never admit a media room.
	mediaGate := media.NewGate()
	roomTokens := media.NewRoomTokens(cfg.JWTSecret, media.DefaultRoomTokenTTL)
	sfu := media.NewSFU(mediaGate, roomTokens)

	// Control plane: one authoritative session manager. Floor transitions
	// mirror into the gate inside the same critical section that decides
	// them (the spec's boundary invariant), and every event fans out to
	// connected clients through the api layer's renderer.
	calls := callcontrol.NewSessionManager(callcontrol.SessionConfig{
		MaxTalkDuration: cfg.MaxTalkDuration,
		Media:           mediaGate,
		Affiliation: func(userID, groupID string) bool {
			aff, err := st.AffiliationState(context.Background(), userID, groupID)
			if err != nil {
				// Deny on failure — the affiliation store, not the
				// client, decides who may join.
				log.Printf("mcptt: affiliation check %s@%s: %v", userID, groupID, err)
				return false
			}
			return aff.State == store.AffiliationAffiliated
		},
	})

	// Carrier RX integration (spec: flag-gated, off by default). Disabled →
	// NewFromConfig returns nil: no adapter, no socket, no tap is ever
	// constructed, which is the flag-off guarantee of zero external sockets.
	// When on, the bridge mirrors what the manager already arbitrated:
	// session events via SetOnEvent, and each call's owning talkgroup via
	// the manager's GroupOf resolver (events carry GroupID; the resolver is
	// only the fallback for events without one).
	carrierBridge, err := integration.NewFromConfig(cfg.Carrier, integration.BridgeOptions{
		GroupOf: calls.GroupOf,
	})
	if err != nil {
		return err
	}
	if carrierBridge != nil {
		calls.SetOnEvent(carrierBridge.HandleEvent)
		carrierBridge.AttachSFU(sfu)
		defer carrierBridge.Close()
		log.Printf("carrier: rx integration enabled (adapter=%s codec=%s groups=%d events_url=%v)",
			cfg.Carrier.Adapter, cfg.Carrier.Codec, len(cfg.Carrier.Groups), cfg.Carrier.EventsURL != "")
	}

	handler := ws.NewHandler(ws.NewHub(), tokens)
	handler.SetSDPHandler(sfu)
	handler.SetCallController(calls)

	// Media consequences of dispatcher actions: removals and force-ends
	// tear transports down server-side so removed parties stop receiving
	// packets immediately. MintRoomToken is the reach the MediaOffer
	// contract assumes: a call participant fetches a room-scoped token
	// (POST /api/calls/{id}/media-token) and presents it with their offer.
	mediaHooks := &api.MediaHooks{
		RemoveParticipant: sfu.Remove,
		EndCall:           sfu.EndCall,
		MintRoomToken: func(callID, userID string) (string, error) {
			return roomTokens.Issue(callID, userID, time.Now())
		},
	}

	apiSrv := api.NewServer(st, tokens, handler, calls, mediaHooks)
	calls.SetOnEvent(apiSrv.HandleCallEvent)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           withLogging(apiSrv.Handler()),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		errc <- srv.ListenAndServe()
	}()
	log.Printf("mcptt: listening on %s", cfg.Addr)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	select {
	case err := <-errc:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case sig := <-stop:
		log.Printf("mcptt: %s received, draining", sig)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(ctx)
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Hijack forwards the hijacker so WebSocket upgrades survive the logging
// wrapper: http.ResponseWriter embedding does not forward http.Hijacker, and
// without this every /ws upgrade fails with 500.
func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("mcptt: underlying ResponseWriter does not support hijacking")
	}
	w.status = http.StatusSwitchingProtocols
	return h.Hijack()
}

// withLogging gives every request one visible access-log line.
func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(sw, r)
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, sw.status, time.Since(start).Round(100*time.Microsecond))
	})
}
