// Package migrations embeds the application's SQL migration files so they
// ship inside the compiled binary. Pass migrations.FS to database.Migrate:
//
//	import "github.com/dewlonsystems/platform-go/migrations"
//	database.Migrate(ctx, pool, migrations.FS)
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
