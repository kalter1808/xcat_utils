package noderange

import (
	"os"
	"reflect"
	"testing"
)

func TestExpand(t *testing.T) {
	tests := []struct {
		name  string
		expr  string
		want  []string
		known map[string]bool
	}{
		{"plain", "node1,node3,node2", []string{"node1", "node2", "node3"}, nil},
		{"dedup sorted", "node2,node1,node2", []string{"node1", "node2"}, nil},
		{"bracket", "node[1-5]", []string{"node1", "node2", "node3", "node4", "node5"}, nil},
		{"bracket list", "node[1,3,5]", []string{"node1", "node3", "node5"}, nil},
		{"bracket padding", "node[001-003]", []string{"node001", "node002", "node003"}, nil},
		{"double bracket", "f[1-2]n[1-2]", []string{"f1n1", "f1n2", "f2n1", "f2n2"}, nil},
		{"hyphen", "node1-node3", []string{"node1", "node2", "node3"}, nil},
		{"colon", "node1:node3", []string{"node1", "node2", "node3"}, nil},
		{"padding preserved", "node001-node003", []string{"node001", "node002", "node003"}, nil},
		{"suffix hyphen range", "rack1-rack4", []string{"rack1", "rack2", "rack3", "rack4"}, nil},
		{"plus", "node10+3", []string{"node10", "node11", "node12", "node13"}, nil},
		{"plus padding", "node010+2", []string{"node010", "node011", "node012"}, nil},
		{"numbers shorthand", "10-12", []string{"node10", "node11", "node12"}, nil},
		{"numbers shorthand plus", "401+10", []string{"node401", "node402", "node403", "node404", "node405", "node406", "node407", "node408", "node409", "node410", "node411"}, nil},
		{"exclude", "node[1-5],-node3", []string{"node1", "node2", "node4", "node5"}, nil},
		{"exclude delayed wins", "node3,node[1-5],-node[2-3]", []string{"node1", "node4", "node5"}, nil},
		{"exclude hyphen atom", "node1-node5,-node2-node3", []string{"node1", "node4", "node5"}, nil},
		{"intersect", "node[1-5]@node[3-8]", []string{"node3", "node4", "node5"}, nil},
		{"intersect no comma", "node[1-5]@node[3-8]", []string{"node3", "node4", "node5"}, nil},
		{"parens union", "(node1,node2),node3", []string{"node1", "node2", "node3"}, nil},
		{"parens exclude", "node[1-5],-(node2,node4)", []string{"node1", "node3", "node5"}, nil},
		{"parens intersect", "node[1-5]@(node3,node9)", []string{"node3"}, nil},
		{"regex no known", "/node[34].*", []string{"/node[34].*"}, nil},
		{"regex with known", "/node[34].*", []string{"node3", "node4"},
			map[string]bool{"node1": true, "node3": true, "node4": true, "x": true}},
		{"ipv4 bracket", "192.168.0.[1-3]", []string{"192.168.0.1", "192.168.0.2", "192.168.0.3"}, nil},
		{"ipv4 hyphen", "192.168.0.1-192.168.0.3", []string{"192.168.0.1", "192.168.0.2", "192.168.0.3"}, nil},
		{"ipv4 with multiple dots hyphen", "10.0.0.[1-2],10.0.1.[1-2]", []string{"10.0.0.1", "10.0.0.2", "10.0.1.1", "10.0.1.2"}, nil},
		{"hostname with hyphen in name", "a-b1-a-b3", []string{"a-b1", "a-b2", "a-b3"}, nil},
		{"hostname ending in hyphen range", "gpu-1-gpu-3", []string{"gpu-1", "gpu-2", "gpu-3"}, nil},
		{"quoted", "'node1,node2'", []string{"node1", "node2"}, nil},
		{"empty atoms skipped", "node1,,node2,", []string{"node1", "node2"}, nil},
		{"mixed everything", "storage,rack1,node[1-2],node5+1", []string{"node1", "node2", "node5", "node6", "rack1", "storage"}, nil},
		{"literal intersect is empty", "storage@rack1,node1", []string{}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, missed := ExpandWithKnown(tt.expr, tt.known)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("expr %q = %v, want %v (missed=%v)", tt.expr, got, tt.want, missed)
			}
		})
	}
}

func TestExpandNoderangeFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/nodelist"
	content := "node1\nnode2 # inline comment\nnode3:alias\n# comment\n^/other\n\n"
	if err := writeFile(path, content); err != nil {
		t.Fatal(err)
	}
	got, _ := Expand("^" + path)
	want := []string{"node1", "node2", "node3"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("file expansion = %v, want %v", got, want)
	}

	// Nonexistent file: silently skipped, empty result.
	got, _ = Expand("^/nonexistent/file/xyz")
	if len(got) != 0 {
		t.Errorf("nonexistent file should give empty result, got %v", got)
	}
}

func TestUnbalancedParens(t *testing.T) {
	_, missed := Expand("(node1,node2")
	if len(missed) != 1 {
		t.Errorf("unbalanced parens should report the expression as missed, got %v", missed)
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}
