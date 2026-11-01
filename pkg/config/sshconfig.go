package config

import (
	"bufio"
	"io"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"
)

// SSHConfigHosts returns the literal Host aliases of an ssh_config file, in
// order and once each. Patterns (* ? ! %) and names ssh could read as an
// option or split are skipped; Include is not followed.
func SSHConfigHosts(r io.Reader) []string {
	var hosts []string
	seen := map[string]bool{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		i := strings.IndexFunc(line, func(r rune) bool { return r == '=' || unicode.IsSpace(r) })
		if i < 0 || !strings.EqualFold(line[:i], "host") {
			continue
		}
		rest := strings.TrimPrefix(strings.TrimSpace(line[i:]), "=")
		for _, h := range strings.Fields(rest) {
			if strings.HasPrefix(h, "#") {
				break
			}
			if seen[h] || !literalHost(h) {
				continue
			}
			seen[h] = true
			hosts = append(hosts, h)
		}
	}
	return hosts
}

func literalHost(h string) bool {
	switch {
	case h == "", len(h) > 255, h[0] == '-', !utf8.ValidString(h), strings.ContainsAny(h, "*?!%\"'"):
		return false
	}
	return strings.IndexFunc(h, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}

// ReadSSHConfigHosts is SSHConfigHosts of the file at path; an unreadable
// file has no hosts.
func ReadSSHConfigHosts(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	return SSHConfigHosts(f)
}
