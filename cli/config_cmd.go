package cli

import (
	"fmt"
	"os"
)

// cmdConfig handles `weft config export|import`, distinct from `weft
// init-config` (which just writes a fresh default file, not a snapshot of a
// running agent's actual config).
func cmdConfig(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("config: missing subcommand (export|import)")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "export":
		return configExport(rest)
	case "import":
		return configImport(rest)
	default:
		return fmt.Errorf("config: unknown subcommand %q (export|import)", sub)
	}
}

func configExport(args []string) error {
	fs := remoteFlagSet("config export")
	out := fs.String("out", "", "write to this file instead of stdout")
	includeSecrets := fs.Bool("include-secrets", false, "include admin_api_key/webhook secrets/gemini api_key in plaintext")
	rf, err := parseRemote(fs, args)
	if err != nil {
		return err
	}
	path := "/config/export"
	if *includeSecrets {
		path += "?include_secrets=true"
	}
	c := newClient(rf)
	var raw []byte
	if err := c.getText(path, &raw); err != nil {
		return err
	}
	if *out == "" {
		fmt.Print(string(raw))
		return nil
	}
	if err := os.WriteFile(*out, raw, 0o644); err != nil {
		return err
	}
	fmt.Printf("config exported to %s\n", *out)
	return nil
}

func configImport(args []string) error {
	fs := remoteFlagSet("config import")
	rf, err := parseRemote(fs, args)
	if err != nil {
		return err
	}
	pos := fs.Args()
	if len(pos) == 0 {
		return fmt.Errorf("config import: <file> argument is required (a weft.yaml, e.g. from `config export`)")
	}
	body, err := os.ReadFile(pos[0])
	if err != nil {
		return err
	}
	c := newClient(rf)
	var out struct {
		Status string `json:"status"`
		Path   string `json:"path"`
		Note   string `json:"note"`
	}
	if err := c.postRaw("/config/import", "application/yaml", body, &out); err != nil {
		return err
	}
	fmt.Printf("config %s at %s\n", out.Status, out.Path)
	if out.Note != "" {
		fmt.Println(out.Note)
	}
	return nil
}
