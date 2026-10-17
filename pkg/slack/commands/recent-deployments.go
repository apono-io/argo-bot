package commands

import (
	"fmt"
	"strings"
	"time"

	"github.com/apono-io/argo-bot/pkg/deploy"
	"github.com/shomali11/slacker"
	log "github.com/sirupsen/logrus"
	slackgo "github.com/slack-go/slack"
)

type recentDeploymentGroup struct {
	deployerName  string
	deployerEmail string
	version       string
	deployedAt    time.Time
	serviceNames  []string
}

// recentDeploymentsWarning builds a block that mentions whoever recently deployed the requested services to the same
// environment, so the requester checks with them before approving. Returns nil when there is nothing to warn about.
func (c *controller) recentDeploymentsWarning(botCtx slacker.BotContext, ctxLogger *log.Entry, environment string, recentDeployments []deploy.RecentDeployment) slackgo.Block {
	if len(recentDeployments) == 0 {
		return nil
	}

	groups := groupRecentDeployments(recentDeployments)
	deployerMentions := map[string]string{}
	lines := make([]string, 0, len(groups))
	for _, group := range groups {
		mention, ok := deployerMentions[group.deployerEmail]
		if !ok {
			mention = c.deployerMention(botCtx, ctxLogger, group.deployerName, group.deployerEmail)
			deployerMentions[group.deployerEmail] = mention
		}

		lines = append(lines, fmt.Sprintf("• %s: `%s` (version `%s`, %s)",
			mention, strings.Join(group.serviceNames, "`, `"), group.version, timeAgo(group.deployedAt)))
	}

	text := fmt.Sprintf(":warning: *Recently deployed to %s by someone else.* Please check with them before approving:\n%s",
		environment, strings.Join(lines, "\n"))

	return slackgo.NewSectionBlock(slackgo.NewTextBlockObject(slackgo.MarkdownType, text, false, false), nil, nil)
}

// groupRecentDeployments groups services deployed together (same deployer and version), keeping the input order.
func groupRecentDeployments(recentDeployments []deploy.RecentDeployment) []*recentDeploymentGroup {
	var groups []*recentDeploymentGroup
	groupByKey := map[string]*recentDeploymentGroup{}
	for _, recentDeployment := range recentDeployments {
		key := strings.ToLower(recentDeployment.DeployerEmail) + "/" + recentDeployment.Version
		group, ok := groupByKey[key]
		if !ok {
			group = &recentDeploymentGroup{
				deployerName:  recentDeployment.DeployerName,
				deployerEmail: recentDeployment.DeployerEmail,
				version:       recentDeployment.Version,
				deployedAt:    recentDeployment.DeployedAt,
			}
			groupByKey[key] = group
			groups = append(groups, group)
		}

		group.serviceNames = append(group.serviceNames, recentDeployment.ServiceName)
		if recentDeployment.DeployedAt.After(group.deployedAt) {
			group.deployedAt = recentDeployment.DeployedAt
		}
	}

	return groups
}

// deployerMention mentions the deployer's Slack user so they get notified. Looking up a user by email needs the
// users:read.email scope; without it, fall back to the name and email from the deployment commit.
func (c *controller) deployerMention(botCtx slacker.BotContext, ctxLogger *log.Entry, deployerName, deployerEmail string) string {
	user, err := botCtx.SocketModeClient().GetUserByEmail(deployerEmail)
	if err != nil {
		ctxLogger.WithError(err).WithField("deployerEmail", deployerEmail).Warn("Failed to find previous deployer in Slack")
		return fmt.Sprintf("%s (%s)", deployerName, deployerEmail)
	}

	return fmt.Sprintf("<@%s>", user.ID)
}

func timeAgo(t time.Time) string {
	elapsed := time.Since(t)
	switch {
	case elapsed < time.Minute:
		return "just now"
	case elapsed < time.Hour:
		return fmt.Sprintf("%d min ago", int(elapsed.Minutes()))
	default:
		return fmt.Sprintf("%dh %dm ago", int(elapsed.Hours()), int(elapsed.Minutes())%60)
	}
}
