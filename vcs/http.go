package vcs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// retryDelay is the pause before the single retry; tests shorten it.
var retryDelay = 2 * time.Second

// Client is a minimal JSON REST client shared by the adapters. It retries a
// request once on a 5xx or 429 response and does nothing more clever.
type Client struct {
	BaseURL string            // e.g. https://api.github.com
	Header  map[string]string // sent with every request (auth, accept, ...)
	HTTP    *http.Client
}

// StatusError is a non-2xx response.
type StatusError struct {
	Method, URL string
	Status      int
	Message     string
}

func (e *StatusError) Error() string {
	msg := fmt.Sprintf("%s %s: HTTP %d", e.Method, e.URL, e.Status)
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg
}

// Do sends a request with an optional JSON body and decodes a JSON response
// into out when out is not nil. path is relative to BaseURL unless it is a
// full URL. It returns the response headers.
func (c *Client) Do(method, path string, body, out any) (http.Header, error) {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return nil, err
		}
	}
	url := path
	if !strings.HasPrefix(path, "https://") && !strings.HasPrefix(path, "http://") {
		url = strings.TrimSuffix(c.BaseURL, "/") + path
	}

	data, header, err := c.send(method, url, payload)
	var se *StatusError
	if errors.As(err, &se) && (se.Status >= 500 || se.Status == http.StatusTooManyRequests) {
		time.Sleep(retryDelay)
		data, header, err = c.send(method, url, payload)
	}
	if err != nil {
		return nil, err
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return nil, fmt.Errorf("%s %s: decode response: %w", method, url, err)
		}
	}
	return header, nil
}

// GetPages GETs path and every following page (Link rel="next"), decoding
// each page into a T and passing it to each, which returns false to stop.
func GetPages[T any](c *Client, path string, each func(page T) bool) error {
	for path != "" {
		var page T
		header, err := c.Do(http.MethodGet, path, nil, &page)
		if err != nil {
			return err
		}
		if !each(page) {
			return nil
		}
		path = nextLink(header.Get("Link"))
	}
	return nil
}

var nextRE = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

func nextLink(link string) string {
	if m := nextRE.FindStringSubmatch(link); m != nil {
		return m[1]
	}
	return ""
}

func (c *Client) send(method, url string, payload []byte) ([]byte, http.Header, error) {
	req, err := http.NewRequest(method, url, bytes.NewReader(payload))
	if err != nil {
		return nil, nil, err
	}
	for k, v := range c.Header {
		req.Header.Set(k, v)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode/100 != 2 {
		var msg struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &msg)
		return nil, nil, &StatusError{Method: method, URL: url, Status: resp.StatusCode, Message: msg.Message}
	}
	return data, resp.Header, nil
}
