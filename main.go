package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	repoPath, err := initialRepositoryPath(os.Args[1:], os.Getwd)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	program := tea.NewProgram(newTUIModel(NewApp(), repoPath), tea.WithAltScreen())
	if _, err := program.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "nexus failed: %v\n", err)
		os.Exit(1)
	}
}
