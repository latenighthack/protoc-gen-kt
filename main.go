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

	// Linking the generated package registers the buf.validate extensions so options arrive typed.
	_ "buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
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

// parseParameters reads the comma-separated k=v plugin parameter. Unknown keys are errors
// so that a typo cannot silently disable validation.
func parseParameters(raw string) (validate bool, err error) {
	for _, part := range strings.Split(raw, ",") {
		if part == "" {
			continue
		}
		key, value := part, ""
		if i := strings.Index(part, "="); i >= 0 {
			key, value = part[:i], part[i+1:]
		}
		switch key {
		case "validate":
			switch value {
			case "true":
				validate = true
			case "false":
				validate = false
			default:
				return false, fmt.Errorf("invalid value %q for parameter validate (expected true or false)", value)
			}
		case "output_path":
			// consumed by protoc-gen-star itself
		default:
			return false, fmt.Errorf("unknown parameter %q (supported: validate=true|false)", key)
		}
	}
	return validate, nil
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
	withValidate, err := parseParameters(request.GetParameter())
	if err != nil {
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

		// The service template will be specifically handled
		if strings.HasPrefix(e.Name(), "service") || strings.HasPrefix(e.Name(), "server") || strings.HasPrefix(e.Name(), "validate") {
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

	// buf.validate code is opt-in (validate=true)
	if withValidate {
		t, err = templates.ReadFile(path.Join("templates", "validate.kt.tmpl"))
		if err != nil {
			return err
		}
		modules = append(modules, kt.NewValidate(string(t), ".validate.kt"))
	}

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
