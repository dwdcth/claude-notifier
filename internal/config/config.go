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

	// insertedSectionHeadroom is the extra slice capacity reserved when
	// splicing a new [approver] section between existing config lines.
	insertedSectionHeadroom = 4
	// replacedSectionHeadroom is the extra slice capacity reserved when
	// rebuilding the line slice around a replaced [approver] section.
	replacedSectionHeadroom = 2
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
	err := os.MkdirAll(filepath.Dir(path), configDirPerms)
	if err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	existing, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("reading config: %w", err)
		}

		return writeFullConfig(path, cfg)
	}

	patched := patchApproverSection(string(existing), cfg.Approver)

	err = os.WriteFile(path, []byte(patched), configFilePerms)
	if err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	return nil
}

// approverSectionHeader matches the start of the [approver] table.
const approverSectionHeader = "[approver]"

// encodeApprover renders the [approver] section as TOML text. Returns an empty
// string when Approver is the zero value (uninstall scenario) so the caller
// can drop the section entirely.
func encodeApprover(approver Approver) string {
	if approver == (Approver{}) {
		return ""
	}
	var section strings.Builder
	section.WriteString(approverSectionHeader + "\n")
	if approver.Server != "" {
		fmt.Fprintf(&section, "server = %q\n", approver.Server)
	}
	if approver.Topic != "" {
		fmt.Fprintf(&section, "topic = %q\n", approver.Topic)
	}
	if approver.Timeout != 0 {
		fmt.Fprintf(&section, "timeout = %q\n", approver.Timeout.String())
	}
	if approver.Token != "" {
		fmt.Fprintf(&section, "token = %q\n", approver.Token)
	}
	if approver.Username != "" {
		fmt.Fprintf(&section, "username = %q\n", approver.Username)
	}
	if approver.Password != "" {
		fmt.Fprintf(&section, "password = %q\n", approver.Password)
	}
	if approver.TitlePrefix != "" {
		fmt.Fprintf(&section, "title_prefix = %q\n", approver.TitlePrefix)
	}

	return section.String()
}

// patchApproverSection rewrites the [approver] table inside the file content
// while preserving all other lines (global, notifiers, comments, blank lines).
func patchApproverSection(content string, approver Approver) string {
	lines := strings.Split(content, "\n")
	newSection := encodeApprover(approver)

	start := findSectionLine(lines, approverSectionHeader)
	if start == -1 {
		// No existing [approver] section.

		return insertApproverSection(lines, newSection)
	}

	return replaceApproverSection(lines, start, newSection)
}

// findSectionLine returns the index of the line holding the given table
// header, or -1 when no such line exists.
func findSectionLine(lines []string, header string) int {
	for i, ln := range lines {
		if strings.TrimSpace(ln) == header {
			return i
		}
	}

	return -1
}

// insertApproverSection splices a new [approver] section into the document,
// right after [global] (and any key=value lines that belong to it) or at the
// top of the file when no [global] table is present. An empty encoded section
// leaves the content unchanged.
func insertApproverSection(lines []string, newSection string) string {
	if newSection == "" {
		return strings.Join(lines, "\n")
	}
	insertAt := findInsertionPoint(lines)
	updated := make([]string, 0, len(lines)+insertedSectionHeadroom)
	updated = append(updated, lines[:insertAt]...)
	updated = append(updated, sectionLines(newSection)...)
	if insertAt >= len(lines) || strings.TrimSpace(lines[insertAt]) != "" {
		updated = append(updated, "")
	}
	updated = append(updated, lines[insertAt:]...)

	return strings.Join(updated, "\n")
}

// replaceApproverSection swaps the [approver] section starting at lines[start]
// for newSection, preserving every other line.
func replaceApproverSection(lines []string, start int, newSection string) string {
	// The section ends at the next line that begins a new table or
	// array-of-tables header, or at EOF. Consume any trailing blank lines so
	// we don't accumulate them across rewrites.
	end := findSectionEnd(lines, start)

	updated := make([]string, 0, len(lines)+replacedSectionHeadroom)
	updated = append(updated, lines[:start]...)
	if newSection != "" {
		updated = append(updated, sectionLines(newSection)...)
		// Keep exactly one blank separator before the next section. Walk
		// forward over the original trailing blanks so we don't double them.
		end = skipBlankLines(lines, end)
		if end < len(lines) {
			updated = append(updated, "")
		}
	} else {
		// Section removed: drop the blank lines that separated it from the
		// following section so we don't leave a gap.
		end = skipBlankLines(lines, end)
	}
	updated = append(updated, lines[end:]...)

	return strings.Join(updated, "\n")
}

// findSectionEnd returns the index of the first line after lines[start] that
// begins a new table or array-of-tables header, or len(lines) when the
// section runs to the end of the file.
func findSectionEnd(lines []string, start int) int {
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "[") {
			return i
		}
	}

	return len(lines)
}

// skipBlankLines advances idx past any blank (empty or whitespace-only) lines.
func skipBlankLines(lines []string, idx int) int {
	for idx < len(lines) && strings.TrimSpace(lines[idx]) == "" {
		idx++
	}

	return idx
}

// sectionLines splits an encoded section into its logical lines, trimming the
// trailing newline so Join("\n") yields clean output rather than doubling
// blank separators.
func sectionLines(section string) []string {
	return strings.Split(strings.TrimRight(section, "\n"), "\n")
}

// findInsertionPoint returns the line index at which a new [approver] section
// should be inserted: immediately after the [global] table (including its
// key=value rows), or 0 if no [global] table is present.
func findInsertionPoint(lines []string) int {
	globalStart := findSectionLine(lines, "[global]")
	if globalStart == -1 {
		return 0
	}
	for idx := globalStart + 1; idx < len(lines); idx++ {
		trimmed := strings.TrimSpace(lines[idx])
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			return idx
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
	var buf strings.Builder
	buf.WriteString("# claude-notifier configuration\n\n")

	buf.WriteString("[global]\n")
	if cfg.Global.Timeout > 0 {
		fmt.Fprintf(&buf, "timeout = %q\n", cfg.Global.Timeout.String())
	} else {
		fmt.Fprintf(&buf, "timeout = %q\n", defaultTimeout.String())
	}
	buf.WriteString("\n")

	if section := encodeApprover(cfg.Approver); section != "" {
		buf.WriteString(section)
		buf.WriteString("\n")
	}

	err := os.WriteFile(path, []byte(buf.String()), configFilePerms)
	if err != nil {
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
