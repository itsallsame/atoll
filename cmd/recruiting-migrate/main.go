package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
)

func main() {
	dsn := os.Getenv("ATOLL_RECRUITING_MYSQL_DSN")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "ATOLL_RECRUITING_MYSQL_DSN is required")
		os.Exit(2)
	}
	db, err := store.Open(dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "recruiting database unavailable")
		os.Exit(1)
	}
	if err := store.Migrate(ctx, db); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
