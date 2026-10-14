package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestValidateNode(t *testing.T) {
	valid := Node{
		Name:     "prod",
		URL:      "https://rs.example.com/r/F4pZ8kqN2mY7cTx9aB3dLg",
		Password: "correct-horse-battery-staple",
	}
	if err := validateNode(valid); err != nil {
		t.Fatal(err)
	}

	tests := []Node{
		{Name: "prod", URL: "http://rs.example.com/r/F4pZ8kqN2mY7cTx9aB3dLg", Password: valid.Password},
		{Name: "prod", URL: "https://rs.example.com/not-r/account", Password: valid.Password},
		{Name: "prod", URL: valid.URL, Password: "short"},
		{Name: "", URL: valid.URL, Password: valid.Password},
		{Name: "prod", IP: "not-an-ip", URL: valid.URL, Password: valid.Password},
	}
	for _, node := range tests {
		if err := validateNode(node); err == nil {
			t.Fatalf("accepted invalid node: %+v", node)
		}
	}

	loopback := valid
	loopback.URL = "http://127.0.0.1:8999/r/F4pZ8kqN2mY7cTx9aB3dLg"
	if err := validateNode(loopback); err != nil {
		t.Fatalf("loopback development URL rejected: %v", err)
	}
}

func TestNodeFileIsPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.json")
	t.Setenv("RS_NODE_FILE", path)
	node := Node{
		Name:     "prod",
		URL:      "https://rs.example.com/r/F4pZ8kqN2mY7cTx9aB3dLg",
		Password: "correct-horse-battery-staple",
	}
	if err := upsertNodes(node); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("node file mode = %o, want 600", info.Mode().Perm())
	}
	if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("node update created a lock file: %v", err)
	}
	loaded, err := findNode("prod")
	if err != nil {
		t.Fatal(err)
	}
	if loaded != node {
		t.Fatalf("loaded node = %+v, want %+v", loaded, node)
	}
}

func TestDecodeNodeJSON(t *testing.T) {
	input := `[
		{
			"name": "prod",
			"url": "https://rs.example.com/r/F4pZ8kqN2mY7cTx9aB3dLg/",
			"password": "correct-horse-battery-staple"
		}
	]`
	nodes, err := decodeNodeJSON(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].URL != "https://rs.example.com/r/F4pZ8kqN2mY7cTx9aB3dLg" {
		t.Fatalf("decoded nodes = %+v", nodes)
	}
}

func TestDecodeNodeJSONRejectsInvalidInput(t *testing.T) {
	validNode := `{"name":"prod","url":"https://rs.example.com/r/F4pZ8kqN2mY7cTx9aB3dLg","password":"correct-horse-battery-staple"}`
	tests := []string{
		`[]`,
		validNode,
		`[` + validNode + `] true`,
		`[{"name":"prod","url":"https://rs.example.com/r/F4pZ8kqN2mY7cTx9aB3dLg","password":"correct-horse-battery-staple","extra":true}]`,
		`[` + validNode + `,` + validNode + `]`,
	}
	for _, input := range tests {
		if _, err := decodeNodeJSON(strings.NewReader(input)); err == nil {
			t.Fatalf("accepted invalid input: %s", input)
		}
	}
}

func TestUpsertNodes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.json")
	t.Setenv("RS_NODE_FILE", path)
	existing := []Node{
		{Name: "prod", URL: "https://old.example.com/r/F4pZ8kqN2mY7cTx9aB3dLg", Password: "old-correct-horse-password"},
		{Name: "keep", URL: "https://keep.example.com/r/F4pZ8kqN2mY7cTx9aB3dLg", Password: "keep-correct-horse-password"},
	}
	if err := saveNodes(existing); err != nil {
		t.Fatal(err)
	}

	updates := []Node{
		{Name: "prod", URL: "https://new.example.com/r/F4pZ8kqN2mY7cTx9aB3dLg", Password: "new-correct-horse-password"},
		{Name: "stage", URL: "https://stage.example.com/r/F4pZ8kqN2mY7cTx9aB3dLg", Password: "stage-correct-horse-password"},
	}
	if err := upsertNodes(updates...); err != nil {
		t.Fatal(err)
	}

	nodes, err := loadNodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 3 {
		t.Fatalf("node count = %d, want 3", len(nodes))
	}
	if nodes[0] != updates[0] || nodes[1] != existing[1] || nodes[2] != updates[1] {
		t.Fatalf("nodes = %+v", nodes)
	}
}

func TestUpsertNodesMatchesByIDNotIP(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.json")
	t.Setenv("RS_NODE_FILE", path)
	first := Node{
		Name:     "first",
		ID:       "AAAAAAAAAAAAAAAAAAAAAA",
		IP:       "203.0.113.10",
		URL:      "https://first.example.com/r/F4pZ8kqN2mY7cTx9aB3dLg",
		Password: "first-correct-horse-password",
	}
	// Different servers behind one IP (shared proxy) stay separate nodes.
	second := Node{
		Name:     "second",
		ID:       "AQEBAQEBAQEBAQEBAQEBAQ",
		IP:       "203.0.113.10",
		URL:      "https://second.example.com/r/F4pZ8kqN2mY7cTx9aB3dLg",
		Password: "second-correct-horse-password",
	}
	if err := upsertNodes(first, second); err != nil {
		t.Fatal(err)
	}
	// Re-claiming the first server under a new name replaces it by ID.
	renamed := first
	renamed.Name = "renamed"
	renamed.Password = "renamed-correct-horse-password"
	if err := upsertNodes(renamed); err != nil {
		t.Fatal(err)
	}
	nodes, err := loadNodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 || nodes[0] != renamed || nodes[1] != second {
		t.Fatalf("nodes = %+v", nodes)
	}
}

func TestConcurrentNodeUpdatesDoNotLoseWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.json")
	t.Setenv("RS_NODE_FILE", path)

	var group sync.WaitGroup
	errs := make(chan error, 16)
	for i := range 16 {
		group.Add(1)
		go func() {
			defer group.Done()
			errs <- upsertNodes(Node{
				Name:     fmt.Sprintf("node-%d", i),
				URL:      fmt.Sprintf("https://node-%d.example.com/r/F4pZ8kqN2mY7cTx9aB3dLg", i),
				Password: "correct-horse-battery-staple",
			})
		}()
	}
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	nodes, err := loadNodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 16 {
		t.Fatalf("node count = %d, want 16", len(nodes))
	}
}

func TestSaveNodesCreatesPrivateDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nodes")
	t.Setenv("RS_NODE_FILE", filepath.Join(dir, "nodes.json"))
	if err := saveNodes([]Node{{
		Name:     "prod",
		URL:      "https://rs.example.com/r/F4pZ8kqN2mY7cTx9aB3dLg",
		Password: "correct-horse-battery-staple",
	}}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("node directory mode = %o, want 700", info.Mode().Perm())
	}
}

func TestNodeListJSONOmitsPasswords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.json")
	t.Setenv("RS_NODE_FILE", path)
	node := Node{
		Name:     "prod",
		IP:       "203.0.113.10",
		URL:      "https://rs.example.com/r/F4pZ8kqN2mY7cTx9aB3dLg",
		Password: "correct-horse-battery-staple",
	}
	if err := saveNodes([]Node{node}); err != nil {
		t.Fatal(err)
	}
	output, code := captureStdout(t, func() int {
		return cmdNode([]string{"list", "--json"})
	})
	if code != 0 {
		t.Fatalf("node list exit code = %d", code)
	}
	if strings.Contains(output, node.Password) {
		t.Fatal("node list JSON exposed password")
	}
	for _, value := range []string{node.Name, node.IP, node.URL} {
		if !strings.Contains(output, value) {
			t.Fatalf("node list JSON missing %q: %s", value, output)
		}
	}
	if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("node list created a lock file: %v", err)
	}
}

func TestIsLoopbackListen(t *testing.T) {
	for _, address := range []string{"127.0.0.1:8999", "[::1]:8999", "localhost:8999"} {
		if !isLoopbackListen(address) {
			t.Fatalf("loopback address rejected: %s", address)
		}
	}
	for _, address := range []string{":8999", "0.0.0.0:8999", "192.0.2.1:8999", "invalid"} {
		if isLoopbackListen(address) {
			t.Fatalf("public or invalid address accepted: %s", address)
		}
	}
}
