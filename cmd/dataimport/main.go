package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/Duang777/waybill-guardian/internal/platform/filestore"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "validate" {
		fmt.Fprintln(stderr, "usage: dataimport validate --data <file.json|file.csv>")
		return 2
	}
	flags := flag.NewFlagSet("dataimport validate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dataPath := flags.String("data", "", "JSON or CSV data file")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if *dataPath == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: dataimport validate --data <file.json|file.csv>")
		return 2
	}
	loaded, err := filestore.Load(*dataPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(
		stdout,
		"valid dataset=%s format=%s waybills=%d anomalies=%d\n",
		loaded.Source.DatasetID,
		loaded.Source.Format,
		loaded.Stats.Waybills,
		loaded.Stats.Anomalies,
	)
	return 0
}
