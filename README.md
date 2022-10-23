# Argo Bot - Slack bot for Argo CD

## Installing

### Registering as a GitHub App
The first step towards installing `argo-bot` is to register `argo-bot` as a GitHub application using [this link](https://github.com/settings/apps/new), and according to the following instructions:
* **GitHub App Name:** Choose a meaningful value.
* **Homepage URL:** Choose a meaningful value.
* **Logo (optional):** Choose a logo for your app.
* **Webhook:** Deselect `Active` checkbox.
* **Permissions:** Choose the following sets of permissions:
    * **Repository permissions:**
        * _Contents_: _Read & write_
        * _Metadata_: _Read only_
        * _Pull requests_: _Read & write_
* **Where can this GitHub App be installed?** Choose "_Any account_".

Upon successful registration, you'll be taken to the GitHub application's administration page.
Take note of the value of the "_App ID_" field, as it will be needed later on.
Then, scroll down to the bottom of the page and click "_Generate a private key_".
This will generate and download the GitHub application's private key, which will be used to authenticate the application with GitHub.
Take note of the path to where the private key is downloaded.
Finally, click on the "_Install App_" tab, choose the target GitHub organization and click "_Install_" (possibly choosing only a subset of the GitHub organization's repositories).

Upon successful installation, you'll be taken to a page having a URL of following form:

```
https://github.com/organizations/<org>/settings/installations/<installation-id>
```

Take note of the value of `<installation-id>`, as it will be needed later on.

### Registering as a Slack App
After creating the GitHub app you need to register `argo-bot` as a Slack application using [this link](https://api.slack.com/apps?new_app=1), and according to the following instructions:
* Select `From an app manifest` option.
* Select the workspace where you want to install your app.
* Select the YAML tab and replace the content with the content of `docs/assets/slack-app-manifest.yaml` (make sure to replace `<bot-name>` and `<bot-username>`).
* Click `Next` and then `Create`.
* Go to `Basic Information` page and create an app-level token by clicking the `Generate Token and Scopes` according to the following instructions (take note of the generated token after the creation):
  * **Token Name:** _app_
  * **Scopes to be accessed by this token:** _connections:write_
* Install the app using the `Install to Workspace` button.
* Go to `OAuth & Permissions` page and take note of the value of `Bot User OAuth Token`.

### Configuring

On startup, `argo-bot` reads its configuration from the `/etc/argo-bot/config.yaml`.
This configuration file contains the list of deployable services and their environments, the configuration for the deployments repository and other useful options.

```yaml
deploy:
  github:
    organization: <deployments-repo-org>
    repository: <deployments-repo-name>
    author_name: <bot commit author name>
    author_email: <bot commit author email>
  services:
    - name: <service-name>
      githubOrganization: <service-github-organization>
      githubRepository: <service-github-repository>
      environments:
        - name: <environment-name>
          templatePath: "<templates-folder-path>"
          generatedPath: "<generated-files-folder-path>"
          allowedBranches: # Restriction for deployment branches (Example: only master deployment allowed on prod)
            - "master"
        - name: <environment-name>
          templatePath: "<templates-folder-path>"
          generatedPath: "<generated-files-folder-path>"
``` 

You can see a full example for the deployments repository [here](https://github.com/apono-io/argo-bot/tree/master/examples/deployments-repo)

### Running
```shell
# Create secret with GitHub App private key
kubectl create secret generic argo-bot-github-app-private-key --from-file=github-app-private-key.pem=<path-to-private-key>

# Optional: Configure sending logs to Logz.io (you might need to change the listener url)
kubectl create secret generic argo-bot-logging-secret --from-literal=LOGGING_LOGZIO_LISTENER_ADDRESS=https://listener.logz.io:8071 --from-literal=LOGGING_LOGZIO_LOGGING_TOKEN=<logging-token>

# Create argo-bot config
kubectl create configmap argo-bot-config --from-file=config.yaml=<path-to-config-file>

# Installing argo-bot helm chart
# If created logging secret you need to add: --set additionalEnvironmentVariableSecretName=argo-bot-logging-secret
helm install argo-bot ./helm/charts/argo-bot \
  --set github.appId=<github-app-id> \
  --set github.installationId=<github-app-installation-id> \
  --set github.privateKeySecretName=argo-bot-github-app-private-key \
  --set slack.appToken=<slack-app-level-token> \
  --set slack.botToken=<slack-bot-token> \
  --set configMapName=argo-bot-config \
  --wait
```
