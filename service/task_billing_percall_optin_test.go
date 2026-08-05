package service

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/assert"
)

// percallOptInAdaptor is a mockAdaptor that opts into completion-time
// settlement despite per-call billing (as the xAI video adaptor does).
type percallOptInAdaptor struct {
	mockAdaptor
}

func (p *percallOptInAdaptor) SettlesPerCallOnComplete() bool { return true }

// Upstream deliberately skips completion-time settlement for per-call billing
// (TestSettle_PerCallBilling_SkipsAdaptorAdjust pins that). xAI bills video by
// the second actually delivered, so its adaptor opts back in. This guards the
// opt-in without weakening the default.
func TestSettle_PerCallBilling_AdaptorCanOptIn(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 31, 31, 31
	const initQuota, preConsumed = 10000, 5000
	const tokenRemain = 8000
	const actualQuota = 2000

	seedUser(t, userID, initQuota)
	seedToken(t, tokenID, userID, "sk-percall-optin", tokenRemain)
	seedChannel(t, channelID)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	task.PrivateData.BillingContext.PerCallBilling = true

	adaptor := &percallOptInAdaptor{mockAdaptor{adjustReturn: actualQuota}}
	taskResult := &relaycommon.TaskInfo{Status: model.TaskStatusSuccess}

	settleTaskBillingOnComplete(ctx, adaptor, task, taskResult)

	// Charged 5000, actual 2000 → 3000 refunded to both wallet and token.
	refund := preConsumed - actualQuota
	assert.Equal(t, actualQuota, task.Quota, "task quota should be re-priced to the actual amount")
	assert.Equal(t, initQuota+refund, getUserQuota(t, userID), "wallet should be refunded the delta")
	assert.Equal(t, tokenRemain+refund, getTokenRemainQuota(t, tokenID), "token quota should be refunded the delta")
}
