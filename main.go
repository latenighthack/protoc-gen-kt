package main

import (
	"bytes"
	"embed"
	"fmt"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"
	"io"
	"os"
	"path"
	"strings"
	"unicode"

	pgs "github.com/lyft/protoc-gen-star"

	"latenighthack.com/protoc-gen-kt/kt"
)

var (
	//go:embed templates
	templates embed.FS
)

// Reject schemas whose presence semantics cannot be represented by this API.
// Validate the whole request before rendering any files.
func validateRequest(request *pluginpb.CodeGeneratorRequest) error {
	targets := map[string]bool{}
	for _, name := range request.FileToGenerate {
		targets[name] = true
	}
	var optional func([]*descriptorpb.DescriptorProto) bool
	optional = func(messages []*descriptorpb.DescriptorProto) bool {
		for _, message := range messages {
			for _, field := range message.Field {
				if field.GetProto3Optional() {
					return true
				}
			}
			if optional(message.NestedType) {
				return true
			}
		}
		return false
	}
	for _, file := range request.ProtoFile {
		if pkg := file.GetOptions().GetJavaPackage(); pkg != "" {
			for _, part := range strings.Split(pkg, ".") {
				if part == "" {
					return fmt.Errorf("%s: invalid java_package %q", file.GetName(), pkg)
				}
				for i, r := range part {
					if r == '_' || r == '$' || unicode.IsLetter(r) || unicode.Is(unicode.Nl, r) || (i > 0 && (unicode.IsDigit(r) || unicode.IsMark(r))) {
						continue
					}
					return fmt.Errorf("%s: invalid java_package %q", file.GetName(), pkg)
				}
			}
		}
		if !targets[file.GetName()] {
			continue
		}
		if file.GetSyntax() != "proto3" || optional(file.MessageType) {
			return fmt.Errorf("%s: unsupported presence semantics: protoc-gen-kt currently requires proto3 without optional fields", file.GetName())
		}
	}
	return nil
}

func hasWellKnownHelpers(name string) bool {
	return name == "google/protobuf/timestamp.proto" || name == "google/protobuf/duration.proto"
}

func run() error {
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err
	}
	request := new(pluginpb.CodeGeneratorRequest)
	if err := proto.Unmarshal(input, request); err != nil {
		return err
	}
	if err := validateRequest(request); err != nil {
		return err
	}
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

		// The service and wkt templates are specifically handled
		if strings.HasPrefix(e.Name(), "service") || strings.HasPrefix(e.Name(), "server") || strings.HasPrefix(e.Name(), "wkt") {
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

	// and for server

	t, err = templates.ReadFile(path.Join("templates", "server.kt.tmpl"))
	if err != nil {
		return err
	}

	modules = append(
		modules,
		kt.New(
			string(t),
			"."+strings.TrimSuffix("server.kt.tmpl", ".tmpl"),
			func(f pgs.File) bool {
				return len(f.Services()) > 0
			},
		),
	)

	// Well-known-type helpers: only for the two files that have helpers.

	t, err = templates.ReadFile(path.Join("templates", "wkt.kt.tmpl"))
	if err != nil {
		return err
	}

	modules = append(
		modules,
		kt.New(
			string(t),
			"."+strings.TrimSuffix("wkt.kt.tmpl", ".tmpl"),
			func(f pgs.File) bool {
				return hasWellKnownHelpers(f.InputPath().String())
			},
		),
	)

	pgs.Init(pgs.DebugEnv("DEBUG"), pgs.ProtocInput(bytes.NewReader(input))).
		RegisterModule(modules...).
		Render()

	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
