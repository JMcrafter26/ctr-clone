package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"ctindex/internal/store"
)

func main() {
	var (
		dbPath = flag.String("db", "ctindex.db", "sqlite path")
		cmd    = flag.String("cmd", "stats", "command: stats, search, count")
		apex   = flag.String("apex", "example.com", "apex for search")
	)
	flag.Parse()

	st, err := store.Open(*dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open: %v\n", err)
		os.Exit(1)
	}
	defer st.Close()
	ctx := context.Background()
	switch *cmd {
	case "stats":
		n, _ := st.CountAll(ctx)
		fmt.Printf("total: %d\n", n)
		recent, _ := st.Recent(ctx, 5)
		for _, r := range recent {
			fmt.Printf("%s %s %s\n", r.Apex, r.Sub, r.FirstSeen.Format(time.RFC3339))
		}
	case "search":
		rows, err := st.Search(ctx, *apex, false)
		if err != nil {
			fmt.Fprintf(os.Stderr, "search: %v\n", err)
			os.Exit(1)
		}
		for _, r := range rows {
			fmt.Printf("%s\t%s\n", r.Sub, r.FirstSeen.Format(time.RFC3339))
		}
		fmt.Printf("# %d results\n", len(rows))
	case "count":
		n, _ := st.CountAll(ctx)
		fmt.Println(n)
	default:
		fmt.Fprintf(os.Stderr, "unknown cmd %q\n", *cmd)
		os.Exit(1)
	}
}
