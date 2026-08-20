package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestIsOpenAIModelCapacityError(t *testing.T) {
	payload := []byte(`{"error":{"message":"Selected model is at capacity. Please try a different model.","type":"invalid_request_error"}}`)
	require.True(t, isOpenAIModelCapacityError("", payload))
	require.True(t, isOpenAIModelCapacityError("Selected model is at capacity. Please try a different model.", nil))
	require.False(t, isOpenAIModelCapacityError("Our servers are currently overloaded. Please try again later.", nil))
	require.False(t, isOpenAIModelCapacityError("", []byte(`{"response":{"error":{"message":"Our servers are currently overloaded. Please try again later."}}}`)))
	require.False(t, isOpenAIModelCapacityError("server is overloaded", []byte(`{"error":{"code":"server_is_overloaded"}}`)))
	require.False(t, isOpenAIModelCapacityError("You have exhausted your capacity on this model.", nil))
}

func TestNewOpenAIUpstreamFailoverError_ModelCapacityDisablesSameAccountRetry(t *testing.T) {
	err := newOpenAIUpstreamFailoverError(
		http.StatusBadRequest,
		http.Header{},
		[]byte(`{"error":{"message":"Selected model is at capacity. Please try a different model."}}`),
		"Selected model is at capacity. Please try a different model.",
		true,
	)
	require.False(t, err.RetryableOnSameAccount)
	require.True(t, err.IsOpenAIModelCapacityError())
	require.Equal(t, NextAccountRetry, err.NextAccountAction)
}

func TestNewOpenAIStreamFailoverError_ModelCapacityDisablesSameAccountRetry(t *testing.T) {
	account := &Account{
		ID:       12,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"pool_mode": true,
		},
	}
	err := (&OpenAIGatewayService{}).newOpenAIStreamFailoverError(
		nil,
		account,
		false,
		"req_capacity",
		[]byte(`{"type":"response.failed","response":{"error":{"message":"Selected model is at capacity. Please try a different model."}}}`),
		"Selected model is at capacity. Please try a different model.",
	)
	require.False(t, err.RetryableOnSameAccount)
	require.True(t, err.IsOpenAIModelCapacityError())
}

func TestNewOpenAIUpstreamFailoverError_ServerOverloadRemainsRequestScoped(t *testing.T) {
	err := newOpenAIUpstreamFailoverError(
		http.StatusBadRequest,
		http.Header{},
		[]byte(`{"error":{"message":"Our servers are currently overloaded. Please try again later."}}`),
		"Our servers are currently overloaded. Please try again later.",
		true,
	)
	require.True(t, err.RetryableOnSameAccount)
	require.True(t, err.RequestScopedTransient)
	require.False(t, err.IsOpenAIModelCapacityError())
}

func TestOpenAIStreamingPassthrough_ServerOverloadAfterOutputDoesNotCoolAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &OpenAIGatewayService{
		cfg:                 &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}},
		openaiModelCapacity: newOpenAIModelCapacityState(16),
	}
	account := &Account{ID: 41, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	payload := `{"type":"response.failed","response":{"error":{"message":"Our servers are currently overloaded. Please try again later."}}}`

	for attempt := 1; attempt <= 2; attempt++ {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Body: io.NopCloser(strings.NewReader(strings.Join([]string{
				`data: {"type":"response.output_text.delta","delta":"partial"}`,
				"",
				"data: " + payload,
				"",
			}, "\n"))),
		}

		_, err := svc.handleStreamingResponsePassthrough(
			c.Request.Context(), resp, c, account, time.Now(), "gpt-5.6-sol", "gpt-5.6-sol",
		)
		require.Error(t, err)
		var failoverErr *UpstreamFailoverError
		require.False(t, errors.As(err, &failoverErr), "a stream with visible output must not be replayed")
		require.Contains(t, recorder.Body.String(), "partial")
		require.Contains(t, recorder.Body.String(), "Our servers are currently overloaded")

		require.False(t, svc.isOpenAIAccountRequestRuntimeBlocked(account, "gpt-5.6-sol"))
	}
}

func TestOpenAIModelCapacityStateEscalatesAndPreservesLongCooldown(t *testing.T) {
	state := newOpenAIModelCapacityState(16)
	base := time.Unix(1000, 0)

	first := state.recordFailure(11, "gpt-5.5", base)
	second := state.recordFailure(11, "gpt-5.5", base.Add(10*time.Second))
	third := state.recordFailure(11, "gpt-5.5", base.Add(20*time.Second))

	require.Equal(t, 1, first.FailureStreak)
	require.Zero(t, first.Cooldown)
	require.Equal(t, 2, second.FailureStreak)
	require.Equal(t, time.Minute, second.Cooldown)
	require.Equal(t, 3, third.FailureStreak)
	require.Equal(t, 5*time.Minute, third.Cooldown)
	require.True(t, state.isBlocked(11, "gpt-5.5", base.Add(2*time.Minute)))
	require.False(t, state.isBlocked(11, "gpt-5.5", base.Add(6*time.Minute)))

	state.recordFailure(11, "gpt-5.5", base)
	state.recordFailure(11, "gpt-5.5", base.Add(time.Second))
	require.True(t, state.isBlocked(11, "gpt-5.5", base.Add(2*time.Second)))
	state.recordSuccess(11, "gpt-5.5")
	require.False(t, state.isBlocked(11, "gpt-5.5", base.Add(2*time.Second)))
}

func TestReportOpenAIAccountScheduleResult_CapacityCoolsOAuthAccountModel(t *testing.T) {
	svc := &OpenAIGatewayService{openaiModelCapacity: newOpenAIModelCapacityState(16)}
	failoverErr := &UpstreamFailoverError{Reason: openAIModelCapacityFailureReason}
	account := &Account{ID: 31, Platform: PlatformOpenAI, Type: AccountTypeOAuth}

	svc.ReportOpenAIAccountScheduleResult(account.ID, "gpt-5.5", false, nil, failoverErr)
	require.False(t, svc.isOpenAIAccountRequestRuntimeBlocked(account, "gpt-5.5"))
	svc.ReportOpenAIAccountScheduleResult(account.ID, "gpt-5.5", false, nil, failoverErr)
	require.True(t, svc.isOpenAIAccountRequestRuntimeBlocked(account, "gpt-5.5"))
	require.False(t, svc.isOpenAIAccountRequestRuntimeBlocked(account, "gpt-5.6"))

	svc.ReportOpenAIAccountScheduleResult(account.ID, "gpt-5.5", true, nil)
	require.False(t, svc.isOpenAIAccountRequestRuntimeBlocked(account, "gpt-5.5"))
}

func TestOpenAIAccountDistributionAvoidsImmediateRepeat(t *testing.T) {
	state := newOpenAIAccountDistributionState(16)
	key := newOpenAIAccountDistributionKey(nil, PlatformOpenAI, "gpt-5.5", "", "", false)
	state.record(key, 1, time.Unix(1000, 0))
	require.Equal(t, int64(2), state.preferred(key, []int64{1, 2}))
	state.record(key, 2, time.Unix(1001, 0))
	require.Equal(t, int64(1), state.preferred(key, []int64{2, 1}))
}

func TestOpenAIAdvancedSelectionOrderAvoidsImmediateRepeatOnlyForIndependentRequest(t *testing.T) {
	svc := &OpenAIGatewayService{openaiAccountDistribution: newOpenAIAccountDistributionState(16)}
	scheduler := &defaultOpenAIAccountScheduler{service: svc}
	req := OpenAIAccountScheduleRequest{
		Platform:              PlatformOpenAI,
		RequestedModel:        "gpt-5.5",
		DistributeIndependent: true,
	}
	key := newOpenAIAccountDistributionKey(nil, PlatformOpenAI, "gpt-5.5", "", "", false)
	svc.openaiAccountDistribution.record(key, 1, time.Unix(1000, 0))
	order := []openAIAccountCandidateScore{
		{account: &Account{ID: 1}},
		{account: &Account{ID: 2}},
	}

	got := scheduler.distributeSelectionOrder(req, append([]openAIAccountCandidateScore(nil), order...))
	require.Equal(t, int64(2), got[0].account.ID)

	req.DistributeIndependent = false
	got = scheduler.distributeSelectionOrder(req, append([]openAIAccountCandidateScore(nil), order...))
	require.Equal(t, int64(1), got[0].account.ID)
}

func TestShouldDistributeOpenAIAccountRequestPreservesExistingStickyBinding(t *testing.T) {
	svc := &OpenAIGatewayService{cache: &stubGatewayCache{sessionBindings: map[string]int64{
		"openai:sticky": 42,
	}}}
	require.True(t, svc.shouldDistributeOpenAIAccountRequest(context.Background(), nil, "", ""))
	require.False(t, svc.shouldDistributeOpenAIAccountRequest(context.Background(), nil, "resp_123", ""))
	require.False(t, svc.shouldDistributeOpenAIAccountRequest(context.Background(), nil, "", "sticky"))
	require.True(t, svc.shouldDistributeOpenAIAccountRequest(context.Background(), nil, "", "new-session"))
}

func TestShouldDistributeOpenAIAccountRequestDoesNotRotateSessionWithoutCache(t *testing.T) {
	svc := &OpenAIGatewayService{}
	require.False(t, svc.shouldDistributeOpenAIAccountRequest(context.Background(), nil, "", "session-without-cache"))
}
