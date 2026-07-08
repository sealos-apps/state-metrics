package billing

import (
	"fmt"
	"maps"
	"math"
	"strconv"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func optionsFindProjection(projection bson.M) *options.FindOptions {
	return options.Find().SetProjection(projection)
}

func propertyInfo(enum string, properties map[string]PropertyInfo) PropertyInfo {
	if property, ok := properties[enum]; ok {
		return property
	}

	return PropertyInfo{
		Name: "resource_unknown_" + enum,
		Unit: "1",
	}
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	default:
		return ""
	}
}

func int64Value(value any) int64 {
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int8:
		return int64(typed)
	case int16:
		return int64(typed)
	case int32:
		return int64(typed)
	case int64:
		return typed
	case uint:
		return uint64ToInt64(uint64(typed))
	case uint8:
		return int64(typed)
	case uint16:
		return int64(typed)
	case uint32:
		return int64(typed)
	case uint64:
		return uint64ToInt64(typed)
	case float32:
		return int64(typed)
	case float64:
		return int64(typed)
	case primitive.Decimal128:
		i, err := strconv.ParseInt(typed.String(), 10, 64)
		if err == nil {
			return i
		}
	case string:
		i, err := strconv.ParseInt(typed, 10, 64)
		if err == nil {
			return i
		}
	}

	return 0
}

func uint64ToInt64(value uint64) int64 {
	if value > math.MaxInt64 {
		return 0
	}

	parsed, err := strconv.ParseInt(strconv.FormatUint(value, 10), 10, 64)
	if err != nil {
		return 0
	}

	return parsed
}

func arrayValue(value any) []any {
	switch typed := value.(type) {
	case []any:
		return typed
	case primitive.A:
		return []any(typed)
	default:
		return nil
	}
}

func usedMap(value any) map[string]int64 {
	result := make(map[string]int64)
	switch typed := value.(type) {
	case bson.M:
		for key, val := range typed {
			result[key] = int64Value(val)
		}
	case map[string]any:
		for key, val := range typed {
			result[key] = int64Value(val)
		}
	case map[string]int64:
		maps.Copy(result, typed)
	case map[uint8]int64:
		for key, val := range typed {
			result[strconv.Itoa(int(key))] = val
		}
	}

	return result
}
