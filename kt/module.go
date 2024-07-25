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
		"isBytes":                     IsBytes,
		"isBytesPT":                   IsBytesPT,
		"isPacked":                    IsPacked,
		"imports":                     Imports,
		"builderImports":              BuilderImports,
		"packageImports":              PackageImports,
		"default":                     DefaultValue,
	}
)

type TemplateRequiredCheck func(pgs.File) bool

type Module struct {
	pgs.ModuleBase

	excludes map[string]struct{}
	template pgs.Template
	ext      string
	check    TemplateRequiredCheck
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
	if len(f.Messages()) == 0 {
		return
	}

	if !m.check(f) {
		return
	}

	if _, ok := m.excludes[f.Descriptor().GetOptions().GetJavaPackage()]; ok {
		return
	}

	m.AddGeneratorTemplateFile(
		string(OutputPath(f).SetExt(m.ext)),
		m.template,
		f,
	)
}
