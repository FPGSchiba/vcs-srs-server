package main

import (
	"strings"
	"testing"

	"github.com/FPGSchiba/vcs-srs-server/tools/benchsite/schema"
)

func TestChartSVGEscapesInterpolatedNames(t *testing.T) {
	out := chartSVG(schema.Run{}, `sc"><script>x</script>`, `a<b>&"`)
	if strings.Contains(out, "<script>") || strings.Contains(out, "a<b>") {
		t.Fatalf("unescaped name in SVG: %s", out)
	}
}
