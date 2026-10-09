// Command seed populates the demo roster into the configured database.
// Idempotent: existing users/groups are skipped, so it is safe to re-run.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"github.com/debudash/obvious-aitx/mcptt/server/internal/config"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/seed"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/store"
)

func main() {
	dbPath := flag.String("db", "", "SQLite database path (default: MCPTT_DB_PATH or mcptt.db)")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	path := *dbPath
	if path == "" {
		path = cfg.DBPath
	}

	st, err := store.Open(path, cfg.SQLiteBusyTimeout)
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()

	res, err := seed.Run(context.Background(), st, seed.DemoRoster())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("seed: users created=%d skipped=%d; groups created=%d skipped=%d; memberships=%d affiliations=%d\n",
		res.CreatedUsers, res.SkippedUsers, res.CreatedGroups, res.SkippedGroups, res.Memberships, res.Affiliations)
	fmt.Printf("seed: demo password for every account: %s\n", seed.DemoPassword)
}
