package cli

import "path/filepath"

type SessionOpenRequest struct {
	Command, Subcommand, ProjectID, Provider, SessionID string
	ExpectedGenerationID, ExpectedSessionViewDigest     string
	Executable, DataDir                                 string
}

func ParseSessionOpenContract(args []string) (SessionOpenRequest, error) {
	if len(args) == 0 {
		return SessionOpenRequest{}, contractError("sessions subcommand is required")
	}
	if args[0] == "launcher" {
		if len(args) < 2 || args[1] != "verify" {
			return SessionOpenRequest{}, contractError("sessions launcher subcommand is invalid")
		}
		flags, err := parseContractFlags(args[2:], map[string]bool{"provider": true, "executable": true, "data-dir": true, "json": true})
		if err != nil {
			return SessionOpenRequest{}, err
		}
		if err := requireFlags(flags, "provider", "executable"); err != nil {
			return SessionOpenRequest{}, err
		}
		if !sessionProvider(flags.values["provider"]) || !filepath.IsAbs(flags.values["executable"]) || filepath.Clean(flags.values["executable"]) != flags.values["executable"] {
			return SessionOpenRequest{}, contractError("launcher identity is invalid")
		}
		if err := validateInspectDataDir(flags.values["data-dir"]); err != nil {
			return SessionOpenRequest{}, err
		}
		return SessionOpenRequest{Command: "launcher", Subcommand: "verify", Provider: flags.values["provider"], Executable: flags.values["executable"], DataDir: flags.values["data-dir"]}, nil
	}
	if args[0] != "open" {
		return SessionOpenRequest{}, contractError("sessions subcommand is invalid")
	}
	flags, err := parseContractFlags(args[1:], map[string]bool{"project-id": true, "provider": true, "session-id": true, "expected-generation-id": true, "expected-session-view-digest": true, "data-dir": true, "json": true})
	if err != nil {
		return SessionOpenRequest{}, err
	}
	if err := requireFlags(flags, "project-id", "provider", "session-id", "expected-generation-id", "expected-session-view-digest"); err != nil {
		return SessionOpenRequest{}, err
	}
	if err := requireSafeIDs(flags, "project-id", "session-id", "expected-generation-id"); err != nil {
		return SessionOpenRequest{}, err
	}
	if !sessionProvider(flags.values["provider"]) {
		return SessionOpenRequest{}, contractError("Session provider is unsupported")
	}
	if err := requireDigest(flags.values["expected-session-view-digest"]); err != nil {
		return SessionOpenRequest{}, err
	}
	if err := validateInspectDataDir(flags.values["data-dir"]); err != nil {
		return SessionOpenRequest{}, err
	}
	return SessionOpenRequest{Command: "open", ProjectID: flags.values["project-id"], Provider: flags.values["provider"], SessionID: flags.values["session-id"], ExpectedGenerationID: flags.values["expected-generation-id"], ExpectedSessionViewDigest: flags.values["expected-session-view-digest"], DataDir: flags.values["data-dir"]}, nil
}

func sessionProvider(value string) bool {
	return value == "codex" || value == "claude" || value == "opencode"
}
