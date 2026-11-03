// Package migrations embeds the versioned SQL migrations used by the control plane.
package migrations

import "embed"

// FS holds the goose-formatted migrations.
//
//go:embed *.sql
var FS embed.FS
