package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/felipeelias/claude-notifier/internal/notifier"
)

const (
	defaultTimeout = 10 * time.Second

	configDirPerms  = 0750
	configFilePerms = 0600
)

// Global holds top-level configuration.
type Global struct {
	Timeout time.Duration `toml:"timeout"`
}

// Approver holds remote approval configuration.
type Approver struct {
	Server      string        `toml:"server"`
	Topic       string        `toml:"topic"`
	Timeout     time.Duration `toml:"timeout"`
	Token       string        `toml:"token"`
	Username    string        `toml:"username"`
	Password    string        `toml:"password"`
	TitlePrefix string        `toml:"title_prefix"`
}

// Config is the top-level configuration file structure.
type Config struct {
	Global    Global                      `toml:"global"`
	Approver  Approver                    `toml:"approver"`
	Notifiers map[string][]toml.Primitive `toml:"notifiers"`
	meta      toml.MetaData
}

// Load reads and parses a TOML config file.
func Load(path string) (*Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	defer func() { _ = file.Close() }()

	cfg := &Config{
		Global: Global{
			Timeout: defaultTimeout,
		},
	}

	meta, err := toml.NewDecoder(file).Decode(cfg)
	if err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	cfg.meta = meta

	return cfg, nil
}

// Decode unmarshals a plugin's TOML primitive into the given struct.
func (c *Config) Decode(p toml.Primitive, v any) error {
	return c.meta.PrimitiveDecode(p, v)
}

// Save writes the configuration to a TOML file, creating parent directories
// as needed.
//
// Because notifier plugins use toml.Primitive (which does not round-trip
// through BurntSushi/toml's encoder — see upstream issue #76), we cannot
// simply re-encode the whole Config. Instead, we patch the existing file in
// place: the [approver] section is rewritten from cfg.Approver while every
// other line ([global], all [[notifiers.*]], comments) is preserved verbatim.
// If the file does not yet exist, we emit a fresh config from cfg.Global and
// cfg.Approver (notifiers will be empty, matching the old behavior).
func Save(path string, cfg *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), configDirPerms); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	existing, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("reading config: %w", err)
		}
		return writeFullConfig(path, cfg)
	}

	patched, err := patchApproverSection(string(existing), cfg.Approver)
	if err != nil {
		return err
	}

	if err := os.WriteFile(path, []byte(patched), configFilePerms); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	return nil
}

// approverSectionHeader matches the start of the [approver] table.
const approverSectionHeader = "[approver]"

// encodeApprover renders the [approver] section as TOML text. Returns an empty
// string when Approver is the zero value (uninstall scenario) so the caller
// can drop the section entirely.
func encodeApprover(a Approver) string {
	if a == (Approver{}) {
		return ""
	}
	var b strings.Builder
	b.WriteString(approverSectionHeader + "\n")
	if a.Server != "" {
		b.WriteString(fmt.Sprintf("server = %q\n", a.Server))
	}
	if a.Topic != "" {
		b.WriteString(fmt.Sprintf("topic = %q\n", a.Topic))
	}
	if a.Timeout != 0 {
		b.WriteString(fmt.Sprintf("timeout = %q\n", a.Timeout.String()))
	}
	if a.Token != "" {
		b.WriteString(fmt.Sprintf("token = %q\n", a.Token))
	}
	if a.Username != "" {
		b.WriteString(fmt.Sprintf("username = %q\n", a.Username))
	}
	if a.Password != "" {
		b.WriteString(fmt.Sprintf("password = %q\n", a.Password))
	}
	if a.TitlePrefix != "" {
		b.WriteString(fmt.Sprintf("title_prefix = %q\n", a.TitlePrefix))
	}
	return b.String()
}

// patchApproverSection rewrites the [approver] table inside the file content
// while preserving all other lines (global, notifiers, comments, blank lines).
func patchApproverSection(content string, a Approver) (string, error) {
	lines := strings.Split(content, "\n")

	// Locate the [approver] section.
	start := -1
	for i, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if trimmed == approverSectionHeader {
			start = i
			break
		}
	}

	newSection := encodeApprover(a)

	if start == -1 {
		// No existing [approver] section. Drop it in (if non-empty) right
		// after [global] (and any key=value lines that belong to it),
		// otherwise at the top of the file.
		if newSection == "" {
			return content, nil
		}
		insertAt := findInsertionPoint(lines)
		updated := make([]string, 0, len(lines)+4)
		updated = append(updated, lines[:insertAt]...)
		sectionLines := strings.Split(strings.TrimRight(newSection, "\n"), "\n")
		updated = append(updated, sectionLines...)
		if insertAt >= len(lines) || strings.TrimSpace(lines[insertAt]) != "" {
			updated = append(updated, "")
		}
		updated = append(updated, lines[insertAt:]...)
		return strings.Join(updated, "\n"), nil
	}

	// Find the end of the [approver] section: the next line that begins a
	// new table or array-of-tables header, or EOF. Consume any trailing
	// blank lines so we don't accumulate them across rewrites.
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "[") {
			end = i
			break
		}
	}

	updated := make([]string, 0, len(lines)+2)
	updated = append(updated, lines[:start]...)
	if newSection != "" {
		// newSection ends with a trailing newline; trim it so each logical
		// line becomes its own slice element and Join("\n") produces clean
		// output rather than doubling blank separators.
		sectionLines := strings.Split(strings.TrimRight(newSection, "\n"), "\n")
		updated = append(updated, sectionLines...)
		// Keep exactly one blank separator before the next section. Walk
		// forward over the original trailing blanks so we don't double them.
		for end < len(lines) && strings.TrimSpace(lines[end]) == "" {
			end++
		}
		if end < len(lines) {
			updated = append(updated, "")
		}
	} else {
		// Section removed: drop the blank lines that separated it from the
		// following section so we don't leave a gap.
		for end < len(lines) && strings.TrimSpace(lines[end]) == "" {
			end++
		}
	}
	updated = append(updated, lines[end:]...)
	return strings.Join(updated, "\n"), nil
}

// findInsertionPoint returns the line index at which a new [approver] section
// should be inserted: immediately after the [global] table (including its
// key=value rows), or 0 if no [global] table is present.
func findInsertionPoint(lines []string) int {
	globalStart := -1
	for i, ln := range lines {
		if strings.TrimSpace(ln) == "[global]" {
			globalStart = i
			break
		}
	}
	if globalStart == -1 {
		return 0
	}
	for i := globalStart + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			return i
		}
	}
	// global was the last table in the file; insert at EOF.
	return len(lines)
}

// writeFullConfig is used when the config file does not exist yet. It emits a
// fresh TOML document containing the global and approver sections. Notifier
// configuration is not reconstructable from a Config (Primitive does not
// round-trip), so a brand-new file gets no [[notifiers.*]] entries.
func writeFullConfig(path string, cfg *Config) error {
	var b strings.Builder
	b.WriteString("# claude-notifier configuration\n\n")

	b.WriteString("[global]\n")
	if cfg.Global.Timeout > 0 {
		b.WriteString(fmt.Sprintf("timeout = %q\n", cfg.Global.Timeout.String()))
	} else {
		b.WriteString(fmt.Sprintf("timeout = %q\n", defaultTimeout.String()))
	}
	b.WriteString("\n")

	if a := encodeApprover(cfg.Approver); a != "" {
		b.WriteString(a)
		b.WriteString("\n")
	}

	if err := os.WriteFile(path, []byte(b.String()), configFilePerms); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	return nil
}

// DefaultPath returns the default config file path.
//
// Honors the XDG Base Directory Specification: it uses
// $XDG_CONFIG_HOME/claude-notifier/config.toml when XDG_CONFIG_HOME is set,
// otherwise falls back to ~/.config/claude-notifier/config.toml across all
// platforms.
func DefaultPath() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return xdg + "/claude-notifier/config.toml"
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".config/claude-notifier/config.toml"
	}
	return home + "/.config/claude-notifier/config.toml"
}

// Configurable is implemented by notifiers that provide sample config.
type Configurable interface {
	SampleConfig() string
}

// SampleConfig generates a sample config from all registered plugins.
func SampleConfig(reg *notifier.Registry) string {
	var buf strings.Builder
	buf.WriteString("# claude-notifier configuration\n\n")
	buf.WriteString("[global]\ntimeout = \"10s\"\n\n")

	all := reg.All()
	names := make([]string, 0, len(all))
	for name := range all {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		notif := all[name]()
		if conf, ok := notif.(Configurable); ok {
			buf.WriteString(conf.SampleConfig())
			buf.WriteByte('\n')
		} else {
			fmt.Fprintf(&buf, "# [[notifiers.%s]]\n\n", name)
		}
	}

	return buf.String()
}
