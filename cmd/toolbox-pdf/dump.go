package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/sarahmaeve/toolbox/internal/cliutil"
	"github.com/sarahmaeve/toolbox/pkg/pdf"
)

// runDump implements the `dump` subcommand: extract text from a PDF
// and print it to stdout. Pages are separated by form-feed characters
// (\f) which are preserved by `less` and `cat` and easy to grep for.
func runDump(args []string) error {
	fs := flag.NewFlagSet("dump", flag.ContinueOnError)
	page := fs.Int("page", 0, "extract only this page (1-indexed)")
	pages := fs.String("pages", "", "extract this inclusive page range, e.g. 3-7")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: toolbox-pdf dump [-page N | -pages N-M] <file.pdf>")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("expected exactly one PDF path")
	}
	path := fs.Arg(0)

	if *page != 0 && *pages != "" {
		return fmt.Errorf("-page and -pages are mutually exclusive")
	}

	from, to, err := cliutil.ParsePageRange(*page, *pages)
	if err != nil {
		return err
	}

	extracted, err := pdf.ExtractAllPages(path)
	if err != nil {
		return err
	}

	if from == 0 && to == 0 {
		from, to = 1, len(extracted)
	}
	if from < 1 || to > len(extracted) || from > to {
		return fmt.Errorf("page range %d-%d out of bounds (document has %d pages)",
			from, to, len(extracted))
	}

	for i := from; i <= to; i++ {
		if i > from {
			fmt.Println("\f")
		}
		fmt.Println(extracted[i-1])
	}
	return nil
}
