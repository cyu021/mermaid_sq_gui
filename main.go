package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"path/filepath"
	"time"
)

func logMsg(msg string) {
	pc, file, line, ok := runtime.Caller(1)
	if !ok {
		fmt.Printf("[%s] %s\n", time.Now().Format("2006-01-02 15:04:05.000"), msg)
		return
	}
	fn := runtime.FuncForPC(pc).Name()
	if idx := strings.LastIndex(fn, "."); idx != -1 {
		fn = fn[idx+1:]
	}
	file = filepath.Base(file)
	fmt.Printf("[%s] %s (%s:%d) - %s\n", time.Now().Format("2006-01-02 15:04:05.000"), fn, file, line, msg)
}

func init() {
	// Set GOMAXPROCS to NumCPU() - 2 (minimum 1) if not already set (adaptive logic)
	if os.Getenv("GOMAXPROCS") == "" {
		n := runtime.NumCPU() - 2
		if n < 1 {
			n = 1
		}
		runtime.GOMAXPROCS(n)
	}

	// Adaptive FYNE_SCALE logic
	// If not set, we'll let Fyne handle it automatically or set a default if needed
	if os.Getenv("FYNE_SCALE") == "" {
		// Example: os.Setenv("FYNE_SCALE", "1.0")
	}
}

type Participant struct {
	Name       string
	Alias      string
	SourceLine int
}

type DiagramElement interface {
	IsElement()
	GetLine() int
}

type Message struct {
	From       string
	To         string
	Text       string
	LineType   string // ->, ->>, --, -->>
	ArrowHead  bool   // true for ->, ->>, false for -
	Dashed     bool   // true for --
	SourceLine int
}

func (m Message) IsElement()   {}
func (m Message) GetLine() int { return m.SourceLine }

type Note struct {
	Actor1     string
	Actor2     string // for "over"
	Text       string
	Type       string // "left of", "right of", "over"
	SourceLine int
}

func (n Note) IsElement()   {}
func (n Note) GetLine() int { return n.SourceLine }

type BlockSection struct {
	Label      string
	Elements []DiagramElement
	SourceLine int
	EndLine    int
}

type Block struct {
	Type       string // alt, loop, opt
	Label      string
	Sections   []BlockSection
	SourceLine int
	EndLine    int
}

func (b *Block) IsElement()   {}
func (b *Block) GetLine() int { return b.SourceLine }

type SequenceDiagram struct {
	Participants []Participant
	Elements     []DiagramElement
	AutoNumber   bool
}

var _ fyne.Tappable = (*clickArea)(nil)
var _ fyne.SecondaryTappable = (*clickArea)(nil)

type clickArea struct {
	widget.BaseWidget
	onTap          func()
	onSecondaryTap func(*fyne.PointEvent)
}

func newClickArea(onTap func(), onSecondaryTap func(*fyne.PointEvent)) *clickArea {
	c := &clickArea{onTap: onTap, onSecondaryTap: onSecondaryTap}
	c.ExtendBaseWidget(c)
	return c
}

func (c *clickArea) CreateRenderer() fyne.WidgetRenderer {
	return &clickAreaRenderer{rect: canvas.NewRectangle(color.Transparent)}
}

type clickAreaRenderer struct {
	rect *canvas.Rectangle
}

func (r *clickAreaRenderer) Layout(size fyne.Size) {
	r.rect.Resize(size)
}

func (r *clickAreaRenderer) MinSize() fyne.Size {
	return fyne.NewSize(0, 0)
}

func (r *clickAreaRenderer) Refresh() {
}

func (r *clickAreaRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.rect}
}

func (r *clickAreaRenderer) Destroy() {
}

func (c *clickArea) Tapped(_ *fyne.PointEvent) {
	if c.onTap != nil {
		c.onTap()
	}
}

func (c *clickArea) TappedSecondary(pe *fyne.PointEvent) {
	if c.onSecondaryTap != nil {
		c.onSecondaryTap(pe)
	}
}

type canvasLayout struct {
	app *editorApp
}

func (l *canvasLayout) Layout(objects []fyne.CanvasObject, _ fyne.Size) {
	for _, o := range objects {
		o.Resize(l.MinSize(objects))
		o.Move(fyne.NewPos(0, 0))
	}
}

func (l *canvasLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	return l.app.diagramSize
}

type editorApp struct {
	window       fyne.Window
	entry        *widget.Entry
	renderArea   *fyne.Container
	scroll       *container.Scroll
	currentFile  fyne.URI
	statusLabel  *widget.Label
	diagramSize  fyne.Size
	lastDiagram  SequenceDiagram
	detectedOSScale float64
	zoomScale    float32
	stickyHeader *fyne.Container
	stickyContainer *fyne.Container
}

func (e *editorApp) getExeDir() fyne.ListableURI {
	exePath, err := os.Executable()
	if err != nil {
		return nil
	}
	exeDir := filepath.Dir(exePath)
	uri := storage.NewFileURI(exeDir)
	lister, err := storage.ListerForURI(uri)
	if err != nil {
		return nil
	}
	return lister
}

func (e *editorApp) updateStickyHeader() {
	if e.scroll == nil || e.stickyContainer == nil {
		return
	}

	padding := float32(50)
	
	// If scrolled past the initial participant boxes
	if e.scroll.Offset.Y > padding {
		e.drawStickyHeader()
		e.stickyContainer.Show()
		// Sync horizontal scroll
		e.stickyHeader.Move(fyne.NewPos(-e.scroll.Offset.X, 0))
	} else {
		e.stickyContainer.Hide()
	}
}

func (e *editorApp) drawStickyHeader() {
	e.stickyHeader.Objects = nil
	
	padding := float32(50) * e.zoomScale
	pWidth := float32(120) * e.zoomScale
	pHeight := float32(40) * e.zoomScale
	hGap := float32(200) * e.zoomScale

	for i, p := range e.lastDiagram.Participants {
		x := padding + float32(i)*hGap
		y := float32(0) // Top of sticky container

		txt := canvas.NewText(p.Alias, theme.ForegroundColor())
		txt.Alignment = fyne.TextAlignCenter
		txt.TextSize = 14 * e.zoomScale
		txt.TextStyle = fyne.TextStyle{Bold: true}

		minSize := txt.MinSize()
		boxWidth := pWidth
		if minSize.Width+30*e.zoomScale > boxWidth {
			boxWidth = minSize.Width + 30*e.zoomScale
		}

		rect := canvas.NewRectangle(theme.ButtonColor())
		rect.StrokeColor = theme.PrimaryColor()
		rect.StrokeWidth = 2 * e.zoomScale
		rect.Resize(fyne.NewSize(boxWidth, pHeight))

		boxX := x + pWidth/2 - boxWidth/2
		rect.Move(fyne.NewPos(boxX, y))
		e.stickyHeader.Add(rect)

		txt.Move(fyne.NewPos(boxX, y+(pHeight-minSize.Height)/2))
		txt.Resize(fyne.NewSize(boxWidth, minSize.Height))
		e.stickyHeader.Add(txt)
	}
	e.stickyHeader.Refresh()
}

func (e *editorApp) zoomIn() {
	e.zoomScale += 0.1
	if e.zoomScale > 3.0 {
		e.zoomScale = 3.0
	}
	e.updatePreview()
}

func (e *editorApp) zoomOut() {
	e.zoomScale -= 0.1
	if e.zoomScale < 0.1 {
		e.zoomScale = 0.1
	}
	e.updatePreview()
}

func (e *editorApp) showScalingInfo() {
	s := fmt.Sprintf("Cross-Platform Scaling - OS: %s\nDetected OS Scale: %.2f\nApplied Zoom Scale: %.2f (Internal)\n\nFyne Canvas Raw Scale: %f\nFYNE_SCALE Env: %s",
		runtime.GOOS, e.detectedOSScale, e.zoomScale, e.window.Canvas().Scale(), os.Getenv("FYNE_SCALE"))
	dialog.ShowInformation("UI Scaling Info", s, e.window)
}

func (e *editorApp) updatePreview() {
	text := e.entry.Text
	mermaidCode, lineOffset := extractMermaidCode(text)
	e.lastDiagram = parseSequenceDiagram(mermaidCode, lineOffset)
	e.renderArea.Objects = nil
	e.drawDiagram(e.lastDiagram)
	e.renderArea.Refresh()
	e.updateStickyHeader()

	// Cleanup empty blocks after preview update
	newText := e.cleanupEmptyBlocks(text, e.lastDiagram)
	if newText != text {
		e.entry.SetText(newText)
	}
}

func (e *editorApp) cleanupEmptyBlocks(text string, sd SequenceDiagram) string {
	lines := strings.Split(text, "\n")
	toRemove := make(map[int]bool)

	var checkBlock func(b *Block)
	checkBlock = func(b *Block) {
		// If the whole block covers no participants, mark all its lines for removal
		minIdx, _ := e.getBlockParticipantsRange(b, sd)
		if minIdx == -1 {
			for i := b.SourceLine; i <= b.EndLine; i++ {
				toRemove[i] = true
			}
			return
		}

		// Check individual else sections (except the first one which is part of the block start)
		for i := 1; i < len(b.Sections); i++ {
			s := b.Sections[i]
			sMin, _ := e.getElementsParticipantsRange(s.Elements, sd)
			if sMin == -1 {
				// Remove lines from 'else' start to end of section
				for j := s.SourceLine; j <= s.EndLine; j++ {
					toRemove[j] = true
				}
			}
		}

		// Recurse into nested blocks
		for _, s := range b.Sections {
			for _, el := range s.Elements {
				if nb, ok := el.(*Block); ok {
					checkBlock(nb)
				}
			}
		}
	}

	// Find blocks at the top level
	for _, el := range sd.Elements {
		if b, ok := el.(*Block); ok {
			checkBlock(b)
		}
	}

	if len(toRemove) == 0 {
		return text
	}

	var newLines []string
	for i, line := range lines {
		if !toRemove[i] {
			newLines = append(newLines, line)
		}
	}
	return strings.Join(newLines, "\n")
}

func extractMermaidCode(text string) (string, int) {
	lines := strings.Split(text, "\n")
	inBlock := false
	var block []string
	startLine := 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```mermaid") {
			inBlock = true
			startLine = i + 1
			continue
		}
		if inBlock && strings.HasPrefix(trimmed, "```") {
			inBlock = false
			break
		}
		if inBlock {
			block = append(block, line)
		}
	}
	if len(block) > 0 {
		return strings.Join(block, "\n"), startLine
	}
	return text, 0
}

func parseSequenceDiagram(text string, startLineOffset int) SequenceDiagram {
	var sd SequenceDiagram
	currentElements := &sd.Elements
	var stack []*[]DiagramElement
	var blockStack []*Block

	lines := strings.Split(text, "\n")
	
	for i, line := range lines {
		absLine := i + startLineOffset
		line = strings.TrimSpace(line)
		if line == "" || line == "sequenceDiagram" {
			continue
		}
		
		if line == "autonumber" {
			sd.AutoNumber = true
			continue
		}

		if strings.HasPrefix(line, "participant ") {
			pStr := strings.TrimPrefix(line, "participant ")
			if strings.Contains(pStr, " as ") {
				parts := strings.Split(pStr, " as ")
				sd.Participants = append(sd.Participants, Participant{
					Name:       strings.TrimSpace(parts[0]),
					Alias:      strings.TrimSpace(parts[1]),
					SourceLine: absLine,
				})
			} else {
				p := strings.TrimSpace(pStr)
				sd.Participants = append(sd.Participants, Participant{Name: p, Alias: p, SourceLine: absLine})
			}
			continue
		}

		if strings.HasPrefix(line, "loop ") {
			label := strings.TrimPrefix(line, "loop ")
			b := &Block{Type: "loop", Label: label, Sections: []BlockSection{{Label: label, SourceLine: absLine}}, SourceLine: absLine}
			*currentElements = append(*currentElements, b)
			stack = append(stack, currentElements)
			blockStack = append(blockStack, b)
			currentElements = &b.Sections[0].Elements
			continue
		}

		if strings.HasPrefix(line, "alt ") {
			label := strings.TrimPrefix(line, "alt ")
			b := &Block{Type: "alt", Label: "alt", Sections: []BlockSection{{Label: label, SourceLine: absLine}}, SourceLine: absLine}
			*currentElements = append(*currentElements, b)
			stack = append(stack, currentElements)
			blockStack = append(blockStack, b)
			currentElements = &b.Sections[0].Elements
			continue
		}

		if strings.HasPrefix(line, "opt ") {
			label := strings.TrimPrefix(line, "opt ")
			b := &Block{Type: "opt", Label: "opt", Sections: []BlockSection{{Label: label, SourceLine: absLine}}, SourceLine: absLine}
			*currentElements = append(*currentElements, b)
			stack = append(stack, currentElements)
			blockStack = append(blockStack, b)
			currentElements = &b.Sections[0].Elements
			continue
		}

		if strings.HasPrefix(line, "else") {
			if len(blockStack) > 0 && blockStack[len(blockStack)-1].Type == "alt" {
				b := blockStack[len(blockStack)-1]
				// Set EndLine for previous section
				b.Sections[len(b.Sections)-1].EndLine = absLine - 1
				
				label := strings.TrimSpace(strings.TrimPrefix(line, "else"))
				b.Sections = append(b.Sections, BlockSection{Label: label, SourceLine: absLine})
				currentElements = &b.Sections[len(b.Sections)-1].Elements
			}
			continue
		}

		if line == "end" {
			if len(stack) > 0 {
				b := blockStack[len(blockStack)-1]
				b.EndLine = absLine
				b.Sections[len(b.Sections)-1].EndLine = absLine - 1

				currentElements = stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				blockStack = blockStack[:len(blockStack)-1]
			}
			continue
		}

		if strings.HasPrefix(line, "Note ") {
			notePart := strings.TrimPrefix(line, "Note ")
			var noteType, actorsStr, noteText string
			
			if strings.HasPrefix(notePart, "left of ") {
				noteType = "left of"
				rem := strings.TrimPrefix(notePart, "left of ")
				idx := strings.Index(rem, ":")
				if idx != -1 {
					actorsStr = strings.TrimSpace(rem[:idx])
					noteText = strings.TrimSpace(rem[idx+1:])
				}
			} else if strings.HasPrefix(notePart, "right of ") {
				noteType = "right of"
				rem := strings.TrimPrefix(notePart, "right of ")
				idx := strings.Index(rem, ":")
				if idx != -1 {
					actorsStr = strings.TrimSpace(rem[:idx])
					noteText = strings.TrimSpace(rem[idx+1:])
				}
			} else if strings.HasPrefix(notePart, "over ") {
				noteType = "over"
				rem := strings.TrimPrefix(notePart, "over ")
				idx := strings.Index(rem, ":")
				if idx != -1 {
					actorsStr = strings.TrimSpace(rem[:idx])
					noteText = strings.TrimSpace(rem[idx+1:])
				}
			}
			
			if noteType != "" {
				actors := strings.Split(actorsStr, ",")
				a1 := strings.TrimSpace(actors[0])
				a2 := ""
				if len(actors) > 1 {
					a2 = strings.TrimSpace(actors[1])
				}
				*currentElements = append(*currentElements, Note{Actor1: a1, Actor2: a2, Text: noteText, Type: noteType, SourceLine: absLine})
				addIfMissing(&sd, a1, absLine)
				if a2 != "" {
					addIfMissing(&sd, a2, absLine)
				}
				continue
			}
		}

		// Handle messages
		if strings.Contains(line, "->") || strings.Contains(line, "--") {
			parts := strings.Split(line, ":")
			msgText := ""
			if len(parts) > 1 {
				msgText = strings.TrimSpace(parts[1])
			}
			
			connPart := strings.TrimSpace(parts[0])
			var lineType string
			var dashed, arrowhead bool
			
			if strings.Contains(connPart, "-->>") {
				lineType = "-->>"
				dashed = true
				arrowhead = true
			} else if strings.Contains(connPart, "->>") {
				lineType = "->>"
				dashed = false
				arrowhead = true
			} else if strings.Contains(connPart, "--") {
				lineType = "--"
				dashed = true
				arrowhead = false
			} else if strings.Contains(connPart, "->") {
				lineType = "->"
				dashed = false
				arrowhead = true
			}
			
			if lineType != "" {
				nodes := strings.Split(connPart, lineType)
				if len(nodes) == 2 {
					from := strings.TrimSpace(nodes[0])
					to := strings.TrimSpace(nodes[1])
					*currentElements = append(*currentElements, Message{
						From:       from,
						To:         to,
						Text:       msgText,
						LineType:   lineType,
						Dashed:     dashed,
						ArrowHead:  arrowhead,
						SourceLine: absLine,
					})
					
					addIfMissing(&sd, from, absLine)
					addIfMissing(&sd, to, absLine)
				}
			}
		}
	}
	return sd
}

func addIfMissing(sd *SequenceDiagram, name string, line int) {
	if name == "" {
		return
	}
	for _, p := range sd.Participants {
		if p.Name == name || p.Alias == name {
			return
		}
	}
	sd.Participants = append(sd.Participants, Participant{Name: name, Alias: name, SourceLine: line})
}

func (e *editorApp) highlightLine(lineNum int) {
	lines := strings.Split(e.entry.Text, "\n")
	if lineNum < 0 || lineNum >= len(lines) {
		return
	}
	
	// Ensure entry is focused
	e.window.Canvas().Focus(e.entry)

	// Step 1: Move cursor to beginning of line and ensure selection is cleared
	e.entry.CursorRow = lineNum
	e.entry.CursorColumn = 0
	e.entry.Refresh()
	// Send a dummy key move without shift to clear any existing selection
	e.entry.TypedKey(&fyne.KeyEvent{Name: fyne.KeyHome})

	// Step 2: Start selection (simulating shift down)
	e.entry.KeyDown(&fyne.KeyEvent{Name: desktop.KeyShiftLeft})
	
	// Step 3: Move cursor to end of line while shift is down
	e.entry.TypedKey(&fyne.KeyEvent{Name: fyne.KeyEnd})
	
	// Step 4: Release shift
	e.entry.KeyUp(&fyne.KeyEvent{Name: desktop.KeyShiftLeft})
	
	// Final refresh to ensure UI shows the new selection
	e.entry.Refresh()
	
	e.statusLabel.SetText(fmt.Sprintf("Selected line %d", lineNum+1))
}

func (e *editorApp) deleteParticipant(name string) {
	lines := strings.Split(e.entry.Text, "\n")
	var newLines []string
	
	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`)

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Skip diagram header and block markers
		if trimmed == "sequenceDiagram" || trimmed == "autonumber" || strings.HasPrefix(trimmed, "```") {
			newLines = append(newLines, line)
			continue
		}
		
		if re.MatchString(line) {
			// If it matches the participant name, we skip this line (delete it)
			continue
		}
		newLines = append(newLines, line)
	}
	
	e.entry.SetText(strings.Join(newLines, "\n"))
	e.updatePreview()
	e.statusLabel.SetText("Deleted participant " + name + " and related elements")
}

func (e *editorApp) showParticipantMenu(p Participant, pe *fyne.PointEvent) {
	menu := fyne.NewMenu("",
		fyne.NewMenuItem("Delete "+p.Name, func() {
			e.deleteParticipant(p.Name)
		}),
	)
	widget.ShowPopUpMenuAtPosition(menu, e.window.Canvas(), pe.AbsolutePosition)
}

func (e *editorApp) drawDiagram(sd SequenceDiagram) {
	logMsg(fmt.Sprintf("Drawing diagram with %d participants and %d top-level elements", len(sd.Participants), len(sd.Elements)))
	if len(sd.Participants) == 0 {
		e.diagramSize = fyne.NewSize(100, 100)
		e.renderArea.Objects = nil
		e.renderArea.Refresh()
		return
	}

	padding := float32(50) * e.zoomScale
	pWidth := float32(120) * e.zoomScale
	pHeight := float32(40) * e.zoomScale
	vGap := float32(60) * e.zoomScale
	hGap := float32(200) * e.zoomScale

	totalHeight := e.calculateHeight(sd.Elements, vGap, sd) + padding*2 + pHeight*2 + 50*e.zoomScale
	lifelineLength := totalHeight - padding*2 - pHeight

	// Draw lifelines
	for i, p := range sd.Participants {
		x := padding + float32(i)*hGap
		y := padding

		line := canvas.NewLine(theme.DisabledColor())
		line.Position1 = fyne.NewPos(x+pWidth/2, y+pHeight)
		line.Position2 = fyne.NewPos(x+pWidth/2, y+lifelineLength)
		line.StrokeWidth = 1
		e.renderArea.Add(line)

		e.drawParticipantBox(p, x, y, pWidth, pHeight)
		e.drawParticipantBox(p, x, y+lifelineLength, pWidth, pHeight)
	}

	msgCount := 0
	e.drawElements(sd.Elements, padding+pHeight+vGap/2, &msgCount, sd, pWidth, pHeight, vGap, hGap, padding, 0)

	totalWidth := padding*2 + float32(len(sd.Participants))*hGap
	e.diagramSize = fyne.NewSize(totalWidth, totalHeight)
}

func (e *editorApp) calculateHeight(elements []DiagramElement, vGap float32, sd SequenceDiagram) float32 {
	var h float32
	for _, el := range elements {
		switch v := el.(type) {
		case Message, Note:
			h += vGap
		case *Block:
			minIdx, _ := e.getBlockParticipantsRange(v, sd)
			if minIdx == -1 {
				continue
			}

			h += 20 // Header
			for i, s := range v.Sections {
				if i > 0 {
					sMin, _ := e.getElementsParticipantsRange(s.Elements, sd)
					if sMin == -1 {
						continue
					}
					h += 40 // Else divider and gap
				}
				h += e.calculateHeight(s.Elements, vGap, sd)
			}
			h += 40 // Footer and block gap
		}
	}
	return h
}

func (e *editorApp) drawElements(elements []DiagramElement, startY float32, msgCount *int, sd SequenceDiagram, pWidth, pHeight, vGap, hGap, padding float32, depth int) float32 {
	y := startY
	for _, el := range elements {
		switch v := el.(type) {
		case Message:
			*msgCount++
			fromIdx := getParticipantIdx(sd, v.From)
			toIdx := getParticipantIdx(sd, v.To)
			if fromIdx != -1 && toIdx != -1 {
				xFrom := padding + float32(fromIdx)*hGap + pWidth/2
				xTo := padding + float32(toIdx)*hGap + pWidth/2

				if fromIdx == toIdx {
					// Self request
					e.drawSelfRequest(v, xFrom, y, sd.AutoNumber, *msgCount)
				} else {
					col := theme.PrimaryColor()
					if v.Dashed {
						e.drawDashedLine(xFrom, xTo, y, col)
					} else {
						line := canvas.NewLine(col)
						line.Position1 = fyne.NewPos(xFrom, y)
						line.Position2 = fyne.NewPos(xTo, y)
						line.StrokeWidth = 2 * e.zoomScale
						e.renderArea.Add(line)
					}
					if v.ArrowHead {
						e.drawArrowHead(xFrom, xTo, y, v.LineType)
					}
					text := v.Text
					if sd.AutoNumber {
						text = fmt.Sprintf("%d: %s", *msgCount, text)
					}
					txt := canvas.NewText(text, theme.ForegroundColor())
					txt.TextSize = 12 * e.zoomScale
					txt.Move(fyne.NewPos((xFrom+xTo)/2-100*e.zoomScale, y-20*e.zoomScale))
					txt.Resize(fyne.NewSize(200*e.zoomScale, 20*e.zoomScale))
					txt.Alignment = fyne.TextAlignCenter
					e.renderArea.Add(txt)
				}

				// Click area
				lineY := v.SourceLine
				ca := newClickArea(func() { e.highlightLine(lineY) }, nil)
				if fromIdx == toIdx {
					ca.Move(fyne.NewPos(xFrom, y-20*e.zoomScale))
					ca.Resize(fyne.NewSize(60*e.zoomScale, 40*e.zoomScale))
				} else {
					ca.Move(fyne.NewPos(min(xFrom, xTo), y-20*e.zoomScale))
					ca.Resize(fyne.NewSize(abs(xFrom-xTo), 40*e.zoomScale))
				}
				e.renderArea.Add(ca)
			}
			y += vGap

		case Note:
			idx1 := getParticipantIdx(sd, v.Actor1)
			if idx1 != -1 {
				x1 := padding + float32(idx1)*hGap + pWidth/2
				var nx, ny, nw, nh float32
				nh = 30 * e.zoomScale
				ny = y - nh/2

				// Create temporary text to measure min size
				tmpTxt := canvas.NewText(v.Text, color.Black)
				tmpTxt.TextSize = 11 * e.zoomScale
				minW := tmpTxt.MinSize().Width + 20*e.zoomScale

				if v.Type == "left of" {
					nw = minW
					nx = x1 - nw - 10*e.zoomScale
				} else if v.Type == "right of" {
					nw = minW
					nx = x1 + 10*e.zoomScale
				} else if v.Type == "over" {
					if v.Actor2 != "" {
						idx2 := getParticipantIdx(sd, v.Actor2)
						if idx2 != -1 {
							x2 := padding + float32(idx2)*hGap + pWidth/2
							if x1 > x2 {
								x1, x2 = x2, x1
							}
							nx = x1 - 20*e.zoomScale
							nw = (x2 - x1) + 40*e.zoomScale
							// If text is wider than participant gap, expand note
							if minW > nw {
								diff := minW - nw
								nx -= diff / 2
								nw = minW
							}
						} else {
							nw = minW
							nx = x1 - nw/2
						}
					} else {
						nw = minW
						nx = x1 - nw/2
					}
				}
				rect := canvas.NewRectangle(color.RGBA{R: 255, G: 255, B: 200, A: 255})
				rect.StrokeColor = color.Black
				rect.StrokeWidth = 1 * e.zoomScale
				rect.Resize(fyne.NewSize(nw, nh))
				rect.Move(fyne.NewPos(nx, ny))
				e.renderArea.Add(rect)
				txt := canvas.NewText(v.Text, color.Black)
				txt.TextSize = 11 * e.zoomScale
				txt.Alignment = fyne.TextAlignCenter
				txt.Move(fyne.NewPos(nx, ny+(nh-15*e.zoomScale)/2))
				txt.Resize(fyne.NewSize(nw, 15*e.zoomScale))
				e.renderArea.Add(txt)

				// Click area
				lineY := v.SourceLine
				ca := newClickArea(func() { e.highlightLine(lineY) }, nil)
				ca.Move(fyne.NewPos(nx, ny))
				ca.Resize(fyne.NewSize(nw, nh))
				e.renderArea.Add(ca)
			}
			y += vGap

		case *Block:
			minIdx, maxIdx := e.getBlockParticipantsRange(v, sd)
			if minIdx == -1 {
				continue
			}

			blockStartY := y - 20*e.zoomScale
			blockX := padding + float32(minIdx)*hGap - 10*e.zoomScale + float32(depth)*8*e.zoomScale
			blockWidth := float32(maxIdx-minIdx)*hGap + pWidth + 20*e.zoomScale - float32(depth)*16*e.zoomScale

			// Click area for block header
			lineY := v.SourceLine
			ca := newClickArea(func() { e.highlightLine(lineY) }, nil)
			ca.Move(fyne.NewPos(blockX, blockStartY))
			ca.Resize(fyne.NewSize(50*e.zoomScale, 20*e.zoomScale))
			e.renderArea.Add(ca)

			// Draw Block Header
			headerRect := canvas.NewRectangle(color.RGBA{R: 230, G: 230, B: 230, A: 255})
			headerRect.Resize(fyne.NewSize(50*e.zoomScale, 20*e.zoomScale))
			headerRect.Move(fyne.NewPos(blockX, blockStartY))
			e.renderArea.Add(headerRect)

			label := canvas.NewText(v.Type, color.Black)
			label.TextSize = 10 * e.zoomScale
			label.TextStyle = fyne.TextStyle{Bold: true}
			label.Move(fyne.NewPos(blockX+5*e.zoomScale, blockStartY+2*e.zoomScale))
			e.renderArea.Add(label)

			y += 20 * e.zoomScale
			for i, s := range v.Sections {
				// Skip empty else sections (sections after the first one) if they have no participants
				if i > 0 {
					sMin, _ := e.getElementsParticipantsRange(s.Elements, sd)
					if sMin == -1 {
						continue
					}

					y += 10 * e.zoomScale
					// Draw else divider - align to top of box (y-15*e.zoomScale)
					e.drawDashedLine(blockX, blockX+blockWidth, y-15*e.zoomScale, theme.DisabledColor())

					// Draw "else" box
					elseRect := canvas.NewRectangle(color.RGBA{R: 230, G: 230, B: 230, A: 255})
					elseRect.Resize(fyne.NewSize(50*e.zoomScale, 20*e.zoomScale))
					elseRect.Move(fyne.NewPos(blockX, y-15*e.zoomScale))
					e.renderArea.Add(elseRect)

					// Click area for else
					caElse := newClickArea(func() { e.highlightLine(s.SourceLine) }, nil)
					caElse.Move(fyne.NewPos(blockX, y-15*e.zoomScale))
					caElse.Resize(fyne.NewSize(50*e.zoomScale, 20*e.zoomScale))
					e.renderArea.Add(caElse)

					elseLabel := canvas.NewText("else", color.Black)
					elseLabel.TextSize = 10 * e.zoomScale
					elseLabel.TextStyle = fyne.TextStyle{Bold: true}
					elseLabel.Move(fyne.NewPos(blockX+5*e.zoomScale, y-13*e.zoomScale))
					e.renderArea.Add(elseLabel)

					eLabel := canvas.NewText("["+s.Label+"]", theme.ForegroundColor())
					eLabel.TextSize = 10 * e.zoomScale
					eLabel.Move(fyne.NewPos(blockX+60*e.zoomScale, y-13*e.zoomScale))
					e.renderArea.Add(eLabel)
					y += 30 * e.zoomScale
				} else {
					condLabel := canvas.NewText("["+s.Label+"]", theme.ForegroundColor())
					condLabel.TextSize = 10 * e.zoomScale
					condLabel.Move(fyne.NewPos(blockX+60*e.zoomScale, blockStartY+2*e.zoomScale))
					e.renderArea.Add(condLabel)
				}
				y = e.drawElements(s.Elements, y, msgCount, sd, pWidth, pHeight, vGap, hGap, padding, depth+1)
			}

			blockHeight := y - blockStartY
			border := canvas.NewRectangle(color.Transparent)
			border.StrokeColor = theme.DisabledColor()
			border.StrokeWidth = 1 * e.zoomScale
			border.Resize(fyne.NewSize(blockWidth, blockHeight))
			border.Move(fyne.NewPos(blockX, blockStartY))
			e.renderArea.Add(border)
			y += 20 * e.zoomScale // Footer space
			y += 20 * e.zoomScale // Gap between blocks
		}
	}
	return y
}

func (e *editorApp) drawSelfRequest(v Message, x, y float32, autoNumber bool, count int) {
	col := theme.PrimaryColor()
	loopWidth := float32(40) * e.zoomScale
	loopHeight := float32(20) * e.zoomScale

	// Draw the loop line (top, right, bottom)
	if v.Dashed {
		e.drawDashedLine(x, x+loopWidth, y-loopHeight, col)
		// Vertical dashed line
		for vy := y - loopHeight; vy < y; vy += 10 * e.zoomScale {
			l := canvas.NewLine(col)
			l.Position1 = fyne.NewPos(x+loopWidth, vy)
			l.Position2 = fyne.NewPos(x+loopWidth, min(y, vy+5*e.zoomScale))
			l.StrokeWidth = 2 * e.zoomScale
			e.renderArea.Add(l)
		}
		e.drawDashedLine(x, x+loopWidth, y, col)
	} else {
		l1 := canvas.NewLine(col)
		l1.Position1 = fyne.NewPos(x, y-loopHeight)
		l1.Position2 = fyne.NewPos(x+loopWidth, y-loopHeight)
		l1.StrokeWidth = 2 * e.zoomScale
		e.renderArea.Add(l1)

		l2 := canvas.NewLine(col)
		l2.Position1 = fyne.NewPos(x+loopWidth, y-loopHeight)
		l2.Position2 = fyne.NewPos(x+loopWidth, y)
		l2.StrokeWidth = 2 * e.zoomScale
		e.renderArea.Add(l2)

		l3 := canvas.NewLine(col)
		l3.Position1 = fyne.NewPos(x+loopWidth, y)
		l3.Position2 = fyne.NewPos(x, y)
		l3.StrokeWidth = 2 * e.zoomScale
		e.renderArea.Add(l3)
	}

	if v.ArrowHead {
		e.drawArrowHead(x+loopWidth, x, y, v.LineType)
	}

	text := v.Text
	if autoNumber {
		text = fmt.Sprintf("%d: %s", count, text)
	}
	txt := canvas.NewText(text, theme.ForegroundColor())
	txt.TextSize = 12 * e.zoomScale
	txt.Move(fyne.NewPos(x+loopWidth+5*e.zoomScale, y-loopHeight))
	txt.Resize(fyne.NewSize(200*e.zoomScale, 20*e.zoomScale))
	e.renderArea.Add(txt)
}

func min(a, b float32) float32 {
	if a < b {
		return a
	}
	return b
}

func abs(a float32) float32 {
	if a < 0 {
		return -a
	}
	return a
}

func (e *editorApp) drawParticipantBox(p Participant, x, y, w, h float32) {
	txt := canvas.NewText(p.Alias, theme.ForegroundColor())
	txt.Alignment = fyne.TextAlignCenter
	txt.TextSize = 14 * e.zoomScale
	txt.TextStyle = fyne.TextStyle{Bold: true}

	minSize := txt.MinSize()
	boxWidth := w
	if minSize.Width+30*e.zoomScale > boxWidth {
		boxWidth = minSize.Width + 30*e.zoomScale
	}

	rect := canvas.NewRectangle(theme.ButtonColor())
	rect.StrokeColor = theme.PrimaryColor()
	rect.StrokeWidth = 2 * e.zoomScale
	rect.Resize(fyne.NewSize(boxWidth, h))

	// Center the box on the lifeline (which is at x + w/2)
	boxX := x + w/2 - boxWidth/2
	rect.Move(fyne.NewPos(boxX, y))
	e.renderArea.Add(rect)

	txt.Move(fyne.NewPos(boxX, y+(h-minSize.Height)/2))
	txt.Resize(fyne.NewSize(boxWidth, minSize.Height))
	e.renderArea.Add(txt)

	// Click area for participant
	lineY := p.SourceLine
	ca := newClickArea(func() { e.highlightLine(lineY) }, func(pe *fyne.PointEvent) {
		e.showParticipantMenu(p, pe)
	})
	ca.Move(fyne.NewPos(boxX, y))
	ca.Resize(fyne.NewSize(boxWidth, h))
	e.renderArea.Add(ca)
}

func (e *editorApp) drawDashedLine(x1, x2, y float32, col color.Color) {
	step := float32(10) * e.zoomScale
	minX := x1
	maxX := x2
	if x1 > x2 {
		minX, maxX = x2, x1
	}
	for x := minX; x < maxX; x += step * 2 {
		endX := x + step
		if endX > maxX {
			endX = maxX
		}
		l := canvas.NewLine(col)
		l.Position1 = fyne.NewPos(x, y)
		l.Position2 = fyne.NewPos(endX, y)
		l.StrokeWidth = 2 * e.zoomScale
		e.renderArea.Add(l)
	}
}

func (e *editorApp) drawArrowHead(xFrom, xTo, y float32, lineType string) {
	arrowSize := float32(10) * e.zoomScale
	col := theme.PrimaryColor()

	direction := float32(1)
	if xTo < xFrom {
		direction = -1
	}

	a1 := canvas.NewLine(col)
	a1.Position1 = fyne.NewPos(xTo, y)
	a1.Position2 = fyne.NewPos(xTo - direction*arrowSize, y - arrowSize/2)
	a1.StrokeWidth = 2 * e.zoomScale
	e.renderArea.Add(a1)

	a2 := canvas.NewLine(col)
	a2.Position1 = fyne.NewPos(xTo, y)
	a2.Position2 = fyne.NewPos(xTo - direction*arrowSize, y + arrowSize/2)
	a2.StrokeWidth = 2 * e.zoomScale
	e.renderArea.Add(a2)
}

func (e *editorApp) getElementsParticipantsRange(elements []DiagramElement, sd SequenceDiagram) (minIdx, maxIdx int) {
	minIdx = -1
	maxIdx = -1
	updateRange := func(idx int) {
		if idx == -1 {
			return
		}
		if minIdx == -1 || idx < minIdx {
			minIdx = idx
		}
		if maxIdx == -1 || idx > maxIdx {
			maxIdx = idx
		}
	}

	var traverse func(elements []DiagramElement)
	traverse = func(elements []DiagramElement) {
		for _, el := range elements {
			switch v := el.(type) {
			case Message:
				updateRange(getParticipantIdx(sd, v.From))
				updateRange(getParticipantIdx(sd, v.To))
			case Note:
				updateRange(getParticipantIdx(sd, v.Actor1))
				if v.Actor2 != "" {
					updateRange(getParticipantIdx(sd, v.Actor2))
				}
			case *Block:
				for _, s := range v.Sections {
					traverse(s.Elements)
				}
			}
		}
	}

	traverse(elements)
	return minIdx, maxIdx
}

func (e *editorApp) getBlockParticipantsRange(b *Block, sd SequenceDiagram) (minIdx, maxIdx int) {
	minIdx = -1
	maxIdx = -1
	for _, s := range b.Sections {
		sMin, sMax := e.getElementsParticipantsRange(s.Elements, sd)
		if sMin != -1 {
			if minIdx == -1 || sMin < minIdx {
				minIdx = sMin
			}
			if maxIdx == -1 || sMax > maxIdx {
				maxIdx = sMax
			}
		}
	}
	return minIdx, maxIdx
}

func getParticipantIdx(sd SequenceDiagram, name string) int {
	for i, p := range sd.Participants {
		if p.Name == name || p.Alias == name {
			return i
		}
	}
	return -1
}

func (e *editorApp) newFile() {
	e.entry.SetText("sequenceDiagram\n    participant A\n    participant B\n    A->>B: Hello")
	e.currentFile = nil
	e.window.SetTitle("Mermaid Sequence Diagram Editor - New File")
	e.statusLabel.SetText("New File Created")
	e.updatePreview()
}

func sanitizePath(p string) string {
	p = strings.ReplaceAll(p, "₩", "\\")
	return filepath.Clean(p)
}

func (e *editorApp) showManualPathDialog(title, defaultName string, isSave bool, onConfirm func(string)) {
	pathEntry := widget.NewEntry()
	pathEntry.SetPlaceHolder("Enter absolute path here...")
	if defaultName != "" && isSave {
		if l := e.getExeDir(); l != nil {
			pathEntry.SetText(filepath.Join(l.Path(), defaultName))
		} else {
			pathEntry.SetText(defaultName)
		}
	}

	browseBtn := widget.NewButton("Browse", func() {
		if isSave {
			d := dialog.NewFileSave(func(w fyne.URIWriteCloser, err error) {
				if w != nil {
					pathEntry.SetText(w.URI().Path())
					w.Close()
				}
			}, e.window)
			if l := e.getExeDir(); l != nil {
				d.SetLocation(l)
			}
			d.SetFileName(defaultName)
			d.Show()
		} else {
			d := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
				if r != nil {
					pathEntry.SetText(r.URI().Path())
					r.Close()
				}
			}, e.window)
			if l := e.getExeDir(); l != nil {
				d.SetLocation(l)
			}
			d.Show()
		}
	})

	content := container.NewVBox(
		widget.NewLabel("Path:"),
		pathEntry,
		browseBtn,
	)

	d := dialog.NewCustomConfirm(title, "OK", "Cancel", content, func(b bool) {
		if b && pathEntry.Text != "" {
			onConfirm(pathEntry.Text)
		}
	}, e.window)
	d.Resize(fyne.NewSize(500, 200))
	d.Show()
}

func (e *editorApp) openFile() {
	e.showManualPathDialog("Open File", "", false, func(path string) {
		path = sanitizePath(path)
		data, err := os.ReadFile(path)
		if err != nil {
			dialog.ShowError(err, e.window)
			return
		}
		e.entry.SetText(string(data))
		e.currentFile = storage.NewFileURI(path)
		e.window.SetTitle("Mermaid Sequence Diagram Editor - " + filepath.Base(path))
		e.statusLabel.SetText("Opened: " + filepath.Base(path))
		e.updatePreview()
	})
}

func (e *editorApp) saveFile() {
	if e.currentFile == nil {
		e.saveAsFile()
		return
	}
	path := sanitizePath(e.currentFile.Path())
	err := os.WriteFile(path, []byte(e.entry.Text), 0644)
	if err != nil {
		dialog.ShowError(err, e.window)
		return
	}
	e.statusLabel.SetText("Saved: " + filepath.Base(path))
}

func (e *editorApp) saveAsFile() {
	e.showManualPathDialog("Save As", "diagram.md", true, func(path string) {
		path = sanitizePath(path)
		err := os.WriteFile(path, []byte(e.entry.Text), 0644)
		if err != nil {
			dialog.ShowError(err, e.window)
			return
		}
		e.currentFile = storage.NewFileURI(path)
		e.window.SetTitle("Mermaid Sequence Diagram Editor - " + filepath.Base(path))
		e.statusLabel.SetText("Saved As: " + filepath.Base(path))
	})
}

func (e *editorApp) exportPNG() {
	e.showManualPathDialog("Export PNG", "diagram.png", true, func(path string) {
		path = sanitizePath(path)
		
		outW := int(e.diagramSize.Width)
		outH := int(e.diagramSize.Height)
		if outW <= 0 || outH <= 0 {
			return
		}
		img := image.NewRGBA(image.Rect(0, 0, outW, outH))
		bgCol := color.White
		draw.Draw(img, img.Bounds(), &image.Uniform{bgCol}, image.Point{}, draw.Src)

		var fP *opentype.Font
		var fFs []*opentype.Font
		var objs []fyne.CanvasObject
		fyne.DoAndWait(func() {
			fP, fFs = getOpentypeFonts()
			objs = make([]fyne.CanvasObject, len(e.renderArea.Objects))
			copy(objs, e.renderArea.Objects)
		})

		for _, obj := range objs {
			switch v := obj.(type) {
			case *canvas.Line:
				var p1, p2 fyne.Position
				var col color.Color
				var sw float32
				fyne.DoAndWait(func() {
					p1, p2, col, sw = v.Position1, v.Position2, v.StrokeColor, v.StrokeWidth
				})
				x1, y1 := int(p1.X), int(p1.Y)
				x2, y2 := int(p2.X), int(p2.Y)
				th := int(sw)
				if th < 1 {
					th = 1
				}
				if isLight(col) {
					col = color.Black
				}
				drawLine(img, x1, y1, x2, y2, th, col)
			case *canvas.Rectangle:
				var pos fyne.Position
				var size fyne.Size
				var fc, sc color.Color
				var sw float32
				fyne.DoAndWait(func() {
					pos, size, fc, sc, sw = v.Position(), v.Size(), v.FillColor, v.StrokeColor, v.StrokeWidth
				})
				x, y := int(pos.X), int(pos.Y)
				w, h := int(size.Width), int(size.Height)
				th := int(sw)
				if fc != nil && fc != color.Transparent {
					fc = color.White
					rect := image.Rect(x, y, x+w, y+h)
					draw.Draw(img, rect, &image.Uniform{fc}, image.Point{}, draw.Src)
				}
				if sc != nil && sc != color.Transparent && th > 0 {
					if isLight(sc) {
						sc = color.Black
					}
					drawLine(img, x, y, x+w, y, th, sc)
					drawLine(img, x+w, y, x+w, y+h, th, sc)
					drawLine(img, x+w, y+h, x, y+h, th, sc)
					drawLine(img, x, y+h, x, y, th, sc)
				}
			case *canvas.Text:
				var pos fyne.Position
				var size fyne.Size
				var col color.Color
				var ts float32
				var align fyne.TextAlign
				var text string
				var style fyne.TextStyle
				fyne.DoAndWait(func() {
					pos, size, col, ts, align, text, style = v.Position(), v.Size(), v.Color, v.TextSize, v.Alignment, v.Text, v.TextStyle
				})
				x, y := float64(pos.X), float64(pos.Y)
				if col == nil || isLight(col) {
					col = color.Black
				}
				fs := float64(ts)
				baselineY := y + fs*0.8

				fPR, errPR := opentype.NewFace(fP, &opentype.FaceOptions{Size: fs, DPI: 72})
				if errPR != nil {
					logMsg(fmt.Sprintf("ERROR: Failed to create Latin font face: %v", errPR))
				}
				
				var fFRs []font.Face
				// Prioritize bold fallback if style is bold
				orderedFs := fFs
				if style.Bold && len(fFs) > 1 {
					orderedFs = []*opentype.Font{fFs[1], fFs[0]}
				}

				for i, fF := range orderedFs {
					face, err := opentype.NewFace(fF, &opentype.FaceOptions{Size: fs, DPI: 72})
					if err != nil {
						logMsg(fmt.Sprintf("ERROR: Failed to create Fallback font face %d: %v", i, err))
						continue
					}
					fFRs = append(fFRs, face)
				}

				if align == fyne.TextAlignCenter {
					tw := measureCompositeString(text, fPR, fFRs)
					boxW := float64(size.Width)
					x = x + (boxW-tw)/2
				}

				drawCompositeString(img, text, x, baselineY, fPR, fFRs, col)
			}
		}

		f, err := os.Create(path)
		if err != nil {
			fyne.DoAndWait(func() {
				dialog.ShowError(err, e.window)
			})
			return
		}
		defer f.Close()

		err = png.Encode(f, img)
		if err != nil {
			fyne.DoAndWait(func() {
				dialog.ShowError(err, e.window)
			})
		}
		fyne.DoAndWait(func() {
			e.statusLabel.SetText("Exported PNG: " + filepath.Base(path))
		})
	})
}

func (e *editorApp) insertSnippet(snippet string) {
	e.entry.SetText(e.entry.Text + "\n" + snippet)
	e.updatePreview()
}

func colorToRGB(c color.Color) (int, int, int) {
	r, g, b, _ := c.RGBA()
	return int(r >> 8), int(g >> 8), int(b >> 8)
}

func getOpentypeFonts() (*opentype.Font, []*opentype.Font) {
	fP, err := opentype.Parse(theme.DefaultTextFont().Content())
	if err != nil {
		logMsg(fmt.Sprintf("CRITICAL: Failed to parse primary font: %v", err))
	}

	var fallbacks []*opentype.Font
	f, err := opentype.Parse(resourceNotoSansCJKkrRegularOtf.Content())
	if err == nil {
		fallbacks = append(fallbacks, f)
	} else {
		logMsg(fmt.Sprintf("ERROR: Failed to parse bundled Regular font: %v", err))
	}

	fb, err := opentype.Parse(resourceNotoSansCJKkrBoldOtf.Content())
	if err == nil {
		fallbacks = append(fallbacks, fb)
	} else {
		logMsg(fmt.Sprintf("ERROR: Failed to parse bundled Bold font: %v", err))
	}

	return fP, fallbacks
}

func isKorean(r rune) bool {
	// Hangul Syllables, Jamo, and Compatibility Jamo
	return (r >= 0xAC00 && r <= 0xD7AF) || (r >= 0x1100 && r <= 0x11FF) || (r >= 0x3130 && r <= 0x318F)
}

func drawCompositeString(img draw.Image, txt string, x, y float64, fP font.Face, fallbacks []font.Face, col color.Color) {
	dP := &font.Drawer{Dst: img, Src: image.NewUniform(col), Face: fP}
	dP.Dot = fixed.Point26_6{X: fixed.Int26_6(x * 64), Y: fixed.Int26_6(y * 64)}
	
	drawers := make([]*font.Drawer, len(fallbacks))
	for i, f := range fallbacks {
		drawers[i] = &font.Drawer{Dst: img, Src: image.NewUniform(col), Face: f}
	}

	for _, r := range txt {
		isCJK := (r >= 0x2E80 && r <= 0x9FFF) || 
		         (r >= 0x3040 && r <= 0x30FF) || 
		         isKorean(r) || 
		         (r >= 0xFF00 && r <= 0xFFEF)

		drawn := false
		if isCJK {
			for _, df := range drawers {
				if _, ok := df.Face.GlyphAdvance(r); ok {
					df.Dot = dP.Dot
					df.DrawString(string(r))
					dP.Dot = df.Dot
					drawn = true
					break
				}
			}
		}
		
		if !drawn {
			dP.DrawString(string(r))
			for _, df := range drawers {
				df.Dot = dP.Dot
			}
		}
	}
}

func measureCompositeString(txt string, fP font.Face, fallbacks []font.Face) float64 {
	dP := &font.Drawer{Face: fP}
	var totalWidth fixed.Int26_6

	for _, r := range txt {
		isCJK := (r >= 0x2E80 && r <= 0x9FFF) || 
		         (r >= 0x3040 && r <= 0x30FF) || 
		         isKorean(r) || 
		         (r >= 0xFF00 && r <= 0xFFEF)

		measured := false
		if isCJK {
			for _, f := range fallbacks {
				if adv, ok := f.GlyphAdvance(r); ok {
					totalWidth += adv
					measured = true
					break
				}
			}
		}
		
		if !measured {
			totalWidth += dP.MeasureString(string(r))
		}
	}
	return float64(totalWidth) / 64.0
}

func drawLine(img draw.Image, x1, y1, x2, y2, thickness int, col color.Color) {
	dx := x2 - x1
	dy := y2 - y1
	if dx < 0 {
		dx = -dx
	}
	if dy < 0 {
		dy = -dy
	}
	sx := 1
	sy := 1
	if x1 >= x2 {
		sx = -1
	}
	if y1 >= y2 {
		sy = -1
	}
	err := dx - dy

	for {
		for tx := -thickness / 2; tx <= thickness/2; tx++ {
			for ty := -thickness / 2; ty <= thickness/2; ty++ {
				img.Set(x1+tx, y1+ty, col)
			}
		}
		if x1 == x2 && y1 == y2 {
			break
		}
		e2 := 2 * err
		if e2 > -dy {
			err -= dy
			x1 += sx
		}
		if e2 < dx {
			err += dx
			y1 += sy
		}
	}
}

func isLight(c color.Color) bool {
	if c == nil {
		return true
	}
	r, g, b, _ := c.RGBA()
	// Using perceived brightness formula
	luma := (0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)) / 65535.0
	return luma > 0.7
}

func isVeryLight(c color.Color) bool {
	if c == nil {
		return true
	}
	r, g, b, _ := c.RGBA()
	luma := (0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)) / 65535.0
	return luma > 0.9
}

type tooltipButton struct {
	widget.Button
	tip string
	app *editorApp
}

func (t *tooltipButton) Tooltip() string { return t.tip }
func (t *tooltipButton) MouseIn(e *desktop.MouseEvent) {
	if t.app.statusLabel != nil {
		t.app.statusLabel.SetText(t.tip)
	}
}
func (t *tooltipButton) MouseOut() {
	if t.app.statusLabel != nil {
		t.app.statusLabel.SetText("Ready")
	}
}

func newToolBtn(i fyne.Resource, tip string, e *editorApp, tap func()) *tooltipButton {
	b := &tooltipButton{tip: tip, app: e}
	b.Icon, b.OnTapped, b.Importance = i, tap, widget.LowImportance
	b.ExtendBaseWidget(b)
	return b
}

type cjkTheme struct {
	fyne.Theme
	font fyne.Resource
}

func (t *cjkTheme) Font(s fyne.TextStyle) fyne.Resource {
	return t.font
}

func (t *cjkTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	return t.Theme.Color(name, variant)
}

func (t *cjkTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return t.Theme.Icon(name)
}

func (t *cjkTheme) Size(name fyne.ThemeSizeName) float32 {
	return t.Theme.Size(name)
}

func main() {
	myApp := app.NewWithID("com.mermaid.sq.gui")
	
	// Load CJK font for the UI theme
	themeFont := resourceNotoSansCJKkrRegularOtf
	
	myApp.Settings().SetTheme(&cjkTheme{Theme: theme.DarkTheme(), font: themeFont})
	
	myWindow := myApp.NewWindow("Mermaid Sequence Diagram Editor")
	myWindow.SetIcon(resourceIconPng)

	e := &editorApp{
		window: myWindow,
		diagramSize: fyne.NewSize(1000, 1000),
		zoomScale: 1.0,
	}

	e.entry = widget.NewMultiLineEntry()
	e.entry.SetText("sequenceDiagram\n    autonumber\n    participant A as Alice\n    participant B as Bob\n    A->>B: Hello Bob, how are you?\n    alt is hungry\n        loop until full\n            A->>B: Get food\n        end\n    else is thirsty\n        A->>B: Get drink\n    end\n    Note right of B: Bob thinks\n    Note right of B: Chinese: 中文字符测试\n    Note right of B: Japanese: 日本語テスト\n    Note right of B: Korean: 한국어 테스트\n    B-->>A: Jolly good!")
	e.entry.OnChanged = func(s string) {
		e.updatePreview()
	}

	e.renderArea = container.NewWithoutLayout()
	e.scroll = container.NewScroll(container.New(&canvasLayout{app: e}, e.renderArea))
	
	e.stickyHeader = container.NewWithoutLayout()
	e.stickyContainer = container.NewWithoutLayout(e.stickyHeader)
	e.stickyContainer.Hide()

	e.scroll.OnScrolled = func(p fyne.Position) {
		e.updateStickyHeader()
	}

	e.statusLabel = widget.NewLabel("Ready")

	// Menu Bar
	fileMenu := fyne.NewMenu("File",
		fyne.NewMenuItem("New", e.newFile),
		fyne.NewMenuItem("Open", e.openFile),
		fyne.NewMenuItem("Save", e.saveFile),
		fyne.NewMenuItem("Save As...", e.saveAsFile),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Export PNG", e.exportPNG),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Zoom In", e.zoomIn),
		fyne.NewMenuItem("Zoom Out", e.zoomOut),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("UI Scaling Info", e.showScalingInfo),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Exit", func() { os.Exit(0) }),
	)
	insertMenu := fyne.NewMenu("Insert",
		fyne.NewMenuItem("Participant", func() { e.insertSnippet("    participant NewActor") }),
		fyne.NewMenuItem("Message", func() { e.insertSnippet("    Actor1->>Actor2: Message") }),
		fyne.NewMenuItem("Note", func() { e.insertSnippet("    Note over Actor1: Note text") }),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Loop", func() { e.insertSnippet("    loop condition\n        Actor->>Actor: Action\n    end") }),
		fyne.NewMenuItem("Alt", func() { e.insertSnippet("    alt condition\n        A->>B: OK\n    else fallback\n        A->>B: NO\n    end") }),
		fyne.NewMenuItem("Opt", func() { e.insertSnippet("    opt condition\n        A->>B: Optional\n    end") }),
		fyne.NewMenuItem("Self Request", func() { e.insertSnippet("    Actor->>Actor: Self Request") }),
	)
	viewMenu := fyne.NewMenu("View",
		fyne.NewMenuItem("Refresh Preview", func() { e.updatePreview() }),
	)
	mainMenu := fyne.NewMainMenu(fileMenu, insertMenu, viewMenu)
	myWindow.SetMainMenu(mainMenu)

	toolbar := container.NewHBox(
		newToolBtn(theme.DocumentCreateIcon(), "New", e, e.newFile),
		newToolBtn(theme.FileIcon(), "Open", e, e.openFile),
		newToolBtn(theme.DocumentSaveIcon(), "Save", e, e.saveFile),
		newToolBtn(theme.DownloadIcon(), "Export PNG", e, e.exportPNG),
		widget.NewSeparator(),
		newToolBtn(theme.ZoomInIcon(), "Zoom In", e, e.zoomIn),
		newToolBtn(theme.ZoomOutIcon(), "Zoom Out", e, e.zoomOut),
		widget.NewSeparator(),
		newToolBtn(theme.ContentAddIcon(), "Add Participant", e, func() {
			e.insertSnippet("    participant NewActor")
		}),
		newToolBtn(theme.MailForwardIcon(), "Add Message", e, func() {
			e.insertSnippet("    Actor1->>Actor2: Message")
		}),
		newToolBtn(theme.InfoIcon(), "Add Note", e, func() {
			e.insertSnippet("    Note over Actor1: Note text")
		}),
		newToolBtn(theme.MediaReplayIcon(), "Add Loop", e, func() {
			e.insertSnippet("    loop condition\n        Actor->>Actor: Action\n    end")
		}),
		newToolBtn(theme.MenuIcon(), "Add Alt", e, func() {
			e.insertSnippet("    alt condition\n        A->>B: OK\n    else fallback\n        A->>B: NO\n    end")
		}),
		newToolBtn(theme.HelpIcon(), "Add Opt", e, func() {
			e.insertSnippet("    opt condition\n        A->>B: Optional\n    end")
		}),
		newToolBtn(theme.MailReplyIcon(), "Add Self Request", e, func() {
			e.insertSnippet("    Actor->>Actor: Self Request")
		}),
		widget.NewSeparator(),
		newToolBtn(theme.ViewRefreshIcon(), "Refresh Preview", e, func() { e.updatePreview() }),
	)

	content := container.NewHSplit(
		e.entry,
		container.NewStack(e.scroll, e.stickyContainer),
	)
	content.Offset = 0.3

	mainLayout := container.NewBorder(toolbar, e.statusLabel, nil, nil, content)

	myWindow.SetContent(mainLayout)
	myWindow.Resize(fyne.NewSize(1200, 800))
	myWindow.SetMaster()

	e.updatePreview()

	// Defer scaling adjustment to ensure window is mapped and scale is detected
	go func() {
		time.Sleep(1000 * time.Millisecond)

		// Retrieve and log primary display resolution using driver API
		driver := myApp.Driver()
		if canvas := driver.CanvasForObject(nil); canvas != nil {
			logMsg(fmt.Sprintf("Primary Display Resolution: %v", canvas.Size()))
		}

		if os.Getenv("FYNE_SCALE") == "" {
			var osScale float64 = 1.0

			if runtime.GOOS == "linux" {
				// In WSLg/Linux, OS scale is often reported incorrectly.
				// Use xrandr to calculate real scale relative to 1920x1080 base.
				out, err := exec.Command("xrandr", "--query").Output()
				if err == nil {
					outputStr := string(out)
					var curW, curH float64 = 1920, 1080
					if idx := strings.Index(outputStr, "current "); idx != -1 {
						line := outputStr[idx:]
						fmt.Sscanf(line, "current %f x %f", &curW, &curH)
					}
					if curW > 0 {
						osScale = 1920.0 / curW
					}
				} else {
					// Fallback for Linux without xrandr (Wayland/ChromeOS)
					osScale = float64(myWindow.Canvas().Scale())
				}
			} else {
				// On Windows/macOS/Mobile, Fyne's built-in scale detection is reliable.
				osScale = float64(myWindow.Canvas().Scale())
			}

			// Apply user's preferred formula: 2.0 * OS_SCALE - 1.5
			calculatedScale := 2.0*osScale - 1.5
			if calculatedScale < 0.1 {
				calculatedScale = 0.1
			}

			e.detectedOSScale = osScale
			logMsg(fmt.Sprintf("Scaling - OS: %s, Detected OS Scale: %.2f", runtime.GOOS, osScale))

			// Fyne UI updates MUST happen on the main thread
			fyne.Do(func() {
				// Use 1.0 as the base zoom factor to match mermaid-md-gui's behavior
				e.zoomScale = 1.0
				if e.window != nil && e.window.Content() != nil {
					e.window.Content().Refresh()
					e.updatePreview()
				}
			})
		} else {
			// If FYNE_SCALE is provided, we still use 1.0 as our internal zoomScale
			// because FYNE_SCALE already scales the entire coordinate system.
			e.detectedOSScale = float64(myWindow.Canvas().Scale())
			e.zoomScale = 1.0
			logMsg(fmt.Sprintf("FYNE_SCALE detected: %s. Using default internal scale.", os.Getenv("FYNE_SCALE")))
		}
	}()

	myWindow.Show()
	myApp.Run()
}
