package commands

import (
	"github.com/apono-io/argo-bot/pkg/deploy"
	"github.com/shomali11/slacker"
)

func RegisterCommandHandlers(slackerBot *slacker.Slacker, deployer deploy.Deployer) {
	ctrl := controller{
		deployer: deployer,
	}

	slackerBot.Command("version", &slacker.CommandDefinition{
		Handler: ctrl.handleVersion,
	})

	slackerBot.Command("deploy <service> <environment> <commit>", &slacker.CommandDefinition{
		BlockID:     deploymentApprovalBlockId,
		Handler:     ctrl.handleDeploy,
		Interactive: ctrl.handleApproval,
	})
}

type controller struct {
	deployer deploy.Deployer
}
