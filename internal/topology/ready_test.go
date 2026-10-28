package topology

import (
	"reflect"
	"strings"
	"testing"
)

var wantAddrs = map[Role]string{
	NodeA:  "127.0.0.1:15003",
	NodeB:  "127.0.0.1:15013",
	Global: "127.0.0.1:15023",
}

func status(a, b []string, globalAddr string, withGlobal bool) DistributionStatus {
	st := DistributionStatus{GlobalAddr: globalAddr, Nodes: []NodeStatus{
		{ID: "a", Address: wantAddrs[NodeA], Coalitions: a},
		{ID: "b", Address: wantAddrs[NodeB], Coalitions: b},
	}}
	if withGlobal {
		st.Nodes = append(st.Nodes, NodeStatus{ID: "g", Address: wantAddrs[Global], IsGlobal: true})
	}
	return st
}

func TestDistributedReady(t *testing.T) {
	co := []string{"Blue", "Red"}
	g := wantAddrs[Global]
	tests := []struct {
		name string
		st   DistributionStatus
		want string // substring of the error, "" for nil
	}{
		{"ok", status([]string{"Red"}, []string{"Blue"}, g, true), ""},
		{"empty-b", status([]string{"Blue", "Red"}, nil, g, true), "non-empty"},
		{"overlap", status([]string{"Blue", "Red"}, []string{"Blue"}, g, true), "disjoint"},
		{"global-missing", status([]string{"Red"}, []string{"Blue"}, g, false), "global node"},
		{"wanted-nonglobal-reported-global", func() DistributionStatus {
			st := status([]string{"Red"}, []string{"Blue"}, g, true)
			st.Nodes[0].IsGlobal = true
			return st
		}(), "reported global"},
		{"union-mismatch", status([]string{"Red"}, []string{"Green"}, g, true), "union"},
		{"nodeA-missing", DistributionStatus{GlobalAddr: g, Nodes: status(nil, []string{"Blue"}, g, true).Nodes[1:]}, "not present"},
		{"nodeB-missing", DistributionStatus{GlobalAddr: g, Nodes: append(status([]string{"Red"}, nil, g, true).Nodes[:1], status(nil, nil, g, true).Nodes[2])}, "not present"},
		{"globalAddr-mismatch", status([]string{"Red"}, []string{"Blue"}, "127.0.0.1:1", true), "globalAddr"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := DistributedReady(tc.st, wantAddrs, co)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("want nil, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestDistributedReadyPartialWant(t *testing.T) {
	st := status([]string{"Red"}, []string{"Blue"}, wantAddrs[Global], true)
	partial := map[Role]string{NodeA: wantAddrs[NodeA], Global: wantAddrs[Global]}
	err := DistributedReady(st, partial, []string{"Blue", "Red"})
	if err == nil || !strings.Contains(err.Error(), "want must name") {
		t.Fatalf("want error containing %q, got %v", "want must name", err)
	}
}

func TestCoalitionOwner(t *testing.T) {
	st := status([]string{"Red"}, []string{"Blue"}, wantAddrs[Global], true)
	want := map[string]Role{"Red": NodeA, "Blue": NodeB}
	if got := CoalitionOwner(st, wantAddrs); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
