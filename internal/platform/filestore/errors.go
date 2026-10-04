package filestore

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const maxValidationIssues = 100

type ValidationIssue struct {
	Location string
	Field    string
	Code     string
	Message  string
}

type ValidationError struct {
	Issues []ValidationIssue
}

func (e *ValidationError) Error() string {
	if e == nil || len(e.Issues) == 0 {
		return "data validation failed"
	}
	lines := make([]string, len(e.Issues))
	for index, issue := range e.Issues {
		prefix := issue.Location
		if issue.Field != "" {
			if prefix != "" {
				prefix += " "
			}
			prefix += issue.Field
		}
		if prefix == "" {
			lines[index] = issue.Message
		} else {
			lines[index] = prefix + ": " + issue.Message
		}
	}
	return strings.Join(lines, "\n")
}

type issueCollector struct {
	issues []ValidationIssue
}

func (c *issueCollector) add(location, field, code, format string, args ...any) {
	if len(c.issues) >= maxValidationIssues {
		return
	}
	c.issues = append(c.issues, ValidationIssue{
		Location: location,
		Field:    field,
		Code:     code,
		Message:  fmt.Sprintf(format, args...),
	})
}

func (c *issueCollector) err() error {
	if len(c.issues) == 0 {
		return nil
	}
	sort.SliceStable(c.issues, func(left, right int) bool {
		a := c.issues[left]
		b := c.issues[right]
		if a.Location != b.Location {
			return locationLess(a.Location, b.Location)
		}
		if a.Field != b.Field {
			return a.Field < b.Field
		}
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		return a.Message < b.Message
	})
	return &ValidationError{Issues: append([]ValidationIssue(nil), c.issues...)}
}

func locationLess(left, right string) bool {
	leftKind, leftNumber, leftOK := splitLocation(left)
	rightKind, rightNumber, rightOK := splitLocation(right)
	if leftOK && rightOK && leftKind == rightKind {
		return leftNumber < rightNumber
	}
	return left < right
}

func splitLocation(value string) (string, int, bool) {
	if kind, numberText, ok := strings.Cut(value, " "); ok {
		number, err := strconv.Atoi(numberText)
		if err == nil {
			return kind, number, true
		}
	}
	open := strings.LastIndexByte(value, '[')
	if open > 0 && strings.HasSuffix(value, "]") {
		number, err := strconv.Atoi(value[open+1 : len(value)-1])
		if err == nil {
			return value[:open], number, true
		}
	}
	return "", 0, false
}
