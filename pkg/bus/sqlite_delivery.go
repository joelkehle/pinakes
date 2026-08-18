package bus

import (
	"fmt"
	"strings"
	"time"
)

type claimedPush struct {
	messageID   string
	callbackURL string
}

const durableDeliveryBackfill = "durable-delivery-v1"

const (
	durableInboxFallbackMaxEvents = 10_000
	durableInboxMaxBatchBytes     = 32 << 20
)

func deliveryExpiry(message Message) time.Time {
	expiresAt := message.TTLExpiresAt
	if !message.GraceUntil.IsZero() &&
		(expiresAt.IsZero() || message.GraceUntil.Before(expiresAt)) {
		expiresAt = message.GraceUntil
	}
	return expiresAt
}

// backfillLegacyDeliveryState upgrades pre-durability SQLite databases exactly
// once. Old SQLite retained messages but not inboxes or idempotency, so the
// safest reconstructable boundary is: unexpired unacknowledged requests plus
// unexpired response/inform messages. Executing and terminal requests are never
// redelivered.
func (s *SQLiteStore) backfillLegacyDeliveryState() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Beginx()
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	var applied int
	if err := tx.Get(&applied, `SELECT COUNT(*) FROM schema_migrations WHERE key = ?`,
		durableDeliveryBackfill); err != nil {
		return err
	}
	if applied > 0 {
		_ = tx.Rollback()
		committed = true
		return nil
	}

	now := s.inner.now()
	rows, err := tx.Query(`SELECT message_id, to_agent, created_at, ttl_expires_at, grace_until
		FROM messages
		WHERE to_agent <> ''
		  AND (
		    (type = 'request' AND state IN ('pending', 'waiting'))
		    OR type IN ('response', 'inform')
		  )
		  AND message_id NOT IN (SELECT message_id FROM deliveries)
		ORDER BY created_at, message_id`)
	if err != nil {
		return err
	}
	type legacyDelivery struct {
		messageID  string
		target     string
		createdAt  string
		expiresAt  string
		graceUntil string
	}
	var pending []legacyDelivery
	for rows.Next() {
		var item legacyDelivery
		if err := rows.Scan(&item.messageID, &item.target, &item.createdAt, &item.expiresAt, &item.graceUntil); err != nil {
			_ = rows.Close()
			return err
		}
		pending = append(pending, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range pending {
		createdAt, err := parseSQLiteTime(item.createdAt)
		if err != nil {
			return err
		}
		expiresAt := createdAt.Add(s.inner.cfg.DefaultMessageTTL)
		if item.expiresAt != "" {
			expiresAt, err = parseSQLiteTime(item.expiresAt)
			if err != nil {
				return err
			}
		} else if _, err := tx.Exec(`UPDATE messages SET ttl_expires_at = ?
			WHERE message_id = ?`, timeToString(expiresAt), item.messageID); err != nil {
			return err
		}
		if item.graceUntil != "" {
			graceUntil, err := parseSQLiteTime(item.graceUntil)
			if err != nil {
				return err
			}
			if graceUntil.Before(expiresAt) {
				expiresAt = graceUntil
			}
		}
		if !expiresAt.After(now) {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO delivery_cursors
			(target_agent_id, next_seq, acknowledged_cursor) VALUES (?, 0, 0)
			ON CONFLICT(target_agent_id) DO NOTHING`, item.target); err != nil {
			return err
		}
		var seq int
		if err := tx.Get(&seq, `SELECT next_seq FROM delivery_cursors
			WHERE target_agent_id = ?`, item.target); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO deliveries
			(target_agent_id, delivery_seq, message_id, status, next_attempt_at,
			 attempt_count, received_at, expires_at, last_error)
			VALUES (?, ?, ?, 'pending', ?, 0, '', ?, '')`,
			item.target, seq, item.messageID, item.createdAt, timeToString(expiresAt)); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE delivery_cursors SET next_seq = ?
			WHERE target_agent_id = ?`, seq+1, item.target); err != nil {
			return err
		}
	}

	idempotencyCutoff := timeToString(now.Add(-s.inner.cfg.IdempotencyWindow))
	idempotencyRows, err := tx.Query(`SELECT from_agent, to_agent, request_id,
		message_id, created_at FROM messages
		WHERE from_agent <> '' AND to_agent <> '' AND request_id <> ''
		  AND created_at >= ?
		ORDER BY created_at, message_id`, idempotencyCutoff)
	if err != nil {
		return err
	}
	type legacyReceipt struct {
		from, to, requestID, messageID, acceptedAt string
	}
	var receipts []legacyReceipt
	for idempotencyRows.Next() {
		var receipt legacyReceipt
		if err := idempotencyRows.Scan(&receipt.from, &receipt.to, &receipt.requestID,
			&receipt.messageID, &receipt.acceptedAt); err != nil {
			_ = idempotencyRows.Close()
			return err
		}
		receipts = append(receipts, receipt)
	}
	if err := idempotencyRows.Close(); err != nil {
		return err
	}
	for _, receipt := range receipts {
		acceptedAt, err := parseSQLiteTime(receipt.acceptedAt)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO idempotency
			(from_agent, to_agent, request_id, message_id, accepted_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(from_agent, to_agent, request_id) DO NOTHING`,
			receipt.from, receipt.to, receipt.requestID, receipt.messageID,
			receipt.acceptedAt,
			timeToString(acceptedAt.Add(s.inner.cfg.IdempotencyWindow))); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations (key, applied_at)
		VALUES (?, ?)`, durableDeliveryBackfill, timeToString(now)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

// persistAcceptance is the SQLite durability barrier used by Store.sendMessage.
// The in-memory projection and external callbacks are published only after this
// transaction commits.
func (s *SQLiteStore) persistAcceptance(a *sendAcceptance) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Beginx()
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if err := saveConversationTo(tx, &a.conversation); err != nil {
		return err
	}
	if err := saveMessageTo(tx, &a.message); err != nil {
		return err
	}
	if err := saveConversationMessageTo(tx, a.conversation.ConversationID, a.message.MessageID, a.conversationPos); err != nil {
		return err
	}
	if err := saveCountersTo(tx, a.nextConversationID, a.nextMessageID); err != nil {
		return err
	}

	if a.message.To != "" {
		if _, err := tx.Exec(`INSERT INTO delivery_cursors
			(target_agent_id, next_seq, acknowledged_cursor) VALUES (?, 0, 0)
			ON CONFLICT(target_agent_id) DO NOTHING`, a.message.To); err != nil {
			return err
		}
		var deliverySeq int
		if err := tx.Get(&deliverySeq, `SELECT next_seq FROM delivery_cursors WHERE target_agent_id = ?`, a.message.To); err != nil {
			return err
		}
		deliveryStatus := "pending"
		if a.pushURL != "" {
			deliveryStatus = "attempting"
		}
		if _, err := tx.Exec(`INSERT INTO deliveries
			(target_agent_id, delivery_seq, message_id, status, next_attempt_at,
			 attempt_count, received_at, expires_at, last_error)
			VALUES (?, ?, ?, ?, ?, 0, '', ?, '')`,
			a.message.To,
			deliverySeq,
			a.message.MessageID,
			deliveryStatus,
			timeToString(a.message.CreatedAt),
			timeToString(deliveryExpiry(a.message)),
		); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE delivery_cursors SET next_seq = ?
			WHERE target_agent_id = ?`, deliverySeq+1, a.message.To); err != nil {
			return err
		}
	}
	if a.idempotencyKey != "" {
		if _, err := tx.Exec(`INSERT INTO idempotency
			(from_agent, to_agent, request_id, message_id, accepted_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			a.message.From,
			a.message.To,
			a.message.RequestID,
			a.message.MessageID,
			timeToString(a.idempotency.CreatedAt),
			timeToString(a.idempotency.CreatedAt.Add(s.inner.cfg.IdempotencyWindow)),
		); err != nil {
			return err
		}
	}

	if hook := s.testHookBeforeCommit; hook != nil {
		if err := hook(); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	if a.pushURL != "" {
		messageID := a.message.MessageID
		a.pushSuccess = func(attempts int) {
			s.recordPushSuccess(messageID, attempts)
		}
		a.pushFailure = func(attempts int, failure string) {
			s.recordPushFailure(messageID, attempts, failure)
		}
	}
	return nil
}

func (s *SQLiteStore) recordPushSuccess(messageID string, attempts int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE deliveries
		SET status = 'received', received_at = ?, attempt_count = attempt_count + ?,
		    next_attempt_at = '', last_error = ''
		WHERE message_id = ? AND status = 'attempting'`,
		timeToString(s.inner.now()), attempts, messageID)
	s.recordDeliveryPersistenceResultLocked(messageID, "success", err)
}

func (s *SQLiteStore) recordPushFailure(messageID string, attempts int, failure string) {
	s.mu.Lock()
	hook := s.testHookPushReceipt
	s.mu.Unlock()
	if hook != nil {
		hook()
	}
	now := s.inner.now()
	nextAttempt := now.Add(pushCycleBackoff(s.inner.cfg, attempts))
	if len(failure) > 200 {
		failure = failure[:200]
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE deliveries
		SET status = CASE WHEN expires_at <= ? THEN 'failed' ELSE 'pending' END,
		    next_attempt_at = CASE WHEN expires_at <= ? THEN '' ELSE ? END,
		    attempt_count = attempt_count + ?, last_error = ?
		WHERE message_id = ? AND status = 'attempting'`,
		timeToString(now), timeToString(now), timeToString(nextAttempt),
		attempts, failure, messageID)
	s.recordDeliveryPersistenceResultLocked(messageID, "failure", err)
}

func (s *SQLiteStore) recordDeliveryPersistenceResultLocked(messageID, outcome string, err error) {
	if err == nil {
		delete(s.deliveryPersistErrors, messageID)
		return
	}
	s.deliveryPersistErrors[messageID] = err
	s.inner.logger.Printf("ERROR persist push receipt message_id=%s outcome=%s err=%v",
		messageID, outcome, err)
}

func pushCycleBackoff(cfg Config, attempts int) time.Duration {
	backoff := cfg.PushBaseBackoff
	if backoff <= 0 {
		backoff = 500 * time.Millisecond
	}
	for i := 1; i < attempts; i++ {
		backoff *= 2
		if backoff >= 30*time.Second {
			return 30 * time.Second
		}
	}
	return backoff
}

func (s *SQLiteStore) deliveryLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.pruneStop:
			return
		case <-ticker.C:
			for _, claimed := range s.claimDuePushes() {
				s.enqueueClaimedPush(claimed)
			}
		}
	}
}

func (s *SQLiteStore) claimDuePushes() []claimedPush {
	now := timeToString(s.inner.now())
	s.mu.Lock()
	defer s.mu.Unlock()

	_, _ = s.db.Exec(`UPDATE deliveries SET status = 'failed',
		next_attempt_at = '', last_error = 'transport deadline expired'
		WHERE status IN ('pending', 'attempting') AND expires_at <= ?`, now)

	rows, err := s.db.Query(`SELECT d.message_id, a.callback_url
		FROM deliveries d
		JOIN messages m ON m.message_id = d.message_id
		JOIN agents a ON a.agent_id = d.target_agent_id
		WHERE d.status = 'pending'
			  AND (d.next_attempt_at = '' OR d.next_attempt_at <= ?)
			  AND d.expires_at > ?
			  AND a.mode = 'push' AND a.status = 'active' AND a.expires_at > ?
			  AND a.callback_url <> ''
			ORDER BY d.next_attempt_at, d.delivery_seq
			LIMIT 32`, now, now, now)
	if err != nil {
		return nil
	}
	var candidates []claimedPush
	for rows.Next() {
		var claimed claimedPush
		if err := rows.Scan(&claimed.messageID, &claimed.callbackURL); err != nil {
			candidates = nil
			break
		}
		candidates = append(candidates, claimed)
	}
	_ = rows.Close()

	out := make([]claimedPush, 0, len(candidates))
	for _, claimed := range candidates {
		result, err := s.db.Exec(`UPDATE deliveries SET status = 'attempting'
			WHERE message_id = ? AND status = 'pending'`, claimed.messageID)
		if err != nil {
			continue
		}
		if n, err := result.RowsAffected(); err == nil && n == 1 {
			_, _ = s.db.Exec(`UPDATE messages
				SET queued_for_agent = 0,
				    state = CASE WHEN type = 'request' AND state = 'pending'
				                 THEN ? ELSE state END,
				    delivered_at = CASE WHEN delivered_at = '' THEN ? ELSE delivered_at END
				WHERE message_id = ?`, string(StateWaitingAck), now, claimed.messageID)
			out = append(out, claimed)
		}
	}
	return out
}

func (s *SQLiteStore) enqueueClaimedPush(claimed claimedPush) {
	s.mu.Lock()
	row := s.db.QueryRow(`SELECT message_id, type, from_agent, to_agent,
		conversation_id, request_id, in_reply_to, body, meta, attachments, state,
		created_at, terminal_at, delivered_at, last_progress_at, ttl_expires_at,
		grace_until, queued_for_agent
		FROM messages WHERE message_id = ?`, claimed.messageID)
	m, err := scanSQLiteMessage(row)
	s.mu.Unlock()
	if err != nil {
		s.recordPushFailure(claimed.messageID, 0, "message unavailable")
		return
	}
	payload := map[string]any{
		"message_id":      m.MessageID,
		"request_id":      m.RequestID,
		"type":            m.Type,
		"from":            m.From,
		"conversation_id": m.ConversationID,
		"body":            m.Body,
		"meta":            m.Meta,
		"attachments":     m.Attachments,
		"created_at":      m.CreatedAt,
	}
	messageID := m.MessageID
	s.inner.mu.Lock()
	if existing := s.inner.messages[messageID]; existing != nil {
		existing.QueuedForAgent = false
		if existing.Type == MessageTypeRequest && existing.State == StatePending {
			existing.State = StateWaitingAck
		}
		if existing.DeliveredAt.IsZero() {
			existing.DeliveredAt = m.DeliveredAt
		}
	}
	job := pushJob{
		url:     claimed.callbackURL,
		payload: payload,
		onSuccess: func(attempts int) {
			s.recordPushSuccess(messageID, attempts)
		},
		onFailure: func(attempts int, failure string) {
			s.recordPushFailure(messageID, attempts, failure)
		},
	}
	failure := s.inner.enqueuePushJobLocked(job)
	s.inner.mu.Unlock()
	if failure != "" {
		job.onFailure(0, failure)
	}
}

func (s *SQLiteStore) deliveryCounts() (pending, failed int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.db.Get(&pending, `SELECT COUNT(*) FROM deliveries
		WHERE status IN ('pending', 'attempting')`)
	_ = s.db.Get(&failed, `SELECT COUNT(*) FROM deliveries WHERE status = 'failed'`)
	return pending, failed
}

func (s *SQLiteStore) pollDurableInbox(input PollInboxInput) ([]InboxEvent, int, error) {
	agentID := strings.TrimSpace(input.AgentID)
	if agentID == "" {
		return nil, 0, newError(CodeValidation, "agent_id is required", false, 0)
	}
	if !s.inner.acceptsName(agentID) {
		return nil, 0, newError(CodeValidation, "agent_id must be prefixed with personal., ucla., or shared.", false, 0)
	}
	wait := input.Wait
	if wait < 0 {
		wait = 0
	}
	if wait > s.inner.cfg.InboxWaitMax {
		wait = s.inner.cfg.InboxWaitMax
	}
	deadline := s.inner.now().Add(wait)

	for {
		now := s.inner.now()
		s.inner.mu.Lock()
		s.inner.sweepLocked(now)
		agent, ok := s.inner.agents[agentID]
		if !ok || agent.Status != AgentStatusActive {
			s.inner.mu.Unlock()
			return nil, 0, newError(CodeUnauthorized, "agent is not registered/active", false, 0)
		}
		if err := s.inner.authorizeAgentForName(agent, "subscribe", agentID); err != nil {
			s.inner.mu.Unlock()
			return nil, 0, err
		}
		notifier := s.inner.inboxNotifyChanLocked(agentID)
		s.inner.mu.Unlock()

		events, cursor, err := s.readDurableInbox(agentID, input.Cursor, input.Limit)
		if err != nil {
			return nil, 0, err
		}
		if len(events) > 0 || wait == 0 {
			return events, cursor, nil
		}
		remaining := deadline.Sub(s.inner.now())
		if remaining <= 0 {
			return []InboxEvent{}, cursor, nil
		}
		timer := time.NewTimer(remaining)
		select {
		case <-notifier:
			timer.Stop()
		case <-timer.C:
			return []InboxEvent{}, cursor, nil
		}
	}
}

type sequencedMessage struct {
	seq     int
	message Message
}

func (s *SQLiteStore) durableInboxBatchLimits() (int, int) {
	maxEvents := s.inner.cfg.MaxInboxEventsPerAgent
	if maxEvents <= 0 {
		maxEvents = durableInboxFallbackMaxEvents
	}
	maxBytes := s.inner.cfg.MaxInboxBytesPerAgent
	if maxBytes <= 0 || maxBytes > durableInboxMaxBatchBytes {
		maxBytes = durableInboxMaxBatchBytes
	}
	return maxEvents, maxBytes
}

func messageInboxEvent(message Message) InboxEvent {
	return InboxEvent{
		MessageID:      message.MessageID,
		Type:           message.Type,
		From:           message.From,
		ConversationID: message.ConversationID,
		Body:           message.Body,
		Meta:           message.Meta,
		Attachments:    append([]Attachment{}, message.Attachments...),
		CreatedAt:      message.CreatedAt,
	}
}

func (s *SQLiteStore) readDurableInbox(agentID string, requestedCursor, requestedLimit int) ([]InboxEvent, int, error) {
	s.mu.Lock()
	tx, err := s.db.Beginx()
	if err != nil {
		s.mu.Unlock()
		return nil, 0, err
	}
	committed := false
	locked := true
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
		if locked {
			s.mu.Unlock()
		}
	}()

	if _, err := tx.Exec(`INSERT INTO delivery_cursors
		(target_agent_id, next_seq, acknowledged_cursor) VALUES (?, 0, 0)
		ON CONFLICT(target_agent_id) DO NOTHING`, agentID); err != nil {
		return nil, 0, err
	}
	var nextSeq, acknowledged int
	if err := tx.QueryRow(`SELECT next_seq, acknowledged_cursor
		FROM delivery_cursors WHERE target_agent_id = ?`, agentID).
		Scan(&nextSeq, &acknowledged); err != nil {
		return nil, 0, err
	}
	cursor := requestedCursor
	if cursor < acknowledged {
		cursor = acknowledged
	}
	if cursor > nextSeq {
		cursor = nextSeq
	}
	if cursor > acknowledged {
		now := timeToString(s.inner.now())
		if _, err := tx.Exec(`UPDATE delivery_cursors SET acknowledged_cursor = ?
			WHERE target_agent_id = ?`, cursor, agentID); err != nil {
			return nil, 0, err
		}
		if _, err := tx.Exec(`UPDATE deliveries
			SET status = 'received', received_at = ?, last_error = ''
			WHERE target_agent_id = ? AND delivery_seq < ?
			  AND status NOT IN ('received', 'failed')`,
			now, agentID, cursor); err != nil {
			return nil, 0, err
		}
	}

	maxEvents, maxBytes := s.durableInboxBatchLimits()
	if requestedLimit > 0 && requestedLimit < maxEvents {
		maxEvents = requestedLimit
	}
	rows, err := tx.Query(`SELECT d.delivery_seq,
			m.message_id, m.type, m.from_agent, m.to_agent, m.conversation_id,
			m.request_id, m.in_reply_to, m.body, m.meta, m.attachments, m.state,
			m.created_at, m.terminal_at, m.delivered_at, m.last_progress_at,
			m.ttl_expires_at, m.grace_until, m.queued_for_agent
		FROM deliveries d
		JOIN messages m ON m.message_id = d.message_id
		WHERE d.target_agent_id = ? AND d.delivery_seq >= ?
		  AND d.status IN ('pending', 'attempting') AND d.expires_at > ?
		ORDER BY d.delivery_seq
		LIMIT ?`,
		agentID, cursor, timeToString(s.inner.now()), maxEvents)
	if err != nil {
		return nil, 0, err
	}
	var pending []sequencedMessage
	batchBytes := 0
	for rows.Next() {
		var item sequencedMessage
		message, err := scanSQLiteMessage(rows, &item.seq)
		if err != nil {
			_ = rows.Close()
			return nil, 0, err
		}
		item.message = message
		eventBytes := inboxEventSize(messageInboxEvent(message))
		// Keep cursor progress possible if a legacy/imported event exceeds the
		// batch ceiling by itself. Normal HTTP acceptance constrains individual
		// events through MaxBodyBytes.
		if len(pending) > 0 && batchBytes+eventBytes > maxBytes {
			break
		}
		batchBytes += eventBytes
		pending = append(pending, item)
	}
	if err := rows.Close(); err != nil {
		return nil, 0, err
	}
	offeredTime := s.inner.now()
	offeredAt := timeToString(offeredTime)
	for i, item := range pending {
		if _, err := tx.Exec(`UPDATE messages
			SET queued_for_agent = 0,
			    state = CASE WHEN type = 'request' AND state = 'pending'
			                 THEN ? ELSE state END,
			    delivered_at = CASE WHEN delivered_at = '' THEN ? ELSE delivered_at END
			WHERE message_id = ?`, string(StateWaitingAck), offeredAt, item.message.MessageID); err != nil {
			return nil, 0, err
		}
		pending[i].message.QueuedForAgent = false
		if pending[i].message.Type == MessageTypeRequest && pending[i].message.State == StatePending {
			pending[i].message.State = StateWaitingAck
		}
		if pending[i].message.DeliveredAt.IsZero() {
			pending[i].message.DeliveredAt = offeredTime
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, err
	}
	committed = true
	s.mu.Unlock()
	locked = false

	next := cursor
	if len(pending) > 0 {
		next = pending[len(pending)-1].seq + 1
	}
	s.inner.mu.Lock()
	for _, item := range pending {
		if existing := s.inner.messages[item.message.MessageID]; existing != nil {
			existing.QueuedForAgent = false
			if existing.Type == MessageTypeRequest && existing.State == StatePending {
				existing.State = StateWaitingAck
			}
			if existing.DeliveredAt.IsZero() {
				existing.DeliveredAt = item.message.DeliveredAt
			}
		}
	}
	s.inner.mu.Unlock()
	events := make([]InboxEvent, 0, len(pending))
	for _, item := range pending {
		events = append(events, messageInboxEvent(item.message))
	}
	return events, next, nil
}

// persistAck commits the application lifecycle transition and the transport
// receipt together. An application ack is also proof that the request arrived.
func (s *SQLiteStore) persistAck(message *Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Beginx()
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := saveMessageTo(tx, message); err != nil {
		return err
	}
	now := timeToString(s.inner.now())
	if _, err := tx.Exec(`UPDATE deliveries
		SET status = 'received', received_at = ?, last_error = ''
		WHERE message_id = ? AND status NOT IN ('received', 'failed')`,
		now, message.MessageID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func (s *SQLiteStore) loadIdempotency() error {
	now := timeToString(s.inner.now())
	rows, err := s.db.Query(`SELECT from_agent, to_agent, request_id,
		message_id, accepted_at FROM idempotency WHERE expires_at >= ?`, now)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var from, to, requestID, messageID, acceptedAt string
		if err := rows.Scan(&from, &to, &requestID, &messageID, &acceptedAt); err != nil {
			return err
		}
		accepted, err := parseSQLiteTime(acceptedAt)
		if err != nil {
			return fmt.Errorf("parse idempotency accepted_at: %w", err)
		}
		s.inner.idempotency[dedupeKey(from, to, requestID)] = idempotencyEntry{
			MessageID: messageID,
			CreatedAt: accepted,
		}
	}
	return rows.Err()
}

func parseSQLiteTime(raw string) (time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, raw)
}
