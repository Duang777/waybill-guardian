package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const maxJSONSafeInteger int64 = 9_007_199_254_740_991

type canonicalMember struct {
	name  string
	value canonicalValue
}

type canonicalValue struct {
	kind    byte
	scalar  string
	members []canonicalMember
	items   []canonicalValue
}

func CanonicalJSON(value any) ([]byte, error) {
	if err := validateTypedStrings(reflect.ValueOf(value), make(map[typedVisit]bool)); err != nil {
		return nil, err
	}
	var raw bytes.Buffer
	encoder := json.NewEncoder(&raw)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, fmt.Errorf("encode JSON: %w", err)
	}
	return CanonicalizeJSON(bytes.TrimSuffix(raw.Bytes(), []byte{'\n'}))
}

type typedVisit struct {
	kind    reflect.Kind
	pointer uintptr
}

func validateTypedStrings(value reflect.Value, seen map[typedVisit]bool) error {
	if !value.IsValid() {
		return nil
	}
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return nil
		}
		return validateTypedStrings(value.Elem(), seen)
	case reflect.String:
		if !utf8.ValidString(value.String()) {
			return errors.New("typed JSON value contains invalid UTF-8")
		}
		return nil
	case reflect.Pointer, reflect.Map, reflect.Slice:
		if value.IsNil() {
			return nil
		}
		visit := typedVisit{kind: value.Kind(), pointer: value.Pointer()}
		if seen[visit] {
			return nil
		}
		seen[visit] = true
		defer delete(seen, visit)
	}
	if value.CanInterface() {
		if _, ok := value.Interface().(json.Marshaler); ok {
			return nil
		}
	}
	switch value.Kind() {
	case reflect.Pointer:
		return validateTypedStrings(value.Elem(), seen)
	case reflect.Map:
		iterator := value.MapRange()
		for iterator.Next() {
			if err := validateTypedStrings(iterator.Key(), seen); err != nil {
				return err
			}
			if err := validateTypedStrings(iterator.Value(), seen); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		for index := 0; index < value.Len(); index++ {
			if err := validateTypedStrings(value.Index(index), seen); err != nil {
				return err
			}
		}
	case reflect.Struct:
		valueType := value.Type()
		for index := 0; index < value.NumField(); index++ {
			if valueType.Field(index).PkgPath != "" {
				continue
			}
			if err := validateTypedStrings(value.Field(index), seen); err != nil {
				return err
			}
		}
	}
	return nil
}

func CanonicalizeJSON(raw []byte) ([]byte, error) {
	if len(raw) == 0 || !utf8.Valid(raw) {
		return nil, errors.New("canonical JSON input must be non-empty UTF-8")
	}
	if err := validateJSONStringSurrogates(raw); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := parseCanonicalValue(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("canonical JSON input has a trailing value")
		}
		return nil, fmt.Errorf("read trailing JSON: %w", err)
	}

	var result bytes.Buffer
	value.appendTo(&result)
	return result.Bytes(), nil
}

func validateJSONStringSurrogates(raw []byte) error {
	for index := 0; index < len(raw); index++ {
		if raw[index] != '"' {
			continue
		}
		index++
		for index < len(raw) && raw[index] != '"' {
			if raw[index] != '\\' {
				index++
				continue
			}
			index++
			if index >= len(raw) {
				return errors.New("JSON string ends after an escape")
			}
			if raw[index] != 'u' {
				index++
				continue
			}
			value, ok := decodeHexQuad(raw, index+1)
			if !ok {
				index += 5
				continue
			}
			index += 5
			switch {
			case value >= 0xd800 && value <= 0xdbff:
				if index+5 >= len(raw) || raw[index] != '\\' || raw[index+1] != 'u' {
					return errors.New("JSON string contains an unpaired high surrogate")
				}
				low, lowOK := decodeHexQuad(raw, index+2)
				if !lowOK || low < 0xdc00 || low > 0xdfff {
					return errors.New("JSON string contains an unpaired high surrogate")
				}
				index += 6
			case value >= 0xdc00 && value <= 0xdfff:
				return errors.New("JSON string contains an unpaired low surrogate")
			}
		}
	}
	return nil
}

func decodeHexQuad(raw []byte, start int) (uint16, bool) {
	if start+4 > len(raw) {
		return 0, false
	}
	var result uint16
	for _, current := range raw[start : start+4] {
		result <<= 4
		switch {
		case current >= '0' && current <= '9':
			result += uint16(current - '0')
		case current >= 'a' && current <= 'f':
			result += uint16(current-'a') + 10
		case current >= 'A' && current <= 'F':
			result += uint16(current-'A') + 10
		default:
			return 0, false
		}
	}
	return result, true
}

func Digest(value any) (ArtifactDigest, error) {
	canonical, err := CanonicalJSON(value)
	if err != nil {
		return "", err
	}
	return digestBytes(canonical), nil
}

func DigestCanonicalJSON(raw []byte) (ArtifactDigest, error) {
	canonical, err := CanonicalizeJSON(raw)
	if err != nil {
		return "", err
	}
	return digestBytes(canonical), nil
}

func digestBytes(value []byte) ArtifactDigest {
	sum := sha256.Sum256(value)
	return ArtifactDigest(hex.EncodeToString(sum[:]))
}

func parseCanonicalValue(decoder *json.Decoder) (canonicalValue, error) {
	token, err := decoder.Token()
	if err != nil {
		return canonicalValue{}, fmt.Errorf("decode JSON value: %w", err)
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			return parseCanonicalObject(decoder)
		case '[':
			return parseCanonicalArray(decoder)
		default:
			return canonicalValue{}, fmt.Errorf("unexpected JSON delimiter %q", value)
		}
	case string:
		return canonicalValue{kind: 's', scalar: value}, nil
	case json.Number:
		return canonicalNumber(value)
	case bool:
		if value {
			return canonicalValue{kind: 't'}, nil
		}
		return canonicalValue{kind: 'f'}, nil
	case nil:
		return canonicalValue{kind: 'n'}, nil
	default:
		return canonicalValue{}, fmt.Errorf("unsupported JSON token %T", token)
	}
}

func parseCanonicalObject(decoder *json.Decoder) (canonicalValue, error) {
	members := make([]canonicalMember, 0)
	seen := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return canonicalValue{}, fmt.Errorf("decode object property: %w", err)
		}
		name, ok := token.(string)
		if !ok {
			return canonicalValue{}, errors.New("JSON object property is not a string")
		}
		if _, exists := seen[name]; exists {
			return canonicalValue{}, fmt.Errorf("duplicate object property %q", name)
		}
		seen[name] = struct{}{}
		value, err := parseCanonicalValue(decoder)
		if err != nil {
			return canonicalValue{}, fmt.Errorf("decode object property %q: %w", name, err)
		}
		members = append(members, canonicalMember{name: name, value: value})
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return canonicalValue{}, errors.New("JSON object is not terminated")
	}
	sort.Slice(members, func(i, j int) bool {
		return compareUTF16(members[i].name, members[j].name) < 0
	})
	return canonicalValue{kind: 'o', members: members}, nil
}

func parseCanonicalArray(decoder *json.Decoder) (canonicalValue, error) {
	items := make([]canonicalValue, 0)
	for decoder.More() {
		value, err := parseCanonicalValue(decoder)
		if err != nil {
			return canonicalValue{}, fmt.Errorf("decode array item %d: %w", len(items), err)
		}
		items = append(items, value)
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim(']') {
		return canonicalValue{}, errors.New("JSON array is not terminated")
	}
	return canonicalValue{kind: 'a', items: items}, nil
}

func canonicalNumber(value json.Number) (canonicalValue, error) {
	raw := value.String()
	if strings.ContainsAny(raw, ".eE") {
		return canonicalValue{}, fmt.Errorf("JSON number %q must be an integer", raw)
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return canonicalValue{}, fmt.Errorf("JSON number %q must be a signed 64-bit integer", raw)
	}
	if parsed < -maxJSONSafeInteger || parsed > maxJSONSafeInteger {
		return canonicalValue{}, fmt.Errorf("JSON number %q exceeds the safe integer range", raw)
	}
	return canonicalValue{kind: 'd', scalar: strconv.FormatInt(parsed, 10)}, nil
}

func compareUTF16(left, right string) int {
	leftUnits := utf16.Encode([]rune(left))
	rightUnits := utf16.Encode([]rune(right))
	for index := 0; index < len(leftUnits) && index < len(rightUnits); index++ {
		if leftUnits[index] < rightUnits[index] {
			return -1
		}
		if leftUnits[index] > rightUnits[index] {
			return 1
		}
	}
	switch {
	case len(leftUnits) < len(rightUnits):
		return -1
	case len(leftUnits) > len(rightUnits):
		return 1
	default:
		return 0
	}
}

func (value canonicalValue) appendTo(target *bytes.Buffer) {
	switch value.kind {
	case 'o':
		target.WriteByte('{')
		for index, member := range value.members {
			if index > 0 {
				target.WriteByte(',')
			}
			appendJSONString(target, member.name)
			target.WriteByte(':')
			member.value.appendTo(target)
		}
		target.WriteByte('}')
	case 'a':
		target.WriteByte('[')
		for index, item := range value.items {
			if index > 0 {
				target.WriteByte(',')
			}
			item.appendTo(target)
		}
		target.WriteByte(']')
	case 's':
		appendJSONString(target, value.scalar)
	case 'd':
		target.WriteString(value.scalar)
	case 't':
		target.WriteString("true")
	case 'f':
		target.WriteString("false")
	case 'n':
		target.WriteString("null")
	}
}

func appendJSONString(target *bytes.Buffer, value string) {
	const hexadecimal = "0123456789abcdef"

	target.WriteByte('"')
	for _, current := range []byte(value) {
		switch current {
		case '"', '\\':
			target.WriteByte('\\')
			target.WriteByte(current)
		case '\b':
			target.WriteString(`\b`)
		case '\t':
			target.WriteString(`\t`)
		case '\n':
			target.WriteString(`\n`)
		case '\f':
			target.WriteString(`\f`)
		case '\r':
			target.WriteString(`\r`)
		default:
			if current < 0x20 {
				target.WriteString(`\u00`)
				target.WriteByte(hexadecimal[current>>4])
				target.WriteByte(hexadecimal[current&0x0f])
				continue
			}
			target.WriteByte(current)
		}
	}
	target.WriteByte('"')
}
