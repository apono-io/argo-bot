package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/apono-io/argo-bot/pkg/api"
	"github.com/apono-io/argo-bot/pkg/deploy"
	"github.com/apono-io/argo-bot/pkg/github"
	"github.com/apono-io/argo-bot/pkg/utils"
	"github.com/shomali11/slacker"
	log "github.com/sirupsen/logrus"
	slackgo "github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"
)

var (
	freezeApprovalBlockId = "freeze-pr-approval"
	freezeApproveActionId = "freeze-pr-approve"
	freezeDenyActionId    = "freeze-pr-deny"
)

func (c *controller) handleFreeze(botCtx slacker.BotContext, req slacker.Request, _ slacker.ResponseWriter) {
	c.handleFreezeCommands(botCtx, req, deploy.FreezeActionFreeze)
}

func (c *controller) handleUnfreeze(botCtx slacker.BotContext, req slacker.Request, _ slacker.ResponseWriter) {
	c.handleFreezeCommands(botCtx, req, deploy.FreezeActionUnfreeze)
}

func (c *controller) handleFreezeCommands(botCtx slacker.BotContext, req slacker.Request, action deploy.FreezeAction) {
	ctxLogger := log.WithField("slackUserId", botCtx.Event().UserID).
		WithField("slackChannelId", botCtx.Event().ChannelID)

	var (
		serviceName = req.StringParam("services", "")
		environment = req.StringParam("environment", "")
	)

	ctxLogger = ctxLogger.WithField("serviceName", serviceName).
		WithField("environment", environment)

	services := utils.UniqueStrings(strings.Split(serviceName, ","))
	resolvedServices := c.deployer.ResolveTags(services)

	freezeReq := freezeRequest{
		ServiceNames: resolvedServices,
		Environment:  environment,
		UserId:       botCtx.Event().UserID,
		Action:       action,
	}

	channel, timestamp, err := c.sendFreezeDetails(botCtx, ctxLogger, freezeReq)
	if err != nil {
		ctxLogger.WithError(err).
			Error("Failed to send message to user")
		return
	}

	freezeReq.Channel = &channel
	freezeReq.Timestamp = &timestamp
	profile, err := botCtx.SocketModeClient().GetUserProfile(&slackgo.GetUserProfileParameters{UserID: botCtx.Event().UserID})
	if err != nil {
		ctxLogger.WithError(err).Error("Failed to get slack user profile")
	}

	userFullname := fmt.Sprintf("%s %s", profile.FirstName, profile.LastName)
	pr, diff, err := c.deployer.Freeze(services, environment, userFullname, profile.Email, action)
	if err != nil {
		ctxLogger.WithError(err).Error("Failed to freeze services")
		c.sendFreezeErrorMessage(botCtx, ctxLogger, freezeReq, err)
		return
	}

	if pr == nil {
		// If PR is nil, it means no changes were needed
		successMsg := "No changes needed - services already in desired state"
		_, _, _, err = botCtx.SocketModeClient().UpdateMessage(*freezeReq.Channel, *freezeReq.Timestamp,
			c.messageWithFreezeDetails(darkGreenColor, successMsg, freezeReq)...)
		if err != nil {
			ctxLogger.WithError(err).Error("Failed to send success message to user")
		}
		return
	}

	c.sendFreezeApprovalMessage(botCtx, freezeReq, ctxLogger, pr, diff)
}

func (c *controller) sendFreezeDetails(botCtx slacker.BotContext, ctxLogger *log.Entry, req freezeRequest) (string, string, error) {
	freezeMessage := getFreezeMessage(req)
	ctxLogger.Infof("Got request to %s %s to %s from %s", freezeMessage, strings.Join(req.ServiceNames, ","), req.Environment, req.UserId)
	channel, timestamp, _, err := botCtx.SocketModeClient().SendMessage(
		botCtx.Event().ChannelID,
		c.messageWithFreezeDetails(lightBlueColor, noStatus, req)...,
	)

	return channel, timestamp, err
}

func (c *controller) messageWithFreezeDetails(requestDetailsColor string, status string, req freezeRequest, additionalBlocks ...slackgo.Block) []slackgo.MsgOption {
	blocks := []slackgo.Block{
		slackgo.NewSectionBlock(nil, []*slackgo.TextBlockObject{
			slackgo.NewTextBlockObject(slackgo.MarkdownType, fmt.Sprintf("*Services:*\n%s", strings.Join(req.ServiceNames, ", ")), false, false),
			slackgo.NewTextBlockObject(slackgo.MarkdownType, fmt.Sprintf("*Environment:*\n%s", req.Environment), false, false),
			slackgo.NewTextBlockObject(slackgo.MarkdownType, fmt.Sprintf("*User:*\n<@%s>", req.UserId), false, false),
		}, nil),
	}

	if status != noStatus {
		blocks = append(blocks, slackgo.NewContextBlock("", slackgo.NewTextBlockObject(slackgo.MarkdownType, status, false, false)))
	}

	if additionalBlocks != nil {
		blocks = append(blocks, additionalBlocks...)
	}

	freezeMessage := getFreezeMessage(req)
	return []slackgo.MsgOption{
		slackgo.MsgOptionText(fmt.Sprintf("Got new %s request", freezeMessage), false),
		slackgo.MsgOptionAttachments(slackgo.Attachment{
			Color: requestDetailsColor,
			Blocks: slackgo.Blocks{
				BlockSet: blocks,
			},
		}),
	}
}

func (c *controller) sendFreezeErrorMessage(botCtx slacker.BotContext, ctxLogger *log.Entry, req freezeRequest, executionErr error) {
	var errorMsg string
	if validationErr, ok := executionErr.(api.ValidationErr); ok {
		errorMsg = fmt.Sprintf("Validation error: %s", validationErr.Error())
	} else {
		errorMsg = fmt.Sprintf("Error: %s", executionErr.Error())
	}

	var err error
	msgOptions := c.messageWithFreezeDetails(darkRedColor, errorMsg, req)
	if req.Channel != nil && req.Timestamp != nil {
		_, _, _, err = botCtx.SocketModeClient().UpdateMessage(*req.Channel, *req.Timestamp, msgOptions...)
	} else {
		_, _, _, err = botCtx.SocketModeClient().SendMessage(botCtx.Event().ChannelID, msgOptions...)
	}

	if err != nil {
		ctxLogger.WithField("slackUserId", botCtx.Event().UserID).
			WithField("slackChannelId", botCtx.Event().ChannelID).
			WithField("errorMsg", errorMsg).
			WithError(err).
			Error("Failed to send error message to user")
	}
}

func (c *controller) updateFreezeMessage(client *socketmode.Client, callback *slackgo.InteractionCallback, req freezeRequest, color, status string) error {
	options := []slackgo.MsgOption{
		slackgo.MsgOptionReplaceOriginal(callback.ResponseURL),
	}
	options = append(options, c.messageWithFreezeDetails(color, status, req)...)
	_, _, _, err := client.SendMessage(callback.Channel.ID, options...)
	return err
}

func (c *controller) sendFreezeApprovalMessage(botCtx slacker.BotContext, req freezeRequest, ctxLogger *log.Entry, pr *github.PullRequest, diff string) {
	req.PrNumber = pr.Id
	bytes, err := json.Marshal(req)
	if err != nil {
		ctxLogger.WithField("slackUserId", botCtx.Event().UserID).
			WithField("slackChannelId", botCtx.Event().ChannelID).
			WithError(err).
			Error("Failed to marshal request")
		return
	}

	reqJson := string(bytes)
	approveBtn := slackgo.NewButtonBlockElement(freezeApproveActionId, reqJson, slackgo.NewTextBlockObject(slackgo.PlainTextType, "Approve", false, false))
	approveBtn.Style = slackgo.StylePrimary

	rejectBtn := slackgo.NewButtonBlockElement(freezeDenyActionId, reqJson, slackgo.NewTextBlockObject(slackgo.PlainTextType, "Deny", false, false))
	rejectBtn.Style = slackgo.StyleDanger

	diffText := fmt.Sprintf("%s\n```%s```", reviewChangesMsg, c.truncateDiff(diff, textBlockMaxLength))
	if diff == "" {
		diffText = "_Nothing to change, merging this PR will only create empty commit_"
	}

	_, _, _, err = botCtx.SocketModeClient().UpdateMessage(*req.Channel, *req.Timestamp,
		c.messageWithFreezeDetails(lightBlueColor, noStatus, req,
			slackgo.NewSectionBlock(slackgo.NewTextBlockObject(slackgo.MarkdownType, diffText, false, false), nil, nil),
			slackgo.NewContextBlock("", slackgo.NewTextBlockObject(slackgo.MarkdownType, fmt.Sprintf("<%s|Original pull request>", pr.Link), false, false)),
			slackgo.NewActionBlock(freezeApprovalBlockId,
				approveBtn,
				rejectBtn,
			),
		)...,
	)

	if err != nil {
		ctxLogger.WithField("slackUserId", botCtx.Event().UserID).
			WithField("slackChannelId", botCtx.Event().ChannelID).
			WithError(err).
			Error("Failed to send message to user")
	}
}

func (c *controller) handleFreezeApproval(botCtx slacker.InteractiveBotContext, _ *socketmode.Request, callback *slackgo.InteractionCallback) {
	ctx := botCtx.Context()
	logger := log.WithField("slackUserId", callback.User.ID).
		WithField("slackChannelId", callback.Channel.ID)
	blockActions := callback.ActionCallback.BlockActions
	if len(blockActions) != 1 {
		logger.WithField("blockActions", blockActions).Error("Got unexpected amount of block actions")
		return
	}

	action := blockActions[0]
	actionId := action.ActionID

	var req freezeRequest
	err := json.Unmarshal([]byte(action.Value), &req)
	if err != nil {
		logger.WithError(err).Error("Failed to unmarshal request")
		return
	}

	pullRequestNumber := req.PrNumber
	logger = logger.WithField("pullRequestId", pullRequestNumber)

	socketModeClient := botCtx.SocketModeClient()
	switch actionId {
	case freezeApproveActionId:
		c.executeFreezeApprovalAction(ctx, socketModeClient, callback, logger, req, c.deployer.Approve,
			lightGreenColor, "Merging deployment pull request...",
			darkGreenColor, "Deployment pull request merged successfully")
	case freezeDenyActionId:
		c.executeFreezeApprovalAction(ctx, socketModeClient, callback, logger, req, c.deployer.Cancel,
			lightGrayColor, "Closing deployment pull request...",
			darkGrayColor, "Closed deployment pull request")
	default:
		logger.WithField("actionId", actionId).Error("Unexpected action ID")
	}
}

func (c *controller) executeFreezeApprovalAction(ctx context.Context, client *socketmode.Client, callback *slackgo.InteractionCallback, logger *log.Entry,
	req freezeRequest, handler approvalActionHandler, progressColor, progressMsg, successColor, successMsg string) {
	err := c.updateFreezeMessage(client, callback, req, progressColor, progressMsg)
	if err != nil {
		logger.WithError(err).Error("Failed to send progress message to Slack")
	}

	err = handler(ctx, req.PrNumber)
	if err != nil {
		logger.WithError(err).Error("Failed execute approval action")
		err = c.updateFreezeMessage(client, callback, req, darkRedColor, fmt.Sprintf("Error: %s", err.Error()))
		if err != nil {
			logger.WithError(err).Error("Failed to notify user about error during approval process")
		}

		return
	}

	err = c.updateFreezeMessage(client, callback, req, successColor, successMsg)
	if err != nil {
		logger.WithError(err).Error("Failed to send success message to Slack")
	}
}

func getFreezeMessage(req freezeRequest) string {
	return string(req.Action)
}

type freezeRequest struct {
	ServiceNames []string            `json:"service_names"`
	Environment  string              `json:"environment"`
	UserId       string              `json:"user_id"`
	Action       deploy.FreezeAction `json:"action"`
	Channel      *string             `json:"channel,omitempty"`
	Timestamp    *string             `json:"timestamp,omitempty"`
	PrNumber     int                 `json:"pr_number,omitempty"`
}
