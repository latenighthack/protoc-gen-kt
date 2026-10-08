package kt

import (
	"fmt"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	pgs "github.com/lyft/protoc-gen-star"

	"latenighthack.com/protoc-gen-kt/internal/strutil"
)

func IsBytes(field pgs.Field) bool {
	ft := field.Type()

	switch {
	case ft.IsMap():
		return false
	case ft.IsRepeated():
		return false
	case ft.IsEmbed():
		return false
	case ft.IsEnum():
		return false
	}

	return ft.ProtoType() == pgs.BytesT
}

func IsBytesPT(ft pgs.ProtoType) bool {
	return ft == pgs.BytesT
}

func IsPacked(ft pgs.ProtoType) bool {
	switch ft {
	case pgs.BytesT, pgs.StringT, pgs.MessageT:
		return false
	default:
		return true
	}
}

func DefaultValue(field pgs.Field) string {
	ft := field.Type()

	switch {
	case ft.IsMap():
		return "emptyMap()"
	case ft.IsRepeated():
		return "emptyList()"
	case ft.IsEmbed():
		return "null"
	case ft.IsEnum():
		return Type(field).String() + "." + Name(ft.Enum().Values()[0]).String()
	default:
		t := ft.ProtoType()
		switch t {
		case pgs.DoubleT:
			return "0.0"
		case pgs.FloatT:
			return "0.0f"
		case pgs.Int64T, pgs.SFixed64, pgs.SInt64:
			return "0L"
		case pgs.UInt64T, pgs.Fixed64T:
			return "0UL"
		case pgs.Int32T, pgs.SFixed32, pgs.SInt32:
			return "0"
		case pgs.UInt32T, pgs.Fixed32T:
			return "0U"
		case pgs.BoolT:
			return "false"
		case pgs.StringT:
			return "\"\""
		case pgs.BytesT:
			return "ByteArray(0)"
		}
	}

	panic("unreachable: invalid scalar type")
}

func ReaderMethod(t pgs.ProtoType) string {
	switch t {
	case pgs.FloatT:
		return "readFloat"
	case pgs.DoubleT:
		return "readDouble"
	case pgs.Int32T:
		return "readInt32"
	case pgs.Int64T:
		return "readInt64"
	case pgs.UInt32T:
		return "readUInt32"
	case pgs.UInt64T:
		return "readUInt64"
	case pgs.SInt32:
		return "readSInt32"
	case pgs.SInt64:
		return "readSInt64"
	case pgs.Fixed32T:
		return "readFixedInt32"
	case pgs.Fixed64T:
		return "readFixedInt64"
	case pgs.SFixed64:
		return "readSFixedInt64"
	case pgs.SFixed32:
		return "readSFixedInt32"
	case pgs.BoolT:
		return "readBool"
	case pgs.StringT:
		return "readString"
	case pgs.BytesT:
		return "readBytes"
	}

	panic("unreachable: invalid scalar type")
}

func WriterMethod(t pgs.ProtoType) string {
	switch t {
	case pgs.SInt32, pgs.SInt64:
		return "encodeSInt"
	case pgs.Fixed32T, pgs.Fixed64T, pgs.SFixed32, pgs.SFixed64:
		return "encodeFixed"
	default:
		return "encode"
	}
}

func OutputPath(e pgs.Entity) pgs.FilePath {
	out := e.File().InputPath().SetExt("")
	path := strings.Join(strings.Split(strings.ReplaceAll(PackageName(e).String(), "`", ""), "."), "/")

	// Import relative ignores the existing file structure
	return pgs.FilePath(path).Push(out.Base())
}

func StripLastSegment(something string) string {
	remainder := ""

	components := strings.Split(something, ".")
	components = components[:len(components)-1]

	for _, component := range components {
		if len(remainder) > 0 {
			remainder = remainder + "." + component
		} else {
			remainder = component
		}
	}

	return remainder
}

func LowerCamelCase(s string) string {
	return string(replaceProtected(pgs.Name(strutil.LowerCamelCase(s))))
}

func UpperCamelCaseEnum(e pgs.EnumValue) string {
	parts := e.Name().Split()

	var b = strings.Builder{}
	for _, p := range parts {
		b.WriteString(cases.Title(language.English).String(strings.ToLower(p)))
	}

	return b.String()
}

func WireType(t pgs.ProtoType) int {
	switch t {
	case pgs.DoubleT, pgs.Fixed64T, pgs.SFixed64:
		return 1
	case pgs.StringT, pgs.BytesT, pgs.MessageT:
		return 2
	case pgs.FloatT, pgs.Fixed32T, pgs.SFixed32:
		return 5
	default:
		return 0
	}
}
func AcceptedWireTypes(f pgs.Field) string {
	ft := f.Type()
	if ft.IsRepeated() && !ft.IsMap() && IsPacked(ft.Element().ProtoType()) {
		return fmt.Sprintf("(reader.currentWireType == 2U || reader.currentWireType == %dU)", WireType(ft.Element().ProtoType()))
	}
	return fmt.Sprintf("reader.currentWireType == %dU", WireType(ft.ProtoType()))
}

func ShouldPack(f pgs.Field) bool {
	if !IsPacked(f.Type().Element().ProtoType()) {
		return false
	}
	options := f.Descriptor().GetOptions()
	return options == nil || options.Packed == nil || options.GetPacked()
}

func MapDefault(el pgs.FieldTypeElem) string {
	if el.IsEmbed() {
		return BuilderName(el.Embed()).String() + "().build()"
	}
	if el.IsEnum() {
		return importableTypeName(el.Enum()).String() + ".Companion.fromInt(0)"
	}
	switch el.ProtoType() {
	case pgs.DoubleT:
		return "0.0"
	case pgs.FloatT:
		return "0.0f"
	case pgs.Int64T, pgs.SFixed64, pgs.SInt64:
		return "0L"
	case pgs.UInt64T, pgs.Fixed64T:
		return "0UL"
	case pgs.Int32T, pgs.SFixed32, pgs.SInt32:
		return "0"
	case pgs.UInt32T, pgs.Fixed32T:
		return "0U"
	case pgs.BoolT:
		return "false"
	case pgs.StringT:
		return "\"\""
	case pgs.BytesT:
		return "ByteArray(0)"
	}
	panic("unsupported map element")
}
