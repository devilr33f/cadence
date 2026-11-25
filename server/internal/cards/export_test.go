package cards

// ToolNames are the built-in tools the card table decides
func ToolNames() []string {
	out := make([]string, 0, len(toolBuilders))
	for name := range toolBuilders {
		out = append(out, name)
	}
	return out
}
