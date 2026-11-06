package sqlbase

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const maxSafeJSInt = 9007199254740991 // 2^53 - 1

// ColumnScanMeta precomputes column type flags once per result set to avoid
// per-cell string allocations and case conversions during row scanning.
type ColumnScanMeta struct {
	TypeName string
	IsBinary bool
	IsDate   bool
}

func BuildColumnScanMeta(typeNames []string) []ColumnScanMeta {
	out := make([]ColumnScanMeta, len(typeNames))
	for i, tn := range typeNames {
		out[i] = NewColumnScanMeta(tn)
	}
	return out
}

func NewColumnScanMeta(dbTypeName string) ColumnScanMeta {
	if dbTypeName == "" {
		return ColumnScanMeta{}
	}
	upper := strings.ToUpper(dbTypeName)
	isBin := strings.Contains(upper, "BLOB") ||
		strings.Contains(upper, "BINARY") ||
		strings.Contains(upper, "BYTEA") ||
		strings.Contains(upper, "RAW")
	return ColumnScanMeta{
		TypeName: dbTypeName,
		IsBinary: isBin,
		IsDate:   upper == "DATE",
	}
}

// NormalizeValue converts database driver scan values into JSON-safe values
// while preserving 64-bit integer precision and formatting binary/complex types cleanly.
func NormalizeValue(val any, dbTypeName string) any {
	return NormalizeValueFast(val, NewColumnScanMeta(dbTypeName))
}

// NormalizeValueFast normalizes a single cell value using precomputed ColumnScanMeta.
func NormalizeValueFast(val any, meta ColumnScanMeta) any {
	if val == nil {
		return nil
	}

	switch v := val.(type) {
	case bool:
		return v
	case int:
		if int64(v) > maxSafeJSInt || int64(v) < -maxSafeJSInt {
			return strconv.FormatInt(int64(v), 10)
		}
		return v
	case int8:
		return v
	case int16:
		return v
	case int32:
		return v
	case int64:
		if v > maxSafeJSInt || v < -maxSafeJSInt {
			return strconv.FormatInt(v, 10)
		}
		return v
	case uint:
		if uint64(v) > maxSafeJSInt {
			return strconv.FormatUint(uint64(v), 10)
		}
		return v
	case uint8:
		return v
	case uint16:
		return v
	case uint32:
		return v
	case uint64:
		if v > maxSafeJSInt {
			return strconv.FormatUint(v, 10)
		}
		return v
	case float32:
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return strconv.FormatFloat(float64(v), 'g', -1, 32)
		}
		return v
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return strconv.FormatFloat(v, 'g', -1, 64)
		}
		return v
	case string:
		return v
	case []byte:
		if !meta.IsBinary && utf8.Valid(v) {
			return string(v)
		}
		if len(v) == 0 {
			return ""
		}
		limit := len(v)
		suffix := ""
		if limit > 64 {
			limit = 64
			suffix = " ... (" + strconv.Itoa(len(v)) + " bytes)"
		}
		return "0x" + hex.EncodeToString(v[:limit]) + suffix
	case time.Time:
		if meta.IsDate && v.Hour() == 0 && v.Minute() == 0 && v.Second() == 0 && v.Nanosecond() == 0 {
			return v.Format("2006-01-02")
		}
		if v.Nanosecond() > 0 {
			return v.Format("2006-01-02 15:04:05.999999")
		}
		return v.Format("2006-01-02 15:04:05")
	case fmt.Stringer:
		return v.String()
	}

	rv := reflect.ValueOf(val)
	if rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return nil
		}
		return NormalizeValueFast(rv.Elem().Interface(), meta)
	}
	if rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array || rv.Kind() == reflect.Map || rv.Kind() == reflect.Struct {
		if b, err := json.Marshal(val); err == nil {
			return string(b)
		}
	}
	return fmt.Sprintf("%v", val)
}
