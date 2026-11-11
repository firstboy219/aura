// Package shellwatch is the watcher of AURA's server commands (ADR-0065).
//
// Every command AURA wants to run in its shell sandbox is assessed here before
// it runs, by fixed rules (no model): "safe" runs at once, "review" waits for
// the owner's approval of exactly that command, "blocked" never runs. The
// verdict and its reasons are written to the shell log with the command, its
// output and the network it used. The rules are about effects outside the
// sandbox (sending, uploading, publishing, remote logins) and about disguised
// or destructive commands; inside the sandbox AURA may read, write, compute and
// install freely, as Hermes may in its container.
package shellwatch

import (
	"regexp"
	"strings"
)

// Levels of a verdict.
const (
	Safe    = "safe"
	Review  = "review"
	Blocked = "blocked"
)

// Verdict is the watcher's decision on one command.
type Verdict struct {
	Level   string   `json:"level"`
	Reasons []string `json:"reasons"`
}

type rule struct {
	level, reason string
	re            *regexp.Regexp
}

func r(level, reason, pattern string) rule {
	return rule{level, reason, regexp.MustCompile(pattern)}
}

// MaxCommand is the longest command the watcher accepts.
const MaxCommand = 8000

var rules = []rule{
	// Never: destroys the sandbox or hides what runs.
	r(Blocked, "fork bomb", `:\s*\(\s*\)\s*\{[^}]*:\s*\|\s*:`),
	r(Blocked, "wipes the file system root", `\brm\s+(-[a-zA-Z]*\s+)*(-[a-zA-Z]*[rR][a-zA-Z]*\s+)(-[a-zA-Z]*\s+)*(/|/\*|~|\$HOME)(\s|$)`),
	r(Blocked, "formats or overwrites a device", `\b(mkfs(\.\w+)?|wipefs|shred)\b|\bdd\b[^|;&]*\bof=/dev/`),
	r(Blocked, "stops the machine", `\b(shutdown|reboot|halt|poweroff|init\s+0|systemctl\s+(reboot|poweroff|halt))\b`),
	r(Blocked, "reads other processes' secrets", `/proc/(\d+|self|\*)/(environ|mem)\b`),
	r(Blocked, "container or host escape tooling", `\b(nsenter|docker|kubectl|chroot|mount|unshare|setcap|insmod|modprobe)\b`),
	// Ask the owner: effects outside the sandbox, or commands whose content is hidden.
	r(Review, "sends data to a remote server (upload/POST)", `\bcurl\b[^|;&]*(\s-(d|F|T)\b|\s--data(-\w+)?\b|\s--form\b|\s--upload-file\b|\s-X\s*(POST|PUT|PATCH|DELETE)\b|\s--request\s*(POST|PUT|PATCH|DELETE)\b)`),
	r(Review, "sends data to a remote server (upload/POST)", `\bwget\b[^|;&]*--(post-data|post-file|method=(POST|PUT|PATCH|DELETE)|body-data|body-file)`),
	r(Review, "remote login or file transfer", `\b(ssh|scp|sftp|rsync|ftp|telnet|nc|ncat|netcat|socat)\b`),
	r(Review, "publishes code or packages", `\bgit\s+push\b|\b(npm|yarn|pnpm)\s+publish\b|\btwine\s+upload\b|\bgh\s+(release|repo|pr)\b`),
	r(Review, "sends e-mail or messages", `\b(sendmail|mailx?|mutt|swaks)\b|smtp(s)?://|api\.telegram\.org|graph\.microsoft\.com|hooks\.slack\.com|api\.whatsapp`),
	r(Review, "runs code downloaded from the internet", `\b(curl|wget)\b[^;&]*\|\s*(sudo\s+)?(ba|z|da)?sh\b|\b(ba)?sh\s+<\(\s*(curl|wget)`),
	r(Review, "runs hidden (encoded) commands", `base64\s+(-d|--decode)[^|;&]*\|\s*(ba)?sh\b|\beval\b|\bpython3?\s+-c\s+['"][^'"]*(exec|eval)\(`),
	r(Review, "schedules commands", `\b(crontab|at\s+now|systemd-run)\b`),
	r(Review, "changes permissions recursively", `\bchmod\s+-R\b|\bchown\s+-R\b`),
}

// Notes describe what a command does without changing its verdict.
var notes = []rule{
	r(Safe, "installs packages", `\b(pip3?|python3?\s+-m\s+pip|npm|yarn|pnpm|apt(-get)?|gem|cargo|go)\s+(install|add|get)\b`),
	r(Safe, "reads from the internet", `\b(curl|wget|git\s+clone|git\s+pull|git\s+fetch)\b`),
	r(Safe, "deletes files in the sandbox", `\brm\s`),
	r(Safe, "runs a script", `\b(python3?|node|bash|sh)\s+\S+\.(py|js|sh)\b`),
}

// Assess decides a command. Unknown is not dangerous: anything no rule names is safe.
func Assess(command string) Verdict {
	c := strings.TrimSpace(command)
	if c == "" {
		return Verdict{Level: Blocked, Reasons: []string{"empty command"}}
	}
	if len(c) > MaxCommand {
		return Verdict{Level: Blocked, Reasons: []string{"command too long"}}
	}
	if strings.ContainsRune(c, 0) {
		return Verdict{Level: Blocked, Reasons: []string{"binary content in command"}}
	}
	v := Verdict{Level: Safe}
	seen := map[string]bool{}
	add := func(reason string) {
		if !seen[reason] {
			seen[reason] = true
			v.Reasons = append(v.Reasons, reason)
		}
	}
	for _, x := range rules {
		if x.re.MatchString(c) {
			add(x.reason)
			if x.level == Blocked || v.Level == Safe {
				v.Level = x.level
			}
		}
	}
	for _, x := range notes {
		if x.re.MatchString(c) {
			add(x.reason)
		}
	}
	return v
}

var secretLike = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----|\bAKIA[0-9A-Z]{16}\b|\bsk-[A-Za-z0-9_-]{20,}\b|\bgh[pousr]_[A-Za-z0-9]{30,}\b|\bxox[abp]-[A-Za-z0-9-]{10,}\b`)

// Redact hides credential-shaped strings in command output and reports whether it did.
func Redact(s string) (string, bool) {
	if !secretLike.MatchString(s) {
		return s, false
	}
	return secretLike.ReplaceAllString(s, "[REDACTED]"), true
}
