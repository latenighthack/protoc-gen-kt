package kt

import (
	"bytes"
	"text/template"

	"github.com/Masterminds/sprig"
	pgs "github.com/lyft/protoc-gen-star"

	"latenighthack.com/protoc-gen-kt/internal/strutil"
)

var (
	defaultExcludes = map[string]struct{}{
		"io.envoyproxy.pgv.validate": {},
	}
)

var (
	templateFuncs = template.FuncMap{
		"builderName":                 BuilderName,
		"simpleBuilderName":           SimpleBuilderName,
		"stripLastSegment":            StripLastSegment,
		"simpleName":                  SimpleName,
		"originalName":                OriginalName,
		"qualifiedName":               QualifiedName,
		"fullyQualifiedName":          FullyQualifiedName,
		"fullyQualifiedCompanionName": FullyQualifiedCompanionName,
		"escapedFullyQualifiedName":   EscapedFullyQualifiedName,
		"package":                     PackageName,
		"protoPackage":                ProtoPackageName,
		"upperCamel":                  strutil.CamelCase,
		"upperCamelEnum":              UpperCamelCaseEnum,
		"lowerCamel":                  LowerCamelCase,
		"name":                        Name,
		"fieldTypeName":               FieldTypeName,
		"fieldTypeNameNonNull":        FieldTypeNameNonNull,
		"elTypeName":                  ElType,
		"type":                        Type,
		"typeNonNull":                 TypeNonNull,
		"elementTypeNonNull":          ElementTypeNonNull,
		"readerMethod":                ReaderMethod,
		"writerMethod":                WriterMethod,
		"isFloating": func(f pgs.Field) bool {
			return !f.Type().IsRepeated() && !f.Type().IsMap() && (f.Type().ProtoType() == pgs.FloatT || f.Type().ProtoType() == pgs.DoubleT)
		},
		"acceptedWireTypes": AcceptedWireTypes,
		"wireType":          WireType,
		"mapDefault":        MapDefault,
		"isBytes":           IsBytes,
		"isBytesPT":         IsBytesPT,
		"shouldPack":        ShouldPack,
		"isPacked":          IsPacked,
		"imports":           Imports,
		"builderImports":    BuilderImports,
		"packageImports":    PackageImports,
		"default":           DefaultValue,
		"validatePrelude":   ValidatePrelude,
		"validateBody":      ValidateBody,
	}
)

type TemplateRequiredCheck func(pgs.File) bool

type Module struct {
	pgs.ModuleBase

	excludes map[string]struct{}
	template pgs.Template
	ext      string
	check    TemplateRequiredCheck
	// prepare, when set, runs before a file is rendered; an error fails generation.
	prepare func(pgs.File) error
}

// WithPrepare registers a pre-render hook whose error fails generation.
func (m *Module) WithPrepare(f func(pgs.File) error) *Module {
	m.prepare = f
	return m
}

// NewValidate builds the optional buf.validate module.
func NewValidate(src, ext string) *Module {
	return New(src, ext, func(pgs.File) bool { return true }).WithPrepare(CheckValidation)
}

func New(src, ext string, check TemplateRequiredCheck) *Module {
	tmpl := template.New("root")

	funcMap := sprig.TxtFuncMap()
	for k, v := range templateFuncs {
		funcMap[k] = v
	}

	funcMap["include"] = func(name string, data interface{}) (string, error) {
		buf := bytes.NewBuffer(nil)
		if err := tmpl.ExecuteTemplate(buf, name, data); err != nil {
			return "", err
		}
		return buf.String(), nil
	}

	tmpl.Funcs(funcMap)

	return &Module{
		template: template.Must(tmpl.Parse(src)),
		ext:      ext,
		excludes: defaultExcludes,
		check:    check,
	}
}

func (m *Module) Name() string {
	return m.ext
}

func (m *Module) InitContext(ctx pgs.BuildContext) {
	m.ModuleBase.InitContext(ctx)
}

func (m *Module) Execute(targets map[string]pgs.File, _ map[string]pgs.Package) []pgs.Artifact {
	for _, t := range targets {
		m.generate(t)
	}

	return m.Artifacts()
}

func (m *Module) generate(f pgs.File) {
	if !m.check(f) {
		return
	}

	if _, ok := m.excludes[f.Descriptor().GetOptions().GetJavaPackage()]; ok {
		return
	}

	if m.prepare != nil {
		if err := m.prepare(f); err != nil {
			m.Fail(err.Error())
		}
	}

	m.AddGeneratorTemplateFile(
		string(OutputPath(f).SetExt(m.ext)),
		m.template,
		f,
	)
}
