package checks

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// jsonPath evaluates the subset of JSONPath a health endpoint needs:
// $.a.b, $.items[0].name, $["with space"]. It returns whether the path
// resolved; a syntax error is reported separately.
func jsonPath(doc any, path string) (any, bool, error) {
	path = strings.TrimSpace(path)
	if !strings.HasPrefix(path, "$") {
		return nil, false, errors.New("jsonpath must start with $")
	}
	cur := doc
	rest := path[1:]
	for rest != "" {
		switch rest[0] {
		case '.':
			rest = rest[1:]
			end := strings.IndexAny(rest, ".[")
			if end < 0 {
				end = len(rest)
			}
			key := rest[:end]
			if key == "" {
				return nil, false, errors.New("jsonpath: empty key")
			}
			rest = rest[end:]
			m, ok := cur.(map[string]any)
			if !ok {
				return nil, false, nil
			}
			if cur, ok = m[key]; !ok {
				return nil, false, nil
			}
		case '[':
			end := strings.IndexByte(rest, ']')
			if end < 0 {
				return nil, false, errors.New("jsonpath: missing ]")
			}
			inner := strings.TrimSpace(rest[1:end])
			rest = rest[end+1:]
			if len(inner) >= 2 && (inner[0] == '"' || inner[0] == '\'') && inner[len(inner)-1] == inner[0] {
				key := inner[1 : len(inner)-1]
				m, ok := cur.(map[string]any)
				if !ok {
					return nil, false, nil
				}
				if cur, ok = m[key]; !ok {
					return nil, false, nil
				}
				continue
			}
			idx, err := strconv.Atoi(inner)
			if err != nil {
				return nil, false, fmt.Errorf("jsonpath: bad index %q", inner)
			}
			list, ok := cur.([]any)
			if !ok {
				return nil, false, nil
			}
			if idx < 0 {
				idx += len(list)
			}
			if idx < 0 || idx >= len(list) {
				return nil, false, nil
			}
			cur = list[idx]
		default:
			return nil, false, fmt.Errorf("jsonpath: unexpected %q", rest[:1])
		}
	}
	return cur, true, nil
}
