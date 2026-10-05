package tracker

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ── typed columns (memo: typed dynamic fields) ───────────────────────────────────────────────────────────────
//
// A column may declare a type. A declared type is enforced: a value is coerced when that is unambiguous ("$1,999"
// is the number 1999) and refused with a message that says what to send instead. Unknown stays unknown — a field
// the agent could not observe is left out, never invented as 0 or "". A column with no type stays free-form, as
// trackers always were.

const (
	TypeString  = "string"
	TypeNumber  = "number"
	TypeBoolean = "boolean"
	TypeTime    = "time"    // RFC 3339, or a plain date (2026-10-05) when the time of day is unknown
	TypeStrings = "strings" // a list of strings
)

func validType(t string) bool {
	switch t {
	case "", TypeString, TypeNumber, TypeBoolean, TypeTime, TypeStrings:
		return true
	}
	return false
}

var (
	thousands = regexp.MustCompile(`^-?\d{1,3}(,\d{3})+(\.\d+)?$`)
	currency  = strings.NewReplacer("$", "", "€", "", "£", "", "¥", "", "USD", "", "EUR", "", "GBP", "", " ", "", " ", "")
)

// coerce converts v to the column's declared type, or explains why it cannot.
func coerce(c Column, v any) (any, error) {
	if c.Type == "" || v == nil {
		return v, nil
	}
	bad := func(want string) error {
		return fmt.Errorf("column %q expects %s, got %s", c.Name, want, describe(v))
	}
	switch c.Type {
	case TypeString:
		switch x := v.(type) {
		case string:
			return x, nil
		case float64, bool, json.Number:
			return strVal(x), nil
		}
		return nil, bad("text")
	case TypeNumber:
		if f, ok := asFloat(v); ok {
			v = f // Go callers may hand over any numeric kind; JSON gives float64
		}
		switch x := v.(type) {
		case float64:
			if math.IsNaN(x) || math.IsInf(x, 0) {
				return nil, bad("a finite number")
			}
			return x, nil
		case json.Number:
			f, err := x.Float64()
			if err != nil {
				return nil, bad("a number")
			}
			return f, nil
		case string:
			t := currency.Replace(strings.TrimSpace(x))
			if thousands.MatchString(t) {
				t = strings.ReplaceAll(t, ",", "")
			}
			f, err := strconv.ParseFloat(t, 64)
			if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
				return nil, bad("a number (digits only, e.g. 1999 or 1999.50 — put the unit in the column, not the value)")
			}
			return f, nil
		}
		return nil, bad("a number")
	case TypeBoolean:
		switch x := v.(type) {
		case bool:
			return x, nil
		case string:
			switch strings.ToLower(strings.TrimSpace(x)) {
			case "true", "yes", "y", "1":
				return true, nil
			case "false", "no", "n", "0":
				return false, nil
			}
		}
		return nil, bad("true or false")
	case TypeTime:
		s, ok := v.(string)
		if !ok {
			return nil, bad("a date or RFC 3339 time (e.g. 2026-10-05 or 2026-10-05T14:00:00+03:00)")
		}
		s = strings.TrimSpace(s)
		if _, err := time.Parse(time.RFC3339, s); err == nil {
			return s, nil
		}
		if _, err := time.Parse("2006-01-02", s); err == nil {
			return s, nil
		}
		return nil, bad("a date or RFC 3339 time (e.g. 2026-10-05 or 2026-10-05T14:00:00+03:00)")
	case TypeStrings:
		switch x := v.(type) {
		case []any:
			out := make([]string, 0, len(x))
			for _, e := range x {
				s, ok := e.(string)
				if !ok {
					return nil, bad("a list of text items")
				}
				out = append(out, s)
			}
			return out, nil
		case []string:
			return x, nil
		case string:
			var out []string
			for _, p := range strings.Split(x, ",") {
				if p = strings.TrimSpace(p); p != "" {
					out = append(out, p)
				}
			}
			return out, nil
		}
		return nil, bad("a list of text items")
	}
	return v, nil
}

func asFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int32:
		return float64(x), true
	case int64:
		return float64(x), true
	case uint:
		return float64(x), true
	case uint32:
		return float64(x), true
	case uint64:
		return float64(x), true
	}
	return 0, false
}

func describe(v any) string {
	switch x := v.(type) {
	case string:
		return fmt.Sprintf("the text %q", x)
	case float64, json.Number:
		return fmt.Sprintf("the number %v", x)
	case bool:
		return fmt.Sprintf("the boolean %v", x)
	case []any:
		return "a list"
	case map[string]any:
		return "an object"
	}
	return fmt.Sprintf("%T", v)
}

// normalizeData coerces every submitted field that has a typed column, in place of the raw values, and reports
// every problem at once (an agent can fix them in one retry). required, when set, lists columns that must be present
// (a NEW row); fields without a declared column pass through unchanged.
func normalizeData(cols []Column, data map[string]any, requireAll bool) (map[string]any, error) {
	byName := map[string]Column{}
	for _, c := range cols {
		byName[strings.ToLower(c.Name)] = c
	}
	out := make(map[string]any, len(data))
	var problems []string
	for k, v := range data {
		c, ok := byName[strings.ToLower(k)]
		if !ok {
			out[k] = v
			continue
		}
		cv, err := coerce(c, v)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		out[c.Name] = cv // always under the declared name, so "price" and "Price" are one column
	}
	if requireAll {
		for _, c := range cols {
			if c.Required {
				if v, ok := out[c.Name]; !ok || v == nil || v == "" {
					problems = append(problems, fmt.Sprintf("column %q is required for a new row", c.Name))
				}
			}
		}
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("invalid row: %s", strings.Join(problems, "; "))
	}
	return out, nil
}
