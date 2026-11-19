// sre-agent: um binário, vários subcomandos. Uma imagem só serve o intake, o job e as ferramentas.
//
//	sre-agent intake                       servidor do webhook (sempre ligado)
//	sre-agent job [incidente.json]         investiga um incidente (ou INCIDENT_JSON) e sai
//	sre-agent migrate                      aplica as migrations embutidas
//	sre-agent review [feedback ...]        lista relatórios e registra feedback
//	sre-agent import-postmortems <pasta>   importa post-mortems em markdown
package main

import (
	"fmt"
	"log/slog"
	"os"
)

var commands = map[string]func(args []string) error{
	"intake":             runIntake,
	"job":                runJob,
	"migrate":            runMigrate,
	"review":             runReview,
	"import-postmortems": runImportPostmortems,
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if len(os.Args) < 2 || commands[os.Args[1]] == nil {
		fmt.Fprintln(os.Stderr, "uso: sre-agent intake | job [arquivo] | migrate | review [feedback ...] | import-postmortems <pasta>")
		os.Exit(2)
	}
	if err := commands[os.Args[1]](os.Args[2:]); err != nil {
		slog.Error(os.Args[1]+" falhou", "error", err.Error())
		os.Exit(1)
	}
}
