package client

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// maxFrame bounds one line of a Server-Sent Events frame.
const maxFrame = 4 << 20

// Frame is one Server-Sent Events frame: the id and event name the server
// set, and the data lines joined with newlines.
type Frame struct {
	ID    int64
	Event string
	Data  []byte
}

// Error makes APIError usable as an error.
func (e APIError) Error() string { return e.Code + ": " + e.Message }

// Stream opens a Server-Sent Events endpoint and calls fn for every frame
// until ctx ends, the server closes the stream, or fn returns an error,
// which Stream returns as is. lastID above zero is sent as Last-Event-ID.
// A non-2xx response is returned as an APIError. A cancelled ctx is a clean
// stop: Stream returns nil.
func (c *Client) Stream(ctx context.Context, path string, lastID int64, fn func(Frame) error) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "text/event-stream")
	if lastID > 0 {
		req.Header.Set("Last-Event-ID", strconv.FormatInt(lastID, 10))
	}
	resp, err := c.stream.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return c.connectError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return Response{Status: resp.StatusCode, Body: data}.Error()
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), maxFrame)
	var f Frame
	var data []string
	for sc.Scan() {
		line := strings.TrimSuffix(sc.Text(), "\r")
		switch {
		case line == "":
			if len(data) > 0 { // a block of comments alone is not an event
				f.Data = []byte(strings.Join(data, "\n"))
				if err := fn(f); err != nil {
					return err
				}
			}
			f, data = Frame{}, nil
		case strings.HasPrefix(line, ":"):
		default:
			field, value, _ := strings.Cut(line, ":")
			value = strings.TrimPrefix(value, " ")
			switch field {
			case "id":
				f.ID, _ = strconv.ParseInt(value, 10, 64)
			case "event":
				f.Event = value
			case "data":
				data = append(data, value)
			}
		}
	}
	if ctx.Err() != nil {
		return nil
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("read event stream: %w", err)
	}
	return nil
}
