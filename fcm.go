package fcm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"time"

	"github.com/spacemonkeygo/errors"
	"github.com/spacemonkeygo/spacelog"
)

var (
	nowHook   = time.Now   // for testing
	sleepHook = time.Sleep // for testing
	logger    = spacelog.GetLogger()
	Error     = errors.NewClass("fcm")
)

type FcmClient interface {
	SendHttp(m HttpMessage) error
}

type HttpClient interface {
	Do(req *http.Request) (resp *http.Response, err error)
}

type Store interface {
	Update(oldRegId, newRegId string) error
	Delete(regId string) error
}

type Client struct {
	endpoint         string
	apiKey           string
	client           HttpClient
	minBackoff       time.Duration
	maxBackoff       time.Duration
	maxRetryAttempts int
	store            Store
}

// TODO make options
func NewFcmClient(endpoint, apiKey string, client HttpClient, store Store,
	minBackoff, maxBackoff time.Duration, retryAttempts int) *Client {
	return &Client{
		endpoint:         endpoint,
		apiKey:           apiKey,
		client:           client,
		store:            store,
		maxRetryAttempts: retryAttempts,
		minBackoff:       minBackoff,
		maxBackoff:       maxBackoff,
	}
}

type response struct {
	httpResp   *HttpResponse
	statusCode int
	retryAfter time.Duration
}

func NewHttpMessage(registrationIds []string, data Data, notif *Notification) *HttpMessage {
	return &HttpMessage{
		RegistrationIds: registrationIds,
		Data:            data,
		Notification:    notif,
	}
}

// Sends HttpMessages, retries with exponential backoff, processes replies to the Store
func (c *Client) SendHttp(m HttpMessage) error {
	registrationIds := m.RegistrationIds

	currentAttempts := 1
	currentBackoff := c.minBackoff
Loop:
	for currentAttempts <= c.maxRetryAttempts {
		resp, err := c.send(&m)
		if err != nil {
			return Error.Wrap(fmt.Errorf("error sending request to FCM HTTP server: %v", err))
		}

		// TODO also process 500's
		switch resp.statusCode {
		case http.StatusBadRequest:
			return fmt.Errorf("Bad Request, invalid json")
		case http.StatusUnauthorized:
			return fmt.Errorf("Unauthorized")
		case http.StatusOK:
			toRetry, err := c.processResp(registrationIds, resp)
			if err != nil {
				return err
			}
			if toRetry != nil {
				m.RegistrationIds = toRetry

				dur, isUpdateBackoff := c.calcBackoff(resp.retryAfter, currentBackoff)
				if isUpdateBackoff {
					currentBackoff = dur
				}

				logger.Noticef("RegistrationIds: %v (attempt %d of %d)", toRetry,
					currentAttempts, c.maxRetryAttempts)
				currentAttempts += 1
				sleepHook(dur)
				continue
			} else {
				break Loop
			}
		}
	}
	return nil
}

// uses retryAfter if set and counts it as an attempt, others backs off max backoff
func (c *Client) calcBackoff(retryAfter, currentBackoff time.Duration) (time.Duration, bool) {
	if retryAfter != 0*time.Second {
		if retryAfter < c.minBackoff {
			return c.minBackoff, false
		}
		return retryAfter, false
	}
	// FIXME somehow use the first value
	backoff := currentBackoff * 2
	if backoff > c.maxBackoff {
		return c.maxBackoff, true
	} else if backoff < c.minBackoff {
		return c.minBackoff, true
	}
	return backoff, true
}

func (c *Client) processResp(registrationIds []string, resp *response) (toRetry []string,
	err error) {
	httpResp := resp.httpResp
	// All successful
	if httpResp.Failure == 0 && httpResp.CanonicalIds == 0 {
		return nil, nil
	}

	for i, result := range httpResp.Results {
		regId := registrationIds[i]
		// Check for canonical ID
		if result.MessageId != "" {
			if result.RegistrationId != "" {
				logger.Debugf("update: %s to %s", regId, result.RegistrationId)
				err = c.store.Update(regId, result.RegistrationId)
				if err != nil {
					return nil, err
				}
			}
			continue
		}

		if isRetry(result.Error) {
			toRetry = append(toRetry, regId)
		} else {
			logger.Errorf("RegistrationId: %s error: %s", regId, result.Error)
			// Probably an unrecoverable error or NotRegistered
			logger.Debugf("Deleting: %v", regId)
			err = c.store.Delete(regId)
			if err != nil {
				return nil, err
			}
		}
	}

	return toRetry, nil
}

func (c *Client) send(message *HttpMessage) (*response, error) {
	logger.Debugf("message: %v", message)

	data, err := json.Marshal(message)
	if err != nil {
		return nil, Error.Wrap(err)
	}
	logger.Debugf("send json %s", data)

	req, err := http.NewRequest("POST", c.endpoint, bytes.NewReader(data))
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
func parseRetryAfter(date string) (time.Duration, error) {
	d, err := time.ParseDuration(date + "s")
	if err != nil {
		t, err := http.ParseTime(date)
		if t.Before(nowHook()) {
			return 0, nil
		}
		if err != nil {
			return 0, err
		}
		d = t.Sub(nowHook())
	}
	return d, nil
}
