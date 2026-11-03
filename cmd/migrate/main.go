// kern-migrate applies pending database migrations and exits.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/dvorinka/kern/internal/store"
)

func main() {
	dsn := os.Getenv("KERN_DATABASE_URL")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "kern-migrate: KERN_DATABASE_URL is required")
		os.Exit(2)
	}
	ctx := context.Background()
	st, err := store.Open(ctx, dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "kern-migrate:", err)
		os.Exit(1)
	}
	defer st.Close()
	if err := st.Migrate(ctx, dsn); err != nil {
		fmt.Fprintln(os.Stderr, "kern-migrate:", err)
		os.Exit(1)
	}
	fmt.Println("migrations applied")
}
