package tui

import "github.com/charmbracelet/bubbles/key"

type keyMap struct {
	Up      key.Binding
	Down    key.Binding
	Enter   key.Binding
	Tab     key.Binding
	Reply   key.Binding
	Dev     key.Binding
	SB      key.Binding
	New     key.Binding
	Clean   key.Binding
	Kill    key.Binding
	Diff    key.Binding
	Restart key.Binding
	Quit    key.Binding
	Confirm key.Binding
	Cancel  key.Binding
}

var keys = keyMap{
	Up:      key.NewBinding(key.WithKeys("k", "up"), key.WithHelp("↑/k", "up")),
	Down:    key.NewBinding(key.WithKeys("j", "down"), key.WithHelp("↓/j", "down")),
	Enter:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "attach")),
	Tab:     key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next group")),
	Reply:   key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "reply")),
	Dev:     key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "dev")),
	SB:      key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "storybook")),
	New:     key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new")),
	Clean:   key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "clean")),
	Kill:    key.NewBinding(key.WithKeys("K"), key.WithHelp("K", "kill")),
	Diff:    key.NewBinding(key.WithKeys("D"), key.WithHelp("D", "diff")),
	Restart: key.NewBinding(key.WithKeys("R"), key.WithHelp("R", "restart daemon")),
	Quit:    key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	Confirm: key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "confirm")),
	Cancel:  key.NewBinding(key.WithKeys("n", "esc"), key.WithHelp("n/esc", "cancel")),
}

// helpContext describes which overlay (if any) is active so helpBar can show
// the relevant key hints.
type helpContext struct {
	hasWorkspace bool
	confirming   bool
	dispatching  bool
	selecting    bool
}

func helpBar(c helpContext) string {
	if c.dispatching {
		return hk("⏎", "dispatch") + "  " + hk("esc", "cancel")
	}
	if c.selecting {
		return hk("↑/↓", "move") + "  " + hk("⏎", "attach") + "  " + hk("esc", "cancel")
	}
	if c.confirming {
		return hk("y", "confirm") + "  " + hk("n", "cancel") + "  " + hk("esc", "cancel")
	}
	if !c.hasWorkspace {
		return hk("n", "new") + "  " + hk("R", "restart") + "  " + hk("q", "quit")
	}
	return hk("⏎", "attach") + "  " +
		hk("r", "reply") + "  " +
		hk("d", "dev") + "  " +
		hk("s", "sb") + "  " +
		hk("D", "diff") + "  " +
		hk("c", "clean") + "  " +
		hk("K", "kill") + "  " +
		hk("n", "new") + "  " +
		hk("R", "restart") + "  " +
		hk("q", "quit")
}

func hk(k, desc string) string {
	return helpKeyStyle.Render(k) + " " + helpDescStyle.Render(desc)
}
