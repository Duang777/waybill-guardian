package postgres

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/hastekit/agent-sdk-go/pkg/agents/history"
	"github.com/jackc/pgx/v5"
)

const (
	historyPrivacySchemaVersion int16 = 1
	defaultHistoryRetention           = 7 * 24 * time.Hour
)

type HistoryConfig struct {
	TenantID  string
	KeyID     string
	Key       []byte
	Clock     func() time.Time
	Retention time.Duration
}

type ConversationPersistence struct {
	db       *DB
	tenantID string
	keyID    string
	aead     cipher.AEAD
	clock    func() time.Time
}

type checkpointPayload struct {
	Messages []history.Message `json:"messages"`
	Meta     map[string]any    `json:"meta"`
}

func NewConversationPersistence(
	ctx context.Context,
	db *DB,
	config HistoryConfig,
) (*ConversationPersistence, error) {
	if db == nil || db.pool == nil {
		return nil, fmt.Errorf("PostgreSQL database is required")
	}
	if config.TenantID == "" {
		return nil, fmt.Errorf("tenant ID is required")
	}
	if config.KeyID == "" {
		return nil, fmt.Errorf("checkpoint encryption key ID is required")
	}
	if len(config.Key) != 32 {
		return nil, fmt.Errorf("checkpoint encryption key must be 32 bytes")
	}
	block, err := aes.NewCipher(config.Key)
	if err != nil {
		return nil, fmt.Errorf("create checkpoint cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create checkpoint AEAD: %w", err)
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	if config.Retention == 0 {
		config.Retention = defaultHistoryRetention
	}
	if config.Retention < 0 {
		return nil, fmt.Errorf("history retention must be positive")
	}
	persistence := &ConversationPersistence{
		db:       db,
		tenantID: config.TenantID,
		keyID:    config.KeyID,
		aead:     aead,
		clock:    config.Clock,
	}
	if err := persistence.prune(ctx, config.Clock().UTC().Add(-config.Retention)); err != nil {
		return nil, err
	}
	return persistence, nil
}

func (p *ConversationPersistence) NewConversationID(context.Context) string {
	return uuid.NewString()
}

func (p *ConversationPersistence) NewRunID(context.Context) string {
	return uuid.NewString()
}

func (p *ConversationPersistence) Now(context.Context) time.Time {
	return p.clock().UTC()
}

func (p *ConversationPersistence) LoadMessages(
	ctx context.Context,
	namespace string,
	threadID string,
	previousRunID string,
) ([]history.ConversationMessage, error) {
	if namespace == "" || threadID == "" {
		return []history.ConversationMessage{}, nil
	}
	rows, err := p.db.pool.Query(ctx, `
		SELECT sdk_run_id, COALESCE(previous_sdk_run_id, ''), conversation_id, group_id,
		       privacy_schema_version, ciphertext, payload_hash, encryption_key_id
		FROM waybill.agent_checkpoints
		WHERE tenant_id = $1 AND namespace = $2 AND thread_id = $3
		ORDER BY checkpoint_version
	`, p.tenantID, namespace, threadID)
	if err != nil {
		return nil, fmt.Errorf("load PostgreSQL checkpoints: %w", err)
	}
	defer rows.Close()

	var result []history.ConversationMessage
	foundPrevious := previousRunID == ""
	for rows.Next() {
		var runID, storedPreviousRunID, conversationID, groupID string
		var ciphertext []byte
		var payloadHash, keyID string
		var privacySchemaVersion int16
		if err := rows.Scan(
			&runID,
			&storedPreviousRunID,
			&conversationID,
			&groupID,
			&privacySchemaVersion,
			&ciphertext,
			&payloadHash,
			&keyID,
		); err != nil {
			return nil, fmt.Errorf("scan PostgreSQL checkpoint: %w", err)
		}
		if privacySchemaVersion != historyPrivacySchemaVersion {
			return nil, fmt.Errorf(
				"PostgreSQL checkpoint %q has unsupported privacy schema version",
				runID,
			)
		}
		raw, err := p.open(ciphertext, keyID, checkpointAAD(
			p.tenantID,
			namespace,
			threadID,
			runID,
		))
		if err != nil {
			return nil, fmt.Errorf("decrypt PostgreSQL checkpoint %q: %w", runID, err)
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != payloadHash {
			return nil, fmt.Errorf("PostgreSQL checkpoint %q has invalid hash", runID)
		}
		var payload checkpointPayload
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, fmt.Errorf("decode PostgreSQL checkpoint %q: %w", runID, err)
		}
		result = append(result, history.ConversationMessage{
			GroupID:        groupID,
			RunID:          runID,
			ThreadID:       threadID,
			ConversationID: conversationID,
			Messages:       payload.Messages,
			Meta:           payload.Meta,
		})
		if previousRunID != "" && runID == previousRunID {
			foundPrevious = true
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load PostgreSQL checkpoints: %w", err)
	}
	if previousRunID != "" && !foundPrevious {
		return nil, fmt.Errorf("previous SDK run %q is not in thread %q", previousRunID, threadID)
	}
	return result, nil
}

func (p *ConversationPersistence) SaveMessages(
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
	if namespace == "" || runID == "" || threadID == "" || conversationID == "" {
		return fmt.Errorf("namespace, run ID, thread ID, and conversation ID are required")
	}
	groupID = history.NormalizeGroupID(groupID)
	tx, err := p.db.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin PostgreSQL checkpoint save: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()
	if err := p.validateRunClaim(ctx, tx, threadID); err != nil {
		return err
	}

	var (
		version              int64
		storedPreviousRunID  string
		storedThread         string
		storedConversation   string
		storedGroup          string
		ciphertext           []byte
		payloadHash          string
		keyID                string
		privacySchemaVersion int16
	)
	err = tx.QueryRow(ctx, `
		SELECT checkpoint_version, COALESCE(previous_sdk_run_id, ''), thread_id,
		       conversation_id, group_id, privacy_schema_version, ciphertext,
		       payload_hash, encryption_key_id
		FROM waybill.agent_checkpoints
		WHERE tenant_id = $1 AND namespace = $2 AND sdk_run_id = $3
		FOR UPDATE
	`, p.tenantID, namespace, runID).Scan(
		&version,
		&storedPreviousRunID,
		&storedThread,
		&storedConversation,
		&storedGroup,
		&privacySchemaVersion,
		&ciphertext,
		&payloadHash,
		&keyID,
	)
	payload := checkpointPayload{}
	switch {
	case err == nil:
		if storedThread != threadID {
			return fmt.Errorf("SDK run %q belongs to another thread", runID)
		}
		if privacySchemaVersion != historyPrivacySchemaVersion {
			return fmt.Errorf(
				"PostgreSQL checkpoint %q has unsupported privacy schema version",
				runID,
			)
		}
		raw, err := p.open(
			ciphertext,
			keyID,
			checkpointAAD(p.tenantID, namespace, storedThread, runID),
		)
		if err != nil {
			return fmt.Errorf("decrypt PostgreSQL checkpoint %q: %w", runID, err)
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != payloadHash {
			return fmt.Errorf("PostgreSQL checkpoint %q has invalid hash", runID)
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			return fmt.Errorf("decode PostgreSQL checkpoint %q: %w", runID, err)
		}
		previousRunID = storedPreviousRunID
		conversationID = storedConversation
		groupID = storedGroup
	case errors.Is(err, pgx.ErrNoRows):
		if previousRunID != "" {
			var parentThread string
			if err := tx.QueryRow(ctx, `
				SELECT thread_id
				FROM waybill.agent_checkpoints
				WHERE tenant_id = $1 AND namespace = $2 AND sdk_run_id = $3
			`, p.tenantID, namespace, previousRunID).Scan(&parentThread); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return fmt.Errorf("previous SDK run %q does not exist", previousRunID)
				}
				return fmt.Errorf("read PostgreSQL parent checkpoint: %w", err)
			}
			if parentThread != threadID {
				return fmt.Errorf("previous SDK run %q belongs to another thread", previousRunID)
			}
		}
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(MAX(checkpoint_version), 0) + 1
			FROM waybill.agent_checkpoints
			WHERE tenant_id = $1 AND namespace = $2 AND thread_id = $3
		`, p.tenantID, namespace, threadID).Scan(&version); err != nil {
			return fmt.Errorf("allocate PostgreSQL checkpoint version: %w", err)
		}
	default:
		return fmt.Errorf("read PostgreSQL checkpoint: %w", err)
	}
	payload.Messages = append(payload.Messages, messages...)
	if meta != nil {
		payload.Meta = meta
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode PostgreSQL checkpoint: %w", err)
	}
	sealed, err := p.seal(raw, checkpointAAD(p.tenantID, namespace, threadID, runID))
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	if _, err := tx.Exec(ctx, `
		INSERT INTO waybill.agent_checkpoints (
			tenant_id, namespace, thread_id, sdk_run_id, checkpoint_version,
			previous_sdk_run_id, conversation_id, ciphertext, payload_hash,
			encryption_key_id, group_id, privacy_schema_version
		) VALUES (
			$1, $2, $3, $4, $5,
			NULLIF($6, ''), $7, $8, $9,
			$10, $11, $12
		)
		ON CONFLICT (tenant_id, namespace, thread_id, checkpoint_version)
		DO UPDATE SET
			ciphertext = EXCLUDED.ciphertext,
			payload_hash = EXCLUDED.payload_hash,
			encryption_key_id = EXCLUDED.encryption_key_id,
			privacy_schema_version = EXCLUDED.privacy_schema_version
	`, p.tenantID, namespace, threadID, runID, version, previousRunID,
		conversationID, sealed, hash, p.keyID, groupID, historyPrivacySchemaVersion); err != nil {
		return fmt.Errorf("save PostgreSQL checkpoint: %w", MapError(err))
	}
	if _, err := tx.Exec(ctx, `
		UPDATE waybill.runs
		SET checkpoint_version = GREATEST(checkpoint_version, $3),
		    sdk_run_id = $4,
		    updated_at = clock_timestamp()
		WHERE tenant_id = $1 AND run_id = $2
	`, p.tenantID, threadID, version, runID); err != nil {
		return fmt.Errorf("bind PostgreSQL checkpoint to run: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit PostgreSQL checkpoint: %w", err)
	}
	return nil
}

func (p *ConversationPersistence) SaveSummary(
	ctx context.Context,
	namespace string,
	summary history.Summary,
) error {
	if namespace == "" || summary.ThreadID == "" || summary.ID == "" {
		return fmt.Errorf("namespace, thread ID, and summary ID are required")
	}
	if err := p.validateRunClaim(ctx, nil, summary.ThreadID); err != nil {
		return err
	}
	raw, err := json.Marshal(summary)
	if err != nil {
		return fmt.Errorf("encode PostgreSQL summary: %w", err)
	}
	sealed, err := p.seal(
		raw,
		[]byte(p.tenantID+"\x00"+namespace+"\x00"+summary.ThreadID+"\x00summary"),
	)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	if _, err := p.db.pool.Exec(ctx, `
		INSERT INTO waybill.agent_summaries (
			tenant_id, namespace, thread_id, summary_id, payload, payload_hash,
			privacy_schema_version
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (tenant_id, namespace, thread_id)
		DO UPDATE SET
			summary_id = EXCLUDED.summary_id,
			payload = EXCLUDED.payload,
			payload_hash = EXCLUDED.payload_hash,
			privacy_schema_version = EXCLUDED.privacy_schema_version,
			updated_at = clock_timestamp()
	`, p.tenantID, namespace, summary.ThreadID, summary.ID, sealed,
		hex.EncodeToString(sum[:]), historyPrivacySchemaVersion); err != nil {
		return fmt.Errorf("save PostgreSQL summary: %w", err)
	}
	return nil
}

func (p *ConversationPersistence) prune(ctx context.Context, cutoff time.Time) error {
	tx, err := p.db.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin PostgreSQL history retention: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()
	if _, err := tx.Exec(ctx, `
		DELETE FROM waybill.agent_summaries summary
		USING waybill.runs run
		WHERE summary.tenant_id = $1
		  AND run.tenant_id = summary.tenant_id
		  AND run.run_id = summary.thread_id
		  AND run.status IN ('completed', 'rejected', 'failed', 'review_required', 'manual_review')
		  AND run.closed_at < $2
	`, p.tenantID, cutoff); err != nil {
		return fmt.Errorf("prune PostgreSQL history summaries: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM waybill.agent_checkpoints checkpoint
		USING waybill.runs run
		WHERE checkpoint.tenant_id = $1
		  AND run.tenant_id = checkpoint.tenant_id
		  AND run.run_id = checkpoint.thread_id
		  AND run.status IN ('completed', 'rejected', 'failed', 'review_required', 'manual_review')
		  AND run.closed_at < $2
	`, p.tenantID, cutoff); err != nil {
		return fmt.Errorf("prune PostgreSQL history checkpoints: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE waybill.runs
		SET sdk_run_id = NULL,
		    checkpoint_version = 0
		WHERE tenant_id = $1
		  AND status IN ('completed', 'rejected', 'failed', 'review_required', 'manual_review')
		  AND closed_at < $2
	`, p.tenantID, cutoff); err != nil {
		return fmt.Errorf("clear PostgreSQL history pointers: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit PostgreSQL history retention: %w", err)
	}
	return nil
}

func (p *ConversationPersistence) validateRunClaim(
	ctx context.Context,
	tx pgx.Tx,
	threadID string,
) error {
	claim := claimFromContext(ctx)
	if claim == nil ||
		claim.repository == nil ||
		claim.repository.db != p.db ||
		claim.repository.tenantID != p.tenantID ||
		string(claim.runID) != threadID {
		return ErrStaleRunClaim
	}
	query := `
		SELECT EXISTS (
			SELECT 1
			FROM waybill.runs
			WHERE tenant_id = $1 AND run_id = $2
			  AND lease_owner = $3 AND fencing_token = $4
			  AND lease_deadline > clock_timestamp()
		)
	`
	var valid bool
	var err error
	if tx == nil {
		err = p.db.pool.QueryRow(
			ctx,
			query,
			p.tenantID,
			threadID,
			claim.owner,
			claim.fence,
		).Scan(&valid)
	} else {
		err = tx.QueryRow(
			ctx,
			query,
			p.tenantID,
			threadID,
			claim.owner,
			claim.fence,
		).Scan(&valid)
	}
	if err != nil {
		return fmt.Errorf("validate PostgreSQL checkpoint run claim: %w", err)
	}
	if !valid {
		return ErrStaleRunClaim
	}
	return nil
}

func (p *ConversationPersistence) seal(plaintext []byte, additionalData []byte) ([]byte, error) {
	nonce := make([]byte, p.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("create checkpoint nonce: %w", err)
	}
	return p.aead.Seal(nonce, nonce, plaintext, additionalData), nil
}

func (p *ConversationPersistence) open(
	ciphertext []byte,
	keyID string,
	additionalData []byte,
) ([]byte, error) {
	if keyID != p.keyID {
		return nil, fmt.Errorf("checkpoint encryption key %q is unavailable", keyID)
	}
	nonceSize := p.aead.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("checkpoint ciphertext is truncated")
	}
	plaintext, err := p.aead.Open(
		nil,
		ciphertext[:nonceSize],
		ciphertext[nonceSize:],
		additionalData,
	)
	if err != nil {
		return nil, fmt.Errorf("authenticate checkpoint: %w", err)
	}
	return plaintext, nil
}

func checkpointAAD(tenantID, namespace, threadID, runID string) []byte {
	return []byte(tenantID + "\x00" + namespace + "\x00" + threadID + "\x00" + runID)
}

var _ history.ConversationPersistenceAdapter = (*ConversationPersistence)(nil)
