package connector

import (
	"strings"

	"maunium.net/go/mautrix/bridgev2/commands"
)

// helpHandler replaces the built-in help command with one that has more spacing: every
// command is its own list item, separated by an empty line, so that long descriptions are
// easier to tell apart.
func (fc *FChatConnector) helpHandler() commands.CommandHandler {
	return &commands.FullHandler{
		Func: func(ce *commands.Event) {
			ce.Reply(spaceHelp(commands.FormatHelp(ce)))
		},
		Name:    commands.CommandHelp.Name,
		Aliases: commands.CommandHelp.Aliases,
		Help:    commands.CommandHelp.Help,
	}
}

// spaceHelp turns the help text of mautrix (a header line per section and a line per
// command) into sections with loose lists, which clients show with space between the items.
func spaceHelp(text string) string {
	var out strings.Builder
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, " ")
		switch {
		case line == "":
		case strings.HasPrefix(line, "#### "):
			out.WriteString("\n### " + strings.TrimPrefix(line, "#### ") + "\n\n")
		case strings.HasPrefix(line, "**"):
			// "**command** args - description"
			out.WriteString("* " + strings.Replace(line, " - ", " – ", 1) + "\n\n")
		default:
			out.WriteString(line + "\n\n")
		}
	}
	return strings.TrimSpace(out.String())
}
