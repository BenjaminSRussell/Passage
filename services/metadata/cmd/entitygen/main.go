package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BenjaminSRussell/passage/services/metadata/internal/gen"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).Error("entitygen failed", "error", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("entitygen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	in := fs.String("in", "", "directory of *.yaml entity definitions")
	out := fs.String("out", "", "directory for generated artifacts")
	check := fs.Bool("check", false, "fail when generated artifacts differ from -out")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" || *out == "" {
		return errors.New("usage: entitygen -in <dir> -out <dir> [-check]")
	}
	entities, err := gen.LoadDir(*in)
	if err != nil {
		return err
	}
	files := map[string]string{}
	for _, e := range entities {
		o, err := gen.Generate(e)
		if err != nil {
			return err
		}
		for name, content := range o.Files(e.Name) {
			files[name] = content
		}
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	if *check {
		var drifted []string
		for _, name := range names {
			if disk, err := os.ReadFile(filepath.Join(*out, name)); err != nil || string(disk) != files[name] {
				drifted = append(drifted, name)
			}
		}
		if len(drifted) > 0 {
			return fmt.Errorf("generated output drifted from %s: %s", *out, strings.Join(drifted, ", "))
		}
		_, err := fmt.Fprintf(stdout, "%d artifacts match\n", len(names))
		return err
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(*out, name), []byte(files[name]), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	_, err = fmt.Fprintf(stdout, "wrote %d artifacts\n", len(names))
	return err
}
