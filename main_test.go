package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

var testPlugin string

func TestMain(m *testing.M) {
	directory, err := os.MkdirTemp("", "kt-generator-tests-")
	if err != nil {
		panic(err)
	}
	testPlugin = filepath.Join(directory, "protoc-gen-kt")
	output, err := exec.Command("go", "build", "-o", testPlugin, ".").CombinedOutput()
	if err != nil {
		panic(string(output))
	}
	code := m.Run()
	os.RemoveAll(directory)
	os.Exit(code)
}
func generate(t *testing.T, files map[string]string) (map[string]string, string, error) {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	if err := os.Mkdir(out, 0700); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(files))
	for name, content := range files {
		target := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	args := append([]string{"-I" + dir, "--plugin=protoc-gen-kt=" + testPlugin, "--kt_out=" + out}, names...)
	command := exec.Command("protoc", args...)
	data, err := command.CombinedOutput()
	result := map[string]string{}
	walkErr := filepath.WalkDir(out, func(path string, entry os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if !entry.IsDir() {
			body, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			rel, _ := filepath.Rel(out, path)
			result[filepath.ToSlash(rel)] = string(body)
		}
		return nil
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}
	return result, string(data), err
}

// Consumers may opt into Kotlin compilation by supplying compiler/runtime
// classpaths in a JSON file. CI supplies this from its paired ktbuf checkout.
func compileKotlin(t *testing.T, files map[string]string) string {
	t.Helper()
	config := os.Getenv("KTBUF_TEST_CLASSPATHS")
	if config == "" {
		t.Log("Kotlin consumer compilation needs KTBUF_TEST_CLASSPATHS")
		return ""
	}
	data, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	var paths struct {
		Compiler string `json:"compiler"`
		Consumer string `json:"consumer"`
	}
	if err := json.Unmarshal(data, &paths); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	args := []string{"-cp", paths.Compiler, "org.jetbrains.kotlin.cli.jvm.K2JVMCompiler", "-no-stdlib", "-no-reflect", "-classpath", paths.Consumer, "-d", filepath.Join(dir, "classes")}
	names := make([]string, 0, len(files))
	for name := range files {
		if strings.HasSuffix(name, ".kt") && !strings.HasSuffix(name, ".server.kt") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(files[name]), 0600); err != nil {
			t.Fatal(err)
		}
		args = append(args, path)
	}
	java := os.Getenv("KTBUF_TEST_JAVA")
	if java == "" {
		java = "java"
	}
	if output, err := exec.Command(java, args...).CombinedOutput(); err != nil {
		t.Fatalf("generated consumer did not compile: %s", output)
	}
	return filepath.Join(dir, "classes")
}

func runKotlin(t *testing.T, files map[string]string, program string) {
	t.Helper()
	files["Regression.kt"] = program
	classes := compileKotlin(t, files)
	if classes == "" {
		return
	}
	data, err := os.ReadFile(os.Getenv("KTBUF_TEST_CLASSPATHS"))
	if err != nil {
		t.Fatal(err)
	}
	var paths struct {
		Consumer string `json:"consumer"`
	}
	if err := json.Unmarshal(data, &paths); err != nil {
		t.Fatal(err)
	}
	java := os.Getenv("KTBUF_TEST_JAVA")
	if java == "" {
		java = "java"
	}
	if output, err := exec.Command(java, "-cp", classes+string(os.PathListSeparator)+paths.Consumer, "RegressionKt").CombinedOutput(); err != nil {
		t.Fatalf("generated behavior regression failed: %s", output)
	}
}

const testHeader = `syntax="proto3"; package regression; option java_package="regression"; `

func TestDeclarationsWithoutMessages(t *testing.T) {
	files, log, err := generate(t, map[string]string{
		"enum.proto":    testHeader + `enum Standalone { ZERO=0; ONE=1; }`,
		"message.proto": testHeader + `message Request { int32 id=1; }`,
		"service.proto": testHeader + `import "message.proto"; service StandaloneService { rpc Call(Request) returns (Request); }`,
	})
	if err != nil {
		t.Fatal(log)
	}
	if !strings.Contains(files["regression/enum.data.kt"], "sealed class Standalone") {
		t.Fatal("enum-only file was not emitted")
	}
	if !strings.Contains(files["regression/service.service.kt"], "interface StandaloneServiceService") {
		t.Fatal("service-only file was not emitted")
	}
	if !strings.Contains(files["regression/service.server.kt"], "interface StandaloneServiceServer") {
		t.Fatal("server-only file was not emitted")
	}
	compileKotlin(t, files)
}

func TestUnsupportedPresenceSemanticsAreRejected(t *testing.T) {
	for _, schema := range []string{
		`syntax="proto2"; package regression; message Presence { required int32 id=1; optional int32 count=2 [default=7]; }`,
		testHeader + `message Presence { optional int32 count=1; }`,
	} {
		files, log, err := generate(t, map[string]string{"presence.proto": schema})
		if err == nil || !strings.Contains(log, "unsupported presence semantics") || len(files) != 0 {
			t.Fatalf("expected explicit rejection without output, got err=%v files=%v log=%s", err, len(files), log)
		}
	}
}

func TestNegativeFloatingZeroSurvives(t *testing.T) {
	files, log, err := generate(t, map[string]string{"zero.proto": testHeader + `message Zero { float f=1; double d=2; }`})
	if err != nil {
		t.Fatal(log)
	}
	runKotlin(t, files, `import regression.*
fun main() {
    val value = Zero(f=-0.0f,d=-0.0)
    val decoded = Zero.fromByteArray(value.toByteArray())
    check(decoded.f.toRawBits()==value.f.toRawBits())
    check(decoded.d.toRawBits()==value.d.toRawBits())
    check(Zero().toByteArray().isEmpty())
}`)
}

func TestGeneratedEqualityAndHashContracts(t *testing.T) {
	files, log, err := generate(t, map[string]string{"equal.proto": testHeader + `enum First { ZERO=0; ONE=1; } enum Second { OTHER_ZERO=0; } message Equal { repeated bytes chunks=1; float f=2; double d=3; oneof choice { bytes payload=4; } }`})
	if err != nil {
		t.Fatal(log)
	}
	runKotlin(t, files, `import regression.*
fun main() {
    val a = Equal(chunks=listOf(byteArrayOf(1)),choice=Equal.OneOfChoice.payload(byteArrayOf(2)))
    val b = Equal(chunks=listOf(byteArrayOf(1)),choice=Equal.OneOfChoice.payload(byteArrayOf(2)))
    check(a==b && b==a && a.hashCode()==b.hashCode())
    check(setOf(a,b).size==1)
    val c=Equal(unknownFields=byteArrayOf(8,1)); val empty=Equal()
    check(!c.equals(empty) && !empty.equals(c))
    check(c==Equal(unknownFields=byteArrayOf(8,1)))
    val first: First = First.Companion.ZERO
    check(!first.equals("ZERO"))
    check(!first.equals(Second.Companion.OTHER_ZERO))
    check(first.equals(First.UNKNOWN_(0)))
    val zero=Equal(f=0.0f,d=0.0); val negative=Equal(f=-0.0f,d=-0.0)
    check(zero!=negative)
    val nan=Equal(f=Float.NaN,d=Double.NaN)
    check(nan==Equal(f=Float.NaN,d=Double.NaN))
    check(nan.hashCode()==Equal(f=Float.NaN,d=Double.NaN).hashCode())
}`)
}

func TestLegalSchemasCompile(t *testing.T) {
	header := `syntax="proto3"; package regression; option java_package="regression"; `
	cases := map[string]map[string]string{
		"names":            {"case.proto": header + `message Names { int32 writer=1; int32 reader=2; int32 unknown_fields=3; oneof when { string is=4; int32 class=5; } }`},
		"nested":           {"case.proto": header + `message Parent { message inner { int32 id=1; } inner value=1; enum E { zero=0; one=1; } E e=2; }`},
		"package_fallback": {"case.proto": `syntax="proto3"; package regression; message Value { int32 id=1; }`},
		"external":         {"base.proto": `syntax="proto3"; package regression.sub; option java_package="regression.sub"; message Outer { message Inner { int32 id=1; } }`, "case.proto": header + `import "base.proto"; message Value { regression.sub.Outer.Inner value=1; repeated regression.sub.Outer.Inner values=2; } service External { rpc Call(regression.sub.Outer.Inner) returns (regression.sub.Outer.Inner); }`},
	}
	for name, schema := range cases {
		t.Run(name, func(t *testing.T) {
			files, log, err := generate(t, schema)
			if err != nil {
				t.Fatal(log)
			}
			compileKotlin(t, files)
		})
	}
}

func TestKnownFieldsRejectMismatchedWireTypes(t *testing.T) {
	files, log, err := generate(t, map[string]string{"wire.proto": testHeader + `message Child { int32 value=1; } message Wire { int32 i=1; fixed32 f=2; fixed64 d=3; string s=4; Child child=5; oneof choice { bytes payload=6; int32 number=7; } repeated int32 r=8; }`})
	if err != nil {
		t.Fatal(log)
	}
	runKotlin(t, files, `import regression.*
fun main() {
    val malformed = byteArrayOf(10,1,9,16,9,24,9,32,9,40,9,48,9,58,1,9,64,9)
    val decoded = Wire.fromByteArray(malformed)
    check(decoded.i==0 && decoded.f==0U && decoded.d==0UL && decoded.s.isEmpty() && decoded.child==null && decoded.choice==null && decoded.r==listOf(9))
    check(decoded.unknownFields!=null)
    check(Wire.fromByteArray(decoded.toByteArray())==decoded)
    check(Wire.fromByteArray(Wire(i=9,f=10U,d=11UL,s="ok",child=Child(12),r=listOf(13)).toByteArray()).i==9)
}`)
}

func TestRepeatedScalarWireFormsAndPackedOption(t *testing.T) {
	files, log, err := generate(t, map[string]string{"repeat.proto": testHeader + `enum Kind { ZERO=0; ONE=1; } message Repeated { repeated int32 expanded=1 [packed=false]; repeated int32 packed=2; repeated fixed32 fixed=3 [packed=false]; repeated float floats=4 [packed=false]; repeated Kind kinds=5 [packed=false]; }`})
	if err != nil {
		t.Fatal(log)
	}
	runKotlin(t, files, `import regression.*
import com.latenighthack.ktbuf.*
fun main() {
    val value=Repeated(expanded=listOf(1,2),packed=listOf(3,4),fixed=listOf(5U,6U),floats=listOf(1.5f,-0.0f),kinds=listOf(Kind.Companion.ONE))
    check(Repeated.fromByteArray(value.toByteArray())==value)
    check(Repeated(expanded=listOf(1,2)).toByteArray().contentEquals(byteArrayOf(8,1,8,2)))
    check(Repeated(packed=listOf(1,2)).toByteArray().contentEquals(byteArrayOf(18,2,1,2)))
    val mixed=byteArrayOf(10,2,1,2,8,3,16,4,18,2,5,6)
    val decoded=Repeated.fromByteArray(mixed)
    check(decoded.expanded==listOf(1,2,3) && decoded.packed==listOf(4,5,6))
}`)
}
