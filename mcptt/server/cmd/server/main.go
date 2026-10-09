// Command server runs the MCPTT control plane: REST + WSS on one address.
// Media (Pion SFU) joins this process in the media-plane PR.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

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

	handler := ws.NewHandler(ws.NewHub(), tokens)
	handler.SetSDPHandler(sfu)

	// Media consequences of dispatcher actions: removals and force-ends
	// tear transports down server-side so removed parties stop receiving
	// packets immediately.
	mediaHooks := &api.MediaHooks{
		RemoveParticipant: sfu.Remove,
		EndCall:           sfu.EndCall,
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

// withLogging gives every request one visible access-log line.
func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(sw, r)
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, sw.status, time.Since(start).Round(100*time.Microsecond))
	})
}
