// Copyright 2018 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"net/http"
	"time"

	"github.com/opentelemetry/opentelemetry-demo-webstore/src/frontend/instr"
	"go.opentelemetry.io/otel/metric/global"
	semconv "go.opentelemetry.io/otel/semconv/v1.10.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

type ctxKeyLog struct{}
type ctxKeyRequestID struct{}

type httpHandler func(w http.ResponseWriter, r *http.Request)

type logHandler struct {
	log  *logrus.Logger
	next http.Handler
}

var (
	meter                 = global.MeterProvider().Meter("frontend")
	httpRequestCounter, _ = meter.SyncInt64().Counter("http.server.request_count")
	httpServerLatency, _  = meter.SyncFloat64().Histogram("http.server.duration")
)

type responseRecorder struct {
	b      int
	status int
	w      http.ResponseWriter
}

func (r *responseRecorder) Header() http.Header { return r.w.Header() }

func (r *responseRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.w.Write(p)
	r.b += n
	return n, err
}

func (r *responseRecorder) WriteHeader(statusCode int) {
	r.status = statusCode
	r.w.WriteHeader(statusCode)
}

func (lh *logHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	requestID, _ := uuid.NewRandom()
	ctx = context.WithValue(ctx, ctxKeyRequestID{}, requestID.String())

	start := time.Now()
	rr := &responseRecorder{w: w}
	log := lh.log.WithFields(logrus.Fields{
		"http.req.path":   r.URL.Path,
		"http.req.method": r.Method,
		"http.req.id":     requestID.String(),
	})
	if v, ok := r.Context().Value(ctxKeySessionID{}).(string); ok {
		log = log.WithField("session", v)
	}
	log.Debug("request started")
	defer func() {
		log.WithFields(logrus.Fields{
			"http.resp.took_ms": int64(time.Since(start) / time.Millisecond),
			"http.resp.status":  rr.status,
			"http.resp.bytes":   rr.b}).Debugf("request complete")
	}()

	ctx = context.WithValue(ctx, ctxKeyLog{}, log)
	r = r.WithContext(ctx)
	lh.next.ServeHTTP(rr, r)
}

func instrumentHandler(fn httpHandler) httpHandler {
	// Add common attributes to the span for each handler
	// session, request, currency, and user

	return func(w http.ResponseWriter, r *http.Request) {
		requestStartTime := time.Now()
		rid := r.Context().Value(ctxKeyRequestID{})
		requestID := ""
		if rid != nil {
			requestID = rid.(string)
		}
		span := trace.SpanFromContext(r.Context())
		span.SetAttributes(
			instr.SessionId.String(sessionID(r)),
			instr.RequestId.String(requestID),
			instr.Currency.String(currentCurrency(r)),
		)

		email := r.FormValue("email")
		if email != "" {
			span.SetAttributes(instr.UserId.String(email))
		}

		fn(w, r)

		attributes := semconv.HTTPServerMetricAttributesFromHTTPRequest("frontend", r)
		elapsedTime := float64(time.Since(requestStartTime)) / float64(time.Millisecond)
		httpRequestCounter.Add(r.Context(), 1, attributes...)
		httpServerLatency.Record(r.Context(), elapsedTime, attributes...)
	}
}

func ensureSessionID(next http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var sessionID string
		c, err := r.Cookie(cookieSessionID)
		if err == http.ErrNoCookie {
			u, _ := uuid.NewRandom()
			sessionID = u.String()
			http.SetCookie(w, &http.Cookie{
				Name:   cookieSessionID,
				Value:  sessionID,
				MaxAge: cookieMaxAge,
			})
		} else if err != nil {
			return
		} else {
			sessionID = c.Value
		}
		ctx := context.WithValue(r.Context(), ctxKeySessionID{}, sessionID)
		r = r.WithContext(ctx)
		next.ServeHTTP(w, r)
	}
}
