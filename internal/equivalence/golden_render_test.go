package equivalence

import (
	"context"
	"testing"
)

func TestGoldenRenderMetalOperatorFromGit(t *testing.T) {
	docs, err := RenderGolden(context.Background(), GoldenRenderReq{
		RepoURL:   "https://github.com/sapcc/helm-charts.git",
		SHA:       "33e68278ace26fe23e0c2b36a9266a58c9b47707",
		ChartPath: "system/metal-operator-remote",
		Namespace: "shoot--cp--m-qa-de-1",
		ValuesYAML: [][]byte{
			[]byte("global:\n  region: qa-de-1\n"),
		},
	})
	if err != nil {
		t.Fatalf("golden render failed: %v", err)
	}
	if len(docs) == 0 {
		t.Fatal("expected rendered documents from metal-operator-remote")
	}
}
