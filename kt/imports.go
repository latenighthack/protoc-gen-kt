package kt

import pgs "github.com/lyft/protoc-gen-star"

// Extension functions live in the declaration package, including nested types.
// Service signatures must also bring their input/output codecs into scope.
func PackageImports(f pgs.File) []string {
	var imports []string
	add := func(entity pgs.Entity) {
		name := PackageName(entity).String()
		if name != PackageName(f).String() {
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
	for _, service := range f.Services() {
		for _, method := range service.Methods() {
			add(method.Input())
			add(method.Output())
		}
	}
	return unique(imports)
}
func Imports(f pgs.File) []string        { return PackageImports(f) }
func BuilderImports(f pgs.File) []string { return PackageImports(f) }
