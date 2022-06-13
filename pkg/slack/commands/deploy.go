package commands

import (
	"context"
	"fmt"
	log "github.com/sirupsen/logrus"
	slackgo "github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"
	"math"
	"strconv"
)

var (
	deploymentApproveActionId = "deployment-pr-approve"
	deploymentDenyActionId    = "deployment-pr-deny"
)

func (c *controller) handleDeploy(client *socketmode.Client, cmd slackgo.SlashCommand, args []string) {
	ctx := context.Background()
	if len(args) < 3 {
		c.sendHelpResponse(client, cmd)
		return
	}

	var (
		serviceName = args[0]
		environment = args[1]
		userCommit  = args[2]
	)

	commit, commitUrl, err := c.deployer.GetCommitSha(ctx, serviceName, userCommit)
	if err != nil {
		return
	}

	err = c.sendRequestDetails(serviceName, environment, commit, commitUrl, cmd, client)

	pr, diff, err := c.deployer.Deploy(serviceName, environment, commit)
	if err != nil {
		log.WithError(err).Error("Failed to deploy")
		return
	}

	c.sendApprovalMessage(client, cmd, pr.GetNumber(), diff)
}

func (c *controller) sendHelpResponse(client *socketmode.Client, slashCommandEvent slackgo.SlashCommand) {
	_, _, _, err := client.SendMessage(slashCommandEvent.ChannelID,
		slackgo.MsgOptionText("Deploy command must have this format: `/argo deploy [service] [env] [commit]`", false),
		slackgo.MsgOptionPostEphemeral(slashCommandEvent.UserID),
		slackgo.MsgOptionResponseURL(slashCommandEvent.ResponseURL, slackgo.ResponseTypeInChannel),
	)
	if err != nil {
		log.WithField("slackUserId", slashCommandEvent.UserID).
			WithField("slackChannelId", slashCommandEvent.ChannelID).
			WithError(err).
			Error("Failed to send invalid command format message to user")
	}
}

func (c *controller) sendRequestDetails(serviceName string, environment string, commit string, commitUrl string,
	cmd slackgo.SlashCommand, client *socketmode.Client) error {
	log.Infof("Got request to deploy %s to %s with version %s from %s", serviceName, environment, commit, cmd.UserName)
	_, _, _, err := client.SendMessage(cmd.ChannelID, c.requestDetailsMsgOptions(cmd, serviceName, environment, commitUrl, commit, "#AFAFAF")...)
	if err != nil {
		log.WithField("slackUserId", cmd.UserID).
			WithField("slackChannelId", cmd.ChannelID).
			WithError(err).
			Error("Failed to send message to user")
	}
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
							slackgo.NewTextBlockObject(slackgo.MarkdownType, fmt.Sprintf("*Commit:*\n<%s|%s>", commitUrl, commit[:int(math.Min(float64(len(commit)), 7))]), false, false),
							slackgo.NewTextBlockObject(slackgo.MarkdownType, fmt.Sprintf("*Deployer:*\n<@%s>", cmd.UserID), false, false),
						}, nil),
					},
				},
			},
		),
		slackgo.MsgOptionResponseURL(cmd.ResponseURL, slackgo.ResponseTypeInChannel),
	}
}

func (c *controller) sendApprovalMessage(client *socketmode.Client, slashCommandEvent slackgo.SlashCommand, prNumber int, diff string) {
	approveBtn := slackgo.NewButtonBlockElement(deploymentApproveActionId, strconv.Itoa(prNumber), slackgo.NewTextBlockObject(slackgo.PlainTextType, "Approve", false, false))
	approveBtn.Style = slackgo.StylePrimary

	rejectBtn := slackgo.NewButtonBlockElement(deploymentDenyActionId, strconv.Itoa(prNumber), slackgo.NewTextBlockObject(slackgo.PlainTextType, "Deny", false, false))
	rejectBtn.Style = slackgo.StyleDanger

	_, _, _, err := client.SendMessage(slashCommandEvent.ChannelID,
		slackgo.MsgOptionText("Going to deploy the following change to the deployment repository", false),
		slackgo.MsgOptionAttachments(
			slackgo.Attachment{
				Color: "#AFAFAF",
				Blocks: slackgo.Blocks{
					BlockSet: []slackgo.Block{
						slackgo.NewSectionBlock(slackgo.NewTextBlockObject(slackgo.MarkdownType, fmt.Sprintf("```%s```", diff), false, false), nil, nil),
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
		log.WithField("slackUserId", slashCommandEvent.UserID).
			WithField("slackChannelId", slashCommandEvent.ChannelID).
			WithError(err).
			Error("Failed to send message to user")
	}
}

func (c *controller) handleApproval(evt *socketmode.Event, client *socketmode.Client) {
	ctx := context.Background()
	client.Ack(*evt.Request)

	callback, _ := evt.Data.(slackgo.InteractionCallback)
	blockActions := callback.ActionCallback.BlockActions
	if len(blockActions) != 1 {
		log.WithField("blockActions", blockActions).Error("Got unexpected amount of block actions")
		return
	}

	action := blockActions[0]
	actionId := action.ActionID
	pullRequestNumber, err := strconv.Atoi(action.Value)
	if err != nil {
		log.WithError(err).Error("Failed to convert action value to pull request ID")
		return
	}

	prCtxLogger := log.WithField("pullRequestId", pullRequestNumber)
	switch actionId {
	case deploymentApproveActionId:
		_, _, _, err = client.SendMessage(callback.Channel.ID,
			slackgo.MsgOptionReplaceOriginal(callback.ResponseURL),
			slackgo.MsgOptionAttachments(
				slackgo.Attachment{
					Color: "#af91e3",
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
			prCtxLogger.WithError(err).Error("Failed to approve and merge deployment pull request")
			return
		}

		_, _, _, err = client.SendMessage(callback.Channel.ID,
			slackgo.MsgOptionReplaceOriginal(callback.ResponseURL),
			slackgo.MsgOptionAttachments(
				slackgo.Attachment{
					Color: "#8256d0",
					Blocks: slackgo.Blocks{
						BlockSet: []slackgo.Block{
							slackgo.NewSectionBlock(slackgo.NewTextBlockObject(slackgo.PlainTextType, "Deployment pull request merged successfully", false, false), nil, nil),
						},
					},
				},
			),
		)
		if err != nil {
			prCtxLogger.WithError(err).Error("Failed to send merged PR message to Slack")
		}
		return
	case deploymentDenyActionId:
		_, _, _, err = client.SendMessage(callback.Channel.ID,
			slackgo.MsgOptionReplaceOriginal(callback.ResponseURL),
			slackgo.MsgOptionAttachments(
				slackgo.Attachment{
					Color: "#d16460",
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
			return
		}

		_, _, _, err = client.SendMessage(callback.Channel.ID,
			slackgo.MsgOptionReplaceOriginal(callback.ResponseURL),
			slackgo.MsgOptionAttachments(
				slackgo.Attachment{
					Color: "#c93c37",
					Blocks: slackgo.Blocks{
						BlockSet: []slackgo.Block{
							slackgo.NewSectionBlock(slackgo.NewTextBlockObject(slackgo.PlainTextType, "Closed deployment pull request", false, false), nil, nil),
						},
					},
				},
			),
		)
		if err != nil {
			prCtxLogger.WithError(err).Error("Failed to send merged PR message to Slack")
		}
		return
	default:
		prCtxLogger.WithField("actionId", actionId).Error("Unexpected action ID")
	}
}
