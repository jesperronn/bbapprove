package app

import (
	"strings"
	"testing"

	"bbapprove/internal/style"
)

func TestHelpForWindows(t *testing.T) {
	old := helpGOOS
	t.Cleanup(func() { helpGOOS = old })

	helpGOOS = "windows"
	for _, topic := range []string{"config", "rules", "env", "doctor"} {
		text, ok := topicUsageFor(style.Theme{}, topic)
		if !ok {
			t.Fatalf("no topic %q", topic)
		}
		if strings.Contains(text, "~/.config") || strings.Contains(text, "XDG_CONFIG_HOME/") {
			t.Errorf("help %s still shows a Unix config path:\n%s", topic, text)
		}
	}
	cfg, _ := topicUsageFor(style.Theme{}, "config")
	for _, want := range []string{`$XDG_CONFIG_HOME\bbapprove\config.toml`, `(default %AppData%\bbapprove\config.toml)`} {
		if !strings.Contains(cfg, want) {
			t.Errorf("help config lacks %q:\n%s", want, cfg)
		}
	}
	rules, _ := topicUsageFor(style.Theme{}, "rules")
	if want := `$XDG_CONFIG_HOME\bbapprove\ignore (default %AppData%\bbapprove\ignore)`; !strings.Contains(rules, want) {
		t.Errorf("help rules lacks %q:\n%s", want, rules)
	}
	env, _ := topicUsageFor(style.Theme{}, "env")
	if want := "(default %AppData%)"; !strings.Contains(env, want) {
		t.Errorf("help env lacks %q", want)
	}
	if got := doctorUsageFor(style.Theme{}); !strings.Contains(got, `$XDG_CONFIG_HOME\bbapprove\config.toml`) {
		t.Errorf("doctor help keeps the Unix path:\n%s", got)
	}

	helpGOOS = "linux"
	cfg, _ = topicUsageFor(style.Theme{}, "config")
	if !strings.Contains(cfg, "(default ~/.config/bbapprove/config.toml)") {
		t.Errorf("help config lost the Unix path:\n%s", cfg)
	}
}
