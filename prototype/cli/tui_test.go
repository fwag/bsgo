package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTUIView(t *testing.T) {
	m := initialModel()
	view := m.View()

	if !strings.Contains(view, "bsgo") {
		t.Errorf("expected view to contain 'bsgo', got %s", view)
	}
	if !strings.Contains(view, "Active & Pending Jobs") {
		t.Errorf("expected view to contain 'Active & Pending Jobs', got %s", view)
	}
	if !strings.Contains(view, "Worker Fleet Capacity") {
		t.Errorf("expected view to contain 'Worker Fleet Capacity', got %s", view)
	}

	// Test key events
	// Press down arrow
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(model)
	if m.cursor != 1 {
		t.Errorf("expected cursor to be 1 after DownKey, got %d", m.cursor)
	}

	// Press 'l' to toggle log drawer
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	m = updated.(model)
	if !m.viewLog {
		t.Errorf("expected viewLog to be true after pressing 'l'")
	}
	viewWithLog := m.View()
	if !strings.Contains(viewWithLog, "Live NATS Log Stream") {
		t.Errorf("expected view to show log drawer, got %s", viewWithLog)
	}

	// Press 'c' to cancel current job
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = updated.(model)
	if m.jobs[1].Status != "ABORTED" {
		t.Errorf("expected job[1] to be ABORTED after pressing 'c', got %s", m.jobs[1].Status)
	}
}
