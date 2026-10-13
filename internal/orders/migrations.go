package orders

import "embed"

// Migrations is the orders schema, compiled into the binary so the
// service always ships with the schema it expects. Apply it with
// database.RunMigrations(url, orders.Migrations, MigrationsDir).
//
//go:embed migrations/*.sql
var Migrations embed.FS

const MigrationsDir = "migrations"
