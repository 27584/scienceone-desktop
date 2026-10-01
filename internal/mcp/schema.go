package mcp

// Version is reported in initialize and shown in the control panel.
const Version = "1.0.0"

// ToolList is the contract the model sees. The descriptions carry hard-won
// corrections: the caller otherwise assumes Linux, invents its own output
// directory, and tries to launch viewers from the shell.
func ToolList() []ToolSchema {
	return []ToolSchema{
		{
			Name: "list_dir",
			Description: "List files and directories at a path inside the workspace. Directory names end with \"/\". " +
				"Call this before reading or modifying anything in an unfamiliar area. " +
				"Set depth to 2 or 3 to map a whole project in one call instead of walking it level by level.",
			InputSchema: obj(map[string]any{
				"path": str("Path relative to the workspace root. Defaults to the root."),
				"depth": num("Levels to expand recursively. 1 = this directory only (default), 2 = include " +
					"subdirectories, up to 6."),
			}, nil),
		},
		{
			Name: "read_file",
			Description: "Read a UTF-8 text file and return it with 1-based line numbers. Always read a file before " +
				"editing it. For large files, pass start_line and end_line to read a slice.",
			InputSchema: obj(map[string]any{
				"path":       str("Path relative to the workspace root."),
				"start_line": num("First line to return (1-based)."),
				"end_line":   num("Last line to return (inclusive)."),
			}, []string{"path"}),
		},
		{
			Name: "write_file",
			Description: "Write a complete UTF-8 text file, creating parent directories as needed. This OVERWRITES " +
				"the whole file, so for an existing file prefer apply_patch. Use write_file only for new files or " +
				"full rewrites. The user's real Windows desktop is reachable as \"Desktop/\" inside the workspace — " +
				"when the user asks for something \"on my desktop\", write it to Desktop/<name>, not to an output/ " +
				"folder of your own choosing.",
			InputSchema: obj(map[string]any{
				"path":    str("Path relative to the workspace root."),
				"content": str("Full file content to write."),
			}, []string{"path", "content"}),
		},
		{
			Name: "apply_patch",
			Description: "Replace an exact snippet in an existing file. old_string must match the file byte-for-byte, " +
				"including indentation. If old_string occurs more than once, include more surrounding lines to " +
				"disambiguate, or set replace_all. This is the preferred way to edit existing code.",
			InputSchema: obj(map[string]any{
				"path":        str("Path relative to the workspace root."),
				"old_string":  str("Exact text to find."),
				"new_string":  str("Replacement text."),
				"replace_all": boolean("Replace every occurrence instead of requiring uniqueness."),
			}, []string{"path", "old_string", "new_string"}),
		},
		{
			Name: "find_files",
			Description: "Find files by path pattern (glob) under the workspace. Use this when you know roughly " +
				"what a file is called but not where it is. Supports ** for any depth, e.g. \"**/*.py\", " +
				"\"**/test_*.go\", \"src/**/*.ts\".",
			InputSchema: obj(map[string]any{
				"pattern":     str("Glob pattern. ** matches any number of directories."),
				"path":        str("Directory to search from, relative to the workspace root. Defaults to the root."),
				"max_results": num("Maximum paths to return (default 200)."),
			}, []string{"pattern"}),
		},
		{
			Name: "search_code",
			Description: "Search file contents by regular expression under the workspace. Returns \"path:line: text\" " +
				"matches. Use this to locate definitions and usages before editing. It searches contents only, not " +
				"file names — for that use find_files.",
			InputSchema: obj(map[string]any{
				"pattern":        str("Regular expression source."),
				"path":           str("Directory to search, relative to the workspace root."),
				"glob":           str("Filename filter, e.g. \"*.ts\" or \"*.py\"."),
				"max_results":    num("Maximum matches to return (default 80)."),
				"case_sensitive": boolean("Match case exactly. Defaults to false, which is right for finding a symbol you half-remember."),
			}, []string{"pattern"}),
		},
		{
			Name: "run_shell",
			Description: "Run a shell command in the workspace and return its combined output and exit code. This is " +
				"a POSIX shell (Git Bash on Windows), so `||`, `&&`, pipes, and `2>&1` all work. Use forward slashes " +
				"in paths (D:/Projects/foo), or relative paths, since backslash is an escape character. Use it to " +
				"build, run tests, and check syntax after every change. Commands that install dependencies or run a " +
				"full test suite need a larger timeout than the 180s default.",
			InputSchema: obj(map[string]any{
				"command": str("Command line to execute."),
				"cwd":     str("Working directory relative to the workspace root."),
				"timeout": num("Seconds to allow before terminating (default 180, maximum 900)."),
			}, []string{"command"}),
		},
		{
			Name: "git_status",
			Description: "Show what has changed in a git repository: current branch, staged and unstaged files, " +
				"untracked files. Read-only. Call this before and after a set of edits so you can report exactly " +
				"what you touched.",
			InputSchema: obj(map[string]any{
				"cwd": str("Repository directory relative to the workspace root. Defaults to the root."),
			}, nil),
		},
		{
			Name: "git_diff",
			Description: "Show the diff of a git repository, optionally limited to one file. Use it to review your own " +
				"change before reporting success. Read-only.",
			InputSchema: obj(map[string]any{
				"path":   str("Optional file to diff, relative to the workspace root."),
				"cwd":    str("Repository directory relative to the workspace root. Defaults to the root."),
				"staged": boolean("Show the staged diff (git diff --cached) instead of the working tree."),
			}, nil),
		},
		{
			Name: "replace_in_files",
			Description: "Replace a literal string across many files at once, returning which files changed. Use this " +
				"for mechanical renames; use apply_patch when the edit needs surrounding context or must be unique.",
			InputSchema: obj(map[string]any{
				"old_string":  str("Exact literal text to find. Not a regular expression."),
				"new_string":  str("Replacement text."),
				"path":        str("Directory to search from, relative to the workspace root."),
				"glob":        str("Filename filter, e.g. \"*.py\"."),
				"max_files":   num("Safety limit on how many files to rewrite (default 50)."),
			}, []string{"old_string", "new_string"}),
		},
		{
			Name: "open_path",
			Description: "Open a file or folder with the operating system default application: a browser for .html, " +
				"the associated editor for text, Explorer for a folder. This is the ONLY way to open something — " +
				"the machine is Windows and has no xdg-open, so do not try to launch a viewer from run_shell.",
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
