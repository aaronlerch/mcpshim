package config

import (
	"os"
	"strings"
)

// expandEnv expands $VAR, ${VAR}, and ${VAR:-default} sequences in s.
// $$ is replaced with a literal $. Unset variables resolve to empty
// (or to the default in the :-default form). This matches the subset
// of shell expansion that Claude Code uses for MCP configs.
func expandEnv(s string) string {
	out, _ := expandEnvStrict(s)
	return out
}

// expandEnvStrict expands like expandEnv and also reports the first variable
// that was referenced without a default and is unset. Callers that want a
// missing variable to be an error (HTTP service validation) use the second
// result; MCP servers keep the unset-means-empty behavior.
func expandEnvStrict(s string) (string, string) {
	missing := ""
	lookup := func(name string) string {
		v, ok := os.LookupEnv(name)
		if !ok && missing == "" {
			missing = name
		}
		return v
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '$' {
			b.WriteByte(s[i])
			continue
		}
		if i+1 >= len(s) {
			b.WriteByte('$')
			continue
		}
		next := s[i+1]
		if next == '$' {
			b.WriteByte('$')
			i++
			continue
		}
		if next == '{' {
			rel := strings.IndexByte(s[i+2:], '}')
			if rel < 0 {
				b.WriteByte('$')
				continue
			}
			spec := s[i+2 : i+2+rel]
			if idx := strings.Index(spec, ":-"); idx >= 0 {
				name := spec[:idx]
				fallback := spec[idx+2:]
				if val := os.Getenv(name); val != "" {
					b.WriteString(val)
				} else {
					b.WriteString(fallback)
				}
			} else {
				b.WriteString(lookup(spec))
			}
			i = i + 2 + rel
			continue
		}
		if !isVarStart(next) {
			b.WriteByte('$')
			continue
		}
		j := i + 2
		for j < len(s) && isVarRune(s[j]) {
			j++
		}
		b.WriteString(lookup(s[i+1 : j]))
		i = j - 1
	}
	return b.String(), missing
}

func isVarStart(c byte) bool {
	return c == '_' ||
		(c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z')
}

func isVarRune(c byte) bool {
	return isVarStart(c) || (c >= '0' && c <= '9')
}
