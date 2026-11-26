package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
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
		t.Skip("Kotlin consumer compilation needs KTBUF_TEST_CLASSPATHS")
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
		if strings.HasSuffix(name, ".kt") {
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
		"names":            {"case.proto": header + `message Names { int32 writer=1; int32 reader=2; int32 unknown_fields=3; int32 builder_copy=6; string proto_name=7; oneof when { string is=4; int32 class=5; } }`},
		"nested":           {"case.proto": header + `message Parent { message inner { int32 id=1; } inner value=1; enum E { zero=0; one=1; } E e=2; }`},
		"builtin_names":    {"case.proto": header + `message String { string value=1; } message ByteArray { bytes value=1; } message List { repeated String values=1; }`},
		"package_keywords": {"case.proto": `syntax="proto3"; package regression.when; message Value { int32 id=1; }`},
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

func TestMapFieldsRoundTripDefaultsDuplicatesAndBuilders(t *testing.T) {
	files, log, err := generate(t, map[string]string{"maps.proto": testHeader + `enum Kind { ZERO=0; ONE=1; } message Value { int32 id=1; string text=2; } message Maps { map<string,int32> ints=1; map<int32,Value> messages=2; map<bool,Kind> enums=3; map<string,bytes> blobs=4; }`})
	if err != nil {
		t.Fatal(log)
	}
	runKotlin(t, files, `import regression.*
fun main() {
    val value=Maps(ints=mapOf("a" to 7),messages=mapOf(1 to Value(9,"ok")),enums=mapOf(true to Kind.Companion.ONE),blobs=mapOf("b" to byteArrayOf(1,2)))
    val decoded=Maps.fromByteArray(value.toByteArray())
    check(value==decoded && value.hashCode()==decoded.hashCode())
    val defaults=Maps.fromByteArray(byteArrayOf(10,0,18,0,26,0,34,0))
    check(defaults.ints==mapOf("" to 0) && defaults.messages==mapOf(0 to Value()) && defaults.enums==mapOf(false to Kind.Companion.ZERO))
    check(defaults.blobs[""]!!.isEmpty())
    val duplicate=Maps(ints=mapOf("a" to 1)).toByteArray()+Maps(ints=mapOf("a" to 2)).toByteArray()
    check(Maps.fromByteArray(duplicate).ints["a"]==2)
    val built=Maps { ints { "a" to 3 }; messages { "".length to Value(4) }; enums { false to Kind.Companion.ONE }; blobs { "b" to byteArrayOf(5) } }
    check(built.ints["a"]==3 && built.messages[0]?.id==4)
}`)
}

func TestDuplicateEmbeddedMessagesMergeByPresence(t *testing.T) {
	files, log, err := generate(t, map[string]string{"merge.proto": testHeader + `message Child { int32 id=1; string text=2; repeated int32 values=3; } message Merge { Child child=1; oneof choice { Child selected=2; int32 scalar=3; } map<string,Child> children=4; }`})
	if err != nil {
		t.Fatal(log)
	}
	runKotlin(t, files, `import regression.*
fun main() {
    val first=Merge(child=Child(7,"keep",listOf(1)),choice=Merge.OneOfChoice.selected(Child(8,"stay"))).toByteArray()
    val second=Merge(child=Child(text="updated",values=listOf(2)),choice=Merge.OneOfChoice.selected(Child(id=9))).toByteArray()
    val merged=Merge.fromByteArray(first+second)
    check(merged.child==Child(7,"updated",listOf(1,2)))
    check(merged.choice?.getSelected()==Child(9,"stay"))
    // An explicitly present scalar zero clears the previous nonzero value.
    val zero=byteArrayOf(10,2,8,0)
    check(Merge.fromByteArray(first+zero).child?.id==0)
    val scalar=Merge(choice=Merge.OneOfChoice.scalar(4)).toByteArray()
    check(Merge.fromByteArray(first+scalar+second).choice?.getSelected()==Child(9))
    val entry="220d1207080712036f6c6412020800".chunked(2).map { it.toInt(16).toByte() }.toByteArray()
    check(Merge.fromByteArray(entry).children[""]==Child(text="old"))
}`)
}

func TestGeneratedParsingAllocationIsLinear(t *testing.T) {
	files, log, err := generate(t, map[string]string{"resources.proto": testHeader + `message Resources { repeated int32 values=1; Resources nested=2; }`})
	if err != nil {
		t.Fatal(log)
	}
	runKotlin(t, files, `import regression.*
import java.lang.management.ManagementFactory
import com.latenighthack.ktbuf.*
fun main() {
    val bean=ManagementFactory.getThreadMXBean() as com.sun.management.ThreadMXBean
    check(bean.isThreadAllocatedMemorySupported)
    bean.isThreadAllocatedMemoryEnabled=true
    val id=Thread.currentThread().id
    Resources.fromByteArray(Resources(values=listOf(1,2)).toByteArray())
    val n=20000
    val encoded=Resources(values=List(n) { 1 }).toByteArray()
    var before=bean.getThreadAllocatedBytes(id)
    check(Resources.fromByteArray(encoded).values.size==n)
    check(bean.getThreadAllocatedBytes(id)-before < n*512L+2000000L)
    val unknown=ByteArray(30000) { when(it%3) {0->0x98.toByte();1->6;else->1} }
    before=bean.getThreadAllocatedBytes(id)
    check(Resources.fromByteArray(unknown).unknownFields!!.size==unknown.size)
    check(bean.getThreadAllocatedBytes(id)-before < 10000*1024L+2000000L)
    var deep=byteArrayOf()
    repeat(1000) { val child=deep; deep=ProtobufOutputStream.encode { encode(2) { encodeRaw(child) } } }
    try { Resources.fromByteArray(deep); error("missing depth limit") } catch(expected:ProtobufLimitException) {}
    try { Resources.parseFrom(encoded,ProtobufLimits(maxWork=5)); error("missing value work limit") } catch(expected:ProtobufLimitException) {}
    val piece=Resources(nested=Resources(values=List(20) {1})).toByteArray()
    val copies=ByteArray(piece.size*100) { piece[it%piece.size] }
    try { Resources.parseFrom(copies,ProtobufLimits(maxWork=10000)); error("missing merge-copy work limit") } catch(expected:ProtobufLimitException) {}
    try { Resources.parseFrom(encoded,ProtobufLimits(maxMessageBytes=10)); error("missing byte limit") } catch(expected:ProtobufLimitException) {}
}`)
}

func TestValueProjectionKeepsOneofsMapsAndBytes(t *testing.T) {
	files, log, err := generate(t, map[string]string{"values.proto": testHeader + `message Child { int32 id=1; } enum Kind { ZERO=0; ONE=1; } message Values { bytes raw=1; repeated bytes chunks=2; map<string,bytes> blobs=3; map<int32,Child> children=4; repeated Kind kinds=5; oneof choice { bytes selected=6; Child child=7; int32 count=8; Kind kind=9; } }`})
	if err != nil {
		t.Fatal(log)
	}
	runKotlin(t, files, `import regression.*
fun main() {
    val value=Values(raw=byteArrayOf(-1),chunks=listOf(byteArrayOf(-1)),blobs=mapOf("a" to byteArrayOf(-1)),children=mapOf(1 to Child(2)),kinds=listOf(Kind.Companion.ONE),choice=Values.OneOfChoice.selected(byteArrayOf(-1)))
    val projected=value.toValue()
    check(projected["raw"]=="_w" && projected["chunks"]==listOf("_w"))
    check(projected["blobs"]==mapOf("a" to "_w"))
    check((projected["children"] as Map<*,*>)["1"]==(Child(2).toValue()))
    check(projected["kinds"]==listOf("ONE"))
    check(projected["choice"]==mapOf("selected" to "_w"))
    check(Values(choice=Values.OneOfChoice.child(Child(3))).toValue()["choice"]==mapOf("child" to Child(3).toValue()))
    check(Values(choice=Values.OneOfChoice.count(0)).toValue()["choice"]==mapOf("count" to 0))
    check(Values(choice=Values.OneOfChoice.kind(Kind.Companion.ONE)).toValue()["choice"]==mapOf("kind" to "ONE"))
    check(Values().toValue()["choice"]==null)
    check(value.toString().contains("blobs="))
}`)
}

func TestAdditionalParameterMetadataKeepsItsExistingTypeName(t *testing.T) {
	files, log, err := generate(t, map[string]string{"metadata.proto": testHeader + `message Request { int32 id=1; } service Metadata { rpc Call(Request) returns (Request); }`})
	if err != nil {
		t.Fatal(log)
	}
	runKotlin(t, files, `import regression.*
import com.latenighthack.ktbuf.net.*
import kotlinx.coroutines.runBlocking
fun main() = runBlocking {
    var name=""
    val rpc=object:RpcClient {
        override suspend fun unaryCall(method:RpcMethodSpecifier,headers:Map<String,String>,request:ByteArray):RpcResponse {
            check(method.additionalParameters["type"]=="Request")
            return RpcResponse(Request(7).toByteArray(),emptyMap())
        }
        override suspend fun serverStreamingCall(method:RpcMethodSpecifier,block:suspend RpcServerStream.()->Unit,readyCallback:()->Unit) { error("unexpected") }
    }
    val response=MetadataServiceRpc(rpc) { type, _ -> name=type; mapOf("type" to type) }.call(Request())
    check(response.id==7 && name=="Request")
}`)
}

func TestClientStreamingUnaryServiceCompilesAndCollectsFiniteRequests(t *testing.T) {
	files, log, err := generate(t, map[string]string{"upload.proto": testHeader + `message Item { int32 id=1; } service Upload { rpc Collect(stream Item) returns (Item); }`})
	if err != nil {
		t.Fatal(log)
	}
	runKotlin(t, files, `import regression.*
import com.latenighthack.ktbuf.net.*
import kotlinx.coroutines.*
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.flow.*
fun main() = runBlocking {
    val rpc=object:RpcClient {
        override suspend fun unaryCall(method:RpcMethodSpecifier,headers:Map<String,String>,request:ByteArray):RpcResponse = error("wrong transport")
        override suspend fun serverStreamingCall(method:RpcMethodSpecifier,block:suspend RpcServerStream.()->Unit,readyCallback:()->Unit) {
            val response=Channel<ByteArray>(1)
            var sum=0
            object:RpcServerStream {
                override suspend fun receive()=response.receive()
                override suspend fun send(bytes:ByteArray) { sum+=Item.fromByteArray(bytes).id }
                override suspend fun closeOutbound() { response.send(Item(sum).toByteArray()); response.close() }
                override suspend fun closeInbound() { error("discarded response") }
            }.block()
        }
    }
    check(UploadServiceRpc(rpc).collect(flowOf(Item(1),Item(2),Item(3))).id==6)
    check(UploadServiceRpc(rpc).collect(emptyFlow()).id==0)
    check(UploadServer.Descriptor.methods.single().handler is ServerMethod.ClientStreaming)
    val local=LocalUploadServiceRpc(object:UploadServer {
        override suspend fun collect(context:GrpcRequestContext,request:Flow<Item>):Item=Item(request.toList().sumOf {it.id})
    })
    check(local.collect(flowOf(Item(4),Item(5))).id==9)
}`)
}

func TestWireServiceNamesRetainTheProtoIdentifier(t *testing.T) {
	files, log, err := generate(t, map[string]string{"wire.proto": testHeader + `message Request {} service lower_service { rpc lower_call(Request) returns (Request); }`})
	if err != nil {
		t.Fatal(log)
	}
	runKotlin(t, files, `import regression.*
import com.latenighthack.ktbuf.net.*
import kotlinx.coroutines.runBlocking
fun main() = runBlocking<Unit> {
    check(LowerServiceService.Descriptor.serviceName=="lower_service")
    check(LowerServiceServer.Descriptor.serviceName=="lower_service")
    val rpc=object:RpcClient {
        override suspend fun unaryCall(method:RpcMethodSpecifier,headers:Map<String,String>,request:ByteArray):RpcResponse {
            check(method.serviceName=="lower_service" && method.methodName=="lower_call")
            return RpcResponse(byteArrayOf(),emptyMap())
        }
        override suspend fun serverStreamingCall(method:RpcMethodSpecifier,block:suspend RpcServerStream.()->Unit,readyCallback:()->Unit) {error("unexpected")}
    }
    LowerServiceServiceRpc(rpc).lowerCall(Request())
}`)
}

func TestInvalidJavaPackageIsRejectedBeforeAnyOutput(t *testing.T) {
	for _, pkg := range []string{"../escaped", "safe;classInjected", "a.\nnew", "/absolute", "a..b", "a.`injected`"} {
		t.Run(pkg, func(t *testing.T) {
			schema := `syntax="proto3"; package regression; option java_package=` + strconv.Quote(pkg) + `; message Safe { int32 id=1; }`
			files, log, err := generate(t, map[string]string{"package.proto": schema})
			if err == nil || !strings.Contains(log, "invalid java_package") {
				t.Fatalf("expected explicit package rejection: %v %s", err, log)
			}
			if len(files) != 0 {
				t.Fatalf("invalid package emitted files: %v", files)
			}
		})
	}
}

func TestUnicodeAndQuotedJavaPackageCompiles(t *testing.T) {
	files, log, err := generate(t, map[string]string{"package.proto": `syntax="proto3"; package regression; option java_package="café.$pkg.when"; message Safe { int32 id=1; }`})
	if err != nil {
		t.Fatal(log)
	}
	runKotlin(t, files, "import café.`$pkg`.`when`.*\nfun main() {check(Safe.fromByteArray(Safe(7).toByteArray()).id==7)}")
}

func TestDeterministicEncodingSortsMapsAndOrdersFieldsByNumber(t *testing.T) {
	files, log, err := generate(t, map[string]string{"det.proto": testHeader + `message Inner { map<uint32,string> m=1; } message Det { string s=5; oneof pick { int32 x=3; Inner y=9; } int32 z=1; map<string,int32> sm=7; map<sint64,string> lm=8; map<uint64,string> um=10; map<bool,string> bm=11; Inner inner=4; }`})
	if err != nil {
		t.Fatal(log)
	}
	runKotlin(t, files, `import regression.*
import com.latenighthack.ktbuf.*
fun hex(b: ByteArray) = b.joinToString("") { (it.toInt() and 0xff).toString(16).padStart(2, '0') }
fun main() {
    val v = Det(s="s", pick=Det.OneOfPick.x(1), z=2, sm=linkedMapOf("😀" to 1, "｡" to 2, "a" to 3),
        lm=linkedMapOf(5L to "a", -1L to "b"), um=linkedMapOf(ULong.MAX_VALUE to "a", 1UL to "b"), bm=linkedMapOf(true to "t", false to "f"),
        inner=Inner(m=linkedMapOf(0x80000000u to "a", 1u to "b")))
    val det = v.toDeterministicByteArray()
    check(det.contentEquals(v.toByteArray(ProtobufEncodeOptions.Deterministic)))
    check(Det.fromByteArray(det) == v && Det.fromByteArray(v.toByteArray()) == v)
    // Field 1 (z) first, then oneof 3, inner 4 (with nested sorted map), s 5.
    check(hex(det).startsWith("0802" + "1801" + "22"))
    check(!det.contentEquals(v.toByteArray()))
    // Same logical value with different insertion order encodes identically only when deterministic.
    val w = v.copy(sm=linkedMapOf("a" to 3, "｡" to 2, "😀" to 1))
    check(det.contentEquals(w.toDeterministicByteArray()))
    check(!v.toByteArray().contentEquals(w.toByteArray()))
}`)
}

func TestWellKnownHelpersAreEmittedOnlyForTimestampAndDuration(t *testing.T) {
	files, log, err := generate(t, map[string]string{
		"google/protobuf/timestamp.proto": `syntax="proto3"; package google.protobuf; option java_package="com.google.protobuf"; message Timestamp { int64 seconds=1; int32 nanos=2; }`,
		"google/protobuf/duration.proto":  `syntax="proto3"; package google.protobuf; option java_package="com.google.protobuf"; message Duration { int64 seconds=1; int32 nanos=2; }`,
		"google/protobuf/empty.proto":     `syntax="proto3"; package google.protobuf; option java_package="com.google.protobuf"; message Empty {}`,
		"other/timestamp.proto":           `syntax="proto3"; package other; option java_package="other"; message Timestamp { int64 seconds=1; int32 nanos=2; }`,
	})
	if err != nil {
		t.Fatal(log)
	}
	var got []string
	for name := range files {
		if strings.HasSuffix(name, ".wkt.kt") {
			got = append(got, name)
		}
	}
	sort.Strings(got)
	want := []string{"com/google/protobuf/duration.wkt.kt", "com/google/protobuf/timestamp.wkt.kt"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("wkt outputs = %v, want %v", got, want)
	}
	if !strings.Contains(files[want[1]], "fun Timestamp.toEpochMillis()") || strings.Contains(files[want[1]], "Duration.isValid") {
		t.Fatalf("timestamp helpers wrong:\n%s", files[want[1]])
	}
	if !strings.Contains(files[want[0]], "fun kotlin.time.Duration.toProtoDuration()") || strings.Contains(files[want[0]], "toEpochMillis") {
		t.Fatalf("duration helpers wrong:\n%s", files[want[0]])
	}
	compileKotlin(t, files)
}
