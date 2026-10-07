// Package api is the daemon's side of sign-in and enrollment: a device code
// approved in a browser, exchanged for a machine token.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ClientID is the only device-flow client the server accepts.
const ClientID = "hiveworker-daemon"

type Client struct {
	// Server is the web origin, without a trailing slash.
	Server string
	HTTP   *http.Client
}

func New(server string) *Client {
	return &Client{
		Server: strings.TrimRight(server, "/"),
		HTTP:   &http.Client{Timeout: 30 * time.Second},
	}
}

// DeviceCode is what the person needs to approve this daemon.
type DeviceCode struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// Pool is one the person could enroll this machine in.
type Pool struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ChoosePoolError means the person is in several pools and must say which of
// them this machine should serve.
type ChoosePoolError struct{ Pools []Pool }

func (e *ChoosePoolError) Error() string { return "choose a hive" }

// Error is a refusal from the server, in its own words.
type Error struct {
	Status  int
	Code    string
	Message string
	Fields  map[string]string
}

func (e *Error) Error() string {
	message := e.Message
	for _, detail := range e.Fields {
		message += "\n  " + detail
	}
	return message
}

// Machine is what enrolling asks about this computer.
type Machine struct {
	// OrganizationIDs are the pools to serve. Empty lets the server choose,
	// which it can only do for someone in exactly one pool.
	OrganizationIDs []string `json:"organizationIds,omitempty"`
	Name            string   `json:"name"`
	Model           string   `json:"model"`
	OS              string   `json:"os"`
	Arch            string   `json:"arch"`
	Cores           int      `json:"cores"`
	MemoryBytes     int64    `json:"memoryBytes"`
	DaemonVersion   string   `json:"daemonVersion"`
	SpeedScore      float64  `json:"speedScore"`
}

// Refused is a pool the machine asked to serve and may not, with the reason.
type Refused struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Message string `json:"message"`
}

// Enrollment is the server's answer: who this machine now is, and which of
// the pools it asked for it was authorized to serve.
type Enrollment struct {
	MachineID string    `json:"machineId"`
	Token     string    `json:"token"`
	Pools     []Pool    `json:"pools"`
	Refused   []Refused `json:"refused"`
}

// Assignment is what a running daemon should be connected to right now.
type Assignment struct {
	MachineID string `json:"machineId"`
	// TunnelURL is the coordinator's endpoint; each pool is a `pool` query
	// parameter on it.
	TunnelURL string `json:"tunnelUrl"`
	Pools     []Pool `json:"pools"`
}

// ErrUnknownMachine means the machine token is no longer accepted at all.
var ErrUnknownMachine = errors.New("this machine is no longer enrolled; run `hived login`")

func (c *Client) post(ctx context.Context, path, bearer string, body, out any) (int, []byte, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return 0, nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Server+path, bytes.NewReader(encoded))
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	response, err := c.HTTP.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return 0, nil, err
	}
	if response.StatusCode < 300 && out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return response.StatusCode, raw, fmt.Errorf("unexpected response from %s: %w", path, err)
		}
	}
	return response.StatusCode, raw, nil
}

// StartDevice begins sign-in. The returned URL is absolute.
func (c *Client) StartDevice(ctx context.Context) (DeviceCode, error) {
	var code DeviceCode
	status, raw, err := c.post(ctx, "/api/auth/device/code", "", map[string]string{"client_id": ClientID}, &code)
	if err != nil {
		return DeviceCode{}, err
	}
	if status >= 300 || code.DeviceCode == "" {
		return DeviceCode{}, serverError(status, raw)
	}
	base, err := url.Parse(c.Server)
	if err != nil {
		return DeviceCode{}, err
	}
	link, err := base.Parse(code.VerificationURIComplete)
	if err != nil {
		return DeviceCode{}, err
	}
	// Never open a link that leaves the server we were asked to sign in to.
	if link.Host != base.Host {
		return DeviceCode{}, fmt.Errorf("server sent a sign-in link for another site (%s)", link.Host)
	}
	code.VerificationURIComplete = link.String()
	if code.Interval < 1 {
		code.Interval = 5
	}
	return code, nil
}

// ErrDenied and ErrExpired end a device sign-in without a token.
var (
	ErrDenied  = errors.New("sign-in was denied in the browser")
	ErrExpired = errors.New("the code expired before it was approved")
)

// AwaitToken polls until the person approves, denies, or the code expires.
func (c *Client) AwaitToken(ctx context.Context, code DeviceCode) (string, error) {
	interval := time.Duration(code.Interval) * time.Second
	deadline := time.Now().Add(time.Duration(code.ExpiresIn) * time.Second)
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(interval):
		}
		if time.Now().After(deadline) {
			return "", ErrExpired
		}
		var granted struct {
			AccessToken string `json:"access_token"`
		}
		status, raw, err := c.post(ctx, "/api/auth/device/token", "", map[string]string{
			"grant_type":  "urn:ietf:params:oauth:grant-type:device_code",
			"device_code": code.DeviceCode,
			"client_id":   ClientID,
		}, &granted)
		if err != nil {
			// A dropped connection while waiting is not a refusal. The
			// person may be mid-approval; keep asking until the code expires.
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			if failures++; failures > 5 {
				return "", err
			}
			continue
		}
		failures = 0
		if status < 300 && granted.AccessToken != "" {
			return granted.AccessToken, nil
		}
		var refusal struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &refusal)
		switch refusal.Error {
		case "authorization_pending":
		case "slow_down":
			interval += 5 * time.Second
		case "access_denied":
			return "", ErrDenied
		case "expired_token":
			return "", ErrExpired
		default:
			return "", serverError(status, raw)
		}
	}
}

// Enroll trades the sign-in token for a machine token. The server ends the
// sign-in session as it answers, so the token is good for exactly this call.
func (c *Client) Enroll(ctx context.Context, token string, machine Machine) (Enrollment, error) {
	var enrollment Enrollment
	status, raw, err := c.post(ctx, "/api/machines", token, machine, &enrollment)
	if err != nil {
		return Enrollment{}, err
	}
	if status == http.StatusConflict {
		var choice struct {
			Code  string `json:"code"`
			Pools []Pool `json:"pools"`
		}
		if json.Unmarshal(raw, &choice) == nil && choice.Code == "CHOOSE_POOL" {
			return Enrollment{}, &ChoosePoolError{Pools: choice.Pools}
		}
	}
	if status >= 300 {
		return Enrollment{}, serverError(status, raw)
	}
	if enrollment.Token == "" || len(enrollment.Pools) == 0 {
		return Enrollment{}, errors.New("server did not return a machine token")
	}
	return enrollment, nil
}

func serverError(status int, raw []byte) error {
	var body struct {
		Error   string            `json:"error"`
		Message string            `json:"message"`
		Code    string            `json:"code"`
		Fields  map[string]string `json:"fields"`
		// Better Auth's OAuth-style errors.
		Description string `json:"error_description"`
	}
	_ = json.Unmarshal(raw, &body)
	message := body.Description
	if message == "" {
		message = body.Message
	}
	if message == "" {
		message = body.Error
	}
	if message == "" {
		message = fmt.Sprintf("the server answered %d", status)
	}
	return &Error{Status: status, Code: body.Code, Message: message, Fields: body.Fields}
}

// Me asks which pools this machine serves. A daemon polls it: that is how it
// learns it was added to a pool from the dashboard, or dropped from one.
func (c *Client) Me(ctx context.Context, machineToken string) (Assignment, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Server+"/api/machines/me", nil)
	if err != nil {
		return Assignment{}, err
	}
	request.Header.Set("Authorization", "Bearer "+machineToken)
	response, err := c.HTTP.Do(request)
	if err != nil {
		return Assignment{}, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return Assignment{}, err
	}
	if response.StatusCode == http.StatusUnauthorized {
		return Assignment{}, ErrUnknownMachine
	}
	if response.StatusCode >= 300 {
		return Assignment{}, serverError(response.StatusCode, raw)
	}
	var assignment Assignment
	if err := json.Unmarshal(raw, &assignment); err != nil {
		return Assignment{}, fmt.Errorf("unexpected response from /api/machines/me: %w", err)
	}
	if assignment.TunnelURL == "" {
		return Assignment{}, errors.New("server did not say where the coordinator is")
	}
	return assignment, nil
}
