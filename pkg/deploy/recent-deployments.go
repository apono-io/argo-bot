package deploy

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

var deployCommitPattern = regexp.MustCompile(`^Deploy \S+ to \S+ with version (\S+) triggered by (.+) \(([^()\s]+@[^()\s]+)\)`)

const deployCommitsToScan = 10

type RecentDeployment struct {
	ServiceName   string
	DeployerName  string
	DeployerEmail string
	Version       string
	DeployedAt    time.Time
}

func (d *githubDeployer) ListRecentDeploymentsByOthers(ctx context.Context, serviceNames []string, environmentName, userEmail string) ([]RecentDeployment, error) {
	if d.config.RecentDeploymentWindow <= 0 {
		return nil, nil
	}

	serviceToEnvironment, deploymentBranch, err := d.resolveServicesAndEnvironment(serviceNames, environmentName)
	if err != nil {
		return nil, err
	}

	windowStart := time.Now().Add(-d.config.RecentDeploymentWindow)
	var recentDeployments []RecentDeployment
	for service, environment := range serviceToEnvironment {
		if len(environment.AllowedBranches) > 0 {
			continue
		}

		lastDeployment, err := d.getLastDeployment(ctx, deploymentBranch, environment.GeneratedPath)
		if err != nil {
			return nil, fmt.Errorf("failed to get last deployment of service %s, error: %w", service.Name, err)
		}

		if lastDeployment == nil || lastDeployment.DeployedAt.Before(windowStart) || strings.EqualFold(lastDeployment.DeployerEmail, userEmail) {
			continue
		}

		lastDeployment.ServiceName = service.Name
		recentDeployments = append(recentDeployments, *lastDeployment)
	}

	sort.Slice(recentDeployments, func(i, j int) bool {
		return recentDeployments[i].ServiceName < recentDeployments[j].ServiceName
	})

	return recentDeployments, nil
}

func (d *githubDeployer) getLastDeployment(ctx context.Context, deploymentBranch, generatedPath string) (*RecentDeployment, error) {
	commits, err := d.githubClient.ListCommitsForPath(ctx, deploymentBranch, generatedPath, deployCommitsToScan)
	if err != nil {
		return nil, err
	}

	for _, commit := range commits {
		match := deployCommitPattern.FindStringSubmatch(commit.GetCommit().GetMessage())
		if match == nil {
			continue
		}

		return &RecentDeployment{
			Version:       match[1],
			DeployerName:  match[2],
			DeployerEmail: match[3],
			DeployedAt:    commit.GetCommit().GetCommitter().GetDate(),
		}, nil
	}

	return nil, nil
}
