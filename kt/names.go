package kt

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	pgs "github.com/lyft/protoc-gen-star"
	"latenighthack.com/protoc-gen-kt/internal/strutil"
)

func Name(node pgs.Node) pgs.Name {
	// Message or Enum
	type ChildEntity interface {
		Name() pgs.Name
		Parent() pgs.ParentEntity
	}

	switch en := node.(type) {
	case pgs.Package: // the package name for the first file (should be consistent)
		return PackageName(en)
	case pgs.File: // the package name for this file
		return PackageName(en)
	case ChildEntity: // Message or Enum types, which may be nested
		if p, ok := en.Parent().(pgs.Message); ok {
			return pgs.Name(joinChild(Name(p), en.Name()))
		}
		return replaceProtected(PGGUpperCamelCase(en.Name()))
	case pgs.Field: // field names cannot conflict with other generated methods
		return replaceProtected(PGGLowerCamelCase(en.Name()))
	case pgs.OneOf: // oneof field names cannot conflict with other generated methods
		return replaceProtected(PGGLowerCamelCase(en.Name()))
	case pgs.EnumValue: // EnumValue are prefixed with the enum name
		return replaceProtected(pgs.Name(strings.ToUpper(en.Name().String())))
	case pgs.Service: // always return the server name
		return replaceProtected(PGGUpperCamelCase(en.Name()))
	case pgs.Entity: // any other entity should be just upper-camel-cased
		return replaceProtected(PGGLowerCamelCase(en.Name()))
	default:
		panic("unreachable")
	}
}

func PackageName(node pgs.Node) pgs.Name {
	e, ok := node.(pgs.Entity)
	if !ok {
		e = node.(pgs.Package).Files()[0]
	}

	// use import_path parameter ONLY if there is no go_package option in the file.
	return pgs.Name(e.File().Descriptor().GetOptions().GetJavaPackage())
}

func ProtoPackageName(node pgs.Node) pgs.Name {
	e, ok := node.(pgs.Entity)
	if !ok {
		e = node.(pgs.Package).Files()[0]
	}

	return pgs.Name(e.File().Descriptor().GetPackage())
}

func BuilderName(node pgs.Node) pgs.Name {
	switch node.(type) {
	case pgs.Message:
		return PackageName(node) + "." + pgs.Name(strings.ReplaceAll(Name(node).String(), ".", "_")+"Builder")
	default:
		panic("unknown type")
	}
}

func SimpleBuilderName(node pgs.Node) pgs.Name {
	return pgs.Name(strings.ReplaceAll(Name(node).String(), ".", "_") + "Builder")
}

func SimpleName(node pgs.Node) pgs.Name {
	switch en := node.(type) {
	case pgs.Message:
		return replaceProtected(PGGUpperCamelCase(en.Name()))
	case pgs.Enum:
		return replaceProtected(PGGUpperCamelCase(en.Name()))
	default:
		panic("unknown type")
	}
}

func OriginalName(node pgs.Node) pgs.Name {
	switch en := node.(type) {
	case pgs.Message:
		return en.Name()
	case pgs.Enum:
		return en.Name()
	case pgs.Entity:
		return en.Name()
	default:
		panic("unknown type")
	}
}

func QualifiedName(node pgs.Node) pgs.Name {
	switch en := node.(type) {
	case pgs.Message:
		return pgs.Name(en.Name())
	default:
		panic("unknown type")
	}
}

func FullyQualifiedName(node pgs.Node) pgs.Name {
	switch en := node.(type) {
	case pgs.Message:
		return pgs.Name(PackageName(node) + "." + Name(en))
	default:
		panic("unknown type")
	}
}

func FullyQualifiedCompanionName(node pgs.Node) pgs.Name {
	// Message or Enum
	type ChildEntity interface {
		Name() pgs.Name
		Parent() pgs.ParentEntity
	}

	switch en := node.(type) {
	case ChildEntity: // Message or Enum types, which may be nested
		if p, ok := en.Parent().(pgs.Message); ok {
			return pgs.Name(Name(p) + ".Companion." + en.Name())
		}
		return PGGUpperCamelCase(en.Name())
	case pgs.Entity: // any other entity should be just upper-camel-cased
		return PGGUpperCamelCase(en.Name())
	default:
		panic("unreachable")
	}
}

func EscapedFullyQualifiedName(node pgs.Node) string {
	return strings.ReplaceAll(FullyQualifiedName(node).String(), ".", "__")
}

func FieldTypeNameNonNull(fte pgs.FieldTypeElem) TypeName {
	ft := fte.ParentType()

	var t TypeName
	switch {
	case ft.IsMap():
		key := scalarType(ft.Key().ProtoType())
		return TypeName(fmt.Sprintf("Map<%s, %s>", key, elType(ft)))
	case ft.IsRepeated():
		return TypeName(fmt.Sprintf("List<%s>", elType(ft)))
	case ft.IsEmbed():
		return TypeName(Name(ft.Embed()).String())
	case ft.IsEnum():
		t = importableTypeName(ft.Enum())
	default:
		t = scalarType(ft.ProtoType())
	}

	return t
}

func FieldTypeName(fte pgs.FieldTypeElem) TypeName {
	ft := fte.ParentType()

	var t TypeName
	switch {
	case ft.IsMap():
		key := scalarType(ft.Key().ProtoType())
		return TypeName(fmt.Sprintf("Map<%s, %s>", key, elType(ft)))
	case ft.IsRepeated():
		return TypeName(fmt.Sprintf("List<%s>", elType(ft)))
	case ft.IsEmbed():
		return TypeName(Name(ft.Embed()).String() + "?")
	case ft.IsEnum():
		t = importableTypeName(ft.Enum())
	default:
		t = scalarType(ft.ProtoType())
	}

	return t
}

// PGGUpperCamelCase converts Name n to the protoc-gen-go defined upper
// camelcase. The rules are slightly different from pgs.UpperCamelCase in that
// leading underscores are converted to 'X', mid-string underscores followed by
// lowercase letters are removed and the letter is capitalized, all other
// punctuation is preserved. This method should be used when deriving names of
// protoc-gen-go generated code (ie, message/service struct names and field
// names).
//
// See: https://godoc.org/github.com/golang/protobuf/protoc-gen-go/generator#CamelCase
func PGGUpperCamelCase(n pgs.Name) pgs.Name {
	return pgs.Name(strutil.CamelCase(n.String()))
}

// PGGLowerCamelCase converts Name n to the protoc-gen-go defined lower
// camelcase. The rules are slightly different from pgs.LowerCamelCase in that
// leading underscores are converted to 'X', mid-string underscores followed by
// lowercase letters are removed and the letter is capitalized, all other
// punctuation is preserved. This method should be used when deriving names of
// protoc-gen-go generated code (ie, message/service struct names and field
// names).
//
// See: https://godoc.org/github.com/golang/protobuf/protoc-gen-go/generator#CamelCase
func PGGLowerCamelCase(n pgs.Name) pgs.Name {
	return pgs.Name(strutil.LowerCamelCase(n.String()))
}

var protectedNames = map[pgs.Name]pgs.Name{
	"as":        "as_",
	"break":     "break_",
	"class":     "class_",
	"continue":  "continue_",
	"do":        "do_",
	"else":      "else_",
	"false":     "false_",
	"for":       "for_",
	"fun":       "fun_",
	"if":        "if_",
	"in":        "in_",
	"interface": "interface_",
	"is":        "is_",
	"null":      "null_",
	"object":    "object_",
	"package":   "package_",
	"range":     "range_",
	"return":    "return_",
	"super":     "super_",
	"this":      "this_",
	"throw":     "throw_",
	"true":      "true_",
	"try":       "try_",
	"typealias": "typealias_",
	"typeof":    "typeof_",
	"val":       "val_",
	"var":       "var_",
	"when":      "when_",
	"while":     "while_",
	"Any":       "PbAny",
}

func replaceProtected(n pgs.Name) pgs.Name {
	if use, protected := protectedNames[n]; protected {
		return use
	}
	return n
}

func joinChild(a, b pgs.Name) pgs.Name {
	if r, _ := utf8.DecodeRuneInString(b.String()); unicode.IsLetter(r) && unicode.IsLower(r) {
		return pgs.Name(fmt.Sprintf("%s%s", a, PGGUpperCamelCase(b)))
	}
	return joinNames(a, PGGUpperCamelCase(b))
}

func joinNames(a, b pgs.Name) pgs.Name {
	return pgs.Name(fmt.Sprintf("%s.%s", a, b))
}

func unique(list []string) []string {
	unique := make([]string, 0, len(list))

	for l := range list {
		for u := range unique {
			if unique[u] == list[l] {
				goto next
			}
		}

		unique = append(unique, list[l])
	next:
	}

	return unique
}
