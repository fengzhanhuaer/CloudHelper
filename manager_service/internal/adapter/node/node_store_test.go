package node_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudhelper/manager_service/internal/adapter/node"
)

func newStore(t *testing.T) *node.Store {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return node.NewStore(dir)
}

func TestListEmpty(t *testing.T) {
	s := newStore(t)
	nodes, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("expected 0 nodes, got %d", len(nodes))
	}
}

func TestCreateAndList(t *testing.T) {
	s := newStore(t)
	n, err := s.Create("node-alpha")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if n.NodeName != "node-alpha" {
		t.Fatalf("unexpected node name: %s", n.NodeName)
	}
	if n.NodeNo != 1 {
		t.Fatalf("expected NodeNo=1, got %d", n.NodeNo)
	}
	if n.NodeSecret == "" {
		t.Fatal("expected non-empty NodeSecret")
	}
	nodes, _ := s.List()
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(nodes))
	}
}

func TestCreateDuplicateName(t *testing.T) {
	s := newStore(t)
	_, _ = s.Create("node-alpha")
	_, err := s.Create("node-alpha")
	if err == nil {
		t.Fatal("expected error for duplicate node name")
	}
}

func TestCreateIncrementNodeNo(t *testing.T) {
	s := newStore(t)
	n1, _ := s.Create("node-a")
	n2, _ := s.Create("node-b")
	if n2.NodeNo != n1.NodeNo+1 {
		t.Fatalf("expected NodeNo=%d, got %d", n1.NodeNo+1, n2.NodeNo)
	}
}

func TestUpdate(t *testing.T) {
	s := newStore(t)
	n, _ := s.Create("node-foo")
	updated, err := s.Update(n.NodeNo, node.UpdateSettings{
		NodeName:      "node-foo-renamed",
		TargetSystem:  "windows",
		DirectConnect: false,
		Remark:        "test remark",
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.NodeName != "node-foo-renamed" {
		t.Fatalf("expected node-foo-renamed, got %s", updated.NodeName)
	}
	if updated.TargetSystem != "windows" {
		t.Fatalf("expected windows, got %s", updated.TargetSystem)
	}
}

func TestUpdateNotFound(t *testing.T) {
	s := newStore(t)
	_, err := s.Update(999, node.UpdateSettings{
		NodeName:     "ghost",
		TargetSystem: "linux",
	})
	if err == nil {
		t.Fatal("expected error for non-existent node")
	}
}

func TestUpdateInvalidSystem(t *testing.T) {
	s := newStore(t)
	n, _ := s.Create("node-bar")
	_, err := s.Update(n.NodeNo, node.UpdateSettings{
		NodeName:     "node-bar",
		TargetSystem: "freebsd",
	})
	if err == nil {
		t.Fatal("expected error for invalid target system")
	}
}

func TestReplace(t *testing.T) {
	s := newStore(t)
	_, _ = s.Create("old-node")
	newNodes := []node.Node{
		{NodeNo: 10, NodeName: "replaced-node", TargetSystem: "linux"},
	}
	result, err := s.Replace(newNodes)
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if len(result) != 1 || result[0].NodeName != "replaced-node" {
		t.Fatalf("unexpected replace result: %+v", result)
	}
}

func TestPersistence(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	_ = os.MkdirAll(dir, 0o755)
	s1 := node.NewStore(dir)
	_, _ = s1.Create("persist-node")

	s2 := node.NewStore(dir)
	nodes, err := s2.List()
	if err != nil {
		t.Fatalf("reloaded List: %v", err)
	}
	if len(nodes) != 1 || nodes[0].NodeName != "persist-node" {
		t.Fatalf("expected persist-node after reload, got %+v", nodes)
	}
}
