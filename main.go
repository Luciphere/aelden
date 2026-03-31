package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
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
		if !ok || r.IsHidden {
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
		if !ok || r.IsHidden {
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
	focusContent
)

type viewMode int

const (
	worldView viewMode = iota
	campaignView
)

type Player struct {
	Name string `json:"name"`
	Note string `json:"note"`
}

type Session struct {
	Name string `json:"name"`
	Note string `json:"note"`
}

type Combatant struct {
	Name       string `json:"name"`
	Initiative int    `json:"initiative"`
}

type Campaign struct {
	Name       string       `json:"name"`
	General    string       `json:"general"`
	Players    []Player     `json:"players"`
	Sessions   []Session    `json:"sessions"`
	Initiative []Combatant  `json:"initiative,omitempty"`
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
	campAdding   bool
	campAddKind  campItemKind
	campAddInput string
	campConfirm     bool
	campRenaming    bool
	campRenameInput string
	ta              textarea.Model

	// campaign reference picker
	campRefPicking bool
	campRefInsert  bool   // true=insert into textarea, false=follow to world view
	campRefSearch  string
	campRefCursor  int

	// initiative tracker
	showInitiative bool
	initCampIdx    int
	initCursor     int
	initTurn       int
	initAdding     bool
	initAddPhase   int    // 0=name, 1=initiative number
	initAddName    string
	initAddInput   string

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
		ta:           ta,
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
		return m, nil

	case tea.KeyMsg:
		if m.mode == campaignView {
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

			// ── reference picker ─────────────────────────────────
			if m.campRefPicking {
				filtered := m.campRefFiltered()
				switch msg.String() {
				case "ctrl+c":
					return m, tea.Quit
				case "esc":
					m.campRefPicking = false
					m.campRefSearch = ""
				case "enter":
					if m.campRefCursor < len(filtered) {
						r := filtered[m.campRefCursor]
						if m.campRefInsert {
							m.ta.SetValue(m.ta.Value() + "[" + r.Name + "]")
						} else {
							m.campRefPicking = false
							m.campRefSearch = ""
							m.mode = worldView
							m.openByID(r.ID, true)
						}
						if m.campRefInsert {
							m.campRefPicking = false
							m.campRefSearch = ""
						}
					}
				case "up", "k":
					if m.campRefCursor > 0 {
						m.campRefCursor--
					}
				case "down", "j":
					if m.campRefCursor < len(filtered)-1 {
						m.campRefCursor++
					}
				case "backspace":
					if len(m.campRefSearch) > 0 {
						runes := []rune(m.campRefSearch)
						m.campRefSearch = string(runes[:len(runes)-1])
						m.campRefCursor = 0
					}
				default:
					if len(msg.Runes) == 1 {
						m.campRefSearch += string(msg.Runes)
						m.campRefCursor = 0
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
					m.campRenameInput = ""
				case "enter":
					name := strings.TrimSpace(m.campRenameInput)
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
					m.campRenameInput = ""
				case "backspace":
					if len(m.campRenameInput) > 0 {
						runes := []rune(m.campRenameInput)
						m.campRenameInput = string(runes[:len(runes)-1])
					}
				default:
					if len(msg.Runes) == 1 {
						m.campRenameInput += string(msg.Runes)
					}
				}
				return m, nil
			}

			// ── adding state ──────────────────────────────────────
			if m.campAdding {
				switch msg.String() {
				case "ctrl+c":
					return m, tea.Quit
				case "esc":
					m.campAdding = false
					m.campAddInput = ""
				case "enter":
					name := strings.TrimSpace(m.campAddInput)
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
									break
								}
							}
						}
					}
					m.campAdding = false
					m.campAddInput = ""
				case "backspace":
					if len(m.campAddInput) > 0 {
						runes := []rune(m.campAddInput)
						m.campAddInput = string(runes[:len(runes)-1])
					}
				default:
					if len(msg.Runes) == 1 {
						m.campAddInput += string(msg.Runes)
					}
				}
				return m, nil
			}

			// ── global keys (work even while editing) ─────────────
			switch msg.String() {
			case "ctrl+c":
				return m, tea.Quit
			case "tab":
				if m.campEditing {
					m.campCloseEditor()
				}
				m.mode = worldView
				return m, tea.Batch(cmds...)
			case "esc":
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
					m.campExpanded[item.campIdx] = !m.campExpanded[item.campIdx]
					m.buildCampItems()
					if m.campCursor >= len(m.campItems) {
						m.campCursor = len(m.campItems) - 1
					}
				case campKindGeneral, campKindPlayer:
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

			// ── normal navigation ─────────────────────────────────
			switch msg.String() {
			case "up", "k":
				if m.campCursor > 0 {
					m.campCursor--
				}
			case "down", "j":
				if m.campCursor < len(m.campItems)-1 {
					m.campCursor++
				}
			case "right", "l":
				item := m.campCurrentItem()
				if item != nil && item.kind == campKindCampaign {
					m.campExpanded[item.campIdx] = true
					m.buildCampItems()
				}
			case "left", "h":
				item := m.campCurrentItem()
				if item != nil && item.kind == campKindCampaign {
					m.campExpanded[item.campIdx] = false
					m.buildCampItems()
				}
			case "a":
				// Add player to the campaign of the current item
				item := m.campCurrentItem()
				if item != nil {
					m.campAddKind = campKindPlayer
					m.campAdding = true
					m.campAddInput = ""
				}
			case "f":
				note := m.campCurrentNote()
				if note != "" {
					// collect [Name] refs in note that match world articles
					refs := m.campNoteRefs(note)
					if len(refs) > 0 {
						m.campRefPicking = true
						m.campRefInsert = false
						m.campRefSearch = ""
						m.campRefCursor = 0
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
				m.campAddInput = ""
			case "s":
				item := m.campCurrentItem()
				if item != nil {
					m.campAddKind = campKindSession
					m.campAdding = true
					m.campAddInput = fmt.Sprintf("Session %d", len(m.campaign.Campaigns[item.campIdx].Sessions)+1)
				}
			case "r":
				item := m.campCurrentItem()
				if item != nil && item.kind != campKindGeneral {
					switch item.kind {
					case campKindCampaign:
						m.campRenameInput = m.campaign.Campaigns[item.campIdx].Name
					case campKindPlayer:
						m.campRenameInput = m.campaign.Campaigns[item.campIdx].Players[item.playerIdx].Name
					case campKindSession:
						m.campRenameInput = m.campaign.Campaigns[item.campIdx].Sessions[item.playerIdx].Name
					}
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
			if m.focus == focusContent {
				if m.showMentions {
					m.showMentions = false
				} else {
					m.focus = focusList
				}
				return m, nil
			}
		case "enter":
			if m.focus == focusContent && !m.showMentions {
				m.focus = focusList
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
			if m.mode == worldView {
				m.mode = campaignView
				m.campEditing = false
				m.ta.Blur()
			} else {
				if m.campEditing {
					m.campCloseEditor()
				}
				m.campAdding = false
				m.campAddInput = ""
				m.mode = worldView
			}
			return m, nil
		case "f":
			if m.selected != nil && len(m.mentions) > 0 {
				m.showMentions = !m.showMentions
				if m.showMentions {
					m.mentionCursor = 0
					m.mentionInput = ""
					m.focus = focusContent
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
				}
			case "down", "j":
				if m.cursor < len(m.items)-1 {
					m.cursor++
					m.clampOffset()
				}
			case "g":
				m.cursor = 0
				m.listOffset = 0
			case "G":
				m.cursor = len(m.items) - 1
				m.clampOffset()
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

		case focusContent:
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
			} else {
				switch msg.String() {
				default:
					var cmd tea.Cmd
					m.vp, cmd = m.vp.Update(msg)
					cmds = append(cmds, cmd)
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
	// header(1) + searchLine(1) + borderLine(1) + sideHeader w/ bottom border(2) + statusbar(1) = 6 overhead
	return m.height - 6
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
	m.focus = focusContent
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
	if m.mode == campaignView {
		return m.viewCampaign()
	}

	sideW := m.sidebarWidth()
	vis := m.visibleLines()

	// ── Header ────────────────────────────────────────────────
	leftAccent := lipgloss.NewStyle().Background(colorGold).Foreground(colorGold).Render("  ")
	title := lipgloss.NewStyle().
		Background(colorBg).
		Foreground(colorSelFg).
		Bold(true).
		Padding(0, 2).
		Render("⚔  AELDEN")

	var modeLabel string
	var modeBg, modeFg lipgloss.Color
	switch m.focus {
	case focusSearch:
		modeLabel, modeBg, modeFg = " SØGER ", colorAccent, colorBg
	case focusContent:
		if m.showMentions {
			modeLabel, modeBg, modeFg = " LINKS ", colorSelBg, colorSelFg
		} else {
			modeLabel, modeBg, modeFg = " LÆSER ", colorSelBg, colorSelFg
		}
	default:
		if m.searchQuery != "" {
			modeLabel, modeBg, modeFg = " FILTER ", colorFaint, colorBlue
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
	header = lipgloss.NewStyle().Background(colorBg).Width(m.width).Render(header)

	// ── Search bar ────────────────────────────────────────────
	divColor := sBorderNormal
	if m.focus == focusSearch {
		divColor = sBorderActive
	}

	var searchContent string
	if m.focus == focusSearch {
		queryText := lipgloss.NewStyle().Foreground(colorSelFg).Bold(true).Render(m.searchQuery)
		cursor := lipgloss.NewStyle().Background(colorSelFg).Foreground(colorBg).Render(" ")
		label := lipgloss.NewStyle().Foreground(colorGold).Bold(true).Render("SØGER ")
		searchContent = label + queryText + cursor
	} else if m.searchQuery != "" {
		label := lipgloss.NewStyle().Foreground(colorMuted).Render("/ ")
		searchContent = label + lipgloss.NewStyle().Foreground(colorBright).Render(m.searchQuery)
	} else {
		searchContent = sDim.Render("/  søg... (#tag for tag-filter)")
	}
	searchLine := lipgloss.NewStyle().
		Background(colorBg).
		Width(m.width).
		Padding(0, 1).
		Render(searchContent)
	borderLine := lipgloss.NewStyle().Foreground(lipgloss.Color(divColor)).Render(strings.Repeat("─", m.width))
	searchBar := searchLine + "\n" + borderLine

	// ── Sidebar items ─────────────────────────────────────────
	listBorderColor := sBorderNormal
	if m.focus == focusList {
		listBorderColor = sBorderActive
	}

	var sideLabelText string
	if m.searchQuery != "" {
		sideLabelText = fmt.Sprintf("SØGER  %d", len(m.items))
	} else {
		sideLabelText = fmt.Sprintf("ARTIKLER  %d", len(m.resources))
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
		name := r.Name
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

		maxW := sideW - 2
		if runewidth.StringWidth(line) > maxW {
			line = runewidth.Truncate(line, maxW-1, "…")
		}

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
	contentBorderColor := sBorderNormal
	if m.focus == focusContent {
		contentBorderColor = sBorderActive
	}
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

	// ── Status bar ────────────────────────────────────────────
	keys := [][]string{
		{"/", "søg"}, {"↑↓ jk", "naviger"}, {"→ ←", "fold ud/ind"}, {"Enter", "åbn"},
		{"^← ^→", "historik"}, {"Tab", "kampagne"}, {"Esc", "tilbage"}, {"q", "afslut"},
	}
	if m.selected != nil && len(m.mentions) > 0 {
		keys = append(keys, []string{"f", "henvisninger"})
	}
	var keyParts []string
	for _, k := range keys {
		keyParts = append(keyParts, sKey.Render(k[0])+" "+sDim.Render(k[1]))
	}
	statusContent := strings.Join(keyParts, "  ")
	if m.notification != "" {
		notif := lipgloss.NewStyle().Foreground(colorGreen).Bold(true).Render(m.notification)
		gap := m.width - lipgloss.Width(statusContent) - lipgloss.Width(notif) - 4
		if gap > 0 {
			statusContent = statusContent + strings.Repeat(" ", gap) + notif
		}
	}
	statusBar := lipgloss.NewStyle().
		Background(colorBg).
		Width(m.width).
		Padding(0, 1).
		Render(statusContent)

	return header + "\n" + searchBar + "\n" + main + "\n" + statusBar
}

func (m model) viewCampaign() string {
	if m.width == 0 {
		return "Loading..."
	}
	sideW := m.sidebarWidth()
	vis := m.visibleLines()

	// Header
	leftAccent := lipgloss.NewStyle().Background(colorGreen).Foreground(colorGreen).Render("  ")
	title := lipgloss.NewStyle().Background(colorBg).Foreground(colorGreen).Bold(true).Padding(0, 2).Render("⚔  KAMPAGNE")
	modeBadge := lipgloss.NewStyle().Background(colorGreen).Foreground(colorBg).Bold(true).Render(" KAMPAGNE ")
	right := lipgloss.NewStyle().Background(colorBg).Padding(0, 1).Render(modeBadge)
	headerGap := m.width - lipgloss.Width(leftAccent) - lipgloss.Width(title) - lipgloss.Width(right)
	if headerGap < 0 {
		headerGap = 0
	}
	header := leftAccent + title + strings.Repeat(" ", headerGap) + right
	header = lipgloss.NewStyle().Background(colorBg).Width(m.width).Render(header)

	// Hint bar
	var hintText string
	if m.campRefPicking {
		cur := lipgloss.NewStyle().Background(colorSelFg).Foreground(colorBg).Render(" ")
		if m.campRefInsert {
			hintText = lipgloss.NewStyle().Foreground(colorAccent).Render("Søg artikel: ") +
				lipgloss.NewStyle().Foreground(colorSelFg).Render(m.campRefSearch) + cur +
				sDim.Render("  Enter=indsæt  Esc=annuller")
		} else {
			hintText = lipgloss.NewStyle().Foreground(colorAccent).Render("Vælg reference: ") +
				sDim.Render("Enter=åbn i verden  Esc=luk")
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
		cur := lipgloss.NewStyle().Background(colorSelFg).Foreground(colorBg).Render(" ")
		hintText = lipgloss.NewStyle().Foreground(colorGold).Render(prompt+" ") +
			lipgloss.NewStyle().Foreground(colorSelFg).Render(m.campRenameInput) + cur
	} else if m.campEditing {
		hintText = sDim.Render("Enter / Esc = gem & luk")
	} else if m.campAdding {
		prompt := "Ny kampagne navn:"
		if m.campAddKind == campKindPlayer {
			prompt = "Ny spiller navn:"
		} else if m.campAddKind == campKindSession {
			prompt = "Ny sessions navn:"
		}
		cur := lipgloss.NewStyle().Background(colorSelFg).Foreground(colorBg).Render(" ")
		hintText = lipgloss.NewStyle().Foreground(colorGold).Render(prompt+" ") +
			lipgloss.NewStyle().Foreground(colorSelFg).Render(m.campAddInput) + cur
	} else {
		hintText = sDim.Render("Tab=verden  ↑↓=naviger  → Enter=åbn/fold  a=spiller  s=session  c=kampagne  i=initiative  r=omdøb  d=slet  Ctrl+R=indsæt ref (under redigering)")
	}
	searchLine := lipgloss.NewStyle().Background(colorBg).Width(m.width).Padding(0, 1).Render(hintText)
	borderLine := lipgloss.NewStyle().Foreground(lipgloss.Color(sBorderNormal)).Render(strings.Repeat("─", m.width))
	searchBar := searchLine + "\n" + borderLine

	// Sidebar — render from campItems
	sideLabel := lipgloss.NewStyle().Background(colorSideBg).Foreground(colorGreen).Bold(true).Padding(0, 1).
		Render(fmt.Sprintf("KAMPAGNER  %d", len(m.campaign.Campaigns)))
	sideHeader := lipgloss.NewStyle().Background(colorSideBg).
		Border(lipgloss.NormalBorder(), false, false, true, false).
		BorderForeground(colorGreen).Width(sideW).Render(sideLabel)

	var sideLines []string
	maxVisible := vis - 1
	for i, it := range m.campItems {
		if i >= maxVisible {
			break
		}
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
	for len(sideLines) < maxVisible {
		sideLines = append(sideLines, lipgloss.NewStyle().Background(colorSideBg).Render(strings.Repeat(" ", sideW)))
	}

	// Content pane
	contentW := m.width - sideW - 1

	// ── Initiative tracker overlay ────────────────────────────
	if m.showInitiative {
		camp := m.campaign.Campaigns[m.initCampIdx]
		initList := camp.Initiative
		iTitle := lipgloss.NewStyle().Foreground(colorGold).Bold(true).Render("⚔  Initiative — " + camp.Name)
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
				initHint = lipgloss.NewStyle().Foreground(colorGold).Render("Initiative for "+m.initAddName+": ") +
					lipgloss.NewStyle().Foreground(colorSelFg).Render(m.initAddInput) + cur
			}
		} else {
			initHint = sDim.Render("n/Space=næste  p=forrige  r=top  a=tilføj  d=slet  +=op  -=ned  X=ryd  Esc=luk")
		}
		iSearchLine := lipgloss.NewStyle().Background(colorBg).Width(m.width).Padding(0, 1).Render(initHint)
		iBorderLine := lipgloss.NewStyle().Foreground(colorGold).Render(strings.Repeat("─", m.width))
		iSearchBar := iSearchLine + "\n" + iBorderLine

		// Status bar
		var iKeys [][]string
		if m.initAdding {
			iKeys = [][]string{{"Enter", "bekræft"}, {"Esc", "annuller"}}
		} else {
			iKeys = [][]string{{"n Space", "næste tur"}, {"p", "forrige"}, {"r", "top"}, {"a", "tilføj"}, {"d", "slet"}, {"+/-", "justér"}, {"X", "ryd"}, {"Esc", "luk"}}
		}
		var iKeyParts []string
		for _, k := range iKeys {
			iKeyParts = append(iKeyParts, sKey.Render(k[0])+" "+sDim.Render(k[1]))
		}
		iStatusBar := lipgloss.NewStyle().Background(colorBg).Width(m.width).Padding(0, 1).Render(strings.Join(iKeyParts, "  "))

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
		return header + "\n" + iSearchBar + "\n" + strings.Join(iMainLines, "\n") + "\n" + iStatusBar
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
		var lines []string
		lines = append(lines, "")
		if m.campRefInsert {
			lines = append(lines, "  "+lipgloss.NewStyle().Foreground(colorSelFg).Bold(true).Render("↗  Indsæt reference")+"  "+sDim.Render("skriv for at søge"))
		} else {
			lines = append(lines, "  "+lipgloss.NewStyle().Foreground(colorSelFg).Bold(true).Render("↗  Følg reference"))
		}
		lines = append(lines, "  "+sDim.Render(strings.Repeat("─", contentW-4)))
		if len(filtered) == 0 {
			lines = append(lines, "  "+sDim.Render("Ingen resultater"))
		}
		for i, r := range filtered {
			icon := resourceIcon(r.IconGlyph)
			name := r.Name
			if i == m.campRefCursor {
				accent := lipgloss.NewStyle().Foreground(colorSelFg).Render("▌ ")
				lines = append(lines, "  "+accent+lipgloss.NewStyle().Foreground(colorSelFg).Bold(true).Render(icon+name))
			} else {
				lines = append(lines, "    "+lipgloss.NewStyle().Foreground(colorAccent).Render(icon+name))
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
		if len(camp.Players) == 0 {
			lines = append(lines, "  "+sDim.Render("Ingen spillere — tryk 'a' for at tilføje"))
		} else {
			lines = append(lines, "  "+sDim.Render("Spillere:"))
			for _, p := range camp.Players {
				lines = append(lines, "    "+lipgloss.NewStyle().Foreground(colorBright).Render("👤  "+p.Name))
			}
		}
		if len(camp.Sessions) > 0 {
			lines = append(lines, "")
			lines = append(lines, "  "+sDim.Render("Sessioner:"))
			for _, s := range camp.Sessions {
				lines = append(lines, "    "+lipgloss.NewStyle().Foreground(colorBright).Render("📅  "+s.Name))
			}
		}
		contentBody = strings.Join(lines, "\n")
	} else {
		note := m.campCurrentNote()
		if note == "" {
			contentBody = "\n  " + sDim.Render("Ingen noter endnu — tryk Enter for at begynde.")
		} else {
			hasRefs := len(m.campNoteRefs(note)) > 0
			var lines []string
			for _, l := range strings.Split(note, "\n") {
				rendered := "  " + applyInlineStyles(highlightMentions(l))
				lines = append(lines, rendered)
			}
			body := "\n" + strings.Join(lines, "\n")
			if hasRefs {
				body += "\n\n  " + sDim.Render("f") + sDim.Render(" = følg reference")
			}
			contentBody = body
		}
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

	// Status bar
	var keys [][]string
	if m.campConfirm {
		keys = [][]string{{"j", "slet"}, {"n / Esc", "annuller"}}
	} else if m.campRenaming {
		keys = [][]string{{"Enter", "gem"}, {"Esc", "annuller"}}
	} else if m.campEditing {
		keys = [][]string{{"Enter", "gem & luk"}, {"Esc", "gem & luk"}}
	} else {
		keys = [][]string{{"↑↓ jk", "naviger"}, {"→ ←", "fold"}, {"Enter", "åbn/rediger"}, {"a", "spiller"}, {"s", "session"}, {"c", "kampagne"}, {"i", "initiative"}, {"r", "omdøb"}, {"d", "slet"}, {"Tab", "verden"}}
	}
	var keyParts []string
	for _, k := range keys {
		keyParts = append(keyParts, sKey.Render(k[0])+" "+sDim.Render(k[1]))
	}
	statusBar := lipgloss.NewStyle().Background(colorBg).Width(m.width).Padding(0, 1).Render(strings.Join(keyParts, "  "))

	return header + "\n" + searchBar + "\n" + main + "\n" + statusBar
}

// visLen returns the visible display width of a string, stripping ANSI escapes
// and using go-runewidth for accurate wide-character (emoji, CJK) measurement.
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

func (m *model) campCurrentItem() *campListItem {
	if m.campCursor >= 0 && m.campCursor < len(m.campItems) {
		return &m.campItems[m.campCursor]
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

func (m *model) campRefFiltered() []Resource {
	q := strings.ToLower(m.campRefSearch)
	var out []Resource
	if m.campRefInsert {
		// search all resources by name
		for _, r := range m.resources {
			if q == "" || strings.Contains(strings.ToLower(r.Name), q) {
				out = append(out, r)
			}
			if len(out) >= 50 {
				break
			}
		}
	} else {
		// only refs found in the current note
		note := m.campCurrentNote()
		refs := m.campNoteRefs(note)
		for _, r := range refs {
			if q == "" || strings.Contains(strings.ToLower(r.Name), q) {
				out = append(out, r)
			}
		}
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

func main() {
	if len(os.Args) > 1 {
		dataPath = os.Args[1]
	} else {
		home, _ := os.UserHomeDir()
		path, err := newestJSON(filepath.Join(home, "Hentet"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Angiv en fil: aelden <fil.json>\n(%v)\n", err)
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
