package platform

import "testing"

func TestPathKeyNormalizesObsidianUnicodeAndConfiguredCase(t *testing.T) {
	composed, err := PathKey("darwin", CaseInsensitive, "Decisions/Café.md")
	if err != nil {
		t.Fatal(err)
	}
	decomposed, err := PathKey("darwin", CaseInsensitive, "decisions/Cafe\u0301.md")
	if err != nil {
		t.Fatal(err)
	}
	if composed != decomposed {
		t.Fatalf("keys differ: %q %q", composed, decomposed)
	}
	sensitive, err := PathKey("darwin", CaseSensitive, "decisions/CAFÉ.md")
	if err != nil {
		t.Fatal(err)
	}
	if sensitive == composed {
		t.Fatal("case-sensitive volume collapsed distinct names")
	}
}

func TestPathKeyUsesUnicodeCaseFoldAndCanonicalSeparators(t *testing.T) {
	first, err := PathKey("darwin", CaseInsensitive, `Straße\CAFÉ.md`)
	if err != nil {
		t.Fatal(err)
	}
	second, err := PathKey("darwin", CaseInsensitive, "STRASSE/Cafe\u0301.md")
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first != "strasse/café.md" {
		t.Fatalf("first=%q second=%q", first, second)
	}
	windowFolded, err := PathKey("windows", CaseSensitive, "Folder/FILE.md")
	if err != nil {
		t.Fatal(err)
	}
	if windowFolded != "folder/file.md" {
		t.Fatalf("window key=%q", windowFolded)
	}
}

func TestPathKeyRejectsUnsafeRelativePaths(t *testing.T) {
	tests := []string{
		"", ".", "..", "./file.md", "folder/../file.md", "folder//file.md",
		"/absolute.md", `\absolute.md`, `C:file.md`, `C:\file.md`, `C:/file.md`,
		`\\server\share\file.md`, `//server/share/file.md`, `\\?\C:\file.md`, `\\.\C:\file.md`,
		"folder/CON", "folder/con.md", "folder/NUL.txt", "folder/COM1", "folder/LPT9.log",
		"folder/trailing.", "folder/trailing ", "folder/bad\x00name.md",
		"folder/bad:name.md", "folder/bad<name>.md", "folder/bad|name.md", "folder/bad?name.md", "folder/bad*name.md",
	}
	for _, relative := range tests {
		t.Run(relative, func(t *testing.T) {
			if got, err := PathKey("darwin", CaseSensitive, relative); err == nil {
				t.Fatalf("PathKey(%q)=%q, want error", relative, got)
			}
		})
	}
}

func TestPathKeyRejectsUnknownCaseMode(t *testing.T) {
	if _, err := PathKey("darwin", CaseMode("unknown"), "folder/file.md"); err == nil {
		t.Fatal("expected invalid case mode error")
	}
}
