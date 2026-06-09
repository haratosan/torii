package tui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// scannerWidth is the number of cells in the Knight-Rider bar. Six gives a
// recognisable sweep without eating much of the status line.
const scannerWidth = 6

// scannerInterval controls the sweep speed. ~80ms per step → ~12 fps,
// roughly the original KITT cadence and slow enough that a hung agent loop
// is visually obvious.
const scannerInterval = 80 * time.Millisecond

// scannerTickMsg is the wakeup that advances the scanner one cell. The
// model only schedules another tick while busy is true, so the animation
// halts cleanly on Done.
type scannerTickMsg time.Time

type scanner struct {
	pos int
	dir int // +1 or -1
}

func newScanner() scanner {
	return scanner{pos: 0, dir: 1}
}

func (s *scanner) step() {
	s.pos += s.dir
	if s.pos >= scannerWidth-1 {
		s.pos = scannerWidth - 1
		s.dir = -1
	} else if s.pos <= 0 {
		s.pos = 0
		s.dir = 1
	}
}

// scannerTickCmd schedules the next tick. We use tea.Tick so cancellation
// is implicit — when the model stops returning a tick cmd, the animation
// stops too.
func scannerTickCmd() tea.Cmd {
	return tea.Tick(scannerInterval, func(t time.Time) tea.Msg {
		return scannerTickMsg(t)
	})
}

// Color gradient: brightest at the head of the sweep, fading two cells
// behind it. Cells further away are dim. Picked to read well on both the
// default dark Terminal.app palette and the iTerm dark presets.
var (
	scannerBright = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF3030")).Bold(true)
	scannerMid    = lipgloss.NewStyle().Foreground(lipgloss.Color("#A02020"))
	scannerDim    = lipgloss.NewStyle().Foreground(lipgloss.Color("#502020"))
	scannerOff    = lipgloss.NewStyle().Foreground(lipgloss.Color("#202020"))
)

// render returns the six-cell bar. The head cell uses bright; the one and
// two cells "behind" the head fade. The cell layout uses ● for lit and ·
// for dark to keep the silhouette compact.
func (s *scanner) render() string {
	var b strings.Builder
	for i := 0; i < scannerWidth; i++ {
		dist := s.pos - i
		if s.dir < 0 {
			dist = -dist
		}
		switch {
		case dist == 0:
			b.WriteString(scannerBright.Render("●"))
		case dist == 1:
			b.WriteString(scannerMid.Render("●"))
		case dist == 2:
			b.WriteString(scannerDim.Render("●"))
		default:
			b.WriteString(scannerOff.Render("·"))
		}
	}
	return b.String()
}
