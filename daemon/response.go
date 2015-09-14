// -*- Mode: Go; indent-tabs-mode: t -*-

/*
 * Copyright (C) 2015 Canonical Ltd
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License version 3 as
 * published by the Free Software Foundation.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 *
 */

package daemon

import (
	"encoding/json"
	"net/http"

	"launchpad.net/snappy/logger"
)

// ResponseType is the response type
type ResponseType string

// “there are three standard return types: Standard return value,
// Background operation, Error”, each returning a JSON object with the
// following “type” field:
const (
	ResponseTypeSync  ResponseType = "sync"
	ResponseTypeAsync              = "async"
	ResponseTypeError              = "error"
)

// Response knows how to render itself, how to handle itself, and how to find itself
type Response interface {
	Render(w http.ResponseWriter) ([]byte, int)
	Handler(w http.ResponseWriter, r *http.Request)
	Self(*Command, *http.Request) Response // has the same arity as ResponseFunc for convenience
}

type resp struct {
	Type     ResponseType `json:"type"`
	Status   int          `json:"status_code"`
	Metadata interface{}  `json:"metadata"`
}

func (r *resp) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]interface{}{
		"type":        r.Type,
		"status":      http.StatusText(r.Status),
		"status_code": r.Status,
		"metadata":    &r.Metadata,
	})
}

func (r *resp) Render(w http.ResponseWriter) (buf []byte, status int) {
	bs, err := r.MarshalJSON()
	if err != nil {
		logger.Noticef("unable to marshal %#v to JSON: %v", *r, err)
		return nil, http.StatusInternalServerError
	}

	return bs, r.Status
}

func (r *resp) Handler(w http.ResponseWriter, _ *http.Request) {
	bs, status := r.Render(w)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(bs)
}

func (r *resp) Self(*Command, *http.Request) Response {
	return r
}

// SyncResponse builds a "sync" response from the given metadata.
func SyncResponse(metadata interface{}) Response {
	return &resp{
		Type:     ResponseTypeSync,
		Status:   http.StatusOK,
		Metadata: metadata,
	}
}

// ErrorResponse builds an "error" response from the given error status.
func ErrorResponse(status int) Response {
	return &resp{
		Type:   ResponseTypeError,
		Status: status,
	}
}

// standard error responses
var (
	NotFound      = ErrorResponse(http.StatusNotFound)
	BadMethod     = ErrorResponse(http.StatusMethodNotAllowed)
	InternalError = ErrorResponse(http.StatusInternalServerError)
)