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

	slackerBot.Command("deploy <services> <environment> <commit>", &slacker.CommandDefinition{
		BlockID:     deploymentApprovalBlockId,
		Handler:     ctrl.handleDeploy,
		Interactive: ctrl.handleApproval,
	})

	slackerBot.Command("freeze <services> <environment>", &slacker.CommandDefinition{
		BlockID:     freezeApprovalBlockId,
		Handler:     ctrl.handleFreeze,
		Interactive: ctrl.handleFreezeApproval,
	})

	slackerBot.Command("unfreeze <services> <environment>", &slacker.CommandDefinition{
		BlockID:     freezeApprovalBlockId,
		Handler:     ctrl.handleUnfreeze,
		Interactive: ctrl.handleFreezeApproval,
	})
}

type controller struct {
	deployer deploy.Deployer
}
