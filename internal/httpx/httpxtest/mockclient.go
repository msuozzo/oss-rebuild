// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package httpxtest

import (
	"net/http"
)

type Call struct {
	Method   string
	URL      string
	Response *http.Response
	Error    error
}

type MockClient struct {
	Calls        []Call
	URLValidator func(expected, actual string)
	callCount    int
}

func (m *MockClient) Do(req *http.Request) (*http.Response, error) {
	if m.callCount >= len(m.Calls) {
		panic("unexpected request")
	}
	call := m.Calls[m.callCount]
	m.callCount++

	if m.URLValidator != nil {
		if call.Method != "" {
			m.URLValidator(call.Method+" "+call.URL, req.Method+" "+req.URL.String())
		} else {
			m.URLValidator(call.URL, req.URL.String())
		}
	}

	return call.Response, call.Error
}

func (m *MockClient) CallCount() int {
	return m.callCount
}
