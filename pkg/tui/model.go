package tui

import (
	"context"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"

	"github.com/kontrolplane/stdout/pkg/client"
	keys "github.com/kontrolplane/stdout/pkg/keys"
	"github.com/kontrolplane/stdout/pkg/loki"
	"github.com/kontrolplane/stdout/pkg/tui/styles"
)

// Layout constants for consistent sizing across all views
const (
	tableHeaderRows  = 2  // Column titles and the rule under them
	minTableHeight   = 3  // Minimum body rows for any table
	minContentWidth  = 80 // Below this, or minContentHeight, a notice asks for a larger terminal
	minContentHeight = 12
	chromeWidth      = 2 // Frame borders
	chromeHeight     = 8 // Header, spacing, frame edges and padding, footer
)

// The content area fills the terminal. setLayout resizes it and everything derived from it.
var (
	contentWidth  = 140
	contentHeight = 25
	frameWidth    = contentWidth + chromeWidth
)

func setLayout(width, height int) {
	contentWidth = max(minContentWidth, width-chromeWidth)
	contentHeight = max(minContentHeight, height-chromeHeight)
	frameWidth = contentWidth + chromeWidth
	setPanelLayout()
}

// Config holds the settings the command line passes on.
type Config struct {
	Query  string        // a query to tail right away, skipping the label picker
	Since  time.Duration // how far back the labels, the first lines and each older page reach
	Limit  int           // how many lines the tail starts with
	Buffer int           // how many lines the tail keeps
}

type model struct {
	projectName string
	programName string
	config      Config
	page        page
	client      *loki.Client
	info        client.Info
	build       loki.BuildInfo
	buildOK     bool
	context     context.Context
	width       int
	height      int
	keys        keys.KeyMap
	showHelp    bool
	error       string
	loading     bool
	loadingMsg  string
	spinner     spinner.Model
	spinning    bool
	statusMsg   string
	statusTone  styles.Tone
	statusGen   int
	editing     bool // the query editor in the footer has the keys
	editor      textinput.Model
	picker      pickerState
	tail        tailState
	details     detailsState
}

// resize fits the tables, viewports and inputs of every page to the current layout.
func (m model) resize() model {
	m.picker.labelsTable.setSize(leftContentWidth, pickerTableHeight())
	m.picker.valuesTable.setSize(rightContentWidth, pickerTableHeight())
	m.editor.SetWidth(contentWidth - 30)
	m.tail = m.tail.scroll()
	if m.page == lineDetails {
		m.details.resize()
	}
	return m
}
