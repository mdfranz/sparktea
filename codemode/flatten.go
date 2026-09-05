package codemode

import (
	"fmt"

	monty "github.com/ewhauser/gomonty"
)

// flattenValue unwraps a monty.Value into plain Go/JSON types. Value's own
// MarshalJSON always wraps scalars in a {"kind":...,"value":...} envelope
// and Raw() on a compound value returns still-wrapped elements (e.g.
// []monty.Value), not native Go types — so a naive json.Marshal(value.Raw())
// re-triggers that same envelope on every element. This recurses instead.
//
// gomonty's ValueKind constants (valueKindNone, valueKindList, ...) are
// unexported, so the switch below matches on the kind's underlying string
// form directly (Kind() returns a ValueKind, but it's just a defined string
// type, so it compares fine against an untyped string literal).
func flattenValue(v monty.Value) any {
	switch v.Kind() {
	case "none", "ellipsis", "bool", "int", "big_int", "float", "string", "bytes", "path":
		// Already a plain Go (or JSON-marshalable) type for these kinds.
		return v.Raw()
	case "list":
		items, ok := v.Raw().([]monty.Value)
		if !ok {
			return v.String()
		}
		return flattenItems(items)
	case "tuple":
		items, ok := v.Raw().(monty.Tuple)
		if !ok {
			return v.String()
		}
		return flattenItems(items)
	case "set":
		items, ok := v.Raw().(monty.Set)
		if !ok {
			return v.String()
		}
		return flattenItems(items)
	case "frozen_set":
		items, ok := v.Raw().(monty.FrozenSet)
		if !ok {
			return v.String()
		}
		return flattenItems(items)
	case "dict":
		return flattenDict(v)
	case "named_tuple":
		return flattenNamedTuple(v)
	default:
		// Everything else — class_instance (formerly dataclass), function,
		// exception, date/datetime/timedelta/timezone/time, not_implemented,
		// file_handle, a stat-result named tuple not shaped like one of the
		// cases above, repr/cycle placeholders, and any future Monty value
		// kind gomonty's wire format doesn't decode into a richer Go shape
		// yet: fall back to the interpreter's own repr rather than modeling
		// every payload shape for the MVP.
		return v.String()
	}
}

func flattenItems(items []monty.Value) []any {
	out := make([]any, len(items))
	for i, item := range items {
		out[i] = flattenValue(item)
	}
	return out
}

// flattenDict flattens a dict Value. Dict keys aren't guaranteed to be
// strings (Python allows int/tuple/etc. keys), so a dict with any
// non-string key falls back to a [[key, value], ...] array shape instead of
// a Go map.
func flattenDict(v monty.Value) any {
	pairs, ok := v.Raw().(monty.Dict)
	if !ok {
		return v.String()
	}
	for _, pair := range pairs {
		if pair.Key.Kind() != "string" {
			out := make([][2]any, len(pairs))
			for i, p := range pairs {
				out[i] = [2]any{flattenValue(p.Key), flattenValue(p.Value)}
			}
			return out
		}
	}
	out := make(map[string]any, len(pairs))
	for _, pair := range pairs {
		key, _ := pair.Key.Raw().(string)
		out[key] = flattenValue(pair.Value)
	}
	return out
}

// flattenNamedTuple flattens a named_tuple Value (e.g. os.stat_result) into
// a map keyed by field name, falling back to positional names if the wire
// payload is ever missing them.
func flattenNamedTuple(v monty.Value) any {
	nt, ok := v.Raw().(monty.NamedTuple)
	if !ok {
		return v.String()
	}
	out := make(map[string]any, len(nt.Values))
	for i, val := range nt.Values {
		name := fmt.Sprintf("_%d", i)
		if i < len(nt.FieldNames) {
			name = nt.FieldNames[i]
		}
		out[name] = flattenValue(val)
	}
	return out
}
