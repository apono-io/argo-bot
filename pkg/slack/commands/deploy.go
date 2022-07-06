package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/apono-io/argo-bot/pkg/deploy"
	"github.com/google/go-github/v45/github"
	"github.com/shomali11/slacker"
	log "github.com/sirupsen/logrus"
	slackgo "github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"
	"strings"
)

var (
	deploymentApprovalBlockId = "deployment-approval"
	deploymentApproveActionId = "deployment-pr-approve"
	deploymentDenyActionId    = "deployment-pr-deny"

	lightGrayColor  = "#C9C9C9"
	darkGrayColor   = "#AFAFAF"
	lightBlueColor  = "#58B4F5"
	lightGreenColor = "#59B572"
	darkGreenColor  = "#2DA44E"
	darkRedColor    = "#C93C37"

	textBlockMaxLength = 2900

	noStatus         = ""
	reviewChangesMsg = "Going to deploy the following changes to the deployment repository:"
)

func (c *controller) handleDeploy(botCtx slacker.BotContext, req slacker.Request, _ slacker.ResponseWriter) {
	ctxLogger := log.WithField("slackUserId", botCtx.Event().User).
		WithField("slackChannelId", botCtx.Event().Channel)

	var (
		serviceName = req.StringParam("service", "")
		environment = req.StringParam("environment", "")
		userCommit  = req.StringParam("commit", "")
	)

	ctxLogger = ctxLogger.WithField("serviceName", serviceName).
		WithField("environment", environment).
		WithField("userCommit", userCommit)

	deploymentReq := deploymentRequest{
		ServiceName: serviceName,
		Environment: environment,
		UserId:      botCtx.Event().User,
		Commit:      userCommit,
	}

	commit, commitUrl, err := c.deployer.GetCommitSha(botCtx.Context(), serviceName, userCommit)
	if err != nil {
		c.sendErrorMessage(botCtx, ctxLogger, deploymentReq, err)
		return
	}

	deploymentReq.CommitUrl = commitUrl
	deploymentReq.Commit = commit[:7]

	ctxLogger = ctxLogger.WithField("commit", commit)
	channel, timestamp, err := c.sendRequestDetails(botCtx, ctxLogger, deploymentReq)
	if err != nil {
		ctxLogger.WithError(err).
			Error("Failed to send message to user")
		return
	}

	deploymentReq.Channel = &channel
	deploymentReq.Timestamp = &timestamp
	user := "@" + botCtx.Event().UserName
	profile, err := botCtx.SocketMode().GetUserProfile(&slackgo.GetUserProfileParameters{UserID: botCtx.Event().User})
	if err != nil {
		ctxLogger.WithError(err).Error("Failed to get slack user profile")
	}

	user = fmt.Sprintf("%s %s (%s)", profile.FirstName, profile.LastName, profile.Email)
	pr, diff, err := c.deployer.Deploy(serviceName, environment, commit, commitUrl, user)
	if err != nil {
		ctxLogger.WithError(err).Error("Failed to deploy")
		c.sendErrorMessage(botCtx, ctxLogger, deploymentReq, err)
		return
	}

	c.sendApprovalMessage(botCtx, deploymentReq, ctxLogger, pr, diff)
}

func (c *controller) sendRequestDetails(botCtx slacker.BotContext, ctxLogger *log.Entry, req deploymentRequest) (string, string, error) {
	ctxLogger.Infof("Got request to deploy %s to %s with version %s from %s", req.ServiceName, req.Environment, req.Commit, req.UserId)
	channel, timestamp, _, err := botCtx.SocketMode().SendMessage(
		botCtx.Event().Channel,
		c.messageWithRequestDetails(lightBlueColor, noStatus, req)...,
	)

	return channel, timestamp, err
}

func (c *controller) messageWithRequestDetails(requestDetailsColor string, status string, req deploymentRequest, additionalBlocks ...slackgo.Block) []slackgo.MsgOption {
	commit := req.Commit
	if req.CommitUrl != "" {
		commit = fmt.Sprintf("<%s|%s>", req.CommitUrl, req.Commit)
	}

	blocks := []slackgo.Block{
		slackgo.NewSectionBlock(nil, []*slackgo.TextBlockObject{
			slackgo.NewTextBlockObject(slackgo.MarkdownType, fmt.Sprintf("*Service:*\n%s", req.ServiceName), false, false),
			slackgo.NewTextBlockObject(slackgo.MarkdownType, fmt.Sprintf("*Environment:*\n%s", req.Environment), false, false),
			slackgo.NewTextBlockObject(slackgo.MarkdownType, fmt.Sprintf("*Commit:*\n%s", commit), false, false),
			slackgo.NewTextBlockObject(slackgo.MarkdownType, fmt.Sprintf("*Deployer:*\n<@%s>", req.UserId), false, false),
		}, nil),
	}

	if status != noStatus {
		blocks = append(blocks, slackgo.NewContextBlock("", slackgo.NewTextBlockObject(slackgo.MarkdownType, status, false, false)))
	}

	if additionalBlocks != nil {
		blocks = append(blocks, additionalBlocks...)
	}

	return []slackgo.MsgOption{
		slackgo.MsgOptionText("Got new deployment request", false),
		slackgo.MsgOptionAttachments(slackgo.Attachment{
			Color: requestDetailsColor,
			Blocks: slackgo.Blocks{
				BlockSet: blocks,
			},
		}),
	}
}

func (c *controller) sendApprovalMessage(botCtx slacker.BotContext, req deploymentRequest, ctxLogger *log.Entry, pr *github.PullRequest, diff string) {
	req.PrNumber = pr.GetNumber()
	bytes, err := json.Marshal(req)
	if err != nil {
		ctxLogger.WithField("slackUserId", botCtx.Event().User).
			WithField("slackChannelId", botCtx.Event().Channel).
			WithError(err).
			Error("Failed to marshal request")
		return
	}

	reqJson := string(bytes)
	approveBtn := slackgo.NewButtonBlockElement(deploymentApproveActionId, reqJson, slackgo.NewTextBlockObject(slackgo.PlainTextType, "Approve", false, false))
	approveBtn.Style = slackgo.StylePrimary

	rejectBtn := slackgo.NewButtonBlockElement(deploymentDenyActionId, reqJson, slackgo.NewTextBlockObject(slackgo.PlainTextType, "Deny", false, false))
	rejectBtn.Style = slackgo.StyleDanger

	diffText := fmt.Sprintf("%s\n```%s```", reviewChangesMsg, c.truncateDiff(diff, textBlockMaxLength))
	if diff == "" {
		diffText = "_Nothing to change, merging this PR will only create empty commit_"
	}

	_, _, _, err = botCtx.SocketMode().UpdateMessage(*req.Channel, *req.Timestamp,
		c.messageWithRequestDetails(lightBlueColor, noStatus, req,
			slackgo.NewSectionBlock(slackgo.NewTextBlockObject(slackgo.MarkdownType, diffText, false, false), nil, nil),
			slackgo.NewContextBlock("", slackgo.NewTextBlockObject(slackgo.MarkdownType, fmt.Sprintf("<%s|Original pull request>", pr.GetHTMLURL()), false, false)),
			slackgo.NewActionBlock(deploymentApprovalBlockId,
				approveBtn,
				rejectBtn,
			),
		)...,
	)

	if err != nil {
		ctxLogger.WithField("slackUserId", botCtx.Event().User).
			WithField("slackChannelId", botCtx.Event().Channel).
			WithError(err).
			Error("Failed to send message to user")
	}
}

func (c *controller) handleApproval(s *slacker.Slacker, _ *socketmode.Event, callback *slackgo.InteractionCallback, _ *socketmode.Request) {
	ctx := context.Background()
	logger := log.WithField("slackUserId", callback.User.ID).
		WithField("slackChannelId", callback.Channel.ID)
	blockActions := callback.ActionCallback.BlockActions
	if len(blockActions) != 1 {
		logger.WithField("blockActions", blockActions).Error("Got unexpected amount of block actions")
		return
	}

	action := blockActions[0]
	actionId := action.ActionID

	var req deploymentRequest
	err := json.Unmarshal([]byte(action.Value), &req)
	if err != nil {
		logger.WithError(err).Error("Failed to unmarshal request")
		return
	}

	pullRequestNumber := req.PrNumber
	logger = logger.WithField("pullRequestId", pullRequestNumber)

	switch actionId {
	case deploymentApproveActionId:
		c.executeApprovalAction(ctx, s, callback, logger, req, c.deployer.Approve,
			lightGreenColor, "Merging deployment pull request...",
			darkGreenColor, "Deployment pull request merged successfully")
	case deploymentDenyActionId:
		c.executeApprovalAction(ctx, s, callback, logger, req, c.deployer.Cancel,
			lightGrayColor, "Closing deployment pull request...",
			darkGrayColor, "Closed deployment pull request")
	default:
		logger.WithField("actionId", actionId).Error("Unexpected action ID")
	}
}

func (c *controller) executeApprovalAction(ctx context.Context, s *slacker.Slacker, callback *slackgo.InteractionCallback, logger *log.Entry,
	req deploymentRequest, handler approvalActionHandler, progressColor, progressMsg, successColor, successMsg string) {
	err := c.updateMessage(s.SocketMode(), callback, req, progressColor, progressMsg)
	if err != nil {
		logger.WithError(err).Error("Failed to send progress message to Slack")
	}

	err = handler(ctx, req.PrNumber)
	if err != nil {
		logger.WithError(err).Error("Failed execute approval action")
		err = c.updateMessage(s.SocketMode(), callback, req, darkRedColor, fmt.Sprintf("Error: %s", err.Error()))
		if err != nil {
			logger.WithError(err).Error("Failed to notify user about error during approval process")
		}

		return
	}

	err = c.updateMessage(s.SocketMode(), callback, req, successColor, successMsg)
	if err != nil {
		logger.WithError(err).Error("Failed to send success message to Slack")
	}
}

func (c *controller) updateMessage(client *socketmode.Client, callback *slackgo.InteractionCallback, req deploymentRequest, color, status string) error {
	options := []slackgo.MsgOption{
		slackgo.MsgOptionReplaceOriginal(callback.ResponseURL),
	}
	options = append(options, c.messageWithRequestDetails(color, status, req)...)
	_, _, _, err := client.SendMessage(callback.Channel.ID, options...)
	return err
}

func (c *controller) sendErrorMessage(botCtx slacker.BotContext, ctxLogger *log.Entry, req deploymentRequest, executionErr error) {
	var errorMsg string
	if validationErr, ok := executionErr.(deploy.ValidationErr); ok {
		errorMsg = fmt.Sprintf("Validation error: %s", validationErr.Error())
	} else {
		errorMsg = fmt.Sprintf("Error: %s", executionErr.Error())
	}

	var err error
	msgOptions := c.messageWithRequestDetails(darkRedColor, errorMsg, req)
	if req.Channel != nil && req.Timestamp != nil {
		_, _, _, err = botCtx.SocketMode().UpdateMessage(*req.Channel, *req.Timestamp, msgOptions...)
	} else {
		_, _, _, err = botCtx.SocketMode().SendMessage(botCtx.Event().Channel, msgOptions...)
	}

	if err != nil {
		ctxLogger.WithField("slackUserId", botCtx.Event().User).
			WithField("slackChannelId", botCtx.Event().Channel).
			WithField("errorMsg", errorMsg).
			WithError(err).
			Error("Failed to send error message to user")
	}
}

func (c *controller) truncateDiff(text string, width int) string {
	changesStartIdx := strings.Index(text, "---")
	if changesStartIdx != -1 {
		text = text[changesStartIdx:]
	}

	if len(text) <= width {
		return text
	}

	text = string([]byte(text)[:width])
	return fmt.Sprintf("%s...", text)
}

type deploymentRequest struct {
	ServiceName string  `json:"service_name"`
	Environment string  `json:"environment"`
	CommitUrl   string  `json:"commit_url"`
	Commit      string  `json:"commit"`
	UserId      string  `json:"user_id"`
	Channel     *string `json:"channel,omitempty"`
	Timestamp   *string `json:"timestamp,omitempty"`
	PrNumber    int     `json:"pr_number,omitempty"`
}

type approvalActionHandler func(ctx context.Context, pullRequestNumber int) error
