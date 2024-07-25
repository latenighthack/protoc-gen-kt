package main

import (
	"embed"
	"fmt"
	"os"
	"path"
	"strings"

	pgs "github.com/lyft/protoc-gen-star"

	"latenighthack.com/protoc-gen-kt/kt"
)

var (
	//go:embed templates
	templates embed.FS
)

func run() error {
	var modules []pgs.Module

	tmpls, err := templates.ReadDir("templates")
	if err != nil {
		return err
	}

	for _, e := range tmpls {
		if e.IsDir() {
			continue
		}

		t, err := templates.ReadFile(path.Join("templates", e.Name()))
		if err != nil {
			return err
		}

		// The service template will be specifically handled
		if strings.HasPrefix(e.Name(), "service") {
			continue
		}

		modules = append(
			modules,
			kt.New(
				string(t),
				"."+strings.TrimSuffix(e.Name(), ".tmpl"),
				func(_ pgs.File) bool {
					return true
				},
			),
		)
	}

	// Manually add the service template so that we can apply
	// a custom check for necessity. If there are no services
	// this should be skipped

	t, err := templates.ReadFile(path.Join("templates", "service.kt.tmpl"))
	if err != nil {
		return err
	}

	modules = append(
		modules,
		kt.New(
			string(t),
			"."+strings.TrimSuffix("service.kt.tmpl", ".tmpl"),
			func(f pgs.File) bool {
				return len(f.Services()) > 0
			},
		),
	)

	pgs.Init(pgs.DebugEnv("DEBUG")).
		RegisterModule(modules...).
		Render()

	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
