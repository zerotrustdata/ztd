// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

// SSE Event Bridge
//
// GET  /api/v1/sse/events   → Consumer (CLI / dashboard) reads one page of stored events (backfill).
//                            Route is built entirely from auth context:
//                            user_id from cert/cookie, session from header/cookie.
//                            Query string carries only since_id and limit.
// GET  /api/v1/sse/stream   → Consumer streams events via SSE.
//                            Auth: dual — mTLS for CLI/operator, web session
//                            cookie for browser. The unified middleware stamps
//                            context with the appropriate identity.
//                            Route is built entirely from auth context.
//                            Query string carries only since_id.
//
// The Gateway refuses to talk about a bare session id - every routing
// target is tagged at the type level so a web_session_id can never be
// mis-delivered as a cli_session_id (or vice versa). user_id alone is
// not a valid route.
//
// All SSE handlers live on SSEController (sse_controller.go).

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/marshaler"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/response"
	"github.com/zerotrustdata/ztd/internal/services/pubsub"
)

// SSEController handles SSE event poll and stream endpoints.
type SSEController struct {
	cfg       *config.Config
	logger    *slog.Logger
	docStore  *DocumentStoreService
	kvStore   *KVStoreService
	sseStore  *SSEEventService
	pubsub    *GatewayWebSocketHandler
	auth      *AuthService
	responder *response.Writer
	heartbeat time.Duration
}

// SSEControllerDeps groups all dependencies for SSEController.
type SSEControllerDeps struct {
	Cfg       *config.Config
	Logger    *slog.Logger
	DocStore  *DocumentStoreService
	KVStore   *KVStoreService
	SSEStore  *SSEEventService
	Pubsub    *GatewayWebSocketHandler
	Auth      *AuthService
	Responder *response.Writer
	Heartbeat time.Duration
}

// newSSEController creates an SSEController with the given dependencies.
// If Heartbeat is 0, a 30s default is applied.
func newSSEController(d SSEControllerDeps) *SSEController {
	heartbeat := d.Heartbeat
	if heartbeat == 0 {
		heartbeat = 30 * time.Second
	}
	return &SSEController{
		cfg:       d.Cfg,
		logger:    d.Logger,
		docStore:  d.DocStore,
		kvStore:   d.KVStore,
		sseStore:  d.SSEStore,
		pubsub:    d.Pubsub,
		auth:      d.Auth,
		responder: d.Responder,
		heartbeat: heartbeat,
	}
}

// sseAuthError carries an HTTP status and message for SSE authorization failures.
type sseAuthError struct {
	status  int
	message string
}

func (e *sseAuthError) Error() string { return e.message }

// authorizeSSERoute verifies that the authenticated identity (from context) is
// authorized to access the requested SSE routing target. The handler constructs
// the route entirely from auth context (user_id from cert/cookie, session from
// header/cookie); this function enforces ownership as defense-in-depth. Returns
// the pub/sub channel string on success.
func (c *SSEController) authorizeSSERoute(route SSERoute, r *http.Request) (string, error) {
	ctxUserID, _ := r.Context().Value(constants.ContextKeyUserID).(string)
	operatorSessionID, _ := r.Context().Value(constants.ContextKeyOperatorSessionID).(string)
	appID, _ := r.Context().Value(constants.ContextKeyAppID).(string)

	// App certs are not SSE consumers.
	if appID != "" {
		return "", &sseAuthError{status: http.StatusUnauthorized, message: "missing auth identity"}
	}

	// Every SSE consumer must have a user_id from auth context.
	if ctxUserID == "" {
		return "", &sseAuthError{status: http.StatusUnauthorized, message: "missing auth identity"}
	}

	// The route's user_id MUST match the authenticated user. No exceptions.
	if route.UserID != ctxUserID {
		return "", &sseAuthError{status: http.StatusForbidden, message: "user does not match authenticated user"}
	}

	if err := route.validate(); err != nil {
		return "", &sseAuthError{status: http.StatusBadRequest, message: err.Error()}
	}

	// Session ownership verification. The session ID came from context
	// (header for mTLS, cookie for browser), so it is already authenticated
	// as belonging to this user. These checks are defense-in-depth.
	switch {
	case route.CLISessionID != "":
		doc, err := c.docStore.DocGet(r.Context(), marshaler.CollectionName(constants.CollectionCLISessions), route.CLISessionID)
		if err != nil {
			c.logger.Error("SSE: failed to fetch CLI session", string(constants.ConnectionStateError), err, "cli_session_id", route.CLISessionID)
			return "", &sseAuthError{status: http.StatusInternalServerError, message: "failed to verify cli session"}
		}
		if doc == nil {
			return "", &sseAuthError{status: http.StatusForbidden, message: "cli session not found"}
		}
		var cliSess models.CLISession
		b, err := json.Marshal(doc.ForWire())
		if err != nil {
			return "", &sseAuthError{status: http.StatusInternalServerError, message: "failed to verify cli session"}
		}
		if err := json.Unmarshal(b, &cliSess); err != nil {
			return "", &sseAuthError{status: http.StatusInternalServerError, message: "failed to verify cli session"}
		}
		if cliSess.UserID != route.UserID {
			return "", &sseAuthError{status: http.StatusForbidden, message: "user does not own this cli session"}
		}
		// For operator mTLS, also verify operator session ownership. The
		// stamped operator session may be the primary binding or any other
		// session bound to this CLI session (handleCLIAuth admits a bound
		// secondary named by X-ZTD-Operator-Session-ID); cliSessionBindsOperator
		// is the same binding-record check handleCLIAuth uses.
		if operatorSessionID != "" && !cliSessionBindsOperator(&cliSess, operatorSessionID) {
			return "", &sseAuthError{status: http.StatusForbidden, message: "operator session does not own this cli session"}
		}
		return pubsub.SSECLIChannel(route.CLISessionID), nil

	case route.WebSessionID != "":
		if operatorSessionID != "" {
			// Operator mTLS: verify operator owns this web session.
			boundWebSessionID, ok := c.kvStore.KVGet(r.Context(), sessionOperatorBindKey(operatorSessionID))
			if !ok || boundWebSessionID != route.WebSessionID {
				return "", &sseAuthError{status: http.StatusForbidden, message: "operator session does not own this web session"}
			}
		} else {
			// Cookie auth: defense-in-depth — verify route's web_session_id
			// matches the one from auth context (set by cookie validation).
			ctxWebSessionID, _ := r.Context().Value(constants.ContextKeyWebSessionID).(string)
			if ctxWebSessionID != "" && ctxWebSessionID != route.WebSessionID {
				return "", &sseAuthError{status: http.StatusForbidden, message: "web session does not match authenticated session"}
			}
		}
		return pubsub.SSEWebChannel(route.WebSessionID), nil

	default:
		return "", &sseAuthError{status: http.StatusBadRequest, message: "exactly one routing target required"}
	}
}

// buildSSERouteFromContext constructs an SSERoute entirely from auth context.
// No routing or identity ID is ever read from the URL query string — the auth
// middleware stamps ContextKeyUserID (from cert SAN or session cookie) and
// exactly one of ContextKeyCLISessionID (from the X-ZTD-CLI-Session-ID header,
// mTLS only) or ContextKeyWebSessionID (from the session cookie, browser only).
// CLI session takes precedence over web session when both are present, matching
// the established switch behavior. The returned route is not yet validated —
// the caller (or authorizeSSERoute) is responsible for invoking validate().
func buildSSERouteFromContext(ctx context.Context) SSERoute {
	userID, _ := ctx.Value(constants.ContextKeyUserID).(string)
	cliSessionID, _ := ctx.Value(constants.ContextKeyCLISessionID).(string)
	webSessionID, _ := ctx.Value(constants.ContextKeyWebSessionID).(string)

	route := SSERoute{UserID: userID}
	switch {
	case cliSessionID != "":
		route.CLISessionID = cliSessionID
	case webSessionID != "":
		route.WebSessionID = webSessionID
	}
	return route
}

// @Summary		List stored SSE events
// @Description	Returns one page of stored SSE events after a given ID, for backfill; live delivery uses /api/v1/sse/stream. Dual auth: mTLS for CLI/operator, web session
// @Description	cookie for browser. The route is built entirely from auth context: user_id from
// @Description	cert/cookie, session from header/cookie. The query string carries only since_id and limit.
// @Tags			telemetry
// @Produce		json
// @Param			X-ZTD-CLI-Session-ID	header		string	false	"CLI session ID (mTLS only — user_id derived from cert)"
// @Param			since_id				query		int		false	"Return events after this ID"
// @Param			limit					query		int		false	"Maximum events to return"
// @Success		200						{object}	models.SSEEventsResponse
// @Failure		400						{string}	string	"Bad Request"
// @Failure		403						{string}	string	"Forbidden — unauthorized"
// @Router			/api/v1/sse/events [get]
func (c *SSEController) handleInternalSSEEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		c.responder.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	q := r.URL.Query()
	sinceID, _ := strconv.ParseInt(q.Get("since_id"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))

	// Build route from context only. No routing IDs from URL.
	route := buildSSERouteFromContext(r.Context())

	// Authorization: verify the authenticated identity (from context) has the
	// right to access the requested routing buffer. Without this check, any
	// authenticated client could drain any other client's event buffer.
	_, err := c.authorizeSSERoute(route, r)
	if err != nil {
		if sseErr, ok := err.(*sseAuthError); ok {
			c.responder.Error(w, sseErr.status, sseErr.message)
		} else {
			c.responder.Error(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	rows, err := c.sseStore.SSEEventsListSince(route, sinceID, limit)
	if err != nil {
		c.responder.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	c.responder.JSON(w, http.StatusOK, models.SSEEventsResponse{
		Events: rows,
		Count:  len(rows),
	})
}

// @Summary		Stream SSE events
// @Description	Streams events via Server-Sent Events (text/event-stream). Dual auth: mTLS for
// @Description	CLI/operator, web session cookie for browser. Supports Last-Event-ID header for
// @Description	reconnection. The route is built entirely from auth context: user_id from cert/cookie,
// @Description	session from header/cookie. The query string carries only since_id.
// @Tags			telemetry
// @Produce		text/event-stream
// @Param			X-ZTD-CLI-Session-ID	header		string	false	"CLI session ID (mTLS only — user_id derived from cert)"
// @Param			since_id				query		int		false	"Replay events after this ID"
// @Success		200						{string}	string	"SSE stream"
// @Failure		400						{string}	string	"Bad Request"
// @Failure		403						{string}	string	"Forbidden — unauthorized"
// @Router			/api/v1/sse/stream [get]
func (c *SSEController) handleInternalSSEStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		c.responder.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	q := r.URL.Query()

	// Build route from context only. No routing IDs from URL.
	route := buildSSERouteFromContext(r.Context())

	sinceIDStr := q.Get("since_id")
	sinceIDPresent := sinceIDStr != ""
	lastEventIDHeader := r.Header.Get(constants.HeaderLastEventID)
	if lastEventIDHeader != "" {
		sinceIDStr = lastEventIDHeader
	}
	sinceID, _ := strconv.ParseInt(sinceIDStr, 10, 64)

	// Authorization: verify the authenticated identity (from context) has the
	// right to access the requested routing buffer. Returns the pub/sub channel.
	channel, err := c.authorizeSSERoute(route, r)
	if err != nil {
		if sseErr, ok := err.(*sseAuthError); ok {
			c.responder.Error(w, sseErr.status, sseErr.message)
		} else {
			c.responder.Error(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	operatorSessionID, _ := r.Context().Value(constants.ContextKeyOperatorSessionID).(string)
	webSessionID, _ := r.Context().Value(constants.ContextKeyWebSessionID).(string)
	userID, _ := r.Context().Value(constants.ContextKeyUserID).(string)
	var clientLabel string
	if operatorSessionID != "" {
		clientLabel = "operator_session_id=" + operatorSessionID
	} else if webSessionID != "" {
		clientLabel = "web_session_id=" + webSessionID
	} else if userID != "" {
		clientLabel = "cli_user_id=" + userID
	} else {
		clientLabel = "unknown"
	}

	// Set SSE Headers
	w.Header().Set(constants.HeaderContentType, constants.HeaderValueTextEvent)
	w.Header().Set(constants.HeaderCacheControl, constants.HeaderValueNoCache)
	w.Header().Set(constants.HeaderConnection, constants.HeaderValueKeepAlive)
	w.Header().Set(constants.HeaderXAccelBuffering, "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	// http.Server.WriteTimeout is an absolute deadline measured from the first
	// response write, not an idle-between-writes timeout. Left in place, it
	// closes the connection 30s into the stream regardless of heartbeats,
	// producing the connect→30s→disconnect→3s→reconnect cycle seen in the
	// operator log. Clear it for this long-lived stream; the heartbeat loop
	// below handles liveness and r.Context() handles cancellation.
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		c.logger.Warn("SSE Stream: failed to clear WriteTimeout, stream may be killed by server deadline", "channel", channel, "error", err)
	}

	// Subscribe to real-time events FIRST to avoid missing any during replay.
	// The pub/sub payload is a models.SSEPublishedEvent JSON envelope carrying
	// the DB row ID, which the live loop uses to deduplicate against replayed
	// rows (R1) and to emit an `id:` field (R2).
	//
	// R5: back-pressure is enforced via dropOldestBuf, unifying semantics with
	// the WebSocket path. Drop-oldest is the correct policy for an append-only
	// event log: the consumer can recover evicted events via DB replay on
	// reconnect, while a dropped newest event would be lost until the next
	// reconnect. The request context ends the receiving loop. Leave the buffer
	// open because an already-selected callback may finish after unregister.
	buf := newDropOldestBuf(100)
	unregister := c.pubsub.RegisterHandler(channel, func(ch string, data []byte) {
		if ok, dropped := buf.send(data); !ok {
			c.logger.Error("SSE Stream: back-pressure enqueue failed after drop-oldest",
				"channel", channel, "dropped_total", dropped)
		} else if dropped > 0 {
			c.logger.Warn("SSE Stream: back-pressure dropped oldest queued event",
				"channel", channel, "dropped_total", dropped)
		}
	})
	defer unregister()

	// HTTP 200 lets clients start producing events immediately. Register the
	// listener and clear the write deadline before exposing that boundary:
	// ephemeral deployment announcements cannot be recovered through replay.
	// Flush now rather than waiting for the first event or 30s heartbeat.
	flusher.Flush()

	// lastEmittedID is the highest event ID the replay emitted on this
	// connection. It is maintained inside the single HTTP-handler goroutine, so
	// no mutex is required. Any event appended between RegisterHandler and
	// SSEEventsListSince (the duplicate-delivery race window, F1) was already
	// replayed, so the live path suppresses IDs at or below it. Live events do
	// not advance it: row IDs are assigned in commit order but published after
	// commit by concurrent writers, so live events arrive out of ID order and
	// a lower ID after a higher one is a new event, not a duplicate.
	var lastEmittedID int64

	// Replay from DB. A replay is requested when:
	//   - Last-Event-ID header is present (replay from that cursor; 0 means
	//     "from the beginning"),
	//   - since_id query param is present and > 0,
	//   - Neither is present (fresh connection; replay full backlog from ID 0).
	// When since_id=0 is explicitly set without Last-Event-ID, skip replay —
	// the client is signaling it wants live events only.
	replayRequested := lastEventIDHeader != "" || !sinceIDPresent || sinceID > 0
	if replayRequested {
		rows, err := c.sseStore.SSEEventsListSince(route, sinceID, 1000)
		if err != nil {
			// R4: log replay failures and emit an error sentinel so the client
			// has a signal that a gap occurred, rather than silently swallowing
			// the error and proceeding to the live loop.
			c.logger.Error("SSE Stream: replay failed", "channel", channel, "error", err)
			sentinel, mErr := json.Marshal(models.SSEErrorEvent{Type: "error", Reason: "replay_failed"})
			if mErr != nil {
				c.logger.Error("SSE Stream: failed to marshal replay-error sentinel", "channel", channel, "error", mErr)
				return
			}
			if _, wErr := fmt.Fprintf(w, "data: %s\n\n", sentinel); wErr != nil {
				c.logger.Info("SSE Stream: write error during replay sentinel, disconnecting", "channel", channel, "error", wErr)
				return
			}
			flusher.Flush()
		} else {
			for _, row := range rows {
				// R3: capture write errors and exit immediately on a broken
				// pipe to avoid goroutine leaks on half-open connections.
				if _, wErr := fmt.Fprintf(w, "id: %d\ndata: %s\n\n", row.ID, row.Payload); wErr != nil {
					c.logger.Info("SSE Stream: write error during replay, disconnecting", "channel", channel, "error", wErr)
					return
				}
				lastEmittedID = row.ID
			}
			flusher.Flush()
			// R6: signal replay truncation. If the replay hit the limit, the
			// client received only the first `limit` rows and there may be
			// more backlog. Emit a sentinel so the client can decide to
			// reconnect with a higher since_id.
			if len(rows) == 1000 {
				truncSentinel, mErr := json.Marshal(models.SSETruncationEvent{Type: "truncated", SinceID: lastEmittedID, Limit: 1000})
				if mErr != nil {
					c.logger.Error("SSE Stream: failed to marshal truncation sentinel", "channel", channel, "error", mErr)
					return
				}
				if _, wErr := fmt.Fprintf(w, "data: %s\n\n", truncSentinel); wErr != nil {
					c.logger.Info("SSE Stream: write error during truncation sentinel, disconnecting", "channel", channel, "error", wErr)
					return
				}
				flusher.Flush()
			}
		}
	}

	// Stream from pubsub
	ctx := r.Context()
	ticker := time.NewTicker(c.heartbeat)
	defer ticker.Stop()

	c.logger.Info("SSE Stream: client connected", "channel", channel, "client", clientLabel)

	for {
		select {
		case <-ctx.Done():
			c.logger.Info("SSE Stream: client disconnected", "channel", channel)
			return
		case <-ticker.C:
			// R3: exit on heartbeat write error (broken pipe / half-open).
			if _, wErr := fmt.Fprintf(w, ": heartbeat\n\n"); wErr != nil {
				c.logger.Info("SSE Stream: write error on heartbeat, disconnecting", "channel", channel, "error", wErr)
				return
			}
			flusher.Flush()
		case raw := <-buf.recv():
			// R1/R2: unmarshal the SSEPublishedEvent envelope, dedup against
			// lastEmittedID, and emit `id:` + `data:` (no `event:` field — R14).
			var pubEvent models.SSEPublishedEvent
			if err := json.Unmarshal(raw, &pubEvent); err != nil {
				c.logger.Warn("SSE Stream: failed to unmarshal published event", "channel", channel, "error", err)
				continue
			}
			if pubEvent.ID > 0 && pubEvent.ID <= lastEmittedID {
				// Already emitted via replay; suppress the duplicate.
				c.logger.Debug("SSE Stream: live event suppressed as replay duplicate",
					"channel", channel, "event_id", pubEvent.ID, "last_emitted_id", lastEmittedID)
				continue
			}
			// Ephemeral events are never persisted, so they carry ID 0: they
			// are emitted without an `id:` field (it would rewind the client's
			// Last-Event-ID cursor).
			frame := fmt.Sprintf("data: %s\n\n", string(pubEvent.Payload))
			if pubEvent.ID > 0 {
				frame = fmt.Sprintf("id: %d\n%s", pubEvent.ID, frame)
			}
			if _, wErr := fmt.Fprint(w, frame); wErr != nil {
				c.logger.Info("SSE Stream: write error on live event, disconnecting", "channel", channel, "error", wErr)
				return
			}
			flusher.Flush()
		}
	}
}
