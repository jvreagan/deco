package decoclient

import "fmt"

// ToInt converts an interface{} to int, returning 0 for unsupported types.
func ToInt(v interface{}) int {
	switch val := v.(type) {
	case int:
		return val
	case int64:
		return int(val)
	case float64:
		return int(val)
	default:
		return 0
	}
}

// ToFloat converts an interface{} to float64, returning 0 for unsupported types.
func ToFloat(v interface{}) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case int:
		return float64(val)
	case int64:
		return float64(val)
	default:
		return 0
	}
}

// ToString converts an interface{} to string.
func ToString(v interface{}) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// ToBool converts an interface{} to bool, returning false for non-bool types.
func ToBool(v interface{}) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

// GetMap extracts a nested map from data, returning an empty map if not found.
func GetMap(data map[string]interface{}, key string) map[string]interface{} {
	if v, ok := data[key].(map[string]interface{}); ok {
		return v
	}
	return map[string]interface{}{}
}
