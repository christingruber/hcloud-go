package hcloud // import "hetzner.cloud/hcloud"

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ErrorCode represents an error code returned from the API.
type ErrorCode string

const (
	ErrorCodeServiceError ErrorCode = "service_error" // Generic service error
	ErrorCodeUnknownError           = "unknown_error" // Unknown error
)

// Error is an error returned from the API.
type Error struct {
	Code    ErrorCode
	Message string
}

func (e Error) Error() string {
	return fmt.Sprintf("%s (%s)", e.Message, e.Code)
}

// Client is a client for the Hetzner Cloud API.
type Client struct {
	endpoint   string
	token      string
	httpClient *http.Client

	Action ActionClient
	Server ServerClient
	SSHKey SSHKeyClient
}

// A ClientOption is used to configure a Client.
type ClientOption func(*Client)

// WithEndpoint configures a Client to use the specified API endpoint.
func WithEndpoint(endpoint string) ClientOption {
	return func(client *Client) {
		client.endpoint = strings.TrimRight(endpoint, "/")
	}
}

// WithToken configures a Client to use the specified token for authentication.
func WithToken(token string) ClientOption {
	return func(client *Client) {
		client.token = token
	}
}

// NewClient creates a new client.
func NewClient(options ...ClientOption) *Client {
	client := &Client{
		httpClient: &http.Client{},
	}

	for _, option := range options {
		option(client)
	}

	client.Action = ActionClient{client: client}
	client.Server = ServerClient{client: client}
	client.SSHKey = SSHKeyClient{client: client}

	return client
}

// NewRequest creates an HTTP request against the API. The returned request
// is assigned with ctx and has all necessary headers set (auth, user agent, etc.).
func (c *Client) NewRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	url := c.endpoint + path
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "hcloud-go/1.0.0")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.token))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req = req.WithContext(ctx)
	return req, nil
}

// Do performs an HTTP request against the API.
func (c *Client) Do(r *http.Request, v interface{}) (*Response, error) {
	resp, err := c.httpClient.Do(r)
	if err != nil {
		return nil, err
	}
	response := &Response{Response: resp}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return response, err
	}
	resp.Body = ioutil.NopCloser(bytes.NewReader(body))

	if err := response.readMeta(body); err != nil {
		return response, fmt.Errorf("hcloud: error reading response meta data: %s", err)
	}

	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusUnprocessableEntity,
		http.StatusInternalServerError:
		if err := errorFromResponse(resp, body); err != nil {
			return response, err
		}
		return response, fmt.Errorf("hcloud: server responded with status code %d",
			resp.StatusCode)
	default:
		break
	}

	if v != nil {
		if w, ok := v.(io.Writer); ok {
			_, err = io.Copy(w, bytes.NewReader(body))
		} else {
			err = json.Unmarshal(body, v)
		}
	}

	return response, err
}

func errorFromResponse(resp *http.Response, body []byte) error {
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		return nil
	}

	var apiError struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &apiError); err != nil {
		return nil
	}
	if apiError.Error.Code == "" && apiError.Error.Message == "" {
		return nil
	}
	return Error{
		Code:    ErrorCode(apiError.Error.Code),
		Message: apiError.Error.Message,
	}
}

// Response represents a response from the API. It embeds http.Response.
type Response struct {
	*http.Response
	Meta ResponseMeta
}

// ReadBody reads and returns the response's body. After reading the response's body
// is recovered so it can be read again.
//
// TODO(thcyron): Does this method really need to be exported?
func (r *Response) ReadBody() ([]byte, error) {
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	r.Body = ioutil.NopCloser(bytes.NewReader(body))
	return body, err
}

func (r *Response) readMeta(body []byte) error {
	if h := r.Header.Get("RateLimit-Limit"); h != "" {
		r.Meta.Ratelimit.Limit, _ = strconv.Atoi(h)
	}
	if h := r.Header.Get("RateLimit-Remaining"); h != "" {
		r.Meta.Ratelimit.Remaining, _ = strconv.Atoi(h)
	}
	if h := r.Header.Get("RateLimit-Reset"); h != "" {
		if ts, err := strconv.ParseInt(h, 10, 64); err == nil {
			r.Meta.Ratelimit.Reset = time.Unix(ts, 0)
		}
	}

	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var m struct {
			Meta struct {
				Pagination ResponseMetaPagination `json:"pagination"`
			} `json:"meta"`
		}
		if err := json.Unmarshal(body, &m); err != nil {
			return err
		}
		r.Meta.Pagination = &m.Meta.Pagination
	}

	return nil
}

// ResponseMeta represents meta information included in an API response.
type ResponseMeta struct {
	Pagination *ResponseMetaPagination
	Ratelimit  ResponseMetaRatelimit
}

// ResponseMetaPagination represents pagination meta data.
type ResponseMetaPagination struct {
	Page         int
	PerPage      int
	PreviousPage int
	NextPage     int
	LastPage     int
	TotalEntries int
}

// UnmarshalJSON implements json.Unmarshaler.
func (p *ResponseMetaPagination) UnmarshalJSON(data []byte) error {
	var v struct {
		Page         int `json:"page"`
		PerPage      int `json:"per_page"`
		PreviousPage int `json:"previous_page"`
		NextPage     int `json:"next_page"`
		LastPage     int `json:"last_page"`
		TotalEntries int `json:"total_entries"`
	}

	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}

	p.Page = v.Page
	p.PerPage = v.PerPage
	p.PreviousPage = v.PreviousPage
	p.NextPage = v.NextPage
	p.LastPage = v.LastPage
	p.TotalEntries = v.TotalEntries

	return nil
}

// ResponseMetaRatelimit represents ratelimit information.
type ResponseMetaRatelimit struct {
	Limit     int
	Remaining int
	Reset     time.Time
}