// Package db expõe as migrações SQL embutidas no binário, para que o mesmo
// conjunto de arquivos seja aplicado pelo comando migrar e pelos testes.
package db

import "embed"

//go:embed migrations/*.sql
var Migracoes embed.FS
