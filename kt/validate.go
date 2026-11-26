package kt

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"

	validate "buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	pgs "github.com/lyft/protoc-gen-star"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"latenighthack.com/protoc-gen-kt/internal/strutil"
)

// This file turns buf.validate options into Kotlin. Everything here is driven by the
// descriptors in the request; rules this generator cannot reproduce exactly are rejected
// instead of being silently dropped.

type fileValidation struct {
	prelude  string
	messages map[string]string
}

var (
	validationCacheLock sync.Mutex
	validationCache     = map[string]*fileValidation{}
)

// ValidateFile returns (building on first use) the validation code for a file.
func validationFor(f pgs.File) (*fileValidation, error) {
	validationCacheLock.Lock()
	defer validationCacheLock.Unlock()
	key := f.Name().String()
	if v, ok := validationCache[key]; ok {
		return v, nil
	}
	b := &validationBuilder{file: f, out: &fileValidation{messages: map[string]string{}}}
	var walk func(ms []pgs.Message) error
	walk = func(ms []pgs.Message) error {
		for _, m := range ms {
			body, err := b.message(m)
			if err != nil {
				return err
			}
			b.out.messages[m.FullyQualifiedName()] = body
			if err := walk(m.Messages()); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(f.Messages()); err != nil {
		return nil, err
	}
	b.out.prelude = strings.Join(b.regexes, "\n")
	validationCache[key] = b.out
	return b.out, nil
}

// CheckValidation reports unsupported rules for a file. Used to fail generation up front.
func CheckValidation(f pgs.File) error {
	_, err := validationFor(f)
	return err
}

func ValidatePrelude(f pgs.File) (string, error) {
	v, err := validationFor(f)
	if err != nil {
		return "", err
	}
	return v.prelude, nil
}

func ValidateBody(m pgs.Message) (string, error) {
	v, err := validationFor(m.File())
	if err != nil {
		return "", err
	}
	return v.messages[m.FullyQualifiedName()], nil
}

type validationBuilder struct {
	file    pgs.File
	out     *fileValidation
	regexes []string
	// current context for error messages
	where string
	tmp   int
}

func (b *validationBuilder) unsupported(rule string) error {
	return fmt.Errorf("%s: %s: unsupported buf.validate rule %s", b.file.Name(), b.where, rule)
}

func (b *validationBuilder) fail(format string, args ...interface{}) error {
	return fmt.Errorf("%s: %s: %s", b.file.Name(), b.where, fmt.Sprintf(format, args...))
}

func protoMessageName(m pgs.Message) string {
	var parts []string
	var cur pgs.ParentEntity = m
	for {
		msg, ok := cur.(pgs.Message)
		if !ok {
			break
		}
		parts = append([]string{msg.Name().String()}, parts...)
		cur = msg.Parent()
	}
	return strings.Join(parts, ".")
}

// ---------------------------------------------------------------- writer

type kw struct {
	sb     strings.Builder
	indent int
}

func (w *kw) line(format string, args ...interface{}) {
	s := format
	if len(args) > 0 {
		s = fmt.Sprintf(format, args...)
	}
	w.sb.WriteString(strings.Repeat("    ", w.indent))
	w.sb.WriteString(s)
	w.sb.WriteString("\n")
}

func (w *kw) open(format string, args ...interface{}) {
	w.line(format, args...)
	w.indent++
}

func (w *kw) close(s string) {
	w.indent--
	w.line(s)
}

// ---------------------------------------------------------------- literals

func ktString(s string) string {
	var sb strings.Builder
	sb.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			sb.WriteString("\\\"")
		case r == '\\':
			sb.WriteString("\\\\")
		case r == '$':
			sb.WriteString("\\$")
		case r == '\n':
			sb.WriteString("\\n")
		case r == '\r':
			sb.WriteString("\\r")
		case r == '\t':
			sb.WriteString("\\t")
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == 0x2028 || r == 0x2029:
			sb.WriteString(fmt.Sprintf("\\u%04x", r))
		case r < 0x7f:
			sb.WriteRune(r)
		case r <= 0xffff:
			sb.WriteString(fmt.Sprintf("\\u%04x", r))
		default:
			r -= 0x10000
			sb.WriteString(fmt.Sprintf("\\u%04x\\u%04x", 0xd800+(r>>10), 0xdc00+(r&0x3ff)))
		}
	}
	sb.WriteByte('"')
	return sb.String()
}

func ktBytes(b []byte) string {
	if len(b) == 0 {
		return "ByteArray(0)"
	}
	parts := make([]string, len(b))
	for i, c := range b {
		parts[i] = strconv.Itoa(int(int8(c)))
	}
	return "byteArrayOf(" + strings.Join(parts, ", ") + ")"
}

func ktFloat(f float64, single bool) string {
	switch {
	case math.IsNaN(f):
		if single {
			return "Float.NaN"
		}
		return "Double.NaN"
	case math.IsInf(f, 1):
		if single {
			return "Float.POSITIVE_INFINITY"
		}
		return "Double.POSITIVE_INFINITY"
	case math.IsInf(f, -1):
		if single {
			return "Float.NEGATIVE_INFINITY"
		}
		return "Double.NEGATIVE_INFINITY"
	}
	bits := 64
	if single {
		bits = 32
	}
	s := strconv.FormatFloat(f, 'g', -1, bits)
	if !strings.ContainsAny(s, ".e") {
		s += ".0"
	}
	if single {
		s += "f"
	}
	return "(" + s + ")"
}

// ---------------------------------------------------------------- kinds

type elemType interface {
	ProtoType() pgs.ProtoType
	IsEmbed() bool
	IsEnum() bool
	Embed() pgs.Message
	Enum() pgs.Enum
}

// kind describes the value being validated.
type kind struct {
	pt       pgs.ProtoType
	enum     pgs.Enum
	msg      pgs.Message
	ruleName string // the FieldRules oneof name that applies, "" when none
}

const (
	wktDuration  = ".google.protobuf.Duration"
	wktTimestamp = ".google.protobuf.Timestamp"
)

func kindOf(t elemType) kind {
	k := kind{pt: t.ProtoType()}
	switch {
	case t.IsEnum():
		k.enum = t.Enum()
		k.ruleName = "enum"
	case t.IsEmbed():
		k.msg = t.Embed()
		switch k.msg.FullyQualifiedName() {
		case wktDuration:
			k.ruleName = "duration"
		case wktTimestamp:
			k.ruleName = "timestamp"
		}
	default:
		k.ruleName = map[pgs.ProtoType]string{
			pgs.Int32T: "int32", pgs.Int64T: "int64", pgs.UInt32T: "uint32", pgs.UInt64T: "uint64",
			pgs.SInt32: "sint32", pgs.SInt64: "sint64", pgs.Fixed32T: "fixed32", pgs.Fixed64T: "fixed64",
			pgs.SFixed32: "sfixed32", pgs.SFixed64: "sfixed64", pgs.FloatT: "float", pgs.DoubleT: "double",
			pgs.BoolT: "bool", pgs.StringT: "string", pgs.BytesT: "bytes",
		}[k.pt]
	}
	return k
}

func (k kind) isMessage() bool { return k.msg != nil }
func (k kind) isTime() bool    { return k.ruleName == "duration" || k.ruleName == "timestamp" }
func (k kind) isFloat() bool   { return k.pt == pgs.FloatT || k.pt == pgs.DoubleT }
func (k kind) isNumeric() bool {
	if k.isMessage() || k.enum != nil {
		return false
	}
	switch k.pt {
	case pgs.StringT, pgs.BytesT, pgs.BoolT:
		return false
	}
	return true
}
func (k kind) isUnsigned() bool {
	return k.pt == pgs.UInt32T || k.pt == pgs.UInt64T || k.pt == pgs.Fixed32T || k.pt == pgs.Fixed64T
}

// recursable reports whether a call to the message's validateInto is emitted. It fails closed:
// every message type recurses (including messages from other modules, which then must also be
// generated with validate=true or the consumer fails to compile) except google.protobuf.* well-known
// types, which carry no protovalidate rules.
func (k kind) recursable() bool {
	return k.msg != nil && isRecursableMessage(k.msg)
}

func isRecursableMessage(m pgs.Message) bool {
	return m.Package().ProtoName().String() != "google.protobuf"
}

// ValidateImports returns the packages whose validateInto extension functions the file's
// validation code calls on embedded messages declared in other packages.
func ValidateImports(f pgs.File) []string {
	var imports []string
	add := func(m pgs.Message) {
		if !isRecursableMessage(m) {
			return
		}
		if name := PackageName(m).String(); name != PackageName(f).String() {
			imports = append(imports, name)
		}
	}
	for _, message := range f.AllMessages() {
		for _, field := range message.Fields() {
			ft := field.Type()
			if ft.IsEmbed() {
				add(ft.Embed())
			} else if (ft.IsMap() || ft.IsRepeated()) && ft.Element().IsEmbed() {
				add(ft.Element().Embed())
			}
		}
	}
	return unique(imports)
}

func (k kind) numLit(v protoreflect.Value) string {
	switch k.pt {
	case pgs.Int32T, pgs.SInt32, pgs.SFixed32:
		n := v.Int()
		if n == math.MinInt32 {
			return "(-2147483647 - 1)"
		}
		return fmt.Sprintf("%d", n)
	case pgs.Int64T, pgs.SInt64, pgs.SFixed64:
		n := v.Int()
		if n == math.MinInt64 {
			return "(-9223372036854775807L - 1L)"
		}
		return fmt.Sprintf("%dL", n)
	case pgs.UInt32T, pgs.Fixed32T:
		return fmt.Sprintf("%du", v.Uint())
	case pgs.UInt64T, pgs.Fixed64T:
		return fmt.Sprintf("%duL", v.Uint())
	case pgs.FloatT:
		return ktFloat(v.Float(), true)
	case pgs.DoubleT:
		return ktFloat(v.Float(), false)
	}
	panic("not numeric")
}

func (k kind) zeroCheck(v string) string {
	switch {
	case k.isMessage():
		return v + " == null"
	case k.enum != nil:
		return v + ".value == 0"
	}
	switch k.pt {
	case pgs.StringT:
		return v + ".isEmpty()"
	case pgs.BytesT:
		return v + ".isEmpty()"
	case pgs.BoolT:
		return "!" + v
	case pgs.Int32T, pgs.SInt32, pgs.SFixed32:
		return v + " == 0"
	case pgs.Int64T, pgs.SInt64, pgs.SFixed64:
		return v + " == 0L"
	case pgs.UInt32T, pgs.Fixed32T:
		return v + " == 0u"
	case pgs.UInt64T, pgs.Fixed64T:
		return v + " == 0uL"
	case pgs.FloatT:
		return v + " == 0.0f"
	case pgs.DoubleT:
		return v + " == 0.0"
	}
	return "false"
}

// ---------------------------------------------------------------- messages

func (b *validationBuilder) message(m pgs.Message) (string, error) {
	b.where = protoMessageName(m)
	w := &kw{indent: 1}
	opts := m.Descriptor().GetOptions()
	if opts != nil && proto.HasExtension(opts, validate.E_Message) {
		mr, _ := proto.GetExtension(opts, validate.E_Message).(*validate.MessageRules)
		if mr != nil {
			if len(mr.GetCel()) > 0 || len(mr.GetCelExpression()) > 0 {
				name := "cel"
				if len(mr.GetCel()) == 0 {
					name = "cel_expression"
				}
				return "", b.unsupported(name)
			}
			if len(mr.GetOneof()) > 0 {
				return "", b.unsupported("oneof")
			}
			if err := b.rejectUnknown(mr.ProtoReflect(), "", map[string]bool{}); err != nil {
				return "", err
			}
		}
	}

	for _, o := range m.OneOfs() {
		oo := o.Descriptor().GetOptions()
		if oo == nil || !proto.HasExtension(oo, validate.E_Oneof) {
			continue
		}
		or, _ := proto.GetExtension(oo, validate.E_Oneof).(*validate.OneofRules)
		if or == nil {
			continue
		}
		b.where = protoMessageName(m) + "." + o.Name().String()
		if err := b.rejectUnknown(or.ProtoReflect(), "", map[string]bool{"required": true}); err != nil {
			return "", err
		}
		if or.GetRequired() {
			w.line("if (this.%s == null) out.add(Violation(joinPath(path, %s), \"required\", \"exactly one field is required in oneof\"))",
				LowerCamelCase(o.Descriptor().GetName()), ktString(o.Name().String()))
		}
	}

	for _, f := range m.Fields() {
		b.where = protoMessageName(m) + "." + f.Name().String()
		if err := b.field(w, f); err != nil {
			return "", err
		}
	}
	return w.sb.String(), nil
}

// rejectUnknown fails on any set field that is not in allowed, and on unknown/extension fields.
func (b *validationBuilder) rejectUnknown(m protoreflect.Message, prefix string, allowed map[string]bool) error {
	if len(m.GetUnknown()) > 0 {
		return b.unsupported(prefix + "<unrecognised rule>")
	}
	var bad []string
	m.Range(func(fd protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		if fd.IsExtension() || !allowed[string(fd.Name())] {
			bad = append(bad, prefix+string(fd.Name()))
		}
		return true
	})
	if len(bad) > 0 {
		sort.Strings(bad)
		return b.unsupported(bad[0])
	}
	return nil
}

func (b *validationBuilder) field(w *kw, f pgs.Field) error {
	var rules *validate.FieldRules
	if opts := f.Descriptor().GetOptions(); opts != nil && proto.HasExtension(opts, validate.E_Field) {
		rules, _ = proto.GetExtension(opts, validate.E_Field).(*validate.FieldRules)
	}
	ft := f.Type()
	if rules != nil {
		if len(rules.GetCel()) > 0 {
			return b.unsupported("cel")
		}
		if len(rules.GetCelExpression()) > 0 {
			return b.unsupported("cel_expression")
		}
		if len(rules.ProtoReflect().GetUnknown()) > 0 {
			return b.unsupported("<unrecognised rule>")
		}
		if rules.GetIgnore() == validate.Ignore_IGNORE_ALWAYS {
			return nil
		}
	}
	name := Name(f).String()
	protoName := f.Name().String()
	inOneof := f.InOneOf()

	var base kind
	switch {
	case ft.IsMap():
		base = kindOf(ft.Element())
	case ft.IsRepeated():
		base = kindOf(ft.Element())
	default:
		base = kindOf(ft)
	}

	needsWork := rules != nil
	if !needsWork {
		// Only recursion into nested messages.
		if !base.recursable() {
			return nil
		}
	}

	var sub *validate.FieldRules
	if rules != nil {
		sub = rules
	}
	w.open("run {")
	var subject string
	switch {
	case inOneof:
		oo := f.OneOf()
		w.line("val v = this.%s?.get%s()", LowerCamelCase(oo.Descriptor().GetName()), strutil.CamelCase(f.Descriptor().GetName()))
		subject = "v"
	default:
		w.line("val v = this.%s", name)
		subject = "v"
	}
	w.line("val p = joinPath(path, %s)", ktString(protoName))

	required := rules != nil && rules.GetRequired()
	ifZero := rules != nil && rules.GetIgnore() == validate.Ignore_IGNORE_IF_ZERO_VALUE

	// zero/absent test of the whole field
	var zero string
	var typeKind kind
	switch {
	case ft.IsMap() || ft.IsRepeated():
		zero = subject + ".isEmpty()"
		if inOneof {
			return b.fail("repeated/map fields cannot be in a oneof")
		}
	case inOneof:
		typeKind = kindOf(ft)
		zero = subject + " == null"
	default:
		typeKind = kindOf(ft)
		zero = typeKind.zeroCheck(subject)
	}

	if required {
		w.line("if (%s) out.add(Violation(p, \"required\", \"value is required\"))", zero)
	}

	body := &kw{indent: w.indent}
	wrap := func(cond string, inner func(*kw) error) error {
		if cond == "" {
			return inner(body)
		}
		in := &kw{indent: body.indent + 1}
		if err := inner(in); err != nil {
			return err
		}
		if in.sb.Len() == 0 {
			return nil
		}
		body.line("if (%s) {", cond)
		body.sb.WriteString(in.sb.String())
		body.line("}")
		return nil
	}
	switch {
	case inOneof:
		// Explicit presence: rules apply whenever the member is set, even to a zero value.
		// IGNORE_IF_ZERO_VALUE is documented as a no-op for fields that track presence.
		cond := "v != null"
		if err := wrap(cond, func(x *kw) error { return b.elementRules(x, "v", typeKind, sub, "p", "", true) }); err != nil {
			return err
		}
	case ft.IsMap():
		if err := b.mapRules(body, ft, sub, ifZero); err != nil {
			return err
		}
	case ft.IsRepeated():
		if err := b.repeatedRules(body, ft, sub, ifZero); err != nil {
			return err
		}
	default:
		cond := ""
		if typeKind.isMessage() {
			cond = "v != null"
		} else if ifZero {
			cond = "!(" + zero + ")"
		}
		if err := wrap(cond, func(x *kw) error { return b.elementRules(x, "v", typeKind, sub, "p", "", true) }); err != nil {
			return err
		}
	}
	if body.sb.Len() == 0 && !required {
		// nothing emitted: drop the block entirely
		return nil
	}
	w.sb.WriteString(body.sb.String())
	w.close("}")
	return nil
}

// ---------------------------------------------------------------- repeated / map

func ruleMsg(r *validate.FieldRules, name string) protoreflect.Message {
	if r == nil {
		return nil
	}
	m := r.ProtoReflect()
	od := m.Descriptor().Oneofs().ByName("type")
	fd := m.WhichOneof(od)
	if fd == nil || string(fd.Name()) != name {
		return nil
	}
	return m.Get(fd).Message()
}

func typeRuleName(r *validate.FieldRules) string {
	if r == nil {
		return ""
	}
	m := r.ProtoReflect()
	fd := m.WhichOneof(m.Descriptor().Oneofs().ByName("type"))
	if fd == nil {
		return ""
	}
	return string(fd.Name())
}

func (b *validationBuilder) repeatedRules(w *kw, ft pgs.FieldType, r *validate.FieldRules, ifZero bool) error {
	elem := kindOf(ft.Element())
	var rr protoreflect.Message
	if r != nil {
		if tn := typeRuleName(r); tn != "" && tn != "repeated" {
			return b.fail("rule %s does not apply to a repeated field", tn)
		}
		rr = ruleMsg(r, "repeated")
	}
	inner := &kw{indent: w.indent + 1}
	var items *validate.FieldRules
	if rr != nil {
		if err := b.rejectUnknown(rr, "repeated.", map[string]bool{"min_items": true, "max_items": true, "unique": true, "items": true}); err != nil {
			return err
		}
		rep := r.GetRepeated()
		if rep.MinItems != nil {
			inner.line("if (v.size.toULong() < %duL) out.add(Violation(p, \"repeated.min_items\", %s))", rep.GetMinItems(),
				ktString(fmt.Sprintf("must contain at least %d item(s)", rep.GetMinItems())))
		}
		if rep.MaxItems != nil {
			inner.line("if (v.size.toULong() > %duL) out.add(Violation(p, \"repeated.max_items\", %s))", rep.GetMaxItems(),
				ktString(fmt.Sprintf("must contain no more than %d item(s)", rep.GetMaxItems())))
		}
		if rep.GetUnique() {
			if elem.isMessage() {
				return b.fail("repeated.unique is only supported for scalar elements")
			}
			var cond string
			if elem.isFloat() {
				cond = "!isUnique" + map[pgs.ProtoType]string{pgs.FloatT: "Floats", pgs.DoubleT: "Doubles"}[elem.pt] + "(v)"
			} else if elem.pt == pgs.BytesT {
				cond = "v.map { it.toList() }.toSet().size != v.size"
			} else if elem.enum != nil {
				cond = "v.map { it.value }.toSet().size != v.size"
			} else {
				cond = "v.toSet().size != v.size"
			}
			inner.line("if (%s) out.add(Violation(p, \"repeated.unique\", \"repeated value must contain unique items\"))", cond)
		}
		items = rep.GetItems()
	}
	loop := &kw{indent: inner.indent + 1}
	if err := b.nestedRules(loop, "e", elem, items, "ep", "repeated.items."); err != nil {
		return err
	}
	if loop.sb.Len() > 0 {
		inner.line("for ((i, e) in v.withIndex()) {")
		inner.line("    val ep = indexPath(p, i)")
		inner.sb.WriteString(loop.sb.String())
		inner.line("}")
	}
	if inner.sb.Len() == 0 {
		return nil
	}
	if ifZero {
		w.line("if (v.isNotEmpty()) {")
		w.sb.WriteString(inner.sb.String())
		w.line("}")
	} else {
		w.sb.WriteString(inner.sb.String())
	}
	return nil
}

func (b *validationBuilder) mapRules(w *kw, ft pgs.FieldType, r *validate.FieldRules, ifZero bool) error {
	elem := kindOf(ft.Element())
	key := kindOf(ft.Key())
	inner := &kw{indent: w.indent + 1}
	var keys, values *validate.FieldRules
	if r != nil {
		if tn := typeRuleName(r); tn != "" && tn != "map" {
			return b.fail("rule %s does not apply to a map field", tn)
		}
		if rm := ruleMsg(r, "map"); rm != nil {
			if err := b.rejectUnknown(rm, "map.", map[string]bool{"min_pairs": true, "max_pairs": true, "keys": true, "values": true}); err != nil {
				return err
			}
			mr := r.GetMap()
			if mr.MinPairs != nil {
				inner.line("if (v.size.toULong() < %duL) out.add(Violation(p, \"map.min_pairs\", %s))", mr.GetMinPairs(),
					ktString(fmt.Sprintf("map must be at least %d entries", mr.GetMinPairs())))
			}
			if mr.MaxPairs != nil {
				inner.line("if (v.size.toULong() > %duL) out.add(Violation(p, \"map.max_pairs\", %s))", mr.GetMaxPairs(),
					ktString(fmt.Sprintf("map must be at most %d entries", mr.GetMaxPairs())))
			}
			keys, values = mr.GetKeys(), mr.GetValues()
		}
	}
	keyBody := &kw{indent: inner.indent + 1}
	if err := b.nestedRules(keyBody, "k", key, keys, "ep", "map.keys."); err != nil {
		return err
	}
	valBody := &kw{indent: inner.indent + 1}
	if err := b.nestedRules(valBody, "e", elem, values, "ep", "map.values."); err != nil {
		return err
	}
	if keyBody.sb.Len() > 0 || valBody.sb.Len() > 0 {
		inner.line("for ((k, e) in v) {")
		inner.line("    val ep = mapKeyPath(p, k)")
		inner.sb.WriteString(keyBody.sb.String())
		inner.sb.WriteString(valBody.sb.String())
		inner.line("}")
	}
	if inner.sb.Len() == 0 {
		return nil
	}
	if ifZero {
		w.line("if (v.isNotEmpty()) {")
		w.sb.WriteString(inner.sb.String())
		w.line("}")
	} else {
		w.sb.WriteString(inner.sb.String())
	}
	return nil
}

// nestedRules handles an element (repeated item, map key or map value): optional
// FieldRules plus message recursion.
func (b *validationBuilder) nestedRules(w *kw, subject string, k kind, r *validate.FieldRules, pathVar, prefix string) error {
	if r != nil {
		if len(r.GetCel()) > 0 {
			return b.unsupported(prefix + "cel")
		}
		if len(r.GetCelExpression()) > 0 {
			return b.unsupported(prefix + "cel_expression")
		}
		if r.Required != nil {
			return b.unsupported(prefix + "required")
		}
		if r.GetIgnore() == validate.Ignore_IGNORE_ALWAYS {
			return nil
		}
		if len(r.ProtoReflect().GetUnknown()) > 0 {
			return b.unsupported(prefix + "<unrecognised rule>")
		}
	}
	inner := &kw{indent: w.indent + 1}
	if err := b.elementRules(inner, subject, k, r, pathVar, prefix, false); err != nil {
		return err
	}
	if inner.sb.Len() == 0 {
		return nil
	}
	if r != nil && r.GetIgnore() == validate.Ignore_IGNORE_IF_ZERO_VALUE && !k.isMessage() {
		w.line("if (!(%s)) {", k.zeroCheck(subject))
		w.sb.WriteString(inner.sb.String())
		w.line("}")
		return nil
	}
	w.line("run {")
	w.sb.WriteString(inner.sb.String())
	w.line("}")
	return nil
}

// ---------------------------------------------------------------- per-element rules

func (b *validationBuilder) elementRules(w *kw, v string, k kind, r *validate.FieldRules, p, prefix string, top bool) error {
	name := typeRuleName(r)
	if name != "" && name != k.ruleName {
		switch name {
		case "any", "field_mask":
			return b.unsupported(prefix + name)
		}
		return b.fail("rule %s%s does not apply to this field type", prefix, name)
	}
	if name != "" {
		rm := ruleMsg(r, name)
		if err := b.typeRules(w, v, k, rm, p, prefix+name+"."); err != nil {
			return err
		}
	}
	if k.recursable() {
		w.line("%s.validateInto(out, %s)", v, p)
	}
	return nil
}

func (b *validationBuilder) typeRules(w *kw, v string, k kind, rm protoreflect.Message, p, prefix string) error {
	known := map[string]bool{"example": true}
	set := map[string]protoreflect.Value{}
	desc := map[string]protoreflect.FieldDescriptor{}
	var names []string
	rm.Range(func(fd protoreflect.FieldDescriptor, val protoreflect.Value) bool {
		n := string(fd.Name())
		set[n] = val
		desc[n] = fd
		names = append(names, n)
		return true
	})
	sort.Slice(names, func(i, j int) bool { return desc[names[i]].Number() < desc[names[j]].Number() })
	if len(rm.GetUnknown()) > 0 {
		return b.unsupported(prefix + "<unrecognised rule>")
	}
	switch {
	case k.isNumeric():
		for _, n := range []string{"const", "lt", "lte", "gt", "gte", "in", "not_in"} {
			known[n] = true
		}
		if k.isFloat() {
			known["finite"] = true
		}
	case k.pt == pgs.BoolT && k.enum == nil && !k.isMessage():
		known["const"] = true
	case k.pt == pgs.StringT && !k.isMessage():
		for _, n := range []string{"const", "len", "min_len", "max_len", "len_bytes", "min_bytes", "max_bytes", "pattern", "prefix", "suffix", "contains", "not_contains", "in", "not_in"} {
			known[n] = true
		}
	case k.pt == pgs.BytesT && !k.isMessage():
		for _, n := range []string{"const", "len", "min_len", "max_len", "prefix", "suffix", "contains", "in", "not_in"} {
			known[n] = true
		}
	case k.enum != nil:
		for _, n := range []string{"const", "defined_only", "in", "not_in"} {
			known[n] = true
		}
	case k.ruleName == "duration":
		for _, n := range []string{"const", "lt", "lte", "gt", "gte"} {
			known[n] = true
		}
	case k.ruleName == "timestamp":
		for _, n := range []string{"const", "lt", "lte", "gt", "gte", "lt_now", "gt_now", "within"} {
			known[n] = true
		}
	}
	for _, n := range names {
		if !known[n] {
			return b.unsupported(prefix + n)
		}
	}
	viol := func(id, msg string) string {
		return fmt.Sprintf("out.add(Violation(%s, %s, %s))", p, ktString(id), ktString(msg))
	}
	ruleID := k.ruleName
	id := func(n string) string { return ruleID + "." + n }
	test := func(c, n, msg string) { w.line("if (%s) %s", c, viol(id(n), msg)) }

	switch {
	case k.isNumeric():
		if val, ok := set["const"]; ok {
			test(fmt.Sprintf("%s != %s", v, k.numLit(val)), "const", "must equal "+numText(k, val))
		}
		if err := b.numericRange(w, v, k, set, p, ruleID); err != nil {
			return err
		}
		if val, ok := set["in"]; ok && val.List().Len() > 0 {
			test(fmt.Sprintf("!%s.any { it == %s }", listExpr(k, val), v), "in", "must be in list")
		}
		if val, ok := set["not_in"]; ok && val.List().Len() > 0 {
			test(fmt.Sprintf("%s.any { it == %s }", listExpr(k, val), v), "not_in", "must not be in list")
		}
		if val, ok := set["finite"]; ok && val.Bool() {
			test(fmt.Sprintf("%s.isNaN() || %s.isInfinite()", v, v), "finite", "must be finite")
		}
	case k.pt == pgs.BoolT && k.enum == nil && !k.isMessage():
		if val, ok := set["const"]; ok {
			test(fmt.Sprintf("%s != %v", v, val.Bool()), "const", fmt.Sprintf("must equal %v", val.Bool()))
		}
	case k.pt == pgs.StringT:
		b.stringRules(w, v, set, test)
		if val, ok := set["pattern"]; ok {
			re, err := compileRE2ToKotlin(val.String())
			if err != nil {
				return b.unsupported(prefix + "pattern (" + err.Error() + ")")
			}
			b.tmp++
			ident := fmt.Sprintf("ktbufValidateRegex%d", b.tmp)
			b.regexes = append(b.regexes, fmt.Sprintf("private val %s = Regex(%s)", ident, ktString(re)))
			test(fmt.Sprintf("!%s.containsMatchIn(%s)", ident, v), "pattern", "does not match regex pattern `"+val.String()+"`")
		}
	case k.pt == pgs.BytesT:
		b.bytesRules(w, v, set, test)
	case k.enum != nil:
		if val, ok := set["const"]; ok {
			test(fmt.Sprintf("%s.value != %d", v, val.Int()), "const", fmt.Sprintf("must equal %d", val.Int()))
		}
		if val, ok := set["defined_only"]; ok && val.Bool() {
			test(fmt.Sprintf("%s is %s.UNKNOWN_", v, importableTypeName(k.enum)), "defined_only", "must be defined")
		}
		if val, ok := set["in"]; ok && val.List().Len() > 0 {
			test(fmt.Sprintf("%s.value !in %s", v, intListExpr(val)), "in", "must be in list")
		}
		if val, ok := set["not_in"]; ok && val.List().Len() > 0 {
			test(fmt.Sprintf("%s.value in %s", v, intListExpr(val)), "not_in", "must not be in list")
		}
	case k.isTime():
		return b.timeRules(w, v, k, set, p)
	}
	return nil
}

func intListExpr(val protoreflect.Value) string {
	var parts []string
	l := val.List()
	for i := 0; i < l.Len(); i++ {
		parts = append(parts, strconv.FormatInt(l.Get(i).Int(), 10))
	}
	return "listOf(" + strings.Join(parts, ", ") + ")"
}

func listExpr(k kind, val protoreflect.Value) string {
	var parts []string
	l := val.List()
	for i := 0; i < l.Len(); i++ {
		parts = append(parts, k.numLit(l.Get(i)))
	}
	typ := map[pgs.ProtoType]string{}
	_ = typ
	return "listOf(" + strings.Join(parts, ", ") + ")"
}

func numText(k kind, v protoreflect.Value) string {
	switch {
	case k.isFloat():
		return strconv.FormatFloat(v.Float(), 'g', -1, 64)
	case k.isUnsigned():
		return strconv.FormatUint(v.Uint(), 10)
	}
	return strconv.FormatInt(v.Int(), 10)
}

// ---------------------------------------------------------------- string / bytes

func (b *validationBuilder) stringRules(w *kw, v string, set map[string]protoreflect.Value, test func(c, n, msg string)) {
	if val, ok := set["const"]; ok {
		test(fmt.Sprintf("%s != %s", v, ktString(val.String())), "const", "must equal `"+val.String()+"`")
	}
	if val, ok := set["len"]; ok {
		test(fmt.Sprintf("%s.codePointLength().toULong() != %duL", v, val.Uint()), "len", fmt.Sprintf("must be %d characters", val.Uint()))
	}
	if val, ok := set["min_len"]; ok {
		test(fmt.Sprintf("%s.codePointLength().toULong() < %duL", v, val.Uint()), "min_len", fmt.Sprintf("must be at least %d characters", val.Uint()))
	}
	if val, ok := set["max_len"]; ok {
		test(fmt.Sprintf("%s.codePointLength().toULong() > %duL", v, val.Uint()), "max_len", fmt.Sprintf("must be at most %d characters", val.Uint()))
	}
	if val, ok := set["len_bytes"]; ok {
		test(fmt.Sprintf("%s.utf8ByteLength().toULong() != %duL", v, val.Uint()), "len_bytes", fmt.Sprintf("must be %d bytes", val.Uint()))
	}
	if val, ok := set["min_bytes"]; ok {
		test(fmt.Sprintf("%s.utf8ByteLength().toULong() < %duL", v, val.Uint()), "min_bytes", fmt.Sprintf("must be at least %d bytes", val.Uint()))
	}
	if val, ok := set["max_bytes"]; ok {
		test(fmt.Sprintf("%s.utf8ByteLength().toULong() > %duL", v, val.Uint()), "max_bytes", fmt.Sprintf("must be at most %d bytes", val.Uint()))
	}
	if val, ok := set["prefix"]; ok {
		test(fmt.Sprintf("!%s.startsWith(%s)", v, ktString(val.String())), "prefix", "does not have prefix `"+val.String()+"`")
	}
	if val, ok := set["suffix"]; ok {
		test(fmt.Sprintf("!%s.endsWith(%s)", v, ktString(val.String())), "suffix", "does not have suffix `"+val.String()+"`")
	}
	if val, ok := set["contains"]; ok {
		test(fmt.Sprintf("!%s.contains(%s)", v, ktString(val.String())), "contains", "does not contain substring `"+val.String()+"`")
	}
	if val, ok := set["not_contains"]; ok {
		test(fmt.Sprintf("%s.contains(%s)", v, ktString(val.String())), "not_contains", "contains substring `"+val.String()+"`")
	}
	strList := func(val protoreflect.Value) string {
		var parts []string
		l := val.List()
		for i := 0; i < l.Len(); i++ {
			parts = append(parts, ktString(l.Get(i).String()))
		}
		return "listOf(" + strings.Join(parts, ", ") + ")"
	}
	if val, ok := set["in"]; ok && val.List().Len() > 0 {
		test(fmt.Sprintf("%s !in %s", v, strList(val)), "in", "must be in list")
	}
	if val, ok := set["not_in"]; ok && val.List().Len() > 0 {
		test(fmt.Sprintf("%s in %s", v, strList(val)), "not_in", "must not be in list")
	}
}

func (b *validationBuilder) bytesRules(w *kw, v string, set map[string]protoreflect.Value, test func(c, n, msg string)) {
	hex := func(x []byte) string { return fmt.Sprintf("%x", x) }
	if val, ok := set["const"]; ok {
		test(fmt.Sprintf("!%s.contentEquals(%s)", v, ktBytes(val.Bytes())), "const", "must be "+hex(val.Bytes()))
	}
	if val, ok := set["len"]; ok {
		test(fmt.Sprintf("%s.size.toULong() != %duL", v, val.Uint()), "len", fmt.Sprintf("must be %d bytes", val.Uint()))
	}
	if val, ok := set["min_len"]; ok {
		test(fmt.Sprintf("%s.size.toULong() < %duL", v, val.Uint()), "min_len", fmt.Sprintf("must be at least %d bytes", val.Uint()))
	}
	if val, ok := set["max_len"]; ok {
		test(fmt.Sprintf("%s.size.toULong() > %duL", v, val.Uint()), "max_len", fmt.Sprintf("must be at most %d bytes", val.Uint()))
	}
	if val, ok := set["prefix"]; ok {
		test(fmt.Sprintf("!%s.startsWithBytes(%s)", v, ktBytes(val.Bytes())), "prefix", "does not have prefix "+hex(val.Bytes()))
	}
	if val, ok := set["suffix"]; ok {
		test(fmt.Sprintf("!%s.endsWithBytes(%s)", v, ktBytes(val.Bytes())), "suffix", "does not have suffix "+hex(val.Bytes()))
	}
	if val, ok := set["contains"]; ok {
		test(fmt.Sprintf("!%s.containsBytes(%s)", v, ktBytes(val.Bytes())), "contains", "does not contain "+hex(val.Bytes()))
	}
	byteList := func(val protoreflect.Value) string {
		var parts []string
		l := val.List()
		for i := 0; i < l.Len(); i++ {
			parts = append(parts, ktBytes(l.Get(i).Bytes()))
		}
		return "listOf<ByteArray>(" + strings.Join(parts, ", ") + ")"
	}
	if val, ok := set["in"]; ok && val.List().Len() > 0 {
		test(fmt.Sprintf("%s.none { it.contentEquals(%s) }", byteList(val), v), "in", "must be in list")
	}
	if val, ok := set["not_in"]; ok && val.List().Len() > 0 {
		test(fmt.Sprintf("%s.any { it.contentEquals(%s) }", byteList(val), v), "not_in", "must not be in list")
	}
}

// ---------------------------------------------------------------- ranges

type bound struct {
	op   string // lt lte gt gte
	lit  string
	text string
	// sortable value
	f    float64
	i    int64
	u    uint64
	s, n int64
}

func (a bound) geq(c bound, k kind) bool {
	switch {
	case k.isTime():
		if a.s != c.s {
			return a.s > c.s
		}
		return a.n >= c.n
	case k.isFloat():
		return a.f >= c.f
	case k.isUnsigned():
		return a.u >= c.u
	}
	return a.i >= c.i
}

func (b *validationBuilder) mkBound(k kind, op string, val protoreflect.Value) bound {
	bd := bound{op: op}
	switch {
	case k.isTime():
		m := val.Message()
		fs := m.Descriptor().Fields()
		bd.s = m.Get(fs.ByName("seconds")).Int()
		bd.n = m.Get(fs.ByName("nanos")).Int()
		bd.lit = fmt.Sprintf("%dL, %d", bd.s, bd.n)
		bd.text = fmt.Sprintf("%d.%09ds", bd.s, bd.n)
	case k.isFloat():
		bd.f = val.Float()
		bd.lit = k.numLit(val)
		bd.text = numText(k, val)
	case k.isUnsigned():
		bd.u = val.Uint()
		bd.lit = k.numLit(val)
		bd.text = numText(k, val)
	default:
		bd.i = val.Int()
		bd.lit = k.numLit(val)
		bd.text = numText(k, val)
	}
	return bd
}

func (b *validationBuilder) numericRange(w *kw, v string, k kind, set map[string]protoreflect.Value, p, ruleID string) error {
	return b.rangeChecks(w, v, k, set, p, ruleID)
}

func (b *validationBuilder) timeRules(w *kw, v string, k kind, set map[string]protoreflect.Value, p string) error {
	ruleID := k.ruleName
	viol := func(id, msg string) string {
		return fmt.Sprintf("out.add(Violation(%s, %s, %s))", p, ktString(ruleID+"."+id), ktString(msg))
	}
	if val, ok := set["const"]; ok {
		bd := b.mkBound(k, "", val)
		w.line("if (compareTime(%s.seconds, %s.nanos, %s) != 0) %s", v, v, bd.lit, viol("const", "must equal "+bd.text))
	}
	if err := b.rangeChecks(w, v, k, set, p, ruleID); err != nil {
		return err
	}
	if val, ok := set["lt_now"]; ok && val.Bool() {
		w.line("if (isAfterNow(%s.seconds, %s.nanos)) %s", v, v, viol("lt_now", "must be less than now"))
	}
	if val, ok := set["gt_now"]; ok && val.Bool() {
		w.line("if (isBeforeNow(%s.seconds, %s.nanos)) %s", v, v, viol("gt_now", "must be greater than now"))
	}
	if val, ok := set["within"]; ok {
		bd := b.mkBound(kind{ruleName: "duration"}, "", val)
		w.line("if (!isWithinNow(%s.seconds, %s.nanos, %s)) %s", v, v, bd.lit, viol("within", "must be within "+bd.text+" of now"))
	}
	return nil
}

func (b *validationBuilder) rangeChecks(w *kw, v string, k kind, set map[string]protoreflect.Value, p, ruleID string) error {
	var up, lo *bound
	for _, op := range []string{"lt", "lte"} {
		if val, ok := set[op]; ok {
			bd := b.mkBound(k, op, val)
			up = &bd
		}
	}
	for _, op := range []string{"gt", "gte"} {
		if val, ok := set[op]; ok {
			bd := b.mkBound(k, op, val)
			lo = &bd
		}
	}
	if up == nil && lo == nil {
		return nil
	}
	cmp := func(op string, bd bound) string {
		if k.isTime() {
			return fmt.Sprintf("compareTime(%s.seconds, %s.nanos, %s) %s 0", v, v, bd.lit, op)
		}
		return fmt.Sprintf("%s %s %s", v, op, bd.lit)
	}
	upFail := func() string {
		if up.op == "lt" {
			return cmp(">=", *up)
		}
		return cmp(">", *up)
	}
	loFail := func() string {
		if lo.op == "gt" {
			return cmp("<=", *lo)
		}
		return cmp("<", *lo)
	}
	nan := ""
	if k.isFloat() {
		nan = v + ".isNaN() || "
	}
	text := func() string {
		desc := map[string]string{"lt": "less than", "lte": "less than or equal to", "gt": "greater than", "gte": "greater than or equal to"}
		switch {
		case up != nil && lo == nil:
			return "must be " + desc[up.op] + " " + up.text
		case lo != nil && up == nil:
			return "must be " + desc[lo.op] + " " + lo.text
		}
		return "must be " + desc[lo.op] + " " + lo.text + " and " + desc[up.op] + " " + up.text
	}
	var id, cond string
	switch {
	case up != nil && lo == nil:
		id, cond = ruleID+"."+up.op, nan+upFail()
	case lo != nil && up == nil:
		id, cond = ruleID+"."+lo.op, nan+loFail()
	default:
		id = ruleID + "." + lo.op + "_" + up.op
		if up.geq(*lo, k) {
			cond = nan + upFail() + " || " + loFail()
		} else {
			id += "_exclusive"
			cond = nan + "(" + upFail() + " && " + loFail() + ")"
		}
	}
	msg := text()
	if strings.HasSuffix(id, "_exclusive") {
		msg = strings.Replace(msg, " and ", " or ", 1)
	}
	w.line("if (%s) out.add(Violation(%s, %s, %s))", cond, p, ktString(id), ktString(msg))
	return nil
}
