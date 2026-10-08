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
