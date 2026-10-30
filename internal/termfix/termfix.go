// Package termfix stops the terminal from being asked for its background
// colour at start-up. Bubble Tea v1 does that in an init, and waits up to five
// seconds when the terminal does not answer. gg does not use the answer: its
// colours come from its own theme. Setting the value first makes lipgloss skip
// the query. Go runs package inits in import-path order once their imports are
// ready, and this path sorts before Bubble Tea's, so this init runs first.
package termfix

import "github.com/charmbracelet/lipgloss"

func init() {
	lipgloss.SetHasDarkBackground(true)
}
