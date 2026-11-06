package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/eduard-alloy/plumbix/internal/client"
)

// "plumbix change wait" (WU-082): block until the running iteration gets its
// verdict or the change reaches a final state, the way the MCP tool
// wait_for_verdict does, and exit by the state so a script can branch on it.

// waitDefault is how long change wait waits when --timeout is not given: the
// length of Plumbix's own external review step.
const waitDefault = 30 * time.Minute

// waitResult is what change wait prints.
type waitResult struct {
	ChangeID  string          `json:"change_id"`
	Iteration int             `json:"iteration"`
	State     string          `json:"state"`
	Outcome   string          `json:"outcome,omitempty"`
	Criteria  []waitCriterion `json:"criteria,omitempty"`
	TimedOut  bool            `json:"timed_out,omitempty"`
}

type waitCriterion struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// waitExit maps the change's state after the wait to the exit code: 0 when
// it may merge or has merged, 1 when the author has work to do or the change
// is closed, 2 when a person must look, 3 when the wait ran out.
func waitExit(state string, timedOut bool) int {
	if timedOut {
		return 3
	}
	switch state {
	case "approved", "merged":
		return 0
	case "needs_human", "escalated":
		return 2
	}
	return 1
}

type waitChange struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

type waitRun struct {
	Iteration int    `json:"iteration"`
	Status    string `json:"status"`
	Outcome   string `json:"outcome"`
	Result    *struct {
		Criteria []waitCriterion `json:"criteria"`
	} `json:"result"`
}

// wait implements change wait. It finds the running iteration (looking again
// for a few seconds when the change says running but has no run yet), follows
// the change's events until that iteration's verdict, a final state or an
// errored run, then prints the change as it stands.
func (c *clientCmd) wait(ctx context.Context, id string, timeout time.Duration) int {
	cl, ok := c.connect()
	if !ok {
		return 1
	}
	get := func(path string, into any) error {
		resp, err := cl.Do(ctx, http.MethodGet, path, nil)
		if err != nil {
			return err
		}
		if !resp.OK() {
			return resp.Error()
		}
		return json.Unmarshal(resp.Body, into)
	}
	changePath := "/api/v1/changes/" + url.PathEscape(id)
	var ch waitChange
	if err := get(changePath, &ch); err != nil {
		return c.waitFail(err)
	}
	runs := func() ([]waitRun, error) {
		var out struct {
			Runs []waitRun `json:"runs"`
		}
		err := get(changePath+"/runs", &out)
		return out.Runs, err
	}
	running := func(rs []waitRun) int {
		n := 0
		for _, r := range rs {
			if r.Status == "running" {
				n = r.Iteration
			}
		}
		return n
	}
	rs, err := runs()
	if err != nil {
		return c.waitFail(err)
	}
	it := running(rs)
	for tries := 0; it == 0 && ch.State == "running" && tries < 10; tries++ {
		select {
		case <-ctx.Done():
			return c.waitFail(ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
		if rs, err = runs(); err != nil {
			return c.waitFail(err)
		}
		it = running(rs)
		if err := get(changePath, &ch); err != nil {
			return c.waitFail(err)
		}
	}
	timedOut := false
	if it > 0 {
		waitCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		stop := errors.New("done")
		err := cl.Stream(waitCtx, "/api/v1/events?change_id="+url.QueryEscape(id), 0, func(f client.Frame) error {
			var e struct {
				Type    string `json:"type"`
				Payload struct {
					Iteration int    `json:"iteration"`
					Status    string `json:"status"`
					To        string `json:"to"`
				} `json:"payload"`
			}
			if json.Unmarshal(f.Data, &e) != nil {
				return nil
			}
			switch e.Type {
			case "verdict.issued":
				if e.Payload.Iteration == it {
					return stop
				}
			case "change.state_changed":
				if e.Payload.To == "merged" || e.Payload.To == "closed" {
					return stop
				}
			case "run.finished":
				if e.Payload.Iteration == it && e.Payload.Status == "errored" {
					return stop
				}
			}
			return nil
		})
		switch {
		case errors.Is(err, stop):
		case err == nil && waitCtx.Err() != nil && ctx.Err() == nil:
			timedOut = true
		case err != nil:
			return c.waitFail(err)
		case ctx.Err() != nil:
			return c.waitFail(ctx.Err())
		}
		// The verdict event can precede the state by a moment; read the
		// change until it has left running, briefly.
		for tries := 0; ; tries++ {
			if err := get(changePath, &ch); err != nil {
				return c.waitFail(err)
			}
			if timedOut || ch.State != "running" || tries >= 20 {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if rs, err = runs(); err != nil {
			return c.waitFail(err)
		}
	}
	out := waitResult{ChangeID: ch.ID, Iteration: it, State: ch.State, TimedOut: timedOut}
	if out.ChangeID == "" {
		out.ChangeID = id
	}
	for _, r := range rs {
		if (it == 0 || r.Iteration == it) && r.Status != "running" {
			if it == 0 {
				out.Iteration = r.Iteration
			}
			out.Outcome = r.Outcome
			if r.Result != nil {
				out.Criteria = r.Result.Criteria
			}
		}
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Fprintf(c.stdout, "%s\n", b)
	return waitExit(ch.State, timedOut)
}

func (c *clientCmd) waitFail(err error) int {
	var apiErr client.APIError
	if errors.As(err, &apiErr) {
		c.printAPIError(apiErr)
		return 1
	}
	fmt.Fprintf(c.stderr, "plumbix: %v\n", err)
	return 1
}
