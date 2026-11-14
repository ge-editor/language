package mode

import (
	"testing"

	"github.com/ge-editor/gecore/lang"
)

func TestGoModeOffersInstallTools(t *testing.T) {
	provider, ok := NewGoMode().(lang.TerminalCommandProvider)
	if !ok {
		t.Fatal("GoMode does not implement lang.TerminalCommandProvider")
	}
	cmds := provider.TerminalCommands()
	if len(cmds) != 1 || cmds[0].Name != "go-install-tools" {
		t.Fatalf("commands = %+v", cmds)
	}
	if len(cmds[0].Steps) != 6 {
		t.Fatalf("steps = %d, want 6", len(cmds[0].Steps))
	}
	for _, s := range cmds[0].Steps {
		if s.Program != "go" || len(s.Args) != 2 || s.Args[0] != "install" {
			t.Fatalf("step = %+v", s)
		}
	}
}
