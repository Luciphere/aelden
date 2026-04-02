package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	runewidth "github.com/mattn/go-runewidth"
)

var dataPath string

// ---------------------------------------------------------------------------
// Data types
// ---------------------------------------------------------------------------

type Document struct {
	ID      string                 `json:"id"`
	Name    string                 `json:"name"`
	Content map[string]interface{} `json:"content"`
}

type Resource struct {
	ID       string                   `json:"id"`
	Name     string                   `json:"name"`
	ParentID string                   `json:"parentId"`
	Tags     []string                 `json:"tags"`
	IsHidden bool                     `json:"isHidden"`
	IconGlyph string                  `json:"iconGlyph"`
	Documents []Document              `json:"documents"`
	Properties []map[string]interface{} `json:"properties"`
}

type Export struct {
	Resources []Resource `json:"resources"`
}

// ---------------------------------------------------------------------------
// ProseMirror renderer
// ---------------------------------------------------------------------------

func renderInline(nodes []interface{}) string {
	var sb strings.Builder
	for _, n := range nodes {
		node, ok := n.(map[string]interface{})
		if !ok {
			continue
		}
		t, _ := node["type"].(string)
		switch t {
		case "text":
			text, _ := node["text"].(string)
			marks, _ := node["marks"].([]interface{})
			for _, m := range marks {
				mark, _ := m.(map[string]interface{})
				mt, _ := mark["type"].(string)
				switch mt {
				case "strong":
					text = "**" + text + "**"
				case "em":
					text = "_" + text + "_"
				case "code":
					text = "`" + text + "`"
				case "strike":
					text = "~~" + text + "~~"
				}
			}
			sb.WriteString(text)
		case "mention":
			attrs, _ := node["attrs"].(map[string]interface{})
			name, _ := attrs["text"].(string)
			sb.WriteString("[" + name + "]")
		case "hardBreak":
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

func renderNode(node map[string]interface{}, depth int) string {
	t, _ := node["type"].(string)
	rawChildren, _ := node["content"].([]interface{})

	switch t {
	case "doc":
		var sb strings.Builder
		for _, c := range rawChildren {
			if child, ok := c.(map[string]interface{}); ok {
				sb.WriteString(renderNode(child, depth))
			}
		}
		return sb.String()

	case "paragraph":
		text := renderInline(rawChildren)
		if strings.TrimSpace(text) == "" {
			return ""
		}
		return text + "\n\n"

	case "heading":
		attrs, _ := node["attrs"].(map[string]interface{})
		level := 2
		if l, ok := attrs["level"].(float64); ok {
			level = int(l)
		}
		text := renderInline(rawChildren)
		return strings.Repeat("#", level) + " " + text + "\n\n"

	case "bulletList":
		var sb strings.Builder
		for _, item := range rawChildren {
			child, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			itemChildren, _ := child["content"].([]interface{})
			var itemText strings.Builder
			for _, c := range itemChildren {
				if gc, ok := c.(map[string]interface{}); ok {
					itemText.WriteString(renderNode(gc, depth))
				}
			}
			sb.WriteString("• " + strings.TrimSpace(itemText.String()) + "\n")
		}
		return sb.String() + "\n"

	case "orderedList":
		var sb strings.Builder
		i := 1
		for _, item := range rawChildren {
			child, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			itemChildren, _ := child["content"].([]interface{})
			var itemText strings.Builder
			for _, c := range itemChildren {
				if gc, ok := c.(map[string]interface{}); ok {
					itemText.WriteString(renderNode(gc, depth))
				}
			}
			sb.WriteString(fmt.Sprintf("%d. %s\n", i, strings.TrimSpace(itemText.String())))
			i++
		}
		return sb.String() + "\n"

	case "listItem":
		var sb strings.Builder
		for _, c := range rawChildren {
			if child, ok := c.(map[string]interface{}); ok {
				sb.WriteString(renderNode(child, depth))
			}
		}
		return sb.String()

	case "panel":
		attrs, _ := node["attrs"].(map[string]interface{})
		panelType, _ := attrs["panelType"].(string)
		label := strings.ToUpper(panelType)
		var inner strings.Builder
		for _, c := range rawChildren {
			if child, ok := c.(map[string]interface{}); ok {
				inner.WriteString(renderNode(child, depth))
			}
		}
		var sb strings.Builder
		sb.WriteString("┌─ " + label + " ─\n")
		for _, line := range strings.Split(strings.TrimSpace(inner.String()), "\n") {
			sb.WriteString("│ " + line + "\n")
		}
		sb.WriteString("└──────────────\n\n")
		return sb.String()

	case "table":
		var rows [][]string
		for _, row := range rawChildren {
			rowNode, ok := row.(map[string]interface{})
			if !ok {
				continue
			}
			cells, _ := rowNode["content"].([]interface{})
			var rowTexts []string
			for _, cell := range cells {
				cellNode, ok := cell.(map[string]interface{})
				if !ok {
					continue
				}
				cellChildren, _ := cellNode["content"].([]interface{})
				var cellText strings.Builder
				for _, c := range cellChildren {
					if gc, ok := c.(map[string]interface{}); ok {
						cellText.WriteString(renderNode(gc, depth))
					}
				}
				rowTexts = append(rowTexts, strings.TrimSpace(cellText.String()))
			}
			rows = append(rows, rowTexts)
		}
		if len(rows) == 0 {
			return ""
		}
		var sb strings.Builder
		for i, row := range rows {
			sb.WriteString("| " + strings.Join(row, " | ") + " |\n")
			if i == 0 {
				seps := make([]string, len(row))
				for j := range seps {
					seps[j] = "---"
				}
				sb.WriteString("| " + strings.Join(seps, " | ") + " |\n")
			}
		}
		sb.WriteString("\n")
		return sb.String()

	case "extension":
		attrs, _ := node["attrs"].(map[string]interface{})
		params, _ := attrs["parameters"].(map[string]interface{})
		extKey, _ := attrs["extensionKey"].(string)
		if extKey == "block-tag-index" {
			rawTags, _ := params["tags"].([]interface{})
			var tags []string
			for _, tag := range rawTags {
				if s, ok := tag.(string); ok {
					tags = append(tags, s)
				}
			}
			if len(tags) > 0 {
				return "_Tagged: " + strings.Join(tags, ", ") + "_\n\n"
			}
		}
		return ""

	case "bodiedExtension", "layoutSection", "layoutColumn":
		var sb strings.Builder
		for _, c := range rawChildren {
			if child, ok := c.(map[string]interface{}); ok {
				sb.WriteString(renderNode(child, depth))
			}
		}
		return sb.String()

	default:
		var sb strings.Builder
		for _, c := range rawChildren {
			if child, ok := c.(map[string]interface{}); ok {
				sb.WriteString(renderNode(child, depth))
			}
		}
		return sb.String()
	}
}

func renderContent(content map[string]interface{}) string {
	if len(content) == 0 {
		return "(no content)"
	}
	result := renderNode(content, 0)
	result = strings.TrimSpace(result)
	if result == "" {
		return "(no content)"
	}
	return result
}

// ---------------------------------------------------------------------------
// Tree + search
// ---------------------------------------------------------------------------

type TreeNode struct {
	Resource *Resource
	Depth    int
}

func buildTree(resources []Resource) (map[string]*Resource, map[string][]string) {
	idMap := make(map[string]*Resource, len(resources))
	children := make(map[string][]string)
	for i := range resources {
		r := &resources[i]
		idMap[r.ID] = r
		parent := r.ParentID
		if parent == "" {
			parent = "__root__"
		}
		children[parent] = append(children[parent], r.ID)
	}
	return idMap, children
}

func flattenTree(idMap map[string]*Resource, children map[string][]string, parent string, depth int) []TreeNode {
	var result []TreeNode
	for _, rid := range children[parent] {
		r, ok := idMap[rid]
		if !ok {
			continue
		}
		result = append(result, TreeNode{Resource: r, Depth: depth})
		result = append(result, flattenTree(idMap, children, rid, depth+1)...)
	}
	return result
}

func buildVisibleTree(idMap map[string]*Resource, children map[string][]string, expanded map[string]bool, parent string, depth int) []TreeNode {
	var result []TreeNode
	for _, rid := range children[parent] {
		r, ok := idMap[rid]
		if !ok {
			continue
		}
		result = append(result, TreeNode{Resource: r, Depth: depth})
		if expanded[rid] {
			result = append(result, buildVisibleTree(idMap, children, expanded, rid, depth+1)...)
		}
	}
	return result
}

func buildSearchIndex(resources []Resource) map[string]string {
	index := make(map[string]string, len(resources))
	for i := range resources {
		r := &resources[i]
		var sb strings.Builder
		sb.WriteString(strings.ToLower(r.Name))
		sb.WriteString(" ")
		for _, tag := range r.Tags {
			sb.WriteString(strings.ToLower(tag))
			sb.WriteString(" ")
		}
		for _, doc := range r.Documents {
			sb.WriteString(strings.ToLower(renderContent(doc.Content)))
			sb.WriteString(" ")
		}
		index[r.ID] = sb.String()
	}
	return index
}

func resourceIcon(glyph string) string {
	icons := map[string]string{
		"fa-user":     "👤",
		"fa-users":    "👥",
		"fa-map":      "🗺 ",
		"fa-book":     "📖",
		"fa-scroll":   "📜",
		"fa-landmark": "🏛 ",
		"fa-city":     "🏙 ",
		"fa-crown":    "👑",
		"fa-dragon":   "🐉",
		"fa-mountain": "⛰ ",
		"fa-tree":     "🌲",
		"fa-star":     "⭐",
		"fa-globe":    "🌍",
		"fa-flag":     "🚩",
		"fa-shield":   "🛡 ",
		"fa-magic":    "✨",
		"fa-church":   "⛪",
		"fa-home":     "🏠",
		"fa-fort":     "🏰",
		"fa-sword":    "⚔ ",
	}
	for key, emoji := range icons {
		if strings.Contains(glyph, key) {
			return emoji
		}
	}
	return "• "
}

// ---------------------------------------------------------------------------
// Theme
// ---------------------------------------------------------------------------

var (
	colorGold    = lipgloss.Color("#E8B64B")
	colorBlue    = lipgloss.Color("#7EB8CC")
	colorGreen   = lipgloss.Color("#6DBF8A")
	colorTeal    = lipgloss.Color("#5BC4BE")
	colorMuted   = lipgloss.Color("#5C6478")
	colorFaint   = lipgloss.Color("#252C3E")
	colorBright  = lipgloss.Color("#DCE4F0")
	colorAccent  = lipgloss.Color("#4FA8D4")
	colorBg      = lipgloss.Color("#1A1E2E")
	colorSideBg  = lipgloss.Color("#141827")
	colorSelBg   = lipgloss.Color("#2A3854")
	colorSelFg   = lipgloss.Color("#F7C948")
	colorDivider = lipgloss.Color("#2E3547")

	sHeader = lipgloss.NewStyle().
		Background(colorBg).
		Foreground(colorGold).
		Bold(true).
		Padding(0, 2)

	sHeaderRight = lipgloss.NewStyle().
		Background(colorBg).
		Foreground(colorMuted).
		Padding(0, 2)

	sSelected = lipgloss.NewStyle().
		Background(colorSelBg).
		Foreground(colorSelFg).
		Bold(true)

	sNormal = lipgloss.NewStyle().
		Foreground(colorBright)

	sDim = lipgloss.NewStyle().
		Foreground(colorMuted)

	sSectionHeader = lipgloss.NewStyle().
		Foreground(colorBlue).
		Bold(true).
		Padding(0, 1)

	sTitle = lipgloss.NewStyle().
		Foreground(colorGold).
		Bold(true)

	sTag = lipgloss.NewStyle().
		Background(colorFaint).
		Foreground(colorBlue).
		Padding(0, 1)

	sStatusBar = lipgloss.NewStyle().
		Background(colorBg).
		Foreground(colorMuted).
		Padding(0, 1)

	sKey = lipgloss.NewStyle().
		Background(colorFaint).
		Foreground(colorBright).
		Padding(0, 1)

	sBorderActive = lipgloss.Color("#4FA8D4")
	sBorderNormal = lipgloss.Color("#2E3547")
)

// ---------------------------------------------------------------------------
// Styled content renderer
// ---------------------------------------------------------------------------

func styleContent(raw string) string {
	lines := strings.Split(raw, "\n")
	var out []string
	for _, line := range lines {
		out = append(out, styleLine(line))
	}
	return strings.Join(out, "\n")
}

func styleLine(line string) string {
	// Headings
	if strings.HasPrefix(line, "#### ") {
		return lipgloss.NewStyle().Foreground(colorBlue).Bold(true).Render(line[5:])
	}
	if strings.HasPrefix(line, "### ") {
		return lipgloss.NewStyle().Foreground(colorBlue).Bold(true).Render("  " + line[4:])
	}
	if strings.HasPrefix(line, "## ") {
		return lipgloss.NewStyle().Foreground(colorGold).Bold(true).Render(line[3:])
	}
	if strings.HasPrefix(line, "# ") {
		return lipgloss.NewStyle().Foreground(colorGold).Bold(true).Underline(true).Render(line[2:])
	}
	// Dividers
	if strings.HasPrefix(line, "── ") && strings.HasSuffix(line, " ──") {
		return sDim.Render(line)
	}
	// Table separator
	if strings.HasPrefix(line, "| ---") {
		return sDim.Render(line)
	}
	// Table rows
	if strings.HasPrefix(line, "| ") {
		return lipgloss.NewStyle().Foreground(colorBright).Render(line)
	}
	// Panel borders
	if strings.HasPrefix(line, "┌─") || strings.HasPrefix(line, "└─") {
		return sDim.Render(line)
	}
	if strings.HasPrefix(line, "│ ") {
		return lipgloss.NewStyle().Foreground(colorBlue).Render(line)
	}
	// Bullets
	if strings.HasPrefix(line, "• ") {
		bullet := lipgloss.NewStyle().Foreground(colorGold).Render("•")
		rest := applyInlineStyles(line[3:])
		return bullet + " " + rest
	}
	// Ordered list
	if len(line) > 2 && line[1] == '.' && line[2] == ' ' {
		num := lipgloss.NewStyle().Foreground(colorGold).Bold(true).Render(string(line[0]) + ".")
		rest := applyInlineStyles(line[3:])
		return num + " " + rest
	}
	// Tags line
	if strings.HasPrefix(line, "Tags: ") {
		parts := strings.SplitN(line, ": ", 2)
		if len(parts) == 2 {
			tags := strings.Split(parts[1], ", ")
			var rendered []string
			for _, t := range tags {
				rendered = append(rendered, sTag.Render("#"+t))
			}
			return sDim.Render("  ") + strings.Join(rendered, " ")
		}
	}
	// Tagged metadata line (from extension nodes)
	if strings.HasPrefix(line, "_Tagged: ") && strings.HasSuffix(line, "_") {
		inner := strings.TrimSuffix(strings.TrimPrefix(line, "_Tagged: "), "_")
		tags := strings.Split(inner, ", ")
		var rendered []string
		for _, t := range tags {
			rendered = append(rendered, sTag.Render("#"+t))
		}
		return sDim.Render("  ") + strings.Join(rendered, " ")
	}
	// Italic metadata
	if strings.HasPrefix(line, "_") && strings.HasSuffix(line, "_") {
		return sDim.Render(strings.Trim(line, "_"))
	}
	// Default: highlight mentions on plain text first, then apply inline styles
	return applyInlineStyles(highlightMentions(line))
}

func applyInlineStyles(line string) string {
	// Bold: **text**
	var result strings.Builder
	i := 0
	runes := []rune(line)
	for i < len(runes) {
		if i+1 < len(runes) && runes[i] == '*' && runes[i+1] == '*' {
			end := strings.Index(string(runes[i+2:]), "**")
			if end >= 0 {
				bold := string(runes[i+2 : i+2+end])
				result.WriteString(lipgloss.NewStyle().Foreground(colorBright).Bold(true).Render(bold))
				i += 2 + end + 2
				continue
			}
		}
		result.WriteRune(runes[i])
		i++
	}
	styled := result.String()
	// Apply base color to the whole line
	return lipgloss.NewStyle().Foreground(lipgloss.Color("#CBD5E0")).Render(styled)
}

// ---------------------------------------------------------------------------
// Mention extraction
// ---------------------------------------------------------------------------

type mentionLink struct {
	name     string
	resource *Resource
}

func extractMentionNames(node map[string]interface{}) []string {
	t, _ := node["type"].(string)
	if t == "mention" {
		attrs, _ := node["attrs"].(map[string]interface{})
		name, _ := attrs["text"].(string)
		if name != "" {
			return []string{name}
		}
		return nil
	}
	var out []string
	children, _ := node["content"].([]interface{})
	for _, c := range children {
		if child, ok := c.(map[string]interface{}); ok {
			out = append(out, extractMentionNames(child)...)
		}
	}
	return out
}

func highlightMentions(line string) string {
	mentionStyle := lipgloss.NewStyle().Foreground(colorAccent).Underline(true)
	var result strings.Builder
	remaining := line
	for {
		start := strings.Index(remaining, "[")
		if start == -1 {
			result.WriteString(remaining)
			break
		}
		end := strings.Index(remaining[start:], "]")
		if end == -1 {
			result.WriteString(remaining)
			break
		}
		end += start
		result.WriteString(remaining[:start])
		inner := remaining[start+1 : end]
		result.WriteString(mentionStyle.Render("[" + inner + "]"))
		remaining = remaining[end+1:]
	}
	return result.String()
}

// ---------------------------------------------------------------------------
// TUI model
// ---------------------------------------------------------------------------

type focus int

const (
	focusList focus = iota
	focusSearch
)

type viewMode int

const (
	worldView viewMode = iota
	campaignView
	plotView
)

type Player struct {
	Name  string `json:"name"`
	Note  string `json:"note"`
	Blurb string `json:"blurb,omitempty"`
}

type Session struct {
	Name  string `json:"name"`
	Note  string `json:"note"`
	Blurb string `json:"blurb,omitempty"`
}

type Combatant struct {
	Name       string `json:"name"`
	Initiative int    `json:"initiative"`
}

type Task struct {
	Name string `json:"name"`
	Done bool   `json:"done"`
}

type PlotNode struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Description string     `json:"description,omitempty"`
	Consequence string     `json:"consequence,omitempty"`
	WikiRef     string     `json:"wikiRef,omitempty"`
	WikiName    string     `json:"wikiName,omitempty"`
	Chosen      bool       `json:"chosen"`
	Children    []PlotNode `json:"children,omitempty"`
}


type Campaign struct {
	Name       string      `json:"name"`
	General    string      `json:"general"`
	Players    []Player    `json:"players"`
	Sessions   []Session   `json:"sessions"`
	Tasks      []Task      `json:"tasks,omitempty"`
	Initiative []Combatant `json:"initiative,omitempty"`
	Plot       []PlotNode  `json:"plot,omitempty"`
}

type CampaignData struct {
	Campaigns []Campaign `json:"campaigns"`
	// Legacy fields for migration only
	General string   `json:"general,omitempty"`
	Players []Player `json:"players,omitempty"`
}

type campItemKind int

const (
	campKindCampaign campItemKind = iota
	campKindGeneral
	campKindPlayer
	campKindSession
)

type campListItem struct {
	kind      campItemKind
	campIdx   int
	playerIdx int // also used as sessionIdx for campKindSession
	label     string
	depth     int
}

type model struct {
	// data
	resources   []Resource
	idMap       map[string]*Resource
	fullTree    []TreeNode
	searchIndex map[string]string

	// list state
	items      []TreeNode
	cursor     int
	listOffset int

	// content state
	selected *Resource
	content  string
	vp       viewport.Model

	// tree fold state
	children map[string][]string
	expanded map[string]bool

	// navigation history
	history    []string
	historyPos int

	// mentions
	mentions      []mentionLink
	showMentions  bool
	mentionCursor int
	mentionInput  string

	// search
	searchQuery string
	lastQuery   string

	// view mode
	mode viewMode

	// campaign
	campaign     CampaignData
	campaignPath string
	campExpanded map[int]bool
	campItems    []campListItem
	campCursor   int
	campEditing  bool
	campAdding  bool
	campAddKind campItemKind
	campConfirm  bool
	campRenaming bool
	campInput    textinput.Model // shared single-line input for add/rename/task
	ta           textarea.Model
	campBlurbTA  textarea.Model // textarea for blurb editing

	// campaign reference picker
	campRefPicking   bool
	campRefInsert    bool   // true=insert into textarea, false=follow to world view
	campRefSearch    string
	campRefCursor    int
	campRefTagFilter string // non-empty = show articles with this tag (phase 2)

	// task list
	campTaskFocus  bool
	campTaskCursor int
	campTaskAdding bool

	// blurb editing
	campBlurbEditing bool

	// sidebar scrolling
	campListOffset int

	// campaign search
	campSearchQuery  string
	campSearchActive bool

	// initiative tracker
	showInitiative bool
	initCampIdx    int
	initCursor     int
	initTurn       int
	initAdding     bool
	initAddPhase   int // 0=name, 1=initiative number
	initAddName    string
	initAddInput   string

	// plot / decision tree (standalone view)
	plotSideCursor  int  // which campaign is selected in plot view sidebar
	plotCampIdx     int  // derived from plotSideCursor on entry
	plotSideFocus   bool // true = keyboard focus is on the campaign sidebar
	plotFocusCol    int  // which column has keyboard focus
	plotColCursor  []int  // cursor position per column
	plotColOffset  []int  // scroll offset per column
	plotViewStart  int    // leftmost visible column
	plotEditing    bool
	plotEditField  string // "desc" or "consequence"
	plotConfirm    bool
	plotAdding     bool
	plotRenaming   bool
	plotRefPicking bool

	// plot search
	plotSearchQuery  string
	plotSearchActive bool

	// help screen
	showHelp bool

	// file watching
	fileMod      time.Time
	notification string
	notifyTicks  int

	// layout
	focus  focus
	width  int
	height int
}

func initialModel(exp Export) model {
	idMap, children := buildTree(exp.Resources)
	fullTree := flattenTree(idMap, children, "__root__", 0)
	searchIndex := buildSearchIndex(exp.Resources)
	expanded := make(map[string]bool)
	visibleTree := buildVisibleTree(idMap, children, expanded, "__root__", 0)

	vp := viewport.New(80, 40)

	ta := textarea.New()
	ta.Placeholder = "Skriv noter her..."
	ta.ShowLineNumbers = false
	ta.CharLimit = 0

	ci := textinput.New()
	ci.TextStyle = lipgloss.NewStyle().Foreground(colorSelFg)
	ci.CursorStyle = lipgloss.NewStyle().Background(colorSelFg).Foreground(colorBg)
	ci.CharLimit = 120

	blurbTA := textarea.New()
	blurbTA.Placeholder = "Skriv blurb her..."
	blurbTA.ShowLineNumbers = false
	blurbTA.CharLimit = 0


	var fileMod time.Time
	if info, err := os.Stat(dataPath); err == nil {
		fileMod = info.ModTime()
	}

	campExpanded := map[int]bool{0: true}
	m := model{
		resources:    exp.Resources,
		idMap:        idMap,
		children:     children,
		fullTree:     fullTree,
		searchIndex:  searchIndex,
		items:        visibleTree,
		expanded:     expanded,
		cursor:       0,
		listOffset:   0,
		vp:           vp,
		mode:         worldView,
		campaign:     loadCampaign(),
		campaignPath: campaignFilePath(),
		campExpanded: campExpanded,
		ta:          ta,
		campInput:   ci,
		campBlurbTA: blurbTA,
		focus:        focusList,
		fileMod:      fileMod,
	}
	m.buildCampItems()
	return m
}

// fileCheckMsg is sent periodically to check if the data file has changed.
type fileCheckMsg struct{ modTime time.Time }

func watchFileCmd(lastMod time.Time) tea.Cmd {
	return func() tea.Msg {
		time.Sleep(2 * time.Second)
		info, err := os.Stat(dataPath)
		if err != nil {
			return fileCheckMsg{modTime: lastMod}
		}
		return fileCheckMsg{modTime: info.ModTime()}
	}
}

func (m model) Init() tea.Cmd {
	return watchFileCmd(m.fileMod)
}

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case fileCheckMsg:
		// Tick down notification
		if m.notifyTicks > 0 {
			m.notifyTicks--
			if m.notifyTicks == 0 {
				m.notification = ""
			}
		}
		if msg.modTime.After(m.fileMod) {
			m.fileMod = msg.modTime
			f, err := os.Open(dataPath)
			if err == nil {
				var exp Export
				if json.NewDecoder(f).Decode(&exp) == nil {
					selectedID := ""
					if m.selected != nil {
						selectedID = m.selected.ID
					}
					m.resources = exp.Resources
					idMap, children := buildTree(exp.Resources)
					m.idMap = idMap
					m.children = children
					m.fullTree = flattenTree(idMap, children, "__root__", 0)
					m.searchIndex = buildSearchIndex(exp.Resources)
					m.items = buildVisibleTree(idMap, children, m.expanded, "__root__", 0)
					m.searchQuery = ""
					m.lastQuery = ""
					m.cursor = 0
					m.listOffset = 0
					m.selected = nil
					m.mentions = nil
					// Restore previous selection if resource still exists
					if selectedID != "" {
						for i, node := range m.fullTree {
							if node.Resource.ID == selectedID {
								m.cursor = i
								m.clampOffset()
								m.selectCurrent()
								break
							}
						}
					}
					m.notification = "↺  Genindlæst"
					m.notifyTicks = 3
				}
				f.Close()
			}
		}
		cmds = append(cmds, watchFileCmd(m.fileMod))
		return m, tea.Batch(cmds...)

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		contentWidth := m.width - m.sidebarWidth() - 4
		contentHeight := m.height - 6
		m.vp.Width = contentWidth
		m.vp.Height = contentHeight
		if m.selected != nil {
			m.vp.SetContent(styleContent(m.wrapContent(m.content, contentWidth)))
		}
		m.ta.SetWidth(m.width - m.sidebarWidth() - 6)
		m.ta.SetHeight(m.height - 8)
		blurbW := m.width - m.sidebarWidth() - 10
		if blurbW < 10 {
			blurbW = 10
		}
		m.campBlurbTA.SetWidth(blurbW)
		m.campBlurbTA.SetHeight(4)
		m.campInput.Width = m.width - m.sidebarWidth() - 20
		if m.campInput.Width < 10 {
			m.campInput.Width = 10
		}
		return m, nil

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" || msg.String() == "q" {
			return m, tea.Quit
		}
		if msg.String() == "?" {
			m.showHelp = !m.showHelp
			return m, nil
		}
		if m.showHelp {
			m.showHelp = false
			return m, nil
		}
		if m.mode == campaignView || m.mode == plotView {
			// ── initiative tracker ────────────────────────────────
			if m.showInitiative {
				init := m.campaign.Campaigns[m.initCampIdx].Initiative
				if m.initAdding {
					switch msg.String() {
					case "ctrl+c":
						return m, tea.Quit
					case "esc":
						m.initAdding = false
						m.initAddInput = ""
						m.initAddName = ""
						m.initAddPhase = 0
					case "enter":
						if m.initAddPhase == 0 {
							m.initAddName = strings.TrimSpace(m.initAddInput)
							if m.initAddName == "" {
								m.initAdding = false
							} else {
								m.initAddPhase = 1
								m.initAddInput = ""
							}
						} else {
							val := 0
							fmt.Sscanf(strings.TrimSpace(m.initAddInput), "%d", &val)
							c := Combatant{Name: m.initAddName, Initiative: val}
							m.campaign.Campaigns[m.initCampIdx].Initiative = append(
								m.campaign.Campaigns[m.initCampIdx].Initiative, c)
							m.sortInitiative()
							m.saveCampaign()
							// find cursor position of new entry
							for i, com := range m.campaign.Campaigns[m.initCampIdx].Initiative {
								if com.Name == c.Name && com.Initiative == c.Initiative {
									m.initCursor = i
									break
								}
							}
							m.initAdding = false
							m.initAddInput = ""
							m.initAddName = ""
							m.initAddPhase = 0
						}
					case "backspace":
						if len(m.initAddInput) > 0 {
							runes := []rune(m.initAddInput)
							m.initAddInput = string(runes[:len(runes)-1])
						}
					default:
						if len(msg.Runes) == 1 {
							m.initAddInput += string(msg.Runes)
						}
					}
					return m, nil
				}
				// not adding
				switch msg.String() {
				case "ctrl+c":
					return m, tea.Quit
				case "tab":
					m.showInitiative = false
					m.mode = worldView
					return m, nil
				case "esc", "i":
					m.showInitiative = false
					return m, nil
				case "j", "down":
					if m.initCursor < len(init)-1 {
						m.initCursor++
					}
				case "k", "up":
					if m.initCursor > 0 {
						m.initCursor--
					}
				case "n", " ":
					if len(init) > 0 {
						m.initTurn = (m.initTurn + 1) % len(init)
					}
				case "p":
					if len(init) > 0 {
						m.initTurn = (m.initTurn - 1 + len(init)) % len(init)
					}
				case "r":
					m.initTurn = 0
				case "a":
					m.initAdding = true
					m.initAddPhase = 0
					m.initAddInput = ""
					m.initAddName = ""
				case "d":
					if len(init) > 0 {
						ci := m.initCampIdx
						idx := m.initCursor
						m.campaign.Campaigns[ci].Initiative = append(init[:idx], init[idx+1:]...)
						m.saveCampaign()
						if m.initCursor >= len(m.campaign.Campaigns[ci].Initiative) {
							m.initCursor = len(m.campaign.Campaigns[ci].Initiative) - 1
						}
						if m.initCursor < 0 {
							m.initCursor = 0
						}
						if m.initTurn >= len(m.campaign.Campaigns[ci].Initiative) {
							m.initTurn = 0
						}
					}
				case "+", "=":
					if len(init) > 0 {
						m.campaign.Campaigns[m.initCampIdx].Initiative[m.initCursor].Initiative++
						m.sortInitiative()
						m.saveCampaign()
					}
				case "-":
					if len(init) > 0 {
						m.campaign.Campaigns[m.initCampIdx].Initiative[m.initCursor].Initiative--
						m.sortInitiative()
						m.saveCampaign()
					}
				case "X":
					m.campaign.Campaigns[m.initCampIdx].Initiative = nil
					m.initCursor = 0
					m.initTurn = 0
					m.saveCampaign()
				}
				return m, nil
			}

			// ── plot view is a separate mode (if m.mode == plotView below) ──
		if m.mode == plotView {
			// editing description or consequence (single-line, Enter saves)
			if m.plotEditing {
					switch msg.String() {
					case "ctrl+c":
						return m, tea.Quit
					case "esc":
						m.plotEditing = false
						m.campInput.Blur()
					case "enter":
						cur := m.plotCurrentNode()
						if cur != nil {
							val := m.campInput.Value()
							if m.plotEditField == "desc" {
								cur.Description = val
							} else {
								cur.Consequence = val
							}
							m.campaign.Campaigns[m.plotCampIdx].Plot =
								m.plotSetNode(m.campaign.Campaigns[m.plotCampIdx].Plot,
									m.plotCurrentPath(), *cur)
							m.saveCampaign()
						}
						m.plotEditing = false
						m.campInput.Blur()
					default:
						var cmd tea.Cmd
						m.campInput, cmd = m.campInput.Update(msg)
						cmds = append(cmds, cmd)
					}
					return m, tea.Batch(cmds...)
				}
				// adding new node (Enter saves)
				if m.plotAdding {
					switch msg.String() {
					case "ctrl+c":
						return m, tea.Quit
					case "esc":
						m.plotAdding = false
						m.campInput.Blur()
					case "enter":
						title := strings.TrimSpace(m.campInput.Value())
						if title != "" {
							node := PlotNode{
								ID:    fmt.Sprintf("n%d", time.Now().UnixNano()),
								Title: title,
							}
							parentPath := m.plotParentPath()
							plot := m.campaign.Campaigns[m.plotCampIdx].Plot
							plot = m.plotAddChildAt(plot, parentPath, node)
							m.campaign.Campaigns[m.plotCampIdx].Plot = plot
							m.saveCampaign()
							// move cursor to new node
							nodes := m.plotNodesAtCol(m.plotFocusCol)
							idx := len(nodes) - 1
							if idx < 0 {
								idx = 0
							}
							m.setPlotColCursor(m.plotFocusCol, idx)
						}
						m.plotAdding = false
						m.campInput.Blur()
					default:
						var cmd tea.Cmd
						m.campInput, cmd = m.campInput.Update(msg)
						cmds = append(cmds, cmd)
					}
					return m, tea.Batch(cmds...)
				}
				// renaming a plot node (Enter saves)
				if m.plotRenaming {
					switch msg.String() {
					case "ctrl+c":
						return m, tea.Quit
					case "esc":
						m.plotRenaming = false
						m.campInput.Blur()
					case "enter":
						title := strings.TrimSpace(m.campInput.Value())
						if title != "" {
							cur := m.plotCurrentNode()
							if cur != nil {
								cur.Title = title
								m.campaign.Campaigns[m.plotCampIdx].Plot =
									m.plotSetNode(m.campaign.Campaigns[m.plotCampIdx].Plot,
										m.plotCurrentPath(), *cur)
								m.saveCampaign()
							}
						}
						m.plotRenaming = false
						m.campInput.Blur()
					default:
						var cmd tea.Cmd
						m.campInput, cmd = m.campInput.Update(msg)
						cmds = append(cmds, cmd)
					}
					return m, tea.Batch(cmds...)
				}
				// confirm delete
				if m.plotConfirm {
					switch msg.String() {
					case "j", "y":
						path := m.plotCurrentPath()
						m.campaign.Campaigns[m.plotCampIdx].Plot =
							plotDeleteAt(m.campaign.Campaigns[m.plotCampIdx].Plot, path)
						m.saveCampaign()
						// reset cursor in this column
						nodes := m.plotNodesAtCol(m.plotFocusCol)
						cur := m.plotColCursorAt(m.plotFocusCol)
						if cur >= len(nodes) {
							m.setPlotColCursor(m.plotFocusCol, max(0, len(nodes)-1))
						}
						// clear child columns
						m.resetPlotColsAfter(m.plotFocusCol)
						m.plotConfirm = false
					case "n", "esc":
						m.plotConfirm = false
					}
					return m, nil
				}
				// sidebar focus: navigate campaign list
				if m.plotSideFocus {
					switch msg.String() {
					case "tab":
						m.plotSideFocus = false
						m.campInput.Blur()
						m.mode = worldView
						return m, nil
					case "j", "down":
						if m.plotSideCursor < len(m.campaign.Campaigns)-1 {
							m.plotSideCursor++
						}
					case "k", "up":
						if m.plotSideCursor > 0 {
							m.plotSideCursor--
						}
					case "right", "l", "enter":
						m.plotCampIdx = m.plotSideCursor
						m.plotFocusCol = 0
						m.plotViewStart = 0
						m.plotColCursor = []int{0}
						m.plotColOffset = []int{0}
						m.plotSideFocus = false
					}
					return m, nil
				}
				// plot search
				if m.plotSearchActive {
					switch msg.String() {
					case "esc":
						m.plotSearchActive = false
						m.plotSearchQuery = ""
					case "enter":
						m.plotSearchActive = false
					case "backspace":
						if len(m.plotSearchQuery) > 0 {
							runes := []rune(m.plotSearchQuery)
							m.plotSearchQuery = string(runes[:len(runes)-1])
						}
					default:
						if len(msg.Runes) == 1 {
							m.plotSearchQuery += string(msg.Runes)
						}
					}
					return m, nil
				}
				// normal plot navigation
				switch msg.String() {
				case "ctrl+c":
					return m, tea.Quit
				case "/":
					m.plotSearchActive = true
					m.plotSearchQuery = ""
					return m, nil
				case "tab":
					m.campInput.Blur()
					m.mode = worldView
					return m, nil
				case "j", "down":
					nodes := m.plotNodesAtCol(m.plotFocusCol)
					cur := m.plotColCursorAt(m.plotFocusCol)
					if cur < len(nodes)-1 {
						m.setPlotColCursor(m.plotFocusCol, cur+1)
						m.resetPlotColsAfter(m.plotFocusCol)
					}
				case "k", "up":
					cur := m.plotColCursorAt(m.plotFocusCol)
					if cur > 0 {
						m.setPlotColCursor(m.plotFocusCol, cur-1)
						m.resetPlotColsAfter(m.plotFocusCol)
					}
				case "right", "l", "enter":
					// move focus to next column (children of current node)
					m.plotFocusCol++
					m.ensurePlotCol(m.plotFocusCol)
					if m.plotFocusCol-m.plotViewStart >= 3 {
						m.plotViewStart = m.plotFocusCol - 2
					}
				case "left", "h":
					if m.plotFocusCol > 0 {
						m.plotFocusCol--
						if m.plotFocusCol < m.plotViewStart {
							m.plotViewStart = m.plotFocusCol
						}
					} else {
						m.plotSideFocus = true
					}
				case " ":
					cur := m.plotCurrentNode()
					if cur != nil {
						cur.Chosen = !cur.Chosen
						m.campaign.Campaigns[m.plotCampIdx].Plot =
							m.plotSetNode(m.campaign.Campaigns[m.plotCampIdx].Plot,
								m.plotCurrentPath(), *cur)
						m.saveCampaign()
					}
				case "n":
					// add node in the current column
					m.plotAdding = true
					m.campInput.SetValue("")
					m.campInput.Focus()
				case "r":
					cur := m.plotCurrentNode()
					if cur != nil {
						m.campInput.SetValue(cur.Title)
						m.campInput.CursorEnd()
						m.campInput.Focus()
						m.plotRenaming = true
					}
				case "e":
					cur := m.plotCurrentNode()
					if cur != nil {
						m.campInput.SetValue(cur.Description)
						m.campInput.CursorEnd()
						m.campInput.Focus()
						m.plotEditField = "desc"
						m.plotEditing = true
					}
				case "c":
					cur := m.plotCurrentNode()
					if cur != nil {
						m.campInput.SetValue(cur.Consequence)
						m.campInput.CursorEnd()
						m.campInput.Focus()
						m.plotEditField = "consequence"
						m.plotEditing = true
					}
				case "d":
					if m.plotCurrentNode() != nil {
						m.plotConfirm = true
					}
				case "ctrl+r":
					cur := m.plotCurrentNode()
					if cur != nil {
						m.plotRefPicking = true
						m.campRefPicking = true
						m.campRefInsert = true
						m.campRefSearch = ""
						m.campRefCursor = 0
					}
				}
				return m, nil
			}

			// ── reference picker ─────────────────────────────────
			if m.campRefPicking {
				filtered := m.campRefFiltered()
				tags := m.campRefMatchingTags()
				inTagMode := m.campRefInTagMode()
				switch msg.String() {
				case "ctrl+c":
					return m, tea.Quit
				case "esc":
					if m.campRefTagFilter != "" {
						// go back to tag browsing
						m.campRefTagFilter = ""
						m.campRefSearch = "#"
						m.campRefCursor = 0
					} else if m.campRefSearch != "" {
						m.campRefSearch = ""
						m.campRefCursor = 0
					} else {
						m.campRefPicking = false
						m.plotRefPicking = false
					}
				case "enter":
					if m.campRefInsert {
						if inTagMode {
							// select a tag → move to article phase
							if m.campRefCursor < len(tags) {
								m.campRefTagFilter = tags[m.campRefCursor]
								m.campRefSearch = ""
								m.campRefCursor = 0
							}
						} else {
							// insert the selected article
							if m.campRefCursor < len(filtered) {
								res := filtered[m.campRefCursor]
								if m.plotRefPicking {
									// save as wiki link on current plot node
									cur := m.plotCurrentNode()
									if cur != nil {
										cur.WikiRef = res.ID
										cur.WikiName = res.Name
										m.campaign.Campaigns[m.plotCampIdx].Plot =
											m.plotSetNode(m.campaign.Campaigns[m.plotCampIdx].Plot,
												m.plotCurrentPath(), *cur)
										m.saveCampaign()
									}
									m.plotRefPicking = false
								} else {
									m.ta.SetValue(m.ta.Value() + "[" + res.Name + "]")
								}
							}
							m.campRefPicking = false
							m.campRefSearch = ""
							m.campRefTagFilter = ""
						}
					} else {
						// follow mode: digit input → jump by number
						if m.campRefSearch != "" {
							idx := 0
							fmt.Sscanf(m.campRefSearch, "%d", &idx)
							idx--
							if idx >= 0 && idx < len(filtered) {
								m.campRefPicking = false
								m.campRefSearch = ""
								m.mode = worldView
								m.openByID(filtered[idx].ID, true)
							} else {
								m.campRefSearch = ""
							}
						} else if m.campRefCursor < len(filtered) {
							r := filtered[m.campRefCursor]
							m.campRefPicking = false
							m.campRefSearch = ""
							m.mode = worldView
							m.openByID(r.ID, true)
						} else {
							m.campRefPicking = false
						}
					}
				case "up", "k":
					if inTagMode {
						if m.campRefCursor > 0 {
							m.campRefCursor--
						}
					} else if m.campRefInsert {
						m.campRefSearch += string(msg.Runes)
						m.campRefCursor = 0
					} else if m.campRefCursor > 0 {
						m.campRefCursor--
					}
				case "down", "j":
					listLen := len(filtered)
					if inTagMode {
						listLen = len(tags)
					}
					if inTagMode {
						if m.campRefCursor < listLen-1 {
							m.campRefCursor++
						}
					} else if m.campRefInsert {
						m.campRefSearch += string(msg.Runes)
						m.campRefCursor = 0
					} else if m.campRefCursor < listLen-1 {
						m.campRefCursor++
					}
				case "backspace":
					if len(m.campRefSearch) > 0 {
						runes := []rune(m.campRefSearch)
						m.campRefSearch = string(runes[:len(runes)-1])
						if m.campRefInsert {
							m.campRefCursor = 0
						}
					}
				default:
					if len(msg.Runes) == 1 {
						if m.campRefInsert {
							m.campRefSearch += string(msg.Runes)
							m.campRefCursor = 0
						} else if msg.Runes[0] >= '0' && msg.Runes[0] <= '9' {
							// digit buffer for follow mode
							m.campRefSearch += string(msg.Runes[0])
						}
					}
				}
				return m, nil
			}

			// ── confirm-delete state ──────────────────────────────
			if m.campConfirm {
				switch msg.String() {
				case "j":
					item := m.campCurrentItem()
					if item != nil {
						switch item.kind {
						case campKindCampaign:
							if len(m.campaign.Campaigns) > 1 {
								ci := item.campIdx
								m.campaign.Campaigns = append(m.campaign.Campaigns[:ci], m.campaign.Campaigns[ci+1:]...)
								// Fix expanded map keys
								newExp := make(map[int]bool)
								for k, v := range m.campExpanded {
									if k < ci {
										newExp[k] = v
									} else if k > ci {
										newExp[k-1] = v
									}
								}
								m.campExpanded = newExp
								m.saveCampaign()
								m.buildCampItems()
								if m.campCursor >= len(m.campItems) {
									m.campCursor = len(m.campItems) - 1
								}
								if m.campCursor < 0 {
									m.campCursor = 0
								}
							}
						case campKindPlayer:
							ci := item.campIdx
							pi := item.playerIdx
							camp := &m.campaign.Campaigns[ci]
							camp.Players = append(camp.Players[:pi], camp.Players[pi+1:]...)
							m.saveCampaign()
							m.buildCampItems()
							if m.campCursor >= len(m.campItems) {
								m.campCursor = len(m.campItems) - 1
							}
							if m.campCursor < 0 {
								m.campCursor = 0
							}
						case campKindSession:
							ci := item.campIdx
							si := item.playerIdx
							camp := &m.campaign.Campaigns[ci]
							camp.Sessions = append(camp.Sessions[:si], camp.Sessions[si+1:]...)
							m.saveCampaign()
							m.buildCampItems()
							if m.campCursor >= len(m.campItems) {
								m.campCursor = len(m.campItems) - 1
							}
							if m.campCursor < 0 {
								m.campCursor = 0
							}
						// campKindGeneral: no-op
						}
					}
					m.campConfirm = false
				case "n", "esc":
					m.campConfirm = false
				}
				return m, nil
			}

			// ── renaming state ────────────────────────────────────
			if m.campRenaming {
				switch msg.String() {
				case "ctrl+c":
					return m, tea.Quit
				case "esc":
					m.campRenaming = false
					m.campInput.Blur()
				case "enter":
					name := strings.TrimSpace(m.campInput.Value())
					if name != "" {
						item := m.campCurrentItem()
						if item != nil {
							switch item.kind {
							case campKindCampaign:
								m.campaign.Campaigns[item.campIdx].Name = name
							case campKindPlayer:
								m.campaign.Campaigns[item.campIdx].Players[item.playerIdx].Name = name
							case campKindSession:
								m.campaign.Campaigns[item.campIdx].Sessions[item.playerIdx].Name = name
							}
							m.saveCampaign()
							m.buildCampItems()
						}
					}
					m.campRenaming = false
					m.campInput.Blur()
				default:
					var cmd tea.Cmd
					m.campInput, cmd = m.campInput.Update(msg)
					cmds = append(cmds, cmd)
				}
				return m, tea.Batch(cmds...)
			}

			// ── adding state ──────────────────────────────────────
			if m.campAdding {
				switch msg.String() {
				case "ctrl+c":
					return m, tea.Quit
				case "esc":
					m.campAdding = false
					m.campInput.Blur()
				case "enter":
					name := strings.TrimSpace(m.campInput.Value())
					if name != "" {
						if m.campAddKind == campKindCampaign {
							m.campaign.Campaigns = append(m.campaign.Campaigns, Campaign{Name: name})
							newIdx := len(m.campaign.Campaigns) - 1
							m.campExpanded[newIdx] = true
							m.saveCampaign()
							m.buildCampItems()
							// Move cursor to new campaign header
							for i, it := range m.campItems {
								if it.kind == campKindCampaign && it.campIdx == newIdx {
									m.campCursor = i
									m.clampCampOffset()
									break
								}
							}
						} else if m.campAddKind == campKindPlayer {
							item := m.campCurrentItem()
							ci := 0
							if item != nil {
								ci = item.campIdx
							}
							m.campaign.Campaigns[ci].Players = append(
								m.campaign.Campaigns[ci].Players, Player{Name: name},
							)
							m.saveCampaign()
							m.buildCampItems()
							newPIdx := len(m.campaign.Campaigns[ci].Players) - 1
							for i, it := range m.campItems {
								if it.kind == campKindPlayer && it.campIdx == ci && it.playerIdx == newPIdx {
									m.campCursor = i
									m.clampCampOffset()
									break
								}
							}
						} else if m.campAddKind == campKindSession {
							item := m.campCurrentItem()
							ci := 0
							if item != nil {
								ci = item.campIdx
							}
							m.campaign.Campaigns[ci].Sessions = append(
								m.campaign.Campaigns[ci].Sessions, Session{Name: name},
							)
							m.saveCampaign()
							m.buildCampItems()
							newSIdx := len(m.campaign.Campaigns[ci].Sessions) - 1
							for i, it := range m.campItems {
								if it.kind == campKindSession && it.campIdx == ci && it.playerIdx == newSIdx {
									m.campCursor = i
									m.clampCampOffset()
									break
								}
							}
						}
					}
					m.campAdding = false
					m.campInput.Blur()
				default:
					var cmd tea.Cmd
					m.campInput, cmd = m.campInput.Update(msg)
					cmds = append(cmds, cmd)
				}
				return m, tea.Batch(cmds...)
			}

			// ── task adding ───────────────────────────────────────
			if m.campTaskAdding {
				switch msg.String() {
				case "ctrl+c":
					return m, tea.Quit
				case "esc":
					m.campTaskAdding = false
					m.campInput.Blur()
				case "enter":
					name := strings.TrimSpace(m.campInput.Value())
					if name != "" {
						item := m.campCurrentItem()
						ci := 0
						if item != nil {
							ci = item.campIdx
						}
						m.campaign.Campaigns[ci].Tasks = append(
							m.campaign.Campaigns[ci].Tasks, Task{Name: name},
						)
						m.saveCampaign()
						m.campTaskCursor = len(m.campaign.Campaigns[ci].Tasks) - 1
						m.campTaskFocus = true
					}
					m.campTaskAdding = false
					m.campInput.Blur()
				default:
					var cmd tea.Cmd
					m.campInput, cmd = m.campInput.Update(msg)
					cmds = append(cmds, cmd)
				}
				return m, tea.Batch(cmds...)
			}

			// ── blurb editing ─────────────────────────────────────
			if m.campBlurbEditing {
				switch msg.String() {
				case "ctrl+c":
					return m, tea.Quit
				case "esc":
					m.campBlurbEditing = false
					m.campBlurbTA.Blur()
				case "ctrl+s":
					item := m.campCurrentItem()
					if item != nil {
						val := strings.TrimSpace(m.campBlurbTA.Value())
						switch item.kind {
						case campKindPlayer:
							m.campaign.Campaigns[item.campIdx].Players[item.playerIdx].Blurb = val
						case campKindSession:
							m.campaign.Campaigns[item.campIdx].Sessions[item.playerIdx].Blurb = val
						}
						m.saveCampaign()
					}
					m.campBlurbEditing = false
					m.campBlurbTA.Blur()
				default:
					var cmd tea.Cmd
					m.campBlurbTA, cmd = m.campBlurbTA.Update(msg)
					cmds = append(cmds, cmd)
				}
				return m, tea.Batch(cmds...)
			}

			// ── task focus ────────────────────────────────────────
			if m.campTaskFocus {
				item := m.campCurrentItem()
				ci := 0
				if item != nil {
					ci = item.campIdx
				}
				tasks := m.campaign.Campaigns[ci].Tasks
				switch msg.String() {
				case "ctrl+c":
					return m, tea.Quit
				case "esc":
					m.campTaskFocus = false
				case "j", "down":
					if m.campTaskCursor < len(tasks)-1 {
						m.campTaskCursor++
					}
				case "k", "up":
					if m.campTaskCursor > 0 {
						m.campTaskCursor--
					}
				case " ", "enter":
					if m.campTaskCursor < len(tasks) {
						m.campaign.Campaigns[ci].Tasks[m.campTaskCursor].Done = !m.campaign.Campaigns[ci].Tasks[m.campTaskCursor].Done
						m.saveCampaign()
					}
				case "d":
					if m.campTaskCursor < len(tasks) {
						t := m.campaign.Campaigns[ci].Tasks
						m.campaign.Campaigns[ci].Tasks = append(t[:m.campTaskCursor], t[m.campTaskCursor+1:]...)
						m.saveCampaign()
						if m.campTaskCursor >= len(m.campaign.Campaigns[ci].Tasks) {
							m.campTaskCursor = len(m.campaign.Campaigns[ci].Tasks) - 1
						}
						if m.campTaskCursor < 0 {
							m.campTaskCursor = 0
						}
					}
				case "t":
					m.campTaskFocus = false
					m.campTaskAdding = true
					m.campInput.SetValue("")
					m.campInput.Focus()
				}
				return m, nil
			}

			// ── global keys (work even while editing) ─────────────
			switch msg.String() {
			case "ctrl+c", "q":
				return m, tea.Quit
			case "tab":
				if m.mode == plotView {
					m.campInput.Blur()
					m.mode = worldView
					return m, tea.Batch(cmds...)
				}
				if m.campEditing {
					m.campCloseEditor()
				}
				m.campAdding = false
				m.campInput.Blur()
				m.mode = plotView
				return m, tea.Batch(cmds...)
			case "esc":
				if m.mode == plotView {
					m.mode = worldView
					return m, nil
				}
				if m.campEditing {
					m.campCloseEditor()
					return m, nil
				}
				m.mode = worldView
				return m, nil
			case "enter":
				if m.campEditing {
					m.campCloseEditor()
					return m, nil
				}
				item := m.campCurrentItem()
				if item == nil {
					return m, nil
				}
				switch item.kind {
				case campKindCampaign:
					if !m.campExpanded[item.campIdx] {
						m.campExpanded[item.campIdx] = true
						m.buildCampItems()
						if m.campCursor >= len(m.campItems) {
							m.campCursor = len(m.campItems) - 1
						}
					} else {
						m.campTaskFocus = true
						m.campTaskCursor = 0
					}
				case campKindGeneral, campKindPlayer, campKindSession:
					m.campOpenEditor()
				}
				return m, nil
			}

			// ── editing: pass all other keys to textarea ──────────
			if m.campEditing {
				if msg.String() == "ctrl+r" {
					m.campRefPicking = true
					m.campRefInsert = true
					m.campRefSearch = ""
					m.campRefCursor = 0
					return m, nil
				}
				var cmd tea.Cmd
				m.ta, cmd = m.ta.Update(msg)
				cmds = append(cmds, cmd)
				return m, tea.Batch(cmds...)
			}

			// ── campaign search ───────────────────────────────────
			if m.campSearchActive {
				switch msg.String() {
				case "esc":
					m.campSearchActive = false
					m.campSearchQuery = ""
					m.campCursor = 0
					m.campListOffset = 0
				case "enter":
					m.campSearchActive = false
				case "backspace":
					if len(m.campSearchQuery) > 0 {
						runes := []rune(m.campSearchQuery)
						m.campSearchQuery = string(runes[:len(runes)-1])
					}
					m.campCursor = 0
					m.campListOffset = 0
				default:
					if len(msg.Runes) == 1 {
						m.campSearchQuery += string(msg.Runes)
						m.campCursor = 0
						m.campListOffset = 0
					}
				}
				return m, nil
			}

			// ── normal navigation ─────────────────────────────────
			switch msg.String() {
			case "/":
				m.campSearchActive = true
				m.campSearchQuery = ""
				m.campCursor = 0
				m.campListOffset = 0
				return m, nil
			case "up", "k":
				if m.campCursor > 0 {
					m.campCursor--
					m.campTaskFocus = false
					m.clampCampOffset()
				}
			case "down", "j":
				if m.campCursor < len(m.campFilteredItems())-1 {
					m.campCursor++
					m.campTaskFocus = false
					m.clampCampOffset()
				}
			case "right", "l":
				item := m.campCurrentItem()
				if item != nil && item.kind == campKindCampaign {
					m.campExpanded[item.campIdx] = true
					m.buildCampItems()
					m.clampCampOffset()
				}
			case "left", "h":
				item := m.campCurrentItem()
				if item != nil && item.kind == campKindCampaign {
					m.campExpanded[item.campIdx] = false
					m.buildCampItems()
					m.clampCampOffset()
				}
			case "a":
				// Add player to the campaign of the current item
				item := m.campCurrentItem()
				if item != nil {
					m.campAddKind = campKindPlayer
					m.campAdding = true
					m.campInput.SetValue("")
					m.campInput.Focus()
				}
			case "f":
				if m.campRefPicking && !m.campRefInsert {
					m.campRefPicking = false
					m.campRefSearch = ""
				} else {
					note := m.campCurrentNote()
					if note != "" {
						refs := m.campNoteRefs(note)
						if len(refs) > 0 {
							m.campRefPicking = true
							m.campRefInsert = false
							m.campRefSearch = ""
							m.campRefCursor = 0
						}
					}
				}
			case "i":
				item := m.campCurrentItem()
				if item != nil {
					m.initCampIdx = item.campIdx
					m.initCursor = 0
					m.showInitiative = true
				}
			case "c":
				m.campAddKind = campKindCampaign
				m.campAdding = true
				m.campInput.SetValue("")
				m.campInput.Focus()
			case "s":
				item := m.campCurrentItem()
				if item != nil {
					m.campAddKind = campKindSession
					m.campAdding = true
					defaultName := fmt.Sprintf("Session %d", len(m.campaign.Campaigns[item.campIdx].Sessions)+1)
					m.campInput.SetValue(defaultName)
					m.campInput.CursorEnd()
					m.campInput.Focus()
				}
			case "t":
				m.campTaskAdding = true
				m.campInput.SetValue("")
				m.campInput.Focus()
			case "b":
				item := m.campCurrentItem()
				if item != nil && (item.kind == campKindPlayer || item.kind == campKindSession) {
					var existing string
					switch item.kind {
					case campKindPlayer:
						existing = m.campaign.Campaigns[item.campIdx].Players[item.playerIdx].Blurb
					case campKindSession:
						existing = m.campaign.Campaigns[item.campIdx].Sessions[item.playerIdx].Blurb
					}
					m.campBlurbTA.SetValue(existing)
					m.campBlurbTA.Focus()
					m.campBlurbEditing = true
				}
			case "r":
				item := m.campCurrentItem()
				if item != nil && item.kind != campKindGeneral {
					var existing string
					switch item.kind {
					case campKindCampaign:
						existing = m.campaign.Campaigns[item.campIdx].Name
					case campKindPlayer:
						existing = m.campaign.Campaigns[item.campIdx].Players[item.playerIdx].Name
					case campKindSession:
						existing = m.campaign.Campaigns[item.campIdx].Sessions[item.playerIdx].Name
					}
					m.campInput.SetValue(existing)
					m.campInput.CursorEnd()
					m.campInput.Focus()
					m.campRenaming = true
				}
			case "d":
				item := m.campCurrentItem()
				if item == nil || item.kind == campKindGeneral {
					break
				}
				if item.kind == campKindCampaign && len(m.campaign.Campaigns) <= 1 {
					break // can't delete the last campaign
				}
				m.campConfirm = true
			}
			return m, nil
		}

		// Global keys
		switch msg.String() {
		case "ctrl+c", "q":
			if m.focus != focusSearch {
				return m, tea.Quit
			}
		case "/", "ctrl+f":
			m.focus = focusSearch
			return m, nil
		case "esc":
			if m.focus == focusSearch {
				m.focus = focusList
				if m.searchQuery != "" {
					m.searchQuery = ""
					m.items = m.fullTree
					m.cursor = 0
					m.listOffset = 0
				}
				return m, nil
			}
			if m.showMentions {
				m.showMentions = false
				return m, nil
			}
		case "ctrl+left":
			if m.historyPos > 0 {
				m.historyPos--
				m.openByID(m.history[m.historyPos], false)
			}
			return m, nil
		case "ctrl+right":
			if m.historyPos < len(m.history)-1 {
				m.historyPos++
				m.openByID(m.history[m.historyPos], false)
			}
			return m, nil
		case "tab":
			switch m.mode {
			case worldView:
				m.mode = campaignView
				m.campEditing = false
				m.ta.Blur()
			case campaignView:
				if m.campEditing {
					m.campCloseEditor()
				}
				m.campAdding = false
				m.campInput.Blur()
				m.mode = plotView
			default:
				m.campInput.Blur()
				m.mode = worldView
			}
			return m, nil
		case "f":
			if m.selected != nil && len(m.mentions) > 0 {
				m.showMentions = !m.showMentions
				if m.showMentions {
					m.mentionCursor = 0
					m.mentionInput = ""
				}
			}
			return m, nil
		}

		// Mentions overlay keys (active regardless of focus)
		if m.showMentions {
			switch msg.String() {
			case "esc":
				if m.mentionInput != "" {
					m.mentionInput = ""
				} else {
					m.showMentions = false
				}
			case "enter":
				if m.mentionInput != "" {
					idx := 0
					fmt.Sscanf(m.mentionInput, "%d", &idx)
					idx--
					if idx >= 0 && idx < len(m.mentions) {
						m.mentionInput = ""
						m.jumpToMention(m.mentions[idx].resource)
					} else {
						m.mentionInput = ""
					}
				} else {
					m.showMentions = false
				}
			case "backspace":
				if len(m.mentionInput) > 0 {
					m.mentionInput = m.mentionInput[:len(m.mentionInput)-1]
				}
			case "up", "k":
				if m.mentionInput == "" && m.mentionCursor > 0 {
					m.mentionCursor--
				}
			case "down", "j":
				if m.mentionInput == "" && m.mentionCursor < len(m.mentions)-1 {
					m.mentionCursor++
				}
			default:
				if len(msg.Runes) == 1 && msg.Runes[0] >= '0' && msg.Runes[0] <= '9' {
					m.mentionInput += string(msg.Runes[0])
				}
			}
			return m, nil
		}

		// Focus-specific keys
		switch m.focus {
		case focusList:
			switch msg.String() {
			case "up", "k":
				if m.cursor > 0 {
					m.cursor--
					m.clampOffset()
					m.selectCurrent()
				}
			case "down", "j":
				if m.cursor < len(m.items)-1 {
					m.cursor++
					m.clampOffset()
					m.selectCurrent()
				}
			case "g":
				m.cursor = 0
				m.listOffset = 0
				m.selectCurrent()
			case "G":
				m.cursor = len(m.items) - 1
				m.clampOffset()
				m.selectCurrent()
			case "ctrl+d", "pgdown":
				m.vp.HalfViewDown()
				return m, nil
			case "ctrl+u", "pgup":
				m.vp.HalfViewUp()
				return m, nil
			case "enter":
				m.selectCurrent()
			case "right", "l":
				if len(m.items) > 0 {
					r := m.items[m.cursor].Resource
					if len(m.children[r.ID]) > 0 && !m.expanded[r.ID] {
						m.expanded[r.ID] = true
						m.items = buildVisibleTree(m.idMap, m.children, m.expanded, "__root__", 0)
						m.clampOffset()
					}
				}
			case "left", "h":
				if len(m.items) > 0 {
					r := m.items[m.cursor].Resource
					if m.expanded[r.ID] {
						// Collapse this node
						delete(m.expanded, r.ID)
						m.items = buildVisibleTree(m.idMap, m.children, m.expanded, "__root__", 0)
						m.clampOffset()
					} else if r.ParentID != "" {
						// Jump to parent and collapse it
						delete(m.expanded, r.ParentID)
						m.items = buildVisibleTree(m.idMap, m.children, m.expanded, "__root__", 0)
						for i, node := range m.items {
							if node.Resource.ID == r.ParentID {
								m.cursor = i
								break
							}
						}
						m.clampOffset()
					}
				}
			}

		case focusSearch:
			switch msg.String() {
			case "enter", "down":
				m.focus = focusList
				return m, nil
			case "backspace":
				if len(m.searchQuery) > 0 {
					runes := []rune(m.searchQuery)
					m.searchQuery = string(runes[:len(runes)-1])
					m.doSearch(m.searchQuery)
				}
			default:
				// Accept printable single characters
				if len(msg.Runes) == 1 {
					m.searchQuery += string(msg.Runes)
					m.doSearch(m.searchQuery)
				}
			}

		}
	}

	return m, tea.Batch(cmds...)
}

func (m *model) sidebarWidth() int {
	w := m.width * 30 / 100
	if w < 25 {
		w = 25
	}
	if w > 50 {
		w = 50
	}
	return w
}

func (m *model) visibleLines() int {
	// header(1) + tabBar(1) + borderLine(1) + searchLine(1) + sideHeader w/ bottom border(2) = 6 overhead
	return m.height - 6
}

func (m model) renderTabBar() string {
	type tab struct {
		label string
		mode  viewMode
		color lipgloss.Color
	}
	tabs := []tab{
		{"  Wiki  ", worldView, colorTeal},
		{"  Kampagner  ", campaignView, colorGreen},
		{"  Plottråde  ", plotView, colorGold},
	}
	var parts []string
	for _, t := range tabs {
		if t.mode == m.mode {
			parts = append(parts, lipgloss.NewStyle().
				Background(t.color).Foreground(colorBg).Bold(true).
				Render(t.label))
		} else {
			parts = append(parts, lipgloss.NewStyle().
				Background(colorFaint).Foreground(colorMuted).
				Render(t.label))
		}
	}
	bar := strings.Join(parts, lipgloss.NewStyle().Background(colorBg).Render(" "))
	return lipgloss.NewStyle().Background(colorBg).Width(m.width).Padding(0, 1).Render(bar)
}

func (m *model) clampOffset() {
	vis := m.visibleLines()
	if m.cursor < m.listOffset {
		m.listOffset = m.cursor
	}
	if m.cursor >= m.listOffset+vis {
		m.listOffset = m.cursor - vis + 1
	}
}

func (m *model) clampCampOffset() {
	vis := m.visibleLines() - 1 // -1 for sidebar header
	if vis < 1 {
		vis = 1
	}
	if m.campCursor < m.campListOffset {
		m.campListOffset = m.campCursor
	}
	if m.campCursor >= m.campListOffset+vis {
		m.campListOffset = m.campCursor - vis + 1
	}
}

func (m *model) pushHistory(id string) {
	// Truncate forward history if we navigated back
	if m.historyPos < len(m.history)-1 {
		m.history = m.history[:m.historyPos+1]
	}
	// Don't push duplicate of current
	if len(m.history) == 0 || m.history[len(m.history)-1] != id {
		m.history = append(m.history, id)
		m.historyPos = len(m.history) - 1
	}
}

func (m *model) openByID(id string, addToHistory bool) {
	r, ok := m.idMap[id]
	if !ok {
		return
	}
	if addToHistory {
		m.pushHistory(id)
	}
	m.selected = r
	m.loadContent(r)
	// Sync sidebar cursor to this resource
	m.expandToResource(id)
	m.items = buildVisibleTree(m.idMap, m.children, m.expanded, "__root__", 0)
	for i, node := range m.items {
		if node.Resource.ID == id {
			m.cursor = i
			m.clampOffset()
			break
		}
	}
}

func (m *model) loadContent(r *Resource) {
	var sb strings.Builder
	tags := r.Tags
	if len(tags) > 0 {
		sb.WriteString("Tags: " + strings.Join(tags, ", ") + "\n\n")
	}
	for _, doc := range r.Documents {
		if doc.IsHidden() {
			continue
		}
		if len(r.Documents) > 1 {
			sb.WriteString("── " + doc.Name + " ──\n\n")
		}
		sb.WriteString(renderContent(doc.Content))
		sb.WriteString("\n\n")
	}
	for _, prop := range r.Properties {
		title, _ := prop["title"].(string)
		ptype, _ := prop["type"].(string)
		switch ptype {
		case "TEXT_FIELD":
			data, _ := prop["data"].(map[string]interface{})
			frag, _ := data["fragment"].(map[string]interface{})
			text := strings.TrimSpace(renderContent(frag))
			if text != "" && text != "(no content)" {
				sb.WriteString("**" + title + ":** " + text + "\n")
			}
		case "SELECT":
			data, _ := prop["data"].(map[string]interface{})
			val, _ := data["value"].(string)
			if val != "" {
				sb.WriteString("**" + title + ":** " + val + "\n")
			}
		}
	}
	m.content = sb.String()
	contentWidth := m.width - m.sidebarWidth() - 4
	m.vp.SetContent(styleContent(m.wrapContent(m.content, contentWidth)))
	m.vp.GotoTop()
	seen := map[string]bool{}
	m.mentions = nil
	m.showMentions = false
	m.mentionCursor = 0
	for _, doc := range r.Documents {
		for _, name := range extractMentionNames(doc.Content) {
			if seen[name] {
				continue
			}
			seen[name] = true
			for i := range m.resources {
				if strings.EqualFold(m.resources[i].Name, name) {
					m.mentions = append(m.mentions, mentionLink{name: name, resource: &m.resources[i]})
					break
				}
			}
		}
	}
}

func (m *model) selectCurrent() {
	if len(m.items) == 0 {
		return
	}
	r := m.items[m.cursor].Resource
	m.selected = r
	m.pushHistory(r.ID)
	m.loadContent(r)
}

func (m *model) doSearch(query string) {
	query = strings.TrimSpace(query)
	if query == m.lastQuery {
		return
	}
	m.lastQuery = query
	m.cursor = 0
	m.listOffset = 0

	if query == "" {
		m.items = buildVisibleTree(m.idMap, m.children, m.expanded, "__root__", 0)
		return
	}

	q := strings.ToLower(query)
	var results []TreeNode

	if strings.HasPrefix(q, "#") {
		tag := q[1:]
		for i := range m.resources {
			r := &m.resources[i]
			for _, t := range r.Tags {
				if strings.Contains(strings.ToLower(t), tag) {
					results = append(results, TreeNode{Resource: r, Depth: 0})
					break
				}
			}
		}
	} else {
		for i := range m.resources {
			r := &m.resources[i]
			if strings.Contains(m.searchIndex[r.ID], q) {
				results = append(results, TreeNode{Resource: r, Depth: 0})
			}
		}
	}

	m.items = results
}

func (m *model) expandToResource(id string) {
	r, ok := m.idMap[id]
	if !ok || r.ParentID == "" {
		return
	}
	m.expanded[r.ParentID] = true
	m.expandToResource(r.ParentID)
}

func (m *model) jumpToMention(r *Resource) {
	m.searchQuery = ""
	m.lastQuery = ""
	m.showMentions = false
	m.openByID(r.ID, true)
}

func (m model) wrapContent(text string, width int) string {
	if width <= 0 {
		return text
	}
	var sb strings.Builder
	for _, line := range strings.Split(text, "\n") {
		if len(line) <= width {
			sb.WriteString(line + "\n")
			continue
		}
		// Simple word wrap
		words := strings.Fields(line)
		current := ""
		for _, word := range words {
			if current == "" {
				current = word
			} else if len(current)+1+len(word) <= width {
				current += " " + word
			} else {
				sb.WriteString(current + "\n")
				current = word
			}
		}
		if current != "" {
			sb.WriteString(current + "\n")
		}
	}
	return sb.String()
}

// ---------------------------------------------------------------------------
// View
// ---------------------------------------------------------------------------

func (m model) View() string {
	if m.width == 0 {
		return "Loading..."
	}
	if m.showHelp {
		return m.viewHelp()
	}
	if m.mode == campaignView {
		return m.viewCampaign()
	}
	if m.mode == plotView {
		return m.viewPlot()
	}

	sideW := m.sidebarWidth()
	vis := m.visibleLines()

	// ── Header ────────────────────────────────────────────────
	leftAccent := lipgloss.NewStyle().Background(colorTeal).Foreground(colorTeal).Render("  ")
	title := lipgloss.NewStyle().
		Background(colorBg).
		Foreground(colorSelFg).
		Bold(true).
		Padding(0, 2).
		Render("⚔  WIKI")

	var modeLabel string
	var modeBg, modeFg lipgloss.Color
	switch m.focus {
	case focusSearch:
		modeLabel, modeBg, modeFg = " SØGER ", colorAccent, colorBg
	default:
		if m.showMentions {
			modeLabel, modeBg, modeFg = " LINKS ", colorSelBg, colorSelFg
		} else if m.searchQuery != "" {
			modeLabel, modeBg, modeFg = " FILTER ", colorFaint, colorBlue
		} else if m.selected != nil {
			modeLabel, modeBg, modeFg = " LÆSER ", colorSelBg, colorSelFg
		} else {
			modeLabel, modeBg, modeFg = " LISTE ", colorFaint, colorMuted
		}
	}
	modeBadge := lipgloss.NewStyle().Background(modeBg).Foreground(modeFg).Bold(true).Render(modeLabel)

	count := len(m.items)
	total := len(m.resources)
	countBadge := lipgloss.NewStyle().
		Background(colorFaint).
		Foreground(colorBlue).
		Padding(0, 1).
		Render(fmt.Sprintf("%d / %d", count, total))
	right := lipgloss.NewStyle().
		Background(colorBg).
		Padding(0, 1).
		Render(modeBadge + "  " + countBadge + sDim.Render(" artikler"))
	headerGap := m.width - lipgloss.Width(leftAccent) - lipgloss.Width(title) - lipgloss.Width(right)
	if headerGap < 0 {
		headerGap = 0
	}
	header := leftAccent + title + strings.Repeat(" ", headerGap) + right
	header = lipgloss.NewStyle().Background(colorBg).Width(m.width).Render(header) + "\n" + m.renderTabBar()

	// ── Search bar ────────────────────────────────────────────
	divColor := colorTeal
	if m.focus == focusSearch {
		divColor = sBorderActive
	}

	var searchContent string
	if m.focus == focusSearch {
		queryText := lipgloss.NewStyle().Foreground(colorSelFg).Bold(true).Render(m.searchQuery)
		cursor := lipgloss.NewStyle().Background(colorSelFg).Foreground(colorBg).Render(" ")
		label := lipgloss.NewStyle().Foreground(colorTeal).Bold(true).Render("SØGER ")
		searchContent = label + queryText + cursor
	} else if m.searchQuery != "" {
		label := lipgloss.NewStyle().Foreground(colorMuted).Render("/ ")
		searchContent = label + lipgloss.NewStyle().Foreground(colorBright).Render(m.searchQuery)
	} else {
		searchContent = sDim.Render("/  søg...  (#tag for tag-filter)  Ctrl+D/U=scroll indhold")
	}
	if m.notification != "" {
		notif := lipgloss.NewStyle().Foreground(colorGreen).Bold(true).Render(m.notification)
		searchContent = searchContent + strings.Repeat(" ", max(0, m.width-lipgloss.Width(searchContent)-lipgloss.Width(notif)-2)) + notif
	}
	searchLine := lipgloss.NewStyle().
		Background(colorBg).
		Width(m.width).
		Padding(0, 1).
		Render(searchContent)
	borderLine := lipgloss.NewStyle().Foreground(lipgloss.Color(divColor)).Render(strings.Repeat("─", m.width))
	searchBar := borderLine + "\n" + searchLine

	// ── Sidebar items ─────────────────────────────────────────
	listBorderColor := colorTeal

	var sideLabelText string
	if m.searchQuery != "" {
		sideLabelText = fmt.Sprintf("SØGER  %d", len(m.items))
	} else {
		sideLabelText = fmt.Sprintf("WIKI  %d", len(m.resources))
	}
	sideLabel := lipgloss.NewStyle().
		Background(colorSideBg).
		Foreground(lipgloss.Color(listBorderColor)).
		Bold(true).
		Padding(0, 1).
		Render(sideLabelText)
	sideHeader := lipgloss.NewStyle().
		Background(colorSideBg).
		Border(lipgloss.NormalBorder(), false, false, true, false).
		BorderForeground(lipgloss.Color(listBorderColor)).
		Width(sideW).
		Render(sideLabel)

	var sideLines []string
	end := m.listOffset + vis - 1 // -1 for sideHeader
	if end > len(m.items) {
		end = len(m.items)
	}
	for i := m.listOffset; i < end; i++ {
		node := m.items[i]
		r := node.Resource
		indent := strings.Repeat("  ", node.Depth)
		icon := resourceIcon(r.IconGlyph)

		// Compute max width for the name using plain widths only,
		// so ANSI codes in foldIcon don't skew the measurement.
		// Reserve 1 for the ▌ accent on the selected line.
		nameMaxW := sideW - 1 - runewidth.StringWidth(indent) - 2 - runewidth.StringWidth(icon)
		if nameMaxW < 1 {
			nameMaxW = 1
		}
		name := r.Name
		if runewidth.StringWidth(name) > nameMaxW {
			name = runewidth.Truncate(name, nameMaxW-1, "…")
		}

		var foldIcon string
		if len(m.children[r.ID]) > 0 {
			if m.expanded[r.ID] {
				foldIcon = lipgloss.NewStyle().Foreground(colorGold).Render("▾ ")
			} else {
				foldIcon = lipgloss.NewStyle().Foreground(colorMuted).Render("▸ ")
			}
		} else {
			foldIcon = "  "
		}
		line := indent + foldIcon + icon + name

		if i == m.cursor {
			accent := lipgloss.NewStyle().Background(colorSelBg).Foreground(colorSelFg).Render("▌")
			rest := lipgloss.NewStyle().Background(colorSelBg).Foreground(colorSelFg).Bold(true).
				Render(line + strings.Repeat(" ", max(0, sideW-1-visLen(line))))
			sideLines = append(sideLines, accent+rest)
		} else {
			var fg lipgloss.Color
			switch node.Depth {
			case 0:
				fg = colorBright
			case 1:
				fg = colorBlue
			default:
				fg = colorMuted
			}
			sideLines = append(sideLines, lipgloss.NewStyle().Background(colorSideBg).Foreground(fg).Render(line))
		}
	}
	for len(sideLines) < vis-1 {
		sideLines = append(sideLines, lipgloss.NewStyle().Background(colorSideBg).Render(strings.Repeat(" ", sideW)))
	}

	// ── Content pane ──────────────────────────────────────────
	contentBorderColor := colorTeal
	contentW := m.width - sideW - 1

	var contentHeader string
	var contentBody string
	if m.selected != nil {
		scrollPct := fmt.Sprintf("%d%%", int(m.vp.ScrollPercent()*100))
		icon := resourceIcon(m.selected.IconGlyph)
		titleStr := sTitle.Render(icon + " " + m.selected.Name)
		scrollStr := sDim.Render(scrollPct)
		gap := contentW - lipgloss.Width(titleStr) - lipgloss.Width(scrollStr) - 2
		if gap < 0 {
			gap = 0
		}
		contentHeader = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), false, false, true, false).
			BorderForeground(lipgloss.Color(contentBorderColor)).
			Width(contentW).
			Padding(0, 1).
			Render(titleStr + strings.Repeat(" ", gap) + scrollStr)
		if m.showMentions {
			// Mention picker overlay
			var lines []string
			lines = append(lines, "")
			var inputDisplay string
			if m.mentionInput != "" {
				inputDisplay = "  " + lipgloss.NewStyle().Background(colorSelBg).Foreground(colorSelFg).Bold(true).Padding(0, 1).Render("→ "+m.mentionInput) + lipgloss.NewStyle().Background(colorSelFg).Foreground(colorBg).Render(" ") + sDim.Render(" Enter=hop")
			} else {
				inputDisplay = "  " + sDim.Render("skriv nr + Enter  |  Esc=tilbage")
			}
			lines = append(lines, "  "+lipgloss.NewStyle().Foreground(colorSelFg).Bold(true).Render("↗  Henvisninger")+inputDisplay)
			lines = append(lines, "  "+sDim.Render(strings.Repeat("─", contentW-4)))
			for i, ml := range m.mentions {
				num := lipgloss.NewStyle().Background(colorFaint).Foreground(colorGold).Bold(true).Padding(0, 1).Render(fmt.Sprintf("%d", i+1))
				if i == m.mentionCursor {
					accent := lipgloss.NewStyle().Foreground(colorSelFg).Render("▌ ")
					name := lipgloss.NewStyle().Foreground(colorSelFg).Bold(true).Render(ml.name)
					lines = append(lines, "  "+accent+num+" "+name)
				} else {
					name := lipgloss.NewStyle().Foreground(colorAccent).Render(ml.name)
					lines = append(lines, "    "+num+" "+name)
				}
			}
			contentBody = strings.Join(lines, "\n")
		} else {
			contentBody = m.vp.View()
		}
	} else {
		contentHeader = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), false, false, true, false).
			BorderForeground(lipgloss.Color(contentBorderColor)).
			Width(contentW).
			Padding(0, 1).
			Render(sDim.Render("Vælg en artikel"))
		helpKeys := [][]string{
			{"↑↓  j k", "naviger"},
			{"Enter", "åbn artikel"},
			{"/", "søg  (#tag for tag-filter)"},
			{"Tab", "skift panel"},
			{"Esc", "tilbage"},
			{"q", "afslut"},
		}
		var helpLines []string
		helpLines = append(helpLines, "")
		helpLines = append(helpLines, "  "+lipgloss.NewStyle().Foreground(colorSelFg).Bold(true).Render("⚔  Aelden"))
		helpLines = append(helpLines, "  "+sDim.Render("Din verdensbygger"))
		helpLines = append(helpLines, "")
		helpLines = append(helpLines, "  "+sDim.Render("─── Genveje ───────────────"))
		for _, kv := range helpKeys {
			k := lipgloss.NewStyle().Background(colorFaint).Foreground(colorBright).Padding(0, 1).Render(kv[0])
			v := sDim.Render("  " + kv[1])
			helpLines = append(helpLines, "  "+k+v)
		}
		contentBody = strings.Join(helpLines, "\n")
	}

	// ── Combine side + content line by line ───────────────────
	divider := lipgloss.NewStyle().Foreground(lipgloss.Color(listBorderColor)).Render("│")

	sideAllLines := append([]string{sideHeader}, sideLines...)
	contentAllLines := append([]string{contentHeader}, strings.Split(contentBody, "\n")...)

	// Ensure both have same line count
	totalLines := vis + 1
	for len(sideAllLines) < totalLines {
		sideAllLines = append(sideAllLines, "")
	}
	for len(contentAllLines) < totalLines {
		contentAllLines = append(contentAllLines, "")
	}

	var mainLines []string
	for i := 0; i < totalLines; i++ {
		sl := ""
		if i < len(sideAllLines) {
			sl = sideAllLines[i]
		}
		cl := ""
		if i < len(contentAllLines) {
			cl = contentAllLines[i]
		}
		vl := visLen(sl)
		if vl < sideW {
			pad := lipgloss.NewStyle().Background(colorSideBg).Render(strings.Repeat(" ", sideW-vl))
			sl = sl + pad
		}
		mainLines = append(mainLines, sl+divider+cl)
	}
	main := strings.Join(mainLines, "\n")

	return header + "\n" + main + "\n" + searchBar
}

func (m model) viewHelp() string {
	leftAccent := lipgloss.NewStyle().Background(colorAccent).Foreground(colorAccent).Render("  ")
	title := lipgloss.NewStyle().Background(colorBg).Foreground(colorAccent).Bold(true).Padding(0, 2).Render("?  HJÆLP")
	badge := lipgloss.NewStyle().Background(colorAccent).Foreground(colorBg).Bold(true).Render(" HJÆLP ")
	right := lipgloss.NewStyle().Background(colorBg).Padding(0, 1).Render(badge)
	gap := m.width - lipgloss.Width(leftAccent) - lipgloss.Width(title) - lipgloss.Width(right)
	if gap < 0 {
		gap = 0
	}
	header := lipgloss.NewStyle().Background(colorBg).Width(m.width).
		Render(leftAccent + title + strings.Repeat(" ", gap) + right)

	type section struct {
		name string
		keys [][]string
	}
	sections := []section{
		{"Wiki", [][]string{
			{"/", "søg (skriv for at filtrere)"},
			{"↑↓  jk", "naviger liste"},
			{"→  ←", "fold ud / fold ind"},
			{"Enter", "åbn artikel"},
			{"Ctrl+← →", "historik frem/tilbage"},
			{"f", "vis henvisninger"},
			{"Tab", "gå til Kampagner"},
		}},
		{"Kampagner", [][]string{
			{"/", "søg kampagner/spillere/sessioner"},
			{"↑↓  jk", "naviger"},
			{"→  Enter", "åbn / fold ud"},
			{"←", "fold ind"},
			{"a", "ny spiller"},
			{"s", "ny session"},
			{"c", "ny kampagne"},
			{"t", "ny opgave"},
			{"i", "initiativ tracker"},
			{"b", "rediger blurb"},
			{"r", "omdøb"},
			{"d", "slet"},
			{"Tab", "gå til Plottråde"},
		}},
		{"Plottråde", [][]string{
			{"/", "søg noder (fremhæver match)"},
			{"↑↓  jk", "naviger kolonne"},
			{"→  Enter", "gå ind i børn"},
			{"←", "gå tilbage (til kampagnevalg)"},
			{"Space", "marker som valgt/fravalgt"},
			{"n", "ny node"},
			{"r", "omdøb node"},
			{"e", "rediger beskrivelse"},
			{"c", "rediger konsekvens"},
			{"Ctrl+R", "tilknyt wiki-artikel"},
			{"d", "slet node"},
			{"Tab", "gå til Wiki"},
		}},
		{"Generelt", [][]string{
			{"Tab", "skift mellem de tre views"},
			{"?", "åbn/luk denne hjælpeskærm"},
			{"q  Ctrl+C", "afslut programmet"},
		}},
	}

	colW := m.width / 2
	if colW < 30 {
		colW = 30
	}

	var lines []string
	lines = append(lines, "")

	for _, sec := range sections {
		secTitle := lipgloss.NewStyle().Foreground(colorSelFg).Bold(true).Render("  " + sec.name)
		lines = append(lines, secTitle)
		lines = append(lines, "  "+lipgloss.NewStyle().Foreground(colorMuted).Render(strings.Repeat("─", colW-4)))
		for _, kv := range sec.keys {
			k := lipgloss.NewStyle().Background(colorFaint).Foreground(colorBright).Padding(0, 1).Render(kv[0])
			v := sDim.Render("  " + kv[1])
			lines = append(lines, "  "+k+v)
		}
		lines = append(lines, "")
	}

	body := strings.Join(lines, "\n")
	borderLine := lipgloss.NewStyle().Foreground(colorAccent).Render(strings.Repeat("─", m.width))
	hint := lipgloss.NewStyle().Background(colorBg).Width(m.width).Padding(0, 1).Render(sDim.Render("tryk en vilkårlig tast for at lukke"))
	return header + "\n" + body + "\n" + borderLine + "\n" + hint
}

func (m model) viewPlot() string {
	if m.width == 0 {
		return "Loading..."
	}
	sideW := m.sidebarWidth()
	vis := m.visibleLines()
	contentW := m.width - sideW - 1

	// ── Header ────────────────────────────────────────────────
	leftAccent := lipgloss.NewStyle().Background(colorGold).Foreground(colorGold).Render("  ")
	title := lipgloss.NewStyle().Background(colorBg).Foreground(colorGold).Bold(true).Padding(0, 2).Render("🌿  PLOTTRÅDE")
	modeBadge := lipgloss.NewStyle().Background(colorGold).Foreground(colorBg).Bold(true).Render(" PLOTTRÅDE ")
	right := lipgloss.NewStyle().Background(colorBg).Padding(0, 1).Render(modeBadge)
	headerGap := m.width - lipgloss.Width(leftAccent) - lipgloss.Width(title) - lipgloss.Width(right)
	if headerGap < 0 {
		headerGap = 0
	}
	header := leftAccent + title + strings.Repeat(" ", headerGap) + right
	header = lipgloss.NewStyle().Background(colorBg).Width(m.width).Render(header) + "\n" + m.renderTabBar()

	// ── Sidebar: campaign list ────────────────────────────────
	sideLabel := lipgloss.NewStyle().Background(colorSideBg).Foreground(colorGold).Bold(true).Padding(0, 1).
		Render(fmt.Sprintf("PLOTTRÅDE  %d", len(m.campaign.Campaigns)))
	sideHeader := lipgloss.NewStyle().Background(colorSideBg).
		Border(lipgloss.NormalBorder(), false, false, true, false).
		BorderForeground(colorGold).Width(sideW).Render(sideLabel)

	var sideLines []string
	maxVisible := vis - 1
	for i, c := range m.campaign.Campaigns {
		name := c.Name
		maxW := sideW - 2
		if runewidth.StringWidth(name) > maxW {
			name = runewidth.Truncate(name, maxW-1, "…")
		}
		if i == m.plotSideCursor && m.plotSideFocus {
			accent := lipgloss.NewStyle().Background(colorSelBg).Foreground(colorGold).Bold(true).Render("▌")
			rest := lipgloss.NewStyle().Background(colorSelBg).Foreground(colorGold).Bold(true).
				Render(name + strings.Repeat(" ", max(0, sideW-1-visLen(name))))
			sideLines = append(sideLines, accent+rest)
		} else if i == m.plotCampIdx {
			accent := lipgloss.NewStyle().Background(colorSideBg).Foreground(colorGold).Render("▌")
			rest := lipgloss.NewStyle().Background(colorSideBg).Foreground(colorGold).Bold(true).
				Render(name + strings.Repeat(" ", max(0, sideW-1-visLen(name))))
			sideLines = append(sideLines, accent+rest)
		} else {
			sideLines = append(sideLines, lipgloss.NewStyle().Background(colorSideBg).Foreground(colorBright).Render(name))
		}
	}
	for len(sideLines) < maxVisible {
		sideLines = append(sideLines, lipgloss.NewStyle().Background(colorSideBg).Render(strings.Repeat(" ", sideW)))
	}

	// ── Hint bar ──────────────────────────────────────────────
	var pHint string
	if m.plotConfirm {
		pHint = lipgloss.NewStyle().Foreground(colorGold).Bold(true).Render("Slet node (og alle børn)? ") +
			sKey.Render("j") + sDim.Render(" ja  ") + sKey.Render("n / Esc") + sDim.Render(" annuller")
	} else if m.plotAdding {
		pHint = lipgloss.NewStyle().Foreground(colorGold).Render("Ny node: ") + m.campInput.View()
	} else if m.plotRenaming {
		pHint = lipgloss.NewStyle().Foreground(colorGold).Render("Omdøb: ") + m.campInput.View()
	} else if m.plotEditing {
		fieldLabel := "Beskrivelse"
		if m.plotEditField == "consequence" {
			fieldLabel = "Konsekvens"
		}
		pHint = lipgloss.NewStyle().Foreground(colorGold).Render(fieldLabel+": ") + m.campInput.View()
	} else if m.plotSideFocus {
		pHint = sDim.Render("↑↓/jk=vælg kampagne  → Enter=åbn  Tab=artikler")
	} else if m.plotSearchActive {
		queryText := lipgloss.NewStyle().Foreground(colorSelFg).Bold(true).Render(m.plotSearchQuery)
		cursor := lipgloss.NewStyle().Background(colorSelFg).Foreground(colorBg).Render(" ")
		label := lipgloss.NewStyle().Foreground(colorGold).Bold(true).Render("SØGER ")
		pHint = label + queryText + cursor + sDim.Render("  Esc=ryd  Enter=bekræft")
	} else {
		pHint = sDim.Render("/=søg  ↑↓/jk=naviger  → Enter=ind  ←=kampagner  Space=valgt  n=tilføj  e=beskriv  c=konsekvens  r=omdøb  Ctrl+R=wiki  d=slet  Tab=artikler")
	}
	pDivColor := colorGold
	if m.plotSearchActive {
		pDivColor = sBorderActive
	}
	pSearchLine := lipgloss.NewStyle().Background(colorBg).Width(m.width).Padding(0, 1).Render(pHint)
	pBorderLine := lipgloss.NewStyle().Foreground(pDivColor).Render(strings.Repeat("─", m.width))
	pSearchBar := pBorderLine + "\n" + pSearchLine

	// ── Column view ───────────────────────────────────────────
	const numCols = 3
	treeH := (vis * 2) / 3
	if treeH < 5 {
		treeH = 5
	}
	colW := contentW / numCols
	if colW < 10 {
		colW = 10
	}

	colLines := make([][]string, numCols)
	for ci := 0; ci < numCols; ci++ {
		absCol := m.plotViewStart + ci
		nodes := m.plotNodesAtCol(absCol)
		isFocused := absCol == m.plotFocusCol
		cursor := m.plotColCursorAt(absCol)
		offset := m.plotColOffsetAt(absCol)

		var colHeaderStr string
		if absCol == 0 {
			colHeaderStr = "Startpunkter"
		} else {
			parentPath := make([]int, absCol)
			for pi := 0; pi < absCol; pi++ {
				parentPath[pi] = m.plotColCursorAt(pi)
			}
			parent := plotNodeAt(m.campaign.Campaigns[m.plotCampIdx].Plot, parentPath)
			if parent != nil {
				colHeaderStr = parent.Title
			} else {
				colHeaderStr = "…"
			}
		}
		headerFg := colorMuted
		if isFocused {
			headerFg = colorGold
		}
		if runewidth.StringWidth(colHeaderStr) > colW-2 {
			colHeaderStr = runewidth.Truncate(colHeaderStr, colW-3, "…")
		}
		colHeader := lipgloss.NewStyle().Foreground(headerFg).Bold(isFocused).Render(colHeaderStr)
		colLines[ci] = append(colLines[ci], " "+colHeader)
		colLines[ci] = append(colLines[ci], " "+lipgloss.NewStyle().Foreground(headerFg).Render(strings.Repeat("─", colW-2)))

		if len(nodes) == 0 {
			colLines[ci] = append(colLines[ci], " "+sDim.Render("(tom)"))
			if isFocused {
				colLines[ci] = append(colLines[ci], " "+sDim.Render("n=tilføj"))
			}
		} else {
			end := offset + (treeH - 2)
			if end > len(nodes) {
				end = len(nodes)
			}
			if offset > 0 {
				colLines[ci] = append(colLines[ci], " "+lipgloss.NewStyle().Foreground(colorGold).Render("↑"))
			}
			for ni := offset; ni < end; ni++ {
				node := nodes[ni]
				var chosenMark string
				if node.Chosen {
					chosenMark = lipgloss.NewStyle().Foreground(colorGreen).Bold(true).Render("✓ ")
				} else {
					chosenMark = lipgloss.NewStyle().Foreground(colorMuted).Render("○ ")
				}
				hasKids := len(node.Children) > 0
				var arrow string
				if hasKids {
					arrow = lipgloss.NewStyle().Foreground(colorMuted).Render(" →")
				} else {
					arrow = "  "
				}
				maxTitleW := colW - 6
				nodeTitle := node.Title
				if runewidth.StringWidth(nodeTitle) > maxTitleW {
					nodeTitle = runewidth.Truncate(nodeTitle, maxTitleW-1, "…")
				}
				isSelected := isFocused && ni == cursor
				isActive := !isFocused && ni == cursor
				isMatch := m.plotSearchQuery != "" && strings.Contains(strings.ToLower(node.Title), strings.ToLower(m.plotSearchQuery))
				var line string
				if isSelected {
					accent := lipgloss.NewStyle().Foreground(colorGold).Render("▌")
					rest := lipgloss.NewStyle().Background(colorSelBg).Foreground(colorSelFg).Bold(true).
						Render(chosenMark + nodeTitle + strings.Repeat(" ", max(0, colW-2-visLen(chosenMark+nodeTitle+arrow))) + arrow)
					line = accent + rest
				} else if isActive {
					line = " " + lipgloss.NewStyle().Foreground(colorBlue).Render(chosenMark+nodeTitle) + arrow
				} else if isMatch {
					line = " " + lipgloss.NewStyle().Foreground(colorGold).Bold(true).Render("* ") + lipgloss.NewStyle().Foreground(colorSelFg).Bold(true).Render(nodeTitle) + arrow
				} else {
					fg := colorBright
					if node.Chosen {
						fg = colorMuted
					}
					if m.plotSearchQuery != "" {
						fg = colorMuted // dim non-matching nodes
					}
					line = " " + chosenMark + lipgloss.NewStyle().Foreground(fg).Render(nodeTitle) + arrow
				}
				colLines[ci] = append(colLines[ci], line)
			}
			if end < len(nodes) {
				colLines[ci] = append(colLines[ci], " "+lipgloss.NewStyle().Foreground(colorGold).Render("↓"))
			}
		}
		for len(colLines[ci]) < treeH {
			colLines[ci] = append(colLines[ci], "")
		}
	}

	colDiv := lipgloss.NewStyle().Foreground(colorDivider).Render("│")
	var treeRendered []string
	for row := 0; row < treeH; row++ {
		var rowParts []string
		for ci := 0; ci < numCols; ci++ {
			cell := ""
			if row < len(colLines[ci]) {
				cell = colLines[ci][row]
			}
			vl := visLen(cell)
			if vl < colW {
				cell += strings.Repeat(" ", colW-vl)
			}
			if vl > colW {
				cell = runewidth.Truncate(stripANSI(cell), colW, "")
			}
			rowParts = append(rowParts, cell)
		}
		treeRendered = append(treeRendered, strings.Join(rowParts, colDiv))
	}

	// ── Detail pane ───────────────────────────────────────────
	divLine := lipgloss.NewStyle().Foreground(colorMuted).Render(strings.Repeat("─", contentW-2))
	var detailLines []string
	detailLines = append(detailLines, divLine)
	if m.plotEditing {
		fieldLabel := "Beskrivelse"
		if m.plotEditField == "consequence" {
			fieldLabel = "Konsekvens"
		}
		detailLines = append(detailLines, lipgloss.NewStyle().Foreground(colorGold).Bold(true).Render(fieldLabel+": ")+m.campInput.View())
	} else {
		cur := m.plotCurrentNode()
		if cur != nil {
			nodeTitle := lipgloss.NewStyle().Foreground(colorGold).Bold(true).Render(cur.Title)
			detailLines = append(detailLines, nodeTitle)
			if cur.Description != "" {
				detailLines = append(detailLines, sDim.Render("Beskriv: ")+lipgloss.NewStyle().Foreground(colorBright).Render(cur.Description))
			} else {
				detailLines = append(detailLines, sDim.Render("(ingen beskrivelse — e=tilføj)"))
			}
			if cur.Consequence != "" {
				detailLines = append(detailLines, sDim.Render("Konsekvens: ")+lipgloss.NewStyle().Foreground(colorBlue).Render(cur.Consequence))
			} else {
				detailLines = append(detailLines, sDim.Render("(ingen konsekvens — c=tilføj)"))
			}
			if cur.WikiName != "" {
				detailLines = append(detailLines, sDim.Render("🔗 ")+lipgloss.NewStyle().Foreground(colorAccent).Underline(true).Render(cur.WikiName))
			}
		} else {
			detailLines = append(detailLines, sDim.Render("Vælg en node, eller tryk 'n' for at tilføje"))
		}
	}

	pBody := strings.Join(append(treeRendered, detailLines...), "\n")

	// ── Content pane header ───────────────────────────────────
	camp := m.campaign.Campaigns[m.plotCampIdx]
	pContentTitle := lipgloss.NewStyle().Foreground(colorGold).Bold(true).Render(camp.Name)
	pContentHint := sDim.Render("[/]=skift kampagne")
	pGap := contentW - lipgloss.Width(pContentTitle) - lipgloss.Width(pContentHint) - 4
	if pGap < 0 {
		pGap = 0
	}
	pHeader := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), false, false, true, false).
		BorderForeground(colorGold).Width(contentW).Padding(0, 1).
		Render(pContentTitle + strings.Repeat(" ", pGap) + pContentHint)

	// ── Combine sidebar + content line by line ────────────────
	dividerP := lipgloss.NewStyle().Foreground(colorGold).Render("│")
	pSideAll := append([]string{sideHeader}, sideLines...)
	pContAll := append([]string{pHeader}, strings.Split(pBody, "\n")...)
	pTotalLines := vis + 1
	for len(pSideAll) < pTotalLines {
		pSideAll = append(pSideAll, "")
	}
	for len(pContAll) < pTotalLines {
		pContAll = append(pContAll, "")
	}
	var pMainLines []string
	for i := 0; i < pTotalLines; i++ {
		sl := ""
		if i < len(pSideAll) {
			sl = pSideAll[i]
		}
		cl := ""
		if i < len(pContAll) {
			cl = pContAll[i]
		}
		vl := visLen(sl)
		if vl < sideW {
			pad := lipgloss.NewStyle().Background(colorSideBg).Render(strings.Repeat(" ", sideW-vl))
			sl = sl + pad
		}
		pMainLines = append(pMainLines, sl+dividerP+cl)
	}
	return header + "\n" + strings.Join(pMainLines, "\n") + "\n" + pSearchBar
}

func (m model) viewCampaign() string {
	if m.width == 0 {
		return "Loading..."
	}
	sideW := m.sidebarWidth()
	vis := m.visibleLines()

	// Header
	leftAccent := lipgloss.NewStyle().Background(colorGreen).Foreground(colorGreen).Render("  ")
	title := lipgloss.NewStyle().Background(colorBg).Foreground(colorGreen).Bold(true).Padding(0, 2).Render("⚔  KAMPAGNER")
	modeBadge := lipgloss.NewStyle().Background(colorGreen).Foreground(colorBg).Bold(true).Render(" KAMPAGNER ")
	right := lipgloss.NewStyle().Background(colorBg).Padding(0, 1).Render(modeBadge)
	headerGap := m.width - lipgloss.Width(leftAccent) - lipgloss.Width(title) - lipgloss.Width(right)
	if headerGap < 0 {
		headerGap = 0
	}
	header := leftAccent + title + strings.Repeat(" ", headerGap) + right
	header = lipgloss.NewStyle().Background(colorBg).Width(m.width).Render(header) + "\n" + m.renderTabBar()

	// Hint bar
	var hintText string
	if m.campRefPicking {
		inTagMode := m.campRefInTagMode()
		if m.campRefInsert && inTagMode {
			hintText = sDim.Render("skriv tag-navn  |  ↑↓=naviger  |  Enter=vælg  |  Esc=luk")
		} else if m.campRefInsert {
			hintText = sDim.Render("skriv artikelnavn eller #tag  |  ↑↓=naviger  |  Enter=indsæt  |  Esc=luk")
		} else {
			hintText = sDim.Render("skriv nr + Enter  |  ↑↓ jk=naviger  |  Esc=luk")
		}
	} else if m.campConfirm {
		hintText = lipgloss.NewStyle().Foreground(colorGold).Bold(true).Render("Slet? ") +
			sKey.Render("j") + sDim.Render(" ja  ") +
			sKey.Render("n / Esc") + sDim.Render(" annuller")
	} else if m.campRenaming {
		item := m.campCurrentItem()
		prompt := "Omdøb:"
		if item != nil && item.kind == campKindPlayer {
			prompt = "Omdøb spiller:"
		} else if item != nil && item.kind == campKindCampaign {
			prompt = "Omdøb kampagne:"
		} else if item != nil && item.kind == campKindSession {
			prompt = "Omdøb session:"
		}
		hintText = lipgloss.NewStyle().Foreground(colorGold).Render(prompt+" ") + m.campInput.View()
	} else if m.campEditing {
		hintText = sDim.Render("Enter / Esc = gem & luk")
	} else if m.campAdding {
		prompt := "Ny kampagne navn:"
		if m.campAddKind == campKindPlayer {
			prompt = "Ny spiller navn:"
		} else if m.campAddKind == campKindSession {
			prompt = "Ny sessions navn:"
		}
		hintText = lipgloss.NewStyle().Foreground(colorGold).Render(prompt+" ") + m.campInput.View()
	} else if m.campTaskAdding {
		hintText = lipgloss.NewStyle().Foreground(colorGold).Render("Ny opgave: ") + m.campInput.View()
	} else if m.campBlurbEditing {
		hintText = sDim.Render("Ctrl+S = gem  |  Esc = annuller")
	} else if m.campTaskFocus {
		hintText = sDim.Render("j/k=naviger  Space/Enter=toggle  d=slet  t=ny opgave  Esc=luk")
	} else if m.campSearchActive {
		queryText := lipgloss.NewStyle().Foreground(colorSelFg).Bold(true).Render(m.campSearchQuery)
		cursor := lipgloss.NewStyle().Background(colorSelFg).Foreground(colorBg).Render(" ")
		label := lipgloss.NewStyle().Foreground(colorGreen).Bold(true).Render("SØGER ")
		hintText = label + queryText + cursor + sDim.Render("  Esc=ryd  Enter=bekræft")
	} else {
		hintText = sDim.Render("Tab=artikler  /=søg  ↑↓=naviger  → Enter=åbn/fold  a=spiller  s=session  t=opgave  c=kampagne  i=initiativ  b=blurb  r=omdøb  d=slet")
	}
	divColorCamp := sBorderNormal
	if m.campSearchActive {
		divColorCamp = sBorderActive
	}
	searchLine := lipgloss.NewStyle().Background(colorBg).Width(m.width).Padding(0, 1).Render(hintText)
	borderLine := lipgloss.NewStyle().Foreground(lipgloss.Color(divColorCamp)).Render(strings.Repeat("─", m.width))
	searchBar := borderLine + "\n" + searchLine

	// Sidebar — render from campItems (filtered if search active)
	filteredItems := m.campFilteredItems()
	sideLabel := lipgloss.NewStyle().Background(colorSideBg).Foreground(colorGreen).Bold(true).Padding(0, 1).
		Render(fmt.Sprintf("KAMPAGNER  %d", len(m.campaign.Campaigns)))
	sideHeader := lipgloss.NewStyle().Background(colorSideBg).
		Border(lipgloss.NormalBorder(), false, false, true, false).
		BorderForeground(colorGreen).Width(sideW).Render(sideLabel)

	var sideLines []string
	maxVisible := vis - 1
	end := m.campListOffset + maxVisible
	if end > len(filteredItems) {
		end = len(filteredItems)
	}
	for i := m.campListOffset; i < end; i++ {
		it := filteredItems[i]
		line := it.label
		maxW := sideW - 2
		if runewidth.StringWidth(line) > maxW {
			line = runewidth.Truncate(line, maxW-1, "…")
		}
		if i == m.campCursor {
			accent := lipgloss.NewStyle().Background(colorSelBg).Foreground(colorGreen).Render("▌")
			rest := lipgloss.NewStyle().Background(colorSelBg).Foreground(colorSelFg).Bold(true).
				Render(line + strings.Repeat(" ", max(0, sideW-1-visLen(line))))
			sideLines = append(sideLines, accent+rest)
		} else {
			var fg lipgloss.Color
			if it.depth == 0 {
				fg = colorBright
			} else {
				fg = colorMuted
			}
			sideLines = append(sideLines, lipgloss.NewStyle().Background(colorSideBg).Foreground(fg).Render(line))
		}
	}
	// Scroll indicators
	if m.campListOffset > 0 {
		indicator := lipgloss.NewStyle().Background(colorSideBg).Foreground(colorGreen).Render("  ↑ " + strings.Repeat(" ", max(0, sideW-4)))
		sideLines = append([]string{indicator}, sideLines...)
		if len(sideLines) > maxVisible {
			sideLines = sideLines[:maxVisible]
		}
	}
	if end < len(filteredItems) {
		indicator := lipgloss.NewStyle().Background(colorSideBg).Foreground(colorGreen).Render("  ↓ " + strings.Repeat(" ", max(0, sideW-4)))
		if len(sideLines) < maxVisible {
			sideLines = append(sideLines, indicator)
		} else {
			sideLines[maxVisible-1] = indicator
		}
	}
	for len(sideLines) < maxVisible {
		sideLines = append(sideLines, lipgloss.NewStyle().Background(colorSideBg).Render(strings.Repeat(" ", sideW)))
	}

	// Content pane
	contentW := m.width - sideW - 1

	// ── Initiative tracker overlay ────────────────────────────
	if m.showInitiative {
		camp := m.campaign.Campaigns[m.initCampIdx]
		initList := camp.Initiative
		iTitle := lipgloss.NewStyle().Foreground(colorGold).Bold(true).Render("⚔  Initiativ — " + camp.Name)
		iHint := sDim.Render("Esc=luk")
		iGap := contentW - lipgloss.Width(iTitle) - lipgloss.Width(iHint) - 4
		if iGap < 0 {
			iGap = 0
		}
		iHeader := lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), false, false, true, false).
			BorderForeground(colorGold).Width(contentW).Padding(0, 1).
			Render(iTitle + strings.Repeat(" ", iGap) + iHint)

		var iBody string
		if m.initAdding {
			// handled in hint bar, body shows existing list
		}
		var iLines []string
		iLines = append(iLines, "")
		if len(initList) == 0 {
			iLines = append(iLines, "  "+sDim.Render("Tom liste — tryk 'a' for at tilføje combatants"))
		} else {
			turnLabel := lipgloss.NewStyle().Background(colorGold).Foreground(colorBg).Bold(true).
				Render(fmt.Sprintf(" Tur %d/%d ", m.initTurn+1, len(initList)))
			iLines = append(iLines, "  "+turnLabel)
			iLines = append(iLines, "")
			for idx, c := range initList {
				num := lipgloss.NewStyle().Foreground(colorMuted).Render(fmt.Sprintf("%2d. ", idx+1))
				initVal := lipgloss.NewStyle().Background(colorFaint).Foreground(colorBlue).Bold(true).
					Padding(0, 1).Render(fmt.Sprintf("%d", c.Initiative))
				name := lipgloss.NewStyle().Foreground(colorBright).Render(c.Name)
				var line string
				if idx == m.initTurn && idx == m.initCursor {
					arrow := lipgloss.NewStyle().Foreground(colorGold).Bold(true).Render("▶ ")
					nameSt := lipgloss.NewStyle().Foreground(colorGold).Bold(true).Render(c.Name)
					line = "  " + num + arrow + initVal + "  " + nameSt + lipgloss.NewStyle().Foreground(colorGold).Render(" ← aktiv tur")
				} else if idx == m.initTurn {
					nameSt := lipgloss.NewStyle().Foreground(colorGold).Bold(true).Render(c.Name)
					line = "  " + num + lipgloss.NewStyle().Foreground(colorGold).Bold(true).Render("▶ ") + initVal + "  " + nameSt
				} else if idx == m.initCursor {
					arrow := lipgloss.NewStyle().Foreground(colorAccent).Render("› ")
					line = "  " + num + arrow + initVal + "  " + name
				} else {
					line = "  " + num + "  " + initVal + "  " + name
				}
				iLines = append(iLines, line)
			}
		}
		iBody = strings.Join(iLines, "\n")

		// Hint bar override for initiative
		var initHint string
		if m.initAdding {
			if m.initAddPhase == 0 {
				cur := lipgloss.NewStyle().Background(colorSelFg).Foreground(colorBg).Render(" ")
				initHint = lipgloss.NewStyle().Foreground(colorGold).Render("Navn: ") +
					lipgloss.NewStyle().Foreground(colorSelFg).Render(m.initAddInput) + cur
			} else {
				cur := lipgloss.NewStyle().Background(colorSelFg).Foreground(colorBg).Render(" ")
				initHint = lipgloss.NewStyle().Foreground(colorGold).Render("Initiativ for "+m.initAddName+": ") +
					lipgloss.NewStyle().Foreground(colorSelFg).Render(m.initAddInput) + cur
			}
		} else {
			initHint = sDim.Render("n/Space=næste  p=forrige  r=top  a=tilføj  d=slet  +=op  -=ned  X=ryd  Esc=luk")
		}
		iSearchLine := lipgloss.NewStyle().Background(colorBg).Width(m.width).Padding(0, 1).Render(initHint)
		iBorderLine := lipgloss.NewStyle().Foreground(colorGold).Render(strings.Repeat("─", m.width))
		iSearchBar := iBorderLine + "\n" + iSearchLine

		// Combine sidebar + initiative content
		dividerI := lipgloss.NewStyle().Foreground(colorGold).Render("│")
		iSideAll := append([]string{sideHeader}, sideLines...)
		iContAll := append([]string{iHeader}, strings.Split(iBody, "\n")...)
		iTotalLines := vis + 1
		for len(iSideAll) < iTotalLines {
			iSideAll = append(iSideAll, "")
		}
		for len(iContAll) < iTotalLines {
			iContAll = append(iContAll, "")
		}
		var iMainLines []string
		for i := 0; i < iTotalLines; i++ {
			sl := ""
			if i < len(iSideAll) {
				sl = iSideAll[i]
			}
			cl := ""
			if i < len(iContAll) {
				cl = iContAll[i]
			}
			vl := visLen(sl)
			if vl < sideW {
				pad := lipgloss.NewStyle().Background(colorSideBg).Render(strings.Repeat(" ", sideW-vl))
				sl = sl + pad
			}
			iMainLines = append(iMainLines, sl+dividerI+cl)
		}
		return header + "\n" + strings.Join(iMainLines, "\n") + "\n" + iSearchBar
	}

	item := m.campCurrentItem()
	var nbTitle string
	var canEdit bool
	if item != nil {
		switch item.kind {
		case campKindCampaign:
			nbTitle = "⚔  " + m.campaign.Campaigns[item.campIdx].Name
		case campKindGeneral:
			nbTitle = "📋  " + m.campaign.Campaigns[item.campIdx].Name + " — Generelt"
			canEdit = true
		case campKindPlayer:
			nbTitle = "👤  " + m.campaign.Campaigns[item.campIdx].Players[item.playerIdx].Name
			canEdit = true
		case campKindSession:
			nbTitle = "📅  " + m.campaign.Campaigns[item.campIdx].Sessions[item.playerIdx].Name
			canEdit = true
		}
	}
	var editHint string
	if m.campConfirm {
		editHint = lipgloss.NewStyle().Foreground(colorGold).Bold(true).Render("Bekræft sletning")
	} else if m.campEditing {
		editHint = sDim.Render("Enter/Esc=gem")
	} else if canEdit {
		editHint = sDim.Render("Enter=rediger")
	}
	gap := contentW - lipgloss.Width(nbTitle) - lipgloss.Width(editHint) - 4
	if gap < 0 {
		gap = 0
	}
	contentHeader := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), false, false, true, false).
		BorderForeground(colorGreen).
		Width(contentW).Padding(0, 1).
		Render(lipgloss.NewStyle().Foreground(colorGreen).Bold(true).Render(nbTitle) +
			strings.Repeat(" ", gap) + editHint)

	var contentBody string
	if m.campRefPicking {
		filtered := m.campRefFiltered()
		tags := m.campRefMatchingTags()
		inTagMode := m.campRefInTagMode()
		var lines []string
		lines = append(lines, "")
		// header
		if m.campRefInsert {
			if inTagMode {
				tagFilter := strings.TrimPrefix(m.campRefSearch, "#")
				cur := lipgloss.NewStyle().Background(colorSelFg).Foreground(colorBg).Render(" ")
				inputDisplay := "  " + lipgloss.NewStyle().Foreground(colorGold).Render("#") +
					lipgloss.NewStyle().Foreground(colorSelFg).Render(tagFilter) + cur +
					sDim.Render("  Enter=vælg tag  Esc=luk")
				lines = append(lines, "  "+lipgloss.NewStyle().Foreground(colorSelFg).Bold(true).Render("↗  Vælg tag")+inputDisplay)
			} else if m.campRefTagFilter != "" {
				badge := lipgloss.NewStyle().Background(colorFaint).Foreground(colorBlue).Padding(0, 1).Render("#" + m.campRefTagFilter)
				lines = append(lines, "  "+lipgloss.NewStyle().Foreground(colorSelFg).Bold(true).Render("↗  Indsæt reference")+"  "+badge+"  "+sDim.Render("Esc=skift tag"))
			} else {
				cur := lipgloss.NewStyle().Background(colorSelFg).Foreground(colorBg).Render(" ")
				inputDisplay := "  " + lipgloss.NewStyle().Foreground(colorSelFg).Render(m.campRefSearch) + cur +
					sDim.Render("  Enter=indsæt  Esc=luk")
				lines = append(lines, "  "+lipgloss.NewStyle().Foreground(colorSelFg).Bold(true).Render("↗  Indsæt reference")+inputDisplay)
			}
		} else {
			var inputDisplay string
			if m.campRefSearch != "" {
				inputDisplay = "  " + lipgloss.NewStyle().Background(colorSelBg).Foreground(colorSelFg).Bold(true).Padding(0, 1).Render("→ "+m.campRefSearch) +
					lipgloss.NewStyle().Background(colorSelFg).Foreground(colorBg).Render(" ") + sDim.Render(" Enter=hop")
			} else {
				inputDisplay = "  " + sDim.Render("skriv nr + Enter  |  Esc=luk")
			}
			lines = append(lines, "  "+lipgloss.NewStyle().Foreground(colorSelFg).Bold(true).Render("↗  Følg reference")+inputDisplay)
		}
		lines = append(lines, "  "+sDim.Render(strings.Repeat("─", contentW-4)))

		if inTagMode {
			if len(tags) == 0 {
				lines = append(lines, "  "+sDim.Render("Ingen tags fundet"))
			}
			for i, t := range tags {
				pill := sTag.Render("#" + t)
				if i == m.campRefCursor {
					accent := lipgloss.NewStyle().Foreground(colorSelFg).Render("▌ ")
					lines = append(lines, "  "+accent+pill)
				} else {
					lines = append(lines, "    "+pill)
				}
			}
		} else {
			if len(filtered) == 0 {
				lines = append(lines, "  "+sDim.Render("Ingen resultater"))
			}
			for i, r := range filtered {
				num := lipgloss.NewStyle().Background(colorFaint).Foreground(colorGold).Bold(true).Padding(0, 1).Render(fmt.Sprintf("%d", i+1))
				icon := resourceIcon(r.IconGlyph)
				if i == m.campRefCursor {
					accent := lipgloss.NewStyle().Foreground(colorSelFg).Render("▌ ")
					lines = append(lines, "  "+accent+num+" "+lipgloss.NewStyle().Foreground(colorSelFg).Bold(true).Render(icon+r.Name))
				} else {
					lines = append(lines, "    "+num+" "+lipgloss.NewStyle().Foreground(colorAccent).Render(icon+r.Name))
				}
			}
		}
		contentBody = strings.Join(lines, "\n")
	} else if m.campConfirm && item != nil {
		var what string
		switch item.kind {
		case campKindCampaign:
			what = "kampagnen \"" + m.campaign.Campaigns[item.campIdx].Name + "\""
		case campKindPlayer:
			what = "spilleren \"" + m.campaign.Campaigns[item.campIdx].Players[item.playerIdx].Name + "\""
		case campKindSession:
			what = "sessionen \"" + m.campaign.Campaigns[item.campIdx].Sessions[item.playerIdx].Name + "\""
		}
		contentBody = "\n\n  " + lipgloss.NewStyle().Foreground(colorGold).Bold(true).Render("Er du sikker?") +
			"\n\n  " + sDim.Render("Dette vil permanent slette "+what+".") +
			"\n\n  " + sKey.Render("j") + sDim.Render("  ja, slet") +
			"   " + sKey.Render("n") + sDim.Render("  annuller")
	} else if m.campEditing {
		contentBody = m.ta.View()
	} else if item != nil && item.kind == campKindCampaign {
		// Show campaign overview
		camp := m.campaign.Campaigns[item.campIdx]
		var lines []string
		lines = append(lines, "")
		lines = append(lines, "  "+lipgloss.NewStyle().Foreground(colorSelFg).Bold(true).Render(camp.Name))
		lines = append(lines, "")

		// ── Tasks ──
		taskTitle := lipgloss.NewStyle().Foreground(colorGold).Bold(true).Render("✓  Opgaver")
		if m.campTaskFocus {
			lines = append(lines, "  "+taskTitle+"  "+sDim.Render("Space/Enter=toggle  d=slet  t=ny  Esc=luk"))
		} else {
			lines = append(lines, "  "+taskTitle+"  "+sDim.Render("Enter=rediger  t=ny"))
		}
		lines = append(lines, "  "+sDim.Render(strings.Repeat("─", contentW-4)))
		if len(camp.Tasks) == 0 {
			lines = append(lines, "    "+sDim.Render("Ingen opgaver — tryk 't' for at tilføje"))
		} else {
			for i, task := range camp.Tasks {
				var check string
				var nameRender string
				if task.Done {
					check = lipgloss.NewStyle().Foreground(colorGreen).Render("✓")
					nameRender = lipgloss.NewStyle().Foreground(colorMuted).Render(task.Name)
				} else {
					check = lipgloss.NewStyle().Foreground(colorMuted).Render("○")
					nameRender = lipgloss.NewStyle().Foreground(colorBright).Render(task.Name)
				}
				if m.campTaskFocus && i == m.campTaskCursor {
					accent := lipgloss.NewStyle().Foreground(colorSelFg).Render("▌ ")
					lines = append(lines, "  "+accent+check+"  "+nameRender)
				} else {
					lines = append(lines, "    "+check+"  "+nameRender)
				}
			}
		}
		lines = append(lines, "")

		// ── Players ──
		boxW := contentW - 4
		if len(camp.Players) == 0 {
			lines = append(lines, "  "+sDim.Render("Ingen spillere — tryk 'a' for at tilføje"))
		} else {
			lines = append(lines, "  "+lipgloss.NewStyle().Foreground(colorBlue).Bold(true).Render("Spillere"))
			lines = append(lines, "  "+sDim.Render(strings.Repeat("─", contentW-4)))
			for _, p := range camp.Players {
				box := drawCampBox("👤  "+p.Name, p.Blurb, boxW, colorMuted, colorBright, colorBlue)
				for _, bl := range strings.Split(box, "\n") {
					lines = append(lines, "  "+bl)
				}
			}
		}
		lines = append(lines, "")

		// ── Sessions ──
		if len(camp.Sessions) > 0 {
			lines = append(lines, "  "+lipgloss.NewStyle().Foreground(colorBlue).Bold(true).Render("Sessioner"))
			lines = append(lines, "  "+sDim.Render(strings.Repeat("─", contentW-4)))
			for _, s := range camp.Sessions {
				box := drawCampBox("📅  "+s.Name, s.Blurb, boxW, colorMuted, colorBright, colorBlue)
				for _, bl := range strings.Split(box, "\n") {
					lines = append(lines, "  "+bl)
				}
			}
		}
		contentBody = strings.Join(lines, "\n")
	} else {
		// Show blurb at top for player/session items
		var blurb string
		curItem := m.campCurrentItem()
		if curItem != nil {
			switch curItem.kind {
			case campKindPlayer:
				blurb = m.campaign.Campaigns[curItem.campIdx].Players[curItem.playerIdx].Blurb
			case campKindSession:
				blurb = m.campaign.Campaigns[curItem.campIdx].Sessions[curItem.playerIdx].Blurb
			}
		}
		var parts []string
		parts = append(parts, "")
		if curItem != nil && (curItem.kind == campKindPlayer || curItem.kind == campKindSession) {
			boxW := contentW - 4
			if m.campBlurbEditing {
				m.campBlurbTA.SetWidth(boxW - 4)
				editLabel := lipgloss.NewStyle().Foreground(colorGold).Bold(true).Render("Blurb")
				parts = append(parts, "  "+editLabel)
				for _, bl := range strings.Split(m.campBlurbTA.View(), "\n") {
					parts = append(parts, "  "+bl)
				}
			} else if blurb != "" {
				for _, bl := range strings.Split(drawCampBox("Opsummering", blurb, boxW, colorMuted, colorBright, colorBlue), "\n") {
					parts = append(parts, "  "+bl)
				}
			} else {
				for _, bl := range strings.Split(drawCampBox("Opsummering", "(ingen blurb — tryk 'b' for at tilføje)", boxW, colorFaint, colorFaint, colorMuted), "\n") {
					parts = append(parts, "  "+bl)
				}
			}
			parts = append(parts, "")
		}
		note := m.campCurrentNote()
		if note == "" {
			parts = append(parts, "  "+sDim.Render("Ingen noter endnu — tryk Enter for at begynde."))
		} else {
			hasRefs := len(m.campNoteRefs(note)) > 0
			var lines []string
			for _, l := range strings.Split(note, "\n") {
				lines = append(lines, "  "+applyInlineStyles(highlightMentions(l)))
			}
			parts = append(parts, strings.Join(lines, "\n"))
			if hasRefs {
				parts = append(parts, "")
				parts = append(parts, "  "+sDim.Render("f")+"  "+sDim.Render("= følg reference"))
			}
		}
		contentBody = strings.Join(parts, "\n")
	}

	// Combine
	divider := lipgloss.NewStyle().Foreground(colorGreen).Render("│")
	sideAllLines := append([]string{sideHeader}, sideLines...)
	contentAllLines := append([]string{contentHeader}, strings.Split(contentBody, "\n")...)
	totalLines := vis + 1
	for len(sideAllLines) < totalLines {
		sideAllLines = append(sideAllLines, "")
	}
	for len(contentAllLines) < totalLines {
		contentAllLines = append(contentAllLines, "")
	}
	var mainLines []string
	for i := 0; i < totalLines; i++ {
		sl := ""
		if i < len(sideAllLines) {
			sl = sideAllLines[i]
		}
		cl := ""
		if i < len(contentAllLines) {
			cl = contentAllLines[i]
		}
		vl := visLen(sl)
		if vl < sideW {
			pad := lipgloss.NewStyle().Background(colorSideBg).Render(strings.Repeat(" ", sideW-vl))
			sl = sl + pad
		}
		mainLines = append(mainLines, sl+divider+cl)
	}
	main := strings.Join(mainLines, "\n")

	return header + "\n" + main + "\n" + searchBar
}

// visLen returns the visible display width of a string, stripping ANSI escapes
// and using go-runewidth for accurate wide-character (emoji, CJK) measurement.
// drawCampBox draws a box with precise width control.
// title="" → plain top border. body="" → no body lines (compact box).
func drawCampBox(title, body string, boxW int, borderFg, titleFg, bodyFg lipgloss.Color) string {
	bStyle := lipgloss.NewStyle().Foreground(borderFg)
	tStyle := lipgloss.NewStyle().Foreground(titleFg).Bold(true)
	bBody := lipgloss.NewStyle().Foreground(bodyFg)

	innerW := boxW - 4 // │·content·│ → 2 border + 2 padding
	if innerW < 1 {
		innerW = 1
	}

	// Top border
	var top string
	if title == "" {
		top = bStyle.Render("╭" + strings.Repeat("─", boxW-2) + "╮")
	} else {
		titleVisW := runewidth.StringWidth(title)
		dashes := boxW - 5 - titleVisW // "╭─ " + title + " " + dashes + "╮"
		if dashes < 0 {
			dashes = 0
		}
		top = bStyle.Render("╭─ ") + tStyle.Render(title) + bStyle.Render(" "+strings.Repeat("─", dashes)+"╮")
	}

	var lines []string
	lines = append(lines, top)

	// Body lines with word-wrap
	if body != "" {
		var wrapped []string
		for _, line := range strings.Split(body, "\n") {
			if runewidth.StringWidth(line) <= innerW {
				wrapped = append(wrapped, line)
			} else {
				words := strings.Fields(line)
				cur := ""
				for _, w := range words {
					if cur == "" {
						cur = w
					} else if runewidth.StringWidth(cur)+1+runewidth.StringWidth(w) <= innerW {
						cur += " " + w
					} else {
						wrapped = append(wrapped, cur)
						cur = w
					}
				}
				if cur != "" {
					wrapped = append(wrapped, cur)
				}
			}
		}
		for _, wl := range wrapped {
			pad := strings.Repeat(" ", max(0, innerW-runewidth.StringWidth(wl)))
			lines = append(lines, bStyle.Render("│ ")+bBody.Render(wl+pad)+bStyle.Render(" │"))
		}
	}

	lines = append(lines, bStyle.Render("╰"+strings.Repeat("─", boxW-2)+"╯"))
	return strings.Join(lines, "\n")
}

func visLen(s string) int {
	// Strip ANSI escape sequences first
	var clean strings.Builder
	inEscape := false
	for _, r := range s {
		if r == '\x1b' {
			inEscape = true
			continue
		}
		if inEscape {
			if r == 'm' {
				inEscape = false
			}
			continue
		}
		clean.WriteRune(r)
	}
	return runewidth.StringWidth(clean.String())
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ---------------------------------------------------------------------------
// Document helper
// ---------------------------------------------------------------------------

func (d Document) IsHidden() bool {
	return false
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

func campaignFilePath() string {
	ext := filepath.Ext(dataPath)
	return dataPath[:len(dataPath)-len(ext)] + ".campaign.json"
}

func loadCampaign() CampaignData {
	var c CampaignData
	data, err := os.ReadFile(campaignFilePath())
	if err == nil {
		json.Unmarshal(data, &c)
		// Migrate old single-campaign format
		if len(c.Campaigns) == 0 && (c.General != "" || len(c.Players) > 0) {
			c.Campaigns = []Campaign{{
				Name:    "Kampagne 1",
				General: c.General,
				Players: c.Players,
			}}
			c.General = ""
			c.Players = nil
		}
	}
	if len(c.Campaigns) == 0 {
		c.Campaigns = []Campaign{{Name: "Kampagne 1"}}
	}
	return c
}

func (m *model) saveCampaign() {
	data, err := json.MarshalIndent(m.campaign, "", "  ")
	if err == nil {
		os.WriteFile(m.campaignPath, data, 0644)
	}
}

func (m *model) buildCampItems() {
	var items []campListItem
	for i, c := range m.campaign.Campaigns {
		foldIcon := "▸ "
		if m.campExpanded[i] {
			foldIcon = "▾ "
		}
		items = append(items, campListItem{
			kind: campKindCampaign, campIdx: i,
			label: foldIcon + "⚔  " + c.Name, depth: 0,
		})
		if m.campExpanded[i] {
			items = append(items, campListItem{
				kind: campKindGeneral, campIdx: i,
				label: "  📋  Generelt", depth: 1,
			})
			for j, p := range c.Players {
				items = append(items, campListItem{
					kind: campKindPlayer, campIdx: i, playerIdx: j,
					label: "  👤  " + p.Name, depth: 1,
				})
			}
			for k, s := range c.Sessions {
				items = append(items, campListItem{
					kind: campKindSession, campIdx: i, playerIdx: k,
					label: "  📅  " + s.Name, depth: 1,
				})
			}
		}
	}
	m.campItems = items
}

func (m *model) campFilteredItems() []campListItem {
	if m.campSearchQuery == "" {
		return m.campItems
	}
	q := strings.ToLower(m.campSearchQuery)
	var out []campListItem
	for _, it := range m.campItems {
		if strings.Contains(strings.ToLower(it.label), q) {
			out = append(out, it)
		}
	}
	return out
}

func (m *model) campCurrentItem() *campListItem {
	items := m.campFilteredItems()
	if m.campCursor >= 0 && m.campCursor < len(items) {
		return &items[m.campCursor]
	}
	return nil
}

func (m *model) campNoteCount() int {
	return len(m.campItems)
}

func (m *model) campCurrentNote() string {
	item := m.campCurrentItem()
	if item == nil {
		return ""
	}
	switch item.kind {
	case campKindGeneral:
		return m.campaign.Campaigns[item.campIdx].General
	case campKindPlayer:
		return m.campaign.Campaigns[item.campIdx].Players[item.playerIdx].Note
	case campKindSession:
		return m.campaign.Campaigns[item.campIdx].Sessions[item.playerIdx].Note
	}
	return ""
}

func (m *model) campSetNote(val string) {
	item := m.campCurrentItem()
	if item == nil {
		return
	}
	switch item.kind {
	case campKindGeneral:
		m.campaign.Campaigns[item.campIdx].General = val
	case campKindPlayer:
		m.campaign.Campaigns[item.campIdx].Players[item.playerIdx].Note = val
	case campKindSession:
		m.campaign.Campaigns[item.campIdx].Sessions[item.playerIdx].Note = val
	}
}

func (m *model) campRefInTagMode() bool {
	return m.campRefInsert && m.campRefTagFilter == "" && strings.HasPrefix(m.campRefSearch, "#")
}

func (m *model) campRefMatchingTags() []string {
	q := strings.ToLower(strings.TrimPrefix(m.campRefSearch, "#"))
	seen := map[string]bool{}
	var out []string
	for _, r := range m.resources {
		for _, t := range r.Tags {
			tl := strings.ToLower(t)
			if !seen[tl] && (q == "" || strings.Contains(tl, q)) {
				seen[tl] = true
				out = append(out, t)
			}
		}
	}
	return out
}

func (m *model) campRefFiltered() []Resource {
	q := strings.ToLower(m.campRefSearch)
	var out []Resource
	if m.campRefInsert {
		if m.campRefInTagMode() {
			// tag browsing phase — return empty, tags shown separately
			return nil
		}
		// phase 2: filter by selected tag, or by name search
		for _, r := range m.resources {
			var match bool
			if m.campRefTagFilter != "" {
				for _, t := range r.Tags {
					if strings.EqualFold(t, m.campRefTagFilter) {
						match = true
						break
					}
				}
			} else if q == "" {
				match = true
			} else {
				match = strings.Contains(strings.ToLower(r.Name), q)
			}
			if match {
				out = append(out, r)
			}
			if len(out) >= 50 {
				break
			}
		}
	} else {
		// follow mode: show all note refs, digit buffer is not a text filter
		note := m.campCurrentNote()
		out = m.campNoteRefs(note)
	}
	return out
}

func (m *model) campNoteRefs(note string) []Resource {
	var out []Resource
	seen := map[string]bool{}
	remaining := note
	for {
		start := strings.Index(remaining, "[")
		if start == -1 {
			break
		}
		end := strings.Index(remaining[start:], "]")
		if end == -1 {
			break
		}
		name := remaining[start+1 : start+end]
		remaining = remaining[start+end+1:]
		if seen[name] {
			continue
		}
		seen[name] = true
		for i := range m.resources {
			if strings.EqualFold(m.resources[i].Name, name) {
				out = append(out, m.resources[i])
				break
			}
		}
	}
	return out
}

func (m *model) sortInitiative() {
	list := m.campaign.Campaigns[m.initCampIdx].Initiative
	if len(list) == 0 {
		return
	}
	// Remember current turn combatant name to re-find after sort
	var turnName string
	if m.initTurn < len(list) {
		turnName = list[m.initTurn].Name
	}
	var cursorName string
	if m.initCursor < len(list) {
		cursorName = list[m.initCursor].Name
	}
	// Bubble sort descending by initiative
	for i := 0; i < len(list)-1; i++ {
		for j := 0; j < len(list)-1-i; j++ {
			if list[j].Initiative < list[j+1].Initiative {
				list[j], list[j+1] = list[j+1], list[j]
			}
		}
	}
	m.campaign.Campaigns[m.initCampIdx].Initiative = list
	// Restore turn and cursor positions
	for i, c := range list {
		if c.Name == turnName {
			m.initTurn = i
		}
		if c.Name == cursorName {
			m.initCursor = i
		}
	}
}

func (m *model) campOpenEditor() {
	m.ta.SetValue(m.campCurrentNote())
	m.ta.Focus()
	m.campEditing = true
}

func (m *model) campCloseEditor() {
	m.campSetNote(m.ta.Value())
	m.saveCampaign()
	m.ta.Blur()
	m.campEditing = false
}

func newestJSON(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var newest os.FileInfo
	var newestPath string
	for _, e := range entries {
		lower := strings.ToLower(e.Name())
		if e.IsDir() || !strings.HasSuffix(lower, ".json") || strings.HasSuffix(lower, ".campaign.json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if newest == nil || info.ModTime().After(newest.ModTime()) {
			newest = info
			newestPath = filepath.Join(dir, e.Name())
		}
	}
	if newestPath == "" {
		return "", fmt.Errorf("ingen .json filer fundet i %s", dir)
	}
	return newestPath, nil
}

// ---------------------------------------------------------------------------
// Plot / decision tree helpers
// ---------------------------------------------------------------------------

// plotNodesAtCol returns the nodes visible in the given absolute column.
func (m *model) plotNodesAtCol(col int) []PlotNode {
	nodes := m.campaign.Campaigns[m.plotCampIdx].Plot
	for i := 0; i < col; i++ {
		cur := m.plotColCursorAt(i)
		if cur >= len(nodes) {
			return nil
		}
		nodes = nodes[cur].Children
	}
	return nodes
}

// plotCurrentPath returns the path to the currently focused node.
func (m *model) plotCurrentPath() []int {
	path := make([]int, m.plotFocusCol+1)
	for i := 0; i <= m.plotFocusCol; i++ {
		path[i] = m.plotColCursorAt(i)
	}
	return path
}

// plotParentPath returns the path to the parent of nodes in the focused column.
// For col 0, returns nil (root level).
func (m *model) plotParentPath() []int {
	if m.plotFocusCol == 0 {
		return nil
	}
	path := make([]int, m.plotFocusCol)
	for i := 0; i < m.plotFocusCol; i++ {
		path[i] = m.plotColCursorAt(i)
	}
	return path
}

func (m *model) plotCurrentNode() *PlotNode {
	path := m.plotCurrentPath()
	nodes := m.campaign.Campaigns[m.plotCampIdx].Plot
	return plotNodeAt(nodes, path)
}

func (m *model) plotColCursorAt(col int) int {
	if col < len(m.plotColCursor) {
		return m.plotColCursor[col]
	}
	return 0
}

func (m *model) plotColOffsetAt(col int) int {
	if col < len(m.plotColOffset) {
		return m.plotColOffset[col]
	}
	return 0
}

func (m *model) setPlotColCursor(col, val int) {
	for len(m.plotColCursor) <= col {
		m.plotColCursor = append(m.plotColCursor, 0)
		m.plotColOffset = append(m.plotColOffset, 0)
	}
	m.plotColCursor[col] = val
}

func (m *model) ensurePlotCol(col int) {
	for len(m.plotColCursor) <= col {
		m.plotColCursor = append(m.plotColCursor, 0)
		m.plotColOffset = append(m.plotColOffset, 0)
	}
}

// resetPlotColsAfter resets cursor/offset for all columns after col,
// and pulls focus back to col if it was further right.
func (m *model) resetPlotColsAfter(col int) {
	for i := col + 1; i < len(m.plotColCursor); i++ {
		m.plotColCursor[i] = 0
		m.plotColOffset[i] = 0
	}
	if m.plotFocusCol > col {
		m.plotFocusCol = col
	}
}

func plotNodeAt(nodes []PlotNode, path []int) *PlotNode {
	if len(path) == 0 || path[0] >= len(nodes) {
		return nil
	}
	n := &nodes[path[0]]
	for _, idx := range path[1:] {
		if idx >= len(n.Children) {
			return nil
		}
		n = &n.Children[idx]
	}
	return n
}

// plotSetNode returns a copy of the tree with the node at path replaced.
func (m *model) plotSetNode(nodes []PlotNode, path []int, node PlotNode) []PlotNode {
	if len(path) == 0 {
		return nodes
	}
	result := make([]PlotNode, len(nodes))
	copy(result, nodes)
	if path[0] >= len(result) {
		return result
	}
	if len(path) == 1 {
		result[path[0]] = node
		return result
	}
	result[path[0]].Children = m.plotSetNode(result[path[0]].Children, path[1:], node)
	return result
}

func plotDeleteAt(nodes []PlotNode, path []int) []PlotNode {
	if len(path) == 0 || path[0] >= len(nodes) {
		return nodes
	}
	if len(path) == 1 {
		result := make([]PlotNode, 0, len(nodes)-1)
		result = append(result, nodes[:path[0]]...)
		result = append(result, nodes[path[0]+1:]...)
		return result
	}
	result := make([]PlotNode, len(nodes))
	copy(result, nodes)
	result[path[0]].Children = plotDeleteAt(result[path[0]].Children, path[1:])
	return result
}

func (m *model) plotAddChildAt(nodes []PlotNode, parentPath []int, child PlotNode) []PlotNode {
	if len(parentPath) == 0 {
		return append(nodes, child)
	}
	result := make([]PlotNode, len(nodes))
	copy(result, nodes)
	if parentPath[0] >= len(result) {
		return result
	}
	result[parentPath[0]].Children = m.plotAddChildAt(result[parentPath[0]].Children, parentPath[1:], child)
	return result
}

// stripANSI removes ANSI escape codes for width-safe truncation.
func stripANSI(s string) string {
	var out strings.Builder
	inEsc := false
	for _, r := range s {
		if r == '\x1b' {
			inEsc = true
			continue
		}
		if inEsc {
			if r == 'm' {
				inEsc = false
			}
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

func main() {
	if len(os.Args) > 1 {
		dataPath = os.Args[1]
	} else {
		home, _ := os.UserHomeDir()
		candidates := []string{"Hentet", "Downloads", "Dokumenter", "Documents", "Desktop", "Skrivebord"}
		var lastErr error
		var path string
		for _, dir := range candidates {
			path, lastErr = newestJSON(filepath.Join(home, dir))
			if lastErr == nil {
				break
			}
		}
		if path == "" {
			fmt.Fprintf(os.Stderr, "Angiv en fil: aelden <fil.json>\nSøgte i: %s\n(%v)\n",
				strings.Join(candidates, ", "), lastErr)
			os.Exit(1)
		}
		dataPath = path
		fmt.Fprintf(os.Stderr, "Indlæser: %s\n", dataPath)
	}

	f, err := os.Open(dataPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Fejl ved åbning af %s: %v\n", dataPath, err)
		os.Exit(1)
	}
	defer f.Close()

	var exp Export
	if err := json.NewDecoder(f).Decode(&exp); err != nil {
		fmt.Fprintf(os.Stderr, "Fejl ved parsing af JSON: %v\n", err)
		os.Exit(1)
	}

	m := initialModel(exp)
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Fejl: %v\n", err)
		os.Exit(1)
	}
}
