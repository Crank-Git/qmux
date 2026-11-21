package main

import (
	"reflect"
	"testing"
)

// goTestBench is the output of `go test -run=^$ -bench=Dispatch -benchmem ./pkg/server/`
// on the development host, with one ns/op value changed to a decimal.
const goTestBench = `goos: darwin
goarch: arm64
pkg: github.com/Crank-Git/qmux/pkg/server
cpu: Apple M4
BenchmarkDispatchByClass-10    	  925330	      1279 ns/op	    1176 B/op	      19 allocs/op
BenchmarkDispatchByLabel-10    	  925588	      1321.5 ns/op	    1184 B/op	      19 allocs/op
PASS
ok  	github.com/Crank-Git/qmux/pkg/server	2.415s
`

func TestParseDispatchReadsTheNameTheNsPerOpAndTheAllocsPerOp(t *testing.T) {
	got, err := parseDispatch([]byte(goTestBench))
	if err != nil {
		t.Fatalf("parseDispatch: %v", err)
	}
	want := []Dispatch{
		{Name: "BenchmarkDispatchByClass", NsPerOp: 1279, AllocsPerOp: 19},
		{Name: "BenchmarkDispatchByLabel", NsPerOp: 1321.5, AllocsPerOp: 19},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("dispatch = %+v, want %+v", got, want)
	}
}

func TestParseDispatchRejectsOutputWithoutBothMicrobenchmarks(t *testing.T) {
	if _, err := parseDispatch([]byte("PASS\nok\n")); err == nil {
		t.Error("parseDispatch returned no error for output with no benchmark line")
	}
}
