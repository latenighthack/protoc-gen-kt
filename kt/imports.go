package kt

import (
	"strings"

	pgs "github.com/lyft/protoc-gen-star"
)

func Imports(f pgs.File) []string {
	var imports []string

	for _, msg := range f.AllMessages() {
		for _, field := range msg.Fields() {
			if field.Type().IsEmbed() {
				fieldTypeName := FullyQualifiedName(field.Type().Embed()).String()

				if !strings.HasPrefix(fieldTypeName, PackageName(f).String()) {
					imports = append(imports, fieldTypeName)
				}
			} else if field.Type().IsMap() {
				fieldTypeName := qualifiedElType(field.Type()).String()

				if !strings.HasPrefix(fieldTypeName, PackageName(f).String()) {
					imports = append(imports, fieldTypeName)
				}
			} else if field.Type().IsRepeated() {
				if field.Type().Element().IsEmbed() {
					fieldTypeName := qualifiedElType(field.Type()).String()

					if !strings.HasPrefix(fieldTypeName, PackageName(f).String()) {
						imports = append(imports, fieldTypeName)
					}
				}
			}
		}
	}

	return unique(imports)
}

func PackageImports(f pgs.File) []string {
	var imports []string

	for _, msg := range f.AllMessages() {
		for _, field := range msg.Fields() {
			if field.Type().IsEmbed() {
				fieldTypeName := FullyQualifiedName(field.Type().Embed()).String()

				if !strings.HasPrefix(fieldTypeName, PackageName(f).String()) {
					imports = append(imports, PackageName(field.Type().Embed()).String())
				}
			} else if field.Type().IsMap() {
				fieldTypeName := qualifiedElType(field.Type()).String()

				if !strings.HasPrefix(fieldTypeName, PackageName(f).String()) {
					imports = append(imports, StripLastSegment(qualifiedElType(field.Type()).String()))
				}
			} else if field.Type().IsRepeated() {
				if field.Type().Element().IsEmbed() {
					fieldTypeName := qualifiedElType(field.Type()).String()

					if !strings.HasPrefix(fieldTypeName, PackageName(f).String()) {
						imports = append(imports, StripLastSegment(qualifiedElType(field.Type()).String()))
					}
				}
			}
		}
	}

	return unique(imports)
}

func BuilderImports(f pgs.File) []string {
	var imports []string

	for _, msg := range f.AllMessages() {
		for _, field := range msg.Fields() {
			if field.Type().IsEmbed() {
				packageName := PackageName(field.Type().Embed()).String()
				fieldTypeName := FullyQualifiedName(field.Type().Embed()).String()

				if !strings.HasPrefix(fieldTypeName, PackageName(f).String()) {
					imports = append(imports, packageName+"."+SimpleBuilderName(field.Type().Embed()).String())
				}
			}
		}
	}

	return unique(imports)
}
