package fcm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"time"

	"github.com/spacemonkeygo/errors"
	"github.com/spacemonkeygo/spacelog"
)

const (
	endpoint                = "https://fcm.googleapis.com/fcm/send"
	defaultMinBackoff       = 1 * time.Second
	defaultMaxBackoff       = 10 * time.Second
	defaultMaxRetryAttempts = 5
)

var (
	nowHook   = time.Now   // for testing
	sleepHook = time.Sleep // for testing
	logger    = spacelog.GetLogger()
	Error     = errors.NewClass("fcm")
)

type FcmClient interface {
	Send(ctx context.Context, m HttpMessage) error
}

type HttpClient interface {
	Do(req *http.Request) (resp *http.Response, err error)
}

type Store interface {
	// Called when a registration token should be updated
	Update(ctx context.Context, oldRegId, newRegId string) error
	// Called when a registration token should be removed because the application
	// was removed from the device, or an unrecoverable error occurred
	Delete(ctx context.Context, regId string) error
}

type Client struct {
	apiKey  string
	client  HttpClient
	store   Store
	options *ClientOptions
}

type ClientOptions struct {
	MinBackoff       time.Duration
	MaxBackoff       time.Duration
	MaxRetryAttempts int
}

func DefaultClientOptions() *ClientOptions {
	return &ClientOptions{
		MinBackoff:       defaultMinBackoff,
		MaxBackoff:       defaultMaxBackoff,
		MaxRetryAttempts: defaultMaxRetryAttempts,
	}
}

func NewDefaultClient(apiKey string, store Store) *Client {
	return NewFcmClient(apiKey, http.DefaultClient, store, nil)
}

// When options == nil, default values are used
func NewFcmClient(apiKey string, client HttpClient, store Store,
	options *ClientOptions) *Client {
	if options == nil {
		options = DefaultClientOptions()
	}

	return &Client{
		apiKey:  apiKey,
		client:  client,
		store:   store,
		options: options,
	}
}

type response struct {
	httpResp   *HttpResponse
	statusCode int
	// nil when no retryAfter is set
	retryAfter *time.Duration
}

func NewHttpMessage(registrationIds []string, data Data, notif *Notification) *HttpMessage {
	return &HttpMessage{
		RegistrationIds: registrationIds,
		Data:            data,
		Notification:    notif,
	}
}

// Sends HttpMessages, retries with exponential backoff, processes replies to the Store
func (c *Client) Send(ctx context.Context, m HttpMessage) (hr *HttpResponse,
	err error) {
	registrationIds := m.RegistrationIds

	var resp *response
	// Backoff to use when there is no retryAfter header
	currentBackoff := c.options.MinBackoff
Loop:
	for attempts := 1; ; {
		resp, err = c.send(&m)
		if err != nil {
			return nil, Error.Wrap(fmt.Errorf("error sending request to FCM HTTP"+
				" server: %v", err))
		}

		// TODO also process 500's
		switch resp.statusCode {
		case http.StatusBadRequest:
			return nil, fmt.Errorf("Bad Request, invalid json")
		case http.StatusUnauthorized:
			return nil, fmt.Errorf("Unauthorized")
		case http.StatusOK:
			toRetryRegIds, err := c.processResp(ctx, registrationIds, resp)
			if err != nil {
				return nil, err
			}
			if toRetryRegIds != nil {
				m.RegistrationIds = toRetryRegIds

				backoff := c.calcBackoff(resp.retryAfter, currentBackoff)
				if resp.retryAfter == nil {
					currentBackoff = backoff
				}

				logger.Noticef("RegistrationIds: %v (attempt %d of %d)", toRetryRegIds,
					attempts, c.options.MaxRetryAttempts)
				attempts += 1
				// TODO send in context with cancelation
				sleepHook(backoff)
				continue
			} else {
				break Loop
			}
		}
		if attempts >= c.options.MaxRetryAttempts+1 {
			return nil, fmt.Errorf("Exhausted retry attempts")
		}
	}
	if resp == nil {
		return nil, fmt.Errorf("No response")
	}
	return resp.httpResp, nil
}

// uses retryAfter if available, otherwise backs off to max backoff
func (c *Client) calcBackoff(retryAfter *time.Duration,
	currentBackoff time.Duration) (backoff time.Duration) {
	if retryAfter != nil {
		if *retryAfter < c.options.MinBackoff {
			return c.options.MinBackoff
		}
		return *retryAfter
	}
	// TODO somehow use the first backoff value
	backoff = currentBackoff * 2
	if backoff > c.options.MaxBackoff {
		return c.options.MaxBackoff
	} else if backoff < c.options.MinBackoff {
		return c.options.MinBackoff
	}
	return backoff
}

func (c *Client) processResp(ctx context.Context, registrationIds []string,
	resp *response) (toRetry []string,
	err error) {
	httpResp := resp.httpResp
	// All successful
	if httpResp.Failure == 0 && httpResp.CanonicalIds == 0 {
		return nil, nil
	}

	failureReasons := ""

	for i, result := range httpResp.Results {
		regId := registrationIds[i]
		// Check for canonical ID
		if result.MessageId != "" {
			if result.RegistrationId != "" {
				logger.Debugf("update: %s to %s", regId, result.RegistrationId)
				err = c.store.Update(ctx, regId, result.RegistrationId)
				if err != nil {
					return nil, err
				}
			}
			continue
		}

		if isRetry(result.Error) {
			toRetry = append(toRetry, regId)
		} else {
			logger.Noticef("RegistrationId: %s error: %s", regId, result.Error)
			failureReasons += fmt.Sprintf("%d: %s\n", i, result.Error)
			// Probably an unrecoverable error or NotRegistered
			logger.Debugf("Deleting: %v", regId)
			err = c.store.Delete(ctx, regId)
			if err != nil {
				return nil, err
			}
		}
	}

	fmt.Println(httpResp)

	return toRetry, nil
}

func (c *Client) send(message *HttpMessage) (*response, error) {
	logger.Debugf("message: %v", message)

	data, err := json.Marshal(message)
	if err != nil {
		return nil, Error.Wrap(err)
	}
	logger.Debugf("send json %s", data)

	req, err := http.NewRequest("POST", endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, Error.Wrap(err)
	}
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Authorization", fmt.Sprintf("key=%s", c.apiKey))
	logger.Debugf("request: %v", req)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	httpResp := &HttpResponse{}
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	logger.Debugf("response: %v", string(body))
	err = json.Unmarshal(body, &httpResp)
	if err != nil {
		return nil, err
	}

	retryAfter, err := parseRetryAfter(resp.Header.Get("Retry-After"))
	if err != nil {
		return nil, err
	}

	return &response{
		httpResp:   httpResp,
		statusCode: resp.StatusCode,
		retryAfter: retryAfter,
	}, nil
}

func isRetry(err string) bool {
	return err == "Unavailable" || err == "InternalServerError"
}

// Two formats:
// Retry-After: Fri, 31 Dec 1999 23:59:59 GMT
// Retry-After: 120
func parseRetryAfter(date string) (*time.Duration, error) {
	// No header set
	if date == "" {
		return nil, nil
	}

	d, err := time.ParseDuration(date + "s")
	if err != nil {
		t, err := http.ParseTime(date)
		if t.Before(nowHook()) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		d = t.Sub(nowHook())
	}
	return &d, nil
}
