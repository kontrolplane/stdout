package keys

import "charm.land/bubbles/v2/key"

// KeyMap holds the keybindings shared by the views. Moving through a list (up/down, j/k,
// pgup/pgdown, g/G) is handled by the list itself.
type KeyMap struct {
	Left            key.Binding
	Right           key.Binding
	Top             key.Binding
	Bottom          key.Binding
	Help            key.Binding
	View            key.Binding
	Select          key.Binding
	Negate          key.Binding
	Clear           key.Binding
	ClearAll        key.Binding
	Filter          key.Binding
	Edit            key.Binding
	CopyToClipboard key.Binding
	Refresh         key.Binding
	Pause           key.Binding
	Follow          key.Binding
	Wrap            key.Binding
	Labels          key.Binding
	Pinned          key.Binding
	Stream          key.Binding
	Previous        key.Binding
	Next            key.Binding
	ClearBuffer     key.Binding
	SwitchFocus     key.Binding
	Back            key.Binding
	Quit            key.Binding
	ForceQuit       key.Binding
}

var Keys = KeyMap{
	Left: key.NewBinding(
		key.WithKeys("left", "h"),
		key.WithHelp("← | h", "left"),
	),
	Right: key.NewBinding(
		key.WithKeys("right", "l"),
		key.WithHelp("→ | l", "right"),
	),
	Top: key.NewBinding(
		key.WithKeys("g", "home"),
		key.WithHelp("g", "top"),
	),
	Bottom: key.NewBinding(
		key.WithKeys("G", "end"),
		key.WithHelp("G", "bottom"),
	),
	Help: key.NewBinding(
		key.WithKeys("?"),
		key.WithHelp("?", "help"),
	),
	View: key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "view"),
	),
	Select: key.NewBinding(
		key.WithKeys("space"),
		key.WithHelp("space", "select"),
	),
	Negate: key.NewBinding(
		key.WithKeys("!"),
		key.WithHelp("!", "exclude"),
	),
	Clear: key.NewBinding(
		key.WithKeys("x"),
		key.WithHelp("x", "clear label"),
	),
	ClearAll: key.NewBinding(
		key.WithKeys("X"),
		key.WithHelp("X", "clear all"),
	),
	Filter: key.NewBinding(
		key.WithKeys("/"),
		key.WithHelp("/", "filter"),
	),
	Edit: key.NewBinding(
		key.WithKeys("e"),
		key.WithHelp("e", "edit query"),
	),
	CopyToClipboard: key.NewBinding(
		key.WithKeys("c"),
		key.WithHelp("c", "copy"),
	),
	Refresh: key.NewBinding(
		key.WithKeys("r"),
		key.WithHelp("r", "refresh"),
	),
	Pause: key.NewBinding(
		key.WithKeys("p"),
		key.WithHelp("p", "pause"),
	),
	Follow: key.NewBinding(
		key.WithKeys("f"),
		key.WithHelp("f", "follow"),
	),
	Wrap: key.NewBinding(
		key.WithKeys("w"),
		key.WithHelp("w", "wrap"),
	),
	Labels: key.NewBinding(
		key.WithKeys("l"),
		key.WithHelp("l", "labels"),
	),
	Pinned: key.NewBinding(
		key.WithKeys("tab"),
		key.WithHelp("tab", "pinned"),
	),
	Stream: key.NewBinding(
		key.WithKeys("s"),
		key.WithHelp("s", "tail stream"),
	),
	Previous: key.NewBinding(
		key.WithKeys("["),
		key.WithHelp("[", "previous line"),
	),
	Next: key.NewBinding(
		key.WithKeys("]"),
		key.WithHelp("]", "next line"),
	),
	ClearBuffer: key.NewBinding(
		key.WithKeys("ctrl+l"),
		key.WithHelp("ctrl+l", "clear"),
	),
	SwitchFocus: key.NewBinding(
		key.WithKeys("tab", "shift+tab"),
		key.WithHelp("tab", "switch list"),
	),
	Back: key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "back"),
	),
	Quit: key.NewBinding(
		key.WithKeys("q"),
		key.WithHelp("q", "quit"),
	),
	ForceQuit: key.NewBinding(
		key.WithKeys("ctrl+c"),
		key.WithHelp("ctrl+c", "quit"),
	),
}
