package mcp

// Version is reported in initialize and shown in the control panel.
const Version = "2.2.0"

// ToolList is the contract the model sees.
//
// Names and parameter shapes follow the mainstream coding-harness convention
// (read / write / edit / glob / grep / bash), because a model that has been
// trained on those reaches for them without being told twice. The descriptions
// carry the host-specific corrections that a generic harness does not need:
// this machine is Windows, has no xdg-open, and the user's desktop is reachable
// as Desktop/.
func ToolList() []ToolSchema {
	return []ToolSchema{
		{
			Name: "ls",
			Description: "List the entries of a directory. Directory names end with \"/\". " +
				"Pass depth 2 or 3 to expand subdirectories and map a whole project in one call " +
				"instead of walking it level by level.",
			InputSchema: obj(map[string]any{
				"path":  str("Directory relative to the workspace root. Defaults to the root."),
				"depth": num("Levels to expand. 1 = this directory only (default), up to 6."),
			}, nil),
		},
		{
			Name: "read",
			Description: "Read a UTF-8 text file and return it with 1-based line numbers. " +
				"Always read a file before editing it. For a large file, pass offset and limit " +
				"to read a window of it.",
			InputSchema: obj(map[string]any{
				"path":   str("File path relative to the workspace root."),
				"offset": num("First line to return, 1-based. Defaults to 1."),
				"limit":  num("How many lines to return. Defaults to the rest of the file."),
			}, []string{"path"}),
		},
		{
			Name: "readLines",
			Description: "Read a UTF-8 text file as a structured array of numbered lines. " +
				"Use this when you need exact line numbers to feed into edit, rather than a " +
				"formatted block of text.",
			InputSchema: obj(map[string]any{
				"path":   str("File path relative to the workspace root."),
				"offset": num("First line to return, 1-based. Defaults to 1."),
				"limit":  num("How many lines to return. Defaults to the rest of the file."),
			}, []string{"path"}),
		},
		{
			Name: "write",
			Description: "Create or completely overwrite a UTF-8 text file. Parent directories are " +
				"created as needed. This REPLACES the whole file, so for an existing file prefer edit. " +
				"The user's real Windows desktop is reachable as \"Desktop/\" inside the workspace — " +
				"when the user asks for something \"on my desktop\", write it to Desktop/<name>, not to " +
				"an output/ folder of your own choosing.",
			InputSchema: obj(map[string]any{
				"path":    str("File path relative to the workspace root."),
				"content": str("Full file content to write."),
			}, []string{"path", "content"}),
		},
		{
			Name: "edit",
			Description: "Replace an exact string in an existing file. oldString must match the file " +
				"byte-for-byte, including indentation. By default oldString must be unique; if it " +
				"appears more than once either include more surrounding lines or set replaceAll. " +
				"This is the preferred way to change existing code.",
			InputSchema: obj(map[string]any{
				"path":       str("File path relative to the workspace root."),
				"oldString":  str("Exact text to find."),
				"newString":  str("Replacement text."),
				"replaceAll": boolean("Replace every occurrence instead of requiring a unique match."),
			}, []string{"path", "oldString", "newString"}),
		},
		{
			Name: "glob",
			Description: "Find file paths by glob pattern. Use this when you know roughly what a file " +
				"is called but not where it is. \"**\" spans any number of directories, e.g. \"**/*.py\", " +
				"\"src/**/*.ts\". Returns paths only, not contents.",
			InputSchema: obj(map[string]any{
				"pattern": str("Glob pattern. ** matches any number of directories."),
				"path":    str("Directory to search from, relative to the workspace root. Defaults to the root."),
				"maxResults": num("Maximum paths to return (default 200)."),
			}, []string{"pattern"}),
		},
		{
			Name: "grep",
			Description: "Search file contents with a regular expression and return matching lines with " +
				"their line numbers, grouped by file. Use it to locate definitions and usages before " +
				"editing. Searches contents only — for file names use glob.",
			InputSchema: obj(map[string]any{
				"pattern":       str("Regular expression source."),
				"path":          str("Directory to search, relative to the workspace root."),
				"glob":          str("Filename filter, e.g. \"*.ts\" or \"*.py\"."),
				"maxResults":    num("Maximum matching lines to return (default 80)."),
				"caseSensitive": boolean("Match case exactly. Defaults to false."),
			}, []string{"pattern"}),
		},
		{
			Name: "todoWrite",
			Description: "Record or update the structured task list for the current work. Send the " +
				"complete list every time; it replaces the previous one (there is no partial update). " +
				"Use it to plan multi-step work and keep the user informed of progress. Keep exactly " +
				"one item in_progress at a time.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"todos": map[string]any{
						"type":        "array",
						"description": "The complete task list.",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"content": str("Short imperative description of the task."),
								"status": map[string]any{
									"type":        "string",
									"enum":        []string{"pending", "in_progress", "completed"},
									"description": "pending | in_progress | completed",
								},
							},
							"required": []string{"content", "status"},
						},
					},
				},
				"required": []string{"todos"},
			},
		},
		{
			Name: "bash",
			Description: "Run a POSIX shell command and return its combined output and exit code. This is " +
				"Git Bash on Windows, so `||`, `&&`, pipes, `2>&1` and heredocs all work. Use forward " +
				"slashes in paths (D:/Projects/foo) since backslash is an escape character. A non-zero " +
				"exit is reported as [exit code: N] in the output and is not treated as a tool failure. " +
				"Commands that install dependencies or run a full test suite need a larger timeout.",
			InputSchema: obj(map[string]any{
				"command": str("Command line to execute."),
				"cwd":     str("Working directory relative to the workspace root."),
				"timeout": num("Seconds to allow before terminating (default 180, maximum 900)."),
			}, []string{"command"}),
		},
		{
			Name: "pwsh",
			Description: "Run a PowerShell command (powershell -NoProfile -Command) and return its output. " +
				"Use it only for things that are awkward in bash: Windows APIs, registry, services, " +
				"WMI, or .NET types. For everyday file and build work prefer bash. A non-zero exit is " +
				"reported as [exit code: N] and is not treated as a tool failure.",
			InputSchema: obj(map[string]any{
				"command": str("PowerShell command line to execute."),
				"cwd":     str("Working directory relative to the workspace root."),
				"timeout": num("Seconds to allow before terminating (default 180, maximum 900)."),
			}, []string{"command"}),
		},
		{
			Name: "open",
			Description: "Open a file or folder with the operating system's default application: a browser " +
				"for .html, the associated editor for text, Explorer for a folder. This is the ONLY way to " +
				"open something — the machine is Windows and has no xdg-open, so do not try to launch a " +
				"viewer from bash.",
			InputSchema: obj(map[string]any{
				"path": str("File or folder to open, relative to the workspace root."),
			}, []string{"path"}),
		},
		{
			Name: "ping",
			Description: "Connectivity check. Returns the workspace root, platform, and whether the desktop is linked.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		},
	}
}

func obj(props map[string]any, required []string) map[string]any {
	schema := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func str(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func num(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func boolean(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}
