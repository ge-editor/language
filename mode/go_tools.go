package mode

import "github.com/ge-editor/gecore/lang"

// goTools are the tools that VS Code's "Go: Install/Update Tools" installs
// for the Go extension, as `go install` arguments.
var goTools = []string{
	"golang.org/x/tools/gopls@latest",                // language server
	"github.com/go-delve/delve/cmd/dlv@latest",       // debugger
	"honnef.co/go/tools/cmd/staticcheck@latest",      // linter
	"github.com/cweill/gotests/gotests@latest",       // generate tests
	"github.com/josharian/impl@latest",               // generate interface stubs
	"github.com/haya14busa/goplay/cmd/goplay@latest", // Go Playground client
}

// TerminalCommands implements lang.TerminalCommandProvider.
//
// go-install-tools runs `go install <tool>@latest` for each of goTools in the
// terminal, then waits for Enter so the result can be read.
func (gm *GoMode) TerminalCommands() []lang.TerminalCommand {
	steps := make([]lang.TerminalStep, 0, len(goTools))
	for _, tool := range goTools {
		steps = append(steps, lang.TerminalStep{
			Program: "go",
			Args:    []string{"install", tool},
		})
	}
	return []lang.TerminalCommand{{
		Name:           "go-install-tools",
		Description:    "Go: Install/Update Tools (gopls, dlv, staticcheck, gotests, impl, goplay)",
		Steps:          steps,
		PauseOnSuccess: true,
	}}
}
