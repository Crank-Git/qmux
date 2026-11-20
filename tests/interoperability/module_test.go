package interoperability

import (
	"runtime/debug"
	"testing"

	// The harness must build against the root module in this checkout. This import
	// keeps the require in go.mod, so that the replace directive has an effect.
	_ "github.com/Crank-Git/qmux"
)

func TestRootModuleResolvesToTheLocalCheckoutThroughReplace(t *testing.T) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		t.Fatal("the test binary has no build information")
	}
	for _, dep := range info.Deps {
		if dep.Path != "github.com/Crank-Git/qmux" {
			continue
		}
		if dep.Replace == nil || dep.Replace.Path != "../../" {
			t.Fatalf("github.com/Crank-Git/qmux replace = %+v, want path ../../", dep.Replace)
		}
		return
	}
	t.Fatal("the build does not include github.com/Crank-Git/qmux")
}
