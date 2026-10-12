// Command folio creates new folio projects.
//
//	go install github.com/AlexDev404/folio/cmd/folio@latest
//	folio new myapp
//	folio new -module github.com/me/myapp myapp
package main

import (
	"embed"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

//go:embed all:skeleton
var skeleton embed.FS

const usage = `folio creates new folio projects.

Usage:
  folio new [-module path] <directory>

The module path defaults to the directory name.
`

func main() {
	if len(os.Args) < 2 || os.Args[1] != "new" {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	flags := flag.NewFlagSet("new", flag.ExitOnError)
	module := flags.String("module", "", "Go module path (default: the directory name)")
	flags.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	flags.Parse(os.Args[2:])
	if flags.NArg() != 1 {
		flags.Usage()
		os.Exit(2)
	}

	if err := create(flags.Arg(0), *module); err != nil {
		fmt.Fprintln(os.Stderr, "folio:", err)
		os.Exit(1)
	}
}

func create(dir, module string) error {
	name := filepath.Base(filepath.Clean(dir))
	if module == "" {
		module = name
	}
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		return fmt.Errorf("%s already exists and isn't empty", dir)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	replacer := strings.NewReplacer("{{MODULE}}", module, "{{NAME}}", name)
	err := fs.WalkDir(skeleton, "skeleton", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := skeleton.ReadFile(p)
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(p, "skeleton/")
		// .skel files are templates for files Go would otherwise treat as part
		// of this module (go.mod, *.go); only they get placeholders replaced.
		if out, ok := strings.CutSuffix(rel, ".skel"); ok {
			rel = out
			data = []byte(replacer.Replace(string(data)))
		}
		if path.Base(rel) == "gitignore" {
			rel = path.Join(path.Dir(rel), ".gitignore")
		}
		target := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		return err
	}
	fmt.Printf("Created %s (module %s).\n", dir, module)

	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = dir
	tidy.Stdout, tidy.Stderr = os.Stdout, os.Stderr
	if err := tidy.Run(); err != nil {
		fmt.Printf("\n`go mod tidy` failed (%v); run it yourself once folio is reachable.\n", err)
	}

	fmt.Printf(`
Next steps:
  cd %s
  go run . -dev      # http://localhost:4000, reloads on changes
  go test ./...
`, dir)
	return nil
}
