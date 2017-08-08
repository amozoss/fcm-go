package fcm

import (
	"context"
	"io/ioutil"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/amozoss/atest"
)

var (
	ctx = context.Background()
)

//func init() {
//	spacelog.MustSetup("test", spacelog.SetupConfig{Level: "debug"})
//}

func TestSendRetry(t *testing.T) {
	test := NewTestFcmClient(t)
	httpMsg := HttpMessage{
		RegistrationIds: []string{
			"a",
			"b",
		},
	}
	totalSleep := 0 * time.Second

	orgSleepHook := sleepHook
	defer func() {
		sleepHook = orgSleepHook
	}()

	// Total up how long it slept to determine if backoff works
	sleepHook = func(dur time.Duration) {
		totalSleep += dur
	}

	unavailableMsg := `{ "multicast_id": 108,
		"success": 0,
		"failure": 1,
		"canonical_ids": 0,
		"results": [
			{ "error": "Unavailable" }
		]
	}`
	// 5s
	resp1 := NewResponse(200, unavailableMsg)
	resp1.Header.Set("Retry-After", "5")
	test.AddResponse(resp1)

	// 2s
	resp2 := NewResponse(200, unavailableMsg)
	test.AddResponse(resp2)

	// 4s
	resp3 := NewResponse(200, unavailableMsg)
	test.AddResponse(resp3)

	// 15s
	resp4 := NewResponse(200, unavailableMsg)
	resp4.Header.Set("Retry-After", "15")
	test.AddResponse(resp4)

	successMsg := `{ "multicast_id": 108,
		"success": 1,
		"failure": 0,
		"canonical_ids": 0,
		"results": [
			{ "message_id": "12" }
		]
	}`
	resp5 := NewResponse(200, successMsg)
	test.AddResponse(resp5)

	httpResp, err := test.fcmClient.Send(ctx, httpMsg)
	test.AssertNoError(err)
	test.AssertEqual(uint(1), httpResp.Success)
	test.AssertEqual(uint(0), httpResp.Failure)
	test.AssertEqual(uint(0), httpResp.CanonicalIds)
	test.AssertEqual(1, len(httpResp.Results))
	test.AssertEqual("12", httpResp.Results[0].MessageId)

	test.AssertEqual(26*time.Second, totalSleep)
}

func TestProcessRespSuccess(t *testing.T) {
	test := NewTestFcmClient(t)

	resp := &response{
		httpResp: &HttpResponse{
			MulticastId:  108,
			Success:      1,
			Failure:      0,
			CanonicalIds: 0,
			Results: []Result{
				{
					MessageId: "1:08",
				},
			},
		},
	}
	regIds := []string{
		"a",
	}

	toRetry, err := test.fcmClient.processResp(ctx, regIds, resp)
	test.AssertNoError(err)
	test.AssertEqual(len(toRetry), 0)
}

func TestProcessRespMultipleResults(t *testing.T) {
	test := NewTestFcmClient(t)

	resp := &response{
		httpResp: &HttpResponse{
			MulticastId:  216,
			Success:      3,
			Failure:      3,
			CanonicalIds: 1,
			Results: []Result{
				{
					MessageId: "1:0408",
				},
				{
					Error: "Unavailable",
				},
				{
					Error: "InvalidRegistration",
				},
				{
					MessageId: "1:1516",
				},
				{
					MessageId:      "1:2342",
					RegistrationId: "32",
				},
				{
					Error: "NotRegistered",
				},
			},
		},
	}
	regIds := []string{
		"4",
		"8",
		"15",
		"16",
		"23",
		"42",
	}
	test.AddRegIds(regIds)

	toRetry, err := test.fcmClient.processResp(ctx, regIds, resp)
	test.AssertNoError(err)

	expectedRetry := []string{
		"8",
	}
	test.AssertEqual(toRetry, expectedRetry)

	expectedRegIds := []string{ // result is sorted
		"16",
		"32",
		"4",
		"8",
	}
	test.AssertEqual(expectedRegIds, test.GetRegIds())

}

func TestParseRetryAfter(t *testing.T) {
	test := NewTestFcmClient(t)

	orgNowHook := nowHook
	defer func() {
		nowHook = orgNowHook
	}()

	// Should parse duration from date
	nowHook = func() time.Time {
		return time.Date(1999, time.December, 31, 23, 59, 0, 0, time.UTC)
	}
	date := "Fri, 31 Dec 1999 23:59:59 GMT"
	dur, err := parseRetryAfter(date)
	test.AssertNoError(err)
	test.AssertEqual(*dur, 59*time.Second)

	// Should handle past dates
	nowHook = func() time.Time {
		return time.Date(2016, time.December, 31, 0, 0, 0, 0, time.UTC)
	}
	date = "Fri, 31 Dec 1999 23:59:59 GMT"
	dur, err = parseRetryAfter(date)
	test.AssertNoError(err)
	test.AssertNil(dur)

	// Should parse numbers
	dur_str := "5"
	dur, err = parseRetryAfter(dur_str)
	test.AssertNoError(err)
	test.AssertEqual(*dur, 5*time.Second)

	// Should handle invalid content
	dur_str = "5 blab"
	dur, err = parseRetryAfter(dur_str)
	test.AssertNoError(err)
	test.AssertNil(dur)
}

//////////////////////////////////////////////////////////////
// Helpers
//////////////////////////////////////////////////////////////

type TestFcmClient struct {
	*atest.Test
	fcmClient *Client
	Store

	responses []*http.Response
	RespCount int // number of things Do is called
	regIds    map[string]bool
}

func NewResponse(code int, body string) *http.Response {
	return &http.Response{
		StatusCode: code,
		Header:     make(http.Header),
		Body:       ioutil.NopCloser(strings.NewReader(body)),
	}
}

func (t *TestFcmClient) AddResponse(resp *http.Response) {
	t.responses = append(t.responses, resp)
}

func NewTestFcmClient(t *testing.T) *TestFcmClient {
	test := &TestFcmClient{
		Test:      atest.Wrap(t, 2),
		regIds:    make(map[string]bool),
		responses: make([]*http.Response, 0),
	}
	fc := NewFcmClient("api_key", test, test, nil)
	test.fcmClient = fc
	return test
}

// Mock HttpClient
// Will iterate through responses, but serves the last one repeatedly
func (t *TestFcmClient) Do(req *http.Request) (resp *http.Response, err error) {
	size := len(t.responses)
	t.Assert(size > 0)

	if t.RespCount > size-1 {
		// Require responses to be set before Do is called
		t.Fatalf("Do called %d times, expected %d", t.RespCount, size)
	}
	resp = t.responses[t.RespCount]
	t.RespCount += 1
	return
}

func (t *TestFcmClient) GetRegIds() (regIds []string) {
	for regId := range t.regIds {
		regIds = append(regIds, regId)
	}
	sort.Strings(regIds)
	return
}

func (t *TestFcmClient) AddRegIds(regIds []string) {
	for _, regId := range regIds {
		t.regIds[regId] = true
	}
}

// Mock Store
func (t *TestFcmClient) Update(ctx context.Context, oldRegId,
	newRegId string) error {
	delete(t.regIds, oldRegId)
	t.regIds[newRegId] = true
	return nil
}

// Mock Store
func (t *TestFcmClient) Delete(ctx context.Context, regId string) error {
	delete(t.regIds, regId)
	return nil
}
