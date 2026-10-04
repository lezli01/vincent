package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "sitever:", err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: sitever versions | sitever assemble -in DIR -out DIR")
	}
	switch args[0] {
	case "versions":
		tags, err := readLines(stdin)
		if err != nil {
			return err
		}
		for _, t := range filterTags(tags) {
			if _, err := fmt.Fprintln(stdout, t); err != nil {
				return err
			}
		}
		return nil
	case "assemble":
		fs := flag.NewFlagSet("assemble", flag.ContinueOnError)
		in := fs.String("in", "", "directory holding one built tree per version (latest, dev, vX.Y.Z)")
		out := fs.String("out", "", "directory to write the assembled site to; must not exist or be empty")
		base := fs.String("base", "/vincent", "the site's base path on its host")
		url := fs.String("url", "https://lezli01.is-a.dev", "the site's origin, for absolute canonical URLs")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *in == "" || *out == "" {
			return fmt.Errorf("assemble needs -in and -out")
		}
		s, err := loadSite(*in, strings.TrimSuffix(*base, "/"), strings.TrimSuffix(*url, "/"))
		if err != nil {
			return err
		}
		return s.assemble(*out)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}
