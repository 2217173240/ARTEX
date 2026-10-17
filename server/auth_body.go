package server

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"time"
)

const (
	maxAuthBodyBytes    = 4 << 10
	authBodyReadTimeout = 5 * time.Second
)

// These outer gates can reject a credential request before its handler runs.
// Install the same unread-body cleanup only for the three credential routes.
func guardRejectedAuthBody(w http.ResponseWriter, r *http.Request) func() {
	switch r.URL.Path {
	case "/api/auth/init", "/api/auth/login", "/api/auth/change-password":
		return newAuthBody(w, r, authBodyReadTimeout).finish
	default:
		return func() {}
	}
}

// authBody bounds only credential routes, including their early rejection paths.
// Unknown JSON fields remain compatible, but count toward the same byte limit.
type authBody struct {
	w           http.ResponseWriter
	r           *http.Request
	controller  *http.ResponseController
	deadlineErr error
	complete    bool
	connection  string
}

func newAuthBody(w http.ResponseWriter, r *http.Request, timeout time.Duration) *authBody {
	b := &authBody{w: w, r: r, controller: http.NewResponseController(w), connection: w.Header().Get("Connection")}
	b.deadlineErr = b.controller.SetReadDeadline(time.Now().Add(timeout))
	r.Body = http.MaxBytesReader(w, r.Body, maxAuthBodyBytes)
	// Set this before any early writeErr: setting it in finish would be too late
	// once headers are committed. HTTP/2 closes the request stream instead.
	if r.ProtoMajor < 2 {
		w.Header().Set("Connection", "close")
	}
	return b
}

func (b *authBody) finish() {
	if b.complete {
		return
	}
	b.r.Close = true
	if b.r.ProtoMajor < 2 {
		// Closing an unread HTTP/1 body can drain it. Expire the retained read
		// deadline first so server cleanup cannot wait for a slow sender.
		_ = b.controller.SetReadDeadline(time.Now())
	}
	// The HTTP server owns Body.Close; do not perform a draining Close here.
}

func (b *authBody) decode(into any) bool {
	// ResponseRecorder and other non-server writers have no socket deadline.
	// Real net/http HTTP/1 and HTTP/2 writers support it; wrappers must Unwrap.
	if b.deadlineErr != nil && !errors.Is(b.deadlineErr, http.ErrNotSupported) {
		writeErr(b.w, http.StatusServiceUnavailable, "请求读取暂时不可用")
		return false
	}
	var err error
	if b.r.ContentLength > maxAuthBodyBytes {
		err = &http.MaxBytesError{Limit: maxAuthBodyBytes}
	} else {
		decoder := json.NewDecoder(b.r.Body)
		err = decoder.Decode(into)
		if err == nil {
			var extra any
			err = decoder.Decode(&extra)
			if err == io.EOF {
				err = nil
			} else if err == nil {
				err = errors.New("multiple JSON values")
			}
		}
	}
	if err != nil {
		var tooLarge *http.MaxBytesError
		var timeout net.Error
		switch {
		case errors.As(err, &tooLarge):
			writeErr(b.w, http.StatusRequestEntityTooLarge, "请求正文过大")
		case errors.As(err, &timeout) && timeout.Timeout():
			writeErr(b.w, http.StatusRequestTimeout, "请求正文读取超时")
		default:
			writeErr(b.w, http.StatusBadRequest, "请求格式错误")
		}
		return false
	}
	if b.deadlineErr == nil {
		if err := b.controller.SetReadDeadline(time.Time{}); err != nil {
			writeErr(b.w, http.StatusServiceUnavailable, "请求读取暂时不可用")
			return false
		}
	}
	b.complete = true
	if b.r.ProtoMajor < 2 {
		if b.connection == "" {
			b.w.Header().Del("Connection")
		} else {
			b.w.Header().Set("Connection", b.connection)
		}
	}
	return true
}
