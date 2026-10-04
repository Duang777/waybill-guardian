package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/hastekit/agent-sdk-go/pkg/agents"
	"github.com/hastekit/agent-sdk-go/pkg/agents/history"
	"github.com/hastekit/agent-sdk-go/pkg/gateway/llm/responses"
)

const (
	historySchemaKey     = "history_schema_version"
	historySchemaVersion = 1
)

var (
	ErrUnsafeHistory = errors.New("agent history contains prohibited data")

	mobileNumberPattern     = regexp.MustCompile(`(^|[^0-9])1[3-9]([ -]?[0-9]){9}([^0-9]|$)`)
	licensePlatePattern     = regexp.MustCompile(`[京津沪渝冀豫云辽黑湘皖鲁新苏浙赣鄂桂甘晋蒙陕吉闽贵粤青藏川宁琼使领][A-HJ-NP-Z][· -]?[A-HJ-NP-Z0-9]{5,6}`)
	forbiddenJSONKeyPattern = regexp.MustCompile(`(?i)"(shipper_phone|phone|plate|license_plate|longitude|latitude|template_id|params)"[[:space:]]*:`)
	opaqueIDPattern         = regexp.MustCompile(`^(?:(?:msg_|fc_|call_)[0-9a-f]{16}|(?:RA|CL|SMS)-[0-9a-f]{12})$`)
	forbiddenHistoryKeys    = map[string]struct{}{
		"phone":         {},
		"shipper_phone": {},
		"plate":         {},
		"license_plate": {},
		"longitude":     {},
		"latitude":      {},
		"template_id":   {},
		"params":        {},
	}
)

type historyGuard struct {
	agents.NoopMiddleware
}

func (historyGuard) WrapModelCall(next agents.ModelCallFunc) agents.ModelCallFunc {
	return func(
		ctx context.Context,
		call *agents.ModelCall,
		request *responses.Request,
	) (*responses.Response, error) {
		if err := validateHistoryValue("model request", request); err != nil {
			return nil, err
		}
		response, err := next(ctx, call, request)
		if err != nil {
			return nil, err
		}
		if err := validateHistoryValue("model response", response); err != nil {
			return nil, err
		}
		return response, nil
	}
}

type guardedHistoryPersistence struct {
	history.ConversationPersistenceAdapter
}

func guardHistoryPersistence(
	persistence history.ConversationPersistenceAdapter,
) history.ConversationPersistenceAdapter {
	return &guardedHistoryPersistence{ConversationPersistenceAdapter: persistence}
}

func (p *guardedHistoryPersistence) LoadMessages(
	ctx context.Context,
	namespace string,
	threadID string,
	previousRunID string,
) ([]history.ConversationMessage, error) {
	rows, err := p.ConversationPersistenceAdapter.LoadMessages(
		ctx,
		namespace,
		threadID,
		previousRunID,
	)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if err := requireCurrentHistorySchema(rows[i].Meta); err != nil {
			return nil, fmt.Errorf("load history run %q: %w", rows[i].RunID, err)
		}
		if err := validateHistoryValue("loaded history", rows[i]); err != nil {
			return nil, err
		}
	}
	return rows, nil
}

func (p *guardedHistoryPersistence) SaveMessages(
	ctx context.Context,
	namespace string,
	groupID string,
	runID string,
	previousRunID string,
	threadID string,
	conversationID string,
	messages []history.Message,
	meta map[string]any,
) error {
	stampedMeta := stampHistorySchema(meta)
	if err := validateHistoryValue("history messages", messages); err != nil {
		return err
	}
	if err := validateHistoryValue("history metadata", stampedMeta); err != nil {
		return err
	}
	return p.ConversationPersistenceAdapter.SaveMessages(
		ctx,
		namespace,
		groupID,
		runID,
		previousRunID,
		threadID,
		conversationID,
		messages,
		stampedMeta,
	)
}

func (p *guardedHistoryPersistence) SaveSummary(
	ctx context.Context,
	namespace string,
	summary history.Summary,
) error {
	summary.Meta = stampHistorySchema(summary.Meta)
	if err := validateHistoryValue("history summary", summary); err != nil {
		return err
	}
	return p.ConversationPersistenceAdapter.SaveSummary(ctx, namespace, summary)
}

func (p *guardedHistoryPersistence) Close() error {
	closer, ok := p.ConversationPersistenceAdapter.(io.Closer)
	if !ok {
		return nil
	}
	return closer.Close()
}

func stampHistorySchema(meta map[string]any) map[string]any {
	stamped := make(map[string]any, len(meta)+1)
	for key, value := range meta {
		stamped[key] = value
	}
	stamped[historySchemaKey] = historySchemaVersion
	return stamped
}

func requireCurrentHistorySchema(meta map[string]any) error {
	value, ok := meta[historySchemaKey]
	if !ok {
		return fmt.Errorf("%w: missing %s", ErrUnsafeHistory, historySchemaKey)
	}
	current := false
	switch version := value.(type) {
	case int:
		current = version == historySchemaVersion
	case int64:
		current = version == historySchemaVersion
	case float64:
		current = version == historySchemaVersion
	case json.Number:
		current = version.String() == "1"
	}
	if !current {
		return fmt.Errorf("%w: unsupported %s", ErrUnsafeHistory, historySchemaKey)
	}
	return nil
}

func validateHistoryValue(label string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode %s for privacy validation: %w", label, err)
	}
	decoded, err := decodeHistoryJSON(raw)
	if err != nil {
		return fmt.Errorf("decode %s for privacy validation: %w", label, err)
	}
	if err := inspectHistoryValue(decoded, "$"); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	return nil
}

func decodeHistoryJSON(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = fmt.Errorf("multiple JSON values")
		}
		return nil, err
	}
	return decoded, nil
}

func inspectHistoryValue(value any, path string) error {
	return inspectHistoryField(value, path, "")
}

func inspectHistoryField(value any, path, field string) error {
	switch current := value.(type) {
	case map[string]any:
		for key, child := range current {
			if _, prohibited := forbiddenHistoryKeys[strings.ToLower(key)]; prohibited {
				return fmt.Errorf("%w: prohibited field at %s.%s", ErrUnsafeHistory, path, key)
			}
			if err := inspectHistoryField(child, path+"."+key, key); err != nil {
				return err
			}
		}
	case []any:
		for index, child := range current {
			if err := inspectHistoryField(child, fmt.Sprintf("%s[%d]", path, index), field); err != nil {
				return err
			}
		}
	case string:
		if isOpaqueHistoryIdentifier(field, current) {
			return nil
		}
		if candidate, ok := nestedJSONCandidate(current); ok {
			nested, err := decodeHistoryJSON([]byte(candidate))
			if err != nil {
				return fmt.Errorf("%w: malformed nested JSON at %s", ErrUnsafeHistory, path)
			}
			return inspectHistoryValue(nested, path+"<json>")
		}
		if mobileNumberPattern.MatchString(current) {
			return fmt.Errorf("%w: mobile number at %s", ErrUnsafeHistory, path)
		}
		if licensePlatePattern.MatchString(strings.ToUpper(current)) {
			return fmt.Errorf("%w: license plate at %s", ErrUnsafeHistory, path)
		}
		if forbiddenJSONKeyPattern.MatchString(current) {
			return fmt.Errorf("%w: prohibited JSON field at %s", ErrUnsafeHistory, path)
		}
	}
	return nil
}

func isOpaqueHistoryIdentifier(key string, value string) bool {
	key = strings.ToLower(key)
	if key == "queued_approvals" || key == "queued_rejections" {
		return opaqueIDPattern.MatchString(value)
	}
	if key != "id" &&
		key != "traceid" &&
		!strings.HasSuffix(key, "_id") {
		return false
	}
	if isOpaqueUUID(value) || opaqueIDPattern.MatchString(value) {
		return true
	}
	return key == "incident_id" &&
		strings.HasPrefix(value, "delay-") &&
		isCanonicalUUID(strings.TrimPrefix(value, "delay-"))
}

func isOpaqueUUID(value string) bool {
	candidate := value
	if separator := strings.IndexByte(value, '_'); separator >= 0 {
		switch value[:separator] {
		case "msg", "fc", "rs", "ws", "resp":
			candidate = value[separator+1:]
		default:
			return false
		}
	}
	return isCanonicalUUID(candidate)
}

func isCanonicalUUID(candidate string) bool {
	parsed, err := uuid.Parse(candidate)
	return err == nil && parsed.String() == strings.ToLower(candidate)
}

func nestedJSONCandidate(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		return trimmed, true
	}
	if !strings.HasPrefix(trimmed, "```") || !strings.HasSuffix(trimmed, "```") {
		return "", false
	}
	body := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "```"), "```"))
	if newline := strings.IndexByte(body, '\n'); newline >= 0 {
		language := strings.TrimSpace(body[:newline])
		if language == "" || strings.EqualFold(language, "json") {
			body = strings.TrimSpace(body[newline+1:])
		}
	}
	if strings.HasPrefix(body, "{") || strings.HasPrefix(body, "[") {
		return body, true
	}
	return "", false
}

var _ agents.Middleware = historyGuard{}
var _ history.ConversationPersistenceAdapter = (*guardedHistoryPersistence)(nil)
