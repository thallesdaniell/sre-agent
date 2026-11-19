// Package migrations embute os arquivos .sql no binário: a imagem não precisa copiar a pasta.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
