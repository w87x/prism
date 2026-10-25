package memory

import (
	"encoding/json"
	"regexp"
	"strconv"
)

// IDs is a list of fact ids as a model writes them. Models answer {"evidence":[12,"34","#56","fact 78"]} as readily as [12,34,56,78]; a strict
// []int64 turned every such answer into "unparsable model output after 3 tries" and the whole analysis step failed (cross-bank analysis and
// level-2 synthesis both did, in one install). Numbers, numeric strings and strings that contain a number are all read; anything else is skipped.
type IDs []int64

var firstNumber = regexp.MustCompile(`\d+`)

func (p *IDs) UnmarshalJSON(b []byte) error {
	var raw any
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	var out []int64
	var add func(v any)
	add = func(v any) {
		switch x := v.(type) {
		case float64:
			out = append(out, int64(x))
		case string:
			if m := firstNumber.FindString(x); m != "" {
				if n, err := strconv.ParseInt(m, 10, 64); err == nil {
					out = append(out, n)
				}
			}
		case []any:
			for _, e := range x {
				add(e)
			}
		}
	}
	add(raw)
	*p = out
	return nil
}
