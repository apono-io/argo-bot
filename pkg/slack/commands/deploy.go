package commands

import (
	"context"
	"fmt"
	"github.com/apono-io/argo-bot/pkg/deploy"
	"github.com/google/go-github/v45/github"
	log "github.com/sirupsen/logrus"
	slackgo "github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"
	"strconv"
)

var (
	deploymentApproveActionId = "deployment-pr-approve"
	deploymentDenyActionId    = "deployment-pr-deny"

	grayColor        = "#AFAFAF"
	lightPurpleColor = "#af91e3"
	darkPurpleColor  = "#8256d0"
	lightRedColor    = "#d16460"
	darkRedColor     = "#c93c37"

	textBlockMaxLength = 3000
)

func (c *controller) handleDeploy(client *socketmode.Client, cmd slackgo.SlashCommand, args []string) {
	ctxLogger := log.WithField("slackUserId", cmd.UserID).
		WithField("slackChannelId", cmd.ChannelID)
	if len(args) < 3 {
		c.sendHelpResponse(client, cmd, ctxLogger)
		return
	}

	var (
		serviceName = args[0]
		environment = args[1]
		userCommit  = args[2]
	)

	ctxLogger = ctxLogger.WithField("serviceName", serviceName).
		WithField("environment", environment).
		WithField("userCommit", userCommit)

	ctx := context.Background()
	commit, commitUrl, err := c.deployer.GetCommitSha(ctx, serviceName, userCommit)
	if err != nil {
		c.sendErrorMessage(client, cmd, ctxLogger, err)
		return
	}

	ctxLogger = ctxLogger.WithField("commit", commit)
	err = c.sendRequestDetails(ctxLogger, serviceName, environment, commit, commitUrl, cmd, client)
	if err != nil {
		ctxLogger.WithError(err).
			Error("Failed to send message to user")
		return
	}

	user := "@" + cmd.UserName
	profile, err := client.GetUserProfile(&slackgo.GetUserProfileParameters{UserID: cmd.UserID})
	if err != nil {
		ctxLogger.WithError(err).Error("Failed to get slack user profile")
	}

	user = fmt.Sprintf("%s %s (%s)", profile.FirstName, profile.LastName, profile.Email)
	pr, diff, err := c.deployer.Deploy(serviceName, environment, commit, commitUrl, user)
	if err != nil {
		ctxLogger.WithError(err).Error("Failed to deploy")
		c.sendErrorMessage(client, cmd, ctxLogger, err)
		return
	}

	c.sendApprovalMessage(client, cmd, ctxLogger, pr, diff)
}

func (c *controller) sendHelpResponse(client *socketmode.Client, slashCommandEvent slackgo.SlashCommand, ctxLogger *log.Entry) {
	_, _, _, err := client.SendMessage(slashCommandEvent.ChannelID,
		slackgo.MsgOptionText("Deploy command must have this format: `/argo deploy [service] [env] [commit]`", false),
		slackgo.MsgOptionPostEphemeral(slashCommandEvent.UserID),
		slackgo.MsgOptionResponseURL(slashCommandEvent.ResponseURL, slackgo.ResponseTypeInChannel),
	)
	if err != nil {
		ctxLogger.WithField("slackUserId", slashCommandEvent.UserID).
			WithField("slackChannelId", slashCommandEvent.ChannelID).
			WithError(err).
			Error("Failed to send invalid command format message to user")
	}
}

func (c *controller) sendRequestDetails(ctxLogger *log.Entry, serviceName string, environment string, commit string, commitUrl string, cmd slackgo.SlashCommand, client *socketmode.Client) error {
	ctxLogger.Infof("Got request to deploy %s to %s with version %s from %s", serviceName, environment, commit, cmd.UserName)
	_, _, _, err := client.SendMessage(cmd.ChannelID, c.requestDetailsMsgOptions(cmd, serviceName, environment, commitUrl, commit, grayColor)...)
	return err
}

func (c *controller) requestDetailsMsgOptions(cmd slackgo.SlashCommand, serviceName, environment, commitUrl, commit, color string) []slackgo.MsgOption {
	return []slackgo.MsgOption{
		slackgo.MsgOptionText("Got new deployment request", false),
		slackgo.MsgOptionAttachments(
			slackgo.Attachment{
				Color: color,
				Blocks: slackgo.Blocks{
					BlockSet: []slackgo.Block{
						slackgo.NewSectionBlock(nil, []*slackgo.TextBlockObject{
							slackgo.NewTextBlockObject(slackgo.MarkdownType, fmt.Sprintf("*Service:*\n%s", serviceName), false, false),
							slackgo.NewTextBlockObject(slackgo.MarkdownType, fmt.Sprintf("*Environment:*\n%s", environment), false, false),
							slackgo.NewTextBlockObject(slackgo.MarkdownType, fmt.Sprintf("*Commit:*\n<%s|%s>", commitUrl, commit[:7]), false, false),
							slackgo.NewTextBlockObject(slackgo.MarkdownType, fmt.Sprintf("*Deployer:*\n<@%s>", cmd.UserID), false, false),
						}, nil),
					},
				},
			},
		),
		slackgo.MsgOptionResponseURL(cmd.ResponseURL, slackgo.ResponseTypeInChannel),
	}
}

func (c *controller) sendApprovalMessage(client *socketmode.Client, slashCommandEvent slackgo.SlashCommand, ctxLogger *log.Entry, pr *github.PullRequest, diff string) {
	prNumber := pr.GetNumber()
	approveBtn := slackgo.NewButtonBlockElement(deploymentApproveActionId, strconv.Itoa(prNumber), slackgo.NewTextBlockObject(slackgo.PlainTextType, "Approve", false, false))
	approveBtn.Style = slackgo.StylePrimary

	rejectBtn := slackgo.NewButtonBlockElement(deploymentDenyActionId, strconv.Itoa(prNumber), slackgo.NewTextBlockObject(slackgo.PlainTextType, "Deny", false, false))
	rejectBtn.Style = slackgo.StyleDanger

	diffText := fmt.Sprintf("```%s```", c.truncateDiff(diff, textBlockMaxLength-6))
	if diff == "" {
		diffText = "_Nothing to change, merging this PR will only create empty commit_"
	}

	_, _, _, err := client.SendMessage(slashCommandEvent.ChannelID,
		slackgo.MsgOptionText("Going to deploy the following change to the deployment repository", false),
		slackgo.MsgOptionAttachments(
			slackgo.Attachment{
				Color: grayColor,
				Blocks: slackgo.Blocks{
					BlockSet: []slackgo.Block{
						slackgo.NewSectionBlock(slackgo.NewTextBlockObject(slackgo.MarkdownType, diffText, false, false), nil, nil),
						slackgo.NewSectionBlock(slackgo.NewTextBlockObject(slackgo.MarkdownType, fmt.Sprintf("<%s|Original pull request>", pr.GetHTMLURL()), false, false), nil, nil),
						slackgo.NewActionBlock("",
							approveBtn,
							rejectBtn,
						),
					},
				},
			},
		),
		slackgo.MsgOptionResponseURL(slashCommandEvent.ResponseURL, slackgo.ResponseTypeInChannel),
	)

	if err != nil {
		ctxLogger.WithField("slackUserId", slashCommandEvent.UserID).
			WithField("slackChannelId", slashCommandEvent.ChannelID).
			WithError(err).
			Error("Failed to send message to user")
	}
}

func (c *controller) handleApproval(evt *socketmode.Event, client *socketmode.Client) {
	ctx := context.Background()
	client.Ack(*evt.Request)

	callback, _ := evt.Data.(slackgo.InteractionCallback)
	ctxLogger := log.WithField("slackUserId", callback.User.ID).
		WithField("slackChannelId", callback.Channel.ID)
	blockActions := callback.ActionCallback.BlockActions
	if len(blockActions) != 1 {
		ctxLogger.WithField("blockActions", blockActions).Error("Got unexpected amount of block actions")
		return
	}

	action := blockActions[0]
	actionId := action.ActionID
	pullRequestNumber, err := strconv.Atoi(action.Value)
	if err != nil {
		ctxLogger.WithError(err).Error("Failed to convert action value to pull request ID")
		return
	}

	prCtxLogger := ctxLogger.WithField("pullRequestId", pullRequestNumber)
	switch actionId {
	case deploymentApproveActionId:
		_, _, _, err = client.SendMessage(callback.Channel.ID,
			slackgo.MsgOptionReplaceOriginal(callback.ResponseURL),
			slackgo.MsgOptionAttachments(
				slackgo.Attachment{
					Color: lightPurpleColor,
					Blocks: slackgo.Blocks{
						BlockSet: []slackgo.Block{
							slackgo.NewSectionBlock(slackgo.NewTextBlockObject(slackgo.PlainTextType, "Merging deployment pull request...", false, false), nil, nil),
						},
					},
				},
			),
		)
		if err != nil {
			prCtxLogger.WithError(err).Error("Failed to send merging PR message to Slack")
		}

		err = c.deployer.Approve(ctx, pullRequestNumber)
		if err != nil {
			prCtxLogger.WithError(err).Error("Failed to merge deployment pull request")
			c.updateApprovalMessage(client, callback, prCtxLogger, darkRedColor, fmt.Sprintf("Failed to merge deployment pull request, error: %s", err.Error()))
			return
		}

		c.updateApprovalMessage(client, callback, prCtxLogger, darkPurpleColor, "Deployment pull request merged successfully")
		return
	case deploymentDenyActionId:
		_, _, _, err = client.SendMessage(callback.Channel.ID,
			slackgo.MsgOptionReplaceOriginal(callback.ResponseURL),
			slackgo.MsgOptionAttachments(
				slackgo.Attachment{
					Color: lightRedColor,
					Blocks: slackgo.Blocks{
						BlockSet: []slackgo.Block{
							slackgo.NewSectionBlock(slackgo.NewTextBlockObject(slackgo.PlainTextType, "Closing deployment pull request...", false, false), nil, nil),
						},
					},
				},
			),
		)

		err = c.deployer.Cancel(ctx, pullRequestNumber)
		if err != nil {
			prCtxLogger.WithError(err).Error("Failed to cancel deployment pull request")
			c.updateApprovalMessage(client, callback, prCtxLogger, darkRedColor, fmt.Sprintf("Failed to cancel deployment pull request, error: %s", err.Error()))
			return
		}

		c.updateApprovalMessage(client, callback, prCtxLogger, darkRedColor, "Closed deployment pull request")
		return
	default:
		prCtxLogger.WithField("actionId", actionId).Error("Unexpected action ID")
	}
}

func (c *controller) updateApprovalMessage(client *socketmode.Client, callback slackgo.InteractionCallback, prCtxLogger *log.Entry, color, text string) {
	_, _, _, err := client.SendMessage(callback.Channel.ID,
		slackgo.MsgOptionReplaceOriginal(callback.ResponseURL),
		slackgo.MsgOptionAttachments(
			slackgo.Attachment{
				Color: color,
				Blocks: slackgo.Blocks{
					BlockSet: []slackgo.Block{
						slackgo.NewSectionBlock(slackgo.NewTextBlockObject(slackgo.MarkdownType, text, false, false), nil, nil),
					},
				},
			},
		),
	)

	if err != nil {
		prCtxLogger.WithError(err).Error("Failed to update approval message")
	}
}

func (c *controller) sendErrorMessage(client *socketmode.Client, slashCommandEvent slackgo.SlashCommand, ctxLogger *log.Entry, executionErr error) {
	var errorMsg string
	if validationErr, ok := executionErr.(deploy.ValidationErr); ok {
		errorMsg = fmt.Sprintf("*Validation error:* %s", validationErr.Error())
	} else {
		errorMsg = fmt.Sprintf("Unexpected error during deployment pull request creation, error: %s", executionErr.Error())
	}

	_, _, _, err := client.SendMessage(slashCommandEvent.ChannelID,
		slackgo.MsgOptionAttachments(
			slackgo.Attachment{
				Color: darkRedColor,
				Blocks: slackgo.Blocks{
					BlockSet: []slackgo.Block{
						slackgo.NewSectionBlock(slackgo.NewTextBlockObject(slackgo.MarkdownType, errorMsg, false, false), nil, nil),
					},
				},
			},
		),
		slackgo.MsgOptionResponseURL(slashCommandEvent.ResponseURL, slackgo.ResponseTypeInChannel),
	)

	if err != nil {
		ctxLogger.WithField("slackUserId", slashCommandEvent.UserID).
			WithField("slackChannelId", slashCommandEvent.ChannelID).
			WithError(err).
			Error("Failed to send message to user")
	}
}

func (c *controller) truncateDiff(text string, width int) string {
	if len(text) <= width {
		return text
	}

	text = string([]byte(text)[:width-3])
	return fmt.Sprintf("%s...", text)
}
