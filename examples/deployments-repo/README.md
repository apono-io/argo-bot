# Example deployments repository

## Repository structure
In this example you can see the different ways you can configure argo-bot to work with your deployments repo:

### One template for multiple environments (`templates/accounts-service`)
   In this example we have multiple files each with different Kubernetes resource.
   The template is populated with the values and copied to the output path for the specific environment (prod: `auto-generated/prod/accounts-service`, staging: `auto-generated/staging/accounts-service`).
### Separate template for each environment (`templates/prod/users-service` and `templates/staging/users-service`)
   When your deployment setup is different for each environment you can create a separate template for each environment.
   Also, in this example the Kubernetes deployment and service definitions are combined in the same YAML file. 
   The template is populated with the values and copied to the output path for the specific environment (prod: `auto-generated/prod/users-service`, staging: `auto-generated/staging/users-service`).
### Non ArgoCD deployment using GitHub actions (`templates/fronend`)
   In this example we created a template with one text file `templates/fronend/version` that will be populated with the version commit id we want to deploy.
   We then add GitHub workflows for each environment that trigger on change to the output file and execute custom deployment logic.
   The template is populated with the values and copied to the output path for the specific environment (prod: `auto-generated/prod/frontend`, staging: `auto-generated/staging/frontend`).

In the case of the first two examples you will need to configure the applications in your ArgoCD setup to watch the output directory:
* `auto-generated/prod/accounts-service`
* `auto-generated/staging/accounts-service`
* `auto-generated/prod/users-service`
* `auto-generated/staging/users-service`
* etc...

## Configuration file
```yaml
deploy:
  github:
    organization: <deployments-repo-org>
    repository: <deployments-repo-name>
    author_name: <bot commit author name>
    author_email: <bot commit author email>
  services:
    - name: accounts-service
      githubOrganization: example
      githubRepository: my-mono-repo
      environments:
        - name: prod
          templatePath: "templates/accounts-service"
          generatedPath: "auto-generated/prod/accounts-service"
          allowedBranches:
            - "master"
        - name: staging
          templatePath: "templates/accounts-service"
          generatedPath: "auto-generated/staging/accounts-service"
    - name: users-service
      githubOrganization: example
      githubRepository: my-mono-repo
      environments:
         - name: prod
           templatePath: "templates/prod/users-service"
           generatedPath: "auto-generated/prod/users-service"
           allowedBranches:
              - "master"
         - name: staging
           templatePath: "templates/prod/users-service"
           generatedPath: "auto-generated/staging/users-service"
    - name: frontend
      githubOrganization: example
      githubRepository: frontend-repo
      environments:
         - name: prod
           templatePath: "templates/frontend"
           generatedPath: "auto-generated/prod/frontend"
           allowedBranches:
              - "master"
         - name: staging
           templatePath: "templates/frontend"
           generatedPath: "auto-generated/staging/frontend"
```