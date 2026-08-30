package cli

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
)

// The value conversions `vincent config` uses on both legs (task 060).
//
// They are here rather than in config.go so that file stays the key table a
// reader can diff against the configuration reference. internal/config is
// imported for ByteSize alone: "512MB" has to mean the same thing on the
// command line as it does in the file, and re-implementing the parser would
// be the second implementation that eventually disagrees.

func intField(read func(apiclient.Config) int, patch func(*int) apiclient.ConfigPatch) configField {
	return configField{
		read: func(c apiclient.Config) string { return strconv.Itoa(read(c)) },
		write: func(s string) (apiclient.ConfigPatch, error) {
			n, err := strconv.Atoi(strings.TrimSpace(s))
			if err != nil {
				return apiclient.ConfigPatch{}, fmt.Errorf("want a whole number, got %q", s)
			}
			return patch(&n), nil
		},
	}
}

func boolField(read func(apiclient.Config) bool, patch func(*bool) apiclient.ConfigPatch) configField {
	return configField{
		read: func(c apiclient.Config) string { return strconv.FormatBool(read(c)) },
		write: func(s string) (apiclient.ConfigPatch, error) {
			b, err := strconv.ParseBool(strings.TrimSpace(s))
			if err != nil {
				return apiclient.ConfigPatch{}, fmt.Errorf("want true or false, got %q", s)
			}
			return patch(&b), nil
		},
	}
}

// listField is the whitespace-separated form. An empty argument sets the empty
// list rather than leaving the key alone: `vincent config set notify.on ""` is
// how the hook is switched off, and treating it as a no-op would leave no way
// to do that from the command line.
func listField(read func(apiclient.Config) []string, patch func(*[]string) apiclient.ConfigPatch) configField {
	return configField{
		read: func(c apiclient.Config) string { return strings.Join(read(c), " ") },
		write: func(s string) (apiclient.ConfigPatch, error) {
			v := strings.Fields(s)
			if v == nil {
				v = []string{}
			}
			return patch(&v), nil
		},
	}
}

// byteSizeText renders a byte count the way the file spells it, so what `get`
// prints is what `set` accepts.
func byteSizeText(n int64) string { return config.ByteSize(n).String() }

func parseByteSizeArg(s string) (int64, error) {
	v, err := config.ParseByteSize(strings.TrimSpace(s))
	if err != nil {
		return 0, err
	}
	return v.Bytes(), nil
}

// floatText drops the exponent and the trailing zeros: a spend ceiling of 0
// reads "0", not "0.000000".
func floatText(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func parseFloatArg(s string) (float64, error) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, fmt.Errorf("want a number, got %q", s)
	}
	return f, nil
}

// parseInheritArg reads `environment.inherit`'s union: the two words, or a
// list of names with or without the brackets `get` prints.
func parseInheritArg(s string) apiclient.ConfigInherit {
	t := strings.TrimSpace(s)
	switch strings.ToLower(t) {
	case "all", "none":
		return apiclient.ConfigInherit{Mode: strings.ToLower(t)}
	}
	t = strings.TrimSuffix(strings.TrimPrefix(t, "["), "]")
	names := strings.FieldsFunc(t, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
	if names == nil {
		names = []string{}
	}
	return apiclient.ConfigInherit{Mode: "list", Names: names}
}

func pairsText(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, " ")
}

func parsePairsArg(s string) (map[string]string, error) {
	out := map[string]string{}
	for _, tok := range strings.Fields(s) {
		name, value, ok := strings.Cut(tok, "=")
		if !ok || name == "" {
			return nil, fmt.Errorf("want NAME=VALUE pairs, got %q", tok)
		}
		out[name] = value
	}
	return out, nil
}
