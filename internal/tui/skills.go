package tui

// readSkillsDecline and writeSkillsDecline are the published-skill offer's
// memory (task 095), kept in tui.json beside the status-line offer's — a
// preference about a prompt, which is what that file is for. A read failure
// answers false, the way every other read of that file does: the cost of
// being wrong is one more offer on one more view.
func readSkillsDecline(dataDir string) bool {
	return readTUIState(dataDir).SkillsDeclined
}

func writeSkillsDecline(dataDir string) error {
	return mergeTUIState(dataDir, "skills_declined", true)
}
